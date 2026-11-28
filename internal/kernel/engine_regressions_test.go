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
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/runtime"
	rtacp "github.com/hermeshai/hermesh/internal/runtime/acp"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
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

func runStatus(t *testing.T, eng *Engine, sid, runID string) sessionv1.RunStatus {
	t.Helper()
	proj, err := eng.GetSession(context.Background(), OwnerPrincipal("o", "O"), sid)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range proj.Runs {
		if r.RunId == runID {
			return r.Status
		}
	}
	return sessionv1.RunStatus_RUN_STATUS_UNSPECIFIED
}

// Regression: owner approves "git push origin feature" for the session;
// the same turn's "git push --force origin main" then runs with no approval.
func TestSessionRuleNeverPreapprovesForcePush(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "repo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
		InitialPrompt: "shell git push origin feature|pushed\nshell git push --force origin main|FORCED",
	})
	if err != nil {
		t.Fatal(err)
	}
	proj := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	if _, err := eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: l.Session.SessionId, RequestId: proj.Session.PendingRequestId,
		Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_SESSION,
	}); err != nil {
		t.Fatal(err)
	}
	final := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	ran := false
	for _, it := range final.Items {
		if it.Command == "git push --force origin main" && it.Status == "completed" {
			ran = true
		}
	}
	if ran && len(final.Requests) == 1 {
		t.Errorf("force push executed without its own approval (rule %q)", final.Requests[0].GetApproval().GetRuleKey())
	}
}

// Regression: a message queued by the owner rewrites session.EffectiveRuntimeMode, which the live
// gate of the grant's ALREADY RUNNING turn reads -> the grant's turn escalates to FULL_ACCESS.
func TestQueuedDispatchDoesNotChangeActiveRunMode(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS)

	l, err := eng.StartSession(ctx, grant, &sessionv1.StartSessionRequest{
		ProjectId:     "repo",
		InitialPrompt: "sleep 700ms\nshell npm install evil-pkg|installed",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: l.Session.SessionId, Text: "say later", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
	}); err != nil {
		t.Fatal(err)
	}
	final := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	if len(final.Requests) == 0 {
		t.Errorf("grant (ceiling AUTO_EDITS) ran `npm install` without approval after owner queued a FULL_ACCESS message")
	}
}

// Regression: changing the permission mode while a turn runs only took effect on the next turn; the
// running turn's gate kept the mode the turn started with.
func TestModeChangeMidRunAppliesToActiveRun(t *testing.T) {
	ctx := context.Background()
	owner := OwnerPrincipal("dev_owner", "Owner")
	for _, tc := range []struct {
		name         string
		start        *Principal
		from, to     identityv1.RuntimeMode
		wantApproval bool
	}{
		{"loosen", owner, identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, false},
		{"tighten", owner, identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, true},
		// The owner raising the mode never lifts the running grant turn above the grant's ceiling.
		{"grant ceiling", testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS), identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
			defer eng.Close()
			repo := initGitProject(t)
			_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
			l, err := eng.StartSession(ctx, tc.start, &sessionv1.StartSessionRequest{
				ProjectId:     "repo",
				RuntimeMode:   tc.from,
				InitialPrompt: "sleep 700ms\nshell npm install leftpad|installed",
			})
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
			if _, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{
				SessionId: l.Session.SessionId, RuntimeMode: tc.to,
			}); err != nil {
				t.Fatal(err)
			}
			final := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
			if got := len(final.Requests) > 0; got != tc.wantApproval {
				t.Errorf("approval requested = %v, want %v (mode %v -> %v mid-run)", got, tc.wantApproval, tc.from, tc.to)
			}
		})
	}
}

