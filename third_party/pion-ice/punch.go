// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
)

const (
	boundedPunchMaxPackets = 128
	boundedPunchInterval   = 40 * time.Millisecond
	boundedPunchDuration   = 6 * time.Second
	boundedPunchRadius     = 16
)

type boundedPunchTarget struct {
	local, remote Candidate
}

type boundedPunchState struct {
	generation    uint64
	localUfrag    string
	remoteUfrag   string
	started, last time.Time
	targets       []boundedPunchTarget
	next, sent    int
	done          bool
	running       bool
	attempted     map[string]bool
}

// BoundedNATPunchNet explicitly opts a transport.Net into the same experimental
// behavior as WithBoundedNATPunch, for users whose API only exposes WithNet.
// Pass a *BoundedNATPunchNet to WithNet to enable the opt-in.
type BoundedNATPunchNet struct {
	transport.Net
	// Report receives address/port and budget diagnostics, never ICE credentials.
	Report func(string)
}

func (a *Agent) reportBoundedPunch(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	a.log.Infof("%s", message)
	if n, ok := a.net.(*BoundedNATPunchNet); ok && n.Report != nil {
		n.Report(message)
	}
}

// startBoundedPunch is called by the ordinary ICE contact loop. Its dedicated
// timer schedules only bounded probes, leaving normal ICE check frequency alone.
func (a *Agent) startBoundedPunch() {
	a.boundedPunch()
	s := &a.boundedPunchState
	if s.done || s.running || len(s.targets) == 0 {
		return
	}
	s.running = true
	generation, localUfrag, remoteUfrag := a.gatherGeneration, a.localUfrag, a.remoteUfrag
	ctx, cancel := context.WithTimeout(a.loop, boundedPunchDuration-time.Since(s.started))
	go func() {
		defer cancel()
		ticker := time.NewTicker(boundedPunchInterval)
		defer ticker.Stop()
		reason := "deadline-or-close"
		defer func() {
			_ = a.loop.Run(a.loop, func(context.Context) {
				state := &a.boundedPunchState
				if state.generation == generation && state.localUfrag == localUfrag && state.remoteUfrag == remoteUfrag {
					state.running = false
					a.finishBoundedPunch(reason)
				}
			})
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			stop := false
			err := a.loop.Run(ctx, func(context.Context) {
				if a.gatherGeneration != generation || a.localUfrag != localUfrag || a.remoteUfrag != remoteUfrag {
					reason, stop = "generation", true
					return
				}
				a.boundedPunch()
				stop = a.boundedPunchState.done
			})
			if err != nil || stop {
				return
			}
		}
	}()
}

func (a *Agent) finishBoundedPunch(reason string) {
	s := &a.boundedPunchState
	if !s.done && !s.started.IsZero() {
		a.reportBoundedPunch("Bounded NAT punch stopped: targets=%d attempted=%d sent=%d reason=%s", len(s.targets), len(s.attempted), s.sent, reason)
	}
	s.done = true
	s.targets = nil
}

// WithBoundedNATPunch opts into a short, rate-limited search of nearby IPv4
// ports when the controlled peer has selected TURN. Signaling must authenticate
// the remote peer and its candidates. Both peers must support renomination.
// This can help sequential destination-dependent mappings; it cannot reliably
// find uniformly random mappings and does not replace TURN.
func WithBoundedNATPunch() AgentOption {
	return func(a *Agent) error {
		if a.constructed {
			return ErrAgentOptionNotUpdatable
		}
		a.boundedPunchEnabled = true
		return nil
	}
}

