package screen

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// The TURN requests a test server is slow over.
const (
	turnAllocate         = 0x0003
	turnCreatePermission = 0x0008
)

// slowConn is a TURN server's socket that takes its time over each request
// of one kind, and counts them.
type slowConn struct {
	net.PacketConn
	request uint16
	delay   time.Duration
	asked   *atomic.Int32
}

func (c slowConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(p)
	if n >= 20 && binary.BigEndian.Uint16(p[:2]) == c.request {
		c.asked.Add(1)
		time.Sleep(c.delay)
	}
	return n, a, err
}

// slowTURN is a TURN server on this computer that takes delay over each
// request of one kind and says its relayed addresses are at relay. It
// forwards to this computer and to its relayed addresses, as a relay on the
// internet forwards to public addresses and its own. It returns its address
// (the user is "u", the password "p") and how many such requests came.
func slowTURN(t *testing.T, request uint16, delay time.Duration, relay net.IP) (string, *atomic.Int32) {
	t.Helper()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	asked := &atomic.Int32{}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(r *turn.RequestAttributes) (string, []byte, bool) {
			return r.Username, turn.GenerateAuthKey(r.Username, r.Realm, "p"), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            slowConn{udp, request, delay, asked},
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: relay, Address: "127.0.0.1"},
			PermissionHandler:     func(_ net.Addr, ip net.IP) bool { return ip.IsLoopback() || ip.Equal(relay) },
		}},
	})
	if err != nil {
		udp.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return udp.LocalAddr().String(), asked
}

type loopbackNet struct{ *systemNet }

func (n loopbackNet) Interfaces() ([]*transport.Interface, error) {
	all, err := n.systemNet.Interfaces()
	var out []*transport.Interface
	for _, iface := range all {
		if iface.Flags&net.FlagLoopback != 0 {
			out = append(out, iface)
		}
	}
	return out, err
}

// turnPair is two connections on this computer alone, each with a relayed
// address at a TURN server that takes delay over a permission and says its
// relayed addresses are at relay. The first is told of twelve more
// addresses of the second, on a private network, where nothing answers.
// It returns how long the two took to open a data channel, and how many
// permissions the server was asked for in that time and the settle after.
func turnPair(t *testing.T, delay time.Duration, relay net.IP, settle time.Duration) (time.Duration, int32) {
	t.Helper()
	server, asked := slowTURN(t, turnCreatePermission, delay, relay)
	n, err := newSystemNet()
	if err != nil {
		t.Fatal(err)
	}
	api, err := newAPI(nil, loopbackNet{n})
	if err != nil {
		t.Fatal(err)
	}
	cfg := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"turn:" + server + "?transport=udp"}, Username: "u", Credential: "p"}}}
	a, err := api.NewPeerConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := api.NewPeerConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dc, err := a.CreateDataChannel(inputChannel, nil)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	dc.OnOpen(func() { close(ready) })
	extras := ""
	for i := 1; i <= 12; i++ {
		extras += fmt.Sprintf("a=candidate:unreachable%d 1 udp 2130706431 192.168.77.%d 50000 typ host\r\n", i, i)
	}
	setLocal := func(pc *webrtc.PeerConnection, d webrtc.SessionDescription) webrtc.SessionDescription {
		t.Helper()
		done := webrtc.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(d); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("gathering did not finish")
		}
		out := *pc.LocalDescription()
		if !strings.Contains(out.SDP, "typ relay") {
			t.Fatal("the slow TURN path was not gathered")
		}
		out.SDP = strings.ReplaceAll(out.SDP, "a=end-of-candidates\r\n", extras+"a=end-of-candidates\r\n")
		return out
	}
	offer, err := a.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	offer = setLocal(a, offer)
	started := time.Now()
	if err := b.SetRemoteDescription(offer); err != nil {
		t.Fatal(err)
	}
	answer, err := b.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	answer = setLocal(b, answer)
	if err := a.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Until(started.Add(2 * time.Second))):
		t.Fatalf("slow TURN blocked the direct data channel: %v", time.Since(started))
	}
	took := time.Since(started)
	time.Sleep(settle)
	return took, asked.Load()
}

// A slow TURN server on this computer's network refuses the addresses it
// cannot reach. Its permissions must not hold up a direct path that works.
func TestSlowTURNDoesNotBlockDirectChecks(t *testing.T) {
	took, _ := turnPair(t, 150*time.Millisecond, net.ParseIP("127.0.0.1"), 0)
	if took > 2*time.Second {
		t.Fatalf("the direct data channel took %v", took)
	}
}

// A TURN server on the internet forwards to no private address, and every
// address here but the relayed ones is private: each end asks once, for the
// other's relayed address. Each pair of a relayed address and a private one
// used to ask at every check, for as long as the connection lasted.
func TestPublicTURNIsNotAskedForPrivatePeers(t *testing.T) {
	took, asked := turnPair(t, 0, net.ParseIP("203.0.113.5"), 3*time.Second)
	if took > 2*time.Second {
		t.Fatalf("the direct data channel took %v", took)
	}
	t.Logf("the TURN server was asked for %d permissions", asked)
	if asked > 2 {
		t.Errorf("the TURN server was asked for %d permissions, want one from each end", asked)
	}
}

// The probe asks from one socket after another how this end is seen by the
// TURN server's STUN port and by a relayed address on it. With no NAT
// between (the server is on this computer) each socket is seen at one port
// by both.
func TestProbeNAT(t *testing.T) {
	server, _ := slowTURN(t, turnAllocate, 0, net.ParseIP("127.0.0.1"))
	servers := []*relayv1.IceServer{{Urls: []string{"stun:" + server}}, {Urls: []string{"turn:" + server + "?transport=udp"}, Username: "u", Credential: "p"}}
	got := probeNAT(context.Background(), servers)
	t.Log(got)
	system, _, _ := strings.Cut(got, ";")
	m := regexp.MustCompile(`^system 127\.0\.0\.1:(\d+)>(\d+) (\d+)>(\d+)$`).FindStringSubmatch(system)
	if m == nil || m[1] != m[2] || m[3] != m[4] || m[1] == m[3] {
		t.Errorf("by the system's route: %q, want two sockets, each at one port for both destinations", system)
	}
	if got := probeNAT(context.Background(), servers[:1]); got != "" {
		t.Errorf("with no TURN server: %q", got)
	}
	if got := probeNAT(context.Background(), []*relayv1.IceServer{{Urls: []string{"turn:" + server + "?transport=udp"}, Username: "u", Credential: "wrong"}}); got != "" {
		t.Errorf("with a server that refuses the credential: %q", got)
	}
}
