package relay

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
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
	// Listen is the address it takes UDP and TCP on, ":3478". Empty: this
	// relay runs none, and hands out credentials for the one at Host, which
	// another node of the same center runs with the same Secret (a host with
	// one public address can give its STUN and TURN port to one node only).
	Listen string
	// Host is the name or address the owners' devices and machines reach
	// the server by, with its port when Listen is empty; its address is
	// also where forwarded packets leave from.
	Host string
	// Secret signs the credentials. The nodes of a center share it, so
	// that any node hands out credentials the one serving takes. Empty: a
	// random one, for a relay that is one process.
	Secret []byte
	// MinPort and MaxPort bound the UDP ports a forwarded session takes
	// (one each), so that a firewall opens those alone. 0: any port.
	MinPort, MaxPort uint16
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
	secret := hex.EncodeToString(cfg.Secret)
	if len(cfg.Secret) == 0 {
		b := make([]byte, 32)
		rand.Read(b)
		secret = hex.EncodeToString(b)
	}
	if cfg.Listen == "" { // another node serves
		if _, _, err := net.SplitHostPort(cfg.Host); err != nil {
			return nil, fmt.Errorf("TURN host %q, served by another node, needs its port: %w", cfg.Host, err)
		}
		if len(cfg.Secret) == 0 {
			return nil, errors.New("TURN served by another node needs the secret the nodes share")
		}
		return &turnServer{secret: secret, host: cfg.Host}, nil
	}
	if (cfg.MinPort == 0) != (cfg.MaxPort == 0) || cfg.MinPort > cfg.MaxPort {
		return nil, fmt.Errorf("TURN relay ports %d-%d: give both, the first no greater", cfg.MinPort, cfg.MaxPort)
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("TURN listen address: %w", err)
	}
	host := cfg.Host
	if h, p, err := net.SplitHostPort(cfg.Host); err == nil {
		host, port = h, p
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("TURN host %q: %v", host, err)
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
	t := &turnServer{secret: secret, host: net.JoinHostPort(host, port)}
	any := "0.0.0.0"
	if ip.To4() == nil {
		any = "::"
	}
	var gen turn.RelayAddressGenerator = &turn.RelayAddressGeneratorStatic{RelayAddress: ip, Address: any}
	if cfg.MinPort != 0 {
		gen = &turn.RelayAddressGeneratorPortRange{RelayAddress: ip, Address: any, MinPort: cfg.MinPort, MaxPort: cfg.MaxPort}
	}
	// A relay on the internet forwards to public addresses only (and to its
	// own, where both ends' packets meet): never into the network it is in.
	// One on a private network, or the tests' on this computer, serves that.
	private := func(a net.IP) bool {
		return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified()
	}
	allowed := func(_ net.Addr, peer net.IP) bool { return peer.Equal(ip) || !private(peer) || private(ip) }
	t.srv, err = turn.NewServer(turn.ServerConfig{
		Realm:             "hermesh",
		AuthHandler:       turn.LongTermTURNRESTAuthHandler(t.secret, nil),
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: countingConn{udp}, RelayAddressGenerator: gen, PermissionHandler: allowed}},
		ListenerConfigs:   []turn.ListenerConfig{{Listener: tcp, RelayAddressGenerator: gen, PermissionHandler: allowed}},
	})
	if err != nil {
		udp.Close()
		tcp.Close()
		return nil, err
	}
	return t, nil
}

// close stops the server this relay runs, if any.
func (t *turnServer) close() {
	if t != nil && t.srv != nil {
		t.srv.Close()
	}
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

// ParsePortRange reads a range of ports written "49160-49200".
func ParsePortRange(s string) (min, max uint16, err error) {
	lo, hi, ok := strings.Cut(s, "-")
	a, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
	b, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
	if !ok || err1 != nil || err2 != nil || a == 0 || a > b {
		return 0, 0, fmt.Errorf("%q is not a range of ports like 49160-49200", s)
	}
	return uint16(a), uint16(b), nil
}
