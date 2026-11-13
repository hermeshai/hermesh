// SPDX-License-Identifier: MIT

//go:build !js

package ice

// Isolated experiments for two destination-dependent mappings. Every socket
// belongs to vnet; this file does not enable any production/public probe.

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

func dualNATNetwork(t *testing.T, filtering vnet.EndpointDependencyType) *virtualNet {
	t.Helper()
	typeOfNAT := &vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: filtering}
	v, err := buildVNet(typeOfNAT, typeOfNAT)
	require.NoError(t, err)
	t.Cleanup(v.close)
	return v
}

func dualNATSocket(t *testing.T, n *vnet.Net, local string) net.PacketConn {
	t.Helper()
	c, err := n.ListenPacket("udp4", net.JoinHostPort(local, "0"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func dualNATMapped(t *testing.T, c net.PacketConn) *net.UDPAddr {
	t.Helper()
	m := stun.MustBuild(stun.BindingRequest, stun.TransactionID)
	_, err := c.WriteTo(m.Raw, &net.UDPAddr{IP: net.ParseIP(vnetSTUNServerIP), Port: vnetSTUNServerPort})
	require.NoError(t, err)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(time.Second)))
	buf := make([]byte, 1500)
	n, _, err := c.ReadFrom(buf)
	require.NoError(t, err)
	response := &stun.Message{Raw: buf[:n]}
	require.NoError(t, response.Decode())
	var mapped stun.XORMappedAddress
	require.NoError(t, mapped.GetFrom(response))
	require.NoError(t, c.SetReadDeadline(time.Time{}))
	return &net.UDPAddr{IP: mapped.IP, Port: mapped.Port}
}

func dualNATRequest() *stun.Message {
	return stun.MustBuild(stun.BindingRequest, stun.TransactionID, stun.NewUsername("dual:experiment"),
		stun.NewSoftware("dual-nat-experiment"), stun.NewShortTermIntegrity("test-session-password"), stun.Fingerprint)
}

func TestDualNATMappingIsNotFiltering(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, filtering := range []vnet.EndpointDependencyType{vnet.EndpointIndependent, vnet.EndpointAddrPortDependent} {
		t.Run(map[vnet.EndpointDependencyType]string{vnet.EndpointIndependent: "independent filter admits asymmetric sources", vnet.EndpointAddrPortDependent: "exact filter rejects asymmetric sources"}[filtering], func(t *testing.T) {
			v := dualNATNetwork(t, filtering)
			a, b := dualNATSocket(t, v.net0, vnetLocalIPA), dualNATSocket(t, v.net1, vnetLocalIPB)
			ap, bp := dualNATMapped(t, a), dualNATMapped(t, b)
			var mismatched atomic.Int32
			v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
				from, to := c.SourceAddr().(*net.UDPAddr), c.DestinationAddr().(*net.UDPAddr)
				if from.IP.Equal(ap.IP) && to.IP.Equal(bp.IP) && from.Port != ap.Port ||
					from.IP.Equal(bp.IP) && to.IP.Equal(ap.IP) && from.Port != bp.Port {
					mismatched.Add(1)
				}
				return true
			})
			for _, side := range []struct {
				c    net.PacketConn
				peer net.Addr
			}{{a, bp}, {b, ap}} {
				_, err := side.c.WriteTo(dualNATRequest().Raw, side.peer)
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool { return mismatched.Load() == 2 }, time.Second, time.Millisecond)
			for _, c := range []net.PacketConn{a, b} {
				require.NoError(t, c.SetReadDeadline(time.Now().Add(60*time.Millisecond)))
				buf := make([]byte, 1500)
				n, _, err := c.ReadFrom(buf)
				if filtering == vnet.EndpointIndependent {
					require.NoError(t, err)
					message := &stun.Message{Raw: buf[:n]}
					require.NoError(t, message.Decode())
					require.NoError(t, stun.NewShortTermIntegrity("test-session-password").Check(message))
				} else {
					var timeout net.Error
					require.ErrorAs(t, err, &timeout)
					require.True(t, timeout.Timeout())
				}
			}
			t.Logf("destination-dependent mapping on both ends; changed peer-directed sources=%d; inbound_admitted=%t",
				mismatched.Load(), filtering == vnet.EndpointIndependent)
		})
	}
}

