package kernel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
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

// Regression (th_kouddhys_01m842705w9q5jwejy3rr50qmp): drivers emit msg_1/rsn_msg_1 starting from 1
// in each process. A global PRIMARY KEY(item_id) in turn_items caused Thread B's reply to overwrite
// Thread A's msg_1 (leaving Thread B empty!) and caused a resumed/restarted process in Thread A to
// overwrite Turn 1's msg_1.
func TestCrossThreadAndMultiRunItemIsolation(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")

	la, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		InitialPrompt: "think alpha_thought\nsay reply_thread_A_turn_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	projA1 := waitForThreadStatus(t, eng, owner, la.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)

	lb, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		InitialPrompt: "think beta_thought\nsay reply_thread_B_turn_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	projB := waitForThreadStatus(t, eng, owner, lb.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)

	// Simulate IdleTTL reaping Thread A's session before Turn 2 runs, so the new process resets itemSeq to 1.
	eng.Supervisor().Detach(projA1.Thread.ActiveProviderThreadId, true)
	if _, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: la.Thread.ThreadId,
		Text:     "think alpha_thought_2\nsay reply_thread_A_turn_2",
	}); err != nil {
		t.Fatal(err)
	}
	projA2 := waitForThreadStatus(t, eng, owner, la.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)

	assistantTexts := func(p *threadv2.ThreadProjection) []string {
		var out []string
		for _, it := range p.Items {
			if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
				out = append(out, it.Text)
			}
		}
		return out
	}
	bMsgs := assistantTexts(projB)
	if len(bMsgs) != 1 || bMsgs[0] != "reply_thread_B_turn_1" {
		t.Fatalf("Thread B assistant replies = %v, want [reply_thread_B_turn_1]", bMsgs)
	}
	aMsgs := assistantTexts(projA2)
	if len(aMsgs) != 2 || aMsgs[0] != "reply_thread_A_turn_1" || aMsgs[1] != "reply_thread_A_turn_2" {
		t.Fatalf("Thread A assistant replies = %v, want [reply_thread_A_turn_1 reply_thread_A_turn_2]", aMsgs)
	}
}

