package e2e

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/owner"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/fake"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// world is an in-process deployment: relay, owner (driven like the app),
// two machines and one paired agent. The tests then act as a compromised
// relay (A7, §3.8 of the design).
type world struct {
	relay     *relay.Server
	relayURL  string
	app       *testApp
	ownerID   string
	trust     *identity.Trust
	machines  []*machine.Machine
	agentKey  *identity.SoftwareKey
	grant     *identityv1.Signed
	rootChain *identityv1.RootChain
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmp := t.TempDir()
	rs, err := relay.New(relay.Config{DataDir: filepath.Join(tmp, "relay"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(rs.Handler())
	t.Cleanup(func() { hs.Close(); rs.Close() })
	claim, _ := rs.ClaimCode()

	o, err := owner.Open(owner.Config{DataDir: filepath.Join(tmp, "owner"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); o.Close() })
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	go o.Run(ctx)
	go o.ServeLocal(ctx, sock)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: hs.URL, ClaimCode: claim, RootPublicKey: rootPub(root), RootLabel: "mac"})
	if err != nil {
		t.Fatal(err)
	}
	w := &world{relay: rs, relayURL: hs.URL, app: app, ownerID: setup.OwnerId}
	addr := setup.ConnectAddress
	_, ownerID, _ := connaddr.Parse(addr)
	w.trust = identity.NewTrust(ownerID)

	for i, name := range []string{"alpha", "beta"} {
		n, err := machine.Open(machine.Config{DataDir: filepath.Join(tmp, name), Logger: quiet,
			Providers: map[string]provider.Provider{"claude": fake.Provider{}, "codex": fake.Provider{}}})
		if err != nil {
			t.Fatal(err)
		}
		// Closed once it has stopped running (it writes its audit log until then).
		var ran chan struct{}
		t.Cleanup(func() {
			cancel()
			if ran != nil {
				<-ran
			}
			n.Close()
		})
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- n.Pair(ctx, addr, name, &syncWriter{w: &out}, machine.PairOptions{}) }()
		eventually(t, 10*time.Second, "pairing code", func() bool { return codeRE.MatchString(syncRead(&out)) })
		code := codeRE.FindStringSubmatch(syncRead(&out))[1]
		if _, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: name}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		ran = make(chan struct{})
		go func() {
			n.Run(ctx)
			close(ran)
		}()
		w.machines = append(w.machines, n)
		_ = i
	}
	eventually(t, 10*time.Second, "machines online", func() bool {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		on := 0
		for _, n := range res.Machines {
			if n.Online {
				on++
			}
		}
		return on == 2
	})
	repo := gitRepo(t, map[string]string{"README.md": "x\n"})
	for _, n := range w.machines {
		if _, err := app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: n.MachineID(), ProjectId: "demo", Path: repo}); err != nil {
			t.Fatal(err)
		}
	}

	// Pair an agent.
	w.agentKey, _ = identity.GenerateKey()
	pr := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_CLIENT, SubjectPublicKey: identity.MarshalPublicKey(w.agentKey.Public()),
		Label: "agent", OwnerId: ownerID, CreatedAt: timestamppb.Now()}
	var out bytes.Buffer
	done := make(chan *identityv1.Credentials, 1)
	go func() {
		c, _, err := machine.PairWithRelay(ctx, http.DefaultClient, hs.URL, pr, w.agentKey, &syncWriter{w: &out}, machine.PairOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- c
	}()
	eventually(t, 10*time.Second, "agent code", func() bool { return codeRE.MatchString(syncRead(&out)) })
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: codeRE.FindStringSubmatch(syncRead(&out))[1], Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	creds := <-done
	if _, err := w.trust.AcceptRootChain(creds.RootChain); err != nil {
		t.Fatal(err)
	}
	w.grant, w.rootChain = creds.GetGrant(), creds.RootChain
	return w
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

var bufMu sync.Mutex

func (s *syncWriter) Write(b []byte) (int, error) {
	bufMu.Lock()
	defer bufMu.Unlock()
	return s.w.Write(b)
}

func syncRead(b *bytes.Buffer) string {
	bufMu.Lock()
	defer bufMu.Unlock()
	return b.String()
}

func (w *world) agentCreds(grant *identityv1.Signed) func() *identityv1.Credentials {
	return func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: w.rootChain, Subject: &identityv1.Credentials_Grant{Grant: grant}}
	}
}

