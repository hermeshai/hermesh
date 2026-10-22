// Package shellenv resolves the user's login-shell PATH; launchd and
// systemd start daemons with a minimal PATH that misses user-installed CLIs.
package shellenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	once sync.Once
	path string
)

// Path returns the login-shell PATH merged with the current one (cached).
func Path() string {
	once.Do(func() { path = resolve() })
	return path
}

func resolve() string {
	cur := os.Getenv("PATH")
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sh, "-lic", `printf '__HERMESH_PATH__%s' "$PATH"`).Output()
	var parts []string
	if err == nil {
		if _, p, ok := strings.Cut(string(out), "__HERMESH_PATH__"); ok {
			parts = append(parts, filepath.SplitList(strings.TrimSpace(p))...)
		}
	}
	parts = append(parts, filepath.SplitList(cur)...)
	home, _ := os.UserHomeDir()
	parts = append(parts, filepath.Join(home, ".local/bin"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	seen := map[string]bool{}
	var out2 []string
	for _, p := range parts {
		if p != "" && !seen[p] {
			seen[p] = true
			out2 = append(out2, p)
		}
	}
	return strings.Join(out2, string(filepath.ListSeparator))
}

// Env returns the environment for a child process: the full current
// environment with the resolved PATH.
func Env(tmpDir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "TMPDIR=") {
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+Path())
	if tmpDir != "" {
		env = append(env, "TMPDIR="+tmpDir)
	}
	return env
}
