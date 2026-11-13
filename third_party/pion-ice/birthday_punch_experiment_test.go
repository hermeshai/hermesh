// SPDX-License-Identifier: MIT

//go:build !js

package ice

// Test-only birthday punching. The vnet E2E proves authentication, socket
// adoption and cleanup with Pion's LINEAR NAT allocator. The separate Monte
// Carlo test models unknown/random allocation; it is not carrier evidence.
// No real network socket, public scan, or production option is used here.

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

type birthdayHit struct {
	conn net.PacketConn
	data []byte
	from netip.AddrPort
}

type birthdayPool struct {
	e          *punchExperiment
	generation uint64
	ufrag      string
	remote     string
	password   string
	conns      []net.PacketConn
	hits       chan birthdayHit
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	adopted    net.PacketConn
	rejected   atomic.Int32
}

func newBirthdayPool(t *testing.T, e *punchExperiment, n int) *birthdayPool {
	t.Helper()
	require.Contains(t, []int{8, 32, 64, 128}, n)
	p := &birthdayPool{e: e, hits: make(chan birthdayHit, n)}
	p.ctx, p.cancel = context.WithTimeout(e.phone.loop, 4*time.Second)
	var remotePassword string
	var tieBreaker uint64
	require.NoError(t, e.phone.loop.Run(e.phone.loop, func(context.Context) {
		p.generation, p.ufrag, p.remote = e.phone.gatherGeneration, e.phone.localUfrag, e.phone.remoteUfrag
		p.password, remotePassword, tieBreaker = e.phone.localPwd, e.phone.remotePwd, e.phone.tieBreaker
	}))
	for range n {
		c, err := e.v.net1.ListenPacket("udp4", net.JoinHostPort(vnetLocalIPB, "0"))
		require.NoError(t, err)
		p.conns = append(p.conns, c)
	}
	t.Cleanup(func() { p.cancel(); p.wg.Wait() })
	for _, c := range p.conns {
		p.wg.Add(1)
		go func(c net.PacketConn) {
			defer p.wg.Done()
			buf := make([]byte, 1500)
			for {
				n, from, err := c.ReadFrom(buf)
				if err != nil {
					return
				}
				m := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
				var username stun.Username
				if m.Decode() != nil || m.Type != stun.BindingRequest ||
					username.GetFrom(m) != nil || string(username) != p.ufrag+":"+p.remote ||
					stun.MessageIntegrity([]byte(p.password)).Check(m) != nil ||
					from.String() != e.public.addr().String() {
					p.rejected.Add(1)
					continue
				}
				p.hits <- birthdayHit{c, m.Raw, netAddrToAddrPort(from)}
				return // the ICE reader may take ownership only after this reader exits
			}
		}(c)
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		defer func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			for _, c := range p.conns {
				if c != p.adopted {
					_ = c.Close()
				}
			}
		}()
		for {
			select {
			case <-p.ctx.Done():
				return
			case <-e.machine.loop.Done():
				p.cancel()
				return
			case <-tick.C:
				current := false
				err := e.phone.loop.Run(p.ctx, func(context.Context) {
					current = e.phone.gatherGeneration == p.generation && e.phone.localUfrag == p.ufrag && e.phone.remoteUfrag == p.remote
				})
				if err != nil || !current {
					p.cancel()
					return
				}
			}
		}
	}()
	for _, c := range p.conns {
		m := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
			stun.NewUsername(p.remote+":"+p.ufrag), AttrControlling(tieBreaker),
			PriorityAttr(e.base.prflxPriority()), stun.NewSoftware("birthday-pool-open"),
			stun.NewShortTermIntegrity(remotePassword), stun.Fingerprint)
		_, err := c.WriteTo(m.Raw, e.public.addr())
		require.NoError(t, err)
	}
	return p
}

func (p *birthdayPool) adopt(t *testing.T, hit birthdayHit) {
	t.Helper()
	addr := hit.conn.LocalAddr().(*net.UDPAddr)
	c, err := NewCandidateHost(&CandidateHostConfig{
		Network: "udp4", Address: addr.IP.String(), Port: addr.Port, Component: ComponentRTP,
	})
	require.NoError(t, err)
	p.mu.Lock()
	active := p.ctx.Err() == nil
	if active {
		p.adopted = hit.conn
	}
	p.mu.Unlock()
	require.True(t, active, "expired socket pool must not add a candidate")
	// addCandidate validates the original gather generation on the ICE loop.
	require.NoError(t, p.e.phone.addCandidate(p.ctx, c, hit.conn, &p.generation, false))
	p.cancel()
	p.wg.Wait()
	// Replay the already authenticated first request through normal ICE too.
	// All following traffic, checks and nomination use the standard ICE reader.
	c.handleInboundPacket(hit.data, nil, hit.from)
}

