package owner

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type stubMachineInfo struct {
	machinev1connect.UnimplementedMachineServiceHandler
	block *atomic.Bool
}

func (s stubMachineInfo) GetInfo(ctx context.Context, _ *machinev1.GetInfoRequest) (*machinev1.GetInfoResponse, error) {
	if s.block != nil && s.block.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &machinev1.GetInfoResponse{Status: &machinev1.MachineStatus{Name: "test-mac"}}, nil
}

type stubHerService struct {
	herv1connect.UnimplementedHerServiceHandler
}

func (stubHerService) Call(_ context.Context, _ *herv1.CallRequest) (*herv1.CallResponse, error) {
	return &herv1.CallResponse{Response: &herv1.Response{Result: []byte(`{"ok":true}`)}}, nil
}

type testIdentities struct {
	ownerID      string
	machineID    string
	rootPub      []byte
	chain        *identityv1.RootChain
	device       *identity.SoftwareKey
	deviceCert   *identityv1.Signed
	machineKey   *identity.SoftwareKey
	machineCreds func() *identityv1.Credentials
	deviceCreds  func() *identityv1.Credentials
}

func makeTestIdentities(t *testing.T) testIdentities {
	t.Helper()
	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	device, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	machKey, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	machineID := identity.MachineID(identity.MarshalPublicKey(machKey.Public()))
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}
	dc, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(device.Public()),
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        machineID,
		MachinePublicKey: identity.MarshalPublicKey(machKey.Public()),
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	return testIdentities{
		ownerID:    ownerID,
		machineID:  machineID,
		rootPub:    rootPub,
		chain:      chain,
		device:     device,
		deviceCert: dc,
		machineKey: machKey,
		deviceCreds: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: dc}}
		},
		machineCreds: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_MachineCert{MachineCert: mc}}
		},
	}
}

func TestAdaptiveProbeTimeoutBounds(t *testing.T) {
	cases := []struct {
		srtt time.Duration
		want time.Duration
	}{
		{0, time.Second},
		{150 * time.Millisecond, time.Second},
		{300 * time.Millisecond, time.Second},
		{500 * time.Millisecond, 1500 * time.Millisecond},
		{2 * time.Second, 4 * time.Second},
	}
	for _, tc := range cases {
		if got := adaptiveProbeTimeout(tc.srtt); got != tc.want {
			t.Errorf("adaptiveProbeTimeout(%v) = %v, want %v", tc.srtt, got, tc.want)
		}
	}
}

func TestCellularOnlyFiltersRFC1918DirectAddrs(t *testing.T) {
	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	all := []string{"192.168.1.2:9000", "10.0.0.5:9000", "172.16.4.2:9000", "[2408::1]:9000", "203.0.113.5:9000"}
	o.saveDirectAddrs("m_1", all)

	if got := o.directAddrs("m_1"); !slices.Equal(got, all) {
		t.Fatalf("wifi directAddrs = %v, want %v", got, all)
	}
	dev.SetCellularOnly(true)
	wantCell := []string{"[2408::1]:9000", "203.0.113.5:9000"}
	if got := o.directAddrs("m_1"); !slices.Equal(got, wantCell) {
		t.Fatalf("cellular directAddrs = %v, want %v", got, wantCell)
	}
	dev.SetCellularOnly(false)
	if got := o.directAddrs("m_1"); !slices.Equal(got, all) {
		t.Fatalf("restored directAddrs = %v, want %v", got, all)
	}
}

