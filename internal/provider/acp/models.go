package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

const modelCatalogTTL = 5 * time.Minute

type cachedCatalog struct {
	context string
	value   provider.ModelCatalog
	known   bool
	checked time.Time
}

func modelHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// Never inspect credentials or private caches. Scope by runtime installation
// and non-secret routing configuration. An account changed outside Hermesh in
// the same configuration directory cannot be detected here; these are dated
// observations, never an account-validity claim, and never persisted to disk.
func modelContext(bin string, env []string) string {
	if env == nil {
		env = os.Environ()
	}
	home, _ := os.UserHomeDir()
	values := map[string]string{"HOME": home}
	for _, e := range env {
		for _, k := range []string{"HOME", "GEMINI_HOME", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "CLOUDSDK_CONFIG"} {
			if v, ok := strings.CutPrefix(e, k+"="); ok {
				values[k] = v
			}
		}
	}
	stamp := bin
	if st, err := os.Stat(bin); err == nil {
		stamp += ":" + strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	}
	b, _ := json.Marshal(values)
	return modelHash(stamp + string(b))
}

// ModelCatalogs reads context metadata and memory, never starting an ACP
// process. Keep workspace observations separate, including defaults.
func (p *Provider) ModelCatalogs() []provider.ModelCatalog {
	if p.Name() != "antigravity" {
		return nil
	}
	key := modelContext(p.binary(), provider.ProbeEnv(p.PathEnv))
	p.modelsMu.Lock()
	defer p.modelsMu.Unlock()
	var out []provider.ModelCatalog
	for _, c := range p.catalogs {
		if c.context == key {
			v := c.value
			v.Models = slices.Clone(v.Models)
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = append(out, provider.ModelCatalog{Scope: "isolated", State: "unknown"})
	}
	slices.SortFunc(out, func(a, b provider.ModelCatalog) int { return strings.Compare(a.Scope, b.Scope) })
	return out
}

func (p *Provider) saveCatalog(key string, c provider.ModelCatalog) {
	p.modelsMu.Lock()
	defer p.modelsMu.Unlock()
	if p.catalogs == nil {
		p.catalogs = map[string]cachedCatalog{}
	}
	id := key + ":" + c.Scope
	known := c.State == "known"
	if old, ok := p.catalogs[id]; ok {
		// An update replaces options/currentValue, never the initial default.
		if c.Source == "acp_update" {
			c.DefaultModel = old.value.DefaultModel
		}
		// Failed refreshes retain the last observation, explicitly expired.
		if c.State != "known" && old.known {
			c.Models, c.DefaultModel, c.CurrentModel = old.value.Models, old.value.DefaultModel, old.value.CurrentModel
			c.ObservedAt, c.ExpiresAt = old.value.ObservedAt, time.Now()
			c.Source = old.value.Source
			known = true
		}
	}
	p.catalogs[id] = cachedCatalog{context: key, value: c, known: known, checked: time.Now()}
	// A bounded, process-local cache; no private account identifiers or paths
	// are returned to clients. Evict the oldest observation first.
	if len(p.catalogs) > 32 {
		oldest := id
		for k, v := range p.catalogs {
			if v.value.ObservedAt.Before(p.catalogs[oldest].value.ObservedAt) {
				oldest = k
			}
		}
		delete(p.catalogs, oldest)
	}
}

type modelChoice struct {
	Value   string        `json:"value"`
	Name    string        `json:"name"`
	Options []modelChoice `json:"options"`
}

func modelOptions(data json.RawMessage) ([]provider.Model, string, string) {
	var r struct {
		ConfigOptions []struct {
			ID, Category, Type string
			CurrentValue       json.RawMessage `json:"currentValue"`
			Options            json.RawMessage `json:"options"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(data, &r) != nil {
		return nil, "", "error"
	}
	for _, o := range r.ConfigOptions {
		if o.Category != "model" && o.ID != "model" {
			continue
		}
		if o.Type != "select" {
			return nil, "", "error"
		}
		var current string
		var choices []modelChoice
		if json.Unmarshal(o.CurrentValue, &current) != nil || json.Unmarshal(o.Options, &choices) != nil || string(o.Options) == "null" {
			return nil, "", "error"
		}
		models := []provider.Model{}
		var walk func([]modelChoice)
		walk = func(choices []modelChoice) {
			for _, c := range choices {
				if c.Value != "" && !slices.ContainsFunc(models, func(m provider.Model) bool { return m.ID == c.Value }) {
					models = append(models, provider.Model{ID: c.Value, Name: c.Name})
				}
				walk(c.Options)
			}
		}
		walk(choices)
		return models, current, "known"
	}
	return nil, "", "unknown"
}

func (p *Provider) observeModels(key, scope, source string, data json.RawMessage, initial bool) {
	models, current, state := modelOptions(data)
	c := provider.ModelCatalog{Scope: scope, Source: source, State: state, Models: models, CurrentModel: current,
		ObservedAt: time.Now(), ExpiresAt: time.Now().Add(modelCatalogTTL)}
	if initial {
		c.DefaultModel = current
	} else {
		c.Source = "acp_update"
	}
	p.saveCatalog(key, c)
}

func paramsForOptions(options json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(map[string]json.RawMessage{"configOptions": options})
	return b
}

// discoverModels only uses the public initialize and (advertised, currently
// unstable) session/options methods. NEVER fall back to session/new or prompt.
func (p *Provider) discoverModels(ctx context.Context, bin string, pre, env []string) {
	key := modelContext(bin, env)
	p.modelsMu.Lock()
	old, ok := p.catalogs[key+":isolated"]
	p.modelsMu.Unlock()
	if ok && time.Since(old.checked) < modelCatalogTTL {
		return
	}
	state := "error"
	source := "acp_initialize"
	defer func() {
		if state != "" {
			p.saveCatalog(key, provider.ModelCatalog{Scope: "isolated", Source: source, State: state,
				ObservedAt: time.Now(), ExpiresAt: time.Now().Add(modelCatalogTTL)})
		}
	}()
	dir, err := os.MkdirTemp("", "hermesh-acp-models-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	proc, err := provider.StartProc(ctx, bin, append(slices.Clone(pre), p.Args...), dir, env)
	if err != nil {
		return
	}
	defer proc.Stop(time.Second)
	s := &session{name: p.Name(), proc: proc, ctx: ctx, events: make(chan provider.Event, 256), pending: map[int64]func(rpcResult){},
		known: map[string]*toolCall{}, calls: map[string]provider.Decision{}, finished: map[string]bool{},
		gate: func(context.Context, provider.ToolAction) provider.Decision {
			return provider.Decision{Reason: "model discovery only"}
		}}
	go s.read()
	res, err := s.call(ctx, "initialize", map[string]any{"protocolVersion": 1,
		"clientInfo": map[string]any{"name": "hermesh", "version": "0.1.0"}, "clientCapabilities": map[string]any{}})
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			SessionCapabilities struct {
				Options json.RawMessage `json:"options"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err == nil && (json.Unmarshal(res, &init) != nil || init.ProtocolVersion != 1) {
		return
	}
	if err == nil {
		options := init.AgentCapabilities.SessionCapabilities.Options
		var capability map[string]json.RawMessage
		if len(options) != 0 && json.Unmarshal(options, &capability) != nil {
			return
		}
		if capability == nil {
			state = "session_required"
			return
		}
		source = "acp_options"
		res, err = s.call(ctx, "session/options", map[string]any{"cwd": filepath.Clean(dir)})
	}
	if err != nil {
		var rpc *rpcError
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			state = "timeout"
		case errors.As(err, &rpc) && rpc.Code == codeAuthRequired:
			state = "auth_required"
		}
		return // Never expose raw server errors/stderr (may contain credentials).
	}
	p.observeModels(key, "isolated", "acp_options", res, true)
	state = ""
}
