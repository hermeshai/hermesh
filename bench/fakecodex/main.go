// Command fakecodex speaks the Codex app-server protocol (JSON-RPC over
// stdio, JSONL) so load tests exercise the real Codex adapter, process
// spawning and pipes without calling a model. Behaviour per turn:
//
//	FAKE_CODEX_TURN_MS       total turn duration (default 200)
//	FAKE_CODEX_DELTAS        agentMessage delta notifications (default 20)
//	FAKE_CODEX_COMMANDS      commandExecution items (default 2)
//	FAKE_CODEX_OUTPUT_BYTES  aggregated output per command (default 512)
//	FAKE_CODEX_WRITE         files written per turn (default 1)
//	FAKE_CODEX_MESSAGES      agent messages between commands (default 0)
//	FAKE_CODEX_BALLAST_MB    resident memory to hold, like a real CLI (default 0)
//	FAKE_CODEX_LOG           file to append each thread/start's params to
//	FAKE_CODEX_START_MS      time thread/start takes to answer (default 0)
//	FAKE_CODEX_FAIL_START    a file: while it exists, thread/start removes
//	                         it and Codex exits, as one dying at its start
//
// model/list offers gpt-fake-big (the default) and gpt-fake-small;
// config/read answers FAKE_CODEX_CONFIG, a JSON object (default {}).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}

var (
	turnMS   = envInt("FAKE_CODEX_TURN_MS", 200)
	deltas   = envInt("FAKE_CODEX_DELTAS", 20)
	commands = envInt("FAKE_CODEX_COMMANDS", 2)
	messages = envInt("FAKE_CODEX_MESSAGES", 0)
	outBytes = envInt("FAKE_CODEX_OUTPUT_BYTES", 512)
	writes   = envInt("FAKE_CODEX_WRITE", 1)
	ballast  []byte
)

type msg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

var (
	outMu sync.Mutex
	out   = bufio.NewWriterSize(os.Stdout, 64<<10)
)

func send(v any) {
	b, _ := json.Marshal(v)
	outMu.Lock()
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
	outMu.Unlock()
}

func reply(id json.RawMessage, result any) { send(map[string]any{"id": id, "result": result}) }
func notify(method string, params any)     { send(map[string]any{"method": method, "params": params}) }

type state struct {
	mu       sync.Mutex
	thread   string
	cwd      string
	turn     int
	cancel   chan struct{}
	nextItem int
}

func main() {
	if mb := envInt("FAKE_CODEX_BALLAST_MB", 0); mb > 0 {
		ballast = make([]byte, mb<<20)
		for i := 0; i < len(ballast); i += 4096 {
			ballast[i] = 1
		}
	}
	st := &state{}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{"userAgent": "fakecodex/1.0", "platformOs": "fake"})
		case "initialized":
		case "model/list":
			efforts := []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"}}
			reply(m.ID, map[string]any{"data": []any{
				map[string]any{"id": "gpt-fake-big", "displayName": "GPT-Fake-Big", "isDefault": true, "defaultReasoningEffort": "high", "supportedReasoningEfforts": efforts},
				map[string]any{"id": "gpt-fake-small", "displayName": "GPT-Fake-Small", "defaultReasoningEffort": "low", "supportedReasoningEfforts": efforts},
			}})
		case "config/read":
			cfg := json.RawMessage(`{}`)
			if c := os.Getenv("FAKE_CODEX_CONFIG"); c != "" {
				cfg = json.RawMessage(c)
			}
			reply(m.ID, map[string]any{"config": cfg})
		case "thread/start", "thread/resume":
			if f := os.Getenv("FAKE_CODEX_FAIL_START"); f != "" && os.Remove(f) == nil {
				fmt.Fprintln(os.Stderr, "fakecodex: failing this start")
				os.Exit(1)
			}
			time.Sleep(time.Duration(envInt("FAKE_CODEX_START_MS", 0)) * time.Millisecond)
			if log := os.Getenv("FAKE_CODEX_LOG"); log != "" && m.Method == "thread/start" {
				if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					f.Write(append(append([]byte{}, m.Params...), '\n'))
					f.Close()
				}
			}
			var p struct {
				Cwd      string `json:"cwd"`
				ThreadID string `json:"threadId"`
			}
			json.Unmarshal(m.Params, &p)
			st.mu.Lock()
			st.cwd = p.Cwd
			st.thread = p.ThreadID
			if st.thread == "" {
				st.thread = fmt.Sprintf("thr_%d_%d", os.Getpid(), time.Now().UnixNano())
			}
			st.mu.Unlock()
			reply(m.ID, map[string]any{"thread": map[string]any{"id": st.thread}})
			notify("thread/started", map[string]any{"thread": map[string]any{"id": st.thread}})
		case "turn/start":
			var p struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			json.Unmarshal(m.Params, &p)
			st.mu.Lock()
			st.turn++
			turnID := fmt.Sprintf("turn_%d", st.turn)
			cancel := make(chan struct{})
			st.cancel = cancel
			st.mu.Unlock()
			reply(m.ID, map[string]any{"turn": map[string]any{"id": turnID}})
			text := ""
			if len(p.Input) > 0 {
				text = p.Input[0].Text
			}
			go runTurn(st, turnID, text, cancel)
		case "turn/interrupt":
			st.mu.Lock()
			if st.cancel != nil {
				close(st.cancel)
				st.cancel = nil
			}
			st.mu.Unlock()
			reply(m.ID, map[string]any{})
		default:
			if len(m.ID) > 0 && m.Method != "" {
				reply(m.ID, map[string]any{})
			}
		}
	}
}

