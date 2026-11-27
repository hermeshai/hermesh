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
	got := herModels(raw)
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
