package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestSlowStart: on a machine too busy to start sessions at their usual
// pace, a start is slow, not failed; one that fails or is stopped leaves
// nothing in the way, and keeps a task the agent never got for the next
// message. The agent that asked and the owner's iPhone hear of a start
// that failed, and why, each time it fails.
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

	t.Run("failed start is told, and why", func(t *testing.T) {
		spool := filepath.Join(t.TempDir(), "push")
		w.relayArgs = append(w.relayArgs, "--push-spool", spool)
		w.restartRelay()
		eventually(t, 30*time.Second, "the machine is back on the relay", func() bool {
			r := w.cli(30*time.Second, "project", "list")
			return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo") && !strings.Contains(r.stdout, "offline")
		})
		key, _ := ecdh.X25519().GenerateKey(rand.Reader)
		eventually(t, 30*time.Second, "the iPhone registered", func() bool {
			_, err := w.app.client.RegisterPush(context.Background(), &localv1.RegisterPushRequest{Token: "abc123", Environment: "development",
				NoticePublicKey: key.PublicKey().Bytes(), Kinds: []string{"failure"}})
			return err == nil
		})
		time.Sleep(time.Second) // the machine hears of the registration over its update stream
		seen := map[string]bool{}
		// told waits for the session's next news and notice: both say the
		// start failed, with what Codex did.
		told := func(id string) {
			t.Helper()
			for {
				m := w.waitMessage()
				w.ack(m.ID)
				if m.News == nil || m.News.SessionID != id { // of the sessions before
					continue
				}
				if m.News.Kind != "failure" || m.News.Failure == nil || *m.News.Failure != "start_failed" || !strings.Contains(m.Text, "codex exited") {
					t.Fatalf("news of the failed start: %+v", m)
				}
				break
			}
			eventually(t, 30*time.Second, "a notice of the failed start", func() bool {
				for _, s := range readSpool(t, spool, seen) {
					sealed, _ := base64.StdEncoding.DecodeString(s.Payload.Hermesh.N)
					n := &sessionv1.Notice{}
					if err := identity.OpenNotice(key.Bytes(), sealed, n); err != nil {
						t.Fatalf("open notice: %v", err)
					}
					if n.SessionId != id {
						continue
					}
					if n.Kind != "failure" || n.Failure != sessionv1.FailureKind_FAILURE_KIND_START_FAILED || !strings.Contains(n.FailureMessage, "codex exited") {
						t.Fatalf("notice of the failed start: %+v", n)
					}
					return true
				}
				return false
			})
		}
		mark("fail")
		s, r := w.startSession(time.Minute, "--agent", "codex", "--prompt", "third task")
		if s.SessionID == "" {
			t.Fatalf("start: %s %s", r.stdout, r.stderr)
		}
		told(s.SessionID)
		mark("fail")
		if r := w.cli(time.Minute, "session", "send", s.SessionID, "--message", "again"); r.code != 0 {
			t.Fatalf("send: %s %s", r.stdout, r.stderr)
		}
		told(s.SessionID)
	})
}
