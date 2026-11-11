//go:build unix

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// TestLinuxScreenVM (HERMESH_LINUX_VM=<a Lima instance>) is Computer Control
// on a real Linux desktop (docs/computer-control-linux.md §10): a virtual
// machine whose user is logged in to its desktop (GNOME on Ubuntu, say),
// with foot installed. The release build of the helper and a Linux hermeshd
// go to ~/.local/bin there; `hermeshd pair` installs hermeshd as the systemd
// user service it is, which finds the desktop by itself. An iPhone's core
// opens the screen through the relay on this computer, a click and
// "Hello VM 你好" reach a terminal there; locking the screen takes it from
// the phone with the reason, unlocking gives it back.
//
// HERMESH_LINUX_VM_HOST is this computer as the machine reaches it
// (host.lima.internal by default); HERMESH_SCREEN_LINUX_RELEASE the helper,
// else apps/screen/linux/build-release.sh builds it.
func TestLinuxScreenVM(t *testing.T) {
	vm := os.Getenv("HERMESH_LINUX_VM")
	if vm == "" {
		t.Skip("set HERMESH_LINUX_VM to a Lima instance with a desktop logged in")
	}
	host := os.Getenv("HERMESH_LINUX_VM_HOST")
	if host == "" {
		host = "host.lima.internal"
	}
	sh := func(timeout time.Duration, script string) string {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "limactl", "shell", vm, "--", "bash", "-lc", script).CombinedOutput()
		if err != nil {
			t.Fatalf("in %s: %s: %v\n%s", vm, script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The desktop's session, as the user's programs have it.
	session := `export XDG_RUNTIME_DIR=/run/user/$(id -u) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u)/bus WAYLAND_DISPLAY=wayland-0;`

	helper := os.Getenv("HERMESH_SCREEN_LINUX_RELEASE")
	if helper == "" {
		helper = filepath.Join(t.TempDir(), "hermesh-screen")
		if out, err := exec.Command("apps/screen/linux/build-release.sh", helper).CombinedOutput(); err != nil {
			t.Fatalf("build-release.sh: %v\n%s", err, out)
		}
	}
	linuxBin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(linuxBin, "hermeshd"), "./cmd/hermeshd")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data := "$HOME/.hermesh-e2e"
	stop := func() {
		exec.Command("limactl", "shell", vm, "--", "bash", "-lc", "~/.local/bin/hermeshd service uninstall --data-dir "+data+" >/dev/null 2>&1; pkill -f '[h]ermeshd pair'; rm -rf "+data+" /tmp/typed.txt /tmp/hermesh-pair.log; pkill -x foot; sudo loginctl unlock-sessions; true").Run()
	}
	stop()
	t.Cleanup(stop)
	t.Cleanup(func() { // runs first: what the machine said, when the test failed
		if t.Failed() {
			out, _ := exec.Command("limactl", "shell", vm, "--", "bash", "-lc", "tail -40 "+data+"/logs/hermeshd.log; ~/.local/bin/hermeshd screen --data-dir "+data).CombinedOutput()
			t.Logf("the machine:\n%s", out)
		}
	})
	sh(time.Minute, "mkdir -p ~/.local/bin")
	for f, name := range map[string]string{filepath.Join(linuxBin, "hermeshd"): "hermeshd", helper: "hermesh-screen"} {
		if out, err := exec.Command("limactl", "copy", f, vm+":.local/bin/"+name).CombinedOutput(); err != nil {
			t.Fatalf("limactl copy: %v %s", err, out)
		}
	}
	sh(time.Minute, "chmod +x ~/.local/bin/hermeshd ~/.local/bin/hermesh-screen")
	t.Logf("the helper there: %s; the desktop: %s", sh(time.Minute, "~/.local/bin/hermesh-screen --version"),
		sh(time.Minute, `loginctl show-session $(loginctl show-user $(id -u) -p Display --value) -p Type -p Desktop`))

	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.GenerateKey()
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)
	addr := strings.Replace(w.addr, "127.0.0.1", host, 1)
	// Pairing installs the service; a proxy the machine may have (Lima passes this computer's on) is not for this computer.
	sh(time.Minute, fmt.Sprintf(`mkdir -p ~/.config/systemd/user/hermeshd.service.d && printf '[Service]\nEnvironment=NO_PROXY=localhost,127.0.0.1,%s no_proxy=localhost,127.0.0.1,%s\n' > ~/.config/systemd/user/hermeshd.service.d/e2e.conf`, host, host))
	t.Cleanup(func() {
		exec.Command("limactl", "shell", vm, "--", "rm", "-rf", ".config/systemd/user/hermeshd.service.d").Run()
	})
	sh(time.Minute, fmt.Sprintf("export NO_PROXY=$NO_PROXY,%s no_proxy=$no_proxy,%s; nohup ~/.local/bin/hermeshd pair %s --name vm-desktop --data-dir %s > /tmp/hermesh-pair.log 2>&1 &", host, host, addr, data))
	var code string
	eventually(t, 2*time.Minute, "the machine's pairing code", func() bool {
		out, _ := exec.Command("limactl", "shell", vm, "--", "cat", "/tmp/hermesh-pair.log").Output()
		if c := codeRE.FindStringSubmatch(string(out)); c != nil {
			code = c[1]
		}
		return code != ""
	})
	approved, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "vm-desktop"})
	if err != nil {
		t.Fatal(err)
	}
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
	eventually(t, 2*time.Minute, "the desktop offered to the iPhone", offered)
	t.Logf("hermeshd screen: %s", sh(time.Minute, "~/.local/bin/hermeshd screen --data-dir "+data))
	t.Logf("the service: %s", sh(time.Minute, "systemctl --user is-active hermeshd; loginctl show-user $(id -u) -p Linger"))

	// A terminal, filling the screen, writes what reaches it.
	sh(time.Minute, session+" (setsid foot --maximized -o key-bindings.clipboard-paste=Control+v sh -c 'stty raw -echo; dd bs=1 of=/tmp/typed.txt 2>/dev/null' >/dev/null 2>&1 &)")
	time.Sleep(3 * time.Second)
	s := openScreen(t, m, approved.MachineId)
	format, path := s.picture(t)
	t.Logf("the picture: %dx%d, %s", format.GetWidth(), format.GetHeight(), path)
	for _, in := range []*screenv1.Input{
		{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.5, Y: 0.5, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Down: true, Clicks: 1}}},
		{Event: &screenv1.Input_Button{Button: &screenv1.Button{X: 0.5, Y: 0.5, Button: screenv1.MouseButton_MOUSE_BUTTON_LEFT, Clicks: 1}}},
		{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "Hello VM "}}},
		{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "你好"}}},
	} {
		b, _ := proto.Marshal(in)
		if err := screenrtc.WriteMessage(s.conn, screenrtc.KindInput, b); err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond)
	}
	typed := func() string {
		out, _ := exec.Command("limactl", "shell", vm, "--", "cat", "/tmp/typed.txt").Output()
		return string(out)
	}
	// Chinese is pasted on GNOME (§3.4): this terminal pastes with Ctrl+V.
	eventually(t, 20*time.Second, "what the phone typed, in the terminal", func() bool { return typed() == "Hello VM 你好" })

	// The screen locks: GNOME ends the session, the phone is told why, and so is a phone that connects then.
	sh(time.Minute, "sudo loginctl lock-sessions")
	if why := s.ended(t); why != screenrtc.ErrLocked {
		t.Errorf("locked: the phone was told %q", why)
	}
	eventually(t, 30*time.Second, "the machine says it is locked", func() bool {
		out, _ := exec.Command("limactl", "shell", vm, "--", "bash", "-lc", "~/.local/bin/hermeshd screen --data-dir "+data).Output()
		return strings.Contains(string(out), "locked")
	})
	if !offered() {
		t.Error("a locked desktop is no longer offered: the phone would not say why")
	}
	if why := openScreen(t, m, approved.MachineId).ended(t); why != screenrtc.ErrLocked {
		t.Errorf("connecting while locked: the phone was told %q", why)
	}
	sh(time.Minute, "sudo loginctl unlock-sessions")
	eventually(t, 60*time.Second, "the machine unlocked", func() bool {
		out, _ := exec.Command("limactl", "shell", vm, "--", "bash", "-lc", "~/.local/bin/hermeshd screen --data-dir "+data).Output()
		return strings.Contains(string(out), "ready")
	})
	s3 := openScreen(t, m, approved.MachineId)
	if f, _ := s3.picture(t); f.GetWidth() == 0 {
		t.Error("no picture after unlocking")
	}
}
