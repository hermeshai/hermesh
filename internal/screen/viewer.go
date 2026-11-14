package screen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
)

// ViewerConfig is what a device's side needs.
type ViewerConfig struct {
	// Log records the first packet/frame and the receive totals, locally.
	Log *slog.Logger
	// Connect is ScreenService.Connect on the machine, through its tunnel.
	Connect func(context.Context, *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error)
	// Candidates is ScreenService.Candidates on the machine. With it the
	// offer goes as soon as this device's local addresses are in it, and the
	// other candidates follow; nil: the offer waits for all of them.
	Candidates func(context.Context, *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error)
	// Name is this device's, for the notice on the machine.
	Name string
	// Pointer: whether the picture should have the pointer in it.
	Pointer bool
	// Servers are the relay's STUN and TURN servers, when it has any.
	Servers []*relayv1.IceServer
	// Fresh are those servers as they are when the session looks for a path
	// again: the credentials of Servers last an hour, and on another
	// network another server may be the near one. nil, or none returned,
	// keeps the ones the session has.
	Fresh func(context.Context) []*relayv1.IceServer
	// NoRelay is called when the servers gave this device no relayed
	// address: the next ones asked for should be looked at again.
	NoRelay func()
	// Frame gets each frame, whole and in order, as Annex B; and, while the
	// machine measures the picture's path, when it passed each stage.
	Frame func(key bool, data []byte, trace *Trace)
	// Event gets what the machine reports: the picture's size, its state.
	Event func(*screenv1.ScreenEvent)
	// Status gets the connection's state as it changes, and every few seconds.
	Status func(Status)
}

// Status is the connection as the viewer sees it.
type Status struct {
	// Phase: "connecting", "connected", "reconnecting".
	Phase string `json:"phase"`
	// Path: "direct", or "relay" when the packets go through a TURN server;
	// empty while the path in use is not known.
	Path string `json:"path,omitempty"`
	// RTT is the round trip to the machine, in milliseconds.
	RTT int `json:"rtt_ms,omitempty"`
	// Local and Remote are the two ends of the path in use, each as its kind
	// and address ("host 192.168.1.5:50123", "srflx …", "relay …"), for
	// telling what a slow session goes through.
	Local  string `json:"local,omitempty"`
	Remote string `json:"remote,omitempty"`
	// Nearby, with Path "relay": the other end has an address on one of
	// this device's networks, or both ends reach the internet from one
	// address, so they are most likely on one network, where a direct path
	// should have worked. Something there keeps them apart: the app or the
	// machine not allowed on the local network, or the network itself.
	Nearby bool `json:"nearby,omitempty"`
}

// Viewer is a session with a machine, seen from the device.
type Viewer struct {
	cfg     ViewerConfig
	pc      *webrtc.PeerConnection
	input   *webrtc.DataChannel
	pointer *webrtc.DataChannel
	session string
	started time.Time

	omu     sync.Mutex // offers and applying candidate batches, one at a time
	pending *viewerOffer

	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	err  error
	ssrc uint32 // the picture's track, once it came
	// offers counts the offers sent: what was found for an earlier one is
	// not used once another went.
	offers int
	// full: the machine takes no candidates after the offer (it is older),
	// so every offer waits for all of them.
	full bool
	// theirs are the machine's candidates that came after its answer.
	theirs []string
	// servers are the relay's servers the connection has now.
	servers []*relayv1.IceServer
}

// A failed signaling call leaves Pion in have-local-offer. Keep that exact
// offer until its answer arrives; creating another offer is invalid there.
type viewerOffer struct {
	g       *gathering
	n       int
	request *screenv1.ConnectRequest
}

