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
	"unicode/utf8"

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
	// Optional overrides for deterministic tests; zero uses production defaults.
	PostPromptMergeWindow time.Duration
	OutOfTurnToolWait     time.Duration
	OutOfTurnDebounce     time.Duration
	OutOfTurnQuietLimit   time.Duration
	BusyPromptRetryDelay  time.Duration
}

func (d *Driver) mergeWindow() time.Duration {
	if d.PostPromptMergeWindow > 0 {
		return d.PostPromptMergeWindow
	}
	return 1500 * time.Millisecond
}

func (d *Driver) outOfTurnToolWait() time.Duration {
	if d.OutOfTurnToolWait > 0 {
		return d.OutOfTurnToolWait
	}
	return 6 * time.Second
}

func (d *Driver) outOfTurnDebounce() time.Duration {
	if d.OutOfTurnDebounce > 0 {
		return d.OutOfTurnDebounce
	}
	return 1500 * time.Millisecond
}

func (d *Driver) outOfTurnQuietLimit() time.Duration {
	if d.OutOfTurnQuietLimit > 0 {
		return d.OutOfTurnQuietLimit
	}
	return 45 * time.Second
}

func (d *Driver) busyPromptRetryDelay() time.Duration {
	if d.BusyPromptRetryDelay > 0 {
		return d.BusyPromptRetryDelay
	}
	return 350 * time.Millisecond
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
	sessCtx, sessCancel := context.WithCancel(ctx)
	s := &session{
		drv:           d,
		proc:          proc,
		spec:          spec,
		events:        make(chan runtime.Event, 256),
		pending:       map[int64]func(rpcRes){},
		terminals:     map[string]*managedTerm{},
		agentTerms:    map[string]*acpAgentTerm{},
		tools:         map[string]*acpToolState{},
		bgTasks:       map[string]*acpBgTask{},
		sessionCtx:    sessCtx,
		sessionCancel: sessCancel,
		turnCtx:       sessCtx,
	}
	s.alive.Store(true)
	go s.readLoop()

	clientCaps := map[string]any{
		"fs":          map[string]any{"readTextFile": true, "writeTextFile": true},
		"terminal":    true,
		"elicitation": map[string]any{"form": map[string]any{}},
	}
	if spec.Instance != nil && strings.EqualFold(spec.Instance.AcpFlavor, "copilot") {
		clientCaps["_meta"] = map[string]any{
			"github.com/copilot": map[string]any{
				"events": []string{"subagent.started", "subagent.completed", "subagent.failed", "assistant.idle"},
			},
		}
	}
	initRes, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": clientCaps,
		"clientInfo":         map[string]any{"name": "hermesh", "version": "2.0.0"},
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
	terminalID      string
	output          string
	diffNewText     string
	rawInput        json.RawMessage
	lastArtifactSig string
	started         bool
	asyncLaunch     bool
	asyncAgentID    string
}

type rpcRes struct {
	result json.RawMessage
	err    error
}

type managedTerm struct {
	id        string
	command   string
	cmd       *exec.Cmd
	outBuf    bytes.Buffer
	mu        sync.Mutex
	done      chan struct{}
	exit      int
	lastFlush time.Time
	promoted  bool
}

type acpAgentTerm struct {
	id        string
	toolID    string
	title     string
	command   string
	outBuf    bytes.Buffer
	carryUTF8 []byte
	running   bool
	exitCode  int32
	hasExit   bool
}

func (at *acpAgentTerm) appendBytes(raw []byte) {
	if len(raw) == 0 {
		return
	}
	buf := raw
	if len(at.carryUTF8) > 0 {
		buf = append(append([]byte(nil), at.carryUTF8...), raw...)
		at.carryUTF8 = at.carryUTF8[:0]
	}
	validEnd := len(buf)
	for back := 1; back <= 3 && back <= len(buf); back++ {
		idx := len(buf) - back
		b := buf[idx]
		if b < 0x80 {
			break
		}
		if utf8.RuneStart(b) {
			if !utf8.FullRune(buf[idx:]) {
				validEnd = idx
				at.carryUTF8 = append(at.carryUTF8[:0], buf[idx:]...)
			}
			break
		}
	}
	if validEnd > 0 {
		_, _ = at.outBuf.Write(buf[:validEnd])
	}
}

func (at *acpAgentTerm) flushCarry() {
	if len(at.carryUTF8) > 0 {
		_, _ = at.outBuf.Write(at.carryUTF8)
		at.carryUTF8 = at.carryUTF8[:0]
	}
}

type acpBgTask struct {
	taskID      string
	itemID      string
	agentID     string
	kind        threadv2.BackgroundTaskKind
	title       string
	command     string
	output      string
	awaited     bool
	asyncLaunch bool
	stopScope   threadv2.BackgroundTaskStopScope
	running     bool
}

