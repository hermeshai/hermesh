package main

import (
	"bytes"
	"strings"
	"testing"

	rt "github.com/hermeshai/hermesh/internal/runtime"
)

func TestReportDoctorAgents(t *testing.T) {
	t.Run("one signed-in coding agent is enough", func(t *testing.T) {
		var out bytes.Buffer
		ok := reportDoctorAgents(&out, map[string]rt.Info{
			"claude": {Available: true, Version: "2.1.0", LoggedIn: true},
		}, false)
		if !ok {
			t.Fatalf("expected healthy when claude is signed in:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "  claude     2.1.0, signed in\n") || !strings.Contains(out.String(), "  codex      not installed:") {
			t.Fatalf("unexpected output:\n%s", out.String())
		}
	})

	t.Run("acp provider signed in satisfies doctor", func(t *testing.T) {
		var out bytes.Buffer
		ok := reportDoctorAgents(&out, map[string]rt.Info{
			"opencode": {Available: true, Version: "2.4.2", Binary: "/home/dev/.local/share/hermesh/providers/bin/opencode", LoggedIn: true},
		}, false)
		if !ok {
			t.Fatalf("expected healthy when opencode ACP is signed in:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "  opencode   2.4.2 at /home/dev/.local/share/hermesh/providers/bin/opencode, signed in\n") {
			t.Fatalf("unexpected output:\n%s", out.String())
		}
	})

	t.Run("acp provider not signed in fails when nothing else is ready", func(t *testing.T) {
		var out bytes.Buffer
		ok := reportDoctorAgents(&out, map[string]rt.Info{
			"antigravity": {Available: true, Version: "1.5.9", Binary: "/usr/local/bin/agy", LoggedIn: false},
		}, false)
		if ok {
			t.Fatalf("expected unhealthy when no provider is signed in:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "NOT SIGNED IN → run `hermesh agent login antigravity` once as this user") {
			t.Fatalf("unexpected output:\n%s", out.String())
		}
	})

	t.Run("her enabled satisfies doctor without coding agents", func(t *testing.T) {
		var out bytes.Buffer
		if !reportDoctorAgents(&out, nil, true) {
			t.Fatalf("expected healthy when Her is enabled:\n%s", out.String())
		}
	})
}
