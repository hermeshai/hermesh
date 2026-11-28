// Package her adapts Hermesh's personal assistant to the runtime.Driver contract: a session of this
// driver is a conversation of Her's besides her main one (docs/her-conversations.md §5). Her keeps the
// conversation and shows it; the kernel keeps the session's record (its state, title, pins, children)
// from what the conversation does, so it is listed, synced and announced as any session is.
package her

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

// Bridge is the Her service as the driver reaches it (internal/herhost).
type Bridge interface {
	// Available: Her runs on this machine.
	Available() bool
	// Models are those Her can use, her default first.
	Models(ctx context.Context) ([]*sessionv1.ModelCatalogEntry, error)
	// NewConversation starts a conversation of Her's (bots/conversation_new); empty model and
	// thinking: her main conversation's.
	NewConversation(ctx context.Context, model, thinking string) (string, error)
	// Attach follows one of her conversations, loading it.
	Attach(ctx context.Context, conversation string) (Conversation, error)
	// Op sends one op to a conversation that need not be followed.
	Op(ctx context.Context, conversation string, op any) error
	// DeleteConversation moves one to Her's trash.
	DeleteConversation(ctx context.Context, conversation string) error
}

// Conversation is one conversation of Her's, followed.
type Conversation interface {
	// Events are her session events (session/event's "event"), until Close.
	Events() <-chan json.RawMessage
	// Op sends an op to it (session/op).
	Op(ctx context.Context, op any) error
	Close() error
}

type Driver struct {
	Bridge Bridge
}

func NewDriver(b Bridge) *Driver { return &Driver{Bridge: b} }

func (d *Driver) Kind() string { return "her" }

func (d *Driver) Capabilities(_ string) *sessionv1.ProviderCapabilities {
	return &sessionv1.ProviderCapabilities{
		MidTurnSteer: true,
		NativeResume: true,
		SupportedRuntimeModes: []identityv1.RuntimeMode{
			identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
			identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		},
	}
}

func (d *Driver) Probe(ctx context.Context, _ *sessionv1.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	if d.Bridge == nil || !d.Bridge.Available() {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "Her is not enabled on this machine",
		}
	}
	models, _ := d.Bridge.Models(ctx)
	return runtime.ProbeResult{
		Installed:     true,
		Authenticated: len(models) > 0,
		AuthLabel:     "Her",
		Compatibility: sessionv1.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
		Models:        models,
	}
}

// Delete deletes the conversation of a deleted session (runtime.Deleter).
func (d *Driver) Delete(ctx context.Context, ref string) error {
	if d.Bridge == nil {
		return nil
	}
	return d.Bridge.DeleteConversation(ctx, ref)
}

// Archive mirrors the session's archiving into Her's conversation (runtime.Archiver).
func (d *Driver) Archive(ctx context.Context, ref string, archived bool) error {
	if d.Bridge == nil {
		return nil
	}
	return d.Bridge.Op(ctx, ref, map[string]any{"type": "set_archived", "archived": archived})
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	if d.Bridge == nil || !d.Bridge.Available() {
		return nil, errors.New("Her is not running on this machine")
	}
	id := spec.ResumeCursor
	if id == "" {
		var err error
		if id, err = d.Bridge.NewConversation(ctx, modelOf(spec.Selection), effort(spec.Selection)); err != nil {
			return nil, err
		}
	}
	conv, err := d.Bridge.Attach(ctx, id)
	if err != nil {
		return nil, err
	}
	s := &session{
		id:     id,
		conv:   conv,
		gate:   spec.Gate,
		events: make(chan runtime.Event, 64),
		closed: make(chan struct{}),
		asks:   map[string]context.CancelFunc{},
		agents: map[string]subAgent{},
	}
	s.alive.Store(true)
	s.emit(runtime.Event{Kind: runtime.EventNativeSession, NativeID: id, ResumeCursor: id})
	go s.follow()
	return s, nil
}

// modelOf is the model asked for, or "" for Her's own when none was named ("default").
func modelOf(sel *sessionv1.ModelSelection) string {
	if m := sel.GetModel(); m != "default" {
		return m
	}
	return ""
}

// effort is the selection's reasoning effort, which Her calls thinking.
func effort(sel *sessionv1.ModelSelection) string {
	return sel.GetOptions()["effort"].GetStringValue()
}

type subAgent struct {
	callID  string
	title   string
	running bool
}

type session struct {
	id     string
	conv   Conversation
	gate   runtime.GateFunc
	events chan runtime.Event
	closed chan struct{}
	alive  atomic.Bool
	once   sync.Once

	emitMu sync.RWMutex
	done   bool

	mu            sync.Mutex
	asks          map[string]context.CancelFunc // approvals put to the owner, by askKey
	agents        map[string]subAgent           // Her's sub-agents, by agent_id
	settledAgents []runtime.Event               // sub-agent settlements held while !inTask until task_started or idle
	turnPending   bool                          // StartTurn awaits task_started
	inTask        bool                          // between task_started and task_completed/failed/interrupted
}

