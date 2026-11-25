// Package scripted implements a deterministic in-process runtime.Driver for
// unit, chaos, and E2E testing of every Hermesh session capability: streaming
// deltas, reasoning, tool approvals, file edits, shell commands, structured
// questions, plan proposals, secret requests, mid-turn steering, hot model/mode
// switching, resume cursors, MCP tool invocation (`message <to>|<text>`),
// crashes, hangs, retries, and context-window reporting.
package scripted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

var lastScripts, failTimes sync.Map

type Driver struct {
	DriverName       string
	DisableHotSwitch bool
	DisableSteer     bool
	DisableResume    bool
	EnableTitleGen   bool
	TitleFunc        func(ctx context.Context, req runtime.TitleGenRequest) (runtime.TitleGenResult, error)

	mu         sync.Mutex
	startSpecs []runtime.StartSpec
	setTitles  []string
	sessionSeq atomic.Uint64
}

func NewDriver() *Driver { return &Driver{DriverName: "scripted"} }

func NewNamed(name string) *Driver { return &Driver{DriverName: name} }

func (d *Driver) Kind() string {
	if d.DriverName != "" {
		return d.DriverName
	}
	return "scripted"
}

func (d *Driver) Name() string { return d.Kind() }

func (d *Driver) Discover(context.Context) runtime.Info {
	return runtime.Info{
		Name:        d.Kind(),
		Available:   true,
		Version:     "2.0.0",
		LoggedIn:    true,
		ModelsState: runtime.ModelsKnown,
		ModelsAt:    time.Now(),
	}
}

