package screen

import (
	"context"
	"fmt"
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

// Of a center's several TURN servers, each end keeps the one that gives it
// a relayed address soonest, and the STUN server. One that answers and
// does not take the credential, one that answers nothing, and one that
// takes TCP alone are not it; when none gives an address, all are kept.
func TestNearestTURNServer(t *testing.T) {
	far, _ := slowTURN(t, turnAllocate, 150*time.Millisecond, net.ParseIP("127.0.0.1"))
	near, _ := slowTURN(t, turnAllocate, 0, net.ParseIP("127.0.0.1"))
	c, err := net.ListenPacket("udp4", "127.0.0.1:0") // nothing answers here
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	silent := c.LocalAddr().String()
	node := func(addr, pass string) *relayv1.IceServer {
		return &relayv1.IceServer{Urls: []string{"turn:" + addr + "?transport=udp", "turn:" + addr + "?transport=tcp"}, Username: "u", Credential: pass}
	}
	stunServer := &relayv1.IceServer{Urls: []string{"stun:" + far}}
	tls := &relayv1.IceServer{Urls: []string{"turns:" + near + "?transport=tcp"}, Username: "u", Credential: "p"}
	urls := func(list []*relayv1.IceServer) string {
		var out []string
		for _, s := range list {
			out = append(out, s.Urls[0]+" "+s.Credential)
		}
		return strings.Join(out, ", ")
	}
	old := measureWithin
	measureWithin = time.Second
	defer func() { measureWithin = old }()
	for _, c := range []struct {
		name     string
		in, want []*relayv1.IceServer
	}{
		{"the near one of two", []*relayv1.IceServer{stunServer, node(far, "p"), node(near, "p")}, []*relayv1.IceServer{stunServer, node(near, "p")}},
		{"not one that refuses the credential", []*relayv1.IceServer{stunServer, node(near, "another secret's"), node(far, "p")}, []*relayv1.IceServer{stunServer, node(far, "p")}},
		{"the one that answers", []*relayv1.IceServer{stunServer, node(silent, "p"), node(far, "p"), tls}, []*relayv1.IceServer{stunServer, node(far, "p")}},
		{"one server", []*relayv1.IceServer{stunServer, node(far, "p")}, []*relayv1.IceServer{stunServer, node(far, "p")}},
		{"none gives an address", []*relayv1.IceServer{stunServer, node(silent, "p"), tls}, []*relayv1.IceServer{stunServer, node(silent, "p"), tls}},
	} {
		if got := nearest(c.in); urls(got) != urls(c.want) {
			t.Errorf("%s: kept %s, want %s", c.name, urls(got), urls(c.want))
		}
	}
}

// A session that got no relayed address doubts the servers kept: they are
// asked for and measured again, and the TURN server that went since is not
// the one kept. The first answer is the one kept too, not all the relay gave.
func TestServersDoubted(t *testing.T) {
	a, _ := slowTURN(t, turnAllocate, 0, net.ParseIP("127.0.0.1"))
	b, _ := slowTURN(t, turnAllocate, 100*time.Millisecond, net.ParseIP("127.0.0.1"))
	var gone atomic.Bool // the first server has gone: it takes no credential
	var asked atomic.Int32
	fetch := func(context.Context) []*relayv1.IceServer {
		asked.Add(1)
		pass := "p"
		if gone.Load() {
			pass = "gone"
		}
		return []*relayv1.IceServer{
			{Urls: []string{"turn:" + a + "?transport=udp"}, Username: "u", Credential: pass},
			{Urls: []string{"turn:" + b + "?transport=udp"}, Username: "u", Credential: "p"},
		}
	}
	kept := func(list []*relayv1.IceServer) string {
		if len(list) != 1 {
			return fmt.Sprint(len(list), " servers")
		}
		return strings.TrimSuffix(strings.TrimPrefix(list[0].Urls[0], "turn:"), "?transport=udp")
	}
	var s Servers
	conn := new(int)
	s.Doubt() // nothing kept: nothing asked
	if got := kept(s.Get(context.Background(), conn, fetch)); got != a {
		t.Fatalf("kept %s at first, want the near one %s", got, a)
	}
	gone.Store(true)
	if got := kept(s.Get(context.Background(), conn, fetch)); got != a || asked.Load() != 1 {
		t.Fatalf("kept %s after %d asks, want the one kept and no other ask", got, asked.Load())
	}
	s.Doubt()
	deadline := time.Now().Add(5 * time.Second)
	for kept(s.Get(context.Background(), conn, fetch)) != b {
		if time.Now().After(deadline) {
			t.Fatalf("still %s after the doubt, want %s", kept(s.Get(context.Background(), conn, fetch)), b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
