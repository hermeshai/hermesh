// SPDX-License-Identifier: MIT

//go:build !js

package ice

// This file is an isolated experiment, not a product feature. All packets use
// vnet. Guessed ports are never added to the ICE checklist: the existing
// authenticated peer-reflexive discovery and nomination must validate a path.

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

type punchExperiment struct {
	v                  *virtualNet
	machine, phone     *Agent
	base, public, peer Candidate
	in, out            *Conn
	wireProbes         atomic.Int32
}

func newPunchExperiment(t *testing.T) *punchExperiment {
	return newPunchExperimentWithNet(t, false, 20*time.Millisecond)
}

func newPunchExperimentWithNet(t *testing.T, optIn bool, machineInterval time.Duration) *punchExperiment {
	t.Helper()
	v, err := buildVNet(
		&vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrPortDependent},
		&vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent},
	)
	require.NoError(t, err)
	t.Cleanup(v.close)
	newAgent := func(n transport.Net, ip string) *Agent {
		if optIn && ip == vnetLocalIPA {
			n = &BoundedNATPunchNet{Net: n}
		}
		conn, err := n.ListenPacket("udp4", net.JoinHostPort(ip, "0"))
		require.NoError(t, err)
		mux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: conn, Net: n})
		t.Cleanup(func() { require.NoError(t, mux.Close()) })
		interval, zero := 20*time.Millisecond, time.Duration(0)
		if ip == vnetLocalIPA {
			interval = machineInterval
		}
		a, err := newAgentFromConfig(&AgentConfig{
			Net: n, NetworkTypes: []NetworkType{NetworkTypeUDP4}, MulticastDNSMode: MulticastDNSModeDisabled,
			UDPMux: mux, UDPMuxSrflx: mux, KeepaliveInterval: &interval, CheckInterval: &interval,
			HostAcceptanceMinWait: &zero, SrflxAcceptanceMinWait: &zero,
			PrflxAcceptanceMinWait: &zero, RelayAcceptanceMinWait: &zero,
			Urls: []*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort,
				Proto: stun.ProtoTypeUDP, Username: "user", Password: "pass"}},
		}, WithNet(n), WithRenomination(DefaultNominationValueGenerator()), WithAutomaticRenomination(interval))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, a.Close()) })
		return a
	}
	e := &punchExperiment{v: v, machine: newAgent(v.net0, vnetLocalIPA), phone: newAgent(v.net1, vnetLocalIPB)}
	ac, bc := gatherAndCollectCandidates(t, e.machine), gatherAndCollectCandidates(t, e.phone)
	for _, c := range ac {
		if c.Type() == CandidateTypeHost {
			e.base = c
		}
		if c.Type() == CandidateTypeServerReflexive {
			e.public = c
		}
	}
	for _, c := range bc {
		if c.Type() == CandidateTypeServerReflexive {
			e.peer = c
		}
	}
	require.NotNil(t, e.base)
	require.NotNil(t, e.public)
	require.NotNil(t, e.peer)
	require.Equal(t, e.base.Port(), e.public.RelatedAddress().Port)
	for _, side := range []struct {
		to *Agent
		cs []Candidate
	}{{e.machine, bc}, {e.phone, ac}} {
		for _, c := range side.cs {
			if c.Type() == CandidateTypeRelay {
				punchSignal(t, side.to, c)
			}
		}
	}
	au, ap, err := e.machine.GetLocalUserCredentials()
	require.NoError(t, err)
	bu, bp, err := e.phone.GetLocalUserCredentials()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	accepted := make(chan *Conn, 1)
	go func() { c, _ := e.machine.Accept(ctx, bu, bp); accepted <- c }()
	e.out, err = e.phone.Dial(ctx, au, ap)
	e.in = <-accepted
	require.NoError(t, err)
	require.NotNil(t, e.in)
	require.False(t, e.direct())
	e.transfer(t)
	// Count only the experiment's tagged authenticated Binding requests, not
	// TURN traffic, ICE retransmits or successful-path keepalives.
	v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
		m := &stun.Message{Raw: c.UserData()}
		if m.Decode() == nil && m.Type == stun.BindingRequest {
			software, _ := m.Get(stun.AttrSoftware)
			if string(software) == "bounded-vnet-experiment" {
				e.wireProbes.Add(1)
			}
		}
		return true
	})
	return e
}

