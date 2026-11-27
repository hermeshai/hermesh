// Package runtime defines the unified long-lived provider driver contract,
// session lease supervisor (Idle TTL + Force Stop), process/probe/failure
// utilities, and driver adapters.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

var (
	ErrHotSwitchUnsupported = errors.New("in-session switch unsupported")
	ErrSteerUnsupported     = errors.New("mid-turn steer unsupported")
	ErrSessionClosed        = errors.New("provider session closed")
)

// ProbeResult reports live discovery from an installed provider CLI/service.
type ProbeResult struct {
	Installed            bool
	Authenticated        bool
	AuthLabel            string
	Version              string
	Binary               string
	Compatibility        sessionv1.CompatibilityStatus
	CompatibilityMessage string
	Models               []*sessionv1.ModelCatalogEntry
	WorkspaceOptions     []*sessionv1.OptionDescriptor
	SlashCommands        []*sessionv1.SlashCommand
	Skills               []*sessionv1.SkillEntry
}

// ToolGateRequest is raised synchronously by a live session when the agent
// wants to execute a tool, ask questions, propose a plan, or request a secret.
type ToolGateRequest struct {
	// Delegated: the agent has already decided that the owner must answer (Her's own approval
	// policy did), so the machine's policy and allow rules never answer in the owner's place.
	Delegated bool
	// Background: asked by an awaited background task (such as one of Her's sub-agents), not by
	// the foreground turn, so a foreground turn's end neither cancels nor clears it.
	Background bool
	Kind       sessionv1.RuntimeRequestKind
	Approval   *sessionv1.ToolApprovalPayload
	UserInput  *sessionv1.UserInputPayload
	Plan       *sessionv1.PlanReviewPayload
	Secret     *sessionv1.SecretInputPayload
}

// ToolGateResult is returned to the blocked provider session once policy or the
// operator/agent resolves the request.
type ToolGateResult struct {
	Allowed      bool
	CancelTurn   bool
	Feedback     string
	Answers      map[string]*sessionv1.QuestionAnswer
	SecretValue  string
	PlanApproved bool
}

// GateFunc evaluates policy and, when needed, suspends the turn on a durable
// RuntimeRequest until resolved or ctx is cancelled.
type GateFunc func(ctx context.Context, req ToolGateRequest) (ToolGateResult, error)

// EventKind categorizes normalized streaming events from any underlying driver.
type EventKind int

const (
	EventNativeSession EventKind = iota
	EventAssistantDelta
	EventAssistantMessage
	EventReasoningDelta
	// EventReasoningCompleted: the model stopped thinking (ItemID is the reasoning's). Drivers that cannot tell
	// leave it out; the kernel then ends the reasoning at the model's next step.
	EventReasoningCompleted
	EventToolStarted
	EventToolCompleted
	EventFileChanged
	EventPlanProposed
	EventTodoUpdated
	EventFocusUpdated
	EventUsage
	EventNotice
	EventBackgroundTask
	EventTurnCompleted
	EventTurnInterrupted
	EventTurnFailed
	// EventQuestionAsked: the agent asked the owner something (Question) and ends its turn on it.
	// When the turn completes, the session waits on the question; the answer starts the next turn.
	EventQuestionAsked
)

type Event struct {
	Kind                EventKind
	NativeID            string
	ResumeCursor        string
	Model               string
	Effort              string
	TaskID              string
	ItemID              string
	Text                string
	ToolName            string
	Command             string
	Input               string
	ExitCode            int32
	HasExitCode         bool
	Output              string
	FilePath            string
	Diff                string
	Additions           uint32
	Deletions           uint32
	PlanTitle           string
	RequestReview       bool
	TodoSteps           []*sessionv1.TodoStep
	HasFocus            bool
	Usage               *sessionv1.ContextWindowUsage
	Notice              *sessionv1.Notice
	Failure             *Failure
	BackgroundKind      sessionv1.BackgroundTaskKind
	BackgroundStatus    sessionv1.BackgroundTaskStatus
	BackgroundStopScope sessionv1.BackgroundTaskStopScope
	BackgroundAwaited   bool
	ErrorCode           string
	ErrorMessage        string
	Question            *sessionv1.UserInputPayload
}