// Regression: a cosmetic owner action (pin) on a grant-created session recomputes the ceiling with
// the owner's (empty) grant -> MaxRuntimeMode/EffectiveRuntimeMode jump to FULL_ACCESS.
func TestPinKeepsGrantCeiling(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED)

	l, err := eng.StartSession(ctx, grant, &sessionv1.StartSessionRequest{
		ProjectId: "repo", RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{
		SessionId: l.Session.SessionId, HasPinned: true, Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Session.MaxRuntimeMode != l.Session.MaxRuntimeMode || res.Session.EffectiveRuntimeMode != l.Session.EffectiveRuntimeMode {
		t.Errorf("pinning changed runtime ceiling/mode of a grant-created session")
	}
	if !res.Session.UpdatedAt.AsTime().Equal(l.Session.UpdatedAt.AsTime()) {
		t.Errorf("pinning changed UpdatedAt: got %v, want %v", res.Session.UpdatedAt.AsTime(), l.Session.UpdatedAt.AsTime())
	}
}

// Regression: ForkSession never checks allowsAgent on req.ModelSelection, and DispatchMessage only
// checks overrides -> a grant restricted to "scripted" runs on "alt-agent".
func TestForkRespectsGrantAgentAllowList(t *testing.T) {
	ctx := context.Background()
	eng, _, drv2 := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO)

	l, err := eng.StartSession(ctx, grant, &sessionv1.StartSessionRequest{ProjectId: "repo", InitialPrompt: "say hi"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, grant, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	_, err = eng.ForkSession(ctx, grant, &sessionv1.ForkSessionRequest{
		SourceSessionId: l.Session.SessionId,
		ModelSelection:  &sessionv1.ModelSelection{InstanceId: "alt-agent"},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("fork onto a disallowed provider: got %v, want PermissionDenied", err)
	}

	owner := OwnerPrincipal("dev_owner", "Owner")
	if _, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{
		SessionId: l.Session.SessionId, ModelSelection: &sessionv1.ModelSelection{InstanceId: "alt-agent"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ForkSession(ctx, grant, &sessionv1.ForkSessionRequest{SourceSessionId: l.Session.SessionId}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("fork inheriting a disallowed provider: got %v, want PermissionDenied", err)
	}
	if _, err := eng.DispatchMessage(ctx, grant, &sessionv1.DispatchMessageRequest{SessionId: l.Session.SessionId, Text: "say from alt"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("dispatch onto a disallowed provider: got %v, want PermissionDenied", err)
	}
	if drv2.StartCount() != 0 {
		t.Fatalf("alt driver started %d sessions for a grant limited to scripted", drv2.StartCount())
	}
}

// Regression: tool output truncation keeps valid UTF-8 AND stores full output in blob store.
func TestToolOutputTruncationKeepsUTF8AndStoresBlob(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	out := strings.Repeat("中", 12000) // 36000 bytes; 16384 % 3 != 0
	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "shell cat big.txt|" + out + "\nsay done",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	found := false
	for _, it := range final.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION {
			found = true
			if it.Status != "completed" || it.Output == "" || !it.OutputTruncated || it.OutputBlobSha256 == "" {
				t.Errorf("unexpected large CJK command item: status=%q len=%d truncated=%v blob=%q", it.Status, len(it.Output), it.OutputTruncated, it.OutputBlobSha256)
			}
			data, _, _, err := eng.Store().GetBlob(it.OutputBlobSha256)
			if err != nil || string(data) != out {
				t.Errorf("expected full 36000-byte CJK output in blob, err=%v len=%d", err, len(data))
			}
		}
	}
	if !found {
		t.Errorf("command item missing")
	}
}

// Regression: concurrent retries of the same command_id run once.
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
			res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{CommandId: "cmd_same", Title: "x"})
			if err == nil {
				ids.Store(res.Session.SessionId, true)
			}
		}()
	}
	wg.Wait()
	all, _ := eng.ListSessions(ctx, owner, &sessionv1.ListSessionsRequest{IncludeArchived: true})
	if len(all.GetSessions()) > 1 {
		t.Errorf("one command_id created %d sessions", len(all.GetSessions()))
	}
}

// Regression: InterruptSession rejects a foreign run_id.
func TestInterruptRejectsForeignRunID(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := testGrant(identityv1.RuntimeMode_RUNTIME_MODE_AUTO)

	ownerSess, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "repo", InitialPrompt: "sleep 3s\nsay owner done"})
	if err != nil {
		t.Fatal(err)
	}
	grantSess, err := eng.StartSession(ctx, grant, &sessionv1.StartSessionRequest{ProjectId: "repo", Title: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.GetSession(ctx, grant, ownerSess.Session.SessionId); err == nil {
		t.Fatal("grant should not see owner session")
	}
	time.Sleep(100 * time.Millisecond)
	_, _ = eng.InterruptSession(ctx, grant, &sessionv1.InterruptSessionRequest{
		SessionId: grantSess.Session.SessionId, RunId: ownerSess.InitialRun.RunId, Force: true,
	})
	time.Sleep(300 * time.Millisecond)
	st := runStatus(t, eng, ownerSess.Session.SessionId, ownerSess.InitialRun.RunId)
	if st != sessionv1.RunStatus_RUN_STATUS_RUNNING {
		t.Errorf("grant killed a run in a session it cannot view (status=%v)", st)
	}
}

// Regression: forking a WORKTREE session carries uncommitted file changes from latest checkpoint,
// and reviving a released worktree restores latest checkpoint files too!
func TestForkAndReleasedWorktreeRestoreCheckpointFiles(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "repo", InitialPrompt: "write feature.txt|work in progress"})
	if err != nil {
		t.Fatal(err)
	}
	// The worktree is made by the first turn.
	wtPath := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE).Session.WorkspaceStrategy.WorktreePath
	f, err := eng.ForkSession(ctx, owner, &sessionv1.ForkSessionRequest{SourceSessionId: l.Session.SessionId})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Session.WorkspaceStrategy.WorktreePath, "feature.txt")); err != nil {
		t.Errorf("forked session's workspace is missing the source session's file changes: %v", err)
	}

	// Simulate releasing the source session's worktree on disk, then sending a new turn -> feature.txt must be restored!
	eng.Supervisor().Detach(l.Session.ActiveProviderSessionId, true)
	if err := eng.VCS().RemoveWorktree(ctx, repo, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: l.Session.SessionId,
		Text:      "say resumed after release",
	}); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	data, err := os.ReadFile(filepath.Join(wtPath, "feature.txt"))
	if err != nil || string(data) != "work in progress" {
		t.Fatalf("expected released worktree to restore feature.txt from checkpoint, err=%v data=%q", err, string(data))
	}
}