// boundedPunch runs only on the agent task loop. It does not add guessed
// candidates or accept responses from unknown peers. Ordinary authenticated
// inbound ICE checks must discover, validate and nominate a direct path.
func (a *Agent) boundedPunch() {
	if !a.boundedPunchEnabled || !a.automaticRenomination || !a.enableRenomination ||
		a.isControlling.Load() || a.loop.Err() != nil || a.remoteUfrag == "" || a.remotePwd == "" {
		return
	}
	s := &a.boundedPunchState
	if s.generation != a.gatherGeneration || s.localUfrag != a.localUfrag || s.remoteUfrag != a.remoteUfrag {
		a.finishBoundedPunch("generation")
		*s = boundedPunchState{generation: a.gatherGeneration, localUfrag: a.localUfrag, remoteUfrag: a.remoteUfrag}
	}
	if s.done {
		return
	}
	selected := a.getSelectedPair()
	if selected == nil {
		return
	}
	if selected.Local.Type() != CandidateTypeRelay && selected.Remote.Type() != CandidateTypeRelay {
		a.finishBoundedPunch("direct")
		return
	}
	now := time.Now()
	if s.started.IsZero() {
		s.targets = a.boundedPunchTargets()
		if len(s.targets) == 0 {
			return // Trickle may still deliver the public address.
		}
		s.started = now
		s.attempted = make(map[string]bool)
		a.reportBoundedPunch("Bounded NAT punch started: targets=%d packet_limit=%d", len(s.targets), boundedPunchMaxPackets)
		for _, target := range s.targets {
			base := target.local.addrPort().String()
			if target.local.Type() == CandidateTypeServerReflexive && target.local.RelatedAddress() != nil {
				base = fmt.Sprintf("%s:%d", target.local.RelatedAddress().Address, target.local.RelatedAddress().Port)
			}
			a.reportBoundedPunch("Bounded NAT punch target: base=%s seed=%s kind=%s radius=%d", base,
				target.remote.addrPort(), target.remote.Type(), boundedPunchRadius)
		}
	}
	if s.sent >= boundedPunchMaxPackets || now.Sub(s.started) >= boundedPunchDuration ||
		s.next >= len(s.targets)*2*boundedPunchRadius {
		a.finishBoundedPunch("budget-or-deadline")
		return
	}
	if !s.last.IsZero() && now.Sub(s.last) < boundedPunchInterval {
		return
	}
	// Round-robin bases/seeds at each distance, preferring the next port.
	for s.next < len(s.targets)*2*boundedPunchRadius {
		target := s.targets[s.next%len(s.targets)]
		step := s.next / len(s.targets)
		delta := step/2 + 1
		if step%2 != 0 {
			delta = -delta
		}
		s.next++
		port := target.remote.Port() + delta
		if port < 1024 || port > 65535 {
			continue
		}
		remote, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
			Network: "udp4", Address: target.remote.Address(), Port: port, Component: target.local.Component(),
		})
		if err != nil {
			continue
		}
		key := target.local.ID() + "/" + remote.addrPort().String()
		if s.attempted[key] {
			continue
		}
		msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
			stun.NewUsername(a.remoteUfrag+":"+a.localUfrag), AttrControlled(a.tieBreaker),
			PriorityAttr(target.local.prflxPriority()), stun.NewShortTermIntegrity(a.remotePwd), stun.Fingerprint)
		if err != nil {
			return
		}
		s.attempted[key] = true
		s.last = now
		if n, err := target.local.writeTo(msg.Raw, remote); err == nil && n == len(msg.Raw) {
			s.sent++
		}
		return
	}
}

