//go:build unix

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// procNamed is the process w started last under name.
func procNamed(t *testing.T, w *ownerMac, name string) *proc {
	t.Helper()
	for i := len(w.logs) - 1; i >= 0; i-- {
		if w.logs[i].name == name {
			return w.logs[i]
		}
	}
	t.Fatalf("no process named %s", name)
	return nil
}

// machine is the machine as the screen shows it now (nil: not shown).
func (s *screen) machine(id string) *localv1.Machine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.machines[id]
}

// TestFrozenMachineNoticed: a machine stops answering while its connection
// to the relay stands (frozen here; a Mac that fell asleep is the same).
// The owner's app says it cannot be reached about 20 s later: the tunnel it
// follows the machine through checks for that, and so does the relay for
// the machine's connection (either took 30–45 s, by yamux's keepalive). The
// app follows the machine again once it answers.
func TestFrozenMachineNoticed(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	// Mostly waiting (for pings to go unanswered): it runs beside the other
	// tests that are, after the rest of the suite.
	t.Parallel()
	w, vpsID, _ := farOwnerWithVPS(t, 0)
	vps := procNamed(t, w, "vps-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scr := watchScreen(ctx, t, w)
	followed := func() bool {
		m := scr.machine(vpsID)
		return m != nil && m.Online && m.Unreachable == "" && !m.Syncing && m.Status != nil
	}
	eventually(t, 15*time.Second, "the machine followed", followed)

	if err := vps.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	frozen := time.Now()
	var seen *localv1.Machine
	eventually(t, 27*time.Second, "the frozen machine shown as unreachable", func() bool {
		seen = scr.machine(vpsID)
		return seen != nil && seen.Unreachable != ""
	})
	t.Logf("noticed after %v: %q (the relay lists it online: %v)", time.Since(frozen).Round(100*time.Millisecond), seen.Unreachable, seen.Online)

	if err := vps.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the machine followed again", followed)
}

// TestMachineBackAfterDeadLink: the path between a machine and the relay
// dies without a word (the machine changed networks; or it slept, and its
// connection was gone when it woke). The machine notices by itself and
// connects again: within the time an unanswered ping takes while it runs
// (yamux's keepalive took 30–45 s), and at once after it did not run for a
// while. The owner's app then follows it again. With no network at all, the
// relay shows the machine as offline about 20 s after it last heard from it.
func TestMachineBackAfterDeadLink(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	// Mostly waiting (for pings to go unanswered): it runs beside the other
	// tests that are, after the rest of the suite.
	t.Parallel()
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The machine reaches the relay over a network of its own.
	network, relay := newCutProxy(t, strings.TrimPrefix(st.RelayUrl, "http://"), 0)
	daemon, data := filepath.Join(w.bin, "hermeshd"), filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", env, daemon, "pair", "http://"+relay+"/"+st.OwnerId, "--name", "vps-1", "--no-service", "--data-dir", data)
	if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	vps := start(t, "vps-1", env, daemon, "run", "--data-dir", data)
	w.logs = append(w.logs, vps)
	connections := func() int { return strings.Count(vps.output(), "connected to relay") }

	scr := watchScreen(ctx, t, w)
	followed := func() bool {
		scr.mu.Lock()
		defer scr.mu.Unlock()
		for _, m := range scr.machines {
			if m.MachineName == "vps-1" {
				return m.Online && m.Unreachable == "" && !m.Syncing && m.Status != nil
			}
		}
		return false
	}
	eventually(t, 30*time.Second, "the machine followed", followed)

	// The path dies while the machine runs: what it had goes nowhere, a new
	// connection gets through.
	dead := func() {
		network.setCut(true)
		network.setCut(false)
	}
	dead()
	began := time.Now()
	eventually(t, 29*time.Second, "the machine connected again", func() bool { return connections() == 2 })
	t.Logf("a dead path: connected again after %v", time.Since(began).Round(100*time.Millisecond))
	eventually(t, 20*time.Second, "the machine followed again", followed)

	// The machine sleeps, and its connection is gone when it wakes.
	if err := vps.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	dead()
	time.Sleep(10 * time.Second)
	if err := vps.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	began = time.Now()
	eventually(t, 9*time.Second, "the machine connected again after its sleep", func() bool { return connections() == 3 })
	t.Logf("after a sleep: connected again after %v", time.Since(began).Round(100*time.Millisecond))
	eventually(t, 20*time.Second, "the machine followed again", followed)

	// The machine's network goes away for good: the relay, hearing nothing
	// from it, says it is offline (within 30–45 s before), and online again
	// once the network is back.
	network.setCut(true)
	began = time.Now()
	eventually(t, 26*time.Second, "the machine shown as offline", func() bool {
		scr.mu.Lock()
		defer scr.mu.Unlock()
		for _, m := range scr.machines {
			if m.MachineName == "vps-1" {
				return !m.Online
			}
		}
		return false
	})
	t.Logf("no network: shown as offline after %v", time.Since(began).Round(100*time.Millisecond))
	network.setCut(false)
	eventually(t, 90*time.Second, "the machine followed again", followed)
}
