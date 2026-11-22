package runtime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/acp"
	"github.com/hermeshai/hermesh/internal/runtime/claude"
	"github.com/hermeshai/hermesh/internal/runtime/codex"
)

// TestHelperMockCLI runs as a real OS child process speaking authentic Claude
// stream-json, Codex app-server JSON-RPC, or ACP JSON-RPC 2.0 over stdio.
func TestHelperMockCLI(t *testing.T) {
	mode := os.Getenv("HERMESH_MOCK_DRIVER")
	if mode == "" {
		return
	}
	for _, a := range os.Args {
		if a == "--version" {
			fmt.Println("mock-cli 2.5.0")
			os.Exit(0)
		}
		if a == "status" {
			fmt.Println(`{"loggedIn":true,"email":"dev@hermesh.ai"}`)
			fmt.Println("Logged in")
			os.Exit(0)
		}
	}
	switch mode {
	case "claude":
		runMockClaude()
	case "codex":
		runMockCodex()
	case "acp":
		runMockACP()
	}
	os.Exit(0)
}

func runMockClaude() {
	sc := bufio.NewScanner(os.Stdin)
	write := func(v any) {
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(b, '\n'))
	}
	model := "claude-sonnet-4-6"
	sessionID := "cl_sess_01"
	for i, a := range os.Args {
		if a == "--resume" && i+1 < len(os.Args) {
			sessionID = os.Args[i+1]
		}
		if a == "--model" && i+1 < len(os.Args) {
			model = os.Args[i+1]
		}
	}

	pendingCtrl := map[string]func(map[string]any){}
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m["type"] {
		case "control_request":
			reqID, _ := m["request_id"].(string)
			reqObj, _ := m["request"].(map[string]any)
			sub, _ := reqObj["subtype"].(string)
			if sub == "initialize" {
				write(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID, "model": model})
			} else if sub == "set_model" {
				if nm, ok := reqObj["model"].(string); ok {
					model = nm
				}
			} else if sub == "interrupt" {
				write(map[string]any{"type": "result", "subtype": "interrupted"})
			}
			write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": reqID}})

		case "control_response":
			respWrap, _ := m["response"].(map[string]any)
			reqID, _ := respWrap["request_id"].(string)
			inner, _ := respWrap["response"].(map[string]any)
			if fn := pendingCtrl[reqID]; fn != nil {
				delete(pendingCtrl, reqID)
				fn(inner)
			}

		case "user":
			write(map[string]any{"type": "user", "isReplay": true})
			msgObj, _ := m["message"].(map[string]any)
			prompt, _ := msgObj["content"].(string)
			if strings.Contains(prompt, "run_bash") {
				pendingCtrl["perm_1"] = func(res map[string]any) {
					beh, _ := res["behavior"].(string)
					replyText := fmt.Sprintf("claude(%s) bash=%s", model, beh)
					if beh == "deny" {
						replyText += ":" + fmt.Sprint(res["message"])
					}
					write(map[string]any{
						"type":  "stream_event",
						"event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": replyText}},
					})
					write(map[string]any{
						"type":    "assistant",
						"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": replyText}}},
					})
					write(map[string]any{
						"type":  "result",
						"usage": map[string]any{"input_tokens": 100, "output_tokens": 25},
					})
				}
				write(map[string]any{
					"type":       "control_request",
					"request_id": "perm_1",
					"request": map[string]any{
						"subtype":   "can_use_tool",
						"tool_name": "Bash",
						"input":     map[string]any{"command": "npm publish"},
					},
				})
			} else {
				txt := "claude(" + model + ") ok"
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": txt}}},
				})
				write(map[string]any{
					"type":  "result",
					"usage": map[string]any{"input_tokens": 50, "output_tokens": 10},
				})
			}
		}
	}
}

