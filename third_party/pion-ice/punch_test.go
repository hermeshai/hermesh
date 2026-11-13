// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func punchOnLoop(t *testing.T, a *Agent, run func()) {
	t.Helper()
	require.NoError(t, a.loop.Run(a.loop, func(context.Context) { run() }))
}

func TestBoundedPunchRelayToDirect(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("transport opt-in runs independently of one-second ICE cadence", func(t *testing.T) {
		e := newPunchExperimentWithNet(t, true, time.Second)
		e.signalDirect(t)
		// Only real ContactCandidates starts the production worker; this test
		// never manually drives boundedPunch or startBoundedPunch.
		require.Eventually(t, e.direct, 4*time.Second, 10*time.Millisecond)
		e.transfer(t)
		punchOnLoop(t, e.machine, func() {
			assert.True(t, e.machine.boundedPunchEnabled)
			assert.Equal(t, time.Second, e.machine.keepaliveInterval)
			assert.Equal(t, time.Second, e.machine.checkInterval)
			assert.Positive(t, e.machine.boundedPunchState.sent)
			t.Logf("one-second ICE cadence, dedicated punch worker sent=%d", e.machine.boundedPunchState.sent)
		})
	})
	t.Run("opt-in upgrades real relay data to direct and stops", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.signalDirect(t)
		assert.Never(t, e.direct, 150*time.Millisecond, 5*time.Millisecond)
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunchEnabled = true
			e.machine.startBoundedPunch()
		})
		assert.Eventually(t, func() bool {
			return e.direct()
		}, 3*time.Second, 10*time.Millisecond)
		e.transfer(t)
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunch()
			sent := e.machine.boundedPunchState.sent
			assert.Positive(t, sent)
			assert.Less(t, sent, boundedPunchMaxPackets)
			assert.True(t, e.machine.boundedPunchState.done)
			for i := 0; i < 10; i++ {
				e.machine.boundedPunch()
			}
			assert.Equal(t, sent, e.machine.boundedPunchState.sent)
			t.Logf("TURN upgraded to authenticated direct; extra_requests=%d; stopped=true", sent)
		})
	})
	t.Run("bad inbound integrity cannot discover or nominate direct", func(t *testing.T) {
		e := newPunchExperiment(t)
		var badPackets atomic.Int32
		e.v.wan.AddChunkFilter(func(c vnet.Chunk) bool {
			if c.SourceAddr().(*net.UDPAddr).IP.String() != vnetGlobalIPB ||
				c.DestinationAddr().(*net.UDPAddr).IP.String() != vnetGlobalIPA {
				return true
			}
			m := &stun.Message{Raw: c.UserData()}
			if m.Decode() == nil && m.Type == stun.BindingRequest {
				integrity, err := m.Get(stun.AttrMessageIntegrity)
				if err == nil {
					integrity[0] ^= 0xff
					badPackets.Add(1)
				}
			}
			return true
		})
		e.signalDirect(t)
		punchOnLoop(t, e.machine, func() { e.machine.boundedPunchEnabled = true })
		for i := 0; i < 40; i++ {
			punchOnLoop(t, e.machine, e.machine.boundedPunch)
			time.Sleep(10 * time.Millisecond)
		}
		assert.False(t, e.direct())
		punchOnLoop(t, e.machine, func() {
			for _, p := range e.machine.checklist {
				if p.Local.Type() != CandidateTypeRelay && p.Remote.Address() == vnetGlobalIPB {
					assert.Zero(t, p.RequestsReceived(), "unverified direct checks must not pass integrity validation")
					assert.NotEqual(t, CandidatePairStateSucceeded, p.state)
				}
			}
		})
		e.transfer(t)
		assert.Positive(t, badPackets.Load())
	})
}