type session struct {
	drv             *Driver
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
	sessionCtx      context.Context
	sessionCancel   context.CancelFunc

	emitMu       sync.RWMutex
	eventsClosed bool

	mu                 sync.Mutex
	pending            map[int64]func(rpcRes)
	terminals          map[string]*managedTerm
	agentTerms         map[string]*acpAgentTerm
	tools              map[string]*acpToolState
	bgTasks            map[string]*acpBgTask
	turnCtx            context.Context
	inPrompt           bool
	awaitingMerge      bool
	mergeTimer         *time.Timer
	outOfTurnActive    bool
	outOfTurnReplyOpen bool
	outOfTurnSawReply  bool
	outOfTurnSawUsage  bool
	outOfTurnTimer     *time.Timer
	openServerReqs     int
	msgItemID          string
	msgBuf             strings.Builder
}

func tailUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func (s *session) gateCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inPrompt && s.turnCtx != nil && s.turnCtx.Err() == nil {
		return s.turnCtx
	}
	return s.sessionCtx
}

func (s *session) stopTimersLocked() {
	if s.mergeTimer != nil {
		s.mergeTimer.Stop()
		s.mergeTimer = nil
	}
	if s.outOfTurnTimer != nil {
		s.outOfTurnTimer.Stop()
		s.outOfTurnTimer = nil
	}
}

func (s *session) hasOpenWakeToolLocked() bool {
	for id, st := range s.tools {
		if bg := s.bgTasks[id]; bg != nil && bg.running {
			continue
		}
		if st.status == "in_progress" || st.status == "pending" {
			return true
		}
	}
	return false
}

func (s *session) noteWakeActivityLocked(opensReply bool) {
	if s.inPrompt {
		return
	}
	if s.awaitingMerge {
		if s.mergeTimer != nil {
			s.mergeTimer.Stop()
			s.mergeTimer = nil
		}
		s.awaitingMerge = false
	}
	if !s.outOfTurnActive {
		s.outOfTurnActive = true
		s.outOfTurnSawReply = false
		s.outOfTurnSawUsage = false
		if s.msgItemID == "" {
			s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
		}
	}
	s.outOfTurnSawUsage = false
	if opensReply {
		s.outOfTurnReplyOpen = true
		s.outOfTurnSawReply = true
	}
	s.rearmOutOfTurnTimerLocked()
}

func (s *session) rearmOutOfTurnTimerLocked() {
	if s.outOfTurnTimer != nil {
		s.outOfTurnTimer.Stop()
		s.outOfTurnTimer = nil
	}
	if !s.outOfTurnActive || s.inPrompt {
		return
	}
	if s.openServerReqs > 0 || s.hasOpenWakeToolLocked() {
		return
	}
	wait := s.drv.outOfTurnDebounce()
	if s.outOfTurnReplyOpen {
		wait = s.drv.outOfTurnQuietLimit()
	} else if !s.outOfTurnSawUsage {
		wait = s.drv.outOfTurnToolWait()
	}
	s.outOfTurnTimer = time.AfterFunc(wait, s.settleOutOfTurn)
}

func (s *session) settleOutOfTurn() {
	s.mu.Lock()
	if !s.outOfTurnActive || s.inPrompt {
		s.mu.Unlock()
		return
	}
	if s.openServerReqs > 0 || s.hasOpenWakeToolLocked() {
		s.rearmOutOfTurnTimerLocked()
		s.mu.Unlock()
		return
	}
	sawReply := s.outOfTurnSawReply
	s.outOfTurnActive = false
	s.outOfTurnReplyOpen = false
	s.outOfTurnSawReply = false
	s.outOfTurnSawUsage = false
	s.outOfTurnTimer = nil

	var autoSettled []runtime.Event
	if sawReply {
		for _, bg := range s.bgTasks {
			if bg.running && bg.awaited && bg.asyncLaunch {
				bg.running = false
				autoSettled = append(autoSettled,
					runtime.Event{
						Kind:                runtime.EventBackgroundTask,
						TaskID:              bg.taskID,
						ItemID:              bg.itemID,
						ToolName:            bg.title,
						Command:             bg.command,
						Output:              bg.output,
						ExitCode:            0,
						HasExitCode:         true,
						BackgroundKind:      bg.kind,
						BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
						BackgroundStopScope: bg.stopScope,
						BackgroundAwaited:   bg.awaited,
					},
					runtime.Event{
						Kind:        runtime.EventToolCompleted,
						ItemID:      bg.itemID,
						ToolName:    bg.title,
						Command:     bg.command,
						ExitCode:    0,
						HasExitCode: true,
						Output:      bg.output,
					},
				)
			}
		}
	}
	s.mu.Unlock()

	for _, ev := range autoSettled {
		s.emit(ev)
	}
	s.flushAssistantSegment()
	s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
}

func (s *session) finishDeferredPrompt() {
	s.mu.Lock()
	if !s.awaitingMerge || s.inPrompt {
		s.mu.Unlock()
		return
	}
	s.awaitingMerge = false
	s.mergeTimer = nil
	s.mu.Unlock()

	s.flushAssistantSegment()
	s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
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

func isACPBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already processing") ||
		strings.Contains(msg, "session is busy") ||
		strings.Contains(msg, "agent is busy") ||
		strings.Contains(msg, "already running") ||
		strings.Contains(msg, "turn in progress")
}

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	s.mu.Lock()
	s.stopTimersLocked()
	s.inPrompt = true
	s.awaitingMerge = false
	s.outOfTurnActive = false
	s.outOfTurnReplyOpen = false
	s.outOfTurnSawReply = false
	s.outOfTurnSawUsage = false
	s.turnCtx = ctx
	s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
	s.msgBuf.Reset()
	keptTools := map[string]*acpToolState{}
	for id, st := range s.tools {
		if bg := s.bgTasks[id]; bg != nil && bg.running {
			keptTools[id] = st
		}
	}
	s.tools = keptTools
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
	err := s.sendPromptWithBusyRetry(ctx, prompt, 0)
	if err != nil {
		s.mu.Lock()
		s.inPrompt = false
		s.mu.Unlock()
	}
	return err
}