func TestBirthdayPunchExperimentAdoptsOneSocket(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, count := range []int{8, 32, 64, 128} {
		t.Run(fmt.Sprintf("sockets_%d", count), func(t *testing.T) {
			e := newPunchExperiment(t)
			e.signalDirect(t)
			before, err := e.phone.GetLocalCandidates()
			require.NoError(t, err)
			var openings atomic.Int32
			var mappings sync.Map
			// Isolate the pool's path: otherwise a probe can also hit the
			// original phone socket and bypass the adoption being tested.
			e.v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
				m := &stun.Message{Raw: c.UserData()}
				if m.Decode() == nil {
					software, _ := m.Get(stun.AttrSoftware)
					if string(software) == "birthday-pool-open" {
						mappings.Store(c.SourceAddr().String(), true)
						openings.Add(1)
					}
				}
				if c.SourceAddr().String() == e.public.addr().String() &&
					c.DestinationAddr().(*net.UDPAddr).IP.String() == e.peer.Address() {
					_, allowed := mappings.Load(c.DestinationAddr().String())
					return allowed
				}
				return true
			})
			p := newBirthdayPool(t, e, count)
			require.Eventually(t, func() bool { return openings.Load() == int32(count) }, time.Second, time.Millisecond)
			still, err := e.phone.GetLocalCandidates()
			require.NoError(t, err)
			require.Len(t, still, len(before), "N speculative sockets must not create N candidate rows")
			// Fixed independent permutation, not a port learned from a capture.
			// vnet allocates linearly from 49152: this 1024-port fixture proves
			// handoff only, not real-world unknown-port success probability.
			ports := rand.New(rand.NewSource(42)).Perm(1024)
			for i := range ports {
				ports[i] += 49152
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan int, 1)
			go func() {
				sent, _ := e.run(ctx, ports, 1024, time.Millisecond, e.generation(t))
				done <- sent
			}()
			select {
			case hit := <-p.hits:
				p.adopt(t, hit)
			case <-ctx.Done():
				t.Fatal("bounded vnet fixture did not reach a temporary socket")
			}
			require.Eventually(t, e.direct, time.Second, time.Millisecond)
			cancel()
			sent := <-done
			require.LessOrEqual(t, sent, 1024)
			after, err := e.phone.GetLocalCandidates()
			require.NoError(t, err)
			require.Len(t, after, len(before)+1)
			e.transfer(t)
			for _, c := range p.conns {
				if c != p.adopted {
					birthdayAssertReleased(t, e, c)
				}
			}
			t.Logf("N=%d opening_requests=%d mac_probes=%d candidate_growth=1 direct=true", count, openings.Load(), sent)
		})
	}
}

func TestBirthdayPunchExperimentCleanup(t *testing.T) {
	defer test.CheckRoutines(t)()
	for _, stop := range []string{"budget", "deadline", "restart", "close", "peer close"} {
		t.Run(stop, func(t *testing.T) {
			e := newPunchExperiment(t)
			p := newBirthdayPool(t, e, 8)
			switch stop {
			case "budget":
				p.cancel()
			case "deadline":
				<-p.ctx.Done()
				require.ErrorIs(t, p.ctx.Err(), context.DeadlineExceeded)
			case "restart":
				require.NoError(t, e.phone.Restart("", ""))
			case "close":
				require.NoError(t, e.phone.Close())
			case "peer close":
				require.NoError(t, e.machine.Close())
			}
			p.wg.Wait()
			for _, c := range p.conns {
				birthdayAssertReleased(t, e, c)
			}
			require.Nil(t, p.adopted)
		})
	}
}

