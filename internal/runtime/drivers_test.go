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
	"github.com/hermeshai/hermesh/internal/runtime/her"
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
			if strings.Contains(prompt, "multistep_tools") {
				write(map[string]any{
					"type":  "stream_event",
					"event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "let me read first"}},
				})
				write(map[string]any{
					"type": "assistant",
					"message": map[string]any{"content": []any{
						map[string]any{"type": "text", "text": "let me read first"},
						map[string]any{"type": "tool_use", "id": "tu_ask", "name": "AskUserQuestion", "input": map[string]any{}},
						map[string]any{"type": "tool_use", "id": "tu_read", "name": "Read", "input": map[string]any{"file_path": "pkg/foo.go"}},
					}},
				})
				write(map[string]any{
					"type": "user",
					"message": map[string]any{"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu_ask", "content": "ignored"},
						map[string]any{"type": "tool_result", "tool_use_id": "tu_read", "content": []any{map[string]any{"type": "text", "text": "package foo"}}},
					}},
				})
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "read finished"}}},
				})
				write(map[string]any{
					"type":  "result",
					"usage": map[string]any{"input_tokens": 60, "output_tokens": 15},
				})
			} else if strings.Contains(prompt, "run_bash") {
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
		case "thread/start":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"thread": map[string]any{"id": "cx_th_99"}}})
		case "thread/resume":
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.ThreadID == "locked_cursor" {
				write(map[string]any{"id": *m.ID, "error": map[string]any{"code": -32000, "message": "thread already has an active writer"}})
			} else {
				write(map[string]any{"id": *m.ID, "result": map[string]any{"thread": map[string]any{"id": "cx_th_99"}}})
			}
		case "thread/fork":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"thread": map[string]any{"id": "cx_th_forked"}}})
		case "thread/unarchive":
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "turn/start":
			var p struct {
				Model  string `json:"model"`
				Effort string `json:"effort"`
				Input  []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(m.Params, &p)
			curTurnModel, curEffort = p.Model, p.Effort
			write(map[string]any{"id": *m.ID, "result": map[string]any{"turn": map[string]any{"id": "turn_1"}}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "turn_1"}}})
			if len(p.Input) > 0 && strings.Contains(p.Input[0].Text, "propose_codex_plan") {
				write(map[string]any{
					"method": "item/completed",
					"params": map[string]any{
						"item": map[string]any{
							"id":   "cx_plan_1",
							"type": "plan",
							"text": "# Codex Plan\n\n1. Inspect\n2. Ship",
						},
					},
				})
				write(map[string]any{
					"method": "item/completed",
					"params": map[string]any{
						"item": map[string]any{
							"id":   "it_plan_msg",
							"type": "agentMessage",
							"text": "Plan ready for review.",
						},
					},
				})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"status": "completed"}}})
				continue
			}
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
		if m.ID != nil && m.Method == "" && *m.ID == 777 {
			// Response to session/request_permission!
			var res struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(m.Result, &res)
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "tc_1",
						"title":         "Run pytest",
						"kind":          "execute",
						"status":        "in_progress",
						"rawInput":      map[string]any{"command": "pytest -q"},
					},
				},
			})
			// tool_call_update with array Content (previously failed json.Unmarshal in handleSessionUpdate!):
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call_update",
						"toolCallId":    "tc_1",
						"status":        "completed",
						"content": []any{
							map[string]any{
								"type":    "content",
								"content": map[string]any{"type": "text", "text": "3 passed"},
							},
						},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      778,
				"method":  "elicitation/create",
				"params": map[string]any{
					"mode":    "form",
					"message": "Target env?",
					"requestedSchema": map[string]any{
						"required": []string{"env"},
						"properties": map[string]any{
							"env": map[string]any{"type": "string", "title": "Env", "enum": []string{"staging", "prod"}},
						},
					},
				},
			})
			// Stash chosen permission option in curModel for assertion.
			curModel = res.Outcome.OptionID
			continue
		}
		if m.ID != nil && m.Method == "" && *m.ID == 778 {
			var res struct {
				Action  string            `json:"action"`
				Content map[string]string `json:"content"`
			}
			_ = json.Unmarshal(m.Result, &res)
			post := fmt.Sprintf("perm=%s env=%s:%s", curModel, res.Action, res.Content["env"])
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": post},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      promptReqID,
				"result":  map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 90, "outputTokens": 30, "cachedReadTokens": 10}},
			})
			continue
		}
		if m.ID != nil && m.Method == "" && *m.ID == 779 {
			var res struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(m.Result, &res)
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "plan_exit_opt=" + res.Outcome.OptionID},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      promptReqID,
				"result":  map[string]any{"stopReason": "end_turn"},
			})
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
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "antigravity_artifacts") {
				brainDir := os.Getenv("MOCK_BRAIN_DIR")
				planPath := filepath.Join(brainDir, "implementation_plan.md")
				_ = os.WriteFile(planPath, []byte("# v1 Plan\n\nInitial draft\n"), 0o644)
				_ = os.WriteFile(planPath+".metadata.json", []byte(`{"summary":"Plan summary","userFacing":true,"requestFeedback":true}`), 0o644)
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_plan_w",
							"title":         "write_file",
							"kind":          "edit",
							"status":        "in_progress",
							"rawInput": map[string]any{
								"Path":    planPath,
								"Content": "# v1 Plan\n\nInitial draft\n",
								"ArtifactMetadata": map[string]any{
									"Summary":         "Plan summary",
									"UserFacing":      true,
									"RequestFeedback": true,
								},
							},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call_update",
							"toolCallId":    "tc_plan_w",
							"status":        "completed",
							"locations":     []any{map[string]any{"path": planPath}},
						},
					},
				})
				// Subsequent edit_file omits ArtifactMetadata in rawInput and only sends partial Target/Replacement;
				// driver must read full updated markdown + .metadata.json sidecar from disk!
				_ = os.WriteFile(planPath, []byte("# Final Unified Plan\n\n1. Adapter\n2. Kernel\n3. UI\n"), 0o644)
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_plan_e",
							"title":         "edit_file",
							"kind":          "edit",
							"status":        "completed",
							"locations":     []any{map[string]any{"path": planPath}},
							"rawInput": map[string]any{
								"Path":        planPath,
								"Target":      "Initial draft",
								"Replacement": "1. Adapter\n2. Kernel\n3. UI",
							},
						},
					},
				})
				// Deliverable artifact (UserFacing=true, RequestFeedback=false) -> inline PROPOSED_PLAN without review gate.
				walkPath := filepath.Join(brainDir, "walkthrough.md")
				_ = os.WriteFile(walkPath, []byte("# Walkthrough\n\nVerified end-to-end.\n"), 0o644)
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_walk",
							"title":         "write_file",
							"kind":          "edit",
							"status":        "completed",
							"rawInput": map[string]any{
								"Path":    walkPath,
								"Content": "# Walkthrough\n\nVerified end-to-end.\n",
								"ArtifactMetadata": map[string]any{
									"Summary":         "Walkthrough summary",
									"UserFacing":      true,
									"RequestFeedback": false,
								},
							},
						},
					},
				})
				// Non-markdown widget (UserFacing=true, RequestFeedback=false, .html) -> must NOT emit EventPlanProposed.
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_html",
							"title":         "write_file",
							"kind":          "edit",
							"status":        "completed",
							"rawInput": map[string]any{
								"Path":    filepath.Join(brainDir, "widget.html"),
								"Content": "<div>widget</div>",
								"ArtifactMetadata": map[string]any{
									"Summary":         "HTML widget",
									"UserFacing":      true,
									"RequestFeedback": false,
								},
							},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "See implementation_plan.md and click Proceed."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      promptReqID,
					"result":  map[string]any{"stopReason": "end_turn"},
				})
			} else if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "acp_exit_plan_mode") {
				_ = os.WriteFile("PLAN.md", []byte("# Standard ACP Plan\n\nStep A\nStep B\n"), 0o644)
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      779,
					"method":  "session/request_permission",
					"params": map[string]any{
						"toolCall": map[string]any{
							"toolCallId": "tc_exit",
							"name":       "exit_plan_mode",
							"title":      "Exit Plan Mode",
							"kind":       "switch_mode",
							"rawInput":   map[string]any{"plan_path": "PLAN.md"},
						},
						"options": []any{
							map[string]any{"optionId": "allow_plan", "kind": "allow_once"},
							map[string]any{"optionId": "reject_plan", "kind": "reject_once"},
						},
					},
				})
			} else if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "multistep_acp") {
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "acp_preamble"},
						},
					},
				})
				// Options[0] is allow_once! When Gate denies, driver MUST pick opt_reject (never Options[0]).
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      777,
					"method":  "session/request_permission",
					"params": map[string]any{
						"toolCall": map[string]any{"title": "rm -rf /tmp/x", "kind": "execute"},
						"options": []any{
							map[string]any{"optionId": "opt_allow", "kind": "allow_once"},
							map[string]any{"optionId": "opt_reject", "kind": "reject_once"},
						},
					},
				})
			} else {
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      888,
					"method":  "fs/write_text_file",
					"params":  map[string]any{"path": "generated_by_acp.txt", "content": "hello from acp\n"},
				})
			}
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

