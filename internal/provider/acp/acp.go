// Package acp drives coding agents that speak the Agent Client Protocol
// (JSON-RPC 2.0 over stdio, one message per line): Gemini CLI, Cursor,
// Copilot, OpenCode, Qwen Code, Goose and others (see Agents).
//
// The gate sees (and audits) every tool call, and answers the agent's
// permission requests.
package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

// Agent describes how to start one ACP agent.
type Agent struct {
	Name    string   // provider name ("gemini")
	Command string   // binary looked up on PATH
	Args    []string // arguments that serve ACP on stdio
	// Paths are extra install locations; relative ones are under $HOME.
	Paths []string
	// Apps are desktop apps whose own copy of the CLI is used when there is
	// no other.
	Apps []App
	// DesktopApps are the vendor's desktop apps that don't bring this CLI:
	// installed without it, the agent says the CLI is what is missing.
	DesktopApps []string
	// Auth lists `authenticate` method ids in order of preference. When
	// set, the first one the agent offers is sent before any session call.
	Auth []string
}

// App is a desktop app (macOS, Electron) that keeps the agent's CLI as a
// script its executable runs as Node; paths are inside the .app, which is
// looked for in ~/Applications, then /Applications.
type App struct {
	Bundle, Exec, Script string
}

type Provider struct {
	Agent
	// PathEnv is the PATH used to find binaries ("": the login shell's).
	PathEnv string
	// Binary overrides discovery (tests use a fake agent).
	Binary string
}

func (p *Provider) Name() string { return p.Agent.Name }

func (p *Provider) binary() string {
	if p.Binary != "" {
		return p.Binary
	}
	home, _ := os.UserHomeDir()
	extra := []string{"/opt/homebrew/bin/" + p.Command, "/usr/local/bin/" + p.Command, filepath.Join(home, ".local/bin", p.Command)}
	for _, x := range p.Paths {
		if !filepath.IsAbs(x) {
			x = filepath.Join(home, x)
		}
		extra = append(extra, x)
	}
	return provider.LookPath(p.Command, provider.SearchPath(p.PathEnv), extra...)
}

