// Package service installs hermeshd as a per-user background service: a
// launchd LaunchAgent on macOS, a systemd user unit on Linux, a Task
// Scheduler logon task on Windows.
package service

import (
	"errors"
	"strings"
)

const label = "com.hermesh.daemon"

// ErrNoSystemd: this system has no systemd user manager to run the
// service (a container, WSL without systemd).
var ErrNoSystemd = errors.New("this system has no systemd for this user (a container, or WSL). Keep Hermesh running with\n  nohup hermeshd run >/dev/null 2>&1 &\nor make `hermeshd run` the container's command")

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
