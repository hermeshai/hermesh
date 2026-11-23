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
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/kernel/vcs"
)

// A new session's branch (docs/thread-composer.md §16): in the project's own folder on another local branch, switched
// to first (not under a running session, not over uncommitted changes); in a worktree from another branch, or from
// origin's latest.
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
	launch := func(ws *threadv2.WorkspaceStrategy, prompt string) (*threadv2.Thread, error) {
		res, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{ProjectId: "proj", Workspace: ws, InitialPrompt: prompt})
		if err != nil {
			return nil, err
		}
		return res.Thread, nil
	}
	inPlace := func(branch string) *threadv2.WorkspaceStrategy {
		return &threadv2.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_IN_PLACE, Branch: branch}
	}

	// In place on feat/x: the folder is switched first, and the session records the branch.
	th, err := launch(inPlace("feat/x"), "sleep 2s")
	if err != nil {
		t.Fatal(err)
	}
	if got := vcs.CurrentBranch(ctx, repo); got != "feat/x" || th.GetWorkspace().GetBranch() != "feat/x" {
		t.Fatalf("folder on %q, session on %q", got, th.GetWorkspace().GetBranch())
	}

	// Not while a session works in the folder.
	if _, err := launch(inPlace("main"), "say hi"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "is working in") {
		t.Fatalf("switch under a running session: %v", err)
	}
	waitForThreadStatus(t, eng, owner, th.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)

	// Not over uncommitted changes: the folder stays where it was.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := launch(inPlace("main"), "say hi"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("switch over changes: %v", err)
	}
	if got := vcs.CurrentBranch(ctx, repo); got != "feat/x" {
		t.Fatalf("folder moved to %q", got)
	}
	// The branch it is on already needs no switch, changes or not.
	if _, err := launch(inPlace("feat/x"), "say hi"); err != nil {
		t.Fatalf("on the branch checked out: %v", err)
	}
	git(repo, "checkout", "--", "main.go")

	// A worktree from another local branch.
	git(repo, "commit", "-q", "--allow-empty", "-m", "on feat/x")
	th, err = launch(&threadv2.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE, BaseRef: "feat/x"}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(th.GetWorkspace().GetWorktreePath(), "log", "-1", "--format=%s"); got != "on feat/x" {
		t.Fatalf("worktree from feat/x starts at %q", got)
	}

	// A worktree from origin's main: fetched first, so it has the commit the local main lacks.
	th, err = launch(&threadv2.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE, BaseRef: "main", FromRemote: true}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(th.GetWorkspace().GetWorktreePath(), "log", "-1", "--format=%s"); got != "on origin only" || th.GetWorkspace().GetBaseRef() != "origin/main" {
		t.Fatalf("worktree from origin starts at %q (base %q)", got, th.GetWorkspace().GetBaseRef())
	}
	// A branch origin doesn't have: said, no worktree.
	if _, err := launch(&threadv2.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE, BaseRef: "nowhere", FromRemote: true}, "say hi"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "fetch nowhere from origin") {
		t.Fatalf("missing remote branch: %v", err)
	}
}