// evilTunnel acts as the relay: it opens a raw tunnel to a machine and runs
// the client handshake with the given key and credentials.
func (w *world) evilTunnel(t *testing.T, machineID string, key *identity.SoftwareKey, creds func() *identityv1.Credentials, expectMachine string) (*transport.H2Client, error) {
	raw, err := w.relay.RawTunnel(machineID, &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: machineID,
		PrincipalRole: relayv1.Role_ROLE_CLIENT, SubjectId: "c_forged"})
	if err != nil {
		t.Fatal(err)
	}
	tc, _, err := transport.ClientHandshake(context.Background(), raw, key.CryptoSigner(), creds, w.trust, expectMachine)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return transport.NewH2Client(tc)
}

func startSession(ctx context.Context, h *transport.H2Client, prompt string) error {
	_, err := sessionv1connect.NewSessionServiceClient(h, "https://machine").StartSession(ctx,
		&sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "claude", Prompt: prompt})
	return err
}

func TestEvilRelay(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newWorld(t)
	ctx := context.Background()
	alpha, beta := w.machines[0], w.machines[1]

	t.Run("legit agent works through a raw tunnel", func(t *testing.T) {
		h, err := w.evilTunnel(t, alpha.MachineID(), w.agentKey, w.agentCreds(w.grant), alpha.MachineID())
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		if err := startSession(ctx, h, "say ok"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("forged grant signed by the relay is rejected", func(t *testing.T) {
		evil, _ := identity.GenerateKey()
		g := &identityv1.Grant{OwnerId: w.ownerID, GrantId: "g_evil", Version: 1, ClientId: identity.ClientID(identity.MarshalPublicKey(evil.Public())),
			ClientPublicKey: identity.MarshalPublicKey(evil.Public()), MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"claude"},
			NotBefore: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}
		signed, _ := identity.Sign(g, evil)
		if h, err := w.evilTunnel(t, alpha.MachineID(), evil, w.agentCreds(signed), alpha.MachineID()); err == nil {
			defer h.Close()
			if startSession(ctx, h, "say pwned") == nil {
				t.Fatal("forged grant accepted")
			}
		}
	})

	t.Run("stolen grant without the agent key is rejected", func(t *testing.T) {
		evil, _ := identity.GenerateKey()
		if h, err := w.evilTunnel(t, alpha.MachineID(), evil, w.agentCreds(w.grant), alpha.MachineID()); err == nil {
			defer h.Close()
			if startSession(ctx, h, "say pwned") == nil {
				t.Fatal("grant presented with a foreign key accepted")
			}
		}
	})

	t.Run("rerouting to another machine is detected by the agent", func(t *testing.T) {
		raw, err := w.relay.RawTunnel(beta.MachineID(), &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: beta.MachineID()})
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if _, _, err := transport.ClientHandshake(ctx, raw, w.agentKey.CryptoSigner(), w.agentCreds(w.grant), w.trust, alpha.MachineID()); err == nil {
			t.Fatal("agent accepted the wrong machine")
		}
	})

	t.Run("a machine impersonated by the relay is detected", func(t *testing.T) {
		evil, _ := identity.GenerateKey()
		nc := &identityv1.MachineCert{OwnerId: w.ownerID, MachineId: alpha.MachineID(), MachinePublicKey: identity.MarshalPublicKey(evil.Public())}
		fakeCert, _ := identity.Sign(nc, evil)
		a, b := net.Pipe()
		go transport.ServerHandshake(ctx, b, evil.CryptoSigner(), func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: w.rootChain, Subject: &identityv1.Credentials_MachineCert{MachineCert: fakeCert}}
		}, w.trust, func(*identity.Subject) error { return nil })
		if _, _, err := transport.ClientHandshake(ctx, a, w.agentKey.CryptoSigner(), w.agentCreds(w.grant), w.trust, alpha.MachineID()); err == nil {
			t.Fatal("agent accepted an impersonated machine")
		}
	})

	t.Run("relay sees only ciphertext", func(t *testing.T) {
		raw, err := w.relay.RawTunnel(alpha.MachineID(), &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: alpha.MachineID()})
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{Conn: raw}
		tc, _, err := transport.ClientHandshake(ctx, rec, w.agentKey.CryptoSigner(), w.agentCreds(w.grant), w.trust, alpha.MachineID())
		if err != nil {
			t.Fatal(err)
		}
		h, _ := transport.NewH2Client(tc)
		defer h.Close()
		secret := "say the-secret-prompt-7f3a"
		if err := startSession(ctx, h, secret); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(rec.bytes(), []byte("the-secret-prompt-7f3a")) {
			t.Fatal("prompt visible on the wire")
		}
	})

	t.Run("replayed bytes do not start a session", func(t *testing.T) {
		raw, _ := w.relay.RawTunnel(alpha.MachineID(), &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: alpha.MachineID()})
		rec := &recorder{Conn: raw}
		tc, _, err := transport.ClientHandshake(ctx, rec, w.agentKey.CryptoSigner(), w.agentCreds(w.grant), w.trust, alpha.MachineID())
		if err != nil {
			t.Fatal(err)
		}
		h, _ := transport.NewH2Client(tc)
		if err := startSession(ctx, h, "say replay-me-91c2"); err != nil {
			t.Fatal(err)
		}
		h.Close()
		var before int
		eventually(t, 10*time.Second, "original session recorded", func() bool {
			before = countSessions(t, w, alpha, "replay-me-91c2")
			return before == 1
		})
		replay, _ := w.relay.RawTunnel(alpha.MachineID(), &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: alpha.MachineID()})
		replay.Write(rec.bytes())
		time.Sleep(time.Second)
		replay.Close()
		if after := countSessions(t, w, alpha, "replay-me-91c2"); after != before {
			t.Fatalf("replay created a session (%d → %d)", before, after)
		}
	})

	t.Run("tampered traffic is rejected", func(t *testing.T) {
		raw, _ := w.relay.RawTunnel(alpha.MachineID(), &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: alpha.MachineID()})
		flip := &flipper{Conn: raw, after: 4000}
		tc, _, err := transport.ClientHandshake(ctx, flip, w.agentKey.CryptoSigner(), w.agentCreds(w.grant), w.trust, alpha.MachineID())
		if err != nil {
			return // tampering broke the handshake: fine
		}
		h, _ := transport.NewH2Client(tc)
		defer h.Close()
		big := "say " + strings.Repeat("x", 8000)
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if startSession(cctx, h, big) == nil {
			t.Fatal("tampered request was accepted")
		}
	})

	t.Run("swapped pairing key is caught by the owner", func(t *testing.T) {
		victim, _ := identity.GenerateKey()
		evil, _ := identity.GenerateKey()
		code := identity.PairingCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, identity.MarshalPublicKey(victim.Public()))
		pr := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_CLIENT, SubjectPublicKey: identity.MarshalPublicKey(evil.Public()),
			Label: "victim", OwnerId: w.ownerID, CreatedAt: timestamppb.Now()}
		signed, _ := identity.Sign(pr, evil)
		w.relay.Store().PutPairing(&relay.Pairing{Code: code, Kind: pr.Kind, Request: signed, OwnerID: w.ownerID, ExpiresAt: time.Now().Add(5 * time.Minute)})
		if _, err := w.app.client.LookupPairing(ctx, &localv1.LookupPairingRequest{Code: code}); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("lookup err = %v", err)
		}
		if _, err := w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: code, Spec: &localv1.GrantSpec{}}); err == nil {
			t.Fatal("approved a swapped key")
		}
	})

	t.Run("a fake owner handed out at pairing is rejected", func(t *testing.T) {
		evil, _ := identity.GenerateKey()
		evilRoot := &identityv1.RootCert{OwnerId: identity.OwnerID(identity.MarshalPublicKey(evil.Public())), Version: 1,
			RootKeys: []*identityv1.RootKey{{PublicKey: identity.MarshalPublicKey(evil.Public())}}}
		s, _ := identity.Sign(evilRoot, evil)
		if _, err := identity.NewTrust(w.ownerID).AcceptRootChain(&identityv1.RootChain{Certs: []*identityv1.Signed{s}}); err == nil {
			t.Fatal("agent trusted a different owner")
		}
	})

	t.Run("forged admin command is rejected", func(t *testing.T) {
		evil, _ := identity.GenerateKey()
		cmd := &identityv1.AdminCommand{OwnerId: w.ownerID, CommandId: "x", TargetMachineId: alpha.MachineID(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
			Command: &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: "evil", Path: "/"}}}
		signed, _ := identity.Sign(cmd, evil)
		if _, err := alpha.ApplyAdminCommand(ctx, signed); err == nil {
			t.Fatal("forged admin command applied")
		}
	})

	t.Run("agents cannot call admin services", func(t *testing.T) {
		h, err := w.evilTunnel(t, alpha.MachineID(), w.agentKey, w.agentCreds(w.grant), alpha.MachineID())
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		_, err = machineAdmin(h).PushRevocations(ctx, &machinev1.PushRevocationsRequest{Revocations: &identityv1.RevocationList{RevokedGrantIds: []string{"g"}}})
		if err == nil {
			t.Fatal("agent pushed revocations")
		}
		// Settings go unsigned only from the owner's trusted devices.
		_, err = machineAdmin(h).ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Settings: &identityv1.AdminCommand{OwnerId: w.ownerID, CommandId: "y",
			TargetMachineId: alpha.MachineID(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
			Command: &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: "evil", Path: "/"}}}})
		if err == nil {
			t.Fatal("an agent changed the machine's projects")
		}
	})

	t.Run("a machine's name comes from the machine, end to end", func(t *testing.T) {
		// The relay can say anything about names in its list; the name an
		// agent holds a machine to is the one the machine presents in the
		// handshake, which the relay cannot change.
		for _, n := range []*machine.Machine{alpha, beta} {
			raw, err := w.relay.RawTunnel(n.MachineID(), &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: n.MachineID(),
				PrincipalRole: relayv1.Role_ROLE_CLIENT, SubjectId: "c_forged"})
			if err != nil {
				t.Fatal(err)
			}
			_, sub, err := transport.ClientHandshake(ctx, raw, w.agentKey.CryptoSigner(), w.agentCreds(w.grant), w.trust, n.MachineID())
			raw.Close()
			if err != nil {
				t.Fatal(err)
			}
			if sub.MachineName != n.Name() || n.Name() == "" {
				t.Fatalf("%s presented %q", n.Name(), sub.MachineName)
			}
		}
	})

	t.Run("relay cannot claim to be the owner without the root key", func(t *testing.T) {
		cl := relayv1connect.NewRelayPublicServiceClient(http.DefaultClient, w.relayURL)
		if _, err := cl.Claim(ctx, &relayv1.ClaimRequest{ClaimCode: "x"}); err == nil {
			t.Fatal("re-claim succeeded")
		}
	})
}

