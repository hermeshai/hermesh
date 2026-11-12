//go:build unix

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// linuxDesktops are the desktops TestLinuxScreen runs on, each in a
// container with no screen: what it installs, and the commands that start
// it and a terminal that writes what it is typed to /tmp/typed.txt (as the
// user "me", in /tmp/run). The terminal opens after a moment, once the
// session's keyboard exists: a container has no keyboard of its own. It
// pastes the clipboard with Ctrl+Shift+V, as terminals do (xterm told so).
// copy copies its first argument there as a program of that desktop does.
var linuxDesktops = []struct {
	name, packages, start, copy string
	width, height               uint32
	way                         string // as `hermeshd screen` names it
}{
	{"x11", "xvfb xterm font-misc-misc xsetroot xclip",
		`Xvfb :5 -noreset -screen 0 1600x900x24 >/tmp/run/x.log 2>&1 &
sleep 1; export DISPLAY=:5; xsetroot -solid '#d02020'
(sleep 4; xterm -u8 -xrm 'XTerm*VT100.translations: #override Ctrl Shift <Key>V: insert-selection(CLIPBOARD)' -geometry 80x20+500+300 -e sh -c 'stty raw -echo; dd bs=1 of=/tmp/typed.txt 2>/dev/null') &`,
		`printf %s "$1" | DISPLAY=:5 xclip -selection clipboard`, 1600, 900, "X11"},
	{"sway", "sway swaybg foot font-dejavu libcap-setcap wl-clipboard",
		`printf 'output * bg #2040d0 solid_color\noutput HEADLESS-1 resolution 1280x720\n' > /tmp/run/sway.conf
WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1 sway -c /tmp/run/sway.conf >/tmp/run/sway.log 2>&1 &
sleep 2
(sleep 4; WAYLAND_DISPLAY=$(cd /tmp/run && ls wayland-* | grep -v lock | head -1) foot sh -c 'stty raw -echo; dd bs=1 of=/tmp/typed.txt 2>/dev/null') &`,
		`printf %s "$1" | WAYLAND_DISPLAY=$(cd /tmp/run && ls wayland-* | grep -v lock | head -1) wl-copy`, 1280, 720, "wlroots"},
	{"gnome", "mutter pipewire wireplumber foot font-dejavu mesa-dri-gallium wl-clipboard",
		`pipewire >/tmp/run/pw.log 2>&1 &
sleep 0.5; wireplumber >/tmp/run/wp.log 2>&1 &
mutter --wayland --headless --no-x11 --virtual-monitor 1280x720 >/tmp/run/mutter.log 2>&1 &
sleep 3
(sleep 4; WAYLAND_DISPLAY=wayland-0 foot sh -c 'stty raw -echo; dd bs=1 of=/tmp/typed.txt 2>/dev/null') &`,
		`printf %s "$1" | WAYLAND_DISPLAY=wayland-0 wl-copy`, 1280, 720, "GNOME"},
}

