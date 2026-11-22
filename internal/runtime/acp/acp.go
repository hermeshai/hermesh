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
		AgentCapabilities struct {
			LoadSession        bool `json:"loadSession"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(initRes, &caps)
	s.supportsImages = caps.AgentCapabilities.PromptCapabilities.Image

	params := map[string]any{"cwd": spec.Workdir, "mcpServers": []any{}}
	if spec.ResumeCursor != "" {
		params["sessionId"] = spec.ResumeCursor
		s.replaying.Store(true)
		_, loadErr := s.call(ctx, "session/load", params)
		s.replaying.Store(false)
		if loadErr != nil {
			proc.Stop(time.Second)
			return nil, fmt.Errorf("acp session/load: %w", loadErr)
		}
		s.sessionID = spec.ResumeCursor
	} else {
		newRes, newErr := s.call(ctx, "session/new", params)
		if newErr != nil {
			proc.Stop(time.Second)
			return nil, fmt.Errorf("acp session/new: %w", newErr)
		}
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
	proc           *provider.Proc
	spec           runtime.StartSpec
	sessionID      string
	supportsImages bool
	replaying      atomic.Bool
	events         chan runtime.Event
	alive          atomic.Bool
	nextID         atomic.Int64
	itemSeq        atomic.Uint64
	closeOnce      sync.Once

	mu        sync.Mutex
	pending   map[int64]func(rpcRes)
	terminals map[string]*managedTerm
	turnCtx   context.Context
	msgItemID string
	msgBuf    strings.Builder
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
	s.mu.Lock()
	msgID := s.msgItemID
	full := s.msgBuf.String()
	s.msgBuf.Reset()
	s.mu.Unlock()

	if strings.TrimSpace(full) != "" {
		s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: msgID, Text: full})
	}
	if r.err != nil {
		s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "acp_error", ErrorMessage: r.err.Error()})
		return
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens  uint64 `json:"inputTokens"`
			OutputTokens uint64 `json:"outputTokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(r.result, &res)
	if res.Usage.InputTokens > 0 || res.Usage.OutputTokens > 0 {
		s.emit(runtime.Event{
			Kind: runtime.EventUsage,
			Usage: &threadv2.ContextWindowUsage{
				InputTokens:        res.Usage.InputTokens,
				OutputTokens:       res.Usage.OutputTokens,
				ContextUsedTokens:  res.Usage.InputTokens + res.Usage.OutputTokens,
				ContextLimitTokens: 200000,
			},
		})
	}
	if res.StopReason == "cancelled" {
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
		return
	}
	s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
}

func (s *session) Steer(context.Context, string, []runtime.TurnAttachment) error {
	return runtime.ErrSteerUnsupported
}

func (s *session) SetSelection(ctx context.Context, sel *threadv2.ModelSelection) error {
	if sel == nil {
		return nil
	}
	if sel.Model != "" && sel.Model != "default" {
		_, _ = s.call(ctx, "session/set_model", map[string]any{"sessionId": s.sessionID, "modelId": sel.Model})
	}
	for k, v := range sel.Options {
		var rawVal any
		if sv, ok := v.Kind.(*threadv2.OptionValue_StringValue); ok {
			rawVal = sv.StringValue
		} else if bv, ok := v.Kind.(*threadv2.OptionValue_BoolValue); ok {
			rawVal = bv.BoolValue
		}
		if rawVal != nil {
			_, _ = s.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": s.sessionID,
				"configId":  k,
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
	select {
	case s.events <- ev:
	case <-s.proc.Done():
	}
}

func (s *session) readLoop() {
	defer func() {
		s.alive.Store(false)
		close(s.events)
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
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			Entries    []struct {
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
		if u.Content.Text != "" {
			s.mu.Lock()
			msgID := s.msgItemID
			s.msgBuf.WriteString(u.Content.Text)
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgID, Text: u.Content.Text})
		}
	case "agent_thought_chunk":
		if u.Content.Text != "" {
			s.mu.Lock()
			msgID := s.msgItemID
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventReasoningDelta, ItemID: "rsn_" + msgID, Text: u.Content.Text})
		}
	case "tool_call", "tool_call_update":
		id := u.ToolCallID
		if id == "" {
			id = fmt.Sprintf("acp_tool_%d", s.itemSeq.Add(1))
		}
		if u.Status == "completed" || u.Status == "failed" {
			exit := int32(0)
			if u.Status == "failed" {
				exit = 1
			}
			s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: id, ToolName: u.Title, ExitCode: exit})
		} else {
			s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: id, ToolName: u.Title})
		}
	case "plan":
		var steps []*threadv2.TodoStep
		for _, e := range u.Entries {
			steps = append(steps, &threadv2.TodoStep{Text: e.Content, Status: e.Status})
		}
		s.emit(runtime.Event{Kind: runtime.EventTodoUpdated, TodoSteps: steps})
	}
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
				Title string `json:"title"`
				Kind  string `json:"kind"`
			} `json:"toolCall"`
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &p)
		cat := "mcp"
		switch p.ToolCall.Kind {
		case "edit", "write":
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
				},
			})
			if err != nil || res.CancelTurn {
				reply(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
				return
			}
			allowed = res.Allowed
		}
		pickKind := "allow_once"
		if !allowed {
			pickKind = "reject_once"
		}
		chosenID := ""
		for _, o := range p.Options {
			if o.Kind == pickKind || (allowed && strings.Contains(o.Kind, "allow")) || (!allowed && strings.Contains(o.Kind, "reject")) {
				chosenID = o.OptionID
				break
			}
		}
		if chosenID == "" && len(p.Options) > 0 {
			chosenID = p.Options[0].OptionID
		}
		reply(map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosenID}})

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
		cwd := p.Cwd
		if cwd == "" {
			cwd = s.spec.Workdir
		}
		termID := fmt.Sprintf("term_%d", s.itemSeq.Add(1))
		cmd := provider.Command(context.Background(), p.Command, p.Args...)
		cmd.Dir = cwd
		mt := &managedTerm{cmd: cmd, done: make(chan struct{})}
		cmd.Stdout = &mt.outBuf
		cmd.Stderr = &mt.outBuf
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
			kept, _ := provider.KeepOutput(mt.outBuf.String())
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
		reply(map[string]any{"output": mt.outBuf.String(), "truncated": false})

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