// Regression: RollbackCheckpoint(restore_files) in an IN_PLACE repo keeps user's staged index.
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

	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "repo",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, InitialPrompt: "write agent.txt|agent"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	ckpts, _ := eng.ListCheckpoints(ctx, owner, l.Session.SessionId)
	if len(ckpts) == 0 || ckpts[0].RunOrdinal != 0 {
		t.Fatalf("no baseline checkpoint: %v", ckpts)
	}
	if _, err := eng.RollbackCheckpoint(ctx, owner, &sessionv1.RollbackCheckpointRequest{
		SessionId: l.Session.SessionId, CheckpointId: ckpts[0].CheckpointId, RestoreFiles: true,
	}); err != nil {
		t.Fatal(err)
	}
	after := git("diff", "--cached", "--name-only")
	if before != "" && after == "" {
		t.Errorf("rollback unstaged the user's staged change to %s", before)
	}
}

// Regression: interrupt-drain path copies the queued run's mode onto the session.
func TestInterruptDrainUsesQueuedRunMode(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "repo",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, InitialPrompt: "sleep 5s\nsay done"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: l.Session.SessionId,
		Text: "shell npm install x|ok", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: l.Session.SessionId,
		Text: "say third", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{SessionId: l.Session.SessionId}); err != nil {
		t.Fatal(err)
	}
	final := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	if len(final.Requests) == 0 {
		t.Errorf("SUPERVISED run executed `npm install` without approval after interrupt-drain")
	}
}

func TestProjectAgentsLimitSessions(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("lim", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", []string{"alt"})
	owner := OwnerPrincipal("dev_owner", "Owner")
	scriptedSel := &sessionv1.ModelSelection{InstanceId: "scripted"}

	if _, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "lim", ModelSelection: scriptedSel}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("launch with a provider the project is not for: %v", err)
	}
	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "lim", ModelSelection: &sessionv1.ModelSelection{InstanceId: "alt-agent"}})
	if err != nil {
		t.Fatalf("launch with the project's provider (by driver): %v", err)
	}
	sid := l.Session.SessionId
	if _, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{SessionId: sid, ModelSelection: scriptedSel}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("switching to a provider the project is not for: %v", err)
	}
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: sid, Text: "say hi", ModelSelection: scriptedSel}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("dispatching with a provider the project is not for: %v", err)
	}
	if _, err := eng.ForkSession(ctx, owner, &sessionv1.ForkSessionRequest{SourceSessionId: sid, ModelSelection: scriptedSel}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forking onto a provider the project is not for: %v", err)
	}
	_ = eng.SyncWorkspace("lim", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", []string{"claude"})
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: sid, Text: "say hi"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a turn after the project was narrowed: %v", err)
	}
}

