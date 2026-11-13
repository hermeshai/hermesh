package owner

import (
	"context"
	"io"
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
