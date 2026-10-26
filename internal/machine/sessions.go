package machine

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/shellenv"
	"github.com/hermeshai/hermesh/pkg/identity"
)

type Limits struct {
	// MaxConcurrent caps the sessions running at once; more wait queued.
	// 0, the default: no cap.
	MaxConcurrent int
	// Liveness of a running turn (see docs/reliability-plan.md §3): the
	// agent must write something within FirstOutputTimeout of starting,
	// then may stay silent for IdleTimeout, or for ToolTimeout while one
	// of its tools runs (0: no limit while a tool runs). TurnTimeout caps a
	// turn that keeps making progress (0: no cap).
	FirstOutputTimeout time.Duration
	IdleTimeout        time.Duration
	ToolTimeout        time.Duration
	TurnTimeout        time.Duration
	IdleProcessTTL     time.Duration
}

func DefaultLimits() Limits {
	return Limits{FirstOutputTimeout: 3 * time.Minute, IdleTimeout: 15 * time.Minute, ToolTimeout: 2 * time.Hour,
		IdleProcessTTL: 10 * time.Minute}
}

type PrincipalKind int

const (
	PrincipalClient PrincipalKind = iota + 1
	PrincipalOwner
)

// Principal is the verified caller of an RPC.
type Principal struct {
	Kind     PrincipalKind
	ClientID string
	Grant    *identityv1.Grant
}

func (p *Principal) ID() string {
	if p.Kind == PrincipalOwner {
		return "owner"
	}
	return p.ClientID
}

// Sessions is the session kernel of a machine.
type Sessions struct {
	// changeLog numbers its sessions' and projects' changes (feed.go).
	changesOnce sync.Once
	changeLog   *changeLog

	st        *Store
	ws        *Workspace
	audit     *Audit
	log       *slog.Logger
	machineID string
	dataDir   string
	providers map[string]provider.Provider
	info      func(name string) provider.Info
	limits    Limits
	// janitorEvery is how often the Janitor checks (default 10s).
	janitorEvery time.Duration
	paused       func() bool
	// defaults is what an agent's sessions run with when the client names
	// no model.
	defaults func(agent string) provider.Settings

	mu         sync.Mutex
	live       map[string]*live
	dispatchMu sync.Mutex
	w          *writer
	// counts tracks live sessions per status so hot paths never scan.
	counts [10]atomic.Int64
	// watchers are the WatchSessions streams.
	watchMu  sync.Mutex
	watchers map[*watcher]bool
	// notice tells the owner's devices of a question, a failure or a
	// finished turn (notify.go); nil in tests.
	notice func(noticeSession, *sessionv1.SessionEvent)
}

func (s *Sessions) count(st sessionv1.SessionStatus, d int64) {
	if int(st) < len(s.counts) && !isTerminal(st) {
		s.counts[st].Add(d)
	}
}

type live struct {
	id            string
	stop          *provider.Failure // an interrupt that came while preparing
	emitMu        sync.Mutex        // orders emits of one session into the writer
	persistedSeq  uint64
	statRunning   bool
	statPending   bool
	repo          string
	lastCommit    string
	mayHaveEdits  bool // a tool that can change files ran this turn
	mu            sync.Mutex
	row           *sessionRow
	sess          provider.Session
	seq           uint64
	turn          uint32
	subs          map[chan *sessionv1.SessionEvent]bool // true: it takes drafts
	lastEvent     time.Time
	lastProgress  time.Time // last event that was not an error
	turnStarted   time.Time
	procStarted   time.Time
	idleSince     time.Time
	pending       message // the prompt of the turn to start
	resume        bool
	queuedAt      time.Time
	awaitingFirst bool
	tools         map[string]string
	// The current turn: its prompt, tool calls so far, whether its process
	// was started with a resume, and whether it is being finished (by its
	// result, a crash or the watchdog; only one of them finishes it).
	turnMsg   message
	turnTools int
	// followUps are messages sent while the turn could not take them
	// (starting, finishing, or an agent without mid-turn input); they
	// become the next turn when this one succeeds.
	followUps []message
	// questions the agent waits on, oldest first.
	questions    []*question
	questionSeq  int
	resumedStart bool
	finishing    bool
	// restarting re-runs the current turn in a fresh provider session
	// (the resume was refused before the turn did anything).
	restarting bool
	// lostContext says why the provider conversation was dropped; the next
	// fresh provider session is told (see continuityNotice).
	lostContext string
	// lastErr is the agent's last error this turn (e.g. a retry it keeps
	// making), and when: a stall after it is reported as that failure.
	lastErr   *provider.Failure
	lastErrAt time.Time
	// draft is the reply the agent is still writing (docs/reply-streaming.md):
	// draftQueued bytes of it are queued for subscribers (0: the next send
	// is the whole draft), draftTimer is the pending send, and
	// draftShown what subscribers have been sent of it.
	draft       strings.Builder
	draftQueued int
	draftTimer  *time.Timer
	draftShown  string
}

// continuityNotice tells an agent starting a fresh provider session in an
// existing Hermesh session that the earlier conversation is gone.
func continuityNotice(reason string, inPlace bool) string {
	if reason == "" {
		reason = "the agent's session ended"
	}
	r, _ := provider.Truncate(provider.Redact(reason), 300)
	work := "Work from previous turns is still in this worktree, committed once per turn; check `git log` and `git diff` to see what was done, "
	if inPlace {
		work = "Work from previous turns is still in this directory; look at its files (and `git status` if it is a git repository) to see what was done, "
	}
	return "[Hermesh] The earlier conversation in this session could not be restored (" + strings.TrimSpace(r) + "). " + work +
		"then continue with the request below.\n\n"
}

// isTerminal: an archived session takes no more messages until it is
// unarchived. Nothing else ends a session.
func isTerminal(s sessionv1.SessionStatus) bool {
	return s == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
}

