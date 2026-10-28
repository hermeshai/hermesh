package peer

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// admittingRelay admits every peer and keeps its connection open.
func admittingRelay(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "r"})
		br := bufio.NewReader(conn)
		var hello relayv1.Hello
		if err := transport.ReadFrame(br, &hello); err != nil {
			return
		}
		transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		sess, err := yamux.Server(bufConn{conn, br}, transport.YamuxConfig())
		if err != nil {
			return
		}
		<-sess.CloseChan()
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A client shared by several users stays open until the last of them
// closes it.
func TestHold(t *testing.T) {
	key, _ := identity.GenerateKey()
	d := &Dialer{RelayURL: admittingRelay(t), Role: relayv1.Role_ROLE_CLIENT, Key: key,
		Credentials: func() *identityv1.Credentials { return &identityv1.Credentials{} }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := d.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Hold()
	c.Hold()
	for range 2 {
		c.Close()
		select {
		case <-c.Done():
			t.Fatal("closed while held")
		default:
		}
		if err := c.Ping(ctx); err != nil {
			t.Fatalf("ping while held: %v", err)
		}
	}
	c.Close()
	select {
	case <-c.Done():
	default:
		t.Fatal("the last Close must close it")
	}
}
