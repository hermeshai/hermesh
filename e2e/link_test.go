//go:build unix

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// linkPID is the process serving the link of the agent whose home is home
// (0: none wrote the lock).
func linkPID(home string) int {
	b, _ := os.ReadFile(filepath.Join(home, "link", "lock"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// TestAgentLink: an agent's commands share one connection to the relay
// (docs/cli.md, the link). The first command connects by itself and starts
// the link; those after it reach the relay and their machines through it:
// no connection of their own, and round trips fewer than a command that
// connects by itself. Nothing depends on the link: with it killed a command
// works as before (and starts another), and a revoked agent is told so. The
// link ends when no command used it for a while, and with its home.
func TestAgentLink(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	const oneWay = 150 * time.Millisecond
	w := newFarOwnerMac(t, oneWay, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	hermesh, home := filepath.Join(w.bin, "hermesh"), filepath.Join(w.tmp, "agent")
	// The agents' connections the relay saw come, so far.
	connected := func() int {
		n := 0
		for _, l := range strings.Split(w.relay.output(), "\n") {
			if strings.Contains(l, `msg="peer connected"`) && strings.Contains(l, "role=ROLE_CLIENT") {
				n++
			}
		}
		return n
	}
	cli := func(args ...string) result {
		t.Helper()
		r := w.cli(time.Minute, args...)
		if r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		return r
	}

	// The first command: its own connection, and the link it starts.
	base := connected()
	cli("project", "list")
	eventually(t, 15*time.Second, "the link connected", func() bool { return alive(linkPID(home)) && connected() == base+2 })
	link := linkPID(home)

	// Those after it: none of their own.
	s, r := w.startSession(time.Minute, "--wait", "--prompt", "say hi")
	if r.code != 0 || s.Status != "idle" {
		t.Fatalf("start: %+v", r)
	}
	for _, args := range [][]string{{"project", "list"}, {"session", "get", s.SessionID}, {"session", "list"}, {"message", "list"},
		{"session", "events", s.SessionID}} {
		cli(args...)
	}
	if got := connected(); got != base+2 {
		t.Fatalf("%d connections from the agent, want %d: commands after the first made their own", got-base, 2)
	}

	// And sooner than a command that connects by itself.
	timed := func(env ...string) time.Duration {
		best := time.Duration(1 << 62)
		for range 3 {
			began := time.Now()
			if r := run(t, append(w.cliEnv, env...), time.Minute, hermesh, "session", "get", s.SessionID); r.code != 0 {
				t.Fatalf("session get: %+v", r)
			}
			best = min(best, time.Since(began))
		}
		return best
	}
	alone, linked := timed("HERMESH_LINK=off"), timed()
	saved := float64(alone-linked) / float64(2*oneWay)
	t.Logf("session get: %v by itself, %v through the link: %.1f round trips saved", alone.Round(time.Millisecond), linked.Round(time.Millisecond), saved)
	// The upgrade and the end-to-end handshake, two round trips here; a real
	// relay's TCP and TLS handshakes come on top.
	if saved < 1.5 {
		t.Errorf("the link saved %.1f round trips, want about 2", saved)
	}

	// The link killed: the next command works by itself, and starts another.
	before := connected()
	syscall.Kill(link, syscall.SIGKILL)
	eventually(t, 5*time.Second, "the link gone", func() bool { return !alive(link) })
	cli("session", "get", s.SessionID)
	eventually(t, 15*time.Second, "another link", func() bool {
		pid := linkPID(home)
		return pid != link && alive(pid) && connected() == before+2
	})
	cli("session", "get", s.SessionID)
	if got := connected(); got != before+2 {
		t.Fatalf("%d connections more, want 2: the new link was not used", got-before)
	}

	// A link no command uses ends by itself.
	syscall.Kill(linkPID(home), syscall.SIGKILL)
	eventually(t, 5*time.Second, "the link gone", func() bool { return !alive(linkPID(home)) })
	brief := start(t, "link", w.cliEnv, hermesh, "link", "--idle", "2s")
	eventually(t, 15*time.Second, "the link serving", func() bool { return linkPID(home) == brief.cmd.Process.Pid })
	before = connected()
	cli("session", "get", s.SessionID)
	if got := connected(); got > before+1 { // the link's own, if it came after before was read
		t.Fatalf("%d connections more: the command did not use the link", got-before)
	}
	if code := brief.wait(t, 20*time.Second); code != 0 {
		t.Fatalf("the idle link ended with %d: %s", code, brief.output())
	}

	// It ends with its home: another agent's, here.
	ctx := context.Background()
	home2 := filepath.Join(w.tmp, "agent2")
	env2 := []string{"HERMESH_HOME=" + home2}
	login := start(t, "login2", env2, hermesh, "auth", "login", w.addr, "--name", "bot2")
	if _, err := w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login.pairingCode(t), Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	if c := login.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("login: %s", login.output())
	}
	if r := run(t, env2, time.Minute, hermesh, "project", "list"); r.code != 0 {
		t.Fatalf("project list: %+v", r)
	}
	eventually(t, 15*time.Second, "the second agent's link", func() bool { return alive(linkPID(home2)) })
	link2 := linkPID(home2)
	os.RemoveAll(home2)
	eventually(t, 15*time.Second, "the link gone with its home", func() bool { return !alive(link2) })

	cli("project", "list")
	eventually(t, 15*time.Second, "a link again", func() bool { return alive(linkPID(home)) })
	link = linkPID(home)
	clients, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// The owner changes the grant: the command that learns of it works, the
	// link connects anew with the grant its home now holds (the relay and
	// the machines go by the one they were shown), and is used as before.
	before = connected()
	for _, c := range clients.Clients {
		if c.Label == "bot" {
			if _, err := w.app.client.UpdateClient(ctx, &localv1.UpdateClientRequest{GrantId: c.GrantId, Spec: &localv1.GrantSpec{Label: "bot", TtlDays: 3}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	eventually(t, 15*time.Second, "the new grant adopted", func() bool {
		cli("session", "get", s.SessionID)
		return strings.Contains(cli("auth", "status").stdout, "v2")
	})
	cli("session", "get", s.SessionID)
	settled := connected()
	cli("session", "get", s.SessionID)
	cli("session", "list")
	if got := connected(); got != settled || linkPID(home) != link {
		t.Fatalf("after the grant changed: %d connections more, link %d (was %d): commands made their own", got-settled, linkPID(home), link)
	}
	t.Logf("the grant changed: %d connections more until the link was used again", settled-before)

	// Revoked: the command says so, as one that connects by itself does,
	// and the link ends.
	for _, c := range clients.Clients {
		if c.Label == "bot" {
			if _, err := w.app.client.RevokeClient(ctx, &localv1.RevokeClientRequest{GrantId: c.GrantId}); err != nil {
				t.Fatal(err)
			}
		}
	}
	eventually(t, 15*time.Second, "the revoked agent told so", func() bool {
		r := w.cli(time.Minute, "project", "list", "-o", "json")
		return r.code == 3 && strings.Contains(r.stdout, `"revoked"`)
	})
	eventually(t, 15*time.Second, "the revoked agent's link gone", func() bool { return !alive(link) })
}

// TestAgentLinkThatFails: a command never waits long on its link. One that
// does not answer (frozen here), and one whose connection to the relay died
// without a word, so that the command's first calls get no answer: either
// way the command goes on by itself after a few seconds, and works.
func TestAgentLinkThatFails(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	home := filepath.Join(w.tmp, "agent")
	// The agent reaches the relay over a network of its own.
	statePath := filepath.Join(home, "state.json")
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	json.Unmarshal(b, &st)
	network, relay := newCutProxy(t, strings.TrimPrefix(st["relay_url"].(string), "http://"), 0)
	st["relay_url"] = "http://" + relay
	b, _ = json.Marshal(st)
	if err := os.WriteFile(statePath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	list := func() time.Duration {
		t.Helper()
		began := time.Now()
		if r := w.cli(time.Minute, "session", "list"); r.code != 0 {
			t.Fatalf("session list: %+v", r)
		}
		return time.Since(began)
	}
	// The agent's connections the relay saw come, so far.
	connected := func() int {
		n := 0
		for _, l := range strings.Split(w.relay.output(), "\n") {
			if strings.Contains(l, `msg="peer connected"`) && strings.Contains(l, "role=ROLE_CLIENT") {
				n++
			}
		}
		return n
	}
	// used: a command makes no connection of its own, the link is the same.
	var link int
	used := func() bool {
		before := connected()
		list()
		return connected() == before && linkPID(home) == link
	}
	base := connected()
	list()
	eventually(t, 15*time.Second, "the link connected", func() bool { return alive(linkPID(home)) && connected() == base+2 })
	link = linkPID(home)
	if !used() {
		t.Fatal("the command after the first made a connection of its own")
	}

	// The link frozen: it takes the command's connection and says nothing.
	syscall.Kill(link, syscall.SIGSTOP)
	took := list()
	syscall.Kill(link, syscall.SIGCONT)
	t.Logf("a frozen link: the command took %v", took.Round(100*time.Millisecond))
	if took < 2*time.Second || took > 15*time.Second {
		t.Errorf("with its link frozen the command took %v, want the 3 s it waits for it and what a command takes", took)
	}

	// The link's connection dead without a word, a moment after the relay
	// answered on it: the command's first calls go nowhere.
	list()
	network.setCut(true)
	network.setCut(false)
	took = list()
	t.Logf("a link whose connection died: the command took %v", took.Round(100*time.Millisecond))
	if took < 2*time.Second || took > 20*time.Second {
		t.Errorf("with its link's connection dead the command took %v, want the 5 s its first calls get and what a command takes", took)
	}
	// The link notices and connects again: commands go through it as before.
	eventually(t, 90*time.Second, "the link used again", used)
}
