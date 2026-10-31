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

// A release tag exists upstream but not locally: starting fails without
// fetching or running the agent, and syncing the tag lets the same session
// execute its original task along with the retry message.
func TestMissingBaseRefRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	remote := filepath.Join(t.TempDir(), "upstream.git")
	gitOut(t, w.repo, "clone", "--bare", w.repo, remote)
	gitOut(t, w.repo, "remote", "add", "upstream", remote)
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			if fallback {
				gitOut(t, w.repo, "config", "extensions.worktreeConfig", "true")
			}
			ref := fmt.Sprintf("ios-release-%v", fallback)
			gitOut(t, remote, "tag", ref, w.repoHead)
			s, r := w.startSession(time.Minute, "--base", ref, "--wait", "--prompt", "write original.txt original task\nsay original executed")
			if r.code != 5 || s.Status != "crashed" || s.LastFailure == nil || s.LastFailure.Kind != "base_ref_unavailable" {
				t.Fatalf("missing base: %+v\n%s", s, r.stdout+r.stderr)
			}
			for _, text := range []string{"demo", "local repository", ref} {
				if !strings.Contains(s.LastFailure.Message, text) {
					t.Fatalf("failure missing %q: %+v", text, s.LastFailure)
				}
			}
			if s.Ref != "" || !strings.Contains(s.LastFailure.Hint, "sync") || !strings.Contains(s.LastFailure.Hint, "same session") {
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
			if err := exec.Command("git", "-C", w.repo, "rev-parse", "--verify", "--quiet", ref).Run(); err == nil {
				t.Fatal("start fetched the upstream tag")
			}
			if retried, r := w.send(time.Minute, s.SessionID, "say retry before sync"); r.code != 5 || retried.LastFailure == nil || retried.LastFailure.Kind != "base_ref_unavailable" {
				t.Fatalf("retry without syncing: %+v\n%s", retried, r.stdout+r.stderr)
			}
			assertMissing()
			if _, r := w.startSession(time.Minute, "--wait", "--prompt", "say other session works"); r.code != 0 {
				t.Fatalf("another session: %+v", r)
			}
			gitOut(t, w.repo, "fetch", "--no-tags", "upstream", "refs/tags/"+ref+":refs/tags/"+ref)
			got, r := w.send(time.Minute, s.SessionID, "write retry.txt retry message\nsay retry executed")
			if r.code != 0 || got.SessionID != s.SessionID || got.Status != "idle" || got.Result != "retry executed" {
				t.Fatalf("retry after syncing: %+v\n%s", got, r.stdout+r.stderr)
			}
			for file, want := range map[string]string{"original.txt": "original task", "retry.txt": "retry message"} {
				b, err := os.ReadFile(filepath.Join(w.worktree(s.SessionID), file))
				if err != nil || strings.TrimSpace(string(b)) != want {
					t.Fatalf("%s: %q, %v", file, b, err)
				}
			}
			if head := gitOut(t, w.repo, "rev-parse", "HEAD"); head != w.repoHead || gitOut(t, w.repo, "status", "--porcelain") != "" {
				t.Fatal("the project's checkout changed")
			}
			gitOut(t, w.repo, "fsck", "--no-progress")
		})
	}
}