func punchSignal(t *testing.T, to *Agent, c Candidate) {
	t.Helper()
	remote, err := c.copy()
	require.NoError(t, err)
	require.NoError(t, to.AddRemoteCandidate(remote))
	require.Eventually(t, func() bool {
		found := false
		err := to.loop.Run(to.loop, func(context.Context) {
			found = to.findRemoteCandidate(c.NetworkType(), c.addrPort()) != nil
		})
		return err == nil && found
	}, time.Second, time.Millisecond)
}

func (e *punchExperiment) signalDirect(t *testing.T) {
	punchSignal(t, e.machine, e.peer)
	punchSignal(t, e.phone, e.public)
}

func (e *punchExperiment) direct() bool {
	p, _ := e.phone.GetSelectedCandidatePair()
	return p != nil && p.Local.Type() != CandidateTypeRelay && p.Remote.Type() != CandidateTypeRelay
}

func (e *punchExperiment) transfer(t *testing.T) {
	t.Helper()
	for _, pair := range [][2]*Conn{{e.in, e.out}, {e.out, e.in}} {
		require.NoError(t, pair[1].SetReadDeadline(time.Now().Add(time.Second)))
		_, err := pair[0].Write([]byte("frame and input"))
		require.NoError(t, err)
		buf := make([]byte, 64)
		n, err := pair[1].Read(buf)
		require.NoError(t, err)
		require.Equal(t, "frame and input", string(buf[:n]))
	}
}

// run sends at most one request per tick and checks the captured ICE credentials
// inside the agent loop immediately before each write. Restart therefore makes
// an old generation inert. It never creates candidates, pairs or transactions;
// regular authenticated inbound ICE requests discover the usable peer address.
func (e *punchExperiment) run(ctx context.Context, ports []int, budget int, interval time.Duration, generation string) (int, string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sent := 0
	for _, port := range ports {
		if sent >= budget {
			return sent, "budget"
		}
		select {
		case <-ctx.Done():
			return sent, "canceled"
		case <-ticker.C:
		}
		stop := ""
		err := e.machine.loop.Run(ctx, func(context.Context) {
			if ctx.Err() != nil {
				stop = "canceled"
				return
			}
			if e.machine.localUfrag+":"+e.machine.remoteUfrag != generation {
				stop = "generation"
				return
			}
			p := e.machine.getSelectedPair()
			if p != nil && p.Local.Type() != CandidateTypeRelay && p.Remote.Type() != CandidateTypeRelay {
				stop = "direct"
				return
			}
			remote, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
				Network: "udp4", Address: e.peer.Address(), Port: port, Component: ComponentRTP,
			})
			if err != nil {
				stop = "invalid-port"
				return
			}
			msg := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(e.machine.remoteUfrag+":"+e.machine.localUfrag),
				AttrControlled(e.machine.tieBreaker), PriorityAttr(e.base.prflxPriority()),
				stun.NewSoftware("bounded-vnet-experiment"),
				stun.NewShortTermIntegrity(e.machine.remotePwd), stun.Fingerprint)
			e.machine.sendSTUN(msg, e.base, remote)
			sent++
		})
		if err != nil {
			return sent, "closed-or-canceled"
		}
		if stop != "" {
			return sent, stop
		}
	}
	return sent, "exhausted"
}

func (e *punchExperiment) generation(t *testing.T) string {
	t.Helper()
	var generation string
	require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
		generation = e.machine.localUfrag + ":" + e.machine.remoteUfrag
	}))
	return generation
}

func punchPorts(start, count int) []int {
	ports := make([]int, count)
	for i := range ports {
		ports[i] = start + i
	}
	return ports
}

// Allocating unrelated mappings models an unobserved allocation gap. vnet's
// allocator is linear, so this is NOT a full random NAT implementation: for a
// single hidden peer mapping it gives an exactly controlled unknown port.
func (e *punchExperiment) consumeMappings(t *testing.T, count int) {
	t.Helper()
	conn, err := e.v.net1.ListenPacket("udp4", net.JoinHostPort(vnetLocalIPB, "0"))
	require.NoError(t, err)
	defer conn.Close()
	var seen atomic.Int32
	e.v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
		if string(c.UserData()) == "allocation-gap" {
			seen.Add(1)
			return false
		}
		return true
	})
	for i := 0; i < count; i++ {
		_, err := conn.WriteTo([]byte("allocation-gap"), &net.UDPAddr{IP: net.ParseIP(vnetSTUNServerIP), Port: 10000 + i})
		require.NoError(t, err)
	}
	if count > 0 {
		require.Eventually(t, func() bool { return seen.Load() == int32(count) }, time.Second, time.Millisecond)
	}
}