func cerr(code connect.Code, format string, a ...any) error {
	return connect.NewError(code, fmt.Errorf(format, a...))
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Recover loads the sessions that are not archived after a restart. Running
// processes from the previous daemon are killed and those sessions marked
// crashed.
func (s *Sessions) Recover() error {
	rows, err := s.st.Sessions(`status != ?`, int(sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED))
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Pgid > 0 { // the previous daemon's agent and everything it started
			provider.KillTree(r.Pgid)
			r.Pgid = 0
		}
		seq, _ := s.st.MaxSeq(r.ID)
		s.count(r.Status, 1)
		l := &live{id: r.ID, row: r, seq: seq, persistedSeq: seq, turn: r.Progress.GetTurnCount(), subs: map[chan *sessionv1.SessionEvent]bool{}, tools: map[string]string{}, idleSince: time.Now()}
		s.mu.Lock()
		s.live[r.ID] = l
		s.mu.Unlock()
		if r.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE && r.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			r.Progress.PendingQuestion = nil // it went with the agent process
			if turnActive(r.Status) || r.Status == sessionv1.SessionStatus_SESSION_STATUS_PREPARING {
				r.Progress.LastFailure = (&provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, Message: "hermeshd restarted during the turn"}).Proto()
			}
			s.setStatus(l, sessionv1.SessionStatus_SESSION_STATUS_CRASHED, "hermeshd restarted")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Authorization helpers.

func (s *Sessions) canRead(p *Principal, r *sessionRow) bool {
	if p.Kind == PrincipalOwner || r.CreatedBy == p.ClientID {
		return true
	}
	g := p.Grant
	return g.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_ALL && identity.Allows(g.ProjectIds, r.ProjectID)
}

func (s *Sessions) canWrite(p *Principal, r *sessionRow) bool {
	return p.Kind == PrincipalClient && r.CreatedBy == p.ClientID
}

func (s *Sessions) find(p *Principal, id string, write bool) (*live, *sessionRow, error) {
	s.mu.Lock()
	l := s.live[id]
	s.mu.Unlock()
	var r *sessionRow
	if l != nil {
		l.mu.Lock()
		r = l.row
		l.mu.Unlock()
	} else {
		var err error
		if r, err = s.st.Session(id); err != nil {
			return nil, nil, err
		}
	}
	if r == nil || !s.canRead(p, r) {
		return nil, nil, cerr(connect.CodeNotFound, "session %s not found", id)
	}
	if write && !s.canWrite(p, r) {
		return nil, nil, cerr(connect.CodePermissionDenied, "only the client that started the session may change it")
	}
	return l, r, nil
}

// ---------------------------------------------------------------------------
// RPC operations.

func (s *Sessions) Start(ctx context.Context, p *Principal, req *sessionv1.StartSessionRequest) (*sessionv1.Session, error) {
	if p.Kind != PrincipalClient {
		return nil, cerr(connect.CodePermissionDenied, "only clients may start sessions")
	}
	g := p.Grant
	if req.IdempotencyKey != "" {
		if id, err := s.st.Idempotent(p.ClientID, req.IdempotencyKey); err != nil {
			return nil, err
		} else if id != "" {
			_, r, err := s.find(p, id, false)
			if err != nil {
				return nil, err
			}
			return s.toProto(r), nil
		}
	}
	if s.paused() {
		return nil, cerr(connect.CodeUnavailable, "this machine is paused by its owner")
	}
	if !identity.Allows(g.ProjectIds, req.ProjectId) {
		return nil, cerr(connect.CodePermissionDenied, "project %q is not in this client's grant", req.ProjectId)
	}
	if !identity.Allows(g.Agents, req.Agent) {
		return nil, cerr(connect.CodePermissionDenied, "agent %q is not in this client's grant", req.Agent)
	}
	prov := s.providers[req.Agent]
	if prov == nil {
		return nil, cerr(connect.CodeInvalidArgument, "unknown agent %q (one of: %s)", req.Agent, strings.Join(AgentNames(), ", "))
	}
	proj, err := s.st.Project(req.ProjectId)
	if err != nil {
		return nil, err
	}
	if proj == nil {
		return nil, cerr(connect.CodeNotFound, "project %q does not exist on this machine", req.ProjectId)
	}
	if !proj.AllowsAgent(req.Agent) {
		return nil, cerr(connect.CodePermissionDenied, "project %q is not set up for agent %q (it is for: %s)", req.ProjectId, req.Agent, strings.Join(proj.Agents, ", "))
	}
	if proj.InPlace() && req.BaseRef != "" {
		return nil, cerr(connect.CodeInvalidArgument, "project %q works in place (no worktree), so it has no base ref", req.ProjectId)
	}
	info := s.info(req.Agent)
	if !info.Available {
		return nil, cerr(connect.CodeFailedPrecondition, "%s is not installed on this machine: %s", req.Agent, info.Detail)
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0 {
		return nil, cerr(connect.CodeInvalidArgument, "prompt is empty")
	}
	if err := checkAttachments(req.Prompt, req.Attachments); err != nil {
		return nil, err
	}
	if max := g.MaxConcurrentSessions; max > 0 && s.activeFor(p.ClientID) >= int(max) {
		return nil, cerr(connect.CodeResourceExhausted, "this client already has %d active sessions", max)
	}
	id := "s_" + strings.TrimPrefix(s.machineID, "m_")[:8] + "_" + strings.ToLower(ulid.Make().String())
	prompt, err := newMessage(s.attachmentsDir(id), req.Prompt, req.Attachments)
	if err != nil {
		return nil, fmt.Errorf("saving attachments: %w", err)
	}
	now := time.Now()
	title := provider.CleanTitle(req.Title)
	if title == "" {
		title = provider.CleanTitle(req.Prompt)
	}
	// The client's model, else the machine's defaults; what neither sets
	// follows the agent's own configuration.
	set := s.defaults(req.Agent)
	if req.Model != "" {
		set.Model = req.Model
	}
	r := &sessionRow{ID: id, ProjectID: req.ProjectId, Agent: req.Agent,
		Status: sessionv1.SessionStatus_SESSION_STATUS_QUEUED, CreatedBy: p.ClientID, GrantID: g.GrantId, Model: set.Model, Effort: set.Effort, Speed: set.Speed,
		BaseCommit: req.BaseRef, Workspace: proj.Workspace, QuestionTimeout: -1, Title: title, Progress: &sessionv1.SessionProgress{Phase: "queued"}, CreatedAt: now, UpdatedAt: now}
	if req.QuestionTimeoutSeconds != nil {
		r.QuestionTimeout = int(*req.QuestionTimeoutSeconds)
	}
	s.count(r.Status, 1)
	l := &live{id: id, row: r, subs: map[chan *sessionv1.SessionEvent]bool{}, tools: map[string]string{}, pending: prompt, lastEvent: now, queuedAt: now}
	s.mu.Lock()
	s.live[id] = l
	s.mu.Unlock()
	s.audit.Log(AuditRecord{Kind: "session.start", Subject: p.ID(), Session: id, Action: req.Agent + " " + req.ProjectId, Decision: "allow",
		Detail: fmt.Sprintf("prompt_sha=%s%s", sha(req.Prompt), filesDetail(prompt.files))})
	// The QUEUED status event carries the row; wait for it (and the
	// idempotency key) to commit so a started session survives a crash.
	// Not r.Status: once in s.live, dispatch may already be moving it on.
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Status{Status: &sessionv1.StatusChanged{Status: sessionv1.SessionStatus_SESSION_STATUS_QUEUED}}})
	var idem *idemKey
	if req.IdempotencyKey != "" {
		idem = &idemKey{p.ClientID, req.IdempotencyKey, id}
	}
	commit := time.Now()
	err = s.w.sync(writeOp{idem: idem})
	mStartCommit.Since(commit)
	if err != nil { // never stored: forget it
		s.mu.Lock()
		delete(s.live, id)
		s.mu.Unlock()
		l.mu.Lock()
		st := l.row.Status
		l.mu.Unlock()
		s.count(st, -1)
		return nil, err
	}
	s.dispatch()
	l.mu.Lock()
	defer l.mu.Unlock()
	return s.toProto(l.row), nil
}

func (s *Sessions) activeFor(clientID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range s.live {
		l.mu.Lock()
		st := l.row.Status
		if l.row.CreatedBy == clientID && (st == sessionv1.SessionStatus_SESSION_STATUS_QUEUED ||
			st == sessionv1.SessionStatus_SESSION_STATUS_PREPARING || turnActive(st)) {
			n++
		}
		l.mu.Unlock()
	}
	return n
}

// Counts returns live sessions by status ("running", "idle", ...).
func (s *Sessions) Counts() map[string]uint32 {
	out := map[string]uint32{}
	for i := range s.counts {
		if n := s.counts[i].Load(); n > 0 {
			out[strings.ToLower(strings.TrimPrefix(sessionv1.SessionStatus(i).String(), "SESSION_STATUS_"))] = uint32(n)
		}
	}
	return out
}

// Running counts sessions that hold a slot.
func (s *Sessions) Running() int {
	return int(s.counts[sessionv1.SessionStatus_SESSION_STATUS_PREPARING].Load() + s.counts[sessionv1.SessionStatus_SESSION_STATUS_RUNNING].Load() +
		s.counts[sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT].Load())
}

func (s *Sessions) Get(p *Principal, id string) (*sessionv1.Session, error) {
	l, r, err := s.find(p, id, false)
	if err != nil {
		return nil, err
	}
	if l != nil { // r is the live row, changed by the session's goroutines
		l.mu.Lock()
		defer l.mu.Unlock()
	}
	return s.toProto(r), nil
}

// List returns the sessions p can read: all of them, or one page (with
// where the next starts) when req has a limit; in req's view.
func (s *Sessions) List(p *Principal, req *sessionv1.ListSessionsRequest) ([]*sessionv1.Session, *sessionv1.SessionCursor, error) {
	list, next, err := s.list(p, req)
	if req.View == sessionv1.SessionView_SESSION_VIEW_BASIC {
		list = BasicSessions(list)
	}
	return list, next, err
}

func (s *Sessions) list(p *Principal, req *sessionv1.ListSessionsRequest) ([]*sessionv1.Session, *sessionv1.SessionCursor, error) {
	if req.Limit > 0 {
		return s.listPage(p, req)
	}
	rows, err := s.st.Sessions("")
	if err != nil {
		return nil, nil, err
	}
	var out []*sessionv1.Session
	for _, r := range rows {
		s.mu.Lock()
		l := s.live[r.ID]
		s.mu.Unlock()
		if l != nil {
			l.mu.Lock()
			r = l.row
			l.mu.Unlock()
		}
		if !s.canRead(p, r) {
			continue
		}
		if req.Status != 0 && r.Status != req.Status {
			continue
		}
		if req.Since != nil && r.UpdatedAt.Before(req.Since.AsTime()) {
			continue
		}
		out = append(out, s.toProto(r))
	}
	return out, nil, nil
}

// ActiveByProject counts the sessions with a turn in progress (queued,
// preparing, running, or waiting for an answer) in each project.
func (s *Sessions) ActiveByProject() map[string]uint32 {
	s.mu.Lock()
	ls := make([]*live, 0, len(s.live))
	for _, l := range s.live {
		ls = append(ls, l)
	}
	s.mu.Unlock()
	out := map[string]uint32{}
	for _, l := range ls {
		l.mu.Lock()
		status, project := l.row.Status, l.row.ProjectID
		l.mu.Unlock()
		switch status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED, sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			out[project]++
		}
	}
	return out
}

