package kernel

import (
	"bufio"
	"context"
	"encoding/json"
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

	// 1. Wait for Run #1 to complete while bg-shell (350ms) and bg-daemon are still running.
	var afterRun1 *sessionv1.SessionProjection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, err := eng.GetSession(ctx, owner, sid)
		if err == nil && len(p.Runs) == 1 && p.Runs[0].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			afterRun1 = p
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if afterRun1 == nil {
		t.Fatalf("timed out waiting for Run #1 completion")
	}
	if afterRun1.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE {
		t.Fatalf("Session.Status after Run #1 = %v, want IDLE", afterRun1.Session.Status)
	}
	if afterRun1.Session.ActiveBackgroundCount != 1 || afterRun1.Session.ActiveDaemonCount != 1 {
		t.Fatalf("expected ActiveBackgroundCount=1 ActiveDaemonCount=1, got bg=%d daemon=%d",
			afterRun1.Session.ActiveBackgroundCount, afterRun1.Session.ActiveDaemonCount)
	}

	// Verify the bg-shell TurnItem stayed "running" (NOT prematurely settled by SettleRunItems).
	var bgCmdItem *sessionv1.TurnItem
	for _, it := range afterRun1.Items {
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
	if eng.sup.Get(afterRun1.Session.ActiveProviderSessionId) == nil {
		t.Fatalf("Supervisor.ReapIdle prematurely reaped a session with active background tasks!")
	}

	// 2. Wait for bg-shell wakeup -> automatic system Continuation Run #2 completes!
	var afterCont *sessionv1.SessionProjection
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, err := eng.GetSession(ctx, owner, sid)
		if err == nil && len(p.Runs) >= 2 && p.Runs[1].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED && p.Session.ActiveBackgroundCount == 0 {
			afterCont = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if afterCont == nil {
		p, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for Continuation Run #2; runs=%+v bg=%+v", p.GetRuns(), p.GetBackgroundTasks())
	}

	contRun := afterCont.Runs[1]
	if contRun.GetAuthor().GetKind() != sessionv1.ActorKind_ACTOR_KIND_SYSTEM || contRun.WakeReason == "" {
		t.Fatalf("expected Continuation Run #2 to have SYSTEM author and WakeReason, got author=%+v wake=%q", contRun.GetAuthor(), contRun.WakeReason)
	}
	if afterCont.Session.ActiveDaemonCount != 1 {
		t.Fatalf("expected daemon to remain running (ActiveDaemonCount=1), got %d", afterCont.Session.ActiveDaemonCount)
	}

	var settledCmd *sessionv1.TurnItem
	var sawWakeMsg bool
	for _, it := range afterCont.Items {
		if it.Command == "make e2e" {
			settledCmd = it
		}
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && strings.Contains(it.Text, "Background verification succeeded!") {
			sawWakeMsg = true
		}
	}
	if settledCmd == nil || settledCmd.Status != "completed" || !strings.Contains(settledCmd.Output, "42 e2e specs passed") {
		t.Fatalf("expected original make e2e TurnItem to settle to completed with output, got %+v", settledCmd)
	}
	if !sawWakeMsg {
		t.Fatalf("expected assistant message from Continuation Run #2")
	}

	// 3. Stop the remaining daemon via StopBackgroundTask -> ActiveDaemonCount drops to 0!
	var daemonTaskID string
	for _, bt := range afterCont.BackgroundTasks {
		if bt.Kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON && bt.Status == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
			daemonTaskID = bt.TaskId
		}
	}
	if daemonTaskID == "" {
		t.Fatalf("expected running daemon task in projection, got %+v", afterCont.BackgroundTasks)
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
	trigger := make(chan struct{})
	var gateCtxErr error

	drv := &customTestDriver{
		kind: "oot",
		caps: &sessionv1.ProviderCapabilities{NativeResume: true},
		onStart: func(_ context.Context, spec runtime.StartSpec) (runtime.Session, error) {
			ch := make(chan runtime.Event, 32)
			sess := &customTestSession{
				events: ch,
				onTurn: func(turnCtx context.Context, _ runtime.TurnInput, out chan<- runtime.Event) {
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

					// After Turn #1's turnCtx is cancelled by driveRun returning, simulate an out-of-turn wakeup
					// that requests permission via spec.Gate using a live session context!
					go func() {
						<-trigger
						// Verify turnCtx is indeed already cancelled, while out-of-turn Gate with background ctx works!
						_ = turnCtx.Err()
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
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	// Trigger out-of-turn wakeup requiring permission!
	close(trigger)
	waitingProj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	if gateCtxErr != nil {
		t.Fatalf("out-of-turn Gate failed: %v", gateCtxErr)
	}
	if len(waitingProj.Runs) != 2 || waitingProj.Runs[1].GetAuthor().GetKind() != sessionv1.ActorKind_ACTOR_KIND_SYSTEM {
		t.Fatalf("expected system Continuation Run #2 created for out-of-turn request, got runs=%+v", waitingProj.Runs)
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
	if doneProj.Session.ActiveBackgroundCount != 0 {
		t.Fatalf("expected ActiveBackgroundCount=0 after wakeup completion, got %d", doneProj.Session.ActiveBackgroundCount)
	}
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

	// 1. Wait for Run #1 to finish IDLE while agy_tc_1 stays RUNNING:
	p1 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if len(p1.Runs) != 1 || p1.Session.ActiveBackgroundCount != 1 || len(p1.BackgroundTasks) != 1 {
		t.Fatalf("after Run #1: runs=%d activeBg=%d tasks=%+v", len(p1.Runs), p1.Session.ActiveBackgroundCount, p1.BackgroundTasks)
	}
	run1ID := p1.Runs[0].RunId
	if p1.BackgroundTasks[0].RunId != run1ID || p1.BackgroundTasks[0].ItemId == "" {
		t.Fatalf("expected agy_tc_1 BackgroundTask linked to Run #1 (%s) and non-empty ItemId, got %+v", run1ID, p1.BackgroundTasks[0])
	}

	// 2. Append live log output while IDLE and verify Engine updates OutputTail without spawning a phantom Continuation Run:
	log1 := filepath.Join(brainDir, ".system_generated", "tasks", "task-1.log")
	if err := os.WriteFile(log1, []byte("booting e2e runner...\nrunning TestStorage 1/3...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	sawTailInEngine := false
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.BackgroundTasks) == 1 && strings.Contains(cur.BackgroundTasks[0].OutputTail, "running TestStorage 1/3...") {
			if len(cur.Runs) != 1 {
				t.Fatalf("live log polling must not create a Continuation Run, got %d runs", len(cur.Runs))
			}
			sawTailInEngine = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawTailInEngine {
		t.Fatal("timed out waiting for live OutputTail update in Engine projection")
	}

	// 3. Trigger Wakeup #1 (task-1 fails with exit code 2, launches chained task-2):
	if err := os.WriteFile(filepath.Join(brainDir, "gate_wake_1"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var p2 *sessionv1.SessionProjection
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.Runs) == 2 && cur.Runs[1].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			p2 = cur
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p2 == nil {
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for Continuation Run #2 completion; runs=%+v tasks=%+v", cur.GetRuns(), cur.GetBackgroundTasks())
	}
	run2ID := p2.Runs[1].RunId
	if p2.Session.ActiveBackgroundCount != 1 {
		t.Fatalf("expected ActiveBackgroundCount=1 (task-2 still running) after Run #2, got %d", p2.Session.ActiveBackgroundCount)
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
		btMap["agy_tc_2"].RunId != run2ID ||
		btMap["agy_tc_2"].ItemId == "" {
		t.Fatalf("expected agy_tc_2 RUNNING linked to Continuation Run #2 (%s) and non-empty ItemId, got %+v", run2ID, btMap["agy_tc_2"])
	}
	// Verify agy_tc_2's TurnItem in Run #2 stayed "running" (was NOT prematurely marked completed when Run #2 finished):
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

	// 4. Trigger Wakeup #2 (task-2 finishes with exit code 0):
	if err := os.WriteFile(filepath.Join(brainDir, "gate_wake_2"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var p3 *sessionv1.SessionProjection
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := eng.GetSession(ctx, owner, sid)
		if gErr == nil && len(cur.Runs) == 3 && cur.Runs[2].Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED && cur.Session.ActiveBackgroundCount == 0 {
			p3 = cur
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p3 == nil {
		cur, _ := eng.GetSession(ctx, owner, sid)
		t.Fatalf("timed out waiting for Continuation Run #3 completion; runs=%+v tasks=%+v", cur.GetRuns(), cur.GetBackgroundTasks())
	}
	for _, bt := range p3.BackgroundTasks {
		if bt.TaskId == "agy_tc_2" && (bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED || bt.GetExitCode() != 0 || bt.RunId != run2ID) {
			t.Fatalf("expected agy_tc_2 COMPLETED(0) with preserved RunId=%s, got %+v", run2ID, bt)
		}
	}
}
