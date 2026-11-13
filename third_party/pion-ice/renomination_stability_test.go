// SPDX-License-Identifier: MIT

package ice

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRenominationKeepsStableRelayAndFindsNATDirect(t *testing.T) {
	a, err := NewAgentWithOptions(WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	defer a.Close()
	pair := func(local, remote Candidate, rtt time.Duration) *CandidatePair {
		p := newCandidatePair(local, remote, true)
		p.state = CandidatePairStateSucceeded
		p.UpdateRoundTripTime(rtt)
		return p
	}
	host, err := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "192.0.2.1", Port: 10000, Component: 1})
	require.NoError(t, err)
	relay := func(port int) Candidate {
		c, err := NewCandidateRelay(&CandidateRelayConfig{Network: "udp", Address: "192.0.2.2", Port: port, Component: 1})
		require.NoError(t, err)
		return c
	}
	t.Run("relay jitter does not move the path", func(t *testing.T) {
		current := pair(host, relay(20000), 500*time.Millisecond)
		jitter := pair(host, relay(20001), 480*time.Millisecond)
		require.False(t, a.shouldRenominate(current, jitter))
		faster := pair(host, relay(20002), 250*time.Millisecond)
		require.True(t, a.shouldRenominate(current, faster))
	})
	t.Run("negative scores do not reverse improvement", func(t *testing.T) {
		current := pair(relay(10000), relay(20000), 3*time.Second)
		worse := pair(relay(10001), relay(20001), 3100*time.Millisecond)
		require.False(t, a.shouldRenominate(current, worse))
	})
	t.Run("a NAT direct pair is direct too", func(t *testing.T) {
		srflx, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{Network: "udp", Address: "192.0.2.3", Port: 30000, Component: 1})
		require.NoError(t, err)
		prflx, err := NewCandidatePeerReflexive(&CandidatePeerReflexiveConfig{Network: "udp", Address: "192.0.2.4", Port: 40000, Component: 1})
		require.NoError(t, err)
		current := pair(host, relay(20000), 500*time.Millisecond)
		direct := pair(srflx, prflx, 500*time.Millisecond)
		require.True(t, a.shouldRenominate(current, direct))
		a.checklist = []*CandidatePair{current, direct}
		require.Same(t, direct, a.findBestCandidatePair())
	})
}
