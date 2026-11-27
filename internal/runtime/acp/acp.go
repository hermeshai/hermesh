// Package acp implements the unified Hermesh runtime.Driver for Agent Client
// Protocol (ACP) JSON-RPC 2.0 stdio servers (Gemini CLI, Antigravity, Cursor,
// OpenCode, Copilot, Kiro, Qwen, Goose, DSH, Grok, and custom user ACP servers).
package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

// Agent describes how to start one ACP agent.
type Agent struct {
	Name        string
	Command     string
	Args        []string
	Paths       []string
	Apps        []App
	DesktopApps []string
	Auth        []string
	fallback    *fallbackAgent
}

type fallbackAgent struct {
	command string
	args    []string
	paths   []string
}

type App struct {
	Bundle, Exec, Script string
}

type Provider struct {
	Agent
	PathEnv       string
	Binary        string
	drv           *Driver
	modelsMu      sync.Mutex
	list          *modelList
	acpMu         sync.Mutex
	acpProbe      map[string]bool
	chosenStamp   string
	chosenLaunch  launch
	startSem      chan struct{}
	usageMu       sync.Mutex
	usageKey      string
	usageAt       time.Time
	usageCooldown time.Duration
	now           func() time.Time
}

type launch struct {
	bin      string
	pre      []string
	args     []string
	env      []string
	mode     antigravityMode
	probeErr error
}

func (l launch) probeArgs() []string {
	return append(slices.Clone(l.pre), "--version")
}

func (l launch) acpArgs() []string {
	return append(slices.Clone(l.pre), l.args...)
}

var systemBinDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

func (p *Provider) Name() string { return p.Agent.Name }

func (p *Provider) effectiveBinary() string {
	if p.Binary != "" {
		return p.Binary
	}
	if p.Name() != "" {
		if b := os.Getenv("HERMESH_" + strings.ToUpper(p.Name()) + "_BIN"); b != "" {
			return b
		}
	}
	return ""
}

func (p *Provider) binary() string {
	if b := p.effectiveBinary(); b != "" {
		return b
	}
	home, _ := os.UserHomeDir()
	var extra []string
	for _, dir := range systemBinDirs {
		extra = append(extra, filepath.Join(dir, p.Command))
	}
	extra = append(extra, filepath.Join(home, ".local/bin", p.Command))
	for _, x := range p.Paths {
		if !filepath.IsAbs(x) {
			x = filepath.Join(home, x)
		}
		extra = append(extra, x)
	}
	return runtime.LookPath(p.Command, runtime.SearchPath(p.PathEnv), extra...)
}

func (p *Provider) lookAll(command string, paths ...string) []string {
	home, _ := os.UserHomeDir()
	var candidates []string
	for _, dir := range filepath.SplitList(runtime.SearchPath(p.PathEnv)) {
		if dir != "" {
			candidates = append(candidates, filepath.Join(dir, command))
		}
	}
	for _, dir := range systemBinDirs {
		candidates = append(candidates, filepath.Join(dir, command))
	}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".local/bin", command))
	}
	for _, x := range paths {
		if !filepath.IsAbs(x) {
			x = filepath.Join(home, x)
		}
		candidates = append(candidates, x)
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		found := runtime.LookPath("", "", c)
		if found == "" {
			continue
		}
		key := filepath.Clean(found)
		if goruntime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, found)
	}
	return out
}

func (p *Provider) resolve(ctx context.Context) launch {
	if p.Name() == "antigravity" {
		return p.resolveAntigravityLogin(ctx)
	}
	bin, pre, env := p.command()
	return launch{bin: bin, pre: pre, args: p.Args, env: env}
}

func (p *Provider) resolveForStart(ctx context.Context, env []string) launch {
	if p.Name() == "antigravity" {
		return p.resolveAntigravityStart(ctx, env)
	}
	bin, pre, extraEnv := p.command()
	return launch{bin: bin, pre: pre, args: p.Args, env: extraEnv}
}

func (p *Provider) notFoundDetail() string {
	if p.Name() == "antigravity" && p.fallback != nil {
		return p.Command + " or " + p.fallback.command + " not found"
	}
	return p.Command + " not found"
}

func (p *Provider) notFoundInfo() runtime.Info {
	info := runtime.Info{Name: p.Name(), Problem: runtime.ProblemNotFound, Detail: p.notFoundDetail()}
	if app := runtime.AppWithoutCLI(p.DesktopApps...); app != "" && p.effectiveBinary() == "" {
		info.Problem, info.Detail = runtime.ProblemAppWithoutCLI, app
	}
	return info
}

func (p *Provider) command() (bin string, pre, env []string) {
	if bin := p.binary(); bin != "" {
		return bin, nil, nil
	}
	home, _ := os.UserHomeDir()
	dirs := []string{filepath.Join(home, "Applications"), "/Applications"}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "Programs"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		dirs = append(dirs, pf)
	}
	for _, a := range p.Apps {
		for _, dir := range dirs {
			app := filepath.Join(dir, a.Bundle)
			exe, script := filepath.Join(app, a.Exec), filepath.Join(app, a.Script)
			if scriptThere(script) && runtime.LookPath("", "", exe) != "" {
				return exe, []string{script}, []string{"ELECTRON_RUN_AS_NODE=1"}
			}
		}
	}
	return "", nil, nil
}