func (s *session) sendPromptWithBusyRetry(turnCtx context.Context, prompt []any, attempt int) error {
	params := map[string]any{
		"sessionId": s.sessionID,
		"prompt":    prompt,
	}
	return s.request("session/prompt", params, func(r rpcRes) {
		if r.err != nil && isACPBusyError(r.err) && attempt < 8 && s.Alive() && turnCtx.Err() == nil {
			delay := s.drv.busyPromptRetryDelay()
			go func() {
				select {
				case <-time.After(delay):
					s.mu.Lock()
					if !s.Alive() || !s.inPrompt {
						s.mu.Unlock()
						return
					}
					s.msgBuf.Reset()
					s.mu.Unlock()
					if err := s.sendPromptWithBusyRetry(turnCtx, prompt, attempt+1); err != nil {
						s.onPromptResult(rpcRes{err: err})
					}
				case <-turnCtx.Done():
					s.mu.Lock()
					s.inPrompt = false
					s.stopTimersLocked()
					s.mu.Unlock()
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
				case <-s.sessionCtx.Done():
				case <-s.proc.Done():
				}
			}()
			return
		}
		s.onPromptResult(r)
	})
}

func detectACPAsyncLaunch(rawInput, meta json.RawMessage) (isAsync bool, agentID string) {
	if len(rawInput) > 0 {
		var in struct {
			RunInBackground      *bool `json:"run_in_background"`
			RunInBackgroundCamel *bool `json:"runInBackground"`
			Background           *bool `json:"background"`
		}
		if json.Unmarshal(rawInput, &in) == nil {
			if (in.RunInBackground != nil && *in.RunInBackground) ||
				(in.RunInBackgroundCamel != nil && *in.RunInBackgroundCamel) ||
				(in.Background != nil && *in.Background) {
				isAsync = true
			}
		}
	}
	if len(meta) > 0 {
		var m struct {
			IsAsync    *bool  `json:"isAsync"`
			AgentID    string `json:"agentId"`
			TaskID     string `json:"taskId"`
			ClaudeCode struct {
				ToolResponse struct {
					IsAsync *bool  `json:"isAsync"`
					AgentID string `json:"agentId"`
					TaskID  string `json:"taskId"`
				} `json:"toolResponse"`
			} `json:"claudeCode"`
		}
		if json.Unmarshal(meta, &m) == nil {
			if (m.IsAsync != nil && *m.IsAsync) ||
				(m.ClaudeCode.ToolResponse.IsAsync != nil && *m.ClaudeCode.ToolResponse.IsAsync) {
				isAsync = true
			}
			for _, id := range []string{m.AgentID, m.TaskID, m.ClaudeCode.ToolResponse.AgentID, m.ClaudeCode.ToolResponse.TaskID} {
				if strings.TrimSpace(id) != "" {
					agentID = strings.TrimSpace(id)
					break
				}
			}
		}
	}
	return isAsync, agentID
}

func classifyACPBackgroundTool(st *acpToolState) (kind threadv2.BackgroundTaskKind, title string, cmd string, awaited bool, ok bool) {
	if st == nil || (!st.asyncLaunch && st.status != "in_progress" && st.status != "pending") {
		return 0, "", "", false, false
	}
	var in struct {
		IsDaemon         *bool  `json:"IsDaemon"`
		IsDaemonSnake    *bool  `json:"is_daemon"`
		Persistent       *bool  `json:"persistent"`
		ToolSummary      string `json:"toolSummary"`
		Description      string `json:"description"`
		CommandLine      string `json:"CommandLine"`
		CommandLineSnake string `json:"command_line"`
		Command          string `json:"command"`
		Role             string `json:"Role"`
		TypeName         string `json:"TypeName"`
		SubagentType     string `json:"subagent_type"`
		CronExpression   string `json:"CronExpression"`
		DurationSeconds  int64  `json:"DurationSeconds"`
	}
	_ = json.Unmarshal(st.rawInput, &in)
	isDaemon := (in.IsDaemon != nil && *in.IsDaemon) ||
		(in.IsDaemonSnake != nil && *in.IsDaemonSnake) ||
		(in.Persistent != nil && *in.Persistent)

	cmd = st.command
	for _, c := range []string{in.CommandLine, in.CommandLineSnake, in.Command} {
		if cmd == "" && strings.TrimSpace(c) != "" {
			cmd = strings.TrimSpace(c)
		}
	}
	title = strings.TrimSpace(st.title)
	for _, cand := range []string{in.ToolSummary, in.Description, in.Role, in.SubagentType, cmd} {
		if title == "" && strings.TrimSpace(cand) != "" {
			title = strings.TrimSpace(cand)
		}
	}
	lowerTitle := strings.ToLower(title)
	lowerKind := strings.ToLower(st.kind)

	switch {
	case lowerKind == "execute" || lowerKind == "command" || lowerKind == "bash" || cmd != "":
		if isDaemon {
			return threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON, title, cmd, false, true
		}
		return threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND, title, cmd, true, true
	case strings.Contains(lowerTitle, "schedule") || in.CronExpression != "" || in.DurationSeconds > 0:
		if isDaemon {
			return threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON, title, cmd, false, true
		}
		return threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE, title, cmd, true, true
	case strings.Contains(lowerTitle, "subagent") || strings.Contains(lowerTitle, "agent") || lowerKind == "agent" || in.Role != "" || in.TypeName != "" || in.SubagentType != "" || st.asyncLaunch:
		return threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, title, cmd, true, true
	default:
		return 0, "", "", false, false
	}
}

