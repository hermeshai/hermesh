package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInspectGitStatusStates(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGitTest(t, repo, "init", "--bare", "-b", "main", origin)
	runGitTest(t, repo, "remote", "add", "origin", origin)
	runGitTest(t, repo, "push", "-u", "origin", "main")
	initialHead := runGitTest(t, repo, "rev-parse", "HEAD")

	mgr := NewManager(filepath.Join(t.TempDir(), "worktrees"))
	wtPath, branch, err := mgr.PrepareWorktree(ctx, repo, "s_01", "main", "")
	if err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}

	// 1. Pure Q&A session (0 edits, 0 commits on a branch created off pushed main) -> CLEAN.
	st := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_CLEAN || st.Branch != branch || st.HeadCommit != "" {
		t.Fatalf("clean worktree got %+v", st)
	}

	// 2. Dirty working tree (modified + untracked) before any session commit -> DIRTY, no HeadCommit.
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("# Changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_DIRTY || st.DirtyFiles != 2 || st.HeadCommit != "" {
		t.Fatalf("dirty worktree got %+v", st)
	}

	// 3. Local commit on --no-track worktree branch -> UNPUSHED (1 commit ahead, commit summary set).
	runGitTest(t, wtPath, "add", ".")
	runGitTest(t, wtPath, "commit", "-m", "feat: sidebar git status")
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_UNPUSHED || st.CommitsAhead != 1 || st.HeadCommit == "" || st.HeadSubject != "feat: sidebar git status" {
		t.Fatalf("unpushed worktree got %+v", st)
	}

	// 4. Push WITHOUT -u (no @{upstream}, only refs/remotes/origin/<branch>) -> PUSHED.
	runGitTest(t, wtPath, "push", "origin", branch)
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_PUSHED || st.TargetRef != "origin/"+branch || st.HeadSubject != "feat: sidebar git status" {
		t.Fatalf("pushed without upstream got %+v", st)
	}

	// 5. Another local commit after pushing with -u -> UNPUSHED against upstream, then PUSHED again.
	runGitTest(t, wtPath, "push", "-u", "origin", branch)
	if err := os.WriteFile(filepath.Join(wtPath, "new.go"), []byte("package main\n// v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, wtPath, "add", "new.go")
	runGitTest(t, wtPath, "commit", "-m", "feat: second commit")
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_UNPUSHED || st.CommitsAhead != 1 || st.TargetRef != "origin/"+branch {
		t.Fatalf("ahead of upstream got %+v", st)
	}
	runGitTest(t, wtPath, "push")
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_PUSHED {
		t.Fatalf("pushed with upstream got %+v", st)
	}

	// 6. Released worktree (directory removed while branch survives) -> still resolves PUSHED, then MERGED after merge into main.
	if err := mgr.RemoveWorktree(ctx, repo, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_PUSHED {
		t.Fatalf("released worktree pushed got %+v", st)
	}

	runGitTest(t, repo, "merge", "--no-ff", "-m", "Merge session branch", branch)
	st = mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_MERGED || st.TargetRef != "main" {
		t.Fatalf("merged branch got %+v", st)
	}
}

func TestInspectGitStatusCherryPickMerged(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	initialHead := runGitTest(t, repo, "rev-parse", "HEAD")

	mgr := NewManager(filepath.Join(t.TempDir(), "worktrees"))
	wtPath, branch, err := mgr.PrepareWorktree(ctx, repo, "s_cp", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "cherry.txt"), []byte("picked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, wtPath, "add", "cherry.txt")
	runGitTest(t, wtPath, "commit", "-m", "fix: cherry-picked fix")
	commitOID := runGitTest(t, wtPath, "rev-parse", "HEAD")

	// Advance main independently, then cherry-pick the session commit onto main.
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("main-advance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "other.txt")
	runGitTest(t, repo, "commit", "-m", "chore: advance main")
	runGitTest(t, repo, "cherry-pick", commitOID)

	st := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  branch,
		BaseRef:        "main",
		InitialHeadOID: initialHead,
		HasChangesHint: true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_MERGED || st.TargetRef != "main" {
		t.Fatalf("cherry-picked session got %+v, want MERGED into main", st)
	}
}
