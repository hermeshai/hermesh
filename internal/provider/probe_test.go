//go:build unix

package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Each way an agent's check can fail is told apart, with what to show.
func TestProbeTellsWhyAnAgentCannotRun(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, body string
		problem    Problem
		detail     string
	}{
		{"ok", "#!/bin/sh\necho 1.2.3\n", ProblemNone, ""},
		{"fails", "#!/bin/sh\necho 'config.toml: bad value' >&2\nexit 1\n", ProblemFailed, "config.toml: bad value"},
		{"silent", "#!/bin/sh\nexit 3\n", ProblemFailed, "exit status 3"},
		{"forks", "#!/bin/sh\necho \"$0: fork: Resource temporarily unavailable\" >&2\nexit 128\n", ProblemNoProcesses, filepath.Join(dir, "forks") + ": fork: Resource temporarily unavailable"},
		{"node", "#!/usr/bin/env hermesh-no-such-runtime\n", ProblemMissingRuntime, "hermesh-no-such-runtime"},
		{"interpreter", "#!/hermesh/no/such/node\n", ProblemMissingRuntime, "node"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, p, d := Probe(context.Background(), os.Environ(), script(c.name, c.body), "--version")
			if p != c.problem || d != c.detail {
				t.Fatalf("got %v %q (printed %q), want %v %q", p, d, out, c.problem, c.detail)
			}
		})
	}
	t.Run("hangs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, p, _ := Probe(ctx, os.Environ(), script("hangs", "#!/bin/sh\nexec sleep 10\n")); p != ProblemTimedOut {
			t.Fatalf("got %v, want timed out", p)
		}
	})
}

// A script finds its interpreter on the PATH it was found on, as Node CLIs
// installed by npm need.
func TestProbeEnvCarriesThePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hermesh-runtime"), []byte("#!/bin/sh\necho ran\n"), 0o755)
	agent := filepath.Join(dir, "agent")
	os.WriteFile(agent, []byte("#!/usr/bin/env hermesh-runtime\n"), 0o755)
	if out, p, d := Probe(context.Background(), ProbeEnv(dir+":/usr/bin:/bin"), agent, "--version"); p != ProblemNone || string(out) != "ran\n" {
		t.Fatalf("got %v %q %q", p, d, out)
	}
}