func birthdayAssertReleased(t *testing.T, e *punchExperiment, c net.PacketConn) {
	t.Helper()
	// vnet currently permits WriteTo after Close. Rebinding is a stronger
	// check that the local socket was actually removed from its UDP table.
	rebound, err := e.v.net1.ListenPacket("udp4", c.LocalAddr().String())
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

func TestBirthdayPunchExperimentRejectsUnauthenticated(t *testing.T) {
	t.Cleanup(test.CheckRoutines(t))
	e := newPunchExperiment(t)
	mapping := make(chan *net.UDPAddr, 8)
	e.v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
		m := &stun.Message{Raw: c.UserData()}
		if m.Decode() == nil {
			software, _ := m.Get(stun.AttrSoftware)
			if string(software) == "birthday-pool-open" {
				mapping <- c.SourceAddr().(*net.UDPAddr)
			}
		}
		return true
	})
	p := newBirthdayPool(t, e, 8)
	var address *net.UDPAddr
	select {
	case address = <-mapping:
	case <-time.After(time.Second):
		t.Fatal("missing temporary vnet mapping")
	}
	remote, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
		Network: "udp4", Address: address.IP.String(), Port: address.Port, Component: ComponentRTP,
	})
	require.NoError(t, err)
	for _, corrupt := range []string{"username", "integrity"} {
		username, password := p.ufrag+":"+p.remote, p.password
		if corrupt == "username" {
			username = "another-session:peer"
		} else {
			password = "incorrect-password"
		}
		require.NoError(t, e.machine.loop.Run(e.machine.loop, func(context.Context) {
			m := stun.MustBuild(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(username), AttrControlled(e.machine.tieBreaker),
				PriorityAttr(e.base.prflxPriority()), stun.NewShortTermIntegrity(password), stun.Fingerprint)
			e.machine.sendSTUN(m, e.base, remote)
		}))
	}
	require.Eventually(t, func() bool { return p.rejected.Load() == 2 }, time.Second, time.Millisecond)
	require.Empty(t, p.hits)
	require.Nil(t, p.adopted)
	e.transfer(t) // rejected packets must not disturb the working relay
}

const birthdayPortSpace = 65536 - 1024

func birthdayDistinct(r *rand.Rand, n, space int) []int {
	seen := make(map[int]bool, n)
	out := make([]int, 0, n)
	for len(out) < n {
		port := r.Intn(space)
		if !seen[port] {
			seen[port] = true
			out = append(out, port)
		}
	}
	return out
}

func TestBirthdayPunchExperimentUnknownDistribution(t *testing.T) {
	// No network packets. Independent seeds keep the guessing side blind to
	// the mappings. All hidden ports and guesses are unique. Each simulated
	// mapping remains valid throughout the bounded attempt, with no loss.
	const trials = 5000
	for _, distribution := range []string{"uniform", "one-cluster", "four-clusters"} {
		for _, n := range []int{8, 32, 64, 128} {
			for _, k := range []int{128, 512, 1024, 2048} {
				if n < 64 && k == 2048 || n == 128 && k < 1024 {
					continue
				}
				r, q := rand.New(rand.NewSource(721)), rand.New(rand.NewSource(997))
				hits, sent := 0, 0
				for range trials {
					mapped := birthdayDistinct(r, n, birthdayPortSpace)
					if distribution != "uniform" {
						clusters := 1
						if distribution == "four-clusters" {
							clusters = 4
						}
						// Separate random blocks model unknown subscriber port
						// clusters, without guessing their location from the peer.
						starts := birthdayDistinct(r, clusters, birthdayPortSpace/128)
						for i := range mapped {
							mapped[i] = starts[i%clusters]*128 + i/clusters
						}
					}
					hidden := make(map[int]bool, n)
					for _, port := range mapped {
						hidden[port] = true
					}
					for _, port := range birthdayDistinct(q, k, birthdayPortSpace) {
						sent++
						if hidden[port] {
							hits++
							break
						}
					}
				}
				miss := 1.0
				for i := range k {
					miss *= float64(birthdayPortSpace-n-i) / float64(birthdayPortSpace-i)
				}
				probability := 1 - miss
				require.Less(t, math.Abs(float64(hits)/trials-probability), 0.035)
				t.Logf("distribution=%s N=%d K=%d hits=%d/%d probability=%.3f%% theory=%.3f%% mean_extra_requests=%.1f max_extra_requests=%d",
					distribution, n, k, hits, trials, 100*float64(hits)/trials, 100*probability, float64(n)+float64(sent)/trials, n+k)
			}
		}
	}
}
