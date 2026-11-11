//go:build unix

package e2e

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	screenrtc "github.com/hermeshai/hermesh/internal/screen"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// screenStream is the app's side of Computer Control: the connection it
// opens to its core for a machine's screen.
type screenStream struct {
	conn net.Conn
	r    *bufio.Reader
}

func openScreen(t *testing.T, m *mobileCore, machineID string) *screenStream {
	t.Helper()
	conn, err := net.Dial("tcp", "127.0.0.1:"+m.port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "GET /screen/%s?name=test-iphone&pointer=1 HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: hermesh-screen\r\n\r\n", machineID, m.token)
	r := bufio.NewReader(conn)
	res, err := http.ReadResponse(r, nil)
	if err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("opening the screen: %v %v", res, err)
	}
	return &screenStream{conn: conn, r: r}
}

// live reads the stream until the picture has come (frames from a keyframe
// on, the Mac's report of its size) and the two ends are connected, and
// returns the path between them: "direct" or "relay".
func (s *screenStream) live(t *testing.T) string {
	t.Helper()
	var frames int
	var firstKey bool
	var format *screenv1.Format
	path := ""
	deadline := time.Now().Add(60 * time.Second)
	for (frames < 20 || format == nil || path == "") && time.Now().Before(deadline) {
		s.conn.SetReadDeadline(deadline)
		k, p, err := screenrtc.ReadMessage(s.r)
		if err != nil {
			t.Fatalf("reading the screen: %v", err)
		}
		switch k {
		case screenrtc.KindVideo:
			if frames == 0 {
				firstKey = p[0] == 1
			}
			frames++
		case screenrtc.KindState:
			var ev screenv1.ScreenEvent
			if err := proto.Unmarshal(p, &ev); err != nil {
				t.Fatal(err)
			}
			if f := ev.GetFormat(); f != nil {
				format = f
			}
			if st := ev.GetState(); st.GetError() == "encoder" {
				t.Skip("this Mac has no H.264 encoder")
			}
		case screenrtc.KindStatus:
			var status struct{ Phase, Path, Error string }
			json.Unmarshal(p, &status)
			if status.Phase == "failed" {
				t.Fatalf("the session failed: %s", status.Error)
			}
			if status.Phase == "connected" {
				path = status.Path
			}
		}
	}
	if frames < 20 || !firstKey || path == "" || format.GetWidth() != 640 || format.GetHeight() != 400 {
		t.Fatalf("frames %d, first is a keyframe %v, path %q, format %v", frames, firstKey, path, format)
	}
	return path
}

// ended reads the stream until the session has ended, and returns why.
func (s *screenStream) ended(t *testing.T) string {
	t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		k, p, err := screenrtc.ReadMessage(s.r)
		if err != nil {
			t.Fatalf("the stream closed with no reason given: %v", err)
		}
		var status struct{ Phase, Error string }
		if k == screenrtc.KindStatus && json.Unmarshal(p, &status) == nil && status.Phase == "failed" {
			return status.Error
		}
	}
}

// clipboard reads the stream until the machine sends what was copied there,
// and returns it.
func (s *screenStream) clipboard(t *testing.T) string {
	t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		k, p, err := screenrtc.ReadMessage(s.r)
		if err != nil {
			t.Fatalf("waiting for the machine's clipboard: %v", err)
		}
		var ev screenv1.ScreenEvent
		if k == screenrtc.KindState && proto.Unmarshal(p, &ev) == nil && ev.GetClipboard() != nil {
			return ev.GetClipboard().GetText()
		}
	}
}

// screenMachine waits until the phone sees a machine that can show its
// screen, and returns its id.
func screenMachine(t *testing.T, m *mobileCore) string {
	t.Helper()
	var id string
	eventually(t, 60*time.Second, "the Mac's screen offered to the iPhone", func() bool {
		res, err := m.client.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		for _, mc := range res.Machines {
			if mc.Status.GetScreen().GetAvailable() {
				id = mc.MachineId
			}
		}
		return id != ""
	})
	return id
}

// Where the two ends reach each other only through the relay (the phone
// takes no other path here), the relay's TURN server forwards the picture:
// the session connects, and says it is relayed.
func TestScreenThroughTURN(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("e2e on macOS")
	}
	env, _ := screenEnv(t)
	w := newOwnerMac(t, append([]string{"HERMESH_FAKE_PROVIDERS=1"}, env...), &localv1.GrantSpec{})
	w.relayArgs = append(w.relayArgs, "--turn-listen", fmt.Sprintf("127.0.0.1:%d", freePort(t)), "--turn-host", "127.0.0.1")
	w.restartRelay()
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.GenerateKey()
	t.Setenv("HERMESH_SCREEN_RELAY_ONLY", "1") // the phone's core, started next
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)
	s := openScreen(t, m, screenMachine(t, m))
	if path := s.live(t); path != "relay" {
		t.Fatalf("path: %q, want relay", path)
	}
	// The app leaves: the session ends on the Mac.
	viewer := func() string {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil || len(res.Machines) != 1 {
			return "?"
		}
		return res.Machines[0].Status.GetScreen().GetViewer()
	}
	eventually(t, 30*time.Second, "the Mac's app sees who is in control", func() bool { return viewer() == "test-iphone" })
	s.conn.Close()
	eventually(t, 30*time.Second, "the session ended on the Mac", func() bool { return viewer() == "" })
}

// Where the relay's STUN and TURN server can't be reached (a network that
// blocks it), the two ends stop waiting for its addresses and connect
// directly: the picture comes in seconds, where each end used to wait 7.8 s
// for a TURN answer before it even made its half of the handshake.
func TestScreenUnreachableTURN(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("e2e on macOS")
	}
	env, _ := screenEnv(t)
	w := newOwnerMac(t, append([]string{"HERMESH_FAKE_PROVIDERS=1"}, env...), &localv1.GrantSpec{})
	port := freePort(t)
	// The relay serves TURN here, but tells everyone it is at an address where nothing answers.
	w.relayArgs = append(w.relayArgs, "--turn-listen", fmt.Sprintf("127.0.0.1:%d", port), "--turn-host", fmt.Sprintf("192.0.2.1:%d", port))
	w.restartRelay()
	st, err := w.app.client.GetStatus(context.Background(), &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.GenerateKey()
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)
	id := screenMachine(t, m)
	start := time.Now()
	s := openScreen(t, m, id)
	if path := s.live(t); path != "direct" {
		t.Errorf("path: %q, want direct", path)
	}
	took := time.Since(start)
	t.Logf("the picture came %v after the page opened", took.Round(time.Millisecond))
	if took > 6*time.Second {
		t.Errorf("the picture took %v; the unreachable server held it up", took)
	}
}

