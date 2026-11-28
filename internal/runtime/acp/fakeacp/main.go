// Command fakeacp is a scripted coding agent that speaks the Agent Client
// Protocol on stdio, for tests of the ACP adapter. Each prompt line is a
// command:
//
//	say <text>          agent message (streamed in chunks); \n in text is a newline
//	stream <text>       agent message written slowly: a few characters every
//	                    50 ms, as a reply grows (\n as in say)
//	write <path> <text> edit tool call, asks permission, then writes the file
//	shell <command>     execute tool call, asks permission, then runs it
//	read <path>         read tool call without a permission request
//	sleep <duration>    pause (session/cancel ends the turn as cancelled)
//	history             agent message "history: <n> turns"
//	crash               exit in the middle of the turn
//	fail <text>         answer the prompt with a JSON-RPC error
//	fail-code <n> <t>   same, with error code n
//	settings            agent message "settings: <model> <effort>" (what
//	                    the session runs with)
//	images              agent message "images: <media types>" (the image
//	                    blocks of the prompt)
//	ask <title>|<a,b>   elicitation/create (a form: a choice of a or b, and
//	                    a note), then agent message "answer: <choice>
//	                    <note>" or "declined: <action>"; "no elicitation"
//	                    when the client does not take forms
//
// Sessions are kept under $FAKE_ACP_STATE (default $TMPDIR/fakeacp) so a
// new process can load them. Environment:
//
//	FAKE_ACP_RESUME=load|resume|none|load-fallback  how sessions resume (default load)
//	FAKE_ACP_AUTH=<id>                require `authenticate` with this method
//	FAKE_ACP_MODELS=a,b               models session/set_model accepts
//	FAKE_ACP_CONFIG=1                 offer the models as a "model" config
//	                                  option (session/set_config_option)
//	                                  and drop session/set_model
//	FAKE_ACP_ALLOW_ALWAYS_ONLY=1      offer only allow_always/reject_once
//	FAKE_ACP_IMAGES=1                 take image prompt blocks
//	FAKE_ACP_OPTIONS=1                advertise session/options (never creates a session)
//	FAKE_ACP_OPTIONS_ERROR=<code>     fail session/options; "wait" waits for cancellation
//	FAKE_ACP_CONFIG_FILE=<path>       configOptions JSON used by new/options
//	FAKE_ACP_EFFORTS=a=low,high;b=low a "thought_level" config option, its
//	                                  choices those of the selected model
//	                                  (with FAKE_ACP_CONFIG=1)
//	FAKE_ACP_LEGACY=1                 offer the models as the older "models"
//	                                  session state (session/set_model)
//	FAKE_ACP_CLOSE=1                  advertise and take session/close
//	FAKE_ACP_LOG=<path>               append method names only, for no-prompt assertions
package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type msg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

var (
	outMu     sync.Mutex
	out       = bufio.NewWriter(os.Stdout)
	stateDir  = os.Getenv("FAKE_ACP_STATE")
	resume    = os.Getenv("FAKE_ACP_RESUME")
	authID    = os.Getenv("FAKE_ACP_AUTH")
	authed    = false
	mode      string
	nextID    = 1000
	waiting   = map[string]chan msg{}
	waitMu    sync.Mutex
	cancelled = make(chan struct{}, 1)
	cwd       string
	// The session's selected model and thought level.
	curModel, curEffort string

	turnMu       sync.Mutex
	turnRunning  bool
	spokeInTurn  bool
	activeReqID  json.RawMessage
	steeredItems []steerInput
)

type steerInput struct {
	text   string
	images []string
}

func send(v any) {
	b, _ := json.Marshal(v)
	outMu.Lock()
	out.Write(append(b, '\n'))
	out.Flush()
	outMu.Unlock()
}

