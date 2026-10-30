package machine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

func testRepo(t *testing.T, extraConfig ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	run("init", "-q")
	for i := 0; i+1 < len(extraConfig); i += 2 {
		run("config", extraConfig[i], extraConfig[i+1])
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

// TestWorktreeLifecycle: worktrees laid out by addWorktree are ordinary
// worktrees to git, in both the direct and the git-worktree-add paths.
func TestWorktreeLifecycle(t *testing.T) {
	for name, cfg := range map[string][]string{
		"direct":   nil,
		"fallback": {"core.sparseCheckout", "true"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := testRepo(t, cfg...)
			if direct := plainGitDir(repo) != ""; direct != (name == "direct") {
				t.Fatalf("plainGitDir direct=%v", direct)
			}
			w := NewWorkspace(filepath.Join(t.TempDir(), "worktrees"))
			var wg sync.WaitGroup
			ids := []string{"s_1", "s_2", "s_3", "s_4", "s_5", "s_6", "s_7", "s_8"}
			for _, id := range ids {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					base, branch, err := w.Create(ctx, repo, "", id)
					if err != nil || len(base) != 40 || branch != "hermesh/"+id {
						t.Errorf("create %s: %q %q %v", id, base, branch, err)
					}
				}(id)
			}
			wg.Wait()
			list := gitOut(t, repo, "worktree", "list", "--porcelain")
			for _, id := range ids {
				if !strings.Contains(list, "branch refs/heads/hermesh/"+id+"\n") {
					t.Fatalf("git does not list %s:\n%s", id, list)
				}
			}
			wt := w.Path("s_1")
			if st := gitOut(t, wt, "status", "--porcelain"); st != "" {
				t.Fatalf("fresh worktree not clean: %q", st)
			}
			// Git for Windows checks text out with CRLF (core.autocrlf).
			if b, _ := os.ReadFile(filepath.Join(wt, "sub", "b.txt")); strings.ReplaceAll(string(b), "\r", "") != "b\n" {
				t.Fatalf("checkout missing files: %q", b)
			}
			os.WriteFile(filepath.Join(wt, "c.txt"), []byte("c\n"), 0o644)
			head, stat, err := w.Snapshot(ctx, repo, "s_1", 1)
			if err != nil || stat.FilesChanged != 1 || head == "" {
				t.Fatalf("snapshot: %q %v %v", head, stat, err)
			}
			if got := strings.TrimSpace(gitOut(t, repo, "rev-parse", "hermesh/s_1")); got != head {
				t.Fatalf("branch at %s, snapshot %s", got, head)
			}
			// prune keeps live worktrees
			gitOut(t, repo, "worktree", "prune")
			if !strings.Contains(gitOut(t, repo, "worktree", "list", "--porcelain"), "branch refs/heads/hermesh/s_1\n") {
				t.Fatal("prune dropped a live worktree")
			}
			for _, id := range ids {
				if err := w.Remove(ctx, repo, id); err != nil {
					t.Fatal(err)
				}
			}
			if list := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, "hermesh/") {
				t.Fatalf("removed worktrees still listed:\n%s", list)
			}
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Fatal("worktree directory left behind")
			}
			gitOut(t, repo, "rev-parse", "--verify", "hermesh/s_1") // branch kept
			// other bases: a branch name, a commit id, a tag; bad refs fail cleanly
			first := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
			gitOut(t, repo, "tag", "v1")
			def := strings.TrimSpace(gitOut(t, repo, "symbolic-ref", "--short", "HEAD"))
			for i, ref := range []string{def, first, "v1", first[:10]} {
				id := fmt.Sprintf("s_ref%d", i)
				if base, _, err := w.Create(ctx, repo, ref, id); err != nil || base != first {
					t.Fatalf("base %q: %q %v", ref, base, err)
				}
			}
			for i, ref := range []string{"nope", "--output=/tmp/x"} {
				id := fmt.Sprintf("s_bad%d", i)
				if _, _, err := w.Create(ctx, repo, ref, id); err == nil {
					t.Fatalf("base %q accepted", ref)
				}
				if _, err := os.Stat(w.Path(id)); !os.IsNotExist(err) {
					t.Fatalf("failed create left %s behind", w.Path(id))
				}
			}
			gitOut(t, repo, "worktree", "prune")
			gitOut(t, repo, "fsck", "--no-progress")
		})
	}
}

// An agent that switches branches (or detaches HEAD) in its worktree must
// not take its work off the session branch, which is what is delivered.
func TestSnapshotFollowsAgentBranchSwitch(t *testing.T) {
	ctx := context.Background()
	repo := testRepo(t)
	w := NewWorkspace(filepath.Join(t.TempDir(), "worktrees"))
	if _, _, err := w.Create(ctx, repo, "", "s_1"); err != nil {
		t.Fatal(err)
	}
	wt := w.Path("s_1")
	gitOut(t, wt, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(wt, "y.txt"), []byte("two\n"), 0o644)
	head, stat, err := w.Snapshot(ctx, repo, "s_1", 1)
	if err != nil || stat.FilesChanged != 1 {
		t.Fatalf("snapshot: %v %v", stat, err)
	}
	if got := strings.TrimSpace(gitOut(t, repo, "rev-parse", "hermesh/s_1")); got != head {
		t.Fatalf("branch at %s, snapshot %s", got, head)
	}
	if b := gitOut(t, repo, "show", head+":y.txt"); strings.TrimSpace(b) != "two" {
		t.Fatalf("session branch lacks the agent's work: %q", b)
	}
	if st := gitOut(t, wt, "status", "--porcelain"); st != "" || strings.TrimSpace(gitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD")) != "feature" {
		t.Fatalf("the agent's own checkout was disturbed: %q", st)
	}
	// Detached HEAD, then nothing new: the branch stays in step.
	gitOut(t, wt, "checkout", "-q", "--detach")
	os.WriteFile(filepath.Join(wt, "z.txt"), []byte("three\n"), 0o644)
	head2, _, err := w.Snapshot(ctx, repo, "s_1", 2)
	if err != nil || head2 == head || strings.TrimSpace(gitOut(t, repo, "show", head2+":z.txt")) != "three" {
		t.Fatalf("detached snapshot: %s %v", head2, err)
	}
	head3, stat3, err := w.Snapshot(ctx, repo, "s_1", 3)
	if err != nil || head3 != head2 || stat3.FilesChanged != 0 {
		t.Fatalf("no-op snapshot: %s %v %v", head3, stat3, err)
	}
}

func TestProjectBase(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	if b, want := projectBase(ctx, Project{Path: repo, DefaultBaseRef: "HEAD"}), git("symbolic-ref", "--short", "HEAD"); b != want {
		t.Fatalf("HEAD resolves to %q, want the branch %q", b, want)
	}
	if b := projectBase(ctx, Project{Path: repo, DefaultBaseRef: "v1.2"}); b != "v1.2" {
		t.Fatalf("a named ref is kept: %q", b)
	}
	git("checkout", "-q", "--detach")
	if b, want := projectBase(ctx, Project{Path: repo, DefaultBaseRef: "HEAD"}), git("rev-parse", "--short", "HEAD"); b != want {
		t.Fatalf("detached HEAD resolves to %q, want the commit %q", b, want)
	}
	if b := projectBase(ctx, Project{Path: repo, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE}); b != "" {
		t.Fatalf("an in-place project has no base: %q", b)
	}
}