// Regression: upgrading an existing v3 SQLite DB where turn_items had PRIMARY KEY(item_id) must
// rebuild turn_items per thread from thread_events so previously invisible sessions display intact.
func TestStoreMigration4RepairsCollidedTurnItems(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "threads_v2.db")
	legacyDDL := []string{`
CREATE TABLE workspaces (project_id TEXT PRIMARY KEY, path TEXT NOT NULL, default_base_ref TEXT NOT NULL, workspace_mode INTEGER NOT NULL, description TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL);
CREATE TABLE provider_instances (instance_id TEXT PRIMARY KEY, driver TEXT NOT NULL, enabled INTEGER NOT NULL, payload BLOB NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE threads (thread_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, status INTEGER NOT NULL, archived INTEGER NOT NULL DEFAULT 0, pinned INTEGER NOT NULL DEFAULT 0, last_seq INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE provider_threads (provider_thread_id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, instance_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE runs (run_id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, ordinal INTEGER NOT NULL, status INTEGER NOT NULL, queue_order REAL NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE turn_items (item_id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, run_id TEXT NOT NULL, seq INTEGER NOT NULL, kind INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE runtime_requests (request_id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, run_id TEXT NOT NULL, status INTEGER NOT NULL, created_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE checkpoints (checkpoint_id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, run_ordinal INTEGER NOT NULL, created_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE thread_allow_rules (thread_id TEXT NOT NULL, rule_key TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (thread_id, rule_key));
CREATE TABLE blobs (sha256 TEXT PRIMARY KEY, name TEXT NOT NULL, mime_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE command_receipts (command_id TEXT PRIMARY KEY, method TEXT NOT NULL, payload BLOB NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE thread_events (thread_id TEXT NOT NULL, seq INTEGER NOT NULL, occurred_at INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY (thread_id, seq));
`, `ALTER TABLE runs ADD COLUMN grant_ceiling INTEGER NOT NULL DEFAULT 0;`, `ALTER TABLE workspaces ADD COLUMN agents TEXT NOT NULL DEFAULT '';`}

	rawDB, err := sqlitedb.Open(dbPath, legacyDDL)
	if err != nil {
		t.Fatal(err)
	}
	mustBytes := func(m proto.Message) []byte {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	putThread := func(tid string, lastSeq uint64) {
		th := &threadv2.Thread{ThreadId: tid, ProjectId: "scratch", Status: threadv2.ThreadStatus_THREAD_STATUS_IDLE, LastSeq: lastSeq}
		_, err := rawDB.Exec(`INSERT INTO threads(thread_id, project_id, status, last_seq, created_at, updated_at, payload) VALUES(?, 'scratch', 1, ?, 1, 1, ?)`, tid, lastSeq, mustBytes(th))
		if err != nil {
			t.Fatal(err)
		}
	}
	putRun := func(tid, rid string, ord uint32, st threadv2.RunStatus) {
		r := &threadv2.Run{RunId: rid, ThreadId: tid, Ordinal: ord, Status: st}
		_, err := rawDB.Exec(`INSERT INTO runs(run_id, thread_id, ordinal, status, created_at, updated_at, payload) VALUES(?, ?, ?, ?, 1, 1, ?)`, rid, tid, ord, int32(st), mustBytes(r))
		if err != nil {
			t.Fatal(err)
		}
	}
	putEv := func(tid string, seq uint64, typ string, it *threadv2.TurnItem) {
		ev := &threadv2.ThreadEvent{ThreadId: tid, Seq: seq, Type: typ, Item: it}
		_, err := rawDB.Exec(`INSERT INTO thread_events(thread_id, seq, occurred_at, payload) VALUES(?, ?, 1, ?)`, tid, seq, mustBytes(ev))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Thread 1 had 2 completed runs that both used msg_1, plus a tool whose completion wiped ToolName/Command.
	putThread("th_old", 20)
	putRun("th_old", "r_old_1", 1, threadv2.RunStatus_RUN_STATUS_COMPLETED)
	putRun("th_old", "r_old_2", 2, threadv2.RunStatus_RUN_STATUS_COMPLETED)
	u1 := &threadv2.TurnItem{ItemId: "itm_u1", ThreadId: "th_old", RunId: "r_old_1", Seq: 2, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE, Status: "completed", Text: "q1"}
	putEv("th_old", 3, "item.updated", u1)
	putEv("th_old", 5, "item.updated", &threadv2.TurnItem{ItemId: "t_bash", ThreadId: "th_old", RunId: "r_old_1", Seq: 4, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION, Status: "running", ToolName: "Bash", Command: "pwd"})
	putEv("th_old", 6, "item.updated", &threadv2.TurnItem{ItemId: "t_bash", ThreadId: "th_old", RunId: "r_old_1", Seq: 4, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL, Status: "completed", Output: "/repo"})
	putEv("th_old", 8, "item.updated", &threadv2.TurnItem{ItemId: "msg_1", ThreadId: "th_old", RunId: "r_old_1", Seq: 7, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE, Status: "completed", Text: "old_turn_1_answer"})
	u2 := &threadv2.TurnItem{ItemId: "itm_u2", ThreadId: "th_old", RunId: "r_old_2", Seq: 10, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE, Status: "completed", Text: "q2"}
	putEv("th_old", 11, "item.updated", u2)
	// Run 2 reused msg_1 and stole seq=7 in legacy DB!
	putEv("th_old", 13, "item.updated", &threadv2.TurnItem{ItemId: "msg_1", ThreadId: "th_old", RunId: "r_old_2", Seq: 7, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE, Status: "running", Streaming: true, Text: "old_turn_2_answer"})

	// Thread 2 (like th_kouddhys_01m842705w9q5jwejy3rr50qmp): msg_1 stole seq=198 from an earlier thread and never inserted a row for th_new!
	putThread("th_new", 10)
	putRun("th_new", "r_new_1", 1, threadv2.RunStatus_RUN_STATUS_COMPLETED)
	unew := &threadv2.TurnItem{ItemId: "itm_unew", ThreadId: "th_new", RunId: "r_new_1", Seq: 2, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE, Status: "completed", Text: "hello antigravity"}
	putEv("th_new", 3, "item.updated", unew)
	putEv("th_new", 5, "item.updated", &threadv2.TurnItem{ItemId: "msg_1", ThreadId: "th_new", RunId: "r_new_1", Seq: 198, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE, Status: "completed", Text: "new_thread_visible_answer"})

	// Corrupted legacy turn_items state: msg_1 sits under th_old with th_new's payload, and th_new only has itm_unew!
	_, _ = rawDB.Exec(`INSERT INTO turn_items(item_id, thread_id, run_id, seq, kind, payload) VALUES('itm_u1', 'th_old', 'r_old_1', 2, 1, ?)`, mustBytes(u1))
	_, _ = rawDB.Exec(`INSERT INTO turn_items(item_id, thread_id, run_id, seq, kind, payload) VALUES('itm_u2', 'th_old', 'r_old_2', 10, 1, ?)`, mustBytes(u2))
	_, _ = rawDB.Exec(`INSERT INTO turn_items(item_id, thread_id, run_id, seq, kind, payload) VALUES('msg_1', 'th_old', 'r_old_1', 7, 2, ?)`, mustBytes(&threadv2.TurnItem{ItemId: "msg_1", ThreadId: "th_new", RunId: "r_new_1", Seq: 7, Kind: threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE, Text: "new_thread_visible_answer"}))
	_, _ = rawDB.Exec(`INSERT INTO turn_items(item_id, thread_id, run_id, seq, kind, payload) VALUES('itm_unew', 'th_new', 'r_new_1', 2, 1, ?)`, mustBytes(unew))
	rawDB.Close()

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open migration #4 failed: %v", err)
	}
	newProj, err := st.Projection("th_new")
	if err != nil {
		t.Fatal(err)
	}
	if len(newProj.Items) != 2 || newProj.Items[1].Text != "new_thread_visible_answer" || newProj.Items[1].Seq <= newProj.Items[0].Seq {
		t.Fatalf("th_new repaired items = %+v, want user + new_thread_visible_answer in seq order", newProj.Items)
	}

	oldProj, err := st.Projection("th_old")
	if err != nil {
		t.Fatal(err)
	}
	var oldReplies []string
	var repairedTool *threadv2.TurnItem
	for _, it := range oldProj.Items {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			if it.Streaming || it.Status != "completed" {
				t.Errorf("completed run item left unsettled: %+v", it)
			}
			oldReplies = append(oldReplies, it.Text)
		}
		if it.ItemId == "t_bash" {
			repairedTool = it
		}
	}
	if len(oldReplies) != 2 || oldReplies[0] != "old_turn_1_answer" || oldReplies[1] != "old_turn_2_answer" {
		t.Fatalf("th_old repaired replies = %v, want [old_turn_1_answer old_turn_2_answer]", oldReplies)
	}
	if repairedTool == nil || repairedTool.Kind != threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION || repairedTool.Command != "pwd" || repairedTool.Output != "/repo" {
		t.Fatalf("th_old tool metadata not preserved on repair: %+v", repairedTool)
	}
	st.Close()

	// Second open must succeed as a fast no-op.
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("second store.Open: %v", err)
	}
	st2.Close()
}