// Dial opens a session: it returns once the machine has answered, and the
// picture follows when a path between the two is found (Status).
func Dial(ctx context.Context, cfg ViewerConfig) (*Viewer, error) {
	network := blockedNet(cfg.Servers)
	if network == nil {
		var err error
		network, err = newSystemNet()
		if err != nil {
			return nil, err
		}
	}
	punch := &ice.BoundedNATPunchNet{Net: network, Report: func(message string) {
		if cfg.Log != nil {
			cfg.Log.Info("screen: NAT punch", "detail", message)
		}
	}}
	api, err := newAPI(nil, punch, nil)
	if err != nil {
		return nil, err
	}
	conf := webrtc.Configuration{ICEServers: iceServers(cfg.Servers)}
	if os.Getenv("HERMESH_SCREEN_RELAY_ONLY") != "" { // the tests: as where no direct path exists
		conf.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	pc, err := api.NewPeerConnection(conf)
	if err != nil {
		return nil, err
	}
	v := &Viewer{cfg: cfg, pc: pc, done: make(chan struct{}), servers: cfg.Servers, started: time.Now()}
	fail := func(err error) (*Viewer, error) {
		pc.Close()
		return nil, err
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return fail(err)
	}
	if v.input, err = pc.CreateDataChannel(inputChannel, nil); err != nil {
		return fail(err)
	}
	unordered, none := false, uint16(0)
	if v.pointer, err = pc.CreateDataChannel(pointerChannel, &webrtc.DataChannelInit{Ordered: &unordered, MaxRetransmits: &none}); err != nil {
		return fail(err)
	}
	v.input.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev screenv1.ScreenEvent
		if proto.Unmarshal(m.Data, &ev) != nil {
			return
		}
		if cfg.Event != nil {
			cfg.Event(&ev)
		}
		switch ev.GetState().GetError() {
		case "ended":
			go v.close(errors.New("the machine ended the session"))
		case "locked":
			go v.close(errors.New(ErrLocked))
		}
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go v.receive(t) })
	// The path can change while connected (newAPI): say so as it does.
	pc.SCTP().Transport().ICETransport().OnSelectedCandidatePairChange(func(*webrtc.ICECandidatePair) {
		if pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			go v.status("connected")
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateConnected:
			v.status("connected")
		case webrtc.PeerConnectionStateDisconnected:
			v.status("reconnecting")
		case webrtc.PeerConnectionStateFailed:
			go v.close(errors.New("the connection to the machine was lost"))
		case webrtc.PeerConnectionStateClosed:
			go v.close(nil)
		}
	})
	v.status("connecting")
	if err := v.offer(ctx, false); err != nil {
		return fail(err)
	}
	go v.watch()
	return v, nil
}

// offer sends an offer and takes the machine's answer: the first, or one
// that restarts ICE on the session under way. Where the machine takes the
// candidates later, the offer goes with this device's local addresses and
// the rest are exchanged once they are found (exchange): two ends on one
// network find each other without waiting for the relay's servers.
func (v *Viewer) offer(ctx context.Context, restart bool) error {
	v.omu.Lock()
	defer v.omu.Unlock()
	if v.pending != nil {
		if err := v.finishOffer(ctx); err != nil {
			return err
		}
		// The network may have changed again while signaling was down.
		// Once stable, gather a fresh offer for the current interfaces.
	}
	if restart && v.cfg.Fresh != nil {
		if list := v.cfg.Fresh(ctx); len(list) > 0 {
			conf := v.pc.GetConfiguration()
			conf.ICEServers = iceServers(list)
			v.pc.SetConfiguration(conf) // takes effect at the gathering this offer starts
			v.mu.Lock()
			v.servers = list
			v.mu.Unlock()
		}
	}
	o, err := v.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	if err != nil {
		return err
	}
	g, err := gather(v.pc, o, v.cfg.NoRelay)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.offers++
	n, later := v.offers, v.cfg.Candidates != nil && !v.full
	v.theirs = nil
	v.mu.Unlock()
	g.wait(later)
	sdp := v.pc.LocalDescription().SDP
	v.pending = &viewerOffer{g: g, n: n, request: &screenv1.ConnectRequest{
		Offer: sdp, SessionId: v.session, Viewer: v.cfg.Name, Pointer: v.cfg.Pointer, CandidatesLater: later,
	}}
	return v.finishOffer(ctx)
}

