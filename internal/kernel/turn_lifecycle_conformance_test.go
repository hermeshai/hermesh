package kernel

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/acp"
	"github.com/hermeshai/hermesh/internal/runtime/claude"
	"github.com/hermeshai/hermesh/internal/runtime/codex"
	"github.com/hermeshai/hermesh/internal/runtime/her"
)

// TestHelperLifecycleConformanceCLI runs as a real OS child process speaking
// authentic ACP (Antigravity & generic Gemini), Claude stream-json, or Codex
// app-server JSON-RPC over stdio to verify end-to-end turn lifecycle invariants.
func TestHelperLifecycleConformanceCLI(t *testing.T) {
	mode := os.Getenv("HERMESH_CONFORMANCE_CLI")
	if mode == "" {
		return
	}
	for _, a := range os.Args {
		if a == "--version" {
			_, _ = os.Stdout.WriteString("mock-cli 2.5.0\n")
			os.Exit(0)
		}
		if a == "status" {
			_, _ = os.Stdout.WriteString("{\"loggedIn\":true}\nLogged in\n")
			os.Exit(0)
		}
	}
	switch mode {
	case "agy_issue55":
		runConformanceAgyIssue55()
	case "generic_acp":
		runConformanceGenericACP()
	case "claude":
		runConformanceClaude()
	case "codex":
		runConformanceCodex()
	}
	os.Exit(0)
}

func waitConformanceGate(dir, name string) bool {
	if dir == "" {
		return true
	}
	p := filepath.Join(dir, name)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func runConformanceAgyIssue55() {
	brainDir := os.Getenv("MOCK_CONF_BRAIN")
	gateDir := os.Getenv("MOCK_CONF_GATES")
	tasksDir := filepath.Join(brainDir, ".system_generated", "tasks")
	msgsDir := filepath.Join(brainDir, ".system_generated", "messages")
	_ = os.MkdirAll(tasksDir, 0o755)
	_ = os.MkdirAll(msgsDir, 0o755)

	var outMu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		outMu.Lock()
		_, _ = os.Stdout.Write(append(b, '\n'))
		outMu.Unlock()
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	turnNum := 0
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": 1}})
		case "session/new":
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"sessionId": "agy_conf_s1"}})
		case "session/load", "session/set_mode", "session/set_config_option", "session/set_model":
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "session/cancel":
			continue
		case "session/prompt":
			turnNum++
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(m.Params, &p)
			promptText := ""
			if len(p.Prompt) > 0 {
				promptText = p.Prompt[0].Text
			}

			if !strings.Contains(promptText, "turn4_issue55") {
				reply := "Turn " + string(rune('0'+turnNum)) + " quick reply"
				if strings.Contains(promptText, "turn3") {
					reply = "Turn 3 previous answer"
				} else if strings.Contains(promptText, "queued_after_turn4") {
					reply = "Queued task done"
				}
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": reply},
						},
					},
				})
				write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue
			}

			// Turn 4 (Issue #55 reproduction):
			// 1. Set focus + multi-step checklist:
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "tc_focus_1",
						"title":         "Run update_focus",
						"kind":          "other",
						"status":        "completed",
						"rawInput":      map[string]any{"focus": "Inspecting HermeshKit tests"},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "tc_steps_1",
						"title":         "Run update_steps",
						"kind":          "other",
						"status":        "completed",
						"rawInput": map[string]any{
							"steps": []any{
								map[string]any{"title": "Run unit tests", "status": "in_progress"},
								map[string]any{"title": "Commit verified fix", "status": "pending"},
							},
						},
					},
				},
			})
			_ = os.WriteFile(filepath.Join(gateDir, "seen_turn4_focus"), []byte("1"), 0o644)
			if !waitConformanceGate(gateDir, "proceed_launch_bg") {
				return
			}

			// 2. Launch auto-backgrounded xcodebuild test (Awaited=true):
			log1 := filepath.Join(tasksDir, "task-xcode.log")
			_ = os.WriteFile(log1, []byte("Compiling HermeshKit...\n"), 0o644)
			ack1 := "Tool is running as a background task with task id: conv_55/task-xcode\nTask logs are available at: file://" + log1 + "\n"
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "tc_xcode",
						"title":         "Run run_command",
						"kind":          "execute",
						"status":        "in_progress",
						"rawInput": map[string]any{
							"CommandLine":       "xcodebuild test -scheme HermeshKit",
							"WaitMsBeforeAsync": 5000,
							"toolSummary":       "Run HermeshKit unit tests",
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
						"toolCallId":    "tc_xcode",
						"status":        "completed",
						"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": ack1}}},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "Waiting for HermeshKit tests to finish."},
					},
				},
			})
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})

			// 3. Background task completes on gate -> wakeup runs foreground git commit with a REAL OS child sleep
			// longer than OutOfTurnQuietLimit (120ms), then pauses longer than OutOfTurnDebounce (40ms) after tool
			// completion to prove StepAwaitingPostToolModel + Proc.HasActiveDescendants() prevent premature turn end!
			go func() {
				if !waitConformanceGate(gateDir, "finish_xcode_bg") {
					return
				}
				_ = os.WriteFile(log1, []byte("Compiling HermeshKit...\nTest Suite 'All tests' passed.\n"), 0o644)
				_ = os.WriteFile(filepath.Join(msgsDir, "msg_xcode.json"), []byte(`{"sender":"conv_55/task-xcode","content":"Task id \"conv_55/task-xcode\" finished with result:\nCommand exited with code 0.\n"}`), 0o644)
				_ = os.WriteFile(filepath.Join(msgsDir, "read.json"), []byte(`["msg_xcode.json"]`), 0o644)

				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_commit",
							"title":         "Run run_command",
							"kind":          "execute",
							"status":        "in_progress",
							"rawInput": map[string]any{
								"CommandLine": "git commit -m 'fix'",
								"toolSummary": "Commit verified fix",
							},
						},
					},
				})
				// Spawn a real OS child process longer than OutOfTurnQuietLimit (120ms):
				child := exec.Command("sleep", "0.25")
				_ = child.Start()
				_ = os.WriteFile(filepath.Join(gateDir, "commit_tool_running"), []byte("1"), 0o644)
				_ = child.Wait()

				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call_update",
							"toolCallId":    "tc_commit",
							"status":        "completed",
							"rawOutput":     map[string]any{"output": "[main abc1234] fix"},
						},
					},
				})
				// Post-tool LLM thinking pause (220ms >> OutOfTurnDebounce 40ms AND > OutOfTurnQuietLimit 120ms):
				time.Sleep(220 * time.Millisecond)

				// Mark all checklist steps done:
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_steps_done",
							"title":         "Run update_steps",
							"kind":          "other",
							"status":        "completed",
							"rawInput": map[string]any{
								"steps": []any{
									map[string]any{"title": "Run unit tests", "status": "done"},
									map[string]any{"title": "Commit verified fix", "status": "done"},
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
							"content":       map[string]any{"type": "text", "text": "All HermeshKit tests passed and committed."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 200000},
					},
				})
			}()
		}
	}
}

