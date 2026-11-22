package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
)

func testGrant(maxMode identityv1.RuntimeMode) *Principal {
	return GrantPrincipal(&identityv1.Grant{
		GrantId:        "gr_review",
		ClientId:       "cli_review",
		ClientLabel:    "Review Bot",
		ProjectIds:     []string{"repo"},
		Agents:         []string{"scripted"},
		MaxRuntimeMode: maxMode,
		TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_OWN,
	})
}

func runStatus(t *testing.T, eng *Engine, tid, runID string) threadv2.RunStatus {
	t.Helper()
	proj, err := eng.GetThread(context.Background(), OwnerPrincipal("o", "O"), tid)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range proj.Runs {
		if r.RunId == runID {
			return r.Status
		}
	}
	return threadv2.RunStatus_RUN_STATUS_UNSPECIFIED
}

// Regression: owner approves "git push origin feature" for the thread;
// the same turn's "git push --force origin main" then runs with no approval.
func TestThreadRuleNeverPreapprovesForcePush(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ProjectId:     "repo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
		InitialPrompt: "shell git push origin feature|pushed\nshell git push --force origin main|FORCED",
	})
	if err != nil {
		t.Fatal(err)
	}
	proj := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	if _, err := eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId: l.Thread.ThreadId, RequestId: proj.Thread.PendingRequestId,
		Decision: threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_THREAD,
	}); err != nil {
		t.Fatal(err)
	}
	final := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	ran := false
	for _, it := range final.Items {
		if it.Command == "git push --force origin main" && it.Status == "completed" {
			ran = true
		}
	}
	t.Logf("requests=%d forcePushExecuted=%v status=%v", len(final.Requests), ran, final.Thread.Status)
	if ran && len(final.Requests) == 1 {
		t.Errorf("force push executed without its own approval (rule %q)", final.Requests[0].GetApproval().GetRuleKey())
	}
}

// Regression: a message queued by the owner rewrites thread.EffectiveRuntimeMode, which the live
// gate of the grant's ALREADY RUNNING turn reads -> the grant's turn escalates to FULL_ACCESS.
func TestQueuedDispatchDoesNotChangeActiveRunMode(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS)

	l, err := eng.LaunchThread(ctx, grant, &threadv2.LaunchThreadRequest{
		ProjectId:     "repo",
		InitialPrompt: "sleep 700ms\nshell npm install evil-pkg|installed",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: l.Thread.ThreadId, Text: "say later", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
	}); err != nil {
		t.Fatal(err)
	}
	final := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	t.Logf("grant run mode=%v, thread status=%v, requests=%d", l.InitialRun.RuntimeMode, final.Thread.Status, len(final.Requests))
	if len(final.Requests) == 0 {
		t.Errorf("grant (ceiling AUTO_EDITS) ran `npm install` without approval after owner queued a FULL_ACCESS message")
	}
}

