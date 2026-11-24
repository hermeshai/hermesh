// Package claude implements the long-lived Hermesh runtime.Driver for Claude
// Code over its bidirectional stream-json stdio control protocol.
package claude

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type Driver struct {
	PathEnv string
	Binary  string
}

func NewDriver(pathEnv string) *Driver { return &Driver{PathEnv: pathEnv} }

func (d *Driver) Kind() string { return "claude" }
func (d *Driver) Name() string { return "claude" }

func (d *Driver) Capabilities(_ string) *sessionv1.ProviderCapabilities {
	return &sessionv1.ProviderCapabilities{
		InSessionModelSwitch:  true,
		InSessionOptionSwitch: false, // effort flag requires fast --resume cycle
		InSessionModeSwitch:   true,
		MidTurnSteer:          true,
		LiveApproval:          true,
		LiveUserInput:         true,
		NativeResume:          true,
		NativeFork:            true,
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

var (
	versionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)
	efforts   = []string{"low", "medium", "high", "xhigh", "max"}
)

// Models are the aliases Claude Code takes for its latest models, listed
// when it does not report which it offers.
var Models = []runtime.Model{
	{ID: "fable", Name: "Fable", Efforts: efforts},
	{ID: "opus", Name: "Opus", Efforts: efforts},
	{ID: "sonnet", Name: "Sonnet", Efforts: efforts},
	{ID: "haiku", Name: "Haiku", Efforts: efforts},
}

func (d *Driver) resolveBinary(cfg *sessionv1.ProviderInstanceConfig) string {
	if cfg != nil && cfg.BinaryPath != "" {
		return cfg.BinaryPath
	}
	if d.Binary != "" {
		return d.Binary
	}
	home, _ := os.UserHomeDir()
	return runtime.LookPath("claude", runtime.SearchPath(d.PathEnv),
		filepath.Join(home, ".local/bin/claude"),
		filepath.Join(home, ".claude/local/claude"),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		desktopCLI(home),
	)
}

func desktopCLI(home string) string {
	dirs, _ := filepath.Glob(filepath.Join(home, "Library/Application Support/Claude/claude-code/*"))
	slices.SortFunc(dirs, func(a, b string) int {
		return slices.Compare(versionKey(filepath.Base(b)), versionKey(filepath.Base(a)))
	})
	for _, dir := range dirs {
		bins, _ := filepath.Glob(filepath.Join(dir, "*/claude.app/Contents/MacOS/claude"))
		if p := runtime.LookPath("claude", "", bins...); p != "" {
			return p
		}
	}
	return ""
}

func versionKey(v string) []int {
	var k []int
	for _, s := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(s)
		k = append(k, n)
	}
	return k
}

func (d *Driver) Discover(ctx context.Context) runtime.Info {
	info := runtime.Info{Name: "claude"}
	bin := d.resolveBinary(nil)
	if !runtime.Exists(bin) {
		info.Problem, info.Detail = runtime.ProblemNotFound, "claude not found"
		if app := runtime.AppWithoutCLI("Claude.app"); app != "" && d.Binary == "" {
			info.Problem, info.Detail = runtime.ProblemAppWithoutCLI, app
		}
		return info
	}
	info.Binary = bin
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	env := runtime.ProbeEnv(d.PathEnv)
	out, problem, detail := runtime.Probe(ctx, env, bin, "--version")
	if problem != runtime.ProblemNone {
		info.Problem, info.Detail = problem, detail
		return info
	}
	info.Available = true
	info.Version = versionRE.FindString(string(out))
	if out, problem, _ := runtime.Probe(ctx, env, bin, "auth", "status"); problem == runtime.ProblemNone {
		var st struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(out, &st) == nil {
			info.LoggedIn = st.LoggedIn
		}
	}
	if !info.LoggedIn && os.Getenv("ANTHROPIC_API_KEY") != "" {
		info.LoggedIn = true
	}
	info.Models, info.ModelsState, info.ModelsAt = Models, runtime.ModelsKnown, time.Now()
	if ms, err := listModels(ctx, env, bin); err == nil && len(ms) > 0 {
		info.Models = ms
	}
	return info
}

