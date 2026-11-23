// Package runtime defines the unified long-lived provider driver contract,
// session lease supervisor (Idle TTL + Force Stop), and driver adapters.
package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
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
	Compatibility        threadv2.CompatibilityStatus
	CompatibilityMessage string
	Models               []*threadv2.ModelCatalogEntry
	WorkspaceOptions     []*threadv2.OptionDescriptor
	SlashCommands        []*threadv2.SlashCommand
	Skills               []*threadv2.SkillEntry
}

// ToolGateRequest is raised synchronously by a live session when the agent
// wants to execute a tool, ask questions, propose a plan, or request a secret.
type ToolGateRequest struct {
	Kind      threadv2.RuntimeRequestKind
	Approval  *threadv2.ToolApprovalPayload
	UserInput *threadv2.UserInputPayload
	Plan      *threadv2.PlanReviewPayload
	Secret    *threadv2.SecretInputPayload
}

// ToolGateResult is returned to the blocked provider session once policy or the
// human operator resolves the request.
type ToolGateResult struct {
	Allowed      bool
	CancelTurn   bool
	Feedback     string
	Answers      map[string]*threadv2.QuestionAnswer
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
	EventToolStarted
	EventToolCompleted
	EventFileChanged
	EventPlanProposed
	EventTodoUpdated
	EventUsage
	EventNotice
	EventTurnCompleted
	EventTurnInterrupted
	EventTurnFailed
)

type Event struct {
	Kind          EventKind
	NativeID      string
	ResumeCursor  string
	ItemID        string
	Text          string
	ToolName      string
	Command       string
	ExitCode      int32
	Output        string
	FilePath      string
	Diff          string
	Additions     uint32
	Deletions     uint32
	PlanTitle     string
	RequestReview bool
	TodoSteps     []*threadv2.TodoStep
	Usage         *threadv2.ContextWindowUsage
	ErrorCode     string
	ErrorMessage  string
}

type StartSpec struct {
	ThreadID        string
	Workdir         string
	Instance        *threadv2.ProviderInstanceConfig
	Selection       *threadv2.ModelSelection
	EffectiveMode   identityv1.RuntimeMode
	InteractionMode threadv2.InteractionMode
	ResumeCursor    string
	ExtraEnv        map[string]string
	Gate            GateFunc
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
	Selection       *threadv2.ModelSelection
	EffectiveMode   identityv1.RuntimeMode
	InteractionMode threadv2.InteractionMode
}

// Session represents a live long-running provider process/connection attached
// to a single ProviderThread.
type Session interface {
	// Events streams normalized events for the lifetime of the session.
	Events() <-chan Event
	// StartTurn begins executing a new turn on this live session.
	StartTurn(ctx context.Context, in TurnInput) error
	// Steer injects mid-turn user guidance into the active turn if supported.
	Steer(ctx context.Context, text string, attachments []TurnAttachment) error
	// SetSelection updates model/options in-place without restarting if supported.
	SetSelection(ctx context.Context, sel *threadv2.ModelSelection) error
	// SetModes updates runtime/interaction modes in-place if supported.
	SetModes(ctx context.Context, runtimeMode identityv1.RuntimeMode, interactionMode threadv2.InteractionMode) error
	// Interrupt requests graceful cancellation of the active turn.
	Interrupt(ctx context.Context) error
	// ForceStop immediately kills the underlying OS process group/tree.
	ForceStop() error
	// Alive reports whether the underlying process/connection is still usable.
	Alive() bool
	// Close gracefully terminates the session and releases OS resources.
	Close() error
}

// Driver constructs and probes sessions for one provider family ("claude", "codex", "acp", "her", "scripted").
type Driver interface {
	Kind() string
	Capabilities(acpFlavor string) *threadv2.ProviderCapabilities
	Probe(ctx context.Context, cfg *threadv2.ProviderInstanceConfig, workdir string) ProbeResult
	StartSession(ctx context.Context, spec StartSpec) (Session, error)
}

// LeaseEntry tracks an active or idle Session managed by Supervisor.
type LeaseEntry struct {
	ProviderThreadID string
	ThreadID         string
	InstanceID       string
	DriverKind       string
	Selection        *threadv2.ModelSelection
	RuntimeMode      identityv1.RuntimeMode
	InteractionMode  threadv2.InteractionMode
	Session          Session

	mu         sync.Mutex
	lastActive time.Time
	inTurn     bool
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

func (l *LeaseEntry) IdleFor(now time.Time) (idle bool, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inTurn {
		return false, 0
	}
	return true, now.Sub(l.lastActive)
}

// Supervisor manages live provider sessions across threads, reaping idle ones
// past IdleTTL and force-killing unresponsive ones on shutdown or ForceStop.
type Supervisor struct {
	idleTTL  time.Duration
	mu       sync.Mutex
	sessions map[string]*LeaseEntry // providerThreadID -> LeaseEntry
	stopCh   chan struct{}
	once     sync.Once
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
	old := s.sessions[entry.ProviderThreadID]
	s.sessions[entry.ProviderThreadID] = entry
	s.mu.Unlock()
	if old != nil && old.Session != entry.Session {
		_ = old.Session.Close()
	}
}

func (s *Supervisor) Get(providerThreadID string) *LeaseEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.sessions[providerThreadID]
	if e != nil && !e.Session.Alive() {
		delete(s.sessions, providerThreadID)
		return nil
	}
	return e
}

func (s *Supervisor) Detach(providerThreadID string, force bool) {
	s.DetachSession(providerThreadID, nil, force)
}

func (s *Supervisor) DetachSession(providerThreadID string, match Session, force bool) {
	s.mu.Lock()
	e := s.sessions[providerThreadID]
	if e != nil && match != nil && e.Session != match {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, providerThreadID)
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
