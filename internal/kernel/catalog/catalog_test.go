package catalog

import (
	"context"
	"slices"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func slugs(inst *sessionv1.ProviderInstance) []string {
	var out []string
	for _, m := range inst.Models {
		out = append(out, m.Slug)
	}
	return out
}

func TestResolveInstanceOffersTheAgentsOwnModels(t *testing.T) {
	live := map[string][]runtime.Model{
		"claude":      {{ID: "opus", Name: "Opus", Efforts: []string{"low", "medium", "high", "max"}}, {ID: "haiku", Name: "Haiku"}},
		"antigravity": {{ID: "small", Name: "SMALL", Efforts: []string{"low", "high"}}, {ID: "large", Name: "LARGE", Efforts: []string{"low", "high", "xhigh"}}},
	}
	s := NewService(nil, func(agent string) ([]runtime.Model, runtime.Settings, bool) {
		ms, ok := live[agent]
		def := runtime.Settings{}
		if agent == "antigravity" {
			def = runtime.Settings{Model: "large", Effort: "xhigh"}
		}
		return ms, def, ok
	})
	ctx := context.Background()

	cl := s.ResolveInstance(ctx, &sessionv1.ProviderInstanceConfig{InstanceId: "claude", Driver: "claude", CustomModels: []string{"my-model"}}, "", true)
	if got := slugs(cl); !slices.Equal(got, []string{"opus", "haiku", "my-model"}) {
		t.Fatalf("claude models = %v", got)
	}
	opus := cl.Models[0]
	if len(opus.OptionDescriptors) != 1 || opus.OptionDescriptors[0].Id != "reasoningEffort" || opus.OptionDescriptors[0].Role != "reasoning" ||
		opus.OptionDescriptors[0].CurrentValue.GetStringValue() != "medium" || len(opus.OptionDescriptors[0].Choices) != 4 {
		t.Fatalf("opus reasoning = %v", opus.OptionDescriptors)
	}
	if len(cl.Models[1].OptionDescriptors) != 0 || slices.ContainsFunc(cl.Models, func(m *sessionv1.ModelCatalogEntry) bool { return m.IsDefault }) {
		t.Fatalf("haiku takes no effort and no model is the default: %v", cl.Models)
	}

	ag := s.ResolveInstance(ctx, &sessionv1.ProviderInstanceConfig{InstanceId: "antigravity", Driver: "acp", AcpFlavor: "antigravity"}, "", true)
	if got := slugs(ag); !slices.Equal(got, []string{"small", "large"}) {
		t.Fatalf("antigravity models = %v", got)
	}
	large := ag.Models[1]
	if !large.IsDefault || ag.Models[0].IsDefault || large.OptionDescriptors[0].Id != "thought_level" || large.OptionDescriptors[0].CurrentValue.GetStringValue() != "xhigh" {
		t.Fatalf("the machine's default (large, xhigh): %v", ag.Models)
	}
	if ag.Models[0].OptionDescriptors[0].CurrentValue.GetStringValue() != "low" {
		t.Fatalf("small's effort = %v", ag.Models[0].OptionDescriptors[0].CurrentValue)
	}

	cx := s.ResolveInstance(ctx, &sessionv1.ProviderInstanceConfig{InstanceId: "codex", Driver: "codex"}, "", true)
	if got, want := slugs(cx), []string{"gpt-5.4", "gpt-5.3-codex", "gpt-5.4-mini"}; !slices.Equal(got, want) {
		t.Fatalf("codex could not tell: the manifest's %v, got %v", want, got)
	}
}
