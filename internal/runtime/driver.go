// Package runtime defines the unified long-lived provider driver contract,
// session lease supervisor (Idle TTL + Force Stop), process/probe/failure
// utilities, and driver adapters.
package runtime

import (
	"context"
	"errors"
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
	Kind      sessionv1.RuntimeRequestKind
	Approval  *sessionv1.ToolApprovalPayload
	UserInput *sessionv1.UserInputPayload
	Plan      *sessionv1.PlanReviewPayload
	Secret    *sessionv1.SecretInputPayload
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
	EventUsage
	EventNotice
	EventBackgroundTask
	EventTurnCompleted
	EventTurnInterrupted
	EventTurnFailed
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
	Usage               *sessionv1.ContextWindowUsage
	Notice              *sessionv1.Notice
	Failure             *Failure
	BackgroundKind      sessionv1.BackgroundTaskKind
	BackgroundStatus    sessionv1.BackgroundTaskStatus
	BackgroundStopScope sessionv1.BackgroundTaskStopScope
	BackgroundAwaited   bool
	ErrorCode           string
	ErrorMessage        string
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

// Archiver is implemented by a Driver that mirrors archive/unarchive state into
// the provider's native thread list (e.g. Codex app-server thread/archive).
type Archiver interface {
	Archive(ctx context.Context, ref string, archived bool) error
}

// BackgroundController is optionally implemented by Sessions that can stop a
// running background task (either individually or session-wide).
type BackgroundController interface {
	StopBackgroundTask(ctx context.Context, taskID string) error
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
