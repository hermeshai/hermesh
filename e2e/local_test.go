package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestLocalAgent: an agent on a machine's own computer (Claude Code with
// the CLI on the Mac that runs hermeshd) reaches that machine without the
// relay: no tunnel through it, and sooner; a machine on another computer
// is reached through the relay as before. With the relay stopped, the
// agent still works with this computer's machine (it starts a session,
// reads it, lists) and is told the others cannot be reached; its inbox,
// which is at the relay, cannot. A revoked agent is refused all the same.
func TestLocalAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	const oneWay = 150 * time.Millisecond
	door, _ := os.MkdirTemp(shortTemp, "hl")
	t.Cleanup(func() { os.RemoveAll(door) })
	here := []string{"HERMESH_LOCAL=on", "HERMESH_LOCAL_DIR=" + door}
	// The owner's Mac serves its own agents; the server is another computer.
	w := newFarOwnerMac(t, oneWay, append([]string{"HERMESH_FAKE_PROVIDERS=1"}, here...), &localv1.GrantSpec{})
	ctx := context.Background()
	daemon, data := filepath.Join(w.bin, "hermeshd"), filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", nil, daemon, "pair", w.addr, "--name", "vps-1", "--no-service", "--data-dir", data)
	if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	w.logs = append(w.logs, start(t, "vps-1", []string{"HERMESH_FAKE_PROVIDERS=1"}, daemon, "run", "--data-dir", data))
	var vpsID string
	eventually(t, 30*time.Second, "the server online", func() bool {
		res, _ := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineName == "vps-1" && m.Online {
				vpsID = m.MachineId
			}
		}
		return vpsID != ""
	})
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "site", Path: gitRepo(t, map[string]string{"README.md": "# site\n"})}); err != nil {
		t.Fatal(err)
	}

	hermesh := filepath.Join(w.bin, "hermesh")
	cli := func(env []string, args ...string) result {
		return run(t, append(append([]string{}, w.cliEnv...), env...), time.Minute, hermesh, args...)
	}
	ok := func(env []string, args ...string) result {
		t.Helper()
		r := cli(env, args...)
		if r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		return r
	}
	tunnels := func() int { return metric(t, w.relayDebug, "hermesh_relay_tunnels_total") }

	// The agent as on another computer: through the relay.
	s, r := w.startSession(time.Minute, "--project", "demo", "--wait", "--prompt", "say hi")
	if r.code != 0 || s.Status != "idle" {
		t.Fatalf("start: %+v", r)
	}
	if tunnels() == 0 {
		t.Fatal("an agent on another computer made no tunnel through the relay")
	}

	// On the Mac itself: no tunnel through the relay for the Mac's machine,
	// one for the server's.
	before := tunnels()
	h := firstSession(ok(here, "session", "start", "--project", "owner-mac/demo", "-o", "json", "--wait", "--prompt", "say here").stdout)
	if h.Status != "idle" {
		t.Fatalf("a session started from the Mac itself: %+v", h)
	}
	ok(here, "session", "get", h.SessionID)
	ok(here, "session", "events", h.SessionID)
	if got := tunnels(); got != before {
		t.Fatalf("%d tunnels through the relay for the Mac's own machine", got-before)
	}
	if out := ok(here, "project", "list").stdout; !strings.Contains(out, "owner-mac/demo") || !strings.Contains(out, "vps-1/site") {
		t.Fatalf("projects: %s", out)
	}
	if got := tunnels(); got != before+1 {
		t.Fatalf("%d tunnels more after listing, want 1: the server's", got-before)
	}

	// And sooner than through the relay (neither with a link, to compare
	// the ways alone).
	timed := func(env ...string) time.Duration {
		best := time.Duration(1 << 62)
		for range 3 {
			began := time.Now()
			ok(append(env, "HERMESH_LINK=off"), "session", "get", h.SessionID)
			best = min(best, time.Since(began))
		}
		return best
	}
	far, near := timed(), timed(here...)
	saved := float64(far-near) / float64(2*oneWay)
	t.Logf("session get: %v through the relay, %v on the Mac itself: %.1f round trips saved", far.Round(time.Millisecond), near.Round(time.Millisecond), saved)
	if saved < 1.5 {
		t.Errorf("the local way saved %.1f round trips, want about 2", saved)
	}

	// The relay stopped: the Mac's own machine can still be used.
	w.relay.stop()
	began := time.Now()
	r = cli(here, "session", "start", "--project", "owner-mac/demo", "-o", "json", "--wait", "--prompt", "say offline")
	off := firstSession(r.stdout)
	if r.code != 0 || off.Status != "idle" || !strings.Contains(r.stderr, "the relay cannot be reached") {
		t.Fatalf("a session started with the relay stopped: %+v", r)
	}
	t.Logf("with the relay stopped: a session started and finished in %v", time.Since(began).Round(time.Millisecond))
	if r := cli(here, "session", "get", off.SessionID, "-o", "json"); r.code != 0 || firstSession(r.stdout).SessionID != off.SessionID {
		t.Fatalf("session get with the relay stopped: %+v", r)
	}
	if r := cli(here, "session", "list"); r.code != 0 || !strings.Contains(r.stdout, off.SessionID) {
		t.Fatalf("session list with the relay stopped: %+v", r)
	}
	if r := cli(here, "project", "list"); r.code != 0 || !strings.Contains(r.stdout, "owner-mac/demo") || strings.Contains(r.stdout, "vps-1/site") {
		t.Fatalf("project list with the relay stopped: %+v", r)
	}
	// The server, and the inbox: not without the relay.
	if r := cli(here, "session", "start", "--project", "vps-1/site", "--prompt", "say hi"); r.code != 4 {
		t.Fatalf("a session on the server with the relay stopped: %+v", r)
	}
	if r := cli(here, "message", "list"); r.code == 0 {
		t.Fatalf("the inbox with the relay stopped: %+v", r)
	}
	// An agent on another computer: nothing, as before.
	if r := cli(nil, "session", "get", off.SessionID); r.code != 4 && r.code != 1 {
		t.Fatalf("an agent on another computer with the relay stopped: %+v", r)
	}

	// Revoked (the Mac's machine knows): refused on the Mac itself too,
	// with the relay back and without.
	w.startRelay()
	eventually(t, 30*time.Second, "the Mac back on the relay", func() bool { return cli(nil, "project", "list").code == 0 })
	eventually(t, 30*time.Second, "the agent revoked, once the owner's app is back on the relay too", func() bool {
		clients, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil || len(clients.Clients) != 1 {
			return false
		}
		_, err = w.app.client.RevokeClient(ctx, &localv1.RevokeClientRequest{GrantId: clients.Clients[0].GrantId})
		return err == nil
	})
	eventually(t, 15*time.Second, "the revoked agent told so", func() bool {
		r := cli(here, "session", "get", off.SessionID, "-o", "json")
		return r.code == 3 && strings.Contains(r.stdout, `"revoked"`)
	})
	w.relay.stop()
	if r := cli(here, "session", "get", off.SessionID); r.code == 0 {
		t.Fatalf("a revoked agent read a session with the relay stopped: %+v", r)
	}
}