func TestDualNATFixedTargetBirthdayFailsEvenWithKnownPoolPorts(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("128 oracle destinations cannot restore the Mac source endpoint", func(t *testing.T) {
		v := dualNATNetwork(t, vnet.EndpointAddrPortDependent)
		mac := dualNATSocket(t, v.net0, vnetLocalIPA)
		advertised := dualNATMapped(t, mac)
		const count = 128
		mappings := make(chan net.Addr, count)
		var wrongSource atomic.Int32
		v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
			from, to := c.SourceAddr().(*net.UDPAddr), c.DestinationAddr().(*net.UDPAddr)
			if from.IP.String() == vnetGlobalIPB && to.String() == advertised.String() {
				mappings <- from
			}
			if from.IP.Equal(advertised.IP) && to.IP.String() == vnetGlobalIPB && from.Port != advertised.Port {
				wrongSource.Add(1)
			}
			return true
		})
		pool := make([]net.PacketConn, count)
		for i := range pool {
			pool[i] = dualNATSocket(t, v.net1, vnetLocalIPB)
			_, err := pool[i].WriteTo(dualNATRequest().Raw, advertised)
			require.NoError(t, err)
		}
		for range count {
			select {
			case exactMapping := <-mappings:
				// An oracle is stronger than guessing: even a 100% destination
				// hit rate cannot fix the wrong source tuple in this NAT model.
				_, err := mac.WriteTo(dualNATRequest().Raw, exactMapping)
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("virtual pool did not establish its mappings")
			}
		}
		require.Eventually(t, func() bool { return wrongSource.Load() == count }, time.Second, time.Millisecond)
		var received atomic.Int32
		var readers sync.WaitGroup
		for _, c := range pool {
			readers.Add(1)
			go func(c net.PacketConn) {
				defer readers.Done()
				_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				if _, _, err := c.ReadFrom(make([]byte, 1500)); err == nil {
					received.Add(1)
				}
			}(c)
		}
		readers.Wait()
		require.Zero(t, received.Load())
		t.Logf("pool=%d, exact destination probes=%d, unexpected Mac source ports=%d, pool_received=%d",
			count, count, wrongSource.Load(), received.Load())
	})
}

func dualNATSTUNEndpoint(t *testing.T, v *virtualNet, ip string) *net.UDPAddr {
	t.Helper()
	n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
	require.NoError(t, err)
	require.NoError(t, v.wan.AddNet(n))
	c := dualNATSocket(t, n, ip)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			request := &stun.Message{Raw: buf[:n]}
			if request.Decode() != nil || request.Type != stun.BindingRequest {
				continue
			}
			address := from.(*net.UDPAddr)
			response := stun.MustBuild(stun.BindingSuccess, stun.NewTransactionIDSetter(request.TransactionID),
				&stun.XORMappedAddress{IP: address.IP, Port: address.Port})
			_, _ = c.WriteTo(response.Raw, from)
		}
	}()
	return c.LocalAddr().(*net.UDPAddr)
}

