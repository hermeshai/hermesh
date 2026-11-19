package machine

import (
	"cmp"
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	screenhost "github.com/hermeshai/hermesh/internal/screen"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
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

// serveDirectQUIC listens for direct E2E QUIC/HTTP3 UDP connections from the
// owner's devices (LAN IPv4 / public IPv6), giving 1-RTT handshakes,
// independent non-HoL streams, and QUIC connection migration. Global IPv6
// addresses are bound explicitly so outbound/return IPv6 packets route over
// the physical interface even when a TUN VPN scopes the default IPv6 route.
func (n *Machine) serveDirectQUIC(ctx context.Context) {
	accept := func(s *identity.Subject) error {
		if s.Kind != identity.SubjectOwnerDevice {
			return errors.New("direct QUIC tunnel requires owner device")
		}
		return nil
	}
	ep, err := transport.ListenQUICEndpoint("udp4", ":0", n.key.CryptoSigner(), n.credentials, n.trust, accept)
	if err != nil {
		ep, err = transport.ListenQUICEndpoint("udp", ":0", n.key.CryptoSigner(), n.credentials, n.trust, accept)
		if err != nil {
			n.log.Warn("direct QUIC endpoint", "err", err)
			return
		}
	}
	if ua, ok := ep.Addr().(*net.UDPAddr); ok {
		n.directQUICPort.Store(int32(ua.Port))
		n.log.Info("direct QUIC endpoint listening", "port", ua.Port)
	}
	n.quicMu.Lock()
	n.quicCtx = ctx
	n.quicPrimary = ep
	n.quicV6 = map[string]*transport.QUICEndpoint{}
	n.quicMu.Unlock()

	n.syncDirectQUICV6()

	go func() {
		<-ctx.Done()
		n.directQUICPort.Store(0)
		n.quicMu.Lock()
		primary := n.quicPrimary
		v6s := n.quicV6
		n.quicPrimary = nil
		n.quicV6 = nil
		n.quicCtx = nil
		n.quicMu.Unlock()
		if primary != nil {
			_ = primary.Close()
		}
		for _, v6ep := range v6s {
			_ = v6ep.Close()
		}
	}()
	n.acceptQUICEndpoint(ctx, ep)
}

func (n *Machine) acceptQUICEndpoint(ctx context.Context, ep *transport.QUICEndpoint) {
	for {
		qconn, err := ep.Accept(ctx)
		if err != nil {
			return
		}
		go n.serveQUICTunnel(ctx, qconn, "direct QUIC, not through the relay", true)
	}
}

const directIPCacheTTL = 3 * time.Second

type directIPSnapshot struct {
	at       time.Time
	all      []string
	ipv4     []string
	globalV6 []string
}

func (n *Machine) cachedDirectIPs(force bool) (directIPSnapshot, bool) {
	n.directIPMu.Lock()
	if !force && !n.directIPs.at.IsZero() && time.Since(n.directIPs.at) < directIPCacheTTL {
		snap := n.directIPs
		n.directIPMu.Unlock()
		return snap, false
	}
	n.directIPMu.Unlock()

	snap := scanDirectIPs()
	n.directIPMu.Lock()
	n.directIPs = snap
	n.directIPMu.Unlock()
	return snap, true
}

func (n *Machine) syncDirectQUICV6() {
	snap, _ := n.cachedDirectIPs(true)
	n.syncDirectQUICV6With(snap.globalV6)
}

func (n *Machine) syncDirectQUICV6With(want []string) {
	n.quicMu.Lock()
	ctx := n.quicCtx
	if ctx == nil || ctx.Err() != nil {
		n.quicMu.Unlock()
		return
	}
	existing := make(map[string]bool, len(n.quicV6))
	for ip := range n.quicV6 {
		existing[ip] = true
	}
	n.quicMu.Unlock()
	wantSet := make(map[string]bool, len(want))
	for _, ip := range want {
		wantSet[ip] = true
	}
	accept := func(s *identity.Subject) error {
		if s.Kind != identity.SubjectOwnerDevice {
			return errors.New("direct QUIC tunnel requires owner device")
		}
		return nil
	}
	for _, ip := range want {
		if existing[ip] {
			continue
		}
		ep, err := transport.ListenQUICEndpoint("udp6", net.JoinHostPort(ip, "0"), n.key.CryptoSigner(), n.credentials, n.trust, accept)
		if err != nil {
			continue
		}
		n.quicMu.Lock()
		if n.quicCtx == nil || n.quicCtx.Err() != nil || n.quicV6 == nil {
			n.quicMu.Unlock()
			_ = ep.Close()
			return
		}
		if _, dup := n.quicV6[ip]; dup {
			n.quicMu.Unlock()
			_ = ep.Close()
			continue
		}
		n.quicV6[ip] = ep
		n.quicMu.Unlock()
		if ua, ok := ep.Addr().(*net.UDPAddr); ok {
			n.log.Info("direct QUIC IPv6 endpoint listening", "addr", net.JoinHostPort(ip, strconv.Itoa(ua.Port)))
		}
		go n.acceptQUICEndpoint(ctx, ep)
	}
	// Close bound endpoints whose IPv6 address disappeared from local interfaces.
	var stale []*transport.QUICEndpoint
	n.quicMu.Lock()
	for ip, ep := range n.quicV6 {
		if !wantSet[ip] {
			delete(n.quicV6, ip)
			stale = append(stale, ep)
		}
	}
	n.quicMu.Unlock()
	for _, ep := range stale {
		_ = ep.Close()
	}
}

// punchDirect sends UDP hole-punch packets from the machine's bound QUIC
// endpoints toward targets and returns the machine's current QUIC addresses.
func (n *Machine) punchDirect(targets []string) []string {
	n.syncDirectQUICV6()
	addrs := n.directQUICAddresses()
	if len(targets) == 0 {
		return addrs
	}
	var dsts []*net.UDPAddr
	for _, t := range targets {
		if len(dsts) >= 8 {
			break
		}
		ua, err := net.ResolveUDPAddr("udp", t)
		if err != nil || ua.Port <= 0 || ua.IP == nil || !ua.IP.IsGlobalUnicast() {
			continue
		}
		dsts = append(dsts, ua)
	}
	if len(dsts) == 0 {
		return addrs
	}
	n.quicMu.Lock()
	ctx := n.quicCtx
	primary := n.quicPrimary
	var v6eps []*transport.QUICEndpoint
	for _, ep := range n.quicV6 {
		v6eps = append(v6eps, ep)
	}
	n.quicMu.Unlock()

	sendOnce := func() {
		for _, dst := range dsts {
			if dst.IP.To4() == nil {
				for _, ep := range v6eps {
					_ = ep.WritePunch(dst)
				}
			} else if primary != nil {
				_ = primary.WritePunch(dst)
			}
		}
	}
	// First punch synchronously so firewall pinholes open before the RPC returns.
	sendOnce()
	if ctx != nil {
		go func() {
			for range 2 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(120 * time.Millisecond):
					sendOnce()
				}
			}
		}()
	}
	return addrs
}

