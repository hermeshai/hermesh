package screen

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
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
	// (apps/screen/macos, apps/screen/linux); "" on a machine that has none.
	Bin string
	// Off is a file whose presence turns Computer Control off on this
	// machine (`hermeshd screen off`); "" for none.
	Off string
	// Args are passed to the helper (the tests' picture and input log).
	Args []string
	Log  *slog.Logger
	// Servers asks the relay for its STUN and TURN servers, as a session
	// starts; nil, or none returned, leaves the paths the two ends find by
	// themselves.
	Servers func(context.Context) []*relayv1.IceServer
	// NoRelay is called when those servers gave a session no relayed
	// address: the next ones asked for should be looked at again.
	NoRelay func()
}

// Host serves this machine's screen to one viewer at a time.
type Host struct {
	cfg HostConfig
	// trace: frames carry the times they passed each stage (trace.go).
	trace bool

	mu  sync.Mutex
	cur *hostSession

	// What the helper last said of this desktop (Linux, where a desktop
	// comes and goes with a login).
	pmu     sync.Mutex
	probed  time.Time
	probing bool
	desktop Desktop
}

// Desktop is what the helper says of this machine's desktop: the way it
// would reach it, or why it can't.
type Desktop struct {
	Available bool   `json:"available"`
	Way       string `json:"way"`
	Error     string `json:"error"`
	Detail    string `json:"detail"`
}

func NewHost(cfg HostConfig) *Host {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Host{cfg: cfg, trace: os.Getenv("HERMESH_SCREEN_TRACE") != ""}
}

// Available says whether this machine can show its screen.
func (h *Host) Available() bool {
	if h.cfg.Bin == "" || h.isOff() {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	return h.Desktop().Available
}

func (h *Host) isOff() bool {
	if h.cfg.Off == "" {
		return false
	}
	_, err := os.Stat(h.cfg.Off)
	return err == nil
}

// ErrLocked is why a locked desktop shows nothing: the viewer is told so.
const ErrLocked = "the screen is locked"

// probeEvery is how long the helper's word on the desktop is kept.
const probeEvery = 30 * time.Second

// Desktop is what the helper said of this machine's desktop. On Linux it
// asks the helper (--status), keeps the answer for a while and asks again
// off to the side, the first time while the caller waits; while a session
// lasts the answer stands. Elsewhere the helper always can.
func (h *Host) Desktop() Desktop {
	if h.cfg.Bin == "" {
		return Desktop{Error: "no_helper"}
	}
	if slices.Contains(h.cfg.Args, "--test-source") {
		return Desktop{Available: true, Way: "test"}
	}
	if runtime.GOOS != "linux" {
		return Desktop{Available: true, Way: runtime.GOOS}
	}
	h.pmu.Lock()
	first := h.probed.IsZero()
	stale := time.Since(h.probed) > probeEvery && !h.probing && h.Viewer() == ""
	if stale && !first {
		h.probing = true
		go h.probe()
	}
	h.pmu.Unlock()
	if first {
		h.probe()
	}
	h.pmu.Lock()
	defer h.pmu.Unlock()
	return h.desktop
}

func (h *Host) probe() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var d Desktop
	out, err := exec.CommandContext(ctx, h.cfg.Bin, "--status").Output()
	if err != nil || json.Unmarshal(out, &d) != nil {
		d = Desktop{Error: "helper", Detail: fmt.Sprint(err)}
	}
	h.pmu.Lock()
	h.desktop, h.probed, h.probing = d, time.Now(), false
	h.pmu.Unlock()
}

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
		h.cfg.Log.Info("screen: the viewer looks for a path again", "session", s.id, "after", time.Since(s.opened).Round(time.Millisecond))
		// The relay's servers as they are now: the credentials the session
		// began with last an hour.
		if h.cfg.Servers != nil {
			if list := h.cfg.Servers(ctx); len(list) > 0 {
				conf := s.pc.GetConfiguration()
				conf.ICEServers = iceServers(list)
				s.pc.SetConfiguration(conf)
			}
		}
		answer, err := s.answer(offer, req.CandidatesLater)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return &screenv1.ConnectResponse{Answer: answer, SessionId: s.id, CandidatesLater: req.CandidatesLater}, nil
	}
	if h.Desktop().Error == "locked" { // GNOME shares no locked screen (docs/computer-control-linux.md §3.7)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(ErrLocked))
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
	answer, err := s.answer(offer, req.CandidatesLater)
	if err != nil {
		s.end("")
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &screenv1.ConnectResponse{Answer: answer, SessionId: s.id, CandidatesLater: req.CandidatesLater}, nil
}

// Candidates takes the candidates the viewer found after its offer, and
// answers with this machine's once they are found.
func (h *Host) Candidates(ctx context.Context, req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
	h.mu.Lock()
	s := h.cur
	h.mu.Unlock()
	if s == nil || s.id != req.SessionId {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the session has ended"))
	}
	return s.candidates(req)
}

// connectWithin: how long a viewer has to find a path to this machine
// before its session ends (a variable for the tests).
var connectWithin = 20 * time.Second

// hostSession is one viewer's session: its connection and its helper.
type hostSession struct {
	h      *Host
	id     string
	viewer string
	opened time.Time
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticSample
	cmd    *exec.Cmd
	done   chan struct{} // closed when the session ended

	cmu    sync.Mutex // closing the connection, or reading its statistics
	amu    sync.Mutex // one answer at a time
	gath   *gathering // the latest answer's
	logged int        // how many candidates the log's latest line counted

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
	s := &hostSession{h: h, id: hex.EncodeToString(id), viewer: req.Viewer, opened: time.Now(), done: make(chan struct{}), bitrate: startBitrate}
	api, err := newAPI(func(e cc.BandwidthEstimator) { e.OnTargetBitrateChange(s.setBitrate) }, nil)
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
				h.cfg.Log.Info("screen: input ready", "session", s.id, "after", time.Since(s.opened).Round(time.Millisecond))
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
	// What the session goes through, as it changes: for telling why one is
	// slow (the viewer picks the path, this end follows).
	s.pc.SCTP().Transport().ICETransport().OnSelectedCandidatePairChange(func(p *webrtc.ICECandidatePair) {
		h.cfg.Log.Info("screen: path", "session", s.id, "local", pathEnd(p.Local), "remote", pathEnd(p.Remote), "after", time.Since(s.opened).Round(time.Millisecond))
	})
	connected := make(chan struct{})
	var once sync.Once
	s.pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		h.cfg.Log.Info("screen: connection", "session", s.id, "state", st.String(), "after", time.Since(s.opened).Round(time.Millisecond))
		switch st {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { close(connected) })
			s.keyframe()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go s.end("")
		}
	})
	// A session slow to connect says how its checks went. Not once it is
	// connected: pion closes a connection by itself when the other end
	// leaves, and its statistics are not safe to read meanwhile.
	go func() {
		select {
		case <-s.done:
		case <-connected:
		case <-time.After(4 * time.Second):
			s.logConnectivity()
		}
	}()
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
	// Up to 120 frames a second, which a 120 Hz screen gives and a 60 Hz one halves by itself: the
	// picture on the phone is then 4 ms younger on average than at 60 (docs/computer-control.md §5).
	s.toHelper(KindStart, map[string]any{"max_edge": 1920, "fps": 120, "bitrate": startBitrate, "pointer": req.Pointer})
	return s, nil
}

