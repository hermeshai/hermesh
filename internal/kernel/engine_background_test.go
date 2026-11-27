package kernel

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

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/acp"
)

func TestBackgroundTaskLifecycleContinuationCheckpointAndStop(t *testing.T) {
	ctx := context.Background()
	// Use a short IdleTTL (80ms) to prove Supervisor.ReapIdle does NOT kill a session
	// while background tasks or daemons are running!
	eng, _, _ := openTestEngine(t, t.TempDir(), 80*time.Millisecond)
	defer eng.Close()

	repo := initGitProject(t)
	if err := eng.SyncWorkspace("bgproj", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "BG repo", nil); err != nil {
		t.Fatal(err)
	}
	owner := OwnerPrincipal("dev_owner", "Owner")

	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId: "bgproj",
		InitialPrompt: strings.Join([]string{
			"bg-daemon npm run dev|listening on :5173",
			"bg-shell 350ms|make e2e|42 e2e specs passed|Background verification succeeded!",
			"say Foreground reply complete; waiting on make e2e.",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sid := launched.Session.SessionId

	// 1. Wait for foreground segment to enter awaiting_background while bg-shell (350ms) and bg-daemon are still running.
	var midRun *sessionv1.SessionProjection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, err := eng.GetSession(ctx, owner, sid)
		if err == nil && len(p.Runs) == 1 && p.Session.GetProgress().GetPhase() == "awaiting_background" {
			midRun = p
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if midRun == nil {
		t.Fatalf("timed out waiting for Run #1 to enter awaiting_background")
	}
	if midRun.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING || midRun.Runs[0].Status != sessionv1.RunStatus_RUN_STATUS_RUNNING {
		t.Fatalf("Session/Run must remain RUNNING while awaiting make e2e, got session=%v run=%v", midRun.Session.Status, midRun.Runs[0].Status)
	}
	if midRun.Session.ActiveBackgroundCount != 1 || midRun.Session.ActiveDaemonCount != 1 {
		t.Fatalf("expected ActiveBackgroundCount=1 ActiveDaemonCount=1, got bg=%d daemon=%d",
			midRun.Session.ActiveBackgroundCount, midRun.Session.ActiveDaemonCount)
	}
	if gotNow := midRun.Session.GetProgress().GetCurrentTool(); gotNow != "make e2e" {
		t.Fatalf("expected Progress.CurrentTool='make e2e' during background wait, got %q", gotNow)
	}

	// Verify the bg-shell TurnItem stayed "running".
	var bgCmdItem *sessionv1.TurnItem
	for _, it := range midRun.Items {
		if it.Command == "make e2e" {
			bgCmdItem = it
		}
	}
	if bgCmdItem == nil || bgCmdItem.Status != "running" {
		t.Fatalf("expected make e2e TurnItem to remain status=running while background task runs, got %+v", bgCmdItem)
	}

	// Wait longer than IdleTTL (80ms) and trigger ReapIdle explicitly: live session MUST stay alive!
	time.Sleep(120 * time.Millisecond)
	eng.sup.ReapIdle(time.Now())
	if eng.sup.Get(midRun.Session.ActiveProviderSessionId) == nil {
		t.Fatalf("Supervisor.ReapIdle prematurely reaped a session with active background tasks!")
	}

	// 2. Wait for bg-shell wakeup -> completes on the SAME logical Run #1 (TurnCount==1)!
	var afterDone *sessionv1.SessionProjection
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, err := eng.GetSession(ctx, owner, sid)
		if err == nil && len(p.Runs) == 1 && p.Runs[0].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED && p.Session.ActiveBackgroundCount == 0 {
			afterDone = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if afterDone == nil {
		p, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for unified Run #1 completion; runs=%+v bg=%+v", p.GetRuns(), p.GetBackgroundTasks())
	}
	if afterDone.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || afterDone.Session.GetProgress().GetTurnCount() != 1 {
		t.Fatalf("expected Session IDLE with TurnCount=1 (no synthetic Run fragmentation), got status=%v turns=%d",
			afterDone.Session.Status, afterDone.Session.GetProgress().GetTurnCount())
	}
	if afterDone.Session.ActiveDaemonCount != 1 {
		t.Fatalf("expected daemon to remain running (ActiveDaemonCount=1), got %d", afterDone.Session.ActiveDaemonCount)
	}

	var settledCmd *sessionv1.TurnItem
	var sawForegroundMsg, sawWakeMsg bool
	for _, it := range afterDone.Items {
		if it.Command == "make e2e" {
			settledCmd = it
		}
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			if strings.Contains(it.Text, "Foreground reply complete") {
				sawForegroundMsg = true
			}
			if strings.Contains(it.Text, "Background verification succeeded!") {
				sawWakeMsg = true
			}
		}
	}
	if settledCmd == nil || settledCmd.Status != "completed" || !strings.Contains(settledCmd.Output, "42 e2e specs passed") {
		t.Fatalf("expected original make e2e TurnItem to settle to completed with output, got %+v", settledCmd)
	}
	if !sawForegroundMsg || !sawWakeMsg {
		t.Fatalf("expected both foreground and wakeup assistant messages preserved on Run #1, got fg=%v wake=%v", sawForegroundMsg, sawWakeMsg)
	}
	if afterDone.Session.Result != "Background verification succeeded!" {
		t.Fatalf("expected final Result='Background verification succeeded!', got %q", afterDone.Session.Result)
	}

	// 3. Stop the remaining daemon via StopBackgroundTask -> ActiveDaemonCount drops to 0!
	var daemonTaskID string
	for _, bt := range afterDone.BackgroundTasks {
		if bt.Kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON && bt.Status == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
			daemonTaskID = bt.TaskId
		}
	}
	if daemonTaskID == "" {
		t.Fatalf("expected running daemon task in projection, got %+v", afterDone.BackgroundTasks)
	}
	stopRes, err := eng.StopBackgroundTask(ctx, owner, &sessionv1.StopBackgroundTaskRequest{
		CommandId: "cmd_stop_daemon",
		SessionId: sid,
		TaskId:    daemonTaskID,
	})
	if err != nil {
		t.Fatalf("StopBackgroundTask: %v", err)
	}
	if stopRes.Session.ActiveDaemonCount != 0 || len(stopRes.StoppedTasks) != 1 || stopRes.StoppedTasks[0].Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
		t.Fatalf("unexpected StopBackgroundTask response: %+v", stopRes)
	}
}

func TestOutOfTurnPermissionGateCreatesContinuationRunAndCapturesCheckpoint(t *testing.T) {
	ctx := context.Background()
	t.Run("awaited_background_gate_stays_on_same_run", func(t *testing.T) {
		trigger := make(chan struct{})
		var gateCtxErr error

		drv := &customTestDriver{
			kind: "oot",
			caps: &sessionv1.ProviderCapabilities{NativeResume: true},
			onStart: func(_ context.Context, spec runtime.StartSpec) (runtime.Session, error) {
				ch := make(chan runtime.Event, 32)
				sess := &customTestSession{
					events: ch,
					onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
						out <- runtime.Event{Kind: runtime.EventToolStarted, ItemID: "bg_1", ToolName: "run_command", Command: "sleep 1 && deploy"}
						out <- runtime.Event{
							Kind:                runtime.EventBackgroundTask,
							TaskID:              "bg_1",
							ItemID:              "bg_1",
							ToolName:            "run_command",
							Command:             "sleep 1 && deploy",
							BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
							BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
							BackgroundAwaited:   true,
							BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
						}
						out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m_1", Text: "Started deploy watcher"}
						out <- runtime.Event{Kind: runtime.EventTurnCompleted}

						go func() {
							<-trigger
							res, err := spec.Gate(context.Background(), runtime.ToolGateRequest{
								Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
								Approval: &sessionv1.ToolApprovalPayload{
									ToolCategory: "command",
									ToolName:     "Bash",
									Command:      "npm publish",
								},
							})
							if err != nil {
								gateCtxErr = err
								return
							}
							out <- runtime.Event{
								Kind:                runtime.EventBackgroundTask,
								TaskID:              "bg_1",
								ItemID:              "bg_1",
								ToolName:            "run_command",
								Command:             "sleep 1 && deploy",
								BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
								BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
								BackgroundAwaited:   true,
								BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
								HasExitCode:         true,
								ExitCode:            0,
							}
							out <- runtime.Event{Kind: runtime.EventToolCompleted, ItemID: "bg_1", ExitCode: 0, Output: "watcher done"}
							if res.Allowed {
								out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m_2", Text: "Out-of-turn approval granted!"}
							}
							out <- runtime.Event{Kind: runtime.EventTurnCompleted}
						}()
					},
				}
				return sess, nil
			},
		}

		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_oot",
			Drivers:   map[string]runtime.Driver{"oot": drv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId: "oot-agent", Driver: "oot", DisplayName: "OOT", Enabled: true,
		})
		l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "oot-agent", Model: "m1"},
			RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
			InitialPrompt:  "deploy when ready",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := l.Session.SessionId
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p, _ := eng.GetSession(ctx, owner, sid)
			if p.GetSession().GetProgress().GetPhase() == "awaiting_background" {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		close(trigger)
		waitingProj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
		if gateCtxErr != nil {
			t.Fatalf("out-of-turn Gate failed: %v", gateCtxErr)
		}
		if len(waitingProj.Runs) != 1 {
			t.Fatalf("expected awaited background wakeup to stay on Run #1, got runs=%+v", waitingProj.Runs)
		}

		_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
			SessionId: sid,
			RequestId: waitingProj.Session.PendingRequestId,
			Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		})
		if err != nil {
			t.Fatalf("RespondRuntimeRequest: %v", err)
		}
		doneProj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
		if doneProj.Session.ActiveBackgroundCount != 0 || len(doneProj.Runs) != 1 || doneProj.Session.Result != "Out-of-turn approval granted!" {
			t.Fatalf("unexpected final state: activeBg=%d runs=%d result=%q", doneProj.Session.ActiveBackgroundCount, len(doneProj.Runs), doneProj.Session.Result)
		}
	})

	t.Run("daemon_wake_from_idle_spawns_continuation_run", func(t *testing.T) {
		trigger := make(chan struct{})
		var gateCtxErr error
		drv := &customTestDriver{
			kind: "oot_daemon",
			caps: &sessionv1.ProviderCapabilities{NativeResume: true},
			onStart: func(_ context.Context, spec runtime.StartSpec) (runtime.Session, error) {
				ch := make(chan runtime.Event, 32)
				return &customTestSession{
					events: ch,
					onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
						out <- runtime.Event{
							Kind:                runtime.EventBackgroundTask,
							TaskID:              "cron_1",
							ToolName:            "Standing cron",
							BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON,
							BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
							BackgroundAwaited:   false,
							BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
						}
						out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m_1", Text: "Cron armed"}
						out <- runtime.Event{Kind: runtime.EventTurnCompleted}
						go func() {
							<-trigger
							res, err := spec.Gate(context.Background(), runtime.ToolGateRequest{
								Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
								Approval: &sessionv1.ToolApprovalPayload{
									ToolCategory: "command",
									ToolName:     "Bash",
									Command:      "npm publish",
								},
							})
							if err != nil {
								gateCtxErr = err
								return
							}
							if res.Allowed {
								out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m_2", Text: "Cron publish approved!"}
							}
							out <- runtime.Event{Kind: runtime.EventTurnCompleted}
						}()
					},
				}, nil
			},
		}
		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_oot_daemon",
			Drivers:   map[string]runtime.Driver{"oot_daemon": drv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		owner := OwnerPrincipal("dev_owner", "Owner")
		_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId: "oot-daemon", Driver: "oot_daemon", DisplayName: "OOT Daemon", Enabled: true,
		})
		l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "oot-daemon", Model: "m1"},
			RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
			InitialPrompt:  "arm cron",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := l.Session.SessionId
		waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

		close(trigger)
		waitingProj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
		if gateCtxErr != nil {
			t.Fatalf("out-of-turn Gate failed: %v", gateCtxErr)
		}
		if len(waitingProj.Runs) != 2 || waitingProj.Runs[1].GetAuthor().GetKind() != sessionv1.ActorKind_ACTOR_KIND_SYSTEM {
			t.Fatalf("expected system Continuation Run #2 created when waking from IDLE, got runs=%+v", waitingProj.Runs)
		}
		_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
			SessionId: sid,
			RequestId: waitingProj.Session.PendingRequestId,
			Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		})
		if err != nil {
			t.Fatalf("RespondRuntimeRequest: %v", err)
		}
		waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	})
}

