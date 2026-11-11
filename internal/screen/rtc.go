package screen

import (
	"net"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/webrtc/v4"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// What both ends must name alike.
const (
	inputChannel   = "input"   // ordered and reliable
	pointerChannel = "pointer" // unordered, nothing sent twice
)

var h264 = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeH264,
	ClockRate:   90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640032",
}

// Bitrates the sender moves between as the network allows.
const (
	minBitrate   = 400_000
	startBitrate = 2_500_000
	maxBitrate   = 8_000_000
)

// newAPI is pion set up the same way on both ends: H.264 only, lost packets
// asked for again, and on the sending side a bandwidth estimate (estimate
// receives it once the connection has a stream).
func newAPI(estimate func(cc.BandwidthEstimator)) (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: h264, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	i := &interceptor.Registry{}
	if estimate != nil {
		f, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
			// No pacer: pion's hands packets on every 5 ms, which holds each
			// frame back 2.7 ms at the median (docs/computer-control.md §5).
			// A frame is a few kilobytes and leaves at once; the encoder's
			// bitrate, not a queue here, is what follows the network.
			return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(startBitrate), gcc.SendSideBWEMinBitrate(minBitrate), gcc.SendSideBWEMaxBitrate(maxBitrate),
				gcc.SendSideBWEPacer(gcc.NewNoOpPacer()))
		})
		if err != nil {
			return nil, err
		}
		f.OnNewPeerConnection(func(_ string, e cc.BandwidthEstimator) { estimate(e) })
		i.Add(f)
		if err := webrtc.ConfigureTWCCHeaderExtensionSender(m, i); err != nil {
			return nil, err
		}
	}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, err
	}
	var s webrtc.SettingEngine
	// No mDNS names: an iPhone app may not send multicast without asking.
	s.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	// A viewer on the machine itself (the Simulator, the tests).
	s.SetIncludeLoopbackCandidate(true)
	// None of the other end's IPv6 link-local addresses. Every interface has
	// one (VPNs, virtual machines, AirDrop: a Mac here had 17), none carries
	// its zone across, and checking them held the first picture up 2–6 s on
	// one computer; without them, 30–300 ms. A filter on this end's own
	// addresses would be quicker still, but pion then asks STUN and TURN from
	// each address apart: a dozen TURN allocations a session.
	s.SetRemoteIPFilter(func(ip net.IP) bool { return !ip.IsLinkLocalUnicast() })
	s.SetICETimeouts(5*time.Second, 12*time.Second, time.Second)
	// A STUN or TURN server that does not answer must not hold the handshake up for long.
	s.SetSTUNGatherTimeout(3 * time.Second)
	// How long a working path waits before it is taken, in case a better one
	// works too. pion's 0.5/1/2 s are for candidates that trickle in; here
	// both ends have all of them before the first check, so a better path
	// answers about as soon as a worse one. Through TURN, waiting longer
	// gains nothing when no direct path exists, which is when it is taken.
	s.SetSrflxAcceptanceMinWait(100 * time.Millisecond)
	s.SetPrflxAcceptanceMinWait(300 * time.Millisecond)
	s.SetRelayAcceptanceMinWait(time.Second)
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i), webrtc.WithSettingEngine(s)), nil
}

// iceServers are the relay's servers as pion takes them.
func iceServers(list []*relayv1.IceServer) []webrtc.ICEServer {
	var out []webrtc.ICEServer
	for _, s := range list {
		out = append(out, webrtc.ICEServer{URLs: s.Urls, Username: s.Username, Credential: s.Credential})
	}
	return out
}

// gatherWithin: the longest the candidates are gathered for (a variable for
// the tests).
var gatherWithin = 2 * time.Second

// gathered sets pc's local description and returns it with the candidates
// it has: the handshake is one call each way, with nothing trickled. It
// returns once gathering is done, or once a TURN address came (the last
// kind to come; the second TURN transport adds nothing), or after
// gatherWithin: a server that does not answer would otherwise hold the
// handshake up for seconds (STUN 3 s, TURN over UDP 7.8 s).
func gathered(pc *webrtc.PeerConnection, d webrtc.SessionDescription) (string, error) {
	enough := make(chan struct{})
	var once sync.Once
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || c.Typ == webrtc.ICECandidateTypeRelay {
			once.Do(func() { close(enough) })
		}
	})
	if err := pc.SetLocalDescription(d); err != nil {
		return "", err
	}
	t := time.NewTimer(gatherWithin)
	defer t.Stop()
	select {
	case <-enough:
	case <-t.C:
	}
	return pc.LocalDescription().SDP, nil
}