func (s *session) promoteOpenBackgroundWork() (newEvents []runtime.Event, hasAwaited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, st := range s.tools {
		kind, title, cmd, awaited, ok := classifyACPBackgroundTool(st)
		if !ok {
			continue
		}
		bg := s.bgTasks[id]
		if bg == nil {
			bg = &acpBgTask{
				taskID:      id,
				itemID:      id,
				agentID:     st.asyncAgentID,
				kind:        kind,
				title:       title,
				command:     cmd,
				output:      tailUTF8(st.output, 16384),
				awaited:     awaited,
				asyncLaunch: st.asyncLaunch,
				stopScope:   threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
				running:     true,
			}
			s.bgTasks[id] = bg
			newEvents = append(newEvents, runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			})
		}
		if bg.running && bg.awaited {
			hasAwaited = true
		}
	}

	for termID, at := range s.agentTerms {
		if !at.running {
			continue
		}
		taskKey := termID
		if at.toolID != "" {
			taskKey = at.toolID
		}
		bg := s.bgTasks[taskKey]
		if bg == nil {
			title := at.title
			if title == "" {
				title = at.command
			}
			if title == "" {
				title = "Terminal"
			}
			bg = &acpBgTask{
				taskID:    taskKey,
				itemID:    taskKey,
				kind:      threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
				title:     title,
				command:   at.command,
				output:    tailUTF8(at.outBuf.String(), 16384),
				awaited:   true,
				stopScope: threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
				running:   true,
			}
			s.bgTasks[taskKey] = bg
			newEvents = append(newEvents, runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			})
		}
		if bg.running && bg.awaited {
			hasAwaited = true
		}
	}

	for termID, mt := range s.terminals {
		select {
		case <-mt.done:
			continue
		default:
		}
		mt.mu.Lock()
		outSnap := tailUTF8(mt.outBuf.String(), 16384)
		mt.promoted = true
		mt.mu.Unlock()
		bg := s.bgTasks[termID]
		if bg == nil {
			bg = &acpBgTask{
				taskID:    termID,
				itemID:    termID,
				kind:      threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
				title:     mt.command,
				command:   mt.command,
				output:    outSnap,
				awaited:   true,
				stopScope: threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
				running:   true,
			}
			s.bgTasks[termID] = bg
			newEvents = append(newEvents, runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			})
		}
		if bg.running && bg.awaited {
			hasAwaited = true
		}
	}

	for _, bg := range s.bgTasks {
		if bg.running && bg.awaited {
			hasAwaited = true
		}
	}
	return newEvents, hasAwaited
}

func (s *session) onPromptResult(r rpcRes) {
	s.flushAssistantSegment()
	if r.err != nil {
		s.mu.Lock()
		s.inPrompt = false
		s.stopTimersLocked()
		s.mu.Unlock()
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
		s.mu.Lock()
		s.inPrompt = false
		s.stopTimersLocked()
		var stopped []runtime.Event
		for _, bg := range s.bgTasks {
			if bg.running {
				bg.running = false
				stopped = append(stopped, runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					Text:                "cancelled",
					BackgroundKind:      bg.kind,
					BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				})
			}
		}
		s.mu.Unlock()
		for _, ev := range stopped {
			s.emit(ev)
		}
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
	case "refusal", "max_tokens", "max_turn_requests":
		s.mu.Lock()
		s.inPrompt = false
		s.stopTimersLocked()
		s.mu.Unlock()
		s.emit(runtime.Event{
			Kind:         runtime.EventTurnFailed,
			ErrorCode:    res.StopReason,
			ErrorMessage: "acp turn stopped: " + res.StopReason,
		})
	default:
		bgEvents, hasAwaited := s.promoteOpenBackgroundWork()
		for _, ev := range bgEvents {
			s.emit(ev)
		}
		s.mu.Lock()
		s.inPrompt = false
		if hasAwaited {
			s.awaitingMerge = true
			s.mergeTimer = time.AfterFunc(s.drv.mergeWindow(), s.finishDeferredPrompt)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
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
	s.mu.Lock()
	wasOutOfTurn := s.outOfTurnActive
	s.outOfTurnActive = false
	s.outOfTurnReplyOpen = false
	s.outOfTurnSawReply = false
	s.outOfTurnSawUsage = false
	s.awaitingMerge = false
	s.stopTimersLocked()
	s.mu.Unlock()
	err := s.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": s.sessionID},
	})
	if wasOutOfTurn {
		s.flushAssistantSegment()
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
	}
	return err
}

