package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.txt"), []byte("keep-or-delete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
}

func TestWorktreeLifecycleAndStaleRecovery(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	mgr := NewManager(filepath.Join(t.TempDir(), "worktrees"))

	// Missing base ref returns clear error.
	if _, _, err := mgr.PrepareWorktree(ctx, repo, "th_bad", "nonexistent-branch", ""); err == nil {
		t.Fatalf("expected error for missing base ref")
	}

	wtPath, branch, err := mgr.PrepareWorktree(ctx, repo, "th_01", "main", "")
	if err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	if branch != "hermesh/th_01" {
		t.Fatalf("branch = %q, want hermesh/th_01", branch)
	}
	if !IsGitRepo(ctx, wtPath) {
		t.Fatalf("worktree %q is not a git repo", wtPath)
	}

	// Simulate user deleting worktree directory manually on disk -> PrepareWorktree recovers from branch!
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}
	wtPath2, branch2, err := mgr.PrepareWorktree(ctx, repo, "th_01", "main", branch)
	if err != nil {
		t.Fatalf("PrepareWorktree recovery: %v", err)
	}
	if wtPath2 != wtPath || branch2 != branch {
		t.Fatalf("recovered worktree mismatch: %q %q", wtPath2, branch2)
	}

	if err := mgr.RemoveWorktree(ctx, repo, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree directory to be removed")
	}
}

func TestShadowCheckpointDiffAndExactFileRestore(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	mgr := NewManager(filepath.Join(t.TempDir(), "worktrees"))

	// Baseline checkpoint #0.
	cp0, err := mgr.CaptureCheckpoint(ctx, repo, "cp_0", "th_1", "", 0, "")
	if err != nil || cp0 == nil {
		t.Fatalf("CaptureCheckpoint #0: %v", err)
	}

	// Run 1: modify README.md, delete legacy.txt, create untracked src/feature.go.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Hello v2\nAdded line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "legacy.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "feature.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify real .git/index was NOT staged by CaptureCheckpoint!
	cp1, err := mgr.CaptureCheckpoint(ctx, repo, "cp_1", "th_1", "run_1", 1, cp0.GitTreeOid)
	if err != nil || cp1 == nil {
		t.Fatalf("CaptureCheckpoint #1: %v", err)
	}
	if len(cp1.Files) != 3 {
		t.Fatalf("expected 3 changed files in cp1, got %d: %+v", len(cp1.Files), cp1.Files)
	}
	cachedDiff, err := runGit(ctx, repo, nil, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cachedDiff) != "" {
		t.Fatalf("CaptureCheckpoint polluted real .git/index! staged files: %s", cachedDiff)
	}

	// Verify git fsck passes cleanly (pinned checkpoint ref is a valid commit object).
	if out, fsckErr := runGit(ctx, repo, nil, "fsck", "--no-progress"); fsckErr != nil {
		t.Fatalf("git fsck failed: %v\n%s", fsckErr, out)
	}

	// Verify DiffWorkingTree against cp0 includes all 3 files.
	patch, files, err := mgr.DiffWorkingTree(ctx, repo, cp0.GitTreeOid)
	if err != nil {
		t.Fatalf("DiffWorkingTree: %v", err)
	}
	if !strings.Contains(patch, "src/feature.go") || len(files) != 3 {
		t.Fatalf("unexpected diff: files=%d patch=%s", len(files), patch)
	}

	// Run 2: further edit src/feature.go, recreate legacy.txt, add extra junk.txt.
	if err := os.WriteFile(filepath.Join(repo, "src", "feature.go"), []byte("package src // broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "legacy.txt"), []byte("recreated in run 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "junk.txt"), []byte("temporary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rewind to Checkpoint #1:
	// - src/feature.go must revert to "package src\n"
	// - legacy.txt (deleted in Run 1, recreated in Run 2) must be deleted again!
	// - junk.txt must be deleted!
	if err := mgr.RestoreTree(ctx, repo, cp1.GitTreeOid); err != nil {
		t.Fatalf("RestoreTree(cp1): %v", err)
	}
	gotFeature, err := os.ReadFile(filepath.Join(repo, "src", "feature.go"))
	if err != nil || string(gotFeature) != "package src\n" {
		t.Fatalf("src/feature.go after restore = %q, err=%v", string(gotFeature), err)
	}
	if _, err := os.Stat(filepath.Join(repo, "legacy.txt")); !os.IsNotExist(err) {
		t.Fatalf("legacy.txt should be absent at cp1")
	}
	if _, err := os.Stat(filepath.Join(repo, "junk.txt")); !os.IsNotExist(err) {
		t.Fatalf("junk.txt should be cleaned at cp1")
	}

	// Rewind all the way back to Checkpoint #0:
	// - README.md reverts to "# Hello\n"
	// - legacy.txt comes back!
	// - src/feature.go is removed!
	if err := mgr.RestoreTree(ctx, repo, cp0.GitTreeOid); err != nil {
		t.Fatalf("RestoreTree(cp0): %v", err)
	}
	gotReadme, _ := os.ReadFile(filepath.Join(repo, "README.md"))
	if string(gotReadme) != "# Hello\n" {
		t.Fatalf("README.md at cp0 = %q", string(gotReadme))
	}
	if _, err := os.Stat(filepath.Join(repo, "legacy.txt")); err != nil {
		t.Fatalf("legacy.txt must exist at cp0: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "feature.go")); !os.IsNotExist(err) {
		t.Fatalf("src/feature.go must be removed at cp0")
	}
}
