// Package service installs hermeshd as a per-user background service
// (launchd LaunchAgent on macOS, systemd user unit on Linux).
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// Install writes and loads the service definition running `bin run`.
func Install(bin, dataDir, logDir string) error {
	switch runtime.GOOS {
	case "darwin":
		os.MkdirAll(logDir, 0o700)
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string><string>--data-dir</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, xmlEscape(bin), xmlEscape(dataDir), xmlEscape(filepath.Join(logDir, "hermeshd.log")), xmlEscape(filepath.Join(logDir, "hermeshd.log")))
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
		unit := fmt.Sprintf(`[Unit]
Description=Hermesh daemon
After=network-online.target

[Service]
ExecStart=%s run --data-dir %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, bin, dataDir)
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
		if user := os.Getenv("USER"); user != "" {
			if err := exec.Command("loginctl", "enable-linger", user).Run(); err != nil {
				fmt.Fprintln(os.Stderr, "note: could not enable lingering; run `sudo loginctl enable-linger "+user+"` so hermeshd keeps running after you log out")
			}
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
		return exec.Command("systemctl", "--user", "restart", "hermeshd.service").Run()
	}
	return fmt.Errorf("unsupported platform %s", runtime.GOOS)
}

func Installed() bool {
	p := launchdPlist()
	if runtime.GOOS == "linux" {
		p = systemdUnit()
	}
	_, err := os.Stat(p)
	return err == nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
