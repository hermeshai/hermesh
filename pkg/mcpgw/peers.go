package mcpgw

import (
	"time"

	"github.com/hermeshai/hermesh/internal/cli"
)

// Each connection keeps its relay connection, its tunnels to machines and
// its machine list from one tool call to the next (a cli.Peer), so that a
// call reaches its machine over the tunnel the last one made: a poll costs
// one round trip to the machine, not a new tunnel and then the call. See
// docs/mcp-gateway.md §8.

// peerIdle: a connection's peer unused this long is closed. It holds the
// connection's key, so it lets it go when the key cache would.
const peerIdle = keyCacheTTL

type keptPeer struct {
	p    *cli.Peer
	used time.Time
}

// peer is the connection's peer, made on first use.
func (g *Gateway) peer(id string) *cli.Peer {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := g.peers[id]
	if k == nil {
		k = &keptPeer{p: &cli.Peer{}}
		g.peers[id] = k
	}
	k.used = g.cfg.Now()
	return k.p
}

// dropPeer closes the connection's peer.
func (g *Gateway) dropPeer(id string) {
	g.mu.Lock()
	k := g.peers[id]
	delete(g.peers, id)
	g.mu.Unlock()
	if k != nil {
		k.p.Close()
	}
}

// closeIdlePeers closes the peers unused for peerIdle, or all of them.
func (g *Gateway) closeIdlePeers(all bool) {
	now := g.cfg.Now()
	var idle []*cli.Peer
	g.mu.Lock()
	for id, k := range g.peers {
		if all || now.Sub(k.used) >= peerIdle {
			idle = append(idle, k.p)
			delete(g.peers, id)
		}
	}
	g.mu.Unlock()
	for _, p := range idle {
		p.Close()
	}
}