func (a *Agent) boundedPunchTargets() []boundedPunchTarget {
	// A successful public STUN mapping is required before spending probes on
	// a base. This excludes unproven VM/bridge host interfaces. Prefer the
	// corresponding host alias and deduplicate all srflx aliases of its socket.
	bases := make([]Candidate, 0, 2)
	baseKeys := make(map[string]bool)
	for _, physical := range []bool{true, false} {
		for _, c := range a.localCandidates[NetworkTypeUDP4] {
			if c.Type() != CandidateTypeServerReflexive || len(bases) == 2 {
				continue
			}
			related := c.RelatedAddress()
			if related == nil {
				continue
			}
			ip, err := netip.ParseAddr(related.Address)
			if err != nil || ip.IsUnspecified() == physical || related.Port <= 0 || related.Port > 65535 ||
				ip.IsLoopback() || ip.IsLinkLocalUnicast() || !boundedPunchPublicIP(c.addrPort().Addr()) {
				continue
			}
			key := netip.AddrPortFrom(ip.Unmap(), uint16(related.Port)).String()
			if baseKeys[key] {
				continue
			}
			baseKeys[key] = true
			base := c
			for _, host := range a.localCandidates[NetworkTypeUDP4] {
				if host.Type() == CandidateTypeHost && host.Address() == related.Address &&
					host.Port() == related.Port && host.Component() == c.Component() {
					base = host
					break
				}
			}
			bases = append(bases, base)
		}
	}
	seeds := a.boundedPunchSeeds()
	targets := make([]boundedPunchTarget, 0, len(bases)*len(seeds))
	for _, remote := range seeds {
		for _, local := range bases {
			if local.Component() == remote.Component() {
				targets = append(targets, boundedPunchTarget{local, remote})
			}
		}
	}
	return targets
}

func boundedPunchPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && ip.As4()[0] != 0 &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) &&
		!netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

// Keep one advertised mapping, then spend the second slot on a different
// observed port window instead of another nearly identical STUN mapping.
func (a *Agent) boundedPunchSeeds() []Candidate {
	relayIPs := make(map[netip.Addr]bool)
	for _, c := range a.remoteCandidates[NetworkTypeUDP4] {
		if c.Type() == CandidateTypeRelay {
			relayIPs[c.addrPort().Addr().Unmap()] = true
		}
	}
	eligible := make([]Candidate, 0)
	seen := make(map[netip.AddrPort]bool)
	for _, kind := range []CandidateType{CandidateTypeServerReflexive, CandidateTypePeerReflexive} {
		for _, c := range a.remoteCandidates[NetworkTypeUDP4] {
			addr := c.addrPort()
			if c.Type() != kind || !boundedPunchPublicIP(addr.Addr()) || relayIPs[addr.Addr().Unmap()] || seen[addr] {
				continue
			}
			if kind == CandidateTypePeerReflexive {
				// A signaled prflx label alone does not prove an authenticated
				// observation. RequestsReceived is updated after ICE integrity
				// validation, including when the request arrived through TURN.
				verified := false
				for _, pair := range a.checklist {
					if pair.Remote.Equal(c) && pair.RequestsReceived() > 0 {
						verified = true
						break
					}
				}
				if !verified {
					continue
				}
			}
			seen[addr] = true
			eligible = append(eligible, c)
		}
	}
	if len(eligible) < 2 {
		return eligible
	}
	first := eligible[0]
	var second Candidate
	bestGain, bestSameIP := -1, false
	for _, c := range eligible[1:] {
		sameIP := c.addrPort().Addr() == first.addrPort().Addr()
		gain := boundedPunchWindowGain(c, first)
		if second == nil || (sameIP && !bestSameIP) || (sameIP == bestSameIP &&
			(gain > bestGain || (gain == bestGain && c.Type() == CandidateTypePeerReflexive && second.Type() != CandidateTypePeerReflexive))) {
			second, bestGain, bestSameIP = c, gain, sameIP
		}
	}
	return []Candidate{first, second}
}

func boundedPunchWindowGain(candidate, first Candidate) int {
	gain := 0
	for delta := -boundedPunchRadius; delta <= boundedPunchRadius; delta++ {
		port := candidate.Port() + delta
		if delta == 0 || port < 1024 || port > 65535 {
			continue
		}
		distance := port - first.Port()
		if candidate.addrPort().Addr() == first.addrPort().Addr() && distance != 0 &&
			distance >= -boundedPunchRadius && distance <= boundedPunchRadius {
			continue
		}
		gain++
	}
	return gain
}