// Computer Control end to end, but for the app's page: an iPhone's core,
// joined as a trusted device, opens the screen of the owner's Mac through
// the relay. The Mac's hermeshd runs the real helper with its test picture.
// The frames arrive from a keyframe on, the Mac reports the picture's size,
// what the phone types reaches the helper, the clipboard goes both ways
// (the helper's is a file here), the Mac says who is in control,
// and when the app leaves the session ends.
func TestScreen(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("e2e on macOS")
	}
	env, log := screenEnv(t)
	w := newOwnerMac(t, append([]string{"HERMESH_FAKE_PROVIDERS=1"}, env...), &localv1.GrantSpec{})
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.GenerateKey()
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)

	// The phone sees that the Mac can show its screen.
	var machineID string
	viewer := func() (string, bool) {
		res, err := m.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return "", false
		}
		for _, mc := range res.Machines {
			if mc.Status.GetScreen().GetAvailable() {
				machineID = mc.MachineId
				return mc.Status.Screen.Viewer, true
			}
		}
		return "", false
	}
	eventually(t, 60*time.Second, "the Mac's screen offered to the iPhone", func() bool { _, ok := viewer(); return ok })

	start := time.Now()
	s := openScreen(t, m, machineID)
	if path := s.live(t); path != "direct" {
		t.Errorf("path: %q, want direct", path)
	}
	t.Logf("the picture came %v after the page opened", time.Since(start).Round(time.Millisecond))

	// What the phone does reaches the Mac's helper.
	for _, in := range []*screenv1.Input{
		{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.5, Y: 0.25, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Down: true, Clicks: 1}}},
		{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.5, Y: 0.25, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Clicks: 1}}},
		{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "你好"}}},
	} {
		b, _ := proto.Marshal(in)
		if err := screenrtc.WriteMessage(s.conn, screenrtc.KindInput, b); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 20*time.Second, "the phone's input at the Mac", func() bool {
		b, _ := os.ReadFile(log)
		return strings.Contains(string(b), `"down":true`) && strings.Contains(string(b), "你好")
	})

	// The phone's clipboard goes to the Mac's and is pasted there; what is
	// copied on the Mac comes back to the phone, but never the phone's own.
	clipboard := filepath.Join(filepath.Dir(log), "screen-clipboard.txt")
	b, _ := proto.Marshal(&screenv1.Input{Event: &screenv1.Input_Clipboard{Clipboard: &screenv1.Clipboard{Text: "从手机 copied", Paste: screenv1.Paste_PASTE_APP}}})
	if err := screenrtc.WriteMessage(s.conn, screenrtc.KindInput, b); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "the phone's clipboard on the Mac", func() bool {
		in, _ := os.ReadFile(log)
		c, _ := os.ReadFile(clipboard)
		return strings.Contains(string(in), `"paste":"PASTE_APP"`) && strings.Contains(string(in), "从手机 copied") && string(c) == "从手机 copied"
	})
	time.Sleep(time.Second) // time enough for the helper to send it back, were it to
	if err := os.WriteFile(clipboard, []byte("copied on the Mac 在电脑上"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.clipboard(t); got != "copied on the Mac 在电脑上" {
		t.Errorf("the phone got the clipboard %q", got)
	}

	eventually(t, 30*time.Second, "the Mac says who is in control", func() bool { v, _ := viewer(); return v == "test-iphone" })
	// The Mac's own app sees it too (its menu bar names the device), and stops it there: the phone is told why.
	eventually(t, 30*time.Second, "the Mac's app sees who is in control", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && len(res.Machines) == 1 && res.Machines[0].Local && res.Machines[0].Status.GetScreen().GetViewer() == "test-iphone"
	})
	if _, err := w.app.client.StopScreen(ctx, &localv1.StopScreenRequest{}); err != nil {
		t.Fatal(err)
	}
	if why := s.ended(t); why != "the machine ended the session" {
		t.Errorf("the phone was told %q", why)
	}
	eventually(t, 30*time.Second, "nobody in control", func() bool { v, ok := viewer(); return ok && v == "" })
}

// TestScreenLatency measures where the picture's delay comes from, against
// this Mac's real screen (HERMESH_SCREEN_REAL=1, which needs Screen
// Recording for whatever starts the test): the Mac's hermeshd stamps each
// frame at every stage (HERMESH_SCREEN_TRACE), the phone's core adds its
// own, and this test, reading the stream as the app does, the last. It
// prints each stage's delay. Something must move on the screen meanwhile:
// HERMESH_SCREEN_TICKER names a program to run for that (a clock, say).
// HERMESH_SCREEN_POINTER names one that prints "display <w> <h>" and then
// "<µs since 1970> <x> <y>" each time the Mac's pointer moves: with it the
// test also measures the way back, from the app sending a pointer move to
// the Mac's pointer being there (it moves the real pointer about).
//
//	HERMESH_SCREEN_REAL=1 HERMESH_SCREEN_TICKER=/path/to/clock go test ./e2e -run TestScreenLatency -v
func TestScreenLatency(t *testing.T) {
	if os.Getenv("HERMESH_SCREEN_REAL") == "" || runtime.GOOS != "darwin" {
		t.Skip("set HERMESH_SCREEN_REAL=1 to measure against this Mac's screen")
	}
	trace := filepath.Join(t.TempDir(), "trace.csv")
	env, _ := screenEnv(t)
	w := newOwnerMac(t, append([]string{"HERMESH_FAKE_PROVIDERS=1", "HERMESH_SCREEN_TRACE=1"}, env...), &localv1.GrantSpec{})
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.GenerateKey()
	t.Setenv("HERMESH_SCREEN_TRACE", trace) // the phone's core, started next
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)
	s := openScreen(t, m, screenMachine(t, m))
	if ticker := os.Getenv("HERMESH_SCREEN_TICKER"); ticker != "" {
		c := exec.Command(ticker, "14")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer c.Process.Kill()
	}
	// Read as the app does, noting when each frame was in hand, by the name its header carries.
	read := map[int64]int64{}
	var path string
	began := time.Now()
	for time.Since(began) < 13*time.Second {
		s.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		k, p, err := screenrtc.ReadMessage(s.r)
		if err != nil {
			t.Fatalf("reading the screen: %v", err)
		}
		switch k {
		case screenrtc.KindVideo:
			read[int64(binary.BigEndian.Uint64(p[1:9]))] = time.Now().UnixMicro()
		case screenrtc.KindStatus:
			var status struct{ Phase, Path, Local, Remote, Error string }
			json.Unmarshal(p, &status)
			if status.Phase == "connected" {
				path = fmt.Sprintf("%s: %s to %s", status.Path, status.Local, status.Remote)
			}
		}
	}
	pointer := measurePointer(t, s)
	s.conn.Close()
	t.Logf("path: %s", path)
	if pointer != "" {
		t.Log(pointer)
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("%d frames read, none of them traced: %v", len(read), err)
	}
	stages := []string{"screen → captured", "captured → encoded", "encoded → sent", "sent → received", "received → handed on", "handed on → read by the app", "screen → read by the app"}
	samples := make([][]float64, len(stages))
	var sizes float64
	var first, last int64
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var d, c, e, snt, r, h int64
		var size int
		var key bool
		if _, err := fmt.Sscanf(line, "%d,%d,%d,%d,%d,%d,%d,%t", &d, &c, &e, &snt, &r, &h, &size, &key); err != nil || d == 0 {
			continue // a keyframe made again from the last picture has no screen time
		}
		a, ok := read[snt]
		if !ok || snt-began.UnixMicro() < 2_000_000 { // the first two seconds: the session settles
			continue
		}
		for i, v := range []int64{c - d, e - c, snt - e, r - snt, h - r, a - h, a - d} {
			samples[i] = append(samples[i], float64(v)/1000)
		}
		sizes += float64(size)
		if first == 0 {
			first = snt
		}
		last = snt
		n++
	}
	if n < 30 {
		t.Fatalf("only %d frames measured: nothing moved on the screen?", n)
	}
	t.Logf("%d frames in %.1f s (%.0f a second), %.1f kB each on average", n, float64(last-first)/1e6, float64(n-1)/(float64(last-first)/1e6), sizes/float64(n)/1000)
	t.Logf("%-30s %8s %8s %8s", "stage (ms)", "median", "90%", "worst")
	for i, name := range stages {
		v := samples[i]
		sort.Float64s(v)
		t.Logf("%-30s %8.1f %8.1f %8.1f", name, v[len(v)/2], v[len(v)*9/10], v[len(v)-1])
	}
}