func TestACPMultiStepToolArrayContentPermissionDenialAndElicitation(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	drv := acp.NewDriver("")

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_acp_2",
		Workdir:  t.TempDir(),
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "antigravity",
			Driver:     "acp",
			AcpFlavor:  "antigravity",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "acp"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "acp"},
		},
		Gate: func(_ context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			if req.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL {
				return runtime.ToolGateResult{Allowed: false}, nil
			}
			if req.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
				return runtime.ToolGateResult{
					Allowed: true,
					Answers: map[string]*threadv2.QuestionAnswer{
						"env": {SelectedOptions: []string{"staging"}},
					},
				}, nil
			}
			return runtime.ToolGateResult{Allowed: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r_acp", Prompt: "multistep_acp"}); err != nil {
		t.Fatal(err)
	}
	var events []runtime.Event
	timeout := time.After(8 * time.Second)
loop:
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for ACP multistep turn")
		case ev := <-sess.Events():
			events = append(events, ev)
			if ev.Kind == runtime.EventTurnCompleted {
				break loop
			}
		}
	}

	var msgIDs []string
	var msgTexts []string
	var completedTool *runtime.Event
	for i := range events {
		ev := events[i]
		if ev.Kind == runtime.EventAssistantMessage {
			msgIDs = append(msgIDs, ev.ItemID)
			msgTexts = append(msgTexts, ev.Text)
		}
		if ev.Kind == runtime.EventToolCompleted {
			completedTool = &events[i]
		}
	}
	if len(msgTexts) != 2 || msgTexts[0] != "acp_preamble" || msgTexts[1] != "perm=opt_reject env=accept:staging" || msgIDs[0] == msgIDs[1] {
		t.Fatalf("expected 2 distinct assistant messages (preamble + post-tool), got IDs=%v texts=%v", msgIDs, msgTexts)
	}
	if completedTool == nil || completedTool.Command != "pytest -q" || completedTool.Output != "3 passed" {
		t.Fatalf("ACP tool_call_update array content not parsed/merged: %+v", completedTool)
	}
}