func scriptThere(script string) bool {
	if _, err := os.Stat(script); err == nil {
		return true
	}
	for dir := filepath.Dir(script); ; dir = filepath.Dir(dir) {
		if strings.HasSuffix(filepath.Base(dir), ".asar") {
			st, err := os.Stat(dir)
			return err == nil && !st.IsDir()
		}
		if dir == filepath.Dir(dir) {
			return false
		}
	}
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?`)

func (p *Provider) Discover(ctx context.Context) runtime.Info {
	if p.Name() == "antigravity" {
		return p.discoverAntigravity(ctx)
	}
	return p.discoverLaunch(ctx, p.resolve(ctx), runtime.ProbeEnv(p.PathEnv))
}

func (p *Provider) discoverLaunch(ctx context.Context, cmd launch, baseEnv []string) runtime.Info {
	info := runtime.Info{Name: p.Name()}
	if !runtime.Exists(cmd.bin) {
		return p.notFoundInfo()
	}
	info.Binary = cmd.bin
	if cmd.probeErr != nil {
		info.Problem, info.Detail = runtime.ProblemFailed, cmd.probeErr.Error()
		switch {
		case errors.Is(cmd.probeErr, context.DeadlineExceeded):
			info.Problem = runtime.ProblemTimedOut
		case errors.Is(cmd.probeErr, syscall.EAGAIN):
			info.Problem = runtime.ProblemNoProcesses
		}
		return info
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	env := append(slices.Clip(baseEnv), cmd.env...)
	out, problem, detail := runtime.Probe(ctx, env, cmd.bin, cmd.probeArgs()...)
	if problem != runtime.ProblemNone {
		info.Problem, info.Detail = problem, detail
		return info
	}
	info.Available = true
	info.Version = versionRE.FindString(string(out))
	info.LoggedIn = true
	if cmd.mode == antigravityStandalone {
		info.LoggedIn = antigravityStandaloneConfigured(env)
	}
	if info.LoggedIn {
		p.fillModels(ctx, &info, cmd, env)
	} else {
		info.ModelsState, info.ModelsAt = runtime.ModelsAuthRequired, time.Now()
	}
	return info
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

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

func newProbeSession(proc *runtime.Proc, ctx context.Context) *session {
	s := &session{
		proc:      proc,
		events:    make(chan runtime.Event, 256),
		pending:   map[int64]func(rpcRes){},
		terminals: map[string]*managedTerm{},
		tools:     map[string]*acpToolState{},
		turnCtx:   ctx,
	}
	s.alive.Store(true)
	return s
}

type Driver struct {
	PathEnv string
	// Optional overrides for deterministic tests; zero uses production defaults.
	PostPromptMergeWindow  time.Duration
	OutOfTurnToolWait      time.Duration
	OutOfTurnDebounce      time.Duration
	OutOfTurnQuietLimit    time.Duration
	BusyPromptRetryDelay   time.Duration
	TaskLogPollInterval    time.Duration
	ScheduleExpiryGrace    time.Duration
	SessionStartTimeout    time.Duration
	SessionStartRetryDelay time.Duration
	MaxConcurrentStarts    int

	mu        sync.Mutex
	providers map[string]*Provider
}

func (d *Driver) taskLogPollInterval() time.Duration {
	if d != nil && d.TaskLogPollInterval > 0 {
		return d.TaskLogPollInterval
	}
	return 750 * time.Millisecond
}

func (d *Driver) scheduleExpiryGrace() time.Duration {
	if d != nil && d.ScheduleExpiryGrace > 0 {
		return d.ScheduleExpiryGrace
	}
	return 5 * time.Second
}

func (d *Driver) mergeWindow() time.Duration {
	if d != nil && d.PostPromptMergeWindow > 0 {
		return d.PostPromptMergeWindow
	}
	return 1500 * time.Millisecond
}

func (d *Driver) outOfTurnToolWait() time.Duration {
	if d != nil && d.OutOfTurnToolWait > 0 {
		return d.OutOfTurnToolWait
	}
	return 3 * time.Minute
}

func (d *Driver) outOfTurnDebounce() time.Duration {
	if d != nil && d.OutOfTurnDebounce > 0 {
		return d.OutOfTurnDebounce
	}
	return 1500 * time.Millisecond
}

func (d *Driver) outOfTurnQuietLimit() time.Duration {
	if d != nil && d.OutOfTurnQuietLimit > 0 {
		return d.OutOfTurnQuietLimit
	}
	return 90 * time.Second
}

func (d *Driver) busyPromptRetryDelay() time.Duration {
	if d != nil && d.BusyPromptRetryDelay > 0 {
		return d.BusyPromptRetryDelay
	}
	return 350 * time.Millisecond
}

func (d *Driver) sessionStartTimeout() time.Duration {
	if d != nil && d.SessionStartTimeout > 0 {
		return d.SessionStartTimeout
	}
	return 60 * time.Second
}

func (d *Driver) sessionStartRetryDelay() time.Duration {
	if d != nil && d.SessionStartRetryDelay > 0 {
		return d.SessionStartRetryDelay
	}
	return 500 * time.Millisecond
}

func (d *Driver) maxConcurrentStarts() int {
	if d != nil && d.MaxConcurrentStarts > 0 {
		return d.MaxConcurrentStarts
	}
	return 3
}

func (p *Provider) acquireStartSlot(ctx context.Context) (func(), error) {
	if p == nil {
		return func() {}, nil
	}
	p.acpMu.Lock()
	if p.startSem == nil {
		p.startSem = make(chan struct{}, p.drv.maxConcurrentStarts())
	}
	sem := p.startSem
	p.acpMu.Unlock()

	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func NewDriver(pathEnv string) *Driver {
	return &Driver{PathEnv: pathEnv, providers: map[string]*Provider{}}
}

func (d *Driver) Kind() string { return "acp" }

func (d *Driver) ProviderFor(flavor, binary string) *Provider {
	if flavor == "" {
		flavor = "gemini"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.providers == nil {
		d.providers = map[string]*Provider{}
	}
	key := flavor + "\x00" + binary
	p, ok := d.providers[key]
	if !ok {
		ag, _ := AgentByName(flavor)
		p = &Provider{Agent: ag, PathEnv: d.PathEnv, Binary: binary, drv: d}
		d.providers[key] = p
	}
	return p
}

func (d *Driver) Capabilities(_ string) *sessionv1.ProviderCapabilities {
	return &sessionv1.ProviderCapabilities{
		InSessionModelSwitch:  true,
		InSessionOptionSwitch: true,
		InSessionModeSwitch:   true,
		MidTurnSteer:          false,
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

func (d *Driver) resolveLaunch(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, env []string) (*Provider, launch) {
	flavorName := "gemini"
	var binOverride string
	if cfg != nil {
		if cfg.AcpFlavor != "" {
			flavorName = cfg.AcpFlavor
		} else if cfg.InstanceId != "" {
			flavorName = cfg.InstanceId
		}
		binOverride = cfg.BinaryPath
	}
	p := d.ProviderFor(flavorName, binOverride)
	if cfg != nil && cfg.BinaryPath != "" && len(cfg.LaunchArgs) > 0 {
		return p, launch{bin: cfg.BinaryPath, args: slices.Clone(cfg.LaunchArgs)}
	}
	l := p.resolveForStart(ctx, env)
	if cfg != nil && len(cfg.LaunchArgs) > 0 {
		l.pre = nil
		l.args = slices.Clone(cfg.LaunchArgs)
	}
	return p, l
}

func (d *Driver) Probe(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv)
	_, cmd := d.resolveLaunch(ctx, cfg, env)
	if !runtime.Exists(cmd.bin) {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "ACP CLI not found on PATH",
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	probeEnv := append(slices.Clip(env), cmd.env...)
	out, prob, detail := runtime.Probe(pctx, probeEnv, cmd.bin, cmd.probeArgs()...)
	if prob != runtime.ProblemNone {
		return runtime.ProbeResult{
			Installed:            true,
			Binary:               cmd.bin,
			Compatibility:        sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_BROKEN,
			CompatibilityMessage: detail,
		}
	}
	return runtime.ProbeResult{
		Installed:     true,
		Binary:        cmd.bin,
		Authenticated: true,
		Version:       versionRE.FindString(string(out)),
		Compatibility: sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
}

func formatACPMCPServers(servers []runtime.MCPServer) []any {
	out := make([]any, 0, len(servers))
	for _, m := range servers {
		envList := make([]map[string]string, 0, len(m.Env))
		for k, v := range m.Env {
			envList = append(envList, map[string]string{"name": k, "value": v})
		}
		args := m.Args
		if args == nil {
			args = []string{}
		}
		out = append(out, map[string]any{
			"name":    m.Name,
			"command": m.Command,
			"args":    args,
			"env":     envList,
		})
	}
	return out
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	var instEnv map[string]string
	if spec.Instance != nil {
		instEnv = spec.Instance.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv, spec.ExtraEnv)
	prov, cmd := d.resolveLaunch(ctx, spec.Instance, env)
	return prov.startRuntimeSession(ctx, cmd, env, spec)
}

const (
	maxSessionStartAttempts = 3
	codeInternalError       = -32603
	codeResourceNotFound    = -32002
)

func (p *Provider) startRuntimeSession(ctx context.Context, cmd launch, baseEnv []string, spec runtime.StartSpec) (runtime.Session, error) {
	if cmd.probeErr != nil {
		return nil, cmd.probeErr
	}
	if !runtime.Exists(cmd.bin) {
		return nil, runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_START_FAILED, p.notFoundDetail())
	}
	startCtx := spec.StartCtx
	if startCtx == nil {
		startCtx = ctx
	}
	isResume := spec.ResumeCursor != ""
	baseDelay := p.drv.sessionStartRetryDelay()

	var (
		lastMethod string
		lastStderr string
		lastErr    error
	)
	for attempt := range maxSessionStartAttempts {
		releaseSlot, slotErr := p.acquireStartSlot(startCtx)
		if slotErr != nil {
			return nil, slotErr
		}
		attemptCtx, cancel := context.WithTimeout(startCtx, p.drv.sessionStartTimeout())
		sess, method, stderrTail, err := p.startRuntimeSessionAttempt(ctx, attemptCtx, cmd, baseEnv, spec)
		cancel()
		releaseSlot()
		if err == nil {
			return sess, nil
		}
		if startCtx.Err() != nil {
			return nil, startCtx.Err()
		}
		lastMethod, lastStderr, lastErr = method, stderrTail, err
		if attempt == maxSessionStartAttempts-1 || !isTransientACPStartError(err, isResume) {
			break
		}
		delay := baseDelay * time.Duration(1<<attempt)
		if q := int64(delay / 4); q > 0 {
			delay += time.Duration(time.Now().UnixNano() % q)
		}
		select {
		case <-time.After(delay):
		case <-startCtx.Done():
			return nil, startCtx.Err()
		}
	}
	return nil, classifyACPStartFailure(p, lastMethod, lastErr, lastStderr, isResume)
}

func (p *Provider) startRuntimeSessionAttempt(leaseCtx, attemptCtx context.Context, cmd launch, baseEnv []string, spec runtime.StartSpec) (*session, string, string, error) {
	env := append(slices.Clip(baseEnv), cmd.env...)
	proc, err := runtime.StartProc(attemptCtx, cmd.bin, cmd.acpArgs(), spec.Workdir, env)
	if err != nil {
		return nil, "start", "", err
	}
	sessCtx, sessCancel := context.WithCancel(leaseCtx)
	s := &session{
		drv:           p.drv,
		proc:          proc,
		prov:          p,
		bin:           cmd.bin,
		env:           env,
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

	abort := func(method string, err error) (*session, string, string, error) {
		stderrTail := strings.TrimSpace(proc.StderrTail())
		sessCancel()
		proc.Stop(time.Second)
		if stderrTail == "" {
			stderrTail = strings.TrimSpace(proc.StderrTail())
		}
		return nil, method, stderrTail, err
	}

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
	initRes, err := s.call(attemptCtx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": clientCaps,
		"clientInfo":         map[string]any{"name": "hermesh", "version": "2.0.0"},
	})
	if err != nil {
		return abort("initialize", err)
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

	authID := authMethod(p.Auth, caps.AuthMethods)
	if authID == "" {
		authID = pickAuthMethod(caps.AuthMethods, env)
	}
	if authID != "" {
		if _, authErr := s.call(attemptCtx, "authenticate", map[string]any{"methodId": authID}); authErr != nil {
			return abort("authenticate", runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_AUTH, authErr.Error()))
		}
	}

	canResume := len(caps.AgentCapabilities.SessionCapabilities.Resume) > 0 &&
		string(bytes.TrimSpace(caps.AgentCapabilities.SessionCapabilities.Resume)) != "null"

	params := map[string]any{
		"cwd":        spec.Workdir,
		"mcpServers": formatACPMCPServers(spec.MCPServers),
	}
	var sessRes json.RawMessage
	if spec.ResumeCursor != "" {
		params["sessionId"] = spec.ResumeCursor
		tryResume := func() (json.RawMessage, error) {
			var res json.RawMessage
			var rErr error
			if canResume {
				res, rErr = s.call(attemptCtx, "session/resume", params)
			}
			if (rErr != nil || !canResume) && caps.AgentCapabilities.LoadSession {
				s.replaying.Store(true)
				res, rErr = s.call(attemptCtx, "session/load", params)
				s.replaying.Store(false)
			} else if !canResume && !caps.AgentCapabilities.LoadSession {
				rErr = errors.New("acp agent does not support session resume or load")
			}
			return res, rErr
		}
		var resumeErr error
		sessRes, resumeErr = tryResume()
		if resumeErr != nil && s.Alive() && attemptCtx.Err() == nil && isTransientACPStartError(resumeErr, true) {
			select {
			case <-time.After(p.drv.sessionStartRetryDelay()):
				if s.Alive() && attemptCtx.Err() == nil {
					sessRes, resumeErr = tryResume()
				}
			case <-attemptCtx.Done():
				resumeErr = attemptCtx.Err()
			}
		}
		if resumeErr != nil {
			return abort("session/resume", resumeErr)
		}
		s.sessionID = spec.ResumeCursor
	} else {
		callSessionNew := func() (json.RawMessage, string, error) {
			res, nErr := s.call(attemptCtx, "session/new", params)
			if nErr != nil {
				return nil, "", nErr
			}
			var nr struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(res, &nr)
			if nr.SessionID == "" {
				return nil, "", errors.New("acp session/new returned empty sessionId")
			}
			return res, nr.SessionID, nil
		}
		var sid string
		var newErr error
		sessRes, sid, newErr = callSessionNew()
		if newErr != nil && s.Alive() && attemptCtx.Err() == nil && isTransientACPStartError(newErr, false) {
			select {
			case <-time.After(p.drv.sessionStartRetryDelay()):
				if s.Alive() && attemptCtx.Err() == nil {
					sessRes, sid, newErr = callSessionNew()
				}
			case <-attemptCtx.Done():
				newErr = attemptCtx.Err()
			}
		}
		if newErr != nil {
			return abort("session/new", newErr)
		}
		s.sessionID = sid
	}
	opts := s.parseSessionConfigOptions(sessRes)
	if p != nil && spec.ResumeCursor == "" {
		p.learn(modelContext(cmd.bin, env), opts, true)
	}
	if spec.Selection != nil {
		if err := s.applyInitialSelection(attemptCtx, spec.Selection, opts); err != nil {
			return abort("session/set_config_option", err)
		}
	}
	_, curOwn, _ := s.curOptions.list()
	s.emit(runtime.Event{
		Kind:         runtime.EventNativeSession,
		NativeID:     s.sessionID,
		ResumeCursor: s.sessionID,
		Model:        curOwn.Model,
		Effort:       curOwn.Effort,
	})
	_ = s.SetModes(attemptCtx, spec.EffectiveMode, spec.InteractionMode)
	s.proc.SnapshotBaseline()
	return s, "", "", nil
}

func isPermanentFailureKind(k sessionv1.FailureKind) bool {
	switch k {
	case sessionv1.FailureKind_FAILURE_KIND_AUTH,
		sessionv1.FailureKind_FAILURE_KIND_QUOTA,
		sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE,
		sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST,
		sessionv1.FailureKind_FAILURE_KIND_REFUSED,
		sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW,
		sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED,
		sessionv1.FailureKind_FAILURE_KIND_START_FAILED:
		return true
	}
	return false
}

func isTransientACPStartError(err error, isResume bool) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var fe *runtime.FailureError
	if errors.As(err, &fe) {
		return !isPermanentFailureKind(fe.Kind)
	}
	var rpc *rpcError
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case codeAuthRequired, -32700, -32600, -32601, -32602:
			return false
		case codeResourceNotFound:
			if isResume {
				return false
			}
		}
		if isResume && runtime.IsResumeRejected(err.Error()) {
			return false
		}
		return !isPermanentFailureKind(runtime.Classify(rpc.Message))
	}
	if isResume && (runtime.IsResumeRejected(err.Error()) || strings.Contains(err.Error(), "does not support session resume or load")) {
		return false
	}
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, syscall.EPIPE) ||
		strings.Contains(err.Error(), "acp exited:") ||
		strings.Contains(err.Error(), "returned empty sessionId") {
		return true
	}
	switch runtime.Classify(err.Error()) {
	case sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT,
		sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR,
		sessionv1.FailureKind_FAILURE_KIND_NETWORK:
		return true
	}
	return false
}

func tailStderrSnippet(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < 3; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			kept = append(kept, l)
		}
	}
	slices.Reverse(kept)
	return runtime.Redact(strings.Join(kept, "; "))
}

func classifyACPStartFailure(p *Provider, method string, err error, stderr string, isResume bool) error {
	if err == nil {
		return nil
	}
	var fe *runtime.FailureError
	if errors.As(err, &fe) {
		return err
	}
	var rpc *rpcError
	if errors.As(err, &rpc) && rpc.Code == codeAuthRequired {
		msg := err.Error()
		if p != nil && p.Name() == "antigravity" {
			msg += "; run `hermesh agent login antigravity`"
		}
		return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_AUTH, msg)
	}
	prefix := "acp"
	if method != "" && !strings.HasPrefix(err.Error(), "acp ") {
		prefix = "acp " + method + ": "
	} else {
		prefix = ""
	}
	msg := prefix + err.Error()
	if tail := tailStderrSnippet(stderr); tail != "" && !strings.Contains(msg, tail) {
		msg += " (stderr: " + tail + ")"
	}
	if isResume {
		return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, msg)
	}
	if k := runtime.Classify(err.Error()); k != sessionv1.FailureKind_FAILURE_KIND_UNKNOWN {
		return runtime.Fail(k, msg)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_TIMEOUT, msg)
	}
	if strings.Contains(err.Error(), "acp exited:") {
		return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, msg)
	}
	if (errors.As(err, &rpc) && rpc.Code == codeInternalError) || strings.Contains(err.Error(), "returned empty sessionId") {
		return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR, msg)
	}
	return errors.New(msg)
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
	summary         string
	terminalID      string
	output          string
	diffNewText     string
	rawInput        json.RawMessage
	lastArtifactSig string
	started         bool
	asyncLaunch     bool
	asyncAgentID    string
	asyncLogPath    string
	asyncTaskDesc   string
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
	taskID         string
	itemID         string
	agentID        string
	logPath        string
	kind           sessionv1.BackgroundTaskKind
	title          string
	command        string
	output         string
	awaited        bool
	asyncLaunch    bool
	launchAcked    bool
	launchedTurn   uint64
	maxTriggers    int
	triggersSeen   int
	timerCondition string
	expiresAt      time.Time
	ignoreCondMsgs map[string]bool
	stopScope      sessionv1.BackgroundTaskStopScope
	running        bool
}

type session struct {
	drv             *Driver
	proc            *runtime.Proc
	prov            *Provider
	bin             string
	env             []string
	spec            runtime.StartSpec
	sessionID       string
	supportsImages  bool
	modelOptionID   string
	thoughtOptionID string
	curOptions      sessionOptions
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

	mu             sync.Mutex
	pending        map[int64]func(rpcRes)
	terminals      map[string]*managedTerm
	agentTerms     map[string]*acpAgentTerm
	tools          map[string]*acpToolState
	bgTasks        map[string]*acpBgTask
	agyMessagesDir string
	seenTaskMsgs   map[string]bool
	turnSeq        uint64
	turnCtx        context.Context
	inPrompt       bool
	awaitingMerge  bool
	mergeTimer     *time.Timer
	wakeSteps      runtime.TurnStepTracker
	outOfTurnTimer *time.Timer
	agyInitialMsgs map[string]bool
	openServerReqs int
	msgItemID      string
	msgBuf         strings.Builder
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

func (s *session) hasRunningBgTaskLocked() bool {
	for _, bg := range s.bgTasks {
		if bg != nil && bg.running {
			return true
		}
	}
	return false
}

func (s *session) recordAgyMessagesDirLocked(dir string) {
	if dir == "" || s.agyMessagesDir != "" {
		return
	}
	s.agyMessagesDir = dir
	s.agyInitialMsgs = snapshotExistingMsgs(dir)
}

// hasUnreadAntigravityWakeMsgsLocked checks Antigravity's read.json (when
// present) to detect newly-delivered wakeup messages in .system_generated/messages
// that the agent process has not yet consumed into an ACP wakeup turn.
func (s *session) hasUnreadAntigravityWakeMsgsLocked() bool {
	dir := s.agyMessagesDir
	if dir == "" {
		return false
	}
	readRaw, err := os.ReadFile(filepath.Join(dir, "read.json"))
	if err != nil {
		return false
	}
	readStr := string(readRaw)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	cutoff := time.Now().Add(-2 * time.Minute)
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || name == "read.json" || !strings.HasSuffix(name, ".json") {
			continue
		}
		full := filepath.Join(dir, name)
		if s.agyInitialMsgs[full] {
			continue
		}
		stem := strings.TrimSuffix(name, ".json")
		if strings.Contains(readStr, name) || (stem != "" && strings.Contains(readStr, stem)) {
			continue
		}
		info, iErr := ent.Info()
		if iErr != nil || info.ModTime().Before(cutoff) {
			continue
		}
		msgRaw, rErr := os.ReadFile(full)
		if rErr != nil {
			continue
		}
		var meta struct {
			HideFromUser bool `json:"hideFromUser"`
		}
		if json.Unmarshal(msgRaw, &meta) == nil && meta.HideFromUser {
			continue
		}
		return true
	}
	return false
}

func (s *session) PendingWakeup() bool {
	s.mu.Lock()
	active := s.inPrompt || s.awaitingMerge || s.wakeSteps.Active() || s.openServerReqs > 0 || s.hasOpenWakeToolLocked()
	unreadWake := !active && s.hasUnreadAntigravityWakeMsgsLocked()
	s.mu.Unlock()
	return active || unreadWake
}

func (s *session) beginWakeStepLocked() bool {
	if s.inPrompt {
		return false
	}
	if s.awaitingMerge {
		if s.mergeTimer != nil {
			s.mergeTimer.Stop()
			s.mergeTimer = nil
		}
		s.awaitingMerge = false
	}
	if !s.wakeSteps.Active() {
		s.turnSeq++
		if s.msgItemID == "" {
			s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
		}
	}
	return true
}

func (s *session) noteWakeReplyLocked() {
	if !s.beginWakeStepLocked() {
		return
	}
	s.wakeSteps.NoteReplyChunk()
	s.rearmOutOfTurnTimerLocked()
}

func (s *session) noteWakeToolLocked(completed bool) {
	if !s.beginWakeStepLocked() {
		return
	}
	if completed {
		s.wakeSteps.NoteToolCompleted()
	} else {
		s.wakeSteps.NoteToolActive()
	}
	s.rearmOutOfTurnTimerLocked()
}

func (s *session) rearmOutOfTurnTimerLocked() {
	if s.outOfTurnTimer != nil {
		s.outOfTurnTimer.Stop()
		s.outOfTurnTimer = nil
	}
	if !s.wakeSteps.Active() || s.inPrompt {
		return
	}
	hasOpen := s.openServerReqs > 0 || s.hasOpenWakeToolLocked()
	wait, ok := s.wakeSteps.NextSettleDelay(hasOpen, runtime.TurnStepConfig{
		Debounce:          s.drv.outOfTurnDebounce(),
		StreamingQuiet:    s.drv.outOfTurnQuietLimit(),
		PostToolModelWait: s.drv.outOfTurnToolWait(),
	})
	if !ok {
		return
	}
	s.outOfTurnTimer = time.AfterFunc(wait, s.settleOutOfTurn)
}

func (s *session) settleOutOfTurn() {
	s.mu.Lock()
	if !s.wakeSteps.Active() || s.inPrompt {
		s.mu.Unlock()
		return
	}
	if s.openServerReqs > 0 || s.hasOpenWakeToolLocked() || s.hasUnreadAntigravityWakeMsgsLocked() {
		s.rearmOutOfTurnTimerLocked()
		s.mu.Unlock()
		return
	}
	phase := s.wakeSteps.Phase()
	hasBg := s.hasRunningBgTaskLocked()
	if phase != runtime.StepModelResponded && !hasBg {
		s.mu.Unlock()
		if s.proc.HasActiveDescendants() {
			s.mu.Lock()
			if s.wakeSteps.Active() && !s.inPrompt {
				s.rearmOutOfTurnTimerLocked()
			}
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		if !s.wakeSteps.Active() || s.inPrompt {
			s.mu.Unlock()
			return
		}
	}
	sawReply := s.wakeSteps.SawReply()
	wokeTurn := s.turnSeq
	s.wakeSteps.Reset()
	s.outOfTurnTimer = nil

	autoSettled := s.syncAntigravityCompletionsLocked()
	if sawReply {
		for _, bg := range s.bgTasks {
			trackedByMsgs := bg.messagesDir() != "" || (s.agyMessagesDir != "" && bg.agentID != "")
			recurringCron := bg.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE && bg.maxTriggers == 0
			if bg.running && bg.awaited && bg.asyncLaunch && bg.launchedTurn < wokeTurn && !trackedByMsgs && !recurringCron {
				bg.running = false
				if st := s.tools[bg.itemID]; st != nil {
					st.status = "completed"
				}
				kept, _ := runtime.KeepOutput(bg.output)
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
						BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
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
						Output:      kept,
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

func (s *session) parseSessionConfigOptions(raw json.RawMessage) sessionOptions {
	opts := optionsOf(raw)
	s.curOptions = opts
	if opts.model != nil {
		s.modelOptionID = opts.model.ID
	}
	if opts.thought != nil {
		s.thoughtOptionID = opts.thought.ID
	}
	return opts
}

func (s *session) applyInitialSelection(ctx context.Context, sel *sessionv1.ModelSelection, opts sessionOptions) error {
	if sel == nil {
		return nil
	}
	wantModel := strings.TrimSpace(sel.Model)
	if wantModel == "default" {
		wantModel = ""
	}
	wantEffort := runtime.SelectionEffort(sel)
	explicitEffort := sel.Options["reasoningEffort"].GetStringValue() != "" || sel.Options["effort"].GetStringValue() != ""

	if opts.model != nil {
		if wantModel != "" {
			var avail []string
			found := false
			for _, c := range opts.model.values() {
				avail = append(avail, c.Value)
				if c.Value == wantModel {
					found = true
				}
			}
			if len(avail) > 0 && !found {
				return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE,
					fmt.Sprintf("model %q: not offered by the agent (available: %s)", wantModel, strings.Join(avail, ", ")))
			}
			if wantModel != opts.model.current() {
				res, err := s.call(ctx, "session/set_config_option", map[string]any{
					"sessionId": s.sessionID,
					"configId":  opts.model.ID,
					"value":     wantModel,
				})
				if err != nil {
					return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, err.Error())
				}
				opts = s.parseSessionConfigOptions(res)
				if s.prov != nil {
					s.prov.learn(modelContext(s.bin, s.env), opts, false)
				}
			}
		}
		if wantEffort != "" {
			if opts.thought == nil {
				if explicitEffort {
					return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, "agent takes no reasoning effort")
				}
				return nil
			}
			effs := opts.efforts()
			if len(effs) > 0 && !slices.Contains(effs, wantEffort) {
				return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE,
					fmt.Sprintf("reasoning effort %q: not offered by the agent (available: %s)", wantEffort, strings.Join(effs, ", ")))
			}
			if wantEffort != opts.thought.current() {
				res, err := s.call(ctx, "session/set_config_option", map[string]any{
					"sessionId": s.sessionID,
					"configId":  opts.thought.ID,
					"value":     wantEffort,
				})
				if err != nil {
					return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, err.Error())
				}
				opts = s.parseSessionConfigOptions(res)
			}
		}
		return nil
	}

	if len(opts.legacy) > 0 {
		if wantEffort != "" && explicitEffort {
			return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, "agent takes no reasoning effort")
		}
		if wantModel != "" {
			var avail []string
			found := false
			for _, m := range opts.legacy {
				avail = append(avail, m.ID)
				if m.ID == wantModel {
					found = true
				}
			}
			if !found {
				return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE,
					fmt.Sprintf("model %q: not offered by the agent (available: %s)", wantModel, strings.Join(avail, ", ")))
			}
			if _, err := s.call(ctx, "session/set_model", map[string]any{"sessionId": s.sessionID, "modelId": wantModel}); err != nil {
				return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, err.Error())
			}
			s.curOptions.current = wantModel
		}
		return nil
	}

	return s.SetSelection(ctx, sel)
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
	settledBeforeTurn := s.syncAntigravityCompletionsLocked()
	s.inPrompt = true
	s.turnSeq++
	s.awaitingMerge = false
	s.wakeSteps.Reset()
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
	for _, ev := range settledBeforeTurn {
		s.emit(ev)
	}

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

var (
	agyBgTaskIDRE          = regexp.MustCompile(`(?m)^Tool is running as a background task with task id:\s*(\S+)`)
	agyBgLogURIRE          = regexp.MustCompile(`(?m)^Task logs are available at:\s*(file://[^\r\n]+)`)
	agyTaskDescRE          = regexp.MustCompile(`(?m)^Task Description:\s*([^\r\n]+)`)
	agyTaskCanceledOutRE   = regexp.MustCompile(`(?im)\bTask\s+"?([^\s"]+)"?\s+(?:was\s+)?cancell?ed\b`)
	agyTaskNotRunningOutRE = regexp.MustCompile(`(?im)\btask\s+"([^"]+)"(?::\s*task)?(?:\s+is)?\s+(?:not running|not found)(?:\s*\(status:\s*([A-Z_]+)\))?`)
	claudeBgIDRE           = regexp.MustCompile(`(?i)Command running in background with ID:\s*([A-Za-z0-9_./:-]+)`)
	agyExitCodeRE          = regexp.MustCompile(`(?m)^Command exited with code (-?\d+)\.`)
	subagentConvRE         = regexp.MustCompile(`"conversationId"\s*:\s*"([^"]+)"|(?m)^conversationId:\s*(\S+)`)
)

func parseACPInputFields(raw json.RawMessage) map[string]json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "{}" {
		return nil
	}
	if trimmed[0] == '"' {
		var inner string
		if json.Unmarshal(trimmed, &inner) != nil {
			return nil
		}
		trimmed = bytes.TrimSpace([]byte(inner))
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(trimmed, &m) != nil || len(m) == 0 {
		return nil
	}
	for _, wrapKey := range []string{"args", "input", "arguments"} {
		subRaw, ok := m[wrapKey]
		if !ok || len(m) > 4 {
			continue
		}
		for k, v := range parseACPInputFields(subRaw) {
			if _, exists := m[k]; !exists {
				m[k] = v
			}
		}
	}
	return m
}

func lookupACPInputField(m map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	if len(m) == 0 {
		return nil, false
	}
	for _, want := range keys {
		if v, ok := m[want]; ok && len(bytes.TrimSpace(v)) > 0 && string(bytes.TrimSpace(v)) != "null" {
			return bytes.TrimSpace(v), true
		}
	}
	for _, want := range keys {
		for k, v := range m {
			if strings.EqualFold(k, want) && len(bytes.TrimSpace(v)) > 0 && string(bytes.TrimSpace(v)) != "null" {
				return bytes.TrimSpace(v), true
			}
		}
	}
	return nil, false
}

func acpFieldString(m map[string]json.RawMessage, keys ...string) string {
	v, ok := lookupACPInputField(m, keys...)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			var inner string
			if json.Unmarshal([]byte(s), &inner) == nil {
				return inner
			}
		}
		return s
	}
	return ""
}