// NormalizeTodoStatus maps provider-specific checklist step states onto Hermesh's canonical set.
func NormalizeTodoStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "completed", "complete", "done":
		return "completed"
	case "in_progress", "inprogress", "active", "running":
		return "in_progress"
	case "skipped", "cancelled", "canceled":
		return "skipped"
	case "failed", "error":
		return "failed"
	default:
		return "pending"
	}
}

type StartSpec struct {
	SessionID       string
	Workdir         string
	ReadDirs        []string
	MCPServers      []MCPServer
	Title           string
	Instance        *sessionv1.ProviderInstanceConfig
	Selection       *sessionv1.ModelSelection
	EffectiveMode   identityv1.RuntimeMode
	InteractionMode sessionv1.InteractionMode
	ResumeCursor    string
	ExtraEnv        map[string]string
	Gate            GateFunc
	// StartCtx, when non-nil, bounds only process startup and session handshake
	// (e.g. to an active run's cancellation context) without shortening the
	// long-lived warm lease context passed to Driver.StartSession.
	StartCtx context.Context
	// Limits is told what the agent says of its account's rate limits, when
	// it does (limits.go).
	Limits func(Limits)
}

type TurnAttachment struct {
	Name     string
	MimeType string
	Path     string
	Data     []byte
}

type TurnInput struct {
	RunID           string
	Prompt          string
	HandoffPrefix   string
	Attachments     []TurnAttachment
	Selection       *sessionv1.ModelSelection
	EffectiveMode   identityv1.RuntimeMode
	InteractionMode sessionv1.InteractionMode
}

// Session represents a live long-running provider process/connection attached
// to a single ProviderSession.
type Session interface {
	// Events streams normalized events for the lifetime of the session.
	Events() <-chan Event
	// StartTurn begins executing a new turn on this live session.
	StartTurn(ctx context.Context, in TurnInput) error
	// Steer injects mid-turn user guidance into the active turn if supported.
	Steer(ctx context.Context, text string, attachments []TurnAttachment) error
	// SetSelection updates model/options in-place without restarting if supported.
	SetSelection(ctx context.Context, sel *sessionv1.ModelSelection) error
	// SetModes updates runtime/interaction modes in-place if supported.
	SetModes(ctx context.Context, runtimeMode identityv1.RuntimeMode, interactionMode sessionv1.InteractionMode) error
	// Interrupt requests graceful cancellation of the active turn.
	Interrupt(ctx context.Context) error
	// ForceStop immediately kills the underlying OS process group/tree.
	ForceStop() error
	// Alive reports whether the underlying process/connection is still usable.
	Alive() bool
	// Close gracefully terminates the session and releases OS resources.
	Close() error
}

// Titler is implemented by a live Session that can update its conversation
// title in the provider's native app while running (e.g. Codex).
type Titler interface {
	SetTitle(ctx context.Context, title string) error
}

type TitleGenRequest struct {
	Prompt    string
	Images    []TurnAttachment
	ModelHint string
}

type TitleGenResult struct {
	Title       string `json:"title"`
	Provisional bool   `json:"provisional"`
}

// TitleGenerator is optionally implemented by Drivers that can run a fast,
// tool-free structured title generation pass without polluting user sessions.
type TitleGenerator interface {
	GenerateTitle(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, req TitleGenRequest) (TitleGenResult, error)
}

// TitleGeneratorEnabled is optionally implemented by test/fake TitleGenerators
// whose title generation is gated on configuration or environment.
type TitleGeneratorEnabled interface {
	TitleGeneratorEnabled() bool
}

// Archiver is implemented by a Driver that mirrors archive/unarchive state into
// the provider's native thread list (e.g. Codex app-server thread/archive).
type Archiver interface {
	Archive(ctx context.Context, ref string, archived bool) error
}

// Deleter is implemented by a Driver whose provider keeps the session's conversation itself
// (Her's), so that deleting the session deletes it there too.
type Deleter interface {
	Delete(ctx context.Context, ref string) error
}

