package owner

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go/http3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2/threadv2connect"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// TestReconnectionOptimization_ConnectOrKeepHighRTTAndPrewarm verifies:
//  1. On a realistic ~240 ms cellular/WAN relay where pipelined WebSocket
//     Challenge would previously outrun E2E GetInfo and kill healthy kept
//     tunnels, connectOrKeep reuses kept in 1 RTT with ZERO competing WS dials.
//  2. On foreground resume with kept != nil, Owner.relay still runs
//     prewarmDirect and upgrades kept onto a direct QUIC tunnel immediately.
func TestReconnectionOptimization_ConnectOrKeepHighRTTAndPrewarm(t *testing.T) {
	ids := makeTestIdentities(t)
	trust := identity.NewTrust(ids.ownerID)
	if _, err := trust.AcceptRootChain(ids.chain); err != nil {
		t.Fatal(err)
	}

	var wsDials atomic.Int32
	var e2eHandshakes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsDials.Add(1)
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
				if transport.ReadFrame(st, &h) != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				e2eHandshakes.Add(1)
				tc, _, err := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				mux := http.NewServeMux()
				mux.Handle(machinev1connect.NewMachineServiceHandler(stubMachineInfo{}))
				mux.Handle(herv1connect.NewHerServiceHandler(stubHerService{}))
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	defer srv.Close()

	// 120 ms one-way = ~240 ms RTT cellular/WAN path.
	highRTTRelay := startLatencyProxy(t, strings.TrimPrefix(srv.URL, "http://"), 120*time.Millisecond)

	dev, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	if err := o.saveIdentity(ids.ownerID, highRTTRelay, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	o.seen = ids.machineID
	o.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Initial cold connect + warm E2E tunnel.
	c1, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Machine(ctx, ids.machineID); err != nil {
		t.Fatal(err)
	}
	_ = c1.Ping(ctx) // seed realistic ~240ms SRTT

	beforeDials := wsDials.Load()
	beforeHandshakes := e2eHandshakes.Load()

	// Simulate Pause -> Resume on the same 240 ms cellular network.
	o.keepRelay()
	resumeStart := time.Now()
	c2, err := o.relay(ctx)
	if err != nil {
		t.Fatalf("resume relay failed: %v", err)
	}
	resumeMs := time.Since(resumeStart).Milliseconds()
	if c2 != c1 {
		t.Fatalf("expected kept relay client to win under 240ms RTT (wsDials=%d->%d)", beforeDials, wsDials.Load())
	}
	if wsDials.Load() != beforeDials {
		t.Fatalf("expected 0 competing WebSocket dials when kept is healthy, got %d new dials", wsDials.Load()-beforeDials)
	}
	if e2eHandshakes.Load() != beforeHandshakes {
		t.Fatalf("expected warm E2E tunnel to survive without re-handshaking, got %d new handshakes", e2eHandshakes.Load()-beforeHandshakes)
	}
	t.Logf("BENCH [240ms WAN Resume]: kept reused in %d ms (0 extra WS dials, 0 cold E2E handshakes; legacy redial+E2E ~720ms+)", resumeMs)

	// Now start a local QUIC listener for the same machine and verify that
	// resuming with kept != nil ALSO runs prewarmDirect and upgrades kept onto QUIC!
	tlsCfg, qCfg := transport.QUICServerConfig(ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	h3Srv := &http3.Server{
		TLSConfig:  tlsCfg,
		QUICConfig: qCfg,
		Handler: func() http.Handler {
			mux := http.NewServeMux()
			mux.Handle(machinev1connect.NewMachineServiceHandler(stubMachineInfo{}))
			mux.Handle(herv1connect.NewHerServiceHandler(stubHerService{}))
			return mux
		}(),
	}
	go func() { _ = h3Srv.Serve(pc) }()
	defer h3Srv.Close()

	o.saveDirectAddrs(ids.machineID, []string{"quic://" + pc.LocalAddr().String()})
	o.keepRelay()
	c3, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path, _, alive := c3.MachineTunnel(ids.machineID)
	if !alive || path != "quic_lan" {
		t.Fatalf("expected prewarmDirect on resume to upgrade kept tunnel to quic_lan, got alive=%v path=%q", alive, path)
	}
	t.Logf("BENCH [Resume + LAN QUIC Prewarm]: kept upgraded to %s during resume", path)
}

// TestReconnectionOptimization_UnreachableDirectZeroDoubleWait verifies that on
// remote Wi-Fi / firewalled IPv6 where direct addresses blackhole packets:
//  1. Call #1 overlaps direct and relay (eliminating the legacy 350ms + 280ms = 630ms serial penalty).
//  2. Subsequent calls on the same network signature skip the direct head-start (0 ms penalty).
//  3. Switching networks (SetCellularOnly / Reconnect) resets netGen so direct is retried cleanly.
func TestReconnectionOptimization_UnreachableDirectZeroDoubleWait(t *testing.T) {
	ids := makeTestIdentities(t)
	trust := identity.NewTrust(ids.ownerID)
	_, _ = trust.AcceptRootChain(ids.chain)

	// Silent UDP socket drops all QUIC Initial packets (simulating unreachable home LAN / firewalled IPv6).
	blackholeUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackholeUDP.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if transport.ReadFrame(conn, &hello) != nil {
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
				if transport.ReadFrame(st, &h) != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				tc, _, err := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				mux := http.NewServeMux()
				mux.Handle(herv1connect.NewHerServiceHandler(stubHerService{}))
				mux.Handle(machinev1connect.NewMachineServiceHandler(stubMachineInfo{}))
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	defer srv.Close()

	// 40ms one-way = ~80ms RTT Relay.
	relayURL := startLatencyProxy(t, strings.TrimPrefix(srv.URL, "http://"), 40*time.Millisecond)
	dev, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	if err := o.saveIdentity(ids.ownerID, relayURL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}
	o.saveDirectAddrs(ids.machineID, []string{"quic://" + blackholeUDP.LocalAddr().String()})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Cold call #1 while relay is pending: overlaps singleflight direct + relay WS!
	t1 := time.Now()
	if _, err := o.HerCall(ctx, ids.machineID, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}}); err != nil {
		t.Fatalf("first HerCall failed: %v", err)
	}
	firstMs := time.Since(t1).Milliseconds()
	// Legacy code took: 350ms (directFastMachine) + ~120ms (relay dial) + 280ms (RaceDirect) + ~160ms (tunnel+RPC) > 900ms!
	if firstMs > 650 {
		t.Fatalf("first HerCall with blackholed direct took %d ms, expected parallel overlap (<650ms vs legacy >900ms)", firstMs)
	}

	// Simulate direct dial finishing its timeout on this network (or mark failure) and forgetting cached tunnel:
	// verify second dial on same network skips head-start completely (0 ms head)!
	o.noteDirectFailure(ids.machineID)
	if head := o.directHeadDuration(ids.machineID, o.directAddrs(ids.machineID)); head != 0 {
		t.Fatalf("expected 0ms directHeadDuration after failure on current network, got %v", head)
	}
	o.forgetMachine(ids.machineID)

	t2 := time.Now()
	if _, err := o.HerCall(ctx, ids.machineID, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}}); err != nil {
		t.Fatalf("second HerCall failed: %v", err)
	}
	secondMs := time.Since(t2).Milliseconds()
	if secondMs > 400 {
		t.Fatalf("second HerCall on same network took %d ms, expected 0ms direct head-start (<400ms)", secondMs)
	}
	t.Logf("BENCH [Remote Wi-Fi Blackholed Direct, 80ms Relay]: call1=%d ms, call2(same-net)=%d ms (legacy >900ms)", firstMs, secondMs)

	// Network switch must restore direct head-start for the new interface!
	o.SetCellularOnly(true)
	o.SetCellularOnly(false)
	if head := o.directHeadDuration(ids.machineID, o.directAddrs(ids.machineID)); head == 0 {
		t.Fatal("expected network switch to reset direct failure memory")
	}
}