func (s *Sessions) Send(ctx context.Context, p *Principal, id, text string, atts []*sessionv1.Attachment) (*sessionv1.Session, error) {
	l, _, err := s.find(p, id, true)
	if err != nil {
		return nil, err
	}
	if err := checkAttachments(text, atts); err != nil {
		return nil, err
	}
	if l == nil {
		return nil, cerr(connect.CodeFailedPrecondition, "session %s is archived (read-only); unarchive it to send messages", id)
	}
	if s.paused() {
		return nil, cerr(connect.CodeUnavailable, "this machine is paused by its owner")
	}
	m, err := newMessage(s.attachmentsDir(id), text, atts)
	if err != nil {
		return nil, fmt.Errorf("saving attachments: %w", err)
	}
	if err := s.take(ctx, l, m, p.ID()); err != nil {
		return nil, err
	}
	s.audit.Log(AuditRecord{Kind: "session.send", Subject: p.ID(), Session: id, Decision: "allow", Detail: "message_sha=" + sha(text) + filesDetail(m.files)})
	l.mu.Lock()
	defer l.mu.Unlock()
	return s.toProto(l.row), nil
}

// take delivers a message by the session's state: a new turn when idle,
// into the running turn when the agent takes input mid-turn, otherwise
// as the turn after the current one.
func (s *Sessions) take(ctx context.Context, l *live, m message, by string) error {
	for {
		l.mu.Lock()
		switch l.row.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT: // the answer to the agent's question
			l.mu.Unlock()
			if s.answerWithText(l, by, m.text) {
				return nil
			}
			continue // answered meanwhile
		case sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
			return s.nextTurn(ctx, l, m)
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED: // not started: one prompt
			l.pending = l.pending.join(m)
			l.mu.Unlock()
			return nil
		case sessionv1.SessionStatus_SESSION_STATUS_RUNNING:
			ps := l.sess
			if ps == nil || l.finishing {
				break
			}
			l.mu.Unlock()
			if ps.Steer(ctx, m.input()) == nil {
				l.mu.Lock()
				l.turnMsg = l.turnMsg.join(m) // a restarted turn includes it
				l.mu.Unlock()
				s.emit(l, m.userEvent())
				return nil
			}
			l.mu.Lock()
			if st := l.row.Status; st != sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
				l.mu.Unlock()
				continue // the turn ended meanwhile
			}
		case sessionv1.SessionStatus_SESSION_STATUS_PREPARING:
		default:
			l.mu.Unlock()
			return cerr(connect.CodeFailedPrecondition, "session %s is archived (read-only); unarchive it to send messages", l.id)
		}
		l.followUps = append(l.followUps, m)
		l.mu.Unlock()
		return nil
	}
}

// nextTurn starts a turn with m, in the running agent process or a new
// one. It is called with l.mu held and releases it.
func (s *Sessions) nextTurn(ctx context.Context, l *live, m message) error {
	ps := l.sess
	alive := ps != nil
	if alive {
		select {
		case <-ps.Done():
			alive = false
		default:
		}
	}
	if alive && l.row.ProviderRef == "" && l.turn > 0 {
		// The provider conversation was dropped (refused resume, context
		// overflow): continue in a fresh process.
		alive = false
		l.sess = nil
		go ps.Close()
	}
	if !alive {
		l.pending, l.resume, l.queuedAt = m, l.row.ProviderRef != "", time.Now()
		l.mu.Unlock()
		s.setStatus(l, sessionv1.SessionStatus_SESSION_STATUS_QUEUED, "")
		s.dispatch()
		return nil
	}
	l.turn++
	l.turnStarted, l.lastEvent, l.awaitingFirst = time.Now(), time.Now(), true
	l.row.Progress.TurnStartedAt = timestamppb.Now()
	l.lastProgress, l.lastErr = time.Now(), nil
	l.turnMsg, l.turnTools, l.resumedStart, l.finishing = m, 0, false, false
	turn := l.turn
	// Running before the agent has the message: a fast agent may finish
	// the turn, and the session go idle, before Send returns.
	changed := s.setStatusLocked(l, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)
	l.mu.Unlock()
	if changed {
		s.statusChanged(l, sessionv1.SessionStatus_SESSION_STATUS_RUNNING, "")
	}
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_TurnStarted{TurnStarted: &sessionv1.TurnStarted{Turn: turn}}})
	s.emit(l, m.userEvent())
	if err := ps.Send(ctx, m.input()); err != nil {
		s.interruptTurn(l, ps, &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, Message: "could not send the message: " + err.Error()})
		return err
	}
	return nil
}

// Interrupt stops the turn in progress (or about to start). The session
// stays, idle, and the next message goes on in the same conversation.
func (s *Sessions) Interrupt(ctx context.Context, p *Principal, id string) (*sessionv1.Session, error) {
	l, _, err := s.find(p, id, p.Kind != PrincipalOwner)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, cerr(connect.CodeFailedPrecondition, "session %s is archived", id)
	}
	s.interrupt(l, "interrupted by "+p.ID())
	s.audit.Log(AuditRecord{Kind: "session.interrupt", Subject: p.ID(), Session: id, Decision: "allow"})
	l.mu.Lock()
	defer l.mu.Unlock()
	return s.toProto(l.row), nil
}

func (s *Sessions) interrupt(l *live, by string) {
	f := &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED, Message: by}
	l.mu.Lock()
	switch l.row.Status {
	case sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
		l.mu.Unlock()
		s.stopTurn(l, nil, f, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	case sessionv1.SessionStatus_SESSION_STATUS_QUEUED: // not started: it never runs
		l.pending, l.followUps, l.resume, l.restarting = message{}, nil, false, false
		l.mu.Unlock()
		s.settle(l, f, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	case sessionv1.SessionStatus_SESSION_STATUS_PREPARING: // prepare stops it once the agent is up
		l.stop = f
		l.mu.Unlock()
	default:
		l.mu.Unlock()
	}
}

// InterruptByClient stops the turns of a revoked agent; its sessions stay
// for the owner.
func (s *Sessions) InterruptByClient(clientID, reason string) {
	s.mu.Lock()
	var ls []*live
	for _, l := range s.live {
		l.mu.Lock()
		if l.row.CreatedBy == clientID {
			ls = append(ls, l)
		}
		l.mu.Unlock()
	}
	s.mu.Unlock()
	for _, l := range ls {
		s.interrupt(l, reason)
	}
}

// Archive makes an idle session read-only and frees its worktree; the
// session's branch keeps every turn's work, and its events and diff stay
// readable.
func (s *Sessions) Archive(ctx context.Context, p *Principal, id string) (*sessionv1.Session, error) {
	l, r, err := s.find(p, id, p.Kind != PrincipalOwner)
	if err != nil {
		return nil, err
	}
	if l == nil { // already archived
		return s.toProto(r), nil
	}
	l.mu.Lock()
	if st := l.row.Status; st != sessionv1.SessionStatus_SESSION_STATUS_IDLE && st != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
		l.mu.Unlock()
		return nil, cerr(connect.CodeFailedPrecondition, "session %s has a turn in progress (%s); interrupt it first", id, phaseOf(st))
	}
	ps := l.sess
	l.sess = nil
	inPlace := l.row.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE
	changed := s.setStatusLocked(l, sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED)
	row := snapshotRow(l.row)
	l.mu.Unlock()
	if ps != nil {
		ps.Close()
	}
	if !inPlace && s.ws.Exists(id) {
		if proj, _ := s.st.Project(row.ProjectID); proj != nil {
			if err := s.ws.Remove(ctx, proj.Path, id); err != nil {
				s.log.Warn("archive: worktree not removed", "session", id, "err", err)
			}
		} else {
			os.RemoveAll(s.ws.Path(id))
		}
	}
	if changed {
		s.statusChanged(l, sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED, "archived by "+p.ID())
	}
	// Retire it now, and wait until the archive is stored, so an unarchive
	// that follows at once sees an archived session and numbers its events
	// after this one (delivery would retire it only later).
	s.mu.Lock()
	if s.live[id] == l {
		delete(s.live, id)
	}
	s.mu.Unlock()
	if err := s.w.sync(writeOp{}); err != nil {
		return nil, err
	}
	s.audit.Log(AuditRecord{Kind: "session.archive", Subject: p.ID(), Session: id, Decision: "allow"})
	return s.toProto(row), nil
}

