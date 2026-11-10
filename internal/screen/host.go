package screen

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
)

// HostConfig is what a machine's side needs.
type HostConfig struct {
	// Bin is the helper that captures, encodes and posts input
	// (apps/screen/macos); "" on a machine that has none.
	Bin string
	// Args are passed to the helper (the tests' picture and input log).
	Args []string
	Log  *slog.Logger
	// Servers asks the relay for its STUN and TURN servers, as a session
	// starts; nil, or none returned, leaves the paths the two ends find by
	// themselves.
	Servers func(context.Context) []*relayv1.IceServer
}

// Host serves this machine's screen to one viewer at a time.
type Host struct {
	cfg HostConfig

	mu  sync.Mutex
	cur *hostSession
}

func NewHost(cfg HostConfig) *Host {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Host{cfg: cfg}
}

// Available says whether this machine can show its screen.
func (h *Host) Available() bool { return h.cfg.Bin != "" }

// Viewer is the device in control now, as it named itself; "" when none is.
func (h *Host) Viewer() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cur == nil {
		return ""
	}
	return h.cur.viewer
}

// Stop ends the session under way, if any.
func (h *Host) Stop() {
	h.mu.Lock()
	s := h.cur
	h.mu.Unlock()
	if s != nil {
		s.end("ended")
	}
}

// Connect answers a viewer's offer: a new session, which ends the one
// before, or a live session's move to another network path.
func (h *Host) Connect(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
	if !h.Available() {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this machine can't show its screen"))
	}
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.Offer}
	if req.SessionId != "" {
		h.mu.Lock()
		s := h.cur
		h.mu.Unlock()
		if s == nil || s.id != req.SessionId {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("the session has ended"))
		}
		answer, err := s.answer(offer)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return &screenv1.ConnectResponse{Answer: answer, SessionId: s.id}, nil
	}
	h.Stop()
	s, err := h.open(ctx, req)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	h.mu.Lock()
	prev := h.cur
	h.cur = s
	h.mu.Unlock()
	if prev != nil { // two viewers at once: the later one stays
		prev.end("ended")
	}
	answer, err := s.answer(offer)
	if err != nil {
		s.end("")
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &screenv1.ConnectResponse{Answer: answer, SessionId: s.id}, nil
}

// connectWithin: how long a viewer has to find a path to this machine
// before its session ends (a variable for the tests).
var connectWithin = 20 * time.Second

// hostSession is one viewer's session: its connection and its helper.
type hostSession struct {
	h      *Host
	id     string
	viewer string
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticSample
	cmd    *exec.Cmd
	done   chan struct{} // closed when the session ended

	wmu   sync.Mutex // writes to the helper
	stdin io.WriteCloser

	mu       sync.Mutex
	input    *webrtc.DataChannel
	format   *screenv1.ScreenEvent // the latest of each, for a channel that opens later
	state    *screenv1.ScreenEvent
	last     time.Time // the frame before
	bitrate  int       // as last told to the helper
	bitrateT time.Time
	keyT     time.Time
	ended    bool
}

func (h *Host) open(ctx context.Context, req *screenv1.ConnectRequest) (*hostSession, error) {
	id := make([]byte, 8)
	rand.Read(id)
	s := &hostSession{h: h, id: hex.EncodeToString(id), viewer: req.Viewer, done: make(chan struct{}), bitrate: startBitrate}
	api, err := newAPI(func(e cc.BandwidthEstimator) { e.OnTargetBitrateChange(s.setBitrate) })
	if err != nil {
		return nil, err
	}
	var cfg webrtc.Configuration
	if h.cfg.Servers != nil {
		cfg.ICEServers = iceServers(h.cfg.Servers(ctx))
	}
	if s.pc, err = api.NewPeerConnection(cfg); err != nil {
		return nil, err
	}
	if s.track, err = webrtc.NewTrackLocalStaticSample(h264, "video", "screen"); err != nil {
		s.pc.Close()
		return nil, err
	}
	sender, err := s.pc.AddTrack(s.track)
	if err != nil {
		s.pc.Close()
		return nil, err
	}
	go s.readRTCP(sender)
	s.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case inputChannel:
			dc.OnOpen(func() {
				s.mu.Lock()
				s.input = dc
				format, state := s.format, s.state
				s.mu.Unlock()
				s.sendEvent(format)
				s.sendEvent(state)
			})
			dc.OnMessage(func(m webrtc.DataChannelMessage) { s.onInput(m.Data) })
		case pointerChannel:
			dc.OnMessage(func(m webrtc.DataChannelMessage) { s.onInput(m.Data) })
		}
	})
	connected := make(chan struct{})
	var once sync.Once
	s.pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { close(connected) })
			s.keyframe()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go s.end("")
		}
	})
	// A viewer that left before a path was found sends no goodbye.
	go func() {
		select {
		case <-connected:
		case <-s.done:
		case <-time.After(connectWithin):
			s.end("")
		}
	}()

	s.cmd = exec.Command(h.cfg.Bin, h.cfg.Args...)
	if s.stdin, err = s.cmd.StdinPipe(); err != nil {
		s.pc.Close()
		return nil, err
	}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		s.pc.Close()
		return nil, err
	}
	if err := s.cmd.Start(); err != nil {
		s.pc.Close()
		return nil, err
	}
	go s.readHelper(stdout)
	s.toHelper(KindStart, map[string]any{"max_edge": 1920, "fps": 30, "bitrate": startBitrate, "pointer": req.Pointer})
	return s, nil
}

