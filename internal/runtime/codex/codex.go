// Package codex implements the long-lived Hermesh v2 runtime.Driver for Codex
// via `codex app-server` JSON-RPC over stdio.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type Driver struct {
	PathEnv string
	Binary  string
}

func NewDriver(pathEnv string) *Driver { return &Driver{PathEnv: pathEnv} }

func (d *Driver) Kind() string { return "codex" }

func (d *Driver) Capabilities(_ string) *threadv2.ProviderCapabilities {
	return &threadv2.ProviderCapabilities{
		InSessionModelSwitch:  true,
		InSessionOptionSwitch: true,
		InSessionModeSwitch:   true,
		MidTurnSteer:          true,
		LiveApproval:          true,
		LiveUserInput:         true,
		NativeResume:          true,
		NativeFork:            true,
		ConversationRollback:  true,
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

func (d *Driver) resolveBinary(cfg *threadv2.ProviderInstanceConfig) string {
	if cfg != nil && cfg.BinaryPath != "" {
		return cfg.BinaryPath
	}
	if d.Binary != "" {
		return d.Binary
	}
	home, _ := os.UserHomeDir()
	return provider.LookPath("codex", provider.SearchPath(d.PathEnv),
		"/opt/homebrew/bin/codex",
		"/usr/local/bin/codex",
		filepath.Join(home, ".local/bin/codex"),
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
	)
}

func (d *Driver) Probe(ctx context.Context, cfg *threadv2.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	bin := d.resolveBinary(cfg)
	if !provider.Exists(bin) {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "codex CLI not found on PATH",
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
	res := runtime.ProbeResult{
		Installed:     true,
		Version:       versionRE.FindString(string(out)),
		Compatibility: threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
	login := provider.Command(pctx, bin, "login", "status")
	login.Env = env
	if lOut, err := login.CombinedOutput(); err == nil && strings.Contains(string(lOut), "Logged in") {
		res.Authenticated = true
		res.AuthLabel = "Codex Account"
	} else if os.Getenv("OPENAI_API_KEY") != "" || instEnv["OPENAI_API_KEY"] != "" {
		res.Authenticated = true
		res.AuthLabel = "OPENAI_API_KEY"
	}
	return res
}

func mapSandboxAndPolicy(rm identityv1.RuntimeMode, im threadv2.InteractionMode) (sandbox, approvalPolicy string) {
	if im == threadv2.InteractionMode_INTERACTION_MODE_PLAN {
		return "read-only", "on-request"
	}
	switch rm {
	case identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS:
		return "danger-full-access", "never"
	case identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED:
		return "workspace-write", "untrusted"
	default:
		return "workspace-write", "on-request"
	}
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	bin := d.resolveBinary(spec.Instance)
	if !provider.Exists(bin) {
		return nil, errors.New("codex CLI not found")
	}
	var instEnv map[string]string
	var args = []string{"app-server"}
	if spec.Instance != nil {
		instEnv = spec.Instance.Env
		if len(spec.Instance.LaunchArgs) > 0 {
			args = slices.Clone(spec.Instance.LaunchArgs)
		}
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv, spec.ExtraEnv)
	proc, err := provider.StartProc(ctx, bin, args, spec.Workdir, env)
	if err != nil {
		return nil, err
	}
	s := &session{
		proc:            proc,
		spec:            spec,
		events:          make(chan runtime.Event, 256),
		pending:         map[int64]chan rpcRes{},
		selection:       proto.Clone(spec.Selection).(*threadv2.ModelSelection),
		effectiveMode:   spec.EffectiveMode,
		interactionMode: spec.InteractionMode,
		turnCtx:         ctx,
	}
	s.alive.Store(true)
	go s.readLoop()

	if _, err := s.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "hermesh", "version": "2.0.0"},
	}); err != nil {
		proc.Stop(time.Second)
		return nil, fmt.Errorf("codex initialize: %w", err)
	}
	_ = s.notify("initialized", nil)

	sandbox, approval := mapSandboxAndPolicy(spec.EffectiveMode, spec.InteractionMode)
	params := map[string]any{
		"cwd":            spec.Workdir,
		"sandbox":        sandbox,
		"approvalPolicy": approval,
	}
	applySelectionParams(params, spec.Selection)

	method := "thread/start"
	if spec.ResumeCursor != "" {
		method = "thread/resume"
		params["threadId"] = spec.ResumeCursor
		_, _ = s.call(ctx, "thread/unarchive", map[string]any{"threadId": spec.ResumeCursor})
	}
	raw, err := s.call(ctx, method, params)
	if err != nil {
		proc.Stop(time.Second)
		return nil, fmt.Errorf("codex %s: %w", method, err)
	}
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(raw, &tr)
	if tr.Thread.ID == "" {
		proc.Stop(time.Second)
		return nil, errors.New("codex returned empty thread id")
	}
	s.codexThreadID = tr.Thread.ID
	s.emit(runtime.Event{
		Kind:         runtime.EventNativeSession,
		NativeID:     tr.Thread.ID,
		ResumeCursor: tr.Thread.ID,
	})
	return s, nil
}

func applySelectionParams(params map[string]any, sel *threadv2.ModelSelection) {
	if sel == nil {
		return
	}
	if sel.Model != "" && sel.Model != "default" {
		params["model"] = sel.Model
	}
	if eff := sel.Options["reasoningEffort"].GetStringValue(); eff != "" {
		params["effort"] = eff
	}
	if sel.Options["fastMode"].GetBoolValue() {
		params["serviceTier"] = "fast"
	}
}

type rpcRes struct {
	result json.RawMessage
	err    error
}

type session struct {
	proc          *provider.Proc
	spec          runtime.StartSpec
	codexThreadID string
	events        chan runtime.Event
	alive         atomic.Bool
	nextID        atomic.Int64
	itemSeq       atomic.Uint64
	closeOnce     sync.Once

	mu              sync.Mutex
	pending         map[int64]chan rpcRes
	turnCtx         context.Context
	turnID          string
	msgItemID       string
	selection       *threadv2.ModelSelection
	effectiveMode   identityv1.RuntimeMode
	interactionMode threadv2.InteractionMode
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

func (s *session) notify(method string, params any) error {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	return s.writeJSON(m)
}

func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan rpcRes, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	if err := s.writeJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.proc.Done():
		return nil, fmt.Errorf("codex exited: %v", s.proc.Err())
	}
}

