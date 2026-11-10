package relay

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pion/turn/v5"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/metrics"
)

// STUN and TURN for Computer Control (docs/computer-control.md §7): the
// owner's device and a machine find a direct path with STUN, and where
// there is none this relay forwards their packets (TURN). The packets are
// sealed between the two ends (DTLS and SRTP, their fingerprints agreed in
// the end-to-end tunnel): the relay forwards bytes it can't read, as it
// does for tunnels.

// TURNConfig turns the STUN and TURN server on.
type TURNConfig struct {
	// Listen is the address it takes UDP and TCP on, ":3478".
	Listen string
	// Host is the name or address the owners' devices and machines reach
	// this relay by; its address is also where forwarded packets leave from.
	Host string
}

// turnTTL: how long a credential lasts. A session asks for one as it starts.
const turnTTL = time.Hour

type turnServer struct {
	srv    *turn.Server
	secret string
	host   string // host:port, as in the URLs
}

var mTURNBytes = metrics.Default.Counter("hermesh_relay_turn_bytes_total")

// countingConn counts what passes the TURN port, both ways.
type countingConn struct{ net.PacketConn }

func (c countingConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(p)
	mTURNBytes.Add(int64(n))
	return n, a, err
}

func (c countingConn) WriteTo(p []byte, a net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, a)
	mTURNBytes.Add(int64(n))
	return n, err
}

func startTURN(cfg TURNConfig) (*turnServer, error) {
	if cfg.Host == "" {
		return nil, errors.New("TURN needs the host its users reach it by")
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("TURN listen address: %w", err)
	}
	ips, err := net.LookupIP(cfg.Host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("TURN host %q: %v", cfg.Host, err)
	}
	ip := ips[0]
	for _, c := range ips { // packets leave over IPv4 where the host has both
		if c.To4() != nil {
			ip = c
			break
		}
	}
	udp, err := net.ListenPacket("udp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		udp.Close()
		return nil, err
	}
	b := make([]byte, 32)
	rand.Read(b)
	t := &turnServer{secret: hex.EncodeToString(b), host: net.JoinHostPort(cfg.Host, port)}
	gen := &turn.RelayAddressGeneratorStatic{RelayAddress: ip, Address: "0.0.0.0"}
	if ip.To4() == nil {
		gen.Address = "::"
	}
	// A relay on the internet forwards to public addresses only (and to its
	// own, where both ends' packets meet): never into the network it is in.
	// One on a private network, or the tests' on this computer, serves that.
	private := func(a net.IP) bool {
		return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified()
	}
	t.srv, err = turn.NewServer(turn.ServerConfig{
		Realm:       "hermesh",
		AuthHandler: turn.LongTermTURNRESTAuthHandler(t.secret, nil),
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: countingConn{udp}, RelayAddressGenerator: gen,
			PermissionHandler: func(_ net.Addr, peer net.IP) bool { return peer.Equal(ip) || !private(peer) || private(ip) }}},
		ListenerConfigs: []turn.ListenerConfig{{Listener: tcp, RelayAddressGenerator: gen,
			PermissionHandler: func(_ net.Addr, peer net.IP) bool { return peer.Equal(ip) || !private(peer) || private(ip) }}},
	})
	if err != nil {
		udp.Close()
		tcp.Close()
		return nil, err
	}
	return t, nil
}

// servers are the URLs and a fresh credential for one admitted peer.
func (t *turnServer) servers(subject string) []*relayv1.IceServer {
	if t == nil {
		return nil
	}
	user, pass, err := turn.GenerateLongTermTURNRESTCredentials(t.secret, subject, turnTTL)
	if err != nil {
		return nil
	}
	return []*relayv1.IceServer{
		{Urls: []string{"stun:" + t.host}},
		{Urls: []string{"turn:" + t.host + "?transport=udp", "turn:" + t.host + "?transport=tcp"}, Username: user, Credential: pass},
	}
}
