package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/runtime"
)

const (
	modelsFresh      = 24 * time.Hour
	codeAuthRequired = -32000
)

type modelList struct {
	context string
	models  []runtime.Model
	own     runtime.Settings
	state   string
	at      time.Time
}

func modelHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func modelContext(bin string, env []string) string {
	if env == nil {
		env = os.Environ()
	}
	home, _ := os.UserHomeDir()
	values := map[string]string{"HOME": home}
	for _, e := range env {
		for _, k := range []string{"HOME", "GEMINI_HOME", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_QUOTA_PROJECT", "CLOUDSDK_CONFIG", "AGY_ADC_AUTH"} {
			if v, ok := strings.CutPrefix(e, k+"="); ok {
				values[k] = v
			}
		}
		if v, ok := strings.CutPrefix(e, "GEMINI_API_KEY="); ok && v != "" {
			values["GEMINI_API_KEY"] = modelHash(v)
		}
	}
	stamp := bin
	if st, err := os.Stat(bin); err == nil {
		stamp += ":" + strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	}
	b, _ := json.Marshal(values)
	return modelHash(stamp + string(b))
}

type modelChoice struct {
	Value   string        `json:"value"`
	Name    string        `json:"name"`
	Options []modelChoice `json:"options"`
}

type configOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      []modelChoice   `json:"options"`
}

func (o configOption) values() []modelChoice {
	var out []modelChoice
	var walk func([]modelChoice)
	walk = func(cs []modelChoice) {
		for _, c := range cs {
			if c.Value != "" && !slices.ContainsFunc(out, func(x modelChoice) bool { return x.Value == c.Value }) {
				out = append(out, c)
			}
			walk(c.Options)
		}
	}
	walk(o.Options)
	return out
}

func (o configOption) current() string {
	var v string
	json.Unmarshal(o.CurrentValue, &v)
	return v
}

type sessionOptions struct {
	model, thought *configOption
	legacy         []runtime.Model
	current        string
}

