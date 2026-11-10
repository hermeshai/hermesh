package screen

import (
	"context"
	"errors"
	"os"
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
	// Name is this device's, for the notice on the machine.
	Name string
	// Pointer: whether the picture should have the pointer in it.
	Pointer bool
	// Servers are the relay's STUN and TURN servers, when it has any.
	Servers []*relayv1.IceServer
	// Frame gets each frame, whole and in order, as Annex B.
	Frame func(key bool, data []byte)
	// Event gets what the machine reports: the picture's size, its state.
	Event func(*screenv1.ScreenEvent)
	// Status gets the connection's state as it changes, and every few seconds.
	Status func(Status)
}

// Status is the connection as the viewer sees it.
type Status struct {
	// Phase: "connecting", "connected", "reconnecting".
	Phase string `json:"phase"`
	// Path: "direct", or "relay" when the packets go through a TURN server.
	Path string `json:"path,omitempty"`
	// RTT is the round trip to the machine, in milliseconds.
	RTT int `json:"rtt_ms,omitempty"`
}

// Viewer is a session with a machine, seen from the device.
type Viewer struct {
	cfg     ViewerConfig
	pc      *webrtc.PeerConnection
	input   *webrtc.DataChannel
	pointer *webrtc.DataChannel
	session string

	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	err  error
	ssrc uint32 // the picture's track, once it came
}

// Dial opens a session: it returns once the machine has answered, and the
// picture follows when a path between the two is found (Status).
func Dial(ctx context.Context, cfg ViewerConfig) (*Viewer, error) {
	api, err := newAPI(nil)
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
		if st := ev.GetState(); st != nil && st.Error == "ended" {
			go v.close(errors.New("the machine ended the session"))
		}
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go v.receive(t) })
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
// that restarts ICE on the session under way.
func (v *Viewer) offer(ctx context.Context, restart bool) error {
	o, err := v.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	if err != nil {
		return err
	}
	sdp, err := gathered(v.pc, o)
	if err != nil {
		return err
	}
	res, err := v.cfg.Connect(ctx, &screenv1.ConnectRequest{Offer: sdp, SessionId: v.session, Viewer: v.cfg.Name, Pointer: v.cfg.Pointer})
	if err != nil {
		return err
	}
	v.session = res.SessionId
	return v.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: res.Answer})
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
				v.cfg.Frame(f.key, f.data)
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
		st.Path, st.RTT = v.path()
	}
	v.cfg.Status(st)
}

// path is how the packets travel now, and the round trip that way.
func (v *Viewer) path() (string, int) {
	stats := v.pc.GetStats()
	for _, s := range stats {
		pair, ok := s.(webrtc.ICECandidatePairStats)
		if !ok || !pair.Nominated || pair.State != webrtc.StatsICECandidatePairStateSucceeded {
			continue
		}
		path := "direct"
		for _, id := range []string{pair.LocalCandidateID, pair.RemoteCandidateID} {
			if c, ok := stats[id].(webrtc.ICECandidateStats); ok && c.CandidateType == webrtc.ICECandidateTypeRelay {
				path = "relay"
			}
		}
		return path, int(pair.CurrentRoundTripTime * 1000)
	}
	return "direct", 0
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