func TestBoundedPunchBudgetRateDeadlineAndRestart(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("budget, same-credentials restart, and deadline", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.consumeMappings(t, 64)
		e.signalDirect(t)
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunchEnabled = true
			e.machine.boundedPunch()
			assert.Equal(t, 1, e.machine.boundedPunchState.sent)
			e.machine.boundedPunch()
			assert.Equal(t, 1, e.machine.boundedPunchState.sent, "rate limit must cover repeated forced contacts")
			for i := 0; i < boundedPunchMaxPackets+1; i++ {
				e.machine.boundedPunchState.last = time.Time{}
				e.machine.boundedPunch()
			}
			assert.True(t, e.machine.boundedPunchState.done)
			assert.LessOrEqual(t, e.machine.boundedPunchState.sent, boundedPunchMaxPackets)
			assert.GreaterOrEqual(t, e.machine.boundedPunchState.sent, 32)
			t.Logf("budget exhausted: extra_requests=%d, direct=false", e.machine.boundedPunchState.sent)
		})
		assert.Never(t, e.direct, 100*time.Millisecond, 5*time.Millisecond)
		e.transfer(t)
		localUfrag, localPwd, err := e.machine.GetLocalUserCredentials()
		assert.NoError(t, err)
		remoteUfrag, remotePwd, err := e.phone.GetLocalUserCredentials()
		assert.NoError(t, err)
		var previous uint64
		punchOnLoop(t, e.machine, func() { previous = e.machine.gatherGeneration })
		assert.NoError(t, e.machine.Restart(localUfrag, localPwd))
		assert.NoError(t, e.machine.SetRemoteCredentials(remoteUfrag, remotePwd))
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunch()
			assert.Greater(t, e.machine.gatherGeneration, previous)
			assert.Equal(t, e.machine.gatherGeneration, e.machine.boundedPunchState.generation)
			assert.Zero(t, e.machine.boundedPunchState.sent)
			assert.Empty(t, e.machine.boundedPunchState.targets)
		})
	})
	t.Run("expired attempt never emits a delayed burst", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.signalDirect(t)
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunchEnabled = true
			e.machine.boundedPunch()
			e.machine.boundedPunchState.started = time.Now().Add(-boundedPunchDuration)
			e.machine.boundedPunchState.last = time.Time{}
			e.machine.boundedPunch()
			assert.True(t, e.machine.boundedPunchState.done)
			assert.Equal(t, 1, e.machine.boundedPunchState.sent)
		})
	})
	t.Run("restart cancels an active worker with reused credentials", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.consumeMappings(t, 64)
		e.signalDirect(t)
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunchEnabled = true
			e.machine.startBoundedPunch()
			assert.True(t, e.machine.boundedPunchState.running)
		})
		ufrag, pwd, err := e.machine.GetLocalUserCredentials()
		require.NoError(t, err)
		require.NoError(t, e.machine.Restart(ufrag, pwd))
		require.Eventually(t, func() bool {
			stopped := false
			punchOnLoop(t, e.machine, func() { stopped = !e.machine.boundedPunchState.running })
			return stopped
		}, time.Second, 5*time.Millisecond)
		punchOnLoop(t, e.machine, func() {
			assert.LessOrEqual(t, e.machine.boundedPunchState.sent, 1)
			assert.True(t, e.machine.boundedPunchState.done)
		})
	})
}

func TestBoundedPunchTargetRestrictions(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("default off, public peers only, aliases deduplicated", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.signalDirect(t)
		punchOnLoop(t, e.machine, func() {
			e.machine.boundedPunch()
			assert.Zero(t, e.machine.boundedPunchState.sent)
			locals := e.machine.localCandidates[NetworkTypeUDP4]
			defer func() { e.machine.localCandidates[NetworkTypeUDP4] = locals }()
			vm, err := NewCandidateHost(&CandidateHostConfig{
				Network: "udp4", Address: "10.211.55.2", Port: 30000, Component: ComponentRTP,
			})
			assert.NoError(t, err)
			e.machine.localCandidates[NetworkTypeUDP4] = append([]Candidate{vm}, e.machine.localCandidates[NetworkTypeUDP4]...)
			targets := e.machine.boundedPunchTargets()
			baseIDs := make(map[string]bool)
			for _, target := range targets {
				baseIDs[target.local.ID()] = true
				assert.NotEqual(t, vm.ID(), target.local.ID(), "unproven VM interface must not consume budget")
				assert.NotEqual(t, vnetSTUNServerIP, target.remote.Address())
			}
			assert.Len(t, baseIDs, 1, "host and srflx share the same socket")
			for _, address := range []string{"10.0.0.1", "127.0.0.1", "169.254.1.1", "100.64.1.1", "198.18.0.1", "0.0.0.0", "224.0.0.1"} {
				c, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
					Network: "udp4", Address: address, Port: 5000, Component: ComponentRTP,
				})
				assert.NoError(t, err)
				e.machine.remoteCandidates[NetworkTypeUDP4] = []Candidate{c}
				assert.Empty(t, e.machine.boundedPunchTargets(), address)
			}
		})
	})
}

