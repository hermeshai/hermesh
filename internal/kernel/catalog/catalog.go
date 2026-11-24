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

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/acp"
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
	inst *sessionv1.ProviderInstance
}

// AgentModels gives the models a coding agent offers on this machine as the
// agent itself lists them and what its sessions run with when none is named.
type AgentModels func(agent string) (models []runtime.Model, def runtime.Settings, ok bool)

type Service struct {
	mu      sync.Mutex
	drivers map[string]runtime.Driver
	models  AgentModels
	cache   map[string]cachedSnapshot
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
func DefaultInstances(includeScripted bool) []*sessionv1.ProviderInstanceConfig {
	out := []*sessionv1.ProviderInstanceConfig{
		{InstanceId: "claude", Driver: "claude", DisplayName: "Claude Code", Enabled: true},
		{InstanceId: "codex", Driver: "codex", DisplayName: "Codex", Enabled: true},
	}
	displayNames := map[string]string{
		"gemini":      "Gemini CLI",
		"antigravity": "Antigravity",
		"cursor":      "Cursor",
		"copilot":     "GitHub Copilot",
		"opencode":    "OpenCode",
		"qwen":        "Qwen Code",
		"goose":       "Goose",
		"auggie":      "Auggie",
		"kimi":        "Kimi",
		"kiro":        "Kiro",
		"qoder":       "Qoder",
		"deepseek":    "DeepSeek",
		"codebuddy":   "CodeBuddy",
		"grok":        "Grok",
	}
	for _, a := range acp.Agents {
		dn := displayNames[a.Name]
		if dn == "" {
			dn = a.Name
		}
		out = append(out, &sessionv1.ProviderInstanceConfig{
			InstanceId:  a.Name,
			Driver:      "acp",
			AcpFlavor:   a.Name,
			DisplayName: dn,
			Enabled:     true,
		})
	}
	if includeScripted {
		out = append([]*sessionv1.ProviderInstanceConfig{
			{InstanceId: "scripted", Driver: "scripted", DisplayName: "Scripted Agent", Enabled: true},
			{InstanceId: "fake", Driver: "fake", DisplayName: "Fake Agent", Enabled: true},
		}, out...)
	}
	return out
}

func manifestKey(cfg *sessionv1.ProviderInstanceConfig) string {
	if cfg.Driver == "acp" && cfg.AcpFlavor != "" {
		return cfg.AcpFlavor
	}
	if cfg.Driver == "fake" {
		return "scripted"
	}
	return cfg.Driver
}

func buildDescriptor(tpl manifestOption) *sessionv1.OptionDescriptor {
	if tpl.Type == "boolean" {
		return &sessionv1.OptionDescriptor{
			Id:           tpl.ID,
			Label:        tpl.Label,
			Description:  tpl.Description,
			Type:         sessionv1.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_BOOLEAN,
			Role:         tpl.Role,
			CurrentValue: &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_BoolValue{BoolValue: tpl.DefaultBool}},
		}
	}
	choices := make([]*sessionv1.OptionChoice, 0, len(tpl.Choices))
	for _, c := range tpl.Choices {
		choices = append(choices, &sessionv1.OptionChoice{
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
	return &sessionv1.OptionDescriptor{
		Id:           tpl.ID,
		Label:        tpl.Label,
		Description:  tpl.Description,
		Type:         sessionv1.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_SELECT,
		Role:         tpl.Role,
		Choices:      choices,
		CurrentValue: &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: cur}},
	}
}

func bundledModelsFor(key string) (displayName string, models []*sessionv1.ModelCatalogEntry) {
	doc := bundled()
	p, ok := doc.Providers[key]
	if !ok {
		return key, nil
	}
	for _, m := range p.Models {
		var opts []*sessionv1.OptionDescriptor
		for _, optKey := range m.Options {
			if tpl, ok := doc.OptionTemplates[optKey]; ok {
				opts = append(opts, buildDescriptor(tpl))
			}
		}
		models = append(models, &sessionv1.ModelCatalogEntry{
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

func (s *Service) ResolveInstance(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, workdir string, refresh bool) *sessionv1.ProviderInstance {
	cacheKey := cfg.InstanceId + "\x00" + workdir
	if !refresh {
		s.mu.Lock()
		if hit, ok := s.cache[cacheKey]; ok && time.Since(hit.at) < cacheTTL {
			cloned := proto.Clone(hit.inst).(*sessionv1.ProviderInstance)
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

	inst := &sessionv1.ProviderInstance{
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
		if inst.BinaryPath == "" && probe.Binary != "" {
			inst.BinaryPath = probe.Binary
		}
		inst.Compatibility = probe.Compatibility
		inst.CompatibilityMessage = probe.CompatibilityMessage
		inst.Models = mergeModels(manifestModels, probe.Models, cfg.CustomModels)
		inst.WorkspaceOptions = probe.WorkspaceOptions
		inst.SlashCommands = probe.SlashCommands
		inst.Skills = probe.Skills
	}
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
	s.cache[cacheKey] = cachedSnapshot{at: time.Now(), inst: proto.Clone(inst).(*sessionv1.ProviderInstance)}
	s.mu.Unlock()
	return inst
}

func liveModels(driver string, ms []runtime.Model, def runtime.Settings) []*sessionv1.ModelCatalogEntry {
	effortID := "reasoningEffort"
	if driver == "acp" {
		effortID = "thought_level"
	}
	out := make([]*sessionv1.ModelCatalogEntry, 0, len(ms))
	for _, m := range ms {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		e := &sessionv1.ModelCatalogEntry{Slug: m.ID, Name: name, ShortName: name, IsDefault: m.ID == def.Model}
		if len(m.Efforts) > 0 {
			cur := m.Efforts[0]
			if slices.Contains(m.Efforts, def.Effort) {
				cur = def.Effort
			} else if slices.Contains(m.Efforts, "medium") {
				cur = "medium"
			}
			choices := make([]*sessionv1.OptionChoice, 0, len(m.Efforts))
			for _, id := range m.Efforts {
				choices = append(choices, &sessionv1.OptionChoice{Id: id, Label: id, IsDefault: id == cur})
			}
			e.OptionDescriptors = []*sessionv1.OptionDescriptor{{
				Id:           effortID,
				Label:        "Reasoning",
				Type:         sessionv1.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_SELECT,
				Role:         "reasoning",
				Choices:      choices,
				CurrentValue: &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: cur}},
			}}
		}
		out = append(out, e)
	}
	return out
}

func mergeModels(base []*sessionv1.ModelCatalogEntry, discovered []*sessionv1.ModelCatalogEntry, custom []string) []*sessionv1.ModelCatalogEntry {
	bySlug := map[string]*sessionv1.ModelCatalogEntry{}
	var order []string
	for _, m := range base {
		cp := proto.Clone(m).(*sessionv1.ModelCatalogEntry)
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
			cp := proto.Clone(d).(*sessionv1.ModelCatalogEntry)
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
			bySlug[slug] = &sessionv1.ModelCatalogEntry{
				Slug:      slug,
				Name:      slug,
				ShortName: slug,
				IsCustom:  true,
			}
			order = append(order, slug)
		}
	}
	out := make([]*sessionv1.ModelCatalogEntry, 0, len(order))
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

func scanWorkspaceCustomizations(workdir string) ([]*sessionv1.SlashCommand, []*sessionv1.SkillEntry) {
	var cmds []*sessionv1.SlashCommand
	var skills []*sessionv1.SkillEntry

	cmdDir := filepath.Join(workdir, ".claude", "commands")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				name := strings.TrimSuffix(e.Name(), ".md")
				cmds = append(cmds, &sessionv1.SlashCommand{
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
				skills = append(skills, &sessionv1.SkillEntry{
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
func DefaultSelection(inst *sessionv1.ProviderInstance) *sessionv1.ModelSelection {
	model := "default"
	var chosen *sessionv1.ModelCatalogEntry
	for _, m := range inst.Models {
		if m.IsDefault {
			chosen = m
			break
		}
	}
	if chosen == nil && len(inst.Models) > 0 {
		chosen = inst.Models[0]
	}
	opts := map[string]*sessionv1.OptionValue{}
	if chosen != nil {
		model = chosen.Slug
		for _, d := range chosen.OptionDescriptors {
			if d.CurrentValue != nil {
				opts[d.Id] = proto.Clone(d.CurrentValue).(*sessionv1.OptionValue)
			}
		}
	}
	return &sessionv1.ModelSelection{
		InstanceId: inst.InstanceId,
		Model:      model,
		Options:    opts,
	}
}
