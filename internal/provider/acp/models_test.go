package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

func modelsText(ms []provider.Model) string {
	var parts []string
	for _, m := range ms {
		parts = append(parts, fmt.Sprintf("%s(%s)%s", m.ID, m.Name, strings.Join(m.Efforts, "/")))
	}
	return strings.Join(parts, " ")
}

// TestModelDiscovery: an ACP agent's models, read without a prompt from
// session/options when it offers it, else from a session it is never asked
// anything in (each model selected once for its efforts, then closed when it
// can be), else from the older models state. A list read stands for a day;
// what was not read is tried again at the next check.
func TestModelDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     []string
		state   string
		models  string
		own     string
		methods string
	}{
		{"options", []string{"FAKE_ACP_OPTIONS=1", "FAKE_ACP_MODELS=small,large"}, "known", "small(SMALL) large(LARGE)", "small ",
			"initialize session/options"},
		{"session", []string{"FAKE_ACP_CONFIG=1", "FAKE_ACP_MODELS=small,large", "FAKE_ACP_EFFORTS=small=low,high;large=low,high,xhigh", "FAKE_ACP_CLOSE=1"},
			"known", "small(SMALL)low/high large(LARGE)low/high/xhigh", "small high",
			"initialize session/new session/set_config_option session/close"},
		{"unclosable", []string{"FAKE_ACP_CONFIG=1", "FAKE_ACP_MODELS=small"}, "known", "small(SMALL)", "small ", "initialize session/new"},
		{"legacy", []string{"FAKE_ACP_LEGACY=1", "FAKE_ACP_MODELS=a,b"}, "known", "a(A) b(B)", "a ", "initialize session/new"},
		{"unsupported", nil, "unsupported", "", " ", "initialize session/new"},
		{"auth", []string{"FAKE_ACP_AUTH=login"}, "auth_required", "", " ", "initialize session/new"},
		{"error", []string{"FAKE_ACP_OPTIONS=1", "FAKE_ACP_OPTIONS_ERROR=-32603"}, "error", "", " ", "initialize session/options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"FAKE_ACP_OPTIONS", "FAKE_ACP_CONFIG", "FAKE_ACP_MODELS", "FAKE_ACP_EFFORTS", "FAKE_ACP_CLOSE", "FAKE_ACP_LEGACY", "FAKE_ACP_AUTH", "FAKE_ACP_OPTIONS_ERROR"} {
				t.Setenv(k, "")
			}
			for _, kv := range tc.env {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			t.Setenv("FAKE_ACP_STATE", t.TempDir())
			log := filepath.Join(t.TempDir(), "methods")
			t.Setenv("FAKE_ACP_LOG", log)
			p := &Provider{Agent: Agent{Name: "fake", Command: "fakeacp"}, Binary: fakeBin}
			i := p.Discover(context.Background())
			if !i.Available || i.ModelsState != tc.state || modelsText(i.Models) != tc.models || i.Own.Model+" "+i.Own.Effort != tc.own ||
				i.ModelsAt.IsZero() || i.AllModels != (tc.models != "") || i.LoggedIn != (tc.state != "auth_required") {
				t.Fatalf("info: %+v", i)
			}
			methods := func() string {
				b, _ := os.ReadFile(log)
				return strings.Join(strings.Fields(strings.ReplaceAll(string(b), "session/set_config_option\nsession/set_config_option", "session/set_config_option")), " ")
			}
			if got := methods(); got != tc.methods {
				t.Fatalf("methods %q, want %q", got, tc.methods)
			}
			again := p.Discover(context.Background())
			if read := methods() != tc.methods; read != (tc.state == "auth_required" || tc.state == "error") {
				t.Fatalf("checked again (read again: %v): %q", read, methods())
			}
			if again.ModelsState != tc.state || modelsText(again.Models) != tc.models {
				t.Fatalf("again: %+v", again)
			}
		})
	}
}

// TestAntigravityNotSignedIn: an Antigravity not signed in needs it to tell
// its models, and is not started to ask.
func TestAntigravityNotSignedIn(t *testing.T) {
	t.Setenv("GEMINI_HOME", t.TempDir())
	log := filepath.Join(t.TempDir(), "methods")
	t.Setenv("FAKE_ACP_LOG", log)
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	if i := p.Discover(context.Background()); i.LoggedIn || i.ModelsState != provider.ModelsAuthRequired || len(i.Models) != 0 {
		t.Fatalf("info: %+v", i)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("started: %s", b)
	}
}

