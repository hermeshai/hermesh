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
