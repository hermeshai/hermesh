package peer

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
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

type herCalls struct {
	herv1connect.UnimplementedHerServiceHandler
}

func (herCalls) Call(_ context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	return &herv1.CallResponse{Response: &herv1.Response{Result: []byte(strconv.Quote(req.Request.Method))}}, nil
}

// A machine's Her service and its console's plain HTTP go over the tunnel's
// HTTP/2 connection, to the machine's own routes.
func TestMachineHer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle(herv1connect.NewHerServiceHandler(herCalls{}))
	mux.HandleFunc("/her/console/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "console at "+r.URL.Path) })
	machineSide, ownerSide := net.Pipe()
	go transport.ServeH2(ctx, machineSide, mux)
	h2, err := transport.NewH2Client(ownerSide)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	n := newMachine("m_1", "box", h2)
	res, err := n.Her.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
	if err != nil || string(res.Response.Result) != `"models/list"` {
		t.Fatalf("Her.Call: %v %v", res, err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://machine/her/console/index.html", nil)
	resp, err := n.HTTP().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != "console at /her/console/index.html" {
		t.Fatalf("console: %d %q", resp.StatusCode, b)
	}
}

// A client with a link reaches the relay's control services and its
// machines through it, with no connection of its own, and holds a machine
// to the name the link says it presented. Once the link fails the client
// connects by itself, as one without.
func TestLink(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The link's end: what it carries to, by where the command asked for.
	mux := http.NewServeMux()
	mux.Handle(herv1connect.NewHerServiceHandler(herCalls{}))
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "the relay, through the link") })
	var asked []string
	fails := false
	key, _ := identity.GenerateKey()
	d := &Dialer{RelayURL: admittingRelay(t), Role: relayv1.Role_ROLE_CLIENT, Key: key,
		Credentials: func() *identityv1.Credentials { return &identityv1.Credentials{} },
		Link: func(_ context.Context, machineID string) (net.Conn, string, error) {
			asked = append(asked, machineID)
			if fails {
				return nil, "", io.ErrClosedPipe
			}
			linkSide, commandSide := net.Pipe()
			go transport.ServeH2(ctx, linkSide, mux)
			return commandSide, "box", nil
		}}
	c, err := d.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc, err := c.Control()
	if err != nil || !c.Linked() {
		t.Fatalf("control: %v (linked: %v)", err, c.Linked())
	}
	res, err := hc.Get("http://relay/whoami")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "the relay, through the link" {
		t.Fatalf("control call: %q", b)
	}
	n, err := c.Machine(ctx, "m_1")
	if err != nil || n.Name != "box" {
		t.Fatalf("machine: %+v %v", n, err)
	}
	if res, err := n.Her.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}}); err != nil || string(res.Response.Result) != `"models/list"` {
		t.Fatalf("a call to the machine: %v %v", res, err)
	}
	if again, _ := c.Machine(ctx, "m_1"); again != n {
		t.Fatal("the machine was reached anew")
	}
	if _, err := c.Control(); err != nil || len(asked) != 2 {
		t.Fatalf("the link was asked %v, want once for the relay and once for the machine (%v)", asked, err)
	}
	select {
	case <-c.Done():
		t.Fatal("a client with a link has a connection of its own")
	default:
	}

	// The link fails: the client connects to the relay itself.
	fails = true
	brief, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err := c.Machine(brief, "m_2"); err == nil { // the stand-in relay has no machines
		t.Fatal("reached a machine nobody has")
	}
	if c.Linked() {
		t.Fatal("still linked after the link failed")
	}
	if _, err := c.Control(); err != nil {
		t.Fatalf("control, by itself: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("the client's own connection: %v", err)
	}
	if got, _ := c.Machine(ctx, "m_1"); got != n {
		t.Fatal("the machine reached through the link was dropped")
	}
}