// TestLinuxScreen (HERMESH_DOCKER=1) is Computer Control on Linux end to end
// (docs/computer-control-linux.md §10): on each desktop of linuxDesktops, a
// Linux hermeshd in a container, with the helper beside it and its desktop
// found by itself, is paired with the owner's Mac; an iPhone's core, joined
// as a trusted device, sees that it can show its screen and opens it through
// the relay. The picture comes from a keyframe on at the desktop's size,
// what the phone types (Chinese among it) reaches the terminal there, so does
// the phone's clipboard, pasted as a terminal pastes, and what a program
// copies there reaches the phone; `hermeshd screen stop` at the machine ends
// the session, the phone told why.
// `hermeshd screen` says which way it uses; `hermeshd screen off` takes the
// screen from the phone, and `on` gives it back.
//
// The helper is HERMESH_SCREEN_LINUX, else built in Docker (linuxScreenHelper).
func TestLinuxScreen(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	helper := linuxScreenHelper(t)
	linuxBin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(linuxBin, "hermeshd"), "./cmd/hermeshd")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	b, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linuxBin, "hermesh-screen"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), func() *identity.SoftwareKey { k, _ := identity.GenerateKey(); return k }())
	// Docker Desktop (a Mac) reaches this computer's loopback as host.docker.internal; on Linux the
	// containers share this computer's network.
	addr, network := strings.Replace(w.addr, "127.0.0.1", "host.docker.internal", 1), "bridge"
	if runtime.GOOS == "linux" {
		addr, network = w.addr, "host"
	}
	for _, d := range linuxDesktops {
		t.Run(d.name, func(t *testing.T) {
			name := fmt.Sprintf("hermesh-e2e-screen-%s-%d", d.name, time.Now().UnixNano())
			// The desktop and hermeshd run as the user, in one session bus; hermeshd's environment names no display, as under systemd.
			session := filepath.Join(t.TempDir(), "session.sh")
			os.WriteFile(session, []byte("export LANG=C.UTF-8 LC_ALL=C.UTF-8 XDG_RUNTIME_DIR=/tmp/run\nmkdir -p /tmp/run && chmod 700 /tmp/run\n"+d.start+
				"\nenv -u DISPLAY -u WAYLAND_DISPLAY /opt/hermesh/hermeshd pair "+addr+" --name linux-"+d.name+" --no-service --data-dir /data\n"+
				"exec env -u DISPLAY -u WAYLAND_DISPLAY /opt/hermesh/hermeshd run --data-dir /data\n"), 0o644)
			script := strings.Join([]string{
				"apk add --no-cache dbus python3 libstdc++ " + d.packages + " >/dev/null",
				"(command -v setcap >/dev/null && setcap -r /usr/bin/sway 2>/dev/null; true)",
				"mkdir -p /run/dbus && rm -f /usr/share/dbus-1/system-services/* && dbus-daemon --system --fork",
				"adduser -D -u 1000 me && mkdir -p /data && chown me /data",
				"exec su me -c 'dbus-run-session -- sh /opt/session.sh'",
			}, " && ")
			dockerRun := exec.Command("docker", "run", "--rm", "-d", "--name", name, "--shm-size=512m", "--network", network,
				"-v", linuxBin+":/opt/hermesh:ro", "-v", session+":/opt/session.sh:ro", "-e", "HERMESH_FAKE_PROVIDERS=1", "alpine:edge", "sh", "-c", script)
			if out, err := dockerRun.CombinedOutput(); err != nil {
				t.Fatalf("docker run: %v %s", err, out)
			}
			t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
			logs := func() string { o, _ := exec.Command("docker", "logs", name).CombinedOutput(); return string(o) }
			var code string
			eventually(t, 3*time.Minute, "the Linux machine's pairing code", func() bool {
				if m := codeRE.FindStringSubmatch(logs()); m != nil {
					code = m[1]
				}
				return code != ""
			})
			approved, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "linux-" + d.name})
			if err != nil {
				t.Fatalf("approve: %v\n%s", err, logs())
			}
			// The phone sees that the machine can show its screen.
			offered := func() bool {
				res, err := m.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
				if err != nil {
					return false
				}
				for _, mc := range res.Machines {
					if mc.MachineId == approved.MachineId && mc.Status.GetScreen().GetAvailable() {
						return true
					}
				}
				return false
			}
			eventually(t, 90*time.Second, "the Linux screen offered to the iPhone", offered)
			hermeshd := func(args string) string {
				out, err := exec.Command("docker", "exec", name, "su", "me", "-c", "/opt/hermesh/hermeshd "+args+" --data-dir /data").CombinedOutput()
				if err != nil {
					t.Fatalf("hermeshd %s: %v %s", args, err, out)
				}
				return string(out)
			}
			if out := hermeshd("screen"); !strings.Contains(out, "ready, through "+d.way) {
				t.Errorf("hermeshd screen: %q", out)
			}
			s := openScreen(t, m, approved.MachineId)
			format, path := s.picture(t)
			if format.GetWidth() != d.width || format.GetHeight() != d.height {
				t.Errorf("format %v, want %dx%d", format, d.width, d.height)
			}
			t.Logf("connected: %s", path)
			// A click on the terminal, then typing; Chinese reaches it too (on GNOME, pasted as a terminal pastes).
			time.Sleep(4 * time.Second) // the terminal opens
			send := func(ins ...*screenv1.Input) {
				for _, in := range ins {
					b, _ := proto.Marshal(in)
					if err := screenrtc.WriteMessage(s.conn, screenrtc.KindInput, b); err != nil {
						t.Fatal(err)
					}
					time.Sleep(300 * time.Millisecond)
				}
			}
			send(
				&screenv1.Input{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.5, Y: 0.5, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Down: true, Clicks: 1}}},
				&screenv1.Input{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.5, Y: 0.5, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Clicks: 1}}},
				&screenv1.Input{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "Hello Linux "}}},
				&screenv1.Input{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "你好", Paste: screenv1.Paste_PASTE_TERMINAL}}},
			)
			typed := func() string {
				o, _ := exec.Command("docker", "exec", name, "cat", "/tmp/typed.txt").Output()
				return string(o)
			}
			eventually(t, 20*time.Second, "what the phone typed, in the terminal", func() bool { return typed() == "Hello Linux 你好" })
			// The phone's clipboard, pasted as a terminal pastes.
			send(&screenv1.Input{Event: &screenv1.Input_Clipboard{Clipboard: &screenv1.Clipboard{Text: "剪贴板 from phone", Paste: screenv1.Paste_PASTE_TERMINAL}}})
			eventually(t, 20*time.Second, "the phone's clipboard, pasted in the terminal", func() bool { return typed() == "Hello Linux 你好剪贴板 from phone" })
			// What a program copies there reaches the phone.
			copied := "copied on linux-" + d.name + " 复制"
			if out, err := exec.Command("docker", "exec", name, "su", "me", "-c", "export XDG_RUNTIME_DIR=/tmp/run; sh -c '"+d.copy+"' - '"+copied+"' >/dev/null 2>&1").CombinedOutput(); err != nil {
				t.Fatalf("copying on the machine: %v %s", err, out)
			}
			if got := linuxCopied(t, s); got != copied {
				t.Errorf("the phone was told %q was copied, want %q", got, copied)
			}
			if out := hermeshd("screen"); !strings.Contains(out, "test-iphone is in control") {
				t.Errorf("hermeshd screen, in a session: %q", out)
			}
			// The machine's user ends it there.
			hermeshd("screen stop")
			if why := s.ended(t); why != "the machine ended the session" {
				t.Errorf("the phone was told %q", why)
			}
			// And turns it off, then on again.
			hermeshd("screen off")
			eventually(t, 60*time.Second, "the screen no longer offered", func() bool { return !offered() })
			hermeshd("screen on")
			eventually(t, 60*time.Second, "the screen offered again", offered)
		})
	}
}

