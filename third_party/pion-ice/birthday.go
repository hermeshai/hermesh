// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/stun/v4"
)

const (
	birthdayCapability = "hermesh-punch"
	birthdayReady      = "hermesh-pool-ready/1"
	birthdaySockets    = 128
	birthdayProbes     = 2048
	birthdayInterval   = 4 * time.Millisecond
	birthdayLifetime   = 15 * time.Second
	birthdayReadyCount = 4
)

// The pointer and epoch are owned by the ICE task loop. Workers own only
// immutable snapshots, atomic counters and the socket pool's mutex.
type birthdayPunchState struct {
	generation         uint64
	local, remote      string
	controlling        bool
	ctx                context.Context
	cancel             context.CancelFunc
	started            time.Time
	target             netip.AddrPort
	peerIP             netip.Addr
	created, opened    atomic.Int32
	probes, ready      atomic.Int32
	received, rejected atomic.Int32
	reclaimed, adopted atomic.Int32
}

func (a *Agent) markBirthdayCandidate(c Candidate) {
	if a.boundedPunchEnabled {
		_ = c.AddExtension(CandidateExtension{Key: birthdayCapability, Value: "1"})
	}
}

func birthdayCapable(c Candidate) bool {
	x, ok := c.GetExtension(birthdayCapability)
	return ok && x.Value == "1"
}