// BackgroundController is optionally implemented by Sessions that can stop a
// running background task (either individually or session-wide).
type BackgroundController interface {
	StopBackgroundTask(ctx context.Context, taskID string) error
}

// WakeupProbe is optionally implemented by Sessions that can inspect
// provider-side wakeup queues or live child tool processes before a logical
// turn settles.
type WakeupProbe interface {
	PendingWakeup() bool
}

// Diagnoser is optionally implemented by Sessions backed by a local OS process
// so the kernel can inspect descendant tool processes, CPU liveness, and stderr
// during quiet periods.
type Diagnoser interface {
	Diagnose() Diagnosis
}

// MaxBackgroundIdleTTL bounds how long awaited background work can keep an
// otherwise silent provider process exempt from Supervisor idle reaping.
const MaxBackgroundIdleTTL = 2 * time.Hour

// Driver constructs and probes sessions for one provider family ("claude", "codex", "acp", "her", "scripted").
type Driver interface {
	Kind() string
	Capabilities(acpFlavor string) *sessionv1.ProviderCapabilities
	Probe(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, workdir string) ProbeResult
	StartSession(ctx context.Context, spec StartSpec) (Session, error)
}

// LeaseEntry tracks an active or idle Session managed by Supervisor.
type LeaseEntry struct {
	ProviderSessionID string
	SessionID         string
	InstanceID        string
	DriverKind        string
	Selection         *sessionv1.ModelSelection
	RuntimeMode       identityv1.RuntimeMode
	InteractionMode   sessionv1.InteractionMode
	Session           Session

	mu         sync.Mutex
	lastActive time.Time
	inTurn     bool
	awaitedBg  int
	daemonBg   int
}

func (l *LeaseEntry) Touch() {
	l.mu.Lock()
	l.lastActive = time.Now()
	l.mu.Unlock()
}

func (l *LeaseEntry) SetInTurn(v bool) {
	l.mu.Lock()
	l.inTurn = v
	l.lastActive = time.Now()
	l.mu.Unlock()
}

func (l *LeaseEntry) SetBackgroundCounts(awaited, daemon int) {
	l.mu.Lock()
	l.awaitedBg = awaited
	l.daemonBg = daemon
	l.lastActive = time.Now()
	l.mu.Unlock()
}

func (l *LeaseEntry) BackgroundCounts() (awaited, daemon int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.awaitedBg, l.daemonBg
}

func (l *LeaseEntry) IdleFor(now time.Time) (idle bool, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inTurn {
		return false, 0
	}
	elapsed := now.Sub(l.lastActive)
	if (l.awaitedBg > 0 || l.daemonBg > 0) && elapsed < MaxBackgroundIdleTTL {
		return false, 0
	}
	return true, elapsed
}

// Supervisor manages live provider sessions across sessions, reaping idle ones
// past IdleTTL and force-killing unresponsive ones on shutdown or ForceStop.
type Supervisor struct {
	idleTTL  time.Duration
	onReap   func(*LeaseEntry)
	mu       sync.Mutex
	sessions map[string]*LeaseEntry // providerSessionID -> LeaseEntry
	stopCh   chan struct{}
	once     sync.Once
}

func (s *Supervisor) SetOnReap(fn func(*LeaseEntry)) {
	s.mu.Lock()
	s.onReap = fn
	s.mu.Unlock()
}

func NewSupervisor(idleTTL time.Duration) *Supervisor {
	if idleTTL <= 0 {
		idleTTL = 15 * time.Minute
	}
	s := &Supervisor{
		idleTTL:  idleTTL,
		sessions: map[string]*LeaseEntry{},
		stopCh:   make(chan struct{}),
	}
	go s.reapLoop()
	return s
}

func (s *Supervisor) Register(entry *LeaseEntry) {
	entry.Touch()
	s.mu.Lock()
	old := s.sessions[entry.ProviderSessionID]
	s.sessions[entry.ProviderSessionID] = entry
	s.mu.Unlock()
	if old != nil && old.Session != entry.Session {
		_ = old.Session.Close()
	}
}

func (s *Supervisor) Get(providerSessionID string) *LeaseEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.sessions[providerSessionID]
	if e != nil && !e.Session.Alive() {
		delete(s.sessions, providerSessionID)
		return nil
	}
	return e
}