func (d *Driver) Capabilities(_ string) *sessionv1.ProviderCapabilities {
	return &sessionv1.ProviderCapabilities{
		InSessionModelSwitch:  !d.DisableHotSwitch,
		InSessionOptionSwitch: !d.DisableHotSwitch,
		InSessionModeSwitch:   !d.DisableHotSwitch,
		MidTurnSteer:          !d.DisableSteer,
		LiveApproval:          true,
		LiveUserInput:         true,
		NativeResume:          !d.DisableResume,
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

func (d *Driver) Probe(_ context.Context, _ *sessionv1.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	return runtime.ProbeResult{
		Installed:     true,
		Authenticated: true,
		AuthLabel:     "scripted@local",
		Version:       "2.0.0",
		Compatibility: sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
}

func (d *Driver) StartCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.startSpecs)
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	d.mu.Lock()
	d.startSpecs = append(d.startSpecs, spec)
	d.mu.Unlock()

	id := d.sessionSeq.Add(1)
	nativeID := spec.ResumeCursor
	if nativeID == "" {
		nativeID = fmt.Sprintf("scr_%s_%d", spec.SessionID, id)
	}
	s := &session{
		drv:             d,
		spec:            spec,
		nativeID:        nativeID,
		selection:       proto.Clone(spec.Selection).(*sessionv1.ModelSelection),
		effectiveMode:   spec.EffectiveMode,
		interactionMode: spec.InteractionMode,
		events:          make(chan runtime.Event, 128),
		turns:           make(chan runtime.TurnInput, 8),
		interruptCh:     make(chan struct{}, 1),
		forceCh:         make(chan struct{}),
		closedCh:        make(chan struct{}),
		bgCancels:       map[string]chan struct{}{},
	}
	s.alive.Store(true)
	s.events <- runtime.Event{
		Kind:         runtime.EventNativeSession,
		NativeID:     nativeID,
		ResumeCursor: nativeID,
	}
	go s.loop(ctx)
	return s, nil
}

type session struct {
	drv         *Driver
	spec        runtime.StartSpec
	nativeID    string
	events      chan runtime.Event
	turns       chan runtime.TurnInput
	interruptCh chan struct{}
	forceCh     chan struct{}
	closedCh    chan struct{}
	closeOnce   sync.Once
	forceOnce   sync.Once
	alive       atomic.Bool
	itemSeq     atomic.Uint64

	mu              sync.Mutex
	inTurn          bool
	selection       *sessionv1.ModelSelection
	effectiveMode   identityv1.RuntimeMode
	interactionMode sessionv1.InteractionMode
	steered         []string
	attachments     []runtime.TurnAttachment
	historyCount    int
	bgCancels       map[string]chan struct{}
}

func (s *session) Events() <-chan runtime.Event { return s.events }
func (s *session) Alive() bool                  { return s.alive.Load() }

func (s *session) SetTitle(_ context.Context, title string) error {
	s.drv.mu.Lock()
	s.drv.setTitles = append(s.drv.setTitles, title)
	s.drv.mu.Unlock()
	return nil
}

func (s *session) SetSelection(_ context.Context, sel *sessionv1.ModelSelection) error {
	if s.drv.DisableHotSwitch {
		return runtime.ErrHotSwitchUnsupported
	}
	s.mu.Lock()
	s.selection = proto.Clone(sel).(*sessionv1.ModelSelection)
	s.mu.Unlock()
	return nil
}

func (s *session) SetModes(_ context.Context, rm identityv1.RuntimeMode, im sessionv1.InteractionMode) error {
	if s.drv.DisableHotSwitch {
		return runtime.ErrHotSwitchUnsupported
	}
	s.mu.Lock()
	s.effectiveMode = rm
	s.interactionMode = im
	s.mu.Unlock()
	return nil
}

func (s *session) StartTurn(_ context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	select {
	case s.turns <- in:
		return nil
	case <-s.closedCh:
		return runtime.ErrSessionClosed
	}
}

func (s *session) Steer(_ context.Context, text string, atts []runtime.TurnAttachment) error {
	if s.drv.DisableSteer {
		return runtime.ErrSteerUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inTurn {
		return errors.New("no active turn to steer")
	}
	s.steered = append(s.steered, text)
	s.attachments = append(s.attachments, atts...)
	return nil
}

func (s *session) StopBackgroundTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	var targets []chan struct{}
	for id, ch := range s.bgCancels {
		if taskID == "" || id == taskID {
			targets = append(targets, ch)
			delete(s.bgCancels, id)
		}
	}
	s.mu.Unlock()
	for _, ch := range targets {
		close(ch)
	}
	return nil
}

func (s *session) Interrupt(context.Context) error {
	select {
	case s.interruptCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *session) ForceStop() error {
	s.alive.Store(false)
	s.forceOnce.Do(func() { close(s.forceCh) })
	return s.Close()
}

func (s *session) Close() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() { close(s.closedCh) })
	return nil
}

func (s *session) emit(ev runtime.Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.closedCh:
		return false
	case <-s.forceCh:
		return false
	}
}

func (s *session) loop(ctx context.Context) {
	defer close(s.events)
	for {
		select {
		case <-ctx.Done():
			s.alive.Store(false)
			return
		case <-s.closedCh:
			return
		case <-s.forceCh:
			return
		case in := <-s.turns:
			s.executeTurn(ctx, in)
		}
	}
}

