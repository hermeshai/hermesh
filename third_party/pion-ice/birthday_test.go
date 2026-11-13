// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

func birthdayPhoneFixture(t *testing.T) (*punchExperiment, *birthdayPunchState) {
	t.Helper()
	e := newPunchExperiment(t)
	// Advertise both capabilities, but leave the Mac worker disabled: each
	// test controls its authenticated hit/order independently of random luck.
	require.NoError(t, e.public.AddExtension(CandidateExtension{Key: birthdayCapability, Value: "1"}))
	require.NoError(t, e.peer.AddExtension(CandidateExtension{Key: birthdayCapability, Value: "1"}))
	require.NoError(t, e.phone.loop.Run(e.phone.loop, func(context.Context) { e.phone.boundedPunchEnabled = true }))
	e.signalDirect(t)
	var s *birthdayPunchState
	require.Eventually(t, func() bool {
		_ = e.phone.loop.Run(e.phone.loop, func(context.Context) { s = e.phone.birthdayPunchState })
		return s != nil
	}, time.Second, time.Millisecond)
	return e, s
}

func birthdaySelectDirect(t *testing.T, e *punchExperiment) {
	t.Helper()
	require.NoError(t, e.phone.loop.Run(e.phone.loop, func(context.Context) {
		for _, p := range e.phone.checklist {
			if !birthdayRelayed(p) {
				p.state = CandidatePairStateSucceeded
				e.phone.setSelectedPair(p)
				return
			}
		}
		t.Error("missing direct candidate pair")
	}))
}

func TestBirthdayPoolStopsOnDirect(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, stage := range []string{"opening", "all ready attempts spent"} {
		t.Run(stage, func(t *testing.T) {
			e, s := birthdayPhoneFixture(t)
			if stage == "opening" {
				require.Eventually(t, func() bool { return s.created.Load() > 0 }, time.Second, time.Millisecond)
			} else {
				require.Eventually(t, func() bool { return s.ready.Load() == birthdayReadyCount }, 4*time.Second, time.Millisecond)
			}
			birthdaySelectDirect(t, e)
			require.Eventually(t, func() bool { return s.created.Load() == s.reclaimed.Load() && s.ctx.Err() != nil }, time.Second, time.Millisecond)
			require.Zero(t, s.adopted.Load())
			if stage == "opening" {
				require.Less(t, s.created.Load(), int32(birthdaySockets))
			}
		})
	}
}

func TestBirthdayPoolAdoptsAndReleasesWinner(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, ending := range []string{"same credentials restart", "close"} {
		t.Run(ending, func(t *testing.T) {
			e, s := birthdayPhoneFixture(t)
			require.Eventually(t, func() bool { return s.opened.Load() == birthdaySockets && s.ready.Load() > 0 }, 3*time.Second, time.Millisecond)
			before, err := e.phone.GetLocalCandidates()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			// Pion vnet allocates sequentially from 49152. These ports are
			// within the new 128-socket pool, beyond the original ICE sockets.
			// This forces a collision to test adoption, not random hit odds.
			_, _ = e.run(ctx, punchPorts(49200, 64), 64, time.Millisecond, e.generation(t))
			require.Eventually(t, e.direct, time.Second, time.Millisecond)
			require.Eventually(t, func() bool { return s.adopted.Load() == 1 && s.reclaimed.Load() == s.created.Load()-1 }, time.Second, time.Millisecond)
			after, err := e.phone.GetLocalCandidates()
			require.NoError(t, err)
			require.Len(t, after, len(before)+1)
			e.transfer(t)
			pair, err := e.phone.GetSelectedCandidatePair()
			require.NoError(t, err)
			address := pair.Local.addr().String()
			if ending == "same credentials restart" {
				u, p, err := e.phone.GetLocalUserCredentials()
				require.NoError(t, err)
				require.NoError(t, e.phone.Restart(u, p))
			} else {
				require.NoError(t, e.phone.Close())
			}
			rebound, err := e.v.net1.ListenPacket("udp4", address)
			require.NoError(t, err, "ICE must release the adopted socket too")
			require.NoError(t, rebound.Close())
		})
	}
}

func TestBirthdayRejectsHitAfterSameCredentialRestart(t *testing.T) {
	t.Cleanup(test.CheckRoutines(t))
	e := newPunchExperiment(t)
	var s *birthdayPunchState
	require.NoError(t, e.phone.loop.Run(e.phone.loop, func(context.Context) {
		s = e.phone.newBirthdayState(e.public.addrPort(), e.peer.addrPort().Addr(), birthdayLifetime)
	}))
	c, err := e.v.net1.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(vnetLocalIPB)})
	require.NoError(t, err)
	p := &birthdaySocketPool{conns: make(map[net.PacketConn]bool), state: s}
	require.True(t, p.add(c))
	u, password, err := e.phone.GetLocalUserCredentials()
	require.NoError(t, err)
	m := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
		stun.NewUsername(s.local+":"+s.remote), AttrControlled(1), PriorityAttr(100),
		stun.NewShortTermIntegrity(password), stun.Fingerprint)
	require.NoError(t, e.phone.Restart(u, password))
	require.False(t, e.phone.adoptBirthdaySocket(s, p, birthdaySocketHit{c, m, s.target}))
	locals, err := e.phone.GetLocalCandidates()
	require.NoError(t, err)
	require.Empty(t, locals)
	p.close()
	require.Zero(t, s.adopted.Load())
	require.Equal(t, int32(1), s.reclaimed.Load())
	birthdayAssertReleased(t, e, c)
}