func TestScratchNeedsANamedGrant(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	grant := func(projects ...string) *Principal {
		return GrantPrincipal(&identityv1.Grant{GrantId: "gr_s", ClientId: "cli_s", ProjectIds: projects, Agents: []string{"*"}})
	}
	if _, err := eng.StartSession(ctx, grant("*"), &sessionv1.StartSessionRequest{Title: "x"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf(`a grant for "*" in the temporary workspace: %v`, err)
	}
	if _, err := eng.StartSession(ctx, grant(ScratchProjectID), &sessionv1.StartSessionRequest{Title: "x"}); err != nil {
		t.Fatalf("a grant naming scratch: %v", err)
	}
	if _, err := eng.StartSession(ctx, OwnerPrincipal("o", "O"), &sessionv1.StartSessionRequest{Title: "x"}); err != nil {
		t.Fatalf("the owner: %v", err)
	}
}

func TestQueueToAnIdleSessionStarts(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{InitialPrompt: "say first"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	res, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: l.Session.SessionId, Text: "say second", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE})
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.Status != sessionv1.RunStatus_RUN_STATUS_RUNNING {
		t.Fatalf("queued to an idle session: run %v, want running", res.Run.Status)
	}
	final := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if final.Session.GetProgress().GetTurnCount() != 2 || final.Session.QueuedRunCount != 0 {
		t.Fatalf("runs %d, queued %d", final.Session.GetProgress().GetTurnCount(), final.Session.QueuedRunCount)
	}
	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{SessionId: l.Session.SessionId, Operation: sessionv1.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedRunStartIsPublished(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")
	for _, end := range []string{"completed", "interrupted"} {
		l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{InitialPrompt: "sleep 600ms\nsay first"})
		if err != nil {
			t.Fatal(err)
		}
		sid := l.Session.SessionId
		started := make(chan *sessionv1.SessionEvent, 8)
		subCtx, stop := context.WithCancel(ctx)
		go eng.SubscribeSession(subCtx, owner, sid, 0, func(ev *sessionv1.SessionEvent) error {
			if ev.Type == "run.started" {
				started <- ev
			}
			return nil
		})
		q, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: sid, Text: "say second", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE})
		if err != nil {
			t.Fatal(err)
		}
		if end == "interrupted" {
			if _, err := eng.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{SessionId: sid}); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case ev := <-started:
			if ev.Run.GetRunId() != q.Run.RunId || ev.Run.Status != sessionv1.RunStatus_RUN_STATUS_RUNNING || ev.Item.GetText() != "say second" {
				t.Errorf("%s: run.started %v %v %q", end, ev.Run.GetRunId(), ev.Run.GetStatus(), ev.Item.GetText())
			}
		case <-time.After(8 * time.Second):
			t.Errorf("%s: no run.started for the queued run", end)
		}
		stop()
		waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	}
}

func TestCrossSessionAndMultiRunItemIsolation(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")

	la, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		InitialPrompt: "think alpha_thought\nsay reply_session_A_turn_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	projA1 := waitForSessionStatus(t, eng, owner, la.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	lb, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		InitialPrompt: "think beta_thought\nsay reply_session_B_turn_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	projB := waitForSessionStatus(t, eng, owner, lb.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	eng.Supervisor().Detach(projA1.Session.ActiveProviderSessionId, true)
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: la.Session.SessionId,
		Text:      "think alpha_thought_2\nsay reply_session_A_turn_2",
	}); err != nil {
		t.Fatal(err)
	}
	projA2 := waitForSessionStatus(t, eng, owner, la.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	assistantTexts := func(p *sessionv1.SessionProjection) []string {
		var out []string
		for _, it := range p.Items {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
				out = append(out, it.Text)
			}
		}
		return out
	}
	bMsgs := assistantTexts(projB)
	if len(bMsgs) != 1 || bMsgs[0] != "reply_session_B_turn_1" {
		t.Fatalf("Session B assistant replies = %v, want [reply_session_B_turn_1]", bMsgs)
	}
	aMsgs := assistantTexts(projA2)
	if len(aMsgs) != 2 || aMsgs[0] != "reply_session_A_turn_1" || aMsgs[1] != "reply_session_A_turn_2" {
		t.Fatalf("Session A assistant replies = %v, want [reply_session_A_turn_1 reply_session_A_turn_2]", aMsgs)
	}
}

// Reasoning is live while the model thinks, and ends at its next step, a reply here, with no end word from the agent.
func TestReasoningIsLiveUntilTheNextStep(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")

	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		InitialPrompt: "think pondering\nsleep 1500ms\nsay replying_now\nsleep 1500ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := l.Session.SessionId
	item := func(p *sessionv1.SessionProjection, kind sessionv1.TurnItemKind) *sessionv1.TurnItem {
		for _, it := range p.GetItems() {
			if it.Kind == kind {
				return it
			}
		}
		return nil
	}
	waitFor := func(what string, ok func(rsn, msg *sessionv1.TurnItem) bool) {
		t.Helper()
		for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(15 * time.Millisecond) {
			p, err := eng.GetSession(ctx, owner, sid)
			if err == nil && ok(item(p, sessionv1.TurnItemKind_TURN_ITEM_KIND_REASONING), item(p, sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE)) {
				return
			}
		}
		t.Fatalf("timed out waiting until %s", what)
	}
	waitFor("it thinks", func(rsn, msg *sessionv1.TurnItem) bool {
		return rsn != nil && rsn.Streaming && rsn.Text == "pondering" && msg == nil
	})
	waitFor("it replies, done thinking", func(rsn, msg *sessionv1.TurnItem) bool {
		return msg != nil && msg.Streaming && !rsn.Streaming && rsn.Text == "pondering"
	})
	p := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if rsn := item(p, sessionv1.TurnItemKind_TURN_ITEM_KIND_REASONING); rsn.Streaming {
		t.Fatalf("reasoning still live after the turn: %v", rsn)
	}
}