// command is how to start the CLI: its binary, the arguments ahead of the
// agent's own and extra environment (bin is "" when there is none).
func (p *Provider) command() (bin string, pre, env []string) {
	if bin := p.binary(); bin != "" {
		return bin, nil, nil
	}
	home, _ := os.UserHomeDir()
	for _, a := range p.Apps {
		for _, dir := range []string{filepath.Join(home, "Applications"), "/Applications"} {
			app := filepath.Join(dir, a.Bundle)
			exe, script := filepath.Join(app, a.Exec), filepath.Join(app, a.Script)
			if _, err := os.Stat(script); err == nil && provider.LookPath("", "", exe) != "" {
				return exe, []string{script}, []string{"ELECTRON_RUN_AS_NODE=1"}
			}
		}
	}
	return "", nil, nil
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+[-\w.]*`)

func (p *Provider) Discover(ctx context.Context) provider.Info {
	info := provider.Info{Name: p.Name()}
	bin, pre, env := p.command()
	if !provider.Exists(bin) {
		info.Problem, info.Detail = provider.ProblemNotFound, p.Command+" not found"
		if app := provider.AppWithoutCLI(p.DesktopApps...); app != "" && p.Binary == "" {
			info.Problem, info.Detail = provider.ProblemAppWithoutCLI, app
		}
		return info
	}
	info.Binary = bin
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, problem, detail := provider.Probe(ctx, append(provider.ProbeEnv(p.PathEnv), env...), bin, append(pre, "--version")...)
	if problem != provider.ProblemNone {
		info.Problem, info.Detail = problem, detail
		return info
	}
	info.Available = true
	info.Version = versionRE.FindString(string(out))
	// ACP has no way to ask for the login state before a session; a
	// missing login fails the session with the agent's own message.
	info.LoggedIn = true
	return info
}

// withPath sets PATH so node-based CLIs find `node` (version probes run
// outside a session environment).
func withPath(env []string, path string) []string {
	if path == "" {
		return env
	}
	return append(shellenv.Without(env, "PATH"), "PATH="+path)
}

func (p *Provider) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	bin, pre, env := p.command()
	if bin == "" {
		return nil, errors.New(p.Command + " not found")
	}
	if env != nil {
		if spec.Env == nil {
			spec.Env = os.Environ()
		}
		spec.Env = append(slices.Clip(spec.Env), env...)
	}
	proc, err := provider.StartProc(ctx, bin, append(pre, p.Args...), spec.WorkDir, spec.Env)
	if err != nil {
		return nil, err
	}
	s := &session{name: p.Name(), proc: proc, events: make(chan provider.Event, 256), pending: map[int64]func(rpcResult){},
		gate: spec.Gate, ctx: ctx, known: map[string]*toolCall{}, calls: map[string]provider.Decision{}, finished: map[string]bool{}}
	go s.read()
	fail := func(err error) (provider.Session, error) {
		proc.Stop(time.Second)
		return nil, fmt.Errorf("%s: %w", p.Name(), err)
	}
	// Node-based agents can take a while to boot and load their config.
	hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := s.call(hctx, "initialize", map[string]any{"protocolVersion": 1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false,
			// Questions as forms (elicitation/create); agents without it
			// turn their ask-the-user tools off.
			"elicitation": map[string]any{"form": map[string]any{}}},
		"clientInfo": map[string]any{"name": "hermesh", "version": "0.1.0"}})
	if err != nil {
		return fail(fmt.Errorf("initialize: %w", classified(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED)))
	}
	var init struct {
		AgentCapabilities struct {
			LoadSession        bool `json:"loadSession"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	json.Unmarshal(res, &init)
	s.images = init.AgentCapabilities.PromptCapabilities.Image
	if m := authMethod(p.Auth, init.AuthMethods); m != "" {
		if _, err := s.call(hctx, "authenticate", map[string]any{"methodId": m}); err != nil {
			return fail(provider.Fail(sessionv1.FailureKind_FAILURE_KIND_AUTH, fmt.Sprintf("authenticate (%s): %v; run `%s` once as this user to log in", m, err, p.Command)))
		}
	}
	// The session/new, /resume or /load result (it lists config options).
	res = nil
	servers := []any{}
	for _, m := range spec.MCPServers {
		env := []any{}
		for k, v := range m.Env {
			env = append(env, map[string]any{"name": k, "value": v})
		}
		servers = append(servers, map[string]any{"name": m.Name, "command": m.Command, "args": m.Args, "env": env})
	}
	params := map[string]any{"cwd": spec.WorkDir, "mcpServers": servers}
	caps := init.AgentCapabilities
	switch {
	case spec.ResumeRef == "":
		res, err = s.call(hctx, "session/new", params)
		if err != nil {
			return fail(fmt.Errorf("session/new: %w", classified(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED)))
		}
		var r struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(res, &r)
		if r.SessionID == "" {
			return fail(errors.New("session/new returned no session id"))
		}
		s.sessionID = r.SessionID
	case len(caps.SessionCapabilities.Resume) > 0 && string(caps.SessionCapabilities.Resume) != "null":
		params["sessionId"] = spec.ResumeRef
		if res, err = s.call(hctx, "session/resume", params); err != nil {
			return fail(fmt.Errorf("session/resume: %w", resumeError(err)))
		}
		s.sessionID = spec.ResumeRef
	case caps.LoadSession:
		// session/load replays the conversation as session/update
		// notifications before it answers; they are not new events.
		params["sessionId"] = spec.ResumeRef
		s.replaying.Store(true)
		res, err = s.call(hctx, "session/load", params)
		s.replaying.Store(false)
		if err != nil {
			return fail(fmt.Errorf("session/load: %w", resumeError(err)))
		}
		s.sessionID = spec.ResumeRef
	default:
		return fail(provider.Fail(sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, "the agent cannot resume sessions"))
	}
	s.emit(provider.Event{Kind: provider.EventRef, Ref: s.sessionID})
	if spec.Model != "" {
		if err := s.setModel(hctx, res, spec.Model); err != nil {
			return fail(provider.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, fmt.Sprintf("model %q: %v", spec.Model, err)))
		}
	}
	if err := s.Send(ctx, provider.Input{Text: spec.Prompt, Images: spec.Images}); err != nil {
		return fail(err)
	}
	return s, nil
}

