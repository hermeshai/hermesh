package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// A release tag pushed after the project was cloned: the session fetches
// that tag alone from the project's remote and starts from it. A base that
// is on no remote yet fails without running the agent, and once it is
// pushed the same session executes its original task along with the retry
// message.
func TestMissingBaseRefRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	remote := filepath.Join(t.TempDir(), "upstream.git")
	gitOut(t, w.repo, "clone", "--bare", w.repo, remote)
	gitOut(t, w.repo, "remote", "add", "upstream", remote)
	// Another computer pushes a commit the project does not have, and tags.
	other := filepath.Join(t.TempDir(), "other")
	gitOut(t, w.repo, "clone", remote, other)
	if err := os.WriteFile(filepath.Join(other, "release.txt"), []byte("released\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, other, "add", "release.txt")
	gitOut(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "release")
	push := func(tag string) {
		gitOut(t, other, "tag", tag)
		gitOut(t, other, "push", "origin", "refs/tags/"+tag)
	}
	hasTag := func(tag string) bool {
		return exec.Command("git", "-C", w.repo, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag).Run() == nil
	}
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			if fallback {
				gitOut(t, w.repo, "config", "extensions.worktreeConfig", "true")
			}
			pushed, unasked := fmt.Sprintf("ios-release-%v", fallback), fmt.Sprintf("unasked-%v", fallback)
			push(pushed)
			push(unasked)
			s, r := w.startSession(time.Minute, "--base", pushed, "--wait", "--prompt", "say started from the release")
			if r.code != 0 || s.Status != "idle" || s.Result != "started from the release" {
				t.Fatalf("base on the remote only: %+v\n%s", s, r.stdout+r.stderr)
			}
			if b, err := os.ReadFile(filepath.Join(w.worktree(s.SessionID), "release.txt")); err != nil || string(b) != "released\n" {
				t.Fatalf("the worktree is not at the pushed commit: %q, %v", b, err)
			}
			if !hasTag(pushed) || hasTag(unasked) {
				t.Fatalf("fetched %s: %v, and %s: %v", pushed, hasTag(pushed), unasked, hasTag(unasked))
			}

			ref := fmt.Sprintf("ios-next-%v", fallback)
			s, r = w.startSession(time.Minute, "--base", ref, "--wait", "--prompt", "write original.txt original task\nsay original executed")
			if r.code != 5 || s.Status != "crashed" || s.LastFailure == nil || s.LastFailure.Kind != "base_ref_unavailable" {
				t.Fatalf("missing base: %+v\n%s", s, r.stdout+r.stderr)
			}
			for _, text := range []string{`"demo"`, "local repository", ref, "or on its remote upstream"} {
				if !strings.Contains(s.LastFailure.Message, text) {
					t.Fatalf("failure missing %q: %+v", text, s.LastFailure)
				}
			}
			if s.Ref != "" || !strings.Contains(s.LastFailure.Hint, "project's remote") || !strings.Contains(s.LastFailure.Hint, "same session") {
				t.Fatalf("agent started or recovery advice missing: %+v", s)
			}
			branch := "refs/heads/hermesh/" + s.SessionID
			assertMissing := func() {
				t.Helper()
				if _, err := os.Stat(w.worktree(s.SessionID)); !os.IsNotExist(err) {
					t.Fatalf("failed start left a worktree: %v", err)
				}
				if err := exec.Command("git", "-C", w.repo, "show-ref", "--verify", "--quiet", branch).Run(); err == nil {
					t.Fatal("failed start left a branch")
				}
				if out := gitOut(t, w.repo, "worktree", "list", "--porcelain"); strings.Contains(out, s.SessionID) {
					t.Fatalf("failed start left worktree metadata: %s", out)
				}
			}
			assertMissing()
			if retried, r := w.send(time.Minute, s.SessionID, "say retry before the push"); r.code != 5 || retried.LastFailure == nil || retried.LastFailure.Kind != "base_ref_unavailable" {
				t.Fatalf("retry before the push: %+v\n%s", retried, r.stdout+r.stderr)
			}
			assertMissing()
			if _, r := w.startSession(time.Minute, "--wait", "--prompt", "say other session works"); r.code != 0 {
				t.Fatalf("another session: %+v", r)
			}
			push(ref)
			got, r := w.send(time.Minute, s.SessionID, "write retry.txt retry message\nsay retry executed")
			if r.code != 0 || got.SessionID != s.SessionID || got.Status != "idle" || got.Result != "retry executed" {
				t.Fatalf("retry after the push: %+v\n%s", got, r.stdout+r.stderr)
			}
			for file, want := range map[string]string{"original.txt": "original task", "retry.txt": "retry message", "release.txt": "released"} {
				b, err := os.ReadFile(filepath.Join(w.worktree(s.SessionID), file))
				if err != nil || strings.TrimSpace(string(b)) != want {
					t.Fatalf("%s: %q, %v", file, b, err)
				}
			}
			if head := gitOut(t, w.repo, "rev-parse", "HEAD"); head != w.repoHead || gitOut(t, w.repo, "status", "--porcelain") != "" {
				t.Fatal("the project's checkout changed")
			}
			if !strings.Contains(w.owner.output(), `msg="fetched the base ref"`) {
				t.Fatal("the fetch is not in the machine's log")
			}
			gitOut(t, w.repo, "fsck", "--no-progress")
		})
	}
}
