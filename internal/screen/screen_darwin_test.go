package screen

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
)

// The host created a session, but the initial answer was lost in its tunnel.
// Opening again is automatic and the real helper's picture still arrives.
func TestSessionRecoversLostInitialAnswer(t *testing.T) {
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}})
	defer h.Stop()
	var frames atomic.Int32
	calls := 0
	v, err := Dial(context.Background(), ViewerConfig{
		Connect: func(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
			calls++
			res, err := h.Connect(ctx, req)
			if err == nil && calls == 1 {
				return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("the answer was lost"))
			}
			return res, err
		},
		Candidates: h.Candidates,
		Frame:      func(bool, []byte, *Trace) { frames.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	waitFor(t, "the picture after a lost initial answer", func() bool { return frames.Load() > 20 })
	if calls != 2 {
		t.Fatalf("initial offers=%d, want 2", calls)
	}
}

// helperBin is the helper to test with: $HERMESH_SCREEN_BIN, else the one
// built here from apps/screen/macos.
func helperBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("HERMESH_SCREEN_BIN"); bin != "" {
		return bin
	}
	dir, err := filepath.Abs("../../apps/screen/macos")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("swift", "build", "-c", "release", "--product", "hermesh-screen")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the helper: %v\n%s", err, out)
	}
	out, err := exec.Command("swift", "build", "-c", "release", "--show-bin-path", "--package-path", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "hermesh-screen")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A viewer connects to a host that runs the real helper with its test