// TestSessionModelAndEffort: a session runs with the model and reasoning
// effort asked for, set through its config options, and says what it runs
// with; one the agent does not offer fails the start, naming those it does.
// A new session's options refresh the models listed.
func TestSessionModelAndEffort(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_CONFIG=1", "FAKE_ACP_MODELS=small,large", "FAKE_ACP_EFFORTS=small=low,high;large=low,high,xhigh")
	start := func(model, effort string) ([]provider.Event, error) {
		t.Helper()
		s, err := f.p.Start(context.Background(), provider.StartSpec{WorkDir: f.dir, TempDir: t.TempDir(), Prompt: "settings", Model: model, Effort: effort, Env: f.env, Gate: f.gate.gate})
		if err != nil {
			return nil, err
		}
		defer s.Close()
		evs, _ := collect(t, s)
		return evs, nil
	}
	for _, c := range []struct{ model, effort, runs string }{
		{"", "", "small high"},
		{"large", "xhigh", "large xhigh"},
		{"large", "", "large high"},
		{"", "low", "small low"},
	} {
		evs, err := start(c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		ref := of(evs, provider.EventRef)[0]
		if ref.Model+" "+ref.Effort != c.runs || !strings.Contains(of(evs, provider.EventAgentMessage)[0].Text, "settings: "+c.runs) {
			t.Fatalf("%+v: ref %+v, events %+v", c, ref, evs)
		}
	}
	for _, c := range []struct{ model, effort, refusal string }{
		{"huge", "", `model "huge": not offered by the agent (available: small, large)`},
		{"small", "xhigh", `reasoning effort "xhigh": not offered by the agent (available: low, high)`},
	} {
		_, err := start(c.model, c.effort)
		if provider.FailureOf(err, 0).Kind != sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE || !strings.Contains(err.Error(), c.refusal) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	l, fresh := f.p.models(modelContext(fakeBin, f.env))
	if !fresh || modelsText(l.models) != "small(SMALL)low/high large(LARGE)low/high/xhigh" || l.own.Model+" "+l.own.Effort != "small high" {
		t.Fatalf("learned: %+v", l)
	}
}

// TestSessionLegacyModel: an agent with the older models state takes a
// model it lists, through session/set_model, and no reasoning effort.
func TestSessionLegacyModel(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_LEGACY=1", "FAKE_ACP_MODELS=a,b")
	s, err := f.start("settings", "", "b")
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := collect(t, s)
	if ref := of(evs, provider.EventRef)[0]; ref.Model != "b" || !strings.Contains(of(evs, provider.EventAgentMessage)[0].Text, "settings: b") {
		t.Fatalf("events: %+v", evs)
	}
	if _, err := f.start("settings", "", "c"); err == nil || !strings.Contains(err.Error(), "available: a, b") {
		t.Fatal(err)
	}
	_, err = f.p.Start(context.Background(), provider.StartSpec{WorkDir: f.dir, TempDir: t.TempDir(), Prompt: "settings", Effort: "high", Env: f.env, Gate: f.gate.gate})
	if err == nil || !strings.Contains(err.Error(), "takes no reasoning effort") {
		t.Fatal(err)
	}
}

func TestOptionsOf(t *testing.T) {
	for _, tc := range []struct {
		data, models, own string
		ok                bool
	}{
		{`{}`, "", " ", false},
		{`{"configOptions":[]}`, "", " ", false},
		{`invalid`, "", " ", false},
		{`{"configOptions":[{"id":"model","type":"boolean","currentValue":true}]}`, "", " ", false},
		{`{"configOptions":[{"id":"model","type":"select","currentValue":"","options":[]}]}`, "", " ", true},
		{`{"configOptions":[{"id":"other","type":"boolean","currentValue":true},{"id":"llm","category":"model","type":"select","currentValue":"x","options":[{"group":"g","options":[{"value":"x","name":"X"},{"value":"x"},{"value":"y"}]}]},{"id":"t","category":"thought_level","type":"select","currentValue":"low","options":[{"value":"low"},{"value":"max"}]}]}`,
			"x(X)low/max y()", "x low", true},
		{`{"models":{"availableModels":[{"modelId":"m","name":"M"}],"currentModelId":"m"}}`, "m(M)", "m ", true},
	} {
		models, own, ok := optionsOf(json.RawMessage(tc.data)).list()
		if ok != tc.ok || modelsText(models) != tc.models || own.Model+" "+own.Effort != tc.own {
			t.Errorf("%s: %v %s %+v", tc.data, ok, modelsText(models), own)
		}
	}
	if strings.Contains(modelContext(fakeBin, []string{"GEMINI_HOME=/private/path"}), "/private/path") {
		t.Fatal("context exposes configuration path")
	}
}