// Regression: a cosmetic owner action (pin) on a grant-created thread recomputes the ceiling with
// the owner's (empty) grant -> MaxRuntimeMode/EffectiveRuntimeMode jump to FULL_ACCESS.
func TestPinKeepsGrantCeiling(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED)

	l, err := eng.LaunchThread(ctx, grant, &threadv2.LaunchThreadRequest{
		ProjectId: "repo", RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.UpdateThreadConfig(ctx, owner, &threadv2.UpdateThreadConfigRequest{
		ThreadId: l.Thread.ThreadId, HasPinned: true, Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before pin: max=%v eff=%v; after pin: max=%v eff=%v",
		l.Thread.MaxRuntimeMode, l.Thread.EffectiveRuntimeMode, res.Thread.MaxRuntimeMode, res.Thread.EffectiveRuntimeMode)
	if res.Thread.MaxRuntimeMode != l.Thread.MaxRuntimeMode || res.Thread.EffectiveRuntimeMode != l.Thread.EffectiveRuntimeMode {
		t.Errorf("pinning changed runtime ceiling/mode of a grant-created thread")
	}
}

// Regression: ForkThread never checks allowsAgent on req.ModelSelection, and DispatchMessage only
// checks overrides -> a grant restricted to "scripted" runs on "alt-agent".
func TestForkRespectsGrantAgentAllowList(t *testing.T) {
	ctx := context.Background()
	eng, _, drv2 := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO)

	l, err := eng.LaunchThread(ctx, grant, &threadv2.LaunchThreadRequest{ProjectId: "repo", InitialPrompt: "say hi"})
	if err != nil {
		t.Fatal(err)
	}
	waitForThreadStatus(t, eng, grant, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	_, err = eng.ForkThread(ctx, grant, &threadv2.ForkThreadRequest{
		SourceThreadId: l.Thread.ThreadId,
		ModelSelection: &threadv2.ModelSelection{InstanceId: "alt-agent"},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("fork onto a disallowed provider: got %v, want PermissionDenied", err)
	}

	// A thread the owner moved to alt-agent must not let the grant run turns there either.
	owner := OwnerPrincipal("dev_owner", "Owner")
	if _, err := eng.UpdateThreadConfig(ctx, owner, &threadv2.UpdateThreadConfigRequest{
		ThreadId: l.Thread.ThreadId, ModelSelection: &threadv2.ModelSelection{InstanceId: "alt-agent"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ForkThread(ctx, grant, &threadv2.ForkThreadRequest{SourceThreadId: l.Thread.ThreadId}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("fork inheriting a disallowed provider: got %v, want PermissionDenied", err)
	}
	if _, err := eng.DispatchMessage(ctx, grant, &threadv2.DispatchMessageRequest{ThreadId: l.Thread.ThreadId, Text: "say from alt"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("dispatch onto a disallowed provider: got %v, want PermissionDenied", err)
	}
	if drv2.StartCount() != 0 {
		t.Fatalf("alt driver started %d sessions for a grant limited to scripted", drv2.StartCount())
	}
}

// Regression: tool output is truncated at byte 32768, splitting a UTF-8 rune; proto.Marshal then
// rejects the item, and the error is swallowed -> the command item silently disappears.
func TestToolOutputTruncationKeepsUTF8(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	out := strings.Repeat("中", 12000) // 36000 bytes; 32768 % 3 != 0
	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "shell cat big.txt|" + out + "\nsay done",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	found := false
	for _, it := range final.Items {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION {
			found = true
			t.Logf("command item status=%q outputLen=%d truncated=%v", it.Status, len(it.Output), it.OutputTruncated)
			if it.Status != "completed" || it.Output == "" {
				t.Errorf("completed write of >32KiB CJK output was dropped; item stuck at %q with empty output", it.Status)
			}
		}
	}
	if !found {
		t.Errorf("command item missing")
	}
}

// Regression: receipts are checked outside the write transaction -> concurrent retries of the
// same command_id each create a thread.
func TestConcurrentRetriesOfOneCommandRunOnce(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	var wg sync.WaitGroup
	ids := sync.Map{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{CommandId: "cmd_same", Title: "x"})
			if err == nil {
				ids.Store(res.Thread.ThreadId, true)
			}
		}()
	}
	wg.Wait()
	all, _ := eng.ListThreads(ctx, owner, "", true, 0)
	n := 0
	ids.Range(func(_, _ any) bool { n++; return true })
	t.Logf("distinct thread ids returned=%d, threads stored=%d", n, len(all))
	if len(all) > 1 {
		t.Errorf("one command_id created %d threads", len(all))
	}
}

// Regression: InterruptRun trusts req.RunId without checking it belongs to req.ThreadId.
// A grant force-interrupting ITS OWN thread with ANOTHER thread's run id kills that run.
func TestInterruptRejectsForeignRunID(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO)

	ownerTh, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "repo", InitialPrompt: "sleep 3s\nsay owner done"})
	if err != nil {
		t.Fatal(err)
	}
	grantTh, err := eng.LaunchThread(ctx, grant, &threadv2.LaunchThreadRequest{ProjectId: "repo", Title: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.GetThread(ctx, grant, ownerTh.Thread.ThreadId); err == nil {
		t.Fatal("grant should not see owner thread")
	}
	time.Sleep(100 * time.Millisecond)
	_, err = eng.InterruptRun(ctx, grant, &threadv2.InterruptRunRequest{
		ThreadId: grantTh.Thread.ThreadId, RunId: ownerTh.InitialRun.RunId, Force: true,
	})
	t.Logf("InterruptRun err=%v", err)
	time.Sleep(300 * time.Millisecond)
	st := runStatus(t, eng, ownerTh.Thread.ThreadId, ownerTh.InitialRun.RunId)
	t.Logf("owner run status after grant call = %v", st)
	if st != threadv2.RunStatus_RUN_STATUS_RUNNING {
		t.Errorf("grant killed a run in a thread it cannot view (status=%v)", st)
	}
}

// Regression: forking a WORKTREE thread creates the new worktree from the branch HEAD, so the
// source thread's (uncommitted) file changes are missing although history is copied.
func TestForkWorktreeCarriesSourceFiles(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "repo", InitialPrompt: "write feature.txt|work in progress"})
	if err != nil {
		t.Fatal(err)
	}
	waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	f, err := eng.ForkThread(ctx, owner, &threadv2.ForkThreadRequest{SourceThreadId: l.Thread.ThreadId})
	if err != nil {
		t.Fatal(err)
	}
	_, srcErr := os.Stat(filepath.Join(l.Thread.Workspace.WorktreePath, "feature.txt"))
	_, forkErr := os.Stat(filepath.Join(f.Thread.Workspace.WorktreePath, "feature.txt"))
	t.Logf("source has feature.txt=%v, fork worktree=%q has it=%v", srcErr == nil, f.Thread.Workspace.WorktreePath, forkErr == nil)
	if srcErr == nil && forkErr != nil {
		t.Errorf("forked thread's workspace is missing the source thread's file changes")
	}
}

// Regression: RollbackCheckpoint(restore_files) in an IN_PLACE repo rewrites the user's real
// .git/index (read-tree --reset ... read-tree HEAD) -> staged-but-uncommitted work is unstaged.
func TestRollbackKeepsUserIndex(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, _ := cmd.CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	_ = os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// staged by user\n"), 0o644)
	git("add", "main.go")
	before := git("diff", "--cached", "--name-only")

	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "repo",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, InitialPrompt: "write agent.txt|agent"})
	if err != nil {
		t.Fatal(err)
	}
	waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	ckpts, _ := eng.ListCheckpoints(ctx, owner, l.Thread.ThreadId)
	if len(ckpts) == 0 || ckpts[0].RunOrdinal != 0 {
		t.Fatalf("no baseline checkpoint: %v", ckpts)
	}
	if _, err := eng.RollbackCheckpoint(ctx, owner, &threadv2.RollbackCheckpointRequest{
		ThreadId: l.Thread.ThreadId, CheckpointId: ckpts[0].CheckpointId, RestoreFiles: true,
	}); err != nil {
		t.Fatal(err)
	}
	after := git("diff", "--cached", "--name-only")
	_, agentErr := os.Stat(filepath.Join(repo, "agent.txt"))
	t.Logf("staged before=%q after=%q agent.txt removed=%v", before, after, agentErr != nil)
	if before != "" && after == "" {
		t.Errorf("rollback unstaged the user's staged change to %s", before)
	}
}

