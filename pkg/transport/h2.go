package transport

import (
	"context"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
)

// ServeH2 serves HTTP/2 (prior knowledge) on an established connection
// until it closes. ctx becomes the base context of every request.
func ServeH2(ctx context.Context, c net.Conn, h http.Handler) {
	// One tunnel carries every concurrent call of an agent (event streams
	// stay open for a whole turn), so allow far more than the default 250.
	srv := &http2.Server{MaxConcurrentStreams: 4096}
	srv.ServeConn(c, &http2.ServeConnOpts{Context: ctx, Handler: h})
}

// H2Client is an http.Client bound to a single established connection.
type H2Client struct {
	*http.Client
	cc   *http2.ClientConn
	conn net.Conn
}

// NewH2Client starts an HTTP/2 client on c. Requests must use a base URL
// like "http://peer" or "https://peer"; the host is ignored.
func NewH2Client(c net.Conn) (*H2Client, error) {
	t := &http2.Transport{AllowHTTP: true}
	cc, err := t.NewClientConn(c)
	if err != nil {
		return nil, err
	}
	return &H2Client{Client: &http.Client{Transport: cc}, cc: cc, conn: c}, nil
}

func (c *H2Client) Close() error {
	c.cc.Close()
	return c.conn.Close()
}

// Alive reports whether the connection can take new requests.
func (c *H2Client) Alive() bool { return c.cc.CanTakeNewRequest() }

// ConnListener turns pushed connections into a net.Listener.
type ConnListener struct {
	ch     chan net.Conn
	once   sync.Once
	closed chan struct{}
	addr   net.Addr
}

func NewConnListener() *ConnListener {
	return &ConnListener{ch: make(chan net.Conn), closed: make(chan struct{}), addr: dummyAddr("tunnel")}
}

func (l *ConnListener) Push(c net.Conn) error {
	select {
	case l.ch <- c:
		return nil
	case <-l.closed:
		return net.ErrClosed
	}
}

func (l *ConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *ConnListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *ConnListener) Addr() net.Addr { return l.addr }

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }
