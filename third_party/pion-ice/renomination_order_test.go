// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/require"
)

// A delayed response to an older nomination must not move the sender back
// after a newer path was selected. Use real authenticated UDP responses,
// then verify that application bytes still leave through the newer path.
func TestRenominationIgnoresOutOfOrderSuccess(t *testing.T) {
	defer test.CheckRoutines(t)()
	a, err := NewAgentWithOptions(WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithMulticastDNSMode(MulticastDNSModeDisabled), WithRenomination(DefaultNominationValueGenerator()))
	require.NoError(t, err)
	defer a.Close()
	open := func() *net.UDPConn {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		t.Cleanup(func() { c.Close() })
		return c
	}
	physical, first, second := open(), open(), open()
	candidate := func(c *net.UDPConn) *CandidateHost {
		addr := c.LocalAddr().(*net.UDPAddr)
		h, err := NewCandidateHost(&CandidateHostConfig{Network: "udp4", Address: addr.IP.String(), Port: addr.Port, Component: ComponentRTP})
		require.NoError(t, err)
		return h
	}
	local, oldRemote, newRemote := candidate(physical), candidate(first), candidate(second)
	require.NoError(t, a.addCandidate(context.Background(), local, physical, nil, false))
	require.NoError(t, a.AddRemoteCandidate(oldRemote))
	require.NoError(t, a.AddRemoteCandidate(newRemote))
	require.Eventually(t, func() bool {
		var ready bool
		_ = a.loop.Run(a.loop, func(context.Context) {
			ready = a.findPair(local, oldRemote) != nil && a.findPair(local, newRemote) != nil
		})
		return ready
	}, time.Second, time.Millisecond)
	const password = "peer-password"
	require.NoError(t, a.SetRemoteCredentials("peer", password))
	require.NoError(t, a.loop.Run(a.loop, func(context.Context) {
		a.setRole(true)
		a.startedFn()
		a.updateConnectionState(ConnectionStateChecking)
		_ = a.sendNominationRequest(a.findPair(local, oldRemote), 1)
		_ = a.sendNominationRequest(a.findPair(local, newRemote), 2)
	}))
	read := func(c *net.UDPConn) *stun.Message {
		require.NoError(t, c.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 1500)
		n, _, err := c.ReadFromUDP(buf)
		require.NoError(t, err)
		m := &stun.Message{Raw: buf[:n]}
		require.NoError(t, m.Decode())
		return m
	}
	oldRequest, newRequest := read(first), read(second)
	reply := func(c *net.UDPConn, m *stun.Message) {
		addr := physical.LocalAddr().(*net.UDPAddr)
		r, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(m.TransactionID),
			&stun.XORMappedAddress{IP: addr.IP, Port: addr.Port}, stun.NewShortTermIntegrity(password), stun.Fingerprint)
		require.NoError(t, err)
		_, err = c.WriteToUDP(r.Raw, addr)
		require.NoError(t, err)
	}
	reply(second, newRequest)
	require.Eventually(t, func() bool {
		p, _ := a.GetSelectedCandidatePair()
		return p != nil && p.Remote.Equal(newRemote)
	}, time.Second, time.Millisecond)
	reply(first, oldRequest)
	require.Eventually(t, func() bool {
		var seen bool
		_ = a.loop.Run(a.loop, func(context.Context) { seen = a.findPair(local, oldRemote).ResponsesReceived() > 0 })
		return seen
	}, time.Second, time.Millisecond)
	selected, err := a.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.True(t, selected.Remote.Equal(newRemote), "the delayed old response rolled selection back")
	conn := &Conn{agent: a}
	_, err = conn.Write([]byte("frame after direct upgrade"))
	require.NoError(t, err)
	buf := make([]byte, 128)
	require.NoError(t, second.SetReadDeadline(time.Now().Add(time.Second)))
	n, _, err := second.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Equal(t, "frame after direct upgrade", string(buf[:n]))
}