func (s *session) StopBackgroundTask(ctx context.Context, taskID string) error {
	if taskID != "" {
		s.mu.Lock()
		mt := s.terminals[taskID]
		if mt != nil {
			delete(s.terminals, taskID)
			bg := s.bgTasks[taskID]
			if bg != nil {
				bg.running = false
			}
			s.mu.Unlock()
			if mt.cmd != nil && mt.cmd.Process != nil {
				_ = mt.cmd.Process.Kill()
			}
			if bg != nil {
				s.emit(runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					Text:                "stopped by user",
					BackgroundKind:      bg.kind,
					BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				})
			}
			return nil
		}
		s.mu.Unlock()
	}

	// Agent-owned ACP tasks (run_command / schedule / invoke_subagent) have no
	// single-task kill RPC in ACP v1; session/cancel stops all session background work.
	s.killTerminals()
	s.mu.Lock()
	var stopped []runtime.Event
	for _, bg := range s.bgTasks {
		if bg.running {
			bg.running = false
			stopped = append(stopped, runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				Text:                "stopped by user",
				BackgroundKind:      bg.kind,
				BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			})
		}
	}
	s.mu.Unlock()
	for _, ev := range stopped {
		s.emit(ev)
	}
	return s.Interrupt(ctx)
}

func (s *session) ForceStop() error {
	s.alive.Store(false)
	s.mu.Lock()
	s.stopTimersLocked()
	s.mu.Unlock()
	if s.sessionCancel != nil {
		s.sessionCancel()
	}
	s.killTerminals()
	s.closeOnce.Do(func() { s.proc.Stop(0) })
	return nil
}