func runConformanceGenericACP() {
	gateDir := os.Getenv("MOCK_CONF_GATES")
	var outMu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		outMu.Lock()
		_, _ = os.Stdout.Write(append(b, '\n'))
		outMu.Unlock()
	}

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": 1}})
		case "session/new":
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"sessionId": "gem_conf_1"}})
		case "session/load", "session/set_mode", "session/set_config_option", "session/set_model":
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "session/prompt":
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(m.Params, &p)
			prompt := ""
			if len(p.Prompt) > 0 {
				prompt = p.Prompt[0].Text
			}
			if strings.Contains(prompt, "start_daemon") {
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "gem_dev",
							"title":         "Bash",
							"kind":          "execute",
							"status":        "completed",
							"rawInput": map[string]any{
								"command":           "vite --port 5173",
								"description":       "Start Vite dev server",
								"run_in_background": true,
							},
							"rawOutput": map[string]any{"output": "Command running in background with ID: gem_pty_dev"},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "Dev server listening on 5173."},
						},
					},
				})
				write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue
			}

			// Awaited background command on generic ACP:
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "gem_cargo",
						"title":         "Bash",
						"kind":          "execute",
						"status":        "in_progress",
						"rawInput": map[string]any{
							"command":           "cargo test --workspace",
							"description":       "Run Rust workspace tests",
							"run_in_background": true,
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
						"toolCallId":    "gem_cargo",
						"status":        "completed",
						"content":       []any{map[string]any{"type": "terminal", "terminalId": "term_cargo_1"}},
						"rawOutput":     map[string]any{"output": "Command running in background with ID: term_cargo_1"},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "Running cargo test in background."},
					},
				},
			})
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})

			go func() {
				if !waitConformanceGate(gateDir, "finish_gem_cargo") {
					return
				}
				exitZero := 0
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "terminal_output",
							"terminalId":    "term_cargo_1",
							"output":        "test result: ok. 42 passed\n",
							"exitStatus":    map[string]any{"exitCode": exitZero},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "Rust workspace tests passed (42/42)."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{"sessionUpdate": "usage_update", "used": 300, "size": 100000},
					},
				})
			}()
		}
	}
}

