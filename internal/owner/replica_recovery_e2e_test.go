package owner

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/types/known/timestamppb"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// startLatencyProxy inserts a fixed one-way delay in both directions on a TCP
// proxy so tests can exercise realistic high-RTT cellular/relay paths (~340 ms).
func startLatencyProxy(t *testing.T, target string, oneWay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	delayCopy := func(dst, src net.Conn) {
		defer dst.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				time.Sleep(oneWay)
				if _, werr := dst.Write(chunk); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				in.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, in, out)
			mu.Unlock()
			go delayCopy(out, in)
			go delayCopy(in, out)
		}
	}()
	return "http://" + ln.Addr().String()
}

type recoveryWatchMachine struct{ stubMachineInfo }

func (s recoveryWatchMachine) Watch(ctx context.Context, _ *machinev1.WatchRequest, stream *connect.ServerStream[machinev1.WatchResponse]) error {
	if s.block != nil && s.block.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := stream.Send(&machinev1.WatchResponse{
		Reset_: true, ResumeToken: []byte("test-token"), SentAt: timestamppb.Now(),
		Status: &machinev1.MachineStatus{Name: "test-mac"},
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

type recoveryRelayControl struct {
	relayv1connect.UnimplementedOwnerControlServiceHandler
	machines *relayv1.ListMachinesResponse
}

func (s recoveryRelayControl) ListMachines(context.Context, *relayv1.ListMachinesRequest) (*relayv1.ListMachinesResponse, error) {
	return s.machines, nil
}

func (recoveryRelayControl) ListClients(context.Context, *relayv1.ListClientsRequest) (*relayv1.ListClientsResponse, error) {
	return &relayv1.ListClientsResponse{}, nil
}

func (recoveryRelayControl) ListPairingRequests(context.Context, *relayv1.ListPairingRequestsRequest) (*relayv1.ListPairingRequestsResponse, error) {
	return &relayv1.ListPairingRequestsResponse{}, nil
}

func (s recoveryRelayControl) WatchOwner(ctx context.Context, _ *relayv1.WatchOwnerRequest, stream *connect.ServerStream[relayv1.OwnerUpdate]) error {
	if err := stream.Send(&relayv1.OwnerUpdate{
		Machines: s.machines, Clients: &relayv1.ListClientsResponse{}, Pairings: &relayv1.ListPairingRequestsResponse{},
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// startRecoveryRelay serves a loopback relay with a controllable machine tunnel.
// The client still uses yamux, signed test identities, TLS, and Connect RPCs.
func startRecoveryRelay(t *testing.T, ids testIdentities, tunnel func(carrier int32, stream net.Conn) machinev1connect.MachineServiceHandler) string {
	t.Helper()
	var carriers atomic.Int32
	creds := ids.machineCreds()
	control := http.NewServeMux()
	control.Handle(relayv1connect.NewOwnerControlServiceHandler(recoveryRelayControl{
		machines: &relayv1.ListMachinesResponse{Machines: []*relayv1.MachineEntry{{
			MachineCert: creds.GetMachineCert(), Name: "test-mac", Online: true, LastSeen: timestamppb.Now(),
		}}},
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		carrier := carriers.Add(1)
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		if err := transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"}); err != nil {
			return
		}
		var hello relayv1.Hello
		if err := transport.ReadFrame(conn, &hello); err != nil {
			return
		}
		if err := transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true}); err != nil {
			return
		}
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
				if transport.ReadFrame(st, &h) != nil {
					return
				}
				switch h.Kind {
				case relayv1.StreamKind_STREAM_KIND_CONTROL:
					transport.ServeH2(r.Context(), st, control)
				case relayv1.StreamKind_STREAM_KIND_TUNNEL:
					handler := tunnel(carrier, st)
					if handler == nil {
						return
					}
					tc, _, err := transport.ServerHandshake(r.Context(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, identity.NewTrust(ids.ownerID), func(*identity.Subject) error { return nil })
					if err != nil {
						return
					}
					defer tc.Close()
					mux := http.NewServeMux()
					mux.Handle(machinev1connect.NewMachineServiceHandler(handler))
					transport.ServeH2(r.Context(), tc, mux)
				}
			}()
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func openRecoveryDevice(t *testing.T, ids testIdentities, relayURL string) *Device {
	t.Helper()
	dev, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)
	if err := dev.owner().saveIdentity(ids.ownerID, relayURL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}
	return dev
}

// TestFollowRemoteRecoversBlackholedRelayCarrier verifies that when the outer
// relay answers Ping cleanly (relay connected) while carrier #1 blackholes E2E
// tunnel handshakes, replica.followRemote classifies the timeout as
// "did not answer in time" AND triggers recoverTunnel to drop carrier #1 so
// the next follow attempt succeeds on carrier #2 (both direct and joining Warm).
func TestFollowRemoteRecoversBlackholedRelayCarrier(t *testing.T) {
	for _, withWarm := range []bool{false, true} {
		name := "direct_dial"
		if withWarm {
			name = "joining_warm"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ids := makeTestIdentities(t)
			relayURL := startRecoveryRelay(t, ids, func(carrier int32, stream net.Conn) machinev1connect.MachineServiceHandler {
				if carrier == 1 {
					_, _ = io.Copy(io.Discard, stream)
					return nil
				}
				return recoveryWatchMachine{}
			})
			o := openRecoveryDevice(t, ids, relayURL).owner()
			// Simulate cached IPv6 direct addr (triggers directHeadWAN = 280ms in RaceDirect).
			o.saveDirectAddrs(ids.machineID, []string{"quic://[::1]:1"})

			if withWarm {
				o.mu.Lock()
				o.seen = ids.machineID
				o.mu.Unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*followTimeout)
			defer cancel()
			c1, err := o.relay(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := c1.Ping(ctx); err != nil {
				t.Fatalf("carrier #1 should answer Ping: %v", err)
			}
			firstCarrier := c1.Done()

			s := &sources{ctx: ctx, feeds: map[string]*feed{}}
			f := &feed{name: "test-mac", online: true}
			err = o.rep.followRemote(s, ids.machineID, f, func() { t.Error("unexpectedly ready on blackholed carrier #1") })
			if err == nil || err.Error() != "did not answer in time" {
				t.Fatalf("followRemote on carrier #1 = %v, want did not answer in time", err)
			}

			// Carrier #1 must have been dropped by recoverTunnel so the next
			// followRemote immediately dials carrier #2 and becomes ready.
			select {
			case <-firstCarrier:
			case <-time.After(time.Second):
				t.Fatal("the original relay stayed open after the tunnel handshake timed out")
			}

			readyCh := make(chan struct{})
			fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer fcancel()
			s2 := &sources{ctx: fctx, feeds: map[string]*feed{}}
			go func() {
				_ = o.rep.followRemote(s2, ids.machineID, f, func() { close(readyCh) })
			}()
			select {
			case <-readyCh:
			case <-time.After(4 * time.Second):
				t.Fatal("the next follow did not become ready on the replacement relay")
			}
		})
	}
}

// TestReconnectProbesRelayedTunnelAndWakesReplicaRetry verifies that on a high-RTT
// (~340 ms) relay where outer Ping succeeds, calling dev.Reconnect() (network
// switch or user tapping Reconnect Now):
//  1. wakes a machine feed sleeping in retry backoff without waiting 30 s, and
//  2. probes and evicts a blackholed cached relayed tunnel so the feed recovers.
func TestReconnectProbesRelayedTunnelAndWakesReplicaRetry(t *testing.T) {
	ids := makeTestIdentities(t)
	var tunnelNum atomic.Int32
	var blockFirstTunnel atomic.Bool
	var blockAllTunnels atomic.Bool
	relayURL := startRecoveryRelay(t, ids, func(_ int32, _ net.Conn) machinev1connect.MachineServiceHandler {
		n := tunnelNum.Add(1)
		if blockAllTunnels.Load() {
			return nil // the machine closes the tunnel, putting the feed in backoff
		}
		machine := recoveryWatchMachine{}
		if n == 1 {
			machine.block = &blockFirstTunnel
		}
		return machine
	})
	// Cellular relay latency of about 340 ms.
	dev := openRecoveryDevice(t, ids, startLatencyProxy(t, strings.TrimPrefix(relayURL, "http://"), 170*time.Millisecond))
	o := dev.owner()
	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		dev.Run(runCtx)
	}()
	defer func() {
		runCancel()
		<-runDone
	}()

	updates := make(chan *localv1.WatchResponse, 32)
	go func() {
		_ = (&localService{d: dev}).watchRound(runCtx, func(update *localv1.WatchResponse) error {
			select {
			case updates <- update:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		})
	}()
	waitTunnelPhase := func(t *testing.T, want string, within time.Duration) *localv1.Machine {
		t.Helper()
		timer := time.NewTimer(within)
		defer timer.Stop()
		for {
			select {
			case update := <-updates:
				for _, m := range update.GetMachines() {
					if m.MachineId == ids.machineID && m.GetTunnel().GetPhase() == want {
						return m
					}
				}
			case <-timer.C:
				t.Fatalf("machine did not reach tunnel phase %q within %v", want, within)
			}
		}
	}

	// Establish initial ready relayed tunnel at ~340 ms RTT.
	m := waitTunnelPhase(t, "ready", 10*time.Second)
	if m.GetTunnel().GetPath() != "relay" {
		t.Fatalf("expected relay path, got %q", m.GetTunnel().GetPath())
	}
	if st := o.Status(); !st.RelayConnected {
		t.Fatalf("expected relay connected, got %+v", st.RelayHealth)
	}

	// The outer relay stays healthy when one cached tunnel stops answering.
	ctx, cancel := context.WithTimeout(runCtx, 2*time.Second)
	c, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	carrier := c.Done()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("the relay should answer independently of its tunnels: %v", err)
	}
	cancel()
	blockFirstTunnel.Store(true)
	dev.Reconnect()
	waitTunnelPhase(t, "ready", 7*time.Second)
	if tunnelNum.Load() < 2 {
		t.Fatal("Reconnect did not replace the unresponsive tunnel")
	}
	select {
	case <-carrier:
		t.Fatal("Reconnect dropped the healthy outer relay")
	default:
	}

	// Force the feed into a long retry backoff (30 s) while
	// outer relay stays connected, then unblock and call dev.Reconnect().
	// Feed must wake immediately instead of waiting 30 s.
	o.rep.mu.Lock()
	if o.rep.src != nil {
		if f := o.rep.src.feeds[ids.machineID]; f != nil {
			f.retry = retryMax // 30s backoff
		}
	}
	o.rep.mu.Unlock()
	blockAllTunnels.Store(true)
	o.forgetMachine(ids.machineID)
	waitTunnelPhase(t, "unreachable", 5*time.Second)

	blockAllTunnels.Store(false)
	reconnStart := time.Now()
	dev.Reconnect()
	waitTunnelPhase(t, "ready", 6*time.Second)
	if took := time.Since(reconnStart); took > 6*time.Second {
		t.Fatalf("dev.Reconnect() took %v to wake backed-off feed", took)
	}
}
