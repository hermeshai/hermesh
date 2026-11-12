package screen

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// blockedNet is this end as an iPhone whose owner has not yet allowed the
// app on the local network, for the tests ($HERMESH_SCREEN_BLOCK_LOCAL, in
// seconds): until then what it sends from its own addresses is dropped,
// but for what goes to the relay's servers. nil without the variable.
func blockedNet(servers []*relayv1.IceServer) transport.Net {
	secs, err := strconv.Atoi(os.Getenv("HERMESH_SCREEN_BLOCK_LOCAL"))
	if err != nil || secs <= 0 {
		return nil
	}
	std, err := stdnet.NewNet()
	if err != nil {
		return nil
	}
	relay := map[string]bool{}
	for _, s := range servers {
		for _, u := range s.Urls {
			_, hostport, _ := strings.Cut(u, ":")
			hostport, _, _ = strings.Cut(hostport, "?")
			if a, err := net.ResolveUDPAddr("udp", hostport); err == nil {
				relay[a.String()] = true
			}
		}
	}
	until := time.Now().Add(time.Duration(secs) * time.Second)
	return &blockNet{Net: std, open: func(to net.Addr) bool { return time.Now().After(until) || relay[to.String()] }}
}

type blockNet struct {
	*stdnet.Net
	open func(net.Addr) bool
}

func (n *blockNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	return &blockConn{UDPConn: c, open: n.open}, nil
}

type blockConn struct {
	transport.UDPConn
	open func(net.Addr) bool
}

func (c *blockConn) WriteTo(p []byte, to net.Addr) (int, error) {
	if !c.open(to) {
		return len(p), nil
	}
	return c.UDPConn.WriteTo(p, to)
}

func (c *blockConn) WriteToUDP(p []byte, to *net.UDPAddr) (int, error) {
	if !c.open(to) {
		return len(p), nil
	}
	return c.UDPConn.WriteToUDP(p, to)
}