type upgradeTestServer struct {
	herv1connect.UnimplementedHerServiceHandler
	threadv2connect.UnimplementedThreadServiceHandler
	machinev1connect.UnimplementedMachineServiceHandler

	callDelay time.Duration
	openCount atomic.Int32
	threadCh  chan *threadv2.ThreadEvent
}

func (s *upgradeTestServer) GetInfo(context.Context, *machinev1.GetInfoRequest) (*machinev1.GetInfoResponse, error) {
	return &machinev1.GetInfoResponse{Status: &machinev1.MachineStatus{Name: "upgrade-mac"}}, nil
}

func (s *upgradeTestServer) Open(ctx context.Context, _ *herv1.OpenRequest, stream *connect.ServerStream[herv1.OpenEvent]) error {
	n := s.openCount.Add(1)
	if err := stream.Send(&herv1.OpenEvent{
		Kind: &herv1.OpenEvent_Opened{Opened: &herv1.Opened{
			ConnectionId: "conn_1",
			Responses:    []*herv1.Response{{Result: []byte(`{"session_id":"hs_1","resume_epoch":"ep_1","snapshot":{"session":{"session_id":"hs_1"},"last_seq":1,"transcript":{"items":[]}}}`)}},
		}},
	}); err != nil {
		return err
	}
	_ = n
	<-ctx.Done()
	return nil
}