func runMockCodex() {
	sc := bufio.NewScanner(os.Stdin)
	write := func(v any) {
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(b, '\n'))
	}
	var waitingApproval bool
	var curTurnModel string
	var curEffort string
	for sc.Scan() {
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.ID != nil && m.Method == "" && waitingApproval {
			waitingApproval = false
			var res struct {
				Decision string `json:"decision"`
			}
			_ = json.Unmarshal(m.Result, &res)
			msg := fmt.Sprintf("codex(%s/%s) decision=%s", curTurnModel, curEffort, res.Decision)
			write(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"delta": msg}})
			write(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"id": "it_1", "type": "agentMessage", "text": msg}}})
			write(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 200, "outputTokens": 40}}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"status": "completed"}}})
			continue
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "thread/start", "thread/resume":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"thread": map[string]any{"id": "cx_th_99"}}})
		case "thread/unarchive":
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "turn/start":
			var p struct {
				Model  string `json:"model"`
				Effort string `json:"effort"`
			}
			_ = json.Unmarshal(m.Params, &p)
			curTurnModel, curEffort = p.Model, p.Effort
			write(map[string]any{"id": *m.ID, "result": map[string]any{"turn": map[string]any{"id": "turn_1"}}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "turn_1"}}})
			waitingApproval = true
			write(map[string]any{
				"id":     777,
				"method": "item/commandExecution/requestApproval",
				"params": map[string]any{"command": "git commit -m 'ship'", "reason": "commit work"},
			})
		case "turn/steer":
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		}
	}
}