// measurePointer times the owner's input on its way to the Mac: pointer
// moves sent as the app sends them, and when the Mac's pointer got to each.
func measurePointer(t *testing.T, s *screenStream) string {
	tool := os.Getenv("HERMESH_SCREEN_POINTER")
	if tool == "" {
		return ""
	}
	c := exec.Command(tool, "8")
	out, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	sc := bufio.NewScanner(out)
	var width int
	if !sc.Scan() {
		t.Fatal("the pointer tool said nothing")
	}
	fmt.Sscanf(sc.Text(), "display %d", &width)
	seen := map[int][]int64{} // by x, each time the pointer got there
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			var at int64
			var x, y int
			if _, err := fmt.Sscanf(sc.Text(), "%d %d %d", &at, &x, &y); err == nil {
				seen[x] = append(seen[x], at)
			}
		}
	}()
	time.Sleep(500 * time.Millisecond)
	sent := map[int]int64{}
	for i := range 40 {
		fx := 0.30 + float64(i)*0.008
		b, _ := proto.Marshal(&screenv1.Input{Event: &screenv1.Input_Move{Move: &screenv1.Move{X: fx, Y: 0.55}}})
		sent[int(math.Round(fx*float64(width-1)))] = time.Now().UnixMicro()
		if err := screenrtc.WriteMessage(s.conn, screenrtc.KindMove, b); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	c.Process.Kill()
	<-done
	var ms []float64
	for x, at := range sent {
		for _, got := range seen[x] {
			if got >= at-1000 { // not an earlier visit to the same place
				ms = append(ms, float64(got-at)/1000)
				break
			}
		}
	}
	if len(ms) < 10 {
		return fmt.Sprintf("the way back: only %d of %d pointer moves seen", len(ms), len(sent))
	}
	sort.Float64s(ms)
	return fmt.Sprintf("the way back, a pointer move from the app to the Mac's pointer: median %.1f ms, 90%% %.1f, worst %.1f (%d of %d moves)", ms[len(ms)/2], ms[len(ms)*9/10], ms[len(ms)-1], len(ms), len(sent))
}