func acpFieldInt64(m map[string]json.RawMessage, keys ...string) (int64, bool) {
	v, ok := lookupACPInputField(m, keys...)
	if !ok {
		return 0, false
	}
	var n int64
	if json.Unmarshal(v, &n) == nil {
		return n, true
	}
	var f float64
	if json.Unmarshal(v, &f) == nil {
		return int64(f), true
	}
	if s := strings.TrimSpace(acpFieldString(m, keys...)); s != "" {
		if parsed, err := strconv.ParseInt(s, 10, 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func acpFieldBool(m map[string]json.RawMessage, keys ...string) (bool, bool) {
	v, ok := lookupACPInputField(m, keys...)
	if !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal(v, &b) == nil {
		return b, true
	}
	switch strings.ToLower(strings.TrimSpace(acpFieldString(m, keys...))) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

func acpFieldStrings(m map[string]json.RawMessage, keys ...string) []string {
	v, ok := lookupACPInputField(m, keys...)
	if !ok {
		return nil
	}
	var arr []string
	if json.Unmarshal(v, &arr) == nil {
		return arr
	}
	if s := strings.TrimSpace(acpFieldString(m, keys...)); s != "" {
		if strings.HasPrefix(s, "[") && json.Unmarshal([]byte(s), &arr) == nil {
			return arr
		}
		return []string{s}
	}
	return nil
}

func parseAntigravityTimerDesc(desc string) (durSpec string, dur time.Duration, prompt string, ok bool) {
	trimmed := strings.TrimSpace(desc)
	if len(trimmed) < len("Timer:") || !strings.EqualFold(trimmed[:len("Timer:")], "Timer:") {
		return "", 0, "", false
	}
	rest := strings.TrimSpace(trimmed[len("Timer:"):])
	durSpec = rest
	if idx := strings.Index(rest, ", Prompt:"); idx >= 0 {
		durSpec = strings.TrimSpace(rest[:idx])
		prompt = strings.TrimSpace(rest[idx+len(", Prompt:"):])
	}
	if compact := strings.ReplaceAll(durSpec, " ", ""); compact != "" {
		if parsed, err := time.ParseDuration(compact); err == nil && parsed > 0 {
			dur = parsed
		}
	}
	return durSpec, dur, prompt, true
}

func validAntigravityTaskLogPath(rawURI string) string {
	rawURI = strings.TrimSpace(rawURI)
	if !strings.HasPrefix(rawURI, "file://") {
		return ""
	}
	u, err := url.Parse(rawURI)
	if err != nil || u.Path == "" {
		return ""
	}
	p := u.Path
	if goruntime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	clean := filepath.Clean(p)
	slashed := filepath.ToSlash(clean)
	if !strings.Contains(slashed, "/.system_generated/tasks/") || !strings.HasSuffix(slashed, ".log") {
		return ""
	}
	if st, err := os.Lstat(clean); err == nil && !st.Mode().IsRegular() {
		return ""
	}
	return clean
}

func stripAntigravityAsyncWrapper(out string) string {
	cleaned := out
	if idx := strings.Index(cleaned, "\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS:"); idx >= 0 {
		cleaned = cleaned[:idx]
	} else if strings.HasPrefix(cleaned, "YOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS:") {
		cleaned = ""
	}
	if idx := strings.Index(cleaned, "\nLog output:\n"); idx >= 0 {
		cleaned = cleaned[idx+len("\nLog output:\n"):]
	} else if strings.HasPrefix(cleaned, "Tool is running as a background task with task id:") ||
		strings.Contains(cleaned, "\nTool is running as a background task with task id:") {
		cleaned = ""
	}
	return strings.TrimSpace(cleaned)
}

func detectACPAsyncLaunch(rawInput, meta json.RawMessage, outText string) (isAsync bool, agentID string, logPath string, taskDesc string, cleanedOut string) {
	cleanedOut = outText
	syncCompleted := strings.Contains(outText, "Command exited with code ") || strings.Contains(outText, "\nCompleted At: ")
	if m := parseACPInputFields(rawInput); len(m) > 0 {
		if bg, ok := acpFieldBool(m, "run_in_background", "runInBackground", "background"); ok && bg {
			isAsync = true
		}
		if !syncCompleted {
			isDaemon, _ := acpFieldBool(m, "IsDaemon", "is_daemon", "persistent")
			durSec, _ := acpFieldInt64(m, "DurationSeconds", "duration_seconds")
			cronExpr := strings.TrimSpace(acpFieldString(m, "CronExpression", "cron_expression"))
			typeName := strings.TrimSpace(acpFieldString(m, "TypeName", "type_name"))
			role := strings.TrimSpace(acpFieldString(m, "Role", "role"))
			if isDaemon || cronExpr != "" || durSec > 0 || (typeName != "" && role != "") {
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
	if m := agyBgTaskIDRE.FindStringSubmatch(outText); len(m) == 2 {
		isAsync = true
		if agentID == "" {
			agentID = strings.TrimSpace(m[1])
		}
		if lm := agyBgLogURIRE.FindStringSubmatch(outText); len(lm) == 2 {
			logPath = validAntigravityTaskLogPath(lm[1])
		}
		if dm := agyTaskDescRE.FindStringSubmatch(outText); len(dm) == 2 {
			taskDesc = strings.TrimSpace(dm[1])
		}
		cleanedOut = stripAntigravityAsyncWrapper(outText)
	} else if m := claudeBgIDRE.FindStringSubmatch(outText); len(m) == 2 {
		isAsync = true
		if agentID == "" {
			agentID = strings.TrimSpace(m[1])
		}
	} else if isAsync && agentID == "" {
		if m := subagentConvRE.FindStringSubmatch(outText); len(m) == 3 {
			agentID = strings.TrimSpace(m[1])
			if agentID == "" {
				agentID = strings.TrimSpace(m[2])
			}
		}
	}
	return isAsync, agentID, logPath, taskDesc, cleanedOut
}

func detectACPTaskKill(rawInput json.RawMessage) (killAllSubagents bool, targetIDs []string) {
	m := parseACPInputFields(rawInput)
	if len(m) == 0 {
		return false, nil
	}
	act := strings.ToLower(strings.TrimSpace(acpFieldString(m, "Action", "action")))
	switch act {
	case "kill_all":
		return true, nil
	case "kill", "stop", "cancel":
		for _, id := range append(
			[]string{acpFieldString(m, "TaskId", "task_id", "taskId")},
			acpFieldStrings(m, "ConversationIds", "conversation_ids", "conversationIds")...,
		) {
			if t := strings.TrimSpace(id); t != "" && !slices.Contains(targetIDs, t) {
				targetIDs = append(targetIDs, t)
			}
		}
	}
	return false, targetIDs
}

func readFileTailUTF8(path string, maxBytes int64) (string, int64, time.Time, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, time.Time{}, errors.New("not a regular file")
	}
	size := st.Size()
	if size == 0 {
		return "", 0, st.ModTime(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	defer f.Close()
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", 0, time.Time{}, err
		}
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return "", 0, time.Time{}, err
	}
	start := 0
	if offset > 0 {
		for start < len(buf) && !utf8.RuneStart(buf[start]) {
			start++
		}
	}
	return string(buf[start:]), size, st.ModTime(), nil
}

func (bg *acpBgTask) messagesDir() string {
	if bg == nil || bg.logPath == "" {
		return ""
	}
	tasksDir := filepath.Dir(bg.logPath)
	if filepath.Base(tasksDir) != "tasks" {
		return ""
	}
	return filepath.Join(filepath.Dir(tasksDir), "messages")
}

type agyTaskCompletion struct {
	status   sessionv1.BackgroundTaskStatus
	exitCode int32
	hasExit  bool
	content  string
	summary  string
}

func parseACPScheduleMeta(rawInput json.RawMessage, asyncTaskDesc string) (maxTriggers int, timerCondition string, duration time.Duration) {
	m := parseACPInputFields(rawInput)
	cronExpr := strings.TrimSpace(acpFieldString(m, "CronExpression", "cron_expression"))
	durSec, _ := acpFieldInt64(m, "DurationSeconds", "duration_seconds")
	maxIter, _ := acpFieldInt64(m, "MaxIterations", "max_iterations")
	tc := strings.TrimSpace(acpFieldString(m, "TimerCondition", "timer_condition"))
	if tc != "" && !strings.EqualFold(tc, "never") {
		timerCondition = tc
	}
	if cronExpr == "" && durSec > 0 {
		return 1, timerCondition, time.Duration(durSec) * time.Second
	}
	if cronExpr != "" && maxIter > 0 {
		return int(maxIter), "", 0
	}
	if cronExpr == "" {
		if _, parsedDur, _, ok := parseAntigravityTimerDesc(asyncTaskDesc); ok {
			return 1, timerCondition, parsedDur
		}
	}
	return 0, "", 0
}

func snapshotExistingMsgs(dir string) map[string]bool {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for _, ent := range entries {
		name := ent.Name()
		if !ent.IsDir() && name != "read.json" && strings.HasSuffix(name, ".json") {
			out[filepath.Join(dir, name)] = true
		}
	}
	return out
}

// syncAntigravityCompletionsLocked inspects unread .system_generated/messages/*.json files
// once each and settles any matching running acpBgTask with its real exit code and final log tail.
func (s *session) syncAntigravityCompletionsLocked() []runtime.Event {
	dirs := map[string]bool{}
	if s.agyMessagesDir != "" {
		dirs[s.agyMessagesDir] = true
	}
	byKey := map[string]*acpBgTask{}
	hasExpiry := false
	for _, bg := range s.bgTasks {
		if bg.running {
			if !bg.expiresAt.IsZero() {
				hasExpiry = true
			}
			if d := bg.messagesDir(); d != "" {
				dirs[d] = true
				s.recordAgyMessagesDirLocked(d)
			}
			if bg.agentID != "" {
				byKey[bg.agentID] = bg
			}
			if bg.taskID != "" {
				byKey[bg.taskID] = bg
			}
			if bg.itemID != "" {
				byKey[bg.itemID] = bg
			}
		}
	}
	if len(byKey) == 0 || (len(dirs) == 0 && !hasExpiry) {
		return nil
	}
	if s.seenTaskMsgs == nil {
		s.seenTaskMsgs = map[string]bool{}
	}
	done := map[*acpBgTask]agyTaskCompletion{}
	var progress []runtime.Event
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			name := ent.Name()
			if ent.IsDir() || name == "read.json" || !strings.HasSuffix(name, ".json") {
				continue
			}
			full := filepath.Join(dir, name)
			if s.seenTaskMsgs[full] {
				continue
			}
			raw, rErr := os.ReadFile(full)
			if rErr != nil {
				continue
			}
			var msg struct {
				Sender        string `json:"sender"`
				Content       string `json:"content"`
				HideFromUser  bool   `json:"hideFromUser"`
				RenderDetails struct {
					MessageTitle string `json:"messageTitle"`
				} `json:"renderDetails"`
				SourceMetadata struct {
					Tool struct {
						ToolCall struct {
							ID string `json:"id"`
						} `json:"toolCall"`
					} `json:"tool"`
				} `json:"sourceMetadata"`
			}
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			s.seenTaskMsgs[full] = true

			// A one-shot timer with TimerCondition ('any' or a sender ID) is canceled early by Antigravity
			// as soon as a matching wakeup message arrives.
			if !msg.HideFromUser && msg.Sender != "" {
				for _, cand := range s.bgTasks {
					if !cand.running || cand.timerCondition == "" || msg.Sender == cand.agentID || cand.ignoreCondMsgs[full] {
						continue
					}
					if cand.timerCondition == "any" || cand.timerCondition == msg.Sender {
						if _, exists := done[cand]; !exists {
							done[cand] = agyTaskCompletion{
								status:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
								summary: "canceled by timer condition",
							}
						}
					}
				}
			}

			bg := byKey[msg.Sender]
			if bg == nil && msg.SourceMetadata.Tool.ToolCall.ID != "" {
				bg = byKey[msg.SourceMetadata.Tool.ToolCall.ID]
			}
			if bg == nil {
				continue
			}
			msgTitle := strings.TrimSpace(msg.RenderDetails.MessageTitle)
			switch {
			case strings.Contains(msg.Content, "finished with result:"):
				code := int32(0)
				if m := agyExitCodeRE.FindStringSubmatch(msg.Content); len(m) == 2 {
					if n, pErr := strconv.Atoi(m[1]); pErr == nil {
						code = int32(n)
					}
				}
				st := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				if code != 0 {
					st = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
				}
				done[bg] = agyTaskCompletion{status: st, exitCode: code, hasExit: true, content: msg.Content}

			case strings.Contains(msg.Content, "was canceled with result:") || strings.HasSuffix(msgTitle, "was canceled"):
				done[bg] = agyTaskCompletion{
					status:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
					content: msg.Content,
					summary: "canceled",
				}

			case bg.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT && msg.Sender != "" && msg.Sender == bg.agentID:
				done[bg] = agyTaskCompletion{
					status:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
					hasExit: true,
					content: msg.Content,
				}

			case bg.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE &&
				(strings.HasSuffix(msgTitle, "Timer has expired") || (msg.Sender != "" && msg.Sender == bg.agentID)):
				bg.triggersSeen++
				if strings.HasSuffix(msgTitle, "Timer has expired") || (bg.maxTriggers > 0 && bg.triggersSeen >= bg.maxTriggers) {
					done[bg] = agyTaskCompletion{
						status:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
						hasExit: true,
						content: msg.Content,
					}
				} else if msg.Content != "" {
					bg.output = tailUTF8(msg.Content, 16384)
					if st := s.tools[bg.itemID]; st != nil {
						st.output = bg.output
					}
					progress = append(progress, runtime.Event{
						Kind:                runtime.EventBackgroundTask,
						TaskID:              bg.taskID,
						ItemID:              bg.itemID,
						ToolName:            bg.title,
						Command:             bg.command,
						Output:              bg.output,
						BackgroundKind:      bg.kind,
						BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
						BackgroundStopScope: bg.stopScope,
						BackgroundAwaited:   bg.awaited,
					})
				}
			}
		}
	}
	now := time.Now()
	for _, cand := range s.bgTasks {
		if !cand.running || cand.expiresAt.IsZero() || now.Before(cand.expiresAt) {
			continue
		}
		if _, exists := done[cand]; !exists {
			done[cand] = agyTaskCompletion{
				status:  sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
				summary: "timer finished",
			}
		}
	}
	out := progress
	for bg, comp := range done {
		if bg == nil || !bg.running {
			continue
		}
		bg.running = false
		if bg.logPath != "" {
			if tail, _, _, err := readFileTailUTF8(bg.logPath, 16384); err == nil && tail != "" {
				bg.output = tail
			}
		}
		if bg.output == "" && comp.content != "" {
			bg.output = tailUTF8(comp.content, 16384)
		}
		if st := s.tools[bg.itemID]; st != nil {
			if comp.status == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED {
				st.status = "failed"
			} else {
				st.status = "completed"
			}
			if bg.output != "" {
				st.output = bg.output
			}
		}
		kept, _ := runtime.KeepOutput(bg.output)
		out = append(out,
			runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				Text:                comp.summary,
				ExitCode:            comp.exitCode,
				HasExitCode:         comp.hasExit,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    comp.status,
				BackgroundStopScope: bg.stopScope,
				BackgroundAwaited:   bg.awaited,
			},
			runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      bg.itemID,
				ToolName:    bg.title,
				Command:     bg.command,
				ExitCode:    comp.exitCode,
				HasExitCode: true,
				Output:      kept,
			},
		)
	}
	return out
}

func (s *session) startTaskLogPoller(taskID, logPath string) {
	s.mu.Lock()
	hasMsgsDir := s.agyMessagesDir != ""
	bgInit := s.bgTasks[taskID]
	hasExpiry := bgInit != nil && !bgInit.expiresAt.IsZero()
	s.mu.Unlock()
	if logPath == "" && !hasMsgsDir && !hasExpiry {
		return
	}
	interval := s.drv.taskLogPollInterval()
	go func() {
		var lastSize int64 = -1
		var lastMod time.Time
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.sessionCtx.Done():
				return
			case <-ticker.C:
			}
			if !s.Alive() {
				return
			}
			var tail string
			var size int64
			var mod time.Time
			var err error
			if logPath != "" {
				tail, size, mod, err = readFileTailUTF8(logPath, 16384)
			} else {
				err = errors.New("no log file")
			}
			s.mu.Lock()
			bg := s.bgTasks[taskID]
			if bg == nil || !bg.running {
				s.mu.Unlock()
				return
			}
			var evs []runtime.Event
			if err == nil && (size != lastSize || !mod.Equal(lastMod)) {
				lastSize, lastMod = size, mod
				if tail != "" && tail != bg.output {
					bg.output = tail
					if st := s.tools[bg.itemID]; st != nil {
						st.output = tail
					}
					evs = append(evs, runtime.Event{
						Kind:                runtime.EventBackgroundTask,
						TaskID:              bg.taskID,
						ItemID:              bg.itemID,
						ToolName:            bg.title,
						Command:             bg.command,
						Output:              bg.output,
						BackgroundKind:      bg.kind,
						BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
						BackgroundStopScope: bg.stopScope,
						BackgroundAwaited:   bg.awaited,
					})
				}
			}
			settled := s.syncAntigravityCompletionsLocked()
			stillRunning := bg.running
			s.mu.Unlock()

			if len(settled) > 0 {
				for _, ev := range settled {
					s.emit(ev)
				}
			} else {
				for _, ev := range evs {
					s.emit(ev)
				}
			}
			if !stillRunning {
				return
			}
		}
	}()
}