func listModels(ctx context.Context, env []string, bin string) ([]runtime.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "hermesh-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	proc, err := runtime.StartProc(ctx, bin, []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--settings", `{"crossSessionInbound":"refuse"}`,
	}, dir, env)
	if err != nil {
		return nil, err
	}
	defer proc.Stop(time.Second)
	answer := make(chan []byte, 1)
	read := make(chan struct{})
	go func() {
		defer close(read)
		proc.Lines(func(b []byte) {
			if bytes.Contains(b, []byte(`"hermesh-models"`)) {
				select {
				case answer <- bytes.Clone(b):
				default:
				}
			}
		})
	}()
	if err := proc.WriteLine([]byte(`{"type":"control_request","request_id":"hermesh-models","request":{"subtype":"initialize"}}`)); err != nil {
		return nil, err
	}
	select {
	case b := <-answer:
		return modelsFrom(b)
	case <-read:
		return nil, errors.New("claude gave no models")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func modelsFrom(line []byte) ([]runtime.Model, error) {
	var m struct {
		Type     string
		Response struct {
			Subtype   string
			RequestID string `json:"request_id"`
			Error     string
			Response  struct {
				Models []struct {
					Value, DisplayName    string
					SupportedEffortLevels []string
				}
			}
		}
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	if m.Type != "control_response" || m.Response.RequestID != "hermesh-models" || m.Response.Subtype != "success" {
		return nil, fmt.Errorf("claude: %s", cmp.Or(m.Response.Error, "no models"))
	}
	var ms []runtime.Model
	for _, x := range m.Response.Response.Models {
		if x.Value != "" && x.Value != "default" {
			ms = append(ms, runtime.Model{ID: x.Value, Name: x.DisplayName, Efforts: x.SupportedEffortLevels})
		}
	}
	return ms, nil
}

func (d *Driver) Probe(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	bin := d.resolveBinary(cfg)
	if !runtime.Exists(bin) {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "claude CLI not found on PATH",
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv)
	out, prob, detail := runtime.Probe(pctx, env, bin, "--version")
	if prob != runtime.ProblemNone {
		return runtime.ProbeResult{
			Installed:            true,
			Binary:               bin,
			Compatibility:        sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_BROKEN,
			CompatibilityMessage: detail,
		}
	}
	res := runtime.ProbeResult{
		Installed:     true,
		Binary:        bin,
		Version:       versionRE.FindString(string(out)),
		Compatibility: sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
	if authOut, authProb, _ := runtime.Probe(pctx, env, bin, "auth", "status"); authProb == runtime.ProblemNone {
		var st struct {
			LoggedIn bool   `json:"loggedIn"`
			Email    string `json:"email"`
		}
		if json.Unmarshal(authOut, &st) == nil {
			res.Authenticated = st.LoggedIn
			res.AuthLabel = st.Email
		}
	}
	if !res.Authenticated && (os.Getenv("ANTHROPIC_API_KEY") != "" || instEnv["ANTHROPIC_API_KEY"] != "") {
		res.Authenticated = true
		res.AuthLabel = "ANTHROPIC_API_KEY"
	}
	return res
}

func BuildArgs(spec runtime.StartSpec) []string {
	permMode := "default"
	if spec.InteractionMode == sessionv1.InteractionMode_INTERACTION_MODE_PLAN {
		permMode = "plan"
	}
	var args []string
	if spec.Instance != nil && len(spec.Instance.LaunchArgs) > 0 {
		args = append(args, spec.Instance.LaunchArgs...)
	}
	args = append(args,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--replay-user-messages",
		"--include-partial-messages",
		"--permission-prompt-tool", "stdio",
		"--permission-mode", permMode,
		"--settings", `{"crossSessionInbound":"refuse"}`,
	)
	for _, dir := range spec.ReadDirs {
		if dir != "" {
			args = append(args, "--add-dir", dir)
		}
	}
	if spec.ResumeCursor != "" {
		args = append(args, "--resume", spec.ResumeCursor)
	}
	if spec.Selection != nil {
		if m := strings.TrimSpace(spec.Selection.Model); m != "" && m != "default" {
			args = append(args, "--model", m)
		}
		if opt := spec.Selection.Options["reasoningEffort"]; opt != nil {
			if sv := opt.GetStringValue(); sv != "" {
				args = append(args, "--effort", sv)
			}
		}
		if opt := spec.Selection.Options["fastMode"]; opt != nil && opt.GetBoolValue() {
			args = append(args, "--settings", `{"fastMode":true}`)
		}
	}
	if len(spec.MCPServers) > 0 {
		servers := map[string]any{}
		for _, m := range spec.MCPServers {
			servers[m.Name] = map[string]any{"type": "stdio", "command": m.Command, "args": m.Args, "env": m.Env}
		}
		b, _ := json.Marshal(map[string]any{"mcpServers": servers})
		args = append(args, "--mcp-config", string(b))
	}
	return args
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	bin := d.resolveBinary(spec.Instance)
	if !runtime.Exists(bin) {
		return nil, runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_START_FAILED, "claude CLI not found")
	}
	var instEnv map[string]string
	if spec.Instance != nil {
		instEnv = spec.Instance.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv, spec.ExtraEnv)
	proc, err := runtime.StartProc(ctx, bin, BuildArgs(spec), spec.Workdir, env)
	if err != nil {
		return nil, err
	}
	sessCtx, sessCancel := context.WithCancel(ctx)
	s := &session{
		proc:            proc,
		spec:            spec,
		selection:       proto.Clone(spec.Selection).(*sessionv1.ModelSelection),
		events:          make(chan runtime.Event, 256),
		skipToolResult:  map[string]bool{},
		asyncToolInputs: map[string]claudeAsyncInput{},
		bgTasks:         map[string]*claudeBgTask{},
		sessionCtx:      sessCtx,
		sessionCancel:   sessCancel,
		turnCtx:         sessCtx,
	}
	s.alive.Store(true)
	go s.readLoop()

	initMsg := map[string]any{
		"type":       "control_request",
		"request_id": "hermesh-init",
		"request": map[string]any{
			"subtype": "initialize",
		},
	}
	if err := s.writeJSON(initMsg); err != nil {
		proc.Stop(time.Second)
		return nil, err
	}
	return s, nil
}

type claudeAsyncInput struct {
	kind    sessionv1.BackgroundTaskKind
	title   string
	command string
}

type claudeBgTask struct {
	taskID     string
	itemID     string
	kind       sessionv1.BackgroundTaskKind
	title      string
	command    string
	outputFile string
	outputTail string
	running    bool
	tailing    bool
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func (b *claudeBgTask) stopTail() {
	if b != nil && b.stopCh != nil {
		b.stopOnce.Do(func() { close(b.stopCh) })
	}
}

type session struct {
	proc      *runtime.Proc
	spec      runtime.StartSpec
	events    chan runtime.Event
	alive     atomic.Bool
	reqSeq    atomic.Uint64
	itemSeq   atomic.Uint64
	closeOnce sync.Once

	emitMu       sync.RWMutex
	eventsClosed bool

	mu              sync.Mutex
	selection       *sessionv1.ModelSelection
	sessionCtx      context.Context
	sessionCancel   context.CancelFunc
	turnCtx         context.Context
	inTurn          bool
	sent            int
	seen            int
	msgItemID       string
	msgUsed         bool
	skipToolResult  map[string]bool
	asyncToolInputs map[string]claudeAsyncInput // tool_use_id -> async launch metadata
	bgTasks         map[string]*claudeBgTask    // task_id -> background task state
}

func (s *session) gateCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inTurn && s.turnCtx != nil && s.turnCtx.Err() == nil {
		return s.turnCtx
	}
	return s.sessionCtx
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

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	s.mu.Lock()
	s.turnCtx = ctx
	s.inTurn = true
	s.sent++
	s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
	s.msgUsed = false
	s.skipToolResult = map[string]bool{}
	s.mu.Unlock()
	return s.writeUserMessage(in.Prompt, in.Attachments)
}

func (s *session) Steer(_ context.Context, text string, atts []runtime.TurnAttachment) error {
	s.mu.Lock()
	if !s.inTurn {
		s.mu.Unlock()
		return errors.New("no active turn to steer")
	}
	s.sent++
	s.mu.Unlock()
	return s.writeUserMessage(text, atts)
}

func (s *session) writeUserMessage(text string, atts []runtime.TurnAttachment) error {
	var content any = text
	var blocks []any
	if len(atts) > 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
		for _, a := range atts {
			if strings.HasPrefix(a.MimeType, "image/") && len(a.Data) > 0 {
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": a.MimeType,
						"data":       base64.StdEncoding.EncodeToString(a.Data),
					},
				})
			} else if a.Path != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": fmt.Sprintf("\n[Attached file: %s (%s)]", a.Path, a.Name),
				})
			}
		}
		content = blocks
	}
	err := s.writeJSON(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	})
	if err != nil {
		s.mu.Lock()
		s.sent--
		s.mu.Unlock()
	}
	return err
}