func TestClaudeMultiStepToolResultArrayAndSkipInteractiveTools(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	drv := claude.NewDriver("")

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_cl_multi",
		Workdir:  t.TempDir(),
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "claude",
			Driver:     "claude",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "claude"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r_cl", Prompt: "multistep_tools"}); err != nil {
		t.Fatal(err)
	}
	var msgIDs, msgTexts []string
	var startedPath, completedOut string
	var completedToolIDs []string
	timeout := time.After(8 * time.Second)
loop:
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for Claude multistep turn")
		case ev := <-sess.Events():
			switch ev.Kind {
			case runtime.EventAssistantMessage:
				msgIDs = append(msgIDs, ev.ItemID)
				msgTexts = append(msgTexts, ev.Text)
			case runtime.EventToolStarted:
				startedPath = ev.FilePath
			case runtime.EventToolCompleted:
				completedToolIDs = append(completedToolIDs, ev.ItemID)
				completedOut = ev.Output
			case runtime.EventTurnCompleted:
				break loop
			}
		}
	}
	if len(msgTexts) != 2 || msgIDs[0] == msgIDs[1] || msgTexts[0] != "let me read first" || msgTexts[1] != "read finished" {
		t.Fatalf("Claude multi-step messages collided: IDs=%v texts=%v", msgIDs, msgTexts)
	}
	if len(completedToolIDs) != 1 || completedToolIDs[0] != "tu_read" {
		t.Fatalf("expected only tu_read completion (no orphan AskUserQuestion completion), got %v", completedToolIDs)
	}
	if startedPath != "pkg/foo.go" || completedOut != "package foo" {
		t.Fatalf("Claude Read FilePath=%q Output=%q, want pkg/foo.go / package foo", startedPath, completedOut)
	}
}

