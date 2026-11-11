package screen

import (
	"context"
	"sync"
	"time"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// Servers keeps the relay's STUN and TURN servers between sessions. Asking
// for them is a round trip to the relay before a session can start, and
// their credentials last an hour (pkg/relay/turn.go), so each end asks
// ahead of time and keeps the answer.
type Servers struct {
	mu   sync.Mutex
	key  any // the relay connection they came over
	list []*relayv1.IceServer
	at   time.Time
	busy bool
}

const (
	// ServersFresh: kept servers older than this are asked for again in the
	// background, and still used meanwhile.
	ServersFresh = 20 * time.Minute
	// serversKeep: past this they are not used; the credentials would
	// expire during a session.
	serversKeep = 40 * time.Minute
)

// Get returns the servers kept for the connection key, or asks with fetch
// when none are (or they are too old). An empty answer is not kept: the
// relay runs none, or did not answer.
func (s *Servers) Get(ctx context.Context, key any, fetch func(context.Context) []*relayv1.IceServer) []*relayv1.IceServer {
	s.mu.Lock()
	if s.key == key && s.list != nil {
		age := time.Since(s.at)
		if age < serversKeep {
			list := s.list
			if age >= ServersFresh {
				s.refreshLocked(key, fetch)
			}
			s.mu.Unlock()
			return list
		}
	}
	s.mu.Unlock()
	list := fetch(ctx)
	s.put(key, list)
	return list
}

// Warm asks in the background, unless fresh servers are kept for key.
func (s *Servers) Warm(key any, fetch func(context.Context) []*relayv1.IceServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == key && s.list != nil && time.Since(s.at) < ServersFresh {
		return
	}
	s.refreshLocked(key, fetch)
}

func (s *Servers) refreshLocked(key any, fetch func(context.Context) []*relayv1.IceServer) {
	if s.busy {
		return
	}
	s.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		list := fetch(ctx)
		cancel()
		s.put(key, list)
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
}

func (s *Servers) put(key any, list []*relayv1.IceServer) {
	if len(list) == 0 {
		return
	}
	s.mu.Lock()
	s.key, s.list, s.at = key, list, time.Now()
	s.mu.Unlock()
}