func (s *session) SetSelection(_ context.Context, sel *sessionv1.ModelSelection) error {
	if sel == nil {
		return nil
	}
	s.mu.Lock()
	prev := s.selection
	s.mu.Unlock()
	if prev != nil {
		if prev.Options["reasoningEffort"].GetStringValue() != sel.Options["reasoningEffort"].GetStringValue() ||
			prev.Options["fastMode"].GetBoolValue() != sel.Options["fastMode"].GetBoolValue() {
			return runtime.ErrHotSwitchUnsupported
		}
	}
	id := fmt.Sprintf("hermesh-model-%d", s.reqSeq.Add(1))
	if err := s.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request": map[string]any{
			"subtype": "set_model",
			"model":   sel.Model,
		},
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.selection = proto.Clone(sel).(*sessionv1.ModelSelection)
	s.mu.Unlock()
	return nil
}

func (s *session) SetModes(_ context.Context, _ identityv1.RuntimeMode, im sessionv1.InteractionMode) error {
	mode := "default"
	if im == sessionv1.InteractionMode_INTERACTION_MODE_PLAN {
		mode = "plan"
	}
	id := fmt.Sprintf("hermesh-mode-%d", s.reqSeq.Add(1))
	return s.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request": map[string]any{
			"subtype": "set_permission_mode",
			"mode":    mode,
		},
	})
}

func (s *session) Interrupt(context.Context) error {
	id := fmt.Sprintf("hermesh-int-%d", s.reqSeq.Add(1))
	return s.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]any{"subtype": "interrupt"},
	})
}

