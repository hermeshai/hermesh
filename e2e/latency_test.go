package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// delayProxy forwards TCP connections to target, holding every byte for
// oneWay in each direction: a far relay, with a round trip of 2*oneWay.
func delayProxy(t *testing.T, target string, oneWay time.Duration) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	pump := func(dst, src net.Conn) {
		type chunk struct {
			at time.Time
			b  []byte
		}
		ch := make(chan chunk, 4096)
		go func() {
			defer close(ch)
			for {
				b := make([]byte, 32<<10)
				n, err := src.Read(b)
				if n > 0 {
					ch <- chunk{time.Now().Add(oneWay), b[:n]}
				}
				if err != nil {
					return
				}
			}
		}()
		for c := range ch {
			time.Sleep(time.Until(c.at))
			if _, err := dst.Write(c.b); err != nil {
				break
			}
		}
		io.Copy(io.Discard, src)
		dst.Close()
		src.Close()
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			r, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			go pump(r, c)
			go pump(c, r)
		}
	}()
	return l.Addr().String()
}

// TestRelayRoundTrips counts the round trips to a far relay that each kind
// of command waits for: connecting, admission, the grant check, the machine
// list and the E2E handshake go out together, so a command costs the
// WebSocket upgrade, one round of all of those, and its own calls.
func TestRelayRoundTrips(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	s, r := w.startSession(time.Minute, "--wait", "--prompt", "say hi")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}

	// Point the agent at the relay through the proxy.
	statePath := filepath.Join(w.tmp, "agent", "state.json")
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	json.Unmarshal(b, &st)
	direct := st["relay_url"].(string)
	setRelay := func(u string) {
		st["relay_url"] = u
		b, _ := json.Marshal(st)
		os.WriteFile(statePath, b, 0o600)
	}
	const oneWay = 100 * time.Millisecond
	far := "http://" + delayProxy(t, direct[len("http://"):], oneWay)

	timed := func(args ...string) time.Duration {
		best := time.Duration(1 << 62)
		for range 3 {
			began := time.Now()
			if r := w.cli(time.Minute, args...); r.code != 0 {
				t.Fatalf("%v: %+v", args, r)
			}
			best = min(best, time.Since(began))
		}
		return best
	}
	for _, c := range []struct {
		args []string
		max  float64 // round trips
	}{
		{[]string{"project", "list"}, 3.5},
		{[]string{"session", "get", s.SessionID}, 3.5},
		{[]string{"session", "list"}, 3.5},
		{[]string{"session", "start", "--project", "owner-mac/demo", "--prompt", "say hi"}, 3.5},
		{[]string{"session", "start", "--project", "demo", "--prompt", "say hi"}, 4.5},
	} {
		setRelay(direct)
		local := timed(c.args...)
		setRelay(far)
		remote := timed(c.args...)
		rounds := float64(remote-local) / float64(2*oneWay)
		t.Logf("%-50s %4.1f round trips (%v far, %v near)", fmt.Sprint(c.args), rounds, remote.Round(time.Millisecond), local.Round(time.Millisecond))
		if rounds > c.max {
			t.Errorf("%v: %.1f round trips to the relay, want at most %.1f", c.args, rounds, c.max)
		}
	}

	// The last machine list only picks what to connect to early: missing,
	// stale, or warmed with a grant the owner narrowed since (the machine
	// refuses it after the handshake), commands still work.
	ctx := context.Background()
	clients, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	oldGrant := st["grant"]
	for _, c := range []struct {
		name string
		prep func()
	}{
		{"no machine list", func() { delete(st, "machines") }},
		{"stale machine list", func() {
			st["machines"] = []map[string]any{{"id": "m_" + strings.Repeat("a", 24), "name": "owner-mac", "online": true}}
		}},
		{"grant changed by the owner", func() {
			if _, err := w.app.client.UpdateClient(ctx, &localv1.UpdateClientRequest{GrantId: clients.Clients[0].GrantId,
				Spec: &localv1.GrantSpec{Label: "bot", Agents: []string{"claude"}}}); err != nil { // narrower: v1 stops working
				t.Fatal(err)
			}
			setRelay(direct)
			eventually(t, 30*time.Second, "updated grant", func() bool {
				return strings.Contains(w.cli(time.Minute, "auth", "status").stdout, "updated v1 → v2")
			})
			st["grant"] = oldGrant // the agent has not picked it up yet
		}},
	} {
		c.prep()
		setRelay(far)
		for _, args := range [][]string{{"session", "start", "--project", "owner-mac/demo", "--prompt", "say hi"}, {"session", "get", s.SessionID}, {"project", "list"}} {
			if r := w.cli(time.Minute, args...); r.code != 0 || !strings.Contains(r.stdout, "owner-mac") {
				t.Fatalf("%s, %v: %+v", c.name, args, r)
			}
		}
		b, _ := os.ReadFile(statePath)
		var now struct {
			Machines []struct{ ID, Name string }
			Grant    string
		}
		json.Unmarshal(b, &now)
		if len(now.Machines) != 1 || now.Machines[0].Name != "owner-mac" || strings.HasPrefix(now.Machines[0].ID, "m_aaaa") {
			t.Fatalf("%s: machine list not brought up to date: %s", c.name, b)
		}
		if now.Grant == oldGrant && c.name == "grant changed by the owner" {
			t.Fatalf("%s: the new grant was not picked up", c.name)
		}
	}
}
