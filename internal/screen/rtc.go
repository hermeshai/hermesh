package screen

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/transport/v5"
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
func newAPI(estimate func(cc.BandwidthEstimator), network transport.Net) (*webrtc.API, error) {
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
	// works too. pion's 0.5/1/2 s are for candidates that trickle in one by
	// one; here each end has the other's in two lots at most (the local
	// addresses, then the rest together), so a better path answers about as
	// soon as a worse one. Through TURN, waiting longer gains nothing when
	// no direct path exists, which is when it is taken; and the path moves
	// to a better one that answers later (below).
	s.SetSrflxAcceptanceMinWait(100 * time.Millisecond)
	s.SetPrflxAcceptanceMinWait(300 * time.Millisecond)
	s.SetRelayAcceptanceMinWait(time.Second)
	// The path taken first need not be the one kept: the viewer goes on
	// checking the others every second and moves to a better one that
	// answers (always from the relay to a local address), a second apart at
	// least: pion's 3 s made three moves in a row take 8 s. An iPhone asks
	// its owner before the app's first packet to the local network, and
	// drops what is sent meanwhile; the session starts through the relay
	// and moves once the owner allowed it. For that, no pair is given up
	// while the first path is looked for (pion's 7 checks are 1.4 s).
	if err := s.SetICERenomination(webrtc.WithRenominationInterval(time.Second)); err != nil {
		return nil, err
	}
	s.SetICEMaxBindingRequests(100)
	if network == nil {
		var err error
		network, err = newSystemNet()
		if err != nil {
			return nil, err
		}
	}
	s.SetNet(network)
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

// gatherWithin: the longest the candidates are gathered for; settle: how
// long after the latest local address the local ones count as all in
// (variables for the tests).
var (
	gatherWithin = 2 * time.Second
	settle       = 30 * time.Millisecond
)

// relayWithin: how long after an offer a relayed address has to come from
// the TURN servers given, before they are doubted (a variable for the
// tests). An allocation is two round trips, three over TCP.
var relayWithin = 5 * time.Second

// exchangeWithin: how long after an offer the two ends go on telling each
// other of candidates they find. pion gives up on a STUN server after 3 s
// and on a TURN allocation after 7.8 s.
const exchangeWithin = 15 * time.Second

// gathering follows the candidates a connection finds for one offer or
// answer.
type gathering struct {
	start time.Time

	mu      sync.Mutex
	list    []string      // each as a "candidate:" line
	last    time.Time     // when the latest came
	enough  bool          // all are in, or a TURN address is
	all     bool          // all are in
	relayed bool          // a TURN address is among them
	more    chan struct{} // closed, and made anew, as each comes
}

// gather sets pc's local description, which starts the gathering. noRelay,
// when not nil, is called if pc has TURN servers and no relayed address
// came from them within relayWithin.
func gather(pc *webrtc.PeerConnection, d webrtc.SessionDescription, noRelay func()) (*gathering, error) {
	g := &gathering{start: time.Now(), more: make(chan struct{})}
	turns := slices.ContainsFunc(pc.GetConfiguration().ICEServers, func(s webrtc.ICEServer) bool { return s.Username != "" })
	if noRelay != nil && turns {
		time.AfterFunc(relayWithin, func() {
			g.mu.Lock()
			relayed := g.relayed
			g.mu.Unlock()
			if !relayed && pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
				noRelay()
			}
		})
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if c == nil {
			g.enough, g.all = true, true
		} else {
			g.list, g.last = append(g.list, c.ToJSON().Candidate), time.Now()
			// A TURN address is the last kind to come; the second TURN
			// transport adds nothing.
			g.relayed = g.relayed || c.Typ == webrtc.ICECandidateTypeRelay
			g.enough = g.enough || g.relayed
		}
		close(g.more)
		g.more = make(chan struct{})
	})
	if err := pc.SetLocalDescription(d); err != nil {
		return nil, err
	}
	return g, nil
}