func runMockACP() {
	sc := bufio.NewScanner(os.Stdin)
	write := func(v any) {
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(b, '\n'))
	}
	var promptReqID int64
	var curModel = "gemini-2.5-pro"
	for sc.Scan() {
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.ID != nil && m.Method == "" && *m.ID == 888 {
			// Response to fs/write_text_file!
			status := "wrote_ok"
			if m.Error != nil {
				status = "write_err:" + m.Error.Message
			}
			txt := fmt.Sprintf("acp(%s) %s", curModel, status)
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": txt},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      promptReqID,
				"result":  map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 80, "outputTokens": 20}},
			})
			continue
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{"agentCapabilities": map[string]any{"loadSession": true}}})
		case "session/new":
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{"sessionId": "acp_sess_42"}})
		case "session/load", "session/set_mode", "session/set_config_option":
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{}})
		case "session/set_model":
			var p struct {
				ModelID string `json:"modelId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			curModel = p.ModelID
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{}})
		case "session/prompt":
			promptReqID = *m.ID
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      888,
				"method":  "fs/write_text_file",
				"params":  map[string]any{"path": "generated_by_acp.txt", "content": "hello from acp\n"},
			})
		}
	}
}

func collectTurn(t *testing.T, sess runtime.Session) (msgs []string, nativeID string) {
	t.Helper()
	timeout := time.After(8 * time.Second)
	for {
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for turn completion")
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("session closed unexpectedly before turn completed")
			}
			switch ev.Kind {
			case runtime.EventNativeSession:
				nativeID = ev.NativeID
			case runtime.EventAssistantMessage:
				msgs = append(msgs, ev.Text)
			case runtime.EventTurnCompleted:
				return msgs, nativeID
			case runtime.EventTurnFailed:
				t.Fatalf("turn failed: %s (%s)", ev.ErrorMessage, ev.ErrorCode)
			}
		}
	}
}

func TestClaudeDriverSubprocessProtocol(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	drv := claude.NewDriver("")
	var gateCalls int
	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_cl",
		Workdir:  t.TempDir(),
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "claude",
			Driver:     "claude",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "claude"},
		},
		Selection: &threadv2.ModelSelection{InstanceId: "claude", Model: "claude-sonnet-4-6"},
		Gate: func(_ context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			gateCalls++
			if gateCalls == 1 {
				return runtime.ToolGateResult{Allowed: false, Feedback: "no publishing"}, nil
			}
			return runtime.ToolGateResult{Allowed: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	// Turn 1: triggers can_use_tool -> Gate declines with feedback!
	if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r1", Prompt: "run_bash"}); err != nil {
		t.Fatal(err)
	}
	msgs, nativeID := collectTurn(t, sess)
	if nativeID != "cl_sess_01" {
		t.Fatalf("nativeID = %q, want cl_sess_01", nativeID)
	}
	if len(msgs) == 0 || !strings.Contains(msgs[0], "claude(claude-sonnet-4-6) bash=deny:no publishing") {
		t.Fatalf("unexpected Turn 1 output: %v", msgs)
	}

	// Turn 2: hot in-session model switch (`set_model`) -> Gate approves!
	if err := sess.SetSelection(ctx, &threadv2.ModelSelection{InstanceId: "claude", Model: "claude-opus-4-6"}); err != nil {
		t.Fatalf("SetSelection hot switch: %v", err)
	}
	if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r2", Prompt: "run_bash"}); err != nil {
		t.Fatal(err)
	}
	msgs2, _ := collectTurn(t, sess)
	if len(msgs2) == 0 || !strings.Contains(msgs2[0], "claude(claude-opus-4-6) bash=allow") {
		t.Fatalf("unexpected Turn 2 output after hot model switch: %v", msgs2)
	}
}

func TestCodexDriverSubprocessProtocol(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	drv := codex.NewDriver("")

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_cx",
		Workdir:  t.TempDir(),
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "codex",
			Driver:     "codex",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "app-server"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "codex"},
		},
		Selection: &threadv2.ModelSelection{
			InstanceId: "codex",
			Model:      "gpt-5.4",
			Options: map[string]*threadv2.OptionValue{
				"reasoningEffort": {Kind: &threadv2.OptionValue_StringValue{StringValue: "xhigh"}},
			},
		},
		Gate: func(_ context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			if req.Approval.GetCommand() != "git commit -m 'ship'" {
				t.Errorf("unexpected codex approval command: %q", req.Approval.GetCommand())
			}
			return runtime.ToolGateResult{Allowed: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.StartTurn(ctx, runtime.TurnInput{
		RunID:  "r1",
		Prompt: "ship it",
		Selection: &threadv2.ModelSelection{
			InstanceId: "codex",
			Model:      "gpt-5.4",
			Options: map[string]*threadv2.OptionValue{
				"reasoningEffort": {Kind: &threadv2.OptionValue_StringValue{StringValue: "xhigh"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	msgs, nativeID := collectTurn(t, sess)
	if nativeID != "cx_th_99" {
		t.Fatalf("nativeID = %q, want cx_th_99", nativeID)
	}
	if len(msgs) == 0 || !strings.Contains(msgs[0], "codex(gpt-5.4/xhigh) decision=accept") {
		t.Fatalf("unexpected codex output: %v", msgs)
	}
}

func TestACPDriverSubprocessProtocolAndFilesystemGate(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	workdir := t.TempDir()
	drv := acp.NewDriver("")

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_acp",
		Workdir:  workdir,
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "gemini",
			Driver:     "acp",
			AcpFlavor:  "gemini",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "--acp"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "acp"},
		},
		Selection: &threadv2.ModelSelection{InstanceId: "gemini", Model: "gemini-3.1-pro"},
		Gate: func(_ context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			return runtime.ToolGateResult{Allowed: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r1", Prompt: "generate file"}); err != nil {
		t.Fatal(err)
	}
	msgs, nativeID := collectTurn(t, sess)
	if nativeID != "acp_sess_42" {
		t.Fatalf("nativeID = %q, want acp_sess_42", nativeID)
	}
	if len(msgs) == 0 || !strings.Contains(msgs[0], "acp(gemini-3.1-pro) wrote_ok") {
		t.Fatalf("unexpected ACP output: %v", msgs)
	}
	written, err := os.ReadFile(filepath.Join(workdir, "generated_by_acp.txt"))
	if err != nil || string(written) != "hello from acp\n" {
		t.Fatalf("ACP fs/write_text_file did not write file: %q (err=%v)", string(written), err)
	}
}