// picture reads the stream until the picture has come (frames from a
// keyframe on, the machine's report of its size) and the two ends are
// connected, and returns the size and the path between them.
func (s *screenStream) picture(t *testing.T) (*screenv1.Format, string) {
	t.Helper()
	var frames int
	var firstKey bool
	var format *screenv1.Format
	path := ""
	deadline := time.Now().Add(90 * time.Second)
	for (frames < 1 || format == nil || path == "") && time.Now().Before(deadline) {
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
			if e := ev.GetState().GetError(); e != "" {
				t.Fatalf("the machine can't show its screen: %s %s", e, ev.GetState().GetDetail())
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
	if frames < 1 || !firstKey || path == "" || format == nil {
		t.Fatalf("frames %d, first is a keyframe %v, path %q, format %v", frames, firstKey, path, format)
	}
	return format, path
}

// linuxCopied reads the stream until the machine tells what was copied
// there, and returns it: the first it tells, so text the phone gave it
// coming back fails the test.
func linuxCopied(t *testing.T, s *screenStream) string {
	t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		k, p, err := screenrtc.ReadMessage(s.r)
		if err != nil {
			t.Fatalf("waiting for what was copied on the machine: %v", err)
		}
		var ev screenv1.ScreenEvent
		if k == screenrtc.KindState && proto.Unmarshal(p, &ev) == nil && ev.GetClipboard() != nil {
			return ev.GetClipboard().GetText()
		}
	}
}

// linuxScreenHelper is hermesh-screen for Linux on this computer's
// architecture: $HERMESH_SCREEN_LINUX, else built in Docker from
// apps/screen/linux, linked to musl dynamically (the containers are Alpine)
// so it can load PipeWire.
func linuxScreenHelper(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("HERMESH_SCREEN_LINUX"); bin != "" {
		return bin
	}
	toml, err := os.ReadFile("apps/screen/linux/rust-toolchain.toml")
	if err != nil {
		t.Fatal(err)
	}
	toolchain := regexp.MustCompile(`channel = "(.+)"`).FindSubmatch(toml)[1]
	src, _ := filepath.Abs("apps/screen/linux")
	out := t.TempDir()
	start := time.Now()
	build := exec.Command("docker", "run", "--rm", "-v", src+":/src:ro", "-v", out+":/out", "-v", "hermesh-e2e-screen-target:/target",
		"-v", "hermesh-e2e-cargo-registry:/usr/local/cargo/registry", "-e", "CARGO_TARGET_DIR=/target", "-e", "RUSTFLAGS=-C target-feature=-crt-static",
		"-w", "/src", fmt.Sprintf("rust:%s-alpine", toolchain),
		"sh", "-c", "apk add --no-cache build-base nasm >/dev/null && cargo build --locked --release && cp /target/release/hermesh-screen /out/")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hermesh-screen for Linux: %v\n%s", err, b)
	}
	t.Logf("hermesh-screen for Linux built in %v", time.Since(start).Round(time.Second))
	return filepath.Join(out, "hermesh-screen")
}