// picture: frames arrive from a keyframe on, the machine says the picture's
// size, input reaches the helper, and leaving stops it.
func TestSession(t *testing.T) {
	dir := t.TempDir()
	log, clip := filepath.Join(dir, "input.jsonl"), filepath.Join(dir, "clipboard.txt")
	var mu sync.Mutex
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source", "--input-log", log, "--test-clipboard", clip}})
	var frames, keys int
	var first bool
	var format *screenv1.Format
	var state *screenv1.State
	var copied []string
	var phase string
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := Dial(ctx, ViewerConfig{
		Connect: h.Connect, Candidates: h.Candidates, Name: "test phone", Pointer: true,
		Frame: func(key bool, data []byte, _ *Trace) {
			mu.Lock()
			defer mu.Unlock()
			if frames == 0 {
				first = key
			}
			frames++
			if key {
				keys++
			}
		},
		Event: func(ev *screenv1.ScreenEvent) {
			mu.Lock()
			defer mu.Unlock()
			if f := ev.GetFormat(); f != nil {
				format = f
			}
			if s := ev.GetState(); s != nil {
				state = s
			}
			if c := ev.GetClipboard(); c != nil {
				copied = append(copied, c.Text)
			}
		},
		Status: func(s Status) { mu.Lock(); phase = s.Phase; mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	get := func(f func() bool) func() bool {
		return func() bool { mu.Lock(); defer mu.Unlock(); return f() }
	}
	waitFor(t, "frames", get(func() bool {
		if state != nil && state.Error == "encoder" {
			t.Skip("this Mac has no H.264 encoder")
		}
		return frames > 20
	}))
	mu.Lock()
	if !first {
		t.Error("the first frame shown is not a keyframe")
	}
	mu.Unlock()
	waitFor(t, "the picture's size", get(func() bool { return format != nil && format.Width == 640 && format.Height == 400 }))
	waitFor(t, "the state", get(func() bool { return state != nil && state.Error == "" && state.CanControl }))
	waitFor(t, "connected", get(func() bool { return phase == "connected" }))
	if got := h.Viewer(); got != "test phone" {
		t.Errorf("viewer: %q", got)
	}

	send := func(move bool, in *screenv1.Input) {
		t.Helper()
		b, _ := proto.Marshal(in)
		waitFor(t, "the input channel", func() bool { return v.Send(move, b) == nil })
	}
	send(false, &screenv1.Input{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.25, Y: 0.5, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Down: true, Clicks: 1}}})
	send(false, &screenv1.Input{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "你好 hi"}}})
	send(false, &screenv1.Input{Event: &screenv1.Input_Key{Key: &screenv1.Key{Character: "c", Command: true}}})
	send(true, &screenv1.Input{Event: &screenv1.Input_Move{Move: &screenv1.Move{X: 0.75, Y: 0.125}}})
	waitFor(t, "the input at the helper", func() bool {
		b, _ := os.ReadFile(log)
		s := string(b)
		return strings.Contains(s, `"button":"MOUSE_BUTTON_LEFT"`) && strings.Contains(s, `"down":true`) && strings.Contains(s, "你好 hi") &&
			strings.Contains(s, `"character":"c"`) && strings.Contains(s, `"command":true`) && strings.Contains(s, `"x":0.75`)
	})

	// The clipboard both ways: the viewer's text goes to the machine's
	// clipboard; what is copied there comes back, as long as it is new text,
	// and not too long.
	send(false, &screenv1.Input{Event: &screenv1.Input_Clipboard{Clipboard: &screenv1.Clipboard{Text: "from the viewer"}}})
	waitFor(t, "the viewer's clipboard", func() bool { b, _ := os.ReadFile(clip); return string(b) == "from the viewer" })
	time.Sleep(500 * time.Millisecond)
	for _, text := range []string{"", strings.Repeat("x", ClipboardMax+1), "copied 一", "", "copied 一", "copied 二"} {
		os.WriteFile(clip+".new", []byte(text), 0o600) // whole, never half written
		os.Rename(clip+".new", clip)
		time.Sleep(400 * time.Millisecond)
	}
	waitFor(t, "the machine's clipboard", get(func() bool { return len(copied) >= 2 }))
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	if strings.Join(copied, "|") != "copied 一|copied 二" {
		t.Errorf("the viewer got the clipboards %q", copied)
	}
	mu.Unlock()

	// A keyframe on request: the viewer lost its place.
	mu.Lock()
	before := keys
	mu.Unlock()
	waitFor(t, "another keyframe", func() bool {
		h.cur.keyframe() // asked again, as a viewer does: one that follows another too closely is passed over
		mu.Lock()
		defer mu.Unlock()
		return keys > before
	})

	// The viewer's network changed: it finds a path again on the session
	// it has, and the picture goes on.
	if err := v.Restart(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	mu.Lock()
	seen := frames
	mu.Unlock()
	waitFor(t, "frames after the restart", get(func() bool { return frames > seen+20 }))
	if got := h.Viewer(); got != "test phone" {
		t.Errorf("viewer after the restart: %q", got)
	}

	// A second viewer takes over: the first is told, and ends.
	var phase2 string
	v2, err := Dial(ctx, ViewerConfig{Connect: h.Connect, Name: "second", Status: func(s Status) { mu.Lock(); phase2 = s.Phase; mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the first viewer was not ended")
	}
	if v.Err() == nil {
		t.Error("the first viewer ended without a reason")
	}
	waitFor(t, "the second viewer", func() bool { return h.Viewer() == "second" })
	waitFor(t, "the second viewer connected", get(func() bool { return phase2 == "connected" }))
	s := h.cur
	v2.Close()
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the session did not end after its viewer left")
	}
	if err := s.cmd.ProcessState; err == nil || !err.Exited() {
		t.Error("the helper is still running")
	}
	waitFor(t, "no viewer", func() bool { return h.Viewer() == "" })
}

// A viewer that leaves before a path is found says nothing: its session
// ends by itself, and the helper with it.
func TestSessionEndsWhenViewerNeverConnects(t *testing.T) {
	old := connectWithin
	connectWithin = 500 * time.Millisecond
	defer func() { connectWithin = old }()
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}})
	// An offer from a viewer that then goes away.
	api, err := newAPI(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	pc.CreateDataChannel(inputChannel, nil)
	o, _ := pc.CreateOffer(nil)
	g, err := gather(pc, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.wait(false)
	sdp := pc.LocalDescription().SDP
	pc.Close()
	if _, err := h.Connect(context.Background(), &screenv1.ConnectRequest{Offer: sdp, Viewer: "gone"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to end", func() bool { return h.Viewer() == "" })
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Servers that do not answer (a network that blocks the relay's STUN and
// TURN) do not hold a session on one network up: each end's half of the
// handshake goes with its local addresses, and the rest follow. An older
// viewer, which sends all its candidates in its offer, and an older machine,
// which takes none after it, still connect.
func TestCandidatesLater(t *testing.T) {
	dead := []*relayv1.IceServer{{Urls: []string{"stun:192.0.2.1:3478", "turn:192.0.2.1:3478?transport=udp"}, Username: "u", Credential: "p"}}
	for _, c := range []struct {
		name          string
		viewer, older bool // the viewer sends candidates later; the machine is an older one
		within        time.Duration
	}{
		{"both", true, false, 1500 * time.Millisecond},
		{"an older viewer", false, false, 10 * time.Second},
		{"an older machine", true, true, 10 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var log bytes.Buffer
			h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}, Servers: func(context.Context) []*relayv1.IceServer { return dead },
				Log: slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return log.Write(p) }), nil))})
			defer h.Stop()
			var connected time.Time
			var exchanged int
			var offers int
			var start time.Time
			cfg := ViewerConfig{
				Name: "test phone", Servers: dead,
				Connect: func(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
					mu.Lock()
					offers++
					mu.Unlock()
					if c.older {
						t.Logf("offer at %v, restart=%v, candidates=%s", time.Since(start).Round(time.Millisecond), req.SessionId != "", kinds(strings.Split(req.Offer, "\n")))
					}
					if c.older { // it does not know the field
						req.CandidatesLater = false
					}
					res, err := h.Connect(ctx, req)
					if c.older && err == nil {
						t.Logf("answer at %v, candidates=%s", time.Since(start).Round(time.Millisecond), kinds(strings.Split(res.Answer, "\n")))
					}
					return res, err
				},
				Status: func(s Status) {
					mu.Lock()
					defer mu.Unlock()
					if s.Phase == "connected" && connected.IsZero() {
						connected = time.Now()
					}
				},
			}
			if c.viewer {
				cfg.Candidates = func(ctx context.Context, req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
					mu.Lock()
					exchanged++
					mu.Unlock()
					if c.older {
						return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
					}
					return h.Candidates(ctx, req)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start = time.Now()
			v, err := Dial(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			waitFor(t, "connected", func() bool { mu.Lock(); defer mu.Unlock(); return !connected.IsZero() })
			mu.Lock()
			took := connected.Sub(start)
			mu.Unlock()
			t.Logf("connected after %v", took.Round(time.Millisecond))
			if took > c.within {
				mu.Lock()
				t.Log(log.String())
				mu.Unlock()
				t.Errorf("connected after %v, want within %v", took, c.within)
			}
			if c.viewer && !c.older {
				waitFor(t, "the other candidates exchanged", func() bool { mu.Lock(); defer mu.Unlock(); return exchanged >= 1 })
			}
			// The machine's log says what each end found and the path taken
			// (docs/peer-connectivity-tests.md reads these lines).
			waitFor(t, "the session's lines in the log", func() bool {
				mu.Lock()
				defer mu.Unlock()
				l := log.String()
				return strings.Contains(l, `msg="screen: candidates"`) && strings.Contains(l, `local="host `) && strings.Contains(l, `remote="host `) &&
					strings.Contains(l, `msg="screen: path"`) && strings.Contains(l, " after=")
			})
			if c.older {
				time.Sleep(gatherWithin + time.Second)
				mu.Lock()
				if exchanged != 0 {
					t.Errorf("candidates were sent to a machine that takes none: %d times", exchanged)
				}
				if offers != 1 {
					t.Errorf("the older machine got %d offers with no new candidates, want one", offers)
				}
				mu.Unlock()
			}
		})
	}
}