// Unarchive checks the session's branch out again in its worktree and
// makes the session idle; the next message resumes its conversation.
func (s *Sessions) Unarchive(ctx context.Context, p *Principal, id string) (*sessionv1.Session, error) {
	l, r, err := s.find(p, id, p.Kind != PrincipalOwner)
	if err != nil {
		return nil, err
	}
	if l != nil { // not archived
		l.mu.Lock()
		defer l.mu.Unlock()
		return s.toProto(l.row), nil
	}
	if r.Workspace != identityv1.Workspace_WORKSPACE_IN_PLACE && !s.ws.Exists(id) {
		proj, err := s.st.Project(r.ProjectID)
		if err != nil || proj == nil {
			return nil, cerr(connect.CodeFailedPrecondition, "project %q no longer exists", r.ProjectID)
		}
		if err := s.ws.Restore(ctx, proj.Path, id); err != nil {
			return nil, fmt.Errorf("restore the worktree: %w", err)
		}
	}
	seq, _ := s.st.MaxSeq(id)
	r.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
	r.Progress.Phase = phaseOf(r.Status)
	l = &live{id: id, row: r, seq: seq, persistedSeq: seq, turn: r.Progress.GetTurnCount(), subs: map[chan *sessionv1.SessionEvent]bool{},
		tools: map[string]string{}, idleSince: time.Now()}
	s.mu.Lock()
	if s.live[id] != nil { // unarchived meanwhile
		s.mu.Unlock()
		return s.Get(p, id)
	}
	s.live[id] = l
	s.mu.Unlock()
	s.count(r.Status, 1)
	s.statusChanged(l, r.Status, "unarchived by "+p.ID())
	s.audit.Log(AuditRecord{Kind: "session.unarchive", Subject: p.ID(), Session: id, Decision: "allow"})
	l.mu.Lock()
	defer l.mu.Unlock()
	return s.toProto(l.row), nil
}

// deleteWait bounds how long Delete waits for an interrupted turn to stop.
const deleteWait = 15 * time.Second

// Delete deletes a session for good (the owner only). A turn in progress is
// interrupted; the session is then archived, which frees its worktree (its
// branch keeps every turn's work), and its record, events, turns and
// attachments go. Its tokens stay in Usage.
func (s *Sessions) Delete(ctx context.Context, p *Principal, id string) error {
	if p.Kind != PrincipalOwner {
		return cerr(connect.CodePermissionDenied, "only the owner may delete a session")
	}
	if _, _, err := s.find(p, id, false); err != nil {
		return err
	}
	// Archiving needs the session settled: interrupt it until it is (a
	// stopped turn may hand over to a queued follow-up).
	deadline := time.Now().Add(deleteWait)
	for {
		_, err := s.Archive(ctx, p, id)
		if err == nil {
			break
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || time.Now().After(deadline) {
			return err
		}
		s.mu.Lock()
		l := s.live[id]
		s.mu.Unlock()
		if l != nil {
			s.interrupt(l, "deleted by "+p.ID())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	r, err := s.st.Session(id)
	if err != nil || r == nil {
		return err
	}
	if err := s.st.DeleteSession(r); err != nil {
		return err
	}
	os.RemoveAll(filepath.Dir(s.attachmentsDir(id)))
	s.audit.Log(AuditRecord{Kind: "session.delete", Subject: p.ID(), Session: id, Decision: "allow"})
	s.changed(id)
	return nil
}

// Rename changes a session's title (the owner only). It is not activity:
// the session keeps its place in lists.
func (s *Sessions) Rename(ctx context.Context, p *Principal, id, title string) (*sessionv1.Session, error) {
	if p.Kind != PrincipalOwner {
		return nil, cerr(connect.CodePermissionDenied, "only the owner may rename a session")
	}
	title = provider.CleanTitle(title)
	if title == "" {
		return nil, cerr(connect.CodeInvalidArgument, "a session's title can't be empty")
	}
	l, r, err := s.find(p, id, false)
	if err != nil {
		return nil, err
	}
	var row *sessionRow
	if l != nil {
		l.mu.Lock()
		l.row.Title = title
		c := *l.row
		c.Progress = proto.Clone(l.row.Progress).(*sessionv1.SessionProgress)
		row = &c
		l.mu.Unlock()
	} else {
		r.Title = title
		row = r
	}
	if err := s.w.sync(writeOp{row: row}); err != nil {
		return nil, err
	}
	s.audit.Log(AuditRecord{Kind: "session.rename", Subject: p.ID(), Session: id, Decision: "allow"})
	return s.toProto(row), nil
}

// Stream replays stored events after req.AfterSeq and then, if requested,
// follows live events until the session ends or ctx is done.
func (s *Sessions) Stream(ctx context.Context, p *Principal, req *sessionv1.StreamEventsRequest, send func(*sessionv1.SessionEvent) error) error {
	if req.View == sessionv1.EventView_EVENT_VIEW_BASIC {
		full := send
		send = func(e *sessionv1.SessionEvent) error { return full(BasicEvent(e)) }
	}
	l, _, err := s.find(p, req.SessionId, false)
	if err != nil {
		return err
	}
	var ch chan *sessionv1.SessionEvent
	var upTo uint64
	var draft string
	if req.Follow && l != nil {
		ch = make(chan *sessionv1.SessionEvent, 256)
		l.mu.Lock()
		l.subs[ch] = req.Partial
		upTo = l.persistedSeq
		if req.Partial {
			draft = l.draftShown
		}
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			delete(l.subs, ch)
			l.mu.Unlock()
		}()
	}
	last := req.AfterSeq
	for {
		evs, err := s.st.Events(req.SessionId, last, 500)
		if err != nil {
			return err
		}
		for _, e := range evs {
			if ch != nil && e.Seq > upTo {
				break
			}
			if err := send(e); err != nil {
				return err
			}
			last = e.Seq
		}
		if len(evs) < 500 || (ch != nil && last >= upTo) {
			break
		}
	}
	if ch == nil {
		return nil
	}
	// A stream that starts mid-reply gets the draft so far, then its pieces.
	if draft != "" {
		if err := send(&sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_AgentDraft{AgentDraft: &sessionv1.AgentDraft{Text: draft, Whole: true}}}); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return cerr(connect.CodeResourceExhausted, "event stream fell behind; resume with after_seq=%d", last)
			}
			if e.GetAgentDraft() != nil {
				if err := send(e); err != nil {
					return err
				}
				continue
			}
			if e.Seq <= last {
				continue
			}
			if err := send(e); err != nil {
				return err
			}
			last = e.Seq
			if st := e.GetStatus(); st != nil && isTerminal(st.Status) {
				return nil
			}
		}
	}
}

// ListEvents returns one page of a session's stored events, oldest first.
func (s *Sessions) ListEvents(p *Principal, req *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
	if _, _, err := s.find(p, req.SessionId, false); err != nil {
		return nil, err
	}
	if req.AfterSeq > 0 && req.BeforeSeq > 0 {
		return nil, cerr(connect.CodeInvalidArgument, "give after_seq or before_seq, not both")
	}
	limit := int(req.Limit)
	switch {
	case limit == 0:
		limit = 50
	case limit > 1000:
		return nil, cerr(connect.CodeInvalidArgument, "at most 1000 events per page")
	}
	evs, before, after, err := s.st.EventPage(req.SessionId, req.AfterSeq, req.BeforeSeq, limit)
	if err != nil {
		return nil, err
	}
	if req.View == sessionv1.EventView_EVENT_VIEW_BASIC {
		for i, e := range evs {
			evs[i] = BasicEvent(e)
		}
	}
	return &sessionv1.ListEventsResponse{Events: evs, HasMoreBefore: before, HasMoreAfter: after}, nil
}

const maxDiff = 10 << 20

func (s *Sessions) Diff(ctx context.Context, p *Principal, req *sessionv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	_, r, err := s.find(p, req.SessionId, false)
	if err != nil {
		return nil, err
	}
	if r.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE {
		return nil, cerr(connect.CodeFailedPrecondition, "session %s works in place in its project directory, so it has no diff of its own; "+
			"the changes are in the directory (ask the agent, e.g. for `git status`)", r.ID)
	}
	proj, err := s.st.Project(r.ProjectID)
	if err != nil || proj == nil {
		return nil, cerr(connect.CodeFailedPrecondition, "project %q no longer exists", r.ProjectID)
	}
	if r.BaseCommit == "" || len(r.BaseCommit) != 40 {
		return &sessionv1.GetDiffResponse{}, nil
	}
	from, to := r.BaseCommit, r.BaseCommit
	turns := r.Progress.GetTurnCount()
	if req.Turn > 0 {
		if req.Turn > turns {
			return nil, cerr(connect.CodeInvalidArgument, "session has %d turns", turns)
		}
		if to, err = s.st.TurnCommit(r.ID, req.Turn); err != nil || to == "" {
			return nil, cerr(connect.CodeNotFound, "turn %d has no snapshot", req.Turn)
		}
		if req.Turn > 1 {
			if prev, _ := s.st.TurnCommit(r.ID, req.Turn-1); prev != "" {
				from = prev
			}
		}
	} else {
		for t := turns; t >= 1; t-- {
			if c, _ := s.st.TurnCommit(r.ID, t); c != "" {
				to = c
				break
			}
		}
	}
	stat, err := diffStat(ctx, proj.Path, from, to)
	if err != nil {
		return nil, err
	}
	resp := &sessionv1.GetDiffResponse{Stat: stat, BaseCommit: from, HeadCommit: to}
	if req.StatOnly {
		return resp, nil
	}
	d, err := git(ctx, proj.Path, "diff", "--binary", from, to)
	if err != nil {
		return nil, err
	}
	if req.Offset > uint64(len(d)) {
		req.Offset = uint64(len(d))
	}
	d = d[req.Offset:]
	if len(d) > maxDiff {
		d, resp.Truncated, resp.NextOffset = d[:maxDiff], true, req.Offset+maxDiff
	}
	resp.Diff = d
	return resp, nil
}