func optionsOf(result json.RawMessage) sessionOptions {
	var r struct {
		ConfigOptions []configOption `json:"configOptions"`
		Models        *struct {
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Name    string `json:"name"`
			} `json:"availableModels"`
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	json.Unmarshal(result, &r)
	var o sessionOptions
	for i, c := range r.ConfigOptions {
		switch {
		case c.Type != "select":
		case c.Category == "model" || (c.Category == "" && c.ID == "model"):
			o.model = &r.ConfigOptions[i]
		case c.Category == "thought_level":
			o.thought = &r.ConfigOptions[i]
		}
	}
	if o.model == nil && r.Models != nil {
		for _, m := range r.Models.AvailableModels {
			if m.ModelID != "" {
				o.legacy = append(o.legacy, runtime.Model{ID: m.ModelID, Name: m.Name})
			}
		}
		o.current = r.Models.CurrentModelID
	}
	return o
}

func (o sessionOptions) efforts() []string {
	if o.thought == nil {
		return nil
	}
	var es []string
	for _, c := range o.thought.values() {
		es = append(es, c.Value)
	}
	return es
}

func (o sessionOptions) list() (models []runtime.Model, own runtime.Settings, ok bool) {
	switch {
	case o.model != nil:
		own.Model = o.model.current()
		for _, c := range o.model.values() {
			m := runtime.Model{ID: c.Value, Name: c.Name}
			if c.Value == own.Model {
				m.Efforts = o.efforts()
			}
			models = append(models, m)
		}
		if o.thought != nil {
			own.Effort = o.thought.current()
		}
		return models, own, true
	case o.legacy != nil:
		return o.legacy, runtime.Settings{Model: o.current}, true
	}
	return nil, own, false
}

func (p *Provider) models(key string) (modelList, bool) {
	p.modelsMu.Lock()
	defer p.modelsMu.Unlock()
	if p.list == nil || p.list.context != key {
		return modelList{}, false
	}
	return *p.list, time.Since(p.list.at) < modelsFresh
}

func (p *Provider) learn(key string, o sessionOptions, initial bool) {
	models, own, ok := o.list()
	if !ok {
		return
	}
	p.modelsMu.Lock()
	defer p.modelsMu.Unlock()
	old := modelList{}
	if p.list != nil && p.list.context == key {
		old = *p.list
	}
	for i, m := range models {
		if k := slices.IndexFunc(old.models, func(x runtime.Model) bool { return x.ID == m.ID }); k >= 0 && m.Efforts == nil {
			models[i].Efforts = old.models[k].Efforts
		}
	}
	if !initial {
		own = old.own
	}
	p.list = &modelList{context: key, models: models, own: own, state: runtime.ModelsKnown, at: time.Now()}
}

func (p *Provider) fillModels(ctx context.Context, info *runtime.Info, cmd launch, env []string) {
	key := modelContext(cmd.bin, env)
	l, fresh := p.models(key)
	if !fresh {
		state := p.readModels(ctx, key, cmd, env)
		if l, fresh = p.models(key); !fresh {
			l = modelList{state: state, at: time.Now()}
			if state == runtime.ModelsUnsupported {
				p.modelsMu.Lock()
				p.list = &modelList{context: key, state: state, at: l.at}
				p.modelsMu.Unlock()
			}
		}
	}
	info.Models, info.Own, info.ModelsState, info.ModelsAt = slices.Clone(l.models), l.own, l.state, l.at
	info.AllModels = len(l.models) > 0
	if l.state == runtime.ModelsAuthRequired {
		info.LoggedIn = false
	}
}

func (p *Provider) readModels(ctx context.Context, key string, cmd launch, env []string) string {
	dir, err := os.MkdirTemp("", "hermesh-models-")
	if err != nil {
		return runtime.ModelsError
	}
	defer os.RemoveAll(dir)
	proc, err := runtime.StartProc(ctx, cmd.bin, cmd.acpArgs(), dir, env)
	if err != nil {
		return runtime.ModelsError
	}
	defer proc.Stop(time.Second)
	s := newProbeSession(proc, ctx)
	go s.readLoop()
	failed := func(err error) string {
		var rpc *rpcError
		if errors.As(err, &rpc) && rpc.Code == codeAuthRequired {
			return runtime.ModelsAuthRequired
		}
		return runtime.ModelsError
	}
	res, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "hermesh", "version": "0.1.0"},
	})
	if err != nil {
		return failed(err)
	}
	var init struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				Options json.RawMessage `json:"options"`
				Close   json.RawMessage `json:"close"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	json.Unmarshal(res, &init)
	offered := func(c json.RawMessage) bool { return len(c) > 0 && string(c) != "null" }
	if m := authMethod(p.Auth, init.AuthMethods); m != "" {
		if _, err := s.call(ctx, "authenticate", map[string]any{"methodId": m}); err != nil {
			return runtime.ModelsAuthRequired
		}
	}
	caps := init.AgentCapabilities.SessionCapabilities
	params := map[string]any{"cwd": dir, "mcpServers": []any{}}
	if offered(caps.Options) {
		res, err := s.call(ctx, "session/options", params)
		if err != nil {
			return failed(err)
		}
		o := optionsOf(res)
		if _, _, ok := o.list(); !ok {
			return runtime.ModelsUnsupported
		}
		p.learn(key, o, true)
		return ""
	}
	res, err = s.call(ctx, "session/new", params)
	if err != nil {
		return failed(err)
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(res, &r)
	o := optionsOf(res)
	if _, _, ok := o.list(); !ok {
		return runtime.ModelsUnsupported
	}
	p.learn(key, o, true)
	if o.model != nil && o.thought != nil {
		for _, c := range o.model.values() {
			if c.Value == o.model.current() {
				continue
			}
			res, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": r.SessionID, "configId": o.model.ID, "value": c.Value})
			if err != nil {
				break
			}
			p.learn(key, optionsOf(res), false)
		}
	}
	if offered(caps.Close) {
		s.call(ctx, "session/close", map[string]any{"sessionId": r.SessionID})
	}
	return ""
}