// setModel selects the session's model through its "model" config option
// when the session offers one, else through session/set_model. An unknown
// model fails the start: running on a different one silently is worse.
func (s *session) setModel(ctx context.Context, session json.RawMessage, model string) error {
	type choice struct {
		Value   string   `json:"value"`
		Options []choice `json:"options"` // a group of choices
	}
	var r struct {
		ConfigOptions []struct {
			ID       string   `json:"id"`
			Category string   `json:"category"`
			Options  []choice `json:"options"`
		} `json:"configOptions"`
	}
	json.Unmarshal(session, &r)
	for _, o := range r.ConfigOptions {
		if o.Category != "model" && o.ID != "model" {
			continue
		}
		var values []string
		var walk func([]choice)
		walk = func(cs []choice) {
			for _, c := range cs {
				if c.Value != "" {
					values = append(values, c.Value)
				}
				walk(c.Options)
			}
		}
		walk(o.Options)
		if !contains(values, model) {
			return fmt.Errorf("not offered by the agent (available: %s)", strings.Join(values, ", "))
		}
		_, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.sessionID, "configId": o.ID, "value": model})
		return err
	}
	_, err := s.call(ctx, "session/set_model", map[string]any{"sessionId": s.sessionID, "modelId": model})
	return err
}

func authMethod(want []string, offered []struct {
	ID string `json:"id"`
}) string {
	for _, w := range want {
		for _, o := range offered {
			if o.ID == w {
				return w
			}
		}
	}
	return ""
}

