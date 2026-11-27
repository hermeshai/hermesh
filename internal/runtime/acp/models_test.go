package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func modelsText(ms []runtime.Model) string {
	var parts []string
	for _, m := range ms {
		parts = append(parts, fmt.Sprintf("%s(%s)%s", m.ID, m.Name, strings.Join(m.Efforts, "/")))
	}
	return strings.Join(parts, " ")
}

func runRuntimeTurn(t *testing.T, p *Provider, spec runtime.StartSpec, prompt string) ([]runtime.Event, error) {
	t.Helper()
	cmd := p.resolveForStart(context.Background(), os.Environ())
	s, err := p.startRuntimeSession(context.Background(), cmd, os.Environ(), spec)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := s.StartTurn(context.Background(), runtime.TurnInput{RunID: "r1", Prompt: prompt}); err != nil {
		return nil, err
	}
	var evs []runtime.Event
	timeout := time.After(8 * time.Second)
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for ACP turn")
		case ev, ok := <-s.Events():
			if !ok {
				return evs, nil
			}
			evs = append(evs, ev)
			if ev.Kind == runtime.EventTurnCompleted || ev.Kind == runtime.EventTurnFailed || ev.Kind == runtime.EventTurnInterrupted {
				return evs, nil
			}
		}
	}
}

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

func TestAntigravityNotSignedIn(t *testing.T) {
	t.Setenv("GEMINI_HOME", t.TempDir())
	log := filepath.Join(t.TempDir(), "methods")
	t.Setenv("FAKE_ACP_LOG", log)
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	if i := p.Discover(context.Background()); i.LoggedIn || i.ModelsState != runtime.ModelsAuthRequired || len(i.Models) != 0 {
		t.Fatalf("info: %+v", i)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("started: %s", b)
	}
}

func TestSessionModelAndEffort(t *testing.T) {
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	t.Setenv("FAKE_ACP_CONFIG", "1")
	t.Setenv("FAKE_ACP_MODELS", "small,large")
	t.Setenv("FAKE_ACP_EFFORTS", "small=low,high;large=low,high,xhigh")
	p := &Provider{Agent: Agent{Name: "fake", Command: "fakeacp"}, Binary: fakeBin}

	start := func(model, effort string) ([]runtime.Event, error) {
		t.Helper()
		sel := &sessionv1.ModelSelection{Model: model, Options: map[string]*sessionv1.OptionValue{}}
		if effort != "" {
			sel.Options["reasoningEffort"] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: effort}}
		}
		return runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir(), Selection: sel}, "settings")
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
		var native runtime.Event
		var msg string
		for _, ev := range evs {
			if ev.Kind == runtime.EventNativeSession {
				native = ev
			}
			if ev.Kind == runtime.EventAssistantMessage {
				msg = ev.Text
			}
		}
		if native.Model+" "+native.Effort != c.runs || !strings.Contains(msg, "settings: "+c.runs) {
			t.Fatalf("%+v: native %+v, msg %q", c, native, msg)
		}
	}
	// UI/manifest "thought_level" and CLI "effort" option keys must also apply initial effort cleanly.
	for _, optKey := range []string{"thought_level", "effort"} {
		evs, err := runRuntimeTurn(t, p, runtime.StartSpec{
			Workdir: t.TempDir(),
			Selection: &sessionv1.ModelSelection{
				Model: "large",
				Options: map[string]*sessionv1.OptionValue{
					optKey:  {Kind: &sessionv1.OptionValue_StringValue{StringValue: "low"}},
					"speed": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "fast"}},
				},
			},
		}, "settings")
		if err != nil {
			t.Fatalf("option key %s: %v", optKey, err)
		}
		var got string
		for _, ev := range evs {
			if ev.Kind == runtime.EventAssistantMessage {
				got = ev.Text
			}
		}
		if !strings.Contains(got, "settings: large low") {
			t.Fatalf("option key %s: got %q, want large low", optKey, got)
		}
	}
	for _, c := range []struct{ model, effort, refusal string }{
		{"huge", "", `model "huge": not offered by the agent (available: small, large)`},
		{"small", "xhigh", `reasoning effort "xhigh": not offered by the agent (available: low, high)`},
	} {
		_, err := start(c.model, c.effort)
		if err == nil || runtime.FailureOf(err, 0).Kind != sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE || !strings.Contains(err.Error(), c.refusal) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	l, fresh := p.models(modelContext(fakeBin, os.Environ()))
	if !fresh || modelsText(l.models) != "small(SMALL)low/high large(LARGE)low/high/xhigh" || l.own.Model+" "+l.own.Effort != "small high" {
		t.Fatalf("learned: %+v", l)
	}
}

func TestSessionLegacyModel(t *testing.T) {
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	t.Setenv("FAKE_ACP_LEGACY", "1")
	t.Setenv("FAKE_ACP_MODELS", "a,b")
	p := &Provider{Agent: Agent{Name: "fake", Command: "fakeacp"}, Binary: fakeBin}

	evs, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir(), Selection: &sessionv1.ModelSelection{Model: "b"}}, "settings")
	if err != nil {
		t.Fatal(err)
	}
	var native runtime.Event
	var msg string
	for _, ev := range evs {
		if ev.Kind == runtime.EventNativeSession {
			native = ev
		}
		if ev.Kind == runtime.EventAssistantMessage {
			msg = ev.Text
		}
	}
	if native.Model != "b" || !strings.Contains(msg, "settings: b") {
		t.Fatalf("events: %+v", evs)
	}
	if _, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir(), Selection: &sessionv1.ModelSelection{Model: "c"}}, "settings"); err == nil || !strings.Contains(err.Error(), "available: a, b") {
		t.Fatal(err)
	}
	_, err = runRuntimeTurn(t, p, runtime.StartSpec{
		Workdir: t.TempDir(),
		Selection: &sessionv1.ModelSelection{
			Options: map[string]*sessionv1.OptionValue{
				"reasoningEffort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "high"}},
			},
		},
	}, "settings")
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