func birthdayPhysical(c Candidate) bool {
	r := c.RelatedAddress()
	if c.Type() != CandidateTypeServerReflexive || c.NetworkType() != NetworkTypeUDP4 ||
		r == nil || r.Port <= 0 || r.Port > 65535 || !boundedPunchPublicIP(c.addrPort().Addr()) {
		return false
	}
	ip, err := netip.ParseAddr(r.Address)
	return err == nil && ip.Is4() && ip.IsGlobalUnicast() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

func (a *Agent) birthdayLocalBase(target netip.AddrPort) (Candidate, Candidate) {
	for _, c := range a.localCandidates[NetworkTypeUDP4] {
		if !birthdayPhysical(c) || target.IsValid() && c.addrPort() != target {
			continue
		}
		for _, h := range a.localCandidates[NetworkTypeUDP4] {
			r := c.RelatedAddress()
			if h.Type() == CandidateTypeHost && h.Address() == r.Address && h.Port() == r.Port {
				return h, c
			}
		}
		return c, c
	}
	return nil, nil
}

func (a *Agent) birthdayRemote(ip netip.Addr) Candidate {
	for _, c := range a.remoteCandidates[NetworkTypeUDP4] {
		if birthdayCapable(c) && birthdayPhysical(c) && (!ip.IsValid() || c.addrPort().Addr() == ip) {
			return c
		}
	}
	return nil
}

// A signaled address alone must not authorize a random-port probe burst.
// Require a received, integrity-checked request from that IP in this epoch,
// including a peer-reflexive source learned through TURN. Exclude TURN IPs.
func (a *Agent) birthdayVerifiedIP(ip netip.Addr) bool {
	if !boundedPunchPublicIP(ip) || a.birthdayRemote(ip) == nil {
		return false
	}
	for _, c := range a.remoteCandidates[NetworkTypeUDP4] {
		if c.Type() == CandidateTypeRelay && c.addrPort().Addr() == ip {
			return false
		}
	}
	for _, p := range a.checklist {
		if p.Remote.Type() != CandidateTypeRelay && p.Remote.addrPort().Addr() == ip && p.RequestsReceived() > 0 {
			return true
		}
	}
	return false
}

func birthdayRelayed(p *CandidatePair) bool {
	return p != nil && (p.Local.Type() == CandidateTypeRelay || p.Remote.Type() == CandidateTypeRelay)
}

func (a *Agent) birthdayCurrent(s *birthdayPunchState) bool {
	return a.birthdayPunchState == s && s.ctx.Err() == nil &&
		a.gatherGeneration == s.generation && a.localUfrag == s.local && a.remoteUfrag == s.remote &&
		a.isControlling.Load() == s.controlling
}

func (a *Agent) newBirthdayState(target netip.AddrPort, peerIP netip.Addr, lifetime time.Duration) *birthdayPunchState {
	ctx, cancel := context.WithTimeout(a.loop, lifetime)
	s := &birthdayPunchState{generation: a.gatherGeneration, local: a.localUfrag, remote: a.remoteUfrag,
		controlling: a.isControlling.Load(), ctx: ctx, cancel: cancel, started: time.Now(), target: target, peerIP: peerIP}
	a.birthdayPunchState = s
	return s
}

func (a *Agent) birthdayAttempted() bool {
	s := a.birthdayPunchState
	if s == nil {
		return false
	}
	if s.generation == a.gatherGeneration && s.local == a.localUfrag && s.remote == a.remoteUfrag {
		return true // a duplicate ready must never reset the budget
	}
	s.cancel()
	return false
}

func (a *Agent) stopBirthdayPunch(_ string) {
	if s := a.birthdayPunchState; s != nil {
		s.cancel()
	}
}

// Called by normal connectivity checks. Only the controlling peer opens a
// pool. The controlled peer waits for authenticated readiness over TURN.
func (a *Agent) startBirthdayPunch() {
	if selected := a.getSelectedPair(); selected != nil && !birthdayRelayed(selected) {
		a.stopBirthdayPunch("direct")
		return
	}
	if !a.boundedPunchEnabled || !a.automaticRenomination || !a.enableRenomination ||
		!a.isControlling.Load() || !birthdayRelayed(a.getSelectedPair()) || a.birthdayAttempted() {
		return
	}
	base, public := a.birthdayLocalBase(netip.AddrPort{})
	remote := a.birthdayRemote(netip.Addr{})
	if base == nil || remote == nil {
		return
	}
	s := a.newBirthdayState(remote.addrPort(), public.addrPort().Addr(), birthdayLifetime)
	password, remotePassword, tieBreaker := a.localPwd, a.remotePwd, a.tieBreaker
	ip := net.ParseIP(public.RelatedAddress().Address)
	priority := base.prflxPriority()
	a.reportBoundedPunch("Birthday pool started: target=%s source=%s peer_ip=%s socket_limit=%d lifetime=%s",
		s.target, ip, s.peerIP, birthdaySockets, birthdayLifetime)
	go a.runBirthdayPool(s, ip, password, remotePassword, tieBreaker, priority)
}

// Called only after normal ICE username/MESSAGE-INTEGRITY verification. Ready
// identifies a gathered public endpoint and its local sending socket. NAT may
// expose another port toward the peer; the pool authenticates that same-IP
// peer-reflexive endpoint before handing it to ICE.
func (a *Agent) handleBirthdayRequest(m *stun.Message, _, _ Candidate) {
	if !a.boundedPunchEnabled || !a.automaticRenomination || !a.enableRenomination ||
		a.isControlling.Load() || !birthdayRelayed(a.getSelectedPair()) || a.birthdayAttempted() {
		return
	}
	software, err := m.Get(stun.AttrSoftware)
	parts := strings.Fields(string(software))
	if err != nil || len(parts) != 4 || parts[0] != birthdayReady {
		return
	}
	target, err := netip.ParseAddrPort(parts[1])
	if err != nil {
		return
	}
	ip, err := netip.ParseAddr(parts[2])
	remaining, timeErr := strconv.Atoi(parts[3])
	// Keep at least two seconds of slack beyond the eight-second probe
	// schedule. Late ready retransmissions cannot start a pool-less search.
	if err != nil || timeErr != nil || remaining < int((birthdayProbes*birthdayInterval+2*time.Second)/time.Millisecond) ||
		remaining > int(birthdayLifetime/time.Millisecond) || !a.birthdayVerifiedIP(ip) {
		return
	}
	base, _ := a.birthdayLocalBase(target)
	if base == nil {
		return
	}
	s := a.newBirthdayState(target, ip, time.Duration(remaining)*time.Millisecond-2*time.Second)
	a.reportBoundedPunch("Birthday probes started: base=%s mapped=%s peer_ip=%s packet_limit=%d interval=%s",
		base.addrPort(), target, ip, birthdayProbes, birthdayInterval)
	go a.runBirthdayProbes(s, base)
}

func (a *Agent) reportBirthday(s *birthdayPunchState, reason string) {
	a.reportBoundedPunch("Birthday stopped: target=%s peer_ip=%s created=%d opened=%d ready=%d probes=%d received=%d rejected=%d reclaimed=%d adopted=%d elapsed=%s reason=%s",
		s.target, s.peerIP, s.created.Load(), s.opened.Load(), s.ready.Load(), s.probes.Load(),
		s.received.Load(), s.rejected.Load(), s.reclaimed.Load(), s.adopted.Load(), time.Since(s.started).Round(time.Millisecond), reason)
}

func (a *Agent) runBirthdayProbes(s *birthdayPunchState, base Candidate) {
	reason := "budget"
	defer func() { s.cancel(); a.reportBirthday(s, reason) }()
	var seed [32]byte
	if _, err := cryptorand.Read(seed[:]); err != nil {
		reason = "random-source"
		return
	}
	rng := rand.New(rand.NewChaCha8(seed))
	seen := make(map[uint16]bool, birthdayProbes)
	tick := time.NewTimer(birthdayInterval)
	defer tick.Stop()
	for len(seen) < birthdayProbes {
		select {
		case <-s.ctx.Done():
			reason = "deadline-or-close"
			return
		case <-tick.C:
		}
		port := uint16(1024 + rng.IntN(65536-1024))
		for seen[port] {
			port = uint16(1024 + rng.IntN(65536-1024))
		}
		seen[port] = true
		stop := false
		err := a.loop.Run(s.ctx, func(context.Context) {
			if !a.birthdayCurrent(s) || !birthdayRelayed(a.getSelectedPair()) {
				reason, stop = "generation-or-direct", true
				return
			}
			remote, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
				Network: "udp4", Address: s.peerIP.String(), Port: int(port), Component: base.Component(),
			})
			if err != nil {
				return
			}
			m, err := stun.Build(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(s.remote+":"+s.local), AttrControlled(a.tieBreaker),
				PriorityAttr(base.prflxPriority()), stun.NewShortTermIntegrity(a.remotePwd), stun.Fingerprint)
			if err == nil {
				if n, err := base.writeTo(m.Raw, remote); err == nil && n == len(m.Raw) {
					s.probes.Add(1)
				}
			}
		})
		if err != nil || stop {
			return
		}
		tick.Reset(birthdayInterval)
	}
}