func normalizeACPBackgroundToolName(t string) string {
	norm := strings.ToLower(strings.TrimSpace(t))
	for _, pfx := range []string{"run ", "call ", "tool: ", "tool "} {
		if strings.HasPrefix(norm, pfx) {
			norm = strings.TrimSpace(norm[len(pfx):])
			break
		}
	}
	if idx := strings.LastIndex(norm, "__"); idx >= 0 {
		norm = norm[idx+2:]
	} else if idx := strings.LastIndex(norm, "/"); idx >= 0 {
		norm = norm[idx+1:]
	} else if idx := strings.LastIndex(norm, "."); idx >= 0 {
		norm = norm[idx+1:]
	}
	return norm
}

func isGenericACPBackgroundTitle(t string) bool {
	switch normalizeACPBackgroundToolName(t) {
	case "", "run_command", "bash", "terminal", "execute", "command", "invoke_subagent", "subagent", "task", "agent", "schedule", "run_workflow", "workflow":
		return true
	default:
		return false
	}
}

func classifyACPBackgroundTool(st *acpToolState) (kind sessionv1.BackgroundTaskKind, title string, cmd string, awaited bool, ok bool) {
	if st == nil || (!st.asyncLaunch && st.status != "in_progress" && st.status != "pending") {
		return 0, "", "", false, false
	}
	m := parseACPInputFields(st.rawInput)
	isDaemon, hasExplicitDaemon := acpFieldBool(m, "IsDaemon", "is_daemon", "persistent")
	durSec, _ := acpFieldInt64(m, "DurationSeconds", "duration_seconds")
	cronExpr := strings.TrimSpace(acpFieldString(m, "CronExpression", "cron_expression"))
	toolSummary := strings.TrimSpace(acpFieldString(m, "toolSummary", "tool_summary"))
	description := strings.TrimSpace(acpFieldString(m, "Description", "description"))
	role := strings.TrimSpace(acpFieldString(m, "Role", "role"))
	typeName := strings.TrimSpace(acpFieldString(m, "TypeName", "type_name"))
	subagentType := strings.TrimSpace(acpFieldString(m, "subagent_type", "subagentType"))
	promptText := strings.TrimSpace(acpFieldString(m, "Prompt", "prompt"))

	timerSpec, _, timerPrompt, isTimerDesc := parseAntigravityTimerDesc(st.asyncTaskDesc)
	isCronDesc := strings.HasPrefix(strings.ToLower(strings.TrimSpace(st.asyncTaskDesc)), "cron:")
	if promptText == "" && timerPrompt != "" {
		promptText = timerPrompt
	}

	rawTitle := strings.TrimSpace(st.title)
	lowerRawTitle := strings.ToLower(rawTitle)
	normRawTool := normalizeACPBackgroundToolName(rawTitle)
	lowerKind := strings.ToLower(st.kind)

	cmd = st.command
	if cmd == "" {
		cmd = strings.TrimSpace(acpFieldString(m, "CommandLine", "command_line", "command"))
	}
	if cmd == "" && !isTimerDesc && !isCronDesc && st.asyncTaskDesc != "" &&
		(lowerKind == "execute" || lowerKind == "command" || lowerKind == "bash" || normRawTool == "run_command") {
		cmd = st.asyncTaskDesc
	}

	if !isGenericACPBackgroundTitle(rawTitle) && rawTitle != cmd {
		title = rawTitle
	}
	for _, cand := range []string{toolSummary, description, role, subagentType, promptText, cmd, st.asyncTaskDesc, rawTitle} {
		if title == "" && strings.TrimSpace(cand) != "" {
			title = strings.TrimSpace(cand)
		}
	}
	lowerTitle := strings.ToLower(title)

	switch {
	case lowerKind == "execute" || lowerKind == "command" || lowerKind == "bash" || normRawTool == "run_command" || cmd != "":
		if isDaemon || (!hasExplicitDaemon && st.asyncLaunch && runtime.IsDaemonCommand(cmd, title)) {
			return sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON, title, cmd, false, true
		}
		return sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND, title, cmd, true, true
	case normRawTool == "schedule" || strings.Contains(lowerTitle, "schedule") || strings.Contains(lowerRawTitle, "schedule") ||
		cronExpr != "" || durSec > 0 || isTimerDesc || isCronDesc:
		schedSpec := cronExpr
		if schedSpec == "" && durSec > 0 {
			schedSpec = fmt.Sprintf("%ds", durSec)
		}
		if schedSpec == "" && timerSpec != "" {
			schedSpec = timerSpec
		}
		if isDaemon {
			return sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON, title, schedSpec, false, true
		}
		return sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE, title, schedSpec, true, true
	case normRawTool == "run_workflow" || normRawTool == "workflow":
		return sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND, title, description, true, true
	case normRawTool == "invoke_subagent" || strings.Contains(lowerTitle, "subagent") || strings.Contains(lowerTitle, "agent") ||
		strings.Contains(lowerRawTitle, "subagent") || strings.Contains(lowerRawTitle, "agent") ||
		lowerKind == "agent" || role != "" || typeName != "" || subagentType != "" || st.asyncLaunch:
		return sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, title, promptText, true, true
	default:
		return 0, "", "", false, false
	}
}