func TestBoundedPunchSeedDiversity(t *testing.T) {
	srflx := func(port int) Candidate {
		c, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
			Network: "udp4", Address: vnetGlobalIPB, Port: port, Component: ComponentRTP,
		})
		require.NoError(t, err)
		return c
	}
	prflx := func(ip string, port int) Candidate {
		c, err := NewCandidatePeerReflexive(&CandidatePeerReflexiveConfig{
			Network: "udp4", Address: ip, Port: port, Component: ComponentRTP,
		})
		require.NoError(t, err)
		return c
	}
	first, neighbor := srflx(17158), srflx(17159)
	far, edge := prflx(vnetGlobalIPB, 44700), prflx(vnetGlobalIPB, 65530)
	for _, observed := range []Candidate{far, edge} {
		for _, candidates := range [][]Candidate{{first, neighbor, observed}, {observed, neighbor, first}} {
			a := &Agent{remoteCandidates: map[NetworkType][]Candidate{NetworkTypeUDP4: candidates}}
			seeds := a.boundedPunchSeeds()
			require.Len(t, seeds, 2)
			require.Equal(t, CandidateTypeServerReflexive, seeds[1].Type(), "unverified prflx label is not an observation")
			pair := &CandidatePair{Remote: observed}
			pair.UpdateRequestReceived()
			a.checklist = []*CandidatePair{pair}
			seeds = a.boundedPunchSeeds()
			require.Len(t, seeds, 2)
			require.Equal(t, CandidateTypeServerReflexive, seeds[0].Type())
			require.Equal(t, observed, seeds[1], "second slot must cover the distinct authenticated mapping window")
		}
	}
	require.Equal(t, 2, boundedPunchWindowGain(neighbor, first))
	require.Equal(t, 32, boundedPunchWindowGain(far, first))
	require.Equal(t, 21, boundedPunchWindowGain(edge, first))

	turnObserved := prflx(vnetSTUNServerIP, 44700)
	relay, err := NewCandidateRelay(&CandidateRelayConfig{Network: "udp4", Address: vnetSTUNServerIP, Port: 49152, Component: ComponentRTP})
	require.NoError(t, err)
	turnPair := &CandidatePair{Remote: turnObserved}
	turnPair.UpdateRequestReceived()
	a := &Agent{remoteCandidates: map[NetworkType][]Candidate{NetworkTypeUDP4: {first, turnObserved, relay}}, checklist: []*CandidatePair{turnPair}}
	require.Equal(t, []Candidate{first}, a.boundedPunchSeeds(), "authenticated TURN-address observations are still excluded")
}

func TestBoundedPunchReportsMetadata(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("start targets and stop reach the opt-in reporter", func(t *testing.T) {
		e := newPunchExperiment(t)
		e.signalDirect(t)
		var reports []string
		punchOnLoop(t, e.machine, func() {
			e.machine.net = &BoundedNATPunchNet{Net: e.machine.net, Report: func(message string) {
				reports = append(reports, message)
			}}
			e.machine.boundedPunchEnabled = true
			e.machine.startBoundedPunch()
		})
		require.Eventually(t, e.direct, 3*time.Second, 10*time.Millisecond)
		e.transfer(t)
		require.Eventually(t, func() bool {
			stopped := false
			punchOnLoop(t, e.machine, func() { stopped = e.machine.boundedPunchState.done })
			return stopped
		}, time.Second, 10*time.Millisecond)
		punchOnLoop(t, e.machine, func() {
			joined := strings.Join(reports, "\n")
			assert.Contains(t, joined, "punch started:")
			assert.Contains(t, joined, "target: base=")
			assert.Contains(t, joined, "seed=")
			assert.Contains(t, joined, "punch stopped:")
			assert.Contains(t, joined, "reason=direct")
			for _, credential := range []string{e.machine.localUfrag, e.machine.remoteUfrag, e.machine.localPwd, e.machine.remotePwd} {
				assert.NotContains(t, joined, credential)
			}
		})
	})
}

