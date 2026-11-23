// Package acp implements the unified Hermesh v2 runtime.Driver for Agent Client
// Protocol (ACP) JSON-RPC 2.0 stdio servers (Gemini CLI, Antigravity, Cursor,
// OpenCode, Copilot, Kiro, Qwen, Goose, and custom user ACP servers).
package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type Flavor struct {
	Name    string
	Command string
	Args    []string
	Paths   []string
}

var builtinFlavors = map[string]Flavor{
	"gemini":      {Name: "gemini", Command: "gemini", Args: []string{"--acp"}},
	"antigravity": {Name: "antigravity", Command: "agy", Args: []string{"acp"}},
	"cursor":      {Name: "cursor", Command: "cursor-agent", Args: []string{"acp"}},
	"opencode":    {Name: "opencode", Command: "opencode", Args: []string{"acp"}, Paths: []string{".opencode/bin/opencode"}},
	"copilot":     {Name: "copilot", Command: "copilot", Args: []string{"--acp"}},
	"kiro":        {Name: "kiro", Command: "kiro-cli", Args: []string{"acp"}},
	"qwen":        {Name: "qwen", Command: "qwen", Args: []string{"--acp"}},
	"goose":       {Name: "goose", Command: "goose", Args: []string{"acp"}},
}

type Driver struct {
	PathEnv string
}

func NewDriver(pathEnv string) *Driver { return &Driver{PathEnv: pathEnv} }

func (d *Driver) Kind() string { return "acp" }