func (s *session) executeTurn(ctx context.Context, in runtime.TurnInput) {
	select {
	case <-s.interruptCh:
	default:
	}

	s.mu.Lock()
	s.inTurn = true
	s.steered = nil
	s.attachments = slices.Clone(in.Attachments)
	if in.Selection != nil {
		s.selection = proto.Clone(in.Selection).(*sessionv1.ModelSelection)
	}
	if in.EffectiveMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		s.effectiveMode = in.EffectiveMode
	}
	if in.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		s.interactionMode = in.InteractionMode
	}
	s.historyCount++
	turnNum := s.historyCount
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inTurn = false
		s.mu.Unlock()
	}()

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	prompt := in.Prompt
	if b, err := os.ReadFile(os.Getenv("HERMESH_FAKE_SCRIPTS")); err == nil {
		var scripts map[string]string
		_ = json.Unmarshal(b, &scripts)
		for p, script := range scripts {
			if strings.HasSuffix(strings.TrimSpace(prompt), p) {
				prompt = script
			}
		}
	}
	if strings.Contains(prompt, "automatic retry") {
		if last, ok := lastScripts.Load(s.nativeID); ok {
			prompt = last.(string) + "\n" + prompt
		}
	} else {
		lastScripts.Store(s.nativeID, prompt)
	}

	usage := &sessionv1.ContextWindowUsage{
		InputTokens:        120,
		OutputTokens:       45,
		ContextUsedTokens:  165,
		ContextLimitTokens: 200000,
	}

	lines := strings.Split(strings.TrimSpace(prompt), "\n")
	if len(lines) == 1 && !isDirective(lines[0]) {
		if strings.Contains(strings.ToLower(lines[0]), "single word: ready") {
			lines = []string{"say ready"}
		} else {
			lines = []string{"say Acknowledged: " + lines[0]}
		}
	}

	var replyParts []string
	msgItemID := fmt.Sprintf("msg_%d", s.itemSeq.Add(1))

	runLines := func(scriptLines []string) (done bool) {
		for _, rawLine := range scriptLines {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			select {
			case <-s.interruptCh:
				s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
				return true
			case <-s.forceCh:
				s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
				return true
			case <-s.closedCh:
				return true
			default:
			}

			verb, rest, _ := strings.Cut(line, " ")
			rest = strings.TrimSpace(rest)

			switch verb {
			case "Reply":
				replyParts = append(replyParts, "ready")
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: "ready"})

			case "say":
				text := strings.ReplaceAll(rest, `\n`, "\n")
				replyParts = append(replyParts, text)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: text})

			case "stream", "draft":
				chunks := strings.Split(rest, "|")
				for _, ch := range chunks {
					part := strings.ReplaceAll(ch, `\n`, "\n")
					if verb == "stream" {
						replyParts = append(replyParts, part)
					}
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: part})
					select {
					case <-time.After(20 * time.Millisecond):
					case <-turnCtx.Done():
						return true
					case <-s.closedCh:
						return true
					case <-s.interruptCh:
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					case <-s.forceCh:
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
				}

			case "warn":
				s.emit(runtime.Event{Kind: runtime.EventNotice, Text: rest})

			case "limits": // limits <m>:<u>,... [plan=<p>]: the account's rate limits (StartSpec.Limits), a window of m minutes, u% used, resetting in m minutes
				var l runtime.Limits
				for _, w := range strings.Split(rest, ",") {
					if plan, ok := strings.CutPrefix(w, "plan="); ok {
						l.Plan = plan
						continue
					}
					m, u, _ := strings.Cut(w, ":")
					mins, _ := strconv.Atoi(m)
					used, _ := strconv.Atoi(u)
					l.Windows = append(l.Windows, runtime.LimitWindow{Minutes: uint32(mins), UsedPercent: uint32(used), ResetsAt: time.Now().Add(time.Duration(mins) * time.Minute).Truncate(time.Second)})
				}
				if s.spec.Limits != nil {
					s.spec.Limits(l)
				}

			case "think":
				s.emit(runtime.Event{
					Kind:   runtime.EventReasoningDelta,
					ItemID: fmt.Sprintf("rsn_%d", s.itemSeq.Add(1)),
					Text:   strings.ReplaceAll(rest, `\n`, "\n"),
				})

			case "sleep", "chatter":
				dur, err := time.ParseDuration(rest)
				if err != nil {
					dur = 200 * time.Millisecond
				}
				select {
				case <-time.After(dur):
				case <-turnCtx.Done():
					return true
				case <-s.closedCh:
					return true
				case <-s.interruptCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				case <-s.forceCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				}
			case "tool-sleep":
				dur, _ := time.ParseDuration(rest)
				callID := fmt.Sprintf("cmd_%d", s.itemSeq.Add(1))
				s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: callID, ToolName: "Bash", Command: "sleep " + rest})
				select {
				case <-time.After(dur):
				case <-turnCtx.Done():
					return true
				case <-s.closedCh:
					return true
				case <-s.interruptCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				case <-s.forceCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				}
				s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: callID, ToolName: "Bash", Command: "sleep " + rest, Output: "slept"})

			case "output":
				n, _ := strconv.Atoi(rest)
				var b strings.Builder
				for i := 1; b.Len() < n-4; i++ {
					fmt.Fprintf(&b, "line %07d of the output\n", i)
				}
				out := b.String()[:max(n-4, 0)] + "END\n"
				callID := fmt.Sprintf("cmd_%d", s.itemSeq.Add(1))
				s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: callID, ToolName: "Bash", Command: "print " + rest})
				kept, _ := runtime.KeepOutput(out)
				s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: callID, ToolName: "Bash", Command: "print " + rest, Output: kept})

			case "hang":
				select {
				case <-turnCtx.Done():
					return true
				case <-s.closedCh:
					return true
				case <-s.interruptCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				case <-s.forceCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				}

			case "crash":
				s.alive.Store(false)
				msg := "scripted agent process exited unexpectedly"
				s.emit(runtime.Event{
					Kind:         runtime.EventTurnFailed,
					ErrorCode:    "process_crashed",
					ErrorMessage: msg,
					Failure:      &runtime.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, Message: msg},
				})
				_ = s.Close()
				return true

			case "overflow":
				msg := "Prompt is too long"
				s.emit(runtime.Event{
					Kind:         runtime.EventTurnFailed,
					ErrorCode:    "context_overflow",
					ErrorMessage: msg,
					Failure:      &runtime.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW, Message: msg},
				})
				return true

			case "refuse-resume", "resume-start-reject":
				if s.spec.ResumeCursor != "" && turnNum == 1 {
					msg := "No conversation found with session ID: " + s.spec.ResumeCursor
					s.alive.Store(false)
					s.emit(runtime.Event{
						Kind:         runtime.EventTurnFailed,
						ErrorCode:    "resume_rejected",
						ErrorMessage: msg,
						Failure:      &runtime.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, Message: msg},
					})
					_ = s.Close()
					return true
				}

			case "retry-forever":
				for {
					s.emit(runtime.Event{
						Kind:    runtime.EventNotice,
						Text:    rest,
						Failure: &runtime.Failure{Kind: runtime.Classify(rest), Message: rest},
					})
					select {
					case <-time.After(50 * time.Millisecond):
					case <-s.interruptCh:
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					case <-s.forceCh:
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					case <-s.closedCh:
						return true
					}
				}

			case "fail-times":
				nStr, text, _ := strings.Cut(rest, " ")
				times, _ := strconv.Atoi(nStr)
				key := s.nativeID + "\x00" + line
				v, _ := failTimes.LoadOrStore(key, 0)
				if v.(int) < times {
					failTimes.Store(key, v.(int)+1)
					f := runtime.FailureOf(errors.New(text), sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR)
					s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "provider_error", ErrorMessage: text, Failure: f})
					return true
				}

			case "fail":
				code, msg, ok := strings.Cut(rest, "|")
				if !ok {
					code, msg = "provider_error", rest
				}
				msg = strings.TrimSpace(msg)
				f := runtime.FailureOf(errors.New(msg), sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR)
				s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: strings.TrimSpace(code), ErrorMessage: msg, Failure: f})
				return true

			case "message":
				to, text, _ := strings.Cut(rest, "|")
				callID := fmt.Sprintf("mcp_%d", s.itemSeq.Add(1))
				s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: callID, ToolName: "message_send", FilePath: to})
				res, isErr := s.callTool(turnCtx, "message_send", map[string]any{"to": to, "message": text})
				exit := int32(0)
				if isErr {
					exit = 1
				}
				replyParts = append(replyParts, res)
				s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: callID, ToolName: "message_send", FilePath: to, ExitCode: exit, Output: res})

			case "write":
				var relPath, content string
				if strings.Contains(rest, "|") {
					relPath, content, _ = strings.Cut(rest, "|")
				} else {
					relPath, content, _ = strings.Cut(rest, " ")
					content += "\n"
				}
				relPath = strings.TrimSpace(relPath)
				content = strings.ReplaceAll(content, `\n`, "\n")
				target := relPath
				if !filepath.IsAbs(target) {
					target = filepath.Join(s.spec.Workdir, target)
				}
				callID := fmt.Sprintf("edit_%d", s.itemSeq.Add(1))
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
						Approval: &sessionv1.ToolApprovalPayload{
							ToolCategory: "file_write",
							ToolName:     "WriteFile",
							TargetPath:   target,
							DiffPreview:  "+" + content,
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					if !res.Allowed {
						note := "write declined"
						if res.Feedback != "" {
							note += ": " + res.Feedback
						}
						replyParts = append(replyParts, "["+note+"]")
						s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: "[" + note + "]"})
						continue
					}
				}
				_ = os.MkdirAll(filepath.Dir(target), 0o755)
				if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
					s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "io_error", ErrorMessage: err.Error()})
					return true
				}
				s.emit(runtime.Event{
					Kind:      runtime.EventFileChanged,
					ItemID:    callID,
					FilePath:  relPath,
					Diff:      "@@ -0,0 +1 @@\n+" + content,
					Additions: uint32(max(1, strings.Count(content, "\n"))),
				})

			case "shell", "run":
				cmdStr, outStr, hasPipe := strings.Cut(rest, "|")
				cmdStr = strings.TrimSpace(cmdStr)
				outStr = strings.ReplaceAll(outStr, `\n`, "\n")
				if !hasPipe && verb == "shell" {
					outStr = "simulated"
				}
				callID := fmt.Sprintf("cmd_%d", s.itemSeq.Add(1))
				if verb == "shell" && s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
						Approval: &sessionv1.ToolApprovalPayload{
							ToolCategory: "command",
							ToolName:     "Bash",
							Command:      cmdStr,
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					if !res.Allowed {
						note := "command declined"
						if res.Feedback != "" {
							note += ": " + res.Feedback
						}
						replyParts = append(replyParts, "["+note+"]")
						s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: "[" + note + "]"})
						continue
					}
				}
				s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: callID, ToolName: "Bash", Command: cmdStr})
				s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: callID, ToolName: "Bash", Command: cmdStr, ExitCode: 0, Output: outStr})

			case "bg-shell", "bg-daemon":
				// bg-shell <dur>|<cmd>|<output>|<wake_reply>
				// bg-daemon <cmd>|<output>
				isDaemon := verb == "bg-daemon"
				parts := strings.SplitN(rest, "|", 4)
				var dur time.Duration
				var cmdStr, outStr, wakeReply string
				if isDaemon {
					p2 := strings.SplitN(rest, "|", 2)
					cmdStr = strings.TrimSpace(p2[0])
					if len(p2) > 1 {
						outStr = strings.ReplaceAll(p2[1], `\n`, "\n")
					}
					dur = 10 * time.Minute
				} else {
					if len(parts) > 0 {
						d, err := time.ParseDuration(strings.TrimSpace(parts[0]))
						if err != nil {
							d = 500 * time.Millisecond
						}
						dur = d
					}
					if len(parts) > 1 {
						cmdStr = strings.TrimSpace(parts[1])
					}
					if len(parts) > 2 {
						outStr = strings.ReplaceAll(parts[2], `\n`, "\n")
					}
					if len(parts) > 3 {
						wakeReply = strings.ReplaceAll(parts[3], `\n`, "\n")
					}
				}
				callID := fmt.Sprintf("bg_%d", s.itemSeq.Add(1))
				bgKind := sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_COMMAND
				if isDaemon {
					bgKind = sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON
				}
				stopCh := make(chan struct{})
				s.mu.Lock()
				s.bgCancels[callID] = stopCh
				s.mu.Unlock()

				initOut := outStr
				if !isDaemon {
					initOut = "[running] " + cmdStr
				}
				s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: callID, ToolName: "Bash", Command: cmdStr})
				s.emit(runtime.Event{
					Kind:                runtime.EventBackgroundTask,
					TaskID:              callID,
					ItemID:              callID,
					ToolName:            cmdStr,
					Command:             cmdStr,
					Output:              initOut,
					BackgroundKind:      bgKind,
					BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
					BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
					BackgroundAwaited:   !isDaemon,
				})
				go func(id, cmd, finalOut, reply string, wait time.Duration, daemon bool, cancelCh chan struct{}) {
					defer func() {
						s.mu.Lock()
						delete(s.bgCancels, id)
						s.mu.Unlock()
					}()
					select {
					case <-cancelCh:
						s.emit(runtime.Event{
							Kind:                runtime.EventBackgroundTask,
							TaskID:              id,
							ItemID:              id,
							ToolName:            cmd,
							Command:             cmd,
							Output:              finalOut,
							Text:                "stopped by user",
							ExitCode:            130,
							HasExitCode:         true,
							BackgroundKind:      bgKind,
							BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
							BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
							BackgroundAwaited:   !daemon,
						})
						s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: id, ToolName: "Bash", Command: cmd, ExitCode: 130, HasExitCode: true, Output: "stopped"})
						return
					case <-s.closedCh:
						return
					case <-s.forceCh:
						return
					case <-time.After(wait):
					}
					s.emit(runtime.Event{
						Kind:                runtime.EventBackgroundTask,
						TaskID:              id,
						ItemID:              id,
						ToolName:            cmd,
						Command:             cmd,
						Output:              finalOut,
						ExitCode:            0,
						HasExitCode:         true,
						BackgroundKind:      bgKind,
						BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
						BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
						BackgroundAwaited:   !daemon,
					})
					s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: id, ToolName: "Bash", Command: cmd, ExitCode: 0, HasExitCode: true, Output: finalOut})
					if !daemon && strings.TrimSpace(reply) != "" {
						wakeMsgID := fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
						s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: wakeMsgID, Text: reply})
						s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: wakeMsgID, Text: reply})
						s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
					}
				}(callID, cmdStr, outStr, wakeReply, dur, isDaemon, stopCh)

			case "ask":
				rawParts := strings.Split(rest, "|")
				threePart := len(rawParts) <= 3
				var qid, header, qText, optCSV string
				if threePart {
					for len(rawParts) < 3 {
						rawParts = append(rawParts, "")
					}
					qid, header, qText, optCSV = "q1", rawParts[0], rawParts[1], rawParts[2]
				} else {
					qid, header, qText, optCSV = rawParts[0], rawParts[1], rawParts[2], rawParts[3]
				}
				qid = strings.TrimSpace(qid)
				var opts []*sessionv1.QuestionOption
				for _, o := range strings.Split(optCSV, ",") {
					o = strings.TrimSpace(o)
					if o != "" {
						opts = append(opts, &sessionv1.QuestionOption{Id: o, Label: o})
					}
				}
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
						UserInput: &sessionv1.UserInputPayload{
							Questions: []*sessionv1.QuestionItem{{
								Id:         qid,
								Header:     strings.TrimSpace(header),
								Text:       strings.TrimSpace(qText),
								Options:    opts,
								AllowOther: true,
								Optional:   false,
							}},
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					var txt string
					if !res.Allowed {
						txt = "declined: " + res.Feedback
					} else {
						ans := res.Answers[qid]
						summary := "none"
						if ans != nil {
							vals := slices.Clone(ans.SelectedOptions)
							if ans.CustomText != "" {
								vals = append(vals, ans.CustomText)
							}
							if len(ans.Attachments) > 0 {
								vals = append(vals, fmt.Sprintf("files=%d", len(ans.Attachments)))
							}
							sep := ","
							if threePart {
								sep = ", "
							}
							summary = strings.Join(vals, sep)
						}
						if threePart {
							txt = "answer: " + summary
						} else {
							txt = "answer=" + summary
						}
					}
					replyParts = append(replyParts, txt)
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})
				}

			case "propose-plan":
				title, md, _ := strings.Cut(rest, "|")
				title = strings.TrimSpace(title)
				md = strings.ReplaceAll(md, `\n`, "\n")
				planID := fmt.Sprintf("plan_%d", s.itemSeq.Add(1))
				s.emit(runtime.Event{Kind: runtime.EventPlanProposed, ItemID: planID, PlanTitle: title, Text: md})
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
						Plan: &sessionv1.PlanReviewPayload{
							PlanId:   planID,
							Title:    title,
							Markdown: md,
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					if res.PlanApproved {
						txt := "plan_approved=true"
						replyParts = append(replyParts, txt)
						s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})
					} else if res.Feedback != "" {
						txt := "plan_feedback=" + res.Feedback
						replyParts = append(replyParts, txt)
						s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})
					}
				}

			case "propose-artifact":
				parts := strings.SplitN(rest, "|", 3)
				if len(parts) == 3 {
					title := strings.TrimSpace(parts[0])
					reqReview := strings.EqualFold(strings.TrimSpace(parts[1]), "true")
					md := strings.ReplaceAll(parts[2], `\n`, "\n")
					planID := "plan_" + strings.ToLower(strings.ReplaceAll(title, " ", "_"))
					s.emit(runtime.Event{
						Kind:          runtime.EventPlanProposed,
						ItemID:        planID,
						PlanTitle:     title,
						Text:          md,
						RequestReview: reqReview,
					})
				}

			case "ask-secret":
				label, reason, _ := strings.Cut(rest, "|")
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT,
						Secret: &sessionv1.SecretInputPayload{
							Label:    strings.TrimSpace(label),
							Reason:   strings.TrimSpace(reason),
							Delivery: "memory",
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					var txt string
					if !res.Allowed {
						txt = "declined: " + res.Feedback
					} else {
						txt = fmt.Sprintf("secret_received_len=%d", len(res.SecretValue))
					}
					replyParts = append(replyParts, txt)
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})
				}

			case "tokens":
				var parts []string
				if strings.Contains(rest, "|") {
					parts = strings.Split(rest, "|")
				} else {
					parts = strings.Fields(rest)
				}
				if len(parts) >= 2 {
					inTok, _ := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
					outTok, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
					usage.InputTokens = inTok
					usage.OutputTokens = outTok
					usage.ContextUsedTokens = inTok + outTok
				}
				if len(parts) >= 4 {
					used, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
					limit, _ := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 64)
					usage.ContextUsedTokens = used
					usage.ContextLimitTokens = limit
				}

			case "notice":
				txt := "notice: no"
				if in.HandoffPrefix != "" || strings.Contains(prompt, "[Hermesh] The earlier conversation") {
					txt = "notice: yes"
				}
				replyParts = append(replyParts, txt)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})

			case "resumed":
				cur := s.spec.ResumeCursor
				if cur == "" && turnNum > 1 {
					cur = s.nativeID
				}
				txt := "resumed: " + cur
				replyParts = append(replyParts, txt)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})

			case "images":
				s.mu.Lock()
				var names []string
				for _, a := range s.attachments {
					if strings.HasPrefix(a.MimeType, "image/") {
						if a.Name != "" {
							names = append(names, a.Name)
						} else if a.Path != "" {
							names = append(names, filepath.Base(a.Path))
						}
					}
				}
				s.mu.Unlock()
				txt := "images: " + strings.Join(names, ",")
				replyParts = append(replyParts, txt)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})

			case "env":
				v := ""
				if s.spec.Instance != nil && s.spec.Instance.Env != nil {
					v = s.spec.Instance.Env[rest]
				}
				if s.spec.ExtraEnv != nil {
					if ev, ok := s.spec.ExtraEnv[rest]; ok {
						v = ev
					}
				}
				if v == "" {
					v = os.Getenv(rest)
				}
				txt := rest + "=" + v
				replyParts = append(replyParts, txt)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})

			case "inspect":
				s.mu.Lock()
				var optPairs []string
				for k, v := range s.selection.GetOptions() {
					if sv, ok := v.Kind.(*sessionv1.OptionValue_StringValue); ok {
						optPairs = append(optPairs, k+"="+sv.StringValue)
					} else if bv, ok := v.Kind.(*sessionv1.OptionValue_BoolValue); ok {
						optPairs = append(optPairs, fmt.Sprintf("%s=%v", k, bv.BoolValue))
					}
				}
				slices.Sort(optPairs)
				var attNames []string
				for _, a := range s.attachments {
					attNames = append(attNames, a.Name)
				}
				info := fmt.Sprintf(
					"instance=%s model=%s opts=[%s] runtime=%s interaction=%s native=%s handoff=%v attachments=[%s]",
					s.selection.GetInstanceId(),
					s.selection.GetModel(),
					strings.Join(optPairs, ","),
					s.effectiveMode.String(),
					s.interactionMode.String(),
					s.nativeID,
					in.HandoffPrefix != "",
					strings.Join(attNames, ","),
				)
				s.mu.Unlock()
				replyParts = append(replyParts, info)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: info})
			}
		}
		return false
	}

	if runLines(lines) {
		return
	}

	for {
		s.mu.Lock()
		steered := s.steered
		s.steered = nil
		s.mu.Unlock()
		if len(steered) == 0 {
			break
		}
		for _, st := range steered {
			stLines := strings.Split(strings.TrimSpace(st), "\n")
			if len(stLines) == 1 && !isDirective(stLines[0]) {
				stLines = []string{"say Steered: " + stLines[0]}
			}
			if runLines(stLines) {
				return
			}
		}
	}

	var lastReply string
	if len(replyParts) > 0 {
		lastReply = replyParts[len(replyParts)-1]
		s.emit(runtime.Event{
			Kind:   runtime.EventAssistantMessage,
			ItemID: msgItemID,
			Text:   strings.Join(replyParts, "\n"),
		})
	}
	s.emit(runtime.Event{Kind: runtime.EventUsage, Usage: usage})
	s.emit(runtime.Event{Kind: runtime.EventTurnCompleted, Text: lastReply})
}

func (s *session) callTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	if len(s.spec.MCPServers) == 0 {
		return "no Hermesh tools in this session", true
	}
	m := s.spec.MCPServers[0]
	cmd := exec.CommandContext(ctx, m.Command, m.Args...)
	cmd.Env = os.Environ()
	for k, v := range m.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "scripted", Version: "2"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return "starting the tools: " + err.Error(), true
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	var out []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			out = append(out, t.Text)
		}
	}
	return strings.Join(out, "\n"), res.IsError
}

func isDirective(line string) bool {
	verb, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch verb {
	case "Reply", "say", "stream", "draft", "warn", "think", "sleep", "chatter", "tool-sleep", "output",
		"hang", "crash", "overflow", "refuse-resume", "resume-start-reject", "retry-forever", "fail-times",
		"fail", "message", "write", "shell", "run", "bg-shell", "bg-daemon", "ask", "propose-plan", "propose-artifact", "ask-secret",
		"tokens", "notice", "resumed", "images", "env", "inspect":
		return true
	}
	return false
}
