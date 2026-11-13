// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

// The system route cannot reach STUN or the peer (as with a VPN that drops
// UDP), but a socket bound to the physical interface can. Both ends are
// behind NATs that only admit packets from a destination they contacted.
type blockedDefaultRoute struct{ transport.Net }

func (n blockedDefaultRoute) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, addr)
	if err == nil && (addr == nil || len(addr.IP) == 0 || addr.IP.IsUnspecified()) {
		return droppedUDP{c}, nil
	}
	return c, err
}

type droppedUDP struct{ transport.UDPConn }

func (c droppedUDP) WriteTo(b []byte, _ net.Addr) (int, error)        { return len(b), nil }
func (c droppedUDP) WriteToUDP(b []byte, _ *net.UDPAddr) (int, error) { return len(b), nil }

func TestHostSocketDiscoversPublicAddressAndConnects(t *testing.T) {
	t.Run("stable mappings with port filters", func(t *testing.T) {
		nat := &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
		testHostSocketNAT(t, nat, nat)
	})
	t.Run("destination dependent phone mapping", func(t *testing.T) {
		machine := &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrDependent}
		phone := &vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
		testHostSocketNAT(t, machine, phone)
	})
}

func testHostSocketNAT(t *testing.T, machine, phone *vnet.NATType) {
	t.Helper()
	defer test.CheckRoutines(t)()
	v, err := buildVNet(machine, phone)
	require.NoError(t, err)
	defer v.close()
	newAgent := func(n transport.Net) *Agent {
		a, err := NewAgentWithOptions(
			WithNet(blockedDefaultRoute{n}),
			WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort, Proto: stun.ProtoTypeUDP}}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithCandidateTypes([]CandidateType{CandidateTypeHost, CandidateTypeServerReflexive}),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithSTUNGatherTimeout(100*time.Millisecond),
		)
		require.NoError(t, err)
		return a
	}
	a, b := newAgent(v.net0), newAgent(v.net1)
	defer a.Close()
	defer b.Close()
	var in, out *Conn
	for run := 0; run < 2; run++ {
		if run > 0 {
			require.NoError(t, a.Restart("", ""))
			require.NoError(t, b.Restart("", ""))
		}
		gather := func(agent *Agent) Candidate {
			candidates := gatherAndCollectCandidates(t, agent)
			for _, c := range candidates {
				if c.Type() != CandidateTypeServerReflexive {
					continue
				}
				for _, host := range candidates {
					if host.Type() == CandidateTypeHost && c.RelatedAddress().Address == host.Address() && c.RelatedAddress().Port == host.Port() {
						return c
					}
				}
			}
			t.Fatal("no public candidate from a host socket: physical interface was not probed")
			return nil
		}
		ac, err := gather(a).copy()
		require.NoError(t, err)
		bc, err := gather(b).copy()
		require.NoError(t, err)
		require.NoError(t, a.AddRemoteCandidate(bc))
		require.NoError(t, b.AddRemoteCandidate(ac))
		au, ap, err := a.GetLocalUserCredentials()
		require.NoError(t, err)
		bu, bp, err := b.GetLocalUserCredentials()
		require.NoError(t, err)
		if run == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			accepted := make(chan *Conn, 1)
			go func() { conn, _ := a.Accept(ctx, bu, bp); accepted <- conn }()
			out, err = b.Dial(ctx, au, ap)
			in = <-accepted
			cancel()
			require.NoError(t, err)
			require.NotNil(t, in)
		} else {
			require.NoError(t, a.SetRemoteCredentials(bu, bp))
			require.NoError(t, b.SetRemoteCredentials(au, ap))
			require.Eventually(t, func() bool {
				ap, _ := a.GetSelectedCandidatePair()
				bp, _ := b.GetSelectedCandidatePair()
				return ap != nil && bp != nil
			}, 5*time.Second, 10*time.Millisecond)
		}
		for _, pair := range [][2]*Conn{{in, out}, {out, in}} {
			require.NoError(t, pair[1].SetReadDeadline(time.Now().Add(time.Second)))
			_, err := pair[0].Write([]byte("picture and input"))
			require.NoError(t, err)
			buf := make([]byte, 128)
			n, err := pair[1].Read(buf)
			require.NoError(t, err)
			require.Equal(t, "picture and input", string(buf[:n]))
		}
	}
}