func TestStoreImportsLegacyV1Sessions(t *testing.T) {
	dataDir := t.TempDir()
	legDB, err := sqlitedb.Open(filepath.Join(dataDir, "hermeshd.db"), []sqlitedb.Migration{{
		Version:     1,
		Description: "v1_sessions",
		SQL: `
CREATE TABLE sessions (
  session_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  agent TEXT NOT NULL,
  status INTEGER NOT NULL,
  created_by TEXT NOT NULL,
  grant_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  provider_ref TEXT NOT NULL DEFAULT '',
  base_commit TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  worktree TEXT NOT NULL DEFAULT '',
  pgid INTEGER NOT NULL DEFAULT 0,
  progress BLOB,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  workspace INTEGER NOT NULL DEFAULT 0,
  result TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  effort TEXT NOT NULL DEFAULT '',
  conversation TEXT NOT NULL DEFAULT '',
  agent_model TEXT NOT NULL DEFAULT '',
  agent_effort TEXT NOT NULL DEFAULT ''
);
CREATE TABLE session_events (
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (session_id, seq)
);`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	prog2, err := proto.Marshal(&sessionv1.SessionProgress{TurnCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	prog1, err := proto.Marshal(&sessionv1.SessionProgress{TurnCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = legDB.Exec(`INSERT INTO sessions(session_id, project_id, agent, status, created_by, model, provider_ref, branch, progress, error, created_at, updated_at, workspace, result, title, effort, conversation)
		VALUES('s_legacy_1', 'proj', 'claude', 3, 'c_her', 'claude-sonnet-4-6', 'ref_1', 'main', ?, '', 1000, 2000, 1, 'done!', 'Legacy Task', 'high', 'bot_1')`, prog2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legDB.Exec(`INSERT INTO sessions(session_id, project_id, agent, status, created_by, model, provider_ref, branch, progress, error, created_at, updated_at, workspace, result, title, effort, conversation)
		VALUES('s_legacy_failed', 'proj', 'codex', ?, 'owner', 'gpt-5', '', 'feat', ?, 'boom', 1500, 2500, 2, '', 'Crashed Task', 'medium', '')`, int32(sessionv1.SessionStatus_SESSION_STATUS_CRASHED), prog1)
	if err != nil {
		t.Fatal(err)
	}
	legDB.Close()

	st, err := store.Open(filepath.Join(dataDir, "sessions.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	s1, err := st.Session("s_legacy_1")
	if err != nil {
		t.Fatalf("imported s_legacy_1 missing: %v", err)
	}
	if s1.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || s1.CreatedBy != "c_her" || s1.CreatedAs != "bot_1" || s1.GetProgress().GetTurnCount() != 2 {
		t.Fatalf("unexpected imported s_legacy_1: %+v", s1)
	}
	s2, err := st.Session("s_legacy_failed")
	if err != nil {
		t.Fatalf("imported s_legacy_failed missing: %v", err)
	}
	if s2.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED || s2.Error != "boom" {
		t.Fatalf("unexpected imported s_legacy_failed: %+v", s2)
	}
	st.Close()
}

type customTestDriver struct {
	kind    string
	caps    *sessionv1.ProviderCapabilities
	onStart func(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error)
}

func (d *customTestDriver) Kind() string { return d.kind }
func (d *customTestDriver) Capabilities(string) *sessionv1.ProviderCapabilities {
	return d.caps
}
func (d *customTestDriver) Probe(context.Context, *sessionv1.ProviderInstanceConfig, string) runtime.ProbeResult {
	return runtime.ProbeResult{Installed: true, Authenticated: true, Compatibility: sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED}
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
func (s *customTestSession) SetSelection(context.Context, *sessionv1.ModelSelection) error {
	return nil
}
func (s *customTestSession) SetModes(context.Context, identityv1.RuntimeMode, sessionv1.InteractionMode) error {
	return nil
}
func (s *customTestSession) Interrupt(context.Context) error { return nil }
func (s *customTestSession) ForceStop() error                { return nil }
func (s *customTestSession) Alive() bool                     { return true }
func (s *customTestSession) Close() error                    { return nil }

func TestToolUpdatePreservesMetadataAndRunTerminationSettlesItems(t *testing.T) {
	ctx := context.Background()
	drv := &customTestDriver{
		kind: "custom",
		caps: &sessionv1.ProviderCapabilities{NativeResume: true},
		onStart: func(_ context.Context, _ runtime.StartSpec) (runtime.Session, error) {
			return &customTestSession{
				events: make(chan runtime.Event, 32),
				onTurn: func(_ context.Context, _ runtime.TurnInput, ch chan<- runtime.Event) {
					ch <- runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: "msg_1", Text: "preamble"}
					ch <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "msg_1", Text: "preamble"}
					ch <- runtime.Event{Kind: runtime.EventToolStarted, ItemID: "t1", ToolName: "Bash", Command: "echo ok", Input: "{\n  \"command\": \"echo ok\"\n}"}
					ch <- runtime.Event{Kind: runtime.EventToolCompleted, ItemID: "t1", ExitCode: 0, Output: "ok"}
					bigIn := `{"content":"` + strings.Repeat("参数行ABCD", 3000) + `"}`
					ch <- runtime.Event{Kind: runtime.EventToolStarted, ItemID: "t_big", ToolName: "Write", FilePath: "big.txt", Input: bigIn}
					ch <- runtime.Event{Kind: runtime.EventToolCompleted, ItemID: "t_big", ExitCode: 0}
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
	_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId: "custom-agent", Driver: "custom", DisplayName: "Custom", Enabled: true,
	})

	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "custom-agent", Model: "m1"},
		InitialPrompt:  "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	proj := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	var kinds []sessionv1.TurnItemKind
	var bashItem, bigItem, readItem, finalMsg *sessionv1.TurnItem
	for _, it := range proj.Items {
		kinds = append(kinds, it.Kind)
		if strings.HasSuffix(it.ItemId, ":t1") {
			bashItem = it
		}
		if strings.HasSuffix(it.ItemId, ":t_big") {
			bigItem = it
		}
		if strings.HasSuffix(it.ItemId, ":t2") {
			readItem = it
		}
		if strings.HasSuffix(it.ItemId, ":msg_2") {
			finalMsg = it
		}
	}
	if bashItem == nil || bashItem.Kind != sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION || bashItem.ToolName != "Bash" || bashItem.Command != "echo ok" || !strings.Contains(bashItem.Input, `"command": "echo ok"`) || bashItem.Output != "ok" {
		t.Fatalf("bash tool lost metadata or input on completion: %+v", bashItem)
	}
	if bigItem == nil || !bigItem.InputTruncated || bigItem.InputBlobSha256 == "" || len(bigItem.Input) >= 20000 {
		t.Fatalf("large tool input was not inline-truncated with blob sha: %+v", bigItem)
	}
	inBlobData, _, _, err := eng.Store().GetBlob(bigItem.InputBlobSha256)
	if err != nil || uint64(len(inBlobData)) != bigItem.InputSize || !strings.HasSuffix(string(inBlobData), `"}`) {
		t.Fatalf("full input blob round-trip failed: err=%v len=%d size=%d", err, len(inBlobData), bigItem.InputSize)
	}
	if readItem == nil || readItem.FilePath != "README.md" || readItem.Status == "running" {
		t.Fatalf("dangling Read tool was not settled or lost FilePath: %+v", readItem)
	}
	if finalMsg == nil || finalMsg.Text != "final answer" || finalMsg.Streaming || finalMsg.Status != "completed" {
		t.Fatalf("delta-only post-tool message not finalized: %+v (kinds=%v)", finalMsg, kinds)
	}
	if err := eng.DeleteSession(ctx, owner, l.Session.SessionId); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, _, _, err := eng.Store().GetBlob(bigItem.InputBlobSha256); err == nil {
		t.Fatalf("expected input blob %s to be pruned on DeleteSession", bigItem.InputBlobSha256)
	}
}

func TestUnresumableCursorSelfHealsWithTranscriptHandoff(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var prompts []string
	drv := &customTestDriver{
		kind: "resumable",
		caps: &sessionv1.ProviderCapabilities{NativeResume: true},
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
	_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId: "res-agent", Driver: "resumable", DisplayName: "Resumable", Enabled: true,
	})

	l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "res-agent", Model: "m1"},
		InitialPrompt:  "remember_secret_42",
	})
	if err != nil {
		t.Fatal(err)
	}
	p1 := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	eng.Supervisor().Detach(p1.Session.ActiveProviderSessionId, true)

	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: l.Session.SessionId,
		Text:      "what did I ask earlier?",
	}); err != nil {
		t.Fatal(err)
	}
	p2 := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_CRASHED)
	if p2.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE {
		t.Fatalf("expected self-healed IDLE session on expired resume cursor, got %v (%s)", p2.Session.Status, p2.Session.Error)
	}
	mu.Lock()
	secondPrompt := prompts[1]
	mu.Unlock()
	if !strings.Contains(secondPrompt, "remember_secret_42") || !strings.Contains(secondPrompt, "answer_1") {
		t.Fatalf("self-healed session did not receive transcript handoff: %q", secondPrompt)
	}
}

func TestPromoteToSteerFallsBackToInterruptAndFrontWhenSteerUnsupported(t *testing.T) {
	ctx := context.Background()
	acpLike := &scripted.Driver{DisableSteer: true}
	liveDrv := scripted.NewDriver()
	eng, err := Open(Config{
		DataDir:         t.TempDir(),
		MachineID:       "m_steer_fallback",
		Drivers:         map[string]runtime.Driver{"acp": acpLike, "live": liveDrv},
		IncludeScripted: true,
		IdleTTL:         time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	owner := OwnerPrincipal("dev_owner", "Owner")
	_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId: "antigravity", Driver: "acp", AcpFlavor: "antigravity", DisplayName: "Antigravity", Enabled: true,
	})
	_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId: "claude", Driver: "live", DisplayName: "Claude", Enabled: true,
	})

	// 1. ACP provider (DisableSteer: true): promoting the 2nd queued run interrupts the slow turn
	// and runs the promoted run FIRST, followed by the remaining queued run.
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "antigravity", Model: "gemini-3.1-pro"},
		InitialPrompt:  "think pondering\nsleep 15s\nsay never_reaches_slow_end",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		if p.GetSession().GetActiveProviderSessionId() != "" && len(p.GetItems()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	q1, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say acp_queued_second", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}
	q2, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say acp_steered_first", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid,
		Operation: sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER,
		RunId:     q2.Run.RunId,
	}); err != nil {
		t.Fatalf("ManageQueue(PROMOTE_TO_STEER) on ACP failed: %v", err)
	}

	settled := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	var completedPrompts []string
	var run1Status sessionv1.RunStatus
	for _, r := range settled.Runs {
		if r.RunId == launched.InitialRun.RunId {
			run1Status = r.Status
		}
		if r.Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			completedPrompts = append(completedPrompts, r.Prompt)
		}
	}
	if run1Status != sessionv1.RunStatus_RUN_STATUS_INTERRUPTED {
		t.Fatalf("expected initial ACP run to be INTERRUPTED by promote-to-steer, got %v", run1Status)
	}
	if len(completedPrompts) != 2 || completedPrompts[0] != "say acp_steered_first" || completedPrompts[1] != "say acp_queued_second" {
		t.Fatalf("expected [acp_steered_first, acp_queued_second], got %v (q1=%s)", completedPrompts, q1.Run.RunId)
	}

	// 2. Promoting when the run is no longer queued returns FailedPrecondition instead of silent 200 OK.
	_, staleErr := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid,
		Operation: sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER,
		RunId:     q2.Run.RunId,
	})
	if connect.CodeOf(staleErr) != connect.CodeFailedPrecondition {
		t.Fatalf("expected FailedPrecondition for already-finished run, got %v", staleErr)
	}

	// 3. Even on a live-steer provider, if the queued run switched model/provider, PROMOTE_TO_STEER
	// interrupts and switches rather than injecting into the old model's turn.
	l2, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "claude", Model: "scripted-pro"},
		InitialPrompt:  "hang",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid2 := l2.Session.SessionId
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid2)
		if p.GetSession().GetActiveProviderSessionId() != "" && p.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	switchedQ, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId:      sid2,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "antigravity", Model: "gemini-3.1-pro"},
		Text:           "say switched_provider_steered",
		Intent:         sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid2,
		Operation: sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER,
		RunId:     switchedQ.Run.RunId,
	}); err != nil {
		t.Fatalf("ManageQueue(PROMOTE_TO_STEER) across provider switch failed: %v", err)
	}
	settled2 := waitForSessionStatus(t, eng, owner, sid2, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if settled2.Session.GetModelSelection().GetInstanceId() != "antigravity" {
		t.Fatalf("expected session switched to antigravity, got %+v", settled2.Session.GetModelSelection())
	}
}

