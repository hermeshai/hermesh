package screen

import (
	"strings"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"
)

// Exercise the same ICE -> WebRTC -> SDP conversion used by the app. The
// capability must survive it, and a peer without the extension must still
// exchange actual input data with an enabled peer.
func TestPunchCapabilityThroughWebRTC(t *testing.T) {
	for _, both := range []bool{false, true} {
		name := "older peer"
		if both {
			name = "both peers"
		}
		t.Run(name, func(t *testing.T) {
			makePeer := func(enabled bool) *webrtc.PeerConnection {
				t.Helper()
				n, err := newSystemNet()
				if err != nil {
					t.Fatal(err)
				}
				var network transport.Net = loopbackNet{n}
				if enabled {
					network = &ice.BoundedNATPunchNet{Net: network}
				}
				api, err := newAPI(nil, network, nil)
				if err != nil {
					t.Fatal(err)
				}
				pc, err := api.NewPeerConnection(webrtc.Configuration{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { pc.Close() })
				return pc
			}
			a, b := makePeer(true), makePeer(both)
			received, sent := make(chan string, 1), make(chan error, 1)
			b.OnDataChannel(func(c *webrtc.DataChannel) {
				c.OnMessage(func(m webrtc.DataChannelMessage) { received <- string(m.Data) })
			})
			dc, err := a.CreateDataChannel(inputChannel, nil)
			if err != nil {
				t.Fatal(err)
			}
			dc.OnOpen(func() { sent <- dc.SendText("input over negotiated ICE") })
			offer, err := a.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			exchange := func(from, to *webrtc.PeerConnection, d webrtc.SessionDescription, enabled bool) {
				t.Helper()
				g, err := gather(from, d, nil)
				if err != nil {
					t.Fatal(err)
				}
				g.wait(false)
				if got := strings.Contains(from.LocalDescription().SDP, "hermesh-punch 1"); got != enabled {
					t.Fatalf("capability in SDP=%v, enabled=%v", got, enabled)
				}
				for _, text := range g.candidates() {
					candidate, err := ice.UnmarshalCandidate(strings.TrimPrefix(text, "candidate:"))
					if err != nil {
						t.Fatal(err)
					}
					ext, ok := candidate.GetExtension("hermesh-punch")
					if ok != enabled || ok && ext.Value != "1" {
						t.Fatalf("trickle candidate capability=%v, enabled=%v", ok, enabled)
					}
				}
				if err := to.SetRemoteDescription(*from.LocalDescription()); err != nil {
					t.Fatal(err)
				}
			}
			exchange(a, b, offer, true)
			answer, err := b.CreateAnswer(nil)
			if err != nil {
				t.Fatal(err)
			}
			exchange(b, a, answer, both)
			select {
			case err := <-sent:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("data channel did not open")
			}
			select {
			case got := <-received:
				if got != "input over negotiated ICE" {
					t.Fatalf("input=%q", got)
				}
			case <-time.After(time.Second):
				t.Fatal("input did not arrive")
			}
		})
	}
}
