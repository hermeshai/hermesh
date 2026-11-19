package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// antigravityEnv is a daemon's environment with fakeacp as a signed-in
// Antigravity offering small (low, high) and large (low, high, xhigh), its
// RPCs logged to the file returned.
func antigravityEnv(t *testing.T) ([]string, string) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "antigravity-acp"), 0o700)
	os.WriteFile(filepath.Join(home, "antigravity-acp", "settings.json"), []byte(`{"auth":{"type":"oauth-personal"}}`), 0o600)
	log := filepath.Join(t.TempDir(), "methods")
	return []string{"GEMINI_HOME=" + home, "FAKE_ACP_STATE=" + t.TempDir(), "FAKE_ACP_LOG=" + log,
		"HERMESH_ANTIGRAVITY_BIN=" + filepath.Join(binaries(t), "fakeacp"), "FAKE_ACP_CONFIG=1", "FAKE_ACP_CLOSE=1",
		"FAKE_ACP_MODELS=small,large", "FAKE_ACP_EFFORTS=small=low,high;large=low,high,xhigh"}, log
}

// TestAntigravityModelDefaults: an ACP agent (Antigravity) lists the models
// it offers, each with its reasoning efforts, read without a prompt, for
// agents (agent list, coding_agent_list) and the app; the owner sets its
// defaults on the machine; a session started without a model runs with
// them, one naming a model or effort with those, as the agent answered;
// a default effort the model named does not take follows the agent's own;
// a model or effort it does not offer is refused before a session starts.
// All through the real relay and daemon, with fakeacp: no Google requests.
func TestAntigravityModelDefaults(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env, log := antigravityEnv(t)
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"antigravity"}})
	ctx := context.Background()

	type listed struct {
		Agent         string
		DefaultModel  string `json:"default_model"`
		DefaultEffort string `json:"default_effort"`
		ModelsState   string `json:"models_state"`
		ObservedAt    int64  `json:"models_observed_at_unix_ms"`
		Models        []struct {
			ID, Name string
			Efforts  []string
		}
	}
	list := func() listed {
		t.Helper()
		r := w.cli(30*time.Second, "agent", "list", "--agent", "antigravity", "-o", "json")
		var doc struct{ Agents []listed }
		if r.code != 0 || json.Unmarshal([]byte(r.stdout), &doc) != nil || len(doc.Agents) != 1 {
			t.Fatalf("agent list: %+v", r)
		}
		return doc.Agents[0]
	}
	a := list()
	var models []string
	for _, m := range a.Models {
		models = append(models, m.ID+"("+m.Name+")"+strings.Join(m.Efforts, "/"))
	}
	if a.ModelsState != "known" || a.ObservedAt == 0 || strings.Join(models, " ") != "small(SMALL)low/high large(LARGE)low/high/xhigh" ||
		a.DefaultModel != "small" || a.DefaultEffort != "high" {
		t.Fatalf("listed: %+v", a)
	}
	if r := w.cli(30*time.Second, "agent", "list", "--agent", "antigravity"); r.code != 0 || !strings.Contains(r.stdout, "small, large") {
		t.Fatalf("human-readable: %+v", r)
	}
	// Read in one session never prompted and closed; listing again reads
	// nothing more.
	list()
	if b, _ := os.ReadFile(log); strings.Join(strings.Fields(string(b)), " ") != "initialize session/new session/set_config_option session/close" {
		t.Fatalf("reading the models: %q", b)
	}

	refused := func(want string, args ...string) {
		t.Helper()
		var failed struct {
			Error struct{ Kind, Message string }
		}
		if _, r := w.startSession(30*time.Second, append([]string{"--agent", "antigravity", "--prompt", "settings"}, args...)...); r.code == 0 ||
			json.Unmarshal([]byte(r.stdout), &failed) != nil || failed.Error.Kind != "usage" || !strings.Contains(failed.Error.Message, want) {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	refused(`antigravity on this machine has no model "huge" (one of: small, large)`, "--model", "huge")
	refused(`antigravity's model small has no reasoning effort "xhigh" (one of: low, high)`, "--model", "small", "--effort", "xhigh")

	machines, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
	if err != nil || len(machines.Machines) == 0 {
		t.Fatal(err)
	}
	machineID := machines.Machines[0].MachineId
	if _, err := w.app.client.SetAgentDefaults(ctx, &localv1.SetAgentDefaultsRequest{MachineId: machineID,
		Defaults: &identityv1.AgentDefaults{Agent: "antigravity", Model: "large", Effort: "xhigh"}}); err != nil {
		t.Fatal(err)
	}
	var shown *machinev1.AgentStatus
	eventually(t, 30*time.Second, "defaults shown", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		for _, x := range res.Machines[0].Status.GetAgents() {
			if x.Name == "antigravity" {
				shown = x
			}
		}
		return shown.GetDefaults().GetModel() == "large"
	})
	if shown.Own.GetModel() != "small" || shown.Own.GetEffort() != "high" || len(shown.Models) != 2 || shown.ModelsState != "known" {
		t.Fatalf("app status: %v", shown)
	}
	if a := list(); a.DefaultModel != "large" || a.DefaultEffort != "xhigh" {
		t.Fatalf("listed defaults: %+v", a)
	}

	for _, c := range []struct {
		args []string
		runs string
	}{
		{nil, "large xhigh"},
		{[]string{"--model", "small", "--effort", "low"}, "small low"},
		{[]string{"--model", "small"}, "small high"}, // xhigh: not small's
		{[]string{"--effort", "low"}, "large low"},
	} {
		s, r := w.startSession(time.Minute, append([]string{"--agent", "antigravity", "--wait", "--prompt", "settings"}, c.args...)...)
		if r.code != 0 || s.Status != "idle" || s.Result != "settings: "+c.runs {
			t.Fatalf("%v: %+v", c.args, r)
		}
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range res.Sessions {
			if x.SessionId == s.SessionID && x.Model+" "+x.Effort != c.runs {
				t.Fatalf("%v: the app shows %s %s", c.args, x.Model, x.Effort)
			}
		}
	}
}