func buildTurnInput(text string, atts []runtime.TurnAttachment) []any {
	out := []any{map[string]any{"type": "text", "text": text}}
	for _, a := range atts {
		if strings.HasPrefix(a.MimeType, "image/") && a.Path != "" {
			out = append(out, map[string]any{"type": "localImage", "path": a.Path})
		} else if a.Path != "" {
			out = append(out, map[string]any{"type": "text", "text": fmt.Sprintf("[Attached: %s]", a.Path)})
		}
	}
	return out
}

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	s.mu.Lock()
	s.turnCtx = ctx
	if in.Selection != nil {
		s.selection = proto.Clone(in.Selection).(*threadv2.ModelSelection)
	}
	if in.EffectiveMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		s.effectiveMode = in.EffectiveMode
	}
	if in.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		s.interactionMode = in.InteractionMode
	}
	sel := s.selection
	rm, im := s.effectiveMode, s.interactionMode
	s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
	s.mu.Unlock()

	sandbox, approval := mapSandboxAndPolicy(rm, im)
	params := map[string]any{
		"threadId":       s.codexThreadID,
		"input":          buildTurnInput(in.Prompt, in.Attachments),
		"sandbox":        sandbox,
		"approvalPolicy": approval,
	}
	applySelectionParams(params, sel)

	res, err := s.call(ctx, "turn/start", params)
	if err != nil {
		return err
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &tr)
	if tr.Turn.ID != "" {
		s.mu.Lock()
		s.turnID = tr.Turn.ID
		s.mu.Unlock()
	}
	return nil
}