// rpcError is a JSON-RPC error from the agent.
type rpcError struct {
	Code    int
	Message string
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// ACP error codes (agentclientprotocol.com/protocol/schema#errorcode).
const (
	codeAuthRequired     = -32000
	codeResourceNotFound = -32002
)

// classified gives an agent error its failure kind: auth_required by code,
// otherwise by text, else fallback.
func classified(err error, fallback sessionv1.FailureKind) error {
	var re *rpcError
	if errors.As(err, &re) && re.Code == codeAuthRequired {
		return provider.Fail(sessionv1.FailureKind_FAILURE_KIND_AUTH, err.Error())
	}
	f := provider.FailureOf(err, fallback)
	return &provider.FailureError{Failure: *f}
}

// resumeError classifies a session/load or session/resume error: only a
// not-found code or wording counts as a refused resume (a timeout, a
// login or an unreachable MCP server does not).
func resumeError(err error) error {
	var re *rpcError
	if errors.As(err, &re) && (re.Code == codeResourceNotFound || provider.IsResumeRejected(re.Message)) {
		return provider.Fail(sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, err.Error())
	}
	return classified(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED)
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type session struct {
	name      string
	proc      *provider.Proc
	events    chan provider.Event
	gate      provider.Gate
	ctx       context.Context
	sessionID string
	// images: the agent takes image content blocks (promptCapabilities).
	images    bool
	replaying atomic.Bool
	// closeMu orders emits from Start against the reader closing events.
	closeMu sync.RWMutex
	closed  bool

	mu      sync.Mutex
	nextID  int64
	pending map[int64]func(rpcResult)
	// The current turn: streamed message text not yet emitted, and the
	// last complete message (the turn's result).
	msg  strings.Builder
	last string
	// Tool calls seen this session: what is known so far (agents announce
	// a call before filling in its input), the gate's decision, and
	// whether the result was reported.
	known    map[string]*toolCall
	calls    map[string]provider.Decision
	finished map[string]bool
	// forms are elicitation requests waiting for an answer.
	forms map[string]form
}

// form is a pending elicitation: its request id and fields in order.
type form struct {
	reqID  json.RawMessage
	fields []formField
}

type formField struct {
	name, typ string
	multi     bool
	values    map[string]any // option label -> value
}

// elicit turns a form elicitation into a question (URL mode is not
// offered, so not taken).
func (s *session) elicit(id json.RawMessage, params json.RawMessage) error {
	var p struct {
		Mode            string `json:"mode"`
		Message         string `json:"message"`
		RequestedSchema struct {
			Properties json.RawMessage `json:"properties"`
			Required   []string        `json:"required"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	if p.Mode != "" && p.Mode != "form" {
		return fmt.Errorf("elicitation mode %q is not supported", p.Mode)
	}
	names, props, err := orderedObject(p.RequestedSchema.Properties)
	if err != nil || len(names) == 0 {
		return errors.New("the form has no fields")
	}
	q := &provider.Question{ID: "form:" + string(id)}
	f := form{reqID: id}
	for i, name := range names {
		var sc struct {
			Type        string `json:"type"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Enum        []any  `json:"enum"`
			OneOf       []struct {
				Const any    `json:"const"`
				Title string `json:"title"`
			} `json:"oneOf"`
			Items struct {
				Enum  []any `json:"enum"`
				AnyOf []struct {
					Const any    `json:"const"`
					Title string `json:"title"`
				} `json:"anyOf"`
			} `json:"items"`
		}
		json.Unmarshal(props[name], &sc)
		it := provider.QuestionItem{ID: name, Header: firstNonEmpty(sc.Title, name), Text: firstNonEmpty(sc.Description, sc.Title, name)}
		if i == 0 && p.Message != "" {
			it.Text = strings.TrimSpace(p.Message + "\n" + sc.Description)
		}
		ff := formField{name: name, typ: sc.Type, values: map[string]any{}}
		add := func(label string, v any) {
			if label == "" {
				label = fmt.Sprint(v)
			}
			it.Options = append(it.Options, provider.Option{Label: label})
			ff.values[label] = v
		}
		switch {
		case sc.Type == "array":
			ff.multi, it.MultiSelect = true, true
			for _, e := range sc.Items.Enum {
				add("", e)
			}
			for _, o := range sc.Items.AnyOf {
				add(o.Title, o.Const)
			}
		case sc.Type == "boolean":
			add("Yes", true)
			add("No", false)
		default:
			for _, e := range sc.Enum {
				add("", e)
			}
			for _, o := range sc.OneOf {
				add(o.Title, o.Const)
			}
		}
		it.AllowOther = len(it.Options) == 0
		it.Optional = !slices.Contains(p.RequestedSchema.Required, name)
		q.Items = append(q.Items, it)
		f.fields = append(f.fields, ff)
	}
	s.mu.Lock()
	if s.forms == nil {
		s.forms = map[string]form{}
	}
	s.forms[q.ID] = f
	s.mu.Unlock()
	s.emit(provider.Event{Kind: provider.EventQuestion, Question: q})
	return nil
}

// Answer fills in the form (accept), or declines it.
func (s *session) Answer(ctx context.Context, id string, r provider.Reply) error {
	s.mu.Lock()
	f, ok := s.forms[id]
	delete(s.forms, id)
	s.mu.Unlock()
	if !ok {
		return errors.New("no such question")
	}
	result := map[string]any{"action": "decline"}
	if !r.Declined {
		content := map[string]any{}
		for _, a := range r.Answers {
			for _, ff := range f.fields {
				if ff.name == a.ItemID {
					content[ff.name] = ff.value(a)
				}
			}
		}
		result = map[string]any{"action": "accept", "content": content}
	}
	return s.write(map[string]any{"jsonrpc": "2.0", "id": f.reqID, "result": result})
}

// value is an answer in the field's schema type.
func (ff formField) value(a provider.Answer) any {
	if ff.multi {
		out := []any{}
		for _, l := range a.Selected {
			out = append(out, ff.values[l])
		}
		return out
	}
	if len(a.Selected) > 0 {
		return ff.values[a.Selected[0]]
	}
	t := strings.TrimSpace(a.Text)
	switch ff.typ {
	case "integer":
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
	case "number":
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return n
		}
	}
	return t
}

// orderedObject decodes a JSON object keeping its keys' order.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("not an object")
	}
	var names []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		names = append(names, k)
		vals[k] = v
	}
	return names, vals, nil
}

