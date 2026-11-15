package screen

import (
	"net"
	"strings"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// systemNet enumerates interfaces for each gather. stdnet otherwise keeps
// the addresses it saw when the ICE agent was first created, even on restart.
// Each enumeration owns its snapshot: ongoing gathers never race a refresh.
// Filtering happens here rather than via Pion's SetInterfaceFilter/SetIPFilter
// because Pion treats those filters as a signal to allocate TURN on every
// local interface address instead of once on the default route.
type systemNet struct {
	*stdnet.Net
}

func newSystemNet() (*systemNet, error) {
	n, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return &systemNet{Net: n}, nil
}

func (*systemNet) Interfaces() ([]*transport.Interface, error) {
	n, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	all, err := n.Interfaces()
	if err != nil {
		return nil, err
	}
	return filterInterfaces(all), nil
}

func filterInterfaces(all []*transport.Interface) []*transport.Interface {
	out := make([]*transport.Interface, 0, len(all))
	seen := map[string]bool{}
	for _, iface := range all {
		if !UsableInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		filtered := transport.NewInterface(iface.Interface)
		kept := 0
		// Each macOS/iOS interface with SLAAC privacy extensions carries
		// multiple global IPv6s in the same /64; keeping one avoids multiplying
		// ICE host-pair probes across identical routes.
		seenV6Prefix := map[[8]byte]bool{}
		for _, addr := range addrs {
			ip := addrIP(addr)
			if !UsableInterfaceIP(ip) {
				continue
			}
			key := ip.String()
			if seen[key] {
				continue
			}
			if ip.To4() == nil {
				if v6 := ip.To16(); v6 != nil {
					var p64 [8]byte
					copy(p64[:], v6[:8])
					if seenV6Prefix[p64] {
						continue
					}
					seenV6Prefix[p64] = true
				}
			}
			seen[key] = true
			filtered.AddAddress(addr)
			kept++
		}
		if kept > 0 {
			out = append(out, filtered)
		}
	}
	return out
}

// UsableInterface reports whether an interface can carry peer traffic.
// Apple peer-to-peer/internal radios, hypervisor/container bridges, carrier
// IPsec tunnels (ipsec*), and secondary iOS cellular bearers (pdp_ip1..N,
// used for IMS/carrier control) have no peer or STUN route and otherwise
// stall STUN gathering for 3 s.
func UsableInterface(name string) bool {
	for _, prefix := range []string{
		"awdl", "llw", "anpi", "anri", "ap", "nan", "gif", "stf", "ipsec",
		"bridge", "vnic", "vmnet", "vboxnet", "vmenet", "virbr", "docker",
	} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	if strings.HasPrefix(name, "pdp_ip") && name != "pdp_ip0" {
		return false
	}
	return true
}

// UsableInterfaceIP reports whether an interface IP can be used for ICE.
// It excludes link-local addresses, RFC 2544 benchmark / Surge Fake-IP
// space (198.18.0.0/15), and IPv6 Unique Local Addresses (fc00::/7).
func UsableInterfaceIP(ip net.IP) bool {
	if len(ip) == 0 || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
			return false
		}
		return true
	}
	return !ip.IsPrivate()
}

func addrIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.IPNet:
		return a.IP
	case *net.IPAddr:
		return a.IP
	default:
		return nil
	}
}

func (n *systemNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	if err := bindUDPInterface(c, addr); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