func TestPromoteToSteerRealACPDriver(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "fakeacp")
	build := exec.Command("go", "build", "-o", fakeBin, "../runtime/acp/fakeacp")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeacp: %v\n%s", err, out)
	}
	t.Setenv("FAKE_ACP_STATE", filepath.Join(tmp, "state"))
	t.Setenv("FAKE_ACP_MODELS", "gemini-3-pro")

	eng, err := Open(Config{
		DataDir:   filepath.Join(tmp, "data"),
		MachineID: "m_real_acp",
		Drivers:   map[string]runtime.Driver{"acp": rtacp.NewDriver("")},
		IdleTTL:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	owner := OwnerPrincipal("dev_owner", "Owner")
	repo := initGitProject(t)
	_ = eng.SyncWorkspace("demo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil)
	_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "gemini",
		Driver:      "acp",
		AcpFlavor:   "gemini",
		BinaryPath:  fakeBin,
		DisplayName: "Gemini CLI",
		Enabled:     true,
	})

	sel := &sessionv1.ModelSelection{InstanceId: "gemini", Model: "gemini-3-pro"}
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:      "demo",
		Workspace:      &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE},
		ModelSelection: sel,
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
		InitialPrompt:  "focus Checking ACP state\nsleep 500ms\nsay acp_initial_done",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	// Test BOTH immediate promote during worktree/process startup AND mid-turn live steer without interrupting Run 1!
	q1, _ := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, ModelSelection: sel, RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, Text: "say acp_queued_second", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	q2, _ := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, ModelSelection: sel, RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, Text: "say acp_steered_first", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})

	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid,
		Operation: sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER,
		RunId:     q2.Run.RunId,
	}); err != nil {
		t.Fatalf("ManageQueue(PROMOTE_TO_STEER): %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		if p.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, _ := eng.GetSession(ctx, owner, sid)
	if p.GetSession().GetStatus() != sessionv1.SessionStatus_SESSION_STATUS_IDLE {
		for _, r := range p.GetRuns() {
			t.Logf("run %s ord=%d status=%v err=%q ps=%s prompt=%q", r.RunId, r.Ordinal, r.Status, r.ErrorMessage, r.ProviderSessionId, r.Prompt)
		}
		for _, it := range p.GetItems() {
			t.Logf("item %s run=%s kind=%v status=%s text=%q", it.ItemId, it.RunId, it.Kind, it.Status, it.Text)
		}
		t.Fatalf("session stuck on %v (err=%q)", p.GetSession().GetStatus(), p.GetSession().GetError())
	}
	for _, r := range p.GetRuns() {
		if r.RunId == launched.InitialRun.RunId && r.Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			t.Fatalf("expected initial ACP run to complete without interruption, got %v (err=%q)", r.Status, r.ErrorMessage)
		}
	}
	var replies []string
	for _, it := range p.GetItems() {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			replies = append(replies, it.Text)
		}
	}
	if len(replies) != 2 || replies[0] != "acp_initial_done\nacp_steered_first" || replies[1] != "acp_queued_second" {
		t.Fatalf("expected replies [acp_initial_done\\nacp_steered_first, acp_queued_second], got %q (q1=%s)", replies, q1.Run.RunId)
	}

	// Also verify direct mid-turn DISPATCH_INTENT_STEER AFTER pre-steer assistant text streamed:
	// pre-steer bubble flushes before the steered user item, and post-steer bubble appears after it!
	for _, mode := range []string{"prompt_handover", "ext", "reject"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{}
			switch mode {
			case "ext":
				env["FAKE_ACP_STEERING_EXT"] = "1"
			case "reject":
				env["FAKE_ACP_REJECT_STEER"] = "1"
			}
			instID := "gemini_" + mode
			_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
				InstanceId:  instID,
				Driver:      "acp",
				AcpFlavor:   "gemini",
				BinaryPath:  fakeBin,
				DisplayName: "Gemini " + mode,
				Enabled:     true,
				Env:         env,
			})
			msel := &sessionv1.ModelSelection{InstanceId: instID, Model: "gemini-3-pro"}
			l, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
				ProjectId:      "demo",
				ModelSelection: msel,
				RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
				InitialPrompt:  "say mode_initial_" + mode + "\nsleep 600ms",
			})
			if err != nil {
				t.Fatal(err)
			}
			// Wait until pre-steer assistant delta streamed (StartTurn active and inside sleep 600ms).
			waitDeadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(waitDeadline) {
				gp, _ := eng.GetSession(ctx, owner, l.Session.SessionId)
				sawReply := false
				for _, it := range gp.GetItems() {
					if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
						sawReply = true
						break
					}
				}
				if sawReply {
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
			stRes, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
				SessionId:      l.Session.SessionId,
				ModelSelection: msel,
				RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
				Text:           "say mode_steered_" + mode,
				Intent:         sessionv1.DispatchIntent_DISPATCH_INTENT_STEER,
			})
			if err != nil {
				t.Fatalf("DispatchMessage(STEER) in %s mode: %v", mode, err)
			}
			if mode != "reject" && !stRes.SteeredActiveRun {
				t.Fatalf("expected SteeredActiveRun=true in %s mode, got %+v", mode, stRes)
			}
			settled := waitForSessionStatus(t, eng, owner, l.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
			for _, r := range settled.GetRuns() {
				if r.RunId == l.InitialRun.RunId && r.Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED {
					t.Fatalf("%s: initial run must NOT be interrupted, got %v (%q)", mode, r.Status, r.ErrorMessage)
				}
			}
			var got []string
			for _, it := range settled.GetItems() {
				if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
					got = append(got, it.Text)
				}
			}
			if len(got) != 2 || got[0] != "mode_initial_"+mode || got[1] != "mode_steered_"+mode {
				t.Fatalf("%s: got replies %q, want [mode_initial_%s mode_steered_%s]", mode, got, mode, mode)
			}
		})
	}
}