type customTestDriver struct {
	kind    string
	caps    *threadv2.ProviderCapabilities
	onStart func(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error)
}

func (d *customTestDriver) Kind() string { return d.kind }
func (d *customTestDriver) Capabilities(string) *threadv2.ProviderCapabilities {
	return d.caps
}
func (d *customTestDriver) Probe(context.Context, *threadv2.ProviderInstanceConfig, string) runtime.ProbeResult {
	return runtime.ProbeResult{Installed: true, Authenticated: true, Compatibility: threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED}
}
func (d *customTestDriver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	return d.onStart(ctx, spec)
}

type customTestSession struct {
	events chan runtime.Event
	onTurn func(ctx context.Context, in runtime.TurnInput, ch chan<- runtime.Event)
}

func (s *customTestSession) Events() <-chan runtime.Event { return s.events }
func (s *customTestSession) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	go s.onTurn(ctx, in, s.events)
	return nil
}
func (s *customTestSession) Steer(context.Context, string, []runtime.TurnAttachment) error {
	return runtime.ErrSteerUnsupported
}
func (s *customTestSession) SetSelection(context.Context, *threadv2.ModelSelection) error { return nil }
func (s *customTestSession) SetModes(context.Context, identityv1.RuntimeMode, threadv2.InteractionMode) error {
	return nil
}
func (s *customTestSession) Interrupt(context.Context) error { return nil }
func (s *customTestSession) ForceStop() error                { return nil }
func (s *customTestSession) Alive() bool                     { return true }
func (s *customTestSession) Close() error                    { return nil }

