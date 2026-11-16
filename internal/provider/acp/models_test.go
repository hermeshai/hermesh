package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

func TestAntigravityModelDiscovery(t *testing.T) {
	for _, tc := range []struct{ name, options, models, failure, want string }{
		{"unsupported", "", "small,large", "", "session_required"},
		{"supported", "1", "small,large", "", "known"},
		{"empty", "1", "", "", "known"},
		{"auth", "1", "small", "-32000", "auth_required"},
		{"error", "1", "small", "-32603", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GEMINI_HOME", t.TempDir())
			t.Setenv("FAKE_ACP_STATE", t.TempDir())
			t.Setenv("FAKE_ACP_OPTIONS", tc.options)
			t.Setenv("FAKE_ACP_MODELS", tc.models)
			t.Setenv("FAKE_ACP_OPTIONS_ERROR", tc.failure)
			log := filepath.Join(t.TempDir(), "methods")
			t.Setenv("FAKE_ACP_LOG", log)
			p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
			if c := p.ModelCatalogs(); len(c) != 1 || c[0].State != "unknown" {
				t.Fatalf("before discovery: %+v", c)
			}
			if i := p.Discover(context.Background()); !i.Available {
				t.Fatal(i)
			}
			cs := p.ModelCatalogs()
			if len(cs) != 1 || cs[0].State != tc.want || cs[0].Scope != "isolated" || cs[0].ObservedAt.IsZero() {
				t.Fatalf("catalogs: %+v", cs)
			}
			c := cs[0]
			if tc.name == "supported" && (len(c.Models) != 2 || c.Models[1].ID != "large" || c.DefaultModel != "small" || c.Source != "acp_options") {
				t.Fatalf("models: %+v", c)
			}
			if tc.name == "empty" && (len(c.Models) != 0 || c.DefaultModel != "") {
				t.Fatalf("empty: %+v", c)
			}
			before, _ := os.ReadFile(log)
			p.ModelCatalogs()
			p.Discover(context.Background())
			after, _ := os.ReadFile(log)
			if string(before) != string(after) {
				t.Fatalf("fresh catalog re-probed: %s", after)
			}
			want := "initialize\n"
			if tc.options == "1" {
				want += "session/options\n"
			}
			if string(after) != want {
				t.Fatalf("discovery sent unexpected RPCs: %q", after)
			}
			files, _ := os.ReadDir(os.Getenv("FAKE_ACP_STATE"))
			if len(files) != 0 {
				t.Fatal("model discovery created a session")
			}
		})
	}
}

func TestAntigravityModelTimeout(t *testing.T) {
	t.Setenv("GEMINI_HOME", t.TempDir())
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	t.Setenv("FAKE_ACP_OPTIONS", "1")
	t.Setenv("FAKE_ACP_OPTIONS_ERROR", "wait")
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p.discoverModels(ctx, fakeBin, nil, os.Environ())
	if cs := p.ModelCatalogs(); len(cs) != 1 || cs[0].State != "timeout" {
		t.Fatalf("timeout: %+v", cs)
	}
}

func TestAntigravitySessionModelCatalog(t *testing.T) {
	t.Setenv("GEMINI_HOME", t.TempDir())
	f := newFixture(t, "FAKE_ACP_CONFIG=1", "FAKE_ACP_MODELS=small,large")
	f.p.Agent = antigravityAgent("darwin")
	s, err := f.start("say ok", "", "large")
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := collect(t, s)
	ref := of(evs, provider.EventRef)[0].Ref
	catalog := func() provider.ModelCatalog {
		t.Helper()
		cs := f.p.ModelCatalogs()
		if len(cs) != 1 {
			t.Fatalf("catalogs: %+v", cs)
		}
		return cs[0]
	}
	if c := catalog(); c.DefaultModel != "small" || c.CurrentModel != "large" || len(c.Models) != 2 {
		t.Fatalf("override replaced default: %+v", c)
	}
	options := `[{"id":"model","category":"model","type":"select","currentValue":"next","options":[{"value":"next","name":"Next"}]}]`
	if err := s.Send(context.Background(), provider.Input{Text: "model-options " + options + "\nsay changed"}); err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	if c := catalog(); c.DefaultModel != "small" || c.CurrentModel != "next" || len(c.Models) != 1 || c.Models[0].Name != "Next" || c.Source != "acp_update" {
		t.Fatalf("update: %+v", c)
	}
	s.Close()
	// A resumed session's selected model does not become a new-session default.
	f.p = &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	s, err = f.start("say resumed", ref, "")
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	if c := catalog(); c.DefaultModel != "" || c.CurrentModel != "small" {
		t.Fatalf("resume inferred default: %+v", c)
	}
}

func TestAntigravityCatalogExpiryAndIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_HOME", home)
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	t.Setenv("FAKE_ACP_OPTIONS", "1")
	t.Setenv("FAKE_ACP_MODELS", "small")
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	p.Discover(context.Background())
	key := modelContext(fakeBin, os.Environ())
	initial, _ := json.Marshal(map[string]any{"configOptions": []any{map[string]any{"id": "model", "type": "select", "currentValue": "project-only", "options": []any{map[string]any{"value": "project-only"}}}}})
	p.observeModels(key, "one/session-a", "acp_session", initial, true)
	p.observeModels(key, "two/session-b", "acp_session", paramsForOptions(json.RawMessage(`[]`)), true)
	cs := p.ModelCatalogs()
	if len(cs) != 3 || cs[1].DefaultModel != "project-only" || cs[2].DefaultModel != "" || cs[2].State != "unknown" {
		t.Fatalf("scopes mixed: %+v", cs)
	}
	// Returned data cannot mutate the cache.
	cs[0].Models[0].ID = "mutated"
	if p.ModelCatalogs()[0].Models[0].ID != "small" {
		t.Fatal("catalog aliases cache")
	}
	p.modelsMu.Lock()
	v := p.catalogs[key+":isolated"]
	v.value.ExpiresAt = time.Now().Add(-time.Second)
	v.checked = time.Now().Add(-modelCatalogTTL)
	p.catalogs[key+":isolated"] = v
	p.modelsMu.Unlock()
	t.Setenv("FAKE_ACP_OPTIONS_ERROR", "-32603")
	p.Discover(context.Background())
	for range 2 { // Repeated failures must not erase the last observation.
		p.saveCatalog(key, provider.ModelCatalog{Scope: "isolated", State: "error"})
	}
	c := p.ModelCatalogs()[0]
	if c.State != "error" || len(c.Models) != 1 || c.Models[0].ID != "small" || time.Now().Before(c.ExpiresAt) {
		t.Fatalf("expired observation: %+v", c)
	}
	t.Setenv("GEMINI_HOME", t.TempDir())
	if cs := p.ModelCatalogs(); len(cs) != 1 || cs[0].State != "unknown" || len(cs[0].Models) != 0 {
		t.Fatalf("auth contexts mixed: %+v", cs)
	}
	t.Setenv("GEMINI_HOME", home)
	p.Binary = filepath.Join(t.TempDir(), "different-runtime")
	if cs := p.ModelCatalogs(); len(cs) != 1 || cs[0].State != "unknown" {
		t.Fatalf("runtimes mixed: %+v", cs)
	}
}

func TestAntigravityModelResponseOrdering(t *testing.T) {
	t.Setenv("GEMINI_HOME", t.TempDir())
	options := `[{"id":"model","type":"select","currentValue":"newer","options":[{"value":"newer"}]}]`
	f := newFixture(t, "FAKE_ACP_CONFIG=1", "FAKE_ACP_MODELS=small,large", "FAKE_ACP_EAGER_UPDATE="+options)
	f.p.Agent = antigravityAgent("darwin")
	s, err := f.start("say ok", "", "large")
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	if cs := f.p.ModelCatalogs(); len(cs) != 1 || cs[0].CurrentModel != "newer" || cs[0].DefaultModel != "small" {
		t.Fatalf("setter response overwrote the following notification: %+v", cs)
	}
}

func TestModelOptionsStates(t *testing.T) {
	for _, tc := range []struct {
		data, state string
		count       int
	}{
		{`{}`, "unknown", 0},
		{`{"configOptions":[]}`, "unknown", 0},
		{`{"configOptions":[{"id":"model","type":"select","currentValue":"","options":[]}]}`, "known", 0},
		{`{"configOptions":[{"id":"model","type":"select","currentValue":"x"}]}`, "error", 0},
		{`{"configOptions":[{"id":"other","type":"boolean","currentValue":true},{"id":"llm","category":"model","type":"select","currentValue":"x","options":[{"group":"g","options":[{"value":"x"},{"value":"x"}]}]}]}`, "known", 1},
		{`{"configOptions":[{"id":"model","type":"boolean","currentValue":true}]}`, "error", 0},
		{`invalid`, "error", 0},
	} {
		models, _, state := modelOptions(json.RawMessage(tc.data))
		if state != tc.state || len(models) != tc.count {
			t.Fatalf("%s: %s %+v", tc.data, state, models)
		}
	}
	if strings.Contains(modelContext(fakeBin, []string{"GEMINI_HOME=/private/path"}), "/private/path") {
		t.Fatal("context exposes configuration path")
	}
}
