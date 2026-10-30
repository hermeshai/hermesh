package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestSlowStart: on a machine too busy to start sessions at their usual
// pace, a start is slow, not failed; one that fails or is stopped leaves
// nothing in the way, and keeps a task the agent never got for the next
// message.
func TestSlowStart(t *testing.T) {
	unixOnly(t, "its fake codex and git filter are shell scripts")
	if testing.Short() {
		t.Skip("e2e")
	}
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex")
	os.WriteFile(codex, []byte(`#!/bin/sh
case "$1" in
--version) echo "codex-cli 0.164.0";;
login) echo "Logged in using ChatGPT";;
*) [ -f `+dir+`/slow ] && export FAKE_CODEX_START_MS=35000
   FAKE_CODEX_FAIL_START=`+dir+`/fail exec `+filepath.Join(binaries(t), "fakecodex")+` "$@";;
esac
`), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_CODEX_BIN=" + codex}, &localv1.GrantSpec{Agents: []string{"codex"}})
	eventually(t, 30*time.Second, "codex checked", func() bool {
		res, err := w.app.client.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
		if err != nil || len(res.Machines) == 0 {
			return false
		}
		for _, a := range res.Machines[0].Status.GetAgents() {
			if a.Name == "codex" && a.LoggedIn {
				return true
			}
		}
		return false
	})
	mark := func(name string) { os.WriteFile(filepath.Join(dir, name), nil, 0o644) }

	t.Run("slow agent", func(t *testing.T) {
		// Codex answers thread/start after 35 s, past the 30 s it once had.
		mark("slow")
		defer os.Remove(filepath.Join(dir, "slow"))
		s, r := w.startSession(3*time.Minute, "--agent", "codex", "--wait", "--prompt", "slow hello")
		if s.Status != "idle" || s.Result != "done: slow hello" {
			t.Fatalf("start: %+v\n%s %s", s, r.stdout, r.stderr)
		}
	})

	t.Run("failed start keeps the task", func(t *testing.T) {
		mark("fail")
		s, _ := w.startSession(time.Minute, "--agent", "codex", "--wait", "--prompt", "first task")
		eventually(t, 30*time.Second, "start failed", func() bool { return w.get(s.SessionID).Status == "crashed" })
		got, r := w.send(time.Minute, s.SessionID, "go on")
		if got.Status != "idle" || got.Result != "done: first task\n\ngo on" {
			t.Fatalf("after the next message: %+v\n%s %s", got, r.stdout, r.stderr)
		}
	})

	t.Run("checkout stopped halfway", func(t *testing.T) {
		// A repository whose worktrees git adds itself (per-worktree
		// config), with a checkout that stalls while dir/stall exists.
		gitOut(t, w.repo, "config", "extensions.worktreeConfig", "true")
		gitOut(t, w.repo, "config", "filter.slow.smudge", `sh -c 'touch `+dir+`/smudging; while [ -f `+dir+`/stall ]; do sleep 0.1; done; cat'`)
		gitOut(t, w.repo, "config", "filter.slow.clean", "cat")
		gitOut(t, w.repo, "config", "filter.slow.required", "true")
		os.WriteFile(filepath.Join(w.repo, ".git", "info", "attributes"), []byte("README.md filter=slow\n"), 0o644)
		mark("stall")
		s, r := w.startSession(time.Minute, "--agent", "codex", "--prompt", "never runs")
		if r.code != 0 {
			t.Fatalf("start: %s %s", r.stdout, r.stderr)
		}
		eventually(t, time.Minute, "checkout under way", func() bool {
			_, err := os.Stat(filepath.Join(dir, "smudging"))
			return err == nil
		})
		if r := w.cli(30*time.Second, "session", "interrupt", s.SessionID); r.code != 0 {
			t.Fatalf("interrupt: %s %s", r.stdout, r.stderr)
		}
		eventually(t, 30*time.Second, "start stopped", func() bool { return w.get(s.SessionID).Status == "idle" })
		if _, err := os.Stat(w.worktree(s.SessionID)); !os.IsNotExist(err) {
			t.Fatalf("half checkout left at %s", w.worktree(s.SessionID))
		}
		if err := exec.Command("git", "-C", w.repo, "rev-parse", "--verify", "-q", "hermesh/"+s.SessionID).Run(); err == nil {
			t.Fatal("its branch was left behind")
		}
		os.Remove(filepath.Join(dir, "stall"))
		got, r := w.send(time.Minute, s.SessionID, "now run")
		if got.Status != "idle" || got.Result != "done: now run" {
			t.Fatalf("after the next message: %+v\n%s %s", got, r.stdout, r.stderr)
		}
		if st := gitOut(t, w.worktree(s.SessionID), "status", "--porcelain"); strings.Contains(st, "README.md") {
			t.Fatalf("the worktree is not a clean checkout: %q", st)
		}
	})
}
