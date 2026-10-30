package machine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A worktree with work in it moves out whole, by the path the workspace
// gave it (on Windows, with backslashes and maybe a short name).
func TestMoveWorktree(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if _, err := git(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	wt := filepath.Join(t.TempDir(), "s_1")
	if _, err := git(ctx, repo, "worktree", "add", "-q", "-b", "s_1", wt); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("work\n"), 0o644)
	to := filepath.Join(t.TempDir(), "kept")
	if err := moveWorktree(ctx, repo, wt, to); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(to, "unsaved.txt")); err != nil || string(b) != "work\n" {
		t.Fatalf("moved: %q %v", b, err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
}
