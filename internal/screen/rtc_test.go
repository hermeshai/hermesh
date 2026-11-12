package screen

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
)

// offerWith gathers an offer's candidates against servers and returns the
// description and how long that took.
func offerWith(t *testing.T, servers []webrtc.ICEServer) (string, time.Duration) {
	t.Helper()
	api, err := newAPI(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	if _, err := pc.CreateDataChannel(inputChannel, nil); err != nil {
		t.Fatal(err)
	}
	o, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	g, err := gather(pc, o)
	if err != nil {
		t.Fatal(err)
	}
	g.wait(false)
	return pc.LocalDescription().SDP, time.Since(start)
}

// STUN and TURN servers nobody answers at (a network that blocks them) hold
// the offer up for gatherWithin, no longer, and it goes with the addresses
// it has. pion alone would wait 3 s for STUN and 7.8 s for TURN.
func TestGatheredGivesUpOnServersThatDoNotAnswer(t *testing.T) {
	dead := "192.0.2.1:3478" // TEST-NET-1: nothing answers there
	sdp, took := offerWith(t, []webrtc.ICEServer{
		{URLs: []string{"stun:" + dead}},
		{URLs: []string{"turn:" + dead + "?transport=udp", "turn:" + dead + "?transport=tcp"}, Username: "u", Credential: "p"},
	})
	if took < gatherWithin-100*time.Millisecond || took > gatherWithin+time.Second {
		t.Errorf("gathered in %v, want about %v", took, gatherWithin)
	}
	if !strings.Contains(sdp, "typ host") {
		t.Errorf("the offer has no local address:\n%s", sdp)
	}
}

// The offer goes as soon as a TURN address is in it: here a STUN server that
// does not answer would otherwise hold it up 3 s.
func TestGatheredGoesWithTheFirstTURNAddress(t *testing.T) {
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, turn.GenerateAuthKey(ra.Username, ra.Realm, "p"), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            udp,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	sdp, took := offerWith(t, []webrtc.ICEServer{
		{URLs: []string{"stun:192.0.2.1:3478"}},
		{URLs: []string{"turn:" + udp.LocalAddr().String() + "?transport=udp"}, Username: "u", Credential: "p"},
	})
	if took > time.Second {
		t.Errorf("gathered in %v, want it to go once the TURN address came", took)
	}
	if !strings.Contains(sdp, "typ relay") {
		t.Errorf("the offer has no TURN address:\n%s", sdp)
	}
}

// With early, the wait ends as soon as the local addresses are in, whatever
// the servers do: the offer can go, and the other candidates follow.
func TestGatheringEarlyDoesNotWaitForServers(t *testing.T) {
	api, err := newAPI(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dead := "192.0.2.1:3478" // TEST-NET-1: nothing answers there
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{
		{URLs: []string{"stun:" + dead}},
		{URLs: []string{"turn:" + dead + "?transport=udp"}, Username: "u", Credential: "p"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.CreateDataChannel(inputChannel, nil); err != nil {
		t.Fatal(err)
	}
	o, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	g, err := gather(pc, o)
	if err != nil {
		t.Fatal(err)
	}
	g.wait(true)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("the local addresses took %v", took)
	}
	sdp := pc.LocalDescription().SDP
	if !strings.Contains(sdp, "typ host") {
		t.Errorf("the offer has no local address:\n%s", sdp)
	}
	if iceUfrag(sdp) == "" {
		t.Errorf("no ICE username fragment in:\n%s", sdp)
	}
	if got := kinds(g.candidates()); !strings.HasPrefix(got, "host ") {
		t.Errorf("candidates: %q", got)
	}
	g.wait(false)
	if took := time.Since(start); took < gatherWithin-100*time.Millisecond || took > gatherWithin+time.Second {
		t.Errorf("the gathering ended after %v, want about %v", took, gatherWithin)
	}
}
