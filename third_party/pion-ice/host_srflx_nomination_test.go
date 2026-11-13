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

// A host and its reflexive address share a socket, but have distinct mux
// readers. A later check from the reflexive candidate must not steal the
// success response of the host's nominated triggered check. The real UDP
// responder below controls packet order without depending on RTT or timers.
func TestHostSRFLXMuxPreservesNomination(t *testing.T) {
	for _, role := range []string{"controlling", "controlled"} {
		t.Run(role, func(t *testing.T) {
			t.Run("shared socket", func(t *testing.T) {
				testHostSRFLXMuxNomination(t, role == "controlling", false)
			})
			t.Run("different socket rejects response", func(t *testing.T) {
				testHostSRFLXMuxNomination(t, role == "controlling", true)
			})
		})
	}
}

func testHostSRFLXMuxNomination(t *testing.T, controlling, differentSocket bool) {
	t.Helper()
	defer test.CheckRoutines(t)()
	a, err := NewAgentWithOptions(
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer a.Close()
	localUfrag, localPwd, err := a.GetLocalUserCredentials()
	require.NoError(t, err)
	const remoteUfrag, remotePwd = "peer", "peer-password"

	physical, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	mux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: physical})
	defer mux.Close()
	base := physical.LocalAddr().(*net.UDPAddr)
	hostConn, err := mux.GetConn(localUfrag, base)
	require.NoError(t, err)
	otherMux, responseAddr := mux, base
	if differentSocket {
		other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: base.IP})
		require.NoError(t, err)
		otherMux = NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: other})
		defer otherMux.Close()
		responseAddr = other.LocalAddr().(*net.UDPAddr)
	}
	srflxConn, err := otherMux.GetConnForURL(localUfrag, "stun:test.invalid", responseAddr)
	require.NoError(t, err)
	host, err := NewCandidateHost(&CandidateHostConfig{
		Network: "udp4", Address: base.IP.String(), Port: base.Port, Component: ComponentRTP,
	})
	require.NoError(t, err)
	srflx, err := NewCandidateServerReflexive(&CandidateServerReflexiveConfig{
		Network: "udp4", Address: "203.0.113.8", Port: base.Port, Component: ComponentRTP,
		// Even a claimed matching base must not authorize another socket.
		RelAddr: base.IP.String(), RelPort: base.Port,
	})
	require.NoError(t, err)
	require.NoError(t, a.addCandidate(context.Background(), host, &hostUDPMuxConn{PacketConn: hostConn, mux: mux}, nil, false))
	require.NoError(t, a.addCandidate(context.Background(), srflx, srflxConn, nil, false))
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	peerAddr := peer.LocalAddr().(*net.UDPAddr)
	remote, err := NewCandidateHost(&CandidateHostConfig{
		Network: "udp4", Address: peerAddr.IP.String(), Port: peerAddr.Port, Component: ComponentRTP,
	})
	require.NoError(t, err)
	require.NoError(t, a.AddRemoteCandidate(remote))
	require.Eventually(t, func() bool {
		var ready bool
		err := a.loop.Run(context.Background(), func(context.Context) {
			ready = a.findPair(host, remote) != nil && a.findPair(srflx, remote) != nil
		})
		return err == nil && ready
	}, time.Second, time.Millisecond)
	require.NoError(t, a.SetRemoteCredentials(remoteUfrag, remotePwd))
	// Start the real candidate readers and controlled selector. Drive checks
	// explicitly so periodic checks cannot hide the ordering regression.
	require.NoError(t, a.loop.Run(context.Background(), func(context.Context) {
		a.setRole(controlling)
		a.startedFn()
		a.updateConnectionState(ConnectionStateChecking)
	}))
	readSTUN := func() *stun.Message {
		t.Helper()
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 1500)
		n, _, err := peer.ReadFromUDP(buf)
		require.NoError(t, err)
		msg := &stun.Message{Raw: buf[:n]}
		require.NoError(t, msg.Decode())
		return msg
	}
	ping := func(local Candidate) {
		t.Helper()
		require.NoError(t, a.loop.Run(context.Background(), func(context.Context) {
			a.getSelector().PingCandidate(local, remote)
		}))
	}

	if controlling {
		require.NoError(t, a.loop.Run(context.Background(), func(context.Context) {
			a.getSelector().(*controllingSelector).nominatePair(a.findPair(host, remote))
		}))
	} else {
		// Register the host reader, then deliver an authenticated nomination.
		// The controlled agent replies and sends its triggered check.
		ping(host)
		readSTUN()
		nomination, err := stun.Build(stun.BindingRequest, stun.TransactionID,
			stun.NewUsername(localUfrag+":"+remoteUfrag), UseCandidate(), AttrControlling(1),
			PriorityAttr(remote.Priority()), stun.NewShortTermIntegrity(localPwd), stun.Fingerprint)
		require.NoError(t, err)
		_, err = peer.WriteToUDP(nomination.Raw, base)
		require.NoError(t, err)
		response := readSTUN()
		require.Equal(t, stun.BindingSuccess, response.Type)
		require.Equal(t, nomination.TransactionID, response.TransactionID)
	}
	triggered := readSTUN()
	require.Equal(t, stun.BindingRequest, triggered.Type)
	if !controlling {
		require.NoError(t, a.loop.Run(context.Background(), func(context.Context) {
			require.True(t, a.findPair(host, remote).nominateOnBindingSuccess)
		}))
	}

	// Interleave one ordinary check before the triggered check's delayed
	// success. Both packets use the exact same physical UDP source port.
	ping(srflx)
	readSTUN()
	success, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(triggered.TransactionID),
		&stun.XORMappedAddress{IP: base.IP, Port: base.Port},
		stun.NewShortTermIntegrity(remotePwd), stun.Fingerprint)
	require.NoError(t, err)
	_, err = peer.WriteToUDP(success.Raw, responseAddr)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var processed bool
		err := a.loop.Run(context.Background(), func(context.Context) {
			processed = true
			for _, request := range a.pendingBindingRequests {
				if request.transactionID == triggered.TransactionID {
					processed = false
				}
			}
		})
		return err == nil && processed
	}, time.Second, time.Millisecond)

	// This is what Accept/Dial and the WebRTC handshake wait for. Merely
	// marking the other same-socket pair succeeded cannot complete it.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if differentSocket {
		require.ErrorIs(t, a.AwaitConnect(ctx), ErrCanceledByCaller)
		require.NoError(t, a.loop.Run(context.Background(), func(context.Context) {
			require.NotEqual(t, CandidatePairStateSucceeded, a.findPair(host, remote).state)
			require.NotEqual(t, CandidatePairStateSucceeded, a.findPair(srflx, remote).state)
		}))
		_, err := (&Conn{agent: a}).Write([]byte("screen frame"))
		require.ErrorIs(t, err, ErrNoCandidatePairs)
		return
	}
	require.NoError(t, a.AwaitConnect(ctx), "the nominated triggered check succeeded, so ICE must become connected")
	selected, err := a.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.True(t, host.Equal(selected.Local), "the response must complete its originating pair")
	conn := &Conn{agent: a}
	_, err = conn.Write([]byte("screen frame"))
	require.NoError(t, err)
	buf := make([]byte, 1500)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	n, _, err := peer.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Equal(t, "screen frame", string(buf[:n]))
}