func (s *session) Steer(ctx context.Context, text string, atts []runtime.TurnAttachment) error {
	s.mu.Lock()
	tid := s.turnID
	s.mu.Unlock()
	if tid == "" {
		return errors.New("no active turn to steer")
	}
	_, err := s.call(ctx, "turn/steer", map[string]any{
		"threadId":       s.codexThreadID,
		"expectedTurnId": tid,
		"input":          buildTurnInput(text, atts),
	})
	return err
}

func (s *session) SetSelection(_ context.Context, sel *threadv2.ModelSelection) error {
	s.mu.Lock()
	s.selection = proto.Clone(sel).(*threadv2.ModelSelection)
	s.mu.Unlock()
	return nil
}

func (s *session) SetModes(_ context.Context, rm identityv1.RuntimeMode, im threadv2.InteractionMode) error {
	s.mu.Lock()
	s.effectiveMode = rm
	s.interactionMode = im
	s.mu.Unlock()
	return nil
}

func (s *session) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	tid := s.turnID
	s.mu.Unlock()
	if tid == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.call(cctx, "turn/interrupt", map[string]any{"threadId": s.codexThreadID, "turnId": tid})
	return err
}

func (s *session) ForceStop() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() { s.proc.Stop(0) })
	return nil
}

func (s *session) Close() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() { s.proc.Stop(2 * time.Second) })
	return nil
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
			ch := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ch != nil {
				r := rpcRes{result: m.Result}
				if m.Error != nil {
					r.err = fmt.Errorf("%s (code %d)", m.Error.Message, m.Error.Code)
				}
				ch <- r
			}
		case m.ID != nil:
			go s.handleServerRequest(*m.ID, m.Method, m.Params)
		default:
			s.handleNotification(m.Method, m.Params)
		}
	})
}