func joinIPsPort(ips []string, port int) []string {
	if port <= 0 || len(ips) == 0 {
		return nil
	}
	pstr := strconv.Itoa(port)
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, pstr))
	}
	return out
}

func (n *Machine) directStatusAddrs() (tcpAddrs, quicAddrs []string) {
	snap, refreshed := n.cachedDirectIPs(false)
	if refreshed {
		n.syncDirectQUICV6With(snap.globalV6)
	}
	tcpAddrs = joinIPsPort(snap.all, int(n.directPort.Load()))
	quicAddrs = n.directQUICAddressesFrom(snap)
	return tcpAddrs, quicAddrs
}

func (n *Machine) directAddresses() []string {
	snap, _ := n.cachedDirectIPs(false)
	return joinIPsPort(snap.all, int(n.directPort.Load()))
}

func (n *Machine) directQUICAddresses() []string {
	snap, refreshed := n.cachedDirectIPs(false)
	if refreshed {
		n.syncDirectQUICV6With(snap.globalV6)
	}
	return n.directQUICAddressesFrom(snap)
}

func (n *Machine) directQUICAddressesFrom(snap directIPSnapshot) []string {
	out := joinIPsPort(snap.ipv4, int(n.directQUICPort.Load()))
	n.quicMu.Lock()
	var v6Addrs []string
	for ip, ep := range n.quicV6 {
		if ua, ok := ep.Addr().(*net.UDPAddr); ok && ua.Port > 0 {
			v6Addrs = append(v6Addrs, net.JoinHostPort(ip, strconv.Itoa(ua.Port)))
		}
	}
	n.quicMu.Unlock()
	slices.Sort(v6Addrs)
	out = append(out, v6Addrs...)
	return out
}

func scanDirectIPs() directIPSnapshot {
	snap := directIPSnapshot{at: time.Now()}
	ifaces, err := net.Interfaces()
	if err != nil {
		return snap
	}
	type candidate struct {
		tier  int
		ip    string
		iface string
		v6    [8]byte
		isV6  bool
	}
	var cands []candidate
	seen := map[string]bool{}
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
			ipStr := ip.String()
			if seen[ipStr] {
				continue
			}
			seen[ipStr] = true
			c := candidate{tier: 2, ip: ipStr, iface: iface.Name}
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
	// Preserve OS interface address order within each tier so macOS/Linux
	// preferred non-deprecated IPv6 addresses stay ahead of deprecated
	// temporary privacy addresses when deduplicating per /64 prefix.
	slices.SortStableFunc(cands, func(a, b candidate) int {
		return cmp.Compare(a.tier, b.tier)
	})
	type ifacePrefix struct {
		iface string
		v6    [8]byte
	}
	seenV6 := map[ifacePrefix]bool{}
	snap.all = make([]string, 0, len(cands))
	for _, c := range cands {
		if c.isV6 {
			k := ifacePrefix{iface: c.iface, v6: c.v6}
			if seenV6[k] {
				continue
			}
			seenV6[k] = true
			snap.globalV6 = append(snap.globalV6, c.ip)
		} else {
			snap.ipv4 = append(snap.ipv4, c.ip)
		}
		snap.all = append(snap.all, c.ip)
	}
	return snap
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