func TestBoundedPunchSeparateMappingWindows(t *testing.T) {
	defer test.CheckRoutines(t)()
	t.Run("nearby STUN mappings miss while authenticated peer mapping succeeds", func(t *testing.T) {
		e := newPunchExperiment(t)
		// Observe a second real STUN mapping before unrelated allocations move
		// the NAT allocator to another window. No guessed mapping is signaled.
		conn, err := e.v.net1.ListenPacket("udp4", net.JoinHostPort(vnetLocalIPB, "0"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		query := stun.MustBuild(stun.BindingRequest, stun.TransactionID)
		_, err = conn.WriteTo(query.Raw, &net.UDPAddr{IP: net.ParseIP(vnetSTUNServerIP), Port: vnetSTUNServerPort})
		require.NoError(t, err)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 1500)
		n, _, err := conn.ReadFrom(buf)
		require.NoError(t, err)
		response := &stun.Message{Raw: buf[:n]}
		require.NoError(t, response.Decode())
		var mapped stun.XORMappedAddress
		require.NoError(t, mapped.GetFrom(response))
		require.NoError(t, conn.SetReadDeadline(time.Time{}))
		local := conn.LocalAddr().(*net.UDPAddr)
		advertised, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
			Network: "udp4", Address: mapped.IP.String(), Port: mapped.Port, Component: ComponentRTP,
			RelAddr: local.IP.String(), RelPort: local.Port,
		})
		require.NoError(t, err)
		require.Less(t, mapped.Port-e.peer.Port(), boundedPunchRadius)
		e.consumeMappings(t, 64)
		punchSignal(t, e.machine, e.peer)
		punchSignal(t, e.machine, advertised)
		host, err := NewCandidateHost(&CandidateHostConfig{Network: "udp4", Address: local.IP.String(), Port: local.Port, Component: ComponentRTP})
		require.NoError(t, err)
		// Register that same real socket with the phone ICE agent. Its checks
		// through the established TURN path authenticate the later mapping.
		require.NoError(t, e.phone.AddVirtualCandidate(host, conn))
		require.Eventually(t, func() bool {
			observed := false
			punchOnLoop(t, e.machine, func() {
				for _, p := range e.machine.checklist {
					if p.Remote.Type() == CandidateTypePeerReflexive && p.Remote.Address() == vnetGlobalIPB &&
						p.Remote.Port() > mapped.Port+32 && p.RequestsReceived() > 0 {
						observed = true
					}
				}
			})
			return observed
		}, time.Second, 5*time.Millisecond)
		punchSignal(t, e.phone, e.public)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sent, _ := e.run(ctx, punchPorts(e.peer.Port()-16, 40), 40, 5*time.Millisecond, e.generation(t))
		require.Equal(t, 40, sent)
		require.Never(t, e.direct, 80*time.Millisecond, 5*time.Millisecond)
		e.transfer(t)
		punchOnLoop(t, e.machine, func() {
			seeds := e.machine.boundedPunchSeeds()
			assert.Len(t, seeds, 2)
			assert.Equal(t, CandidateTypePeerReflexive, seeds[1].Type())
			assert.Greater(t, seeds[1].Port(), mapped.Port+32)
			e.machine.boundedPunchEnabled = true
			e.machine.startBoundedPunch()
		})
		require.Eventually(t, e.direct, 3*time.Second, 10*time.Millisecond)
		e.transfer(t)
		punchOnLoop(t, e.machine, func() {
			t.Logf("two adjacent STUN windows: no direct after %d probes; diverse authenticated mapping: direct after %d more probes",
				sent, e.machine.boundedPunchState.sent)
		})
	})
}
