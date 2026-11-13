package screen

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/turn/v5"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// probeWithin: how long one question of the probe waits for its answer (a
// variable for the tests).
var probeWithin = 1500 * time.Millisecond

// probeNAT finds out how this end's network maps its packets, for the log:
// whether a socket is seen at the same public port by two destinations,
// and how far apart the ports of consecutive sockets are. These two
// destinations may share an IP: this measures mapping, not NAT filtering
// or general reachability, and cannot establish that direct ICE will work.
// It needs nothing but the relay's TURN server: one
// destination is its STUN port, the other a relayed address allocated on it
// for the probe, where this end reads what arrives and from where.
//
// It asks through each way a packet can leave: the system's route (which a
// VPN or proxy app may own; pion's sockets for STUN and TURN go that way),
// and each of this end's addresses, bound to its interface as the local
// candidates' sockets are. Two sockets a way, one after the other. A line
// reads
//
//	system 203.0.113.7:40001>40001 40002>40002; 192.168.1.5 203.0.113.7:50123>50123 50124>50124
//
// the public address, then for each socket the port the STUN server saw and
// the port the relayed address saw. "" when there is no TURN server, or it
// gave no address.
func probeNAT(ctx context.Context, servers []*relayv1.IceServer) string {
	var server *net.UDPAddr
	var user, pass string
	for _, s := range servers {
		if a := turnUDP(s); a != nil {
			server, user, pass = a, s.Username, s.Credential
			break
		}
	}
	if server == nil || server.IP.To4() == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 4*probeWithin)
	defer cancel()
	control, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return ""
	}
	defer control.Close()
	c, err := turn.NewClient(&turn.ClientConfig{STUNServerAddr: server.String(), TURNServerAddr: server.String(), Conn: control, Username: user, Password: pass})
	if err != nil {
		return ""
	}
	defer c.Close()
	if c.Listen() != nil {
		return ""
	}
	allocated := make(chan net.PacketConn, 1)
	go func() {
		relayed, err := c.Allocate()
		if err != nil {
			relayed = nil
		}
		allocated <- relayed
	}()
	var relayed net.PacketConn
	select {
	case relayed = <-allocated:
	case <-ctx.Done():
		control.Close() // ends the allocation that waits
		relayed = <-allocated
	}
	if relayed == nil {
		return ""
	}
	defer relayed.Close()

	// What arrives at the relayed address, by the token it carries.
	var mu sync.Mutex
	seen := map[string]chan net.Addr{}
	go func() {
		b := make([]byte, 64)
		for {
			n, from, err := relayed.ReadFrom(b)
			if err != nil {
				return
			}
			mu.Lock()
			if ch := seen[string(b[:n])]; ch != nil {
				select {
				case ch <- from:
				default:
				}
			}
			mu.Unlock()
		}
	}()
	arrival := func(token string) chan net.Addr {
		ch := make(chan net.Addr, 1)
		mu.Lock()
		seen[token] = ch
		mu.Unlock()
		return ch
	}

	// one asks from a new socket on local (nil: the system's route) and
	// returns the public address the STUN server saw, and the port the
	// relayed address saw.
	one := func(local net.IP) (ip string, toSTUN, toRelay int) {
		var conn net.PacketConn
		var err error
		if local == nil {
			conn, err = net.ListenPacket("udp4", ":0")
		} else if n, e := newSystemNet(); e != nil {
			err = e
		} else if c, e := n.ListenUDP("udp4", &net.UDPAddr{IP: local}); e != nil {
			err = e
		} else {
			conn = c
		}
		if err != nil {
			return "", 0, 0
		}
		defer conn.Close()
		mapped := stunMapped(conn, server)
		if mapped == nil {
			return "", 0, 0
		}
		// The relayed address lets in what comes from an address it has
		// sent to: any port of it.
		if _, err := relayed.WriteTo([]byte{0}, mapped); err != nil {
			return mapped.IP.String(), mapped.Port, 0
		}
		token := make([]byte, 16)
		rand.Read(token)
		from := arrival(string(token))
		for i := 0; i < 3; i++ {
			conn.WriteTo(token, relayed.LocalAddr())
			select {
			case a := <-from:
				if u, ok := a.(*net.UDPAddr); ok {
					return mapped.IP.String(), mapped.Port, u.Port
				}
			case <-time.After(probeWithin / 3):
			case <-ctx.Done():
				return mapped.IP.String(), mapped.Port, 0
			}
		}
		return mapped.IP.String(), mapped.Port, 0
	}
	// way asks from two sockets, one after the other.
	way := func(name string, local net.IP) string {
		out, public := name, ""
		for i := 0; i < 2; i++ {
			ip, a, b := one(local)
			switch {
			case ip == "":
				out += " none"
				continue
			case public == "":
				public = ip
				out += " " + ip + ":"
			case ip != public:
				out += " " + ip + ":"
			default:
				out += " "
			}
			out += fmt.Sprint(a) + ">"
			if b == 0 {
				out += "none"
			} else {
				out += fmt.Sprint(b)
			}
		}
		return strings.ReplaceAll(out, ": ", ":")
	}
	ways := []struct {
		name  string
		local net.IP
	}{{"system", nil}}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsGlobalUnicast() && len(ways) < 7 {
			ways = append(ways, struct {
				name  string
				local net.IP
			}{n.IP.String(), n.IP})
		}
	}
	lines := make([]string, len(ways))
	var wg sync.WaitGroup
	for i, w := range ways {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines[i] = way(w.name, w.local)
		}()
	}
	wg.Wait()
	return strings.Join(lines, "; ")
}

// turnUDP is the address of a TURN server's UDP port, nil when it has none.
func turnUDP(s *relayv1.IceServer) *net.UDPAddr {
	if s.Username == "" {
		return nil
	}
	for _, u := range s.Urls {
		scheme, rest, _ := strings.Cut(u, ":")
		hostport, query, _ := strings.Cut(rest, "?")
		if scheme != "turn" || strings.Contains(query, "transport=tcp") {
			continue
		}
		if _, _, err := net.SplitHostPort(hostport); err != nil {
			hostport = net.JoinHostPort(hostport, "3478")
		}
		if a, err := net.ResolveUDPAddr("udp", hostport); err == nil {
			return a
		}
	}
	return nil
}

// stunMapped is the public address a STUN server sees conn at; nil when it
// does not answer in probeWithin.
func stunMapped(conn net.PacketConn, server *net.UDPAddr) *net.UDPAddr {
	req, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		return nil
	}
	b := make([]byte, 1500)
	for i := 0; i < 3; i++ {
		if _, err := conn.WriteTo(req.Raw, server); err != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(probeWithin / 3))
		for {
			n, _, err := conn.ReadFrom(b)
			if err != nil {
				break
			}
			res := &stun.Message{Raw: b[:n]}
			var mapped stun.XORMappedAddress
			if res.Decode() == nil && res.TransactionID == req.TransactionID && mapped.GetFrom(res) == nil {
				return &net.UDPAddr{IP: mapped.IP, Port: mapped.Port}
			}
		}
	}
	return nil
}
