package herhost

import (
	"encoding/json"
	"testing"
)

// Her's models as the her driver offers them: those she can use and shows, her default first, her
// thinking levels as the effort option.
func TestHerModels(t *testing.T) {
	raw := json.RawMessage(`{"models":[
		{"id":"tc/glm","name":"GLM-5.3","available":true,"shown":true,"thinking_levels":["off","low","high"],"default_thinking":"high"},
		{"id":"tc/auto","name":"Auto (General Token Plan)","available":true,"shown":true,"thinking_levels":[]},
		{"id":"tc/hidden","name":"Hidden","available":true,"shown":false},
		{"id":"gpt/x","name":"No key","available":false,"shown":true},
		{"id":"tc/old","name":"Old","available":true,"shown":true,"deprecated":true}],
		"defaults":{"model":"tc/auto"}}`)
	got := herModels(raw, "", "")
	if len(got) != 2 || got[0].Slug != "tc/auto" || !got[0].IsDefault || got[1].Slug != "tc/glm" {
		t.Fatalf("models: %v", got)
	}
	if len(got[0].OptionDescriptors) != 0 {
		t.Fatalf("a model without levels has an effort option: %v", got[0])
	}
	opt := got[1].OptionDescriptors
	if len(opt) != 1 || opt[0].Id != "effort" || len(opt[0].Choices) != 2 || opt[0].CurrentValue.GetStringValue() != "high" {
		t.Fatalf("effort: %v", opt)
	}

	// Her's main conversation model and thinking override models/list's generic fallback default.
	gotBot := herModels(raw, "tc/glm", "low")
	if len(gotBot) != 2 || gotBot[0].Slug != "tc/glm" || !gotBot[0].IsDefault || gotBot[1].IsDefault {
		t.Fatalf("bot default models: %v", gotBot)
	}
	if cur := gotBot[0].OptionDescriptors[0].CurrentValue.GetStringValue(); cur != "low" {
		t.Fatalf("bot default thinking = %q, want low", cur)
	}

	// Even a hidden model configured on Her's main conversation stays visible as her default; an unavailable one falls back.
	gotHidden := herModels(raw, "tc/hidden", "")
	if len(gotHidden) != 3 || gotHidden[0].Slug != "tc/hidden" || !gotHidden[0].IsDefault {
		t.Fatalf("hidden bot default: %v", gotHidden)
	}
	gotUnavailable := herModels(raw, "gpt/x", "")
	if len(gotUnavailable) != 2 || gotUnavailable[0].Slug != "tc/auto" || !gotUnavailable[0].IsDefault {
		t.Fatalf("unavailable bot default fallback: %v", gotUnavailable)
	}
}

func TestMainSessionSettings(t *testing.T) {
	raw := json.RawMessage(`{"session_id":"s_main","snapshot":{"session":{"model":"chatgpt/gpt-6.2-sol","thinking":"high"}}}`)
	model, thinking := mainSessionSettings(raw)
	if model != "chatgpt/gpt-6.2-sol" || thinking != "high" {
		t.Fatalf("got (%q, %q), want (chatgpt/gpt-6.2-sol, high)", model, thinking)
	}
}

func TestSnapshotEvents(t *testing.T) {
	raw := json.RawMessage(`{"session_id":"s_1","snapshot":{
		"active_task":{"task_id":"t_live","approvals":[{"call_id":"c_main","name":"bash","summary":"rm -rf tmp"}]},
		"agents":[
			{"agent_id":"ag_run","name":"research","description":"查文档","call_id":"c_spawn_1","status":"running",
			 "active_task":{"task_id":"t_ag","approvals":[{"call_id":"c_sub","name":"bash","summary":"touch a.txt"}]}},
			{"agent_id":"ag_done","name":"old","call_id":"c_spawn_0","status":"completed"}
		]
	}}`)
	evs := snapshotEvents(raw)
	if len(evs) != 4 {
		t.Fatalf("got %d events: %s", len(evs), evs)
	}
	var types []string
	for _, b := range evs {
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		types = append(types, m["type"].(string))
	}
	want := []string{"task_started", "approval_requested", "agent_started", "agent_event"}
	for i, w := range want {
		if types[i] != w {
			t.Fatalf("event %d: got %q, want %q (%s)", i, types[i], w, evs[i])
		}
	}
}
