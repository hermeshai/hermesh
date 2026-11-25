package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/vcs"
)

// A new session's branch: in the project's own folder on another local branch, switched
// to first (not under a running session, not over uncommitted changes); in a worktree from
// another branch, or from origin's latest.
func TestLaunchOnBranches(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")

	repo := initGitProject(t)
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "branch", "feat/x")
	// origin, a commit ahead of the local main.
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(repo, "init", "--bare", "-b", "main", origin)
	git(repo, "remote", "add", "origin", origin)
	git(repo, "push", "-q", "origin", "main")
	other := filepath.Join(t.TempDir(), "other")
	git(repo, "clone", "-q", origin, other)
	git(other, "-c", "user.name=T", "-c", "user.email=t@h.local", "commit", "-q", "--allow-empty", "-m", "on origin only")
	git(other, "push", "-q", "origin", "main")

	if err := eng.SyncWorkspace("proj", repo, "HEAD", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil); err != nil {
		t.Fatal(err)
	}
	launch := func(ws *sessionv1.WorkspaceStrategy, prompt string) (*sessionv1.Session, error) {
		res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ProjectId: "proj", Workspace: ws, InitialPrompt: prompt})
		if err != nil {
			return nil, err
		}
		return res.Session, nil
	}
	// The first turn sets the folder up; its WORKSPACE item says how, once it is done.
	setUp := func(sess *sessionv1.Session) (*sessionv1.WorkspaceStep, *sessionv1.TurnItem, *sessionv1.Session) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			proj, err := eng.GetSession(ctx, owner, sess.SessionId)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range proj.Items {
				if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_WORKSPACE && it.Status != "running" {
					return it.Workspace, it, proj.Session
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no settled workspace step in %v", proj.Items)
			}
			time.Sleep(15 * time.Millisecond)
		}
	}
	inPlace := func(branch string) *sessionv1.WorkspaceStrategy {
		return &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_IN_PLACE, Branch: branch}
	}

	// In place on feat/x: the answer comes before the folder is switched, which the first turn
	// does and says.
	sess, err := launch(inPlace("feat/x"), "sleep 2s")
	if err != nil {
		t.Fatal(err)
	}
	if sess.GetWorkspaceStrategy().GetBranch() != "feat/x" || sess.GetBranch() != "feat/x" {
		t.Fatalf("session on %q", sess.GetWorkspaceStrategy().GetBranch())
	}
	step, item, _ := setUp(sess)
	if item.Status != "completed" || step.Action != sessionv1.WorkspaceAction_WORKSPACE_ACTION_SWITCH || step.Branch != "feat/x" || step.PreviousBranch != "main" || step.Path != repo {
		t.Fatalf("switch step %v (%s)", step, item.Status)
	}
	if got := vcs.CurrentBranch(ctx, repo); got != "feat/x" {
		t.Fatalf("folder on %q", got)
	}

	// Not while a session works in the folder: refused at once, as before.
	if _, err := launch(inPlace("main"), "say hi"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "is working in") {
		t.Fatalf("switch under a running session: %v", err)
	}
	waitForSessionStatus(t, eng, owner, sess.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	// Not over uncommitted changes: the turn fails at its first step, which says why, and the
	// folder stays where it was.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err = launch(inPlace("main"), "say hi")
	if err != nil {
		t.Fatal(err)
	}
	step, item, after := setUp(sess)
	if item.Status != "failed" || step.Action != sessionv1.WorkspaceAction_WORKSPACE_ACTION_SWITCH || !strings.Contains(item.Text, "uncommitted changes") || item.GetFailure().GetMessage() != item.Text {
		t.Fatalf("switch over changes: %v (%s: %q)", step, item.Status, item.Text)
	}
	if proj := waitForSessionStatus(t, eng, owner, sess.SessionId, sessionv1.SessionStatus_SESSION_STATUS_CRASHED); proj.Runs[0].ErrorCode != "workspace_error" {
		t.Fatalf("run ended %q, session %v", proj.Runs[0].ErrorCode, after.Status)
	}
	if got := vcs.CurrentBranch(ctx, repo); got != "feat/x" {
		t.Fatalf("folder moved to %q", got)
	}
	// The branch it is on already needs no switch, changes or not: the folder is kept.
	sess, err = launch(inPlace("feat/x"), "say hi")
	if err != nil {
		t.Fatalf("on the branch checked out: %v", err)
	}
	if step, item, _ := setUp(sess); item.Status != "completed" || step.Action != sessionv1.WorkspaceAction_WORKSPACE_ACTION_KEEP || step.Branch != "feat/x" {
		t.Fatalf("keep step %v (%s)", step, item.Status)
	}
	waitForSessionStatus(t, eng, owner, sess.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	git(repo, "checkout", "--", "main.go")

	// A worktree from another local branch: its branch is named in the answer, made in the turn.
	git(repo, "commit", "-q", "--allow-empty", "-m", "on feat/x")
	sess, err = launch(&sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE, BaseRef: "feat/x"}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if sess.GetWorkspaceStrategy().GetBranch() != "hermesh/"+sess.SessionId || sess.GetWorkspaceStrategy().GetWorktreePath() != "" {
		t.Fatalf("answered with %v", sess.GetWorkspaceStrategy())
	}
	step, item, after = setUp(sess)
	if item.Status != "completed" || step.Action != sessionv1.WorkspaceAction_WORKSPACE_ACTION_CREATE || step.Branch != "hermesh/"+sess.SessionId || step.BaseRef != "feat/x" || step.Path != after.GetWorkspaceStrategy().GetWorktreePath() {
		t.Fatalf("worktree step %v (%s), session %v", step, item.Status, after.GetWorkspaceStrategy())
	}
	if got := git(step.Path, "log", "-1", "--format=%s"); got != "on feat/x" {
		t.Fatalf("worktree from feat/x starts at %q", got)
	}

	// A worktree from origin's main: fetched first, so it has the commit the local main lacks.
	sess, err = launch(&sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE, BaseRef: "main", FromRemote: true}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	step, item, after = setUp(sess)
	if item.Status != "completed" || step.BaseRef != "origin/main" || after.GetWorkspaceStrategy().GetBaseRef() != "origin/main" {
		t.Fatalf("worktree from origin: %v (%s), session %v", step, item.Status, after.GetWorkspaceStrategy())
	}
	if got := git(step.Path, "log", "-1", "--format=%s"); got != "on origin only" {
		t.Fatalf("worktree from origin starts at %q", got)
	}
	// A branch origin doesn't have: the fetch fails, said in the step, and no worktree.
	sess, err = launch(&sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE, BaseRef: "nowhere", FromRemote: true}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	step, item, _ = setUp(sess)
	if item.Status != "failed" || step.Action != sessionv1.WorkspaceAction_WORKSPACE_ACTION_FETCH || !strings.Contains(item.Text, "fetch nowhere from origin") {
		t.Fatalf("missing remote branch: %v (%s: %q)", step, item.Status, item.Text)
	}
	if proj := waitForSessionStatus(t, eng, owner, sess.SessionId, sessionv1.SessionStatus_SESSION_STATUS_CRASHED); proj.Session.GetWorkspaceStrategy().GetWorktreePath() != "" {
		t.Fatalf("a worktree after a failed fetch: %v", proj.Session.GetWorkspaceStrategy())
	}
}