// finishOffer is called with omu held, including when a later Restart resumes
// an interrupted exchange. A transient failure must not discard its offer.
func (v *Viewer) finishOffer(ctx context.Context) error {
	p := v.pending
	g, n, sdp, later := p.g, p.n, p.request.Offer, p.request.CandidatesLater
	asked := time.Now()
	res, tries, err := v.connect(ctx, p.request)
	if err != nil {
		return err
	}
	v.session = res.SessionId
	if err := v.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: res.Answer}); err != nil {
		return err
	}
	v.pending = nil
	switch {
	case later && res.CandidatesLater:
		go v.exchange(g, n, res.SessionId, iceUfrag(sdp))
		go v.report(g, n, res.SessionId, iceUfrag(sdp), fmt.Sprintf("answered in %v, try %d", time.Since(asked).Round(time.Millisecond), tries))
	case later:
		// The machine takes none after all (an older one, its state not yet
		// read again): it knows this device's local addresses alone, which
		// is enough on one network. Else an offer with all of this device's
		// candidates follows, as every later one is.
		v.mu.Lock()
		v.full = true
		v.mu.Unlock()
		go func() {
			g.wait(false)
			if !g.addsTo(sdp) {
				return // the older machine already has every candidate
			}
			for i := 0; i < 20 && v.current(n) && !v.Connected(); i++ {
				time.Sleep(50 * time.Millisecond)
			}
			if v.current(n) && !v.Connected() {
				ctx, cancel := v.within(10 * time.Second)
				defer cancel()
				v.offer(ctx, true)
			}
		}()
	}
	return nil
}

// An initial answer that arrives after the host's ICE deadline is useless.
// Retry transient signaling failures with a fresh host session initially;
// established sessions retry the same offer on the same session.
func (v *Viewer) connect(ctx context.Context, req *screenv1.ConnectRequest) (res *screenv1.ConnectResponse, tries int, err error) {
	for tries < 3 {
		tries++
		attempt, cancel := context.WithTimeout(ctx, 6*time.Second)
		res, err = v.cfg.Connect(attempt, req)
		cancel()
		if err == nil || ctx.Err() != nil || (connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeDeadlineExceeded && !errors.Is(err, context.DeadlineExceeded)) {
			break
		}
	}
	return res, tries, err
}

// report tells the machine, for its log, how the offer numbered n went on
// this side: how long its answer took, and how this device's network maps
// its packets (probeNAT). The owner reads a session's story in one place.
func (v *Viewer) report(g *gathering, n int, session, ufrag, answered string) {
	ctx, cancel := v.within(15 * time.Second)
	defer cancel()
	v.mu.Lock()
	servers := v.servers
	v.mu.Unlock()
	text := answered
	if mapped := probeNAT(ctx, servers); mapped != "" {
		text += "; " + mapped
	}
	if !v.current(n) {
		return
	}
	mine, all := g.found()
	res, err := v.cfg.Candidates(ctx, &screenv1.CandidatesRequest{SessionId: session, Ufrag: ufrag, Candidates: mine, Complete: all, Network: text})
	if err == nil {
		v.addCandidates(n, res.Candidates)
	}
}

// exchange gives the machine the candidates found for the offer numbered n
// and takes the machine's: first once this end has a TURN address or
// gatherWithin has passed, then again as either end finds more (a TURN
// server that is slow, its second transport), until both have found all of
// theirs or exchangeWithin has passed. Every call carries all of this
// end's candidates so far, so one that is lost costs nothing.
func (v *Viewer) exchange(g *gathering, n int, session, ufrag string) {
	g.wait(false)
	sent, have, theirs, failed := -1, 0, false, 0
	for v.current(n) && failed < 3 {
		mine, all := g.found()
		if len(mine) != sent || !theirs {
			ctx, cancel := v.within(10 * time.Second)
			res, err := v.cfg.Candidates(ctx, &screenv1.CandidatesRequest{SessionId: session, Ufrag: ufrag, Candidates: mine, Complete: all, Have: uint32(have)})
			cancel()
			if err == nil {
				err = v.addCandidates(n, res.Candidates)
			}
			if err != nil {
				failed++
				select {
				case <-v.done:
				case <-time.After(time.Second):
				}
				continue
			}
			// A machine that answers once has given all it will.
			sent, have, theirs, failed = len(mine), len(res.Candidates), res.Complete || !res.Again, 0
		}
		left := time.Until(g.start.Add(exchangeWithin))
		if all && theirs || left <= 0 {
			return
		}
		if theirs { // else the machine's call waits for its next one
			g.grown(sent, left)
		}
	}
}