func (s *session) Events() <-chan provider.Event { return s.events }
func (s *session) Done() <-chan struct{}         { return s.proc.Done() }
func (s *session) Err() error                    { return s.proc.Err() }
func (s *session) Pgid() int                     { return s.proc.Pgid() }
func (s *session) LastActivity() time.Time       { return s.proc.LastOutput() }
func (s *session) Close() error                  { s.proc.Stop(3 * time.Second); return nil }

func (s *session) emit(e provider.Event) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.events <- e:
	case <-s.proc.Done():
	}
}

func (s *session) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.proc.WriteLine(b)
}

// request sends a request; done runs on the reader goroutine with the
// response.
func (s *session) request(method string, params any, done func(rpcResult)) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pending[id] = done
	s.mu.Unlock()
	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ch := make(chan rpcResult, 1)
	if err := s.request(method, params, func(r rpcResult) { ch <- r }); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.proc.Done():
		return nil, fmt.Errorf("exited: %v", s.proc.Err())
	}
}

func (s *session) Send(ctx context.Context, in provider.Input) error {
	s.mu.Lock()
	s.msg.Reset()
	s.last = ""
	s.mu.Unlock()
	prompt := []any{map[string]any{"type": "text", "text": in.Text}}
	for _, img := range in.Images {
		if !s.images {
			break // the text lists the files by path
		}
		if b, err := os.ReadFile(img.Path); err == nil {
			prompt = append(prompt, map[string]any{"type": "image", "mimeType": img.MediaType, "data": base64.StdEncoding.EncodeToString(b)})
		}
	}
	return s.request("session/prompt", map[string]any{"sessionId": s.sessionID, "prompt": prompt}, s.turnDone)
}

// Steer is not part of ACP: a session takes one prompt per turn.
func (s *session) Steer(context.Context, provider.Input) error {
	return errors.New(s.name + " (ACP) takes input only between turns")
}

func (s *session) Interrupt(ctx context.Context) error {
	return s.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": s.sessionID}})
}

// turnDone handles the session/prompt response, which ends the turn.
func (s *session) turnDone(r rpcResult) {
	s.flush()
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	e := provider.Event{Kind: provider.EventTurnCompleted, Text: last}
	if r.err != nil {
		e.IsError, e.Text = true, r.err.Error()
		e.Failure = provider.FailureOf(classified(r.err, sessionv1.FailureKind_FAILURE_KIND_UNKNOWN), sessionv1.FailureKind_FAILURE_KIND_UNKNOWN)
		s.emit(e)
		return
	}
	var p struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens       uint64 `json:"inputTokens"`
			OutputTokens      uint64 `json:"outputTokens"`
			CachedReadTokens  uint64 `json:"cachedReadTokens"`
			CachedWriteTokens uint64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}
	json.Unmarshal(r.result, &p)
	e.Usage = provider.Usage{InputTokens: p.Usage.InputTokens + p.Usage.CachedReadTokens + p.Usage.CachedWriteTokens, OutputTokens: p.Usage.OutputTokens}
	switch p.StopReason {
	case "end_turn", "":
	case "cancelled":
		e.IsError, e.Text = true, "interrupted"
	default: // max_tokens, max_turn_requests, refusal
		e.IsError = true
		e.Text = strings.TrimSpace("stopped (" + p.StopReason + ") " + last)
		kind := sessionv1.FailureKind_FAILURE_KIND_LIMIT_REACHED
		if p.StopReason == "refusal" {
			kind = sessionv1.FailureKind_FAILURE_KIND_REFUSED
		}
		e.Failure = &provider.Failure{Kind: kind, Message: e.Text}
	}
	s.emit(e)
}