func (s *session) isAntigravityManagedTaskLocked(cand *acpBgTask) bool {
	if cand == nil || cand.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT {
		return false
	}
	if s.terminals[cand.taskID] != nil {
		return false
	}
	return cand.logPath != "" || strings.Contains(cand.agentID, "/task-") || (s.agyMessagesDir != "" && cand.asyncLaunch)
}

func (s *session) settleACPBackgroundTaskLocked(cand *acpBgTask, settledStatus sessionv1.BackgroundTaskStatus, summaryText string, hasExit bool, exitCode int32) []runtime.Event {
	if cand == nil || !cand.running {
		return nil
	}
	cand.running = false
	if tst := s.tools[cand.itemID]; tst != nil {
		if settledStatus == sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED {
			tst.status = "failed"
		} else {
			tst.status = "completed"
		}
	}
	kept, _ := runtime.KeepOutput(cand.output)
	return []runtime.Event{
		{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              cand.taskID,
			ItemID:              cand.itemID,
			ToolName:            cand.title,
			Command:             cand.command,
			Output:              cand.output,
			Text:                summaryText,
			ExitCode:            exitCode,
			HasExitCode:         hasExit,
			BackgroundKind:      cand.kind,
			BackgroundStatus:    settledStatus,
			BackgroundStopScope: cand.stopScope,
			BackgroundAwaited:   cand.awaited,
		},
		{
			Kind:        runtime.EventToolCompleted,
			ItemID:      cand.itemID,
			ToolName:    cand.title,
			Command:     cand.command,
			ExitCode:    exitCode,
			HasExitCode: true,
			Output:      kept,
		},
	}
}

func statusFromACPNotRunningCode(code string) (sessionv1.BackgroundTaskStatus, string, bool, int32) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "DONE", "COMPLETED", "SUCCEEDED":
		return sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED, "", true, 0
	case "FAILED", "ERROR":
		return sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED, "failed", true, 1
	default:
		return sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED, "stopped by agent", false, 0
	}
}

func (s *session) reconcileACPTaskControlLocked(rawInput json.RawMessage, status, outText string) []runtime.Event {
	if status != "completed" && status != "failed" {
		return nil
	}
	var evs []runtime.Event

	// 1. Input-driven kill / stop / cancel (manage_task, manage_subagents, TaskStop, KillShell):
	if status == "completed" || strings.Contains(outText, "task is not running") || strings.Contains(outText, "not found") {
		if killAll, targetIDs := detectACPTaskKill(rawInput); killAll || len(targetIDs) > 0 {
			settledStatus := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
			summaryText := "stopped by agent"
			hasExit := false
			exitCode := int32(0)
			if status == "failed" {
				if m := agyTaskNotRunningOutRE.FindStringSubmatch(outText); len(m) == 3 && m[2] != "" {
					settledStatus, summaryText, hasExit, exitCode = statusFromACPNotRunningCode(m[2])
				}
			}
			for _, cand := range s.bgTasks {
				if !cand.running {
					continue
				}
				matched := (killAll && cand.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT) ||
					slices.Contains(targetIDs, cand.agentID) ||
					slices.Contains(targetIDs, cand.taskID)
				if matched {
					evs = append(evs, s.settleACPBackgroundTaskLocked(cand, settledStatus, summaryText, hasExit, exitCode)...)
				}
			}
		}
	}

	// 2. Output-driven explicit cancel / not-running receipts (covers empty or provider-omitted rawInput):
	for _, m := range agyTaskCanceledOutRE.FindAllStringSubmatch(outText, -1) {
		if len(m) != 2 {
			continue
		}
		target := strings.TrimSpace(m[1])
		for _, cand := range s.bgTasks {
			if cand.running && (cand.agentID == target || cand.taskID == target) {
				evs = append(evs, s.settleACPBackgroundTaskLocked(cand, sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED, "stopped by agent", false, 0)...)
			}
		}
	}
	for _, m := range agyTaskNotRunningOutRE.FindAllStringSubmatch(outText, -1) {
		if len(m) != 3 {
			continue
		}
		target := strings.TrimSpace(m[1])
		stCode, summary, hasExit, exitCode := statusFromACPNotRunningCode(m[2])
		for _, cand := range s.bgTasks {
			if cand.running && (cand.agentID == target || cand.taskID == target) {
				evs = append(evs, s.settleACPBackgroundTaskLocked(cand, stCode, summary, hasExit, exitCode)...)
			}
		}
	}

	if status != "completed" {
		return evs
	}

	// 3. Authoritative manage_task(list) & manage_subagents(list) reconciliation:
	if strings.Contains(outText, "No background tasks are currently running.") {
		evs = append(evs, s.syncAntigravityCompletionsLocked()...)
		for _, cand := range s.bgTasks {
			if cand.running && s.isAntigravityManagedTaskLocked(cand) {
				stCode := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				summary := ""
				hasExit := true
				if cand.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE {
					stCode = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
					summary = "canceled"
					hasExit = false
				}
				evs = append(evs, s.settleACPBackgroundTaskLocked(cand, stCode, summary, hasExit, 0)...)
			}
		}
	} else if idx := strings.Index(outText, "background task(s) currently running:"); idx >= 0 {
		rest := outText[idx+len("background task(s) currently running:"):]
		if arrStart := strings.IndexByte(rest, '['); arrStart >= 0 {
			var liveItems []struct {
				TaskID string `json:"taskId"`
			}
			dec := json.NewDecoder(strings.NewReader(rest[arrStart:]))
			if dec.Decode(&liveItems) == nil {
				evs = append(evs, s.syncAntigravityCompletionsLocked()...)
				live := make(map[string]bool, len(liveItems))
				for _, it := range liveItems {
					if tid := strings.TrimSpace(it.TaskID); tid != "" {
						live[tid] = true
					}
				}
				for _, cand := range s.bgTasks {
					if cand.running && s.isAntigravityManagedTaskLocked(cand) && cand.agentID != "" && !live[cand.agentID] && !live[cand.taskID] {
						stCode := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
						summary := ""
						hasExit := true
						if cand.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE {
							stCode = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
							summary = "canceled"
							hasExit = false
						}
						evs = append(evs, s.settleACPBackgroundTaskLocked(cand, stCode, summary, hasExit, 0)...)
					}
				}
			}
		}
	}

	if strings.Contains(outText, "No active direct subagents") || strings.Contains(outText, "No active subagents") {
		evs = append(evs, s.syncAntigravityCompletionsLocked()...)
		for _, cand := range s.bgTasks {
			if cand.running && cand.kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT {
				evs = append(evs, s.settleACPBackgroundTaskLocked(cand, sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED, "", true, 0)...)
			}
		}
	}

	return evs
}