// addCandidates keeps the generation check and the whole batch on the
// same side of a restart. The RPC itself must not hold omu: it can be slow.
func (v *Viewer) addCandidates(n int, candidates []string) error {
	v.omu.Lock()
	defer v.omu.Unlock()
	if !v.current(n) {
		return nil
	}
	for _, c := range candidates {
		if err := v.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c}); err != nil {
			return err
		}
	}
	v.mu.Lock()
	v.theirs = candidates
	v.mu.Unlock()
	return nil
}

// current says whether the offer numbered n is the latest, in a session
// that goes on.
func (v *Viewer) current(n int) bool {
	select {
	case <-v.done:
		return false
	default:
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.offers == n
}

// within is a context that ends after d, or with the session.
func (v *Viewer) within(d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	go func() {
		select {
		case <-v.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// Restart finds a new path to the machine: this device's network changed.
func (v *Viewer) Restart(ctx context.Context) error { return v.offer(ctx, true) }

// receive turns the track's packets into frames, and asks for a keyframe
// while there is none to start from.
func (v *Viewer) receive(t *webrtc.TrackRemote) {
	v.omu.Lock()
	session := v.session
	v.omu.Unlock()
	var packets, frames int
	defer func() {
		if v.cfg.Log != nil {
			v.cfg.Log.Info("screen: viewer receive ended", "session", session, "packets", packets, "frames", frames)
		}
	}()
	v.mu.Lock()
	v.ssrc = uint32(t.SSRC())
	v.mu.Unlock()
	a := newAssembler()
	a.roundTrip(v.roundTrip())
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		var asked time.Time
		for {
			select {
			case <-stop:
				return
			case <-v.done:
				return
			case now := <-tick.C:
				// A keyframe asked for takes a round trip to come: not asked
				// for again before it could have.
				wait := a.roundTrip(v.roundTrip())
				if a.wantKey(now) && now.Sub(asked) >= max(400*time.Millisecond, wait) {
					asked = now
					v.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(t.SSRC())}})
				}
			}
		}
	}()
	for {
		p, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		packets++
		if packets == 1 && v.cfg.Log != nil {
			v.cfg.Log.Info("screen: viewer first packet", "session", session, "after", time.Since(v.started).Round(time.Millisecond))
		}
		for _, f := range a.push(p, time.Now()) {
			frames++
			if frames == 1 && v.cfg.Log != nil {
				v.cfg.Log.Info("screen: viewer first frame", "session", session, "after", time.Since(v.started).Round(time.Millisecond), "bytes", len(f.data), "key", f.key)
			}
			if v.cfg.Frame != nil {
				data, trace := takeTrace(f.data)
				if trace != nil {
					trace.Bytes, trace.Key = len(data), f.key
				}
				v.cfg.Frame(f.key, data, trace)
			}
		}
	}
}

// Read the transport directly: first-frame recovery must not wait for a
// UI status update (or depend on a Status callback being installed).
func (v *Viewer) roundTrip() time.Duration {
	stats, ok := v.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePairStats()
	if ok && stats.CurrentRoundTripTime > 0 {
		return time.Duration(stats.CurrentRoundTripTime * float64(time.Second))
	}
	// Until the first measurement, leave one retransmission time to arrive.
	return 500 * time.Millisecond
}

