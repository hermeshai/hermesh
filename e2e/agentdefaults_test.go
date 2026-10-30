package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// TestAgentDefaults: the owner sets, from the app, the model, reasoning
// effort and speed Codex's sessions on this Mac run with. An agent starting
// a session without a model gets them, passed to Codex for that session
// alone; one naming a model gets that model. The app shows the models Codex
// offers and Codex's own configuration (what following it means), which
// Hermesh never changes; cleared, sessions follow it again.
func TestAgentDefaults(t *testing.T) {
	unixOnly(t, "its fake codex is a shell script")
	if testing.Short() {
		t.Skip("e2e")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "threads")
	codex := filepath.Join(dir, "codex")
	os.WriteFile(codex, []byte(`#!/bin/sh
case "$1" in
--version) echo "codex-cli 0.164.0";;
login) echo "Logged in using ChatGPT";;
*) FAKE_CODEX_LOG=`+log+` FAKE_CODEX_CONFIG='{"model":"gpt-fake-big","model_reasoning_effort":"high","service_tier":"priority"}' exec `+filepath.Join(binaries(t), "fakecodex")+` "$@";;
esac
`), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_CODEX_BIN=" + codex}, &localv1.GrantSpec{Agents: []string{"codex"}})
	ctx := context.Background()
	status := func() (string, *machinev1.AgentStatus) {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil || len(res.Machines) == 0 {
			return "", nil
		}
		for _, a := range res.Machines[0].Status.GetAgents() {
			if a.Name == "codex" {
				return res.Machines[0].MachineId, a
			}
		}
		return res.Machines[0].MachineId, nil
	}
	eventually(t, 30*time.Second, "codex checked", func() bool { _, a := status(); return a.GetLoggedIn() })
	machineID, a := status()
	if len(a.Models) != 2 || a.Models[1].Id != "gpt-fake-small" || strings.Join(a.Models[1].Efforts, ",") != "low,high" {
		t.Fatalf("models: %v", a.Models)
	}
	if own := a.Own; own.GetModel() != "gpt-fake-big" || own.GetEffort() != "high" || own.GetSpeed() != identityv1.AgentSpeed_AGENT_SPEED_FAST || a.Defaults != nil {
		t.Fatalf("own %v, defaults %v", own, a.Defaults)
	}

	set := func(d *identityv1.AgentDefaults) {
		t.Helper()
		if _, err := w.app.client.SetAgentDefaults(ctx, &localv1.SetAgentDefaultsRequest{MachineId: machineID, Defaults: d}); err != nil {
			t.Fatal(err)
		}
	}
	// What Codex was told by the last session started, in thread/start.
	last := func(args ...string) map[string]any {
		t.Helper()
		if s, r := w.startSession(60*time.Second, append([]string{"--agent", "codex", "--prompt", "hi", "--wait"}, args...)...); r.code != 0 || s.Status != "idle" {
			t.Fatalf("start: %d %s %s", r.code, r.stdout, r.stderr)
		}
		b, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		var p map[string]any
		json.Unmarshal([]byte(lines[len(lines)-1]), &p)
		return p
	}

	set(&identityv1.AgentDefaults{Agent: "codex", Model: "gpt-fake-small", Effort: "low", Speed: identityv1.AgentSpeed_AGENT_SPEED_STANDARD})
	if _, a := status(); a.Defaults.GetModel() != "gpt-fake-small" || a.Own.GetModel() != "gpt-fake-big" {
		t.Fatalf("after setting: defaults %v, own %v", a.Defaults, a.Own)
	}
	p := last()
	if tier, ok := p["serviceTier"]; p["model"] != "gpt-fake-small" || !ok || tier != nil || p["config"].(map[string]any)["model_reasoning_effort"] != "low" {
		t.Fatalf("defaults not passed: %v", p)
	}
	if p := last("--model", "gpt-fake-big"); p["model"] != "gpt-fake-big" || p["config"].(map[string]any)["model_reasoning_effort"] != "low" {
		t.Fatalf("the agent's model: %v", p)
	}

	set(&identityv1.AgentDefaults{Agent: "codex"})
	p = last()
	for _, k := range []string{"model", "serviceTier"} {
		if _, ok := p[k]; ok {
			t.Fatalf("cleared, but %s still set: %v", k, p)
		}
	}
	// Only the session's own tools remain in its config (docs/agent-messages.md §7.4).
	if cfg, _ := p["config"].(map[string]any); cfg["model_reasoning_effort"] != nil || cfg["mcp_servers"] == nil {
		t.Fatalf("cleared, but the effort is still set, or the tools are missing: %v", p)
	}
}