func (s *session) promoteOpenBackgroundWork() (newEvents []runtime.Event, hasAwaited bool) {
	s.mu.Lock()
	var pollers [][2]string

	for id, st := range s.tools {
		kind, title, cmd, awaited, ok := classifyACPBackgroundTool(st)
		if !ok {
			continue
		}
		bg := s.bgTasks[id]
		if bg == nil {
			maxTrig, timerCond, schedDur := parseACPScheduleMeta(st.rawInput, st.asyncTaskDesc)
			var expiresAt time.Time
			if kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE && schedDur > 0 {
				expiresAt = time.Now().Add(schedDur + s.drv.scheduleExpiryGrace())
			}
			bg = &acpBgTask{
				taskID:         id,
				itemID:         id,
				agentID:        st.asyncAgentID,
				logPath:        st.asyncLogPath,
				kind:           kind,
				title:          title,
				command:        cmd,
				output:         tailUTF8(st.output, 16384),
				awaited:        awaited,
				asyncLaunch:    st.asyncLaunch,
				launchedTurn:   s.turnSeq,
				maxTriggers:    maxTrig,
				timerCondition: timerCond,
				expiresAt:      expiresAt,
				stopScope:      sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
				running:        true,
			}
			if d := bg.messagesDir(); d != "" {
				s.recordAgyMessagesDirLocked(d)
			}
			if bg.timerCondition != "" {
				msgDir := bg.messagesDir()
				if msgDir == "" {
					msgDir = s.agyMessagesDir
				}
				bg.ignoreCondMsgs = snapshotExistingMsgs(msgDir)
			}
			s.bgTasks[id] = bg
			if bg.logPath != "" || s.agyMessagesDir != "" || !bg.expiresAt.IsZero() {
				pollers = append(pollers, [2]string{bg.taskID, bg.logPath})
			}
			newEvents = append(newEvents, runtime.Event{
				Kind:                runtime.EventBackgroundTask,
				TaskID:              bg.taskID,
				ItemID:              bg.itemID,
				ToolName:            bg.title,
				Command:             bg.command,
				Output:              bg.output,
				BackgroundKind:      bg.kind,
				BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
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
				kind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
				title:     title,
				command:   at.command,
				output:    tailUTF8(at.outBuf.String(), 16384),
				awaited:   true,
				stopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
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
				BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
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
				kind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND,
				title:     mt.command,
				command:   mt.command,
				output:    outSnap,
				awaited:   true,
				stopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
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
				BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
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
	s.mu.Unlock()
	for _, p := range pollers {
		s.startTaskLogPoller(p[0], p[1])
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
		var rpc *rpcError
		kind := runtime.Classify(r.err.Error())
		if errors.As(r.err, &rpc) && rpc.Code == codeAuthRequired {
			kind = sessionv1.FailureKind_FAILURE_KIND_AUTH
		}
		if kind == sessionv1.FailureKind_FAILURE_KIND_UNKNOWN {
			kind = sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR
		}
		s.emit(runtime.Event{
			Kind:         runtime.EventTurnFailed,
			ErrorCode:    "acp_error",
			ErrorMessage: r.err.Error(),
			Failure:      &runtime.Failure{Kind: kind, Message: r.err.Error()},
		})
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
			Usage: &sessionv1.ContextWindowUsage{
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
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
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
	case "refusal":
		s.mu.Lock()
		s.inPrompt = false
		s.stopTimersLocked()
		s.mu.Unlock()
		s.emit(runtime.Event{
			Kind:         runtime.EventTurnFailed,
			ErrorCode:    res.StopReason,
			ErrorMessage: "acp turn stopped: " + res.StopReason,
			Failure:      &runtime.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_REFUSED, Message: "acp turn stopped: " + res.StopReason},
		})
	case "max_tokens", "max_turn_requests":
		s.mu.Lock()
		s.inPrompt = false
		s.stopTimersLocked()
		s.mu.Unlock()
		s.emit(runtime.Event{
			Kind:         runtime.EventTurnFailed,
			ErrorCode:    res.StopReason,
			ErrorMessage: "acp turn stopped: " + res.StopReason,
			Failure:      &runtime.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_LIMIT_REACHED, Message: "acp turn stopped: " + res.StopReason},
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

func (s *session) SetSelection(ctx context.Context, sel *sessionv1.ModelSelection) error {
	if sel == nil {
		return nil
	}
	if sel.Model != "" && sel.Model != "default" {
		var err error
		if s.modelOptionID != "" {
			_, err = s.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": s.sessionID,
				"configId":  s.modelOptionID,
				"value":     sel.Model,
			})
		} else {
			_, err = s.call(ctx, "session/set_model", map[string]any{"sessionId": s.sessionID, "modelId": sel.Model})
		}
		if err != nil {
			return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, err.Error())
		}
	}
	for k, v := range sel.Options {
		var rawVal any
		if sv, ok := v.Kind.(*sessionv1.OptionValue_StringValue); ok {
			rawVal = sv.StringValue
		} else if bv, ok := v.Kind.(*sessionv1.OptionValue_BoolValue); ok {
			rawVal = bv.BoolValue
		}
		if rawVal != nil {
			if k == "speed" || k == "fastMode" || k == "fast_mode" || k == "thinking" {
				continue
			}
			cfgID := k
			if k == "reasoningEffort" || k == "effort" || k == "thought_level" {
				if s.thoughtOptionID == "" {
					if k == "thought_level" {
						continue
					}
					return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, "agent takes no reasoning effort")
				}
				cfgID = s.thoughtOptionID
			}
			if _, err := s.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": s.sessionID,
				"configId":  cfgID,
				"value":     rawVal,
			}); err != nil {
				return runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, err.Error())
			}
		}
	}
	return nil
}

func (s *session) SetModes(ctx context.Context, _ identityv1.RuntimeMode, im sessionv1.InteractionMode) error {
	modeID := "default"
	if im == sessionv1.InteractionMode_INTERACTION_MODE_PLAN {
		modeID = "plan"
	}
	_, _ = s.call(ctx, "session/set_mode", map[string]any{"sessionId": s.sessionID, "modeId": modeID})
	return nil
}

func (s *session) Interrupt(context.Context) error {
	s.killTerminals()
	s.mu.Lock()
	inPrompt := s.inPrompt
	wasOutOfTurn := s.wakeSteps.Active()
	s.wakeSteps.Reset()
	s.awaitingMerge = false
	s.stopTimersLocked()
	var stopped []runtime.Event
	if !inPrompt {
		for _, bg := range s.bgTasks {
			if bg.running {
				stopped = append(stopped, s.settleACPBackgroundTaskLocked(
					bg,
					sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
					"cancelled",
					false,
					0,
				)...)
			}
		}
	}
	s.mu.Unlock()
	err := s.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": s.sessionID},
	})
	for _, ev := range stopped {
		s.emit(ev)
	}
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
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
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
				BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
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

