// Command fakeacp is a scripted coding agent that speaks the Agent Client
// Protocol on stdio, for tests of the ACP adapter. Each prompt line is a
// command:
//
//	say <text>          agent message (streamed in chunks); \n in text is a newline
//	write <path> <text> edit tool call, asks permission, then writes the file
//	shell <command>     execute tool call, asks permission, then runs it
//	read <path>         read tool call without a permission request
//	sleep <duration>    pause (session/cancel ends the turn as cancelled)
//	history             agent message "history: <n> turns"
//	crash               exit in the middle of the turn
//	fail <text>         answer the prompt with a JSON-RPC error
//	fail-code <n> <t>   same, with error code n
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
//	FAKE_ACP_RESUME=load|resume|none  how sessions resume (default load)
//	FAKE_ACP_AUTH=<id>                require `authenticate` with this method
//	FAKE_ACP_MODELS=a,b               models session/set_model accepts
//	FAKE_ACP_CONFIG=1                 offer the models as a "model" config
//	                                  option (session/set_config_option)
//	                                  and drop session/set_model
//	FAKE_ACP_ALLOW_ALWAYS_ONLY=1      offer only allow_always/reject_once
//	FAKE_ACP_IMAGES=1                 take image prompt blocks
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	nextID    = 1000
	waiting   = map[string]chan msg{}
	waitMu    sync.Mutex
	cancelled = make(chan struct{}, 1)
	cwd       string
)

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

func load(sid string) (*history, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, sid+".json"))
	if err != nil {
		return nil, err
	}
	var h history
	return &h, json.Unmarshal(b, &h)
}

func save(sid string, h *history) {
	b, _ := json.Marshal(h)
	os.WriteFile(filepath.Join(stateDir, sid+".json"), b, 0o600)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("fakeacp 1.2.3")
		return
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
		caps := map[string]any{"loadSession": resume == "load"}
		if resume == "resume" {
			caps["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
		}
		caps["promptCapabilities"] = map[string]any{"image": os.Getenv("FAKE_ACP_IMAGES") == "1"}
		var auth []any
		if authID != "" {
			auth = []any{map[string]any{"id": "other", "name": "Other"}, map[string]any{"id": authID, "name": "Login"}}
		}
		reply(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": caps, "authMethods": auth})
	case "authenticate":
		if p.MethodID != authID {
			replyErr(m.ID, -32000, "unknown auth method "+p.MethodID)
			return
		}
		authed = true
		reply(m.ID, map[string]any{})
	case "session/new":
		if needAuth {
			replyErr(m.ID, -32000, "Authentication required")
			return
		}
		cwd = p.Cwd
		sid := fmt.Sprintf("fake-%d", time.Now().UnixNano())
		save(sid, &history{})
		res := map[string]any{"sessionId": sid}
		if os.Getenv("FAKE_ACP_CONFIG") == "1" {
			var opts []any
			for _, x := range strings.Split(os.Getenv("FAKE_ACP_MODELS"), ",") {
				opts = append(opts, map[string]any{"value": x, "name": x})
			}
			res["configOptions"] = []any{
				map[string]any{"id": "mode", "category": "mode", "type": "select", "currentValue": "ask", "options": []any{map[string]any{"value": "ask"}}},
				map[string]any{"id": "llm", "category": "model", "type": "select", "currentValue": "default",
					"options": []any{map[string]any{"group": "g", "name": "Models", "options": opts}}},
			}
		}
		reply(m.ID, res)
	case "session/load", "session/resume":
		if needAuth {
			replyErr(m.ID, -32000, "Authentication required")
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
		reply(m.ID, map[string]any{})
	case "session/set_config_option":
		if p.ConfigID == "llm" && os.Getenv("FAKE_ACP_CONFIG") == "1" {
			reply(m.ID, map[string]any{"configOptions": []any{}})
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
				reply(m.ID, map[string]any{})
				return
			}
		}
		replyErr(m.ID, -32602, "unknown model "+p.ModelID)
	case "session/prompt":
		select { // drop a stale cancel
		case <-cancelled:
		default:
		}
		var images []string
		for _, b := range p.Prompt {
			if b.Type == "image" && b.Data != "" {
				images = append(images, b.MimeType)
			}
		}
		go prompt(m.ID, p.SessionID, p.Prompt[0].Text, images)
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

func prompt(id json.RawMessage, sid, text string, images []string) {
	h, err := load(sid)
	if err != nil {
		replyErr(id, -32002, "unknown session")
		return
	}
	h.Turns = append(h.Turns, text)
	save(sid, h)
	for _, line := range strings.Split(text, "\n") {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch cmd {
		case "say":
			r := []rune(strings.ReplaceAll(arg, `\n`, "\n")) // split between characters, as agents stream
			half := len(r) / 2
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(r[:half])}})
			update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}})
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(r[half:])}})
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
		case "history":
			update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": fmt.Sprintf("history: %d turns", len(h.Turns))}})
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
				c := exec.Command("sh", "-c", arg)
				c.Dir = cwd
				b, err := c.CombinedOutput()
				return string(b), err
			})
		case "sleep":
			d, _ := time.ParseDuration(arg)
			select {
			case <-time.After(d):
			case <-cancelled:
				reply(id, map[string]any{"stopReason": "cancelled"})
				return
			}
		case "crash":
			os.Exit(1)
		case "fail":
			replyErr(id, -32603, arg)
			return
		case "fail-code":
			code, text, _ := strings.Cut(arg, " ")
			n, _ := strconv.Atoi(code)
			replyErr(id, n, text)
			return
		}
	}
	reply(id, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 100, "outputTokens": 20, "cachedReadTokens": 5}})
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