// An old response can be in flight while the viewer restarts. It must not
// add its addresses to the new generation, or hold the restart's RPC up.
func TestLateCandidateResponseAfterRestart(t *testing.T) {
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}})
	defer h.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	asked, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var mu sync.Mutex
	calls, frames := 0, 0
	v, err := Dial(ctx, ViewerConfig{
		Connect: h.Connect,
		Candidates: func(ctx context.Context, req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
			res, err := h.Candidates(ctx, req)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			calls++
			first := calls == 1
			mu.Unlock()
			if first {
				close(asked)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				res.Candidates = append(res.Candidates, "candidate:stale 1 udp 2130706431 192.0.2.222 54321 typ host")
				close(returned)
			}
			return res, nil
		},
		Frame: func(bool, []byte, *Trace) { mu.Lock(); frames++; mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	select {
	case <-asked:
	case <-ctx.Done():
		t.Fatal("no candidate exchange")
	}
	waitFor(t, "the initial picture", func() bool { mu.Lock(); defer mu.Unlock(); return frames >= 10 })
	if err := v.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	<-returned
	mu.Lock()
	before := frames
	mu.Unlock()
	waitFor(t, "the picture after the old response", func() bool { mu.Lock(); defer mu.Unlock(); return frames >= before+20 })
	for _, stat := range v.pc.GetStats() {
		if c, ok := stat.(webrtc.ICECandidateStats); ok && c.IP == "192.0.2.222" {
			t.Fatal("the old response added a candidate to the new generation")
		}
	}
}

// A candidate exchange for an offer that another has replaced is refused:
// its candidates belong to paths that are gone.
func TestCandidatesOfAnEarlierOfferAreRefused(t *testing.T) {
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}})
	defer h.Stop()
	var mu sync.Mutex
	var first *screenv1.CandidatesRequest
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := Dial(ctx, ViewerConfig{
		Name: "test phone", Connect: h.Connect,
		Candidates: func(ctx context.Context, req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
			mu.Lock()
			if first == nil {
				first = req
			}
			mu.Unlock()
			return h.Candidates(ctx, req)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	waitFor(t, "the first exchange", func() bool { mu.Lock(); defer mu.Unlock(); return first != nil })
	if err := v.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Candidates(ctx, first); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("the earlier offer's candidates: %v, want them refused", err)
	}
	if _, err := h.Candidates(ctx, &screenv1.CandidatesRequest{SessionId: "none"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("candidates for no session: %v", err)
	}
	waitFor(t, "connected after the restart", v.Connected)
}

// A TURN address that comes after the first exchange of candidates (the
// server is slow, past gatherWithin) still reaches the other end: the viewer calls again as either end finds more, until both have
// found all of theirs, and then stops. A machine that answers once gets a
// second call when the viewer has more to give, and no others.
func TestLateTURNAddressIsExchanged(t *testing.T) {
	old := gatherWithin
	gatherWithin = 400 * time.Millisecond
	defer func() { gatherWithin = old }()
	// Each end's own server, slow over an allocation, which is asked for
	// twice: refused, then with the credentials.
	slow := func() []*relayv1.IceServer {
		server, _ := slowTURN(t, turnAllocate, 400*time.Millisecond, net.ParseIP("127.0.0.1"))
		return []*relayv1.IceServer{{Urls: []string{"turn:" + server + "?transport=udp"}, Username: "u", Credential: "p"}}
	}
	for _, older := range []bool{false, true} {
		name := "both"
		if older {
			name = "a machine that answers once"
		}
		t.Run(name, func(t *testing.T) {
			servers, theirs := slow(), slow()
			h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}, Servers: func(context.Context) []*relayv1.IceServer { return theirs }})
			defer h.Stop()
			var mu sync.Mutex
			var calls int
			var firstHad, sent, got, done bool
			relayed := func(list []string) bool { return strings.Contains(strings.Join(list, "\n"), " typ relay") }
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			v, err := Dial(ctx, ViewerConfig{
				Name: "test phone", Servers: servers, Connect: h.Connect,
				Candidates: func(ctx context.Context, req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
					res, err := h.Candidates(ctx, req)
					if err != nil {
						return nil, err
					}
					if older {
						res.Again, res.Complete = false, false
					}
					mu.Lock()
					defer mu.Unlock()
					calls++
					if calls == 1 {
						firstHad = relayed(req.Candidates) || relayed(res.Candidates)
					}
					sent, got = sent || relayed(req.Candidates), got || relayed(res.Candidates)
					done = done || req.Complete && res.Complete
					return res, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			get := func(f func() bool) func() bool {
				return func() bool { mu.Lock(); defer mu.Unlock(); return f() }
			}
			waitFor(t, "the viewer's TURN address to reach the machine", get(func() bool { return sent }))
			if !older {
				waitFor(t, "the machine's TURN address to reach the viewer", get(func() bool { return got }))
				waitFor(t, "both ends to say they found everything", get(func() bool { return done }))
			}
			time.Sleep(2500 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			t.Logf("%d calls", calls)
			if firstHad {
				t.Error("the first exchange had a TURN address already: the server was not slow enough to test this")
			}
			if older && calls != 2 {
				t.Errorf("%d calls to a machine that answers once, want the first and one with the viewer's TURN address", calls)
			}
			if !older && calls > 8 {
				t.Errorf("%d calls, want a few", calls)
			}
		})
	}
}

// A session that looks for a path again takes the relay's servers as they
// are then, on both ends: the credentials it began with last an hour. Here
// it begins with no server at all, and has TURN addresses after a restart.
func TestRestartTakesTheServersOfTheMoment(t *testing.T) {
	server, _ := slowTURN(t, turnAllocate, 0, net.ParseIP("127.0.0.1"))
	servers := []*relayv1.IceServer{{Urls: []string{"turn:" + server + "?transport=udp"}, Username: "u", Credential: "p"}}
	var mu sync.Mutex
	var asked int // the machine's: none for the first offer
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source"}, Servers: func(context.Context) []*relayv1.IceServer {
		mu.Lock()
		defer mu.Unlock()
		asked++
		if asked == 1 {
			return nil
		}
		return servers
	}})
	defer h.Stop()
	var sent, got bool
	relayed := func(list []string) bool { return strings.Contains(strings.Join(list, "\n"), " typ relay") }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := Dial(ctx, ViewerConfig{
		Name: "test phone", Connect: h.Connect, Fresh: func(context.Context) []*relayv1.IceServer { return servers },
		Candidates: func(ctx context.Context, req *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error) {
			res, err := h.Candidates(ctx, req)
			if err == nil {
				mu.Lock()
				sent, got = sent || relayed(req.Candidates), got || relayed(res.Candidates)
				mu.Unlock()
			}
			return res, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	waitFor(t, "connected", v.Connected)
	time.Sleep(time.Second)
	mu.Lock()
	if sent || got {
		t.Errorf("a TURN address before any server was given: the viewer's %v, the machine's %v", sent, got)
	}
	mu.Unlock()
	if err := v.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both ends' TURN addresses after the restart", func() bool { mu.Lock(); defer mu.Unlock(); return sent && got })
	waitFor(t, "connected after the restart", v.Connected)
}