func (s *session) Diagnose() runtime.Diagnosis {
	return s.proc.Diagnose()
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
			Error  *rpcError        `json:"error"`
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
					r.err = m.Error
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
			ConfigOptions json.RawMessage `json:"configOptions"`
			Used          uint64          `json:"used"`
			Size          uint64          `json:"size"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "config_option_update":
		if len(u.ConfigOptions) > 0 && s.prov != nil {
			wrap, _ := json.Marshal(map[string]any{"configOptions": u.ConfigOptions})
			opts := s.parseSessionConfigOptions(wrap)
			s.prov.learn(modelContext(s.bin, s.env), opts, false)
		}
	case "agent_message_chunk":
		var cb struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &cb) == nil && cb.Text != "" {
			s.mu.Lock()
			s.noteWakeReplyLocked()
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
			s.noteWakeReplyLocked()
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
				bgStatus := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				code := at.exitCode
				if u.Status == "failed" || (at.hasExit && code != 0) {
					bgStatus = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
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
				kept, _ := runtime.KeepOutput(outSnap)
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
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
			if s.wakeSteps.Active() {
				s.rearmOutOfTurnTimerLocked()
			}
		} else if standalone && isNewTerm && (at.command != "" || at.title != "") {
			name := at.title
			if name == "" {
				name = "Terminal"
			}
			var inText string
			if at.command != "" {
				inText = runtime.MarshalToolInput(map[string]any{"command": at.command})
			}
			toolEv = &runtime.Event{
				Kind:     runtime.EventToolStarted,
				ItemID:   termID,
				ToolName: name,
				Command:  at.command,
				Input:    inText,
			}
		} else if standalone && termDone && (at.command != "" || at.title != "") {
			name := at.title
			if name == "" {
				name = "Terminal"
			}
			var inText string
			if at.command != "" {
				inText = runtime.MarshalToolInput(map[string]any{"command": at.command})
			}
			kept, _ := runtime.KeepOutput(outSnap)
			toolEv = &runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      termID,
				ToolName:    name,
				Command:     at.command,
				Input:       inText,
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
		inCmd, inPath, inSummary := extractACPRawInput(u.RawInput)
		candOut := contentOut
		if candOut == "" {
			candOut = rawOut
		}
		isAsyncIn, asyncAgentID, asyncLogPath, asyncTaskDesc, cleanedCandOut := detectACPAsyncLaunch(u.RawInput, u.Meta, candOut)
		if isAsyncIn && candOut != "" {
			if contentOut != "" {
				contentOut = cleanedCandOut
			} else {
				rawOut = cleanedCandOut
			}
		}

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
		if inSummary != "" {
			st.summary = inSummary
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
		if asyncLogPath != "" {
			st.asyncLogPath = asyncLogPath
		}
		if asyncTaskDesc != "" {
			st.asyncTaskDesc = asyncTaskDesc
		}
		if len(u.Locations) > 0 && u.Locations[0].Path != "" {
			st.filePath = u.Locations[0].Path
		} else if diffPath != "" && st.filePath == "" {
			st.filePath = diffPath
		} else if inPath != "" && st.filePath == "" {
			st.filePath = inPath
		}
		if contentOut != "" || (isAsyncIn && candOut != "") {
			st.output = contentOut
			if st.output == "" && rawOut != "" {
				st.output = rawOut
			}
		} else if rawOut != "" {
			st.output = rawOut
		}
		if diffNewText != "" {
			st.diffNewText = diffNewText
		}
		if trimmedIn := bytes.TrimSpace(u.RawInput); len(trimmedIn) > 0 && string(trimmedIn) != "null" && string(trimmedIn) != "{}" {
			st.rawInput = append(st.rawInput[:0], trimmedIn...)
		}
		// Re-evaluate async launch with accumulated st.rawInput + st.output when rawInput arrived in an earlier chunk.
		if !st.asyncLaunch && len(st.rawInput) > 0 {
			if combAsync, combAgent, combLog, combDesc, combOut := detectACPAsyncLaunch(st.rawInput, u.Meta, candOut); combAsync {
				st.asyncLaunch = true
				if combAgent != "" && st.asyncAgentID == "" {
					st.asyncAgentID = combAgent
				}
				if combLog != "" && st.asyncLogPath == "" {
					st.asyncLogPath = combLog
				}
				if combDesc != "" && st.asyncTaskDesc == "" {
					st.asyncTaskDesc = combDesc
				}
				if candOut != "" {
					st.output = combOut
				}
			}
		}
		toolName, cmd, filePath := mapACPToolFields(st, s.spec.Workdir)
		inText := formatACPToolInput(st, cmd, filePath)
		wasStarted := st.started
		hasToolIdentity := st.asyncLaunch || toolName != "" || cmd != "" || filePath != "" || inText != "" || st.summary != ""
		if hasToolIdentity {
			st.started = true
		}
		status := st.status
		outText := st.output
		stSnap := *st

		bg := s.bgTasks[id]
		var bgEv *runtime.Event
		var killedEvs []runtime.Event
		var startPoller [2]string
		// When an async-launch tool (`run_in_background: true`, `_meta...isAsync: true`,
		// or Antigravity's `Tool is running as a background task with task id: ...`)
		// reports `status: "completed"` right away, that event is only the launch ack:
		// keep its transcript item running. Also register and poll immediately if an
		// `in_progress` / `pending` update already carries async-launch metadata or log URI.
		isLaunchAck := st.asyncLaunch && status == "completed" && (bg == nil || !bg.launchAcked)
		registerAsyncNow := bg == nil && st.asyncLaunch && (isLaunchAck || status == "in_progress" || status == "pending")
		if registerAsyncNow {
			if isLaunchAck {
				st.status = "in_progress"
				status = "in_progress"
			}
			if kind, bgTitle, bgCmd, awaited, ok := classifyACPBackgroundTool(st); ok {
				if st.asyncLogPath != "" {
					if tail, _, _, err := readFileTailUTF8(st.asyncLogPath, 16384); err == nil && tail != "" {
						outText = tail
						st.output = tail
					}
				}
				maxTrig, timerCond, schedDur := parseACPScheduleMeta(st.rawInput, st.asyncTaskDesc)
				var expiresAt time.Time
				if kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SCHEDULE && schedDur > 0 {
					expiresAt = time.Now().Add(schedDur + s.drv.scheduleExpiryGrace())
				}
				bg = &acpBgTask{
					taskID:         id,
					itemID:         id,
					agentID:        st.asyncAgentID,
					logPath:        st.asyncLogPath,
					kind:           kind,
					title:          bgTitle,
					command:        bgCmd,
					output:         tailUTF8(outText, 16384),
					awaited:        awaited,
					asyncLaunch:    true,
					launchAcked:    isLaunchAck,
					launchedTurn:   s.turnSeq,
					maxTriggers:    maxTrig,
					timerCondition: timerCond,
					expiresAt:      expiresAt,
					stopScope:      sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
					running:        true,
				}
				if d := bg.messagesDir(); d != "" {
					s.recordAgyMessagesDirLocked(d)
				}
				if bg.timerCondition != "" {
					msgDir := bg.messagesDir()
					if msgDir == "" {
						msgDir = s.agyMessagesDir
					}
					bg.ignoreCondMsgs = snapshotExistingMsgs(msgDir)
				}
				s.bgTasks[id] = bg
				if bg.logPath != "" || s.agyMessagesDir != "" || !bg.expiresAt.IsZero() {
					startPoller = [2]string{bg.taskID, bg.logPath}
				}
				bgEv = &runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              bg.taskID,
					ItemID:              bg.itemID,
					ToolName:            bg.title,
					Command:             bg.command,
					Output:              bg.output,
					BackgroundKind:      bg.kind,
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
		} else {
			killedEvs = s.reconcileACPTaskControlLocked(st.rawInput, status, outText)
		}
		if !registerAsyncNow && bg != nil && bg.running {
			if isLaunchAck {
				bg.launchAcked = true
				st.status = "in_progress"
				status = "in_progress"
				if st.asyncAgentID != "" && bg.agentID == "" {
					bg.agentID = st.asyncAgentID
				}
				if st.asyncLogPath != "" && bg.logPath == "" {
					bg.logPath = st.asyncLogPath
					if d := bg.messagesDir(); d != "" {
						s.recordAgyMessagesDirLocked(d)
					}
					startPoller = [2]string{bg.taskID, bg.logPath}
				}
			}
			bg.output = tailUTF8(outText, 16384)
			if cmd != "" && bg.command == "" {
				bg.command = cmd
			}
			if !isLaunchAck && (status == "completed" || status == "failed") {
				bg.running = false
				bgStatus := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				exitCode := int32(0)
				if status == "failed" {
					bgStatus = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
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
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: bg.stopScope,
					BackgroundAwaited:   bg.awaited,
				}
			}
			if s.wakeSteps.Active() {
				s.rearmOutOfTurnTimerLocked()
			}
		} else {
			s.noteWakeToolLocked(status == "completed" || status == "failed")
		}
		s.mu.Unlock()

		if startPoller[0] != "" {
			s.startTaskLogPoller(startPoller[0], startPoller[1])
		}
		if metaEv, sig, isMeta := parseACPProgressTool(stSnap.title, stSnap.kind, stSnap.rawInput, status); isMeta {
			if metaEv != nil && sig != stSnap.lastArtifactSig {
				s.mu.Lock()
				if cur := s.tools[id]; cur != nil {
					cur.lastArtifactSig = sig
				}
				s.mu.Unlock()
				s.emit(*metaEv)
			}
			return
		}
		hasEmitIdentity := registerAsyncNow || toolName != "" || cmd != "" || filePath != "" || inText != "" || stSnap.summary != ""
		if status != "completed" && status != "failed" && hasEmitIdentity && (!wasStarted || u.SessionUpdate == "tool_call" || registerAsyncNow || toolName != "" || cmd != "" || filePath != "" || inText != "") {
			s.emit(runtime.Event{
				Kind:     runtime.EventToolStarted,
				ItemID:   id,
				Text:     stSnap.summary,
				ToolName: toolName,
				Command:  cmd,
				Input:    inText,
				FilePath: filePath,
			})
		}
		if bgEv != nil {
			s.emit(*bgEv)
		}
		for _, kev := range killedEvs {
			s.emit(kev)
		}
		if status == "completed" || status == "failed" {
			exit := int32(0)
			if status == "failed" {
				exit = 1
			}
			kept, _ := runtime.KeepOutput(outText)
			s.emit(runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      id,
				Text:        stSnap.summary,
				ToolName:    toolName,
				Command:     cmd,
				Input:       inText,
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
		}
	case "plan":
		s.mu.Lock()
		s.noteWakeReplyLocked()
		s.mu.Unlock()
		var steps []*sessionv1.TodoStep
		for _, e := range u.Entries {
			if txt := strings.TrimSpace(e.Content); txt != "" {
				steps = append(steps, &sessionv1.TodoStep{
					Text:   txt,
					Status: runtime.NormalizeTodoStatus(e.Status),
				})
			}
		}
		s.emit(runtime.Event{Kind: runtime.EventTodoUpdated, ItemID: "todo", TodoSteps: steps})
	case "usage_update":
		if u.Size > 0 || u.Used > 0 {
			s.emit(runtime.Event{
				Kind: runtime.EventUsage,
				Usage: &sessionv1.ContextWindowUsage{
					ContextUsedTokens:  u.Used,
					ContextLimitTokens: u.Size,
				},
			})
		}
		s.mu.Lock()
		isWake := s.wakeSteps.Active() && !s.inPrompt
		s.mu.Unlock()
		if isWake {
			// Flush the completed assistant text segment for this wake step before
			// promoting background work or re-arming settle timers, so chained
			// wakeups never concatenate onto a previous wake's msgBuf.
			s.flushAssistantSegment()
			bgEvents, _ := s.promoteOpenBackgroundWork()
			for _, ev := range bgEvents {
				s.emit(ev)
			}
			s.mu.Lock()
			if s.wakeSteps.Active() {
				s.wakeSteps.NoteUsageOrEnd()
				s.rearmOutOfTurnTimerLocked()
			}
			s.mu.Unlock()
		}
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
				kind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
				title:     title,
				awaited:   true,
				stopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION,
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
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
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
		bgStatus := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
		if failed {
			exitCode = 1
			bgStatus = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
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
			kept, _ := runtime.KeepOutput(bg.output)
			toolEv = &runtime.Event{
				Kind:        runtime.EventToolCompleted,
				ItemID:      bg.itemID,
				ToolName:    bg.title,
				ExitCode:    exitCode,
				HasExitCode: true,
				Output:      kept,
			}
		}
		if s.wakeSteps.Active() {
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
		isWake := s.wakeSteps.Active() && !s.inPrompt
		s.mu.Unlock()
		if isWake {
			s.flushAssistantSegment()
			s.mu.Lock()
			if s.wakeSteps.Active() && !s.inPrompt {
				s.wakeSteps.NoteUsageOrEnd()
				s.rearmOutOfTurnTimerLocked()
			}
			s.mu.Unlock()
		}
	}
}

func (s *session) extractACPToolContent(raw json.RawMessage) (output string, diffPath string, diffNewText string, terminalID string) {
	if len(raw) == 0 {
		return "", "", "", ""
	}
	var items []struct {
		Type       string  `json:"type"`
		Path       string  `json:"path"`
		OldText    *string `json:"oldText"`
		NewText    string  `json:"newText"`
		TerminalID string  `json:"terminalId"`
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
			if diffOut := formatACPDiff(it.Path, s.spec.Workdir, it.OldText, it.NewText); diffOut != "" {
				parts = append(parts, diffOut)
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
		Output         string          `json:"output"`
		Stdout         string          `json:"stdout"`
		CombinedOutput string          `json:"combinedOutput"`
		Text           string          `json:"text"`
		Message        string          `json:"message"`
		Content        json.RawMessage `json:"content"`
		Result         json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, cand := range []string{obj.CombinedOutput, obj.Output, obj.Stdout, obj.Text, obj.Message} {
			if cand != "" {
				return cand
			}
		}
		for _, nested := range []json.RawMessage{obj.Content, obj.Result} {
			if len(nested) == 0 || string(bytes.TrimSpace(nested)) == "null" {
				continue
			}
			var ns string
			if json.Unmarshal(nested, &ns) == nil && ns != "" {
				return ns
			}
			var arr []struct {
				Text    string `json:"text"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(nested, &arr) == nil && len(arr) > 0 {
				var parts []string
				for _, item := range arr {
					if item.Text != "" {
						parts = append(parts, item.Text)
					} else if item.Content.Text != "" {
						parts = append(parts, item.Content.Text)
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, "\n")
				}
			}
		}
	}
	return runtime.FormatToolJSON(raw)
}

func formatACPToolInput(st *acpToolState, cmd, filePath string) string {
	if s := runtime.FormatToolJSON(st.rawInput); s != "" {
		return s
	}
	if cmd != "" {
		return runtime.MarshalToolInput(map[string]any{"command": cmd})
	}
	if filePath != "" && filePath != st.title {
		return runtime.MarshalToolInput(map[string]any{"path": filePath})
	}
	if st.filePath != "" {
		return runtime.MarshalToolInput(map[string]any{"path": st.filePath})
	}
	return ""
}

func formatACPDiff(path, workdir string, oldPtr *string, newText string) string {
	if oldPtr == nil && newText == "" {
		return ""
	}
	oldText := ""
	if oldPtr != nil {
		oldText = *oldPtr
	}
	if oldPtr != nil && oldText == newText {
		return ""
	}
	splitLines := func(t string) []string {
		if t == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(t, "\n"), "\n")
	}
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	pre := 0
	for pre < len(oldLines) && pre < len(newLines) && oldLines[pre] == newLines[pre] {
		pre++
	}
	suf := 0
	for pre+suf < len(oldLines) && pre+suf < len(newLines) && oldLines[len(oldLines)-1-suf] == newLines[len(newLines)-1-suf] {
		suf++
	}
	const ctxLines = 3
	oldStart := max(0, pre-ctxLines)
	oldEnd := min(len(oldLines), len(oldLines)-suf+ctxLines)
	newStart := max(0, pre-ctxLines)
	newEnd := min(len(newLines), len(newLines)-suf+ctxLines)
	var b strings.Builder
	rel := path
	if workdir != "" && filepath.IsAbs(rel) {
		if r, err := filepath.Rel(workdir, rel); err == nil && r != "." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			rel = filepath.ToSlash(r)
		}
	}
	if rel != "" {
		if oldPtr == nil || len(oldLines) == 0 {
			fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", rel)
		} else {
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", rel, rel)
		}
	}
	oldPos := oldStart + 1
	if len(oldLines) == 0 {
		oldPos = 0
	}
	newPos := newStart + 1
	if len(newLines) == 0 {
		newPos = 0
	}
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldPos, oldEnd-oldStart, newPos, newEnd-newStart)
	for i := oldStart; i < pre; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	for i := pre; i < len(oldLines)-suf; i++ {
		fmt.Fprintf(&b, "-%s\n", oldLines[i])
	}
	for i := pre; i < len(newLines)-suf; i++ {
		fmt.Fprintf(&b, "+%s\n", newLines[i])
	}
	for i := len(newLines) - suf; i < newEnd; i++ {
		fmt.Fprintf(&b, " %s\n", newLines[i])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func extractACPRawInput(raw json.RawMessage) (cmd, path, summary string) {
	if len(raw) == 0 {
		return "", "", ""
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
		ToolSummary      string `json:"toolSummary"`
		Description      string `json:"description"`
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
		for _, s := range []string{obj.ToolSummary, obj.Description} {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				summary = trimmed
				break
			}
		}
	}
	return cmd, path, summary
}

func normalizeACPProgressToolName(title, kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "execute", "command", "bash", "read", "edit", "write", "delete", "move", "search", "fetch":
		return ""
	}
	norm := strings.ToLower(strings.TrimSpace(title))
	for _, pfx := range []string{"run ", "call ", "tool: ", "tool "} {
		if strings.HasPrefix(norm, pfx) {
			norm = strings.TrimSpace(norm[len(pfx):])
			break
		}
	}
	if idx := strings.LastIndex(norm, "__"); idx >= 0 {
		norm = norm[idx+2:]
	} else if idx := strings.LastIndex(norm, "/"); idx >= 0 {
		norm = norm[idx+1:]
	} else if idx := strings.LastIndex(norm, "."); idx >= 0 {
		norm = norm[idx+1:]
	}
	for _, sfx := range []string{"ask_question", "askuserquestion", "request_user_input", "update_focus", "update_steps", "todowrite", "todo_write", "update_plan"} {
		if norm == sfx || (strings.HasPrefix(norm, "mcp_") && strings.HasSuffix(norm, "_"+sfx)) {
			return sfx
		}
	}
	return ""
}