func (d *Driver) Capabilities(_ string) *threadv2.ProviderCapabilities {
	return &threadv2.ProviderCapabilities{
		InSessionModelSwitch:  true,
		InSessionOptionSwitch: true,
		InSessionModeSwitch:   true,
		MidTurnSteer:          false, // ACP processes one session/prompt per turn
		LiveApproval:          true,
		LiveUserInput:         true,
		NativeResume:          true,
		SupportsImages:        true,
		SupportsFiles:         true,
		ReportsContextWindow:  true,
		SupportedRuntimeModes: []identityv1.RuntimeMode{
			identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
			identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
			identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
			identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		},
	}
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+[-\w.]*`)

func (d *Driver) resolveLaunch(cfg *threadv2.ProviderInstanceConfig) (bin string, args []string) {
	flavorName := "gemini"
	if cfg != nil && cfg.AcpFlavor != "" {
		flavorName = cfg.AcpFlavor
	}
	flv := builtinFlavors[flavorName]
	if cfg != nil && cfg.BinaryPath != "" {
		if len(cfg.LaunchArgs) > 0 {
			return cfg.BinaryPath, slices.Clone(cfg.LaunchArgs)
		}
		return cfg.BinaryPath, slices.Clone(flv.Args)
	}
	cmdName := flv.Command
	if cmdName == "" {
		cmdName = flavorName
	}
	home, _ := os.UserHomeDir()
	var extra []string
	extra = append(extra, "/opt/homebrew/bin/"+cmdName, "/usr/local/bin/"+cmdName, filepath.Join(home, ".local/bin", cmdName))
	for _, rel := range flv.Paths {
		extra = append(extra, filepath.Join(home, rel))
	}
	found := provider.LookPath(cmdName, provider.SearchPath(d.PathEnv), extra...)
	launchArgs := slices.Clone(flv.Args)
	if cfg != nil && len(cfg.LaunchArgs) > 0 {
		launchArgs = slices.Clone(cfg.LaunchArgs)
	}
	return found, launchArgs
}

func (d *Driver) Probe(ctx context.Context, cfg *threadv2.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	bin, _ := d.resolveLaunch(cfg)
	if !provider.Exists(bin) {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "ACP CLI not found on PATH",
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv)
	out, prob, detail := provider.Probe(pctx, env, bin, "--version")
	if prob != provider.ProblemNone {
		return runtime.ProbeResult{
			Installed:            true,
			Compatibility:        threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_BROKEN,
			CompatibilityMessage: detail,
		}
	}
	return runtime.ProbeResult{
		Installed:     true,
		Authenticated: true,
		Version:       versionRE.FindString(string(out)),
		Compatibility: threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	bin, args := d.resolveLaunch(spec.Instance)
	if !provider.Exists(bin) {
		return nil, errors.New("ACP binary not found")
	}
	var instEnv map[string]string
	if spec.Instance != nil {
		instEnv = spec.Instance.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv, spec.ExtraEnv)
	proc, err := provider.StartProc(ctx, bin, args, spec.Workdir, env)
	if err != nil {
		return nil, err
	}
	s := &session{
		proc:      proc,
		spec:      spec,
		events:    make(chan runtime.Event, 256),
		pending:   map[int64]func(rpcRes){},
		terminals: map[string]*managedTerm{},
		tools:     map[string]*acpToolState{},
		turnCtx:   ctx,
	}
	s.alive.Store(true)
	go s.readLoop()

	initRes, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":          map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal":    true,
			"elicitation": map[string]any{"form": map[string]any{}},
		},
		"clientInfo": map[string]any{"name": "hermesh", "version": "2.0.0"},
	})
	if err != nil {
		proc.Stop(time.Second)
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	var caps struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(initRes, &caps)
	s.supportsImages = caps.AgentCapabilities.PromptCapabilities.Image

	if authID := pickAuthMethod(caps.AuthMethods, env); authID != "" {
		_, _ = s.call(ctx, "authenticate", map[string]any{"methodId": authID})
	}

	canResume := len(caps.AgentCapabilities.SessionCapabilities.Resume) > 0 &&
		string(bytes.TrimSpace(caps.AgentCapabilities.SessionCapabilities.Resume)) != "null"

	params := map[string]any{"cwd": spec.Workdir, "mcpServers": []any{}}
	var sessRes json.RawMessage
	if spec.ResumeCursor != "" {
		params["sessionId"] = spec.ResumeCursor
		var resumeErr error
		if canResume {
			sessRes, resumeErr = s.call(ctx, "session/resume", params)
		}
		if (resumeErr != nil || !canResume) && caps.AgentCapabilities.LoadSession {
			s.replaying.Store(true)
			sessRes, resumeErr = s.call(ctx, "session/load", params)
			s.replaying.Store(false)
		} else if !canResume && !caps.AgentCapabilities.LoadSession {
			resumeErr = errors.New("acp agent does not support session resume or load")
		}
		if resumeErr != nil {
			proc.Stop(time.Second)
			return nil, fmt.Errorf("acp session resume: %w", resumeErr)
		}
		s.sessionID = spec.ResumeCursor
	} else {
		newRes, newErr := s.call(ctx, "session/new", params)
		if newErr != nil {
			proc.Stop(time.Second)
			return nil, fmt.Errorf("acp session/new: %w", newErr)
		}
		sessRes = newRes
		var nr struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(newRes, &nr)
		if nr.SessionID == "" {
			proc.Stop(time.Second)
			return nil, errors.New("acp session/new returned empty sessionId")
		}
		s.sessionID = nr.SessionID
	}
	s.parseSessionConfigOptions(sessRes)

	s.emit(runtime.Event{
		Kind:         runtime.EventNativeSession,
		NativeID:     s.sessionID,
		ResumeCursor: s.sessionID,
	})
	if spec.Selection != nil {
		_ = s.SetSelection(ctx, spec.Selection)
	}
	_ = s.SetModes(ctx, spec.EffectiveMode, spec.InteractionMode)
	return s, nil
}

func pickAuthMethod(methods []struct {
	ID string `json:"id"`
}, env []string) string {
	has := func(id string) bool {
		for _, m := range methods {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if has("cursor_login") {
		return "cursor_login"
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "XAI_API_KEY=") && len(kv) > len("XAI_API_KEY=") && has("xai.api_key") {
			return "xai.api_key"
		}
	}
	if has("cached_token") {
		return "cached_token"
	}
	return ""
}

type acpToolState struct {
	id              string
	title           string
	kind            string
	status          string
	command         string
	filePath        string
	output          string
	diffNewText     string
	rawInput        json.RawMessage
	lastArtifactSig string
	started         bool
}

type rpcRes struct {
	result json.RawMessage
	err    error
}

type managedTerm struct {
	cmd    *exec.Cmd
	outBuf bytes.Buffer
	mu     sync.Mutex
	done   chan struct{}
	exit   int
}

type session struct {
	proc            *provider.Proc
	spec            runtime.StartSpec
	sessionID       string
	supportsImages  bool
	modelOptionID   string
	thoughtOptionID string
	replaying       atomic.Bool
	events          chan runtime.Event
	alive           atomic.Bool
	nextID          atomic.Int64
	itemSeq         atomic.Uint64
	closeOnce       sync.Once

	emitMu       sync.RWMutex
	eventsClosed bool

	mu        sync.Mutex
	pending   map[int64]func(rpcRes)
	terminals map[string]*managedTerm
	tools     map[string]*acpToolState
	turnCtx   context.Context
	msgItemID string
	msgBuf    strings.Builder
}

func (s *session) parseSessionConfigOptions(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var sr struct {
		ConfigOptions []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(raw, &sr) != nil {
		return
	}
	for _, opt := range sr.ConfigOptions {
		switch {
		case opt.Category == "model" || opt.ID == "model":
			s.modelOptionID = opt.ID
		case opt.Category == "thought_level":
			s.thoughtOptionID = opt.ID
		}
	}
}

func (s *session) flushAssistantSegment() {
	s.mu.Lock()
	if s.msgBuf.Len() == 0 {
		s.mu.Unlock()
		return
	}
	msgID := s.msgItemID
	full := s.msgBuf.String()
	s.msgBuf.Reset()
	s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
	s.mu.Unlock()

	if strings.TrimSpace(full) != "" {
		s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: msgID, Text: full})
	}
}

func (s *session) Events() <-chan runtime.Event { return s.events }
func (s *session) Alive() bool                  { return s.alive.Load() }

func (s *session) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.proc.WriteLine(b)
}

func (s *session) request(method string, params any, cb func(rpcRes)) error {
	id := s.nextID.Add(1)
	s.mu.Lock()
	s.pending[id] = cb
	s.mu.Unlock()
	if err := s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ch := make(chan rpcRes, 1)
	if err := s.request(method, params, func(r rpcRes) { ch <- r }); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.proc.Done():
		return nil, fmt.Errorf("acp exited: %v", s.proc.Err())
	}
}

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	s.mu.Lock()
	s.turnCtx = ctx
	s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
	s.msgBuf.Reset()
	s.tools = map[string]*acpToolState{}
	s.mu.Unlock()

	prompt := []any{map[string]any{"type": "text", "text": in.Prompt}}
	for _, a := range in.Attachments {
		if s.supportsImages && strings.HasPrefix(a.MimeType, "image/") && len(a.Data) > 0 {
			prompt = append(prompt, map[string]any{
				"type":     "image",
				"mimeType": a.MimeType,
				"data":     base64.StdEncoding.EncodeToString(a.Data),
			})
		} else if a.Path != "" {
			prompt = append(prompt, map[string]any{
				"type": "text",
				"text": fmt.Sprintf("[Attached: %s]", a.Path),
			})
		}
	}
	return s.request("session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    prompt,
	}, s.onPromptResult)
}

func (s *session) onPromptResult(r rpcRes) {
	s.flushAssistantSegment()
	if r.err != nil {
		s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "acp_error", ErrorMessage: r.err.Error()})
		return
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens       uint64 `json:"inputTokens"`
			OutputTokens      uint64 `json:"outputTokens"`
			CachedReadTokens  uint64 `json:"cachedReadTokens"`
			CachedWriteTokens uint64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(r.result, &res)
	inTok := res.Usage.InputTokens + res.Usage.CachedReadTokens + res.Usage.CachedWriteTokens
	if inTok > 0 || res.Usage.OutputTokens > 0 {
		s.emit(runtime.Event{
			Kind: runtime.EventUsage,
			Usage: &threadv2.ContextWindowUsage{
				InputTokens:        inTok,
				OutputTokens:       res.Usage.OutputTokens,
				CacheReadTokens:    res.Usage.CachedReadTokens,
				ContextUsedTokens:  inTok + res.Usage.OutputTokens,
				ContextLimitTokens: 200000,
			},
		})
	}
	switch res.StopReason {
	case "cancelled":
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
	case "refusal", "max_tokens", "max_turn_requests":
		s.emit(runtime.Event{
			Kind:         runtime.EventTurnFailed,
			ErrorCode:    res.StopReason,
			ErrorMessage: "acp turn stopped: " + res.StopReason,
		})
	default:
		s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
	}
}

func (s *session) Steer(context.Context, string, []runtime.TurnAttachment) error {
	return runtime.ErrSteerUnsupported
}

func (s *session) SetSelection(ctx context.Context, sel *threadv2.ModelSelection) error {
	if sel == nil {
		return nil
	}
	if sel.Model != "" && sel.Model != "default" {
		if s.modelOptionID != "" {
			_, _ = s.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": s.sessionID,
				"configId":  s.modelOptionID,
				"value":     sel.Model,
			})
		} else {
			_, _ = s.call(ctx, "session/set_model", map[string]any{"sessionId": s.sessionID, "modelId": sel.Model})
		}
	}
	for k, v := range sel.Options {
		var rawVal any
		if sv, ok := v.Kind.(*threadv2.OptionValue_StringValue); ok {
			rawVal = sv.StringValue
		} else if bv, ok := v.Kind.(*threadv2.OptionValue_BoolValue); ok {
			rawVal = bv.BoolValue
		}
		if rawVal != nil {
			cfgID := k
			if k == "reasoningEffort" && s.thoughtOptionID != "" {
				cfgID = s.thoughtOptionID
			}
			_, _ = s.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": s.sessionID,
				"configId":  cfgID,
				"value":     rawVal,
			})
		}
	}
	return nil
}

func (s *session) SetModes(ctx context.Context, _ identityv1.RuntimeMode, im threadv2.InteractionMode) error {
	modeID := "default"
	if im == threadv2.InteractionMode_INTERACTION_MODE_PLAN {
		modeID = "plan"
	}
	_, _ = s.call(ctx, "session/set_mode", map[string]any{"sessionId": s.sessionID, "modeId": modeID})
	return nil
}

func (s *session) Interrupt(context.Context) error {
	return s.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": s.sessionID},
	})
}

func (s *session) ForceStop() error {
	s.alive.Store(false)
	s.killTerminals()
	s.closeOnce.Do(func() { s.proc.Stop(0) })
	return nil
}

func (s *session) Close() error {
	s.alive.Store(false)
	s.killTerminals()
	s.closeOnce.Do(func() { s.proc.Stop(2 * time.Second) })
	return nil
}

func (s *session) killTerminals() {
	s.mu.Lock()
	terms := make([]*managedTerm, 0, len(s.terminals))
	for _, t := range s.terminals {
		terms = append(terms, t)
	}
	s.terminals = map[string]*managedTerm{}
	s.mu.Unlock()
	for _, t := range terms {
		if t.cmd != nil && t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
	}
}

func (s *session) emit(ev runtime.Event) {
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.eventsClosed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.proc.Done():
	}
}

func (s *session) closeEvents() {
	s.emitMu.Lock()
	if !s.eventsClosed {
		s.eventsClosed = true
		close(s.events)
	}
	s.emitMu.Unlock()
}

func (s *session) readLoop() {
	defer func() {
		s.alive.Store(false)
		s.closeEvents()
	}()
	_ = s.proc.Lines(func(raw []byte) {
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
			Result json.RawMessage  `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(raw), &m) != nil {
			return
		}
		switch {
		case m.ID != nil && m.Method == "":
			var id int64
			_ = json.Unmarshal(*m.ID, &id)
			s.mu.Lock()
			cb := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if cb != nil {
				r := rpcRes{result: m.Result}
				if m.Error != nil {
					r.err = fmt.Errorf("%s (code %d)", m.Error.Message, m.Error.Code)
				}
				cb(r)
			}
		case m.ID != nil:
			go s.handleServerRequest(*m.ID, m.Method, m.Params)
		case m.Method == "session/update":
			if !s.replaying.Load() {
				s.handleSessionUpdate(m.Params)
			}
		}
	})
}

