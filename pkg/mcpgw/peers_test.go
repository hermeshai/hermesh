package mcpgw

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// A connection keeps its relay connection from one call to the next while
// it is used, and closes it when unused for peerIdle or when it ends. A
// grant the owner re-issues meanwhile is picked up, and the connection goes
// on. (Its tunnels to machines: e2e TestMCPRoundTrips.)
func TestConnectionsKeepTheirPeers(t *testing.T) {
	// A relay claimed by an owner, whose root key signs the grants here.
	root, _ := identity.GenerateKey()
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rootCert, err := identity.Sign(&identityv1.RootCert{OwnerId: ownerID, Version: 1, IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub, Label: "test-mac", Platform: "macos"}}}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rootCert}}
	dir := t.TempDir()
	st, err := relay.OpenStore(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	device, _ := identity.Sign(&identityv1.OwnerDeviceCert{OwnerId: ownerID, DevicePublicKey: rootPub, Label: "test-mac"}, root)
	if err := st.SaveClaim(ownerID, chain, device); err != nil {
		t.Fatal(err)
	}
	st.Close()
	r, err := relay.New(relay.Config{DataDir: dir, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	var dials atomic.Int32 // relay connections the gateway opened
	f := newFixture(t, func(c *Config) {
		c.Relay = r
		h := r.Handler()
		c.RelayHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/v1/connect" {
				dials.Add(1)
			}
			h.ServeHTTP(w, req)
		})
	})

	// A connection, approved by the owner.
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	code := f.browser().consentCode(client, redirect)
	_, tok := f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}})
	ctx := context.Background()
	list, err := f.g.Connections(ctx, "a1")
	if err != nil || len(list) != 1 {
		t.Fatalf("connections: %+v %v", list, err)
	}
	conn, err := f.g.db.GetConnection(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.g.key(conn)
	if err != nil {
		t.Fatal(err)
	}
	grant := func(version uint64) (*identityv1.Grant, *identityv1.Signed) {
		pub := identity.MarshalPublicKey(key.Public())
		g := &identityv1.Grant{OwnerId: ownerID, GrantId: "g_test", Version: version, ClientId: identity.ClientID(pub), ClientPublicKey: pub,
			ClientLabel: "Test (127.0.0.1)", MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"*"},
			ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}
		s, err := identity.Sign(g, root)
		if err != nil {
			t.Fatal(err)
		}
		return g, s
	}
	_, g1 := grant(1)
	conn.State, conn.OwnerID, conn.GrantID, conn.ApprovedAt = StateActive, ownerID, "g_test", f.clock.now()
	conn.Grant, _ = proto.Marshal(g1)
	conn.RootChain, _ = proto.Marshal(chain)
	if err := f.g.db.PutConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}

	mc := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s, err := mc.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: f.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{tok["access_token"].(string)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	call := func() {
		t.Helper()
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "project_list"})
		if err != nil || res.IsError {
			t.Fatalf("project_list: %+v %v", res, err)
		}
	}
	online := func() bool {
		entries, _ := r.ClientEntries(ownerID)
		return len(entries) == 1 && entries[0].Online
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	// Used: one relay connection for every call, open between them.
	for range 3 {
		call()
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d relay connections for 3 calls, want 1", n)
	}
	if !online() {
		t.Fatal("the connection's client went offline between calls")
	}
	f.clock.add(peerIdle - time.Second)
	f.g.closeIdlePeers(false)
	call()
	if n := dials.Load(); n != 1 || !online() {
		t.Fatalf("closed before it was idle for %v: %d relay connections", peerIdle, n)
	}

	// Unused for peerIdle: closed; the next call connects again.
	f.clock.add(peerIdle)
	f.g.closeIdlePeers(false)
	until("the idle connection to close", func() bool { return !online() })
	call()
	if n := dials.Load(); n != 2 || !online() {
		t.Fatalf("after the idle close: %d relay connections, online %v", n, online())
	}

	// The owner re-issues the grant: the relay drops the connection admitted
	// with the old one. The next call connects again, picks up the new
	// grant, and the connection goes on with it.
	g2, s2 := grant(2)
	if err := r.Backend().PutGrant(ownerID, g2, s2); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyRevocations(ownerID, &identityv1.RevocationList{MinGrantVersions: map[string]uint64{"g_test": 2}}); err != nil {
		t.Fatal(err)
	}
	until("the relay to drop the old grant's connection", func() bool { return !online() })
	call()
	call()
	if n := dials.Load(); n != 3 || !online() {
		t.Fatalf("after the grant changed: %d relay connections, online %v", n, online())
	}
	stored := &identityv1.Signed{}
	if conn, _ = f.g.db.GetConnection(ctx, conn.ID); conn.State != StateActive || proto.Unmarshal(conn.Grant, stored) != nil || !proto.Equal(stored, s2) {
		t.Fatalf("the connection after the grant changed: %s, its grant the new one: %v", conn.State, proto.Equal(stored, s2))
	}

	// The connection ends: its peer goes.
	if err := f.g.endConnection(ctx, conn.ID, StateRevoked, "test"); err != nil {
		t.Fatal(err)
	}
	f.g.mu.Lock()
	_, kept := f.g.peers[conn.ID]
	f.g.mu.Unlock()
	if kept {
		t.Fatal("an ended connection keeps its peer")
	}
	until("the ended connection's client to go", func() bool { return !online() })
}
