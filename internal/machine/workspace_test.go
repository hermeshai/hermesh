package machine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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

// A checkout stopped halfway (the session's start was stopped) leaves no
// worktree and no branch behind, so the next start makes them afresh.
func TestStoppedCheckoutLeavesNothing(t *testing.T) {
	for name, cfg := range map[string][]string{
		"direct":   nil,
		"fallback": {"extensions.worktreeConfig", "true"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := testRepo(t, cfg...)
			os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("a.txt filter=slow\n"), 0o644)
			gitOut(t, repo, "add", ".gitattributes")
			gitOut(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "attrs")
			gitOut(t, repo, "config", "filter.slow.smudge", "sleep 30; cat")
			w := NewWorkspace(filepath.Join(t.TempDir(), "worktrees"))
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			start := time.Now()
			if _, _, err := w.Create(ctx, repo, "", "s_1"); err == nil {
				t.Fatal("a stopped checkout succeeded")
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("stopping took %s", d)
			}
			if w.Exists("s_1") {
				t.Fatal("half checkout left behind")
			}
			if list := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, "s_1") {
				t.Fatalf("git still lists it:\n%s", list)
			}
			if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "-q", "hermesh/s_1").Output(); err == nil {
				t.Fatalf("branch left behind at %s", out)
			}
			gitOut(t, repo, "config", "filter.slow.smudge", "cat")
			if _, _, err := w.Create(context.Background(), repo, "", "s_1"); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if st := gitOut(t, w.Path("s_1"), "status", "--porcelain"); st != "" {
				t.Fatalf("retried worktree not clean: %q", st)
			}
		})
	}
}

// A git killed while it moves the session branch leaves the branch's lock
// file; the failed start removes it, so the next start gets through.
func TestKilledCheckoutLeavesNoLock(t *testing.T) {
	repo := testRepo(t)
	w := NewWorkspace(filepath.Join(t.TempDir(), "worktrees"))
	lock := filepath.Join(repo, ".git", "refs", "heads", "hermesh", "s_1.lock")
	os.MkdirAll(filepath.Dir(lock), 0o755)
	os.WriteFile(lock, nil, 0o644)
	if _, _, err := w.Create(context.Background(), repo, "", "s_1"); err == nil || !strings.Contains(err.Error(), ".lock") {
		t.Fatalf("create with a stale lock: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock left behind")
	}
	if _, _, err := w.Create(context.Background(), repo, "", "s_1"); err != nil {
		t.Fatalf("retry: %v", err)
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

// TestCopyIncluded: a new or restored worktree gets the ignored files
// .worktreeinclude names, and only those.
func TestCopyIncluded(t *testing.T) {
	for name, include := range map[string]string{
		"anywhere": "# local settings\nlocal.cfg\n.env\nsecrets/*\nnotignored.txt\n!secrets/skip\nlink\n",
		"paths":    "/local.cfg\n/.env\nsub/.env\nsecrets/*\n/notignored.txt\n!secrets/skip\n/link\n",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := testRepo(t)
			write := func(rel, s string, mode os.FileMode) {
				p := filepath.Join(repo, filepath.FromSlash(rel))
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(s), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(".gitignore", "*.cfg\n.env\nsecrets/\nbuild/\nlink\n", 0o644)
			gitOut(t, repo, "add", ".gitignore")
			gitOut(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "ignore")
			write(".worktreeinclude", include, 0o644)
			write("local.cfg", "team = X\n", 0o600)
			write(".env", "A=1\n", 0o644)
			write("sub/.env", "B=2\n", 0o644)
			write("secrets/key", "k\n", 0o644)
			write("secrets/nested/key2", "k2\n", 0o644)
			write("secrets/skip", "s\n", 0o644)
			write("build/out", "o\n", 0o644)      // ignored, not named
			write("notignored.txt", "n\n", 0o644) // named, not ignored
			symlinks := runtime.GOOS != "windows"
			if symlinks {
				os.Symlink("local.cfg", filepath.Join(repo, "link"))
			}

			w := NewWorkspace(filepath.Join(t.TempDir(), "worktrees"))
			if _, _, err := w.Create(ctx, repo, "", "s_1"); err != nil {
				t.Fatal(err)
			}
			if err := w.CopyIncluded(ctx, repo, "s_1"); err != nil {
				t.Fatal(err)
			}
			wt := w.Path("s_1")
			check := func() {
				t.Helper()
				for rel, want := range map[string]string{"local.cfg": "team = X\n", ".env": "A=1\n", "sub/.env": "B=2\n",
					"secrets/key": "k\n", "secrets/nested/key2": "k2\n"} {
					if b, err := os.ReadFile(filepath.Join(wt, filepath.FromSlash(rel))); err != nil || string(b) != want {
						t.Errorf("%s: %q %v", rel, b, err)
					}
				}
				for _, rel := range []string{"build/out", "notignored.txt", "secrets/skip", ".worktreeinclude"} {
					if _, err := os.Lstat(filepath.Join(wt, filepath.FromSlash(rel))); !os.IsNotExist(err) {
						t.Errorf("%s copied", rel)
					}
				}
				if symlinks {
					if to, err := os.Readlink(filepath.Join(wt, "link")); err != nil || to != "local.cfg" {
						t.Errorf("link: %q %v", to, err)
					}
				}
			}
			check()
			if st, _ := os.Stat(filepath.Join(wt, "local.cfg")); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
				t.Errorf("mode %v", st.Mode())
			}
			// ignored, so the turn's snapshot leaves them out
			if st := gitOut(t, wt, "status", "--porcelain"); st != "" {
				t.Fatalf("copied files show in status: %q", st)
			}
			// the session's own edits stay
			os.WriteFile(filepath.Join(wt, "local.cfg"), []byte("mine\n"), 0o600)
			if err := w.CopyIncluded(ctx, repo, "s_1"); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(filepath.Join(wt, "local.cfg")); string(b) != "mine\n" {
				t.Fatalf("overwritten: %q", b)
			}
			// restored after archiving
			if err := w.Remove(ctx, repo, "s_1"); err != nil {
				t.Fatal(err)
			}
			if err := w.Restore(ctx, repo, "s_1"); err != nil {
				t.Fatal(err)
			}
			if err := w.CopyIncluded(ctx, repo, "s_1"); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
	// no .worktreeinclude: nothing to do
	repo := testRepo(t)
	os.WriteFile(filepath.Join(repo, ".env"), []byte("A=1\n"), 0o644)
	w := NewWorkspace(filepath.Join(t.TempDir(), "worktrees"))
	if _, _, err := w.Create(context.Background(), repo, "", "s_1"); err != nil {
		t.Fatal(err)
	}
	if err := w.CopyIncluded(context.Background(), repo, "s_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Path("s_1"), ".env")); !os.IsNotExist(err) {
		t.Fatal("copied without .worktreeinclude")
	}
}

func TestIncludePathspecs(t *testing.T) {
	for in, want := range map[string]string{
		"apps/a/Signing.local.xcconfig\n":     ":(literal)apps/a/Signing.local.xcconfig",
		"/config/\n# note\n!config/x\n":       ":(literal)config",
		"apps/*/local.json\nsecrets/**/key\n": ":(literal)apps :(literal)secrets",
		".env\napps/a/x\n":                    "",
		"config/\n":                           "",
		"**/local.json\n":                     "",
		"\n":                                  "",
	} {
		if got := strings.Join(includePathspecs(in), " "); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
