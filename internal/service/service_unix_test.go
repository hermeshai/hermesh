//go:build unix

package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The job Install writes and the one the Mac app ships are the same kind, and
// e2e TestDesktopDaemonUnderLoad runs the app's under launchd.
func TestLaunchdJobsAreInteractive(t *testing.T) {
	const want = "<key>ProcessType</key><string>Interactive</string>"
	if text := plistText("/bin/hermeshd", "/data", "/logs", nil); !strings.Contains(text, want) {
		t.Errorf("the job Install writes has no %s:\n%s", want, text)
	}
	app, err := os.ReadFile(filepath.Join("..", "..", "apps", "desktop", "build", "com.hermesh.app.daemon.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), want) {
		t.Errorf("the app's job has no %s:\n%s", want, app)
	}
}

// InstalledFor finds the service by the data directory Install gave it, and
// not by another one that only starts the same.
func TestInstalledFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := filepath.Join(home, "hermesh")
	if InstalledFor(data) {
		t.Fatal("found with nothing installed")
	}
	path, text := launchdPlist(), plistText("/bin/hermeshd", data, filepath.Join(data, "logs"), nil)
	if runtime.GOOS == "linux" {
		path, text = systemdUnit(), unitText("/bin/hermeshd", data, nil)
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if !InstalledFor(data) {
		t.Fatal("not found for its data directory")
	}
	for _, other := range []string{filepath.Join(data, "machine"), data[:len(data)-1], filepath.Dir(data)} {
		if InstalledFor(other) {
			t.Errorf("found for %s", other)
		}
	}
}

// Install works with systemd 219 (Synology DSM 7): its systemctl has no
// --now, and its user manager no D-Bus, only its own socket.
func TestInstallSystemd219(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	home, bin, run := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("USER", "root")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	os.MkdirAll(filepath.Join(run, "systemd"), 0o755)
	os.WriteFile(filepath.Join(run, "systemd", "private"), nil, 0o644)
	calls := filepath.Join(home, "calls")
	os.WriteFile(filepath.Join(bin, "loginctl"), []byte("#!/bin/sh\necho Linger=yes\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(`#!/bin/sh
for a; do [ "$a" = --now ] && { echo "systemctl: unrecognized option '--now'" >&2; exit 1; }; done
echo "$@" >> `+calls+"\n"), 0o755)

	start := time.Now()
	if err := Install("/bin/hermeshd", filepath.Join(home, "hermesh"), filepath.Join(home, "logs")); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v waiting for a user D-Bus", d)
	}
	b, _ := os.ReadFile(calls)
	if want := "--user daemon-reload\n--user enable hermeshd.service\n--user start hermeshd.service\n"; string(b) != want {
		t.Errorf("systemctl calls:\n%s\nwant:\n%s", b, want)
	}
}