// ---------------------------------------------------------------------------
// Lifecycle.

// dispatch moves queued sessions to PREPARING while slots are free. It is
// serialized so a session is never claimed twice.
func (s *Sessions) dispatch() {
	if s.counts[sessionv1.SessionStatus_SESSION_STATUS_QUEUED].Load() == 0 {
		return
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	var queued []*live
	running := 0
	for _, l := range s.live {
		l.mu.Lock()
		switch l.row.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED:
			queued = append(queued, l)
		case sessionv1.SessionStatus_SESSION_STATUS_PREPARING, sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			running++
		}
		l.mu.Unlock()
	}
	s.mu.Unlock()
	limited := s.limits.MaxConcurrent > 0
	if len(queued) == 0 || limited && running >= s.limits.MaxConcurrent {
		return
	}
	slices.SortFunc(queued, func(a, b *live) int { return a.queuedAt.Compare(b.queuedAt) })
	for _, l := range queued {
		if limited && running >= s.limits.MaxConcurrent {
			return
		}
		l.mu.Lock()
		if l.row.Status != sessionv1.SessionStatus_SESSION_STATUS_QUEUED {
			l.mu.Unlock()
			continue
		}
		mQueueWait.Since(l.queuedAt)
		l.mu.Unlock()
		s.setStatus(l, sessionv1.SessionStatus_SESSION_STATUS_PREPARING, "")
		running++
		go s.prepare(l)
	}
}

func (s *Sessions) prepare(l *live) {
	ctx := context.Background()
	l.mu.Lock()
	id, projectID, prompt, resume, restarting := l.row.ID, l.row.ProjectID, l.pending, l.resume, l.restarting
	needWorktree := l.row.Worktree == "" || !s.ws.Exists(id)
	inPlace := l.row.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE
	l.mu.Unlock()
	fail := func(err error) {
		s.log.Warn("session failed to start", "session", id, "err", err)
		s.interruptStart(l, provider.FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED))
	}
	proj, err := s.st.Project(projectID)
	if err != nil || proj == nil {
		fail(fmt.Errorf("project %q no longer exists", projectID))
		return
	}
	l.mu.Lock()
	l.repo = proj.Path
	if inPlace {
		// The project directory itself, as it was when the session began.
		needWorktree = false
		if l.row.Worktree == "" {
			l.row.Worktree = proj.Path
		}
	}
	dir := l.row.Worktree
	l.mu.Unlock()
	if !needWorktree {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			fail(fmt.Errorf("the session's directory %s no longer exists", dir))
			return
		}
	}
	if needWorktree {
		l.mu.Lock()
		baseRef := l.row.BaseCommit
		l.mu.Unlock()
		if baseRef == "" {
			baseRef = proj.DefaultBaseRef
		}
		wtStart := time.Now()
		base, branch, err := s.ws.Create(ctx, proj.Path, baseRef, id)
		mWorktree.Since(wtStart)
		if err != nil {
			fail(err)
			return
		}
		l.mu.Lock()
		l.row.BaseCommit, l.row.Branch, l.row.Worktree = base, branch, s.ws.Path(id)
		l.mu.Unlock()
	}
	tmp := filepath.Join(s.dataDir, "sessions", id, "tmp")
	os.MkdirAll(tmp, 0o700)
	files := s.attachmentsDir(id) // also for files sent later in the session
	os.MkdirAll(files, 0o700)
	gate := s.gate(l)
	l.mu.Lock()
	spec := provider.StartSpec{WorkDir: l.row.Worktree, TempDir: tmp, Prompt: prompt.text, Images: prompt.input().Images, ReadDirs: []string{files}, Model: l.row.Model,
		Effort: l.row.Effort, Speed: l.row.Speed, Env: shellenv.Env(tmp), Gate: gate}
	if resume && l.row.ProviderRef != "" {
		spec.ResumeRef = l.row.ProviderRef
	} else {
		spec.NewRef = newUUID()
		if l.turn > 0 { // an existing session in a fresh provider session
			spec.Prompt = continuityNotice(l.lostContext, inPlace) + prompt.text
		}
	}
	agent := l.row.Agent
	l.mu.Unlock()
	startAt := time.Now()
	ps, err := s.providers[agent].Start(ctx, spec)
	mProviderStart.Since(startAt)
	if err != nil && spec.ResumeRef != "" {
		f := provider.FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED)
		if f.Kind != sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED {
			// Not evidence that the conversation is gone (login, network,
			// ...): keep it for a later retry.
			s.log.Warn("resume failed; session kept", "session", id, "err", err)
			countResume(agent, "failed_kept")
			s.interruptStart(l, f)
			return
		}
		s.log.Warn("resume refused, continuing in a fresh provider session", "session", id, "err", err)
		countResume(agent, "rejected_fresh")
		s.dropContext(l, nil, f, "the agent's session could not be resumed; continuing in a fresh one (the agent is told the earlier conversation is lost)")
		spec.ResumeRef, spec.NewRef = "", newUUID()
		spec.Prompt = continuityNotice(f.Message, inPlace) + prompt.text
		ps, err = s.providers[agent].Start(ctx, spec)
	} else if err == nil && spec.ResumeRef != "" {
		countResume(agent, "resumed")
	}
	if err != nil { // the session is kept; a later message retries
		fail(err)
		return
	}
	l.mu.Lock()
	if f := l.stop; f != nil { // interrupted while preparing
		l.stop = nil
		l.pending, l.followUps, l.resume, l.restarting = message{}, nil, false, false
		l.mu.Unlock()
		ps.Close()
		s.settle(l, f, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
		return
	}
	l.sess = ps
	l.row.Pgid = ps.Pgid()
	l.procStarted = time.Now()
	if !restarting {
		l.turn++
	}
	l.turnStarted, l.lastEvent, l.awaitingFirst = time.Now(), time.Now(), true
	if !restarting || l.row.Progress.TurnStartedAt == nil { // a restarted turn is the same turn
		l.row.Progress.TurnStartedAt = timestamppb.Now()
	}
	l.lastProgress, l.lastErr = time.Now(), nil
	l.turnMsg, l.turnTools, l.resumedStart, l.finishing, l.restarting = prompt, 0, spec.ResumeRef != "", false, false
	mProcs.Add(1)
	l.pending, l.resume = message{}, false
	turn := l.turn
	l.mu.Unlock()
	s.setStatus(l, sessionv1.SessionStatus_SESSION_STATUS_RUNNING, "")
	if !restarting { // a restarted turn was already announced
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_TurnStarted{TurnStarted: &sessionv1.TurnStarted{Turn: turn}}})
		s.emit(l, prompt.userEvent())
	}
	// Messages sent while the agent was starting: into this turn if it
	// takes them (before pumping, so the turn cannot have finished).
	l.mu.Lock()
	pending := l.followUps
	l.followUps = nil
	l.mu.Unlock()
	for _, m := range pending {
		if ps.Steer(ctx, m.input()) == nil {
			l.mu.Lock()
			l.turnMsg = l.turnMsg.join(m)
			l.mu.Unlock()
			s.emit(l, m.userEvent())
			continue
		}
		l.mu.Lock()
		l.followUps = append(l.followUps, m)
		l.mu.Unlock()
	}
	go s.pump(l, ps)
}

func (s *Sessions) pump(l *live, ps provider.Session) {
	for e := range ps.Events() {
		s.onEvent(l, ps, e)
	}
	<-ps.Done()
	mProcs.Add(-1)
	l.mu.Lock()
	mine := l.sess == ps
	if mine {
		l.sess = nil
		l.row.Pgid = 0
	}
	st := l.row.Status
	l.mu.Unlock()
	if mine && turnActive(st) {
		msg := "agent process exited unexpectedly"
		if err := ps.Err(); err != nil {
			msg += ": " + err.Error()
		}
		s.interruptTurn(l, ps, &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, Message: msg})
	}
	s.dispatch()
}