// Uses an already-installed Electron only; unlike the general desktop harness,
// this test never invokes an installer or updates the running user's app.
func TestAntigravityModelDefaultsDesktop(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("desktop macOS e2e")
	}
	dir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "electron", "dist", "Electron.app", "Contents", "MacOS", "Electron")); err != nil {
		t.Skip("Electron must already be installed")
	}
	env, _ := antigravityEnv(t)
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"antigravity"}})
	out := filepath.Join(t.TempDir(), "app")
	if r := run(t, []string{"HERMESH_DESKTOP_OUT=" + out, "VERSION=0.1.0-rc7"}, 5*time.Minute, "pnpm", "--dir", dir, "build"); r.code != 0 {
		t.Fatalf("build: %+v", r)
	}
	f := desktopFixture{Socket: w.sock, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv}
	file := filepath.Join(t.TempDir(), "fixture.json")
	b, _ := json.Marshal(f)
	os.WriteFile(file, b, 0o600)
	env = []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_DESKTOP_OUT=" + out, "HERMESH_PW_OUT=" + filepath.Join(t.TempDir(), "results"), "HERMESH_MODEL_DEFAULTS_TEST=1"}
	if r := run(t, env, 3*time.Minute, "pnpm", "--dir", dir, "exec", "playwright", "test", "model-defaults.spec.ts"); r.code != 0 {
		t.Fatalf("playwright: %+v", r)
	}
	// What was picked in the app is what a session runs with.
	s, r := w.startSession(time.Minute, "--agent", "antigravity", "--wait", "--prompt", "settings")
	if r.code != 0 || s.Result != "settings: large xhigh" {
		t.Fatalf("session after picking in the app: %+v", r)
	}
}
