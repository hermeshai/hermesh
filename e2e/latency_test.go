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
	"github.com/hermeshai/hermesh/pkg/identity"
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
	// Long enough that a busy machine's jitter (CI: up to ~150 ms a
	// command) stays well under the half round trip the limits allow.
	const oneWay = 250 * time.Millisecond
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

// TestMCPRoundTrips counts the round trips to a far machine that MCP tool
// calls wait for. The gateway keeps a connection's relay connection and its
// tunnels from one call to the next: the first call costs the E2E handshake
// and the call, later ones the call alone. Once the owner changes the grant
// (a rename), the next call makes a tunnel with the new grant, and the
// connection goes on.
func TestMCPRoundTrips(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})
	ctx := context.Background()
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--mcp", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relay)
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })

	// The Mac is far from the relay: its link, which carries the tunnels to
	// its machine, goes through the proxy. The app reaches the relay directly.
	const oneWay = 250 * time.Millisecond
	far := "http://" + delayProxy(t, fmt.Sprintf("127.0.0.1:%d", port), oneWay)
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sock := filepath.Join(sockDir, "o.sock")
	owner := start(t, "owner", []string{"HERMESH_FAKE_PROVIDERS=1"}, filepath.Join(bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(tmp, "owner-mac"), "--socket", sock)
	logs = append(logs, owner)
	root, _ := identity.GenerateKey()
	mac := newTestApp(t, sock, root)
	if _, err := mac.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: far, ClaimCode: claim, RootPublicKey: rootPub(root), RootLabel: "test-mac", RootPlatform: "macos"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	local, err := mac.client.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: "owner-mac"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "machine online", func() bool {
		res, err := mac.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && len(res.Machines) == 1 && res.Machines[0].Online
	})
	repo := gitRepo(t, map[string]string{"README.md": "# demo\n"})
	if _, err := mac.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: local.MachineId, ProjectId: "demo", Path: repo}); err != nil {
		t.Fatal(err)
	}

	// An app connects, and the owner approves it on the Mac.
	app := newOAuthApp(t, mcpAddress(t, mac), "Far app")
	if page := app.connect(app.authorize()); !strings.Contains(page, "Approve on your Mac") {
		t.Fatalf("connecting with the Mac online:\n%s", page)
	}
	var req *localv1.PairingDetails
	eventually(t, 30*time.Second, "the request on the Mac", func() bool {
		l, err := mac.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		req = l.Pairings[0]
		return true
	})
	if _, err := mac.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: req.Code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the page seeing the approval", func() bool { return app.consentState() == "active" })
	session := app.mcpSession(app.finish(app.get(app.consentURL)).Access)

	timed := func(name string, args map[string]any) time.Duration {
		t.Helper()
		began := time.Now()
		if d := callTool(t, session, name, args); !d.OK {
			t.Fatalf("%s: %s", name, d.raw)
		}
		return time.Since(began)
	}
	best := func(name string, args map[string]any) time.Duration {
		t.Helper()
		b := time.Duration(1 << 62)
		for range 3 {
			b = min(b, timed(name, args))
		}
		return b
	}
	// A call over a kept tunnel waits for its own round trip and the
	// machine's work, well under half another; one that makes a tunnel
	// waits for the handshake's round trip too.
	check := func(what string, d time.Duration, newTunnel bool) {
		t.Helper()
		rounds := float64(d) / float64(2*oneWay)
		t.Logf("%-55s %4.1f round trips (%v)", what, rounds, d.Round(time.Millisecond))
		if newTunnel && rounds < 1.5 {
			t.Errorf("%s: %.1f round trips: no handshake with the machine?", what, rounds)
		}
		if !newTunnel && rounds > 1.5 {
			t.Errorf("%s: %.1f round trips, want 1: the tunnel was not kept", what, rounds)
		}
	}
	check("first call (project_list)", timed("project_list", nil), true)
	check("project_list again", best("project_list", nil), false)
	doc := callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "say hi"})
	var started struct {
		Session struct {
			SessionID string `json:"session_id"`
		} `json:"session"`
	}
	if !doc.OK || json.Unmarshal(doc.raw, &started) != nil || started.Session.SessionID == "" {
		t.Fatalf("session_start: %s", doc.raw)
	}
	get := map[string]any{"session_id": started.Session.SessionID}
	check("session_get", best("session_get", get), false)

	// The owner renames the connection: its grant is signed again, and the
	// old version stops working (the relay drops what it admitted with it).
	clients, err := mac.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	if _, err := mac.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: clients.Clients[0].GrantId, Label: "Far app, renamed"}); err != nil {
		t.Fatal(err)
	}
	check("session_get after the grant changed", timed("session_get", get), true)
	check("session_get again", best("session_get", get), false)
	if st := statusOf(t, session); st.State != "active" {
		t.Fatalf("the connection after the grant changed: %+v", st)
	}
	if doc := callTool(t, session, "auth_status", nil); !doc.OK || !strings.Contains(string(doc.raw), "Far app, renamed") {
		t.Fatalf("auth_status after the grant changed: %s", doc.raw)
	}
}
