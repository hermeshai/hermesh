package transport

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// A tunnel's client notices a machine that stopped answering (frozen, or
// asleep with the relay still holding its connection): the calls on the
// tunnel end and it takes no new ones. While the machine answers, an idle
// stream stays open. A plain client (the relay's control stream, which the
// relay connection's own pings watch) waits on.
func TestTunnelClientNoticesSilentPeer(t *testing.T) {
	idle, ping := tunnelIdle, tunnelPing
	tunnelIdle, tunnelPing = 100*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { tunnelIdle, tunnelPing = idle, ping })

	for _, tc := range []struct {
		name   string
		client func(net.Conn) (*H2Client, error)
		closed bool
	}{
		{"tunnel", NewTunnelClient, true},
		{"plain", NewH2Client, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// client ↔ a link that can go silent without closing ↔ server.
			clientSide, linkA := net.Pipe()
			linkB, serverSide := net.Pipe()
			var silent atomic.Bool
			pump := func(dst, src net.Conn) {
				b := make([]byte, 32<<10)
				for {
					n, err := src.Read(b)
					if n > 0 && !silent.Load() {
						if _, err := dst.Write(b[:n]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}
			go pump(linkB, linkA)
			go pump(linkA, linkB)
			t.Cleanup(func() {
				for _, c := range []net.Conn{clientSide, linkA, linkB, serverSide} {
					c.Close()
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go ServeH2(ctx, serverSide, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush() // a stream with nothing to say, as a watch
				<-r.Context().Done()
			}))

			c, err := tc.client(clientSide)
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.Get("http://peer/")
			if err != nil {
				t.Fatal(err)
			}
			ended := make(chan struct{})
			go func() {
				io.Copy(io.Discard, res.Body)
				close(ended)
			}()

			select {
			case <-ended:
				t.Fatal("an idle stream ended while the peer answered")
			case <-time.After(5 * (tunnelIdle + tunnelPing)):
			}
			if !c.Alive() {
				t.Fatal("the connection takes no new requests while the peer answers")
			}

			silent.Store(true)
			select {
			case <-ended:
				if !tc.closed {
					t.Fatal("a plain client gave up on a silent peer")
				}
				if c.Alive() {
					t.Error("the tunnel still takes new requests after its peer went silent")
				}
			case <-time.After(5 * (tunnelIdle + tunnelPing)):
				if tc.closed {
					t.Fatal("the stream is still open: the silent peer went unnoticed")
				}
			}
		})
	}
}
