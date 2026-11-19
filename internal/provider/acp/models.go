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

	"github.com/hermeshai/hermesh/internal/provider"
)

// An ACP agent's models are its session's "model" config option, and each
// model's reasoning efforts its "thought_level" option while that model is
// selected (agentclientprotocol.com/protocol/v1/session-config-options).
// Hermesh reads them, without a prompt, from session/options when the agent
// offers it, else from a session it opens and never prompts; failing those,
// from the older "models" session state. Every session started refreshes
// what was read.

// modelsFresh is how long a list read from the agent stands before a check
// reads it again; a session started refreshes it. A failure is not kept.
const modelsFresh = 24 * time.Hour

// modelList is what was read of an agent's models.
type modelList struct {
	context string // modelContext it was read in
	models  []provider.Model
	own     provider.Settings
	state   string
	at      time.Time
}

func modelHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// modelContext tells installations and accounts apart, by the binary
// (path, size, time) and the variables that choose the agent's
// configuration, without reading any credential: a list read in another
// context is read again.
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

type modelChoice struct {
	Value   string        `json:"value"`
	Name    string        `json:"name"`
	Options []modelChoice `json:"options"` // a group of choices
}

type configOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      []modelChoice   `json:"options"`
}

// values are an option's choices, groups flattened, each once.
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

// sessionOptions are a session's model and thought-level options, from a
// session/new, /load, /resume, /options or /set_config_option result.
type sessionOptions struct {
	model, thought *configOption
	// legacy is the older "models" session state, when there is no model
	// option: its models, and the one selected.
	legacy  []provider.Model
	current string
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
				o.legacy = append(o.legacy, provider.Model{ID: m.ModelID, Name: m.Name})
			}
		}
		o.current = r.Models.CurrentModelID
	}
	return o
}

// efforts are the thought levels offered while the selected model is.
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

// list is what o says of the agent's models: the ones offered, the selected
// one's efforts and (own) what is selected.
func (o sessionOptions) list() (models []provider.Model, own provider.Settings, ok bool) {
	switch {
	case o.model != nil:
		own.Model = o.model.current()
		for _, c := range o.model.values() {
			m := provider.Model{ID: c.Value, Name: c.Name}
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
		return o.legacy, provider.Settings{Model: o.current}, true
	}
	return nil, own, false
}

// models is the agent's model list in context key, as read last; ok: one
// read within modelsFresh.
func (p *Provider) models(key string) (modelList, bool) {
	p.modelsMu.Lock()
	defer p.modelsMu.Unlock()
	if p.list == nil || p.list.context != key {
		return modelList{}, false
	}
	return *p.list, time.Since(p.list.at) < modelsFresh
}

// learn keeps what a session's options say of the models: initial (a new
// session's, before Hermesh changes them) also says the agent's own model
// and effort. Efforts known of models it does not say them for are kept.
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
		if k := slices.IndexFunc(old.models, func(x provider.Model) bool { return x.ID == m.ID }); k >= 0 && m.Efforts == nil {
			models[i].Efforts = old.models[k].Efforts
		}
	}
	if !initial {
		own = old.own
	}
	p.list = &modelList{context: key, models: models, own: own, state: provider.ModelsKnown, at: time.Now()}
}

// fillModels gives info the agent's models: as read within modelsFresh,
// else read now (readModels).
func (p *Provider) fillModels(ctx context.Context, info *provider.Info, bin string, pre, env []string) {
	key := modelContext(bin, env)
	l, fresh := p.models(key)
	if !fresh {
		state := p.readModels(ctx, key, bin, pre, env)
		if l, fresh = p.models(key); !fresh {
			l = modelList{state: state, at: time.Now()}
			if state == provider.ModelsUnsupported {
				p.modelsMu.Lock()
				p.list = &modelList{context: key, state: state, at: l.at}
				p.modelsMu.Unlock()
			}
		}
	}
	info.Models, info.Own, info.ModelsState, info.ModelsAt = slices.Clone(l.models), l.own, l.state, l.at
	info.AllModels = len(l.models) > 0
	if l.state == provider.ModelsAuthRequired {
		info.LoggedIn = false
	}
}

// readModels reads the agent's models without a prompt, in an empty folder:
// session/options when the agent offers it; else in a new session, never
// prompted, each model selected once to read its efforts, then closed when
// the agent can close sessions (one it cannot is left empty in its list).
// It says the state when it learned nothing.
func (p *Provider) readModels(ctx context.Context, key, bin string, pre, env []string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "hermesh-models-")
	if err != nil {
		return provider.ModelsError
	}
	defer os.RemoveAll(dir)
	proc, err := provider.StartProc(ctx, bin, append(slices.Clone(pre), p.Args...), dir, env)
	if err != nil {
		return provider.ModelsError
	}
	defer proc.Stop(time.Second)
	s := &session{name: p.Name(), proc: proc, ctx: ctx, events: make(chan provider.Event, 256), pending: map[int64]func(rpcResult){},
		known: map[string]*toolCall{}, calls: map[string]provider.Decision{}, finished: map[string]bool{},
		gate: func(context.Context, provider.ToolAction) provider.Decision {
			return provider.Decision{Reason: "reading the models only"}
		}}
	go s.read()
	failed := func(err error) string {
		var rpc *rpcError
		if errors.As(err, &rpc) && rpc.Code == codeAuthRequired {
			return provider.ModelsAuthRequired
		}
		return provider.ModelsError // never shown: it may hold credentials
	}
	res, err := s.call(ctx, "initialize", map[string]any{"protocolVersion": 1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "hermesh", "version": "0.1.0"}})
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
			return provider.ModelsAuthRequired
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
			return provider.ModelsUnsupported
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
		return provider.ModelsUnsupported
	}
	p.learn(key, o, true)
	if o.model != nil && o.thought != nil {
		for _, c := range o.model.values() {
			if c.Value == o.model.current() {
				continue
			}
			res, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": r.SessionID, "configId": o.model.ID, "value": c.Value})
			if err != nil {
				break // the efforts read so far stand
			}
			p.learn(key, optionsOf(res), false)
		}
	}
	if offered(caps.Close) {
		s.call(ctx, "session/close", map[string]any{"sessionId": r.SessionID})
	}
	return ""
}
