package peer

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type directFixture struct {
	dialer    *Dialer
	machineID string
}

func setupDirectFixture(t *testing.T, relayTunnelDelay time.Duration, directAddrs []string) directFixture {
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
	ownerID := identity.OwnerID(identity.MarshalPublicKey(root.Public()))
	machineID := identity.MachineID(identity.MarshalPublicKey(machKey.Public()))
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: identity.MarshalPublicKey(root.Public())}},
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
	machTrust := identity.NewTrust(ownerID)
	if _, err := machTrust.AcceptRootChain(chain); err != nil {
		t.Fatal(err)
	}
	devTrust := identity.NewTrust(ownerID)
	if _, err := devTrust.AcceptRootChain(chain); err != nil {
		t.Fatal(err)
	}
	machineCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: chain, MachineName: "box", Subject: &identityv1.Credentials_MachineCert{MachineCert: mc}}
	}
	deviceCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: dc}}
	}

	mux := http.NewServeMux()
	mux.Handle(herv1connect.NewHerServiceHandler(herCalls{}))
	serveMachine := func(raw net.Conn) {
		defer raw.Close()
		tc, _, err := transport.ServerHandshake(t.Context(), raw, machKey.CryptoSigner(), machineCreds, machTrust, func(*identity.Subject) error { return nil })
		if err != nil {
			return
		}
		defer tc.Close()
		transport.ServeH2(t.Context(), tc, mux)
	}

	if len(directAddrs) == 0 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		directAddrs = []string{ln.Addr().String()}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go serveMachine(c)
			}
		}()
	}

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
				var h relayv1.StreamHeader
				if err := transport.ReadFrame(st, &h); err != nil {
					st.Close()
					return
				}
				if h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					st.Close()
					return
				}
				if relayTunnelDelay > 0 {
					select {
					case <-time.After(relayTunnelDelay):
					case <-t.Context().Done():
						st.Close()
						return
					}
				}
				serveMachine(st)
			}()
		}
	}))
	t.Cleanup(srv.Close)

	return directFixture{
		machineID: machineID,
		dialer: &Dialer{
			RelayURL:    srv.URL,
			Role:        relayv1.Role_ROLE_OWNER,
			Key:         device,
			Trust:       devTrust,
			Credentials: deviceCreds,
			Direct:      func(id string) []string { return directAddrs },
		},
	}
}

func closedLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestMachineDirectWinsOverSlowRelay(t *testing.T) {
	fx := setupDirectFixture(t, 1500*time.Millisecond, nil)
	var wonAddr string
	fx.dialer.OnDirectSuccess = func(id, addr string) {
		if id == fx.machineID {
			wonAddr = addr
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := fx.dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	start := time.Now()
	n, err := c.Machine(ctx, fx.machineID)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("Machine took %v, expected direct path far faster than 1.5s relay delay", took)
	}
	if wonAddr == "" {
		t.Fatal("expected OnDirectSuccess callback with winning address")
	}
	res, err := n.Her.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "ping"}})
	if err != nil || string(res.Response.Result) != `"ping"` {
		t.Fatalf("Her.Call over direct tunnel = (%v, %v)", res, err)
	}
}

func TestDirectHeadFor(t *testing.T) {
	if got := directHeadFor([]string{"192.168.1.10:58348"}); got != directHeadLAN {
		t.Fatalf("LAN head = %v, want %v", got, directHeadLAN)
	}
	if got := directHeadFor([]string{"192.168.1.10:58348", "[2408:8207::1]:58348"}); got != directHeadWAN {
		t.Fatalf("IPv6 head = %v, want %v", got, directHeadWAN)
	}
}

func TestMachineDirectFallsBackToRelay(t *testing.T) {
	fx := setupDirectFixture(t, 0, []string{closedLocalAddr(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := fx.dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	n, err := c.Machine(ctx, fx.machineID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := n.Her.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "ping"}})
	if err != nil || string(res.Response.Result) != `"ping"` {
		t.Fatalf("Her.Call over fallback relay tunnel = (%v, %v)", res, err)
	}
}

func TestClientForgetDirectDropsDirectOnly(t *testing.T) {
	fx := setupDirectFixture(t, 1500*time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := fx.dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	directMachine, err := c.Machine(ctx, fx.machineID)
	if err != nil {
		t.Fatal(err)
	}
	if !directMachine.direct {
		t.Fatal("expected cached machine to be marked direct")
	}

	fxRelay := setupDirectFixture(t, 0, []string{closedLocalAddr(t)})
	rc, err := fxRelay.dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	relayMachine, err := rc.Machine(ctx, fxRelay.machineID)
	if err != nil {
		t.Fatal(err)
	}
	if relayMachine.direct {
		t.Fatal("expected fallback relay machine not to be marked direct")
	}

	c.mu.Lock()
	c.machines["relay-peer"] = relayMachine
	c.mu.Unlock()

	c.ForgetDirect()

	c.mu.Lock()
	_, hasDirect := c.machines[fx.machineID]
	_, hasRelay := c.machines["relay-peer"]
	c.mu.Unlock()
	if hasDirect {
		t.Fatal("ForgetDirect should remove cached direct tunnel")
	}
	if !hasRelay {
		t.Fatal("ForgetDirect should keep cached relay tunnel intact")
	}
}

func TestClientRelayMachineSkipsDirect(t *testing.T) {
	fx := setupDirectFixture(t, 0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := fx.dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	n, err := c.RelayMachine(ctx, fx.machineID)
	if err != nil {
		t.Fatal(err)
	}
	if n.direct {
		t.Fatal("RelayMachine should connect via relay without using direct path")
	}
	res, err := n.Her.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "ping"}})
	if err != nil || string(res.Response.Result) != `"ping"` {
		t.Fatalf("Her.Call over RelayMachine = (%v, %v)", res, err)
	}
}

func TestAdoptDirect(t *testing.T) {
	fx := setupDirectFixture(t, 0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := fx.dialer.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	pre, err := fx.dialer.DialDirect(ctx, fx.machineID, fx.dialer.Direct(fx.machineID))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AdoptDirect(fx.machineID, pre) {
		t.Fatal("AdoptDirect rejected first direct tunnel")
	}
	if !pre.direct {
		t.Fatal("adopted tunnel not marked direct")
	}
	got, err := c.Machine(ctx, fx.machineID)
	if err != nil {
		t.Fatal(err)
	}
	if got != pre {
		t.Fatalf("c.Machine returned %p, want adopted %p", got, pre)
	}

	dup, err := fx.dialer.DialDirect(ctx, fx.machineID, fx.dialer.Direct(fx.machineID))
	if err != nil {
		t.Fatal(err)
	}
	if c.AdoptDirect(fx.machineID, dup) {
		t.Fatal("AdoptDirect should reject duplicate when live tunnel exists")
	}
	if _, err := dup.Her.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "ping"}}); err == nil {
		t.Fatal("rejected duplicate tunnel should be closed")
	}
}
