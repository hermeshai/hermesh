//go:build unix

package shellenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// tempVars name the temp folder in a child's environment.
var tempVars = []string{"TMPDIR"}

func sameName(a, b string) bool { return a == b }

// Source says where the PATH comes from, for diagnostics.
func Source() string { return shell() + " -lic" }

func shell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// resolve reads the login shell's PATH: the merged PATH, why the shell's
// could not be read, and whether that was for want of processes.
func resolve() (string, string, bool) {
	cur := os.Getenv("PATH")
	sh := shell()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-lic", `printf '__HERMESH_PATH__%s' "$PATH"`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var parts []string
	problem, short := "", false
	if _, p, ok := strings.Cut(string(out), "__HERMESH_PATH__"); err == nil && ok {
		parts = append(parts, filepath.SplitList(strings.TrimSpace(p))...)
	} else {
		problem = describe(ctx, sh, err, stderr.String())
		short = errors.Is(err, syscall.EAGAIN) || strings.Contains(stderr.String(), "fork failed")
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
	return strings.Join(out2, string(filepath.ListSeparator)), problem, short
}

func describe(ctx context.Context, sh string, err error, stderr string) string {
	shell := filepath.Base(sh) + " -lic"
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("%s did not finish within %d s", shell, int(timeout.Seconds()))
	case err == nil:
		return shell + " printed no PATH"
	}
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return fmt.Sprintf("%s failed: %s", shell, l)
		}
	}
	return fmt.Sprintf("%s failed: %v", shell, err)
}