func (s *session) StopBackgroundTask(ctx context.Context, taskID string) error {
	taskID = strings.TrimSpace(taskID)
	s.mu.Lock()
	var targets []*claudeBgTask
	if taskID != "" {
		if bg := s.bgTasks[taskID]; bg != nil && bg.running {
			targets = append(targets, bg)
		}
	} else {
		for _, bg := range s.bgTasks {
			if bg.running {
				targets = append(targets, bg)
			}
		}
	}
	inTurn := s.inTurn
	for _, bg := range targets {
		bg.running = false
		bg.stopTail()
	}
	s.mu.Unlock()

	var firstErr error
	for _, bg := range targets {
		if !strings.HasPrefix(bg.taskID, "claude-schedule-") {
			reqID := fmt.Sprintf("hermesh-stop-task-%d", s.reqSeq.Add(1))
			if err := s.writeJSON(map[string]any{
				"type":       "control_request",
				"request_id": reqID,
				"request": map[string]any{
					"subtype": "stop_task",
					"task_id": bg.taskID,
				},
			}); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              bg.taskID,
			ItemID:              bg.itemID,
			ToolName:            bg.title,
			Command:             bg.command,
			Output:              bg.outputTail,
			Text:                "stopped by user",
			ExitCode:            130,
			HasExitCode:         true,
			BackgroundKind:      bg.kind,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		})
		if bg.itemID != "" {
			s.emit(runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      bg.itemID,
				ToolName:    bg.title,
				Command:     bg.command,
				ExitCode:    130,
				HasExitCode: true,
				Output:      bg.outputTail,
			})
		}
	}
	if taskID == "" && inTurn {
		if err := s.Interrupt(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *session) ForceStop() error {
	s.alive.Store(false)
	if s.sessionCancel != nil {
		s.sessionCancel()
	}
	s.closeOnce.Do(func() { s.proc.Stop(0) })
	return nil
}

func (s *session) Close() error {
	s.alive.Store(false)
	if s.sessionCancel != nil {
		s.sessionCancel()
	}
	s.closeOnce.Do(func() { s.proc.Stop(2 * time.Second) })
	return nil
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

// rateLimitInfo is a rate_limit_event's (Claude Code 2.1.330,
// testdata/claude-2.1.330-rate-limit-event.json): each window by name, how
// much of it is used (0 to 1) and when it starts again (Unix seconds).
type rateLimitInfo struct {
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

// limitMinutes are the windows Claude Code names, by their length; one it
// names otherwise is left out.
var limitMinutes = map[string]uint32{"five_hour": 5 * 60, "seven_day": 7 * 24 * 60}

func (r *rateLimitInfo) limits() (runtime.Limits, bool) {
	var out runtime.Limits
	for name, w := range r.UnifiedWindows {
		if m, ok := limitMinutes[name]; ok && w.ResetsAt > 0 {
			out.Windows = append(out.Windows, runtime.LimitWindow{Minutes: m, UsedPercent: uint32(min(100, max(0, math.Round(w.Utilization*100)))), ResetsAt: time.Unix(w.ResetsAt, 0)})
		}
	}
	slices.SortFunc(out.Windows, func(a, b runtime.LimitWindow) int { return cmp.Compare(a.Minutes, b.Minutes) })
	return out, len(out.Windows) > 0
}

type wireLine struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	SessionID   string `json:"session_id"`
	Model       string `json:"model"`
	TaskID      string `json:"task_id"`
	ToolUseID   string `json:"tool_use_id"`
	Description string `json:"description"`
	TaskType    string `json:"task_type"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	OutputFile  string `json:"output_file"`
	Tasks       []struct {
		TaskID      string `json:"task_id"`
		TaskType    string `json:"task_type"`
		Description string `json:"description"`
	} `json:"tasks"`
	Message        json.RawMessage `json:"message"`
	RequestID      string          `json:"request_id"`
	Request        json.RawMessage `json:"request"`
	Result         string          `json:"result"`
	IsError        bool            `json:"is_error"`
	IsReplay       bool            `json:"isReplay"`
	Event          json.RawMessage `json:"event"`
	ParentToolID   string          `json:"parent_tool_use_id"`
	Errors         []string        `json:"errors"`
	TerminalReason string          `json:"terminal_reason"`
	CostUSD        float64         `json:"total_cost_usd"`
	Usage          struct {
		InputTokens  uint64 `json:"input_tokens"`
		CacheCreate  uint64 `json:"cache_creation_input_tokens"`
		CacheRead    uint64 `json:"cache_read_input_tokens"`
		OutputTokens uint64 `json:"output_tokens"`
	} `json:"usage"`

	// rate_limit_event lines: the account's windows (once a turn).
	RateLimitInfo *rateLimitInfo `json:"rate_limit_info"`
}

func (s *session) readLoop() {
	var heldResult *wireLine
	defer func() {
		if heldResult != nil {
			s.emitResultLine(*heldResult)
		}
		s.alive.Store(false)
		s.closeEvents()
	}()
	_ = s.proc.Lines(func(raw []byte) {
		var l wireLine
		if json.Unmarshal(bytes.TrimSpace(raw), &l) != nil {
			return
		}
		switch l.Type {
		case "rate_limit_event":
			if s.spec.Limits != nil && l.RateLimitInfo != nil {
				if lim, ok := l.RateLimitInfo.limits(); ok {
					s.spec.Limits(lim)
				}
			}
		case "system":
			switch l.Subtype {
			case "init":
				if l.SessionID != "" {
					s.emit(runtime.Event{
						Kind:         runtime.EventNativeSession,
						NativeID:     l.SessionID,
						ResumeCursor: l.SessionID,
						Model:        l.Model,
					})
				}
			case "task_started", "task_progress", "task_notification":
				s.handleTaskSystemLine(l)
			case "background_tasks_changed":
				s.handleBackgroundTasksChanged(l)
			}

		case "stream_event":
			if l.ParentToolID != "" {
				return
			}
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			}
			if json.Unmarshal(l.Event, &ev) == nil && ev.Type == "content_block_delta" {
				if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
					s.settleScheduledWakeups()
					s.mu.Lock()
					msgID := s.msgItemID
					s.msgUsed = true
					s.mu.Unlock()
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgID, Text: ev.Delta.Text})
				} else if ev.Delta.Type == "thinking_delta" && ev.Delta.Thinking != "" {
					s.mu.Lock()
					msgID := s.msgItemID
					s.mu.Unlock()
					s.emit(runtime.Event{Kind: runtime.EventReasoningDelta, ItemID: "rsn_" + msgID, Text: ev.Delta.Thinking})
				}
			}

		case "assistant":
			if l.ParentToolID != "" {
				return
			}
			var m struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			}
			if json.Unmarshal(l.Message, &m) == nil {
				for _, c := range m.Content {
					switch c.Type {
					case "text":
						if strings.TrimSpace(c.Text) != "" {
							s.settleScheduledWakeups()
							s.mu.Lock()
							msgID := s.msgItemID
							s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
							s.msgUsed = false
							s.mu.Unlock()
							s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: msgID, Text: c.Text})
						}
					case "tool_use":
						s.settleScheduledWakeups()
						s.mu.Lock()
						if s.msgUsed {
							s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
							s.msgUsed = false
						}
						if c.Name == "AskUserQuestion" || c.Name == "ExitPlanMode" {
							if c.ID != "" {
								s.skipToolResult[c.ID] = true
							}
							s.mu.Unlock()
							continue
						}
						asyncMeta, isAsyncLaunch := detectClaudeAsyncTool(c.Name, c.Input)
						if isAsyncLaunch && c.ID != "" {
							s.asyncToolInputs[c.ID] = asyncMeta
							// Attach command to an already-arrived task_started if ordered first.
							for _, bg := range s.bgTasks {
								if bg.itemID == c.ID && bg.command == "" && asyncMeta.command != "" {
									bg.command = asyncMeta.command
								}
							}
						}
						s.mu.Unlock()
						_, cmd, targetPath := classifyTool(c.Name, c.Input)
						summaryPath := toolSummaryPath(c.Input, targetPath)
						s.emit(runtime.Event{
							Kind:     runtime.EventToolStarted,
							ItemID:   c.ID,
							ToolName: c.Name,
							Command:  cmd,
							FilePath: summaryPath,
						})
						if isAsyncLaunch && asyncMeta.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE && c.ID != "" {
							schedID := "claude-schedule-" + c.ID
							s.mu.Lock()
							for _, prev := range s.bgTasks {
								if prev.running && prev.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE {
									prev.running = false
								}
							}
							s.bgTasks[schedID] = &claudeBgTask{
								taskID:  schedID,
								itemID:  c.ID,
								kind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE,
								title:   asyncMeta.title,
								running: true,
							}
							s.mu.Unlock()
							s.emit(runtime.Event{
								Kind:                runtime.EventBackgroundTask,
								TaskID:              schedID,
								ItemID:              c.ID,
								ToolName:            asyncMeta.title,
								BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE,
								BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
								BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
								BackgroundAwaited:   true,
							})
						}
					}
				}
			}

		case "user":
			if l.IsReplay {
				s.mu.Lock()
				s.seen++
				s.mu.Unlock()
				heldResult = nil
				return
			}
			if l.ParentToolID != "" {
				return
			}
			var m struct {
				Content []struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
					IsError   bool            `json:"is_error"`
				} `json:"content"`
			}
			if json.Unmarshal(l.Message, &m) == nil {
				for _, c := range m.Content {
					if c.Type == "tool_result" && c.ToolUseID != "" {
						s.mu.Lock()
						skip := s.skipToolResult[c.ToolUseID]
						asyncMeta, wasAsyncLaunch := s.asyncToolInputs[c.ToolUseID]
						hasRunningBg := false
						for _, bg := range s.bgTasks {
							if bg.itemID == c.ToolUseID && bg.running {
								hasRunningBg = true
								break
							}
						}
						s.mu.Unlock()
						if skip {
							continue
						}
						text := extractToolResultText(c.Content)
						// Async Bash/Monitor/Subagent/ScheduleWakeup immediately return a launch
						// acknowledgement tool_result while the real background task continues running!
						if !c.IsError && (hasRunningBg || wasAsyncLaunch) {
							if !hasRunningBg && wasAsyncLaunch {
								// Synthesize background task if CLI version didn't emit task_started first.
								synthID := extractClaudeBgIDFromAck(text)
								if synthID == "" {
									synthID = "claude-bg-" + c.ToolUseID
								}
								s.mu.Lock()
								if s.bgTasks[synthID] == nil {
									s.bgTasks[synthID] = &claudeBgTask{
										taskID:  synthID,
										itemID:  c.ToolUseID,
										kind:    asyncMeta.kind,
										title:   asyncMeta.title,
										command: asyncMeta.command,
										running: true,
									}
									s.mu.Unlock()
									s.emit(runtime.Event{
										Kind:                runtime.EventBackgroundTask,
										TaskID:              synthID,
										ItemID:              c.ToolUseID,
										ToolName:            asyncMeta.title,
										Command:             asyncMeta.command,
										BackgroundKind:      asyncMeta.kind,
										BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
										BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
										BackgroundAwaited:   true,
									})
								} else {
									s.mu.Unlock()
								}
							}
							continue
						}
						kept, _ := runtime.KeepOutput(text)
						exit := int32(0)
						if c.IsError {
							exit = 1
						}
						s.emit(runtime.Event{
							Kind:     runtime.EventToolCompleted,
							ItemID:   c.ToolUseID,
							ExitCode: exit,
							Output:   kept,
						})
					}
				}
			}

		case "result":
			inTok := l.Usage.InputTokens + l.Usage.CacheCreate + l.Usage.CacheRead
			s.emit(runtime.Event{
				Kind: runtime.EventUsage,
				Usage: &sessionv1.ContextWindowUsage{
					InputTokens:        inTok,
					OutputTokens:       l.Usage.OutputTokens,
					CacheReadTokens:    l.Usage.CacheRead,
					ContextUsedTokens:  inTok + l.Usage.OutputTokens,
					ContextLimitTokens: 200000,
				},
			})
			isTerminalErr := l.IsError || l.TerminalReason == "prompt_too_long"
			s.mu.Lock()
			if s.seen < s.sent && !isTerminalErr {
				cp := l
				heldResult = &cp
				s.mu.Unlock()
				return
			}
			heldResult = nil
			s.inTurn = false
			s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
			s.msgUsed = false
			s.mu.Unlock()

			s.emitResultLine(l)

		case "control_request":
			go s.handleControlRequest(l.RequestID, l.Request)
		}
	})
}

func (s *session) emitResultLine(l wireLine) {
	if l.Subtype == "interrupted" || l.TerminalReason == "interrupted" {
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
		return
	}
	if l.IsError || l.TerminalReason == "prompt_too_long" {
		msg := strings.TrimSpace(strings.Join(l.Errors, "; "))
		if msg == "" {
			msg = strings.TrimSpace(l.Result)
		}
		if msg == "" && l.TerminalReason == "prompt_too_long" {
			msg = "prompt too long for context window"
		}
		if msg == "" {
			msg = "claude reported an error"
		}
		code := l.Subtype
		if code == "" {
			code = l.TerminalReason
		}
		kind := runtime.Classify(msg)
		if l.TerminalReason == "prompt_too_long" {
			kind = sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW
		} else if s.spec.ResumeCursor != "" && runtime.IsResumeRejected(msg) {
			kind = sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED
		}
		s.emit(runtime.Event{
			Kind:         runtime.EventTurnFailed,
			ErrorCode:    code,
			ErrorMessage: msg,
			Failure:      &runtime.Failure{Kind: kind, Message: msg},
		})
		return
	}
	s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
}

func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func toolSummaryPath(raw json.RawMessage, targetPath string) string {
	if targetPath != "" {
		return targetPath
	}
	var extra struct {
		Pattern string `json:"pattern"`
		URL     string `json:"url"`
		Query   string `json:"query"`
	}
	if json.Unmarshal(raw, &extra) == nil {
		for _, s := range []string{extra.Pattern, extra.URL, extra.Query} {
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func (s *session) handleControlRequest(reqID string, raw json.RawMessage) {
	var cr struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &cr) != nil || cr.Subtype != "can_use_tool" {
		_ = s.writeJSON(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": reqID,
				"response":   map[string]any{"behavior": "allow"},
			},
		})
		return
	}

	var inputMap map[string]any
	_ = json.Unmarshal(cr.Input, &inputMap)
	if inputMap == nil {
		inputMap = map[string]any{}
	}

	tctx := s.gateCtx()

	reply := func(resp map[string]any) {
		_ = s.writeJSON(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": reqID,
				"response":   resp,
			},
		})
	}

	if s.spec.Gate == nil {
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})
		return
	}

	switch cr.ToolName {
	case "ExitPlanMode":
		planMD, _ := inputMap["plan"].(string)
		planID := fmt.Sprintf("plan_%d", s.itemSeq.Add(1))
		s.emit(runtime.Event{Kind: runtime.EventPlanProposed, ItemID: planID, PlanTitle: "Implementation Plan", Text: planMD})
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
			Plan: &sessionv1.PlanReviewPayload{
				PlanId:   planID,
				Title:    "Implementation Plan",
				Markdown: planMD,
			},
		})
		if err != nil || !res.PlanApproved {
			msg := res.Feedback
			if msg == "" {
				msg = "Please revise the plan before exiting Plan mode."
			}
			reply(map[string]any{"behavior": "deny", "message": msg})
			return
		}
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})

	case "AskUserQuestion":
		var qPayload struct {
			Questions []struct {
				Question    string `json:"question"`
				Header      string `json:"header"`
				MultiSelect bool   `json:"multiSelect"`
				Options     []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(cr.Input, &qPayload) != nil || len(qPayload.Questions) == 0 {
			reply(map[string]any{"behavior": "deny", "message": "Malformed AskUserQuestion payload"})
			return
		}
		var qItems []*sessionv1.QuestionItem
		for i, q := range qPayload.Questions {
			qid := fmt.Sprintf("q_%d", i+1)
			var opts []*sessionv1.QuestionOption
			for _, o := range q.Options {
				opts = append(opts, &sessionv1.QuestionOption{Id: o.Label, Label: o.Label, Description: o.Description})
			}
			qItems = append(qItems, &sessionv1.QuestionItem{
				Id:          qid,
				Header:      q.Header,
				Text:        q.Question,
				Options:     opts,
				MultiSelect: q.MultiSelect,
				AllowOther:  true,
				Optional:    false,
			})
		}
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind:      sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
			UserInput: &sessionv1.UserInputPayload{Questions: qItems},
		})
		if err != nil || !res.Allowed {
			msg := res.Feedback
			if msg == "" {
				msg = "User declined question"
			}
			reply(map[string]any{"behavior": "deny", "message": msg})
			return
		}
		answersOut := map[string]string{}
		for i, q := range qPayload.Questions {
			qid := fmt.Sprintf("q_%d", i+1)
			if ans := res.Answers[qid]; ans != nil {
				vals := slices.Clone(ans.SelectedOptions)
				if ans.CustomText != "" {
					vals = append(vals, ans.CustomText)
				}
				answersOut[q.Question] = strings.Join(vals, ", ")
			}
		}
		inputMap["answers"] = answersOut
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})

	default:
		cat, cmd, targetPath := classifyTool(cr.ToolName, cr.Input)
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
			Approval: &sessionv1.ToolApprovalPayload{
				ToolCategory: cat,
				ToolName:     cr.ToolName,
				Command:      cmd,
				TargetPath:   targetPath,
			},
		})
		if err != nil || !res.Allowed {
			msg := res.Feedback
			if msg == "" {
				msg = "Tool execution declined by operator"
			}
			reply(map[string]any{"behavior": "deny", "message": msg})
			if res.CancelTurn {
				_ = s.Interrupt(context.Background())
			}
			return
		}
		if cat == "file_write" && targetPath != "" {
			s.emit(runtime.Event{
				Kind:     runtime.EventFileChanged,
				ItemID:   fmt.Sprintf("file_%d", s.itemSeq.Add(1)),
				FilePath: targetPath,
			})
		}
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})
	}
}

var claudeBgAckIDRE = regexp.MustCompile(`(?i)\bID:\s*([A-Za-z0-9_-]+)`)

func extractClaudeBgIDFromAck(text string) string {
	m := claudeBgAckIDRE.FindStringSubmatch(text)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func detectClaudeAsyncTool(name string, raw json.RawMessage) (claudeAsyncInput, bool) {
	var in struct {
		Command         string `json:"command"`
		Description     string `json:"description"`
		Prompt          string `json:"prompt"`
		DelaySeconds    int64  `json:"delaySeconds"`
		DelaySecSnake   int64  `json:"delay_seconds"`
		RunInBackground bool   `json:"run_in_background"`
	}
	_ = json.Unmarshal(raw, &in)
	switch name {
	case "Monitor":
		title := strings.TrimSpace(in.Description)
		if title == "" {
			title = strings.TrimSpace(in.Command)
		}
		if title == "" {
			title = "Monitor"
		}
		return claudeAsyncInput{
			kind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
			title:   title,
			command: strings.TrimSpace(in.Command),
		}, true
	case "ScheduleWakeup":
		title := strings.TrimSpace(in.Description)
		if title == "" {
			sec := in.DelaySeconds
			if sec == 0 {
				sec = in.DelaySecSnake
			}
			if sec > 0 {
				title = fmt.Sprintf("ScheduleWakeup (%ds)", sec)
			} else {
				title = "ScheduleWakeup"
			}
		}
		return claudeAsyncInput{
			kind:  sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE,
			title: title,
		}, true
	case "Bash":
		if !in.RunInBackground {
			return claudeAsyncInput{}, false
		}
		title := strings.TrimSpace(in.Description)
		if title == "" {
			title = strings.TrimSpace(in.Command)
		}
		return claudeAsyncInput{
			kind:    sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
			title:   title,
			command: strings.TrimSpace(in.Command),
		}, true
	case "Agent", "Task":
		if !in.RunInBackground {
			return claudeAsyncInput{}, false
		}
		title := strings.TrimSpace(in.Description)
		if title == "" {
			title = strings.TrimSpace(in.Prompt)
		}
		if title == "" {
			title = name
		}
		return claudeAsyncInput{
			kind:  sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
			title: title,
		}, true
	default:
		return claudeAsyncInput{}, false
	}
}

func (s *session) settleScheduledWakeups() {
	s.mu.Lock()
	if s.inTurn {
		s.mu.Unlock()
		return
	}
	var settled []*claudeBgTask
	for _, bg := range s.bgTasks {
		if bg.running && bg.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE {
			bg.running = false
			settled = append(settled, bg)
		}
	}
	s.mu.Unlock()
	for _, bg := range settled {
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              bg.taskID,
			ItemID:              bg.itemID,
			ToolName:            bg.title,
			BackgroundKind:      bg.kind,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
			HasExitCode:         true,
			ExitCode:            0,
		})
		if bg.itemID != "" {
			s.emit(runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      bg.itemID,
				ToolName:    bg.title,
				ExitCode:    0,
				HasExitCode: true,
			})
		}
	}
}

func (s *session) handleBackgroundTasksChanged(l wireLine) {
	var newEvents []runtime.Event
	s.mu.Lock()
	for _, t := range l.Tasks {
		tid := strings.TrimSpace(t.TaskID)
		if tid == "" || s.bgTasks[tid] != nil {
			continue
		}
		kind := sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND
		if strings.Contains(t.TaskType, "agent") {
			kind = sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT
		}
		desc := strings.TrimSpace(t.Description)
		bg := &claudeBgTask{
			taskID:  tid,
			kind:    kind,
			title:   desc,
			command: desc,
			running: true,
		}
		s.bgTasks[tid] = bg
		newEvents = append(newEvents, runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              tid,
			ToolName:            desc,
			Command:             desc,
			BackgroundKind:      kind,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		})
	}
	s.mu.Unlock()
	for _, ev := range newEvents {
		s.emit(ev)
	}
}

func (s *session) startClaudeOutputTailer(bg *claudeBgTask) {
	go func() {
		ticker := time.NewTicker(350 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.sessionCtx.Done():
				return
			case <-bg.stopCh:
				return
			case <-ticker.C:
				s.mu.Lock()
				running := bg.running
				path := bg.outputFile
				prev := bg.outputTail
				s.mu.Unlock()
				if !running || path == "" {
					return
				}
				tail := readOutputFileTail(path, 16384)
				if tail == "" || tail == prev {
					continue
				}
				s.mu.Lock()
				if !bg.running {
					s.mu.Unlock()
					return
				}
				bg.outputTail = tail
				ev := runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              tail,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
					BackgroundAwaited:   true,
				}
				s.mu.Unlock()
				s.emit(ev)
			}
		}
	}()
}

func (s *session) handleTaskSystemLine(l wireLine) {
	taskID := strings.TrimSpace(l.TaskID)
	if taskID == "" {
		return
	}
	kind := sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND
	if strings.Contains(l.TaskType, "agent") {
		kind = sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT
	}
	outFile := strings.TrimSpace(l.OutputFile)
	outTail := readOutputFileTail(outFile, 16384)

	s.mu.Lock()
	bg := s.bgTasks[taskID]
	if bg == nil {
		// Remove any synthesized placeholder created from tool_result before task_started arrived.
		if l.ToolUseID != "" {
			delete(s.bgTasks, "claude-bg-"+l.ToolUseID)
		}
		bg = &claudeBgTask{
			taskID:  taskID,
			kind:    kind,
			running: true,
			stopCh:  make(chan struct{}),
		}
		s.bgTasks[taskID] = bg
	}
	if l.ToolUseID != "" {
		bg.itemID = strings.TrimSpace(l.ToolUseID)
	}
	if desc := strings.TrimSpace(l.Description); desc != "" {
		bg.title = desc
		if bg.command == "" {
			bg.command = desc
		}
	}
	if bg.itemID != "" {
		if meta, ok := s.asyncToolInputs[bg.itemID]; ok {
			if meta.command != "" {
				bg.command = meta.command
			}
			if bg.title == "" && meta.title != "" {
				bg.title = meta.title
			}
		}
	}
	if outFile != "" {
		bg.outputFile = outFile
	}
	if outTail != "" {
		bg.outputTail = outTail
	}
	shouldStartTailer := (l.Subtype == "task_started" || l.Subtype == "task_progress") && bg.running && bg.outputFile != "" && !bg.tailing
	if shouldStartTailer {
		if bg.stopCh == nil {
			bg.stopCh = make(chan struct{})
		}
		bg.tailing = true
	}
	itemID := bg.itemID
	title := bg.title
	cmd := bg.command
	curTail := bg.outputTail
	if l.Subtype == "task_notification" {
		bg.running = false
		bg.stopTail()
	}
	s.mu.Unlock()

	if shouldStartTailer {
		s.startClaudeOutputTailer(bg)
	}

	switch l.Subtype {
	case "task_started", "task_progress":
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              taskID,
			ItemID:              itemID,
			ToolName:            title,
			Command:             cmd,
			Output:              curTail,
			Text:                strings.TrimSpace(l.Summary),
			BackgroundKind:      kind,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		})
	case "task_notification":
		bgStatus := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
		exitCode := int32(0)
		switch l.Status {
		case "failed":
			bgStatus = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
			exitCode = 1
		case "stopped", "cancelled":
			bgStatus = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
			exitCode = 130
		}
		if curTail == "" {
			curTail = strings.TrimSpace(l.Summary)
		}
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              taskID,
			ItemID:              itemID,
			ToolName:            title,
			Command:             cmd,
			Output:              curTail,
			Text:                strings.TrimSpace(l.Summary),
			ExitCode:            exitCode,
			HasExitCode:         true,
			BackgroundKind:      kind,
			BackgroundStatus:    bgStatus,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		})
		if itemID != "" {
			s.emit(runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      itemID,
				ToolName:    title,
				Command:     cmd,
				ExitCode:    exitCode,
				HasExitCode: true,
				Output:      curTail,
			})
		}
	}
}

func readOutputFileTail(path string, maxBytes int) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= 0 {
		return ""
	}
	offset := int64(0)
	if st.Size() > int64(maxBytes) {
		offset = st.Size() - int64(maxBytes)
	}
	buf := make([]byte, st.Size()-offset)
	n, _ := f.ReadAt(buf, offset)
	buf = buf[:n]
	start := 0
	if offset > 0 {
		for start < len(buf) && (buf[start]&0xc0) == 0x80 {
			start++
		}
	}
	return string(buf[start:])
}

func classifyTool(name string, raw json.RawMessage) (category, cmd, path string) {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Command      string `json:"command"`
	}
	_ = json.Unmarshal(raw, &in)
	p := in.FilePath
	if p == "" {
		p = in.NotebookPath
	}
	if p == "" {
		p = in.Path
	}
	switch name {
	case "Read", "Glob", "Grep", "LS":
		return "read", "", p
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return "file_write", "", p
	case "Bash", "BashOutput", "KillShell", "Monitor":
		return "command", in.Command, ""
	case "WebFetch", "WebSearch":
		return "network", "", ""
	default:
		if strings.HasPrefix(name, "mcp__") {
			return "mcp", "", ""
		}
		return "mcp", "", p
	}
}
