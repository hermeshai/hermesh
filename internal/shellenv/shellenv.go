// Package shellenv resolves the PATH and environment a new terminal of the
// user would have: the login shell's on Unix, where launchd and systemd
// start daemons with a minimal PATH that misses user-installed CLIs, and
// none of what ~/.profile exports; the registry's PATH on Windows.
package shellenv

import (
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// timeout bounds the login shell; retryAfter is how long a failed read is
// kept before the next call reads again.
const (
	timeout    = 10 * time.Second
	retryAfter = time.Minute
)

var (
	mu      sync.Mutex
	path    string
	login   []string // the login shell's variables but PATH
	problem string   // why the login shell's PATH could not be read; "" once it was
	readAt  time.Time
)

// Path returns the login-shell PATH merged with the current one. A read
// that worked is kept; one that failed is tried again a minute later, and
// meanwhile the current PATH and the usual install directories are used.
// A shell that could not start for want of processes is no fault of the
// shell's: it is tried again on the next call.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	if path != "" && (problem == "" || time.Since(readAt) < retryAfter) {
		return path
	}
	p, env, why, short := resolve()
	if short {
		return p
	}
	path, problem, readAt = p, why, time.Now()
	if why == "" {
		login = env
	}
	return path
}

// LastProblem says why the login shell's PATH could not be read the last
// time it was tried, or "" when it was (or was not tried yet); it never
// starts the shell.
func LastProblem() string {
	mu.Lock()
	defer mu.Unlock()
	return problem
}

// Environ is a new terminal's environment: this process's, then the login
// shell's variables it lacks (CLAUDE_CONFIG_DIR, ANTHROPIC_BASE_URL, a
// proxy), with the resolved PATH.
func Environ() []string {
	p := Path()
	mu.Lock()
	extra := login
	mu.Unlock()
	env := Without(os.Environ(), "PATH")
	have := map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		have[name] = true
	}
	for _, kv := range extra {
		if name, _, _ := strings.Cut(kv, "="); !have[name] {
			env = append(env, kv)
		}
	}
	return append(env, "PATH="+p)
}

// Env returns the environment for a child process: a new terminal's (see
// Environ), and tmpDir as its temp folder.
func Env(tmpDir string) []string {
	env := Without(Environ(), tempVars...)
	if tmpDir != "" {
		for _, v := range tempVars {
			env = append(env, v+"="+tmpDir)
		}
	}
	return env
}

// Without is env without the variables named (on Windows, whatever their
// case: "Path" is PATH there).
func Without(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(names, func(n string) bool { return sameName(name, n) }) {
			out = append(out, kv)
		}
	}
	return out
}
