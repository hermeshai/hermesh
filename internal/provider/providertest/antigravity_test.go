package providertest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/acp"
)

// TestAntigravityPermissions tests the real Google server, not just its advertised
// capabilities: a denied edit cannot reach disk, shell input reaches the gate,
// and a running turn can be cancelled without destroying the session.
func TestAntigravityPermissions(t *testing.T) {
	if os.Getenv("HERMESH_REAL_CLI") != "1" {
		t.Skip("set HERMESH_REAL_CLI=1")
	}
	var p *acp.Provider
	for _, candidate := range acp.Providers("") {
		if candidate.Name() == "antigravity" {
			p = candidate
		}
	}
	if p == nil {
		t.Fatal("Antigravity not registered")
	}
	if i := p.Discover(context.Background()); !i.Available || !i.LoggedIn {
		t.Skipf("Antigravity not ready: %+v", i)
	}
	dir := gitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	s, err := p.Start(ctx, provider.StartSpec{WorkDir: dir, TempDir: t.TempDir(), Env: os.Environ(),
		Prompt: "Try to create denied.txt containing blocked. If permission is denied, stop and report it; do not retry or use another tool.",
		Gate: func(_ context.Context, a provider.ToolAction) provider.Decision {
			return provider.Decision{Allow: a.Kind == provider.ToolShell, Reason: "file edits denied by test"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := collect(t, s, 90*time.Second)
	denied := false
	for _, e := range tr.events {
		if e.Kind == provider.EventToolCall && e.ToolKind == provider.ToolEdit && e.Decision != nil && !e.Decision.Allow {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("no denied edit: %+v", tr.events)
	}
	if _, err := os.Stat(filepath.Join(dir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied file: %v", err)
	}
	if err := s.Send(ctx, provider.Input{Text: "Use the terminal tool to execute sleep 90, then reply done. Do not edit files."}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(90 * time.Second)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("session exited before shell command")
			}
			if e.Kind == provider.EventToolCall && e.ToolKind == provider.ToolShell && strings.Contains(e.Summary, "sleep") {
				if err := s.Interrupt(ctx); err != nil {
					t.Fatal(err)
				}
				tr = collect(t, s, 30*time.Second)
				if !tr.result.IsError || tr.result.Text != "interrupted" {
					t.Fatalf("cancel: %+v", tr.result)
				}
				if err := s.Send(ctx, provider.Input{Text: "Reply with exactly alive. Do not use tools."}); err != nil {
					t.Fatal(err)
				}
				tr = collect(t, s, 60*time.Second)
				if tr.result.IsError || !strings.Contains(tr.result.Text, "alive") {
					t.Fatalf("after cancel: %+v", tr.result)
				}
				return
			}
			if e.Kind == provider.EventTurnCompleted {
				t.Fatalf("no shell call before completion: %+v", e)
			}
		case <-deadline:
			t.Fatal("no shell call")
		}
	}
}
