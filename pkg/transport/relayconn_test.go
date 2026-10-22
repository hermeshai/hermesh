package transport

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
)

type grants struct {
	relayv1connect.UnimplementedClientControlServiceHandler
}

func (grants) GetLatestGrant(context.Context, *relayv1.GetLatestGrantRequest) (*relayv1.GetLatestGrantResponse, error) {
	return &relayv1.GetLatestGrantResponse{}, nil
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// fakeRelay answers Hello with ok after checking whether the peer already
// sent more (pipelined reports it), then serves the control stream.
func fakeRelay(t *testing.T, ok bool, pipelined chan<- bool) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := NetConnFromWS(ws)
		defer conn.Close()
		WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "r"})
		br := bufio.NewReader(conn)
		var hello relayv1.Hello
		if err := ReadFrame(br, &hello); err != nil {
			return
		}
		more := make(chan bool, 1)
		go func() { _, err := br.Peek(1); more <- err == nil }()
		early := false
		select {
		case early = <-more:
		case <-time.After(500 * time.Millisecond):
		}
		pipelined <- early
		if !ok {
			WriteFrame(conn, &relayv1.HelloResult{Error: "revoked"})
			return
		}
		WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		if !early {
			<-more // br is read by one goroutine at a time
		}
		sess, err := yamux.Server(bufConn{conn, br}, YamuxConfig())
		if err != nil {
			return
		}
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		var h relayv1.StreamHeader
		ReadFrame(st, &h)
		mux := http.NewServeMux()
		mux.Handle(relayv1connect.NewClientControlServiceHandler(grants{}))
		ServeH2(context.Background(), st, mux)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func dialer(t *testing.T, url string, pipeline bool) *Dialer {
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return &Dialer{RelayURL: url, Role: relayv1.Role_ROLE_CLIENT, Key: key, Pipeline: pipeline,
		Credentials: func() *identityv1.Credentials { return &identityv1.Credentials{} }}
}

func latestGrant(c *RelayConn) error {
	hc, err := c.Control()
	if err != nil {
		return err
	}
	_, err = relayv1connect.NewClientControlServiceClient(hc, "http://relay", ClientNoCompression).
		GetLatestGrant(context.Background(), &relayv1.GetLatestGrantRequest{})
	return err
}

func TestPipelinedDial(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name          string
		ok, pipeline  bool
		wantPipelined bool
	}{
		{"pipelined, admitted", true, true, true},
		{"pipelined, refused", false, true, true},
		{"waits, admitted", true, false, false},
		{"waits, refused", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pipelined := make(chan bool, 1)
			d := dialer(t, fakeRelay(t, c.ok, pipelined), c.pipeline)
			conn, err := d.Dial(ctx)
			if !c.pipeline && !c.ok {
				// Without pipelining, Dial itself reports the refusal.
				if !errors.Is(err, ErrAdmission) {
					t.Fatalf("dial: %v, want ErrAdmission", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// The control call is what goes out ahead of the answer.
			err = latestGrant(conn)
			if got := <-pipelined; got != c.wantPipelined {
				t.Fatalf("request sent before the relay answered Hello: %v, want %v", got, c.wantPipelined)
			}
			switch {
			case c.ok && err != nil:
				t.Fatalf("call: %v", err)
			case !c.ok && !errors.Is(err, ErrAdmission):
				t.Fatalf("call on a refused connection: %v, want ErrAdmission", err)
			case !c.ok:
				if aerr := conn.Admitted(ctx); !errors.Is(aerr, ErrAdmission) {
					t.Fatalf("admitted: %v", aerr)
				}
			}
		})
	}
}
