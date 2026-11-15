package owner

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1/screenv1connect"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// A persistently sleeping machine must not make every screen retry reset the
// shared relay. Candidates cannot spend a Connect request's recovery budget.
func TestScreenTunnelRecoveryOncePerConnectRequest(t *testing.T) {
	var dials atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		dials.Add(1)
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
			t.Error(err)
			return
		}
		defer sess.Close()
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() { defer st.Close(); io.Copy(io.Discard, st) }()
		}
	}))
	t.Cleanup(srv.Close)
	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)
	o := dev.owner()
	o.ownerID = "o_test"
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	d := &peer.Dialer{RelayURL: srv.URL, Role: relayv1.Role_ROLE_OWNER, Key: key, Trust: identity.NewTrust(o.ownerID), Credentials: func() *identityv1.Credentials { return &identityv1.Credentials{} }}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var c *peer.Client
	connectRelay := func() {
		var err error
		c, err = d.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		opened := c
		t.Cleanup(func() { opened.Close() })
		o.mu.Lock()
		o.client = c
		o.mu.Unlock()
	}
	connectRelay()
	call := screenCall(o, "m_sleeping", screenv1connect.ScreenServiceClient.Connect)
	req := &screenv1.ConnectRequest{Offer: "one logical offer"}
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-c.Done():
			connectRelay()
		default:
		}
		short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		_, err := call(short, req)
		stop()
		if err == nil {
			t.Fatal("sleeping machine answered")
		}
	}
	if dials.Load() != 2 {
		t.Fatalf("three retries used %d outer connections, want 2", dials.Load())
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("repeated Connect reset shared connection again: %v", err)
	}
	candidates := screenCall(o, "m_sleeping", screenv1connect.ScreenServiceClient.Candidates)
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = candidates(short, &screenv1.CandidatesRequest{})
	stop()
	if err == nil {
		t.Fatal("sleeping machine answered candidates")
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Candidates reset shared connection: %v", err)
	}
	// A different offer gets its own one-shot budget.
	short, stop = context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = call(short, &screenv1.ConnectRequest{Offer: "new logical offer"})
	stop()
	if err == nil {
		t.Fatal("sleeping machine answered new offer")
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("new offer could not recover its relay")
	}
}

type silentConn struct {
	net.Conn
	silent  *atomic.Bool
	unblock <-chan struct{}
}

func (c silentConn) Read(p []byte) (int, error) {
	if c.silent.Load() {
		<-c.unblock
		return 0, io.EOF
	}
	return c.Conn.Read(p)
}

func (c silentConn) Write(p []byte) (int, error) {
	if c.silent.Load() {
		<-c.unblock
		return 0, io.EOF
	}
	return c.Conn.Write(p)
}

func connectSilentRelay(t *testing.T) (*Device, *Owner, *peer.Client, *atomic.Bool) {
	t.Helper()
	var silent atomic.Bool
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
			if err != nil {
				return
			}
			conn := silentConn{Conn: transport.NetConnFromWS(ws), silent: &silent, unblock: unblock}
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
				go func() { defer st.Close(); io.Copy(io.Discard, st) }()
			}
		}),
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)
	o := dev.owner()
	o.ownerID = "o_test"
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := &peer.Dialer{RelayURL: "http://" + ln.Addr().String(), Role: relayv1.Role_ROLE_OWNER, Key: key, Trust: identity.NewTrust(o.ownerID), Credentials: func() *identityv1.Credentials { return &identityv1.Credentials{} }}
	c, err := d.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	o.mu.Lock()
	o.client = c
	o.mu.Unlock()
	return dev, o, c, &silent
}

func TestDeviceReconnectPingsBeforeDroppingRelay(t *testing.T) {
	dev, _, c, silent := connectSilentRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dev.Reconnect()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-c.Done():
		t.Fatal("Reconnect dropped a healthy relay that answers Ping")
	default:
	}

	silent.Store(true)
	dev.Reconnect()
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("Reconnect kept a dead relay that failed Ping")
	}
}

func TestDevicePathChangedReactsAfterOpen(t *testing.T) {
	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)

	now := localAddresses()
	if now == "" {
		t.Skip("no local non-loopback addresses available")
	}

	// Simulate launching on a different interface address (e.g. Wi-Fi -> 5G).
	dev.nmu.Lock()
	dev.addrs = "192.0.2.10"
	dev.nmu.Unlock()
	ch := dev.networkMoved()
	dev.PathChanged()
	select {
	case <-ch:
	default:
		t.Fatal("first PathChanged did not signal network movement when addresses differed from Open")
	}

	// Simulate launching offline ("") and later gaining network addresses.
	dev.nmu.Lock()
	dev.addrs = ""
	dev.nmu.Unlock()
	ch = dev.networkMoved()
	dev.PathChanged()
	select {
	case <-ch:
	default:
		t.Fatal("PathChanged did not signal transition from empty addresses to online")
	}
}

func TestScreenCallDropsDeadRelayAfterTimeout(t *testing.T) {
	_, o, c, silent := connectSilentRelay(t)
	silent.Store(true)

	callCtx, cancelCall := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancelCall()
	_, err := screenCall(o, "m_test", screenv1connect.ScreenServiceClient.Connect)(callCtx, &screenv1.ConnectRequest{Offer: "sdp"})
	if err == nil {
		t.Fatal("expected screenCall to fail on silent relay")
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	select {
	case <-c.Done():
	case <-waitCtx.Done():
		t.Fatal("screenCall failed to drop a dead relay after call error")
	}
}

func TestScreenMachineWaitsForReconnectProbe(t *testing.T) {
	dev, o, c, silent := connectSilentRelay(t)
	silent.Store(true)
	dev.Reconnect()

	callCtx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	gotClient, _, _, err := o.screenMachine(callCtx, "m_test")
	if err == nil && gotClient == c {
		t.Fatal("screenMachine returned the dead client during an active reconnect probe")
	}
	if gotClient == c {
		t.Fatalf("screenMachine used dead client %p instead of waiting for reconnect probe to clear it (err=%v)", c, err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("expected dead client to already be closed once screenMachine returned")
	}
}

func TestDeviceNextNetworkMoveDistinguishesPathFromReconnect(t *testing.T) {
	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)

	now := localAddresses()
	if now == "" {
		t.Skip("no local non-loopback addresses available")
	}

	dev.nmu.Lock()
	dev.addrs = "192.0.2.10"
	dev.nmu.Unlock()
	ch, _ := dev.nextNetworkMove()
	dev.PathChanged()
	select {
	case <-ch:
	default:
		t.Fatal("PathChanged did not signal network movement")
	}
	ch, forced := dev.nextNetworkMove()
	if forced {
		t.Fatal("PathChanged should not mark move as forced Reconnect")
	}

	dev.Reconnect()
	select {
	case <-ch:
	default:
		t.Fatal("Reconnect did not signal network movement")
	}
	ch, forced = dev.nextNetworkMove()
	if !forced {
		t.Fatal("Reconnect should mark move as forced")
	}

	// Trailing PathChanged after Reconnect (before nextNetworkMove consumes it)
	// must keep the forced flag set.
	dev.Reconnect()
	dev.nmu.Lock()
	dev.addrs = "192.0.2.11"
	dev.nmu.Unlock()
	dev.PathChanged()
	select {
	case <-ch:
	default:
		t.Fatal("Reconnect+PathChanged did not signal network movement")
	}
	_, forced = dev.nextNetworkMove()
	if !forced {
		t.Fatal("trailing PathChanged cleared pending forced Reconnect flag")
	}
}