func (s *session) Events() <-chan runtime.Event { return s.events }
func (s *session) Alive() bool                  { return s.alive.Load() }

func (s *session) emit(ev runtime.Event) {
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.done {
		return
	}
	select {
	case s.events <- ev:
	case <-s.closed:
	}
}

// answersPrefix is how the kernel words the answers to a question when it starts the next turn
// with them (engine.go, RespondRuntimeRequest).
const answersPrefix = "Answers to previous questions:\n"

// asOwner is what Her reads of a message: an answer to her question is the owner's words, as
// tapping it in her chat sends them, not the kernel's report of it.
func asOwner(prompt string) string {
	rest, ok := strings.CutPrefix(prompt, answersPrefix)
	if !ok {
		return prompt
	}
	var said []string
	for line := range strings.SplitSeq(rest, "\n") {
		if _, answer, ok := strings.Cut(line, ": "); ok && strings.TrimSpace(answer) != "" {
			said = append(said, strings.TrimSpace(answer))
		}
	}
	if len(said) == 0 {
		return prompt
	}
	return strings.Join(said, "\n")
}

func buildHerInput(text string, attachments []runtime.TurnAttachment) []map[string]string {
	input := []map[string]string{}
	if strings.TrimSpace(text) != "" {
		input = append(input, map[string]string{"type": "text", "text": text})
	}
	for _, a := range attachments {
		if strings.HasPrefix(a.MimeType, "image/") && len(a.Data) > 0 {
			input = append(input, map[string]string{"type": "image", "mime_type": a.MimeType, "data": base64.StdEncoding.EncodeToString(a.Data)})
		}
	}
	return input
}

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	input := buildHerInput(asOwner(in.HandoffPrefix+in.Prompt), in.Attachments)
	// The run's id names the message, so that sending it again never doubles it.
	s.mu.Lock()
	s.turnPending = true
	s.mu.Unlock()
	if err := s.conv.Op(ctx, map[string]any{"type": "prompt", "input": input, "id": "hm_" + idChars(in.RunID)}); err != nil {
		s.mu.Lock()
		s.turnPending = false
		s.mu.Unlock()
		return err
	}
	return nil
}

// idChars keeps what a prompt id may hold (A-Z a-z 0-9 _ -), at most 61 of them.
func idChars(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 61; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			b = append(b, c)
		}
	}
	return string(b)
}

func (s *session) Steer(ctx context.Context, text string, attachments []runtime.TurnAttachment) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	s.mu.Lock()
	active := s.turnPending || s.inTask
	s.mu.Unlock()
	if !active {
		return errors.New("no active turn to steer")
	}
	input := buildHerInput(text, attachments)
	if len(input) == 0 {
		return nil
	}
	return s.conv.Op(ctx, map[string]any{"type": "prompt", "input": input, "mode": "steer"})
}

func (s *session) SetSelection(ctx context.Context, sel *sessionv1.ModelSelection) error {
	m := modelOf(sel)
	if m == "" {
		return nil
	}
	op := map[string]any{"type": "set_model", "model": m}
	if e := effort(sel); e != "" {
		op["thinking"] = e
	}
	return s.conv.Op(ctx, op)
}

func (s *session) SetModes(context.Context, identityv1.RuntimeMode, sessionv1.InteractionMode) error {
	return nil
}

// SetTitle names her conversation as the session is named (runtime.Titler).
func (s *session) SetTitle(ctx context.Context, title string) error {
	return s.conv.Op(ctx, map[string]any{"type": "set_title", "title": title})
}

func (s *session) Interrupt(ctx context.Context) error {
	return s.conv.Op(ctx, map[string]any{"type": "interrupt"})
}