// answer takes the viewer's offer, the first or one that restarts ICE. With
// later it answers as soon as this machine's local addresses are found; the
// rest go in candidates.
func (s *hostSession) answer(offer webrtc.SessionDescription, later bool) (string, error) {
	s.amu.Lock()
	defer s.amu.Unlock()
	if err := s.pc.SetRemoteDescription(offer); err != nil {
		return "", err
	}
	a, err := s.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	g, err := gather(s.pc, a, s.h.cfg.NoRelay)
	if err != nil {
		return "", err
	}
	s.gath = g
	g.wait(later)
	sdp := s.pc.LocalDescription().SDP
	s.logCandidates(strings.Split(sdp, "\n"), strings.Split(offer.SDP, "\n"))
	return sdp, nil
}

// candidates adds the viewer's candidates for the offer answered last, and
// returns this machine's once its gathering has ended.
func (s *hostSession) candidates(req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
	s.amu.Lock()
	g, offer := s.gath, s.pc.RemoteDescription()
	if g == nil || offer == nil || iceUfrag(offer.SDP) != req.Ufrag {
		s.amu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("another offer came since"))
	}
	for _, c := range req.Candidates {
		if err := s.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c}); err != nil {
			s.amu.Unlock()
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	s.amu.Unlock()
	g.wait(false)
	// A viewer that calls again has these already: wait a moment for more.
	if req.Have > 0 {
		g.grown(int(req.Have), time.Second)
	}
	mine, all := g.found()
	s.mu.Lock()
	n := len(mine) + len(req.Candidates)
	said := n == s.logged
	s.logged = n
	s.mu.Unlock()
	if !said {
		s.logCandidates(mine, req.Candidates)
	}
	return &screenv1.CandidatesResponse{Candidates: mine, Complete: all, Again: true}, nil
}

