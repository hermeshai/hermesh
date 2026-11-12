package screen

import (
	"context"
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