func TestHelperKernelMockACP(t *testing.T) {
	if os.Getenv("HERMESH_KERNEL_MOCK_ACP") != "1" {
		return
	}
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	toFileURI := func(path string) string {
		sl := filepath.ToSlash(path)
		if strings.HasPrefix(sl, "/") {
			return "file://" + sl
		}
		return "file:///" + sl
	}
	brainDir := os.Getenv("MOCK_KERNEL_AGY_BRAIN")
	tasksDir := filepath.Join(brainDir, ".system_generated", "tasks")
	msgsDir := filepath.Join(brainDir, ".system_generated", "messages")
	_ = os.MkdirAll(tasksDir, 0o755)
	log1 := filepath.Join(tasksDir, "task-1.log")
	log2 := filepath.Join(tasksDir, "task-2.log")

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{"agentCapabilities": map[string]any{"loadSession": true}}})
		case "session/new":
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{"sessionId": "agy_kern_sess"}})
		case "session/load", "session/set_mode", "session/set_config_option", "session/set_model":
			write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{}})
		case "session/prompt":
			promptID := *m.ID
			_ = os.WriteFile(log1, []byte("booting e2e runner...\n"), 0o644)
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "agy_tc_1",
						"title":         "run_command",
						"kind":          "execute",
						"status":        "completed",
						"rawInput": map[string]any{
							"CommandLine":       "go test ./e2e/...",
							"WaitMsBeforeAsync": 500,
							"toolSummary":       "E2E test execution",
						},
						"content": []any{
							map[string]any{
								"type": "content",
								"content": map[string]any{
									"type": "text",
									"text": "Tool is running as a background task with task id: conv_k/task-1\nTask logs are available at: " + toFileURI(log1) + "\n\nLog output:\nbooting e2e runner...\n\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: ...",
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
						"content":       map[string]any{"type": "text", "text": "Running E2E tests in background."},
					},
				},
			})
			write(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "end_turn"}})

			go func() {
				waitGate := func(name string) bool {
					p := filepath.Join(brainDir, name)
					for i := 0; i < 400; i++ {
						if _, err := os.Stat(p); err == nil {
							return true
						}
						time.Sleep(15 * time.Millisecond)
					}
					return false
				}
				if !waitGate("gate_wake_1") {
					return
				}
				_ = os.MkdirAll(msgsDir, 0o755)
				_ = os.WriteFile(log1, []byte("booting e2e runner...\nFAIL e2e/storage_test.go\n"), 0o644)
				_ = os.WriteFile(filepath.Join(msgsDir, "msg_1.json"), []byte(`{"sender":"conv_k/task-1","content":"Task id \"conv_k/task-1\" finished with result:\nCommand exited with code 2.\n"}`), 0o644)
				_ = os.WriteFile(log2, []byte("re-running e2e...\n"), 0o644)

				// Wakeup #1 launches agy_tc_2 out-of-turn before sending assistant text!
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "agy_tc_2",
							"title":         "run_command",
							"kind":          "execute",
							"status":        "completed",
							"rawInput": map[string]any{
								"CommandLine":       "TMPDIR=/tmp go test ./e2e/...",
								"WaitMsBeforeAsync": 500,
								"toolSummary":       "Retry E2E with short TMPDIR",
							},
							"content": []any{
								map[string]any{
									"type": "content",
									"content": map[string]any{
										"type": "text",
										"text": "Tool is running as a background task with task id: conv_k/task-2\nTask logs are available at: " + toFileURI(log2) + "\n\nLog output:\nre-running e2e...\n\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: ...",
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
							"content":       map[string]any{"type": "text", "text": "Retrying E2E with short TMPDIR."},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{"sessionUpdate": "usage_update", "used": 500, "size": 200000},
					},
				})

				if !waitGate("gate_wake_2") {
					return
				}
				_ = os.WriteFile(log2, []byte("re-running e2e...\nPASS ok e2e 1.1s\n"), 0o644)
				_ = os.WriteFile(filepath.Join(msgsDir, "msg_2.json"), []byte(`{"sender":"conv_k/task-2","content":"Task id \"conv_k/task-2\" finished with result:\nCommand exited with code 0.\n"}`), 0o644)
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "E2E passed cleanly!"},
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"update": map[string]any{"sessionUpdate": "usage_update", "used": 700, "size": 200000},
					},
				})
			}()
		}
	}
	os.Exit(0)
}

