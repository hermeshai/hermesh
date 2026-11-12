package screen

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
)

// ViewerConfig is what a device's side needs.
type ViewerConfig struct {
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
	// Nearby, with Path "relay": both ends reach the internet from one
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

	omu sync.Mutex // one offer at a time

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
}

// Dial opens a session: it returns once the machine has answered, and the
// picture follows when a path between the two is found (Status).
func Dial(ctx context.Context, cfg ViewerConfig) (*Viewer, error) {
	api, err := newAPI(nil, blockedNet(cfg.Servers))
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
	v := &Viewer{cfg: cfg, pc: pc, done: make(chan struct{})}
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
	o, err := v.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	if err != nil {
		return err
	}
	g, err := gather(v.pc, o)
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
	res, err := v.cfg.Connect(ctx, &screenv1.ConnectRequest{Offer: sdp, SessionId: v.session, Viewer: v.cfg.Name, Pointer: v.cfg.Pointer, CandidatesLater: later})
	if err != nil {
		return err
	}
	v.session = res.SessionId
	if err := v.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: res.Answer}); err != nil {
		return err
	}
	switch {
	case later && res.CandidatesLater:
		go v.exchange(g, n, res.SessionId, iceUfrag(sdp))
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

// exchange gives the machine the candidates found for the offer numbered n
// once the gathering has ended, and takes the machine's.
func (v *Viewer) exchange(g *gathering, n int, session, ufrag string) {
	g.wait(false)
	for try := 0; try < 3 && v.current(n); try++ {
		ctx, cancel := v.within(10 * time.Second)
		res, err := v.cfg.Candidates(ctx, &screenv1.CandidatesRequest{SessionId: session, Ufrag: ufrag, Candidates: g.candidates()})
		cancel()
		if err != nil {
			select {
			case <-v.done:
			case <-time.After(time.Second):
			}
			continue
		}
		v.mu.Lock()
		if v.offers != n {
			v.mu.Unlock()
			return
		}
		v.theirs = res.Candidates
		v.mu.Unlock()
		for _, c := range res.Candidates {
			v.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c})
		}
		return
	}
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
	v.mu.Lock()
	v.ssrc = uint32(t.SSRC())
	v.mu.Unlock()
	a := newAssembler()
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
				if a.wantKey(now) && now.Sub(asked) >= 400*time.Millisecond {
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
		for _, f := range a.push(p, time.Now()) {
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
		st.Nearby = sameAddress(publicAddresses(v.pc.LocalDescription(), nil), publicAddresses(v.pc.RemoteDescription(), theirs))
	}
}

// publicAddresses are the addresses an end is seen at from the relay's STUN
// server: in its description, and among the candidates that came later.
func publicAddresses(d *webrtc.SessionDescription, later []string) []string {
	lines := later
	if d != nil {
		lines = append(strings.Split(d.SDP, "\n"), later...)
	}
	var out []string
	for _, line := range lines {
		// [a=]candidate:<foundation> <component> <transport> <priority> <address> <port> typ <type> ...
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 8 && strings.HasPrefix(strings.TrimPrefix(f[0], "a="), "candidate:") && f[6] == "typ" && f[7] == "srflx" {
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