func (s *Supervisor) Detach(providerSessionID string, force bool) {
	s.DetachSession(providerSessionID, nil, force)
}

func (s *Supervisor) DetachSession(providerSessionID string, match Session, force bool) {
	s.mu.Lock()
	e := s.sessions[providerSessionID]
	if e != nil && match != nil && e.Session != match {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, providerSessionID)
	s.mu.Unlock()
	if e != nil {
		if force {
			_ = e.Session.ForceStop()
		} else {
			_ = e.Session.Close()
		}
	}
}

func (s *Supervisor) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, e := range s.sessions {
		if !e.Session.Alive() {
			delete(s.sessions, id)
			continue
		}
		n++
	}
	return n
}

func (s *Supervisor) ReapIdle(now time.Time) int {
	var expired []*LeaseEntry
	s.mu.Lock()
	onReap := s.onReap
	for id, e := range s.sessions {
		if !e.Session.Alive() {
			delete(s.sessions, id)
			continue
		}
		if idle, d := e.IdleFor(now); idle && d >= s.idleTTL {
			delete(s.sessions, id)
			expired = append(expired, e)
		}
	}
	s.mu.Unlock()
	for _, e := range expired {
		_ = e.Session.Close()
		if onReap != nil {
			onReap(e)
		}
	}
	return len(expired)
}

func (s *Supervisor) reapLoop() {
	interval := min(s.idleTTL/2, 30*time.Second)
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.ReapIdle(now)
		}
	}
}

func (s *Supervisor) Close() error {
	s.once.Do(func() { close(s.stopCh) })
	s.mu.Lock()
	all := make([]*LeaseEntry, 0, len(s.sessions))
	for _, e := range s.sessions {
		all = append(all, e)
	}
	s.sessions = map[string]*LeaseEntry{}
	s.mu.Unlock()
	for _, e := range all {
		_ = e.Session.ForceStop()
	}
	return nil
}

// FormatToolJSON formats a raw JSON value (object, array, or primitive) with
// 2-space indentation for tool input/output display, returning "" when empty
// or a trivial null/{}/[].
func FormatToolJSON(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch string(trimmed) {
	case "null", "{}", "[]", `""`:
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			if inner := strings.TrimSpace(s); len(inner) > 1 && ((inner[0] == '{' && inner[len(inner)-1] == '}') || (inner[0] == '[' && inner[len(inner)-1] == ']')) {
				var buf bytes.Buffer
				if json.Indent(&buf, []byte(inner), "", "  ") == nil {
					kept, _ := KeepOutput(unescapeJSONHTML(buf.String()))
					return kept
				}
			}
			kept, _ := KeepOutput(s)
			return kept
		}
	}
	var buf bytes.Buffer
	if json.Indent(&buf, trimmed, "", "  ") == nil {
		kept, _ := KeepOutput(unescapeJSONHTML(buf.String()))
		return kept
	}
	kept, _ := KeepOutput(string(trimmed))
	return kept
}

// unescapeJSONHTML restores <, >, & that Go's json.Marshal escapes as \u003c,
// \u003e, \u0026 while preserving exact object key order and \\ escapes.
func unescapeJSONHTML(s string) string {
	if !strings.Contains(s, `\u00`) && !strings.Contains(s, `\U00`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if s[i+1] == '\\' {
				b.WriteString(`\\`)
				i++
				continue
			}
			if i+5 < len(s) && s[i+1] == 'u' && s[i+2] == '0' && s[i+3] == '0' {
				switch strings.ToLower(s[i+4 : i+6]) {
				case "3c":
					b.WriteByte('<')
					i += 5
					continue
				case "3e":
					b.WriteByte('>')
					i += 5
					continue
				case "26":
					b.WriteByte('&')
					i += 5
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// MarshalToolInput serializes a non-empty Go map/struct as pretty-printed JSON
// without HTML-escaping (<, >, & stay readable).
func MarshalToolInput(v any) string {
	if v == nil {
		return ""
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(v) != nil {
		return ""
	}
	return FormatToolJSON(bytes.TrimSpace(buf.Bytes()))
}