func TestCodexActiveWriterForkFallbackAndHerCloseSafety(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	drv := codex.NewDriver("")

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		ThreadID:     "th_cx_fork",
		Workdir:      t.TempDir(),
		ResumeCursor: "locked_cursor",
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "codex",
			Driver:     "codex",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "app-server"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "codex"},
		},
	})
	if err != nil {
		t.Fatalf("expected active-writer resume to fall back to thread/fork, got err: %v", err)
	}
	ev := <-sess.Events()
	if ev.Kind != runtime.EventNativeSession || ev.NativeID != "cx_th_forked" {
		t.Fatalf("expected forked native session cx_th_forked, got %+v", ev)
	}
	_ = sess.Close()

	// Her driver: closing while Send is blocked or returning must never panic on closed channel.
	started := make(chan struct{})
	release := make(chan struct{})
	herDrv := her.NewDriver(func() bool { return true }, func(ctx context.Context, _, _ string) (string, string, error) {
		close(started)
		select {
		case <-release:
			return "late reply", "her_s2", nil
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	})
	hs, err := herDrv.StartSession(ctx, runtime.StartSpec{ResumeCursor: "her_s1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.StartTurn(ctx, runtime.TurnInput{RunID: "r_her", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	<-started
	_ = hs.ForceStop()
	close(release)
	time.Sleep(30 * time.Millisecond)
}

func TestACPArtifactProposalsStandardPlanExitAndCodexPlanReview(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	brainDir := t.TempDir()
	workdir := t.TempDir()
	acpDrv := acp.NewDriver("")

	var gatedPlan *threadv2.PlanReviewPayload
	acpSess, err := acpDrv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_acp_plan",
		Workdir:  workdir,
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "antigravity",
			Driver:     "acp",
			AcpFlavor:  "antigravity",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "acp"},
			Env: map[string]string{
				"HERMESH_MOCK_DRIVER": "acp",
				"MOCK_BRAIN_DIR":      brainDir,
			},
		},
		Gate: func(_ context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			if req.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW {
				gatedPlan = req.Plan
				return runtime.ToolGateResult{Allowed: true, PlanApproved: true}, nil
			}
			return runtime.ToolGateResult{Allowed: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("StartSession ACP: %v", err)
	}
	defer acpSess.Close()

	// 1. Antigravity non-blocking write_file + edit_file (sidecar fallback + out-of-workspace brainDir) + walkthrough.md + widget.html filter.
	if err := acpSess.StartTurn(ctx, runtime.TurnInput{RunID: "r_agy", Prompt: "antigravity_artifacts"}); err != nil {
		t.Fatal(err)
	}
	var agyPlans []runtime.Event
	timeout := time.After(8 * time.Second)
agyLoop:
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for antigravity_artifacts turn")
		case ev := <-acpSess.Events():
			if ev.Kind == runtime.EventPlanProposed {
				agyPlans = append(agyPlans, ev)
			}
			if ev.Kind == runtime.EventTurnCompleted {
				break agyLoop
			}
		}
	}
	if len(agyPlans) != 3 {
		t.Fatalf("expected 3 EventPlanProposed (write plan, edit plan, walkthrough; excluding .html), got %d: %+v", len(agyPlans), agyPlans)
	}
	if agyPlans[0].ItemID != agyPlans[1].ItemID || !agyPlans[1].RequestReview || agyPlans[1].PlanTitle != "Final Unified Plan" || !strings.Contains(agyPlans[1].Text, "3. UI") {
		t.Fatalf("expected edit_file to update same ItemID with full disk markdown & RequestReview=true, got %+v -> %+v", agyPlans[0], agyPlans[1])
	}
	if agyPlans[2].RequestReview || agyPlans[2].PlanTitle != "Walkthrough" || !strings.Contains(agyPlans[2].Text, "Verified end-to-end.") {
		t.Fatalf("expected walkthrough.md to emit RequestReview=false inline deliverable, got %+v", agyPlans[2])
	}

	// 2. Standard ACP blocking exit_plan_mode via session/request_permission -> Gate(PLAN_REVIEW).
	if err := acpSess.StartTurn(ctx, runtime.TurnInput{RunID: "r_exit", Prompt: "acp_exit_plan_mode"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := collectTurn(t, acpSess)
	if gatedPlan == nil || gatedPlan.Title != "Standard ACP Plan" || !strings.Contains(gatedPlan.Markdown, "Step B") {
		t.Fatalf("expected Gate(PLAN_REVIEW) with Standard ACP Plan, got %+v", gatedPlan)
	}
	if len(msgs) == 0 || msgs[0] != "plan_exit_opt=allow_plan" {
		t.Fatalf("expected allow_plan selected on plan approval, got %v", msgs)
	}

	// 3. Codex item/completed type="plan" -> emits EventPlanProposed with RequestReview=true.
	cxDrv := codex.NewDriver("")
	cxSess, err := cxDrv.StartSession(ctx, runtime.StartSpec{
		ThreadID: "th_cx_plan",
		Workdir:  t.TempDir(),
		Instance: &threadv2.ProviderInstanceConfig{
			InstanceId: "codex",
			Driver:     "codex",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "app-server"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "codex"},
		},
	})
	if err != nil {
		t.Fatalf("StartSession Codex: %v", err)
	}
	defer cxSess.Close()
	if err := cxSess.StartTurn(ctx, runtime.TurnInput{RunID: "r_cx_plan", Prompt: "propose_codex_plan"}); err != nil {
		t.Fatal(err)
	}
	var cxPlan *runtime.Event
	timeoutCx := time.After(8 * time.Second)
cxLoop:
	for {
		select {
		case <-timeoutCx:
			t.Fatal("timed out waiting for codex plan turn")
		case ev := <-cxSess.Events():
			if ev.Kind == runtime.EventPlanProposed {
				cp := ev
				cxPlan = &cp
			}
			if ev.Kind == runtime.EventTurnCompleted {
				break cxLoop
			}
		}
	}
	if cxPlan == nil || !cxPlan.RequestReview || !strings.Contains(cxPlan.Text, "# Codex Plan") {
		t.Fatalf("expected Codex plan item to set RequestReview=true, got %+v", cxPlan)
	}
}
