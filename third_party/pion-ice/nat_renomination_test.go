// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

// Establish real data through TURN first, then signal the public addresses
// of two NATed peers. Automatic renomination must move to the NAT direct
// path and continue carrying data, even though neither end is a LAN host.
func TestNATDirectReplacesRelay(t *testing.T) {
	t.Run("stable mappings", func(t *testing.T) {
		nat := &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
		testNATRelayUpgrade(t, nat, nat, true)
	})
	phone := &vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
	t.Run("phone mapping changes machine admits same IP", func(t *testing.T) {
		machine := &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrDependent}
		testNATRelayUpgrade(t, machine, phone, true)
	})
	t.Run("phone mapping changes machine requires exact port", func(t *testing.T) {
		machine := &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
		testNATRelayUpgrade(t, machine, phone, false)
	})
}

func testNATRelayUpgrade(t *testing.T, machine, phone *vnet.NATType, direct bool) {
	t.Helper()
	defer test.CheckRoutines(t)()
	v, err := buildVNet(machine, phone)
	require.NoError(t, err)
	defer v.close()
	newAgent := func(n transport.Net) *Agent {
		interval := 100 * time.Millisecond
		a, err := newAgentFromConfig(&AgentConfig{
			Net: n, NetworkTypes: []NetworkType{NetworkTypeUDP4}, MulticastDNSMode: MulticastDNSModeDisabled,
			KeepaliveInterval: &interval, CheckInterval: &interval,
			Urls: []*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort, Proto: stun.ProtoTypeUDP, Username: "user", Password: "pass"}},
		}, WithRenomination(DefaultNominationValueGenerator()), WithAutomaticRenomination(interval))
		require.NoError(t, err)
		return a
	}
	a, b := newAgent(v.net0), newAgent(v.net1)
	defer a.Close()
	defer b.Close()
	ac, bc := gatherAndCollectCandidates(t, a), gatherAndCollectCandidates(t, b)
	add := func(to *Agent, candidates []Candidate, kind CandidateType) {
		count := 0
		for _, c := range candidates {
			if c.Type() == kind {
				remote, err := c.copy()
				require.NoError(t, err)
				require.NoError(t, to.AddRemoteCandidate(remote))
				count++
			}
		}
		require.Positive(t, count)
	}
	add(a, bc, CandidateTypeRelay)
	add(b, ac, CandidateTypeRelay)
	au, ap, err := a.GetLocalUserCredentials()
	require.NoError(t, err)
	bu, bp, err := b.GetLocalUserCredentials()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accepted := make(chan *Conn, 1)
	go func() { c, _ := a.Accept(ctx, bu, bp); accepted <- c }()
	out, err := b.Dial(ctx, au, ap)
	in := <-accepted
	require.NoError(t, err)
	require.NotNil(t, in)
	p, err := b.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.True(t, p.Local.Type() == CandidateTypeRelay || p.Remote.Type() == CandidateTypeRelay)
	transfer := func() {
		require.NoError(t, in.SetReadDeadline(time.Now().Add(time.Second)))
		_, err := out.Write([]byte("frame"))
		require.NoError(t, err)
		buf := make([]byte, 128)
		n, err := in.Read(buf)
		require.NoError(t, err)
		require.Equal(t, "frame", string(buf[:n]))
	}
	transfer()
	add(a, bc, CandidateTypeServerReflexive)
	add(b, ac, CandidateTypeServerReflexive)
	isDirect := func() bool {
		p, _ := b.GetSelectedCandidatePair()
		return p != nil && p.Local.Type() != CandidateTypeRelay && p.Remote.Type() != CandidateTypeRelay
	}
	if direct {
		require.Eventually(t, isDirect, 5*time.Second, 10*time.Millisecond)
	} else {
		// Standard ICE cannot discover an unadvertised destination-specific
		// port through an exact-port filter. TURN must continue carrying data.
		require.Never(t, isDirect, 3*time.Second, 10*time.Millisecond)
	}
	transfer()
}