// answer takes the viewer's offer, the first or one that restarts ICE.
func (s *hostSession) answer(offer webrtc.SessionDescription) (string, error) {
	if err := s.pc.SetRemoteDescription(offer); err != nil {
		return "", err
	}
	a, err := s.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	return gathered(s.pc, a)
}

func (s *hostSession) toHelper(k Kind, v any) {
	var b []byte
	if v != nil {
		b, _ = json.Marshal(v)
	}
	s.rawToHelper(k, b)
}

func (s *hostSession) rawToHelper(k Kind, b []byte) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	WriteMessage(s.stdin, k, b)
}

// keyframe asks the helper for one, at most a few times a second.
func (s *hostSession) keyframe() {
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.keyT) < 200*time.Millisecond {
		s.mu.Unlock()
		return
	}
	s.keyT = now
	s.mu.Unlock()
	s.toHelper(KindKeyframe, nil)
}

// setBitrate follows the network's estimate, leaving it some room, and
// tells the helper when the target moved enough to matter.
func (s *hostSession) setBitrate(estimate int) {
	bps := min(max(estimate*85/100, minBitrate), maxBitrate)
	s.mu.Lock()
	now := time.Now()
	moved := bps > s.bitrate*11/10 || bps < s.bitrate*9/10
	if !moved || now.Sub(s.bitrateT) < 500*time.Millisecond {
		s.mu.Unlock()
		return
	}
	s.bitrate, s.bitrateT = bps, now
	s.mu.Unlock()
	s.toHelper(KindBitrate, map[string]any{"bps": bps})
}

// readRTCP hears the viewer ask for a keyframe.
func (s *hostSession) readRTCP(sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.keyframe()
			}
		}
	}
}

var helperJSON = protojson.MarshalOptions{UseProtoNames: true}

// onInput passes the viewer's input to the helper.
func (s *hostSession) onInput(b []byte) {
	var in screenv1.Input
	if proto.Unmarshal(b, &in) != nil || in.Event == nil {
		return
	}
	if p := in.GetShowPointer(); p != nil {
		s.toHelper(KindPointer, map[string]any{"shown": p.Shown})
		return
	}
	if j, err := helperJSON.Marshal(&in); err == nil {
		s.rawToHelper(KindInput, j)
	}
}

func (s *hostSession) sendEvent(ev *screenv1.ScreenEvent) {
	if ev == nil {
		return
	}
	s.mu.Lock()
	dc := s.input
	s.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	if b, err := proto.Marshal(ev); err == nil {
		dc.Send(b)
	}
}

// readHelper sends the helper's frames and passes on what it reports,
// until it exits.
func (s *hostSession) readHelper(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 256<<10)
	for {
		k, p, err := ReadMessage(r)
		if err != nil {
			break
		}
		switch k {
		case KindVideo:
			if len(p) <= VideoHeader {
				continue
			}
			now := time.Now()
			s.mu.Lock()
			d := now.Sub(s.last)
			s.last = now
			s.mu.Unlock()
			// Only that timestamps differ matters: the viewer shows a frame when it has it.
			s.track.WriteSample(media.Sample{Data: p[VideoHeader:], Duration: min(max(d, time.Millisecond), time.Second)})
		case KindFormat:
			var f struct{ Width, Height uint32 }
			if json.Unmarshal(p, &f) == nil {
				ev := &screenv1.ScreenEvent{Event: &screenv1.ScreenEvent_Format{Format: &screenv1.Format{Width: f.Width, Height: f.Height}}}
				s.mu.Lock()
				s.format = ev
				s.mu.Unlock()
				s.sendEvent(ev)
			}
		case KindState:
			var st struct {
				Error, Detail string
				CanControl    bool `json:"can_control"`
			}
			if json.Unmarshal(p, &st) == nil {
				ev := &screenv1.ScreenEvent{Event: &screenv1.ScreenEvent_State{State: &screenv1.State{Error: st.Error, Detail: st.Detail, CanControl: st.CanControl}}}
				s.mu.Lock()
				s.state = ev
				s.mu.Unlock()
				s.sendEvent(ev)
			}
		case KindLog:
			s.h.cfg.Log.Info("screen helper", "msg", string(p))
		}
	}
	s.end("capture")
}

// end closes the session: the viewer is told why (when there is a reason
// to give), and the helper lets go of what it holds and exits.
func (s *hostSession) end(reason string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.mu.Unlock()
	if reason != "" {
		s.sendEvent(&screenv1.ScreenEvent{Event: &screenv1.ScreenEvent_State{State: &screenv1.State{Error: reason}}})
		time.Sleep(50 * time.Millisecond) // let it leave before the connection closes
	}
	s.pc.Close()
	s.wmu.Lock()
	s.stdin.Close() // the helper releases the buttons it holds and exits
	s.wmu.Unlock()
	exited := make(chan struct{})
	go func() { s.cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		s.cmd.Process.Kill()
		<-exited
	}
	close(s.done)
	s.h.mu.Lock()
	if s.h.cur == s {
		s.h.cur = nil
	}
	s.h.mu.Unlock()
}
