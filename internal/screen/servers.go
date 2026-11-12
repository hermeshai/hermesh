package screen

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v5"

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
	// fetch asks the relay over that connection, for Doubt.
	fetch func(context.Context) []*relayv1.IceServer
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
			s.fetch = fetch
			if age >= ServersFresh {
				s.refreshLocked(key, fetch)
			}
			s.mu.Unlock()
			return list
		}
	}
	s.mu.Unlock()
	return s.put(key, fetch(ctx), fetch)
}

// Doubt forgets the servers kept and asks for them again: a session got no
// relayed address through them. The TURN server kept, of several, may have
// gone since it was measured; asked again, it is measured again, and
// another is kept.
func (s *Servers) Doubt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.list == nil || s.fetch == nil {
		return
	}
	s.list = nil
	s.refreshLocked(s.key, s.fetch)
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
		s.put(key, list, fetch)
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
}

// put keeps the servers the relay gave, the nearest TURN server of several,
// and returns what it kept.
func (s *Servers) put(key any, list []*relayv1.IceServer, fetch func(context.Context) []*relayv1.IceServer) []*relayv1.IceServer {
	if len(list) == 0 {
		return list
	}
	list = nearest(list)
	s.mu.Lock()
	s.key, s.list, s.at, s.fetch = key, list, time.Now(), fetch
	s.mu.Unlock()
	return list
}

// measureWithin: how long a TURN server has to give a relayed address to
// be measured (two round trips to it; a variable for the tests).
var measureWithin = 2 * time.Second

// nearest keeps, of a center's several TURN servers, the one that gives this
// end a relayed address soonest: each end relays through the server near
// it, and the two servers pass the packets on. All of them would mean an
// allocation on each for every session, and every pair of them checked for
// as long as it lasts. A server is measured by what a session needs of it,
// an allocation with the credential over UDP, so one that answers and
// cannot relay (it is down, or has another secret) is never the one kept.
// With one server, or none that gives an address (a network that blocks
// UDP: the checks then find which works, TLS on 443 among them), the list
// stays as it is. A server's every transport belongs in its one entry.
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
		i    int
		took time.Duration
	}
	answers := make(chan answer, len(turns))
	for _, i := range turns {
		go func() { answers <- answer{i, allocationTime(list[i])} }()
	}
	best := answer{i: -1}
	for range turns {
		if a := <-answers; a.took > 0 && (best.i < 0 || a.took < best.took) {
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

// allocationTime is how long a TURN server takes to give a relayed address
// over UDP, which is then given back; 0 when it takes no UDP, gives none,
// or takes longer than measureWithin.
func allocationTime(server *relayv1.IceServer) time.Duration {
	for _, u := range server.Urls {
		scheme, rest, _ := strings.Cut(u, ":")
		hostport, query, _ := strings.Cut(rest, "?")
		if scheme != "turn" || strings.Contains(query, "transport=tcp") {
			continue
		}
		if _, _, err := net.SplitHostPort(hostport); err != nil {
			hostport = net.JoinHostPort(hostport, "3478")
		}
		addr, err := net.ResolveUDPAddr("udp", hostport)
		if err != nil {
			return 0
		}
		network := "udp4"
		if addr.IP.To4() == nil {
			network = "udp6"
		}
		conn, err := net.ListenPacket(network, ":0")
		if err != nil {
			return 0
		}
		c, err := turn.NewClient(&turn.ClientConfig{STUNServerAddr: addr.String(), TURNServerAddr: addr.String(), Conn: conn,
			Username: server.Username, Password: server.Credential})
		if err != nil {
			conn.Close()
			return 0
		}
		took := make(chan time.Duration, 1)
		go func() {
			defer conn.Close()
			defer c.Close()
			if c.Listen() != nil {
				took <- 0
				return
			}
			start := time.Now()
			relayed, err := c.Allocate()
			if err != nil {
				took <- 0
				return
			}
			took <- time.Since(start)
			relayed.Close() // gives the address back
		}()
		select {
		case d := <-took:
			return d
		case <-time.After(measureWithin):
			conn.Close() // ends the allocation that waits
			return 0
		}
	}
	return 0
}