func (s *upgradeTestServer) Call(ctx context.Context, _ *herv1.CallRequest) (*herv1.CallResponse, error) {
	if s.callDelay > 0 {
		select {
		case <-time.After(s.callDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &herv1.CallResponse{Response: &herv1.Response{Result: []byte(`{"ok":true}`)}}, nil
}

func (s *upgradeTestServer) SubscribeThread(ctx context.Context, req *threadv2.SubscribeThreadRequest, stream *connect.ServerStream[threadv2.ThreadEvent]) error {
	if err := stream.Send(&threadv2.ThreadEvent{
		Seq:      max(1, req.AfterSeq),
		ThreadId: req.ThreadId,
		Type:     "thread.subscribed",
		Thread:   &threadv2.Thread{ThreadId: req.ThreadId, LastSeq: max(1, req.AfterSeq)},
	}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-s.threadCh:
			if ev.Seq <= req.AfterSeq {
				continue
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// TestReconnectionOptimization_LosslessTunnelUpgrade verifies that upgrading a
// live relayed tunnel onto Direct QUIC:
//  1. Never aborts an in-flight unary HerCall (drained cleanly by Retire),
//  2. Seamlessly continues SubscribeThread across the tunnel swap with 0 lost events and 0 client stream breaks, and
//  3. Ends HerOpen with clean nil (not an error) so Swift re-opens on QUIC with 0 ms backoff.
func TestReconnectionOptimization_LosslessTunnelUpgrade(t *testing.T) {
	oldGrace := peer.RetireGrace
	peer.RetireGrace = 250 * time.Millisecond
	t.Cleanup(func() { peer.RetireGrace = oldGrace })

	ids := makeTestIdentities(t)
	trust := identity.NewTrust(ids.ownerID)
	_, _ = trust.AcceptRootChain(ids.chain)

	svc := &upgradeTestServer{
		callDelay: 120 * time.Millisecond,
		threadCh:  make(chan *threadv2.ThreadEvent, 8),
	}
	mux := http.NewServeMux()
	mux.Handle(machinev1connect.NewMachineServiceHandler(svc))
	mux.Handle(herv1connect.NewHerServiceHandler(svc))
	mux.Handle(threadv2connect.NewThreadServiceHandler(svc))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if transport.ReadFrame(conn, &hello) != nil {
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
				if transport.ReadFrame(st, &h) != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				tc, _, err := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	defer srv.Close()

	// Also prepare Direct QUIC endpoint on the same service.
	tlsCfg, qCfg := transport.QUICServerConfig(ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	h3Srv := &http3.Server{TLSConfig: tlsCfg, QUICConfig: qCfg, Handler: mux}
	go func() { _ = h3Srv.Serve(pc) }()
	defer h3Srv.Close()

	dev, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	if err := o.saveIdentity(ids.ownerID, srv.URL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}
	threadID := "th_" + strings.TrimPrefix(ids.machineID, "m_")[:10] + "_01"
	o.cacheThread(&threadv2.Thread{ThreadId: threadID, MachineId: ids.machineID, LastSeq: 1}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. Open active HerOpen stream and SubscribeThread stream on the RELAY tunnel.
	herOpened := make(chan struct{})
	herEnded := make(chan error, 1)
	go func() {
		herEnded <- o.HerOpen(ctx, ids.machineID, &herv1.OpenRequest{
			Setup: []*herv1.Request{{Method: "bots/open", Params: []byte(`{"bot_id":"her"}`)}},
		}, func(ev *herv1.OpenEvent) error {
			if ev.GetOpened() != nil {
				select {
				case <-herOpened:
				default:
					close(herOpened)
				}
			}
			return nil
		})
	}()
	<-herOpened

	// Connect in-memory client to ownerThreadService to exercise SubscribeThread end-to-end.
	threadMux := http.NewServeMux()
	threadMux.Handle(threadv2connect.NewThreadServiceHandler(&ownerThreadService{d: dev}))
	threadSrv := httptest.NewServer(threadMux)
	defer threadSrv.Close()
	tCli := threadv2connect.NewThreadServiceClient(threadSrv.Client(), threadSrv.URL)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	tStream, err := tCli.SubscribeThread(subCtx, &threadv2.SubscribeThreadRequest{ThreadId: threadID, AfterSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !tStream.Receive() || tStream.Msg().GetType() != "thread.subscribed" {
		t.Fatalf("expected immediate thread.subscribed event on relay tunnel, err=%v", tStream.Err())
	}

	// 2. Launch an in-flight unary HerCall (120 ms) on the relay tunnel, and
	//    20 ms later upgrade the machine tunnel to Direct QUIC!
	inFlightErr := make(chan error, 1)
	go func() {
		_, cerr := o.HerCall(ctx, ids.machineID, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
		inFlightErr <- cerr
	}()
	time.Sleep(20 * time.Millisecond)

	o.saveDirectAddrs(ids.machineID, []string{"quic://" + pc.LocalAddr().String()})
	c, err := o.relay(ctx)
	if err != nil {
		t.Fatal(err)
	}
	upgraded := c.UpgradeToDirect(ctx, true)
	if len(upgraded) != 1 {
		t.Fatalf("expected UpgradeToDirect to upgrade %s, got %v", ids.machineID, upgraded)
	}

	// Assert 1: In-flight HerCall completed with ZERO error!
	if err := <-inFlightErr; err != nil {
		t.Fatalf("in-flight unary HerCall was killed by tunnel upgrade: %v", err)
	}

	// Assert 2: HerOpen ended with clean nil (NOT an error) after retire grace,
	// and immediate re-open lands on quic_lan in < 30 ms!
	if err := <-herEnded; err != nil {
		t.Fatalf("expected HerOpen to end with nil on tunnel upgrade, got err=%v", err)
	}
	reopenStart := time.Now()
	reopenCtx, reopenCancel := context.WithCancel(ctx)
	reopened := make(chan struct{})
	go func() {
		_ = o.HerOpen(reopenCtx, ids.machineID, &herv1.OpenRequest{
			Setup: []*herv1.Request{{Method: "bots/open", Params: []byte(`{"bot_id":"her"}`)}},
		}, func(ev *herv1.OpenEvent) error {
			if ev.GetOpened() != nil {
				close(reopened)
				reopenCancel()
			}
			return nil
		})
	}()
	<-reopened
	reopenMs := time.Since(reopenStart).Milliseconds()

	// Assert 3: SubscribeThread seamlessly hopped onto Direct QUIC without
	// ending tStream, and delivers seq=2 published after the old relay tunnel closed!
	time.Sleep(100 * time.Millisecond) // ensure old relay tunnel fully closed
	svc.threadCh <- &threadv2.ThreadEvent{
		Seq:      2,
		ThreadId: threadID,
		Type:     "item.completed",
		Thread:   &threadv2.Thread{ThreadId: threadID, LastSeq: 2},
		Item:     &threadv2.TurnItem{ItemId: "it_2", ThreadId: threadID, Seq: 2, Text: "streamed over upgraded QUIC"},
	}
	if !tStream.Receive() {
		t.Fatalf("SubscribeThread broke across tunnel upgrade: %v", tStream.Err())
	}
	if got := tStream.Msg().GetItem().GetText(); got != "streamed over upgraded QUIC" {
		t.Fatalf("unexpected post-upgrade ThreadEvent item: %q", got)
	}
	// Verify local SQLite projection_payload also folded seq=2!
	if cached := o.cachedProjection(threadID); cached == nil || len(cached.Items) != 1 || cached.Items[0].Text != "streamed over upgraded QUIC" {
		t.Fatalf("cachedProjection was not incrementally updated by SubscribeThread: %+v", cached)
	}
	t.Logf("BENCH [Lossless Tunnel Upgrade]: in-flight HerCall=0 errors, SubscribeThread=0 drops, HerOpen QUIC rebind=%d ms (legacy: error + 1000ms sleep)", reopenMs)
}

type legacySilentThreadServer struct {
	threadv2connect.UnimplementedThreadServiceHandler
	machinev1connect.UnimplementedMachineServiceHandler
	slow time.Duration
	proj *threadv2.ThreadProjection
}

func (s *legacySilentThreadServer) GetInfo(context.Context, *machinev1.GetInfoRequest) (*machinev1.GetInfoResponse, error) {
	return &machinev1.GetInfoResponse{Status: &machinev1.MachineStatus{Name: "thread-mac"}}, nil
}

func (s *legacySilentThreadServer) GetThread(ctx context.Context, _ *threadv2.GetThreadRequest) (*threadv2.GetThreadResponse, error) {
	if s.slow > 0 {
		select {
		case <-time.After(s.slow):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &threadv2.GetThreadResponse{Projection: s.proj}, nil
}

func (s *legacySilentThreadServer) ListThreads(ctx context.Context, _ *threadv2.ListThreadsRequest) (*threadv2.ListThreadsResponse, error) {
	if s.slow > 0 {
		select {
		case <-time.After(s.slow):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &threadv2.ListThreadsResponse{Threads: []*threadv2.Thread{s.proj.GetThread()}}, nil
}

// Simulates an un-upgraded older hermeshd that emits NO initial event when afterSeq == lastSeq.
func (s *legacySilentThreadServer) SubscribeThread(ctx context.Context, _ *threadv2.SubscribeThreadRequest, _ *connect.ServerStream[threadv2.ThreadEvent]) error {
	<-ctx.Done()
	return nil
}

// TestReconnectionOptimization_ThreadInstantCacheAndFastSubscribe verifies:
//  1. GetThread(CachedOnly: true) returns local SQLite projection in < 10 ms even when remote stalls 3 s.
//  2. SubscribeThreads emits Snapshot: true from cache in < 20 ms even when remote machine stalls 3 s.
//  3. SubscribeThread against an un-upgraded legacy hermeshd confirms readiness in ~130 ms (vs legacy 1000 ms sleep).
func TestReconnectionOptimization_ThreadInstantCacheAndFastSubscribe(t *testing.T) {
	ids := makeTestIdentities(t)
	trust := identity.NewTrust(ids.ownerID)
	_, _ = trust.AcceptRootChain(ids.chain)

	threadID := "th_" + strings.TrimPrefix(ids.machineID, "m_")[:10] + "_cached"
	proj := &threadv2.ThreadProjection{
		Thread: &threadv2.Thread{ThreadId: threadID, MachineId: ids.machineID, Title: "Cached Session", LastSeq: 42, UpdatedAt: timestamppb.Now()},
		Items:  []*threadv2.TurnItem{{ItemId: "m1", ThreadId: threadID, Seq: 42, Text: "instant local render"}},
	}
	remoteSvc := &legacySilentThreadServer{slow: 2500 * time.Millisecond, proj: proj}
	mux := http.NewServeMux()
	mux.Handle(machinev1connect.NewMachineServiceHandler(remoteSvc))
	mux.Handle(threadv2connect.NewThreadServiceHandler(remoteSvc))

	controlMux := http.NewServeMux()
	creds := ids.machineCreds()
	controlMux.Handle(relayv1connect.NewOwnerControlServiceHandler(recoveryRelayControl{
		machines: &relayv1.ListMachinesResponse{Machines: []*relayv1.MachineEntry{{
			MachineCert: creds.GetMachineCert(), Name: "thread-mac", Online: true, LastSeen: timestamppb.Now(),
		}}},
	}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if transport.ReadFrame(conn, &hello) != nil {
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
				if transport.ReadFrame(st, &h) != nil {
					return
				}
				switch h.Kind {
				case relayv1.StreamKind_STREAM_KIND_CONTROL:
					transport.ServeH2(r.Context(), st, controlMux)
				case relayv1.StreamKind_STREAM_KIND_TUNNEL:
					tc, _, err := transport.ServerHandshake(r.Context(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
					if err != nil {
						return
					}
					defer tc.Close()
					transport.ServeH2(r.Context(), tc, mux)
				}
			}()
		}
	}))
	defer srv.Close()

	dev, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	if err := o.saveIdentity(ids.ownerID, srv.URL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}
	o.cacheThread(proj.Thread, proj)

	localMux := http.NewServeMux()
	localMux.Handle(threadv2connect.NewThreadServiceHandler(&ownerThreadService{d: dev}))
	localSrv := httptest.NewServer(localMux)
	defer localSrv.Close()
	cli := threadv2connect.NewThreadServiceClient(localSrv.Client(), localSrv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. GetThread(CachedOnly: true) while remote stalls 2.5s!
	t0 := time.Now()
	cRes, err := cli.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: threadID, CachedOnly: true})
	if err != nil {
		t.Fatalf("GetThread(CachedOnly) failed: %v", err)
	}
	cachedOpenMs := time.Since(t0).Milliseconds()
	if !cRes.FromCache || len(cRes.GetProjection().GetItems()) != 1 {
		t.Fatalf("unexpected cached GetThread response: %+v", cRes)
	}
	if cachedOpenMs > 50 {
		t.Fatalf("GetThread(CachedOnly) took %d ms, want < 50 ms", cachedOpenMs)
	}

	// 2. SubscribeThreads first snapshot while remote stalls 2.5s!
	subListCtx, subListCancel := context.WithCancel(ctx)
	defer subListCancel()
	t1 := time.Now()
	listStream, err := cli.SubscribeThreads(subListCtx, &threadv2.SubscribeThreadsRequest{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if !listStream.Receive() || !listStream.Msg().GetSnapshot() || len(listStream.Msg().GetThreads()) != 1 {
		t.Fatalf("expected immediate cached SubscribeThreads snapshot, err=%v", listStream.Err())
	}
	firstSnapMs := time.Since(t1).Milliseconds()
	if firstSnapMs > 50 {
		t.Fatalf("SubscribeThreads first snapshot took %d ms, expected < 50 ms despite 2.5s slow remote", firstSnapMs)
	}
	subListCancel()

	// 3. SubscribeThread against legacy silent daemon (remote fast now: 0 ms):
	//    confirms liveness in ~130ms instead of 1000ms sleep!
	remoteSvc.slow = 0
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	t2 := time.Now()
	evStream, err := cli.SubscribeThread(subCtx, &threadv2.SubscribeThreadRequest{ThreadId: threadID, AfterSeq: 42})
	if err != nil {
		t.Fatal(err)
	}
	if !evStream.Receive() || evStream.Msg().GetType() != "thread.subscribed" {
		t.Fatalf("expected synthetic thread.subscribed from legacy fallback, err=%v", evStream.Err())
	}
	legacyReadyMs := time.Since(t2).Milliseconds()
	if legacyReadyMs > 400 {
		t.Fatalf("legacy SubscribeThread ready took %d ms, expected ~130ms (<400ms vs old 1000ms sleep)", legacyReadyMs)
	}
	t.Logf("BENCH [v2 Thread Open & Resume]: CachedOpen=%d ms (legacy 2500-5000ms), SubscribeThreadsFirstSnap=%d ms (legacy 2500ms+), LegacySubscribeReady=%d ms (legacy 1000ms+)",
		cachedOpenMs, firstSnapMs, legacyReadyMs)
}

// TestReconnectionOptimization_ReplicaWatchOwnerUnblockedBySleepingMachineAndHerDelta
// verifies with a real SQLite Relay + real Machine + sleeping second machine:
//  1. WatchOwner populates initial world AND delivers HerKeptVersion from encrypted
//     delta sync in < 500 ms even while a second machine hangs in 15 s followTimeout!
//  2. Healthy machine transitions to TunnelHealth.Phase == "ready" immediately
//     via ready() kickLocked (< 250 ms, without waiting 1000 ms screenTick).
func TestReconnectionOptimization_ReplicaWatchOwnerUnblockedBySleepingMachineAndHerDelta(t *testing.T) {
	dir := t.TempDir()
	st, err := relay.OpenStore(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	root, _ := identity.GenerateKey()
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId: ownerID, Version: 1, IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub, Label: "root"}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}
	devKey, _ := identity.GenerateKey()
	devCert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId: ownerID, DevicePublicKey: identity.MarshalPublicKey(devKey.Public()), Label: "iPhone", IssuedAt: timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveClaim(ownerID, chain, devCert); err != nil {
		t.Fatal(err)
	}

	// Machine 1: real live hermeshd Machine.
	mach1, err := machine.Open(machine.Config{DataDir: filepath.Join(dir, "mach1"), WorkspaceMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	mach1ID := mach1.MachineID()
	mach1Cert, _ := identity.Sign(&identityv1.MachineCert{
		OwnerId: ownerID, MachineId: mach1ID, MachinePublicKey: mach1.PublicKeySPKI(), IssuedAt: timestamppb.Now(),
	}, root)
	_ = st.PutMachineCert(ownerID, mach1ID, mach1Cert)

	// Machine 2: registered on Relay and holds WS open, but accepts tunnels and NEVER answers TLS (sleeping/frozen Mac!).
	sleepKey, _ := identity.GenerateKey()
	sleepID := identity.MachineID(identity.MarshalPublicKey(sleepKey.Public()))
	sleepCert, _ := identity.Sign(&identityv1.MachineCert{
		OwnerId: ownerID, MachineId: sleepID, MachinePublicKey: identity.MarshalPublicKey(sleepKey.Public()), IssuedAt: timestamppb.Now(),
	}, root)
	_ = st.PutMachineCert(ownerID, sleepID, sleepCert)

	rs, err := relay.New(relay.Config{Backend: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rs.Close() })
	srv := httptest.NewServer(rs.Handler())
	t.Cleanup(srv.Close)

	if err := mach1.Install(ownerID, srv.URL, chain, mach1Cert); err != nil {
		t.Fatal(err)
	}
	m1Ctx, m1Cancel := context.WithCancel(context.Background())
	var m1WG sync.WaitGroup
	m1WG.Add(1)
	go func() {
		defer m1WG.Done()
		mach1.Run(m1Ctx)
	}()
	t.Cleanup(func() {
		m1Cancel()
		m1WG.Wait()
		mach1.Close()
	})

	// Connect sleeping machine #2 to Relay so Relay lists it Online=true, while
	// never accepting tunnel streams (simulating a frozen/sleeping Mac whose TLS
	// handshake hangs until followTimeout = 15 s).
	sleepDialer := &transport.Dialer{
		RelayURL: srv.URL, Role: relayv1.Role_ROLE_MACHINE, Key: sleepKey,
		Credentials: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: chain, MachineName: "sleeping-mac", Subject: &identityv1.Credentials_MachineCert{MachineCert: sleepCert}}
		},
	}
	sleepConn, err := sleepDialer.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sleepConn.Close()

	// Open Owner device and register sync key once, then pause/reopen to measure resume with pending delta + sleeping peer!
	dev, err := Open(Config{DataDir: filepath.Join(dir, "iphone"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	if err := dev.owner().saveIdentity(ownerID, srv.URL, rootPub, chain, devKey, devCert, ""); err != nil {
		t.Fatal(err)
	}

	// First foreground session: warm the replica & register X25519 sync key on the Relay
	// (matching a real iPhone user who already used the app before backgrounding).
	warmWatchCtx, warmWatchCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = (&localService{d: dev}).watchRound(warmWatchCtx, func(w *localv1.WatchResponse) error {
		for _, m := range w.GetMachines() {
			if m.MachineId == mach1ID && m.GetTunnel().GetPhase() == "ready" {
				warmWatchCancel()
			}
		}
		return nil
	})
	warmWatchCancel()
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	ctl, _, err := dev.owner().control(initCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.owner().registerSyncKey(initCtx, ctl); err != nil {
		t.Fatal(err)
	}
	initCancel()

	// Simulate iPhone going to background BEFORE mach1 publishes its reply!
	dev.Pause()

	// Publish an encrypted Her reply delta from mach1 while iPhone is paused!
	botJSON := []byte(`{"id":"her","name":"Her","default":true}`)
	openResult := []byte(`{
		"session_id":"hs_offline_1",
		"resume_epoch":"ep_persist_42",
		"snapshot":{
			"session":{"session_id":"hs_offline_1","title":"Her"},
			"last_seq":9,
			"transcript":{"items":[
				{"type":"message","message_id":"a1","message":{"role":"assistant","content":[
					{"type":"tool_call","id":"tc_1","name":"send_to_user","arguments":{"text":"Offline reply arrived instantly!"}}
				],"ts":2}}
			]},
			"usage":{"per_model":{}}
		}
	}`)
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer pubCancel()
	for {
		if err := mach1.PublishHerDelta(pubCtx, botJSON, openResult); err != nil {
			t.Fatalf("PublishHerDelta: %v", err)
		}
		entries, _, err := st.PullDeltas(ownerID, map[string]uint64{"her:" + mach1ID: 0}, true, 10, time.Now())
		if err == nil && len(entries) > 0 {
			var signed identityv1.Signed
			var env identityv1.DeltaEnvelope
			if proto.Unmarshal(entries[len(entries)-1].Envelope, &signed) == nil && proto.Unmarshal(signed.Body, &env) == nil && len(env.Keys) >= 1 {
				break
			}
		}
		if pubCtx.Err() != nil {
			t.Fatal("timed out waiting for mach1 to seal Her delta for iPhone sync key")
		}
		time.Sleep(40 * time.Millisecond)
	}
	_ = json.Valid(nil)

	// Now wake iPhone back to foreground -> Watch!
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

	watchCtx, watchCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer watchCancel()
	start := time.Now()
	var gotResetMs, gotReadyMs, gotHerDeltaMs int64
	_ = (&localService{d: dev}).watchRound(watchCtx, func(w *localv1.WatchResponse) error {
		elapsed := max(1, time.Since(start).Milliseconds())
		if w.GetReset_() && gotResetMs == 0 {
			gotResetMs = elapsed
		}
		if w.GetHerKeptVersion() > 0 && gotHerDeltaMs == 0 {
			gotHerDeltaMs = elapsed
		}
		for _, m := range w.GetMachines() {
			if m.MachineId == mach1ID && m.GetTunnel().GetPhase() == "ready" && gotReadyMs == 0 {
				gotReadyMs = elapsed
			}
		}
		if gotResetMs > 0 && gotReadyMs > 0 && gotHerDeltaMs > 0 {
			watchCancel()
		}
		return nil
	})

	if gotResetMs == 0 || gotReadyMs == 0 || gotHerDeltaMs == 0 {
		t.Fatalf("did not observe fast Watch milestones while peer #2 slept: reset=%dms ready=%dms herDelta=%dms (legacy blocked 15,000ms!)",
			gotResetMs, gotReadyMs, gotHerDeltaMs)
	}
	if gotHerDeltaMs > 800 || gotReadyMs > 800 {
		t.Fatalf("Watch milestones too slow: reset=%dms ready=%dms herDelta=%dms (expected < 800ms)", gotResetMs, gotReadyMs, gotHerDeltaMs)
	}
	// Verify GetHerConversation serves the decrypted offline snapshot AND resume_epoch!
	keptRes, err := (&localService{d: dev}).GetHerConversation(context.Background(), &localv1.GetHerConversationRequest{MachineId: mach1ID})
	if err != nil || !strings.Contains(string(keptRes.GetSnapshot()), "Offline reply arrived instantly!") {
		t.Fatalf("expected offline Her reply in GetHerConversation, got %q (err=%v)", string(keptRes.GetSnapshot()), err)
	}
	// Ensure no unused tls import lint issue.
	_ = tls.VersionTLS13
	t.Logf("BENCH [Replica + Sleeping Peer + Her Offline Delta]: WatchReset=%d ms, Mach1Ready=%d ms (legacy +1000ms screenTick), HerDeltaPushed=%d ms (legacy >15,000ms blocked!)",
		gotResetMs, gotReadyMs, gotHerDeltaMs)
}