func TestNATPunchExperimentLinear(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("neighbor ports recover authenticated direct data", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.signalDirect(t)
		require.Never(t, e.direct, 60*time.Millisecond, 5*time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sent, reason := e.run(ctx, punchPorts(e.peer.Port()+1, 32), 32, 10*time.Millisecond, e.generation(t))
		require.Eventually(t, e.direct, time.Second, 5*time.Millisecond)
		require.Equal(t, "direct", reason)
		require.Less(t, sent, 32)
		require.Eventually(t, func() bool { return e.wireProbes.Load() == int32(sent) }, time.Second, time.Millisecond)
		e.transfer(t)
		t.Logf("direct=true extra_requests=%d stop=%s", sent, reason)
	})
	t.Run("hidden allocation outside window keeps working relay", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.consumeMappings(t, 64)
		e.signalDirect(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sent, reason := e.run(ctx, punchPorts(e.peer.Port()+1, 64), 32, 5*time.Millisecond, e.generation(t))
		require.Equal(t, 32, sent)
		require.Equal(t, "budget", reason)
		require.Never(t, e.direct, 100*time.Millisecond, 5*time.Millisecond)
		e.transfer(t)
		t.Logf("direct=false extra_requests=%d stop=%s", sent, reason)
	})
}

func TestNATPunchExperimentCancellation(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, stop := range []string{"deadline", "restart"} {
		t.Run(stop, func(t *testing.T) {
			e := newPunchExperiment(t)
			generation := e.generation(t)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
			defer cancel()
			if stop == "restart" {
				require.NoError(t, e.machine.Restart("", ""))
			}
			sent, reason := e.run(ctx, punchPorts(e.peer.Port()+100, 32), 32, 10*time.Millisecond, generation)
			if stop == "restart" {
				require.Zero(t, sent)
				require.Equal(t, "generation", reason)
			} else {
				require.LessOrEqual(t, sent, 3)
				require.Contains(t, reason, "canceled")
			}
			t.Logf("%s extra_requests=%d stop=%s", stop, sent, reason)
		})
	}
}

func TestNATPunchExperimentUnknownPortCoverage(t *testing.T) {
	// Exhaustive unknown-port distribution, not a claim that real mobile NATs
	// draw uniformly or preserve mappings for this long. Both the candidate
	// list and budget are identical in every trial; no oracle feeds the runner.
	defer test.CheckRoutines(t)()
	const trials, budget = 16, 8
	hits, packets := 0, 0
	for gap := 0; gap < trials; gap++ {
		t.Run(fmt.Sprintf("gap_%02d", gap), func(t *testing.T) {
			e := newPunchExperiment(t)
			e.consumeMappings(t, gap)
			e.signalDirect(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sent, _ := e.run(ctx, punchPorts(e.peer.Port()+1, budget), budget, 5*time.Millisecond, e.generation(t))
			packets += sent
			deadline := time.Now().Add(150 * time.Millisecond)
			for !e.direct() && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if e.direct() {
				hits++
			}
			e.transfer(t)
			t.Logf("gap=%d direct=%t extra_requests=%d", gap, e.direct(), sent)
		})
	}
	require.Positive(t, hits)
	require.Less(t, hits, trials)
	require.LessOrEqual(t, packets, trials*budget)
	t.Logf("unknown allocation gap: %d/%d direct; %d additional requests; max=%d/trial", hits, trials, packets, budget)
}

func TestNATPunchExperimentUniformPortBound(t *testing.T) {
	// Exact coverage under a stated hypothetical model, not a mobile-NAT
	// measurement: one hidden port uniform over the unprivileged UDP range.
	const portCount = 65536 - 1024
	probed := make(map[int]bool)
	for delta := 1; delta <= boundedPunchRadius; delta++ {
		probed[30000+delta] = true
		probed[30000-delta] = true
	}
	hits := 0
	for hidden := 1024; hidden <= 65535; hidden++ {
		if probed[hidden] {
			hits++
		}
	}
	require.Equal(t, 32, hits)
	t.Logf("one 32-port window: %d/%d = %.5f%%; union bound for 128 attempts = %.5f%%",
		hits, portCount, 100*float64(hits)/portCount, 100*float64(boundedPunchMaxPackets)/portCount)
}