// Regression: interrupt-drain path does not copy the queued run's mode onto the thread
// (completion path does), so the drained run's gate uses the wrong mode.
func TestInterruptDrainUsesQueuedRunMode(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "repo",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, InitialPrompt: "sleep 5s\nsay done"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	q1, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{ThreadId: l.Thread.ThreadId,
		Text: "shell npm install x|ok", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{ThreadId: l.Thread.ThreadId,
		Text: "say third", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.InterruptRun(ctx, owner, &threadv2.InterruptRunRequest{ThreadId: l.Thread.ThreadId}); err != nil {
		t.Fatal(err)
	}
	final := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	t.Logf("queued run mode=%v; status=%v requests=%d", q1.Run.RuntimeMode, final.Thread.Status, len(final.Requests))
	if len(final.Requests) == 0 {
		t.Errorf("SUPERVISED run executed `npm install` without approval after interrupt-drain")
	}
}

// A project set up for some coding agents takes threads of those only, for
// the owner too (it is the project's setting, not a grant's): launching,
// switching the model, dispatching with another provider and forking.
func TestProjectAgentsLimitThreads(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("lim", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", []string{"alt"})
	owner := OwnerPrincipal("dev_owner", "Owner")
	scripted := &threadv2.ModelSelection{InstanceId: "scripted"}

	if _, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "lim", ModelSelection: scripted}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("launch with a provider the project is not for: %v", err)
	}
	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "lim", ModelSelection: &threadv2.ModelSelection{InstanceId: "alt-agent"}})
	if err != nil {
		t.Fatalf("launch with the project's provider (by driver): %v", err)
	}
	tid := l.Thread.ThreadId
	if _, err := eng.UpdateThreadConfig(ctx, owner, &threadv2.UpdateThreadConfigRequest{ThreadId: tid, ModelSelection: scripted}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("switching to a provider the project is not for: %v", err)
	}
	if _, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{ThreadId: tid, Text: "say hi", ModelSelection: scripted}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("dispatching with a provider the project is not for: %v", err)
	}
	if _, err := eng.ForkThread(ctx, owner, &threadv2.ForkThreadRequest{SourceThreadId: tid, ModelSelection: scripted}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forking onto a provider the project is not for: %v", err)
	}
	// Narrowing the project later stops its threads' next turns on other providers.
	_ = eng.SyncWorkspace("lim", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", []string{"claude"})
	if _, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{ThreadId: tid, Text: "say hi"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a turn after the project was narrowed: %v", err)
	}
}