func TestDualNATCoordinatedLinearPrediction(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, scenario := range []string{"advertised mappings fail", "quiet linear next mappings connect", "one unobserved allocation breaks prediction"} {
		t.Run(scenario, func(t *testing.T) {
			v := dualNATNetwork(t, vnet.EndpointAddrPortDependent)
			servers := []*net.UDPAddr{{IP: net.ParseIP(vnetSTUNServerIP), Port: vnetSTUNServerPort},
				dualNATSTUNEndpoint(t, v, "1.2.3.5"), dualNATSTUNEndpoint(t, v, "1.2.3.6")}
			type peer struct {
				agent    *Agent
				public   *net.UDPAddr
				lastPort int
			}
			makePeer := func(n *vnet.Net, address string) peer {
				conn := dualNATSocket(t, n, address)
				mux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: conn, Net: n})
				t.Cleanup(func() { _ = mux.Close() })
				var first *net.UDPAddr
				last := 0
				for _, server := range servers {
					mapped, err := mux.GetXORMappedAddr(server, time.Second)
					require.NoError(t, err)
					if first == nil {
						first = &net.UDPAddr{IP: mapped.IP, Port: mapped.Port}
					} else {
						require.Equal(t, last+1, mapped.Port, "controlled vnet fixture is globally linear")
					}
					last = mapped.Port
				}
				interval, zero := 20*time.Millisecond, time.Duration(0)
				a, err := newAgentFromConfig(&AgentConfig{
					Net: n, UDPMux: mux, UDPMuxSrflx: mux, NetworkTypes: []NetworkType{NetworkTypeUDP4},
					MulticastDNSMode: MulticastDNSModeDisabled, CandidateTypes: []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive},
					CheckInterval: &interval, SrflxAcceptanceMinWait: &zero, PrflxAcceptanceMinWait: &zero,
					Urls: []*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort, Proto: stun.ProtoTypeUDP}},
				})
				require.NoError(t, err)
				t.Cleanup(func() { _ = a.Close() })
				_ = gatherAndCollectCandidates(t, a)
				return peer{a, first, last}
			}
			a, b := makePeer(v.net0, vnetLocalIPA), makePeer(v.net1, vnetLocalIPB)
			if scenario == "one unobserved allocation breaks prediction" {
				_ = dualNATMapped(t, dualNATSocket(t, v.net0, vnetLocalIPA))
			}
			for _, signal := range []struct {
				to   *Agent
				from peer
			}{{a.agent, b}, {b.agent, a}} {
				port := signal.from.public.Port
				if scenario != "advertised mappings fail" {
					port = signal.from.lastPort + 1
				}
				// A predicted address is only a check target, never proof of a
				// usable path. Ordinary ICE identity/transaction checks decide.
				candidate, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
					Network: "udp4", Address: signal.from.public.IP.String(), Port: port, Component: ComponentRTP,
				})
				require.NoError(t, err)
				punchSignal(t, signal.to, candidate)
			}
			au, ap, err := a.agent.GetLocalUserCredentials()
			require.NoError(t, err)
			bu, bp, err := b.agent.GetLocalUserCredentials()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			accepted := make(chan *Conn, 1)
			go func() { c, _ := a.agent.Accept(ctx, bu, bp); accepted <- c }()
			out, err := b.agent.Dial(ctx, au, ap)
			in := <-accepted
			if scenario != "quiet linear next mappings connect" {
				require.Error(t, err)
				require.Nil(t, in)
				t.Log("no direct connection within the bounded attempt")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, in)
			for _, pair := range [][2]*Conn{{in, out}, {out, in}} {
				require.NoError(t, pair[1].SetReadDeadline(time.Now().Add(time.Second)))
				_, err := pair[0].Write([]byte("authenticated direct frame and input"))
				require.NoError(t, err)
				buf := make([]byte, 128)
				n, err := pair[1].Read(buf)
				require.NoError(t, err)
				require.Equal(t, "authenticated direct frame and input", string(buf[:n]))
			}
			t.Log("three sequential observations per socket, one predicted target each, ordinary authenticated ICE and bidirectional data succeeded")
		})
	}
}

func TestDualNATUnknownIndependentMappingBound(t *testing.T) {
	// Conditional bound for a different model, not a measurement of carrier
	// NAT: both source ports are unknown independent uniform choices. A pair
	// of reverse exact-filter openings must match BOTH port coordinates.
	const space, eachSide = 65536 - 1024, 2048
	bound := float64(eachSide*eachSide) / float64(space*space)
	require.Less(t, bound, 0.0011)
	t.Logf("bilateral independent-port model: %d probes per side, union bound %.5f%%; one-sided birthday 98%% is inapplicable", eachSide, 100*bound)
}
