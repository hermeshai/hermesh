package machine

import (
	"cmp"
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
)

// TestAgentDefaults: a Codex session started without a model runs with the
// machine's defaults; one naming a model keeps it, with the default effort
// and speed; a session's process started again keeps what the session
// started with, whatever the defaults are by then. Every coding agent takes
// defaults (an unknown one none), and only Codex a speed.
func TestAgentDefaults(t *testing.T) {
	codex := newRecorder("codex")
	h := newHarnessWith(t, DefaultLimits(), map[string]rt.Driver{"codex": codex})
	ctx := context.Background()
	set := func(d *identityv1.AgentDefaults) error {
		_, err := h.n.execAdmin(ctx, &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: d}})
		return err
	}
	start := func(model string) string {
		t.Helper()
		res, err := h.e.StartSession(ctx, h.kp(), &sessionv1.StartSessionRequest{
			ProjectId:      "demo",
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "codex", Model: model},
			InitialPrompt:  "say one",
		})
		if err != nil {
			t.Fatal(err)
		}
		h.wait(res.Session.SessionId, idle)
		return res.Session.SessionId
	}
	want := func(i int, model, effort string, speed identityv1.AgentSpeed) {
		t.Helper()
		if s := codex.spec(i); s.Model != model || s.Effort != effort || s.Speed != speed {
			t.Fatalf("start %d ran with %q/%q/%v, want %q/%q/%v", i, s.Model, s.Effort, s.Speed, model, effort, speed)
		}
	}

	start("")
	want(0, "", "", identityv1.AgentSpeed_AGENT_SPEED_UNSPECIFIED)

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

	if err := set(&identityv1.AgentDefaults{Agent: "antigravity", Model: "gemini-4-neon", Effort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*identityv1.AgentDefaults{
		{Agent: "fake", Model: "m"},
		{Agent: "antigravity", Speed: identityv1.AgentSpeed_AGENT_SPEED_FAST},
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

// catalog is an agent that lists every model it takes, as Codex does.
type catalog struct{ recorder }

func (c *catalog) Discover(ctx context.Context) rt.Info {
	i := c.recorder.Discover(ctx)
	i.Models = []rt.Model{
		{ID: "gpt-big", Efforts: []string{"low", "high", "xhigh"}},
		{ID: "gpt-small", Efforts: []string{"low", "high"}},
	}
	i.AllModels = true
	i.ModelsState = rt.ModelsKnown
	i.ModelsAt = time.Now()
	return i
}

func TestModelAndEffort(t *testing.T) {
	codex := &catalog{recorder: *newRecorder("codex")}
	h := newHarnessWith(t, DefaultLimits(), map[string]rt.Driver{"codex": codex})
	ctx := context.Background()
	start := func(agent, model, effort string) (*sessionv1.Session, error) {
		var opts map[string]*sessionv1.OptionValue
		if effort != "" {
			opts = map[string]*sessionv1.OptionValue{
				"effort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: effort}},
			}
		}
		res, err := h.e.StartSession(ctx, h.kp(), &sessionv1.StartSessionRequest{
			ProjectId:      "demo",
			ModelSelection: &sessionv1.ModelSelection{InstanceId: agent, Model: model, Options: opts},
			InitialPrompt:  "say one",
		})
		if err != nil {
			return nil, err
		}
		return res.Session, nil
	}
	for _, c := range []struct{ agent, model, effort, refusal string }{
		{"codex", "aurora", "", `codex on this machine has no model "aurora" (one of: gpt-big, gpt-small)`},
		{"codex", "gpt-small", "xhigh", `codex's model gpt-small has no reasoning effort "xhigh" (one of: low, high)`},
		{"codex", "", "max", `codex has no reasoning effort "max" (one of: low, high, xhigh)`},
		{"fake", "", "high", "fake on this machine takes no reasoning effort"},
	} {
		if _, err := start(c.agent, c.model, c.effort); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.refusal) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if all, _ := h.e.ListAllSessions(true); len(all) != 0 {
		t.Fatal("a session was made for a refused model or effort")
	}
	for _, c := range []struct{ agent, model, effort string }{{"codex", "gpt-small", "high"}, {"codex", "", "xhigh"}, {"fake", "aurora", ""}} {
		s, err := start(c.agent, c.model, c.effort)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		h.wait(s.SessionId, idle)
	}
	for i, effort := range []string{"high", "xhigh"} {
		if s := codex.spec(i); s.Effort != effort {
			t.Fatalf("start %d ran with effort %q, want %q", i, s.Effort, effort)
		}
	}

	if _, err := h.n.execAdmin(ctx, &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetAgentDefaults{
		SetAgentDefaults: &identityv1.AgentDefaults{Agent: "codex", Model: "gpt-big", Effort: "xhigh"}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ model, effort string }{{"", "xhigh"}, {"gpt-small", ""}, {"gpt-big", "xhigh"}} {
		s, err := start("codex", c.model, "")
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		h.wait(s.SessionId, idle)
		if got := codex.spec(-1); got.Model != cmp.Or(c.model, "gpt-big") || got.Effort != c.effort {
			t.Fatalf("%+v ran with %q/%q", c, got.Model, got.Effort)
		}
	}
}