// StopBackgroundTask interrupts one of Her's running sub-agents, or all of them when taskID is
// empty (runtime.BackgroundController).
func (s *session) StopBackgroundTask(ctx context.Context, taskID string) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	if taskID != "" {
		return s.conv.Op(ctx, map[string]any{"type": "agent_interrupt", "agent": taskID})
	}
	s.mu.Lock()
	var ids []string
	for id, a := range s.agents {
		if a.running {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	var firstErr error
	for _, id := range ids {
		if err := s.conv.Op(ctx, map[string]any{"type": "agent_interrupt", "agent": id}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *session) ForceStop() error { return s.Close() }

func (s *session) Close() error {
	s.alive.Store(false)
	s.once.Do(func() {
		s.mu.Lock()
		for _, cancel := range s.asks {
			cancel()
		}
		s.mu.Unlock()
		close(s.closed)
		_ = s.conv.Close()
		s.emitMu.Lock()
		s.done = true
		close(s.events)
		s.emitMu.Unlock()
	})
	return nil
}

// herEvent is what the driver reads of one of her session events.
type herEvent struct {
	Type        string          `json:"type"`
	TaskID      string          `json:"task_id"`
	CallID      string          `json:"call_id"`
	AgentID     string          `json:"agent_id"`
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	Args        json.RawMessage `json:"args"`
	Event       json.RawMessage `json:"event"`
	Summary     string          `json:"summary"`
	Reason      string          `json:"reason"`
	Error       struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		ToolName string `json:"tool_name"`
		IsError  bool   `json:"is_error"`
	} `json:"result"`
}

// sendArgs are send_to_user's arguments (her-core bot.rs SendArgs).
type sendArgs struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Options []struct {
		Label string `json:"label"`
	} `json:"options"`
}

const sendToUser = "send_to_user"

// follow turns her events into the runtime's until the conversation closes.
func (s *session) follow() {
	for raw := range s.conv.Events() {
		var ev herEvent
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		s.take(ev)
	}
	s.Close()
}

func (s *session) take(ev herEvent) {
	switch ev.Type {
	case "task_started":
		s.mu.Lock()
		external := !s.turnPending && !s.inTask
		s.turnPending = false
		s.inTask = true
		settled := s.settledAgents
		s.settledAgents = nil
		s.mu.Unlock()
		if external {
			s.emit(runtime.Event{Kind: runtime.EventReasoningDelta, ItemID: "think_" + cmp.Or(ev.TaskID, "turn")})
		}
		for _, bg := range settled {
			s.emit(bg)
		}
	case "tool_started":
		if ev.Name == sendToUser {
			var a sendArgs
			_ = json.Unmarshal(ev.Args, &a)
			if strings.TrimSpace(a.Text) != "" {
				s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: ev.CallID, Text: a.Text})
			}
			if q := question(ev.CallID, a); q != nil {
				s.emit(runtime.Event{Kind: runtime.EventQuestionAsked, ItemID: ev.CallID, Question: q})
			}
			return
		}
		s.emit(runtime.Event{Kind: runtime.EventToolStarted, ItemID: ev.CallID, Text: strings.TrimSpace(ev.Title), ToolName: ev.Name, Command: ev.Title})
	case "tool_completed":
		if ev.Result.ToolName == sendToUser {
			return
		}
		ex := int32(0)
		if ev.Result.IsError {
			ex = 1
		}
		s.emit(runtime.Event{Kind: runtime.EventToolCompleted, ItemID: ev.CallID, ToolName: ev.Result.ToolName, ExitCode: ex, HasExitCode: true})
	case "agent_started":
		if ev.AgentID == "" {
			return
		}
		title := cmp.Or(strings.TrimSpace(ev.Description), strings.TrimSpace(ev.Name), ev.AgentID)
		s.mu.Lock()
		s.agents[ev.AgentID] = subAgent{callID: ev.CallID, title: title, running: true}
		s.mu.Unlock()
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              ev.AgentID,
			ItemID:              ev.CallID,
			ToolName:            title,
			Command:             title,
			BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		})
	case "agent_status":
		s.takeAgentStatus(ev)
	case "agent_event":
		if ev.AgentID == "" || len(ev.Event) == 0 {
			return
		}
		var inner herEvent
		if json.Unmarshal(ev.Event, &inner) != nil {
			return
		}
		switch inner.Type {
		case "approval_requested":
			s.ask(ev.AgentID, inner)
		case "approval_resolved":
			s.settled(askKey(ev.AgentID, inner.CallID))
		}
	case "approval_requested":
		s.ask("", ev)
	case "approval_resolved":
		s.settled(askKey("", ev.CallID))
	case "task_completed":
		s.endTask()
		s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
	case "task_failed":
		s.endTask()
		s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "her_" + ev.Error.Kind, ErrorMessage: ev.Error.Message})
	case "task_interrupted":
		s.endTask()
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
	case "idle":
		s.mu.Lock()
		settled := s.settledAgents
		s.settledAgents = nil
		s.mu.Unlock()
		for _, bg := range settled {
			s.emit(bg)
		}
	}
}

func (s *session) endTask() {
	s.mu.Lock()
	s.inTask = false
	s.turnPending = false
	s.mu.Unlock()
}