// Regression: EventToolCompleted with empty ToolName/Command wiped EventToolStarted metadata,
// delta-only messages stayed streaming=true for live subscribers, and dangling running tools never settled.
func TestToolUpdatePreservesMetadataAndRunTerminationSettlesItems(t *testing.T) {
	ctx := context.Background()
	drv := &customTestDriver{
		kind: "custom",
		caps: &threadv2.ProviderCapabilities{NativeResume: true},
		onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
			return &customTestSession{
				events: make(chan runtime.Event, 32),
				onTurn: func(_ context.Context, _ runtime.TurnInput, ch chan<- runtime.Event) {
					ch <- runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: "msg_1", Text: "preamble"}
					ch <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "msg_1", Text: "preamble"}
					ch <- runtime.Event{Kind: runtime.EventToolStarted, ItemID: "t1", ToolName: "Bash", Command: "echo ok"}
					// Completion omits ToolName and Command (like Claude tool_result / incremental ACP update):
					ch <- runtime.Event{Kind: runtime.EventToolCompleted, ItemID: "t1", ExitCode: 0, Output: "ok"}
					// Post-tool delta-only message + dangling running Read tool before TurnCompleted:
					ch <- runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: "msg_2", Text: "final answer"}
					ch <- runtime.Event{Kind: runtime.EventToolStarted, ItemID: "t2", ToolName: "Read", FilePath: "README.md"}
					ch <- runtime.Event{Kind: runtime.EventTurnCompleted}
				},
			}, nil
		},
	}
	eng, err := Open(Config{
		DataDir:   t.TempDir(),
		MachineID: "m_custom",
		Drivers:   map[string]runtime.Driver{"custom": drv},
		IdleTTL:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	_, _ = eng.UpsertProviderInstance(ctx, owner, &threadv2.ProviderInstanceConfig{
		InstanceId: "custom-agent", Driver: "custom", DisplayName: "Custom", Enabled: true,
	})

	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ModelSelection: &threadv2.ModelSelection{InstanceId: "custom-agent", Model: "m1"},
		InitialPrompt:  "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	proj := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)

	var kinds []threadv2.TurnItemKind
	var bashItem, readItem, finalMsg *threadv2.TurnItem
	for _, it := range proj.Items {
		kinds = append(kinds, it.Kind)
		if strings.HasSuffix(it.ItemId, ":t1") {
			bashItem = it
		}
		if strings.HasSuffix(it.ItemId, ":t2") {
			readItem = it
		}
		if strings.HasSuffix(it.ItemId, ":msg_2") {
			finalMsg = it
		}
	}
	if bashItem == nil || bashItem.Kind != threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION || bashItem.ToolName != "Bash" || bashItem.Command != "echo ok" || bashItem.Output != "ok" {
		t.Fatalf("bash tool lost metadata on completion: %+v", bashItem)
	}
	if readItem == nil || readItem.FilePath != "README.md" || readItem.Status == "running" {
		t.Fatalf("dangling Read tool was not settled or lost FilePath: %+v", readItem)
	}
	if finalMsg == nil || finalMsg.Text != "final answer" || finalMsg.Streaming || finalMsg.Status != "completed" {
		t.Fatalf("delta-only post-tool message not finalized: %+v (kinds=%v)", finalMsg, kinds)
	}
}

// Regression: when a native ResumeCursor expires or the provider rejects resume, driveRun failed
// the run instead of self-healing with a fresh ProviderThread + BuildContextHandoff.
func TestUnresumableCursorSelfHealsWithTranscriptHandoff(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var prompts []string
	drv := &customTestDriver{
		kind: "resumable",
		caps: &threadv2.ProviderCapabilities{NativeResume: true},
		onStart: func(_ context.Context, spec runtime.StartSpec) (runtime.Session, error) {
			if spec.ResumeCursor != "" {
				return nil, errors.New("session cursor expired on provider")
			}
			return &customTestSession{
				events: make(chan runtime.Event, 16),
				onTurn: func(_ context.Context, in runtime.TurnInput, ch chan<- runtime.Event) {
					mu.Lock()
					prompts = append(prompts, in.Prompt)
					n := len(prompts)
					mu.Unlock()
					ch <- runtime.Event{Kind: runtime.EventNativeSession, NativeID: "cur_1", ResumeCursor: "cur_1"}
					ch <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "msg_1", Text: "answer_" + string(rune('0'+n))}
					ch <- runtime.Event{Kind: runtime.EventTurnCompleted}
				},
			}, nil
		},
	}
	eng, err := Open(Config{
		DataDir:   t.TempDir(),
		MachineID: "m_heal",
		Drivers:   map[string]runtime.Driver{"resumable": drv},
		IdleTTL:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	_, _ = eng.UpsertProviderInstance(ctx, owner, &threadv2.ProviderInstanceConfig{
		InstanceId: "res-agent", Driver: "resumable", DisplayName: "Resumable", Enabled: true,
	})

	l, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ModelSelection: &threadv2.ModelSelection{InstanceId: "res-agent", Model: "m1"},
		InitialPrompt:  "remember_secret_42",
	})
	if err != nil {
		t.Fatal(err)
	}
	p1 := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	eng.Supervisor().Detach(p1.Thread.ActiveProviderThreadId, true)

	if _, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: l.Thread.ThreadId,
		Text:     "what did I ask earlier?",
	}); err != nil {
		t.Fatal(err)
	}
	p2 := waitForThreadStatus(t, eng, owner, l.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE, threadv2.ThreadStatus_THREAD_STATUS_FAILED)
	if p2.Thread.Status != threadv2.ThreadStatus_THREAD_STATUS_IDLE {
		t.Fatalf("expected self-healed IDLE thread on expired resume cursor, got %v (%s)", p2.Thread.Status, p2.Thread.LastError)
	}
	mu.Lock()
	secondPrompt := prompts[1]
	mu.Unlock()
	if !strings.Contains(secondPrompt, "remember_secret_42") || !strings.Contains(secondPrompt, "answer_1") {
		t.Fatalf("self-healed session did not receive transcript handoff: %q", secondPrompt)
	}
}
