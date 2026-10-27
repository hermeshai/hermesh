package relay

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// frozen holds back what it read while set, as a suspended process reads
// nothing (its kernel still takes the bytes in).
type frozen struct {
	net.Conn
	set atomic.Bool
}

func (f *frozen) Read(p []byte) (int, error) {
	n, err := f.Conn.Read(p)
	for f.set.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	return n, err
}

// keptAlive connects a yamux client to a server kept alive with quiet, the
// client frozen for frozenFor, and reports whether the server's session
// is still open a while after it thawed.
func keptAlive(t *testing.T, frozenFor, quiet time.Duration) bool {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.ConnectionWriteTimeout = 100 * time.Millisecond
	cfg.LogOutput = io.Discard
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	cc, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	f := &frozen{Conn: cc}
	client, err := yamux.Client(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(<-accepted, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go keepAlive(server, 20*time.Millisecond, quiet)
	time.Sleep(100 * time.Millisecond)
	f.set.Store(true)
	time.Sleep(frozenFor)
	f.set.Store(false)
	time.Sleep(300 * time.Millisecond)
	return !server.IsClosed()
}

// An owner device that answers no ping for a while (the iPhone app
// suspended) keeps its connection until quiet went by, and no longer.
func TestKeepAlive(t *testing.T) {
	if !keptAlive(t, 500*time.Millisecond, 2*time.Second) {
		t.Error("closed after a pause shorter than quiet")
	}
	if keptAlive(t, 1500*time.Millisecond, 500*time.Millisecond) {
		t.Error("kept after a pause longer than quiet")
	}
}