func machineAdmin(h *transport.H2Client) interface {
	PushRevocations(context.Context, *machinev1.PushRevocationsRequest) (*machinev1.PushRevocationsResponse, error)
	ApplyAdminCommand(context.Context, *machinev1.ApplyAdminCommandRequest) (*machinev1.ApplyAdminCommandResponse, error)
} {
	return machinev1connectAdmin(h)
}

func countSessions(t *testing.T, w *world, n *machine.Machine, marker string) int {
	res, err := w.app.client.ListSessions(context.Background(), &localv1.ListSessionsRequest{MachineId: n.MachineID()})
	if err != nil {
		t.Fatal(err)
	}
	c := 0
	for _, s := range res.Sessions {
		if strings.Contains(s.Progress.GetLastMessageExcerpt(), marker) {
			c++
		}
	}
	return c
}

type recorder struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	r.buf.Write(b)
	r.mu.Unlock()
	return r.Conn.Write(b)
}

func (r *recorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf.Bytes()...)
}

// flipper corrupts one byte after `after` bytes have been written.
type flipper struct {
	net.Conn
	n, after int
	done     bool
}

func (f *flipper) Write(b []byte) (int, error) {
	if !f.done && f.n+len(b) > f.after {
		c := append([]byte(nil), b...)
		c[f.after-f.n] ^= 0xff
		f.done = true
		f.n += len(b)
		return f.Conn.Write(c)
	}
	f.n += len(b)
	return f.Conn.Write(b)
}

var _ = peer.Dialer{}

func machinev1connectAdmin(h *transport.H2Client) machinev1connect.MachineAdminServiceClient {
	return machinev1connect.NewMachineAdminServiceClient(h, "https://machine")
}