func (s *session) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	s.mu.Lock()
	tctx := s.turnCtx
	s.mu.Unlock()

	reply := func(res any) {
		_ = s.writeJSON(map[string]any{"id": id, "result": res})
	}
	if s.spec.Gate == nil {
		reply(map[string]any{"decision": "accept"})
		return
	}

	switch method {
	case "item/commandExecution/requestApproval":
		var p struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(params, &p)
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
			Approval: &threadv2.ToolApprovalPayload{
				ToolCategory: "command",
				ToolName:     "CommandExecution",
				Command:      p.Command,
				Reason:       p.Reason,
			},
		})
		if err != nil || res.CancelTurn {
			reply(map[string]any{"decision": "cancel"})
			return
		}
		if !res.Allowed {
			reply(map[string]any{"decision": "decline"})
			return
		}
		reply(map[string]any{"decision": "accept"})

	case "item/fileChange/requestApproval":
		var p struct {
			Reason string `json:"reason"`
			Path   string `json:"path"`
		}
		_ = json.Unmarshal(params, &p)
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
			Approval: &threadv2.ToolApprovalPayload{
				ToolCategory: "file_write",
				ToolName:     "FileChange",
				TargetPath:   p.Path,
				Reason:       p.Reason,
			},
		})
		if err != nil || res.CancelTurn {
			reply(map[string]any{"decision": "cancel"})
			return
		}
		if !res.Allowed {
			reply(map[string]any{"decision": "decline"})
			return
		}
		reply(map[string]any{"decision": "accept"})

	case "item/tool/requestUserInput":
		var p struct {
			Questions []struct {
				ID       string `json:"id"`
				Header   string `json:"header"`
				Question string `json:"question"`
				IsSecret bool   `json:"isSecret"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(params, &p) != nil || len(p.Questions) == 0 {
			reply(map[string]any{"answers": map[string]any{}})
			return
		}
		if len(p.Questions) == 1 && p.Questions[0].IsSecret {
			res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
				Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT,
				Secret: &threadv2.SecretInputPayload{
					Label:    p.Questions[0].Header,
					Reason:   p.Questions[0].Question,
					Delivery: "memory",
				},
			})
			if err != nil || !res.Allowed {
				reply(map[string]any{"answers": map[string]any{}})
				return
			}
			reply(map[string]any{
				"answers": map[string]any{
					p.Questions[0].ID: map[string]any{"answers": []string{res.SecretValue}},
				},
			})
			return
		}
		var qItems []*threadv2.QuestionItem
		for _, q := range p.Questions {
			var opts []*threadv2.QuestionOption
			for _, o := range q.Options {
				opts = append(opts, &threadv2.QuestionOption{Id: o.Label, Label: o.Label, Description: o.Description})
			}
			qItems = append(qItems, &threadv2.QuestionItem{
				Id:          q.ID,
				Header:      q.Header,
				Question:    q.Question,
				Options:     opts,
				AllowCustom: true,
				Required:    true,
			})
		}
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind:      threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
			UserInput: &threadv2.UserInputPayload{Questions: qItems},
		})
		if err != nil || !res.Allowed {
			reply(map[string]any{"answers": map[string]any{}})
			return
		}
		answersOut := map[string]any{}
		for _, q := range p.Questions {
			var list []string
			if ans := res.Answers[q.ID]; ans != nil {
				list = append(list, ans.SelectedOptions...)
				if ans.CustomText != "" {
					list = append(list, ans.CustomText)
				}
			}
			answersOut[q.ID] = map[string]any{"answers": list}
		}
		reply(map[string]any{"answers": answersOut})

	default:
		reply(map[string]any{"decision": "decline"})
	}
}

func (s *session) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &p) == nil && p.Turn.ID != "" {
			s.mu.Lock()
			s.turnID = p.Turn.ID
			s.mu.Unlock()
		}

	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(params, &p) == nil && p.Delta != "" {
			s.mu.Lock()
			msgID := s.msgItemID
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgID, Text: p.Delta})
		}

	case "item/started", "item/completed":
		var p struct {
			Item struct {
				ID               string `json:"id"`
				Type             string `json:"type"`
				Text             string `json:"text"`
				Command          string `json:"command"`
				Status           string `json:"status"`
				ExitCode         *int   `json:"exitCode"`
				AggregatedOutput string `json:"aggregatedOutput"`
				Changes          []struct {
					Path string `json:"path"`
				} `json:"changes"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		done := method == "item/completed"
		switch p.Item.Type {
		case "agentMessage":
			if done && strings.TrimSpace(p.Item.Text) != "" {
				s.mu.Lock()
				msgID := s.msgItemID
				s.mu.Unlock()
				s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: msgID, Text: p.Item.Text})
			}
		case "commandExecution":
			if !done {
				s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: p.Item.ID, ToolName: "CommandExecution", Command: p.Item.Command})
			} else {
				exit := int32(0)
				if p.Item.ExitCode != nil {
					exit = int32(*p.Item.ExitCode)
				}
				kept, _ := provider.KeepOutput(p.Item.AggregatedOutput)
				s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: p.Item.ID, ToolName: "CommandExecution", Command: p.Item.Command, ExitCode: exit, Output: kept})
			}
		case "fileChange":
			if done {
				for _, ch := range p.Item.Changes {
					s.emit(runtime.Event{Kind: runtime.EventFileChanged, ItemID: p.Item.ID + "_" + ch.Path, FilePath: ch.Path})
				}
			}
		}

	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens  uint64 `json:"inputTokens"`
					OutputTokens uint64 `json:"outputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &p) == nil {
			inTok := p.TokenUsage.Total.InputTokens
			outTok := p.TokenUsage.Total.OutputTokens
			s.emit(runtime.Event{
				Kind: runtime.EventUsage,
				Usage: &threadv2.ContextWindowUsage{
					InputTokens:        inTok,
					OutputTokens:       outTok,
					ContextUsedTokens:  inTok + outTok,
					ContextLimitTokens: 272000,
				},
			})
		}

	case "turn/completed":
		var p struct {
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		s.turnID = ""
		s.mu.Unlock()
		switch p.Turn.Status {
		case "completed", "":
			s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
		case "interrupted", "cancelled":
			s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
		default:
			msg := "codex turn " + p.Turn.Status
			if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				msg = p.Turn.Error.Message
			}
			s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: p.Turn.Status, ErrorMessage: msg})
		}
	}
}
