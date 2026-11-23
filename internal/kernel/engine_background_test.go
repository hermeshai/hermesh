package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/runtime"
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

	launched, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ProjectId: "bgproj",
		InitialPrompt: strings.Join([]string{
			"bg-daemon npm run dev|listening on :5173",
			"bg-shell 350ms|make e2e|42 e2e specs passed|Background verification succeeded!",
			"say Foreground reply complete; waiting on make e2e.",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("LaunchThread: %v", err)
	}
	tid := launched.Thread.ThreadId

	// 1. Wait for Run #1 to complete while bg-shell (350ms) and bg-daemon are still running.
	var afterRun1 *threadv2.ThreadProjection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, err := eng.GetThread(ctx, owner, tid)
		if err == nil && len(p.Runs) == 1 && p.Runs[0].Status == threadv2.RunStatus_RUN_STATUS_COMPLETED {
			afterRun1 = p
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if afterRun1 == nil {
		t.Fatalf("timed out waiting for Run #1 completion")
	}
	if afterRun1.Thread.Status != threadv2.ThreadStatus_THREAD_STATUS_IDLE {
		t.Fatalf("Thread.Status after Run #1 = %v, want IDLE", afterRun1.Thread.Status)
	}
	if afterRun1.Thread.ActiveBackgroundCount != 1 || afterRun1.Thread.ActiveDaemonCount != 1 {
		t.Fatalf("expected ActiveBackgroundCount=1 ActiveDaemonCount=1, got bg=%d daemon=%d",
			afterRun1.Thread.ActiveBackgroundCount, afterRun1.Thread.ActiveDaemonCount)
	}

	// Verify the bg-shell TurnItem stayed "running" (NOT prematurely settled by SettleRunItems).
	var bgCmdItem *threadv2.TurnItem
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
	if eng.sup.Get(afterRun1.Thread.ActiveProviderThreadId) == nil {
		t.Fatalf("Supervisor.ReapIdle prematurely reaped a session with active background tasks!")
	}

	// 2. Wait for bg-shell wakeup -> automatic system Continuation Run #2 completes!
	var afterCont *threadv2.ThreadProjection
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, err := eng.GetThread(ctx, owner, tid)
		if err == nil && len(p.Runs) >= 2 && p.Runs[1].Status == threadv2.RunStatus_RUN_STATUS_COMPLETED && p.Thread.ActiveBackgroundCount == 0 {
			afterCont = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if afterCont == nil {
		p, _ := eng.GetThread(ctx, owner, tid)
		t.Fatalf("timed out waiting for Continuation Run #2; runs=%+v bg=%+v", p.GetRuns(), p.GetBackgroundTasks())
	}

	contRun := afterCont.Runs[1]
	if contRun.GetAuthor().GetKind() != threadv2.ActorKind_ACTOR_KIND_SYSTEM || contRun.WakeReason == "" {
		t.Fatalf("expected Continuation Run #2 to have SYSTEM author and WakeReason, got author=%+v wake=%q", contRun.GetAuthor(), contRun.WakeReason)
	}
	if afterCont.Thread.ActiveDaemonCount != 1 {
		t.Fatalf("expected daemon to remain running (ActiveDaemonCount=1), got %d", afterCont.Thread.ActiveDaemonCount)
	}

	var settledCmd *threadv2.TurnItem
	var sawWakeMsg bool
	for _, it := range afterCont.Items {
		if it.Command == "make e2e" {
			settledCmd = it
		}
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && strings.Contains(it.Text, "Background verification succeeded!") {
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
		if bt.Kind == threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON && bt.Status == threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
			daemonTaskID = bt.TaskId
		}
	}
	if daemonTaskID == "" {
		t.Fatalf("expected running daemon task in projection, got %+v", afterCont.BackgroundTasks)
	}
	stopRes, err := eng.StopBackgroundTask(ctx, owner, &threadv2.StopBackgroundTaskRequest{
		CommandId: "cmd_stop_daemon",
		ThreadId:  tid,
		TaskId:    daemonTaskID,
	})
	if err != nil {
		t.Fatalf("StopBackgroundTask: %v", err)
	}
	if stopRes.Thread.ActiveDaemonCount != 0 || len(stopRes.StoppedTasks) != 1 || stopRes.StoppedTasks[0].Status != threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
		t.Fatalf("unexpected StopBackgroundTask response: %+v", stopRes)
	}
}

func TestOutOfTurnPermissionGateCreatesContinuationRunAndCapturesCheckpoint(t *testing.T) {
	ctx := context.Background()
	trigger := make(chan struct{})
	var gateCtxErr error

	drv := &customTestDriver{
		kind: "oot",
		caps: &threadv2.ProviderCapabilities{NativeResume: true},
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
						BackgroundKind:      threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
						BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
						BackgroundAwaited:   true,
						BackgroundStopScope: threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
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
							Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
							Approval: &threadv2.ToolApprovalPayload{
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
							BackgroundKind:      threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
							BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
							BackgroundAwaited:   true,
							BackgroundStopScope: threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
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
	_, _ = eng.UpsertProviderInstance(ctx, owner, &threadv2.ProviderInstanceConfig{
		InstanceId: "oot-agent", Driver: "oot", DisplayName: "OOT", Enabled: true,
	})
	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ModelSelection: &threadv2.ModelSelection{InstanceId: "oot-agent", Model: "m1"},
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
		InitialPrompt:  "deploy when ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := l.Thread.ThreadId
	waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)

	// Trigger out-of-turn wakeup requiring permission!
	close(trigger)
	waitingProj := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	if gateCtxErr != nil {
		t.Fatalf("out-of-turn Gate failed: %v", gateCtxErr)
	}
	if len(waitingProj.Runs) != 2 || waitingProj.Runs[1].GetAuthor().GetKind() != threadv2.ActorKind_ACTOR_KIND_SYSTEM {
		t.Fatalf("expected system Continuation Run #2 created for out-of-turn request, got runs=%+v", waitingProj.Runs)
	}

	_, err = eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:  tid,
		RequestId: waitingProj.Thread.PendingRequestId,
		Decision:  threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest: %v", err)
	}
	doneProj := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if doneProj.Thread.ActiveBackgroundCount != 0 {
		t.Fatalf("expected ActiveBackgroundCount=0 after wakeup completion, got %d", doneProj.Thread.ActiveBackgroundCount)
	}
}
