package runtime_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
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
			} else if sub == "stop_task" {
				if rec := os.Getenv("MOCK_CLAUDE_STOP_REC"); rec != "" {
					tid, _ := reqObj["task_id"].(string)
					_ = os.WriteFile(rec, []byte(tid), 0o644)
				}
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
			if strings.Contains(prompt, "think_then_reply") {
				for _, e := range []map[string]any{
					{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}},
					{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "weigh it"}},
					{"type": "content_block_stop", "index": 0},
					{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}},
					{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "done thinking"}},
					{"type": "content_block_stop", "index": 1},
				} {
					write(map[string]any{"type": "stream_event", "event": e})
				}
				write(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "done thinking"}}}})
				write(map[string]any{"type": "result", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
			} else if strings.Contains(prompt, "multistep_tools") {
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
			} else if strings.Contains(prompt, "bg_claude_task") {
				outFile := os.Getenv("MOCK_CLAUDE_BG_OUT")
				_ = os.WriteFile(outFile, []byte("step 1 ok\nall 99 specs green\n"), 0o644)
				write(map[string]any{
					"type":        "system",
					"subtype":     "task_started",
					"task_id":     "ctask_1",
					"tool_use_id": "tu_bg_1",
					"description": "Run full suite",
					"task_type":   "local_bash",
				})
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Running suite in background."}}},
				})
				write(map[string]any{
					"type":  "result",
					"usage": map[string]any{"input_tokens": 30, "output_tokens": 12},
				})
				go func() {
					time.Sleep(180 * time.Millisecond)
					write(map[string]any{
						"type":        "system",
						"subtype":     "task_notification",
						"task_id":     "ctask_1",
						"tool_use_id": "tu_bg_1",
						"status":      "completed",
						"summary":     "Suite passed",
						"output_file": outFile,
					})
					write(map[string]any{
						"type":    "assistant",
						"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Suite finished with 99 green specs!"}}},
					})
					write(map[string]any{
						"type":  "result",
						"usage": map[string]any{"input_tokens": 45, "output_tokens": 18},
					})
				}()
			} else if strings.Contains(prompt, "claude_deep_bg") {
				outFile := os.Getenv("MOCK_CLAUDE_BG_OUT")
				_ = os.WriteFile(outFile, []byte("booting worker...\n"), 0o644)
				// 1. Assistant launches Bash(run_in_background:true) + ScheduleWakeup:
				write(map[string]any{
					"type": "assistant",
					"message": map[string]any{"content": []any{
						map[string]any{
							"type":  "tool_use",
							"id":    "tu_async_bash",
							"name":  "Bash",
							"input": map[string]any{"command": "pytest -n auto", "description": "Parallel pytest", "run_in_background": true},
						},
						map[string]any{
							"type":  "tool_use",
							"id":    "tu_sched",
							"name":  "ScheduleWakeup",
							"input": map[string]any{"delaySeconds": 5, "description": "Check deploy health"},
						},
					}},
				})
				// 2. Immediate launch-ack tool_result MUST NOT complete tu_async_bash or tu_sched!
				write(map[string]any{
					"type": "user",
					"message": map[string]any{"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu_async_bash", "content": "Command running in background with ID: ctask_stop"},
						map[string]any{"type": "tool_result", "tool_use_id": "tu_sched", "content": "Scheduled wakeup in 5s"},
					}},
				})
				write(map[string]any{
					"type":        "system",
					"subtype":     "task_started",
					"task_id":     "ctask_stop",
					"tool_use_id": "tu_async_bash",
					"description": "Parallel pytest",
					"task_type":   "local_bash",
					"output_file": outFile,
				})
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Launched background pytest and timer."}}},
				})
				write(map[string]any{
					"type":  "result",
					"usage": map[string]any{"input_tokens": 40, "output_tokens": 14},
				})
				// Append live output after 120ms so startClaudeOutputTailer picks it up; then fire scheduled wakeup!
				go func() {
					time.Sleep(120 * time.Millisecond)
					_ = os.WriteFile(outFile, []byte("booting worker...\ncollecting 42 items...\n"), 0o644)
					time.Sleep(550 * time.Millisecond)
					write(map[string]any{
						"type":    "assistant",
						"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Timer fired: deploy healthy."}}},
					})
					write(map[string]any{
						"type":  "result",
						"usage": map[string]any{"input_tokens": 20, "output_tokens": 8},
					})
				}()
			} else if strings.Contains(prompt, "claude_todo_and_desc") {
				write(map[string]any{
					"type": "assistant",
					"message": map[string]any{"content": []any{
						map[string]any{
							"type": "tool_use",
							"id":   "tu_todo_1",
							"name": "TodoWrite",
							"input": map[string]any{
								"todos": []any{
									map[string]any{"content": "Inspect auth module", "status": "in_progress", "activeForm": "Inspecting auth module"},
									map[string]any{"content": "Run test suite", "status": "pending", "activeForm": "Running test suite"},
								},
							},
						},
						map[string]any{
							"type":  "tool_use",
							"id":    "tu_bash_desc",
							"name":  "Bash",
							"input": map[string]any{"command": "pytest -q", "description": "Execute unit tests"},
						},
					}},
				})
				write(map[string]any{
					"type": "user",
					"message": map[string]any{"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu_todo_1", "content": "Todos updated"},
						map[string]any{"type": "tool_result", "tool_use_id": "tu_bash_desc", "content": "12 passed"},
					}},
				})
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "All green."}}},
				})
				write(map[string]any{
					"type":  "result",
					"usage": map[string]any{"input_tokens": 30, "output_tokens": 10},
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
		case "thread/backgroundTerminals/list":
			if os.Getenv("MOCK_CODEX_RESUMED_BG") == "1" {
				write(map[string]any{
					"id": *m.ID,
					"result": map[string]any{
						"data": []any{
							map[string]any{"processId": "pty_resumed_1", "command": "pnpm dev --port 4000"},
						},
					},
				})
			} else {
				write(map[string]any{"id": *m.ID, "result": map[string]any{"data": []any{}}})
			}
		case "thread/backgroundTerminals/terminate":
			var p struct {
				ProcessID string `json:"processId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if rec := os.Getenv("MOCK_CODEX_TERM_REC"); rec != "" {
				_ = os.WriteFile(rec, []byte(p.ProcessID), 0o644)
			}
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "thread/fork":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"thread": map[string]any{"id": "cx_th_forked"}}})
		case "thread/unarchive", "thread/name/set":
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
			if len(p.Input) > 0 && strings.Contains(p.Input[0].Text, "think_then_reply") {
				write(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]any{"id": "it_rsn", "type": "reasoning"}}})
				write(map[string]any{"method": "item/reasoning/summaryTextDelta", "params": map[string]any{"itemId": "it_rsn", "delta": "weigh it"}})
				write(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"id": "it_rsn", "type": "reasoning"}}})
				write(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"itemId": "it_msg", "delta": "done thinking"}})
				write(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"id": "it_msg", "type": "agentMessage", "text": "done thinking"}}})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"status": "completed"}}})
				continue
			}
			if len(p.Input) > 0 && strings.Contains(p.Input[0].Text, "codex_plan_updated") {
				write(map[string]any{
					"method": "turn/plan/updated",
					"params": map[string]any{
						"threadId":    "cx_th_99",
						"turnId":      "turn_1",
						"explanation": "Rewriting sidebar halo without dynamic masks",
						"plan": []any{
							map[string]any{"step": "Check motion constraints", "status": "completed"},
							map[string]any{"step": "Rewrite CALayer halo", "status": "inProgress"},
							map[string]any{"step": "Run GPU benchmark", "status": "pending"},
						},
					},
				})
				write(map[string]any{
					"method": "item/completed",
					"params": map[string]any{
						"item": map[string]any{"id": "msg_plan_up", "type": "agentMessage", "text": "Updated plan."},
					},
				})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"status": "completed"}}})
				continue
			}
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
			if len(p.Input) > 0 && strings.Contains(p.Input[0].Text, "codex_bg_and_collab") {
				// 1. Spawn a background dev server commandExecution whose processId arrives via terminalInteraction:
				write(map[string]any{
					"method": "item/started",
					"params": map[string]any{
						"item": map[string]any{
							"id":      "cmd_dev_1",
							"type":    "commandExecution",
							"command": "npm run dev",
						},
					},
				})
				write(map[string]any{
					"method": "item/commandExecution/terminalInteraction",
					"params": map[string]any{"itemId": "cmd_dev_1", "processId": "pty_real_77"},
				})
				write(map[string]any{
					"method": "item/commandExecution/outputDelta",
					"params": map[string]any{"itemId": "cmd_dev_1", "delta": "listening on :5173\n"},
				})
				// 2. Spawn + wait a collab subagent:
				write(map[string]any{
					"method": "item/completed",
					"params": map[string]any{
						"item": map[string]any{
							"id":                "col_spawn_1",
							"type":              "collabAgentToolCall",
							"tool":              "spawn_agent",
							"prompt":            "Audit auth module",
							"receiverThreadIds": []string{"sub_cx_1"},
						},
					},
				})
				write(map[string]any{
					"method": "item/completed",
					"params": map[string]any{
						"item": map[string]any{
							"id":                "col_wait_1",
							"type":              "collabAgentToolCall",
							"tool":              "wait",
							"receiverThreadIds": []string{"sub_cx_1"},
						},
					},
				})
				write(map[string]any{
					"method": "item/completed",
					"params": map[string]any{
						"item": map[string]any{"id": "msg_bg", "type": "agentMessage", "text": "Dev server left running and subagent finished."},
					},
				})
				// Notice cmd_dev_1 is NOT completed before turn/completed -> promoted to DAEMON!
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
	var busyOnceDone bool
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
			if rec := os.Getenv("MOCK_ACP_INIT_REC"); rec != "" {
				_ = os.WriteFile(rec, m.Params, 0o644)
			}
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
			} else if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "acp_bg_wakeup") {
				// Antigravity-style background run_command (CommandLine + IsDaemon:false) + daemon (IsDaemon:true):
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_daemon",
							"title":         "run_command",
							"kind":          "execute",
							"status":        "in_progress",
							"rawInput": map[string]any{
								"CommandLine":       "npm run dev",
								"IsDaemon":          true,
								"WaitMsBeforeAsync": 500,
								"toolSummary":       "Dev server startup",
							},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_e2e",
							"title":         "run_command",
							"kind":          "execute",
							"status":        "in_progress",
							"rawInput": map[string]any{
								"CommandLine":       "make e2e",
								"IsDaemon":          false,
								"WaitMsBeforeAsync": 500,
								"toolSummary":       "Run E2E suite",
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
							"content":       map[string]any{"type": "text", "text": "E2E running in background, I will report back."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      promptReqID,
					"result":  map[string]any{"stopReason": "end_turn"},
				})
				// Out-of-turn wakeup after PostPromptMergeWindow: completes tc_e2e, streams summary, and emits usage_update!
				go func() {
					time.Sleep(250 * time.Millisecond)
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call_update",
								"toolCallId":    "tc_e2e",
								"status":        "completed",
								"rawOutput":     map[string]any{"exitCode": 0, "stdout": "PASS e2e (14s)"},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "Wakeup report: all E2E tests passed!"},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "usage_update",
								"used":          1200,
								"size":          200000,
							},
						},
					})
				}()
			} else if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "acp_busy_then_utf8_and_copilot") {
				if !busyOnceDone {
					busyOnceDone = true
					write(map[string]any{
						"jsonrpc": "2.0",
						"id":      promptReqID,
						"error":   map[string]any{"code": -32000, "message": "Agent is already processing a prompt"},
					})
					continue
				}
				// 1. Agent-side embedded terminal streaming split 4-byte UTF-8 emoji 🎉 (\xf0\x9f\x8e\x89) across 2 base64 chunks:
				rawLine := []byte("build 🎉 ok\n")
				splitIdx := len("build ") + 2 // right in the middle of 4-byte 🎉!
				c1 := base64.StdEncoding.EncodeToString(rawLine[:splitIdx])
				c2 := base64.StdEncoding.EncodeToString(rawLine[splitIdx:])
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "terminal_update",
							"terminalId":    "aterm_1",
							"command":       "cargo build",
							"status":        "running",
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "terminal_output_chunk",
							"terminalId":    "aterm_1",
							"data":          c1,
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "terminal_output_chunk",
							"terminalId":    "aterm_1",
							"data":          c2,
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "terminal_update",
							"terminalId":    "aterm_1",
							"status":        "completed",
							"exitCode":      0,
						},
					},
				})
				// 2. OpenCode/Claude-ACP style run_in_background:true tool that immediately reports status:"completed" on launch:
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_async_launch",
							"title":         "Bash",
							"kind":          "execute",
							"status":        "completed",
							"rawInput":      map[string]any{"command": "pnpm test:ci", "run_in_background": true},
						},
					},
				})
				// 3. GitHub Copilot detached subagent started before end_turn:
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "github.com/copilot/sessionEvent",
					"params": map[string]any{
						"type":    "subagent.started",
						"agentId": "cp_agent_9",
						"title":   "Security Reviewer",
						"data":    map[string]any{"toolCallId": "tc_cp_sub"},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "Started CI and Security Reviewer."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      promptReqID,
					"result":  map[string]any{"stopReason": "end_turn"},
				})
				// Out-of-turn completion: Copilot subagent completes + wake summary + assistant.idle settles both!
				go func() {
					time.Sleep(220 * time.Millisecond)
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "github.com/copilot/sessionEvent",
						"params": map[string]any{
							"type":    "subagent.completed",
							"agentId": "cp_agent_9",
							"data":    map[string]any{"toolCallId": "tc_cp_sub", "summary": "0 vulnerabilities found"},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "CI and Security Reviewer both succeeded!"},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "github.com/copilot/sessionEvent",
						"params":  map[string]any{"type": "assistant.idle"},
					})
				}()
			} else if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "agy_auto_bg_chain") {
				sysDir := filepath.Join(os.Getenv("MOCK_AGY_BRAIN_DIR"), ".system_generated")
				tasksDir := filepath.Join(sysDir, "tasks")
				msgsDir := filepath.Join(sysDir, "messages")
				_ = os.MkdirAll(tasksDir, 0o755)
				log10 := filepath.Join(tasksDir, "task-10.log")
				log11 := filepath.Join(tasksDir, "task-11.log")
				_ = os.WriteFile(log10, []byte("=== RUN   TestRaceSuite\n"), 0o644)
				toFileURI := func(path string) string {
					sl := filepath.ToSlash(path)
					if strings.HasPrefix(sl, "/") {
						return "file://" + sl
					}
					return "file:///" + sl
				}

				// 1. Synchronous command with IsDaemon:true that exits immediately (must NOT become a BackgroundTask):
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_sync_exit",
							"title":         "run_command",
							"kind":          "execute",
							"status":        "completed",
							"rawInput": map[string]any{
								"CommandLine": "echo instant",
								"IsDaemon":    true,
								"toolSummary": "Instant check",
							},
							"content": []any{
								map[string]any{
									"type": "content",
									"content": map[string]any{
										"type": "text",
										"text": "Created At: 2026-11-26T15:00:00Z\nCompleted At: 2026-11-26T15:00:00Z\nCommand exited with code 0.\n\nCommand output:\ninstant\n",
									},
								},
							},
						},
					},
				})

				// 2. Antigravity run_command auto-backgrounded after WaitMsBeforeAsync (no run_in_background flag):
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_agy_1",
							"title":         "run_command",
							"kind":          "execute",
							"status":        "in_progress",
							"rawInput": map[string]any{
								"CommandLine":                "go test -race ./...",
								"WaitMsBeforeAsync":          500,
								"NotificationTimeoutSeconds": 60,
								"toolSummary":                "Run race suite",
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
							"toolCallId":    "tc_agy_1",
							"status":        "completed",
							"content": []any{
								map[string]any{
									"type": "content",
									"content": map[string]any{
										"type": "text",
										"text": "Tool is running as a background task with task id: conv_99/task-10\nTask logs are available at: " + toFileURI(log10) + "\n\nLog output:\n=== RUN   TestRaceSuite\n\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: wait or continue.",
									},
								},
							},
						},
					},
				})

				// 3. Daemon + Subagent launched in Turn #1 (both immediately acked with status:"completed"):
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_agy_daemon",
							"title":         "run_command",
							"kind":          "execute",
							"status":        "completed",
							"rawInput": map[string]any{
								"CommandLine": "pnpm dev",
								"IsDaemon":    true,
								"toolSummary": "Dev watch server",
							},
							"content": []any{
								map[string]any{
									"type": "content",
									"content": map[string]any{
										"type": "text",
										"text": "Tool is running as a background task with task id: conv_99/task-daemon\n\nLog output:\nlistening on :3000\n\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: ...",
									},
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
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_agy_sub",
							"title":         "invoke_subagent",
							"kind":          "other",
							"status":        "completed",
							"rawInput": map[string]any{
								"TypeName": "research",
								"Role":     "Log Analyzer",
								"Prompt":   "Inspect flaky test logs",
							},
							"content": []any{
								map[string]any{
									"type":    "content",
									"content": map[string]any{"type": "text", "text": `{"conversationId": "sub_conv_77"}`},
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
							"content":       map[string]any{"type": "text", "text": "Tests running in background."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      promptReqID,
					"result":  map[string]any{"stopReason": "end_turn"},
				})

				// Background progression orchestrated via gate files in MOCK_AGY_BRAIN_DIR so there are zero flaky sleeps:
				go func() {
					waitGate := func(name string) bool {
						p := filepath.Join(os.Getenv("MOCK_AGY_BRAIN_DIR"), name)
						for i := 0; i < 400; i++ {
							if _, err := os.Stat(p); err == nil {
								return true
							}
							time.Sleep(15 * time.Millisecond)
						}
						return false
					}
					if !waitGate("step_timeout_wake") {
						return
					}
					_ = os.MkdirAll(msgsDir, 0o755)
					_ = os.WriteFile(filepath.Join(msgsDir, "m0_still_running.json"), []byte(`{"sender":"conv_99/task-10","content":"Task id \"conv_99/task-10\" is still running after 60s."}`), 0o644)
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "Still waiting on race suite..."},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{"sessionUpdate": "usage_update", "used": 900, "size": 200000},
						},
					})

					if !waitGate("step_fail_and_chain") {
						return
					}
					_ = os.WriteFile(log10, []byte("=== RUN   TestRaceSuite\n--- FAIL: TestRaceSuite (1.2s)\n失败原因: 竞态冲突\n"), 0o644)
					_ = os.WriteFile(filepath.Join(msgsDir, "m1_done.json"), []byte(`{"sender":"conv_99/task-10","content":"Task id \"conv_99/task-10\" finished with result:\nThe tool's output was truncated due to being too long.\n\nHere is a preview:\nCommand exited with code 1.\n"}`), 0o644)
					_ = os.WriteFile(log11, []byte("=== RUN   TestRaceSuiteFixed\n"), 0o644)

					// Agent kills daemon + subagent and launches chained task-11 in Wakeup #1:
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_kill_daemon",
								"title":         "manage_task",
								"kind":          "other",
								"status":        "completed",
								"rawInput":      map[string]any{"Action": "kill", "TaskId": "conv_99/task-daemon"},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_kill_sub",
								"title":         "manage_subagents",
								"kind":          "other",
								"status":        "completed",
								"rawInput":      map[string]any{"Action": "kill", "ConversationIds": []string{"sub_conv_77"}},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_agy_2",
								"title":         "run_command",
								"kind":          "execute",
								"status":        "completed",
								"rawInput": map[string]any{
									"CommandLine":       "go test -race ./internal/runtime/...",
									"WaitMsBeforeAsync": 500,
									"toolSummary":       "Re-run fixed race suite",
								},
								"content": []any{
									map[string]any{
										"type": "content",
										"content": map[string]any{
											"type": "text",
											"text": "Tool is running as a background task with task id: conv_99/task-11\nTask logs are available at: " + toFileURI(log11) + "\n\nLog output:\n=== RUN   TestRaceSuiteFixed\n\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: ...",
										},
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
								"content":       map[string]any{"type": "text", "text": "Fixed race and launched re-run."},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{"sessionUpdate": "usage_update", "used": 1400, "size": 200000},
						},
					})

					if !waitGate("step_finish_chain") {
						return
					}
					_ = os.WriteFile(log11, []byte("=== RUN   TestRaceSuiteFixed\n--- PASS: TestRaceSuiteFixed (0.4s)\nPASS\n"), 0o644)
					_ = os.WriteFile(filepath.Join(msgsDir, "m2_done.json"), []byte(`{"sender":"conv_99/task-11","content":"Task id \"conv_99/task-11\" finished with result:\nCommand exited with code 0.\n"}`), 0o644)
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "All race tests passed!"},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{"sessionUpdate": "usage_update", "used": 1600, "size": 200000},
						},
					})
				}()
			} else if len(p.Prompt) > 0 && strings.Contains(p.Prompt[0].Text, "agy_schedule_lifecycle") {
				sysDir := filepath.Join(os.Getenv("MOCK_AGY_BRAIN_DIR"), ".system_generated")
				tasksDir := filepath.Join(sysDir, "tasks")
				msgsDir := filepath.Join(sysDir, "messages")
				_ = os.MkdirAll(tasksDir, 0o755)
				_ = os.MkdirAll(msgsDir, 0o755)
				// Pre-existing message before timers are created must NOT falsely trigger TimerCondition:"any".
				_ = os.WriteFile(filepath.Join(msgsDir, "m_preexisting.json"), []byte(`{"sender":"conv_sched/task-old","content":"old wakeup"}`), 0o644)
				toFileURI := func(name string) string {
					full := filepath.Join(tasksDir, name)
					_ = os.WriteFile(full, nil, 0o644)
					sl := filepath.ToSlash(full)
					if strings.HasPrefix(sl, "/") {
						return "file://" + sl
					}
					return "file:///" + sl
				}
				emitBgLaunch := func(id, title, kind string, rawIn map[string]any, taskID, logFile string) {
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    id,
								"title":         title,
								"kind":          kind,
								"status":        "completed",
								"rawInput":      rawIn,
								"content": []any{
									map[string]any{
										"type": "content",
										"content": map[string]any{
											"type": "text",
											"text": "Tool is running as a background task with task id: " + taskID + "\nTask logs are available at: " + toFileURI(logFile) + "\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: wait or continue.",
										},
									},
								},
							},
						},
					})
				}

				emitBgLaunch("tc_sched_oneshot", "schedule", "other", map[string]any{
					"DurationSeconds": 120,
					"Prompt":          "Check task-361 status if XCUITest has not completed yet.",
				}, "conv_sched/task-367", "task-367.log")
				emitBgLaunch("tc_build", "run_command", "execute", map[string]any{
					"CommandLine":       "xcodebuild test",
					"WaitMsBeforeAsync": 500,
					"toolSummary":       "Run iOS UI tests",
				}, "conv_sched/task-build", "task-build.log")
				emitBgLaunch("tc_sched_cond", "schedule", "other", map[string]any{
					"DurationSeconds": 120,
					"TimerCondition":  "conv_sched/task-build",
					"Prompt":          "Check build if still running.",
				}, "conv_sched/task-cond", "task-cond.log")
				emitBgLaunch("tc_sched_any", "schedule", "other", map[string]any{
					"DurationSeconds": 180,
					"TimerCondition":  "any",
					"Prompt":          "Wake on any message.",
				}, "conv_sched/task-any", "task-any.log")
				emitBgLaunch("tc_canceled_cmd", "run_command", "execute", map[string]any{
					"CommandLine":       "sleep 300",
					"WaitMsBeforeAsync": 500,
					"toolSummary":       "Canceled check",
				}, "conv_sched/task-canceled", "task-canceled.log")
				emitBgLaunch("tc_cron_bounded", "schedule", "other", map[string]any{
					"CronExpression": "*/5 * * * *",
					"MaxIterations":  2,
					"Prompt":         "Poll CI status.",
				}, "conv_sched/task-cron", "task-cron.log")
				emitBgLaunch("tc_already_done", "run_command", "execute", map[string]any{
					"CommandLine":       "echo stale",
					"WaitMsBeforeAsync": 500,
					"toolSummary":       "Stale task",
				}, "conv_sched/task-stale", "task-stale.log")

				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "Timers and tasks armed."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      promptReqID,
					"result":  map[string]any{"stopReason": "end_turn"},
				})

				go func() {
					waitGate := func(name string) bool {
						gp := filepath.Join(os.Getenv("MOCK_AGY_BRAIN_DIR"), name)
						for i := 0; i < 400; i++ {
							if _, err := os.Stat(gp); err == nil {
								return true
							}
							time.Sleep(15 * time.Millisecond)
						}
						return false
					}
					if !waitGate("sched_step_final_kill") {
						return
					}
					_ = os.WriteFile(filepath.Join(msgsDir, "m4_cron_tick2.json"), []byte(`{"sender":"conv_sched/task-cron","content":"Poll CI status (2/2)."}`), 0o644)
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_kill_already_done",
								"title":         "manage_task",
								"kind":          "other",
								"status":        "failed",
								"rawInput":      map[string]any{"Action": "kill", "TaskId": "conv_sched/task-stale"},
								"content": []any{
									map[string]any{
										"type": "content",
										"content": map[string]any{
											"type": "text",
											"text": `Encountered error in tool execution: cannot kill task "conv_sched/task-stale": task is not running (status: DONE)`,
										},
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
								"content":       map[string]any{"type": "text", "text": "All background timers and tasks settled."},
							},
						},
					})
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params":  map[string]any{"update": map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 200000}},
					})
				}()
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
		SessionID: "s_cl",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "claude",
			Driver:     "claude",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "claude"},
		},
		Selection: &sessionv1.ModelSelection{InstanceId: "claude", Model: "claude-sonnet-4-6"},
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

	if err := sess.SetSelection(ctx, &sessionv1.ModelSelection{InstanceId: "claude", Model: "claude-opus-4-6"}); err != nil {
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

// Claude and Codex say when the model starts and stops thinking, before it replies.
func TestClaudeAndCodexMarkWhereThinkingEnds(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	for _, c := range []struct {
		drv  runtime.Driver
		name string
		args []string
	}{
		{claude.NewDriver(""), "claude", []string{"-test.run=TestHelperMockCLI", "--"}},
		{codex.NewDriver(""), "codex", []string{"-test.run=TestHelperMockCLI", "--", "app-server"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sess, err := c.drv.StartSession(ctx, runtime.StartSpec{
				SessionID: "s_" + c.name,
				Workdir:   t.TempDir(),
				Instance: &sessionv1.ProviderInstanceConfig{
					InstanceId: c.name,
					Driver:     c.name,
					BinaryPath: exe,
					LaunchArgs: c.args,
					Env:        map[string]string{"HERMESH_MOCK_DRIVER": c.name},
				},
				Selection: &sessionv1.ModelSelection{InstanceId: c.name, Model: "m"},
			})
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			defer sess.Close()
			if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r1", Prompt: "think_then_reply"}); err != nil {
				t.Fatal(err)
			}
			var got []string
			timeout := time.After(8 * time.Second)
			for done := false; !done; {
				select {
				case <-timeout:
					t.Fatalf("timed out; saw %v", got)
				case ev, ok := <-sess.Events():
					if !ok {
						t.Fatalf("session closed; saw %v", got)
					}
					switch ev.Kind {
					case runtime.EventReasoningDelta:
						got = append(got, "think "+ev.ItemID+" "+ev.Text)
					case runtime.EventReasoningCompleted:
						got = append(got, "thought "+ev.ItemID)
					case runtime.EventAssistantDelta:
						got = append(got, "reply "+ev.Text)
					case runtime.EventTurnCompleted, runtime.EventTurnFailed:
						done = true
					}
				}
			}
			rsn := strings.Fields(got[0])[1]
			want := []string{"think " + rsn + " ", "think " + rsn + " weigh it", "thought " + rsn, "reply done thinking"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("events = %q, want %q", got, want)
			}
		})
	}
}

func TestCodexDriverSubprocessProtocol(t *testing.T) {
	ctx := context.Background()
	exe, _ := os.Executable()
	drv := codex.NewDriver("")

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		SessionID: "s_cx",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "codex",
			Driver:     "codex",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "app-server"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "codex"},
		},
		Selection: &sessionv1.ModelSelection{
			InstanceId: "codex",
			Model:      "gpt-5.4",
			Options: map[string]*sessionv1.OptionValue{
				"reasoningEffort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "xhigh"}},
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
		Selection: &sessionv1.ModelSelection{
			InstanceId: "codex",
			Model:      "gpt-5.4",
			Options: map[string]*sessionv1.OptionValue{
				"reasoningEffort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "xhigh"}},
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
		SessionID: "s_acp",
		Workdir:   workdir,
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "gemini",
			Driver:     "acp",
			AcpFlavor:  "gemini",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "--acp"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "acp"},
		},
		Selection: &sessionv1.ModelSelection{InstanceId: "gemini", Model: "gemini-3.1-pro"},
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
		SessionID: "s_acp_2",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "antigravity",
			Driver:     "acp",
			AcpFlavor:  "antigravity",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "acp"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "acp"},
		},
		Gate: func(_ context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL {
				return runtime.ToolGateResult{Allowed: false}, nil
			}
			if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
				return runtime.ToolGateResult{
					Allowed: true,
					Answers: map[string]*sessionv1.QuestionAnswer{
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
		SessionID: "s_cl_multi",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
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
		SessionID:    "s_cx_fork",
		Workdir:      t.TempDir(),
		ResumeCursor: "locked_cursor",
		Instance: &sessionv1.ProviderInstanceConfig{
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

	var gatedPlan *sessionv1.PlanReviewPayload
	acpSess, err := acpDrv.StartSession(ctx, runtime.StartSpec{
		SessionID: "s_acp_plan",
		Workdir:   workdir,
		Instance: &sessionv1.ProviderInstanceConfig{
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
			if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW {
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

	cxDrv := codex.NewDriver("")
	cxSess, err := cxDrv.StartSession(ctx, runtime.StartSpec{
		SessionID: "s_cx_plan",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
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

func TestACPAndClaudeBackgroundTasksAndOutOfTurnWakeups(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// 1. ACP Antigravity-style background run_command + daemon + post-prompt wakeup settled by usage_update + debounce:
	acpDrv := acp.NewDriver("")
	acpDrv.PostPromptMergeWindow = 80 * time.Millisecond
	acpDrv.OutOfTurnDebounce = 80 * time.Millisecond
	acpSess, err := acpDrv.StartSession(ctx, runtime.StartSpec{
		SessionID: "th_acp_bg",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "antigravity",
			Driver:     "acp",
			AcpFlavor:  "antigravity",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "acp"},
			Env:        map[string]string{"HERMESH_MOCK_DRIVER": "acp"},
		},
	})
	if err != nil {
		t.Fatalf("StartSession ACP: %v", err)
	}
	defer acpSess.Close()

	// Pass a per-turn ctx that we cancel right after Turn #1 completes (simulating driveRun returning)!
	turn1Ctx, cancelTurn1 := context.WithCancel(ctx)
	if err := acpSess.StartTurn(turn1Ctx, runtime.TurnInput{RunID: "r1", Prompt: "acp_bg_wakeup"}); err != nil {
		t.Fatal(err)
	}

	var turn1Bg []*runtime.Event
	timeout := time.After(8 * time.Second)
turn1Loop:
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for ACP Turn #1 completion")
		case ev := <-acpSess.Events():
			if ev.Kind == runtime.EventBackgroundTask {
				cp := ev
				turn1Bg = append(turn1Bg, &cp)
			}
			if ev.Kind == runtime.EventTurnCompleted {
				break turn1Loop
			}
		}
	}
	cancelTurn1()

	var sawDaemon, sawAwaitedCmd bool
	for _, b := range turn1Bg {
		if b.TaskID == "tc_daemon" && b.BackgroundKind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON && !b.BackgroundAwaited {
			sawDaemon = true
		}
		if b.TaskID == "tc_e2e" && b.BackgroundKind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND && b.BackgroundAwaited && b.Command == "make e2e" {
			sawAwaitedCmd = true
		}
	}
	if !sawDaemon || !sawAwaitedCmd {
		t.Fatalf("expected both daemon (IsDaemon=true) and awaited command (CommandLine='make e2e'), got %+v", turn1Bg)
	}

	// Now read the out-of-turn wakeup events until the synthesized EventTurnCompleted fires!
	var wakeMsg string
	var e2eSettled bool
wakeLoop:
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for ACP out-of-turn wakeup settlement")
		case ev := <-acpSess.Events():
			if ev.Kind == runtime.EventBackgroundTask && ev.TaskID == "tc_e2e" && ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED {
				e2eSettled = true
			}
			if ev.Kind == runtime.EventAssistantMessage || ev.Kind == runtime.EventAssistantDelta {
				wakeMsg += ev.Text
			}
			if ev.Kind == runtime.EventTurnCompleted {
				break wakeLoop
			}
		}
	}
	if !e2eSettled || !strings.Contains(wakeMsg, "Wakeup report: all E2E tests passed!") {
		t.Fatalf("expected ACP wakeup completion & report, e2eSettled=%v wakeMsg=%q", e2eSettled, wakeMsg)
	}

	// 2. Claude Code system/task_started + system/task_notification (reading output_file tail) + continuation result:
	bgOutPath := filepath.Join(t.TempDir(), "claude_bg.out")
	clDrv := claude.NewDriver("")
	clSess, err := clDrv.StartSession(ctx, runtime.StartSpec{
		SessionID: "th_cl_bg",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "claude",
			Driver:     "claude",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--"},
			Env: map[string]string{
				"HERMESH_MOCK_DRIVER": "claude",
				"MOCK_CLAUDE_BG_OUT":  bgOutPath,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession Claude: %v", err)
	}
	defer clSess.Close()

	if err := clSess.StartTurn(ctx, runtime.TurnInput{RunID: "rc1", Prompt: "bg_claude_task"}); err != nil {
		t.Fatal(err)
	}
	// Drain Turn #1 + Turn #2 (task_notification continuation)
	var claudeTurns int
	var claudeBgCompleted *runtime.Event
	var claudeWakeText string
	timeoutCl := time.After(8 * time.Second)
	for claudeTurns < 2 {
		select {
		case <-timeoutCl:
			t.Fatalf("timed out waiting for Claude background continuation (turns=%d)", claudeTurns)
		case ev := <-clSess.Events():
			if ev.Kind == runtime.EventBackgroundTask && ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED {
				cp := ev
				claudeBgCompleted = &cp
			}
			if claudeTurns == 1 && ev.Kind == runtime.EventAssistantMessage {
				claudeWakeText = ev.Text
			}
			if ev.Kind == runtime.EventTurnCompleted {
				claudeTurns++
			}
		}
	}
	if claudeBgCompleted == nil || !strings.Contains(claudeBgCompleted.Output, "all 99 specs green") {
		t.Fatalf("expected Claude task_notification to attach output_file tail, got %+v", claudeBgCompleted)
	}
	if !strings.Contains(claudeWakeText, "Suite finished with 99 green specs!") {
		t.Fatalf("expected Claude continuation assistant message, got %q", claudeWakeText)
	}
}

func TestCrossProviderDeepBackgroundAdaptations(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Claude Code: async launch-ack suppression + live output_file tailer + ScheduleWakeup + single-task stop_task:
	clOutFile := filepath.Join(t.TempDir(), "cl_live.out")
	clStopRec := filepath.Join(t.TempDir(), "cl_stop.rec")
	clSess, err := claude.NewDriver("").StartSession(ctx, runtime.StartSpec{
		SessionID: "th_cl_deep",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "claude",
			Driver:     "claude",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--"},
			Env: map[string]string{
				"HERMESH_MOCK_DRIVER":  "claude",
				"MOCK_CLAUDE_BG_OUT":   clOutFile,
				"MOCK_CLAUDE_STOP_REC": clStopRec,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession Claude deep: %v", err)
	}
	defer clSess.Close()

	if err := clSess.StartTurn(ctx, runtime.TurnInput{RunID: "rc_deep", Prompt: "claude_deep_bg"}); err != nil {
		t.Fatal(err)
	}
	var prematureToolDone []string
	var sawLiveTail, sawSchedRunning, sawSchedSettled bool
	clTurns := 0
	timeoutCl := time.After(8 * time.Second)
	for clTurns < 2 {
		select {
		case <-timeoutCl:
			t.Fatalf("timed out waiting for Claude deep background test (turns=%d)", clTurns)
		case ev := <-clSess.Events():
			if clTurns == 0 && ev.Kind == runtime.EventToolCompleted {
				prematureToolDone = append(prematureToolDone, ev.ItemID)
			}
			if ev.Kind == runtime.EventBackgroundTask {
				if ev.TaskID == "ctask_stop" && strings.Contains(ev.Output, "collecting 42 items...") {
					sawLiveTail = true
				}
				if ev.BackgroundKind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE {
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
						sawSchedRunning = true
					}
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED {
						sawSchedSettled = true
					}
				}
			}
			if ev.Kind == runtime.EventTurnCompleted {
				clTurns++
			}
		}
	}
	if len(prematureToolDone) > 0 {
		t.Fatalf("async launch-ack tool_result must NOT emit premature EventToolCompleted in Turn #1, got %v", prematureToolDone)
	}
	if !sawLiveTail {
		t.Fatalf("expected live output_file tailer to stream 'collecting 42 items...' before task_notification")
	}
	if !sawSchedRunning || !sawSchedSettled {
		t.Fatalf("expected ScheduleWakeup task to transition RUNNING -> COMPLETED on wake, got running=%v settled=%v", sawSchedRunning, sawSchedSettled)
	}
	if err := clSess.(runtime.BackgroundController).StopBackgroundTask(ctx, "ctask_stop"); err != nil {
		t.Fatalf("Claude StopBackgroundTask: %v", err)
	}
	var sawClaudeStopped bool
	stopWait := time.After(3 * time.Second)
clStopLoop:
	for {
		select {
		case <-stopWait:
			t.Fatal("timed out waiting for Claude STOPPED event")
		case ev := <-clSess.Events():
			if ev.Kind == runtime.EventBackgroundTask && ev.TaskID == "ctask_stop" && ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
				sawClaudeStopped = true
				break clStopLoop
			}
		}
	}
	// Wait briefly for mock subprocess to write MOCK_CLAUDE_STOP_REC.
	deadline := time.Now().Add(2 * time.Second)
	var stopTaskWire string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(clStopRec); err == nil && len(b) > 0 {
			stopTaskWire = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawClaudeStopped || stopTaskWire != "ctask_stop" {
		t.Fatalf("expected control_request subtype=stop_task with task_id=ctask_stop, got wire=%q stopped=%v", stopTaskWire, sawClaudeStopped)
	}

	// 2. Codex app-server: resume backgroundTerminals/list + DAEMON promotion + real processId terminate + collab subagents:
	cxTermRec := filepath.Join(t.TempDir(), "cx_term.rec")
	cxSess, err := codex.NewDriver("").StartSession(ctx, runtime.StartSpec{
		SessionID:    "th_cx_deep",
		Workdir:      t.TempDir(),
		ResumeCursor: "cx_existing_thread",
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "codex",
			Driver:     "codex",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "app-server"},
			Env: map[string]string{
				"HERMESH_MOCK_DRIVER":   "codex",
				"MOCK_CODEX_RESUMED_BG": "1",
				"MOCK_CODEX_TERM_REC":   cxTermRec,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession Codex deep: %v", err)
	}
	defer cxSess.Close()

	if err := cxSess.StartTurn(ctx, runtime.TurnInput{RunID: "rcx_1", Prompt: "codex_bg_and_collab"}); err != nil {
		t.Fatal(err)
	}
	var sawResumedPty, sawPromotedDaemon, sawSubRunning, sawSubCompleted bool
	timeoutCx := time.After(8 * time.Second)
cxDeepLoop:
	for {
		select {
		case <-timeoutCx:
			t.Fatal("timed out waiting for Codex deep background turn")
		case ev := <-cxSess.Events():
			if ev.Kind == runtime.EventBackgroundTask {
				if ev.TaskID == "pty_resumed_1" && ev.BackgroundKind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON {
					sawResumedPty = true
				}
				if ev.TaskID == "cmd_dev_1" && ev.BackgroundKind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON && !ev.BackgroundAwaited && strings.Contains(ev.Output, ":5173") {
					sawPromotedDaemon = true
				}
				if ev.TaskID == "sub_cx_1" && ev.BackgroundKind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT {
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
						sawSubRunning = true
					}
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED {
						sawSubCompleted = true
					}
				}
			}
			if ev.Kind == runtime.EventTurnCompleted {
				break cxDeepLoop
			}
		}
	}
	if !sawResumedPty || !sawPromotedDaemon || !sawSubRunning || !sawSubCompleted {
		t.Fatalf("Codex deep mismatch: resumedPty=%v promotedDaemon=%v subRun=%v subDone=%v", sawResumedPty, sawPromotedDaemon, sawSubRunning, sawSubCompleted)
	}
	if err := cxSess.(runtime.BackgroundController).StopBackgroundTask(ctx, "cmd_dev_1"); err != nil {
		t.Fatalf("Codex StopBackgroundTask: %v", err)
	}
	terminatedPID, _ := os.ReadFile(cxTermRec)
	if string(terminatedPID) != "pty_real_77" {
		t.Fatalf("expected thread/backgroundTerminals/terminate to receive real processId 'pty_real_77' (not itemId 'cmd_dev_1'), got %q", string(terminatedPID))
	}

	// 3. ACP: Copilot _meta capabilities + busy prompt retry + split-UTF8 terminal_output_chunk + run_in_background launch ack:
	acpInitRec := filepath.Join(t.TempDir(), "acp_init.json")
	acpDrv := acp.NewDriver("")
	acpDrv.BusyPromptRetryDelay = 50 * time.Millisecond
	acpDrv.PostPromptMergeWindow = 80 * time.Millisecond
	acpDrv.OutOfTurnDebounce = 80 * time.Millisecond
	acpDrv.OutOfTurnToolWait = 150 * time.Millisecond
	cpSess, err := acpDrv.StartSession(ctx, runtime.StartSpec{
		SessionID: "th_acp_deep",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "copilot",
			Driver:     "acp",
			AcpFlavor:  "copilot",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "--acp"},
			Env: map[string]string{
				"HERMESH_MOCK_DRIVER": "acp",
				"MOCK_ACP_INIT_REC":   acpInitRec,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession ACP Copilot deep: %v", err)
	}
	defer cpSess.Close()

	initRaw, _ := os.ReadFile(acpInitRec)
	if !strings.Contains(string(initRaw), "github.com/copilot") || !strings.Contains(string(initRaw), "subagent.completed") {
		t.Fatalf("expected Copilot initialize clientCapabilities._meta subscription, got %s", string(initRaw))
	}

	if err := cpSess.StartTurn(ctx, runtime.TurnInput{RunID: "racp_deep", Prompt: "acp_busy_then_utf8_and_copilot"}); err != nil {
		t.Fatal(err)
	}
	var utf8TermOut string
	var asyncPrematureDone bool
	var sawAsyncRunning, sawAsyncSettled, sawCopilotSubRunning, sawCopilotSubDone bool
	acpTurns := 0
	timeoutACP := time.After(8 * time.Second)
	for acpTurns < 2 {
		select {
		case <-timeoutACP:
			t.Fatalf("timed out waiting for ACP deep background turns (turns=%d)", acpTurns)
		case ev := <-cpSess.Events():
			if ev.Kind == runtime.EventTurnFailed {
				t.Fatalf("ACP turn failed instead of retrying busy prompt: %s", ev.ErrorMessage)
			}
			if ev.Kind == runtime.EventToolCompleted && ev.ItemID == "aterm_1" {
				utf8TermOut = ev.Output
			}
			if acpTurns == 0 && ev.Kind == runtime.EventToolCompleted && ev.ItemID == "tc_async_launch" {
				asyncPrematureDone = true
			}
			if ev.Kind == runtime.EventBackgroundTask {
				if ev.TaskID == "tc_async_launch" {
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
						sawAsyncRunning = true
					}
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED {
						sawAsyncSettled = true
					}
				}
				if ev.TaskID == "cp_agent_9" {
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
						sawCopilotSubRunning = true
					}
					if ev.BackgroundStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED && strings.Contains(ev.Text, "0 vulnerabilities") {
						sawCopilotSubDone = true
					}
				}
			}
			if ev.Kind == runtime.EventTurnCompleted {
				acpTurns++
			}
		}
	}
	if utf8TermOut != "build 🎉 ok" && utf8TermOut != "build 🎉 ok\n" {
		t.Fatalf("expected split base64 UTF-8 chunks to assemble cleanly into 'build 🎉 ok', got %q", utf8TermOut)
	}
	if asyncPrematureDone || !sawAsyncRunning || !sawAsyncSettled {
		t.Fatalf("ACP asyncLaunch mismatch: prematureDone=%v running=%v settled=%v", asyncPrematureDone, sawAsyncRunning, sawAsyncSettled)
	}
	if !sawCopilotSubRunning || !sawCopilotSubDone {
		t.Fatalf("Copilot sessionEvent subagent mismatch: running=%v done=%v", sawCopilotSubRunning, sawCopilotSubDone)
	}
}

func TestAntigravityACPAutoBackgroundLogStreamingAndChainedSettlement(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	brainDir := t.TempDir()

	drv := acp.NewDriver("")
	drv.PostPromptMergeWindow = 40 * time.Millisecond
	drv.OutOfTurnDebounce = 40 * time.Millisecond
	drv.TaskLogPollInterval = 20 * time.Millisecond

	sess, err := drv.StartSession(ctx, runtime.StartSpec{
		SessionID: "th_agy_chain",
		Workdir:   t.TempDir(),
		Instance: &sessionv1.ProviderInstanceConfig{
			InstanceId: "antigravity",
			Driver:     "acp",
			AcpFlavor:  "antigravity",
			BinaryPath: exe,
			LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "acp"},
			Env: map[string]string{
				"HERMESH_MOCK_DRIVER": "acp",
				"MOCK_AGY_BRAIN_DIR":  brainDir,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r_agy_1", Prompt: "agy_auto_bg_chain"}); err != nil {
		t.Fatal(err)
	}

	bgByID := map[string]runtime.Event{}
	toolDone := map[string]runtime.Event{}
	timeout := time.After(8 * time.Second)
	waitTurn := func(label string) {
		t.Helper()
		for {
			select {
			case <-timeout:
				t.Fatalf("timed out waiting for %s", label)
			case ev := <-sess.Events():
				if ev.Kind == runtime.EventBackgroundTask {
					bgByID[ev.TaskID] = ev
				}
				if ev.Kind == runtime.EventToolCompleted {
					toolDone[ev.ItemID] = ev
				}
				if ev.Kind == runtime.EventTurnCompleted {
					return
				}
			}
		}
	}

	// 1. Turn #1 finishes: verify synchronous command did NOT become a BackgroundTask,
	// while auto-backgrounded run_command, daemon, and subagent all became RUNNING BackgroundTasks
	// with clean human titles and wrapper-stripped output.
	waitTurn("Turn #1")
	if _, bad := bgByID["tc_sync_exit"]; bad {
		t.Fatalf("synchronous run_command with IsDaemon=true that exited immediately must not become a BackgroundTask")
	}
	if _, premature := toolDone["tc_agy_1"]; premature {
		t.Fatalf("auto-backgrounded run_command launch ack must not emit premature EventToolCompleted")
	}
	bg1 := bgByID["tc_agy_1"]
	if bg1.BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING ||
		bg1.ToolName != "Run race suite" ||
		bg1.Command != "go test -race ./..." ||
		strings.Contains(bg1.Output, "YOU MUST TAKE ONE OF THE FOLLOWING") {
		t.Fatalf("unexpected tc_agy_1 initial BackgroundTask: %+v", bg1)
	}
	bgSub := bgByID["tc_agy_sub"]
	if bgSub.BackgroundKind != sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT ||
		bgSub.ToolName != "Log Analyzer" ||
		bgSub.Command != "Inspect flaky test logs" {
		t.Fatalf("unexpected tc_agy_sub BackgroundTask: %+v", bgSub)
	}

	// 2. Append live UTF-8 output to task-10.log while idle and verify TaskLogPollInterval streams it:
	log10 := filepath.Join(brainDir, ".system_generated", "tasks", "task-10.log")
	if err := os.WriteFile(log10, []byte("=== RUN   TestRaceSuite\n正在执行并发压测 50/100...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sawLiveStream := false
	for !sawLiveStream {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for live task-10.log tail streaming")
		case ev := <-sess.Events():
			if ev.Kind == runtime.EventBackgroundTask {
				bgByID[ev.TaskID] = ev
				if ev.TaskID == "tc_agy_1" && strings.Contains(ev.Output, "正在执行并发压测 50/100...") {
					sawLiveStream = true
				}
			}
		}
	}

	// 3. Trigger NotificationTimeoutSeconds ("still running") wakeup: tc_agy_1 MUST remain RUNNING!
	if err := os.WriteFile(filepath.Join(brainDir, "step_timeout_wake"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitTurn("Timeout Wakeup")
	if bgByID["tc_agy_1"].BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
		t.Fatalf("still-running timeout wakeup must NOT mark tc_agy_1 completed, got %+v", bgByID["tc_agy_1"])
	}

	// 4. Trigger task-10 failure (ExitCode=1) + daemon/subagent kills + chained launch of task-11:
	if err := os.WriteFile(filepath.Join(brainDir, "step_fail_and_chain"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitTurn("Failure & Chained Launch Wakeup")

	if bgByID["tc_agy_1"].BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED ||
		!bgByID["tc_agy_1"].HasExitCode || bgByID["tc_agy_1"].ExitCode != 1 ||
		!strings.Contains(bgByID["tc_agy_1"].Output, "失败原因: 竞态冲突") {
		t.Fatalf("expected tc_agy_1 to settle as FAILED (exitCode=1) with final log tail, got %+v", bgByID["tc_agy_1"])
	}
	if td, ok := toolDone["tc_agy_1"]; !ok || td.ExitCode != 1 {
		t.Fatalf("expected EventToolCompleted for tc_agy_1 with exitCode=1, got %+v", td)
	}
	if bgByID["tc_agy_daemon"].BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
		t.Fatalf("expected manage_task(kill) to stop tc_agy_daemon, got %+v", bgByID["tc_agy_daemon"])
	}
	if bgByID["tc_agy_sub"].BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
		t.Fatalf("expected manage_subagents(kill) to stop tc_agy_sub, got %+v", bgByID["tc_agy_sub"])
	}
	// Crucial check: newly-launched chained task tc_agy_2 MUST still be RUNNING after Wakeup #1 settles!
	if bgByID["tc_agy_2"].BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
		t.Fatalf("chained task tc_agy_2 launched during Wakeup #1 was prematurely settled: %+v", bgByID["tc_agy_2"])
	}

	// 5. Trigger task-11 completion (ExitCode=0):
	if err := os.WriteFile(filepath.Join(brainDir, "step_finish_chain"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitTurn("Chained Completion Wakeup")
	if bgByID["tc_agy_2"].BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED ||
		!bgByID["tc_agy_2"].HasExitCode || bgByID["tc_agy_2"].ExitCode != 0 ||
		!strings.Contains(bgByID["tc_agy_2"].Output, "--- PASS: TestRaceSuiteFixed") {
		t.Fatalf("expected tc_agy_2 to settle as COMPLETED (exitCode=0), got %+v", bgByID["tc_agy_2"])
	}
}

func TestClaudeTodoWriteAndCodexTurnPlanUpdated(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("claude_todowrite_and_tool_description", func(t *testing.T) {
		drv := claude.NewDriver("")
		sess, err := drv.StartSession(ctx, runtime.StartSpec{
			SessionID: "s_cl_todo",
			Workdir:   t.TempDir(),
			Instance: &sessionv1.ProviderInstanceConfig{
				InstanceId: "claude",
				Driver:     "claude",
				BinaryPath: exe,
				LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--"},
				Env:        map[string]string{"HERMESH_MOCK_DRIVER": "claude"},
			},
			Selection: &sessionv1.ModelSelection{InstanceId: "claude", Model: "claude-sonnet-4-6"},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r_todo", Prompt: "claude_todo_and_desc"}); err != nil {
			t.Fatal(err)
		}
		var todos []runtime.Event
		var toolStarts []runtime.Event
		timeout := time.After(8 * time.Second)
		for done := false; !done; {
			select {
			case <-timeout:
				t.Fatal("timed out waiting for claude turn")
			case ev := <-sess.Events():
				switch ev.Kind {
				case runtime.EventTodoUpdated:
					todos = append(todos, ev)
				case runtime.EventToolStarted:
					if ev.ToolName == "TodoWrite" {
						t.Fatalf("TodoWrite leaked as EventToolStarted: %+v", ev)
					}
					toolStarts = append(toolStarts, ev)
				case runtime.EventToolCompleted:
					if ev.ItemID == "tu_todo_1" {
						t.Fatalf("TodoWrite tool_result leaked as EventToolCompleted: %+v", ev)
					}
				case runtime.EventTurnCompleted:
					done = true
				case runtime.EventTurnFailed:
					t.Fatalf("turn failed: %s", ev.ErrorMessage)
				}
			}
		}
		if len(todos) != 1 || todos[0].ItemID != "todo" || !todos[0].HasFocus || todos[0].Text != "Inspecting auth module" || len(todos[0].TodoSteps) != 2 {
			t.Fatalf("unexpected claude TodoUpdated events: %+v", todos)
		}
		if len(toolStarts) != 1 || toolStarts[0].Text != "Execute unit tests" || toolStarts[0].Command != "pytest -q" {
			t.Fatalf("unexpected claude ToolStarted events: %+v", toolStarts)
		}
	})

	t.Run("codex_turn_plan_updated", func(t *testing.T) {
		drv := codex.NewDriver("")
		sess, err := drv.StartSession(ctx, runtime.StartSpec{
			SessionID: "s_cx_todo",
			Workdir:   t.TempDir(),
			Instance: &sessionv1.ProviderInstanceConfig{
				InstanceId: "codex",
				Driver:     "codex",
				BinaryPath: exe,
				LaunchArgs: []string{"-test.run=TestHelperMockCLI", "--", "app-server"},
				Env:        map[string]string{"HERMESH_MOCK_DRIVER": "codex"},
			},
			Selection: &sessionv1.ModelSelection{InstanceId: "codex", Model: "gpt-5.4"},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		if err := sess.StartTurn(ctx, runtime.TurnInput{RunID: "r_cx_todo", Prompt: "codex_plan_updated"}); err != nil {
			t.Fatal(err)
		}
		var todos []runtime.Event
		timeout := time.After(8 * time.Second)
		for done := false; !done; {
			select {
			case <-timeout:
				t.Fatal("timed out waiting for codex turn")
			case ev := <-sess.Events():
				switch ev.Kind {
				case runtime.EventTodoUpdated:
					todos = append(todos, ev)
				case runtime.EventTurnCompleted:
					done = true
				case runtime.EventTurnFailed:
					t.Fatalf("turn failed: %s", ev.ErrorMessage)
				}
			}
		}
		if len(todos) != 1 || todos[0].ItemID != "todo" || !todos[0].HasFocus ||
			todos[0].Text != "Rewriting sidebar halo without dynamic masks" ||
			len(todos[0].TodoSteps) != 3 ||
			todos[0].TodoSteps[0].Status != "completed" ||
			todos[0].TodoSteps[1].Status != "in_progress" ||
			todos[0].TodoSteps[2].Status != "pending" {
			t.Fatalf("unexpected codex TodoUpdated events: %+v", todos)
		}
	})
}

func TestAntigravityACPScheduleLifecycleSettlesSessionStatus(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	brainDir := t.TempDir()
	msgsDir := filepath.Join(brainDir, ".system_generated", "messages")

	drv := acp.NewDriver("")
	drv.PostPromptMergeWindow = 40 * time.Millisecond
	drv.OutOfTurnDebounce = 40 * time.Millisecond
	drv.TaskLogPollInterval = 20 * time.Millisecond

	eng, err := kernel.Open(kernel.Config{
		DataDir:   t.TempDir(),
		MachineID: "m_agy_sched",
		Drivers:   map[string]runtime.Driver{"acp": drv},
		IdleTTL:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	owner := kernel.OwnerPrincipal("dev_owner", "Owner")
	if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "antigravity",
		Driver:      "acp",
		AcpFlavor:   "antigravity",
		DisplayName: "Antigravity",
		Enabled:     true,
		BinaryPath:  exe,
		LaunchArgs:  []string{"-test.run=TestHelperMockCLI", "--", "acp"},
		Env: map[string]string{
			"HERMESH_MOCK_DRIVER": "acp",
			"MOCK_AGY_BRAIN_DIR":  brainDir,
		},
	}); err != nil {
		t.Fatal(err)
	}

	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "antigravity", Model: "default"},
		InitialPrompt:  "agy_schedule_lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := launched.Session.SessionId

	waitProj := func(label string, pred func(*sessionv1.SessionProjection) bool) *sessionv1.SessionProjection {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		var last *sessionv1.SessionProjection
		for time.Now().Before(deadline) {
			p, gErr := eng.GetSession(ctx, owner, sessionID)
			if gErr == nil {
				last = p
				if pred(p) {
					return p
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (last session=%+v bgs=%+v)", label, last.GetSession(), last.GetBackgroundTasks())
		return nil
	}
	bgStatus := func(p *sessionv1.SessionProjection, id string) sessionv1.BackgroundTaskStatus {
		for _, bt := range p.GetBackgroundTasks() {
			if bt.TaskId == id {
				return bt.Status
			}
		}
		return sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED
	}

	// 1. After Turn #1 finishes, all 7 awaited tasks/schedules are RUNNING (pre-existing message did not cancel tc_sched_any):
	p1 := waitProj("initial 7 background tasks armed", func(p *sessionv1.SessionProjection) bool {
		return p.GetSession().GetActiveBackgroundCount() == 7 && p.GetSession().GetActiveRunId() == ""
	})
	if bgStatus(p1, "tc_sched_any") != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
		t.Fatalf("pre-existing message must not cancel tc_sched_any: %+v", p1.GetBackgroundTasks())
	}

	// 2. Hidden cancellation receipt settles tc_canceled_cmd as STOPPED without triggering TimerCondition:"any":
	if err := os.WriteFile(filepath.Join(msgsDir, "m1_hidden_cancel.json"), []byte(`{"sender":"conv_sched/task-canceled","hideFromUser":true,"renderDetails":{"messageTitle":"Canceled check was canceled"},"content":"Task id \"conv_sched/task-canceled\" was canceled with result:\nTool execution was canceled"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p2 := waitProj("hidden cancellation settled", func(p *sessionv1.SessionProjection) bool {
		return bgStatus(p, "tc_canceled_cmd") == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
	})
	if bgStatus(p2, "tc_sched_any") != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
		t.Fatalf("hideFromUser cancellation receipt must not trigger TimerCondition:any: %+v", p2.GetBackgroundTasks())
	}

	// 3. One-shot schedule expires with authentic Antigravity timer payload (no "finished with result:"):
	//    tc_sched_oneshot settles COMPLETED, AND tc_sched_any cancels early as STOPPED!
	if err := os.WriteFile(filepath.Join(msgsDir, "m2_timer_expired.json"), []byte(`{"sender":"conv_sched/task-367","priority":"MESSAGE_PRIORITY_HIGH","renderDetails":{"messageTitle":"2m timer: Timer has expired"},"content":"Check task-361 status if XCUITest has not completed yet."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitProj("one-shot timer expiry and TimerCondition:any early cancel", func(p *sessionv1.SessionProjection) bool {
		return bgStatus(p, "tc_sched_oneshot") == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED &&
			bgStatus(p, "tc_sched_any") == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
	})

	// 4. Build finishes + bounded cron fires tick #1 (1/2):
	//    tc_build settles COMPLETED, tc_sched_cond cancels early via TimerCondition:"conv_sched/task-build",
	//    while tc_cron_bounded (MaxIterations=2) stays RUNNING on tick #1!
	if err := os.WriteFile(filepath.Join(msgsDir, "m3_build_and_cron1.json"), []byte(`{"sender":"conv_sched/task-build","content":"Task id \"conv_sched/task-build\" finished with result:\nCommand exited with code 0.\n"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(msgsDir, "m3b_cron_tick1.json"), []byte(`{"sender":"conv_sched/task-cron","content":"Poll CI status (1/2)."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p4 := waitProj("build finished and conditioned timer canceled", func(p *sessionv1.SessionProjection) bool {
		return bgStatus(p, "tc_build") == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED &&
			bgStatus(p, "tc_sched_cond") == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
	})
	if bgStatus(p4, "tc_cron_bounded") != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
		t.Fatalf("bounded cron (MaxIterations=2) must remain RUNNING after tick #1: %+v", p4.GetBackgroundTasks())
	}

	// 5. Cron tick #2 (2/2) + manage_task(kill) returning "task is not running (status: DONE)":
	//    ActiveBackgroundCount reaches 0, Session.Status settles to IDLE, and archiving succeeds!
	if err := os.WriteFile(filepath.Join(brainDir, "sched_step_final_kill"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	p5 := waitProj("all background work settled and session IDLE", func(p *sessionv1.SessionProjection) bool {
		return p.GetSession().GetActiveBackgroundCount() == 0 &&
			p.GetSession().GetActiveRunId() == "" &&
			p.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	})
	if bgStatus(p5, "tc_cron_bounded") != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED ||
		bgStatus(p5, "tc_already_done") != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED {
		t.Fatalf("expected cron tick #2 and DONE kill check to settle COMPLETED: %+v", p5.GetBackgroundTasks())
	}
	if _, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{
		CommandId:   "cmd_archive_settled",
		SessionId:   sessionID,
		HasArchived: true,
		Archived:    true,
	}); err != nil {
		t.Fatalf("expected settled session to archive cleanly without active-background error: %v", err)
	}
}
