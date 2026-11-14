// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

// The Mac's STUN mapping differs from its mapping toward the phone. Isolate
// the temporary socket path so ordinary ICE cannot connect first and hide
// whether the production pool reader accepted and adopted the reflexive hit.
func newBirthdayReflexiveFixture(t *testing.T, phoneMapping, phoneFilter vnet.EndpointDependencyType) (*punchExperiment, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	v, err := buildVNet(
		&vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent},
		&vnet.NATType{MappingBehavior: phoneMapping, FilteringBehavior: phoneFilter},
	)
	require.NoError(t, err)
	t.Cleanup(v.close)
	makeAgent := func(n transport.Net, ip string) *Agent {
		conn, err := n.ListenPacket("udp4", net.JoinHostPort(ip, "0"))
		require.NoError(t, err)
		mux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: conn, Net: n})
		t.Cleanup(func() { require.NoError(t, mux.Close()) })
		interval, zero := 20*time.Millisecond, time.Duration(0)
		a, err := newAgentFromConfig(&AgentConfig{
			Net: n, NetworkTypes: []NetworkType{NetworkTypeUDP4}, MulticastDNSMode: MulticastDNSModeDisabled,
			UDPMux: mux, UDPMuxSrflx: mux, KeepaliveInterval: &interval, CheckInterval: &interval,
			HostAcceptanceMinWait: &zero, SrflxAcceptanceMinWait: &zero, PrflxAcceptanceMinWait: &zero, RelayAcceptanceMinWait: &zero,
			Urls: []*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort,
				Proto: stun.ProtoTypeUDP, Username: "user", Password: "pass"}},
		}, WithRenomination(DefaultNominationValueGenerator()), WithAutomaticRenomination(interval))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, a.Close()) })
		return a
	}
	e := &punchExperiment{v: v, machine: makeAgent(v.net0, vnetLocalIPA), phone: makeAgent(v.net1, vnetLocalIPB)}
	mac, phone := gatherAndCollectCandidates(t, e.machine), gatherAndCollectCandidates(t, e.phone)
	for _, c := range mac {
		if c.Type() == CandidateTypeHost {
			e.base = c
		}
		if c.Type() == CandidateTypeServerReflexive {
			e.public = c
		}
	}
	for _, c := range phone {
		if c.Type() == CandidateTypeServerReflexive {
			e.peer = c
		}
	}
	require.NotNil(t, e.base)
	require.NotNil(t, e.public)
	require.NotNil(t, e.peer)
	poolPort, macSource := &atomic.Int32{}, &atomic.Int32{}
	v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
		source, destination := c.SourceAddr().(*net.UDPAddr), c.DestinationAddr().(*net.UDPAddr)
		m := &stun.Message{Raw: c.UserData()}
		if source.IP.String() == vnetGlobalIPB && m.Decode() == nil {
			software, _ := m.Get(stun.AttrSoftware)
			if string(software) == "birthday-reflexive-open" {
				poolPort.Store(int32(source.Port))
			}
		}
		if source.IP.String() == vnetGlobalIPA && destination.IP.String() == vnetGlobalIPB {
			if int32(destination.Port) != poolPort.Load() {
				return false
			}
			macSource.Store(int32(source.Port))
		}
		return true
	})
	for _, side := range []struct {
		to *Agent
		cs []Candidate
	}{{e.machine, phone}, {e.phone, mac}} {
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
	e.transfer(t)
	punchSignal(t, e.phone, e.public)
	return e, poolPort, macSource
}