func runConformanceClaude() {
	gateDir := os.Getenv("MOCK_CONF_GATES")
	outFile := filepath.Join(gateDir, "claude_pytest.out")
	var outMu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		outMu.Lock()
		_, _ = os.Stdout.Write(append(b, '\n'))
		outMu.Unlock()
	}

	sc := bufio.NewScanner(os.Stdin)
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
				write(map[string]any{"type": "system", "subtype": "init", "session_id": "cl_conf_1", "model": "claude-sonnet-4-6"})
			}
			write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": reqID}})

		case "user":
			write(map[string]any{"type": "user", "isReplay": true})
			msgObj, _ := m["message"].(map[string]any)
			prompt, _ := msgObj["content"].(string)

			if strings.Contains(prompt, "start_daemon") {
				write(map[string]any{
					"type": "assistant",
					"message": map[string]any{"content": []any{
						map[string]any{
							"type":  "tool_use",
							"id":    "tu_dev",
							"name":  "Bash",
							"input": map[string]any{"command": "npm run dev --port 3000", "description": "Start Next.js dev server", "run_in_background": true},
						},
					}},
				})
				write(map[string]any{
					"type": "user",
					"message": map[string]any{"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu_dev", "content": "Command running in background with ID: ctask_dev"},
					}},
				})
				write(map[string]any{
					"type":        "system",
					"subtype":     "task_started",
					"task_id":     "ctask_dev",
					"tool_use_id": "tu_dev",
					"description": "Start Next.js dev server",
					"task_type":   "local_bash",
				})
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Dev server started on port 3000."}}},
				})
				write(map[string]any{"type": "result", "usage": map[string]any{"input_tokens": 20, "output_tokens": 10}})
				continue
			}

			// Turn 2: Awaited background pytest + TodoWrite + delayed wakeup:
			_ = os.WriteFile(outFile, []byte("collecting items...\n"), 0o644)
			write(map[string]any{
				"type": "assistant",
				"message": map[string]any{"content": []any{
					map[string]any{
						"type": "tool_use",
						"id":   "tu_todo_1",
						"name": "TodoWrite",
						"input": map[string]any{
							"todos": []any{
								map[string]any{"content": "Run pytest suite", "status": "in_progress"},
							},
						},
					},
					map[string]any{
						"type":  "tool_use",
						"id":    "tu_pytest",
						"name":  "Bash",
						"input": map[string]any{"command": "pytest -q", "description": "Run pytest suite", "run_in_background": true},
					},
				}},
			})
			write(map[string]any{
				"type": "user",
				"message": map[string]any{"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "tu_todo_1", "content": "ok"},
					map[string]any{"type": "tool_result", "tool_use_id": "tu_pytest", "content": "Command running in background with ID: ctask_pytest"},
				}},
			})
			write(map[string]any{
				"type":        "system",
				"subtype":     "task_started",
				"task_id":     "ctask_pytest",
				"tool_use_id": "tu_pytest",
				"description": "Run pytest suite",
				"task_type":   "local_bash",
				"output_file": outFile,
			})
			write(map[string]any{
				"type":    "assistant",
				"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Launched pytest in background."}}},
			})
			write(map[string]any{"type": "result", "usage": map[string]any{"input_tokens": 35, "output_tokens": 12}})

			go func() {
				if !waitConformanceGate(gateDir, "finish_claude_pytest") {
					return
				}
				_ = os.WriteFile(outFile, []byte("collecting items...\n18 passed in 0.4s\n"), 0o644)
				write(map[string]any{
					"type":        "system",
					"subtype":     "task_notification",
					"task_id":     "ctask_pytest",
					"tool_use_id": "tu_pytest",
					"status":      "completed",
					"summary":     "18 passed",
					"output_file": outFile,
				})
				// Delay longer than WakeupHandoffGrace (60ms in test) to prove PendingWakeup() holds the run open!
				time.Sleep(150 * time.Millisecond)
				write(map[string]any{
					"type": "assistant",
					"message": map[string]any{"content": []any{
						map[string]any{
							"type": "tool_use",
							"id":   "tu_todo_2",
							"name": "TodoWrite",
							"input": map[string]any{
								"todos": []any{
									map[string]any{"content": "Run pytest suite", "status": "completed"},
								},
							},
						},
					}},
				})
				write(map[string]any{
					"type": "user",
					"message": map[string]any{"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu_todo_2", "content": "ok"},
					}},
				})
				write(map[string]any{
					"type":    "assistant",
					"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Pytest passed (18/18)."}}},
				})
				write(map[string]any{"type": "result", "usage": map[string]any{"input_tokens": 50, "output_tokens": 16}})
			}()
		}
	}
}

func runConformanceCodex() {
	sc := bufio.NewScanner(os.Stdin)
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	for sc.Scan() {
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "thread/start":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"thread": map[string]any{"id": "cx_conf_1"}}})
		case "thread/backgroundTerminals/list":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"data": []any{}}})
		case "thread/unarchive", "thread/name/set":
			write(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "turn/start":
			write(map[string]any{"id": *m.ID, "result": map[string]any{"turn": map[string]any{"id": "turn_cx_1"}}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "turn_cx_1"}}})
			// 1. Spawn background dev server (promoted to DAEMON):
			write(map[string]any{
				"method": "item/started",
				"params": map[string]any{
					"item": map[string]any{"id": "cx_dev_1", "type": "commandExecution", "command": "npm run dev"},
				},
			})
			write(map[string]any{
				"method": "item/commandExecution/terminalInteraction",
				"params": map[string]any{"itemId": "cx_dev_1", "processId": "pty_cx_dev"},
			})
			// 2. Plan starts inProgress then completes all steps before final reply:
			write(map[string]any{
				"method": "turn/plan/updated",
				"params": map[string]any{
					"explanation": "Booting dev preview",
					"plan": []any{
						map[string]any{"step": "Start dev server", "status": "inProgress"},
					},
				},
			})
			write(map[string]any{
				"method": "turn/plan/updated",
				"params": map[string]any{
					"explanation": "",
					"plan": []any{
						map[string]any{"step": "Start dev server", "status": "completed"},
					},
				},
			})
			write(map[string]any{
				"method": "item/completed",
				"params": map[string]any{
					"item": map[string]any{"id": "cx_msg_1", "type": "agentMessage", "text": "Codex dev preview ready."},
				},
			})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"status": "completed"}}})
		}
	}
}