func TestOwnerStatusNotConnectedWhileKeptUnverified(t *testing.T) {
	ids := makeTestIdentities(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if err := transport.ReadFrame(conn, &hello); err != nil {
			return
		}
		_ = transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		sess, err := yamux.Server(conn, transport.YamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		<-sess.CloseChan()
	}))
	defer srv.Close()

	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	if err := o.saveIdentity(ids.ownerID, srv.URL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Admitted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	st := o.Status()
	if !st.RelayConnected {
		t.Fatal("expected RelayConnected=true after verified relay connect")
	}
	if st.GetRelayHealth().GetPhase() != "connected" || st.GetRelayHealth().GetRttMs() == 0 {
		t.Fatalf("unexpected RelayHealth while connected: %+v", st.GetRelayHealth())
	}

	// Simulate Pause(): keepRelay keeps the socket unverified until resume.
	o.keepRelay()
	st = o.Status()
	if st.RelayConnected {
		t.Fatal("expected RelayConnected=false while connection is kept unverified across pause")
	}
	if st.GetRelayHealth().GetPhase() == "connected" {
		t.Fatalf("expected non-connected RelayHealth phase while kept, got %+v", st.GetRelayHealth())
	}
}

func TestConnectOrKeepReplacesBlackholedChildTunnel(t *testing.T) {
	ids := makeTestIdentities(t)
	var blockGetInfo atomic.Bool
	var delaySecondDial atomic.Bool
	var dials atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		num := dials.Add(1)
		if num > 1 && delaySecondDial.Load() {
			time.Sleep(150 * time.Millisecond)
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if err := transport.ReadFrame(conn, &hello); err != nil {
			return
		}
		_ = transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		sess, err := yamux.Server(conn, transport.YamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer st.Close()
				var h relayv1.StreamHeader
				if err := transport.ReadFrame(st, &h); err != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				tc, _, err := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, identity.NewTrust(ids.ownerID), func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				mux := http.NewServeMux()
				mux.Handle(machinev1connect.NewMachineServiceHandler(stubMachineInfo{block: &blockGetInfo}))
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	defer srv.Close()

	d := &peer.Dialer{
		RelayURL:    srv.URL,
		Role:        relayv1.Role_ROLE_OWNER,
		Key:         ids.device,
		Credentials: ids.deviceCreds,
		Trust:       identity.NewTrust(ids.ownerID),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Establish initial relay + cached relayed machine tunnel.
	kept, err := d.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kept.Machine(ctx, ids.machineID); err != nil {
		t.Fatal(err)
	}

	// 2. Healthy case: kept Ping + GetInfo succeed quickly while redial is delayed -> kept wins.
	delaySecondDial.Store(true)
	gotHealthy, err := connectOrKeep(ctx, d, kept, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if gotHealthy != kept {
		gotHealthy.Close()
		t.Fatal("expected connectOrKeep to reuse healthy kept connection")
	}

	// 3. Blackholed child tunnel case: outer yamux still answers Ping, but GetInfo hangs -> redial wins.
	delaySecondDial.Store(false)
	blockGetInfo.Store(true)
	gotRecovered, err := connectOrKeep(ctx, d, kept, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer gotRecovered.Close()
	if gotRecovered == kept {
		t.Fatal("expected connectOrKeep to replace kept connection whose child tunnel was blackholed")
	}
}

func TestHerOnRecoverTunnelAndBoundedCooldown(t *testing.T) {
	ids := makeTestIdentities(t)
	var dials atomic.Int32
	var blackholeAll atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		num := dials.Add(1)
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if err := transport.ReadFrame(conn, &hello); err != nil {
			return
		}
		_ = transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		sess, err := yamux.Server(conn, transport.YamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer st.Close()
				var h relayv1.StreamHeader
				if err := transport.ReadFrame(st, &h); err != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				if num == 1 || blackholeAll.Load() {
					io.Copy(io.Discard, st)
					return
				}
				tc, _, err := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, identity.NewTrust(ids.ownerID), func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				mux := http.NewServeMux()
				mux.Handle(herv1connect.NewHerServiceHandler(stubHerService{}))
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	defer srv.Close()

	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	if err := o.saveIdentity(ids.ownerID, srv.URL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}

	// First connect establishes broken outer carrier #1.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := o.relay(ctx); err != nil {
		t.Fatal(err)
	}

	// Short deadline triggers TLS handshake timeout on carrier #1; herOn must
	// recover onto carrier #2 transparently in a single call? Wait: use a
	// fresh context inside herOn's test by giving enough budget for the retry.
	// Note: ClientHandshake uses min(15s, ctx.Deadline), so to test fast timeout
	// on carrier #1 we check recoverTunnel directly and via a tight deadline first.
	tctx, tcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = o.HerCall(tctx, ids.machineID, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
	tcancel()
	if err == nil {
		// Recovered even within 200ms!
	} else {
		// Carrier #1 was replaced by RecoverTunnel; next call immediately uses carrier #2.
		res, err2 := o.HerCall(ctx, ids.machineID, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
		if err2 != nil {
			t.Fatalf("HerCall after recovery failed: %v (first err: %v)", err2, err)
		}
		if string(res.GetResponse().GetResult()) != `{"ok":true}` {
			t.Fatalf("unexpected HerCall result: %s", res.GetResponse().GetResult())
		}
	}
	if dials.Load() < 2 {
		t.Fatalf("expected outer relay replacement (dials >= 2), got %d", dials.Load())
	}

	// Verify bounded cooldown: when a machine genuinely stops answering on ALL
	// carriers, recoverTunnel drops at most once within recoverCooldown.
	blackholeAll.Store(true)
	o.dropRelay()
	cA, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a1, cancelA1 := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err1 := cA.Machine(a1, ids.machineID)
	cancelA1()
	if !o.recoverTunnel(cA, ids.machineID, err1) {
		t.Fatalf("expected first recoverTunnel to succeed, err1=%v", err1)
	}
	cB, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a2, cancelA2 := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err2 := cB.Machine(a2, ids.machineID)
	cancelA2()
	if o.recoverTunnel(cB, ids.machineID, err2) {
		t.Fatal("expected second recoverTunnel within cooldown to be suppressed")
	}
}

func TestKeepRelayForgetsDirectTunnels(t *testing.T) {
	ids := makeTestIdentities(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if err := transport.ReadFrame(conn, &hello); err != nil {
			return
		}
		_ = transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		sess, err := yamux.Server(conn, transport.YamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer st.Close()
				var h relayv1.StreamHeader
				if err := transport.ReadFrame(st, &h); err != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				tc, _, err := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, identity.NewTrust(ids.ownerID), func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				mux := http.NewServeMux()
				mux.Handle(machinev1connect.NewMachineServiceHandler(stubMachineInfo{}))
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	defer srv.Close()

	trust := identity.NewTrust(ids.ownerID)
	if _, err := trust.AcceptRootChain(ids.chain); err != nil {
		t.Fatal(err)
	}
	dialer := &peer.Dialer{
		RelayURL:    srv.URL,
		Role:        relayv1.Role_ROLE_OWNER,
		Key:         ids.device,
		Trust:       trust,
		Credentials: ids.deviceCreds,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	relayed, err := c.RelayMachine(ctx, ids.machineID)
	if err != nil {
		t.Fatalf("RelayMachine: %v", err)
	}
	otherID := "m_direct_only_test"
	directPeer, err := dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer directPeer.Close()
	directMach, err := directPeer.RelayMachine(ctx, ids.machineID)
	if err != nil {
		t.Fatalf("second RelayMachine: %v", err)
	}
	if !c.AdoptDirect(otherID, directMach) {
		t.Fatal("AdoptDirect failed")
	}

	d, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	o := d.owner()
	o.mu.Lock()
	o.client = c
	o.mu.Unlock()

	o.keepRelay()

	if _, _, ok := c.MachineTunnel(otherID); ok {
		t.Fatal("keepRelay did not close cached direct tunnel on client")
	}
	if directMach.Alive() {
		t.Fatal("direct tunnel remained alive after keepRelay")
	}
	if path, _, ok := c.MachineTunnel(ids.machineID); !ok || path != "relay" || !relayed.Alive() {
		t.Fatalf("keepRelay unexpectedly dropped relayed tunnel: ok=%v path=%q alive=%v", ok, path, relayed.Alive())
	}
}