func (st *state) item() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextItem++
	return fmt.Sprintf("item_%d", st.nextItem)
}

func runTurn(st *state, turnID, prompt string, cancel chan struct{}) {
	notify("turn/started", map[string]any{"turn": map[string]any{"id": turnID}})
	steps := deltas + commands*2 + messages + writes*2 + 1
	pause := time.Duration(turnMS) * time.Millisecond / time.Duration(max(steps, 1))
	wait := func() bool {
		select {
		case <-cancel:
			return false
		case <-time.After(pause):
			return true
		}
	}
	interrupted := func() {
		notify("turn/completed", map[string]any{"turn": map[string]any{"id": turnID, "status": "interrupted"}})
	}
	output := strings.Repeat("x", outBytes)
	for i := 0; i < commands; i++ {
		id := st.item()
		notify("item/started", map[string]any{"item": map[string]any{"id": id, "type": "commandExecution", "command": "echo fake", "status": "inProgress"}})
		if !wait() {
			interrupted()
			return
		}
		notify("item/completed", map[string]any{"item": map[string]any{"id": id, "type": "commandExecution", "command": "echo fake", "status": "completed", "exitCode": 0, "aggregatedOutput": output}})
		if !wait() {
			interrupted()
			return
		}
		// spread the commentary messages over the commands
		if messages > 0 && i*messages/max(commands, 1) != (i+1)*messages/max(commands, 1) {
			notify("item/completed", map[string]any{"item": map[string]any{"id": st.item(), "type": "agentMessage",
				"text": fmt.Sprintf("Step %d: looked at the output, now checking the next file in the module and running its tests.", i)}})
			if !wait() {
				interrupted()
				return
			}
		}
	}
	for i := 0; i < writes; i++ {
		id := st.item()
		name := fmt.Sprintf("fake-%s-%d.txt", turnID, i)
		notify("item/started", map[string]any{"item": map[string]any{"id": id, "type": "fileChange", "changes": []any{map[string]any{"path": name, "kind": "add", "diff": ""}}, "status": "inProgress"}})
		st.mu.Lock()
		cwd := st.cwd
		st.mu.Unlock()
		os.WriteFile(filepath.Join(cwd, name), []byte(prompt+"\n"), 0o644)
		if !wait() {
			interrupted()
			return
		}
		notify("item/completed", map[string]any{"item": map[string]any{"id": id, "type": "fileChange", "status": "completed"}})
		if !wait() {
			interrupted()
			return
		}
	}
	msgID := st.item()
	for i := 0; i < deltas; i++ {
		notify("item/agentMessage/delta", map[string]any{"itemId": msgID, "delta": "tok "})
		if !wait() {
			interrupted()
			return
		}
	}
	final := "done: " + prompt
	notify("item/completed", map[string]any{"item": map[string]any{"id": msgID, "type": "agentMessage", "text": final}})
	notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 100, "outputTokens": 50}}})
	notify("turn/completed", map[string]any{"turn": map[string]any{"id": turnID, "status": "completed"}})
}
