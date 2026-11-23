// Package scripted implements a deterministic in-process runtime.Driver for
// unit, chaos, and E2E testing of every Hermesh v2 capability: streaming
// deltas, reasoning, tool approvals, file edits, shell commands, structured
// questions, plan proposals, secret requests, mid-turn steering, hot model/mode
// switching, resume cursors, crashes, hangs, and context-window reporting.
package scripted

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type Driver struct {
	DisableHotSwitch bool
	DisableSteer     bool
	DisableResume    bool

	mu         sync.Mutex
	startSpecs []runtime.StartSpec
	sessionSeq atomic.Uint64
}

func NewDriver() *Driver { return &Driver{} }

func (d *Driver) Kind() string { return "scripted" }

func (d *Driver) Capabilities(_ string) *threadv2.ProviderCapabilities {
	return &threadv2.ProviderCapabilities{
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

func (d *Driver) Probe(_ context.Context, _ *threadv2.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	return runtime.ProbeResult{
		Installed:     true,
		Authenticated: true,
		AuthLabel:     "scripted@local",
		Version:       "2.0.0",
		Compatibility: threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
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
		nativeID = fmt.Sprintf("scr_%s_%d", spec.ThreadID, id)
	}
	s := &session{
		drv:             d,
		spec:            spec,
		nativeID:        nativeID,
		selection:       proto.Clone(spec.Selection).(*threadv2.ModelSelection),
		effectiveMode:   spec.EffectiveMode,
		interactionMode: spec.InteractionMode,
		events:          make(chan runtime.Event, 128),
		turns:           make(chan runtime.TurnInput, 8),
		interruptCh:     make(chan struct{}, 1),
		forceCh:         make(chan struct{}),
		closedCh:        make(chan struct{}),
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
	selection       *threadv2.ModelSelection
	effectiveMode   identityv1.RuntimeMode
	interactionMode threadv2.InteractionMode
	steered         []string
	attachments     []runtime.TurnAttachment
	historyCount    int
}

func (s *session) Events() <-chan runtime.Event { return s.events }
func (s *session) Alive() bool                  { return s.alive.Load() }

func (s *session) SetSelection(_ context.Context, sel *threadv2.ModelSelection) error {
	if s.drv.DisableHotSwitch {
		return runtime.ErrHotSwitchUnsupported
	}
	s.mu.Lock()
	s.selection = proto.Clone(sel).(*threadv2.ModelSelection)
	s.mu.Unlock()
	return nil
}

func (s *session) SetModes(_ context.Context, rm identityv1.RuntimeMode, im threadv2.InteractionMode) error {
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
	// Drain any stale interrupt signal from before the turn started.
	select {
	case <-s.interruptCh:
	default:
	}

	s.mu.Lock()
	s.inTurn = true
	s.steered = nil
	s.attachments = slices.Clone(in.Attachments)
	if in.Selection != nil {
		s.selection = proto.Clone(in.Selection).(*threadv2.ModelSelection)
	}
	if in.EffectiveMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		s.effectiveMode = in.EffectiveMode
	}
	if in.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		s.interactionMode = in.InteractionMode
	}
	s.historyCount++
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inTurn = false
		s.mu.Unlock()
	}()

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	usage := &threadv2.ContextWindowUsage{
		InputTokens:        120,
		OutputTokens:       45,
		ContextUsedTokens:  165,
		ContextLimitTokens: 200000,
	}

	lines := strings.Split(strings.TrimSpace(in.Prompt), "\n")
	// Plain natural language prompts (not starting with a scripted verb) reply naturally.
	if len(lines) == 1 && !isDirective(lines[0]) {
		lines = []string{"say Acknowledged: " + lines[0]}
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
			case "say":
				text := strings.ReplaceAll(rest, `\n`, "\n")
				replyParts = append(replyParts, text)
				s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: text})

			case "stream":
				chunks := strings.Split(rest, "|")
				for _, ch := range chunks {
					part := strings.ReplaceAll(ch, `\n`, "\n")
					replyParts = append(replyParts, part)
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: part})
					select {
					case <-time.After(20 * time.Millisecond):
					case <-s.interruptCh:
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					case <-s.forceCh:
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
				}

			case "think":
				s.emit(runtime.Event{
					Kind:   runtime.EventReasoningDelta,
					ItemID: fmt.Sprintf("rsn_%d", s.itemSeq.Add(1)),
					Text:   strings.ReplaceAll(rest, `\n`, "\n"),
				})

			case "sleep":
				dur, err := time.ParseDuration(rest)
				if err != nil {
					dur = 200 * time.Millisecond
				}
				select {
				case <-time.After(dur):
				case <-s.interruptCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				case <-s.forceCh:
					s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
					return true
				}

			case "hang":
				// Ignores polite Interrupt! Only ForceStop terminates it.
				<-s.forceCh
				s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
				return true

			case "crash":
				s.alive.Store(false)
				s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "process_crashed", ErrorMessage: "scripted agent process exited unexpectedly"})
				_ = s.Close()
				return true

			case "fail":
				code, msg, ok := strings.Cut(rest, "|")
				if !ok {
					code, msg = "provider_error", rest
				}
				s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: strings.TrimSpace(code), ErrorMessage: strings.TrimSpace(msg)})
				return true

			case "write":
				relPath, content, _ := strings.Cut(rest, "|")
				relPath = strings.TrimSpace(relPath)
				content = strings.ReplaceAll(content, `\n`, "\n")
				target := relPath
				if !filepath.IsAbs(target) {
					target = filepath.Join(s.spec.Workdir, target)
				}
				callID := fmt.Sprintf("edit_%d", s.itemSeq.Add(1))
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
						Approval: &threadv2.ToolApprovalPayload{
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

			case "shell":
				cmdStr, outStr, _ := strings.Cut(rest, "|")
				cmdStr = strings.TrimSpace(cmdStr)
				outStr = strings.ReplaceAll(outStr, `\n`, "\n")
				callID := fmt.Sprintf("cmd_%d", s.itemSeq.Add(1))
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
						Approval: &threadv2.ToolApprovalPayload{
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

			case "ask":
				// ask <id>|<header>|<question>|<opt1,opt2>
				parts := strings.Split(rest, "|")
				for len(parts) < 4 {
					parts = append(parts, "")
				}
				var opts []*threadv2.QuestionOption
				for _, o := range strings.Split(parts[3], ",") {
					o = strings.TrimSpace(o)
					if o != "" {
						opts = append(opts, &threadv2.QuestionOption{Id: o, Label: o})
					}
				}
				if s.spec.Gate != nil {
					res, err := s.spec.Gate(turnCtx, runtime.ToolGateRequest{
						Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
						UserInput: &threadv2.UserInputPayload{
							Questions: []*threadv2.QuestionItem{{
								Id:          strings.TrimSpace(parts[0]),
								Header:      strings.TrimSpace(parts[1]),
								Question:    strings.TrimSpace(parts[2]),
								Options:     opts,
								AllowCustom: true,
								Required:    true,
							}},
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					ans := res.Answers[strings.TrimSpace(parts[0])]
					summary := "none"
					if ans != nil {
						vals := slices.Clone(ans.SelectedOptions)
						if ans.CustomText != "" {
							vals = append(vals, ans.CustomText)
						}
						if len(ans.Attachments) > 0 {
							vals = append(vals, fmt.Sprintf("files=%d", len(ans.Attachments)))
						}
						summary = strings.Join(vals, ",")
					}
					txt := "answer=" + summary
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
						Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
						Plan: &threadv2.PlanReviewPayload{
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
				// Post-turn proposal/deliverable (models Antigravity ArtifactMetadata & Codex plan items):
				// format: propose-artifact <title>|<request_feedback:true/false>|<markdown>
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
						Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT,
						Secret: &threadv2.SecretInputPayload{
							Label:    strings.TrimSpace(label),
							Reason:   strings.TrimSpace(reason),
							Delivery: "memory",
						},
					})
					if err != nil || res.CancelTurn {
						s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
						return true
					}
					// Never echo the secret value itself! Only confirm its byte length.
					txt := fmt.Sprintf("secret_received_len=%d", len(res.SecretValue))
					replyParts = append(replyParts, txt)
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgItemID, Text: txt})
				}

			case "tokens":
				parts := strings.Split(rest, "|")
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

			case "inspect":
				s.mu.Lock()
				var optPairs []string
				for k, v := range s.selection.GetOptions() {
					if sv, ok := v.Kind.(*threadv2.OptionValue_StringValue); ok {
						optPairs = append(optPairs, k+"="+sv.StringValue)
					} else if bv, ok := v.Kind.(*threadv2.OptionValue_BoolValue); ok {
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

	// Process any mid-turn steered inputs that arrived while running!
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

	if len(replyParts) > 0 {
		s.emit(runtime.Event{
			Kind:   runtime.EventAssistantMessage,
			ItemID: msgItemID,
			Text:   strings.Join(replyParts, "\n"),
		})
	}
	s.emit(runtime.Event{Kind: runtime.EventUsage, Usage: usage})
	s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
}

func isDirective(line string) bool {
	verb, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch verb {
	case "say", "stream", "think", "sleep", "hang", "crash", "fail", "write", "shell", "ask", "propose-plan", "propose-artifact", "ask-secret", "tokens", "inspect":
		return true
	}
	return false
}
