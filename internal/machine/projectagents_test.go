package machine

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// A project set up for some coding agents takes sessions of those only;
// one with none takes all.
func TestProjectAgents(t *testing.T) {
	h := newHarness(t, Limits{})
	start := func() error {
		sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: "hi"})
		if err == nil {
			// Let its worktree settle before the test's directories go.
			h.wait(sess.SessionId, idle)
		}
		return err
	}
	p, err := h.s.st.Project("demo")
	if err != nil || p == nil || len(p.Agents) != 0 {
		t.Fatalf("project %+v, %v", p, err)
	}

	p.Agents = []string{"claude", "codex"}
	if err := h.s.st.PutProject(*p); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.s.st.Project("demo"); !slices.Equal(got.Agents, []string{"claude", "codex"}) {
		t.Fatalf("stored agents %q", got.Agents)
	}
	if err := start(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a session of an agent the project is not for: %v", err)
	}

	p.Agents = []string{"fake"}
	if err := h.s.st.PutProject(*p); err != nil {
		t.Fatal(err)
	}
	if err := start(); err != nil {
		t.Fatalf("a session of an agent the project is for: %v", err)
	}
}

func TestProjectAgentsAreKnownNames(t *testing.T) {
	if got, err := projectAgents([]string{"codex", "claude", "codex"}); err != nil || !slices.Equal(got, []string{"codex", "claude"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := projectAgents(nil); err != nil || got != nil {
		t.Fatalf("none: %q, %v", got, err)
	}
	if _, err := projectAgents([]string{"claude", "vim"}); err == nil {
		t.Fatal("an unknown agent was taken")
	}
}
