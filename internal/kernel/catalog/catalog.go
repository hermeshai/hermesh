// Package catalog merges Hermesh's embedded model-manifest.json with live
// provider CLI discovery, custom user models, and workspace slash-commands/skills.
package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/runtime"
)

//go:embed manifest.json
var rawManifest []byte

type manifestDoc struct {
	Providers       map[string]manifestProvider `json:"providers"`
	OptionTemplates map[string]manifestOption   `json:"optionTemplates"`
}

type manifestProvider struct {
	DisplayName  string          `json:"displayName"`
	DefaultModel string          `json:"defaultModel"`
	MinVersion   string          `json:"minVersion"`
	Models       []manifestModel `json:"models"`
}

type manifestModel struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	ShortName string   `json:"shortName"`
	Badge     string   `json:"badge"`
	IsDefault bool     `json:"isDefault"`
	IsLegacy  bool     `json:"isLegacy"`
	Options   []string `json:"options"`
}

type manifestOption struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	Role        string           `json:"role"`
	Type        string           `json:"type"`
	Default     string           `json:"default"`
	DefaultBool bool             `json:"defaultBool"`
	Choices     []manifestChoice `json:"choices"`
}

type manifestChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	IsDefault   bool   `json:"isDefault"`
}

var (
	parsedOnce sync.Once
	parsed     manifestDoc
)

func bundled() manifestDoc {
	parsedOnce.Do(func() {
		_ = json.Unmarshal(rawManifest, &parsed)
	})
	return parsed
}

const cacheTTL = 5 * time.Minute

type cachedSnapshot struct {
	at   time.Time
	inst *threadv2.ProviderInstance
}

// AgentModels gives the models a coding agent offers on this machine as the
// agent itself lists them (the list the machine's settings show) and what its
// sessions run with when none is named; ok is false when it could not tell.
type AgentModels func(agent string) (models []provider.Model, def provider.Settings, ok bool)

type Service struct {
	mu      sync.Mutex
	drivers map[string]runtime.Driver
	models  AgentModels               // nil: the manifest's models only
	cache   map[string]cachedSnapshot // instanceID + "\x00" + workdir
}

func NewService(drivers map[string]runtime.Driver, models AgentModels) *Service {
	return &Service{
		drivers: drivers,
		models:  models,
		cache:   map[string]cachedSnapshot{},
	}
}

func (s *Service) Driver(kind string) runtime.Driver {
	return s.drivers[kind]
}

// DefaultInstances returns standard built-in provider instance configs for a machine.
func DefaultInstances(includeScripted bool) []*threadv2.ProviderInstanceConfig {
	out := []*threadv2.ProviderInstanceConfig{
		{InstanceId: "claude", Driver: "claude", DisplayName: "Claude Code", Enabled: true},
		{InstanceId: "codex", Driver: "codex", DisplayName: "Codex", Enabled: true},
		{InstanceId: "gemini", Driver: "acp", AcpFlavor: "gemini", DisplayName: "Gemini CLI", Enabled: true},
		{InstanceId: "antigravity", Driver: "acp", AcpFlavor: "antigravity", DisplayName: "Antigravity", Enabled: true},
		{InstanceId: "cursor", Driver: "acp", AcpFlavor: "cursor", DisplayName: "Cursor", Enabled: true},
		{InstanceId: "opencode", Driver: "acp", AcpFlavor: "opencode", DisplayName: "OpenCode", Enabled: true},
	}
	if includeScripted {
		out = append([]*threadv2.ProviderInstanceConfig{
			{InstanceId: "scripted", Driver: "scripted", DisplayName: "Scripted Agent", Enabled: true},
		}, out...)
	}
	return out
}

func manifestKey(cfg *threadv2.ProviderInstanceConfig) string {
	if cfg.Driver == "acp" && cfg.AcpFlavor != "" {
		return cfg.AcpFlavor
	}
	return cfg.Driver
}

func buildDescriptor(tpl manifestOption) *threadv2.OptionDescriptor {
	if tpl.Type == "boolean" {
		return &threadv2.OptionDescriptor{
			Id:           tpl.ID,
			Label:        tpl.Label,
			Description:  tpl.Description,
			Type:         threadv2.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_BOOLEAN,
			Role:         tpl.Role,
			CurrentValue: &threadv2.OptionValue{Kind: &threadv2.OptionValue_BoolValue{BoolValue: tpl.DefaultBool}},
		}
	}
	choices := make([]*threadv2.OptionChoice, 0, len(tpl.Choices))
	for _, c := range tpl.Choices {
		choices = append(choices, &threadv2.OptionChoice{
			Id:          c.ID,
			Label:       c.Label,
			Description: c.Description,
			IsDefault:   c.IsDefault,
		})
	}
	cur := tpl.Default
	if cur == "" && len(choices) > 0 {
		cur = choices[0].Id
	}
	return &threadv2.OptionDescriptor{
		Id:           tpl.ID,
		Label:        tpl.Label,
		Description:  tpl.Description,
		Type:         threadv2.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_SELECT,
		Role:         tpl.Role,
		Choices:      choices,
		CurrentValue: &threadv2.OptionValue{Kind: &threadv2.OptionValue_StringValue{StringValue: cur}},
	}
}

