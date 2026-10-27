package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

const ProtocolVersion = 1

// AdmitMessage is what a peer signs to prove key possession at admission.
func AdmitMessage(relayID string, nonce []byte, role relayv1.Role) []byte {
	m := []byte("hermesh-admit-v1\x00" + relayID + "\x00")
	m = append(m, nonce...)
	return append(m, 0, byte(role))
}

func YamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.KeepAliveInterval = 15 * time.Second
	c.ConnectionWriteTimeout = 30 * time.Second
	c.MaxStreamWindowSize = 1 << 20
	c.LogOutput = io.Discard
	return c
}

// WebSocketURL maps a relay base URL (http/https) to its connect endpoint.
func WebSocketURL(relayURL, path string) (string, error) {
	u, err := url.Parse(relayURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("relay url must be http(s): %q", relayURL)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	return u.String(), nil
}

// NetConnFromWS adapts an accepted or dialed websocket to a net.Conn.
func NetConnFromWS(c *websocket.Conn) net.Conn {
	c.SetReadLimit(4 << 20)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary)
}

// Dialer connects a machine, client or owner device to the relay.
type Dialer struct {
	RelayURL    string
	Role        relayv1.Role
	Key         identity.Signer
	Credentials CredentialsFunc
	HTTPClient  *http.Client
	// Pipeline makes Dial return once Hello is sent, so the first calls go
	// out with it instead of a round trip later; a refusal then fails them
	// with ErrAdmission.
	Pipeline bool
}

// RelayConn is a multiplexed connection to the relay.
type RelayConn struct {
	sess  *yamux.Session
	admit *admission

	mu      sync.Mutex
	control *H2Client
}

var ErrAdmission = errors.New("relay refused admission")

// Probe opens a WebSocket to the relay and reads its challenge, then
// closes: what Dial needs from the network, without being admitted. Some
// networks pass plain HTTPS but not WebSockets.
func Probe(ctx context.Context, relayURL string, hc *http.Client) error {
	wsURL, err := WebSocketURL(relayURL, "/v1/connect")
	if err != nil {
		return err
	}
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: hc, Subprotocols: []string{"hermesh.v1"}})
	if err != nil {
		return err
	}
	conn := NetConnFromWS(ws)
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	var ch relayv1.Challenge
	return ReadFrame(conn, &ch)
}

func (d *Dialer) Dial(ctx context.Context) (*RelayConn, error) {
	wsURL, err := WebSocketURL(d.RelayURL, "/v1/connect")
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPClient: d.HTTPClient, Subprotocols: []string{"hermesh.v1"}})
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	conn := NetConnFromWS(ws)
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	var ch relayv1.Challenge
	if err := ReadFrame(conn, &ch); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read challenge: %w", err)
	}
	proof, err := d.Key.SignMessage(AdmitMessage(ch.RelayId, ch.Nonce, d.Role))
	if err != nil {
		conn.Close()
		return nil, err
	}
	hello := &relayv1.Hello{Role: d.Role, Credentials: d.Credentials(), Proof: proof, ProtocolVersion: ProtocolVersion}
	if err := WriteFrame(conn, hello); err != nil {
		conn.Close()
		return nil, err
	}
	// The relay reads nothing past Hello until it admits the peer, so the
	// session may start writing before the answer.
	admit := &admission{Conn: conn, done: make(chan struct{})}
	sess, err := yamux.Client(admit, YamuxConfig())
	if err != nil {
		conn.Close()
		return nil, err
	}
	c := &RelayConn{sess: sess, admit: admit}
	if !d.Pipeline {
		if err := c.Admitted(dctx); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

// admission holds back the session's reads until the relay's HelloResult
// is read, and fails them when it refused the peer.
type admission struct {
	net.Conn
	once sync.Once
	done chan struct{}
	err  error
}

func (a *admission) Read(p []byte) (int, error) {
	a.once.Do(func() {
		defer close(a.done)
		var res relayv1.HelloResult
		switch err := ReadFrame(a.Conn, &res); {
		case err != nil:
			a.err = fmt.Errorf("read hello result: %w", err)
		case !res.Ok:
			a.err = fmt.Errorf("%w: %s", ErrAdmission, res.Error)
		default:
			a.Conn.SetDeadline(time.Time{})
		}
	})
	if a.err != nil {
		return 0, a.err
	}
	return a.Conn.Read(p)
}

// Admitted waits for the relay's answer to Hello.
func (c *RelayConn) Admitted(ctx context.Context) error {
	select {
	case <-c.admit.done:
		return c.admit.err
	case <-c.sess.CloseChan():
		return c.Explain(yamux.ErrSessionShutdown)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Explain returns why the relay refused the connection in place of err, the
// failure of a call sent ahead of its answer.
func (c *RelayConn) Explain(err error) error {
	if c.sess.IsClosed() {
		<-c.admit.done // with the connection closed, reading the answer ends at once
	}
	select {
	case <-c.admit.done:
		if c.admit.err != nil {
			return c.admit.err
		}
	default:
	}
	return err
}

// Control returns the HTTP/2 client for this connection's control stream.
func (c *RelayConn) Control() (*H2Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.control != nil && c.control.Alive() {
		return c.control, nil
	}
	s, err := c.openStream(&relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_CONTROL})
	if err != nil {
		return nil, err
	}
	hc, err := NewH2Client(s)
	if err != nil {
		s.Close()
		return nil, c.Explain(err)
	}
	hc.Client.Transport = explaining{hc.Client.Transport, c}
	c.control = hc
	return hc, nil
}

// explaining fails control calls with the admission error once the relay
// refused the connection.
type explaining struct {
	http.RoundTripper
	c *RelayConn
}

func (t explaining) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.RoundTripper.RoundTrip(r)
	if err != nil {
		return nil, t.c.Explain(err)
	}
	return res, nil
}

// OpenTunnel opens a raw byte stream to a machine through the relay.
func (c *RelayConn) OpenTunnel(machineID string) (net.Conn, error) {
	return c.openStream(&relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: machineID})
}

func (c *RelayConn) openStream(h *relayv1.StreamHeader) (net.Conn, error) {
	s, err := c.sess.OpenStream()
	if err != nil {
		return nil, c.Explain(err)
	}
	if err := WriteFrame(s, h); err != nil {
		s.Close()
		return nil, c.Explain(err)
	}
	return s, nil
}

// AcceptStream waits for a stream opened by the relay (tunnels to a machine).
func (c *RelayConn) AcceptStream() (net.Conn, *relayv1.StreamHeader, error) {
	s, err := c.sess.AcceptStream()
	if err != nil {
		return nil, nil, err
	}
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	var h relayv1.StreamHeader
	if err := ReadFrame(s, &h); err != nil {
		s.Close()
		return nil, nil, err
	}
	s.SetReadDeadline(time.Time{})
	return s, &h, nil
}

func (c *RelayConn) Close() error {
	c.mu.Lock()
	if c.control != nil {
		c.control.Close()
	}
	c.mu.Unlock()
	return c.sess.Close()
}

func (c *RelayConn) Done() <-chan struct{} { return c.sess.CloseChan() }

// Ping waits for the relay to answer a ping: whether the connection still
// works, which one kept while its process was suspended may not.
func (c *RelayConn) Ping(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		_, err := c.sess.Ping()
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *RelayConn) IsClosed() bool { return c.sess.IsClosed() }