func (v *Viewer) status(phase string) {
	if v.cfg.Status == nil {
		return
	}
	st := Status{Phase: phase}
	if phase == "connected" {
		v.path(&st)
	}
	v.cfg.Status(st)
}

// path fills in how the packets travel now, and the round trip that way.
// The path can change while connected (newAPI): this is the one in use.
func (v *Viewer) path(st *Status) {
	ice := v.pc.SCTP().Transport().ICETransport()
	pair, err := ice.GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return
	}
	st.Path = "direct"
	if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		st.Path = "relay"
	}
	st.Local, st.Remote = pathEnd(pair.Local), pathEnd(pair.Remote)
	if stats, ok := ice.GetSelectedCandidatePairStats(); ok {
		st.RTT = int(stats.CurrentRoundTripTime * 1000)
	}
	if st.Path == "relay" {
		v.mu.Lock()
		theirs := v.theirs
		v.mu.Unlock()
		remote := v.pc.RemoteDescription()
		st.Nearby = sameAddress(addresses("srflx", v.pc.LocalDescription(), nil), addresses("srflx", remote, theirs)) ||
			onNetworks(localNetworks(), addresses("host", remote, theirs))
	}
}

// localNetworks are the networks this device is on, by its addresses, but
// for loopback and link-local ones.
func localNetworks() []*net.IPNet {
	addrs, _ := net.InterfaceAddrs()
	var out []*net.IPNet
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
			out = append(out, n)
		}
	}
	return out
}

// onNetworks says whether one of the other end's addresses is on one of
// these networks, and is not this end's own there (two ends behind the same
// kind of VPN have the same address on it).
func onNetworks(networks []*net.IPNet, theirs []string) bool {
	for _, a := range theirs {
		ip := net.ParseIP(a)
		for _, n := range networks {
			if ip != nil && n.Contains(ip) && !n.IP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// addresses are an end's addresses of one kind: "srflx", where the relay's
// STUN server sees it, or "host", its own. They are read from its
// description and from the candidates that came later.
func addresses(kind string, d *webrtc.SessionDescription, later []string) []string {
	lines := later
	if d != nil {
		lines = append(strings.Split(d.SDP, "\n"), later...)
	}
	var out []string
	for _, line := range lines {
		// [a=]candidate:<foundation> <component> <transport> <priority> <address> <port> typ <type> ...
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 8 && strings.HasPrefix(strings.TrimPrefix(f[0], "a="), "candidate:") && f[6] == "typ" && f[7] == kind {
			out = append(out, f[4])
		}
	}
	return out
}

func sameAddress(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// watch reports the path and the round trip while connected.
func (v *Viewer) watch() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-v.done:
			return
		case <-tick.C:
			if v.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
				v.status("connected")
			}
		}
	}
}

// Send passes one Input (as protobuf) to the machine. A move may be lost
// on the way, since the next one replaces it; everything else arrives, in
// order.
func (v *Viewer) Send(move bool, input []byte) error {
	dc := v.input
	if move {
		dc = v.pointer
	}
	if dc.ReadyState() != webrtc.DataChannelStateOpen {
		return errors.New("not connected")
	}
	return dc.Send(input)
}

// RequestKeyframe asks the machine for a frame that needs none before it:
// whatever shows the frames lost its place.
func (v *Viewer) RequestKeyframe() {
	v.mu.Lock()
	ssrc := v.ssrc
	v.mu.Unlock()
	if ssrc != 0 {
		v.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
	}
}

// Connected says whether the picture has a path now.
func (v *Viewer) Connected() bool {
	return v.pc.ConnectionState() == webrtc.PeerConnectionStateConnected
}

// Done is closed when the session has ended; Err says why, nil when this
// side closed it.
func (v *Viewer) Done() <-chan struct{} { return v.done }

func (v *Viewer) Err() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.err
}

func (v *Viewer) close(err error) {
	v.once.Do(func() {
		v.mu.Lock()
		v.err = err
		v.mu.Unlock()
		close(v.done)
		v.pc.Close()
	})
}

// Close ends the session.
func (v *Viewer) Close() { v.close(nil) }