func TestAntigravityACPChainedBackgroundTasksThroughEngine(t *testing.T) {
	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	brainDir := t.TempDir()

	acpDrv := acp.NewDriver("")
	acpDrv.PostPromptMergeWindow = 40 * time.Millisecond
	acpDrv.OutOfTurnDebounce = 40 * time.Millisecond
	acpDrv.TaskLogPollInterval = 20 * time.Millisecond

	eng, err := Open(Config{
		DataDir:   t.TempDir(),
		MachineID: "m_agy_e2e",
		Drivers:   map[string]runtime.Driver{"acp": acpDrv},
		IdleTTL:   time.Minute,
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
		LaunchArgs:  []string{"-test.run=TestHelperKernelMockACP", "--", "acp"},
		Env: map[string]string{
			"HERMESH_KERNEL_MOCK_ACP": "1",
			"MOCK_KERNEL_AGY_BRAIN":   brainDir,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "antigravity"},
		InitialPrompt:  "run e2e tests",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sid := launched.Session.SessionId

	// 1. Wait for foreground segment to suspend in awaiting_background while agy_tc_1 stays RUNNING:
	var p1 *sessionv1.SessionProjection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.Runs) == 1 && cur.Session.GetProgress().GetPhase() == "awaiting_background" && cur.Session.ActiveBackgroundCount == 1 {
			p1 = cur
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p1 == nil || len(p1.Runs) != 1 || p1.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING || len(p1.BackgroundTasks) != 1 {
		t.Fatalf("after foreground segment: proj=%+v", p1)
	}
	run1ID := p1.Runs[0].RunId
	if p1.BackgroundTasks[0].RunId != run1ID || p1.BackgroundTasks[0].ItemId == "" {
		t.Fatalf("expected agy_tc_1 BackgroundTask linked to Run #1 (%s) and non-empty ItemId, got %+v", run1ID, p1.BackgroundTasks[0])
	}
	if gotNow := p1.Session.GetProgress().GetCurrentTool(); gotNow != "E2E test execution" {
		t.Fatalf("expected CurrentTool='E2E test execution', got %q", gotNow)
	}

	// 2. Append live log output while awaiting_background and verify Engine updates OutputTail on the same Run #1:
	log1 := filepath.Join(brainDir, ".system_generated", "tasks", "task-1.log")
	if err := os.WriteFile(log1, []byte("booting e2e runner...\nrunning TestStorage 1/3...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(4 * time.Second)
	sawTailInEngine := false
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.BackgroundTasks) == 1 && strings.Contains(cur.BackgroundTasks[0].OutputTail, "running TestStorage 1/3...") {
			if len(cur.Runs) != 1 || cur.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
				t.Fatalf("expected single RUNNING Run #1 during live log polling, got runs=%d status=%v", len(cur.Runs), cur.Session.Status)
			}
			sawTailInEngine = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawTailInEngine {
		t.Fatal("timed out waiting for live OutputTail update in Engine projection")
	}

	// 3. Trigger Wakeup #1 (task-1 fails with exit code 2, launches chained task-2 on the SAME Run #1):
	if err := os.WriteFile(filepath.Join(brainDir, "gate_wake_1"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var p2 *sessionv1.SessionProjection
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.BackgroundTasks) == 2 && cur.Session.GetProgress().GetPhase() == "awaiting_background" &&
			cur.Session.GetProgress().GetCurrentTool() == "Retry E2E with short TMPDIR" {
			p2 = cur
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p2 == nil {
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for Wakeup #1 chained handoff; session=%+v runs=%+v tasks=%+v", cur.GetSession(), cur.GetRuns(), cur.GetBackgroundTasks())
	}
	if len(p2.Runs) != 1 || p2.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING || p2.Session.ActiveBackgroundCount != 1 {
		t.Fatalf("expected Run #1 to stay RUNNING with ActiveBackgroundCount=1 (task-2 running), got runs=%d status=%v bg=%d",
			len(p2.Runs), p2.Session.Status, p2.Session.ActiveBackgroundCount)
	}
	btMap := map[string]*sessionv1.BackgroundTask{}
	for _, bt := range p2.BackgroundTasks {
		btMap[bt.TaskId] = bt
	}
	if btMap["agy_tc_1"] == nil ||
		btMap["agy_tc_1"].Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED ||
		btMap["agy_tc_1"].GetExitCode() != 2 ||
		btMap["agy_tc_1"].RunId != run1ID {
		t.Fatalf("expected agy_tc_1 FAILED(2) and still preserving RunId=%s, got %+v", run1ID, btMap["agy_tc_1"])
	}
	if btMap["agy_tc_2"] == nil ||
		btMap["agy_tc_2"].Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING ||
		btMap["agy_tc_2"].RunId != run1ID ||
		btMap["agy_tc_2"].ItemId == "" {
		t.Fatalf("expected agy_tc_2 RUNNING linked to Run #1 (%s) and non-empty ItemId, got %+v", run1ID, btMap["agy_tc_2"])
	}
	var item1, item2 *sessionv1.TurnItem
	for _, it := range p2.Items {
		if it.ItemId == btMap["agy_tc_1"].ItemId {
			item1 = it
		}
		if it.ItemId == btMap["agy_tc_2"].ItemId {
			item2 = it
		}
	}
	if item1 == nil || item1.Status != "failed" || item1.ExitCode != 2 {
		t.Fatalf("expected agy_tc_1 TurnItem status=failed exitCode=2, got %+v", item1)
	}
	if item2 == nil || item2.Status != "running" {
		t.Fatalf("expected agy_tc_2 TurnItem status=running while task-2 runs in background, got %+v", item2)
	}

	// 4. Trigger Wakeup #2 (task-2 finishes with exit code 0) -> single Run #1 completes and Session settles to IDLE!
	if err := os.WriteFile(filepath.Join(brainDir, "gate_wake_2"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var p3 *sessionv1.SessionProjection
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.Runs) == 1 && cur.Runs[0].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED && cur.Session.ActiveBackgroundCount == 0 {
			p3 = cur
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p3 == nil {
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for final Run #1 completion; session=%+v runs=%+v tasks=%+v", cur.GetSession(), cur.GetRuns(), cur.GetBackgroundTasks())
	}
	if p3.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || p3.Session.Result != "E2E passed cleanly!" {
		t.Fatalf("expected Session IDLE with Result='E2E passed cleanly!', got status=%v result=%q", p3.Session.Status, p3.Session.Result)
	}
	for _, bt := range p3.BackgroundTasks {
		if bt.TaskId == "agy_tc_2" && (bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED || bt.GetExitCode() != 0 || bt.RunId != run1ID) {
			t.Fatalf("expected agy_tc_2 COMPLETED(0) with preserved RunId=%s, got %+v", run1ID, bt)
		}
	}
}

func TestEngineInPlaceTodoAndFocusUpdates(t *testing.T) {
	ctx := context.Background()
	drv := &customTestDriver{
		kind: "todo_focus",
		caps: &sessionv1.ProviderCapabilities{NativeResume: true},
		onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
			ch := make(chan runtime.Event, 32)
			sess := &customTestSession{
				events: ch,
				onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
					out <- runtime.Event{Kind: runtime.EventFocusUpdated, Text: "Inspecting auth flow"}
					out <- runtime.Event{
						Kind:   runtime.EventTodoUpdated,
						ItemID: "todo",
						TodoSteps: []*sessionv1.TodoStep{
							{Text: "Inspect auth", Status: "in_progress"},
							{Text: "Run tests", Status: "pending"},
						},
					}
					out <- runtime.Event{
						Kind:     runtime.EventToolStarted,
						ItemID:   "tc_1",
						ToolName: "Bash",
						Command:  "go test ./...",
						Text:     "Unit test execution",
					}
					out <- runtime.Event{
						Kind:        runtime.EventToolCompleted,
						ItemID:      "tc_1",
						ToolName:    "Bash",
						Command:     "go test ./...",
						ExitCode:    0,
						HasExitCode: true,
						Output:      "ok",
					}
					out <- runtime.Event{
						Kind:   runtime.EventTodoUpdated,
						ItemID: "todo",
						TodoSteps: []*sessionv1.TodoStep{
							{Text: "Inspect auth", Status: "completed"},
							{Text: "Run tests", Status: "completed"},
						},
					}
					out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m1", Text: "Done."}
					out <- runtime.Event{Kind: runtime.EventTurnCompleted}
				},
			}
			return sess, nil
		},
	}

	eng, err := Open(Config{
		DataDir:   t.TempDir(),
		MachineID: "m_todo",
		Drivers:   map[string]runtime.Driver{"todo_focus": drv},
		IdleTTL:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	owner := OwnerPrincipal("dev_owner", "Owner")
	if _, err := eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "todo-agent",
		Driver:      "todo_focus",
		DisplayName: "Todo Focus",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "todo-agent", Model: "m1"},
		InitialPrompt:  "ship it",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	var proj *sessionv1.SessionProjection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.Runs) == 1 && cur.Runs[0].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			proj = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if proj == nil {
		t.Fatal("timed out waiting for run completion")
	}

	var todos []*sessionv1.TurnItem
	var cmdItem *sessionv1.TurnItem
	for _, it := range proj.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_TODO_LIST {
			todos = append(todos, it)
		}
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION {
			cmdItem = it
		}
	}
	if len(todos) != 1 {
		t.Fatalf("expected exactly 1 in-place TODO_LIST item per run, got %d: %+v", len(todos), todos)
	}
	if todos[0].Text != "Inspecting auth flow" {
		t.Fatalf("expected focus Text preserved across HasFocus=false TodoUpdated, got %q", todos[0].Text)
	}
	if len(todos[0].TodoSteps) != 2 || todos[0].TodoSteps[0].Status != "completed" || todos[0].TodoSteps[1].Status != "completed" {
		t.Fatalf("expected latest TodoSteps persisted in place, got %+v", todos[0].TodoSteps)
	}
	if cmdItem == nil || cmdItem.Text != "Unit test execution" || cmdItem.Command != "go test ./..." {
		t.Fatalf("expected command TurnItem to preserve Text summary and Command, got %+v", cmdItem)
	}
}

func TestForceInterruptAndProviderSwitchSettleBackgroundTasks(t *testing.T) {
	ctx := context.Background()

	t.Run("force_interrupt_settles_running_background_and_daemon_tasks", func(t *testing.T) {
		hangCh := make(chan struct{})
		t.Cleanup(func() { close(hangCh) })

		drv := &customTestDriver{
			kind: "hang_bg",
			caps: &sessionv1.ProviderCapabilities{NativeResume: true},
			onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
				ch := make(chan runtime.Event, 32)
				return &customTestSession{
					events: ch,
					onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
						out <- runtime.Event{Kind: runtime.EventToolStarted, ItemID: "it_cmd", ToolName: "Bash", Command: "sleep 999"}
						out <- runtime.Event{
							Kind:                runtime.EventBackgroundTask,
							TaskID:              "bg_cmd_1",
							ItemID:              "it_cmd",
							ToolName:            "Long build",
							Command:             "sleep 999",
							BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
							BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
							BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
							BackgroundAwaited:   true,
						}
						out <- runtime.Event{
							Kind:                runtime.EventBackgroundTask,
							TaskID:              "bg_daemon_1",
							ToolName:            "Dev server",
							Command:             "pnpm dev",
							BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON,
							BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
							BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
							BackgroundAwaited:   false,
						}
						<-hangCh
					},
				}, nil
			},
		}

		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_force_bg",
			Drivers:   map[string]runtime.Driver{"hang_bg": drv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId: "hang-agent", Driver: "hang_bg", DisplayName: "Hang", Enabled: true,
		})
		started, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "hang-agent", Model: "m1"},
			InitialPrompt:  "start bg and hang",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := started.Session.SessionId

		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p, _ := eng.GetSession(ctx, owner, sid)
			if p.GetSession().GetActiveBackgroundCount() == 1 && p.GetSession().GetActiveDaemonCount() == 1 {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		// Force-interrupt must settle both background tasks and reset counts to 0 immediately.
		intRes, err := eng.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{
			SessionId: sid,
			Force:     true,
		})
		if err != nil {
			t.Fatalf("InterruptSession(Force=true): %v", err)
		}
		if intRes.Session.ActiveBackgroundCount != 0 || intRes.Session.ActiveDaemonCount != 0 {
			t.Fatalf("expected ActiveBackgroundCount=0 ActiveDaemonCount=0 after Force interrupt, got bg=%d daemon=%d",
				intRes.Session.ActiveBackgroundCount, intRes.Session.ActiveDaemonCount)
		}
		proj, err := eng.GetSession(ctx, owner, sid)
		if err != nil {
			t.Fatal(err)
		}
		for _, bt := range proj.BackgroundTasks {
			if bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
				t.Fatalf("expected background task %s STOPPED after Force interrupt, got %+v", bt.TaskId, bt)
			}
		}
		for _, it := range proj.Items {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION && it.Status == "running" {
				t.Fatalf("linked TurnItem remained running after Force interrupt: %+v", it)
			}
		}
	})

	t.Run("fresh_context_provider_replacement_settles_old_tasks_without_clobbering_new", func(t *testing.T) {
		var sessionNum int
		drv := &customTestDriver{
			kind: "switch_bg",
			caps: &sessionv1.ProviderCapabilities{NativeResume: true},
			onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
				sessionNum++
				n := sessionNum
				ch := make(chan runtime.Event, 32)
				return &customTestSession{
					events: ch,
					onTurn: func(_ context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
						out <- runtime.Event{
							Kind:                runtime.EventBackgroundTask,
							TaskID:              fmt.Sprintf("daemon_ps_%d", n),
							ToolName:            fmt.Sprintf("Daemon #%d", n),
							Command:             "pnpm dev",
							BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON,
							BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
							BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
							BackgroundAwaited:   false,
						}
						out <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "m1", Text: fmt.Sprintf("ps_%d_ok", n)}
						out <- runtime.Event{Kind: runtime.EventTurnCompleted}
					},
				}, nil
			},
		}

		eng, err := Open(Config{
			DataDir:   t.TempDir(),
			MachineID: "m_switch_bg",
			Drivers:   map[string]runtime.Driver{"switch_bg": drv},
			IdleTTL:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()

		owner := OwnerPrincipal("dev_owner", "Owner")
		_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
			InstanceId: "sw-agent", Driver: "switch_bg", DisplayName: "Switch", Enabled: true,
		})
		started, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "sw-agent", Model: "m1"},
			InitialPrompt:  "turn 1",
		})
		if err != nil {
			t.Fatal(err)
		}
		sid := started.Session.SessionId

		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p, _ := eng.GetSession(ctx, owner, sid)
			if len(p.GetRuns()) == 1 && p.Runs[0].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		// Dispatch Turn #2 with FRESH_CONTEXT -> replaces PS #1 with PS #2:
		if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
			SessionId: sid,
			Text:      "turn 2 fresh",
			Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT,
		}); err != nil {
			t.Fatal(err)
		}
		var finalProj *sessionv1.SessionProjection
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p, _ := eng.GetSession(ctx, owner, sid)
			if len(p.GetRuns()) == 2 && p.Runs[1].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
				finalProj = p
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if finalProj == nil {
			t.Fatal("timed out waiting for Turn #2 completion")
		}
		if finalProj.Session.ActiveDaemonCount != 1 {
			t.Fatalf("expected ActiveDaemonCount=1 (only PS #2 daemon running), got %d; tasks=%+v",
				finalProj.Session.ActiveDaemonCount, finalProj.BackgroundTasks)
		}
		for _, bt := range finalProj.BackgroundTasks {
			switch bt.TaskId {
			case "daemon_ps_1":
				if bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
					t.Fatalf("expected daemon_ps_1 STOPPED when PS #1 was replaced, got %+v", bt)
				}
			case "daemon_ps_2":
				if bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
					t.Fatalf("expected daemon_ps_2 RUNNING on PS #2, got %+v", bt)
				}
			}
		}
	})
}