func (s *session) Close() error {
	s.alive.Store(false)
	s.mu.Lock()
	s.stopTimersLocked()
	s.mu.Unlock()
	if s.sessionCancel != nil {
		s.sessionCancel()
	}
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
		case m.Method == "github.com/copilot/sessionEvent":
			if !s.replaying.Load() {
				s.handleCopilotSessionEvent(m.Params)
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
			TerminalID    string          `json:"terminalId"`
			Title         string          `json:"title"`
			Command       string          `json:"command"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			Output        string          `json:"output"`
			Chunk         string          `json:"chunk"`
			Data          string          `json:"data"`
			Encoding      string          `json:"encoding"`
			ExitCode      *int32          `json:"exitCode"`
			ExitStatus    *struct {
				ExitCode *int32 `json:"exitCode"`
			} `json:"exitStatus"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
			RawInput  json.RawMessage `json:"rawInput"`
			RawOutput json.RawMessage `json:"rawOutput"`
			Meta      json.RawMessage `json:"_meta"`
			Entries   []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
			Used uint64 `json:"used"`
			Size uint64 `json:"size"`
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
			s.noteWakeActivityLocked(true)
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
			s.noteWakeActivityLocked(true)
			msgID := s.msgItemID
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventReasoningDelta, ItemID: "rsn_" + msgID, Text: cb.Text})
		}
	case "terminal_update", "terminal_output_chunk":
		termID := u.TerminalID
		if termID == "" {
			termID = u.ToolCallID
		}
		if termID == "" {
			return
		}
		var chunkBytes []byte
		if u.Data != "" {
			if strings.EqualFold(u.Encoding, "utf8") || strings.EqualFold(u.Encoding, "text") {
				chunkBytes = []byte(u.Data)
			} else if decoded, err := base64.StdEncoding.DecodeString(u.Data); err == nil {
				chunkBytes = decoded
			} else if decodedRaw, errRaw := base64.RawStdEncoding.DecodeString(u.Data); errRaw == nil {
				chunkBytes = decodedRaw
			} else {
				chunkBytes = []byte(u.Data)
			}
		} else if u.Chunk != "" {
			chunkBytes = []byte(u.Chunk)
		}

		s.mu.Lock()
		at := s.agentTerms[termID]
		isNewTerm := false
		if at == nil {
			at = &acpAgentTerm{id: termID, running: true}
			s.agentTerms[termID] = at
			isNewTerm = true
		}
		if u.ToolCallID != "" && at.toolID == "" {
			at.toolID = u.ToolCallID
		}
		if u.Title != "" {
			at.title = u.Title
		}
		if u.Command != "" {
			at.command = u.Command
		}
		if len(chunkBytes) > 0 {
			at.appendBytes(chunkBytes)
		} else if u.Output != "" && u.SessionUpdate == "terminal_update" {
			at.carryUTF8 = at.carryUTF8[:0]
			at.outBuf.Reset()
			_, _ = at.outBuf.WriteString(u.Output)
		}
		exitCode := u.ExitCode
		if exitCode == nil && u.ExitStatus != nil {
			exitCode = u.ExitStatus.ExitCode
		}
		if exitCode != nil {
			at.exitCode = *exitCode
			at.hasExit = true
		}
		termDone := u.Status == "completed" || u.Status == "exited" || u.Status == "failed" || u.Status == "killed" || exitCode != nil
		if termDone {
			at.running = false
			at.flushCarry()
		}
		outSnap := tailUTF8(at.outBuf.String(), 16384)
		taskKey := termID
		if at.toolID != "" {
			taskKey = at.toolID
			if st := s.tools[at.toolID]; st != nil && outSnap != "" {
				st.output = outSnap
			}
		}
		bg := s.bgTasks[taskKey]
		var bgEv *runtime.Event
		var toolEv *runtime.Event
		standalone := at.toolID == ""
		if bg != nil && bg.running {
			bg.output = outSnap
			if at.command != "" && bg.command == "" {
				bg.command = at.command
			}
			if termDone && standalone {
				bg.running = false
				bgStatus := threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				code := at.exitCode
				if u.Status == "failed" || (at.hasExit && code != 0) {
					bgStatus = threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
					if code == 0 {
						code = 1
					}
				}
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					ExitCode:            code,
					HasExitCode:         true,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    bgStatus,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
				kept, _ := provider.KeepOutput(outSnap)
				toolEv = &runtime.Event{
					Kind:        runtime.EventToolCompleted,
					ItemID:      bg.itemID,
					ToolName:    bg.title,
					Command:     bg.command,
					ExitCode:    code,
					HasExitCode: true,
					Output:      kept,
				}
			} else if outSnap != "" {
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
			if s.outOfTurnActive {
				s.rearmOutOfTurnTimerLocked()
			}
		} else if standalone && isNewTerm && (at.command != "" || at.title != "") {
			name := at.title
			if name == "" {
				name = "Terminal"
			}
			toolEv = &runtime.Event{
				Kind:     runtime.EventToolStarted,
				ItemID:   termID,
				ToolName: name,
				Command:  at.command,
			}
		} else if standalone && termDone && (at.command != "" || at.title != "") {
			name := at.title
			if name == "" {
				name = "Terminal"
			}
			kept, _ := provider.KeepOutput(outSnap)
			toolEv = &runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      termID,
				ToolName:    name,
				Command:     at.command,
				ExitCode:    at.exitCode,
				HasExitCode: true,
				Output:      kept,
			}
		}
		s.mu.Unlock()
		if bgEv != nil {
			s.emit(*bgEv)
		}
		if toolEv != nil {
			s.emit(*toolEv)
		}

	case "tool_call", "tool_call_update":
		s.flushAssistantSegment()
		id := u.ToolCallID
		if id == "" {
			id = fmt.Sprintf("acp_tool_%d", s.itemSeq.Add(1))
		}
		contentOut, diffPath, diffNewText, refTermID := s.extractACPToolContent(u.Content)
		rawOut := extractACPRawOutput(u.RawOutput)
		inCmd, inPath := extractACPRawInput(u.RawInput)
		isAsyncIn, asyncAgentID := detectACPAsyncLaunch(u.RawInput, u.Meta)

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
		if refTermID != "" {
			st.terminalID = refTermID
			if at := s.agentTerms[refTermID]; at != nil && at.toolID == "" {
				at.toolID = id
			}
		}
		if isAsyncIn {
			st.asyncLaunch = true
		}
		if asyncAgentID != "" {
			st.asyncAgentID = asyncAgentID
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

		bg := s.bgTasks[id]
		var bgEv *runtime.Event
		// When an async-launch tool (`run_in_background: true` / `_meta...isAsync: true`)
		// immediately reports `status: "completed"`, that event is only the launch ack:
		// keep its transcript item running and register the live BackgroundTask right away.
		isLaunchAck := bg == nil && st.asyncLaunch && status == "completed"
		if isLaunchAck {
			st.status = "in_progress"
			status = "in_progress"
			if kind, bgTitle, bgCmd, awaited, ok := classifyACPBackgroundTool(st); ok {
				bg = &acpBgTask{
					taskID:      id,
					itemID:      id,
					agentID:     st.asyncAgentID,
					kind:        kind,
					title:       bgTitle,
					command:     bgCmd,
					output:      tailUTF8(outText, 16384),
					awaited:     awaited,
					asyncLaunch: true,
					stopScope:   threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
					running:     true,
				}
				s.bgTasks[id] = bg
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
		} else if bg != nil && bg.running {
			bg.output = tailUTF8(outText, 16384)
			if cmd != "" && bg.command == "" {
				bg.command = cmd
			}
			if status == "completed" || status == "failed" {
				bg.running = false
				bgStatus := threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				exitCode := int32(0)
				if status == "failed" {
					bgStatus = threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
					exitCode = 1
				}
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					ExitCode:            exitCode,
					HasExitCode:         true,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    bgStatus,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			} else if bg.output != "" {
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
			if s.outOfTurnActive {
				s.rearmOutOfTurnTimerLocked()
			}
		} else {
			s.noteWakeActivityLocked(status != "completed" && status != "failed")
		}
		s.mu.Unlock()

		if bgEv != nil {
			s.emit(*bgEv)
		}
		if status == "completed" || status == "failed" {
			exit := int32(0)
			if status == "failed" {
				exit = 1
			}
			kept, _ := provider.KeepOutput(outText)
			s.emit(runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      id,
				ToolName:    toolName,
				Command:     cmd,
				FilePath:    filePath,
				ExitCode:    exit,
				HasExitCode: true,
				Output:      kept,
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
		} else if !wasStarted || u.SessionUpdate == "tool_call" || isLaunchAck || toolName != "" || cmd != "" || filePath != "" {
			s.emit(runtime.Event{
				Kind:     runtime.EventToolStarted,
				ItemID:   id,
				ToolName: toolName,
				Command:  cmd,
				FilePath: filePath,
			})
		}
	case "plan":
		s.mu.Lock()
		s.noteWakeActivityLocked(true)
		s.mu.Unlock()
		var steps []*threadv2.TodoStep
		for _, e := range u.Entries {
			steps = append(steps, &threadv2.TodoStep{Text: e.Content, Status: e.Status})
		}
		s.emit(runtime.Event{Kind: runtime.EventTodoUpdated, TodoSteps: steps})
	case "usage_update":
		if u.Size > 0 || u.Used > 0 {
			s.emit(runtime.Event{
				Kind: runtime.EventUsage,
				Usage: &threadv2.ContextWindowUsage{
					ContextUsedTokens:  u.Used,
					ContextLimitTokens: u.Size,
				},
			})
		}
		s.mu.Lock()
		if s.outOfTurnActive {
			// If an out-of-turn wake started a new background command during the wake,
			// promote it before checking whether any foreground tool remains open.
			s.mu.Unlock()
			bgEvents, _ := s.promoteOpenBackgroundWork()
			for _, ev := range bgEvents {
				s.emit(ev)
			}
			s.mu.Lock()
			if s.outOfTurnActive {
				s.outOfTurnSawUsage = true
				s.outOfTurnReplyOpen = false
				s.rearmOutOfTurnTimerLocked()
			}
		}
		s.mu.Unlock()
	}
}

func (s *session) handleCopilotSessionEvent(params json.RawMessage) {
	var ev struct {
		Type      string `json:"type"`
		AgentID   string `json:"agentId"`
		AgentName string `json:"agentName"`
		Title     string `json:"title"`
		Data      struct {
			ToolCallID string `json:"toolCallId"`
			AgentID    string `json:"agentId"`
			AgentName  string `json:"agentName"`
			Title      string `json:"title"`
			Summary    string `json:"summary"`
			Output     string `json:"output"`
			Error      string `json:"error"`
		} `json:"data"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	agentID := pick(ev.AgentID, ev.Data.AgentID, ev.Data.ToolCallID)
	itemID := pick(ev.Data.ToolCallID, agentID)
	title := pick(ev.Title, ev.Data.Title, ev.AgentName, ev.Data.AgentName, "Subagent")

	switch ev.Type {
	case "subagent.started":
		if agentID == "" {
			agentID = fmt.Sprintf("cp_sub_%d", s.itemSeq.Add(1))
		}
		if itemID == "" {
			itemID = agentID
		}
		s.mu.Lock()
		bg := s.bgTasks[itemID]
		if bg == nil && agentID != itemID {
			bg = s.bgTasks[agentID]
		}
		if bg == nil {
			bg = &acpBgTask{
				taskID:    agentID,
				itemID:    itemID,
				agentID:   agentID,
				kind:      threadv2.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
				title:     title,
				awaited:   true,
				stopScope: threadv2.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
				running:   true,
			}
			s.bgTasks[agentID] = bg
		} else {
			bg.agentID = agentID
			bg.running = true
		}
		needToolStart := s.tools[itemID] == nil
		if needToolStart {
			s.tools[itemID] = &acpToolState{id: itemID, title: title, kind: "agent", status: "in_progress", started: true, asyncLaunch: true, asyncAgentID: agentID}
		} else {
			s.tools[itemID].status = "in_progress"
			s.tools[itemID].asyncLaunch = true
			s.tools[itemID].asyncAgentID = agentID
		}
		bgSnap := *bg
		s.mu.Unlock()

		if needToolStart {
			s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: itemID, ToolName: title})
		}
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              bgSnap.taskID,
			ItemID:              bgSnap.itemID,
			ToolName:            bgSnap.title,
			BackgroundKind:      bgSnap.kind,
			BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
			BackgroundStopScope: bgSnap.stopScope,
			BackgroundAwaited:   bgSnap.awaited,
		})

	case "subagent.completed", "subagent.failed":
		failed := ev.Type == "subagent.failed"
		out := pick(ev.Data.Output, ev.Data.Summary, ev.Data.Error)
		s.mu.Lock()
		var bg *acpBgTask
		for _, k := range []string{agentID, itemID} {
			if k != "" && s.bgTasks[k] != nil {
				bg = s.bgTasks[k]
				break
			}
		}
		if bg == nil && agentID != "" {
			for _, cand := range s.bgTasks {
				if cand.agentID == agentID && cand.running {
					bg = cand
					break
				}
			}
		}
		var bgEv *runtime.Event
		var toolEv *runtime.Event
		exitCode := int32(0)
		bgStatus := threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
		if failed {
			exitCode = 1
			bgStatus = threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
		}
		if bg != nil {
			bg.running = false
			if out != "" {
				bg.output = tailUTF8(out, 16384)
			}
			if st := s.tools[bg.itemID]; st != nil {
				if failed {
					st.status = "failed"
				} else {
					st.status = "completed"
				}
			}
			bgEv = &runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Output:              bg.output,
				Text:                ev.Data.Summary,
				ExitCode:            exitCode,
				HasExitCode:         true,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    bgStatus,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			}
			kept, _ := provider.KeepOutput(bg.output)
			toolEv = &runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      bg.itemID,
				ToolName:    bg.title,
				ExitCode:    exitCode,
				HasExitCode: true,
				Output:      kept,
			}
		}
		if s.outOfTurnActive {
			s.rearmOutOfTurnTimerLocked()
		}
		s.mu.Unlock()
		if bgEv != nil {
			s.emit(*bgEv)
		}
		if toolEv != nil {
			s.emit(*toolEv)
		}

	case "assistant.idle":
		s.mu.Lock()
		if s.outOfTurnActive && !s.inPrompt {
			s.outOfTurnSawUsage = true
			s.outOfTurnReplyOpen = false
			s.rearmOutOfTurnTimerLocked()
		}
		s.mu.Unlock()
	}
}

func (s *session) extractACPToolContent(raw json.RawMessage) (output string, diffPath string, diffNewText string, terminalID string) {
	if len(raw) == 0 {
		return "", "", "", ""
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
			return single.Text, "", "", ""
		}
		return "", "", "", ""
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
				if terminalID == "" {
					terminalID = it.TerminalID
				}
				s.mu.Lock()
				mt := s.terminals[it.TerminalID]
				var agentOut string
				if at := s.agentTerms[it.TerminalID]; at != nil {
					agentOut = at.outBuf.String()
				}
				s.mu.Unlock()
				if mt != nil {
					mt.mu.Lock()
					tOut := mt.outBuf.String()
					mt.mu.Unlock()
					if tOut != "" {
						parts = append(parts, tOut)
					}
				} else if agentOut != "" {
					parts = append(parts, agentOut)
				}
			}
		}
	}
	return strings.Join(parts, "\n"), diffPath, diffNewText, terminalID
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
		Command          string `json:"command"`
		CommandLine      string `json:"CommandLine"`
		CommandLineSnake string `json:"command_line"`
		Path             string `json:"path"`
		FilePath         string `json:"file_path"`
		FilePathCamel    string `json:"filePath"`
		Pattern          string `json:"pattern"`
		Query            string `json:"query"`
		URL              string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, p := range []string{obj.Path, obj.FilePath, obj.FilePathCamel, obj.Pattern, obj.Query, obj.URL} {
			if p != "" {
				path = p
				break
			}
		}
		for _, c := range []string{obj.Command, obj.CommandLine, obj.CommandLineSnake} {
			if c != "" {
				cmd = c
				break
			}
		}
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
		out, _, diffText, _ := s.extractACPToolContent(content)
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
	isDetachedWakeReq := method == "session/request_permission" ||
		method == "elicitation/create" ||
		method == "session/create_elicitation" ||
		method == "fs/write_text_file" ||
		method == "terminal/create"
	if isDetachedWakeReq {
		s.noteWakeActivityLocked(true)
	}
	s.openServerReqs++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.openServerReqs > 0 {
			s.openServerReqs--
		}
		if s.outOfTurnActive {
			if isDetachedWakeReq {
				s.outOfTurnReplyOpen = true
			}
			s.rearmOutOfTurnTimerLocked()
		}
		s.mu.Unlock()
	}()

	tctx := s.gateCtx()

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
		mt := &managedTerm{id: termID, command: fullCmd, cmd: cmd, done: make(chan struct{})}
		lw := &lockedTermWriter{s: s, mt: mt}
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

			s.mu.Lock()
			bg := s.bgTasks[termID]
			var bgEv *runtime.Event
			if bg != nil && bg.running {
				bg.running = false
				bg.output = tailUTF8(outStr, 16384)
				bgStatus := threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				if mt.exit != 0 {
					bgStatus = threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
				}
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					ExitCode:            int32(mt.exit),
					HasExitCode:         true,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    bgStatus,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
			s.mu.Unlock()
			if bgEv != nil {
				s.emit(*bgEv)
			}
			s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: termID, ToolName: "Terminal", Command: fullCmd, ExitCode: int32(mt.exit), HasExitCode: true, Output: kept})
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
	s  *session
	mt *managedTerm
}

func (w *lockedTermWriter) Write(p []byte) (int, error) {
	w.mt.mu.Lock()
	n, err := w.mt.outBuf.Write(p)
	now := time.Now()
	shouldEmit := w.mt.promoted && now.Sub(w.mt.lastFlush) >= 250*time.Millisecond
	var snap string
	if shouldEmit {
		w.mt.lastFlush = now
		snap = tailUTF8(w.mt.outBuf.String(), 16384)
	}
	w.mt.mu.Unlock()
	if shouldEmit && w.s != nil {
		w.s.mu.Lock()
		bg := w.s.bgTasks[w.mt.id]
		var ev *runtime.Event
		if bg != nil && bg.running {
			bg.output = snap
			ev = &runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    threadv2.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			}
		}
		w.s.mu.Unlock()
		if ev != nil {
			w.s.emit(*ev)
		}
	}
	return n, err
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