func bundledModelsFor(key string) (displayName string, models []*threadv2.ModelCatalogEntry) {
	doc := bundled()
	p, ok := doc.Providers[key]
	if !ok {
		return key, nil
	}
	for _, m := range p.Models {
		var opts []*threadv2.OptionDescriptor
		for _, optKey := range m.Options {
			if tpl, ok := doc.OptionTemplates[optKey]; ok {
				opts = append(opts, buildDescriptor(tpl))
			}
		}
		models = append(models, &threadv2.ModelCatalogEntry{
			Slug:              m.Slug,
			Name:              m.Name,
			ShortName:         m.ShortName,
			Badge:             m.Badge,
			IsDefault:         m.IsDefault || m.Slug == p.DefaultModel,
			IsLegacy:          m.IsLegacy,
			MinCliVersion:     p.MinVersion,
			OptionDescriptors: opts,
		})
	}
	return p.DisplayName, models
}

// ResolveInstance merges static manifest metadata, live CLI probe results,
// custom models, redacted env vars, and workspace commands/skills.
func (s *Service) ResolveInstance(ctx context.Context, cfg *threadv2.ProviderInstanceConfig, workdir string, refresh bool) *threadv2.ProviderInstance {
	cacheKey := cfg.InstanceId + "\x00" + workdir
	if !refresh {
		s.mu.Lock()
		if hit, ok := s.cache[cacheKey]; ok && time.Since(hit.at) < cacheTTL {
			cloned := proto.Clone(hit.inst).(*threadv2.ProviderInstance)
			s.mu.Unlock()
			return cloned
		}
		s.mu.Unlock()
	}

	mKey := manifestKey(cfg)
	fallbackName, manifestModels := bundledModelsFor(mKey)
	displayName := cfg.DisplayName
	if displayName == "" {
		displayName = fallbackName
	}

	inst := &threadv2.ProviderInstance{
		InstanceId:  cfg.InstanceId,
		Driver:      cfg.Driver,
		AcpFlavor:   cfg.AcpFlavor,
		DisplayName: displayName,
		Enabled:     cfg.Enabled,
		BinaryPath:  cfg.BinaryPath,
		LaunchArgs:  slices.Clone(cfg.LaunchArgs),
		RedactedEnv: redactEnv(cfg.Env),
		Models:      manifestModels,
	}

	drv := s.drivers[cfg.Driver]
	if drv != nil {
		inst.Capabilities = drv.Capabilities(cfg.AcpFlavor)
		probe := drv.Probe(ctx, cfg, workdir)
		inst.Installed = probe.Installed
		inst.Authenticated = probe.Authenticated
		inst.AuthLabel = probe.AuthLabel
		inst.Version = probe.Version
		inst.Compatibility = probe.Compatibility
		inst.CompatibilityMessage = probe.CompatibilityMessage
		inst.Models = mergeModels(manifestModels, probe.Models, cfg.CustomModels)
		inst.WorkspaceOptions = probe.WorkspaceOptions
		inst.SlashCommands = probe.SlashCommands
		inst.Skills = probe.Skills
	}
	// The agent's own list, where it gave one, is what it offers here; the
	// manifest's is only for an agent that could not tell.
	if s.models != nil {
		if ms, def, ok := s.models(mKey); ok && len(ms) > 0 {
			inst.Models = mergeModels(liveModels(cfg.Driver, ms, def), nil, cfg.CustomModels)
		}
	}

	if workdir != "" {
		wsCmds, wsSkills := scanWorkspaceCustomizations(workdir)
		inst.SlashCommands = append(inst.SlashCommands, wsCmds...)
		inst.Skills = append(inst.Skills, wsSkills...)
	}

	s.mu.Lock()
	s.cache[cacheKey] = cachedSnapshot{at: time.Now(), inst: proto.Clone(inst).(*threadv2.ProviderInstance)}
	s.mu.Unlock()
	return inst
}