func (s *session) takeAgentStatus(ev herEvent) {
	if ev.AgentID == "" {
		return
	}
	s.mu.Lock()
	sub := s.agents[ev.AgentID]
	title := cmp.Or(sub.title, strings.TrimSpace(ev.Name), ev.AgentID)
	var bg runtime.Event
	switch ev.Status {
	case "running":
		sub.title = title
		sub.running = true
		s.agents[ev.AgentID] = sub
		s.mu.Unlock()
		s.emit(runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              ev.AgentID,
			ItemID:              sub.callID,
			ToolName:            title,
			Command:             title,
			BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		})
		return
	case "completed":
		sub.running = false
		s.agents[ev.AgentID] = sub
		bg = runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              ev.AgentID,
			ItemID:              sub.callID,
			ToolName:            title,
			Command:             title,
			Text:                strings.TrimSpace(ev.Summary),
			HasExitCode:         true,
			ExitCode:            0,
			BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		}
	case "failed":
		sub.running = false
		s.agents[ev.AgentID] = sub
		bg = runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              ev.AgentID,
			ItemID:              sub.callID,
			ToolName:            title,
			Command:             title,
			Text:                cmp.Or(strings.TrimSpace(ev.Error.Message), strings.TrimSpace(ev.Summary)),
			HasExitCode:         true,
			ExitCode:            1,
			BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		}
	case "interrupted", "closed":
		sub.running = false
		s.agents[ev.AgentID] = sub
		bg = runtime.Event{
			Kind:                runtime.EventBackgroundTask,
			TaskID:              ev.AgentID,
			ItemID:              sub.callID,
			ToolName:            title,
			Command:             title,
			Text:                strings.TrimSpace(ev.Summary),
			HasExitCode:         true,
			ExitCode:            130,
			BackgroundKind:      sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
			BackgroundStatus:    sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED,
			BackgroundStopScope: sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SINGLE,
			BackgroundAwaited:   true,
		}
	default:
		s.mu.Unlock()
		return
	}
	inTask := s.inTask
	if !inTask {
		// Held until task_started (when the sub-agent's report wakes Her) or idle (when nothing
		// wakes her), so the session never flashes Done between the sub-agent's end and Her's reply.
		s.settledAgents = append(s.settledAgents, bg)
	}
	s.mu.Unlock()
	if inTask {
		s.emit(bg)
	}
}

// question is a question Her asked with answers to tap, as the owner's question in a session.
func question(callID string, a sendArgs) *sessionv1.UserInputPayload {
	if a.Type != "" && a.Type != "question" || a.Type == "" && len(a.Options) == 0 {
		return nil
	}
	item := &sessionv1.QuestionItem{Id: "q", Text: a.Text, AllowOther: true}
	for _, o := range a.Options {
		if strings.TrimSpace(o.Label) != "" {
			item.Options = append(item.Options, &sessionv1.QuestionOption{Label: o.Label})
		}
	}
	return &sessionv1.UserInputPayload{Questions: []*sessionv1.QuestionItem{item}}
}

func askKey(agentID, callID string) string {
	if agentID == "" {
		return callID
	}
	return agentID + "/" + callID
}

// ask puts an approval Her (or one of her sub-agents) asks for to the owner, as the session's
// request: Her's own policy decided that the owner must answer, so the machine's never answers in
// their place (Delegated). It is settled by the owner's answer there, or by their answer in Her's
// chat (approval_resolved).
func (s *session) ask(agentID string, ev herEvent) {
	if s.gate == nil {
		return
	}
	key := askKey(agentID, ev.CallID)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if old, ok := s.asks[key]; ok {
		old()
	}
	s.asks[key] = cancel
	s.mu.Unlock()
	summary, detail, _ := strings.Cut(ev.Summary, "\n")
	go func() {
		defer s.settled(key)
		res, err := s.gate(ctx, runtime.ToolGateRequest{
			Delegated:  true,
			Background: agentID != "",
			Kind:       sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
			Approval: &sessionv1.ToolApprovalPayload{
				ToolCategory: "her",
				ToolName:     ev.Name,
				Command:      strings.TrimSpace(summary),
				DiffPreview:  strings.TrimSpace(detail),
				Reason:       ev.Reason,
			},
		})
		if err != nil || ctx.Err() != nil {
			return
		}
		decision := map[string]any{"type": "approve"}
		switch {
		case res.CancelTurn:
			decision = map[string]any{"type": "abort"}
		case !res.Allowed:
			decision = map[string]any{"type": "deny"}
			if res.Feedback != "" {
				decision["feedback"] = res.Feedback
			}
		}
		op := map[string]any{"type": "approval", "call_id": ev.CallID, "decision": decision}
		if agentID != "" {
			op = map[string]any{"type": "agent_approval", "agent": agentID, "call_id": ev.CallID, "decision": decision}
		}
		_ = s.conv.Op(context.Background(), op)
	}()
}

// settled ends the wait for an approval that was answered.
func (s *session) settled(key string) {
	s.mu.Lock()
	cancel, ok := s.asks[key]
	delete(s.asks, key)
	s.mu.Unlock()
	if ok {
		cancel()
	}
}
