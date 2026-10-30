package machine

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

// Diagnostics describes this machine for a person to send when something
// does not work: version, health, the PATH agents are looked for on and
// what each coding agent's last check found. It starts no process.
func (l *Local) Diagnostics() string {
	n := l.n
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("Hermesh %s · %s/%s · machine %q (%s)", Version, runtime.GOOS, runtime.GOARCH, n.Name(), n.machineID)
	w("time         %s", time.Now().Format("2006-01-02 15:04:05 -0700"))
	if n.Connected() {
		w("relay        connected, %s", n.RelayURL())
	} else {
		w("relay        not connected, %s", n.RelayURL())
	}

	w("\nhealth")
	if pc, err := userProcesses(); err != nil {
		w("  processes  unknown: %v", err)
	} else {
		line := fmt.Sprintf("  processes  %d / %d", pc.used, pc.limit)
		if pc.culpritCount > 0 {
			line += fmt.Sprintf(" · %d ended, not reaped, under %s", pc.culpritCount, pc.culprit)
		}
		w("%s", line)
	}
	if free, size, ok := diskSpace(n.cfg.DataDir); ok {
		w("  disk       %s free of %s (%s)", gb(free), gb(size), n.cfg.DataDir)
	}
	if open, limit, ok := openFiles(); ok {
		w("  open files %d / %d", open, limit)
	}
	if why := shellenv.LastProblem(); why != "" {
		w("  shell PATH %s", why)
	} else {
		w("  shell PATH read from %s -lic", os.Getenv("SHELL"))
	}
	w("\nPATH\n  %s", shellenv.Path())

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.checked.IsZero() {
		w("\ncoding agents (not checked yet)")
	} else {
		w("\ncoding agents (checked %s)", n.checked.Format("15:04:05"))
	}
	for _, name := range AgentNames() {
		i, ok := n.infos[name]
		switch {
		case !ok:
			w("  %-10s not checked", name)
		case i.Available:
			login := "not signed in"
			if i.LoggedIn {
				login = "signed in"
			}
			w("  %-10s %s · %s · %s", name, i.Version, login, i.Binary)
		case i.Problem == provider.ProblemNotFound:
			w("  %-10s not found", name)
		default:
			w("  %-10s %s · %s: %s", name, problemName(i.Problem), i.Binary, i.Detail)
		}
	}
	return b.String()
}

func problemName(p provider.Problem) string {
	switch p {
	case provider.ProblemAppWithoutCLI:
		return "app without its CLI"
	case provider.ProblemNoProcesses:
		return "cannot run (out of processes)"
	case provider.ProblemTimedOut:
		return "cannot run (timed out)"
	case provider.ProblemMissingRuntime:
		return "cannot run (runtime missing)"
	}
	return "cannot run"
}

func gb(bytes uint64) string { return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30)) }
