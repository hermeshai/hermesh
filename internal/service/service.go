// Package service installs hermeshd as a per-user background service
// (launchd LaunchAgent on macOS, systemd user unit on Linux).
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const label = "com.hermesh.daemon"

func launchdPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func systemdUnit() string {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, _ := os.UserHomeDir()
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "systemd", "user", "hermeshd.service")
}

// Install writes and loads the service definition running `bin run`, with
// args before its data directory (a trusted device: --owner --no-machine).
func Install(bin, dataDir, logDir string, args ...string) error {
	switch runtime.GOOS {
	case "darwin":
		os.MkdirAll(logDir, 0o700)
		plist := plistText(bin, dataDir, logDir, args)
		path := launchdPlist()
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
			return err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		exec.Command("launchctl", "bootout", domain+"/"+label).Run()
		if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		return nil
	case "linux":
		unit := unitText(bin, dataDir, args)
		// Lingering first: it starts the user's systemd, which a session
		// opened with su lacks, and keeps it running after logout.
		lingerErr := enableLinger()
		if err := userBus(); err != nil {
			if lingerErr != nil {
				return fmt.Errorf("%w; turn lingering on with `sudo loginctl enable-linger %s` and try again", err, os.Getenv("USER"))
			}
			return err
		}
		path := systemdUnit()
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			return err
		}
		for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", "hermeshd.service"}} {
			if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
			}
		}
		if lingerErr != nil {
			fmt.Fprintln(os.Stderr, "note: could not enable lingering; run `sudo loginctl enable-linger "+os.Getenv("USER")+"` so hermeshd keeps running after you log out")
		}
		return nil
	}
	return fmt.Errorf("unsupported platform %s", runtime.GOOS)
}

func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
		os.Remove(launchdPlist())
		return nil
	case "linux":
		userBus()
		exec.Command("systemctl", "--user", "disable", "--now", "hermeshd.service").Run()
		os.Remove(systemdUnit())
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil
	}
	return fmt.Errorf("unsupported platform %s", runtime.GOOS)
}

// Restart restarts the installed service.
func Restart() error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
	case "linux":
		if err := userBus(); err != nil {
			return err
		}
		if out, err := exec.Command("systemctl", "--user", "restart", "hermeshd.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user restart hermeshd: %v: %s", err, out)
		}
		return nil
	}
	return fmt.Errorf("unsupported platform %s", runtime.GOOS)
}

// ErrNoSystemd: this system has no systemd user manager to run the
// service (a container, WSL without systemd).
var ErrNoSystemd = errors.New("this system has no systemd for this user (a container, or WSL). Keep Hermesh running with\n  nohup hermeshd run >/dev/null 2>&1 &\nor make `hermeshd run` the container's command")

// enableLinger turns lingering on for this user, unless it is on already
// (the installer, as root, turned it on).
func enableLinger() error {
	user := os.Getenv("USER")
	if user == "" {
		return errors.New("USER is not set")
	}
	if out, err := exec.Command("loginctl", "show-user", user, "--property=Linger").Output(); err == nil && strings.TrimSpace(string(out)) == "Linger=yes" {
		return nil
	}
	return exec.Command("loginctl", "enable-linger", user).Run()
}

// userBus makes sure systemctl --user can reach the user's manager: after
// su the session has no XDG_RUNTIME_DIR, though lingering started it.
func userBus() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return ErrNoSystemd
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		os.Setenv("XDG_RUNTIME_DIR", fmt.Sprintf("/run/user/%d", os.Getuid()))
	}
	bus := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bus")
	for i := 0; i < 50; i++ { // the manager lingering just started
		if _, err := os.Stat(bus); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if exec.Command("systemctl", "--user", "show-environment").Run() == nil {
		return nil
	}
	return ErrNoSystemd
}

// Running reports whether the installed service is running now.
func Running() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run() == nil
	case "linux":
		if userBus() != nil {
			return false
		}
		return exec.Command("systemctl", "--user", "is-active", "--quiet", "hermeshd.service").Run() == nil
	}
	return false
}

// AtBoot reports whether the installed service starts with the computer,
// with nobody logged in (on Linux: enabled, with lingering on).
func AtBoot() bool {
	switch runtime.GOOS {
	case "darwin":
		return Installed() // a LaunchAgent: at login
	case "linux":
		if userBus() != nil || exec.Command("systemctl", "--user", "is-enabled", "--quiet", "hermeshd.service").Run() != nil {
			return false
		}
		out, err := exec.Command("loginctl", "show-user", os.Getenv("USER"), "--property=Linger").Output()
		return err == nil && strings.TrimSpace(string(out)) == "Linger=yes"
	}
	return false
}

func Installed() bool {
	p := launchdPlist()
	if runtime.GOOS == "linux" {
		p = systemdUnit()
	}
	_, err := os.Stat(p)
	return err == nil
}

// plistText is the launchd job Install writes.
func plistText(bin, dataDir, logDir string, args []string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string>%s<string>--data-dir</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, xmlEscape(bin), plistArgs(args), xmlEscape(dataDir), xmlEscape(filepath.Join(logDir, "hermeshd.log")), xmlEscape(filepath.Join(logDir, "hermeshd.log")))
}

// unitText is the systemd unit Install writes.
func unitText(bin, dataDir string, args []string) string {
	return fmt.Sprintf(`[Unit]
Description=Hermesh daemon
After=network-online.target

[Service]
ExecStart=%s run %s--data-dir %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, bin, unitArgs(args), dataDir)
}

// Supervised reports whether this process is the installed service for
// dataDir, which starts it again when it exits.
func Supervised(dataDir string) bool {
	switch runtime.GOOS {
	case "darwin":
		return os.Getenv("XPC_SERVICE_NAME") == label && InstalledFor(dataDir)
	case "linux":
		return os.Getenv("INVOCATION_ID") != "" && InstalledFor(dataDir)
	}
	return false
}

// InstalledFor reports whether the installed service runs the machine in
// dataDir.
func InstalledFor(dataDir string) bool {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(systemdUnit())
		return err == nil && strings.Contains(string(b), "--data-dir "+dataDir+"\n")
	}
	b, err := os.ReadFile(launchdPlist())
	return err == nil && strings.Contains(string(b), "<string>--data-dir</string><string>"+xmlEscape(dataDir)+"</string>")
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func plistArgs(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString("<string>" + xmlEscape(a) + "</string>")
	}
	return b.String()
}

func unitArgs(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(a + " ")
	}
	return b.String()
}