// flush emits the streamed message text as one agent message.
func (s *session) flush() {
	s.mu.Lock()
	text := s.msg.String()
	s.msg.Reset()
	if strings.TrimSpace(text) != "" {
		s.last = text
	}
	s.mu.Unlock()
	if strings.TrimSpace(text) != "" {
		s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: text})
	}
}

type message struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result json.RawMessage  `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *session) read() {
	defer func() {
		s.closeMu.Lock()
		s.closed = true
		close(s.events)
		s.closeMu.Unlock()
	}()
	s.proc.Lines(func(b []byte) {
		var m message
		if json.Unmarshal(b, &m) != nil {
			return
		}
		switch {
		case m.ID != nil && m.Method == "": // response
			var id int64
			json.Unmarshal(*m.ID, &id)
			s.mu.Lock()
			done := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if done != nil {
				r := rpcResult{result: m.Result}
				if m.Error != nil {
					r.err = &rpcError{Code: m.Error.Code, Message: m.Error.Message}
				}
				done(r)
			}
		case m.ID != nil:
			s.handleRequest(*m.ID, m.Method, m.Params)
		case m.Method == "session/update":
			if !s.replaying.Load() {
				s.handleUpdate(m.Params)
			}
		}
	})
}

// toolCall is an ACP ToolCall (or the fields of a ToolCallUpdate).
type toolCall struct {
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations"`
	RawInput  json.RawMessage `json:"rawInput"`
	RawOutput json.RawMessage `json:"rawOutput"`
	Content   []struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"content"`
}

func (s *session) handleUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
			toolCall
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &c) == nil && c.Type == "text" {
			s.mu.Lock()
			s.msg.WriteString(c.Text)
			s.mu.Unlock()
			s.emit(provider.Event{Kind: provider.EventAgentDraft, Text: c.Text})
		}
	case "tool_call", "tool_call_update":
		// ToolCall.content is an array; agent_message_chunk's is a block.
		json.Unmarshal(u.Content, &u.toolCall.Content)
		s.flush()
		tc := s.merge(u.toolCall)
		// Decide once the call has its input, or at the latest when it runs.
		done := u.Status == "completed" || u.Status == "failed"
		if hasInput(tc.RawInput) || len(tc.Locations) > 0 || u.Status == "in_progress" || done {
			s.decide(tc)
		}
		if done {
			s.result(u.toolCall)
		}
	}
}

// merge folds an update into what is known about its tool call.
func (s *session) merge(u toolCall) toolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	tc := s.known[u.ToolCallID]
	if tc == nil {
		tc = &toolCall{ToolCallID: u.ToolCallID}
		s.known[u.ToolCallID] = tc
	}
	if u.Title != "" {
		tc.Title = u.Title
	}
	if u.Kind != "" {
		tc.Kind = u.Kind
	}
	if u.Status != "" {
		tc.Status = u.Status
	}
	if len(u.Locations) > 0 {
		tc.Locations = u.Locations
	}
	if hasInput(u.RawInput) {
		tc.RawInput = u.RawInput
	}
	if len(u.Content) > 0 {
		tc.Content = u.Content
	}
	return *tc
}

// hasInput reports whether raw carries a tool input (agents announce calls
// with an empty one).
func hasInput(raw json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		return len(m) > 0
	}
	return len(raw) > 0 && string(raw) != "null"
}

// decide runs the gate once per tool call and reports the call.
func (s *session) decide(tc toolCall) provider.Decision {
	s.mu.Lock()
	d, seen := s.calls[tc.ToolCallID]
	s.mu.Unlock()
	if seen {
		return d
	}
	a := Classify(tc)
	d = provider.Decision{Allow: true}
	if s.gate != nil {
		d = s.gate(s.ctx, a)
	}
	s.mu.Lock()
	s.calls[tc.ToolCallID] = d
	s.mu.Unlock()
	s.emit(provider.Event{Kind: provider.EventToolCall, CallID: a.CallID, ToolKind: a.Kind, ToolName: a.Name, Summary: a.Summary, Decision: &d})
	return d
}

func (s *session) result(tc toolCall) {
	s.mu.Lock()
	if s.finished[tc.ToolCallID] {
		s.mu.Unlock()
		return
	}
	s.finished[tc.ToolCallID] = true
	s.mu.Unlock()
	var b strings.Builder
	for _, c := range tc.Content {
		if c.Type == "content" && c.Content.Type == "text" {
			b.WriteString(c.Content.Text)
		} else if c.Type == "diff" {
			b.WriteString("edited " + c.Path + "\n")
		}
	}
	out := b.String()
	if out == "" && len(tc.RawOutput) > 0 && string(tc.RawOutput) != "null" {
		out = rawText(tc.RawOutput)
	}
	if out == "" {
		out = tc.Status
	}
	sum, trunc := provider.KeepOutput(out)
	s.emit(provider.Event{Kind: provider.EventToolResult, CallID: tc.ToolCallID, Summary: sum, IsError: tc.Status == "failed", Truncated: trunc})
}

func rawText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var o struct {
		Output string `json:"output"`
		Stdout string `json:"stdout"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Output+o.Stdout != "" {
		return o.Output + o.Stdout
	}
	return string(raw)
}