func reply(id json.RawMessage, result any) {
	send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func replyErr(id json.RawMessage, code int, text string) {
	send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": text}})
}

func update(sid string, u map[string]any) {
	send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": u}})
}

// ask sends a request to the client and waits for its response.
func ask(method string, params any) msg {
	waitMu.Lock()
	nextID++
	n := nextID
	ch := make(chan msg, 1)
	waiting[fmt.Sprint(n)] = ch
	waitMu.Unlock()
	send(map[string]any{"jsonrpc": "2.0", "id": n, "method": method, "params": params})
	return <-ch
}

type history struct {
	Turns []string `json:"turns"`
}

// load reads a session's history. Another process of the session may be
// writing it, and Windows lets a reader see it half written: a moment
// later it's whole.
func load(sid string) (h *history, err error) {
	for range 20 {
		var b []byte
		if b, err = os.ReadFile(filepath.Join(stateDir, sid+".json")); err == nil {
			h = &history{}
			if err = json.Unmarshal(b, h); err == nil {
				return h, nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, err
}

func save(sid string, h *history) {
	b, _ := json.Marshal(h)
	os.WriteFile(filepath.Join(stateDir, sid+".json"), b, 0o600)
}

func fakeSetting(envKey, fileName string) string {
	if exe, err := os.Executable(); err == nil {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), fileName)); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func main() {
	authID = fakeSetting("FAKE_ACP_AUTH", "fakeacp.auth")
	if path := os.Getenv("FAKE_ACP_ARGS_LOG"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			b, _ := json.Marshal(os.Args[1:])
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		if fakeSetting("FAKE_ACP_VERSION_FAIL", "fakeacp.version_fail") == "1" {
			fmt.Fprintln(os.Stderr, "version failed")
			os.Exit(1)
		}
		fmt.Println(cmp.Or(fakeSetting("FAKE_ACP_VERSION", "fakeacp.version"), "fakeacp 1.2.3"))
		return
	}
	agyMode := fakeSetting("FAKE_ACP_AGY_MODE", "fakeacp.mode")
	if len(os.Args) > 2 && os.Args[1] == "acp" && os.Args[2] == "--help" {
		switch agyMode {
		case "help-hangs":
			time.Sleep(time.Minute)
			return
		case "legacy-error":
			fmt.Fprintln(os.Stderr, "unknown command: acp")
			os.Exit(1)
		case "ide-launcher":
			fmt.Println("Usage: agy [paths...]\nOpen files in Antigravity.")
			return
		default:
			fmt.Fprintln(os.Stderr, "Usage: agy acp [options]\nStart the ACP stdio server.")
			return
		}
	}
	if len(os.Args) > 2 && os.Args[1] == "-p" && os.Args[2] == "/usage" {
		if usage := fakeSetting("FAKE_ACP_USAGE", "fakeacp.usage"); usage != "" {
			fmt.Println(usage)
			return
		}
		os.Exit(2)
	}
	if agyMode == "acp" && !slices.Equal(os.Args[1:], []string{"acp"}) {
		fmt.Fprintf(os.Stderr, "expected args [acp], got %v\n", os.Args[1:])
		os.Exit(2)
	}
	if stateDir == "" {
		stateDir = filepath.Join(os.TempDir(), "fakeacp")
	}
	os.MkdirAll(stateDir, 0o700)
	if resume == "" {
		resume = "load"
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 32<<20)
	for in.Scan() {
		var m msg
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "" { // response to one of our requests
			waitMu.Lock()
			ch := waiting[string(m.ID)]
			delete(waiting, string(m.ID))
			waitMu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		handle(m)
	}
}

func handle(m msg) {
	if path := os.Getenv("FAKE_ACP_LOG"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintln(f, m.Method)
			f.Close()
		}
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
		MethodID  string `json:"methodId"`
		ModelID   string `json:"modelId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
		Prompt    []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		} `json:"prompt"`
	}
	json.Unmarshal(m.Params, &p)
	needAuth := authID != "" && !authed
	switch m.Method {
	case "initialize":
		var ip struct {
			ClientCapabilities struct {
				Elicitation struct {
					Form *struct{} `json:"form"`
				} `json:"elicitation"`
			} `json:"clientCapabilities"`
		}
		json.Unmarshal(m.Params, &ip)
		canElicit = ip.ClientCapabilities.Elicitation.Form != nil
		caps := map[string]any{"loadSession": resume == "load" || resume == "load-fallback"}
		sessionCaps := map[string]any{}
		if resume == "resume" || resume == "load-fallback" {
			sessionCaps["resume"] = map[string]any{}
		}
		if os.Getenv("FAKE_ACP_OPTIONS") == "1" {
			sessionCaps["options"] = map[string]any{}
		}
		if fakeSetting("FAKE_ACP_CLOSE", "fakeacp.close") == "1" {
			sessionCaps["close"] = map[string]any{}
		}
		caps["sessionCapabilities"] = sessionCaps
		caps["promptCapabilities"] = map[string]any{"image": os.Getenv("FAKE_ACP_IMAGES") == "1"}
		var auth []any
		if authID != "" {
			auth = []any{map[string]any{"id": "other", "name": "Other"}, map[string]any{"id": authID, "name": "Login"}}
		}
		initReply := map[string]any{"protocolVersion": 1, "agentCapabilities": caps, "authMethods": auth}
		if os.Getenv("FAKE_ACP_STEERING_EXT") == "1" {
			initReply["_meta"] = map[string]any{"steering": map[string]any{"supported": true}}
		}
		reply(m.ID, initReply)
	case "authenticate":
		if p.MethodID != authID {
			replyErr(m.ID, -32000, "unknown auth method "+p.MethodID)
			return
		}
		if os.Getenv("FAKE_ACP_AUTH_WAIT") == "1" {
			fmt.Fprintln(os.Stderr, "Open https://example.test/login to sign in")
			time.Sleep(30 * time.Second)
		}
		authed = true
		reply(m.ID, map[string]any{})
	case "session/set_mode":
		var params struct {
			ModeID string `json:"modeId"`
		}
		json.Unmarshal(m.Params, &params)
		mode = params.ModeID
		reply(m.ID, map[string]any{})
	case "session/options":
		if os.Getenv("FAKE_ACP_OPTIONS") != "1" {
			replyErr(m.ID, -32601, "not advertised")
			return
		}
		if code := os.Getenv("FAKE_ACP_OPTIONS_ERROR"); code != "" {
			if code == "wait" {
				time.Sleep(30 * time.Second)
				return
			}
			n, _ := strconv.Atoi(code)
			replyErr(m.ID, n, "sensitive server error must not be exposed")
			return
		}
		curModel = ""
		reply(m.ID, map[string]any{"configOptions": modelConfig()})
	case "session/close":
		if fakeSetting("FAKE_ACP_CLOSE", "fakeacp.close") != "1" {
			replyErr(m.ID, -32601, "not advertised")
			return
		}
		reply(m.ID, map[string]any{})
	case "session/new":
		if cntFile := os.Getenv("FAKE_ACP_NEW_CALLS_FILE"); cntFile != "" {
			prev, _ := os.ReadFile(cntFile)
			seq := len(prev) + 1
			_ = os.WriteFile(cntFile, append(prev, '1'), 0o600)
			if crashN, _ := strconv.Atoi(os.Getenv("FAKE_ACP_NEW_CRASH_FIRST")); crashN > 0 && seq <= crashN {
				fmt.Fprintln(os.Stderr, "language_server bootstrap crashed")
				os.Exit(2)
			}
			if failN, _ := strconv.Atoi(os.Getenv("FAKE_ACP_NEW_FAIL_FIRST")); failN > 0 && seq <= failN {
				replyErr(m.ID, -32603, "Failed to start Antigravity conversation")
				return
			}
		}
		if code := fakeSetting("FAKE_ACP_NEW_ERROR", "fakeacp.new_error"); code != "" {
			n, _ := strconv.Atoi(code)
			replyErr(m.ID, n, "session/new failed")
			return
		}
		if needAuth {
			replyErr(m.ID, -32000, "Authentication required")
			return
		}
		cwd = p.Cwd
		sid := fmt.Sprintf("fake-%d", time.Now().UnixNano())
		save(sid, &history{})
		res := map[string]any{"sessionId": sid}
		curModel, curEffort = "", ""
		if fakeSetting("FAKE_ACP_CONFIG", "fakeacp.config") == "1" {
			res["configOptions"] = modelConfig()
		}
		if os.Getenv("FAKE_ACP_LEGACY") == "1" {
			var available []any
			for _, x := range strings.Split(fakeSetting("FAKE_ACP_MODELS", "fakeacp.models"), ",") {
				if x != "" {
					available = append(available, map[string]any{"modelId": x, "name": strings.ToUpper(x)})
					curModel = cmp.Or(curModel, x)
				}
			}
			res["models"] = map[string]any{"availableModels": available, "currentModelId": curModel}
		}
		reply(m.ID, res)
	case "session/load", "session/resume":
		if needAuth {
			replyErr(m.ID, -32000, "Authentication required")
			return
		}
		if m.Method == "session/resume" {
			if cntFile := os.Getenv("FAKE_ACP_RESUME_CALLS_FILE"); cntFile != "" {
				prev, _ := os.ReadFile(cntFile)
				seq := len(prev) + 1
				_ = os.WriteFile(cntFile, append(prev, '1'), 0o600)
				if failN, _ := strconv.Atoi(os.Getenv("FAKE_ACP_RESUME_FAIL_FIRST")); failN > 0 && seq <= failN {
					replyErr(m.ID, -32603, "Failed to read session "+p.SessionID+"; retry, or load it with session/load")
					return
				}
			}
		}
		if m.Method == "session/resume" && resume == "load-fallback" {
			replyErr(m.ID, -32603, "Failed to read session "+p.SessionID+"; retry, or load it with session/load")
			return
		}
		h, err := load(p.SessionID)
		if err != nil {
			replyErr(m.ID, -32002, "Resource not found: session "+p.SessionID)
			return
		}
		cwd = p.Cwd
		if m.Method == "session/load" { // replay the conversation
			for _, t := range h.Turns {
				update(p.SessionID, map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": t}})
				update(p.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "replayed " + t}})
				update(p.SessionID, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "old", "title": "old", "kind": "edit", "status": "completed"})
			}
		}
		res := map[string]any{}
		if os.Getenv("FAKE_ACP_CONFIG") == "1" {
			res["configOptions"] = modelConfig()
		}
		reply(m.ID, res)
	case "session/set_config_option":
		if p.ConfigID == "effort" && os.Getenv("FAKE_ACP_CONFIG") == "1" && slices.Contains(efforts(curModel), p.Value) {
			curEffort = p.Value
			reply(m.ID, map[string]any{"configOptions": modelConfig()})
			return
		}
		if p.ConfigID == "llm" && os.Getenv("FAKE_ACP_CONFIG") == "1" {
			curModel = p.Value
			reply(m.ID, map[string]any{"configOptions": modelConfig()})
			if options := os.Getenv("FAKE_ACP_EAGER_UPDATE"); options != "" {
				update(p.SessionID, map[string]any{"sessionUpdate": "config_option_update", "configOptions": json.RawMessage(options)})
			}
			return
		}
		replyErr(m.ID, -32602, "unknown config option "+p.ConfigID)
	case "session/set_model":
		if os.Getenv("FAKE_ACP_CONFIG") == "1" {
			replyErr(m.ID, -32601, "Method not found")
			return
		}
		for _, x := range strings.Split(os.Getenv("FAKE_ACP_MODELS"), ",") {
			if x != "" && x == p.ModelID {
				curModel = x
				reply(m.ID, map[string]any{})
				return
			}
		}
		replyErr(m.ID, -32602, "unknown model "+p.ModelID)
	case "_session/steering":
		if os.Getenv("FAKE_ACP_STEERING_EXT") != "1" {
			replyErr(m.ID, -32601, "Method not found: _session/steering")
			return
		}
		var images []string
		var text string
		for i, b := range p.Prompt {
			if i == 0 {
				text = b.Text
			}
			if b.Type == "image" && b.Data != "" {
				images = append(images, b.MimeType)
			}
		}
		turnMu.Lock()
		if !turnRunning {
			turnMu.Unlock()
			reply(m.ID, map[string]any{"outcome": "noActiveTurn"})
			return
		}
		steeredItems = append(steeredItems, steerInput{text: text, images: images})
		turnMu.Unlock()
		reply(m.ID, map[string]any{"outcome": "injected", "messageId": fmt.Sprintf("steer-%d", time.Now().UnixNano())})
	case "session/prompt":
		if os.Getenv("FAKE_ACP_REQUIRE_MODE") == "1" && mode != "default" {
			replyErr(m.ID, -32602, "permission mode was not reset")
			return
		}
		var images []string
		var text string
		for i, b := range p.Prompt {
			if i == 0 {
				text = b.Text
			}
			if b.Type == "image" && b.Data != "" {
				images = append(images, b.MimeType)
			}
		}
		turnMu.Lock()
		if turnRunning {
			if os.Getenv("FAKE_ACP_REJECT_STEER") == "1" {
				turnMu.Unlock()
				replyErr(m.ID, -32603, "Session is already processing a prompt")
				return
			}
			prevID := activeReqID
			activeReqID = m.ID
			steeredItems = append(steeredItems, steerInput{text: text, images: images})
			turnMu.Unlock()
			reply(prevID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 50, "outputTokens": 10, "cachedReadTokens": 5}})
			return
		}
		turnRunning = true
		spokeInTurn = false
		activeReqID = m.ID
		steeredItems = nil
		turnMu.Unlock()
		select { // drop a stale cancel
		case <-cancelled:
		default:
		}
		go prompt(p.SessionID, text, images)
	case "session/cancel":
		select {
		case cancelled <- struct{}{}:
		default:
		}
	default:
		if m.ID != nil {
			replyErr(m.ID, -32601, "method not found")
		}
	}
}

var callN int

// canElicit: the client takes form elicitations.
var canElicit bool

func isFakeDirective(line string) bool {
	cmd, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch cmd {
	case "settings", "model-options", "say", "stream", "images", "ask", "ask3", "history", "write", "read", "shell", "focus", "steps", "run-summary", "think", "sleep", "crash", "fail", "fail-code":
		return true
	}
	return false
}

func finishActivePrompt(result any, errCode int, errText string) {
	turnMu.Lock()
	rid := activeReqID
	turnRunning = false
	activeReqID = nil
	steeredItems = nil
	turnMu.Unlock()
	if rid == nil {
		return
	}
	if errText != "" || errCode != 0 {
		replyErr(rid, errCode, errText)
		return
	}
	reply(rid, result)
}

func prompt(sid, text string, images []string) {
	h, err := load(sid)
	if err != nil {
		finishActivePrompt(nil, -32002, "unknown session")
		return
	}
	h.Turns = append(h.Turns, text)
	save(sid, h)
	if _, after, ok := strings.Cut(text, "</context_handoff>\n\n"); ok {
		text = after
	}
	if runPromptLines(sid, text, images) {
		return
	}
	for {
		turnMu.Lock()
		batch := steeredItems
		steeredItems = nil
		turnMu.Unlock()
		if len(batch) == 0 {
			break
		}
		for _, st := range batch {
			if hh, lErr := load(sid); lErr == nil {
				hh.Turns = append(hh.Turns, st.text)
				save(sid, hh)
			}
			if spokeInTurn {
				update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "\n"}})
				spokeInTurn = false
			}
			stText := strings.TrimSpace(st.text)
			if !strings.Contains(stText, "\n") && !isFakeDirective(stText) {
				stText = "say Steered: " + stText
			}
			if runPromptLines(sid, stText, st.images) {
				return
			}
		}
	}
	finishActivePrompt(map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 100, "outputTokens": 20, "cachedReadTokens": 5}}, 0, "")
}

func runPromptLines(sid, text string, images []string) bool {
	for _, line := range strings.Split(text, "\n") {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch cmd {
		case "settings":
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "settings: " + curModel + " " + curEffort}})
		case "model-options":
			update(sid, map[string]any{"sessionUpdate": "config_option_update", "configOptions": json.RawMessage(arg)})
		case "say":
			spokeInTurn = true
			r := []rune(strings.ReplaceAll(arg, `\n`, "\n")) // split between characters, as agents stream
			half := len(r) / 2
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(r[:half])}})
			update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}})
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(r[half:])}})
		case "stream":
			spokeInTurn = true
			r := []rune(strings.ReplaceAll(arg, `\n`, "\n"))
			for i := 0; i < len(r); i += 4 {
				update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(r[i:min(i+4, len(r))])}})
				select {
				case <-time.After(50 * time.Millisecond):
				case <-cancelled:
					finishActivePrompt(map[string]any{"stopReason": "cancelled"}, 0, "")
					return true
				}
			}
		case "images":
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "images: " + strings.Join(images, ",")}})
		case "ask":
			title, opts, _ := strings.Cut(arg, "|")
			if !canElicit {
				update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "no elicitation"}})
				break
			}
			var oneOf []any
			for _, o := range strings.Split(opts, ",") {
				oneOf = append(oneOf, map[string]any{"const": "v-" + strings.ToLower(o), "title": o})
			}
			r := ask("elicitation/create", map[string]any{"sessionId": sid, "mode": "form", "message": title, "requestedSchema": map[string]any{
				"type": "object", "required": []string{"choice"}, "properties": map[string]any{
					"choice": map[string]any{"type": "string", "title": "Choice", "oneOf": oneOf},
					"note":   map[string]any{"type": "string", "title": "Note", "description": "Anything else"}}}})
			var res struct {
				Action  string         `json:"action"`
				Content map[string]any `json:"content"`
			}
			json.Unmarshal(r.Result, &res)
			text := "declined: " + res.Action
			if res.Action == "accept" {
				text = fmt.Sprintf("answer: %v %v", res.Content["choice"], res.Content["note"])
			}
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
		case "ask3":
			if !canElicit {
				update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "no elicitation"}})
				break
			}
			r := ask("elicitation/create", map[string]any{
				"sessionId": sid,
				"mode":      "form",
				"message":   "The agent has 3 questions.",
				"requestedSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"q0": map[string]any{
							"type":  "string",
							"title": "推进节奏选哪种？",
							"enum":  []string{"(Recommended) 一步到位", "分两阶段"},
						},
						"q0_other": map[string]any{
							"type":        "string",
							"description": "Optional free-form answer to: 推进节奏选哪种？",
						},
						"q1": map[string]any{
							"type":  "string",
							"title": "输出如何处理？",
							"enum":  []string{"(Recommended) 写入会话流", "仅临时抽屉", "自动喂给模型"},
						},
						"q1_other": map[string]any{
							"type":        "string",
							"description": "Optional free-form answer to: 输出如何处理？",
						},
						"q2": map[string]any{
							"type":  "string",
							"title": "终端程序怎么选？",
							"oneOf": []any{
								map[string]any{"const": "auto", "title": "(Recommended) 自动探测运行中终端"},
								map[string]any{"const": "pref", "title": "设置页下拉项"},
							},
						},
						"q2_other": map[string]any{
							"type":        "string",
							"description": "Optional free-form answer to: 终端程序怎么选？",
						},
					},
				},
			})
			var res struct {
				Action  string         `json:"action"`
				Content map[string]any `json:"content"`
			}
			json.Unmarshal(r.Result, &res)
			text := "declined: " + res.Action
			if res.Action == "accept" {
				text = fmt.Sprintf("answers3: q0=%v|q0_other=%v|q1=%v|q2=%v", res.Content["q0"], res.Content["q0_other"], res.Content["q1"], res.Content["q2"])
			}
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
		case "history":
			hh, _ := load(sid)
			nTurns := 0
			if hh != nil {
				nTurns = len(hh.Turns)
			}
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": fmt.Sprintf("history: %d turns", nTurns)}})
		case "write":
			path, body, _ := strings.Cut(arg, " ")
			abs := path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(cwd, path)
			}
			tool(sid, "edit", "Write "+path, map[string]any{"path": abs, "content": body}, abs, func() (string, error) {
				os.MkdirAll(filepath.Dir(abs), 0o755)
				return "wrote " + path, os.WriteFile(abs, []byte(body+"\n"), 0o644)
			})
		case "read":
			callN++
			id := fmt.Sprintf("call-%d", callN)
			abs := filepath.Join(cwd, arg)
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "Read File", "kind": "read", "status": "pending", "rawInput": map[string]any{}})
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": "Read " + arg, "locations": []any{map[string]any{"path": abs}}})
			b, _ := os.ReadFile(abs)
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed", "rawOutput": string(b)})
		case "shell":
			tool(sid, "execute", arg, map[string]any{"command": arg}, "", func() (string, error) {
				c := shellCommand(arg)
				c.Dir = cwd
				b, err := c.CombinedOutput()
				return string(b), err
			})
		case "focus":
			callN++
			cid := fmt.Sprintf("call-%d", callN)
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": cid, "title": "Run update_focus", "kind": "other", "status": "in_progress", "rawInput": map[string]any{"focus": arg}})
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": cid, "status": "completed",
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "OK"}}}})
		case "steps":
			callN++
			cid := fmt.Sprintf("call-%d", callN)
			var steps []any
			for _, part := range strings.Split(arg, "|") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				title, status, ok := strings.Cut(part, "=")
				if !ok {
					status = "pending"
				}
				steps = append(steps, map[string]any{"title": strings.TrimSpace(title), "status": strings.TrimSpace(status)})
			}
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": cid, "title": "Run update_steps", "kind": "other", "status": "in_progress", "rawInput": map[string]any{"steps": steps}})
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": cid, "status": "completed",
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "OK"}}}})
		case "run-summary":
			summary, cmdStr, _ := strings.Cut(arg, "|")
			callN++
			cid := fmt.Sprintf("call-%d", callN)
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": cid, "title": cmdStr, "kind": "execute", "status": "in_progress",
				"rawInput": map[string]any{"CommandLine": cmdStr, "toolSummary": strings.TrimSpace(summary)}})
			c := shellCommand(cmdStr)
			c.Dir = cwd
			b, err := c.CombinedOutput()
			st := "completed"
			outTxt := string(b)
			if err != nil {
				st = "failed"
				outTxt += err.Error()
			}
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": cid, "status": st,
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": outTxt}}}})
		case "think":
			update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": strings.ReplaceAll(arg, `\n`, "\n")}})
		case "sleep":
			d, _ := time.ParseDuration(arg)
			select {
			case <-time.After(d):
			case <-cancelled:
				finishActivePrompt(map[string]any{"stopReason": "cancelled"}, 0, "")
				return true
			}
		case "crash":
			os.Exit(1)
		case "fail":
			finishActivePrompt(nil, -32603, arg)
			return true
		case "fail-code":
			code, text, _ := strings.Cut(arg, " ")
			n, _ := strconv.Atoi(code)
			finishActivePrompt(nil, n, text)
			return true
		}
	}
	return false
}

// modelConfig is the session's config options, selecting the first model
// when none is.
func modelConfig() any {
	if file := os.Getenv("FAKE_ACP_CONFIG_FILE"); file != "" {
		b, _ := os.ReadFile(file)
		return json.RawMessage(b)
	}
	opts := []any{}
	for _, x := range strings.Split(fakeSetting("FAKE_ACP_MODELS", "fakeacp.models"), ",") {
		if x == "" {
			continue
		}
		opts = append(opts, map[string]any{"value": x, "name": strings.ToUpper(x)})
		curModel = cmp.Or(curModel, x)
	}
	config := []any{
		map[string]any{"id": "mode", "category": "mode", "type": "select", "currentValue": "ask", "options": []any{map[string]any{"value": "ask"}}},
		map[string]any{"id": "llm", "category": "model", "type": "select", "currentValue": curModel,
			"options": []any{map[string]any{"group": "g", "name": "Models", "options": opts}}},
	}
	if os.Getenv("FAKE_ACP_EFFORTS") != "" {
		es := efforts(curModel)
		if !slices.Contains(es, curEffort) { // as agents do: the model's default
			curEffort = ""
			if slices.Contains(es, "high") {
				curEffort = "high"
			} else if len(es) > 0 {
				curEffort = es[0]
			}
		}
		var choices []any
		for _, e := range es {
			choices = append(choices, map[string]any{"value": e, "name": e})
		}
		config = append(config, map[string]any{"id": "effort", "category": "thought_level", "type": "select", "currentValue": curEffort, "options": choices})
	}
	return config
}

// efforts are model's thought levels in FAKE_ACP_EFFORTS.
func efforts(model string) []string {
	for _, x := range strings.Split(os.Getenv("FAKE_ACP_EFFORTS"), ";") {
		if m, es, ok := strings.Cut(x, "="); ok && m == model {
			return strings.Split(es, ",")
		}
	}
	return nil
}

func tool(sid, kind, title string, input map[string]any, path string, run func() (string, error)) {
	callN++
	id := fmt.Sprintf("call-%d", callN)
	// Announce the call first, then fill in its input, as real agents do.
	update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "Preparing…", "kind": kind, "status": "pending", "rawInput": map[string]any{}})
	tc := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": title, "rawInput": input}
	if path != "" {
		tc["locations"] = []any{map[string]any{"path": path}}
	}
	update(sid, tc)
	options := []any{
		map[string]any{"optionId": "allow-once", "name": "Allow", "kind": "allow_once"},
		map[string]any{"optionId": "allow-always", "name": "Always allow", "kind": "allow_always"},
		map[string]any{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
	}
	if os.Getenv("FAKE_ACP_ALLOW_ALWAYS_ONLY") == "1" {
		options = []any{options[1], options[2]}
	}
	r := ask("session/request_permission", map[string]any{"sessionId": sid, "toolCall": map[string]any{"toolCallId": id, "title": title, "kind": kind}, "options": options})
	var res struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	json.Unmarshal(r.Result, &res)
	if r.Error != nil || res.Outcome.OptionID != "allow-once" {
		update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "failed",
			"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "rejected"}}}})
		return
	}
	update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "in_progress"})
	text, err := run()
	status := "completed"
	if err != nil {
		status, text = "failed", text+err.Error()
	}
	update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": status,
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}}})
}