func (s *Sessions) onEvent(l *live, ps provider.Session, e provider.Event) {
	l.mu.Lock()
	l.lastEvent = time.Now()
	if e.Kind != provider.EventError { // an error (a retry) is not progress
		l.lastProgress = l.lastEvent
	}
	stale := l.sess != ps
	if l.awaitingFirst && e.Kind != provider.EventRef {
		l.awaitingFirst = false
		mFirstEvent.Since(l.turnStarted)
	}
	l.mu.Unlock()
	if stale {
		return
	}
	switch e.Kind {
	case provider.EventRef:
		l.mu.Lock()
		l.row.ProviderRef = e.Ref
		row := snapshotRow(l.row)
		l.mu.Unlock()
		s.w.put(writeOp{row: row})
	case provider.EventAgentDraft:
		s.addDraft(l, e.Text)
	case provider.EventAgentMessage:
		l.mu.Lock()
		l.clearDraft()
		l.mu.Unlock()
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_AgentMessage{AgentMessage: &sessionv1.AgentMessage{Text: e.Text}}})
	case provider.EventQuestion:
		if e.Question != nil {
			s.storeDraft(l)
			s.ask(l, ps, e.Question)
		}
	case provider.EventToolCall:
		s.storeDraft(l)
		tc := &sessionv1.ToolCall{CallId: e.CallID, Kind: string(e.ToolKind), Name: e.ToolName, Summary: e.Summary, Decision: "allow"}
		if e.Decision != nil && !e.Decision.Allow {
			tc.Decision, tc.Reason = "deny", e.Decision.Reason
		}
		l.mu.Lock()
		l.turnTools++
		l.tools[e.CallID] = e.ToolName + ": " + e.Summary
		switch e.ToolKind {
		case provider.ToolRead, provider.ToolSearch, provider.ToolNetwork:
		default:
			l.mayHaveEdits = true
		}
		l.mu.Unlock()
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_ToolCall{ToolCall: tc}})
	case provider.EventToolResult:
		l.mu.Lock()
		delete(l.tools, e.CallID)
		l.mu.Unlock()
		r := &sessionv1.ToolResult{CallId: e.CallID, Summary: e.Summary, IsError: e.IsError, Truncated: e.Truncated}
		var whole string
		if len(e.Summary) > inlineOutput { // kept whole beside its event, which holds its start
			whole = e.Summary
			r.Summary, _ = provider.Truncate(e.Summary, inlineOutput)
			r.Size, r.Truncated = uint64(len(e.Summary)), true
		}
		s.emitOutput(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_ToolResult{ToolResult: r}}, whole)
	case provider.EventError:
		f := e.Failure
		if f == nil {
			f = &provider.Failure{Kind: provider.Classify(e.Text), Message: e.Text}
		}
		l.mu.Lock()
		l.lastErr, l.lastErrAt = f, time.Now()
		l.mu.Unlock()
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Error{Error: &sessionv1.ErrorEvent{Message: e.Text, Failure: f.Proto()}}})
	case provider.EventTurnCompleted:
		switch e.Failure.GetKind() {
		case sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED:
			l.mu.Lock()
			retry := l.resumedStart && l.turnTools == 0
			agent := l.row.Agent
			l.mu.Unlock()
			if retry {
				countResume(agent, "rejected_fresh")
				s.restartFresh(l, ps, e.Failure)
				return
			}
			s.dropContext(l, ps, e.Failure, "")
		case sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW:
			s.dropContext(l, ps, e.Failure, "")
		}
		s.completeTurn(l, e, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	}
}

// interruptTurn ends a running turn the agent did not finish (crash,
// stall, timeout). The session stays resumable (INTERRUPTED) when its
// provider session is known; otherwise it fails.
// interruptTurn stops a turn that went wrong (stalled, crashed): the
// session is crashed, and the next message retries.
func (s *Sessions) interruptTurn(l *live, ps provider.Session, f *provider.Failure) {
	s.stopTurn(l, ps, f, sessionv1.SessionStatus_SESSION_STATUS_CRASHED)
}

// stopTurn ends the turn in progress with failure f and moves the session
// to status; the agent process goes, its conversation is resumed next time.
func (s *Sessions) stopTurn(l *live, ps provider.Session, f *provider.Failure, status sessionv1.SessionStatus) {
	l.mu.Lock()
	if !turnActive(l.row.Status) || l.finishing {
		l.mu.Unlock()
		return
	}
	l.finishing = true
	if ps == nil {
		ps = l.sess
	}
	if l.sess == ps {
		l.sess = nil
	}
	l.mu.Unlock()
	go func() {
		if ps != nil {
			ps.Close()
		}
		s.finishTurn(l, provider.Event{Kind: provider.EventTurnCompleted, IsError: true, Text: f.Message, Failure: f}, status)
	}()
}

// interruptStart reports a turn whose agent could not be started, keeping
// the session (and its provider session) for a later message.
func (s *Sessions) interruptStart(l *live, f *provider.Failure) {
	pf := f.Proto()
	l.mu.Lock()
	l.row.Error = pf.Message
	l.row.Progress.LastFailure = pf
	l.pending, l.resume, l.restarting = message{}, false, false
	l.idleSince = time.Now()
	agent := l.row.Agent
	l.mu.Unlock()
	countFailure(agent, pf.Kind)
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Error{Error: &sessionv1.ErrorEvent{Message: pf.Message, Failure: pf}}})
	s.setStatus(l, sessionv1.SessionStatus_SESSION_STATUS_CRASHED, pf.Message)
	s.dispatch()
}

// dropContext forgets a provider conversation that cannot continue; the
// next turn starts a fresh provider session and is told why.
func (s *Sessions) dropContext(l *live, ps provider.Session, f *provider.Failure, note string) {
	l.mu.Lock()
	l.row.ProviderRef = ""
	l.lostContext = f.Message
	if ps != nil && l.sess == ps {
		l.sess = nil
	}
	row := snapshotRow(l.row)
	l.mu.Unlock()
	s.w.put(writeOp{row: row})
	if ps != nil {
		go ps.Close()
	}
	if note != "" {
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Error{Error: &sessionv1.ErrorEvent{Message: note, Failure: f.Proto()}}})
	}
}

// restartFresh re-runs the current turn in a fresh provider session after
// the resume was refused before the turn did anything.
func (s *Sessions) restartFresh(l *live, ps provider.Session, f *provider.Failure) {
	s.dropContext(l, ps, f, "the agent's session could not be resumed; retrying this turn in a fresh one (the agent is told the earlier conversation is lost)")
	l.mu.Lock()
	l.clearDraft()
	l.pending, l.resume, l.restarting, l.queuedAt = l.turnMsg, false, true, time.Now()
	l.mu.Unlock()
	s.setStatus(l, sessionv1.SessionStatus_SESSION_STATUS_QUEUED, "restarting in a fresh agent session")
	s.dispatch()
}

// completeTurn finishes the turn with the agent's result.
func (s *Sessions) completeTurn(l *live, e provider.Event, status sessionv1.SessionStatus) {
	l.mu.Lock()
	if l.finishing {
		l.mu.Unlock()
		return
	}
	l.finishing = true
	l.mu.Unlock()
	s.finishTurn(l, e, status)
}