func (s *session) handleRequest(id json.RawMessage, method string, params json.RawMessage) {
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	switch method {
	case "session/request_permission":
		var p struct {
			ToolCall toolCall `json:"toolCall"`
			Options  []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		json.Unmarshal(params, &p)
		d := s.decide(s.merge(p.ToolCall))
		// Never pick allow_always/reject_always: the agent would remember
		// the choice beyond this session.
		want := "reject_once"
		if d.Allow {
			want = "allow_once"
		}
		for _, o := range p.Options {
			if o.Kind == want {
				reply["result"] = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}
				break
			}
		}
		if reply["result"] == nil {
			reply["error"] = map[string]any{"code": -32603, "message": "Hermesh: no " + want + " option offered"}
		}
	case "elicitation/create", "session/create_elicitation":
		if err := s.elicit(id, params); err != nil {
			reply["error"] = map[string]any{"code": -32602, "message": "Hermesh: " + err.Error()}
			break
		}
		return // answered by Answer
	default:
		// fs/*, terminal/* (not advertised) and vendor extensions.
		reply["error"] = map[string]any{"code": -32601, "message": "method not supported by Hermesh: " + method}
	}
	s.write(reply)
}

// Classify maps an ACP tool call to a normalized action.
func Classify(tc toolCall) provider.ToolAction {
	a := provider.ToolAction{CallID: tc.ToolCallID, Name: tc.Kind, Summary: tc.Title, Raw: tc.RawInput}
	if a.Name == "" {
		a.Name = "other"
	}
	for _, l := range tc.Locations {
		a.Paths = append(a.Paths, l.Path)
	}
	for _, c := range tc.Content {
		if c.Type == "diff" && c.Path != "" && !contains(a.Paths, c.Path) {
			a.Paths = append(a.Paths, c.Path)
		}
	}
	switch tc.Kind {
	case "read":
		a.Kind = provider.ToolRead
	case "edit", "delete", "move":
		a.Kind = provider.ToolEdit
	case "search":
		a.Kind = provider.ToolSearch
	case "execute":
		a.Kind = provider.ToolShell
		a.Command = command(tc.RawInput)
		if a.Command == "" {
			a.Command = tc.Title
		}
	case "fetch":
		a.Kind = provider.ToolNetwork
	default:
		a.Kind = provider.ToolOther
	}
	if a.Summary == "" {
		a.Summary = strings.Join(a.Paths, ", ")
	}
	a.Summary, _ = provider.Truncate(a.Summary, 500)
	return a
}

func command(raw json.RawMessage) string {
	var in struct {
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(raw, &in) != nil || len(in.Command) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(in.Command, &str) == nil {
		return str
	}
	var parts []string
	if json.Unmarshal(in.Command, &parts) == nil {
		return strings.Join(parts, " ")
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
