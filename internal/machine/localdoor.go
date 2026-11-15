package machine

import (
	"cmp"
	"context"
	"net"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	screenhost "github.com/hermeshai/hermesh/internal/screen"
)

// serveLocal takes the connections of this user's own agents on this
// computer (paths.Local), until ctx ends. Each is served as a tunnel the
// relay spliced is: the same end-to-end handshake, the same grant and the
// same checks, without the relay. An agent on the machine's own computer
// so reaches it by the shortest way, and also while the relay cannot be
// reached. Only this user can connect; what an agent may do is still what
// its grant says, as the machine knows it.
func (n *Machine) serveLocal(ctx context.Context) {
	path := paths.Local(n.machineID)
	if path == "" {
		return
	}
	ln, err := localipc.Listen(path)
	if err != nil {
		n.log.Warn("local endpoint", "err", err)
		return
	}
	go func() {
		<-ctx.Done()
		ln.Close()
		os.Remove(path)
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go n.serveTunnel(ctx, conn, "from this computer, not through the relay", false)
	}
}

// serveDirect listens for direct E2E TCP connections from the owner's devices
// (LAN IPv4 / public IPv6) so owner signaling bypasses the relay when
// reachable. Agent credentials are refused on this door.
func (n *Machine) serveDirect(ctx context.Context) {
	lc := net.ListenConfig{KeepAlive: 15 * time.Second}
	ln, err := lc.Listen(ctx, "tcp", ":0")
	if err != nil {
		n.log.Warn("direct endpoint", "err", err)
		return
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); ok {
		n.directPort.Store(int32(ta.Port))
		n.log.Info("direct endpoint listening", "port", ta.Port)
	}
	go func() {
		<-ctx.Done()
		n.directPort.Store(0)
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go n.serveTunnel(ctx, conn, "direct TCP, not through the relay", true)
	}
}

func (n *Machine) directAddresses() []string {
	port := int(n.directPort.Load())
	if port <= 0 {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	type candidate struct {
		tier  int
		addr  string
		iface string
		v6    [8]byte
		isV6  bool
	}
	var cands []candidate
	seen := map[string]bool{}
	pstr := strconv.Itoa(port)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || !screenhost.UsableInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip := netAddrIP(a)
			if ip == nil || !ip.IsGlobalUnicast() || !screenhost.UsableInterfaceIP(ip) {
				continue
			}
			hp := net.JoinHostPort(ip.String(), pstr)
			if seen[hp] {
				continue
			}
			seen[hp] = true
			c := candidate{tier: 2, addr: hp, iface: iface.Name}
			if v4 := ip.To4(); v4 != nil {
				if isRFC1918(v4) {
					c.tier = 0
				} else {
					c.tier = 1
				}
			} else if v6 := ip.To16(); v6 != nil {
				c.isV6 = true
				copy(c.v6[:], v6[:8])
			}
			cands = append(cands, c)
		}
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		if c := cmp.Compare(a.tier, b.tier); c != 0 {
			return c
		}
		return cmp.Compare(a.addr, b.addr)
	})
	type ifacePrefix struct {
		iface string
		v6    [8]byte
	}
	seenV6 := map[ifacePrefix]bool{}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if c.isV6 {
			k := ifacePrefix{iface: c.iface, v6: c.v6}
			if seenV6[k] {
				continue
			}
			seenV6[k] = true
		}
		out = append(out, c.addr)
	}
	return out
}

func netAddrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

func isRFC1918(v4 net.IP) bool {
	return v4[0] == 10 ||
		(v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) ||
		(v4[0] == 192 && v4[1] == 168)
}