type birthdaySocketPool struct {
	mu      sync.Mutex
	conns   map[net.PacketConn]bool
	closed  bool
	adopted net.PacketConn
	readers sync.WaitGroup
	hit     chan birthdaySocketHit
	state   *birthdayPunchState
}

type birthdaySocketHit struct {
	conn net.PacketConn
	msg  *stun.Message
	from netip.AddrPort
}

func (p *birthdaySocketPool) add(c net.PacketConn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.state.ctx.Err() != nil {
		_ = c.Close()
		return false
	}
	p.conns[c] = true
	p.state.created.Add(1)
	return true
}

func (p *birthdaySocketPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for c := range p.conns {
		if c != p.adopted {
			_ = c.Close()
			p.state.reclaimed.Add(1)
		}
		delete(p.conns, c)
	}
}

func (a *Agent) runBirthdayPool(s *birthdayPunchState, ip net.IP, password, remotePassword string, tieBreaker uint64, priority uint32) {
	p := &birthdaySocketPool{conns: make(map[net.PacketConn]bool), hit: make(chan birthdaySocketHit, birthdaySockets), state: s}
	// Cancellation must close sockets while opening or WriteTo is in flight.
	closed := make(chan struct{})
	go func() { <-s.ctx.Done(); p.close(); close(closed) }()
	reason := "deadline-or-close"
	defer func() { s.cancel(); <-closed; p.readers.Wait(); a.reportBirthday(s, reason) }()
	tick := time.NewTimer(birthdayInterval)
	defer tick.Stop()
	for range birthdaySockets {
		select {
		case <-s.ctx.Done():
			return
		case hit := <-p.hit:
			if a.adoptBirthdaySocket(s, p, hit) {
				reason = "adopted"
			}
			return
		case <-tick.C:
		}
		current := false
		if err := a.loop.Run(s.ctx, func(context.Context) {
			current = a.birthdayCurrent(s) && birthdayRelayed(a.getSelectedPair())
		}); err != nil || !current {
			reason = "generation-or-direct"
			return
		}
		c, err := a.net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
		if err != nil {
			break
		}
		if !p.add(c) {
			return
		}
		p.readers.Add(1)
		go func(c net.PacketConn) { defer p.readers.Done(); a.readBirthdaySocket(s, p, c, password) }(c)
		m, err := stun.Build(stun.BindingRequest, stun.TransactionID,
			stun.NewUsername(s.remote+":"+s.local), AttrControlling(tieBreaker), PriorityAttr(priority),
			stun.NewShortTermIntegrity(remotePassword), stun.Fingerprint)
		if err != nil {
			return
		}
		_ = c.SetWriteDeadline(time.Now().Add(150 * time.Millisecond))
		if n, err := c.WriteTo(m.Raw, net.UDPAddrFromAddrPort(s.target)); err == nil && n == len(m.Raw) {
			s.opened.Add(1)
		}
		_ = c.SetWriteDeadline(time.Time{})
		tick.Reset(birthdayInterval)
	}
	if s.opened.Load() < 8 {
		reason = "insufficient-openings"
		return
	}
	// All mappings are open before readiness is sent over the selected relay.
	readyTick := time.NewTicker(500 * time.Millisecond)
	defer readyTick.Stop()
	readyDue := true
	for {
		if readyDue {
			stop := false
			err := a.loop.Run(s.ctx, func(context.Context) {
				if !a.birthdayCurrent(s) || !birthdayRelayed(a.getSelectedPair()) {
					stop = true
					return
				}
				if s.ready.Load() >= birthdayReadyCount {
					return
				}
				pair := a.getSelectedPair()
				remaining := birthdayLifetime - time.Since(s.started)
				m, err := stun.Build(stun.BindingRequest, stun.TransactionID,
					stun.NewUsername(s.remote+":"+s.local), AttrControlling(a.tieBreaker),
					PriorityAttr(pair.Local.prflxPriority()), stun.NewSoftware(fmt.Sprintf("%s %s %s %d", birthdayReady, s.target, s.peerIP, remaining.Milliseconds())),
					stun.NewShortTermIntegrity(a.remotePwd), stun.Fingerprint)
				if err == nil {
					a.sendBindingRequest(m, pair.Local, pair.Remote)
					s.ready.Add(1)
				}
			})
			if err != nil || stop {
				reason = "generation-or-direct"
				return
			}
		}
		readyDue = false
		select {
		case <-s.ctx.Done():
			return
		case <-readyTick.C:
			readyDue = true
		case hit := <-p.hit:
			if a.adoptBirthdaySocket(s, p, hit) {
				reason = "adopted"
				return
			}
			return
		}
	}
}

