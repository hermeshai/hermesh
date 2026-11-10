//go:build unix

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
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

// Computer Control end to end, but for the app's page: an iPhone's core,
// joined as a trusted device, opens the screen of the owner's Mac through
// the relay. The Mac's hermeshd runs the real helper with its test picture.
// The frames arrive from a keyframe on, the Mac reports the picture's size,
// what the phone types reaches the helper, the Mac says who is in control,
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

	s := openScreen(t, m, machineID)
	if path := s.live(t); path != "direct" {
		t.Errorf("path: %q, want direct", path)
	}

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