func (s *hostSession) logCandidates(local, remote []string) {
	s.h.cfg.Log.Info("screen: candidates", "session", s.id, "local", kinds(local), "remote", kinds(remote),
		"local_addresses", candidateAddresses(local), "remote_addresses", candidateAddresses(remote))
}

// logConnectivity distinguishes unreachable local paths from media that
// never leaves. It runs outside ICE callbacks and never logs credentials.
func (s *hostSession) logConnectivity() {
	s.cmu.Lock()
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	if ended {
		s.cmu.Unlock()
		return
	}
	stats := s.pc.GetStats()
	s.cmu.Unlock()
	networks := map[string]*net.IPNet{}
	interfaces := map[string]string{}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
				networks[n.IP.String()], interfaces[n.IP.String()] = n, iface.Name
			}
		}
	}
	var packets, bytes uint64
	hasRTPStats := false
	for _, stat := range stats {
		switch v := stat.(type) {
		case webrtc.OutboundRTPStreamStats:
			hasRTPStats = true
			packets += uint64(v.PacketsSent)
			bytes += v.BytesSent
		case webrtc.ICECandidatePairStats:
			local, lok := stats[v.LocalCandidateID].(webrtc.ICECandidateStats)
			remote, rok := stats[v.RemoteCandidateID].(webrtc.ICECandidateStats)
			if !lok || !rok || local.CandidateType != webrtc.ICECandidateTypeHost || remote.CandidateType != webrtc.ICECandidateTypeHost || local.IP == remote.IP {
				continue
			}
			n := networks[local.IP]
			if n == nil || !n.Contains(net.ParseIP(remote.IP)) {
				continue
			}
			s.h.cfg.Log.Info("screen: nearby check", "session", s.id, "interface", interfaces[local.IP], "local", local.IP, "remote", remote.IP,
				"state", v.State, "requests_sent", v.RequestsSent, "responses_received", v.ResponsesReceived, "requests_received", v.RequestsReceived)
		}
	}
	relayProtocol := ""
	if pair, ok := s.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePairStats(); ok {
		if local, ok := stats[pair.LocalCandidateID].(webrtc.ICECandidateStats); ok {
			relayProtocol = local.RelayProtocol
		}
	}
	fields := []any{"session", s.id, "state", s.pc.ConnectionState().String(), "relay_protocol", relayProtocol,
		"rtp_stats_available", hasRTPStats, "after", time.Since(s.opened).Round(time.Millisecond)}
	if hasRTPStats {
		fields = append(fields, "rtp_packets", packets, "rtp_bytes", bytes)
	}
	s.h.cfg.Log.Info("screen: transport", fields...)
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
			if len(p) <= helperVideoHeader {
				continue
			}
			now := time.Now()
			s.mu.Lock()
			first := s.last.IsZero()
			d := now.Sub(s.last)
			s.last = now
			s.mu.Unlock()
			data := p[helperVideoHeader:]
			if first {
				s.h.cfg.Log.Info("screen: first encoded frame", "session", s.id, "bytes", len(data), "after", time.Since(s.opened).Round(time.Millisecond))
			}
			if s.h.trace {
				t := helperStamps(p)
				t.Sent = now.UnixMicro()
				data = withTrace(data, t)
			}
			// Only that timestamps differ matters: the viewer shows a frame when it has it.
			s.track.WriteSample(media.Sample{Data: data, Duration: min(max(d, time.Millisecond), time.Second)})
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
				s.h.cfg.Log.Info("screen: capture state", "session", s.id, "error", st.Error, "can_control", st.CanControl)
				ev := &screenv1.ScreenEvent{Event: &screenv1.ScreenEvent_State{State: &screenv1.State{Error: st.Error, Detail: st.Detail, CanControl: st.CanControl}}}
				s.mu.Lock()
				s.state = ev
				s.mu.Unlock()
				s.sendEvent(ev)
			}
		case KindClipboard:
			var c struct{ Text string }
			if json.Unmarshal(p, &c) == nil && c.Text != "" && len(c.Text) <= ClipboardMax {
				s.sendEvent(&screenv1.ScreenEvent{Event: &screenv1.ScreenEvent_Clipboard{Clipboard: &screenv1.Clipboard{Text: c.Text}}})
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
	s.cmu.Lock()
	s.pc.Close()
	s.cmu.Unlock()
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
	// The desktop may have changed with the session's end (locked, say): asked again before the next.
	s.h.pmu.Lock()
	s.h.probed = time.Time{}
	s.h.pmu.Unlock()
}
