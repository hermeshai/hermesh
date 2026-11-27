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

func TestInspectGitStatusSquashMergeBaselineDirtyAndRemoteSync(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGitTest(t, repo, "init", "--bare", "-b", "main", origin)
	runGitTest(t, repo, "remote", "add", "origin", origin)
	runGitTest(t, repo, "push", "-u", "origin", "main")

	// Pre-existing untracked file in repo before session starts:
	if err := os.MkdirAll(filepath.Join(repo, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skills", "SKILL.md"), []byte("# pre-existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(filepath.Join(t.TempDir(), "worktrees"))
	baseCp, err := mgr.CaptureCheckpoint(ctx, repo, "ckpt_0", "s_inplace", "", 0, "")
	if err != nil || baseCp == nil {
		t.Fatalf("CaptureCheckpoint baseline: %v", err)
	}
	preDirty := map[string]bool{}
	for _, f := range baseCp.Files {
		preDirty[f.Path] = true
	}

	// 1. In-place branch commit + push with untouched pre-existing untracked file -> PUSHED (not masked as DIRTY).
	runGitTest(t, repo, "checkout", "-b", "fix/issue-1331")
	if err := os.WriteFile(filepath.Join(repo, "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "fix.txt")
	runGitTest(t, repo, "commit", "-m", "fix: resolve 1331")
	runGitTest(t, repo, "push", "-u", "origin", "fix/issue-1331")

	st := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:          repo,
		RepoPath:         repo,
		SessionBranch:    "fix/issue-1331",
		BaseRef:          "main",
		InitialHeadOID:   baseCp.HeadOid,
		InitialTreeOID:   baseCp.GitTreeOid,
		PreExistingDirty: preDirty,
		HasChangesHint:   true,
	})
	if st == nil || st.State != sessionv1.GitState_GIT_STATE_PUSHED {
		t.Fatalf("pre-existing untracked file masked PUSHED state: got %+v, want PUSHED", st)
	}

	// Modifying the pre-existing file after baseline MUST report DIRTY.
	if err := os.WriteFile(filepath.Join(repo, "skills", "SKILL.md"), []byte("# modified during session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stMod := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:          repo,
		RepoPath:         repo,
		SessionBranch:    "fix/issue-1331",
		BaseRef:          "main",
		InitialHeadOID:   baseCp.HeadOid,
		InitialTreeOID:   baseCp.GitTreeOid,
		PreExistingDirty: preDirty,
		HasChangesHint:   true,
	})
	if stMod == nil || stMod.State != sessionv1.GitState_GIT_STATE_DIRTY || stMod.DirtyFiles != 1 {
		t.Fatalf("modified pre-existing file must count as dirty: got %+v", stMod)
	}
	// Restore pre-existing file and switch repo back to main.
	if err := os.WriteFile(filepath.Join(repo, "skills", "SKILL.md"), []byte("# pre-existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "checkout", "main")

	// 2. Multi-commit worktree branch squash-merged on remote (GitHub-style) + SyncRemoteTargetBranch:
	wtPath, wtBranch, err := mgr.PrepareWorktree(ctx, repo, "s_squash", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "part1.go"), []byte("package p\nconst A = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, wtPath, "add", "part1.go")
	runGitTest(t, wtPath, "commit", "-m", "feat: part 1")
	if err := os.WriteFile(filepath.Join(wtPath, "part2.go"), []byte("package p\nconst B = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, wtPath, "add", "part2.go")
	runGitTest(t, wtPath, "commit", "-m", "feat: part 2")
	runGitTest(t, wtPath, "push", "origin", wtBranch)

	// No-op branch (reverted back to initial tree) must NOT falsely report MERGED via merge-tree:
	wtNoop, noopBranch, err := mgr.PrepareWorktree(ctx, repo, "s_noop", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtNoop, "tmp.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, wtNoop, "add", "tmp.txt")
	runGitTest(t, wtNoop, "commit", "-m", "chore: add tmp")
	_ = os.Remove(filepath.Join(wtNoop, "tmp.txt"))
	runGitTest(t, wtNoop, "add", "-A")
	runGitTest(t, wtNoop, "commit", "-m", "chore: revert tmp")
	runGitTest(t, wtNoop, "push", "origin", noopBranch)

	stNoop := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtNoop,
		RepoPath:       repo,
		SessionBranch:  noopBranch,
		BaseRef:        "main",
		InitialHeadOID: baseCp.HeadOid,
	})
	if stNoop == nil || stNoop.State != sessionv1.GitState_GIT_STATE_PUSHED {
		t.Fatalf("reverted zero-net-diff branch got %+v, want PUSHED (not MERGED)", stNoop)
	}

	// Simulate GitHub advancing origin/main with an unrelated commit + squash merge of wtBranch in a separate clone:
	ghClone := filepath.Join(t.TempDir(), "github-server-clone")
	runGitTest(t, t.TempDir(), "clone", origin, ghClone)
	runGitTest(t, ghClone, "config", "user.name", "GitHub")
	runGitTest(t, ghClone, "config", "user.email", "noreply@github.com")
	if err := os.WriteFile(filepath.Join(ghClone, "unrelated.md"), []byte("other pr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, ghClone, "add", "unrelated.md")
	runGitTest(t, ghClone, "commit", "-m", "chore: other PR (#100)")
	runGitTest(t, ghClone, "fetch", "origin", wtBranch)
	runGitTest(t, ghClone, "merge", "--squash", "FETCH_HEAD")
	runGitTest(t, ghClone, "commit", "-m", "feat: squash merge s_squash (#101)")
	runGitTest(t, ghClone, "push", "origin", "main")

	// Before SyncRemoteTargetBranch, local repo still has old origin/main -> PUSHED.
	stBefore := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  wtBranch,
		BaseRef:        "main",
		InitialHeadOID: baseCp.HeadOid,
		HasChangesHint: true,
	})
	if stBefore == nil || stBefore.State != sessionv1.GitState_GIT_STATE_PUSHED {
		t.Fatalf("before remote sync got %+v, want PUSHED", stBefore)
	}

	// Remove any existing FETCH_HEAD in repo so we can assert SyncRemoteTargetBranch never writes FETCH_HEAD.
	fetchHeadPath := filepath.Join(repo, ".git", "FETCH_HEAD")
	_ = os.Remove(fetchHeadPath)

	updated, err := mgr.SyncRemoteTargetBranch(ctx, repo, "main")
	if err != nil || !updated {
		t.Fatalf("SyncRemoteTargetBranch expected updated=true err=nil, got updated=%v err=%v", updated, err)
	}
	if _, statErr := os.Stat(fetchHeadPath); !os.IsNotExist(statErr) {
		t.Fatalf("SyncRemoteTargetBranch must not write .git/FETCH_HEAD")
	}

	// Second call when remote has not moved must be a zero-fetch no-op (updated=false).
	updated2, err := mgr.SyncRemoteTargetBranch(ctx, repo, "main")
	if err != nil || updated2 {
		t.Fatalf("second SyncRemoteTargetBranch expected updated=false err=nil, got updated=%v err=%v", updated2, err)
	}

	// After SyncRemoteTargetBranch, multi-commit squash merge into origin/main resolves as MERGED!
	stAfter := mgr.InspectGitStatus(ctx, GitStatusInput{
		Workdir:        wtPath,
		RepoPath:       repo,
		SessionBranch:  wtBranch,
		BaseRef:        "main",
		InitialHeadOID: baseCp.HeadOid,
		HasChangesHint: true,
	})
	if stAfter == nil || stAfter.State != sessionv1.GitState_GIT_STATE_MERGED || stAfter.TargetRef != "main" {
		t.Fatalf("after remote squash merge sync got %+v, want MERGED into main", stAfter)
	}
}
