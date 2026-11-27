// Package her adapts Hermesh's personal assistant to the runtime.Driver contract: a session of this
// driver is a conversation of Her's besides her main one (docs/her-conversations.md §5). Her keeps the
// conversation and shows it; the kernel keeps the session's record (its state, title, pins, children)
// from what the conversation does, so it is listed, synced and announced as any session is.
package her

import (
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

	mu   sync.Mutex
	asks map[string]context.CancelFunc // approvals put to the owner, by call
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

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	input := []map[string]string{}
	if text := asOwner(in.HandoffPrefix + in.Prompt); strings.TrimSpace(text) != "" {
		input = append(input, map[string]string{"type": "text", "text": text})
	}
	for _, a := range in.Attachments {
		if strings.HasPrefix(a.MimeType, "image/") && len(a.Data) > 0 {
			input = append(input, map[string]string{"type": "image", "mime_type": a.MimeType, "data": base64.StdEncoding.EncodeToString(a.Data)})
		}
	}
	// The run's id names the message, so that sending it again never doubles it.
	return s.conv.Op(ctx, map[string]any{"type": "prompt", "input": input, "id": "hm_" + idChars(in.RunID)})
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
	return runtime.ErrSteerUnsupported
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
	Type    string          `json:"type"`
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Title   string          `json:"title"`
	Args    json.RawMessage `json:"args"`
	Summary string          `json:"summary"`
	Reason  string          `json:"reason"`
	Error   struct {
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
	case "approval_requested":
		s.ask(ev)
	case "approval_resolved":
		s.settled(ev.CallID)
	case "task_completed":
		s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
	case "task_failed":
		s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "her_" + ev.Error.Kind, ErrorMessage: ev.Error.Message})
	case "task_interrupted":
		s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
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

// ask puts an approval Her asks for to the owner, as the session's request: Her's own policy decided
// that the owner must answer, so the machine's never answers in their place (Delegated). It is
// settled by the owner's answer there, or by their answer in Her's chat (approval_resolved).
func (s *session) ask(ev herEvent) {
	if s.gate == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if old, ok := s.asks[ev.CallID]; ok {
		old()
	}
	s.asks[ev.CallID] = cancel
	s.mu.Unlock()
	summary, detail, _ := strings.Cut(ev.Summary, "\n")
	go func() {
		defer s.settled(ev.CallID)
		res, err := s.gate(ctx, runtime.ToolGateRequest{
			Delegated: true,
			Kind:      sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
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
		_ = s.conv.Op(context.Background(), map[string]any{"type": "approval", "call_id": ev.CallID, "decision": decision})
	}()
}

// settled ends the wait for an approval that was answered.
func (s *session) settled(callID string) {
	s.mu.Lock()
	cancel, ok := s.asks[callID]
	delete(s.asks, callID)
	s.mu.Unlock()
	if ok {
		cancel()
	}
}