// finishTurn snapshots the worktree, records the turn and moves the
// session to status (IDLE, or INTERRUPTED when the turn was cut short).
func (s *Sessions) finishTurn(l *live, e provider.Event, status sessionv1.SessionStatus) {
	ctx := context.Background()
	s.storeDraft(l) // a reply cut short stays as far as it got
	s.dropQuestions(l)
	l.mu.Lock()
	l.resumedStart = false
	id, turn, repo := l.row.ID, l.turn, l.repo
	// In place: no snapshots, the directory and its git are the owner's.
	inPlace := l.row.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE
	edits := l.mayHaveEdits && !s.ws.none && !inPlace
	l.mayHaveEdits = false
	l.tools = map[string]string{}
	mTurn.Since(l.turnStarted)
	l.mu.Unlock()
	tc := &sessionv1.TurnCompleted{Turn: turn, Result: e.Text, IsError: e.IsError,
		Usage: &sessionv1.Usage{InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens, CostUsd: e.Usage.CostUSD}}
	if e.IsError {
		f := e.Failure
		if f == nil {
			f = &provider.Failure{Kind: provider.Classify(e.Text), Message: e.Text}
		}
		tc.Failure = f.Proto()
		countFailure(l.row.Agent, tc.Failure.Kind)
	}
	var commit string
	var turnStat *sessionv1.DiffStat
	var err error
	if edits {
		snapStart := time.Now()
		commit, turnStat, err = s.ws.Snapshot(ctx, repo, id, turn)
		mSnapshot.Since(snapStart)
	} else {
		// Nothing could have changed files: reuse the previous commit.
		mSnapshotSkipped.Inc()
		l.mu.Lock()
		commit = l.lastCommit
		if commit == "" {
			commit = l.row.BaseCommit
		}
		l.mu.Unlock()
		turnStat = &sessionv1.DiffStat{}
	}
	switch {
	case inPlace:
	case err != nil:
		s.log.Warn("snapshot failed", "session", id, "err", err)
	default:
		tc.Commit, tc.DiffStat = commit, turnStat
		// Committed with (or before) the TurnCompleted event that follows.
		s.w.put(writeOp{turn: &turnRec{id, turn, commit}})
	}
	l.mu.Lock()
	p := l.row.Progress
	p.TurnCount = turn
	p.TurnStartedAt = nil
	if commit != "" {
		l.lastCommit = commit
	}
	if p.Usage == nil {
		p.Usage = &sessionv1.Usage{}
	}
	p.Usage.InputTokens += tc.Usage.InputTokens
	p.Usage.OutputTokens += tc.Usage.OutputTokens
	p.Usage.CostUsd += tc.Usage.CostUsd
	p.LastFailure = tc.Failure
	l.row.Result = e.Text
	if e.Text != "" {
		p.LastMessageExcerpt, _ = provider.Truncate(e.Text, 280)
	}
	l.idleSince = time.Now()
	l.mu.Unlock()
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_TurnCompleted{TurnCompleted: tc}})
	reason := ""
	if status != sessionv1.SessionStatus_SESSION_STATUS_IDLE && tc.Failure != nil {
		reason = tc.Failure.Message
	}
	// Messages that arrived too late for this turn: the next one, without
	// an idle in between (callers waiting for idle wait for them too). A
	// failed turn takes them with it, like the messages steered into it.
	l.mu.Lock()
	next, dropped := l.followUps, 0
	l.followUps = nil
	if isTerminal(l.row.Status) {
		next = nil
	} else if status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || tc.IsError {
		next, dropped = nil, len(next)
	}
	if len(next) > 0 {
		m := next[0]
		for _, o := range next[1:] {
			m = m.join(o)
		}
		if err := s.nextTurn(ctx, l, m); err != nil {
			s.interruptTurn(l, nil, &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, Message: "could not send the next message: " + err.Error()})
		}
	} else {
		changed := s.setStatusLocked(l, status)
		l.mu.Unlock()
		if dropped > 0 {
			msg := fmt.Sprintf("%d message(s) sent during the turn were not delivered because it failed; send them again", dropped)
			s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Error{Error: &sessionv1.ErrorEvent{Message: msg}}})
		}
		if changed {
			s.statusChanged(l, status, reason)
		}
	}
	s.dispatch()
	switch {
	case tc.DiffStat == nil || tc.DiffStat.FilesChanged == 0:
	case turn == 1:
		// The cumulative stat of a one-turn session is the turn's stat.
		l.mu.Lock()
		l.row.Progress.DiffStat = tc.DiffStat
		row := snapshotRow(l.row)
		l.mu.Unlock()
		s.w.put(writeOp{row: row})
	default:
		s.refreshDiffStat(l)
	}
}

// refreshDiffStat recomputes the cumulative diff stat off the turn's
// critical path, coalescing requests that arrive while one is running.
func (s *Sessions) refreshDiffStat(l *live) {
	l.mu.Lock()
	if l.statRunning {
		l.statPending = true
		l.mu.Unlock()
		return
	}
	l.statRunning = true
	l.mu.Unlock()
	go func() {
		for {
			l.mu.Lock()
			repo, base, head := l.repo, l.row.BaseCommit, l.lastCommit
			l.statPending = false
			l.mu.Unlock()
			st, err := diffStat(context.Background(), repo, base, head)
			l.mu.Lock()
			if err == nil {
				l.row.Progress.DiffStat = st
			}
			row := snapshotRow(l.row)
			again := l.statPending
			if !again {
				l.statRunning = false
			}
			l.mu.Unlock()
			s.w.put(writeOp{row: row})
			if !again {
				return
			}
		}
	}()
}

// settle records f for a turn that never ran and moves the session to
// status.
func (s *Sessions) settle(l *live, f *provider.Failure, status sessionv1.SessionStatus) {
	pf := f.Proto()
	l.mu.Lock()
	l.row.Progress.LastFailure = pf
	l.mu.Unlock()
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Error{Error: &sessionv1.ErrorEvent{Message: pf.Message, Failure: pf}}})
	s.setStatus(l, status, pf.Message)
	s.dispatch()
}

func (s *Sessions) setStatus(l *live, st sessionv1.SessionStatus, reason string) {
	l.mu.Lock()
	changed := s.setStatusLocked(l, st)
	l.mu.Unlock()
	if changed {
		s.statusChanged(l, st, reason)
	}
}

// setStatusLocked changes the status (l.mu held); the caller then calls
// statusChanged when it reports a change.
func (s *Sessions) setStatusLocked(l *live, st sessionv1.SessionStatus) bool {
	if l.row.Status == st || isTerminal(l.row.Status) {
		return false
	}
	s.count(l.row.Status, -1)
	if isTerminal(st) {
		mArchived.Inc()
	} else {
		s.count(st, 1)
	}
	l.row.Status = st
	l.row.Progress.Phase = phaseOf(st)
	if st == sessionv1.SessionStatus_SESSION_STATUS_IDLE || st == sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
		l.idleSince = time.Now()
	}
	return true
}

func (s *Sessions) statusChanged(l *live, st sessionv1.SessionStatus, reason string) {
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_Status{Status: &sessionv1.StatusChanged{Status: st, Reason: reason}}})
}

// deliver runs after an event is committed: fan out to subscribers and,
// for a terminal status, retire the session.
func (s *Sessions) deliver(l *live, e *sessionv1.SessionEvent) {
	if d := e.GetAgentDraft(); d != nil {
		l.mu.Lock()
		if d.Whole {
			l.draftShown = d.Text
		} else {
			l.draftShown += d.Text
		}
		for ch, partial := range l.subs {
			if !partial {
				continue
			}
			select {
			case ch <- e:
			default:
				close(ch) // too slow: the client resumes by seq
				delete(l.subs, ch)
			}
		}
		l.mu.Unlock()
		return
	}
	terminal := false
	if st := e.GetStatus(); st != nil && isTerminal(st.Status) {
		terminal = true
	}
	l.mu.Lock()
	l.persistedSeq = e.Seq
	l.draftShown = "" // a stored event ends the draft on every screen
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			close(ch) // too slow: the client resumes by seq
			delete(l.subs, ch)
		}
	}
	if terminal {
		for ch := range l.subs {
			close(ch)
			delete(l.subs, ch)
		}
	}
	l.mu.Unlock()
	if terminal {
		s.mu.Lock()
		if s.live[l.id] == l {
			delete(s.live, l.id)
		}
		s.mu.Unlock()
	}
	s.changed(l.id)
}

func phaseOf(st sessionv1.SessionStatus) string {
	return strings.ToLower(strings.TrimPrefix(st.String(), "SESSION_STATUS_"))
}

// emit assigns the next sequence number, updates progress and queues the
// event for persistence; subscribers get it once it is committed.
func (s *Sessions) emit(l *live, e *sessionv1.SessionEvent) { s.emitOutput(l, e, "") }

// emitOutput is emit for a tool result whose whole output, longer than its
// event holds, is kept beside it.
func (s *Sessions) emitOutput(l *live, e *sessionv1.SessionEvent, output string) {
	l.emitMu.Lock()
	defer l.emitMu.Unlock()
	l.mu.Lock()
	l.seq++
	e.Seq, e.Ts = l.seq, timestamppb.Now()
	if l.draft.Len() > 0 { // the reply goes on after this event: send it whole again
		l.draftQueued = 0
		s.scheduleDraft(l)
	}
	e.TurnId = strconv.FormatUint(uint64(l.turn), 10)
	p := l.row.Progress
	p.LastEventSeq = l.seq
	var row *sessionRow
	switch k := e.Kind.(type) {
	case *sessionv1.SessionEvent_TurnStarted:
		p.LastMessageExcerpt = "" // what the agent said last turn is not this turn's progress
	case *sessionv1.SessionEvent_AgentMessage:
		p.LastMessageExcerpt, _ = provider.Truncate(k.AgentMessage.Text, 280)
	case *sessionv1.SessionEvent_ToolCall:
		p.CurrentTool = k.ToolCall.Name + ": " + k.ToolCall.Summary
		switch k.ToolCall.Kind {
		case "edit":
			p.Phase = "editing"
		case "shell":
			p.Phase = "running_shell"
		default:
			p.Phase = "running"
		}
	case *sessionv1.SessionEvent_ToolResult:
		p.CurrentTool = ""
	case *sessionv1.SessionEvent_TurnCompleted:
		p.CurrentTool = "" // a cut-short turn has no result for its last tool
		row = snapshotRow(l.row)
	case *sessionv1.SessionEvent_Status:
		row = snapshotRow(l.row)
	}
	var ns *noticeSession
	switch k := e.Kind.(type) {
	case *sessionv1.SessionEvent_QuestionAsked, *sessionv1.SessionEvent_TurnCompleted:
		ns = &noticeSession{l.id, l.row.Title, l.row.CreatedBy, l.row.ProjectID, l.row.Agent}
	case *sessionv1.SessionEvent_Status:
		if k.Status.Status == sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			ns = &noticeSession{l.id, l.row.Title, l.row.CreatedBy, l.row.ProjectID, l.row.Agent}
		}
	}
	l.mu.Unlock()
	mEvents.Inc()
	s.w.put(writeOp{l: l, ev: e, row: row, output: output})
	if ns != nil && s.notice != nil {
		go s.notice(*ns, e)
	}
}