func parseACPProgressTool(title, kind string, rawInput json.RawMessage, status string) (ev *runtime.Event, sig string, isMeta bool) {
	norm := normalizeACPProgressToolName(title, kind)
	switch norm {
	case "ask_question", "askuserquestion", "request_user_input":
		return nil, "", status != "failed"

	case "update_focus":
		if len(rawInput) == 0 {
			return nil, "", true
		}
		var in struct {
			Focus string `json:"focus"`
		}
		if json.Unmarshal(rawInput, &in) != nil {
			return nil, "", true
		}
		focus := strings.TrimSpace(in.Focus)
		if focus == "" {
			return nil, "", true
		}
		return &runtime.Event{
			Kind: runtime.EventFocusUpdated,
			Text: focus,
		}, "focus\x00" + focus, true

	case "update_steps", "todowrite", "todo_write", "update_plan":
		if len(rawInput) == 0 {
			return nil, "", true
		}
		type rawStep struct {
			Title      string `json:"title"`
			Content    string `json:"content"`
			Step       string `json:"step"`
			Text       string `json:"text"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		}
		var in struct {
			Focus       string    `json:"focus"`
			Explanation string    `json:"explanation"`
			Steps       []rawStep `json:"steps"`
			Todos       []rawStep `json:"todos"`
			Plan        []rawStep `json:"plan"`
			Items       []rawStep `json:"items"`
		}
		if json.Unmarshal(rawInput, &in) != nil {
			return nil, "", true
		}
		var list []rawStep
		hasFocus := false
		switch {
		case in.Steps != nil:
			list = in.Steps
		case in.Todos != nil:
			list = in.Todos
			hasFocus = true
		case in.Plan != nil:
			list = in.Plan
		case in.Items != nil:
			list = in.Items
		default:
			return nil, "", true
		}
		focus := strings.TrimSpace(in.Focus)
		if focus == "" {
			focus = strings.TrimSpace(in.Explanation)
		}
		if focus != "" {
			hasFocus = true
		}
		var steps []*sessionv1.TodoStep
		var sigBuf strings.Builder
		sigBuf.WriteString("steps")
		for _, item := range list {
			label := ""
			for _, cand := range []string{item.Title, item.Content, item.Step, item.Text} {
				if trimmed := strings.TrimSpace(cand); trimmed != "" {
					label = trimmed
					break
				}
			}
			if label == "" {
				continue
			}
			st := runtime.NormalizeTodoStatus(item.Status)
			steps = append(steps, &sessionv1.TodoStep{Text: label, Status: st})
			if af := strings.TrimSpace(item.ActiveForm); af != "" {
				hasFocus = true
				if st == "in_progress" && focus == "" {
					focus = af
				}
			}
			sigBuf.WriteByte(0)
			sigBuf.WriteString(st)
			sigBuf.WriteByte(':')
			sigBuf.WriteString(label)
		}
		if hasFocus {
			sigBuf.WriteByte(0)
			sigBuf.WriteString(focus)
		}
		return &runtime.Event{
			Kind:      runtime.EventTodoUpdated,
			ItemID:    "todo",
			Text:      focus,
			HasFocus:  hasFocus,
			TodoSteps: steps,
		}, sigBuf.String(), true

	default:
		return nil, "", false
	}
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

func (s *session) extractACPPlanExit(toolCallID, name, title, kind string, rawInput, content json.RawMessage, locPath string) (*sessionv1.PlanReviewPayload, bool) {
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
	return &sessionv1.PlanReviewPayload{
		PlanId:   planID,
		Title:    planTitle,
		Markdown: planMD,
	}, true
}

func mapACPToolFields(st *acpToolState, workdir string) (toolName, cmd, filePath string) {
	filePath = st.filePath
	if workdir != "" && filepath.IsAbs(filePath) {
		if rel, err := filepath.Rel(workdir, filePath); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			filePath = filepath.ToSlash(rel)
		}
	}
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
		if st.kind == "write" || strings.HasPrefix(strings.ToLower(st.title), "write ") {
			toolName = "Write"
		} else {
			toolName = "Edit"
		}
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
		s.noteWakeToolLocked(false)
	}
	s.openServerReqs++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.openServerReqs > 0 {
			s.openServerReqs--
		}
		if s.wakeSteps.Active() {
			if isDetachedWakeReq {
				s.wakeSteps.NoteToolCompleted()
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
					Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
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
				Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &sessionv1.ToolApprovalPayload{
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
				Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &sessionv1.ToolApprovalPayload{
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
				Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &sessionv1.ToolApprovalPayload{
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
				Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
				Approval: &sessionv1.ToolApprovalPayload{
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
		cmd := runtime.Command(context.Background(), p.Command, p.Args...)
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
		termInMap := map[string]any{"command": fullCmd}
		if p.Cwd != "" {
			termInMap["cwd"] = p.Cwd
		}
		termInput := runtime.MarshalToolInput(termInMap)
		s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: termID, ToolName: "Terminal", Command: fullCmd, Input: termInput})
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
			kept, _ := runtime.KeepOutput(outStr)

			s.mu.Lock()
			bg := s.bgTasks[termID]
			var bgEv *runtime.Event
			if bg != nil && bg.running {
				bg.running = false
				bg.output = tailUTF8(outStr, 16384)
				bgStatus := sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
				if mt.exit != 0 {
					bgStatus = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
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
			s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: termID, ToolName: "Terminal", Command: fullCmd, Input: termInput, ExitCode: int32(mt.exit), HasExitCode: true, Output: kept})
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
				BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
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

type elicitChoice struct {
	Const string `json:"const"`
	Title string `json:"title"`
}

type elicitPropSchema struct {
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Enum        []string       `json:"enum"`
	OneOf       []elicitChoice `json:"oneOf"`
	AnyOf       []elicitChoice `json:"anyOf"`
	Items       *struct {
		Type  string         `json:"type"`
		Enum  []string       `json:"enum"`
		OneOf []elicitChoice `json:"oneOf"`
		AnyOf []elicitChoice `json:"anyOf"`
	} `json:"items"`
}

type elicitPropMeta struct {
	key      string
	otherKey string
	typ      string
	choiceID map[string]string
}

var genericElicitMsgRe = regexp.MustCompile(`(?i)^the agent has (?:a|\d+) questions?\.$`)

const elicitFreeFormPrefix = "Optional free-form answer to:"

func compareNaturalKeys(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	for i > 0 && a[i-1] >= '0' && a[i-1] <= '9' {
		i--
	}
	numPrefix := func(s string) (int, int, bool) {
		n, j := 0, 0
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			n = n*10 + int(s[j]-'0')
			j++
		}
		return n, j, j > 0
	}
	if na, la, oka := numPrefix(a[i:]); oka {
		if nb, lb, okb := numPrefix(b[i:]); okb && na != nb {
			if na < nb {
				return -1
			}
			return 1
		} else if okb && la != lb {
			return la - lb
		}
	}
	return strings.Compare(a, b)
}

func extractElicitOptions(prop elicitPropSchema) ([]*sessionv1.QuestionOption, map[string]string, bool) {
	choiceID := map[string]string{}
	addChoices := func(list []elicitChoice, enums []string) []*sessionv1.QuestionOption {
		var opts []*sessionv1.QuestionOption
		for _, o := range list {
			if o.Const == "" && o.Title == "" {
				continue
			}
			lbl := o.Title
			if lbl == "" {
				lbl = o.Const
			}
			id := o.Const
			if id == "" {
				id = lbl
			}
			opts = append(opts, &sessionv1.QuestionOption{Id: id, Label: lbl})
			choiceID[lbl] = id
		}
		if len(opts) == 0 {
			for _, e := range enums {
				opts = append(opts, &sessionv1.QuestionOption{Id: e, Label: e})
				choiceID[e] = e
			}
		}
		return opts
	}

	if prop.Type == "boolean" {
		choiceID["Yes"] = "true"
		choiceID["No"] = "false"
		return []*sessionv1.QuestionOption{
			{Id: "true", Label: "Yes"},
			{Id: "false", Label: "No"},
		}, choiceID, false
	}
	if prop.Type == "array" && prop.Items != nil {
		choices := prop.Items.OneOf
		if len(choices) == 0 {
			choices = prop.Items.AnyOf
		}
		return addChoices(choices, prop.Items.Enum), choiceID, true
	}
	choices := prop.OneOf
	if len(choices) == 0 {
		choices = prop.AnyOf
	}
	return addChoices(choices, prop.Enum), choiceID, false
}

func parseElicitationForm(params json.RawMessage) ([]*sessionv1.QuestionItem, []elicitPropMeta, bool) {
	var p struct {
		Mode            string `json:"mode"`
		Message         string `json:"message"`
		RequestedSchema struct {
			Required   []string                    `json:"required"`
			Properties map[string]elicitPropSchema `json:"properties"`
		} `json:"requestedSchema"`
	}
	if json.Unmarshal(params, &p) != nil || (p.Mode != "" && p.Mode != "form") {
		return nil, nil, false
	}

	var keys []string
	for k := range p.RequestedSchema.Properties {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareNaturalKeys)

	if len(keys) == 0 {
		return []*sessionv1.QuestionItem{{
			Id:         "response",
			Text:       p.Message,
			AllowOther: true,
			Optional:   false,
		}}, []elicitPropMeta{{key: "response", typ: "string"}}, true
	}

	// Pair companion "Optional free-form answer to: <question>" fields (emitted by
	// Antigravity's ask_question) onto their choice question instead of showing
	// duplicate free-form items.
	pairedOther := map[string]string{} // choiceKey -> freeFormKey
	absorbed := map[string]bool{}
	for _, fk := range keys {
		fprop := p.RequestedSchema.Properties[fk]
		if slices.Contains(p.RequestedSchema.Required, fk) {
			continue
		}
		opts, _, _ := extractElicitOptions(fprop)
		if len(opts) > 0 || !strings.HasPrefix(fprop.Description, elicitFreeFormPrefix) {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(fprop.Description, elicitFreeFormPrefix))
		if target == "" {
			continue
		}
		best := ""
		for _, ck := range keys {
			if ck == fk || pairedOther[ck] != "" {
				continue
			}
			cprop := p.RequestedSchema.Properties[ck]
			copts, _, _ := extractElicitOptions(cprop)
			if len(copts) == 0 {
				continue
			}
			if strings.TrimSpace(cprop.Title) == target || strings.TrimSpace(cprop.Description) == target {
				best = ck
				if strings.HasPrefix(fk, ck) {
					break
				}
			}
		}
		if best != "" {
			pairedOther[best] = fk
			absorbed[fk] = true
		}
	}

	genericMsg := genericElicitMsgRe.MatchString(strings.TrimSpace(p.Message))
	var qItems []*sessionv1.QuestionItem
	var metas []elicitPropMeta
	for _, k := range keys {
		if absorbed[k] {
			continue
		}
		prop := p.RequestedSchema.Properties[k]
		opts, choiceID, multiSelect := extractElicitOptions(prop)
		otherKey := pairedOther[k]

		header := prop.Title
		qText := prop.Description
		if qText == "" {
			if (genericMsg || otherKey != "") && prop.Title != "" {
				qText = prop.Title
				header = ""
			} else {
				qText = p.Message
			}
		}
		if qText == "" {
			qText = prop.Title
			header = ""
		}
		if qText == "" {
			qText = k
		}

		optional := !slices.Contains(p.RequestedSchema.Required, k)
		if otherKey != "" && len(p.RequestedSchema.Required) == 0 {
			optional = false
		}

		qItems = append(qItems, &sessionv1.QuestionItem{
			Id:          k,
			Header:      header,
			Text:        qText,
			Options:     opts,
			MultiSelect: multiSelect,
			AllowOther:  len(opts) == 0 || otherKey != "",
			Optional:    optional,
		})
		metas = append(metas, elicitPropMeta{
			key:      k,
			otherKey: otherKey,
			typ:      prop.Type,
			choiceID: choiceID,
		})
	}
	return qItems, metas, true
}

func buildElicitationContent(metas []elicitPropMeta, answers map[string]*sessionv1.QuestionAnswer) map[string]any {
	content := map[string]any{}
	for _, m := range metas {
		ans := answers[m.key]
		if ans == nil {
			continue
		}
		custom := strings.TrimSpace(ans.CustomText)
		if m.otherKey != "" {
			if custom != "" {
				content[m.otherKey] = custom
			}
			if len(ans.SelectedOptions) == 0 {
				continue
			}
		}
		switch m.typ {
		case "array":
			var out []string
			for _, sel := range ans.SelectedOptions {
				if mapped, ok := m.choiceID[sel]; ok {
					out = append(out, mapped)
				} else {
					out = append(out, sel)
				}
			}
			if m.otherKey == "" && custom != "" {
				out = append(out, custom)
			}
			if len(out) > 0 {
				content[m.key] = out
			}
		case "boolean":
			raw := custom
			if raw == "" && len(ans.SelectedOptions) > 0 {
				raw = ans.SelectedOptions[0]
				if mapped, ok := m.choiceID[raw]; ok {
					raw = mapped
				}
			}
			if raw != "" {
				content[m.key] = raw == "true" || strings.EqualFold(raw, "yes")
			}
		default:
			raw := custom
			if m.otherKey != "" {
				raw = ""
			}
			if raw == "" && len(ans.SelectedOptions) > 0 {
				raw = ans.SelectedOptions[0]
				if mapped, ok := m.choiceID[raw]; ok {
					raw = mapped
				}
			}
			if raw != "" {
				content[m.key] = raw
			}
		}
	}
	return content
}

func (s *session) handleElicitation(tctx context.Context, params json.RawMessage, reply func(any)) {
	if s.spec.Gate == nil {
		reply(map[string]any{"action": "decline"})
		return
	}
	qItems, metas, ok := parseElicitationForm(params)
	if !ok {
		reply(map[string]any{"action": "decline"})
		return
	}

	res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
		Kind:      sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
		UserInput: &sessionv1.UserInputPayload{Questions: qItems},
	})
	if err != nil || !res.Allowed {
		if res.CancelTurn {
			reply(map[string]any{"action": "cancel"})
		} else {
			reply(map[string]any{"action": "decline"})
		}
		return
	}
	reply(map[string]any{"action": "accept", "content": buildElicitationContent(metas, res.Answers)})
}

// ProtocolCommand resolves the same executable, arguments and environment as a new ACP session.
func (p *Provider) ProtocolCommand(ctx context.Context, env []string) (string, []string, []string) {
	launch := p.resolveForStart(ctx, env)
	return launch.bin, launch.acpArgs(), launch.env
}