// wait returns once the candidates found are what the handshake goes on
// with: all of them, or a TURN address among them, or what gatherWithin
// gave, since a server that does not answer would otherwise hold the
// handshake up for seconds (STUN 3 s, TURN over UDP 7.8 s). With early it
// returns as soon as the local addresses are in, which takes no server: the
// others follow in ScreenService.Candidates.
func (g *gathering) wait(early bool) {
	limit := time.NewTimer(time.Until(g.start.Add(gatherWithin)))
	defer limit.Stop()
	for {
		g.mu.Lock()
		enough, n, last, more := g.enough, len(g.list), g.last, g.more
		g.mu.Unlock()
		if enough {
			return
		}
		var settled <-chan time.Time
		if early && n > 0 {
			left := settle - time.Since(last)
			if left <= 0 {
				return
			}
			settled = time.After(left)
		}
		select {
		case <-more:
		case <-settled:
		case <-limit.C:
			return
		}
	}
}

// candidates are those found so far.
func (g *gathering) candidates() []string {
	list, _ := g.found()
	return list
}

// found are the candidates found so far, and whether they are all of them.
func (g *gathering) found() ([]string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.list), g.all
}

// grown waits until there are more candidates than n or all are in, for d
// at most.
func (g *gathering) grown(n int, d time.Duration) {
	limit := time.NewTimer(d)
	defer limit.Stop()
	for {
		g.mu.Lock()
		done, more := g.all || len(g.list) > n, g.more
		g.mu.Unlock()
		if done {
			return
		}
		select {
		case <-more:
		case <-limit.C:
			return
		}
	}
}

// addsTo says whether an older peer needs a second offer at all. Restarting
// with exactly the same candidates interrupts a path still connecting.
func (g *gathering) addsTo(sdp string) bool {
	known := map[string]bool{}
	for _, line := range strings.Split(sdp, "\n") {
		known[strings.TrimSpace(line)] = true
	}
	for _, c := range g.candidates() {
		if !known["a="+c] {
			return true
		}
	}
	return false
}

// iceUfrag is the ICE username fragment of a description: what tells one
// offer's candidates from the next one's.
func iceUfrag(sdp string) string {
	for _, line := range strings.Split(sdp, "\n") {
		if u, ok := strings.CutPrefix(strings.TrimSpace(line), "a=ice-ufrag:"); ok {
			return u
		}
	}
	return ""
}

// kinds counts candidates by kind, for the log: "host 3, srflx 1, relay 2".
// They are "candidate:" lines, or a description's lines.
func kinds(lines []string) string {
	n := map[string]int{}
	for _, line := range lines {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 8 && strings.HasPrefix(strings.TrimPrefix(f[0], "a="), "candidate:") && f[6] == "typ" {
			n[f[7]]++
		}
	}
	var out []string
	for _, k := range []string{"host", "srflx", "prflx", "relay"} {
		if n[k] > 0 {
			out = append(out, fmt.Sprintf("%s %d", k, n[k]))
		}
	}
	return strings.Join(out, ", ")
}

// candidateAddresses is local diagnostics, never SDP credentials. A host
// address can be a VPN or a USB debug tunnel rather than the Wi-Fi network.
func candidateAddresses(lines []string) string {
	seen := map[string]bool{}
	var out []string
	for _, line := range lines {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 8 || !strings.HasPrefix(strings.TrimPrefix(f[0], "a="), "candidate:") || f[6] != "typ" {
			continue
		}
		address := fmt.Sprintf("%s %s/%s", f[7], net.JoinHostPort(f[4], f[5]), strings.ToLower(f[2]))
		if !seen[address] {
			seen[address] = true
			out = append(out, address)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// pathEnd is one end of a path, as its kind and address: "host
// 192.168.1.5:50123".
func pathEnd(c *webrtc.ICECandidate) string {
	return fmt.Sprintf("%s %s", c.Typ, net.JoinHostPort(c.Address, strconv.Itoa(int(c.Port))))
}