func TestBirthdayAuthenticatedReflexivePort(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, scenario := range []struct {
		name       string
		mapping    vnet.EndpointDependencyType
		filter     vnet.EndpointDependencyType
		wrongIP    bool
		wrongKey   bool
		wantHit    bool
		wantDirect bool
	}{
		{"phone stable mapping admits changed Mac port", vnet.EndpointIndependent, vnet.EndpointIndependent, false, false, true, true},
		{"another source IP rejected despite valid key", vnet.EndpointIndependent, vnet.EndpointIndependent, true, false, false, false},
		{"same source IP needs current key", vnet.EndpointIndependent, vnet.EndpointIndependent, false, true, false, false},
		{"both dependent mappings with permissive phone filter", vnet.EndpointAddrPortDependent, vnet.EndpointIndependent, false, false, true, false},
		{"both dependent mappings with exact phone filter", vnet.EndpointAddrPortDependent, vnet.EndpointAddrPortDependent, false, false, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e, mappedPort, actualMacPort := newBirthdayReflexiveFixture(t, scenario.mapping, scenario.filter)
			var s *birthdayPunchState
			var phonePassword, machinePassword string
			require.NoError(t, e.phone.loop.Run(e.phone.loop, func(context.Context) {
				target := e.public.addrPort()
				if scenario.wrongIP {
					target = netip.AddrPortFrom(netip.MustParseAddr("27.1.1.99"), target.Port())
				}
				s = e.phone.newBirthdayState(target, e.peer.addrPort().Addr(), 3*time.Second)
				phonePassword, machinePassword = e.phone.localPwd, e.phone.remotePwd
			}))
			p := &birthdaySocketPool{conns: make(map[net.PacketConn]bool), state: s, hit: make(chan birthdaySocketHit, 1)}
			c, err := e.v.net1.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(vnetLocalIPB)})
			require.NoError(t, err)
			require.True(t, p.add(c))
			p.readers.Add(1)
			go func() { defer p.readers.Done(); e.phone.readBirthdaySocket(s, p, c, phonePassword) }()
			t.Cleanup(func() { s.cancel(); p.close(); p.readers.Wait() })
			opening := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(s.remote+":"+s.local), AttrControlling(e.phone.tieBreaker), PriorityAttr(100),
				stun.NewSoftware("birthday-reflexive-open"), stun.NewShortTermIntegrity(machinePassword), stun.Fingerprint)
			_, err = c.WriteTo(opening.Raw, e.public.addr())
			require.NoError(t, err)
			require.Eventually(t, func() bool { return mappedPort.Load() > 0 }, time.Second, time.Millisecond)
			peer, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
				Network: "udp4", Address: vnetGlobalIPB, Port: int(mappedPort.Load()), Component: ComponentRTP,
			})
			require.NoError(t, err)
			require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
				password := phonePassword
				if scenario.wrongKey {
					password = "another-session-key"
				}
				m := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
					stun.NewUsername(s.local+":"+s.remote), AttrControlled(e.machine.tieBreaker),
					PriorityAttr(e.base.prflxPriority()), stun.NewShortTermIntegrity(password), stun.Fingerprint)
				e.machine.sendSTUN(m, e.base, peer)
			}))
			require.Eventually(t, func() bool { return actualMacPort.Load() > 0 }, time.Second, time.Millisecond)
			require.NotEqual(t, int32(e.public.Port()), actualMacPort.Load(), "APDM must change the Mac's source port")
			if !scenario.wantHit {
				require.Never(t, func() bool { return len(p.hit) > 0 }, 100*time.Millisecond, time.Millisecond)
				if scenario.filter == vnet.EndpointIndependent {
					require.Equal(t, int32(1), s.rejected.Load())
				} else {
					require.Zero(t, s.received.Load(), "exact-port NAT drops the packet before application validation")
				}
				require.False(t, e.direct())
				e.transfer(t)
				return
			}
			var hit birthdaySocketHit
			select {
			case hit = <-p.hit:
			case <-time.After(200 * time.Millisecond):
				t.Fatalf("authenticated same-IP request reached pool but was not accepted: received=%d rejected=%d", s.received.Load(), s.rejected.Load())
			}
			require.True(t, e.phone.adoptBirthdaySocket(s, p, hit))
			s.cancel()
			p.close()
			p.readers.Wait()
			if !scenario.wantDirect {
				require.Never(t, e.direct, 150*time.Millisecond, time.Millisecond)
				require.NoError(t, e.phone.loop.Run(e.phone.loop, func(context.Context) {
					reflexive := e.phone.findRemoteCandidate(NetworkTypeUDP4, hit.from)
					require.NotNil(t, reflexive)
					require.Equal(t, CandidateTypePeerReflexive, reflexive.Type())
				}))
				e.transfer(t)
				return
			}
			require.Eventually(t, func() bool {
				macPair, _ := e.machine.GetSelectedCandidatePair()
				return e.direct() && macPair != nil && !birthdayRelayed(macPair)
			}, time.Second, time.Millisecond)
			pair, err := e.phone.GetSelectedCandidatePair()
			require.NoError(t, err)
			require.Equal(t, CandidateTypePeerReflexive, pair.Remote.Type())
			require.Equal(t, int(actualMacPort.Load()), pair.Remote.Port())
			require.NotEqual(t, e.public.Port(), pair.Remote.Port())
			e.transfer(t)
		})
	}
}
