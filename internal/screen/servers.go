package screen

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun/v4"

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
	list = nearest(list)
	s.mu.Lock()
	s.key, s.list, s.at = key, list, time.Now()
	s.mu.Unlock()
}

// measureWithin: how long a TURN server has to answer to be measured.
const measureWithin = time.Second

// nearest keeps, of a center's several TURN servers, the one that answers
// this end soonest: each end relays through the server near it, and the two
// servers pass the packets on. All of them would mean an allocation on each
// for every session, and every pair of them checked for as long as it
// lasts. With one server, or none that answers over UDP (a network that
// blocks it: the checks then find which works), the list stays as it is.
func nearest(list []*relayv1.IceServer) []*relayv1.IceServer {
	var turns []int
	for i, s := range list {
		if s.Username != "" {
			turns = append(turns, i)
		}
	}
	if len(turns) < 2 {
		return list
	}
	type answer struct {
		i   int
		rtt time.Duration
	}
	answers := make(chan answer, len(turns))
	for _, i := range turns {
		go func() { answers <- answer{i, stunRoundTrip(list[i].Urls)} }()
	}
	best := answer{i: -1}
	for range turns {
		if a := <-answers; a.rtt > 0 && (best.i < 0 || a.rtt < best.rtt) {
			best = a
		}
	}
	if best.i < 0 {
		return list
	}
	var out []*relayv1.IceServer
	for i, s := range list {
		if s.Username == "" || i == best.i {
			out = append(out, s)
		}
	}
	return out
}

// stunRoundTrip is how long the first of urls that takes UDP takes to
// answer a STUN request (a TURN server answers those too); 0 when there is
// none, or no answer in measureWithin.
func stunRoundTrip(urls []string) time.Duration {
	for _, u := range urls {
		scheme, rest, _ := strings.Cut(u, ":")
		hostport, query, _ := strings.Cut(rest, "?")
		if scheme != "stun" && scheme != "turn" || strings.Contains(query, "transport=tcp") {
			continue
		}
		if _, _, err := net.SplitHostPort(hostport); err != nil {
			hostport = net.JoinHostPort(hostport, "3478")
		}
		c, err := net.DialTimeout("udp", hostport, measureWithin)
		if err != nil {
			return 0
		}
		defer c.Close()
		req, err := stun.Build(stun.TransactionID, stun.BindingRequest)
		if err != nil {
			return 0
		}
		start := time.Now()
		c.SetDeadline(start.Add(measureWithin))
		if _, err := c.Write(req.Raw); err != nil {
			return 0
		}
		b := make([]byte, 1500)
		for {
			n, err := c.Read(b)
			if err != nil {
				return 0
			}
			if n >= 20 && bytes.Equal(b[8:20], req.TransactionID[:]) {
				return time.Since(start)
			}
		}
	}
	return 0
}
