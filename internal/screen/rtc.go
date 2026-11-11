package screen

import (
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
	s.SetICETimeouts(5*time.Second, 12*time.Second, time.Second)
	// A STUN or TURN server that does not answer must not hold the handshake up for long.
	s.SetSTUNGatherTimeout(3 * time.Second)
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

// gathered sets pc's local description and returns it once every candidate
// is in it: the handshake is one call each way, with nothing trickled.
func gathered(pc *webrtc.PeerConnection, d webrtc.SessionDescription) (string, error) {
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(d); err != nil {
		return "", err
	}
	<-done
	return pc.LocalDescription().SDP, nil
}