func (s *session) handleSessionUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			Locations     []struct {
				Path string `json:"path"`
			} `json:"locations"`
			RawInput  json.RawMessage `json:"rawInput"`
			RawOutput json.RawMessage `json:"rawOutput"`
			Entries   []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var cb struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &cb) == nil && cb.Text != "" {
			s.mu.Lock()
			msgID := s.msgItemID
			s.msgBuf.WriteString(cb.Text)
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgID, Text: cb.Text})
		}
	case "agent_thought_chunk":
		var cb struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &cb) == nil && cb.Text != "" {
			s.mu.Lock()
			msgID := s.msgItemID
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventReasoningDelta, ItemID: "rsn_" + msgID, Text: cb.Text})
		}
	case "tool_call", "tool_call_update":
		s.flushAssistantSegment()
		id := u.ToolCallID
		if id == "" {
			id = fmt.Sprintf("acp_tool_%d", s.itemSeq.Add(1))
		}
		contentOut, diffPath, diffNewText := s.extractACPToolContent(u.Content)
		rawOut := extractACPRawOutput(u.RawOutput)
		inCmd, inPath := extractACPRawInput(u.RawInput)

		s.mu.Lock()
		st := s.tools[id]
		if st == nil {
			st = &acpToolState{id: id}
			s.tools[id] = st
		}
		if u.Title != "" {
			st.title = u.Title
		}
		if u.Kind != "" {
			st.kind = u.Kind
		}
		if u.Status != "" {
			st.status = u.Status
		}
		if inCmd != "" {
			st.command = inCmd
		}
		if len(u.Locations) > 0 && u.Locations[0].Path != "" {
			st.filePath = u.Locations[0].Path
		} else if diffPath != "" && st.filePath == "" {
			st.filePath = diffPath
		} else if inPath != "" && st.filePath == "" {
			st.filePath = inPath
		}
		if contentOut != "" {
			st.output = contentOut
		} else if rawOut != "" {
			st.output = rawOut
		}
		if diffNewText != "" {
			st.diffNewText = diffNewText
		}
		if trimmedIn := bytes.TrimSpace(u.RawInput); len(trimmedIn) > 0 && string(trimmedIn) != "null" && string(trimmedIn) != "{}" {
			st.rawInput = append(st.rawInput[:0], trimmedIn...)
		}
		toolName, cmd, filePath := mapACPToolFields(st)
		wasStarted := st.started
		st.started = true
		status := st.status
		outText := st.output
		stSnap := *st
		s.mu.Unlock()

		if status == "completed" || status == "failed" {
			exit := int32(0)
			if status == "failed" {
				exit = 1
			}
			kept, _ := provider.KeepOutput(outText)
			s.emit(runtime.Event{
				Kind:     runtime.EventToolCompleted,
				ItemID:   id,
				ToolName: toolName,
				Command:  cmd,
				FilePath: filePath,
				ExitCode: exit,
				Output:   kept,
			})
			if status == "completed" {
				if sig := s.maybeEmitACPArtifact(stSnap); sig != "" {
					s.mu.Lock()
					if cur := s.tools[id]; cur != nil {
						cur.lastArtifactSig = sig
					}
					s.mu.Unlock()
				}
			}
		} else if !wasStarted || u.SessionUpdate == "tool_call" || toolName != "" || cmd != "" || filePath != "" {
			s.emit(runtime.Event{
				Kind:     runtime.EventToolStarted,
				ItemID:   id,
				ToolName: toolName,
				Command:  cmd,
				FilePath: filePath,
			})
		}
	case "plan":
		var steps []*threadv2.TodoStep
		for _, e := range u.Entries {
			steps = append(steps, &threadv2.TodoStep{Text: e.Content, Status: e.Status})
		}
		s.emit(runtime.Event{Kind: runtime.EventTodoUpdated, TodoSteps: steps})
	}
}

