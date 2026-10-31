package machine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

// TestAgentDefaults: a Codex session started without a model runs with the
// machine's defaults; one naming a model keeps it, with the default effort
// and speed; a session's process started again keeps what the session
// started with, whatever the defaults are by then. Only Claude Code and
// Codex take defaults, and only Codex a speed.
func TestAgentDefaults(t *testing.T) {
	codex := &recorder{}
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"codex": codex})
	ctx := context.Background()
	set := func(d *identityv1.AgentDefaults) error {
		_, err := h.n.execAdmin(ctx, &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: d}})
		return err
	}
	start := func(model string) string {
		t.Helper()
		s, err := h.s.Start(ctx, h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "codex", Prompt: "say one", Model: model})
		if err != nil {
			t.Fatal(err)
		}
		h.wait(s.SessionId, idle)
		return s.SessionId
	}
	want := func(i int, model, effort string, speed identityv1.AgentSpeed) {
		t.Helper()
		if s := codex.spec(i); s.Model != model || s.Effort != effort || s.Speed != speed {
			t.Fatalf("start %d ran with %q/%q/%v, want %q/%q/%v", i, s.Model, s.Effort, s.Speed, model, effort, speed)
		}
	}

	start("")
	want(0, "", "", identityv1.AgentSpeed_AGENT_SPEED_UNSPECIFIED) // the agent's own configuration

	std := identityv1.AgentSpeed_AGENT_SPEED_STANDARD
	if err := set(&identityv1.AgentDefaults{Agent: "codex", Model: "gpt-5.6-luna", Effort: "low", Speed: std}); err != nil {
		t.Fatal(err)
	}
	for _, a := range h.n.Status().Agents {
		if a.Name == "codex" && a.Defaults.GetModel() != "gpt-5.6-luna" {
			t.Fatalf("status: %v", a)
		}
	}
	id := start("")
	want(1, "gpt-5.6-luna", "low", std)
	start("gpt-6-astra")
	want(2, "gpt-6-astra", "low", std)

	if err := set(&identityv1.AgentDefaults{Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	h.stopProcess(id)
	h.send(id, "again")
	h.wait(id, idle)
	if codex.spec(-1).ResumeRef == "" {
		t.Fatal("not resumed")
	}
	want(-1, "gpt-5.6-luna", "low", std)

	for _, d := range []*identityv1.AgentDefaults{
		{Agent: "fake", Model: "m"},
		{Agent: "claude", Speed: identityv1.AgentSpeed_AGENT_SPEED_FAST},
		{Agent: "codex", Model: "two words"},
		{Agent: "codex", Effort: "High"},
		{Agent: "codex", Speed: 9},
	} {
		if set(d) == nil {
			t.Errorf("%v accepted", d)
		}
	}
}

// TestSessionModel: a session shows the model the agent reported over the
// one Hermesh passed it (an alias), and the effort Hermesh passed over the
// agent's own; both are kept across a restart.
func TestSessionModel(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "hermeshd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Sessions{machineID: "m"}
	for i, c := range []struct {
		row                   sessionRow
		wantModel, wantEffort string
	}{
		{sessionRow{Model: "opus", Effort: "high", AgentModel: "claude-opus-5-5"}, "claude-opus-5-5", "high"},
		{sessionRow{AgentModel: "gpt-6-astra", AgentEffort: "xhigh"}, "gpt-6-astra", "xhigh"},
		{sessionRow{Effort: "low", AgentModel: "gpt-6-astra", AgentEffort: "xhigh"}, "gpt-6-astra", "low"},
		{sessionRow{Model: "sonnet"}, "sonnet", ""}, // not started yet
		{sessionRow{}, "", ""},
	} {
		r := c.row
		r.ID, r.Progress = fmt.Sprint("s", i), &sessionv1.SessionProgress{}
		if err := putSession(st.db, &r); err != nil {
			t.Fatal(err)
		}
		got, err := st.Session(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p := s.toProto(got); p.Model != c.wantModel || p.Effort != c.wantEffort {
			t.Errorf("%+v shows %q/%q, want %q/%q", c.row, p.Model, p.Effort, c.wantModel, c.wantEffort)
		}
	}
}
