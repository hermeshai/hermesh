package screen

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

func TestServers(t *testing.T) {
	var asked atomic.Int32
	answer := []*relayv1.IceServer{{Urls: []string{"stun:relay"}}}
	fetch := func(context.Context) []*relayv1.IceServer { asked.Add(1); return answer }
	none := func(context.Context) []*relayv1.IceServer { asked.Add(1); return nil }
	ctx := context.Background()
	connA, connB := new(int), new(int)

	var s Servers
	// Warmed ahead: the session that follows does not ask.
	s.Warm(connA, fetch)
	deadline := time.Now().Add(5 * time.Second)
	for asked.Load() == 0 || s.Get(ctx, connA, none) == nil {
		if time.Now().After(deadline) {
			t.Fatal("warming never asked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Get(ctx, connA, none); len(got) != 1 || asked.Load() != 1 {
		t.Fatalf("got %v after %d asks, want the warmed answer and no other ask", got, asked.Load())
	}
	// Fresh: warming again does not ask.
	s.Warm(connA, fetch)
	time.Sleep(50 * time.Millisecond)
	if asked.Load() != 1 {
		t.Fatalf("asked %d times, want 1", asked.Load())
	}

	// Another relay connection: asked again.
	if got := s.Get(ctx, connB, fetch); len(got) != 1 || asked.Load() != 2 {
		t.Fatalf("got %v after %d asks on another connection", got, asked.Load())
	}

	// Old: used meanwhile, and asked again in the background.
	s.mu.Lock()
	s.at = time.Now().Add(-ServersFresh - time.Minute)
	s.mu.Unlock()
	if got := s.Get(ctx, connB, fetch); len(got) != 1 {
		t.Fatalf("got %v from old servers", got)
	}
	eventually := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	eventually("old servers asked for again", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return asked.Load() == 3 && time.Since(s.at) < time.Minute
	})

	// Too old to use: asked while the session waits.
	s.mu.Lock()
	s.at = time.Now().Add(-serversKeep)
	s.mu.Unlock()
	if s.Get(ctx, connB, fetch); asked.Load() != 4 {
		t.Fatalf("asked %d times, want 4", asked.Load())
	}

	// An empty answer is not kept: the next session asks.
	var e Servers
	e.Get(ctx, connA, none)
	if e.Get(ctx, connA, fetch); asked.Load() != 6 {
		t.Fatalf("asked %d times, want 6", asked.Load())
	}
}

// stunAnswering is a UDP socket that answers STUN requests after delay,
// as a TURN server does; with a negative delay it answers nothing.
func stunAnswering(t *testing.T, delay time.Duration) string {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		b := make([]byte, 1500)
		for {
			n, from, err := c.ReadFrom(b)
			if err != nil {
				return
			}
			if delay < 0 || n < 20 {
				continue
			}
			res := append([]byte(nil), b[:n]...)
			res[0], res[1] = 0x01, 0x01 // a Binding success, with the request's transaction
			go func() {
				time.Sleep(delay)
				c.WriteTo(res, from)
			}()
		}
	}()
	return c.LocalAddr().String()
}

// Of a center's several TURN servers, each end keeps the one that answers
// it soonest, and the STUN server. One that takes TCP alone is not
// measured; when none answers, all are kept.
func TestNearestTURNServer(t *testing.T) {
	far, near, silent := stunAnswering(t, 150*time.Millisecond), stunAnswering(t, 0), stunAnswering(t, -1)
	node := func(addr string) *relayv1.IceServer {
		return &relayv1.IceServer{Urls: []string{"turn:" + addr + "?transport=udp", "turn:" + addr + "?transport=tcp"}, Username: "u", Credential: "p"}
	}
	stunServer := &relayv1.IceServer{Urls: []string{"stun:" + far}}
	tls := &relayv1.IceServer{Urls: []string{"turns:" + near + "?transport=tcp"}, Username: "u", Credential: "p"}
	urls := func(list []*relayv1.IceServer) string {
		var out []string
		for _, s := range list {
			out = append(out, s.Urls[0])
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name     string
		in, want []*relayv1.IceServer
	}{
		{"the near one of two", []*relayv1.IceServer{stunServer, node(far), node(near)}, []*relayv1.IceServer{stunServer, node(near)}},
		{"the one that answers", []*relayv1.IceServer{stunServer, node(silent), node(far), tls}, []*relayv1.IceServer{stunServer, node(far)}},
		{"one server", []*relayv1.IceServer{stunServer, node(far)}, []*relayv1.IceServer{stunServer, node(far)}},
		{"none answers", []*relayv1.IceServer{stunServer, node(silent), tls}, []*relayv1.IceServer{stunServer, node(silent), tls}},
	} {
		if got := nearest(c.in); urls(got) != urls(c.want) {
			t.Errorf("%s: %s, want %s", c.name, urls(got), urls(c.want))
		}
	}
}