func (s *session) extractACPToolContent(raw json.RawMessage) (output string, diffPath string, diffNewText string) {
	if len(raw) == 0 {
		return "", "", ""
	}
	var items []struct {
		Type       string `json:"type"`
		Path       string `json:"path"`
		NewText    string `json:"newText"`
		TerminalID string `json:"terminalId"`
		Content    struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		var single struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &single) == nil {
			return single.Text, "", ""
		}
		return "", "", ""
	}
	var parts []string
	for _, it := range items {
		switch it.Type {
		case "content":
			if it.Content.Text != "" {
				parts = append(parts, it.Content.Text)
			}
		case "diff":
			if diffPath == "" && it.Path != "" {
				diffPath = it.Path
			}
			if it.NewText != "" {
				diffNewText = it.NewText
			}
			if it.Path != "" {
				parts = append(parts, it.Path)
			}
		case "terminal":
			if it.TerminalID != "" {
				s.mu.Lock()
				mt := s.terminals[it.TerminalID]
				s.mu.Unlock()
				if mt != nil {
					mt.mu.Lock()
					tOut := mt.outBuf.String()
					mt.mu.Unlock()
					if tOut != "" {
						parts = append(parts, tOut)
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n"), diffPath, diffNewText
}

func extractACPRawOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Output         string `json:"output"`
		Stdout         string `json:"stdout"`
		CombinedOutput string `json:"combinedOutput"`
		Content        string `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, cand := range []string{obj.CombinedOutput, obj.Output, obj.Stdout, obj.Content} {
			if cand != "" {
				return cand
			}
		}
	}
	return ""
}

func extractACPRawInput(raw json.RawMessage) (cmd, path string) {
	if len(raw) == 0 {
		return "", ""
	}
	var obj struct {
		Command       string `json:"command"`
		Path          string `json:"path"`
		FilePath      string `json:"file_path"`
		FilePathCamel string `json:"filePath"`
		Pattern       string `json:"pattern"`
		Query         string `json:"query"`
		URL           string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, p := range []string{obj.Path, obj.FilePath, obj.FilePathCamel, obj.Pattern, obj.Query, obj.URL} {
			if p != "" {
				path = p
				break
			}
		}
		cmd = obj.Command
	}
	return cmd, path
}

type acpArtifactMeta struct {
	Present         bool
	Summary         string
	UserFacing      bool
	RequestFeedback bool
}

func decodeACPArtifactMetaJSON(raw json.RawMessage) acpArtifactMeta {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return acpArtifactMeta{}
	}
	if raw[0] == '"' {
		var inner string
		if json.Unmarshal(raw, &inner) != nil {
			return acpArtifactMeta{}
		}
		raw = bytes.TrimSpace([]byte(inner))
	}
	var m struct {
		Summary              string `json:"summary"`
		UserFacing           *bool  `json:"userFacing"`
		UserFacingSnake      *bool  `json:"user_facing"`
		RequestFeedback      *bool  `json:"requestFeedback"`
		RequestFeedbackSnake *bool  `json:"request_feedback"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return acpArtifactMeta{}
	}
	out := acpArtifactMeta{Summary: strings.TrimSpace(m.Summary)}
	if m.UserFacing != nil {
		out.UserFacing = *m.UserFacing
		out.Present = true
	} else if m.UserFacingSnake != nil {
		out.UserFacing = *m.UserFacingSnake
		out.Present = true
	}
	if m.RequestFeedback != nil {
		out.RequestFeedback = *m.RequestFeedback
		out.Present = true
	} else if m.RequestFeedbackSnake != nil {
		out.RequestFeedback = *m.RequestFeedbackSnake
		out.Present = true
	}
	if out.Summary != "" {
		out.Present = true
	}
	return out
}

func parseACPArtifactInput(raw json.RawMessage) (meta acpArtifactMeta, content string, path string) {
	if len(raw) == 0 {
		return acpArtifactMeta{}, "", ""
	}
	var obj struct {
		Content           string          `json:"content"`
		Path              string          `json:"path"`
		FilePath          string          `json:"file_path"`
		FilePathCamel     string          `json:"filePath"`
		ArtifactMetadata  json.RawMessage `json:"artifactMetadata"`
		ArtifactMetaSnake json.RawMessage `json:"artifact_metadata"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return acpArtifactMeta{}, "", ""
	}
	for _, p := range []string{obj.Path, obj.FilePath, obj.FilePathCamel} {
		if p != "" {
			path = p
			break
		}
	}
	metaRaw := obj.ArtifactMetadata
	if len(metaRaw) == 0 {
		metaRaw = obj.ArtifactMetaSnake
	}
	return decodeACPArtifactMetaJSON(metaRaw), obj.Content, path
}

func readACPArtifactSidecar(diskPath string) acpArtifactMeta {
	b, err := os.ReadFile(diskPath + ".metadata.json")
	if err != nil {
		return acpArtifactMeta{}
	}
	return decodeACPArtifactMetaJSON(b)
}

func isMarkdownArtifactPath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case "", ".md", ".markdown", ".txt":
		return true
	default:
		return false
	}
}

func sanitizeACPPlanID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "artifact"
	}
	return out
}

func deriveACPPlanTitle(md, summary, targetPath string) string {
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#")); heading != "" {
				return heading
			}
		}
	}
	for _, line := range strings.Split(summary, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			runes := []rune(s)
			if len(runes) > 80 {
				return string(runes[:80]) + "…"
			}
			return s
		}
	}
	if targetPath != "" {
		return filepath.Base(targetPath)
	}
	return "Implementation Plan"
}

func resolveACPPath(workdir, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) || workdir == "" {
		return p
	}
	return filepath.Join(workdir, p)
}

func (s *session) maybeEmitACPArtifact(st acpToolState) string {
	meta, rawContent, rawPath := parseACPArtifactInput(st.rawInput)
	targetPath := st.filePath
	if targetPath == "" {
		targetPath = rawPath
	}
	diskPath := resolveACPPath(s.spec.Workdir, targetPath)
	if !meta.Present && diskPath != "" {
		ext := strings.ToLower(filepath.Ext(diskPath))
		if ext == ".md" || ext == ".markdown" {
			meta = readACPArtifactSidecar(diskPath)
		}
	}
	if !meta.Present || (!meta.UserFacing && !meta.RequestFeedback) {
		return ""
	}
	if !meta.RequestFeedback && targetPath != "" && !isMarkdownArtifactPath(targetPath) {
		return ""
	}
	var md string
	if diskPath != "" {
		if b, err := os.ReadFile(diskPath); err == nil && len(bytes.TrimSpace(b)) > 0 {
			md = string(b)
		}
	}
	if md == "" && st.diffNewText != "" {
		md = st.diffNewText
	}
	if md == "" {
		md = rawContent
	}
	md = strings.TrimSpace(md)
	if md == "" {
		return ""
	}
	idSeed := st.id
	if targetPath != "" {
		idSeed = filepath.Base(targetPath)
	}
	planID := "plan_" + sanitizeACPPlanID(idSeed)
	title := deriveACPPlanTitle(md, meta.Summary, targetPath)
	sig := fmt.Sprintf("%t\x00%s\x00%s", meta.RequestFeedback, title, md)
	if sig == st.lastArtifactSig {
		return ""
	}
	s.emit(runtime.Event{
		Kind:          runtime.EventPlanProposed,
		ItemID:        planID,
		PlanTitle:     title,
		Text:          md,
		RequestReview: meta.RequestFeedback,
	})
	return sig
}

func (s *session) extractACPPlanExit(toolCallID, name, title, kind string, rawInput, content json.RawMessage, locPath string) (*threadv2.PlanReviewPayload, bool) {
	var in struct {
		Plan          string `json:"plan"`
		Title         string `json:"title"`
		PlanPath      string `json:"plan_path"`
		PlanPathCamel string `json:"planPath"`
		Path          string `json:"path"`
		FilePath      string `json:"file_path"`
		FilePathCamel string `json:"filePath"`
	}
	_ = json.Unmarshal(rawInput, &in)

	normName := strings.ToLower(strings.TrimSpace(name))
	normTitle := strings.ToLower(strings.TrimSpace(title))
	normKind := strings.ToLower(strings.TrimSpace(kind))
	isPlanExit := normName == "exit_plan_mode" || normName == "exitplanmode" ||
		normTitle == "exit_plan_mode" || normTitle == "exitplanmode" || normTitle == "exit plan mode" ||
		(normKind == "switch_mode" && (in.Plan != "" || in.PlanPath != "" || in.PlanPathCamel != "" || strings.Contains(normTitle, "plan")))
	if !isPlanExit {
		return nil, false
	}

	targetPath := ""
	for _, p := range []string{in.PlanPath, in.PlanPathCamel, in.Path, in.FilePath, in.FilePathCamel, locPath} {
		if p != "" {
			targetPath = p
			break
		}
	}
	if targetPath == "" && toolCallID != "" {
		s.mu.Lock()
		if st := s.tools[toolCallID]; st != nil {
			targetPath = st.filePath
		}
		s.mu.Unlock()
	}

	planMD := strings.TrimSpace(in.Plan)
	if planMD == "" && targetPath != "" {
		if b, err := os.ReadFile(resolveACPPath(s.spec.Workdir, targetPath)); err == nil {
			planMD = strings.TrimSpace(string(b))
		}
	}
	if planMD == "" && len(content) > 0 {
		out, _, diffText := s.extractACPToolContent(content)
		if strings.TrimSpace(diffText) != "" {
			planMD = strings.TrimSpace(diffText)
		} else {
			planMD = strings.TrimSpace(out)
		}
	}

	var planID string
	switch {
	case targetPath != "":
		planID = "plan_" + sanitizeACPPlanID(filepath.Base(targetPath))
	case toolCallID != "":
		planID = "plan_" + sanitizeACPPlanID(toolCallID)
	default:
		planID = fmt.Sprintf("plan_%d", s.itemSeq.Add(1))
	}
	planTitle := strings.TrimSpace(in.Title)
	if planTitle == "" {
		planTitle = deriveACPPlanTitle(planMD, "", targetPath)
	}
	return &threadv2.PlanReviewPayload{
		PlanId:   planID,
		Title:    planTitle,
		Markdown: planMD,
	}, true
}

func mapACPToolFields(st *acpToolState) (toolName, cmd, filePath string) {
	filePath = st.filePath
	switch st.kind {
	case "execute", "command", "bash":
		cmd = st.command
		if cmd == "" {
			cmd = st.title
		}
		toolName = "Bash"
	case "read":
		toolName = "Read"
		if filePath == "" {
			filePath = st.title
		}
	case "search":
		toolName = "Grep"
		if filePath == "" {
			filePath = st.title
		}
	case "edit", "delete", "move", "write":
		toolName = "Edit"
		if filePath == "" {
			filePath = st.title
		}
	case "fetch":
		toolName = "WebFetch"
		if filePath == "" {
			filePath = st.title
		}
	default:
		toolName = st.title
	}
	return toolName, cmd, filePath
}

func (s *session) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	s.mu.Lock()
	tctx := s.turnCtx
	s.mu.Unlock()

	reply := func(res any) {
		_ = s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": res})
	}
	replyErr := func(code int, msg string) {
		_ = s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}

	switch method {
	case "session/request_permission":
		var p struct {
			ToolCall struct {
				ToolCallID string          `json:"toolCallId"`
				Title      string          `json:"title"`
				Name       string          `json:"name"`
				Kind       string          `json:"kind"`
				RawInput   json.RawMessage `json:"rawInput"`
				Content    json.RawMessage `json:"content"`
				Locations  []struct {
					Path string `json:"path"`
				} `json:"locations"`
			} `json:"toolCall"`
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &p)
		targetPath := ""
		if len(p.ToolCall.Locations) > 0 {
			targetPath = p.ToolCall.Locations[0].Path
		}
		if planPayload, ok := s.extractACPPlanExit(
			p.ToolCall.ToolCallID,
			p.ToolCall.Name,
			p.ToolCall.Title,
			p.ToolCall.Kind,
			p.ToolCall.RawInput,
			p.ToolCall.Content,
			targetPath,
		); ok {
			s.flushAssistantSegment()
			if planPayload.Markdown != "" {
				s.emit(runtime.Event{
					Kind:      runtime.EventPlanProposed,
					ItemID:    planPayload.PlanId,
					PlanTitle: planPayload.Title,
					Text:      planPayload.Markdown,
				})
			}
			allowed := true
			if s.spec.Gate != nil {
				res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
					Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
					Plan: planPayload,
				})
				if err != nil || res.CancelTurn {
					reply(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
					return
				}
				allowed = res.PlanApproved
			}
			chosenID := pickPermissionOption(p.Options, allowed)
			if chosenID == "" {
				reply(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
				return
			}
			reply(map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosenID}})
			return
		}
		cat := "mcp"
		switch p.ToolCall.Kind {
		case "edit", "write", "delete", "move":
			cat = "file_write"
		case "execute", "command", "bash":
			cat = "command"
		case "read", "search":
			cat = "read"
		}
		allowed := true
		if s.spec.Gate != nil {
			res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
				Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &threadv2.ToolApprovalPayload{
					ToolCategory: cat,
					ToolName:     p.ToolCall.Title,
					Command:      p.ToolCall.Title,
					TargetPath:   targetPath,
				},
			})
			if err != nil || res.CancelTurn {
				reply(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
				return
			}
			allowed = res.Allowed
		}
		chosenID := pickPermissionOption(p.Options, allowed)
		if chosenID == "" {
			reply(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
			return
		}
		reply(map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosenID}})

	case "elicitation/create", "session/create_elicitation":
		s.handleElicitation(tctx, params, reply)

	case "fs/read_text_file":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(params, &p)
		target := p.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(s.spec.Workdir, target)
		}
		if s.spec.Gate != nil {
			res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
				Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &threadv2.ToolApprovalPayload{
					ToolCategory: "read",
					ToolName:     "fs/read_text_file",
					TargetPath:   target,
				},
			})
			if err != nil || !res.Allowed {
				replyErr(-32001, "file read denied by policy")
				return
			}
		}
		data, err := os.ReadFile(target)
		if err != nil {
			replyErr(-32002, err.Error())
			return
		}
		reply(map[string]any{"content": string(data)})

	case "fs/write_text_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(params, &p)
		target := p.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(s.spec.Workdir, target)
		}
		if s.spec.Gate != nil {
			res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
				Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &threadv2.ToolApprovalPayload{
					ToolCategory: "file_write",
					ToolName:     "fs/write_text_file",
					TargetPath:   target,
				},
			})
			if err != nil || !res.Allowed {
				msg := res.Feedback
				if msg == "" {
					msg = "file write denied by policy"
				}
				replyErr(-32001, msg)
				return
			}
		}
		s.flushAssistantSegment()
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		if err := os.WriteFile(target, []byte(p.Content), 0o644); err != nil {
			replyErr(-32603, err.Error())
			return
		}
		rel, _ := filepath.Rel(s.spec.Workdir, target)
		if rel == "" {
			rel = p.Path
		}
		s.emit(runtime.Event{
			Kind:      runtime.EventFileChanged,
			ItemID:    fmt.Sprintf("acp_file_%d", s.itemSeq.Add(1)),
			FilePath:  rel,
			Additions: uint32(max(1, strings.Count(p.Content, "\n"))),
		})
		reply(map[string]any{})

	case "terminal/create":
		var p struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Cwd     string   `json:"cwd"`
		}
		_ = json.Unmarshal(params, &p)
		fullCmd := strings.TrimSpace(p.Command + " " + strings.Join(p.Args, " "))
		if s.spec.Gate != nil {
			res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
				Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &threadv2.ToolApprovalPayload{
					ToolCategory: "command",
					ToolName:     "terminal/create",
					Command:      fullCmd,
				},
			})
			if err != nil || !res.Allowed {
				replyErr(-32001, "terminal command denied by policy")
				return
			}
		}
		s.flushAssistantSegment()
		cwd := p.Cwd
		if cwd == "" {
			cwd = s.spec.Workdir
		}
		termID := fmt.Sprintf("term_%d", s.itemSeq.Add(1))
		cmd := provider.Command(context.Background(), p.Command, p.Args...)
		cmd.Dir = cwd
		mt := &managedTerm{cmd: cmd, done: make(chan struct{})}
		lw := &lockedTermWriter{mt: mt}
		cmd.Stdout = lw
		cmd.Stderr = lw
		if err := cmd.Start(); err != nil {
			replyErr(-32603, err.Error())
			return
		}
		s.mu.Lock()
		s.terminals[termID] = mt
		s.mu.Unlock()
		s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: termID, ToolName: "Terminal", Command: fullCmd})
		go func() {
			err := cmd.Wait()
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					mt.exit = ee.ExitCode()
				} else {
					mt.exit = 1
				}
			}
			close(mt.done)
			mt.mu.Lock()
			outStr := mt.outBuf.String()
			mt.mu.Unlock()
			kept, _ := provider.KeepOutput(outStr)
			s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: termID, ToolName: "Terminal", Command: fullCmd, ExitCode: int32(mt.exit), Output: kept})
		}()
		reply(map[string]any{"terminalId": termID})

	case "terminal/wait_for_exit":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		mt := s.terminals[p.TerminalID]
		s.mu.Unlock()
		if mt == nil {
			replyErr(-32002, "unknown terminalId")
			return
		}
		<-mt.done
		reply(map[string]any{"exitCode": mt.exit})

	case "terminal/output":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		mt := s.terminals[p.TerminalID]
		s.mu.Unlock()
		if mt == nil {
			replyErr(-32002, "unknown terminalId")
			return
		}
		mt.mu.Lock()
		outStr := mt.outBuf.String()
		mt.mu.Unlock()
		reply(map[string]any{"output": outStr, "truncated": false})

	case "terminal/release", "terminal/kill":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		mt := s.terminals[p.TerminalID]
		delete(s.terminals, p.TerminalID)
		s.mu.Unlock()
		if mt != nil && mt.cmd != nil && mt.cmd.Process != nil {
			_ = mt.cmd.Process.Kill()
		}
		reply(map[string]any{})

	default:
		replyErr(-32601, "method not supported")
	}
}

type lockedTermWriter struct {
	mt *managedTerm
}

func (w *lockedTermWriter) Write(p []byte) (int, error) {
	w.mt.mu.Lock()
	defer w.mt.mu.Unlock()
	return w.mt.outBuf.Write(p)
}

func pickPermissionOption(options []struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}, allowed bool) string {
	if allowed {
		for _, o := range options {
			if o.Kind == "allow_once" {
				return o.OptionID
			}
		}
		for _, o := range options {
			if strings.HasPrefix(o.Kind, "allow") {
				return o.OptionID
			}
		}
		return ""
	}
	for _, o := range options {
		if o.Kind == "reject_once" {
			return o.OptionID
		}
	}
	for _, o := range options {
		if strings.HasPrefix(o.Kind, "reject") {
			return o.OptionID
		}
	}
	return ""
}

func (s *session) handleElicitation(tctx context.Context, params json.RawMessage, reply func(any)) {
	if s.spec.Gate == nil {
		reply(map[string]any{"action": "decline"})
		return
	}
	var p struct {
		Mode            string `json:"mode"`
		Message         string `json:"message"`
		RequestedSchema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Type        string   `json:"type"`
				Title       string   `json:"title"`
				Description string   `json:"description"`
				Enum        []string `json:"enum"`
				OneOf       []struct {
					Const string `json:"const"`
					Title string `json:"title"`
				} `json:"oneOf"`
			} `json:"properties"`
		} `json:"requestedSchema"`
	}
	if json.Unmarshal(params, &p) != nil || (p.Mode != "" && p.Mode != "form") {
		reply(map[string]any{"action": "decline"})
		return
	}

	type propMeta struct {
		key      string
		typ      string
		choiceID map[string]string
	}
	var keys []string
	for k := range p.RequestedSchema.Properties {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var qItems []*threadv2.QuestionItem
	var metas []propMeta
	if len(keys) == 0 {
		qItems = append(qItems, &threadv2.QuestionItem{
			Id:          "response",
			Question:    p.Message,
			AllowCustom: true,
			Required:    true,
		})
		metas = append(metas, propMeta{key: "response", typ: "string"})
	} else {
		for _, k := range keys {
			prop := p.RequestedSchema.Properties[k]
			meta := propMeta{key: k, typ: prop.Type, choiceID: map[string]string{}}
			var opts []*threadv2.QuestionOption
			if prop.Type == "boolean" {
				opts = []*threadv2.QuestionOption{
					{Id: "true", Label: "Yes"},
					{Id: "false", Label: "No"},
				}
				meta.choiceID["Yes"] = "true"
				meta.choiceID["No"] = "false"
			} else if len(prop.OneOf) > 0 {
				for _, o := range prop.OneOf {
					lbl := o.Title
					if lbl == "" {
						lbl = o.Const
					}
					opts = append(opts, &threadv2.QuestionOption{Id: o.Const, Label: lbl})
					meta.choiceID[lbl] = o.Const
				}
			} else {
				for _, e := range prop.Enum {
					opts = append(opts, &threadv2.QuestionOption{Id: e, Label: e})
					meta.choiceID[e] = e
				}
			}
			qText := prop.Description
			if qText == "" {
				qText = p.Message
			}
			if qText == "" {
				qText = k
			}
			qItems = append(qItems, &threadv2.QuestionItem{
				Id:          k,
				Header:      prop.Title,
				Question:    qText,
				Options:     opts,
				AllowCustom: len(opts) == 0,
				Required:    slices.Contains(p.RequestedSchema.Required, k),
			})
			metas = append(metas, meta)
		}
	}

	res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
		Kind:      threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
		UserInput: &threadv2.UserInputPayload{Questions: qItems},
	})
	if err != nil || !res.Allowed {
		if res.CancelTurn {
			reply(map[string]any{"action": "cancel"})
		} else {
			reply(map[string]any{"action": "decline"})
		}
		return
	}
	content := map[string]any{}
	for _, m := range metas {
		ans := res.Answers[m.key]
		if ans == nil {
			continue
		}
		raw := ans.CustomText
		if raw == "" && len(ans.SelectedOptions) > 0 {
			raw = ans.SelectedOptions[0]
			if mapped, ok := m.choiceID[raw]; ok {
				raw = mapped
			}
		}
		switch m.typ {
		case "boolean":
			content[m.key] = raw == "true" || strings.EqualFold(raw, "yes")
		default:
			content[m.key] = raw
		}
	}
	reply(map[string]any{"action": "accept", "content": content})
}