// liveModels are an agent's own models as catalog entries: def's model is
// the default, and a model's reasoning efforts its reasoning option, under
// the id the driver reads (the effort def names, else medium, chosen).
func liveModels(driver string, ms []provider.Model, def provider.Settings) []*threadv2.ModelCatalogEntry {
	effortID := "reasoningEffort"
	if driver == "acp" {
		effortID = "thought_level"
	}
	out := make([]*threadv2.ModelCatalogEntry, 0, len(ms))
	for _, m := range ms {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		e := &threadv2.ModelCatalogEntry{Slug: m.ID, Name: name, ShortName: name, IsDefault: m.ID == def.Model}
		if len(m.Efforts) > 0 {
			cur := m.Efforts[0]
			if slices.Contains(m.Efforts, def.Effort) {
				cur = def.Effort
			} else if slices.Contains(m.Efforts, "medium") {
				cur = "medium"
			}
			choices := make([]*threadv2.OptionChoice, 0, len(m.Efforts))
			for _, id := range m.Efforts {
				choices = append(choices, &threadv2.OptionChoice{Id: id, Label: id, IsDefault: id == cur})
			}
			e.OptionDescriptors = []*threadv2.OptionDescriptor{{
				Id:           effortID,
				Label:        "Reasoning",
				Type:         threadv2.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_SELECT,
				Role:         "reasoning",
				Choices:      choices,
				CurrentValue: &threadv2.OptionValue{Kind: &threadv2.OptionValue_StringValue{StringValue: cur}},
			}}
		}
		out = append(out, e)
	}
	return out
}

func mergeModels(base []*threadv2.ModelCatalogEntry, discovered []*threadv2.ModelCatalogEntry, custom []string) []*threadv2.ModelCatalogEntry {
	bySlug := map[string]*threadv2.ModelCatalogEntry{}
	var order []string
	for _, m := range base {
		cp := proto.Clone(m).(*threadv2.ModelCatalogEntry)
		bySlug[cp.Slug] = cp
		order = append(order, cp.Slug)
	}
	for _, d := range discovered {
		if existing, ok := bySlug[d.Slug]; ok {
			if len(d.OptionDescriptors) > 0 {
				existing.OptionDescriptors = d.OptionDescriptors
			}
			if d.Name != "" {
				existing.Name = d.Name
			}
		} else {
			cp := proto.Clone(d).(*threadv2.ModelCatalogEntry)
			bySlug[cp.Slug] = cp
			order = append(order, cp.Slug)
		}
	}
	for _, slug := range custom {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := bySlug[slug]; !ok {
			bySlug[slug] = &threadv2.ModelCatalogEntry{
				Slug:      slug,
				Name:      slug,
				ShortName: slug,
				IsCustom:  true,
			}
			order = append(order, slug)
		}
	}
	out := make([]*threadv2.ModelCatalogEntry, 0, len(order))
	for _, slug := range order {
		out = append(out, bySlug[slug])
	}
	return out
}

func redactEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		upper := strings.ToUpper(k)
		if strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") ||
			strings.Contains(upper, "CREDENTIAL") {
			if v != "" {
				out[k] = "••••••••"
			} else {
				out[k] = ""
			}
		} else {
			out[k] = v
		}
	}
	return out
}

func scanWorkspaceCustomizations(workdir string) ([]*threadv2.SlashCommand, []*threadv2.SkillEntry) {
	var cmds []*threadv2.SlashCommand
	var skills []*threadv2.SkillEntry

	cmdDir := filepath.Join(workdir, ".claude", "commands")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				name := strings.TrimSuffix(e.Name(), ".md")
				cmds = append(cmds, &threadv2.SlashCommand{
					Name:        "/" + name,
					Description: "Workspace command (" + e.Name() + ")",
				})
			}
		}
	}
	for _, rel := range []string{"skills", filepath.Join(".claude", "skills"), filepath.Join(".agents", "skills")} {
		dir := filepath.Join(workdir, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillFile := filepath.Join(dir, e.Name(), "SKILL.md")
			if st, sErr := os.Stat(skillFile); sErr == nil && !st.IsDir() {
				skills = append(skills, &threadv2.SkillEntry{
					Name:          e.Name(),
					Path:          skillFile,
					UserInvocable: true,
				})
			}
		}
	}
	return cmds, skills
}

// DefaultSelection picks a valid default ModelSelection for an instance.
func DefaultSelection(inst *threadv2.ProviderInstance) *threadv2.ModelSelection {
	model := "default"
	var chosen *threadv2.ModelCatalogEntry
	for _, m := range inst.Models {
		if m.IsDefault {
			chosen = m
			break
		}
	}
	if chosen == nil && len(inst.Models) > 0 {
		chosen = inst.Models[0]
	}
	opts := map[string]*threadv2.OptionValue{}
	if chosen != nil {
		model = chosen.Slug
		for _, d := range chosen.OptionDescriptors {
			if d.CurrentValue != nil {
				opts[d.Id] = proto.Clone(d.CurrentValue).(*threadv2.OptionValue)
			}
		}
	}
	return &threadv2.ModelSelection{
		InstanceId: inst.InstanceId,
		Model:      model,
		Options:    opts,
	}
}