func (a *Agent) readBirthdaySocket(s *birthdayPunchState, p *birthdaySocketPool, c net.PacketConn, password string) {
	buf := make([]byte, receiveMTU)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		s.received.Add(1)
		m := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		var username stun.Username
		addr := netAddrToAddrPort(from)
		// A destination-dependent NAT may change the peer's source port.
		// Authenticate that same-IP request, then let ICE discover/validate
		// its peer-reflexive endpoint instead of requiring the STUN port.
		if !addr.IsValid() || addr.Addr().Unmap() != s.target.Addr().Unmap() ||
			m.Decode() != nil || m.Type != stun.BindingRequest ||
			username.GetFrom(m) != nil || string(username) != s.local+":"+s.remote ||
			stun.MessageIntegrity([]byte(password)).Check(m) != nil {
			s.rejected.Add(1)
			continue
		}
		select {
		case p.hit <- birthdaySocketHit{c, m, addr}:
		case <-s.ctx.Done():
		}
		return // never compete with the candidate's subsequent reader
	}
}

// Validate epoch, transfer ownership, publish the one candidate and process
// the hit in one ICE task. addCandidate's stale-generation no-op returns nil,
// so it cannot serve as a two-phase ownership commit for temporary sockets.
func (a *Agent) adoptBirthdaySocket(s *birthdayPunchState, p *birthdaySocketPool, hit birthdaySocketHit) bool {
	adopted := false
	_ = a.loop.Run(s.ctx, func(context.Context) {
		if !a.birthdayCurrent(s) || !birthdayRelayed(a.getSelectedPair()) {
			return
		}
		addr, ok := hit.conn.LocalAddr().(*net.UDPAddr)
		if !ok {
			return
		}
		c, err := NewCandidateHost(&CandidateHostConfig{
			Network: "udp4", Address: addr.IP.String(), Port: addr.Port, Component: ComponentRTP,
		})
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed || s.ctx.Err() != nil || p.adopted != nil || !p.conns[hit.conn] {
			p.mu.Unlock()
			return
		}
		_ = hit.conn.SetDeadline(time.Time{})
		p.adopted = hit.conn
		a.setCandidateExtensions(c, s.generation)
		a.markBirthdayCandidate(c)
		c.start(a, hit.conn, a.startedCh)
		a.localCandidates[c.NetworkType()] = append(a.localCandidates[c.NetworkType()], c)
		for _, remote := range a.remoteCandidates[c.NetworkType()] {
			a.addPair(c, remote)
		}
		s.adopted.Add(1)
		adopted = true
		p.mu.Unlock()
		a.candidateNotifier.EnqueueCandidate(c)
		a.requestConnectivityCheck()
		a.handleInbound(hit.msg, &c.candidateBase, hit.from)
	})
	return adopted
}