// draftEvery paces drafts: subscribers get new reply text at most this often.
const draftEvery = 100 * time.Millisecond

// addDraft adds reply text the agent is still writing.
func (s *Sessions) addDraft(l *live, text string) {
	l.mu.Lock()
	l.draft.WriteString(text)
	s.scheduleDraft(l)
	l.mu.Unlock()
}

// scheduleDraft sends the draft's new text soon (caller holds l.mu).
func (s *Sessions) scheduleDraft(l *live) {
	if l.draftTimer == nil {
		l.draftTimer = time.AfterFunc(draftEvery, func() { s.sendDraft(l) })
	}
}

// sendDraft queues the draft's text not yet queued, behind the events
// before it, for the subscribers that take drafts. It is never stored.
func (s *Sessions) sendDraft(l *live) {
	l.emitMu.Lock()
	defer l.emitMu.Unlock()
	l.mu.Lock()
	l.draftTimer = nil
	d := &sessionv1.AgentDraft{Text: l.draft.String()[l.draftQueued:], Whole: l.draftQueued == 0}
	l.draftQueued = l.draft.Len()
	l.mu.Unlock()
	if d.Text != "" {
		s.w.put(writeOp{l: l, draft: &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_AgentDraft{AgentDraft: d}}})
	}
}

// clearDraft forgets the draft: its final message is stored, or the turn
// is retried (caller holds l.mu).
func (l *live) clearDraft() {
	l.draft.Reset()
	l.draftQueued = 0
}

// storeDraft stores a draft the agent never finished as its message, so
// what the owner saw stays (the turn ends, or the agent moved on).
func (s *Sessions) storeDraft(l *live) {
	l.mu.Lock()
	text := l.draft.String()
	l.clearDraft()
	l.mu.Unlock()
	if strings.TrimSpace(text) != "" {
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_AgentMessage{AgentMessage: &sessionv1.AgentMessage{Text: text}}})
	}
}

func (s *Sessions) toProto(r *sessionRow) *sessionv1.Session {
	return &sessionv1.Session{SessionId: r.ID, MachineId: s.machineID, ProjectId: r.ProjectID, Agent: r.Agent,
		Status: r.Status, CreatedBy: r.CreatedBy, Progress: proto.Clone(r.Progress).(*sessionv1.SessionProgress),
		ProviderSessionRef: r.ProviderRef, CreatedAt: timestamppb.New(r.CreatedAt), UpdatedAt: timestamppb.New(r.UpdatedAt),
		Error: r.Error, Branch: r.Branch, Workspace: r.Workspace, Result: r.Result, Title: r.Title}
}

// Janitor enforces timeouts and stops idle agent processes (their sessions
// stay; the next message resumes them).
func (s *Sessions) Janitor(ctx context.Context) {
	every := s.janitorEvery
	if every == 0 {
		every = 10 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		s.mu.Lock()
		ls := make([]*live, 0, len(s.live))
		for _, l := range s.live {
			ls = append(ls, l)
		}
		s.mu.Unlock()
		for _, l := range ls {
			l.mu.Lock()
			st, ps := l.row.Status, l.sess
			idleSince := l.idleSince
			l.mu.Unlock()
			switch st {
			case sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT: // not stalled: waiting for an answer
				s.expireQuestions(l, now)
			case sessionv1.SessionStatus_SESSION_STATUS_RUNNING:
				if f := s.stalled(l, ps, now); f != nil {
					s.log.Warn("stopping a stalled turn", "session", l.id, "reason", f.Message)
					mWatchdog.Inc()
					s.interruptTurn(l, ps, f)
				}
			case sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
				// The session never ends by itself; only its idle agent
				// process goes, and the next message resumes it.
				if ps != nil && now.Sub(idleSince) > s.limits.IdleProcessTTL {
					l.mu.Lock()
					if l.sess == ps {
						l.sess = nil
					}
					l.mu.Unlock()
					go ps.Close()
				}
			}
		}
	}
}

// stalled decides whether a running turn has stopped making progress.
// Progress is any agent output (ps.LastActivity) or event; a running tool
// gets the tool window, since many tools are silent while they work.
func (s *Sessions) stalled(l *live, ps provider.Session, now time.Time) *provider.Failure {
	l.mu.Lock()
	if l.finishing {
		l.mu.Unlock()
		return nil
	}
	last, turnStarted, procStarted := l.lastProgress, l.turnStarted, l.procStarted
	lastErr, lastErrAt := l.lastErr, l.lastErrAt
	var tool string
	for _, t := range l.tools {
		tool = t
		break
	}
	l.mu.Unlock()
	var out time.Time
	if ps != nil {
		out = ps.LastActivity()
		if out.After(last) {
			last = out
		}
	}
	lim := s.limits
	stall := func(format string, a ...any) *provider.Failure {
		msg := fmt.Sprintf(format, a...)
		if lastErr != nil && lastErrAt.After(last) && lastErr.Kind != sessionv1.FailureKind_FAILURE_KIND_UNKNOWN {
			// It was stuck on this (e.g. retrying a connection): say so.
			return &provider.Failure{Kind: lastErr.Kind, Message: msg + "; last error: " + lastErr.Message, HTTPStatus: lastErr.HTTPStatus}
		}
		return &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_STALLED, Message: msg}
	}
	switch {
	case lim.TurnTimeout > 0 && now.Sub(turnStarted) > lim.TurnTimeout:
		return &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_TIMEOUT, Message: fmt.Sprintf("the turn exceeded the %s limit", lim.TurnTimeout)}
	case ps != nil && out.Before(procStarted) && lim.FirstOutputTimeout > 0 && now.Sub(procStarted) > lim.FirstOutputTimeout:
		return stall("the agent produced no output for %s after starting", lim.FirstOutputTimeout)
	case tool != "":
		if lim.ToolTimeout > 0 && now.Sub(last) > lim.ToolTimeout {
			t, _ := provider.Truncate(tool, 200)
			return stall("no output for %s while running %s", lim.ToolTimeout, t)
		}
	case lim.IdleTimeout > 0 && now.Sub(last) > lim.IdleTimeout:
		return stall("no output from the agent for %s", lim.IdleTimeout)
	}
	return nil
}

// attachmentsDir holds a session's attached files, outside its worktree;
// it is removed with the worktree.
func (s *Sessions) attachmentsDir(id string) string {
	return filepath.Join(s.dataDir, "sessions", id, "attachments")
}

var (
	mQueueWait       = metrics.Default.Histogram("hermesh_machine_queue_wait_seconds")
	mWorktree        = metrics.Default.Histogram("hermesh_machine_worktree_create_seconds")
	mProviderStart   = metrics.Default.Histogram("hermesh_machine_provider_start_seconds")
	mFirstEvent      = metrics.Default.Histogram("hermesh_machine_first_event_seconds")
	mTurn            = metrics.Default.Histogram("hermesh_machine_turn_seconds")
	mSnapshot        = metrics.Default.Histogram("hermesh_machine_snapshot_seconds")
	mSnapshotSkipped = metrics.Default.Counter("hermesh_machine_snapshot_skipped_total")
	mEvents          = metrics.Default.Counter("hermesh_machine_events_total")
	mProcs           = metrics.Default.Gauge("hermesh_machine_provider_processes")
	mStartCommit     = metrics.Default.Histogram("hermesh_machine_start_commit_seconds")
	mWatchdog        = metrics.Default.Counter("hermesh_machine_watchdog_stops_total")
)

var mArchived = metrics.Default.Counter("hermesh_machine_sessions_archived_total")

// countResume counts resume attempts by outcome.
func countResume(agent, outcome string) {
	metrics.Default.Counter(`hermesh_machine_resume_total{agent="` + agent + `",outcome="` + outcome + `"}`).Inc()
}

// countFailure counts a turn or session failure by agent and kind.
func countFailure(agent string, k sessionv1.FailureKind) {
	kind := strings.ToLower(strings.TrimPrefix(k.String(), "FAILURE_KIND_"))
	metrics.Default.Counter(`hermesh_machine_turn_failures_total{agent="` + agent + `",kind="` + kind + `"}`).Inc()
}

// registerMetrics exposes live session counts per status.
func (s *Sessions) registerMetrics() {
	for v := range s.counts {
		st := sessionv1.SessionStatus(v)
		if v == 0 || isTerminal(st) {
			continue
		}
		metrics.Default.GaugeFunc(`hermesh_machine_sessions{status="`+phaseOf(st)+`"}`, func() int64 { return s.counts[v].Load() })
	}
}