// The temporary workspace is not one of the machine's projects: a grant's
// "*" does not reach it, a grant must name it. The owner always may.
func TestScratchNeedsANamedGrant(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	grant := func(projects ...string) *Principal {
		return GrantPrincipal(&identityv1.Grant{GrantId: "gr_s", ClientId: "cli_s", ProjectIds: projects, Agents: []string{"*"}})
	}
	if _, err := eng.LaunchThread(ctx, grant("*"), &threadv2.LaunchThreadRequest{Title: "x"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf(`a grant for "*" in the temporary workspace: %v`, err)
	}
	if _, err := eng.LaunchThread(ctx, grant(ScratchProjectID), &threadv2.LaunchThreadRequest{Title: "x"}); err != nil {
		t.Fatalf("a grant naming scratch: %v", err)
	}
	if _, err := eng.LaunchThread(ctx, OwnerPrincipal("o", "O"), &threadv2.LaunchThreadRequest{Title: "x"}); err != nil {
		t.Fatalf("the owner: %v", err)
	}
}

// A message queued while nothing runs starts at once: queued behind nothing
// it would never start (the composer queues whatever it sends to a session,
// whether or not a turn is still running when it arrives).
func TestQueueToAnIdleThreadStarts(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{InitialPrompt: "say first"})
	if err != nil {
		t.Fatal(err)
	}
	waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	res, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{ThreadId: l.Thread.ThreadId, Text: "say second", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE})
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.Status != threadv2.RunStatus_RUN_STATUS_RUNNING {
		t.Fatalf("queued to an idle thread: run %v, want running", res.Run.Status)
	}
	final := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if final.Thread.RunCount != 2 || final.Thread.QueuedRunCount != 0 {
		t.Fatalf("runs %d, queued %d", final.Thread.RunCount, final.Thread.QueuedRunCount)
	}
	// A held queue still holds.
	if _, err := eng.ManageQueue(ctx, owner, &threadv2.ManageQueueRequest{ThreadId: l.Thread.ThreadId, Operation: threadv2.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE}); err != nil {
		t.Fatal(err)
	}
}

// A queued run that starts when the one before it ends or is interrupted is
// told to subscribers (its run and its message), so a window's copy of the
// thread doesn't keep it queued.
func TestQueuedRunStartIsPublished(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	for _, end := range []string{"completed", "interrupted"} {
		l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{InitialPrompt: "sleep 600ms\nsay first"})
		if err != nil {
			t.Fatal(err)
		}
		tid := l.Thread.ThreadId
		started := make(chan *threadv2.ThreadEvent, 8)
		subCtx, stop := context.WithCancel(ctx)
		go eng.SubscribeThread(subCtx, owner, tid, 0, func(ev *threadv2.ThreadEvent) error {
			if ev.Type == "run.started" {
				started <- ev
			}
			return nil
		})
		q, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{ThreadId: tid, Text: "say second", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE})
		if err != nil {
			t.Fatal(err)
		}
		if end == "interrupted" {
			if _, err := eng.InterruptRun(ctx, owner, &threadv2.InterruptRunRequest{ThreadId: tid}); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case ev := <-started:
			if ev.Run.GetRunId() != q.Run.RunId || ev.Run.Status != threadv2.RunStatus_RUN_STATUS_RUNNING || ev.Item.GetText() != "say second" {
				t.Errorf("%s: run.started %v %v %q", end, ev.Run.GetRunId(), ev.Run.GetStatus(), ev.Item.GetText())
			}
		case <-time.After(8 * time.Second):
			t.Errorf("%s: no run.started for the queued run", end)
		}
		stop()
		waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	}
}
