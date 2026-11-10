package screen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
)

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
	log := filepath.Join(t.TempDir(), "input.jsonl")
	var mu sync.Mutex
	h := NewHost(HostConfig{Bin: helperBin(t), Args: []string{"--test-source", "--input-log", log}})
	var frames, keys int
	var first bool
	var format *screenv1.Format
	var state *screenv1.State
	var phase string
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := Dial(ctx, ViewerConfig{
		Connect: h.Connect, Name: "test phone", Pointer: true,
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
	api, err := newAPI(nil)
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
	sdp, err := gathered(pc, o)
	if err != nil {
		t.Fatal(err)
	}
	pc.Close()
	if _, err := h.Connect(context.Background(), &screenv1.ConnectRequest{Offer: sdp, Viewer: "gone"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to end", func() bool { return h.Viewer() == "" })
}