func TestBirthdayReadyRequiresAuthenticatedKnownEndpoint(t *testing.T) {
	t.Cleanup(test.CheckRoutines(t))
	e := newPunchExperiment(t)
	require.NoError(t, e.peer.AddExtension(CandidateExtension{Key: birthdayCapability, Value: "1"}))
	e.signalDirect(t)
	require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
		e.machine.boundedPunchEnabled = true
		e.machine.boundedPunchState = boundedPunchState{generation: e.machine.gatherGeneration,
			localUfrag: e.machine.localUfrag, remoteUfrag: e.machine.remoteUfrag, done: true}
	}))
	var source, local Candidate
	require.Eventually(t, func() bool {
		_ = e.machine.loop.Run(e.machine.loop, func(context.Context) {
			for _, pair := range e.machine.checklist {
				if pair.Remote.addrPort().Addr() == e.peer.addrPort().Addr() && pair.RequestsReceived() > 0 &&
					pair.Remote.Type() != CandidateTypeRelay {
					local, source = pair.Local, pair.Remote
					break
				}
			}
		})
		return source != nil
	}, time.Second, time.Millisecond)
	send := func(target, ip string, remaining int, correctKey bool) {
		t.Helper()
		require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
			key := e.machine.localPwd
			if !correctKey {
				key = "not-the-session-key"
			}
			m := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(e.machine.localUfrag+":"+e.machine.remoteUfrag), AttrControlling(e.phone.tieBreaker),
				PriorityAttr(100), stun.NewSoftware(fmt.Sprintf("%s %s %s %d", birthdayReady, target, ip, remaining)),
				stun.NewShortTermIntegrity(key), stun.Fingerprint)
			// Same authentication/selector/hook path as an actual received STUN
			// request, including verification before the ready handler runs.
			e.machine.handleInbound(m, local, source.addrPort())
		}))
	}
	for _, invalid := range []string{"integrity", "foreign peer", "unknown base", "late ready"} {
		target, ip, remaining, key := e.public.addrPort().String(), e.peer.Address(), 15000, true
		switch invalid {
		case "integrity":
			key = false
		case "foreign peer":
			ip = "203.0.113.8"
		case "unknown base":
			target = netip.AddrPortFrom(e.public.addrPort().Addr(), e.public.addrPort().Port()+1).String()
		case "late ready":
			remaining = 9000
		}
		send(target, ip, remaining, key)
		require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
			require.Nil(t, e.machine.birthdayPunchState, invalid)
		}))
	}
	send(e.public.addrPort().String(), e.peer.Address(), 15000, true)
	var started *birthdayPunchState
	require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
		started = e.machine.birthdayPunchState
		require.NotNil(t, started)
		deadline, ok := started.ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 13*time.Second)
	}))
	send(e.public.addrPort().String(), e.peer.Address(), 15000, true)
	require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
		require.Same(t, started, e.machine.birthdayPunchState, "duplicate readiness cannot reset the budget")
		e.machine.stopBirthdayPunch("test-complete")
	}))
}

type birthdayProbeRecorder struct {
	Candidate
	ports map[int]bool
	times []time.Time
	wrong bool
	ip    netip.Addr
}

func (c *birthdayProbeRecorder) writeTo(raw []byte, dst Candidate) (int, error) {
	c.times = append(c.times, time.Now())
	if c.ports[dst.Port()] || dst.Port() < 1024 || dst.Port() > 65535 || dst.addrPort().Addr() != c.ip {
		c.wrong = true
	}
	c.ports[dst.Port()] = true
	return c.Candidate.writeTo(raw, dst)
}

func TestBirthdayProbeBudgetAndRoleChange(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, stop := range []string{"budget", "role change"} {
		t.Run(stop, func(t *testing.T) {
			e := newPunchExperiment(t)
			// Keep this isolated budget test on TURN even if random guessing
			// happens to hit a known address. Only its own peer IP is probed.
			e.v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
				return c.SourceAddr().String() != e.public.addr().String() ||
					c.DestinationAddr().(*net.UDPAddr).IP.String() != e.peer.Address()
			})
			var s *birthdayPunchState
			require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
				s = e.machine.newBirthdayState(e.public.addrPort(), e.peer.addrPort().Addr(), birthdayLifetime)
			}))
			base := &birthdayProbeRecorder{Candidate: e.base, ports: make(map[int]bool), ip: s.peerIP}
			done := make(chan struct{})
			go func() { e.machine.runBirthdayProbes(s, base); close(done) }()
			if stop == "role change" {
				require.Eventually(t, func() bool { return s.probes.Load() >= 3 }, time.Second, time.Millisecond)
				require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) { e.machine.setRole(true) }))
			}
			select {
			case <-done:
			case <-time.After(16 * time.Second):
				t.Fatal("probe worker exceeded its lifetime")
			}
			require.False(t, base.wrong)
			if stop == "budget" {
				require.Len(t, base.ports, birthdayProbes)
				require.Equal(t, int32(birthdayProbes), s.probes.Load())
			} else {
				require.Less(t, len(base.ports), birthdayProbes)
			}
			for i := 1; i < len(base.times); i++ {
				require.GreaterOrEqual(t, base.times[i].Sub(base.times[i-1]), birthdayInterval)
			}
		})
	}
}