func TestIssue55FullReproductionE2E(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	brainDir := t.TempDir()
	gateDir := t.TempDir()

	acpDrv := acp.NewDriver("")
	acpDrv.PostPromptMergeWindow = 30 * time.Millisecond
	acpDrv.OutOfTurnDebounce = 40 * time.Millisecond
	acpDrv.OutOfTurnQuietLimit = 120 * time.Millisecond
	// Keep OutOfTurnToolWait at its default (3m) to verify post-tool LLM pause is protected!
	acpDrv.TaskLogPollInterval = 20 * time.Millisecond

	var noticeMu sync.Mutex
	var completedRunNotices []string
	eng, err := Open(Config{
		DataDir:            t.TempDir(),
		MachineID:          "m_issue55_e2e",
		Drivers:            map[string]runtime.Driver{"acp": acpDrv},
		IdleTTL:            time.Minute,
		WakeupHandoffGrace: 80 * time.Millisecond,
		OnNotice: func(s *sessionv1.Session, ev *sessionv1.SessionEvent) {
			if ev.GetType() == "run.completed" && ev.GetRun() != nil {
				noticeMu.Lock()
				completedRunNotices = append(completedRunNotices, ev.GetRun().GetRunId()+":"+s.GetResult())
				noticeMu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	owner := OwnerPrincipal("dev_owner", "Owner")
	_, err = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "antigravity",
		Driver:      "acp",
		AcpFlavor:   "antigravity",
		DisplayName: "Antigravity",
		Enabled:     true,
		BinaryPath:  exe,
		LaunchArgs:  []string{"-test.run=TestHelperLifecycleConformanceCLI", "--", "acp"},
		Env: map[string]string{
			"HERMESH_CONFORMANCE_CLI": "agy_issue55",
			"MOCK_CONF_BRAIN":         brainDir,
			"MOCK_CONF_GATES":         gateDir,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Turns 1..3: establish multi-turn history and non-empty Session.Result ("Turn 3 previous answer"):
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "antigravity"},
		InitialPrompt:  "turn1",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId
	waitSessionIdleResult := func(wantTurns uint32, wantResult string) *sessionv1.SessionProjection {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil &&
				cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
				cur.Session.GetProgress().GetTurnCount() == wantTurns &&
				cur.Session.Result == wantResult {
				return cur
			}
			time.Sleep(15 * time.Millisecond)
		}
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for TurnCount=%d Result=%q; got session=%+v runs=%+v", wantTurns, wantResult, cur.GetSession(), cur.GetRuns())
		return nil
	}

	waitSessionIdleResult(1, "Turn 1 quick reply")
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: sid, Text: "turn2", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START}); err != nil {
		t.Fatal(err)
	}
	waitSessionIdleResult(2, "Turn 2 quick reply")
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: sid, Text: "turn3", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START}); err != nil {
		t.Fatal(err)
	}
	afterTurn3 := waitSessionIdleResult(3, "Turn 3 previous answer")
	if len(afterTurn3.Runs) != 3 {
		t.Fatalf("expected 3 runs after Turn 3, got %d", len(afterTurn3.Runs))
	}

	// Dispatch Turn 4 (Issue #55 scenario):
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "turn4_issue55",
		Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_START,
	}); err != nil {
		t.Fatal(err)
	}

	// Assert Turn 3's result is cleared immediately on Turn 4 start and initial focus is projected:
	var pFocus *sessionv1.SessionProjection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && cur.Session.GetProgress().GetTurnCount() == 4 && cur.Session.GetProgress().GetCurrentTool() == "Inspecting HermeshKit tests" {
			pFocus = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if pFocus == nil {
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("did not observe Turn 4 initial focus; session=%+v", cur.GetSession())
	}
	if pFocus.Session.Result != "" {
		t.Fatalf("Turn 3 Result leaked into running Turn 4: got %q, want empty", pFocus.Session.Result)
	}
	turn4StartedAt := pFocus.Session.GetProgress().GetTurnStartedAt().AsTime()
	turn4RunID := pFocus.Session.ActiveRunId

	// Release foreground background launch and verify Session stays RUNNING in awaiting_background:
	if err := os.WriteFile(filepath.Join(gateDir, "proceed_launch_bg"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var pAwait *sessionv1.SessionProjection
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil &&
			cur.Session.GetProgress().GetPhase() == "awaiting_background" &&
			cur.Session.GetProgress().GetCurrentTool() == "Run HermeshKit unit tests" {
			pAwait = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if pAwait == nil {
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("did not enter awaiting_background; session=%+v runs=%+v", cur.GetSession(), cur.GetRuns())
	}
	if pAwait.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING || pAwait.Session.ActiveRunId != turn4RunID || pAwait.Session.GetProgress().GetTurnCount() != 4 {
		t.Fatalf("expected Turn 4 to stay RUNNING on same Run %s with TurnCount=4, got %+v", turn4RunID, pAwait.Session)
	}

	// Queue Run #5 while Turn 4 is awaiting background task -> verify Run #5 waits in QUEUED state!
	qResp, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "queued_after_turn4",
		Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if qResp.Run.Status != sessionv1.RunStatus_RUN_STATUS_QUEUED {
		t.Fatalf("expected queued message to stay QUEUED behind awaiting_background Turn 4, got %v", qResp.Run.Status)
	}
	time.Sleep(80 * time.Millisecond)
	midCheck, err := eng.GetSession(ctx, owner, sid)
	if err != nil {
		t.Fatal(err)
	}
	if midCheck.Session.ActiveRunId != turn4RunID || midCheck.Session.QueuedRunCount != 1 {
		t.Fatalf("queued run preempted Turn 4 prematurely! active=%s queued=%d", midCheck.Session.ActiveRunId, midCheck.Session.QueuedRunCount)
	}
	noticeMu.Lock()
	noticesBeforeWake := len(completedRunNotices)
	noticeMu.Unlock()
	if noticesBeforeWake != 3 {
		t.Fatalf("expected exactly 3 run.completed notices (Turns 1..3) before Turn 4 wakeup, got %d (%v)", noticesBeforeWake, completedRunNotices)
	}

	// Trigger background completion -> wakeup runs foreground git commit (with real child sleep > QuietLimit)
	// + 220ms post-tool thinking pause + update_steps(all done) + final report -> then Run #5 executes!
	if err := os.WriteFile(filepath.Join(gateDir, "finish_xcode_bg"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Observe foreground git commit tool during wakeup on the SAME Run #4 with preserved TurnStartedAt:
	sawCommitTool := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && cur.Session.GetProgress().GetCurrentTool() == "Commit verified fix" {
			sawCommitTool = true
			if cur.Session.ActiveRunId != turn4RunID || !cur.Session.GetProgress().GetTurnStartedAt().AsTime().Equal(turn4StartedAt) {
				t.Fatalf("expected wakeup tool on same Run %s with unchanged TurnStartedAt, got active=%s started=%v vs %v",
					turn4RunID, cur.Session.ActiveRunId, cur.Session.GetProgress().GetTurnStartedAt().AsTime(), turn4StartedAt)
			}
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawCommitTool {
		t.Fatal("did not observe foreground 'Commit verified fix' tool during wakeup")
	}

	// Wait for both Turn 4 and queued Turn 5 to complete cleanly:
	finalProj := waitSessionIdleResult(5, "Queued task done")
	if len(finalProj.Runs) != 5 {
		t.Fatalf("expected exactly 5 Runs total (zero Continuation Run fragmentation!), got %d", len(finalProj.Runs))
	}
	if finalProj.Runs[3].RunId != turn4RunID || finalProj.Runs[3].Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED ||
		finalProj.Runs[3].GetAuthor().GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
		t.Fatalf("Turn 4 Run unexpected: %+v", finalProj.Runs[3])
	}
	if finalProj.Session.GetProgress().GetCurrentTool() != "" {
		t.Fatalf("expected CurrentTool cleared after settlement, got %q", finalProj.Session.GetProgress().GetCurrentTool())
	}

	noticeMu.Lock()
	gotNotices := append([]string(nil), completedRunNotices...)
	noticeMu.Unlock()
	if len(gotNotices) != 5 {
		t.Fatalf("expected exactly 5 run.completed notices (1 per logical user turn), got %d: %v", len(gotNotices), gotNotices)
	}
	if want := turn4RunID + ":All HermeshKit tests passed and committed."; gotNotices[3] != want {
		t.Fatalf("Turn 4 completion notice = %q, want %q", gotNotices[3], want)
	}
}

type conformanceHerBridge struct {
	mu   sync.Mutex
	conv *conformanceHerConv
}

type conformanceHerConv struct {
	events chan json.RawMessage
	once   sync.Once
}

func (c *conformanceHerConv) Events() <-chan json.RawMessage { return c.events }
func (c *conformanceHerConv) Close() error {
	c.once.Do(func() { close(c.events) })
	return nil
}
func (c *conformanceHerConv) Op(_ context.Context, op any) error {
	b, _ := json.Marshal(op)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["type"] == "prompt" {
		go func() {
			c.events <- json.RawMessage(`{"type":"task_started","task_id":"t_conf_1"}`)
			c.events <- json.RawMessage(`{"type":"tool_started","call_id":"her_tc_1","name":"call_tool","title":"Search repository symbols","args":{"q":"TurnStepTracker"}}`)
			time.Sleep(60 * time.Millisecond)
			c.events <- json.RawMessage(`{"type":"tool_completed","call_id":"her_tc_1","result":{"tool_name":"call_tool","is_error":false}}`)
			c.events <- json.RawMessage(`{"type":"tool_started","call_id":"her_tc_2","name":"send_to_user","args":{"text":"Found TurnStepTracker in lifecycle.go."}}`)
			c.events <- json.RawMessage(`{"type":"tool_completed","call_id":"her_tc_2","result":{"tool_name":"send_to_user"}}`)
			c.events <- json.RawMessage(`{"type":"task_completed","task_id":"t_conf_1"}`)
		}()
	}
	return nil
}

func (b *conformanceHerBridge) Available() bool { return true }
func (b *conformanceHerBridge) Models(context.Context) ([]*sessionv1.ModelCatalogEntry, error) {
	return []*sessionv1.ModelCatalogEntry{{Slug: "tc/auto", Name: "Auto", IsDefault: true}}, nil
}
func (b *conformanceHerBridge) NewConversation(context.Context, string, string) (string, error) {
	return "her_conf_conv_1", nil
}
func (b *conformanceHerBridge) Attach(context.Context, string) (her.Conversation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &conformanceHerConv{events: make(chan json.RawMessage, 16)}
	b.conv = c
	return c, nil
}
func (b *conformanceHerBridge) Op(context.Context, string, any) error        { return nil }
func (b *conformanceHerBridge) DeleteConversation(context.Context, string) error { return nil }

func TestCrossProviderUnifiedLifecycleConformance(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("claude_daemon_non_blocking_and_awaited_single_run", func(t *testing.T) {
		gateDir := t.TempDir()
		claudeDrv := claude.NewDriver("")
		eng, err := Open(Config{
			DataDir:            t.TempDir(),
			MachineID:          "m_conf_claude",
			Drivers:            map[string]runtime.Driver{"claude": claudeDrv},
			IdleTTL:            time.Minute,
			WakeupHandoffGrace: 60 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId:  "claude-conf",
			Driver:      "claude",
			DisplayName: "Claude Conformance",
			Enabled:     true,
			BinaryPath:  exe,
			LaunchArgs:  []string{"-test.run=TestHelperLifecycleConformanceCLI", "--"},
			Env: map[string]string{
				"HERMESH_CONFORMANCE_CLI": "claude",
				"MOCK_CONF_GATES":         gateDir,
			},
		}); err != nil {
			t.Fatal(err)
		}

		// Turn 1: Bash(npm run dev, run_in_background:true) -> DAEMON -> must settle to IDLE immediately!
		res1, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "claude-conf"},
			InitialPrompt:  "start_daemon",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := res1.Session.SessionId
		deadline := time.Now().Add(5 * time.Second)
		var p1 *sessionv1.SessionProjection
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && cur.Session.GetProgress().GetTurnCount() == 1 {
				p1 = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if p1 == nil || p1.Session.ActiveDaemonCount != 1 || p1.Session.ActiveBackgroundCount != 0 {
			t.Fatalf("expected Claude daemon turn to settle IDLE with ActiveDaemonCount=1 ActiveBackgroundCount=0, got %+v", p1.GetSession())
		}

		// Turn 2: Bash(pytest -q, run_in_background:true) -> Awaited -> stays RUNNING in awaiting_background!
		if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
			SessionId: sid,
			Text:      "run_awaited_pytest",
			Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_START,
		}); err != nil {
			t.Fatal(err)
		}
		var pAwait *sessionv1.SessionProjection
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.GetProgress().GetPhase() == "awaiting_background" {
				pAwait = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if pAwait == nil || pAwait.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING ||
			pAwait.Session.ActiveBackgroundCount != 1 || pAwait.Session.ActiveDaemonCount != 1 ||
			pAwait.Session.GetProgress().GetCurrentTool() != "Run pytest suite" {
			t.Fatalf("unexpected Claude awaiting_background state: %+v", pAwait.GetSession())
		}

		// Release pytest completion -> 150ms (> 60ms WakeupHandoffGrace) gap bridged by PendingWakeup() -> single Run #2!
		if err := os.WriteFile(filepath.Join(gateDir, "finish_claude_pytest"), []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		var pDone *sessionv1.SessionProjection
		deadline = time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && cur.Session.Result == "Pytest passed (18/18)." {
				pDone = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if pDone == nil || len(pDone.Runs) != 2 || pDone.Session.GetProgress().GetTurnCount() != 2 || pDone.Session.ActiveDaemonCount != 1 || pDone.Session.GetProgress().GetCurrentTool() != "" {
			t.Fatalf("unexpected Claude final projection: session=%+v runs=%d", pDone.GetSession(), len(pDone.GetRuns()))
		}
	})

	t.Run("generic_acp_daemon_and_awaited_single_run", func(t *testing.T) {
		gateDir := t.TempDir()
		acpDrv := acp.NewDriver("")
		acpDrv.PostPromptMergeWindow = 30 * time.Millisecond
		acpDrv.OutOfTurnDebounce = 40 * time.Millisecond
		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_conf_gem",
			Drivers:   map[string]runtime.Driver{"acp": acpDrv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId:  "gemini-conf",
			Driver:      "acp",
			AcpFlavor:   "gemini",
			DisplayName: "Gemini ACP Conformance",
			Enabled:     true,
			BinaryPath:  exe,
			LaunchArgs:  []string{"-test.run=TestHelperLifecycleConformanceCLI", "--", "acp"},
			Env: map[string]string{
				"HERMESH_CONFORMANCE_CLI": "generic_acp",
				"MOCK_CONF_GATES":         gateDir,
			},
		}); err != nil {
			t.Fatal(err)
		}

		res1, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "gemini-conf"},
			InitialPrompt:  "start_daemon",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := res1.Session.SessionId
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && cur.Session.ActiveDaemonCount == 1 {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
			SessionId: sid,
			Text:      "run_cargo_bg",
			Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_START,
		}); err != nil {
			t.Fatal(err)
		}
		var pAwait *sessionv1.SessionProjection
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.GetProgress().GetPhase() == "awaiting_background" {
				pAwait = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if pAwait == nil || pAwait.Session.GetProgress().GetCurrentTool() != "Run Rust workspace tests" {
			t.Fatalf("expected generic ACP awaiting_background with 'Run Rust workspace tests', got %+v", pAwait.GetSession())
		}

		if err := os.WriteFile(filepath.Join(gateDir, "finish_gem_cargo"), []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		var pDone *sessionv1.SessionProjection
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && cur.Session.Result == "Rust workspace tests passed (42/42)." {
				pDone = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if pDone == nil || len(pDone.Runs) != 2 || pDone.Session.GetProgress().GetTurnCount() != 2 || pDone.Session.ActiveDaemonCount != 1 {
			t.Fatalf("unexpected generic ACP final state: session=%+v runs=%d", pDone.GetSession(), len(pDone.GetRuns()))
		}
	})

	t.Run("codex_daemon_non_blocking_and_plan_focus_clear", func(t *testing.T) {
		codexDrv := codex.NewDriver("")
		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_conf_codex",
			Drivers:   map[string]runtime.Driver{"codex": codexDrv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId:  "codex-conf",
			Driver:      "codex",
			DisplayName: "Codex Conformance",
			Enabled:     true,
			BinaryPath:  exe,
			LaunchArgs:  []string{"-test.run=TestHelperLifecycleConformanceCLI", "--"},
			Env:         map[string]string{"HERMESH_CONFORMANCE_CLI": "codex"},
		}); err != nil {
			t.Fatal(err)
		}

		res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "codex-conf"},
			InitialPrompt:  "boot_preview",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := res.Session.SessionId
		var pDone *sessionv1.SessionProjection
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && cur.Session.Result == "Codex dev preview ready." {
				pDone = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if pDone == nil {
			cur, _ := eng.GetSession(ctx, owner, sid)
			t.Fatalf("timed out waiting for Codex daemon turn IDLE; session=%+v runs=%+v", cur.GetSession(), cur.GetRuns())
		}
		if pDone.Session.ActiveDaemonCount != 1 || pDone.Session.ActiveBackgroundCount != 0 || pDone.Session.GetProgress().GetCurrentTool() != "" {
			t.Fatalf("expected Codex daemon turn IDLE with ActiveDaemonCount=1 and CurrentTool='', got %+v", pDone.GetSession())
		}
	})

	t.Run("her_tool_title_projection_and_clean_settlement", func(t *testing.T) {
		bridge := &conformanceHerBridge{}
		herDrv := her.NewDriver(bridge)
		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_conf_her",
			Drivers:   map[string]runtime.Driver{"her": herDrv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId:  "her",
			Driver:      "her",
			DisplayName: "Her",
			Enabled:     true,
		}); err != nil {
			t.Fatal(err)
		}
		res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "her", Model: "tc/auto"},
			InitialPrompt:  "find tracker",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := res.Session.SessionId
		sawTitle := false
		var pDone *sessionv1.SessionProjection
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil {
				if cur.Session.GetProgress().GetCurrentTool() == "Search repository symbols" {
					sawTitle = true
				}
				if cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && cur.Session.Result == "Found TurnStepTracker in lifecycle.go." {
					pDone = cur
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !sawTitle {
			t.Fatal("expected Her EventToolStarted.Text ('Search repository symbols') to project into Progress.CurrentTool")
		}
		if pDone == nil || pDone.Session.GetProgress().GetCurrentTool() != "" {
			t.Fatalf("unexpected Her final projection: %+v", pDone.GetSession())
		}
	})
}

type bgStopperTestSession struct {
	*customTestSession
	onStopTask func(context.Context, string) error
}

func (s *bgStopperTestSession) StopBackgroundTask(ctx context.Context, taskID string) error {
	if s.onStopTask != nil {
		return s.onStopTask(ctx, taskID)
	}
	return nil
}

func TestAwaitingBackgroundInterruptAndSilentExitSafetyNet(t *testing.T) {
	ctx := context.Background()

	t.Run("interrupt_session_stops_awaited_preserves_daemon", func(t *testing.T) {
		var stoppedIDs []string
		var mu sync.Mutex
		drv := &customTestDriver{
			kind: "bg_intr",
			caps: &sessionv1.ProviderCapabilities{NativeResume: true},
			onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
				ch := make(chan runtime.Event, 32)
				sess := &bgStopperTestSession{
					customTestSession: &customTestSession{
						events: ch,
						onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
							out <- runtime.Event{
								Kind:             runtime.EventBackgroundTask,
								TaskID:           "daemon_1",
								ItemID:           "it_daemon_1",
								ToolName:         "Dev Server",
								Command:          "npm run dev",
								BackgroundKind:   sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON,
								BackgroundStatus: sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
							}
							out <- runtime.Event{
								Kind:              runtime.EventBackgroundTask,
								TaskID:            "await_1",
								ItemID:            "it_await_1",
								ToolName:          "Long Test",
								Command:           "go test ./...",
								BackgroundKind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
								BackgroundStatus:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
								BackgroundAwaited: true,
							}
							out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m1", Text: "Waiting on tests."}
							out <- runtime.Event{Kind: runtime.EventTurnCompleted}
						},
					},
					onStopTask: func(_ context.Context, taskID string) error {
						mu.Lock()
						stoppedIDs = append(stoppedIDs, taskID)
						mu.Unlock()
						ch <- runtime.Event{
							Kind:              runtime.EventBackgroundTask,
							TaskID:            taskID,
							ItemID:            "it_" + taskID,
							BackgroundKind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
							BackgroundStatus:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
							BackgroundAwaited: true,
						}
						return nil
					},
				}
				ch <- runtime.Event{Kind: runtime.EventNativeSession, NativeID: "intr_s1"}
				return sess, nil
			},
		}

		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_bg_intr",
			Drivers:   map[string]runtime.Driver{"bg_intr": drv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId: "bg_intr",
			Driver:     "bg_intr",
			Enabled:    true,
		}); err != nil {
			t.Fatal(err)
		}
		res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "bg_intr"},
			InitialPrompt:  "go",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := res.Session.SessionId
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			cur, _ := eng.GetSession(ctx, owner, sid)
			if cur.GetSession().GetProgress().GetPhase() == "awaiting_background" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		if _, err := eng.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{SessionId: sid, Force: false}); err != nil {
			t.Fatalf("InterruptSession: %v", err)
		}
		intrProj, err := eng.GetSession(ctx, owner, sid)
		if err != nil {
			t.Fatal(err)
		}
		if intrProj.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE ||
			intrProj.Runs[0].Status != sessionv1.RunStatus_RUN_STATUS_INTERRUPTED ||
			intrProj.Session.ActiveDaemonCount != 1 {
			t.Fatalf("expected INTERRUPTED run + IDLE session + preserved ActiveDaemonCount=1, got session=%+v run=%+v", intrProj.Session, intrProj.Runs[0])
		}
		mu.Lock()
		gotStopped := append([]string(nil), stoppedIDs...)
		mu.Unlock()
		if len(gotStopped) != 1 || gotStopped[0] != "await_1" {
			t.Fatalf("expected only await_1 stopped (daemon_1 preserved), got %v", gotStopped)
		}
	})

	t.Run("stop_background_task_silent_exit_settles_via_handoff_grace", func(t *testing.T) {
		drv := &customTestDriver{
			kind: "bg_grace",
			caps: &sessionv1.ProviderCapabilities{NativeResume: true},
			onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
				ch := make(chan runtime.Event, 32)
				sess := &bgStopperTestSession{
					customTestSession: &customTestSession{
						events: ch,
						onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
							out <- runtime.Event{
								Kind:              runtime.EventBackgroundTask,
								TaskID:            "silent_bg_1",
								ItemID:            "it_silent_1",
								ToolName:          "Silent Task",
								Command:           "sleep 30",
								BackgroundKind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
								BackgroundStatus:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
								BackgroundAwaited: true,
							}
							out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m1", Text: "Started task."}
							out <- runtime.Event{Kind: runtime.EventTurnCompleted}
						},
					},
					onStopTask: func(_ context.Context, taskID string) error {
						ch <- runtime.Event{
							Kind:              runtime.EventBackgroundTask,
							TaskID:            taskID,
							ItemID:            "it_silent_1",
							BackgroundKind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
							BackgroundStatus:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
							BackgroundAwaited: true,
						}
						return nil
					},
				}
				ch <- runtime.Event{Kind: runtime.EventNativeSession, NativeID: "grace_s1"}
				return sess, nil
			},
		}

		eng, err := Open(Config{
			DataDir:            t.TempDir(),
			MachineID:          "m_bg_grace",
			Drivers:            map[string]runtime.Driver{"bg_grace": drv},
			IdleTTL:            time.Minute,
			WakeupHandoffGrace: 70 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId: "bg_grace",
			Driver:     "bg_grace",
			Enabled:    true,
		}); err != nil {
			t.Fatal(err)
		}
		res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "bg_grace"},
			InitialPrompt:  "start",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := res.Session.SessionId
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			cur, _ := eng.GetSession(ctx, owner, sid)
			if cur.GetSession().GetProgress().GetPhase() == "awaiting_background" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		if _, err := eng.StopBackgroundTask(ctx, owner, &sessionv1.StopBackgroundTaskRequest{
			SessionId: sid,
			TaskId:    "silent_bg_1",
		}); err != nil {
			t.Fatalf("StopBackgroundTask: %v", err)
		}

		var settled *sessionv1.SessionProjection
		deadline = time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			cur, gErr := eng.GetSession(ctx, owner, sid)
			if gErr == nil && cur.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
				settled = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if settled == nil || settled.Runs[0].Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			t.Fatalf("expected WakeupHandoffGrace to settle silent task exit to COMPLETED/IDLE, got %+v", settled.GetSession())
		}
	})
}
