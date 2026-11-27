package kernel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/catalog"
	"github.com/hermeshai/hermesh/internal/kernel/policy"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/kernel/vcs"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// ScratchProjectID is the workspace the kernel makes on every machine for
// sessions that name no project. It is not one of the machine's projects, so a
// grant's "*" does not cover it: a grant must list it by name.
const ScratchProjectID = "scratch"

const (
	DefaultQuestionTimeout = 30 * time.Minute
	maxInlineToolOutput    = 16384
	maxDiffPageBytes       = 256 << 10

	declinedQuestionMessage = "The user declined to answer. Do not wait for an answer; continue with your best judgment and state the assumption you made."
	noAnswerQuestionFormat  = "No one answered within %s. Do not wait for an answer; continue with your best judgment and state the assumption you made."
	noAnswerersPrompt       = "No one can answer questions in this session. Continue with your best judgment and state the assumption you made."
)

// Principal represents the verified cryptographic identity calling the kernel.
type Principal struct {
	Actor             *sessionv1.Actor
	GrantCeiling      identityv1.RuntimeMode
	AllowedProjects   []string
	AllowedAgents     []string
	TaskVisibility    identityv1.TaskVisibility
	CanApproveRequest bool
	CanManageProvider bool
}

func OwnerPrincipal(deviceID, label string) *Principal {
	if label == "" {
		label = "Owner"
	}
	return &Principal{
		Actor: &sessionv1.Actor{
			Kind:  sessionv1.ActorKind_ACTOR_KIND_OWNER,
			Id:    deviceID,
			Label: label,
		},
		CanApproveRequest: true,
		CanManageProvider: true,
	}
}

func GrantPrincipal(g *identityv1.Grant) *Principal {
	kind := sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT
	label := g.GetClientLabel()
	if g.GetHerMachineId() != "" {
		kind = sessionv1.ActorKind_ACTOR_KIND_HER
		if label == "" {
			label = "Her"
		}
	}
	return &Principal{
		Actor: &sessionv1.Actor{
			Kind:    kind,
			Id:      g.GetClientId(),
			Label:   label,
			GrantId: g.GetGrantId(),
		},
		GrantCeiling:      g.GetMaxRuntimeMode(),
		AllowedProjects:   g.GetProjectIds(),
		AllowedAgents:     g.GetAgents(),
		TaskVisibility:    g.GetTaskVisibility(),
		CanApproveRequest: false,
		CanManageProvider: false,
	}
}

func (p *Principal) allowsProject(projectID string) bool {
	if p == nil {
		return false
	}
	if p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER {
		return true
	}
	if projectID == ScratchProjectID {
		return slices.Contains(p.AllowedProjects, ScratchProjectID)
	}
	return identity.Allows(p.AllowedProjects, projectID)
}

func (p *Principal) allowsAgent(instanceID, driverKind, acpFlavor string) bool {
	if p == nil {
		return false
	}
	if p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER {
		return true
	}
	return identity.Allows(p.AllowedAgents, instanceID) ||
		identity.Allows(p.AllowedAgents, driverKind) ||
		(acpFlavor != "" && identity.Allows(p.AllowedAgents, acpFlavor))
}

// isHerConversation: the session is a conversation of the owner's with Her (docs/her-conversations.md).
func isHerConversation(s *sessionv1.Session) bool {
	return s.GetAgent() == "her" || s.GetModelSelection().GetInstanceId() == "her"
}

func (p *Principal) canViewSession(s *sessionv1.Session) bool {
	if p == nil || s == nil {
		return false
	}
	// The owner's conversations with Her are theirs alone: no agent sees them, Her's tools included.
	if isHerConversation(s) {
		return p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER
	}
	if p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER {
		return true
	}
	if !p.allowsProject(s.ProjectId) {
		return false
	}
	if p.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		return true
	}
	return s.GetActor().GetId() == p.Actor.GetId() || s.GetCreatedBy() == p.Actor.GetId()
}

func defaultRuntimeModeFor(p *Principal, requested identityv1.RuntimeMode) identityv1.RuntimeMode {
	if policy.Rank(requested) > 0 {
		return requested
	}
	if p != nil && (p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER || p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT) {
		if policy.Rank(p.GrantCeiling) > 0 {
			return p.GrantCeiling
		}
		return identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS
	}
	return policy.DefaultSessionRuntimeMode
}

type NoticeHook func(sess *sessionv1.Session, ev *sessionv1.SessionEvent)

type Config struct {
	DataDir          string
	MachineID        string
	Logger           *slog.Logger
	Drivers          map[string]runtime.Driver
	IncludeScripted  bool
	IdleTTL          time.Duration
	QuestionTimeout  time.Duration
	MachineMaxMode   identityv1.RuntimeMode
	IsPaused         func() bool
	DefaultAgent     func(projectID string) string
	MCPServers       func(sessionID string) []runtime.MCPServer
	ReadDirs         func(projectID string) []string
	ExtraEnv         func(sessionID string) map[string]string
	ResolveSelection func(instanceID string, sel *sessionv1.ModelSelection) (*sessionv1.ModelSelection, error)
	ReleaseOnArchive func(ctx context.Context, sess *sessionv1.Session) (kept bool)
	OnNotice         NoticeHook
	OnWork           func(sess *sessionv1.Session, run *sessionv1.Run, ev *sessionv1.SessionEvent)
	OnSessionChanged func(sess *sessionv1.Session)
	OnSessionDeleted func(sessionID string)
	// OnLimits is told what a session's coding agent said of its account's
	// rate limits (runtime.StartSpec.Limits).
	OnLimits         func(sess *sessionv1.Session, l runtime.Limits)
	OnDeliverMessage func(ctx context.Context, p *Principal, req *sessionv1.DeliverMessageRequest) (*sessionv1.DeliverMessageResponse, error)
	// AgentModels: each coding agent's own list of models, as the machine
	// last read it; nil offers the manifest's.
	AgentModels catalog.AgentModels
	// PrimaryTitleGenerator, when set and enabled (e.g. Her's smallest model),
	// runs stateless title generation before falling back to the session's CLI.
	PrimaryTitleGenerator runtime.TitleGenerator
	// CachedAgentModels returns an agent's already-cached model list in memory
	// without triggering any subprocess or network discovery.
	CachedAgentModels func(agent string) []runtime.Model
}

type shellSub struct {
	includeArchived bool
	ch              chan *sessionv1.WatchSessionsResponse
}

type sessionSub struct {
	actorID string
	ch      chan *sessionv1.SessionEvent
}

type Engine struct {
	cfg        Config
	machineID  atomic.Value // string: Config.MachineID, updated on live Retire / DemoteToLocal
	maxMode    atomic.Int32 // the machine's ceiling (Config.MachineMaxMode, then SetMachineMaxMode)
	log        *slog.Logger
	st         *store.Store
	vcs        *vcs.Manager
	cat        *catalog.Service
	sup        *runtime.Supervisor
	scratchDir string
	bgCtx      context.Context
	bgCancel   context.CancelFunc

	mu               sync.Mutex
	wg               sync.WaitGroup
	sessionLocks     map[string]*sync.Mutex
	cmdLocks         map[string]*refLock                    // command_id -> in-flight idempotent command
	waiters          map[string]chan runtime.ToolGateResult // request_id -> waiter
	runCancels       map[string]context.CancelFunc          // run_id -> cancel
	runDoneChs       map[string]<-chan struct{}             // run_id -> runCtx.Done()
	runInterruptedBy map[string]string                      // run_id -> "interrupted by <actor>"
	pumps            map[string]*leasePump                  // provider_session_id -> pump
	titleSem         chan struct{}                          // bounds concurrent title generations
	titleCancels     map[string]context.CancelFunc          // session_id -> cancel
	titleDoneChs     map[string]chan struct{}               // session_id -> closed on completion
	sessionSubs      map[string]map[*sessionSub]struct{}
	shellSubs        map[*shellSub]struct{}
}

func Open(cfg Config) (*Engine, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.QuestionTimeout <= 0 {
		cfg.QuestionTimeout = DefaultQuestionTimeout
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "sessions.db"))
	if err != nil {
		return nil, err
	}
	scratchDir := filepath.Join(cfg.DataDir, "scratch_workspace")
	_ = os.MkdirAll(scratchDir, 0o755)

	bgCtx, bgCancel := context.WithCancel(context.Background())
	e := &Engine{
		cfg:              cfg,
		log:              cfg.Logger,
		st:               st,
		vcs:              vcs.NewManager(filepath.Join(cfg.DataDir, "worktrees")),
		cat:              catalog.NewService(cfg.Drivers, cfg.AgentModels),
		sup:              runtime.NewSupervisor(cfg.IdleTTL),
		scratchDir:       scratchDir,
		bgCtx:            bgCtx,
		bgCancel:         bgCancel,
		sessionLocks:     map[string]*sync.Mutex{},
		cmdLocks:         map[string]*refLock{},
		waiters:          map[string]chan runtime.ToolGateResult{},
		runCancels:       map[string]context.CancelFunc{},
		runDoneChs:       map[string]<-chan struct{}{},
		runInterruptedBy: map[string]string{},
		pumps:            map[string]*leasePump{},
		titleSem:         make(chan struct{}, maxConcurrentTitleGens),
		titleCancels:     map[string]context.CancelFunc{},
		titleDoneChs:     map[string]chan struct{}{},
		sessionSubs:      map[string]map[*sessionSub]struct{}{},
		shellSubs:        map[*shellSub]struct{}{},
	}
	e.sup.SetOnReap(e.onLeaseReaped)
	e.machineID.Store(cfg.MachineID)
	e.maxMode.Store(int32(cfg.MachineMaxMode))
	if err := e.seedDefaults(); err != nil {
		bgCancel()
		_ = st.Close()
		return nil, err
	}
	if cfg.MachineID != "" {
		_ = e.rebindSessionsMachineID(cfg.MachineID)
	}
	if err := e.recoverOrphans(); err != nil {
		e.log.Warn("orphan recovery warning", "err", err)
	}
	return e, nil
}

func (e *Engine) Close() error {
	e.bgCancel()
	_ = e.sup.Close()
	e.wg.Wait()
	return e.st.Close()
}

func (e *Engine) spawnRun(sessionID, runID string, freshContext bool) {
	runCtx, cancelRun := context.WithCancel(e.bgCtx)
	e.mu.Lock()
	e.runCancels[runID] = cancelRun
	e.runDoneChs[runID] = runCtx.Done()
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			cancelRun()
			e.mu.Lock()
			delete(e.runCancels, runID)
			delete(e.runDoneChs, runID)
			e.mu.Unlock()
		}()
		e.driveRun(runCtx, sessionID, runID, freshContext)
	}()
}

func (e *Engine) onLeaseReaped(lease *runtime.LeaseEntry) {
	if lease != nil {
		e.stopLeasePump(lease.ProviderSessionID, lease)
	}
	if lease == nil || e.bgCtx.Err() != nil {
		return
	}
	e.settleSessionBackgroundTasks(lease.SessionID, lease.ProviderSessionID, "session closed after inactivity")
}

func (e *Engine) settleSessionBackgroundTasks(sessionID, matchPSID, summary string) {
	if sessionID == "" || e.bgCtx.Err() != nil {
		return
	}
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sess, err := tx.GetSession(sessionID)
		if err != nil {
			return nil
		}
		if matchPSID != "" && sess.ActiveProviderSessionId != "" && sess.ActiveProviderSessionId != matchPSID {
			return nil
		}
		stopped, items, err := tx.StopRunningBackgroundTasks(sessionID, "", summary)
		if err != nil || len(stopped) == 0 {
			return err
		}
		_, _, _ = tx.RefreshSessionBackgroundCounts(sess)
		if err := putSessionSynced(tx, sess); err != nil {
			return err
		}
		for _, it := range items {
			_ = tx.AppendEvent(&sessionv1.SessionEvent{SessionId: sess.SessionId, Type: "item.updated", Session: sess, Item: it})
		}
		for _, bt := range stopped {
			_ = tx.AppendEvent(&sessionv1.SessionEvent{SessionId: sess.SessionId, Type: "background_task.updated", Session: sess, BackgroundTask: bt})
		}
		return nil
	})
	if err == nil && len(events) > 0 {
		e.publish(events, nil)
	}
}

// machineMax is the machine's own permission-mode ceiling (UNSPECIFIED: none).
func (e *Engine) machineMax() identityv1.RuntimeMode { return identityv1.RuntimeMode(e.maxMode.Load()) }

// SetMachineMaxMode changes the machine's ceiling: launches and dispatches from
// now on are clamped to it (a run in flight keeps the mode it started with).
func (e *Engine) SetMachineMaxMode(m identityv1.RuntimeMode) { e.maxMode.Store(int32(m)) }

func (e *Engine) Store() *store.Store             { return e.st }
func (e *Engine) Supervisor() *runtime.Supervisor { return e.sup }
func (e *Engine) VCS() *vcs.Manager               { return e.vcs }
func (e *Engine) Catalog() *catalog.Service       { return e.cat }

func (e *Engine) BusyCount() int {
	sessions, err := e.st.ListSessions("", false, 0)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range sessions {
		switch s.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_QUEUED,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
			sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			n++
		}
	}
	return n
}

func (e *Engine) SessionSummary() store.SessionSummary {
	sum, err := e.st.SessionSummary()
	if err != nil {
		return store.SessionSummary{Counts: map[string]uint32{}, ActiveByProject: map[string]uint32{}}
	}
	return sum
}

func (e *Engine) RunningCount() uint32 {
	return uint32(e.SessionSummary().Running)
}

func (e *Engine) ActiveByProject() map[string]uint32 {
	return e.SessionSummary().ActiveByProject
}

// ListAllSessions returns every stored session on this machine without actor filtering.
func (e *Engine) ListAllSessions(includeArchived bool) ([]*sessionv1.Session, error) {
	return e.st.ListSessions("", includeArchived, 0)
}

// ResolveDriver returns the configured provider instance and runtime driver for instanceID.
func (e *Engine) ResolveDriver(instanceID string) (*sessionv1.ProviderInstanceConfig, runtime.Driver, error) {
	return e.resolveInstanceConfig(OwnerPrincipal("owner", "Owner"), ScratchProjectID, instanceID)
}

func (e *Engine) seedDefaults() error {
	existing, err := e.st.ProviderInstances()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, c := range existing {
		have[c.InstanceId] = true
	}
	_, err = e.st.Mutate(func(tx *store.Tx) error {
		// Always register a default "scratch" workspace so ad-hoc sessions work immediately.
		if _, wErr := e.st.Workspace(ScratchProjectID); errors.Is(wErr, store.ErrNotFound) {
			if err := tx.PutWorkspace(store.WorkspaceRow{
				ProjectID:     ScratchProjectID,
				Path:          e.scratchDir,
				WorkspaceMode: int32(identityv1.Workspace_WORKSPACE_IN_PLACE),
				Description:   "Scratch workspace",
			}); err != nil {
				return err
			}
		}
		for _, def := range catalog.DefaultInstances(e.cfg.IncludeScripted) {
			if !have[def.InstanceId] && e.cat.Driver(def.Driver) != nil {
				if err := tx.PutProviderInstance(def); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return err
}

// SyncWorkspace keeps workspaces synchronized with Machine's configured projects.
func (e *Engine) SyncWorkspace(projectID, path, defaultBaseRef string, mode identityv1.Workspace, description string, agents []string) error {
	_, err := e.st.Mutate(func(tx *store.Tx) error {
		return tx.PutWorkspace(store.WorkspaceRow{
			ProjectID:      projectID,
			Path:           path,
			DefaultBaseRef: defaultBaseRef,
			WorkspaceMode:  int32(mode),
			Description:    description,
			Agents:         agents,
		})
	})
	return err
}

func (e *Engine) RemoveWorkspace(projectID string) error {
	_, err := e.st.Mutate(func(tx *store.Tx) error {
		return tx.RemoveWorkspace(projectID)
	})
	return err
}

func syncSessionSummary(s *sessionv1.Session) {
	if s == nil {
		return
	}
	if s.Progress == nil {
		s.Progress = &sessionv1.SessionProgress{}
	}
	if sel := s.ModelSelection; sel != nil {
		if sel.InstanceId != "" {
			s.Agent = sel.InstanceId
		}
		if sel.Model != "" {
			s.Model = sel.Model
		}
		s.Effort = extractEffort(sel.Options)
	}
	if ws := s.WorkspaceStrategy; ws != nil {
		s.Workspace = ws.Mode
		if ws.Branch != "" {
			s.Branch = ws.Branch
		}
	}
	if s.CreatedBy == "" && s.Actor != nil {
		if s.Actor.Kind == sessionv1.ActorKind_ACTOR_KIND_OWNER {
			s.CreatedBy = "owner"
		} else {
			s.CreatedBy = s.Actor.Id
		}
	}
	switch s.Status {
	case sessionv1.SessionStatus_SESSION_STATUS_RUNNING:
		s.Progress.Phase = "running"
	case sessionv1.SessionStatus_SESSION_STATUS_PREPARING:
		s.Progress.Phase = "preparing"
	case sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
		s.Progress.Phase = "waiting_input"
	case sessionv1.SessionStatus_SESSION_STATUS_QUEUED:
		s.Progress.Phase = "queued"
	default:
		s.Progress.Phase = ""
		s.Progress.CurrentTool = ""
		s.Progress.TurnStartedAt = nil
	}
}

func extractEffort(opts map[string]*sessionv1.OptionValue) string {
	for _, key := range []string{"reasoningEffort", "thought_level", "effort"} {
		if v, ok := opts[key]; ok && v != nil {
			if sv, ok := v.Kind.(*sessionv1.OptionValue_StringValue); ok && sv.StringValue != "" {
				return sv.StringValue
			}
		}
	}
	return ""
}

func putSessionSynced(tx *store.Tx, s *sessionv1.Session) error {
	syncSessionSummary(s)
	return tx.PutSession(s)
}

// recoverOrphans reconciles sessions interrupted by a daemon crash or power loss:
// in-flight runs become INTERRUPTED, LIVE_RPC requests downgrade to RESUME_TURN
// so they remain answerable, and QUEUED runs stay intact in queue order.
func (e *Engine) recoverOrphans() error {
	sessions, err := e.st.ListSessions("", true, 0)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		hasOrphanBg := s.ActiveBackgroundCount > 0 || s.ActiveDaemonCount > 0
		if s.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING &&
			s.Status != sessionv1.SessionStatus_SESSION_STATUS_PREPARING &&
			s.Status != sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT &&
			!hasOrphanBg &&
			!s.TitleGenerating {
			continue
		}
		runs, _ := e.st.Runs(s.SessionId)
		reqs, _ := e.st.RuntimeRequests(s.SessionId)
		pss, _ := e.st.ProviderSessions(s.SessionId)
		events, err := e.st.Mutate(func(tx *store.Tx) error {
			sess, err := tx.GetSession(s.SessionId)
			if err != nil {
				return nil
			}
			for _, ps := range pss {
				if ps.ProcessAlive {
					ps.ProcessAlive = false
					_ = tx.PutProviderSession(ps)
				}
			}
			stoppedBg, stoppedItems, _ := tx.StopRunningBackgroundTasks(sess.SessionId, "", "hermeshd restarted while background task was running")
			for _, sit := range stoppedItems {
				_ = tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sess.SessionId,
					Type:      "item.updated",
					Item:      sit,
				})
			}
			for _, bt := range stoppedBg {
				_ = tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId:      sess.SessionId,
					Type:           "background_task.updated",
					BackgroundTask: bt,
				})
			}
			_, _, _ = tx.RefreshSessionBackgroundCounts(sess)
			hasPendingResumeReq := false
			for _, rq := range reqs {
				if rq.Status == sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
					if rq.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT ||
						rq.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW {
						rq.ResponseMode = sessionv1.ResponseMode_RESPONSE_MODE_RESUME_TURN
						_ = tx.PutRuntimeRequest(rq)
						hasPendingResumeReq = true
					} else {
						rq.Status = sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_EXPIRED
						rq.ResolvedAt = timestamppb.Now()
						_ = tx.PutRuntimeRequest(rq)
					}
				}
			}
			for _, r := range runs {
				if r.Status == sessionv1.RunStatus_RUN_STATUS_RUNNING ||
					r.Status == sessionv1.RunStatus_RUN_STATUS_PREPARING ||
					r.Status == sessionv1.RunStatus_RUN_STATUS_WAITING_REQUEST {
					r.Status = sessionv1.RunStatus_RUN_STATUS_INTERRUPTED
					r.ErrorMessage = "hermeshd restarted while turn was active"
					r.CompletedAt = timestamppb.Now()
					_ = tx.PutRun(r)
					settled, _ := tx.SettleRunItems(sess.SessionId, r.RunId, "interrupted")
					for _, sit := range settled {
						_ = tx.AppendEvent(&sessionv1.SessionEvent{
							SessionId: sess.SessionId,
							Type:      "item.updated",
							Item:      sit,
						})
					}
				}
			}
			queued, _ := tx.QueuedRuns(sess.SessionId)
			sess.QueuedRunCount = uint32(len(queued))
			sess.ActiveRunId = ""
			sess.TitleGenerating = false
			if hasPendingResumeReq {
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
			} else if len(queued) > 0 {
				sess.QueueHeld = true // Hold queue after unexpected crash so user can inspect or resume.
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_QUEUED
				sess.PendingRequestId = ""
				if sess.Progress != nil {
					sess.Progress.PendingQuestion = nil
				}
			} else {
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
				sess.PendingRequestId = ""
				if sess.Progress != nil {
					sess.Progress.PendingQuestion = nil
				}
			}
			if err := putSessionSynced(tx, sess); err != nil {
				return err
			}
			return tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sess.SessionId,
				Type:      "session.recovered",
				Session:   sess,
			})
		})
		if err == nil {
			e.publish(events, nil)
		}
	}
	return nil
}

func (e *Engine) lockSession(sessionID string) func() {
	e.mu.Lock()
	lk := e.sessionLocks[sessionID]
	if lk == nil {
		lk = &sync.Mutex{}
		e.sessionLocks[sessionID] = lk
	}
	e.mu.Unlock()
	lk.Lock()
	return lk.Unlock
}

// WithSessionLock serializes external workspace maintenance (e.g. ReleaseWorkspaces)
// with session mutations, run dispatch, and archive/unarchive transitions.
func (e *Engine) WithSessionLock(sessionID string, fn func() error) error {
	unlock := e.lockSession(sessionID)
	defer unlock()
	return fn()
}

type refLock struct {
	mu sync.Mutex
	n  int
}

// lockCommand serializes requests that share a command_id, so a retry that
// arrives while the first attempt is still running waits and then replays its
// receipt instead of executing the command a second time.
func (e *Engine) lockCommand(commandID string) func() {
	if commandID == "" {
		return func() {}
	}
	e.mu.Lock()
	l := e.cmdLocks[commandID]
	if l == nil {
		l = &refLock{}
		e.cmdLocks[commandID] = l
	}
	l.n++
	e.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		e.mu.Lock()
		if l.n--; l.n == 0 {
			delete(e.cmdLocks, commandID)
		}
		e.mu.Unlock()
	}
}

func newID(prefix string) string {
	return prefix + "_" + strings.ToLower(ulid.Make().String())
}

// SessionPrefix returns the deterministic 8-char machine routing tag embedded in session IDs.
func SessionPrefix(machineID string) string {
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(machineID)), "m_")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

// OwnsSession reports whether sessionID carries machineID's routing prefix.
func OwnsSession(machineID, sessionID string) bool {
	parts := strings.SplitN(sessionID, "_", 3)
	if len(parts) != 3 || (parts[0] != "s" && parts[0] != "th") {
		return false
	}
	p := SessionPrefix(machineID)
	return p != "" && p == parts[1]
}

func (e *Engine) currentMachineID() string {
	if v, _ := e.machineID.Load().(string); v != "" {
		return v
	}
	return e.cfg.MachineID
}

// SetMachineID updates the machine id stamped on new and existing local
// sessions (e.g. when a machine retires its previous mesh key in place).
func (e *Engine) SetMachineID(machineID string) {
	if machineID == "" {
		return
	}
	e.machineID.Store(machineID)
	_ = e.rebindSessionsMachineID(machineID)
}

func (e *Engine) rebindSessionsMachineID(machineID string) error {
	all, err := e.ListAllSessions(true)
	if err != nil {
		return err
	}
	var changed []*sessionv1.Session
	for _, s := range all {
		if s.MachineId != machineID {
			changed = append(changed, s)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var updated []*sessionv1.Session
	_, err = e.st.Mutate(func(tx *store.Tx) error {
		for _, old := range changed {
			cur, gErr := tx.GetSession(old.SessionId)
			if gErr != nil {
				continue
			}
			if cur.MachineId == machineID {
				continue
			}
			cur.MachineId = machineID
			if pErr := tx.PutSessionPreserveUpdatedAt(cur); pErr != nil {
				return pErr
			}
			updated = append(updated, proto.Clone(cur).(*sessionv1.Session))
		}
		return nil
	})
	if err == nil && len(updated) > 0 {
		e.notifyOutOfBandSessionChanges(updated)
	}
	return err
}

func (e *Engine) notifyOutOfBandSessionChanges(updated []*sessionv1.Session) {
	if len(updated) == 0 {
		return
	}
	if e.cfg.OnSessionChanged != nil {
		for _, s := range updated {
			e.cfg.OnSessionChanged(s)
		}
	}
	e.mu.Lock()
	for sub := range e.shellSubs {
		var filtered []*sessionv1.Session
		for _, s := range updated {
			if sub.includeArchived || s.Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		select {
		case sub.ch <- &sessionv1.WatchSessionsResponse{Sessions: filtered}:
		default:
		}
	}
	e.mu.Unlock()
}

func (e *Engine) inspectSessionGitStatus(ctx context.Context, sess *sessionv1.Session, cp *sessionv1.Checkpoint, cumStat *sessionv1.DiffStat) *sessionv1.GitStatus {
	if sess == nil {
		return nil
	}
	ws, err := e.st.Workspace(sess.ProjectId)
	if err != nil {
		return nil
	}
	strat := sess.GetWorkspaceStrategy()
	mode := strat.GetMode()
	if mode == identityv1.Workspace_WORKSPACE_UNSPECIFIED {
		mode = sess.Workspace
	}
	sessionBranch := cmp.Or(strat.GetBranch(), sess.Branch)
	workdir := ws.Path
	if mode == identityv1.Workspace_WORKSPACE_WORKTREE {
		workdir = strat.GetWorktreePath()
	}

	ckpts, _ := e.st.Checkpoints(sess.SessionId)
	var initialHeadOID, initialTreeOID string
	if len(ckpts) > 0 && ckpts[0].RunOrdinal == 0 {
		initialHeadOID = ckpts[0].HeadOid
		initialTreeOID = ckpts[0].GitTreeOid
	}
	latestCp := cp
	if latestCp == nil && len(ckpts) > 0 && ckpts[len(ckpts)-1].RunOrdinal > 0 {
		latestCp = ckpts[len(ckpts)-1]
	}
	diffStat := cumStat
	if diffStat == nil {
		diffStat = sess.GetProgress().GetDiffStat()
	}
	hasChangesHint := diffStat.GetFilesChanged() > 0

	gCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// An in-place session shares the project folder with other sessions: if its own
	// turns changed neither the working tree nor HEAD, keep it CLEAN even if later
	// work in that shared folder modified or pushed commits.
	if mode != identityv1.Workspace_WORKSPACE_WORKTREE && initialTreeOID != "" && latestCp != nil &&
		latestCp.GitTreeOid == initialTreeOID && latestCp.HeadOid == initialHeadOID && !hasChangesHint {
		if !vcs.IsGitRepo(gCtx, ws.Path) {
			return nil
		}
		return &sessionv1.GitStatus{
			State:  sessionv1.GitState_GIT_STATE_CLEAN,
			Branch: cmp.Or(vcs.CurrentBranch(gCtx, ws.Path), sessionBranch),
		}
	}

	return e.vcs.InspectGitStatus(gCtx, vcs.GitStatusInput{
		Workdir:        workdir,
		RepoPath:       ws.Path,
		SessionBranch:  sessionBranch,
		BaseRef:        cmp.Or(strat.GetBaseRef(), ws.DefaultBaseRef),
		InitialHeadOID: initialHeadOID,
		HasChangesHint: hasChangesHint,
	})
}

func (e *Engine) RefreshGitStatus(ctx context.Context, p *Principal, sessionIDs []string) (*sessionv1.RefreshGitStatusResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var candidates []*sessionv1.Session
	if len(sessionIDs) > 0 {
		seen := make(map[string]bool, len(sessionIDs))
		for _, id := range sessionIDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			s, err := e.st.Session(id)
			if err == nil && p.canViewSession(s) {
				candidates = append(candidates, s)
			}
			if len(candidates) >= 64 {
				break
			}
		}
	} else {
		all, err := e.st.ListSessions("", false, 30)
		if err != nil {
			return nil, err
		}
		for _, s := range all {
			if p.canViewSession(s) && s.ActiveRunId == "" {
				candidates = append(candidates, s)
			}
		}
	}
	if len(candidates) == 0 {
		return &sessionv1.RefreshGitStatusResponse{}, nil
	}

	computed := make([]*sessionv1.GitStatus, len(candidates))
	inspected := make([]bool, len(candidates))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, s := range candidates {
		if s.ActiveRunId != "" {
			continue
		}
		i, s := i, s
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			computed[i] = e.inspectSessionGitStatus(ctx, s, nil, nil)
			inspected[i] = true
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	type gitPatch struct {
		sessionID string
		gitStatus *sessionv1.GitStatus
	}
	var patches []gitPatch
	for i, s := range candidates {
		if inspected[i] && !proto.Equal(s.GitStatus, computed[i]) {
			patches = append(patches, gitPatch{sessionID: s.SessionId, gitStatus: computed[i]})
		}
	}

	byID := make(map[string]*sessionv1.Session, len(candidates))
	for _, s := range candidates {
		byID[s.SessionId] = s
	}

	if len(patches) > 0 {
		var changed []*sessionv1.Session
		_, err := e.st.Mutate(func(tx *store.Tx) error {
			for _, pt := range patches {
				cur, gErr := tx.GetSession(pt.sessionID)
				if gErr != nil || cur.ActiveRunId != "" {
					continue
				}
				if proto.Equal(cur.GitStatus, pt.gitStatus) {
					byID[cur.SessionId] = proto.Clone(cur).(*sessionv1.Session)
					continue
				}
				cur.GitStatus = pt.gitStatus
				if pErr := tx.PutSessionPreserveUpdatedAt(cur); pErr != nil {
					return pErr
				}
				cloned := proto.Clone(cur).(*sessionv1.Session)
				changed = append(changed, cloned)
				byID[cur.SessionId] = cloned
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		e.notifyOutOfBandSessionChanges(changed)
	}

	out := make([]*sessionv1.Session, 0, len(candidates))
	for _, s := range candidates {
		out = append(out, byID[s.SessionId])
	}
	return &sessionv1.RefreshGitStatusResponse{Sessions: out}, nil
}

func (e *Engine) newSessionID() string {
	if p := SessionPrefix(e.currentMachineID()); p != "" {
		return "s_" + p + "_" + strings.ToLower(ulid.Make().String())
	}
	return newID("s")
}

// Following reports whether actorID currently has an active SubscribeSession stream on sessionID.
func (e *Engine) Following(sessionID, actorID string) bool {
	if actorID == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for sub := range e.sessionSubs[sessionID] {
		if sub.actorID == actorID {
			return true
		}
	}
	return false
}

// InterruptByClient stops active or waiting runs on sessions created by clientID (e.g. when a grant is revoked).
func (e *Engine) InterruptByClient(ctx context.Context, clientID string) {
	if clientID == "" {
		return
	}
	all, err := e.st.ListSessions("", true, 0)
	if err != nil {
		return
	}
	owner := OwnerPrincipal("owner", "Owner")
	for _, s := range all {
		if s.CreatedBy != clientID && s.GetActor().GetId() != clientID {
			continue
		}
		switch s.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED,
			sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
			sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			_, _ = e.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{
				SessionId: s.SessionId,
				HoldQueue: true,
			})
		}
	}
}

func (e *Engine) publish(events []*sessionv1.SessionEvent, deletedSessionIDs []string) {
	if len(events) == 0 && len(deletedSessionIDs) == 0 {
		return
	}
	var updatedSessions []*sessionv1.Session
	seenSession := map[string]bool{}
	for _, ev := range events {
		e.mu.Lock()
		subs := e.sessionSubs[ev.SessionId]
		for sub := range subs {
			select {
			case sub.ch <- ev:
			default:
			}
		}
		e.mu.Unlock()

		var sess *sessionv1.Session
		if ev.Session != nil {
			sess = ev.Session
		} else if s, err := e.st.Session(ev.SessionId); err == nil {
			sess = s
		}
		if sess != nil && !seenSession[ev.SessionId] {
			seenSession[ev.SessionId] = true
			updatedSessions = append(updatedSessions, sess)
		}
		if sess != nil {
			if e.cfg.OnNotice != nil && (ev.Type == "session.created" || ev.Type == "request.created" || ev.Type == "request.resolved" || ev.Type == "run.failed" || ev.Type == "run.completed") {
				e.cfg.OnNotice(sess, ev)
			}
			if e.cfg.OnWork != nil && (ev.Type == "session.created" || ev.Type == "run.started" || ((ev.Type == "item.updated" || ev.Type == "queue.updated") && ev.Run != nil) || ev.Type == "request.created" || ev.Type == "request.resolved" || ev.Type == "run.failed" || ev.Type == "run.completed" || ev.Type == "run.interrupted" || ev.Type == "session.updated" || ev.Type == "session.recovered") {
				e.cfg.OnWork(sess, ev.Run, ev)
			}
		}
	}

	if e.cfg.OnSessionChanged != nil {
		for _, s := range updatedSessions {
			e.cfg.OnSessionChanged(s)
		}
	}
	if e.cfg.OnSessionDeleted != nil {
		for _, id := range deletedSessionIDs {
			e.cfg.OnSessionDeleted(id)
		}
	}

	if len(updatedSessions) > 0 || len(deletedSessionIDs) > 0 {
		e.mu.Lock()
		for sub := range e.shellSubs {
			var filtered []*sessionv1.Session
			for _, s := range updatedSessions {
				if sub.includeArchived || s.Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
					filtered = append(filtered, s)
				}
			}
			if len(filtered) == 0 && len(deletedSessionIDs) == 0 {
				continue
			}
			msg := &sessionv1.WatchSessionsResponse{
				Sessions:          filtered,
				DeletedSessionIds: deletedSessionIDs,
			}
			select {
			case sub.ch <- msg:
			default:
			}
		}
		e.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Provider Catalog Operations

func (e *Engine) ListProviders(ctx context.Context, p *Principal, projectID string, refresh bool) (*sessionv1.ListProvidersResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var workdir string
	if projectID != "" && p.allowsProject(projectID) {
		if ws, err := e.st.Workspace(projectID); err == nil {
			workdir = ws.Path
		}
	}
	cfgs, err := e.st.ProviderInstances()
	if err != nil {
		return nil, err
	}
	var out []*sessionv1.ProviderInstance
	for _, cfg := range cfgs {
		if !p.allowsAgent(cfg.InstanceId, cfg.Driver, cfg.AcpFlavor) {
			continue
		}
		if cfg.Driver == "her" && p.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
			continue
		}
		out = append(out, e.cat.ResolveInstance(ctx, cfg, workdir, refresh))
	}
	return &sessionv1.ListProvidersResponse{
		Providers:      out,
		MaxRuntimeMode: policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling),
	}, nil
}

func (e *Engine) UpsertProviderInstance(ctx context.Context, p *Principal, cfg *sessionv1.ProviderInstanceConfig) (*sessionv1.ProviderInstance, error) {
	if p == nil || !p.CanManageProvider {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the owner may configure provider instances"))
	}
	if cfg == nil || strings.TrimSpace(cfg.InstanceId) == "" || strings.TrimSpace(cfg.Driver) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("instance_id and driver are required"))
	}
	if e.cat.Driver(cfg.Driver) == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown provider driver %q", cfg.Driver))
	}
	// Preserve existing secret env values when caller sends redacted placeholder "••••••••".
	existing, _ := e.st.ProviderInstances()
	for _, old := range existing {
		if old.InstanceId == cfg.InstanceId && len(old.Env) > 0 {
			if cfg.Env == nil {
				cfg.Env = map[string]string{}
			}
			for k, v := range cfg.Env {
				if v == "••••••••" {
					if prevVal, ok := old.Env[k]; ok {
						cfg.Env[k] = prevVal
					}
				}
			}
		}
	}
	if _, err := e.st.Mutate(func(tx *store.Tx) error {
		return tx.PutProviderInstance(cfg)
	}); err != nil {
		return nil, err
	}
	return e.cat.ResolveInstance(ctx, cfg, "", true), nil
}

func (e *Engine) RemoveProviderInstance(_ context.Context, p *Principal, instanceID string) error {
	if p == nil || !p.CanManageProvider {
		return connect.NewError(connect.CodePermissionDenied, errors.New("only the owner may remove provider instances"))
	}
	_, err := e.st.Mutate(func(tx *store.Tx) error {
		return tx.DeleteProviderInstance(instanceID)
	})
	return err
}

func (e *Engine) resolveInstanceConfig(p *Principal, projectID, instanceID string) (*sessionv1.ProviderInstanceConfig, runtime.Driver, error) {
	cfgs, err := e.st.ProviderInstances()
	if err != nil {
		return nil, nil, err
	}
	var chosen *sessionv1.ProviderInstanceConfig
	if instanceID != "" {
		for _, c := range cfgs {
			if c.InstanceId == instanceID {
				chosen = c
				break
			}
		}
	} else {
		var preferred []string
		if e.cfg.DefaultAgent != nil {
			if da := strings.TrimSpace(e.cfg.DefaultAgent(projectID)); da != "" {
				preferred = append(preferred, da)
			}
		}
		preferred = append(preferred, "scripted", "fake", "claude", "codex", "gemini", "antigravity", "cursor", "opencode")
		for _, want := range preferred {
			for _, c := range cfgs {
				if c.InstanceId == want && c.Enabled && e.cat.Driver(c.Driver) != nil &&
					(p == nil || p.allowsAgent(c.InstanceId, c.Driver, c.AcpFlavor)) &&
					e.projectAllowsInstance(projectID, c.InstanceId) == nil {
					chosen = c
					break
				}
			}
			if chosen != nil {
				break
			}
		}
		if chosen == nil {
			for _, c := range cfgs {
				if c.Enabled && e.cat.Driver(c.Driver) != nil &&
					(p == nil || p.allowsAgent(c.InstanceId, c.Driver, c.AcpFlavor)) &&
					e.projectAllowsInstance(projectID, c.InstanceId) == nil {
					chosen = c
					break
				}
			}
		}
	}
	if chosen == nil {
		if instanceID == "" {
			return nil, nil, connect.NewError(connect.CodePermissionDenied, errors.New("no permitted provider instance available"))
		}
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("provider instance %q is not configured", instanceID))
	}
	if !chosen.Enabled {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("provider instance %q is disabled", chosen.InstanceId))
	}
	drv := e.cat.Driver(chosen.Driver)
	if drv == nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("driver %q is unavailable", chosen.Driver))
	}
	return chosen, drv, nil
}

// principalAllowsInstance reports whether p may run turns on the configured
// provider instance instanceID (owner and Her are always allowed).
func (e *Engine) principalAllowsInstance(p *Principal, instanceID string) bool {
	cfgs, _ := e.st.ProviderInstances()
	for _, c := range cfgs {
		if c.InstanceId == instanceID {
			return p.allowsAgent(c.InstanceId, c.Driver, c.AcpFlavor)
		}
	}
	return p.allowsAgent(instanceID, "", "")
}

// projectAllowsInstance checks that the project is set up for the provider
// instance (the machine's Project.agents): by instance id, driver or ACP
// flavour. It holds for the owner too: it is the project's own setting, not a grant's.
func (e *Engine) projectAllowsInstance(projectID, instanceID string) error {
	if projectID == "" {
		return nil
	}
	ws, err := e.st.Workspace(projectID)
	if err != nil || len(ws.Agents) == 0 {
		return nil
	}
	ok := slices.Contains(ws.Agents, instanceID)
	cfgs, _ := e.st.ProviderInstances()
	for _, c := range cfgs {
		if c.InstanceId == instanceID {
			ok = ok || slices.Contains(ws.Agents, c.Driver) || (c.AcpFlavor != "" && slices.Contains(ws.Agents, c.AcpFlavor))
		}
	}
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("project %q is not set up for %q (it is for: %s)", projectID, instanceID, strings.Join(ws.Agents, ", ")))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Session Start & Dispatch

// isSubagent: the session is a sub-agent of its parent, which titles it; work Her sent is not one.
func isSubagent(req *sessionv1.StartSessionRequest) bool {
	return req.ParentSessionId != "" && req.ParentRelationship != "her"
}

func (e *Engine) StartSession(ctx context.Context, p *Principal, req *sessionv1.StartSessionRequest) (*sessionv1.StartSessionResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	if e.cfg.IsPaused != nil && e.cfg.IsPaused() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("machine is paused"))
	}
	var cached sessionv1.StartSessionResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "StartSession", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	projectID := strings.TrimSpace(req.ProjectId)
	if projectID == "" {
		projectID = ScratchProjectID
	}
	if !p.allowsProject(projectID) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("grant does not permit project %q", projectID))
	}
	ws, err := e.st.Workspace(projectID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project %q not found on machine", projectID))
	}
	if err != nil {
		return nil, err
	}

	sel := proto.Clone(req.ModelSelection).(*sessionv1.ModelSelection)
	if sel == nil {
		sel = &sessionv1.ModelSelection{}
	}
	instCfg, _, err := e.resolveInstanceConfig(p, projectID, sel.InstanceId)
	if err != nil {
		return nil, err
	}
	if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("grant does not permit provider %q", instCfg.InstanceId))
	}
	// Only Her (or the owner) says that work is Her's, sent from a conversation of the owner's with her.
	if req.ParentRelationship == "her" && p.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_HER && p.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only Her sends work from her conversations"))
	}
	// A conversation with Her is the owner's own (docs/her-conversations.md): no agent opens one.
	if instCfg.Driver == "her" && p.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the owner talks with Her"))
	}
	if err := e.projectAllowsInstance(projectID, instCfg.InstanceId); err != nil {
		return nil, err
	}
	resolvedInst := e.cat.ResolveInstance(ctx, instCfg, ws.Path, false)
	if sel.InstanceId == "" {
		sel.InstanceId = instCfg.InstanceId
	}
	if e.cfg.ResolveSelection != nil {
		var sErr error
		sel, sErr = e.cfg.ResolveSelection(sel.InstanceId, sel)
		if sErr != nil {
			return nil, sErr
		}
	} else if sel.Model == "" {
		def := catalog.DefaultSelection(resolvedInst)
		sel.Model = def.Model
		if len(sel.Options) == 0 {
			sel.Options = def.Options
		}
	}

	reqRuntimeMode := defaultRuntimeModeFor(p, req.RuntimeMode)
	maxMode := policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling)
	effMode := policy.EffectiveMode(e.machineMax(), p.GrantCeiling, reqRuntimeMode)

	imode := req.InteractionMode
	if imode == sessionv1.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		imode = sessionv1.InteractionMode_INTERACTION_MODE_CODE
	}

	wsStrategy := proto.Clone(req.Workspace).(*sessionv1.WorkspaceStrategy)
	if wsStrategy == nil {
		wsStrategy = &sessionv1.WorkspaceStrategy{}
	}
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_UNSPECIFIED {
		wsStrategy.Mode = identityv1.Workspace(ws.WorkspaceMode)
		if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_UNSPECIFIED {
			if vcs.IsGitRepo(ctx, ws.Path) {
				wsStrategy.Mode = identityv1.Workspace_WORKSPACE_WORKTREE
			} else {
				wsStrategy.Mode = identityv1.Workspace_WORKSPACE_IN_PLACE
			}
		}
	}
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_IN_PLACE && req.GetWorkspace().GetBaseRef() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an in-place project takes no base_ref"))
	}
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_WORKTREE && wsStrategy.BaseRef == "" {
		wsStrategy.BaseRef = ws.DefaultBaseRef
	}

	sessionID := e.newSessionID()
	hasInitial := strings.TrimSpace(req.InitialPrompt) != "" || len(req.Attachments) > 0
	titleLocale := runtime.ResolveLocale(req.Locale)
	title := runtime.CleanTitle(req.Title)
	titleRenamed := title != ""
	var (
		titleState      = sessionv1.TitleState_TITLE_STATE_LOCKED
		titleGenerating bool
		titleSeq        uint64 = 1
	)
	if title == "" {
		if !hasInitial {
			title = runtime.LocalizedSeedFallback(titleLocale, "empty", "")
			titleState = sessionv1.TitleState_TITLE_STATE_SEED
			titleSeq = 0
		} else {
			seed, isFinal := runtime.DeriveSeedTitle(req.InitialPrompt, req.Attachments, titleLocale)
			title = seed
			if isSubagent(req) || isFinal || !e.hasTitleGenerator() {
				titleState = sessionv1.TitleState_TITLE_STATE_FINAL
			} else {
				titleState = sessionv1.TitleState_TITLE_STATE_SEED
				titleGenerating = true
			}
		}
	} else if p.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER &&
		strings.TrimSpace(req.Locale) != "" &&
		!isSubagent(req) &&
		hasInitial &&
		!runtime.TitleMatchesLocaleScript(title, titleLocale) &&
		e.hasTitleGenerator() {
		// An agent (such as Her) supplied a title in a different language than the
		// client's UI locale: keep it as the 0ms seed and translate/refine in Pass 1.
		titleRenamed = false
		titleState = sessionv1.TitleState_TITLE_STATE_SEED
		titleGenerating = true
	}

	// The folder is set up at the first turn, which says in the conversation how it went
	// (prepareWorkspace): a worktree made, a branch switched to. Here only what needs no git
	// work, so the answer comes at once: a worktree's branch is named, and a switch is
	// refused while another session works in the folder.
	wsStrategy.WorktreePath = ""
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_WORKTREE {
		if wsStrategy.Branch == "" {
			wsStrategy.Branch = "hermesh/" + sessionID
		}
	} else if cur := vcs.CurrentBranch(ctx, ws.Path); wsStrategy.Branch == "" {
		wsStrategy.Branch = cur
	} else if wsStrategy.Branch != cur && e.busyInPlace(projectID, "") {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("a session is working in %s now; switch branches once it is done", ws.Path))
	}

	status := sessionv1.SessionStatus_SESSION_STATUS_IDLE
	if hasInitial {
		status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
	}

	lineage := &sessionv1.SessionLineage{Relationship: "root", Controllable: true}
	switch {
	case req.ParentSessionId != "" && req.ParentRelationship == "her":
		// Work Her sent from a conversation of hers, which may be on another machine: the
		// conversation does not control it, and it is titled like any other session.
		lineage = &sessionv1.SessionLineage{ParentSessionId: req.ParentSessionId, Relationship: "her"}
	case req.ParentSessionId != "":
		lineage = &sessionv1.SessionLineage{
			ParentSessionId: req.ParentSessionId,
			Relationship:    "subagent",
			Controllable:    true,
		}
	}

	createdBy := p.Actor.GetId()
	if p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER {
		createdBy = "owner"
	}

	sess := &sessionv1.Session{
		SessionId:            sessionID,
		MachineId:            e.currentMachineID(),
		ProjectId:            projectID,
		Title:                title,
		TitleRenamed:         titleRenamed,
		TitleLocale:          titleLocale,
		TitleGenerating:      titleGenerating,
		TitleState:           titleState,
		TitleSeq:             titleSeq,
		Status:               status,
		CreatedBy:            createdBy,
		CreatedAs:            strings.TrimSpace(req.Conversation),
		ModelSelection:       sel,
		RuntimeMode:          reqRuntimeMode,
		EffectiveRuntimeMode: effMode,
		MaxRuntimeMode:       maxMode,
		InteractionMode:      imode,
		WorkspaceStrategy:    wsStrategy,
		Actor:                proto.Clone(p.Actor).(*sessionv1.Actor),
		Lineage:              lineage,
		Progress:             &sessionv1.SessionProgress{},
	}

	var initRun *sessionv1.Run
	var res *sessionv1.StartSessionResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		if err := putSessionSynced(tx, sess); err != nil {
			return err
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "session.created",
			Session:   sess,
		}); err != nil {
			return err
		}
		if hasInitial {
			now := timestamppb.Now()
			initRun = &sessionv1.Run{
				RunId:                  newID("run"),
				SessionId:              sessionID,
				Ordinal:                1,
				Status:                 sessionv1.RunStatus_RUN_STATUS_RUNNING,
				Author:                 proto.Clone(p.Actor).(*sessionv1.Actor),
				AuthorConversation:     strings.TrimSpace(req.Conversation),
				Prompt:                 req.InitialPrompt,
				Attachments:            req.Attachments,
				Intent:                 sessionv1.DispatchIntent_DISPATCH_INTENT_START,
				ModelSelection:         proto.Clone(sel).(*sessionv1.ModelSelection),
				RuntimeMode:            effMode,
				InteractionMode:        imode,
				QuestionTimeoutSeconds: req.QuestionTimeoutSeconds,
				StartedAt:              now,
			}
			sess.Progress.TurnCount = 1
			sess.Progress.TurnStartedAt = now
			sess.ActiveRunId = initRun.RunId
			if err := tx.PutRun(initRun); err != nil {
				return err
			}
			if err := tx.SetRunGrantCeiling(initRun.RunId, p.GrantCeiling); err != nil {
				return err
			}
			userItem := &sessionv1.TurnItem{
				ItemId:      newID("itm"),
				SessionId:   sessionID,
				RunId:       initRun.RunId,
				Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
				Status:      "completed",
				Actor:       proto.Clone(p.Actor).(*sessionv1.Actor),
				Text:        req.InitialPrompt,
				Attachments: req.Attachments,
			}
			if err := tx.PutTurnItem(userItem); err != nil {
				return err
			}
			if err := putSessionSynced(tx, sess); err != nil {
				return err
			}
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "item.updated",
				Session:   sess,
				Run:       initRun,
				Item:      userItem,
			}); err != nil {
				return err
			}
		}
		res = &sessionv1.StartSessionResponse{
			Session:    proto.Clone(sess).(*sessionv1.Session),
			InitialRun: initRun,
		}
		return tx.PutReceipt(req.CommandId, "StartSession", res)
	})
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)

	if titleGenerating {
		e.spawnTitleGen(sessionID, titleSeq, titleGenModeInitial, nil)
	}
	if initRun != nil {
		e.spawnRun(sessionID, initRun.RunId, false)
	}
	return res, nil
}

func (e *Engine) DispatchMessage(ctx context.Context, p *Principal, req *sessionv1.DispatchMessageRequest) (*sessionv1.DispatchMessageResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached sessionv1.DispatchMessageResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "DispatchMessage", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("message text or attachment is required"))
	}

	unlock := e.lockSession(req.SessionId)
	sess, err := e.st.Session(req.SessionId)
	if errors.Is(err, store.ErrNotFound) {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		unlock()
		return nil, err
	}
	if !p.canViewSession(sess) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied for session"))
	}
	if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("archived session is read-only; unarchive first"))
	}
	if e.cfg.IsPaused != nil && e.cfg.IsPaused() {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("machine is paused"))
	}

	intent := req.Intent
	if intent == sessionv1.DispatchIntent_DISPATCH_INTENT_UNSPECIFIED {
		intent = sessionv1.DispatchIntent_DISPATCH_INTENT_AUTO
	}

	// Resolve effective selection & modes (applying optional per-dispatch overrides).
	sel := proto.Clone(sess.ModelSelection).(*sessionv1.ModelSelection)
	if req.ModelSelection != nil && req.ModelSelection.InstanceId != "" {
		instCfg, _, err := e.resolveInstanceConfig(p, sess.ProjectId, req.ModelSelection.InstanceId)
		if err != nil {
			unlock()
			return nil, err
		}
		if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
			unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit target provider"))
		}
		sel = proto.Clone(req.ModelSelection).(*sessionv1.ModelSelection)
		if sel.Model == "" && sess.ModelSelection != nil && sess.ModelSelection.InstanceId == sel.InstanceId {
			sel.Model = sess.ModelSelection.Model
		}
	} else if req.ModelSelection != nil && req.ModelSelection.Model != "" {
		if sel == nil {
			sel = &sessionv1.ModelSelection{}
		}
		sel.Model = req.ModelSelection.Model
		if len(req.ModelSelection.Options) > 0 {
			sel.Options = proto.Clone(req.ModelSelection).(*sessionv1.ModelSelection).Options
		}
	}
	if !e.principalAllowsInstance(p, sel.GetInstanceId()) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit this session's provider"))
	}
	if err := e.projectAllowsInstance(sess.ProjectId, sel.GetInstanceId()); err != nil {
		unlock()
		return nil, err
	}
	reqMode := sess.RuntimeMode
	if req.RuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		reqMode = req.RuntimeMode
	}
	effMode := policy.EffectiveMode(e.machineMax(), p.GrantCeiling, reqMode)
	imode := sess.InteractionMode
	if req.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		imode = req.InteractionMode
	}

	isActive := sess.ActiveRunId != "" && (sess.Status == sessionv1.SessionStatus_SESSION_STATUS_RUNNING || sess.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	_, isShellShortcut := ParseShellShortcut(p.Actor, req.Text)

	// Mid-turn STEER path when an active run is running! (Shell shortcuts run in their own turn.)
	if isActive && !isShellShortcut && intent != sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE && intent != sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT {
		lease := e.sup.Get(sess.ActiveProviderSessionId)
		canLiveSteer := sess.Status == sessionv1.SessionStatus_SESSION_STATUS_RUNNING &&
			lease != nil && SelectionsEqual(lease.Selection, sel)
		if canLiveSteer {
			atts := e.loadTurnAttachments(req.Attachments)
			if sErr := lease.Session.Steer(ctx, req.Text, atts); sErr == nil {
				var res *sessionv1.DispatchMessageResponse
				events, mErr := e.st.Mutate(func(tx *store.Tx) error {
					sNow, err := tx.GetSession(sess.SessionId)
					if err != nil {
						return err
					}
					activeRun, err := tx.GetRun(sNow.ActiveRunId)
					if err != nil {
						return err
					}
					item := &sessionv1.TurnItem{
						ItemId:      newID("itm"),
						SessionId:   sNow.SessionId,
						RunId:       activeRun.RunId,
						Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
						Status:      "steered",
						Actor:       proto.Clone(p.Actor).(*sessionv1.Actor),
						Text:        req.Text,
						Attachments: req.Attachments,
					}
					if err := tx.PutTurnItem(item); err != nil {
						return err
					}
					if err := putSessionSynced(tx, sNow); err != nil {
						return err
					}
					if err := tx.AppendEvent(&sessionv1.SessionEvent{
						SessionId: sNow.SessionId,
						Type:      "item.updated",
						Session:   sNow,
						Run:       activeRun,
						Item:      item,
					}); err != nil {
						return err
					}
					res = &sessionv1.DispatchMessageResponse{
						Session:          proto.Clone(sNow).(*sessionv1.Session),
						Run:              proto.Clone(activeRun).(*sessionv1.Run),
						SteeredActiveRun: true,
					}
					return tx.PutReceipt(req.CommandId, "DispatchMessage", res)
				})
				unlock()
				if mErr != nil {
					return nil, mErr
				}
				e.publish(events, nil)
				return res, nil
			}
		}
		if intent == sessionv1.DispatchIntent_DISPATCH_INTENT_STEER && lease != nil {
			_ = lease.Session.Interrupt(ctx)
		}
	}

	// Otherwise, either start immediately (if idle and queue not held) or enqueue!
	var startedRun *sessionv1.Run
	var res *sessionv1.DispatchMessageResponse
	var dispatchTitleSeq uint64
	freshCtx := intent == sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT

	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sess.SessionId)
		if err != nil {
			return err
		}
		if sNow.Progress == nil {
			sNow.Progress = &sessionv1.SessionProgress{}
		}
		if !sNow.TitleRenamed && sNow.TitleState == sessionv1.TitleState_TITLE_STATE_SEED && !sNow.TitleGenerating && sNow.GetLineage().GetRelationship() != "subagent" {
			loc := runtime.ResolveLocale(req.Locale, sNow.TitleLocale)
			sNow.TitleLocale = loc
			isFork := sNow.GetLineage().GetRelationship() == "fork"
			seed, isFinal := runtime.DeriveSeedTitle(req.Text, req.Attachments, loc)
			if !isFork {
				sNow.Title = seed
			}
			sNow.TitleSeq++
			if !isFork && (isFinal || !e.hasTitleGenerator()) {
				sNow.TitleState = sessionv1.TitleState_TITLE_STATE_FINAL
			} else if e.hasTitleGenerator() {
				sNow.TitleGenerating = true
				dispatchTitleSeq = sNow.TitleSeq
			} else {
				sNow.Title = seed
				sNow.TitleState = sessionv1.TitleState_TITLE_STATE_FINAL
			}
		}
		if sNow.ActiveRunId == "" {
			sNow.ModelSelection = sel
			sNow.RuntimeMode = reqMode
			sNow.EffectiveRuntimeMode = effMode
			sNow.InteractionMode = imode
		}

		queued, err := tx.QueuedRuns(sNow.SessionId)
		if err != nil {
			return err
		}
		queueIntent := intent == sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE
		shouldQueue := sNow.ActiveRunId != "" ||
			(sNow.QueueHeld && (len(queued) > 0 || queueIntent)) ||
			(queueIntent && len(queued) > 0)

		if shouldQueue {
			qOrder := 1000.0
			if len(queued) > 0 {
				if intent == sessionv1.DispatchIntent_DISPATCH_INTENT_STEER {
					qOrder = queued[0].QueueOrder - 1000.0
				} else {
					qOrder = queued[len(queued)-1].QueueOrder + 1000.0
				}
			}
			qRun := &sessionv1.Run{
				RunId:                  newID("run"),
				SessionId:              sNow.SessionId,
				Ordinal:                0,
				Status:                 sessionv1.RunStatus_RUN_STATUS_QUEUED,
				QueueOrder:             qOrder,
				Author:                 proto.Clone(p.Actor).(*sessionv1.Actor),
				AuthorConversation:     strings.TrimSpace(req.Conversation),
				Prompt:                 req.Text,
				Attachments:            req.Attachments,
				Intent:                 intent,
				ModelSelection:         proto.Clone(sel).(*sessionv1.ModelSelection),
				RuntimeMode:            effMode,
				InteractionMode:        imode,
				QuestionTimeoutSeconds: req.QuestionTimeoutSeconds,
			}
			if err := tx.PutRun(qRun); err != nil {
				return err
			}
			if err := tx.SetRunGrantCeiling(qRun.RunId, p.GrantCeiling); err != nil {
				return err
			}
			sNow.QueuedRunCount = uint32(len(queued) + 1)
			if sNow.ActiveRunId == "" {
				sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_QUEUED
			}
			if err := putSessionSynced(tx, sNow); err != nil {
				return err
			}
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sNow.SessionId,
				Type:      "run.queued",
				Session:   sNow,
				Run:       qRun,
			}); err != nil {
				return err
			}
			res = &sessionv1.DispatchMessageResponse{
				Session: proto.Clone(sNow).(*sessionv1.Session),
				Run:     proto.Clone(qRun).(*sessionv1.Run),
			}
			return tx.PutReceipt(req.CommandId, "DispatchMessage", res)
		}

		supersededReq, supersededItem, err := dismissPendingRequestTx(tx, sNow, p.Actor)
		if err != nil {
			return err
		}

		now := timestamppb.Now()
		sNow.QueueHeld = false
		sNow.Error = ""
		sNow.Progress.LastFailure = nil
		sNow.Progress.TurnCount++
		sNow.Progress.TurnStartedAt = now
		startedRun = &sessionv1.Run{
			RunId:                  newID("run"),
			SessionId:              sNow.SessionId,
			Ordinal:                sNow.Progress.TurnCount,
			Status:                 sessionv1.RunStatus_RUN_STATUS_RUNNING,
			Author:                 proto.Clone(p.Actor).(*sessionv1.Actor),
			AuthorConversation:     strings.TrimSpace(req.Conversation),
			Prompt:                 req.Text,
			Attachments:            req.Attachments,
			Intent:                 intent,
			ModelSelection:         proto.Clone(sel).(*sessionv1.ModelSelection),
			RuntimeMode:            effMode,
			InteractionMode:        imode,
			QuestionTimeoutSeconds: req.QuestionTimeoutSeconds,
			StartedAt:              now,
		}
		sNow.ActiveRunId = startedRun.RunId
		sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
		if err := tx.PutRun(startedRun); err != nil {
			return err
		}
		if err := tx.SetRunGrantCeiling(startedRun.RunId, p.GrantCeiling); err != nil {
			return err
		}
		userItem := &sessionv1.TurnItem{
			ItemId:      newID("itm"),
			SessionId:   sNow.SessionId,
			RunId:       startedRun.RunId,
			Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
			Status:      "completed",
			Actor:       proto.Clone(p.Actor).(*sessionv1.Actor),
			Text:        req.Text,
			Attachments: req.Attachments,
		}
		if err := tx.PutTurnItem(userItem); err != nil {
			return err
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		if supersededReq != nil {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sNow.SessionId,
				Type:      "request.resolved",
				Session:   sNow,
				Item:      supersededItem,
				Request:   supersededReq,
			}); err != nil {
				return err
			}
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sNow.SessionId,
			Type:      "item.updated",
			Session:   sNow,
			Run:       startedRun,
			Item:      userItem,
		}); err != nil {
			return err
		}
		res = &sessionv1.DispatchMessageResponse{
			Session: proto.Clone(sNow).(*sessionv1.Session),
			Run:     proto.Clone(startedRun).(*sessionv1.Run),
		}
		return tx.PutReceipt(req.CommandId, "DispatchMessage", res)
	})
	unlock()
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	if dispatchTitleSeq > 0 {
		e.spawnTitleGen(sess.SessionId, dispatchTitleSeq, titleGenModeInitial, nil)
	}
	if startedRun != nil {
		e.spawnRun(sess.SessionId, startedRun.RunId, freshCtx)
	}
	return res, nil
}

// DeliverMessage handles SessionService.DeliverMessage for signed inter-agent mail.
func (e *Engine) DeliverMessage(ctx context.Context, p *Principal, req *sessionv1.DeliverMessageRequest) (*sessionv1.DeliverMessageResponse, error) {
	if e.cfg.OnDeliverMessage != nil {
		return e.cfg.OnDeliverMessage(ctx, p, req)
	}
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("mail delivery is not configured on this engine"))
}

// DeliverInboundMail records a verified inbound agent/Her mail item on sessionID and answers, steers, queues, or starts a turn.
func (e *Engine) DeliverInboundMail(ctx context.Context, p *Principal, sessionID, msgID, senderConv, fromAddr, fromName, inReplyTo, promptText, rawText string, attachments []*sessionv1.AttachmentRef) (*sessionv1.Session, string, error) {
	if p == nil {
		return nil, "", connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	unlock := e.lockSession(sessionID)
	sess, err := e.st.Session(sessionID)
	if errors.Is(err, store.ErrNotFound) {
		unlock()
		return nil, "", connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		unlock()
		return nil, "", err
	}
	if !p.canViewSession(sess) {
		unlock()
		return nil, "", connect.NewError(connect.CodePermissionDenied, errors.New("permission denied for session"))
	}
	if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		unlock()
		return nil, "", connect.NewError(connect.CodeFailedPrecondition, errors.New("archived session cannot receive messages"))
	}
	if e.cfg.IsPaused != nil && e.cfg.IsPaused() {
		unlock()
		return nil, "", connect.NewError(connect.CodeUnavailable, errors.New("this machine is paused"))
	}

	senderActor := proto.Clone(p.Actor).(*sessionv1.Actor)
	if senderActor.Label == "" {
		if fromName != "" {
			senderActor.Label = fromName
		} else {
			senderActor.Label = fromAddr
		}
	}
	if promptText == "" {
		promptText = fmt.Sprintf("[Message from %s (%s)]\n%s", senderActor.Label, fromAddr, rawText)
	}

	// 1. Answer pending USER_INPUT question if this sender is authorized to answer it!
	if sess.PendingRequestId != "" && sess.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
		reqs, _ := e.st.RuntimeRequests(sessionID)
		var pendingReq *sessionv1.RuntimeRequest
		for _, r := range reqs {
			if r.RequestId == sess.PendingRequestId && r.Status == sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
				pendingReq = r
				break
			}
		}
		if pendingReq != nil && pendingReq.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
			activeRun, _ := e.st.Run(pendingReq.RunId)
			if e.canAnswerQuestion(p, sess, activeRun) {
				ansMap := map[string]*sessionv1.QuestionAnswer{}
				if qs := pendingReq.GetUserInput().GetQuestions(); len(qs) > 0 {
					ansMap[qs[0].Id] = &sessionv1.QuestionAnswer{CustomText: rawText, Attachments: attachments}
				} else {
					ansMap["q1"] = &sessionv1.QuestionAnswer{CustomText: rawText, Attachments: attachments}
				}
				evs, mErr := e.st.Mutate(func(tx *store.Tx) error {
					sNow, err := tx.GetSession(sessionID)
					if err != nil {
						return err
					}
					item := &sessionv1.TurnItem{
						ItemId:        newID("mail"),
						SessionId:     sessionID,
						RunId:         pendingReq.RunId,
						Kind:          sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED,
						Status:        "completed",
						Actor:         senderActor,
						Text:          rawText,
						Attachments:   attachments,
						MailMessageId: msgID,
						MailAddress:   fromAddr,
						MailName:      fromName,
						MailDelivered: "answer",
						MailInReplyTo: inReplyTo,
					}
					if err := tx.PutTurnItem(item); err != nil {
						return err
					}
					if err := putSessionSynced(tx, sNow); err != nil {
						return err
					}
					return tx.AppendEvent(&sessionv1.SessionEvent{
						SessionId: sessionID,
						Type:      "item.updated",
						Session:   sNow,
						Item:      item,
					})
				})
				unlock()
				if mErr != nil {
					return nil, "", mErr
				}
				e.publish(evs, nil)
				res, rErr := e.RespondRuntimeRequest(ctx, p, &sessionv1.RespondRuntimeRequestRequest{
					SessionId: sessionID,
					RequestId: pendingReq.RequestId,
					Answers:   ansMap,
				})
				if rErr != nil {
					return nil, "", rErr
				}
				return res.Session, "answer", nil
			}
		}
	}

	// 2. Live steer if currently running and this sender owns the active turn!
	if sess.ActiveRunId != "" && sess.Status == sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
		activeRun, _ := e.st.Run(sess.ActiveRunId)
		sameAsker := activeRun != nil && activeRun.GetAuthor().GetId() == senderActor.GetId()
		if sameAsker {
			if lease := e.sup.Get(sess.ActiveProviderSessionId); lease != nil {
				if sErr := lease.Session.Steer(ctx, promptText, e.loadTurnAttachments(attachments)); sErr == nil {
					var updated *sessionv1.Session
					events, mErr := e.st.Mutate(func(tx *store.Tx) error {
						sNow, err := tx.GetSession(sessionID)
						if err != nil {
							return err
						}
						item := &sessionv1.TurnItem{
							ItemId:        newID("mail"),
							SessionId:     sessionID,
							RunId:         sNow.ActiveRunId,
							Kind:          sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED,
							Status:        "completed",
							Actor:         senderActor,
							Text:          rawText,
							Attachments:   attachments,
							MailMessageId: msgID,
							MailAddress:   fromAddr,
							MailName:      fromName,
							MailDelivered: "joined",
							MailInReplyTo: inReplyTo,
						}
						if err := tx.PutTurnItem(item); err != nil {
							return err
						}
						if err := putSessionSynced(tx, sNow); err != nil {
							return err
						}
						updated = proto.Clone(sNow).(*sessionv1.Session)
						return tx.AppendEvent(&sessionv1.SessionEvent{
							SessionId: sessionID,
							Type:      "item.updated",
							Session:   sNow,
							Item:      item,
						})
					})
					unlock()
					if mErr != nil {
						return nil, "", mErr
					}
					e.publish(events, nil)
					return updated, "joined", nil
				}
			}
		}
	}

	// 3. Otherwise either enqueue (if busy/held) or start a turn on the idle session!
	var startedRun *sessionv1.Run
	var updated *sessionv1.Session
	var delivered string
	effMode := policy.EffectiveMode(e.machineMax(), p.GrantCeiling, sess.RuntimeMode)
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sNow.Progress == nil {
			sNow.Progress = &sessionv1.SessionProgress{}
		}
		queued, _ := tx.QueuedRuns(sessionID)
		if sNow.ActiveRunId != "" || sNow.QueueHeld || len(queued) > 0 {
			delivered = "queued"
			qOrder := 1000.0
			if len(queued) > 0 {
				qOrder = queued[len(queued)-1].QueueOrder + 1000.0
			}
			qRun := &sessionv1.Run{
				RunId:              newID("run"),
				SessionId:          sessionID,
				Ordinal:            0,
				Status:             sessionv1.RunStatus_RUN_STATUS_QUEUED,
				QueueOrder:         qOrder,
				Author:             senderActor,
				AuthorConversation: senderConv,
				Prompt:             promptText,
				Attachments:        attachments,
				Intent:             sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
				ModelSelection:     proto.Clone(sNow.ModelSelection).(*sessionv1.ModelSelection),
				RuntimeMode:        effMode,
				InteractionMode:    sNow.InteractionMode,
			}
			if err := tx.PutRun(qRun); err != nil {
				return err
			}
			if err := tx.SetRunGrantCeiling(qRun.RunId, p.GrantCeiling); err != nil {
				return err
			}
			mailItem := &sessionv1.TurnItem{
				ItemId:        newID("mail"),
				SessionId:     sessionID,
				RunId:         qRun.RunId,
				Kind:          sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED,
				Status:        "completed",
				Actor:         senderActor,
				Text:          rawText,
				Attachments:   attachments,
				MailMessageId: msgID,
				MailAddress:   fromAddr,
				MailName:      fromName,
				MailDelivered: delivered,
				MailInReplyTo: inReplyTo,
			}
			if err := tx.PutTurnItem(mailItem); err != nil {
				return err
			}
			sNow.QueuedRunCount = uint32(len(queued) + 1)
			if sNow.ActiveRunId == "" {
				sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_QUEUED
			}
			if err := putSessionSynced(tx, sNow); err != nil {
				return err
			}
			updated = proto.Clone(sNow).(*sessionv1.Session)
			_ = tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "item.updated",
				Session:   sNow,
				Run:       qRun,
				Item:      mailItem,
			})
			return tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "run.queued",
				Session:   sNow,
				Run:       qRun,
			})
		}

		delivered = "turn"
		supReq, supItem, dErr := dismissPendingRequestTx(tx, sNow, senderActor)
		if dErr != nil {
			return dErr
		}
		now := timestamppb.Now()
		sNow.Error = ""
		sNow.Progress.LastFailure = nil
		sNow.Progress.TurnCount++
		sNow.Progress.TurnStartedAt = now
		startedRun = &sessionv1.Run{
			RunId:              newID("run"),
			SessionId:          sessionID,
			Ordinal:            sNow.Progress.TurnCount,
			Status:             sessionv1.RunStatus_RUN_STATUS_RUNNING,
			Author:             senderActor,
			AuthorConversation: senderConv,
			Prompt:             promptText,
			Attachments:        attachments,
			Intent:             sessionv1.DispatchIntent_DISPATCH_INTENT_START,
			ModelSelection:     proto.Clone(sNow.ModelSelection).(*sessionv1.ModelSelection),
			RuntimeMode:        effMode,
			InteractionMode:    sNow.InteractionMode,
			StartedAt:          now,
		}
		sNow.ActiveRunId = startedRun.RunId
		sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
		sNow.EffectiveRuntimeMode = effMode
		if err := tx.PutRun(startedRun); err != nil {
			return err
		}
		if err := tx.SetRunGrantCeiling(startedRun.RunId, p.GrantCeiling); err != nil {
			return err
		}
		mailItem := &sessionv1.TurnItem{
			ItemId:        newID("mail"),
			SessionId:     sessionID,
			RunId:         startedRun.RunId,
			Kind:          sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED,
			Status:        "completed",
			Actor:         senderActor,
			Text:          rawText,
			Attachments:   attachments,
			MailMessageId: msgID,
			MailAddress:   fromAddr,
			MailName:      fromName,
			MailDelivered: delivered,
			MailInReplyTo: inReplyTo,
		}
		if err := tx.PutTurnItem(mailItem); err != nil {
			return err
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		updated = proto.Clone(sNow).(*sessionv1.Session)
		if supReq != nil {
			_ = tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "request.resolved",
				Session:   sNow,
				Item:      supItem,
				Request:   supReq,
			})
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "item.updated",
			Session:   sNow,
			Run:       startedRun,
			Item:      mailItem,
		})
	})
	unlock()
	if err != nil {
		return nil, "", err
	}
	e.publish(events, nil)
	if startedRun != nil {
		e.spawnRun(sessionID, startedRun.RunId, false)
	}
	return updated, delivered, nil
}

// RecordSentMail appends a TURN_ITEM_KIND_MESSAGE_SENT item when a session sends mail via herlink/MCP.
func (e *Engine) RecordSentMail(sessionID, msgID, toAddr, toName, inReplyTo, body, delivered string) {
	evs, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		it := &sessionv1.TurnItem{
			ItemId:        newID("sent"),
			SessionId:     sessionID,
			RunId:         sNow.ActiveRunId,
			Kind:          sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_SENT,
			Status:        "completed",
			Text:          body,
			MailMessageId: msgID,
			MailAddress:   toAddr,
			MailName:      toName,
			MailDelivered: delivered,
			MailInReplyTo: inReplyTo,
		}
		if err := tx.PutTurnItem(it); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "item.updated",
			Session:   sNow,
			Item:      it,
		})
	})
	if err == nil {
		e.publish(evs, nil)
	}
}

// ---------------------------------------------------------------------------
// Core Run Execution Loop (TransitionPolicy + Checkpoints + RuntimeRequest Gate + Queue Drain)

func (e *Engine) resolveWorkdir(sess *sessionv1.Session) (string, error) {
	return e.resolveWorkdirCtx(e.bgCtx, sess)
}

func (e *Engine) resolveWorkdirCtx(ctx context.Context, sess *sessionv1.Session) (string, error) {
	ws, err := e.st.Workspace(sess.ProjectId)
	if err != nil {
		return "", err
	}
	if sess.GetWorkspaceStrategy().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE {
		wtPath := sess.GetWorkspaceStrategy().GetWorktreePath()
		if wtPath != "" {
			if st, err := os.Stat(wtPath); err == nil && st.IsDir() && vcs.IsGitRepo(ctx, wtPath) {
				return wtPath, nil
			}
		}
		targetWTDir := filepath.Join(e.vcs.WorktreesRoot(), sess.SessionId)
		_, statErr := os.Stat(targetWTDir)
		needsRestore := os.IsNotExist(statErr) || !vcs.IsGitRepo(ctx, targetWTDir)

		baseRef := sess.GetWorkspaceStrategy().GetBaseRef()
		restored, branch, err := e.vcs.PrepareWorktree(ctx, ws.Path, sess.SessionId, baseRef, sess.GetWorkspaceStrategy().GetBranch())
		var baseErr *vcs.BaseRefError
		if errors.As(err, &baseErr) && !sess.GetWorkspaceStrategy().GetFromRemote() {
			fetchStart := time.Now()
			if localRef, remote, fErr := e.vcs.FetchRef(ctx, ws.Path, baseRef); fErr == nil {
				e.log.Info("fetched the base ref", "session", sess.SessionId, "ref", baseRef, "remote", remote, "took", time.Since(fetchStart).Round(time.Millisecond))
				restored, branch, err = e.vcs.PrepareWorktree(ctx, ws.Path, sess.SessionId, localRef, sess.GetWorkspaceStrategy().GetBranch())
			} else {
				err = fErr
			}
		}
		if err != nil {
			if errors.As(err, &baseErr) {
				return "", runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_BASE_REF_UNAVAILABLE,
					fmt.Sprintf("Cannot create the worktree for project %q: base ref %q does not resolve to a commit in local repository %q%s.", sess.ProjectId, baseErr.Ref, ws.Path, baseErr.Upstream()))
			}
			return "", err
		}
		if needsRestore {
			if ckpts, cErr := e.st.Checkpoints(sess.SessionId); cErr == nil && len(ckpts) > 0 {
				if latest := ckpts[len(ckpts)-1]; latest.GitTreeOid != "" {
					_ = e.vcs.RestoreTree(ctx, restored, latest.GitTreeOid)
				}
			}
		}
		_, _ = e.st.Mutate(func(tx *store.Tx) error {
			sNow, err := tx.GetSession(sess.SessionId)
			if err != nil {
				return err
			}
			if sNow.WorkspaceStrategy == nil {
				sNow.WorkspaceStrategy = &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE}
			}
			sNow.WorkspaceStrategy.WorktreePath = restored
			sNow.WorkspaceStrategy.Branch = branch
			return putSessionSynced(tx, sNow)
		})
		return restored, nil
	}
	return ws.Path, nil
}

// workspaceErrorCode is a run's error code when its folder could not be set up.
const workspaceErrorCode = "workspace_error"

// workdirForRun is the folder a run works in. A session's first turn sets it up and says so
// in the conversation (prepareWorkspace), as does the turn after one whose setup failed; a
// later turn finds it (resolveWorkdir), a released worktree restored.
func (e *Engine) workdirForRun(ctx context.Context, sess *sessionv1.Session, run *sessionv1.Run, runs []*sessionv1.Run) (string, error) {
	first := run.Ordinal == 1
	for _, r := range runs {
		if r.Ordinal+1 == run.Ordinal && r.ErrorCode == workspaceErrorCode {
			first = true
		}
	}
	if !first {
		return e.resolveWorkdir(sess)
	}
	return e.prepareWorkspace(ctx, sess, run.RunId)
}

// prepareWorkspace sets a session's folder up as its owner chose: a worktree made (from its
// base fetched from origin first, if asked), a branch switched to in the folder itself, or
// the folder kept as it is. It is one WORKSPACE item of the run, running while a step takes,
// then completed or failed; a run stopped meanwhile leaves it running, to be settled with
// the run. A folder that is not a git repository is used as it is, with no item.
func (e *Engine) prepareWorkspace(ctx context.Context, sess *sessionv1.Session, runID string) (string, error) {
	ws, err := e.st.Workspace(sess.ProjectId)
	if err != nil {
		return "", err
	}
	strat := sess.GetWorkspaceStrategy()
	worktree := strat.GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE
	if !worktree && !vcs.IsGitRepo(ctx, ws.Path) {
		return ws.Path, nil
	}
	step := &sessionv1.WorkspaceStep{Mode: strat.GetMode(), Branch: strat.GetBranch(), BaseRef: strat.GetBaseRef(), Path: ws.Path}
	item := &sessionv1.TurnItem{
		ItemId:    newID("wsp"),
		SessionId: sess.SessionId,
		RunId:     runID,
		Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_WORKSPACE,
		Workspace: step,
	}
	put := func(status string) {
		item.Status = status
		evs, _ := e.st.Mutate(func(tx *store.Tx) error {
			if err := tx.PutTurnItem(item); err != nil {
				return err
			}
			return tx.AppendEvent(&sessionv1.SessionEvent{SessionId: sess.SessionId, Type: "item.updated", Item: item})
		})
		e.publish(evs, nil)
	}
	fail := func(err error) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		f := runtime.FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED)
		item.Failure = f.Proto()
		item.Text = item.Failure.Message // redacted, as it goes to every device
		put("failed")
		return "", runtime.Fail(f.Kind, f.Message)
	}

	if !worktree {
		cur := vcs.CurrentBranch(ctx, ws.Path)
		if step.Branch == "" || step.Branch == cur {
			step.Action = sessionv1.WorkspaceAction_WORKSPACE_ACTION_KEEP
			step.Branch = cur
			put("completed")
			return ws.Path, nil
		}
		step.Action = sessionv1.WorkspaceAction_WORKSPACE_ACTION_SWITCH
		step.PreviousBranch = cur
		put("running")
		if e.busyInPlace(sess.ProjectId, sess.SessionId) {
			return fail(fmt.Errorf("a session is working in %s now; switch branches once it is done", ws.Path))
		}
		if err := e.vcs.SwitchBranch(ctx, ws.Path, step.Branch); err != nil {
			return fail(err)
		}
		put("completed")
		return ws.Path, nil
	}

	base := strat.GetBaseRef()
	if strat.GetFromRemote() && !strings.HasPrefix(base, "origin/") {
		step.Action = sessionv1.WorkspaceAction_WORKSPACE_ACTION_FETCH
		put("running")
		fetched, err := vcs.FetchBase(ctx, ws.Path, base)
		if err != nil {
			return fail(err)
		}
		base = fetched
		step.BaseRef = base
	}
	step.Action = sessionv1.WorkspaceAction_WORKSPACE_ACTION_CREATE
	put("running")
	wtPath, branch, err := e.vcs.PrepareWorktree(ctx, ws.Path, sess.SessionId, base, strat.GetBranch())
	var baseErr *vcs.BaseRefError
	if errors.As(err, &baseErr) && !strat.GetFromRemote() {
		fetchStart := time.Now()
		if localRef, remote, fErr := e.vcs.FetchRef(ctx, ws.Path, base); fErr == nil {
			e.log.Info("fetched the base ref", "session", sess.SessionId, "ref", base, "remote", remote, "took", time.Since(fetchStart).Round(time.Millisecond))
			wtPath, branch, err = e.vcs.PrepareWorktree(ctx, ws.Path, sess.SessionId, localRef, strat.GetBranch())
		} else {
			err = fErr
		}
	}
	if errors.As(err, &baseErr) {
		return fail(runtime.Fail(sessionv1.FailureKind_FAILURE_KIND_BASE_REF_UNAVAILABLE,
			fmt.Sprintf("Cannot create the worktree for project %q: base ref %q does not resolve to a commit in local repository %q%s.", sess.ProjectId, baseErr.Ref, ws.Path, baseErr.Upstream())))
	}
	if err != nil {
		return fail(err)
	}
	step.Branch, step.Path = branch, wtPath
	_, _ = e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sess.SessionId)
		if err != nil {
			return err
		}
		if sNow.WorkspaceStrategy == nil {
			sNow.WorkspaceStrategy = &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE}
		}
		sNow.WorkspaceStrategy.WorktreePath = wtPath
		sNow.WorkspaceStrategy.Branch = branch
		sNow.WorkspaceStrategy.BaseRef = base
		return putSessionSynced(tx, sNow)
	})
	put("completed")
	return wtPath, nil
}

func (e *Engine) loadTurnAttachments(refs []*sessionv1.AttachmentRef) []runtime.TurnAttachment {
	var out []runtime.TurnAttachment
	for _, r := range refs {
		data, name, mime, err := e.st.GetBlob(r.BlobSha256)
		if err != nil {
			continue
		}
		if r.Name != "" {
			name = r.Name
		}
		if r.MimeType != "" {
			mime = r.MimeType
		}
		out = append(out, runtime.TurnAttachment{
			Name:     name,
			MimeType: mime,
			Path:     e.st.BlobPath(r.BlobSha256),
			Data:     data,
		})
	}
	return out
}

func (e *Engine) buildStartSpec(sess *sessionv1.Session, workdir string, instCfg *sessionv1.ProviderInstanceConfig, curRun *sessionv1.Run, resumeCursor string) runtime.StartSpec {
	spec := runtime.StartSpec{
		SessionID:       sess.SessionId,
		Workdir:         workdir,
		Title:           sess.Title,
		Instance:        instCfg,
		Selection:       curRun.ModelSelection,
		EffectiveMode:   curRun.RuntimeMode,
		InteractionMode: curRun.InteractionMode,
		ResumeCursor:    resumeCursor,
		Gate:            e.makeGate(sess.SessionId, workdir),
	}
	if e.cfg.MCPServers != nil {
		spec.MCPServers = e.cfg.MCPServers(sess.SessionId)
	}
	if e.cfg.OnLimits != nil {
		spec.Limits = func(l runtime.Limits) { e.cfg.OnLimits(sess, l) }
	}
	if e.cfg.ReadDirs != nil {
		spec.ReadDirs = e.cfg.ReadDirs(sess.ProjectId)
	}
	if e.cfg.ExtraEnv != nil {
		spec.ExtraEnv = e.cfg.ExtraEnv(sess.SessionId)
	}
	if e.cfg.DataDir != "" {
		tmp := filepath.Join(e.cfg.DataDir, "sessions", sess.SessionId, "tmp")
		_ = os.MkdirAll(tmp, 0o700)
		if spec.ExtraEnv == nil {
			spec.ExtraEnv = map[string]string{}
		}
		for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
			if _, ok := spec.ExtraEnv[k]; !ok {
				spec.ExtraEnv[k] = tmp
			}
		}
	}
	return spec
}

func (e *Engine) driveRun(runCtx context.Context, sessionID, runID string, freshContext bool) {
	if runCtx.Err() != nil {
		e.finishRunInterrupted(sessionID, runID, "interrupted", nil, nil)
		return
	}

	sess, err := e.st.Session(sessionID)
	if err != nil {
		return
	}
	runs, _ := e.st.Runs(sessionID)
	var curRun *sessionv1.Run
	for _, r := range runs {
		if r.RunId == runID {
			curRun = r
			break
		}
	}
	if curRun == nil || curRun.Status != sessionv1.RunStatus_RUN_STATUS_RUNNING {
		return
	}

	workdir, err := e.workdirForRun(runCtx, sess, curRun, runs)
	if err != nil {
		if runCtx.Err() != nil {
			if e.bgCtx.Err() == nil {
				e.finishRunInterrupted(sessionID, runID, "interrupted", nil, nil)
			}
			return
		}
		e.finishRunFailed(sessionID, runID, workspaceErrorCode, err.Error(), runtime.FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED))
		return
	}

	// The baseline checkpoint (ordinal 0) before the very first run, so even run 1 can be
	// rewound: taken while the agent starts, and waited for before the turn is attached.
	existingCkpts, _ := e.st.Checkpoints(sessionID)
	prevTreeOID := ""
	baseline := make(chan struct{})
	if len(existingCkpts) == 0 {
		go func() {
			defer close(baseline)
			if baseCp, cpErr := e.vcs.CaptureCheckpoint(runCtx, workdir, newID("ckpt"), sessionID, "", 0, ""); cpErr == nil && baseCp != nil {
				prevTreeOID = baseCp.GitTreeOid
				_, _ = e.st.Mutate(func(tx *store.Tx) error { return tx.PutCheckpoint(baseCp) })
			}
		}()
		defer func() { <-baseline }() // a run that ends early still waits for it, so nothing outlives the run
	} else {
		prevTreeOID = existingCkpts[len(existingCkpts)-1].GitTreeOid
		close(baseline)
	}

	runPrompt := curRun.Prompt
	if shellCmd, isShell := ParseShellShortcut(curRun.GetAuthor(), curRun.Prompt); isShell {
		<-baseline
		var interrupted bool
		runPrompt, interrupted = e.execOwnerShellCommand(runCtx, sess, curRun, workdir, prevTreeOID, shellCmd)
		if interrupted {
			return
		}
	}

	instCfg, drv, err := e.resolveInstanceConfig(nil, sess.ProjectId, curRun.ModelSelection.GetInstanceId())
	if err != nil {
		e.finishRunFailed(sessionID, runID, "provider_unavailable", err.Error(), &runtime.Failure{
			Kind:    sessionv1.FailureKind_FAILURE_KIND_START_FAILED,
			Message: err.Error(),
		})
		return
	}
	caps := drv.Capabilities(instCfg.AcpFlavor)

	var currentPS *sessionv1.ProviderSession
	if sess.ActiveProviderSessionId != "" {
		pss, _ := e.st.ProviderSessions(sessionID)
		for _, ps := range pss {
			if ps.ProviderSessionId == sess.ActiveProviderSessionId {
				currentPS = ps
				break
			}
		}
	}
	var liveLease *runtime.LeaseEntry
	if currentPS != nil {
		liveLease = e.sup.Get(currentPS.ProviderSessionId)
	}

	plan := DecideTransition(TransitionInput{
		CurrentBinding:      currentPS,
		LiveLease:           liveLease,
		NextSelection:       curRun.ModelSelection,
		NextEffectiveMode:   curRun.RuntimeMode,
		NextInteractionMode: curRun.InteractionMode,
		FreshContext:        freshContext,
		Capabilities:        caps,
	})

	var handoffPrefix string
	var activeLease *runtime.LeaseEntry
	var activePS *sessionv1.ProviderSession

	switch plan.Action {
	case TransitionReuseInPlace:
		activeLease = liveLease
		activePS = currentPS

	case TransitionSwitchInSession:
		prevSel := proto.Clone(liveLease.Selection).(*sessionv1.ModelSelection)
		if plan.ModelChanged || plan.OptionsChanged {
			if err := liveLease.Session.SetSelection(runCtx, curRun.ModelSelection); err != nil {
				e.sup.Detach(currentPS.ProviderSessionId, false)
				liveLease = nil
			} else {
				liveLease.Selection = proto.Clone(curRun.ModelSelection).(*sessionv1.ModelSelection)
			}
		}
		if liveLease != nil && plan.ModeChanged {
			if err := liveLease.Session.SetModes(runCtx, curRun.RuntimeMode, curRun.InteractionMode); err != nil {
				e.stopLeasePump(currentPS.ProviderSessionId, liveLease)
				e.sup.Detach(currentPS.ProviderSessionId, false)
				e.settleSessionBackgroundTasks(sessionID, currentPS.ProviderSessionId, "provider session replaced")
				liveLease = nil
			} else {
				liveLease.RuntimeMode = curRun.RuntimeMode
				liveLease.InteractionMode = curRun.InteractionMode
			}
		}
		if liveLease != nil {
			activeLease = liveLease
			activePS = currentPS
			if plan.ModelChanged || plan.OptionsChanged {
				e.recordSwitchItem(sessionID, runID, prevSel, curRun.ModelSelection, "", "")
			}
		}
	}

	if activeLease == nil {
		if runCtx.Err() != nil {
			if e.bgCtx.Err() == nil {
				e.finishRunInterrupted(sessionID, runID, "interrupted", nil, nil)
			}
			return
		}
		if currentPS != nil {
			e.stopLeasePump(currentPS.ProviderSessionId, nil)
			e.sup.Detach(currentPS.ProviderSessionId, false)
		}
		e.settleSessionBackgroundTasks(sessionID, "", "provider session replaced")
		buildHandoff := func() string {
			if freshContext {
				return ""
			}
			priorItems, _ := e.st.TurnItems(sessionID)
			var historyItems []*sessionv1.TurnItem
			for _, it := range priorItems {
				if it.RunId != runID {
					historyItems = append(historyItems, it)
				}
			}
			return BuildContextHandoff(historyItems, existingCkpts, defaultHandoffBudgetChars)
		}

		isPriorUnstarted := func(pr *sessionv1.Run) bool {
			if pr.RunId == runID || pr.ProviderSessionId != "" || strings.TrimSpace(pr.Prompt) == "" {
				return false
			}
			if pr.Ordinal == 0 || pr.Ordinal >= curRun.Ordinal {
				return false
			}
			if pr.Status == sessionv1.RunStatus_RUN_STATUS_FAILED {
				return true
			}
			return pr.Status == sessionv1.RunStatus_RUN_STATUS_INTERRUPTED && !strings.HasPrefix(pr.ErrorMessage, "steered by ")
		}

		resumeCursor := ""
		var unstartedPrompts []string
		if currentPS == nil && !freshContext {
			for _, pr := range runs {
				if isPriorUnstarted(pr) {
					unstartedPrompts = append(unstartedPrompts, pr.Prompt)
				}
			}
		}
		if plan.Action == TransitionSpawnAndResume && currentPS != nil {
			resumeCursor = currentPS.ResumeCursor
			activePS = currentPS
		} else {
			if len(unstartedPrompts) == 0 {
				handoffPrefix = buildHandoff()
			}
			activePS = &sessionv1.ProviderSession{
				ProviderSessionId: newID("ps"),
				SessionId:         sessionID,
				InstanceId:        instCfg.InstanceId,
				Driver:            instCfg.Driver,
				ProcessAlive:      true,
			}
		}

		drvSess, startErr := drv.StartSession(e.bgCtx, e.buildStartSpec(sess, workdir, instCfg, curRun, resumeCursor))
		if startErr != nil && resumeCursor != "" && runCtx.Err() == nil && e.bgCtx.Err() == nil {
			// Native session cursor expired or cannot be resumed; self-heal by spawning a
			// fresh ProviderSession carrying the Hermesh transcript handoff.
			if currentPS != nil {
				_, _ = e.st.Mutate(func(tx *store.Tx) error {
					if ps, err := tx.GetProviderSession(currentPS.ProviderSessionId); err == nil {
						ps.ProcessAlive = false
						_ = tx.PutProviderSession(ps)
					}
					return nil
				})
			}
			handoffPrefix = buildHandoff()
			plan.HandoffStrategy = "transcript_pack"
			activePS = &sessionv1.ProviderSession{
				ProviderSessionId: newID("ps"),
				SessionId:         sessionID,
				InstanceId:        instCfg.InstanceId,
				Driver:            instCfg.Driver,
				ProcessAlive:      true,
			}
			drvSess, startErr = drv.StartSession(e.bgCtx, e.buildStartSpec(sess, workdir, instCfg, curRun, ""))
		}
		if startErr != nil {
			e.finishRunFailed(sessionID, runID, "session_start_failed", startErr.Error(), runtime.FailureOf(startErr, sessionv1.FailureKind_FAILURE_KIND_START_FAILED))
			return
		}
		if runCtx.Err() != nil {
			_ = drvSess.ForceStop()
			if e.bgCtx.Err() == nil {
				e.finishRunInterrupted(sessionID, runID, "interrupted", nil, nil)
			}
			return
		}
		activePS.ProcessAlive = true
		events, _ := e.st.Mutate(func(tx *store.Tx) error {
			sNow, err := tx.GetSession(sessionID)
			if err != nil {
				return err
			}
			sNow.ActiveProviderSessionId = activePS.ProviderSessionId
			if err := tx.PutProviderSession(activePS); err != nil {
				return err
			}
			return putSessionSynced(tx, sNow)
		})
		e.publish(events, nil)

		if handoffPrefix != "" || (currentPS != nil && currentPS.ProviderSessionId != activePS.ProviderSessionId) {
			var fromSel *sessionv1.ModelSelection
			if liveLease != nil {
				fromSel = liveLease.Selection
			} else {
				for i := len(runs) - 1; i >= 0; i-- {
					if runs[i].RunId != runID && runs[i].ModelSelection != nil {
						fromSel = runs[i].ModelSelection
						break
					}
				}
			}
			if (fromSel != nil && !SelectionsEqual(fromSel, curRun.ModelSelection)) || plan.HandoffStrategy != "" || (currentPS != nil && currentPS.ProviderSessionId != activePS.ProviderSessionId) {
				e.recordSwitchItem(sessionID, runID, fromSel, curRun.ModelSelection, plan.HandoffStrategy, handoffPrefix)
			}
		}

		activeLease = &runtime.LeaseEntry{
			ProviderSessionID: activePS.ProviderSessionId,
			SessionID:         sessionID,
			InstanceID:        instCfg.InstanceId,
			DriverKind:        instCfg.Driver,
			Selection:         proto.Clone(curRun.ModelSelection).(*sessionv1.ModelSelection),
			RuntimeMode:       curRun.RuntimeMode,
			InteractionMode:   curRun.InteractionMode,
			Session:           drvSess,
		}
		<-baseline // registered, an idle lease may be reaped: only once the turn can attach
		e.sup.Register(activeLease)
		e.startLeasePump(activeLease, workdir)
	}

	<-baseline
	pump := e.ensureLeasePump(activeLease, workdir)
	turn := pump.attachTurn(runID, curRun.Ordinal, workdir, prevTreeOID)
	activeLease.SetInTurn(true)

	_, _ = e.st.Mutate(func(tx *store.Tx) error {
		rNow, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		rNow.ProviderSessionId = activePS.ProviderSessionId
		rNow.HandoffSummary = handoffPrefix
		return tx.PutRun(rNow)
	})

	promptToSend := runPrompt
	if currentPS == nil && !freshContext {
		var priorUnexecuted []string
		for _, pr := range runs {
			if pr.RunId != runID && pr.ProviderSessionId == "" && strings.TrimSpace(pr.Prompt) != "" &&
				pr.Ordinal > 0 && pr.Ordinal < curRun.Ordinal &&
				(pr.Status == sessionv1.RunStatus_RUN_STATUS_FAILED ||
					(pr.Status == sessionv1.RunStatus_RUN_STATUS_INTERRUPTED && !strings.HasPrefix(pr.ErrorMessage, "steered by "))) {
				priorUnexecuted = append(priorUnexecuted, pr.Prompt)
			}
		}
		if len(priorUnexecuted) > 0 {
			promptToSend = strings.Join(append(priorUnexecuted, runPrompt), "\n")
		}
	}
	if handoffPrefix != "" {
		promptToSend = handoffPrefix + "\n\n" + runPrompt
	}

	if err := activeLease.Session.StartTurn(runCtx, runtime.TurnInput{
		RunID:           runID,
		Prompt:          promptToSend,
		HandoffPrefix:   handoffPrefix,
		Attachments:     e.loadTurnAttachments(curRun.Attachments),
		Selection:       curRun.ModelSelection,
		EffectiveMode:   curRun.RuntimeMode,
		InteractionMode: curRun.InteractionMode,
	}); err != nil {
		pump.clearActiveIf(runID)
		activeLease.SetInTurn(false)
		e.stopLeasePump(activePS.ProviderSessionId, activeLease)
		e.sup.DetachSession(activePS.ProviderSessionId, activeLease.Session, false)
		e.settleSessionBackgroundTasks(sessionID, activePS.ProviderSessionId, "turn failed to start")
		e.finishRunFailed(sessionID, runID, "turn_start_failed", err.Error(), runtime.FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_UNKNOWN))
		return
	}

	select {
	case <-turn.doneCh:
		return
	case <-runCtx.Done():
		if e.bgCtx.Err() != nil {
			return
		}
		if pump.clearActiveIf(runID) {
			activeLease.SetInTurn(false)
			cp, cumStat := e.captureTurnCheckpoint(sessionID, runID, curRun.Ordinal, workdir, prevTreeOID)
			e.finishRunInterrupted(sessionID, runID, "interrupted", cp, cumStat)
		}
		return
	}
}

func (e *Engine) captureTurnCheckpoint(sessionID, runID string, ordinal uint32, workdir, prevTreeOID string) (*sessionv1.Checkpoint, *sessionv1.DiffStat) {
	if e.bgCtx.Err() != nil || workdir == "" {
		return nil, nil
	}
	cp, cpErr := e.vcs.CaptureCheckpoint(e.bgCtx, workdir, newID("ckpt"), sessionID, runID, ordinal, prevTreeOID)
	treeOID := ""
	if cpErr == nil && cp != nil {
		treeOID = cp.GitTreeOid
	}
	e.pinTurnImages(e.bgCtx, sessionID, runID, ordinal, workdir, treeOID)
	if cpErr != nil || cp == nil {
		return nil, nil
	}
	var cumStat *sessionv1.DiffStat
	existingCkpts, _ := e.st.Checkpoints(sessionID)
	baseTree := ""
	if len(existingCkpts) > 0 && existingCkpts[0].RunOrdinal == 0 {
		baseTree = existingCkpts[0].GitTreeOid
	}
	if baseTree != "" {
		if _, files, dErr := e.vcs.DiffTrees(e.bgCtx, workdir, baseTree, cp.GitTreeOid); dErr == nil {
			cumStat = summarizeDiffFiles(files)
		}
	} else {
		cumStat = summarizeDiffFiles(cp.Files)
	}
	return cp, cumStat
}

type activeTurnHandle struct {
	runID       string
	ordinal     uint32
	workdir     string
	prevTreeOID string
	doneCh      chan struct{}
	doneOnce    sync.Once
}

func (h *activeTurnHandle) closeDone() {
	if h != nil {
		h.doneOnce.Do(func() { close(h.doneCh) })
	}
}

type leasePump struct {
	lease      *runtime.LeaseEntry
	stopCh     chan struct{}
	stopOnce   sync.Once
	mu         sync.Mutex
	workdir    string
	active     *activeTurnHandle
	itemScopes map[string]string
	lastWake   string
}

func (p *leasePump) stop() {
	if p != nil {
		p.stopOnce.Do(func() { close(p.stopCh) })
	}
}

func (p *leasePump) attachTurn(runID string, ordinal uint32, workdir, prevTreeOID string) *activeTurnHandle {
	p.mu.Lock()
	defer p.mu.Unlock()
	if workdir != "" {
		p.workdir = workdir
	}
	if p.active != nil && p.active.runID == runID {
		p.active.ordinal = ordinal
		p.active.prevTreeOID = prevTreeOID
		if workdir != "" {
			p.active.workdir = workdir
		}
		return p.active
	}
	h := &activeTurnHandle{
		runID:       runID,
		ordinal:     ordinal,
		workdir:     p.workdir,
		prevTreeOID: prevTreeOID,
		doneCh:      make(chan struct{}),
	}
	p.active = h
	return h
}

func (p *leasePump) getActive() *activeTurnHandle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func (p *leasePump) takeActive() *activeTurnHandle {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.active
	p.active = nil
	return cur
}

func (p *leasePump) clearActiveIf(runID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != nil && p.active.runID == runID {
		cur := p.active
		p.active = nil
		cur.closeDone()
		return true
	}
	return false
}

func (p *leasePump) rememberScopedItem(nativeID, scopedID string) {
	if nativeID == "" || scopedID == "" {
		return
	}
	p.mu.Lock()
	p.itemScopes[nativeID] = scopedID
	p.mu.Unlock()
}

func (p *leasePump) lookupScopedItem(nativeID string) string {
	if nativeID == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.itemScopes[nativeID]
}

func (e *Engine) startLeasePump(lease *runtime.LeaseEntry, workdir string) *leasePump {
	e.mu.Lock()
	if existing := e.pumps[lease.ProviderSessionID]; existing != nil && existing.lease == lease {
		e.mu.Unlock()
		return existing
	}
	pump := &leasePump{
		lease:      lease,
		stopCh:     make(chan struct{}),
		workdir:    workdir,
		itemScopes: map[string]string{},
	}
	e.pumps[lease.ProviderSessionID] = pump
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.runLeasePump(pump)
	}()
	return pump
}

func (e *Engine) stopLeasePump(psID string, lease *runtime.LeaseEntry) {
	if psID == "" {
		return
	}
	e.mu.Lock()
	p := e.pumps[psID]
	if p != nil && (lease == nil || p.lease == lease) {
		delete(e.pumps, psID)
	} else {
		p = nil
	}
	e.mu.Unlock()
	if p != nil {
		p.stop()
	}
}

func (e *Engine) ensureLeasePump(lease *runtime.LeaseEntry, workdir string) *leasePump {
	return e.startLeasePump(lease, workdir)
}

func (e *Engine) getLeasePump(psID string) *leasePump {
	if psID == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pumps[psID]
}

// ensureContinuationTurn attaches to an already-active run on pump's session or
// spawns a system-authored Continuation Run when out-of-turn agent work arrives.
func (e *Engine) ensureContinuationTurn(pump *leasePump) *activeTurnHandle {
	if cur := pump.getActive(); cur != nil {
		return cur
	}
	sessionID := pump.lease.SessionID
	unlock := e.lockSession(sessionID)

	if cur := pump.getActive(); cur != nil {
		unlock()
		return cur
	}
	sess, err := e.st.Session(sessionID)
	if err != nil || sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		unlock()
		return nil
	}
	existingCkpts, _ := e.st.Checkpoints(sessionID)
	prevTreeOID := ""
	if len(existingCkpts) > 0 {
		prevTreeOID = existingCkpts[len(existingCkpts)-1].GitTreeOid
	}
	pump.mu.Lock()
	workdir := pump.workdir
	wakeReason := pump.lastWake
	pump.mu.Unlock()
	if workdir == "" {
		workdir, _ = e.resolveWorkdir(sess)
	}
	if wakeReason == "" {
		wakeReason = "background_task"
	}

	// If a foreground run was just dispatched and holds ActiveRunId, attach to it.
	if sess.ActiveRunId != "" {
		r, rErr := e.st.Run(sess.ActiveRunId)
		ord := sess.GetProgress().GetTurnCount()
		if rErr == nil {
			ord = r.Ordinal
		}
		h := pump.attachTurn(sess.ActiveRunId, ord, workdir, prevTreeOID)
		pump.lease.SetInTurn(true)
		unlock()
		return h
	}

	var contRun *sessionv1.Run
	events, mErr := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sNow.Progress == nil {
			sNow.Progress = &sessionv1.SessionProgress{}
		}
		supReq, supItem, err := dismissPendingRequestTx(tx, sNow, nil)
		if err != nil {
			return err
		}
		now := timestamppb.Now()
		sNow.Error = ""
		sNow.Progress.LastFailure = nil
		sNow.Progress.TurnCount++
		sNow.Progress.TurnStartedAt = now
		contRun = &sessionv1.Run{
			RunId:             newID("run"),
			SessionId:         sessionID,
			Ordinal:           sNow.Progress.TurnCount,
			Status:            sessionv1.RunStatus_RUN_STATUS_RUNNING,
			Author:            &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_SYSTEM, Id: "background", Label: "Background"},
			WakeReason:        wakeReason,
			Intent:            sessionv1.DispatchIntent_DISPATCH_INTENT_START,
			ModelSelection:    proto.Clone(sNow.ModelSelection).(*sessionv1.ModelSelection),
			RuntimeMode:       sNow.EffectiveRuntimeMode,
			InteractionMode:   sNow.InteractionMode,
			ProviderSessionId: pump.lease.ProviderSessionID,
			CreatedAt:         now,
			StartedAt:         now,
		}
		sNow.ActiveRunId = contRun.RunId
		sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
		if err := tx.PutRun(contRun); err != nil {
			return err
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		if supReq != nil {
			_ = tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "request.resolved",
				Session:   sNow,
				Item:      supItem,
				Request:   supReq,
			})
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "run.started",
			Session:   sNow,
			Run:       contRun,
		})
	})
	if mErr != nil || contRun == nil {
		unlock()
		return nil
	}

	contCtx, contCancel := context.WithCancel(e.bgCtx)
	runID := contRun.RunId
	e.mu.Lock()
	e.runCancels[runID] = contCancel
	e.runDoneChs[runID] = contCtx.Done()
	e.mu.Unlock()

	h := pump.attachTurn(runID, contRun.Ordinal, workdir, prevTreeOID)
	pump.lease.SetInTurn(true)
	unlock()

	e.publish(events, nil)

	// Watch for explicit InterruptSession cancelling contCtx!
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		select {
		case <-h.doneCh:
		case <-contCtx.Done():
			if e.bgCtx.Err() == nil && pump.clearActiveIf(runID) {
				pump.lease.SetInTurn(false)
				cp, cumStat := e.captureTurnCheckpoint(sessionID, runID, h.ordinal, h.workdir, h.prevTreeOID)
				e.finishRunInterrupted(sessionID, runID, "interrupted", cp, cumStat)
			}
		}
		contCancel()
		e.mu.Lock()
		delete(e.runCancels, runID)
		delete(e.runDoneChs, runID)
		e.mu.Unlock()
	}()
	return h
}

func (e *Engine) runLeasePump(pump *leasePump) {
	sessionID := pump.lease.SessionID
	psID := pump.lease.ProviderSessionID

	msgBufs := map[string]*strings.Builder{}
	rsnBufs := map[string]*strings.Builder{}
	var lastAssistantText string
	var pendingPlan *sessionv1.PlanReviewPayload
	var pendingQuestion *sessionv1.UserInputPayload

	resetTurnBuffers := func() {
		clear(msgBufs)
		clear(rsnBufs)
		lastAssistantText = ""
		pendingPlan = nil
		pendingQuestion = nil
	}

	// Reasoning still streaming (item -> run): shown as thinking until it ends, except for the one item given.
	liveRsn := map[string]string{}
	settleReasoning := func(keep string) {
		for itemID, runID := range liveRsn {
			if itemID == keep {
				continue
			}
			delete(liveRsn, itemID)
			var text string
			if buf := rsnBufs[itemID]; buf != nil {
				text = buf.String()
			}
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_REASONING,
					Status:    "completed",
					Text:      text,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)
		}
	}

	pumpSess := pump.lease.Session
	eventsCh := pumpSess.Events()
	for {
		var ev runtime.Event
		var ok bool
		select {
		case <-e.bgCtx.Done():
			e.stopLeasePump(psID, pump.lease)
			return
		case <-pump.stopCh:
			return
		case ev, ok = <-eventsCh:
		}
		if !ok {
			settleReasoning("")
			e.mu.Lock()
			if e.pumps[psID] == pump {
				delete(e.pumps, psID)
			}
			e.mu.Unlock()
			if e.bgCtx.Err() != nil {
				return
			}
			e.sup.DetachSession(psID, pumpSess, false)

			// Settle any background tasks tied to this closed process.
			e.settleSessionBackgroundTasks(sessionID, psID, "provider session exited")

			if cur := pump.takeActive(); cur != nil {
				resetTurnBuffers()
				pump.lease.SetInTurn(false)
				e.mu.Lock()
				doneCh := e.runDoneChs[cur.runID]
				e.mu.Unlock()
				cancelled := false
				if doneCh != nil {
					select {
					case <-doneCh:
						cancelled = true
					default:
					}
				}
				cp, cumStat := e.captureTurnCheckpoint(sessionID, cur.runID, cur.ordinal, cur.workdir, cur.prevTreeOID)
				if cancelled {
					e.finishRunInterrupted(sessionID, cur.runID, "interrupted", cp, cumStat)
				} else {
					e.finishRunFailedWithCheckpoint(sessionID, cur.runID, "session_closed", "agent process exited unexpectedly", &runtime.Failure{
						Kind:    sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED,
						Message: "agent process exited unexpectedly",
					}, cp, cumStat)
				}
				cur.closeDone()
			}
			return
		}

		pump.lease.Touch()

		// Any next step of the model ends its thinking; reasoning ends its own other reasoning.
		switch ev.Kind {
		case runtime.EventReasoningDelta, runtime.EventNativeSession, runtime.EventUsage, runtime.EventNotice, runtime.EventBackgroundTask:
		default:
			settleReasoning("")
		}

		switch ev.Kind {
		case runtime.EventNativeSession:
			events, _ := e.st.Mutate(func(tx *store.Tx) error {
				ps, err := tx.GetProviderSession(psID)
				if err != nil {
					return err
				}
				if ev.NativeID != "" {
					ps.NativeSessionId = ev.NativeID
				}
				if ev.ResumeCursor != "" {
					ps.ResumeCursor = ev.ResumeCursor
				}
				if err := tx.PutProviderSession(ps); err != nil {
					return err
				}
				if sNow, sErr := tx.GetSession(sessionID); sErr == nil {
					prev := sNow.ProviderSessionRef
					ref := ps.ResumeCursor
					if ref == "" {
						ref = ps.NativeSessionId
					}
					if ref != "" {
						sNow.ProviderSessionRef = ref
					}
					if ev.Model != "" {
						sNow.Model = ev.Model
					}
					if ev.Effort != "" {
						sNow.Effort = ev.Effort
					}
					if isHerConversation(sNow) && sNow.ProviderSessionRef != prev {
						sNow.UpdatedAt = timestamppb.Now()
						_ = tx.AppendEvent(&sessionv1.SessionEvent{SessionId: sessionID, Type: "session.updated", Session: sNow})
					}
					_ = putSessionSynced(tx, sNow)
				}
				return nil
			})
			e.publish(events, nil)

		case runtime.EventBackgroundTask:
			taskID := ev.TaskID
			if taskID == "" {
				taskID = ev.ItemID
			}
			if taskID == "" {
				continue
			}
			scopedItemID := pump.lookupScopedItem(ev.ItemID)
			cur := pump.getActive()
			runID := ""
			if cur != nil {
				runID = cur.runID
				if scopedItemID == "" && ev.ItemID != "" {
					scopedItemID = scopeRunItemID(cur.runID, ev.ItemID, "tool")
					pump.rememberScopedItem(ev.ItemID, scopedItemID)
				}
			}
			label := strings.TrimSpace(ev.ToolName)
			if label == "" {
				label = strings.TrimSpace(ev.Command)
			}
			if label != "" {
				pump.mu.Lock()
				pump.lastWake = label
				pump.mu.Unlock()
			}
			var awaitedCnt, daemonCnt uint32
			evs, err := e.st.Mutate(func(tx *store.Tx) error {
				sNow, err := tx.GetSession(sessionID)
				if err != nil {
					return err
				}
				bt := &sessionv1.BackgroundTask{
					TaskId:     taskID,
					SessionId:  sessionID,
					RunId:      runID,
					ItemId:     scopedItemID,
					Kind:       ev.BackgroundKind,
					Status:     ev.BackgroundStatus,
					Title:      label,
					Command:    ev.Command,
					OutputTail: ev.Output,
					Summary:    ev.Text,
					Awaited:    ev.BackgroundAwaited,
					StopScope:  ev.BackgroundStopScope,
				}
				if ev.HasExitCode {
					code := ev.ExitCode
					bt.ExitCode = &code
				}
				if err := tx.PutBackgroundTask(bt); err != nil {
					return err
				}
				awaitedCnt, daemonCnt, _ = tx.RefreshSessionBackgroundCounts(sNow)
				if err := putSessionSynced(tx, sNow); err != nil {
					return err
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId:      sessionID,
					Type:           "background_task.updated",
					Session:        sNow,
					BackgroundTask: bt,
				})
			})
			if err == nil {
				pump.lease.SetBackgroundCounts(int(awaitedCnt), int(daemonCnt))
				e.publish(evs, nil)
			}

		case runtime.EventUsage:
			if ev.Usage != nil {
				cur := pump.getActive()
				_, _ = e.st.Mutate(func(tx *store.Tx) error {
					sNow, err := tx.GetSession(sessionID)
					if err != nil {
						return err
					}
					sNow.ContextUsage = ev.Usage
					if sNow.Progress == nil {
						sNow.Progress = &sessionv1.SessionProgress{}
					}
					sNow.Progress.Usage = &sessionv1.Usage{
						InputTokens:  ev.Usage.InputTokens,
						OutputTokens: ev.Usage.OutputTokens,
					}
					if cur != nil {
						if rNow, rErr := tx.GetRun(cur.runID); rErr == nil {
							rNow.Usage = ev.Usage
							_ = tx.PutRun(rNow)
						}
					}
					return putSessionSynced(tx, sNow)
				})
			}

		case runtime.EventToolStarted, runtime.EventToolCompleted:
			status := "running"
			if ev.Kind == runtime.EventToolCompleted {
				status = "completed"
				if ev.ExitCode != 0 {
					status = "failed"
				}
			}
			inlineBlob := func(name, raw string) (text string, truncated bool, sha string, size uint64) {
				size = uint64(len(raw))
				if len(raw) <= maxInlineToolOutput {
					return raw, false, "", size
				}
				if ref, bErr := e.st.PutBlob(name, "text/plain; charset=utf-8", []byte(raw)); bErr == nil {
					sha = ref.BlobSha256
				}
				cut := maxInlineToolOutput
				for cut > 0 && !utf8.RuneStart(raw[cut]) {
					cut--
				}
				return raw[:cut] + "\n...[output truncated]...", true, sha, size
			}
			input, inputTruncated, inputBlobSha, inputSize := inlineBlob("input.txt", ev.Input)
			output, truncated, blobSha, outputSize := inlineBlob("output.txt", ev.Output)
			existingScoped := pump.lookupScopedItem(ev.ItemID)
			cur := pump.getActive()
			// A background task completing on its own updates its original TurnItem
			// without spawning an empty Continuation Run.
			if cur == nil && ev.Kind == runtime.EventToolCompleted && existingScoped != "" {
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					prev, err := tx.GetTurnItem(sessionID, existingScoped)
					if err != nil {
						return nil
					}
					prev.Status = status
					prev.ExitCode = ev.ExitCode
					if input != "" {
						prev.Input = input
						prev.InputTruncated = inputTruncated
						prev.InputBlobSha256 = inputBlobSha
						prev.InputSize = inputSize
					}
					if output != "" {
						prev.Output = output
						prev.OutputTruncated = truncated
						prev.OutputBlobSha256 = blobSha
						prev.OutputSize = outputSize
					}
					if err := tx.PutTurnItem(prev); err != nil {
						return err
					}
					return tx.AppendEvent(&sessionv1.SessionEvent{
						SessionId: sessionID,
						Type:      "item.updated",
						Item:      prev,
					})
				})
				e.publish(evs, nil)
				continue
			}
			if cur == nil {
				cur = e.ensureContinuationTurn(pump)
				if cur == nil {
					continue
				}
			}
			itemID := existingScoped
			if itemID == "" {
				if ev.ItemID != "" {
					itemID = scopeRunItemID(cur.runID, ev.ItemID, "tool")
					pump.rememberScopedItem(ev.ItemID, itemID)
				} else {
					itemID = newID("tool")
				}
			}
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				prev, _ := tx.GetTurnItem(sessionID, itemID)
				ownerRunID := cur.runID
				toolName := ev.ToolName
				cmd := ev.Command
				filePath := ev.FilePath
				text := strings.TrimSpace(ev.Text)
				if prev != nil {
					if prev.RunId != "" {
						ownerRunID = prev.RunId
					}
					if toolName == "" {
						toolName = prev.ToolName
					}
					if cmd == "" {
						cmd = prev.Command
					}
					if filePath == "" {
						filePath = prev.FilePath
					}
					if text == "" {
						text = prev.Text
					}
					if input == "" && prev.Input != "" {
						input = prev.Input
						inputTruncated = prev.InputTruncated
						inputBlobSha = prev.InputBlobSha256
						inputSize = prev.InputSize
					}
					if output == "" && prev.Output != "" {
						output = prev.Output
						truncated = prev.OutputTruncated
						blobSha = prev.OutputBlobSha256
						outputSize = prev.OutputSize
					}
				}
				kind := sessionv1.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL
				if cmd != "" || (prev != nil && prev.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION) {
					kind = sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION
				}
				it := &sessionv1.TurnItem{
					ItemId:           itemID,
					SessionId:        sessionID,
					RunId:            ownerRunID,
					Kind:             kind,
					Status:           status,
					Text:             text,
					ToolName:         toolName,
					Command:          cmd,
					Input:            input,
					InputTruncated:   inputTruncated,
					InputBlobSha256:  inputBlobSha,
					InputSize:        inputSize,
					FilePath:         filePath,
					ExitCode:         ev.ExitCode,
					Output:           output,
					OutputTruncated:  truncated,
					OutputBlobSha256: blobSha,
					OutputSize:       outputSize,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				if sNow, sErr := tx.GetSession(sessionID); sErr == nil {
					if sNow.Progress == nil {
						sNow.Progress = &sessionv1.SessionProgress{}
					}
					if ev.Kind == runtime.EventToolStarted {
						curTool := text
						if curTool == "" {
							curTool = toolName
						}
						if curTool == "" {
							curTool = cmd
						}
						sNow.Progress.CurrentTool = curTool
					} else {
						sNow.Progress.CurrentTool = ""
					}
					_ = putSessionSynced(tx, sNow)
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventAssistantDelta:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			itemID := scopeRunItemID(cur.runID, ev.ItemID, "msg")
			buf := msgBufs[itemID]
			if buf == nil {
				buf = &strings.Builder{}
				msgBufs[itemID] = buf
			}
			buf.WriteString(ev.Text)
			fullText := buf.String()
			lastAssistantText = fullText
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
					Status:    "running",
					Text:      fullText,
					Streaming: true,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId:    sessionID,
					Type:         "item.delta",
					Item:         it,
					TextDelta:    ev.Text,
					TargetItemId: itemID,
				})
			})
			e.publish(evs, nil)

		case runtime.EventAssistantMessage:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			itemID := scopeRunItemID(cur.runID, ev.ItemID, "msg")
			text := ev.Text
			if text == "" && msgBufs[itemID] != nil {
				text = msgBufs[itemID].String()
			}
			delete(msgBufs, itemID)
			if text != "" {
				lastAssistantText = text
			}
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
					Status:    "completed",
					Text:      text,
					Streaming: false,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				if sNow, sErr := tx.GetSession(sessionID); sErr == nil && text != "" {
					if sNow.Progress == nil {
						sNow.Progress = &sessionv1.SessionProgress{}
					}
					sNow.Progress.LastMessageExcerpt = excerptText(text, 240)
					_ = putSessionSynced(tx, sNow)
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventReasoningDelta:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			itemID := scopeRunItemID(cur.runID, ev.ItemID, "rsn")
			settleReasoning(itemID)
			liveRsn[itemID] = cur.runID
			buf := rsnBufs[itemID]
			if buf == nil {
				buf = &strings.Builder{}
				rsnBufs[itemID] = buf
			}
			buf.WriteString(ev.Text)
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_REASONING,
					Status:    "completed",
					Text:      buf.String(),
					Streaming: true,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventFileChanged:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			var itemID string
			if ev.ItemID != "" {
				itemID = scopeRunItemID(cur.runID, ev.ItemID, "file")
			} else {
				itemID = newID("file")
			}
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE,
					Status:    "completed",
					FilePath:  ev.FilePath,
					Diff:      ev.Diff,
					Additions: ev.Additions,
					Deletions: ev.Deletions,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventQuestionAsked:
			if e.ensureContinuationTurn(pump) != nil && ev.Question != nil && len(ev.Question.Questions) > 0 {
				pendingQuestion = ev.Question
			}

		case runtime.EventPlanProposed:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			var itemID string
			if ev.ItemID != "" {
				itemID = scopeRunItemID(cur.runID, ev.ItemID, "plan")
			} else {
				itemID = newID("plan")
			}
			if ev.RequestReview {
				pendingPlan = &sessionv1.PlanReviewPayload{
					PlanId:   itemID,
					Title:    ev.PlanTitle,
					Markdown: ev.Text,
				}
			} else if pendingPlan != nil && pendingPlan.PlanId == itemID {
				pendingPlan = nil
			}
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_PROPOSED_PLAN,
					Status:    "completed",
					PlanTitle: ev.PlanTitle,
					Text:      ev.Text,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventTodoUpdated:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			rawID := ev.ItemID
			if rawID == "" {
				rawID = "todo"
			}
			itemID := scopeRunItemID(cur.runID, rawID, "todo")
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				prev, _ := tx.GetTurnItem(sessionID, itemID)
				focus := strings.TrimSpace(ev.Text)
				if !ev.HasFocus && prev != nil {
					focus = prev.Text
				}
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_TODO_LIST,
					Status:    "completed",
					Text:      focus,
					TodoSteps: ev.TodoSteps,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				if focus != "" {
					if sNow, sErr := tx.GetSession(sessionID); sErr == nil {
						if sNow.Progress == nil {
							sNow.Progress = &sessionv1.SessionProgress{}
						}
						sNow.Progress.CurrentTool = focus
						_ = putSessionSynced(tx, sNow)
					}
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventFocusUpdated:
			cur := e.ensureContinuationTurn(pump)
			if cur == nil {
				continue
			}
			focus := strings.TrimSpace(ev.Text)
			itemID := scopeRunItemID(cur.runID, "todo", "todo")
			evs, _ := e.st.Mutate(func(tx *store.Tx) error {
				prev, _ := tx.GetTurnItem(sessionID, itemID)
				if focus == "" && prev == nil {
					return nil
				}
				var steps []*sessionv1.TodoStep
				if prev != nil {
					steps = prev.TodoSteps
				}
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     cur.runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_TODO_LIST,
					Status:    "completed",
					Text:      focus,
					TodoSteps: steps,
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
				if sNow, sErr := tx.GetSession(sessionID); sErr == nil {
					if sNow.Progress == nil {
						sNow.Progress = &sessionv1.SessionProgress{}
					}
					sNow.Progress.CurrentTool = focus
					_ = putSessionSynced(tx, sNow)
				}
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "item.updated",
					Item:      it,
				})
			})
			e.publish(evs, nil)

		case runtime.EventNotice:
			text := ev.Text
			if text == "" && ev.Notice != nil {
				text = ev.Notice.Text
				if text == "" {
					text = ev.Notice.FailureMessage
				}
			}
			if text != "" {
				runID := ""
				if cur := pump.getActive(); cur != nil {
					runID = cur.runID
				}
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &sessionv1.TurnItem{
						ItemId:    newID("ntc"),
						SessionId: sessionID,
						RunId:     runID,
						Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_SYSTEM_NOTICE,
						Status:    "completed",
						Text:      text,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&sessionv1.SessionEvent{
						SessionId: sessionID,
						Type:      "item.updated",
						Item:      it,
					})
				})
				e.publish(evs, nil)
			}

		case runtime.EventTurnCompleted:
			cur := pump.takeActive()
			if cur == nil {
				resetTurnBuffers()
				continue
			}
			for itemID, buf := range msgBufs {
				if buf.Len() > 0 {
					full := buf.String()
					lastAssistantText = full
					evs, _ := e.st.Mutate(func(tx *store.Tx) error {
						it := &sessionv1.TurnItem{
							ItemId:    itemID,
							SessionId: sessionID,
							RunId:     cur.runID,
							Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
							Status:    "completed",
							Text:      full,
							Streaming: false,
						}
						if err := tx.PutTurnItem(it); err != nil {
							return err
						}
						return tx.AppendEvent(&sessionv1.SessionEvent{
							SessionId: sessionID,
							Type:      "item.updated",
							Item:      it,
						})
					})
					e.publish(evs, nil)
				}
			}
			if ev.Text != "" {
				lastAssistantText = ev.Text
			}
			resultText := lastAssistantText
			planToReview := pendingPlan
			questionToAsk := pendingQuestion
			resetTurnBuffers()

			capturedCp, cumStat := e.captureTurnCheckpoint(sessionID, cur.runID, cur.ordinal, cur.workdir, cur.prevTreeOID)
			e.finishRunCompleted(sessionID, cur.runID, psID, capturedCp, cumStat, resultText, planToReview, questionToAsk)
			pump.lease.SetInTurn(false)
			cur.closeDone()

		case runtime.EventTurnInterrupted:
			if e.bgCtx.Err() != nil {
				return
			}
			cur := pump.takeActive()
			if cur == nil {
				resetTurnBuffers()
				continue
			}
			for itemID, buf := range msgBufs {
				if buf.Len() > 0 {
					evs, _ := e.st.Mutate(func(tx *store.Tx) error {
						it := &sessionv1.TurnItem{
							ItemId:    itemID,
							SessionId: sessionID,
							RunId:     cur.runID,
							Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
							Status:    "interrupted",
							Text:      buf.String(),
							Streaming: false,
						}
						if err := tx.PutTurnItem(it); err != nil {
							return err
						}
						return tx.AppendEvent(&sessionv1.SessionEvent{
							SessionId: sessionID,
							Type:      "item.updated",
							Item:      it,
						})
					})
					e.publish(evs, nil)
				}
			}
			resetTurnBuffers()
			cp, cumStat := e.captureTurnCheckpoint(sessionID, cur.runID, cur.ordinal, cur.workdir, cur.prevTreeOID)
			e.finishRunInterrupted(sessionID, cur.runID, "interrupted", cp, cumStat)
			pump.lease.SetInTurn(false)
			cur.closeDone()

		case runtime.EventTurnFailed:
			cur := pump.takeActive()
			resetTurnBuffers()
			if cur != nil {
				cp, cumStat := e.captureTurnCheckpoint(sessionID, cur.runID, cur.ordinal, cur.workdir, cur.prevTreeOID)
				e.finishRunFailedWithCheckpoint(sessionID, cur.runID, ev.ErrorCode, ev.ErrorMessage, ev.Failure, cp, cumStat)
				pump.lease.SetInTurn(false)
				cur.closeDone()
			} else {
				pump.lease.SetInTurn(false)
			}
		}
	}
}

func truncateTurnOutput(output string) (string, bool) {
	if len(output) <= 32768 {
		return output, false
	}
	cut := 32768
	for cut > 0 && !utf8.RuneStart(output[cut]) {
		cut--
	}
	return output[:cut] + "\n...[output truncated]...", true
}

func scopeRunItemID(runID, rawID, fallbackPrefix string) string {
	if rawID == "" {
		return fallbackPrefix + "_" + runID
	}
	if strings.HasPrefix(rawID, runID+":") || strings.HasSuffix(rawID, "_"+runID) {
		return rawID
	}
	return runID + ":" + rawID
}

func excerptText(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

func summarizeDiffFiles(files []*sessionv1.CheckpointFile) *sessionv1.DiffStat {
	if len(files) == 0 {
		return nil
	}
	st := &sessionv1.DiffStat{FilesChanged: uint32(len(files))}
	for _, f := range files {
		st.Insertions += f.Additions
		st.Deletions += f.Deletions
	}
	return st
}

func (e *Engine) recordSwitchItem(sessionID, runID string, fromSel, toSel *sessionv1.ModelSelection, handoffStrategy, summary string) {
	kind := sessionv1.TurnItemKind_TURN_ITEM_KIND_MODEL_SWITCH
	if handoffStrategy != "" {
		kind = sessionv1.TurnItemKind_TURN_ITEM_KIND_HANDOFF
	}
	evs, _ := e.st.Mutate(func(tx *store.Tx) error {
		it := &sessionv1.TurnItem{
			ItemId:          newID("sw"),
			SessionId:       sessionID,
			RunId:           runID,
			Kind:            kind,
			Status:          "completed",
			FromSelection:   fromSel,
			ToSelection:     toSel,
			HandoffStrategy: handoffStrategy,
			Text:            summary,
		}
		if err := tx.PutTurnItem(it); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "item.updated",
			Item:      it,
		})
	})
	e.publish(evs, nil)
}

func (e *Engine) finishRunCompleted(sessionID, runID, psID string, cp *sessionv1.Checkpoint, cumStat *sessionv1.DiffStat, resultText string, pendingPlan *sessionv1.PlanReviewPayload, pendingQuestion *sessionv1.UserInputPayload) {
	e.maybeRefineTitleBeforeTurnEnd(sessionID, runID, cp, resultText)
	unlock := e.lockSession(sessionID)
	preSess, _ := e.st.Session(sessionID)
	gitStatus := e.inspectSessionGitStatus(e.bgCtx, preSess, cp, cumStat)
	if pendingPlan != nil {
		if existing, rErr := e.st.RuntimeRequests(sessionID); rErr == nil {
			for _, rq := range existing {
				if rq.RunId == runID && rq.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW {
					pendingPlan = nil
					break
				}
			}
		}
	}
	var nextQueued *sessionv1.Run
	var nextItem *sessionv1.TurnItem
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sess, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sess.Progress == nil {
			sess.Progress = &sessionv1.SessionProgress{}
		}
		r, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		if r.Status != sessionv1.RunStatus_RUN_STATUS_RUNNING && r.Status != sessionv1.RunStatus_RUN_STATUS_WAITING_REQUEST {
			return nil
		}
		settled, _ := tx.SettleRunItems(sessionID, runID, "completed")
		for _, sit := range settled {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "item.updated",
				Item:      sit,
			}); err != nil {
				return err
			}
		}
		r.Status = sessionv1.RunStatus_RUN_STATUS_COMPLETED
		r.CompletedAt = timestamppb.Now()
		var cpItem *sessionv1.TurnItem
		if cp != nil {
			r.CheckpointId = cp.CheckpointId
			if err := tx.PutCheckpoint(cp); err != nil {
				return err
			}
			cpItem = &sessionv1.TurnItem{
				ItemId:       "ckpt_" + cp.CheckpointId,
				SessionId:    sessionID,
				RunId:        runID,
				Kind:         sessionv1.TurnItemKind_TURN_ITEM_KIND_CHECKPOINT,
				Status:       "completed",
				CheckpointId: cp.CheckpointId,
			}
			_ = tx.PutTurnItem(cpItem)
		}
		if cumStat != nil {
			sess.Progress.DiffStat = cumStat
		}
		sess.GitStatus = gitStatus
		if strings.TrimSpace(resultText) != "" {
			sess.Result = resultText
			sess.Progress.LastMessageExcerpt = excerptText(resultText, 240)
		}
		if err := tx.PutRun(r); err != nil {
			return err
		}
		if ps, psErr := tx.GetProviderSession(psID); psErr == nil {
			ps.SyncedRunOrdinal = r.Ordinal
			_ = tx.PutProviderSession(ps)
		}

		queued, _ := tx.QueuedRuns(sessionID)
		var resumeReq *sessionv1.RuntimeRequest
		var resumeReqItem *sessionv1.TurnItem
		if len(queued) > 0 && !sess.QueueHeld {
			now := timestamppb.Now()
			nextQueued = queued[0]
			sess.Progress.TurnCount++
			sess.Progress.TurnStartedAt = now
			nextQueued.Ordinal = sess.Progress.TurnCount
			nextQueued.Status = sessionv1.RunStatus_RUN_STATUS_RUNNING
			nextQueued.StartedAt = now
			if err := tx.PutRun(nextQueued); err != nil {
				return err
			}
			nextItem = &sessionv1.TurnItem{
				ItemId:      newID("itm"),
				SessionId:   sessionID,
				RunId:       nextQueued.RunId,
				Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
				Status:      "completed",
				Actor:       nextQueued.Author,
				Text:        nextQueued.Prompt,
				Attachments: nextQueued.Attachments,
			}
			_ = tx.PutTurnItem(nextItem)
			sess.ActiveRunId = nextQueued.RunId
			sess.QueuedRunCount = uint32(len(queued) - 1)
			sess.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
			sess.ModelSelection = nextQueued.ModelSelection
			sess.EffectiveRuntimeMode = nextQueued.RuntimeMode
			sess.InteractionMode = nextQueued.InteractionMode
			sess.PendingRequestId = ""
			sess.Progress.PendingQuestion = nil
		} else if pendingPlan != nil {
			reqID := newID("req")
			resumeReq = &sessionv1.RuntimeRequest{
				RequestId:    reqID,
				SessionId:    sessionID,
				RunId:        runID,
				Kind:         sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
				Status:       sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING,
				ResponseMode: sessionv1.ResponseMode_RESPONSE_MODE_RESUME_TURN,
				PlanReview:   pendingPlan,
			}
			if err := tx.PutRuntimeRequest(resumeReq); err != nil {
				return err
			}
			resumeReqItem = &sessionv1.TurnItem{
				ItemId:    "reqitm_" + reqID,
				SessionId: sessionID,
				RunId:     runID,
				Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
				Status:    "waiting",
				RequestId: reqID,
			}
			if err := tx.PutTurnItem(resumeReqItem); err != nil {
				return err
			}
			sess.ActiveRunId = ""
			sess.QueuedRunCount = uint32(len(queued))
			sess.Status = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
			sess.PendingRequestId = reqID
			sess.Progress.PendingQuestion = nil
		} else if pendingQuestion != nil {
			// The agent ended its turn on a question (Her's, docs/her-conversations.md §5.3): the session
			// waits on it, and the answer, or any message, starts the next turn.
			reqID := newID("req")
			resumeReq = &sessionv1.RuntimeRequest{
				RequestId:    reqID,
				SessionId:    sessionID,
				RunId:        runID,
				Kind:         sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
				Status:       sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING,
				ResponseMode: sessionv1.ResponseMode_RESPONSE_MODE_RESUME_TURN,
				UserInput:    pendingQuestion,
			}
			if err := tx.PutRuntimeRequest(resumeReq); err != nil {
				return err
			}
			resumeReqItem = &sessionv1.TurnItem{
				ItemId:    "reqitm_" + reqID,
				SessionId: sessionID,
				RunId:     runID,
				Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
				Status:    "waiting",
				RequestId: reqID,
			}
			if err := tx.PutTurnItem(resumeReqItem); err != nil {
				return err
			}
			sess.ActiveRunId = ""
			sess.QueuedRunCount = uint32(len(queued))
			sess.Status = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
			sess.PendingRequestId = reqID
			sess.Progress.PendingQuestion = &sessionv1.Question{
				QuestionId:           reqID,
				Turn:                 r.Ordinal,
				Items:                pendingQuestion.Questions,
				AskedAt:              timestamppb.Now(),
				LocalAnswerAvailable: true,
			}
		} else {
			sess.ActiveRunId = ""
			sess.QueuedRunCount = uint32(len(queued))
			if len(queued) > 0 {
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_QUEUED
			} else {
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
			}
			sess.PendingRequestId = ""
			sess.Progress.PendingQuestion = nil
		}
		if err := putSessionSynced(tx, sess); err != nil {
			return err
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId:  sessionID,
			Type:       "run.completed",
			Session:    sess,
			Run:        r,
			Item:       cpItem,
			Checkpoint: cp,
		}); err != nil {
			return err
		}
		if resumeReq != nil {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "request.created",
				Session:   sess,
				Run:       r,
				Item:      resumeReqItem,
				Request:   resumeReq,
			}); err != nil {
				return err
			}
		}
		return appendRunStarted(tx, sess, nextQueued, nextItem)
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
		if nextQueued != nil {
			e.spawnRun(sessionID, nextQueued.RunId, nextQueued.Intent == sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT)
		}
	}
}

func (e *Engine) finishRunInterrupted(sessionID, runID, reason string, cp *sessionv1.Checkpoint, cumStat *sessionv1.DiffStat) {
	unlock := e.lockSession(sessionID)
	preSess, _ := e.st.Session(sessionID)
	gitStatus := e.inspectSessionGitStatus(e.bgCtx, preSess, cp, cumStat)
	var nextQueued *sessionv1.Run
	var nextItem *sessionv1.TurnItem
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sess, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sess.Progress == nil {
			sess.Progress = &sessionv1.SessionProgress{}
		}
		r, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		if r.Status == sessionv1.RunStatus_RUN_STATUS_INTERRUPTED || r.Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			return nil
		}
		settled, _ := tx.SettleRunItems(sessionID, runID, "interrupted")
		for _, sit := range settled {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "item.updated",
				Item:      sit,
			}); err != nil {
				return err
			}
		}
		e.mu.Lock()
		if recorded := e.runInterruptedBy[runID]; recorded != "" {
			reason = recorded
			delete(e.runInterruptedBy, runID)
		}
		e.mu.Unlock()
		if strings.TrimSpace(reason) == "" {
			reason = "interrupted"
		}
		intFailure := &sessionv1.Failure{
			Kind:    sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED,
			Message: reason,
		}
		r.Status = sessionv1.RunStatus_RUN_STATUS_INTERRUPTED
		r.ErrorMessage = reason
		r.Failure = intFailure
		r.CompletedAt = timestamppb.Now()
		if cp != nil {
			r.CheckpointId = cp.CheckpointId
			_ = tx.PutCheckpoint(cp)
		}
		if cumStat != nil {
			sess.Progress.DiffStat = cumStat
		}
		sess.GitStatus = gitStatus
		if err := tx.PutRun(r); err != nil {
			return err
		}
		queued, _ := tx.QueuedRuns(sessionID)
		if len(queued) > 0 && !sess.QueueHeld {
			now := timestamppb.Now()
			nextQueued = queued[0]
			sess.Progress.TurnCount++
			sess.Progress.TurnStartedAt = now
			sess.Progress.LastFailure = nil
			nextQueued.Ordinal = sess.Progress.TurnCount
			nextQueued.Status = sessionv1.RunStatus_RUN_STATUS_RUNNING
			nextQueued.StartedAt = now
			_ = tx.PutRun(nextQueued)
			nextItem = &sessionv1.TurnItem{
				ItemId:      newID("itm"),
				SessionId:   sessionID,
				RunId:       nextQueued.RunId,
				Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
				Status:      "completed",
				Actor:       nextQueued.Author,
				Text:        nextQueued.Prompt,
				Attachments: nextQueued.Attachments,
			}
			_ = tx.PutTurnItem(nextItem)
			sess.ActiveRunId = nextQueued.RunId
			sess.QueuedRunCount = uint32(len(queued) - 1)
			sess.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
			sess.ModelSelection = nextQueued.ModelSelection
			sess.EffectiveRuntimeMode = nextQueued.RuntimeMode
			sess.InteractionMode = nextQueued.InteractionMode
		} else {
			sess.ActiveRunId = ""
			sess.QueuedRunCount = uint32(len(queued))
			sess.Progress.LastFailure = intFailure
			if len(queued) > 0 {
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_QUEUED
			} else {
				sess.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
			}
		}
		sess.PendingRequestId = ""
		sess.Progress.PendingQuestion = nil
		if err := putSessionSynced(tx, sess); err != nil {
			return err
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "run.interrupted",
			Session:   sess,
			Run:       r,
		}); err != nil {
			return err
		}
		return appendRunStarted(tx, sess, nextQueued, nextItem)
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
		if nextQueued != nil {
			e.spawnRun(sessionID, nextQueued.RunId, nextQueued.Intent == sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT)
		}
	}
}

func appendRunStarted(tx *store.Tx, sess *sessionv1.Session, run *sessionv1.Run, item *sessionv1.TurnItem) error {
	if run == nil {
		return nil
	}
	return tx.AppendEvent(&sessionv1.SessionEvent{
		SessionId: sess.SessionId,
		Type:      "run.started",
		Session:   sess,
		Run:       run,
		Item:      item,
	})
}

func (e *Engine) finishRunFailed(sessionID, runID, code, msg string, f *runtime.Failure) {
	e.finishRunFailedWithCheckpoint(sessionID, runID, code, msg, f, nil, nil)
}

func (e *Engine) finishRunFailedWithCheckpoint(sessionID, runID, code, msg string, f *runtime.Failure, cp *sessionv1.Checkpoint, cumStat *sessionv1.DiffStat) {
	e.maybeRefineTitleBeforeTurnEnd(sessionID, runID, cp, msg)
	unlock := e.lockSession(sessionID)
	preSess, _ := e.st.Session(sessionID)
	gitStatus := e.inspectSessionGitStatus(e.bgCtx, preSess, cp, cumStat)
	var protoFail *sessionv1.Failure
	if f != nil {
		protoFail = f.Proto()
	} else {
		protoFail = &sessionv1.Failure{
			Kind:    sessionv1.FailureKind_FAILURE_KIND_UNKNOWN,
			Message: msg,
		}
	}
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sess, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sess.Progress == nil {
			sess.Progress = &sessionv1.SessionProgress{}
		}
		r, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		if r.Status != sessionv1.RunStatus_RUN_STATUS_RUNNING && r.Status != sessionv1.RunStatus_RUN_STATUS_WAITING_REQUEST {
			return nil
		}
		settled, _ := tx.SettleRunItems(sessionID, runID, "failed")
		for _, sit := range settled {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "item.updated",
				Item:      sit,
			}); err != nil {
				return err
			}
		}
		r.Status = sessionv1.RunStatus_RUN_STATUS_FAILED
		r.Failure = protoFail
		r.ErrorCode = code
		r.ErrorMessage = msg
		r.CompletedAt = timestamppb.Now()
		if cp != nil {
			r.CheckpointId = cp.CheckpointId
			_ = tx.PutCheckpoint(cp)
		}
		if cumStat != nil {
			sess.Progress.DiffStat = cumStat
		}
		sess.GitStatus = gitStatus
		if err := tx.PutRun(r); err != nil {
			return err
		}
		errItem := &sessionv1.TurnItem{
			ItemId:    newID("err"),
			SessionId: sessionID,
			RunId:     runID,
			Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_ERROR,
			Status:    "failed",
			Text:      msg,
			Failure:   protoFail,
		}
		_ = tx.PutTurnItem(errItem)
		queued, _ := tx.QueuedRuns(sessionID)
		if sess.ActiveRunId == runID {
			sess.ActiveRunId = ""
			sess.PendingRequestId = ""
			sess.Progress.PendingQuestion = nil
			sess.Progress.LastFailure = protoFail
			sess.Error = msg
			sess.QueuedRunCount = uint32(len(queued))
			if len(queued) > 0 {
				sess.QueueHeld = true
			}
			sess.Status = sessionv1.SessionStatus_SESSION_STATUS_CRASHED
		}
		if err := putSessionSynced(tx, sess); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "run.failed",
			Session:   sess,
			Run:       r,
			Item:      errItem,
		})
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
	}
}

// ---------------------------------------------------------------------------
// Policy Gate & Durable RuntimeRequest Lifecycle

func (e *Engine) makeGate(sessionID, workdir string) runtime.GateFunc {
	return func(ctx context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
		sess, err := e.st.Session(sessionID)
		if err != nil {
			return runtime.ToolGateResult{}, err
		}
		if sess.ActiveRunId == "" && sess.ActiveProviderSessionId != "" {
			if pump := e.getLeasePump(sess.ActiveProviderSessionId); pump != nil {
				if h := e.ensureContinuationTurn(pump); h != nil {
					if refreshed, rErr := e.st.Session(sessionID); rErr == nil {
						sess = refreshed
					}
				}
			}
		}
		runID := sess.ActiveRunId
		curRun, _ := e.st.Run(runID)

		if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL && req.Approval != nil && !req.Delegated {
			mode, imode := sess.EffectiveRuntimeMode, sess.InteractionMode
			if curRun != nil {
				mode, imode = curRun.RuntimeMode, curRun.InteractionMode
			}
			rules, _ := e.st.AllowRules(sessionID)
			var extraRoots []string
			if e.cfg.ReadDirs != nil {
				extraRoots = e.cfg.ReadDirs(sess.ProjectId)
			}
			dec := policy.EvaluateTool(policy.ToolEvalInput{
				Category:        req.Approval.ToolCategory,
				ToolName:        req.Approval.ToolName,
				Command:         req.Approval.Command,
				TargetPath:      req.Approval.TargetPath,
				EffectiveMode:   mode,
				GrantCeiling:    e.st.RunGrantCeiling(runID),
				InteractionMode: imode,
				Sandbox:         policy.SandboxScope{WorkspaceRoot: workdir, ExtraRoots: extraRoots},
				AllowedRuleKeys: rules,
			})
			if dec.Allow {
				return runtime.ToolGateResult{Allowed: true}, nil
			}
			if dec.DenyImmediately {
				return runtime.ToolGateResult{Allowed: false, Feedback: dec.Reason}, nil
			}
			req.Approval.OutsideWorkspace = dec.OutsideWorkspace
			req.Approval.RuleKey = dec.RuleKey
			if req.Approval.Reason == "" {
				req.Approval.Reason = dec.Reason
			}
		}

		// Handle question_timeout_seconds == 0 (no one can answer; resolve immediately without pausing).
		var qTimeout time.Duration
		if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
			if curRun != nil && curRun.QuestionTimeoutSeconds != nil {
				if *curRun.QuestionTimeoutSeconds == 0 {
					return runtime.ToolGateResult{Allowed: false, Feedback: noAnswerersPrompt}, nil
				}
				qTimeout = time.Duration(*curRun.QuestionTimeoutSeconds) * time.Second
			} else {
				qTimeout = e.cfg.QuestionTimeout
			}
		}

		if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
			e.maybeRefineTitleBeforeTurnEnd(sessionID, runID, nil, "")
		}

		reqID := newID("req")
		waitCh := make(chan runtime.ToolGateResult, 1)
		e.mu.Lock()
		e.waiters[reqID] = waitCh
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			delete(e.waiters, reqID)
			e.mu.Unlock()
		}()

		now := time.Now()
		var expiresAt *timestamppb.Timestamp
		if qTimeout > 0 {
			expiresAt = timestamppb.New(now.Add(qTimeout))
		}

		rr := &sessionv1.RuntimeRequest{
			RequestId:    reqID,
			SessionId:    sessionID,
			RunId:        runID,
			Kind:         req.Kind,
			Status:       sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING,
			ResponseMode: sessionv1.ResponseMode_RESPONSE_MODE_LIVE_RPC,
			Approval:     req.Approval,
			UserInput:    req.UserInput,
			PlanReview:   req.Plan,
			SecretInput:  req.Secret,
			ExpiresAt:    expiresAt,
		}
		events, err := e.st.Mutate(func(tx *store.Tx) error {
			sNow, err := tx.GetSession(sessionID)
			if err != nil {
				return err
			}
			if sNow.Progress == nil {
				sNow.Progress = &sessionv1.SessionProgress{}
			}
			rNow, err := tx.GetRun(runID)
			if err != nil {
				return err
			}
			rNow.Status = sessionv1.RunStatus_RUN_STATUS_WAITING_REQUEST
			sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
			sNow.PendingRequestId = reqID
			if req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT && req.UserInput != nil {
				sNow.Progress.PendingQuestion = &sessionv1.Question{
					QuestionId:           reqID,
					Turn:                 rNow.Ordinal,
					Items:                req.UserInput.Questions,
					AskedAt:              timestamppb.New(now),
					ExpiresAt:            expiresAt,
					LocalAnswerAvailable: true,
				}
			}
			if err := tx.PutRuntimeRequest(rr); err != nil {
				return err
			}
			item := &sessionv1.TurnItem{
				ItemId:    "reqitm_" + reqID,
				SessionId: sessionID,
				RunId:     runID,
				Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
				Status:    "waiting",
				RequestId: reqID,
			}
			if err := tx.PutTurnItem(item); err != nil {
				return err
			}
			if err := tx.PutRun(rNow); err != nil {
				return err
			}
			if err := putSessionSynced(tx, sNow); err != nil {
				return err
			}
			return tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "request.created",
				Session:   sNow,
				Run:       rNow,
				Item:      item,
				Request:   rr,
			})
		})
		if err != nil {
			return runtime.ToolGateResult{}, err
		}
		e.publish(events, nil)

		var timerC <-chan time.Time
		if qTimeout > 0 {
			timer := time.NewTimer(qTimeout)
			defer timer.Stop()
			timerC = timer.C
		}

		e.mu.Lock()
		runDoneCh := e.runDoneChs[runID]
		e.mu.Unlock()

		expirePending := func() {
			if e.bgCtx.Err() == nil {
				_, _ = e.st.Mutate(func(tx *store.Tx) error {
					cur, err := tx.GetRuntimeRequest(reqID)
					if err == nil && cur.Status == sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
						cur.Status = sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_EXPIRED
						if cur.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
							cur.QuestionResolution = sessionv1.QuestionResolution_QUESTION_RESOLUTION_CANCELLED
						}
						cur.ResolvedAt = timestamppb.Now()
						_ = tx.PutRuntimeRequest(cur)
					}
					return nil
				})
			}
		}

		select {
		case res := <-waitCh:
			return res, nil
		case <-timerC:
			expEvs, _ := e.st.Mutate(func(tx *store.Tx) error {
				cur, err := tx.GetRuntimeRequest(reqID)
				if err != nil || cur.Status != sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
					return nil
				}
				cur.Status = sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_EXPIRED
				cur.QuestionResolution = sessionv1.QuestionResolution_QUESTION_RESOLUTION_TIMED_OUT
				cur.ResolvedAt = timestamppb.Now()
				_ = tx.PutRuntimeRequest(cur)
				reqItem := &sessionv1.TurnItem{
					ItemId:    "reqitm_" + reqID,
					SessionId: sessionID,
					RunId:     runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
					Status:    "expired",
					RequestId: reqID,
				}
				_ = tx.PutTurnItem(reqItem)
				sNow, sErr := tx.GetSession(sessionID)
				if sErr != nil {
					return nil
				}
				if rNow, rErr := tx.GetRun(runID); rErr == nil {
					rNow.Status = sessionv1.RunStatus_RUN_STATUS_RUNNING
					_ = tx.PutRun(rNow)
				}
				sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
				if sNow.PendingRequestId == reqID {
					sNow.PendingRequestId = ""
				}
				if sNow.Progress != nil {
					sNow.Progress.PendingQuestion = nil
				}
				_ = putSessionSynced(tx, sNow)
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: sessionID,
					Type:      "request.resolved",
					Session:   sNow,
					Item:      reqItem,
					Request:   cur,
				})
			})
			e.publish(expEvs, nil)
			return runtime.ToolGateResult{Allowed: false, Feedback: fmt.Sprintf(noAnswerQuestionFormat, qTimeout)}, nil
		case <-runDoneCh:
			expirePending()
			return runtime.ToolGateResult{CancelTurn: true}, context.Canceled
		case <-ctx.Done():
			expirePending()
			if req.Delegated {
				// Answered where the agent itself asked (Her's chat): the turn goes on.
				e.resumeAfterRequest(sessionID, runID, reqID)
			}
			return runtime.ToolGateResult{CancelTurn: true}, ctx.Err()
		}
	}
}

// resumeAfterRequest puts a session back to running once a request it waited on was settled
// elsewhere, unless it already waits on another.
func (e *Engine) resumeAfterRequest(sessionID, runID, reqID string) {
	if e.bgCtx.Err() != nil {
		return
	}
	evs, _ := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil || sNow.PendingRequestId != reqID {
			return nil
		}
		sNow.PendingRequestId = ""
		if sNow.Progress != nil {
			sNow.Progress.PendingQuestion = nil
		}
		if rNow, rErr := tx.GetRun(runID); rErr == nil && rNow.Status == sessionv1.RunStatus_RUN_STATUS_WAITING_REQUEST {
			rNow.Status = sessionv1.RunStatus_RUN_STATUS_RUNNING
			_ = tx.PutRun(rNow)
		}
		if sNow.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
			sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{SessionId: sessionID, Type: "session.updated", Session: sNow})
	})
	e.publish(evs, nil)
}

func (e *Engine) canAnswerQuestion(p *Principal, sess *sessionv1.Session, run *sessionv1.Run) bool {
	if p == nil {
		return false
	}
	if p.CanApproveRequest ||
		p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER ||
		p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER {
		return true
	}
	id := p.Actor.GetId()
	if id == "" {
		return false
	}
	if sess.GetActor().GetId() == id || sess.GetCreatedBy() == id {
		return true
	}
	if run != nil && run.GetAuthor().GetId() == id {
		return true
	}
	return false
}

func (e *Engine) RespondRuntimeRequest(ctx context.Context, p *Principal, req *sessionv1.RespondRuntimeRequestRequest) (*sessionv1.RespondRuntimeRequestResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	if req.Decline && len(req.Answers) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("declining a question takes no answer content"))
	}
	var cached sessionv1.RespondRuntimeRequestResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "RespondRuntimeRequest", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockSession(req.SessionId)
	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	reqs, _ := e.st.RuntimeRequests(req.SessionId)
	var target *sessionv1.RuntimeRequest
	for _, r := range reqs {
		if r.RequestId == req.RequestId {
			target = r
			break
		}
	}
	if target == nil && req.RequestId == "" && sess.PendingRequestId != "" {
		for _, r := range reqs {
			if r.RequestId == sess.PendingRequestId {
				target = r
				break
			}
		}
	}
	if target == nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("runtime request not found"))
	}
	if target.Status != sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("runtime request is already resolved"))
	}

	// Enforce cryptographic security boundary: external Grants CANNOT approve
	// tool executions, plan mode exits, or supply secrets! Only Owner can.
	if target.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL ||
		target.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW ||
		target.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT {
		if !p.CanApproveRequest {
			unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only a trusted owner device may approve permissions, plans, or secrets"))
		}
	} else if target.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
		targetRun, _ := e.st.Run(target.RunId)
		if !e.canAnswerQuestion(p, sess, targetRun) {
			unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("caller is not authorized to answer this question"))
		}
		if req.Decline && len(req.Answers) > 0 {
			unlock()
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cannot decline and provide answers simultaneously"))
		}
	}

	e.mu.Lock()
	waitCh := e.waiters[target.RequestId]
	e.mu.Unlock()

	var gateRes runtime.ToolGateResult
	var resumePrompt string

	switch target.Kind {
	case sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL:
		dec := req.Decision
		if dec == sessionv1.ApprovalDecision_APPROVAL_DECISION_UNSPECIFIED {
			dec = sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
		}
		target.ResolvedDecision = dec
		target.ResolvedFeedback = req.Feedback
		switch dec {
		case sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
			sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_SESSION:
			gateRes = runtime.ToolGateResult{Allowed: true}
		case sessionv1.ApprovalDecision_APPROVAL_DECISION_DECLINE:
			gateRes = runtime.ToolGateResult{Allowed: false, Feedback: req.Feedback}
		case sessionv1.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN:
			gateRes = runtime.ToolGateResult{Allowed: false, CancelTurn: true}
		}

	case sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT:
		if req.Decline {
			target.QuestionResolution = sessionv1.QuestionResolution_QUESTION_RESOLUTION_DECLINED
			gateRes = runtime.ToolGateResult{Allowed: false, Feedback: declinedQuestionMessage}
			resumePrompt = declinedQuestionMessage
		} else {
			target.QuestionResolution = sessionv1.QuestionResolution_QUESTION_RESOLUTION_ANSWERED
			target.ResolvedAnswers = req.Answers
			gateRes = runtime.ToolGateResult{Allowed: true, Answers: req.Answers}
			var parts []string
			for qid, a := range req.Answers {
				vals := append([]string{}, a.SelectedOptions...)
				if a.CustomText != "" {
					vals = append(vals, a.CustomText)
				}
				parts = append(parts, qid+": "+strings.Join(vals, ", "))
			}
			resumePrompt = "Answers to previous questions:\n" + strings.Join(parts, "\n")
		}

	case sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW:
		target.ResolvedFeedback = req.Feedback
		if req.ApprovePlan {
			target.ResolvedDecision = sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
			gateRes = runtime.ToolGateResult{Allowed: true, PlanApproved: true}
			if sess.InteractionMode == sessionv1.InteractionMode_INTERACTION_MODE_PLAN {
				resumePrompt = "Plan approved. Proceed with implementation in Code mode."
			} else {
				resumePrompt = "Approved. Proceed."
			}
		} else if req.Decision == sessionv1.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN {
			target.ResolvedDecision = sessionv1.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN
			gateRes = runtime.ToolGateResult{Allowed: false, PlanApproved: false, CancelTurn: true}
		} else {
			target.ResolvedDecision = sessionv1.ApprovalDecision_APPROVAL_DECISION_DECLINE
			gateRes = runtime.ToolGateResult{Allowed: false, PlanApproved: false, Feedback: req.Feedback}
			if fb := strings.TrimSpace(req.Feedback); fb != "" {
				resumePrompt = "Please revise the plan: " + fb
			} else {
				resumePrompt = "Please revise the plan."
			}
		}

	case sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT:
		// NEVER persist req.SecretValue onto target!
		target.ResolvedDecision = sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
		gateRes = runtime.ToolGateResult{Allowed: true, SecretValue: req.SecretValue}
	}

	target.Status = sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_RESOLVED
	target.ResolvedBy = proto.Clone(p.Actor).(*sessionv1.Actor)
	target.ResolvedAt = timestamppb.Now()

	var res *sessionv1.RespondRuntimeRequestResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(req.SessionId)
		if err != nil {
			return err
		}
		if sNow.Progress == nil {
			sNow.Progress = &sessionv1.SessionProgress{}
		}
		if target.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL &&
			req.Decision == sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_SESSION &&
			target.Approval != nil && target.Approval.RuleKey != "" {
			if err := tx.AddAllowRule(req.SessionId, target.Approval.RuleKey); err != nil {
				return err
			}
		}
		if target.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW && req.ApprovePlan {
			sNow.InteractionMode = sessionv1.InteractionMode_INTERACTION_MODE_CODE
			if rNow, rErr := tx.GetRun(target.RunId); rErr == nil {
				rNow.InteractionMode = sessionv1.InteractionMode_INTERACTION_MODE_CODE
				if err := tx.PutRun(rNow); err != nil {
					return err
				}
			}
			if lease := e.sup.Get(sNow.ActiveProviderSessionId); lease != nil {
				_ = lease.Session.SetModes(ctx, sNow.EffectiveRuntimeMode, sessionv1.InteractionMode_INTERACTION_MODE_CODE)
				lease.InteractionMode = sessionv1.InteractionMode_INTERACTION_MODE_CODE
			}
		}
		if err := tx.PutRuntimeRequest(target); err != nil {
			return err
		}
		reqItem := &sessionv1.TurnItem{
			ItemId:    "reqitm_" + target.RequestId,
			SessionId: req.SessionId,
			RunId:     target.RunId,
			Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
			Status:    "completed",
			RequestId: target.RequestId,
		}
		_ = tx.PutTurnItem(reqItem)
		if waitCh != nil {
			if rNow, rErr := tx.GetRun(target.RunId); rErr == nil {
				rNow.Status = sessionv1.RunStatus_RUN_STATUS_RUNNING
				_ = tx.PutRun(rNow)
			}
			sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
			sNow.PendingRequestId = ""
			sNow.Progress.PendingQuestion = nil
		} else {
			sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
			sNow.PendingRequestId = ""
			sNow.Progress.PendingQuestion = nil
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: req.SessionId,
			Type:      "request.resolved",
			Session:   sNow,
			Item:      reqItem,
			Request:   target,
		}); err != nil {
			return err
		}
		res = &sessionv1.RespondRuntimeRequestResponse{
			Session: proto.Clone(sNow).(*sessionv1.Session),
			Request: proto.Clone(target).(*sessionv1.RuntimeRequest),
		}
		return tx.PutReceipt(req.CommandId, "RespondRuntimeRequest", res)
	})
	unlock()
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)

	if waitCh != nil {
		waitCh <- gateRes
	} else if resumePrompt != "" {
		if dispRes, dErr := e.DispatchMessage(ctx, p, &sessionv1.DispatchMessageRequest{
			SessionId: req.SessionId,
			Text:      resumePrompt,
			Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_START,
		}); dErr == nil && dispRes.GetSession() != nil {
			res.Session = dispRes.Session
		}
	}
	return res, nil
}

func dismissPendingRequestTx(tx *store.Tx, sNow *sessionv1.Session, actor *sessionv1.Actor) (*sessionv1.RuntimeRequest, *sessionv1.TurnItem, error) {
	if sNow.PendingRequestId == "" {
		return nil, nil, nil
	}
	var supersededReq *sessionv1.RuntimeRequest
	var supersededItem *sessionv1.TurnItem
	if prevReq, rErr := tx.GetRuntimeRequest(sNow.PendingRequestId); rErr == nil &&
		prevReq.Status == sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
		prevReq.Status = sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_DISMISSED
		if prevReq.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
			prevReq.QuestionResolution = sessionv1.QuestionResolution_QUESTION_RESOLUTION_CANCELLED
		}
		if actor != nil {
			prevReq.ResolvedBy = proto.Clone(actor).(*sessionv1.Actor)
		}
		prevReq.ResolvedAt = timestamppb.Now()
		if err := tx.PutRuntimeRequest(prevReq); err != nil {
			return nil, nil, err
		}
		supersededItem = &sessionv1.TurnItem{
			ItemId:    "reqitm_" + prevReq.RequestId,
			SessionId: sNow.SessionId,
			RunId:     prevReq.RunId,
			Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
			Status:    "dismissed",
			RequestId: prevReq.RequestId,
		}
		_ = tx.PutTurnItem(supersededItem)
		supersededReq = prevReq
	}
	sNow.PendingRequestId = ""
	if sNow.Progress != nil {
		sNow.Progress.PendingQuestion = nil
	}
	return supersededReq, supersededItem, nil
}

// ---------------------------------------------------------------------------
// Interrupt, Queue Management, Session Updates, Checkpoints, Diff & Fork

func (e *Engine) InterruptSession(ctx context.Context, p *Principal, req *sessionv1.InterruptSessionRequest) (*sessionv1.InterruptSessionResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	unlock := e.lockSession(req.SessionId)
	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	if req.RunId != "" && req.RunId != sess.ActiveRunId {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("run is not this session's active run"))
	}
	activeRunID := sess.ActiveRunId
	psID := sess.ActiveProviderSessionId
	var dismissEvents []*sessionv1.SessionEvent

	if req.RunId == "" && activeRunID == "" && sess.PendingRequestId == "" && !req.HoldQueue && (sess.ActiveBackgroundCount > 0 || sess.ActiveDaemonCount > 0) {
		unlock()
		stopRes, err := e.StopBackgroundTask(ctx, p, &sessionv1.StopBackgroundTaskRequest{SessionId: req.SessionId})
		if err != nil {
			return nil, err
		}
		return &sessionv1.InterruptSessionResponse{Session: stopRes.Session}, nil
	}

	if req.HoldQueue || (activeRunID == "" && sess.PendingRequestId != "") {
		dismissEvents, _ = e.st.Mutate(func(tx *store.Tx) error {
			sNow, err := tx.GetSession(req.SessionId)
			if err != nil {
				return err
			}
			if req.HoldQueue {
				sNow.QueueHeld = true
			}
			var supReq *sessionv1.RuntimeRequest
			var supItem *sessionv1.TurnItem
			if activeRunID == "" && sNow.PendingRequestId != "" {
				supReq, supItem, err = dismissPendingRequestTx(tx, sNow, p.Actor)
				if err != nil {
					return err
				}
				queued, _ := tx.QueuedRuns(req.SessionId)
				sNow.QueuedRunCount = uint32(len(queued))
				if len(queued) > 0 {
					sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_QUEUED
				} else {
					sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
				}
			}
			if err := putSessionSynced(tx, sNow); err != nil {
				return err
			}
			if supReq != nil {
				return tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: req.SessionId,
					Type:      "request.resolved",
					Session:   sNow,
					Item:      supItem,
					Request:   supReq,
				})
			}
			return nil
		})
	}
	unlock()
	if len(dismissEvents) > 0 {
		e.publish(dismissEvents, nil)
	}

	who := "owner"
	if p.Actor != nil && p.Actor.Kind != sessionv1.ActorKind_ACTOR_KIND_OWNER && p.Actor.Id != "" {
		who = p.Actor.Id
	}
	intReason := "interrupted by " + who
	if strings.TrimSpace(req.Reason) != "" {
		intReason = req.Reason
	}
	pendingReqID := sess.PendingRequestId
	if activeRunID != "" {
		e.mu.Lock()
		e.runInterruptedBy[activeRunID] = intReason
		var waitCh chan runtime.ToolGateResult
		if pendingReqID != "" {
			waitCh = e.waiters[pendingReqID]
		}
		e.mu.Unlock()
		if waitCh != nil {
			select {
			case waitCh <- runtime.ToolGateResult{CancelTurn: true}:
			default:
			}
		}
	}
	lease := e.sup.Get(psID)
	if req.Force || lease == nil {
		if lease != nil {
			e.stopLeasePump(psID, lease)
			e.sup.DetachSession(psID, lease.Session, true)
		}
		e.settleSessionBackgroundTasks(req.SessionId, psID, intReason)
		if activeRunID != "" {
			e.mu.Lock()
			cancel := e.runCancels[activeRunID]
			e.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			if req.Force {
				e.finishRunInterrupted(req.SessionId, activeRunID, intReason, nil, nil)
			}
		}
	} else {
		_ = lease.Session.Interrupt(ctx)
		if activeRunID != "" {
			sid := req.SessionId
			rid := activeRunID
			targetSession := lease.Session
			time.AfterFunc(2*time.Second, func() {
				if e.bgCtx.Err() != nil {
					return
				}
				e.mu.Lock()
				cancel, stillActive := e.runCancels[rid]
				e.mu.Unlock()
				if !stillActive {
					return
				}
				e.stopLeasePump(psID, lease)
				e.sup.DetachSession(psID, targetSession, true)
				e.settleSessionBackgroundTasks(sid, psID, "session force-stopped")
				if cancel != nil {
					cancel()
				}
			})
		}
	}

	updated, err := e.st.Session(req.SessionId)
	if err != nil {
		return nil, err
	}
	return &sessionv1.InterruptSessionResponse{Session: updated}, nil
}

func (e *Engine) StopBackgroundTask(ctx context.Context, p *Principal, req *sessionv1.StopBackgroundTaskRequest) (*sessionv1.StopBackgroundTaskResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached sessionv1.StopBackgroundTaskResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "StopBackgroundTask", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockSession(req.SessionId)
	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	existingTasks, _ := e.st.BackgroundTasks(req.SessionId)
	effectiveTaskID := strings.TrimSpace(req.TaskId)
	if effectiveTaskID != "" {
		for _, bt := range existingTasks {
			if bt.TaskId == effectiveTaskID && bt.StopScope == sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_SESSION {
				// Session-scoped background tasks share session/cancel, stopping all session tasks.
				effectiveTaskID = ""
				break
			}
		}
	}
	psID := sess.ActiveProviderSessionId
	unlock()

	if lease := e.sup.Get(psID); lease != nil {
		if bc, ok := lease.Session.(runtime.BackgroundController); ok {
			_ = bc.StopBackgroundTask(ctx, req.TaskId)
		} else {
			_ = lease.Session.Interrupt(ctx)
		}
	}

	unlock = e.lockSession(req.SessionId)
	var res *sessionv1.StopBackgroundTaskResponse
	var awaitedCnt, daemonCnt uint32
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(req.SessionId)
		if err != nil {
			return err
		}
		stopped, items, err := tx.StopRunningBackgroundTasks(req.SessionId, effectiveTaskID, "stopped by user")
		if err != nil {
			return err
		}
		awaitedCnt, daemonCnt, _ = tx.RefreshSessionBackgroundCounts(sNow)
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		for _, it := range items {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: req.SessionId,
				Type:      "item.updated",
				Session:   sNow,
				Item:      it,
			}); err != nil {
				return err
			}
		}
		for _, bt := range stopped {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId:      req.SessionId,
				Type:           "background_task.updated",
				Session:        sNow,
				BackgroundTask: bt,
			}); err != nil {
				return err
			}
		}
		res = &sessionv1.StopBackgroundTaskResponse{
			Session:      proto.Clone(sNow).(*sessionv1.Session),
			StoppedTasks: stopped,
		}
		return tx.PutReceipt(req.CommandId, "StopBackgroundTask", res)
	})
	unlock()
	if err != nil {
		return nil, err
	}
	if lease := e.sup.Get(psID); lease != nil {
		lease.SetBackgroundCounts(int(awaitedCnt), int(daemonCnt))
	}
	e.publish(events, nil)
	return res, nil
}

func (e *Engine) ManageQueue(ctx context.Context, p *Principal, req *sessionv1.ManageQueueRequest) (*sessionv1.ManageQueueResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached sessionv1.ManageQueueResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "ManageQueue", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockSession(req.SessionId)
	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	// Promote-to-steer while an active run is running:
	if req.Operation == sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER && sess.ActiveRunId != "" {
		if sess.ActiveRunId == req.RunId {
			var res *sessionv1.ManageQueueResponse
			_, _ = e.st.Mutate(func(tx *store.Tx) error {
				rem, _ := tx.QueuedRuns(req.SessionId)
				res = &sessionv1.ManageQueueResponse{Session: proto.Clone(sess).(*sessionv1.Session), QueuedRuns: rem}
				return tx.PutReceipt(req.CommandId, "ManageQueue", res)
			})
			unlock()
			return res, nil
		}
		runs, _ := e.st.Runs(req.SessionId)
		var qRun *sessionv1.Run
		for _, r := range runs {
			if r.RunId == req.RunId && r.Status == sessionv1.RunStatus_RUN_STATUS_QUEUED {
				qRun = r
				break
			}
		}
		if qRun == nil {
			unlock()
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
		}
		_, isShellShortcut := ParseShellShortcut(qRun.GetAuthor(), qRun.Prompt)
		lease := e.sup.Get(sess.ActiveProviderSessionId)
		canLiveSteer := !isShellShortcut &&
			qRun.Intent != sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT &&
			sess.Status == sessionv1.SessionStatus_SESSION_STATUS_RUNNING &&
			sess.PendingRequestId == "" &&
			lease != nil &&
			(qRun.ModelSelection == nil || SelectionsEqual(lease.Selection, qRun.ModelSelection))
		if canLiveSteer {
			if err := lease.Session.Steer(ctx, qRun.Prompt, e.loadTurnAttachments(qRun.Attachments)); err == nil {
				var res *sessionv1.ManageQueueResponse
				events, mErr := e.st.Mutate(func(tx *store.Tx) error {
					sNow, _ := tx.GetSession(req.SessionId)
					sNow.QueueHeld = false
					qRun.Status = sessionv1.RunStatus_RUN_STATUS_CANCELLED
					qRun.CompletedAt = timestamppb.Now()
					_ = tx.PutRun(qRun)
					item := &sessionv1.TurnItem{
						ItemId:      newID("itm"),
						SessionId:   req.SessionId,
						RunId:       sNow.ActiveRunId,
						Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
						Status:      "promoted_to_steer",
						Actor:       qRun.Author,
						Text:        qRun.Prompt,
						Attachments: qRun.Attachments,
					}
					_ = tx.PutTurnItem(item)
					rem, _ := tx.QueuedRuns(req.SessionId)
					sNow.QueuedRunCount = uint32(len(rem))
					_ = putSessionSynced(tx, sNow)
					_ = tx.AppendEvent(&sessionv1.SessionEvent{
						SessionId: req.SessionId,
						Type:      "queue.promoted",
						Session:   sNow,
						Run:       qRun,
						Item:      item,
					})
					res = &sessionv1.ManageQueueResponse{Session: proto.Clone(sNow).(*sessionv1.Session), QueuedRuns: rem}
					return tx.PutReceipt(req.CommandId, "ManageQueue", res)
				})
				unlock()
				if mErr != nil {
					return nil, mErr
				}
				e.publish(events, nil)
				return res, nil
			}
		}
	}

	var startNow *sessionv1.Run
	var interruptRunID, interruptPSID, interruptPendingReqID string
	var res *sessionv1.ManageQueueResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(req.SessionId)
		if err != nil {
			return err
		}
		if sNow.Progress == nil {
			sNow.Progress = &sessionv1.SessionProgress{}
		}
		queued, err := tx.QueuedRuns(req.SessionId)
		if err != nil {
			return err
		}

		var updatedRuns []*sessionv1.Run
		var startedItem *sessionv1.TurnItem

		switch req.Operation {
		case sessionv1.QueueOperation_QUEUE_OPERATION_EDIT:
			var target *sessionv1.Run
			for _, r := range queued {
				if r.RunId == req.RunId {
					target = r
					break
				}
			}
			if target == nil {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("edited message cannot be empty"))
			}
			target.Prompt = req.Text
			if req.Attachments != nil {
				target.Attachments = req.Attachments
			}
			if err := tx.PutRun(target); err != nil {
				return err
			}
			updatedRuns = []*sessionv1.Run{target}

		case sessionv1.QueueOperation_QUEUE_OPERATION_CANCEL:
			var target *sessionv1.Run
			for _, r := range queued {
				if r.RunId == req.RunId {
					target = r
					break
				}
			}
			if target == nil {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			target.Status = sessionv1.RunStatus_RUN_STATUS_CANCELLED
			target.CompletedAt = timestamppb.Now()
			if err := tx.PutRun(target); err != nil {
				return err
			}
			updatedRuns = []*sessionv1.Run{target}

		case sessionv1.QueueOperation_QUEUE_OPERATION_REORDER:
			idx := slices.IndexFunc(queued, func(r *sessionv1.Run) bool { return r.RunId == req.RunId })
			if idx < 0 {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			moving := queued[idx]
			rest := slices.Delete(slices.Clone(queued), idx, idx+1)
			insertAt := len(rest)
			if req.BeforeRunId != "" {
				if bIdx := slices.IndexFunc(rest, func(r *sessionv1.Run) bool { return r.RunId == req.BeforeRunId }); bIdx >= 0 {
					insertAt = bIdx
				}
			}
			reordered := slices.Insert(rest, insertAt, moving)
			for i, r := range reordered {
				r.QueueOrder = float64((i + 1) * 1000)
				if err := tx.PutRun(r); err != nil {
					return err
				}
			}
			updatedRuns = reordered

		case sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER:
			sNow.QueueHeld = false
			if sNow.ActiveRunId != "" {
				if sNow.ActiveRunId != req.RunId {
					idx := slices.IndexFunc(queued, func(r *sessionv1.Run) bool { return r.RunId == req.RunId })
					if idx < 0 {
						return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
					}
					if idx > 0 {
						moving := queued[idx]
						rest := slices.Delete(slices.Clone(queued), idx, idx+1)
						reordered := slices.Insert(rest, 0, moving)
						for i, r := range reordered {
							r.QueueOrder = float64((i + 1) * 1000)
							if err := tx.PutRun(r); err != nil {
								return err
							}
						}
						updatedRuns = reordered
					} else {
						updatedRuns = []*sessionv1.Run{queued[0]}
					}
					interruptRunID = sNow.ActiveRunId
					interruptPSID = sNow.ActiveProviderSessionId
					interruptPendingReqID = sNow.PendingRequestId
				}
				break
			}
			if len(queued) == 0 {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			if req.RunId != "" && !slices.ContainsFunc(queued, func(r *sessionv1.Run) bool { return r.RunId == req.RunId }) {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			fallthrough

		case sessionv1.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE:
			sNow.QueueHeld = false
			if sNow.ActiveRunId == "" && len(queued) > 0 {
				supReq, supItem, dErr := dismissPendingRequestTx(tx, sNow, p.Actor)
				if dErr != nil {
					return dErr
				}
				pickIdx := 0
				if req.RunId != "" {
					if idx := slices.IndexFunc(queued, func(r *sessionv1.Run) bool { return r.RunId == req.RunId }); idx >= 0 {
						pickIdx = idx
					}
				}
				now := timestamppb.Now()
				startNow = queued[pickIdx]
				sNow.Error = ""
				sNow.Progress.LastFailure = nil
				sNow.Progress.TurnCount++
				sNow.Progress.TurnStartedAt = now
				startNow.Ordinal = sNow.Progress.TurnCount
				startNow.Status = sessionv1.RunStatus_RUN_STATUS_RUNNING
				startNow.StartedAt = now
				if err := tx.PutRun(startNow); err != nil {
					return err
				}
				startedItem = &sessionv1.TurnItem{
					ItemId:      newID("itm"),
					SessionId:   req.SessionId,
					RunId:       startNow.RunId,
					Kind:        sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
					Status:      "completed",
					Actor:       startNow.Author,
					Text:        startNow.Prompt,
					Attachments: startNow.Attachments,
				}
				_ = tx.PutTurnItem(startedItem)
				sNow.ActiveRunId = startNow.RunId
				sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
				sNow.ModelSelection = startNow.ModelSelection
				sNow.EffectiveRuntimeMode = startNow.RuntimeMode
				sNow.InteractionMode = startNow.InteractionMode
				updatedRuns = []*sessionv1.Run{startNow}
				if supReq != nil {
					_ = tx.AppendEvent(&sessionv1.SessionEvent{
						SessionId: req.SessionId,
						Type:      "request.resolved",
						Session:   sNow,
						Item:      supItem,
						Request:   supReq,
					})
				}
			}

		default:
			return connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported queue operation"))
		}

		rem, err := tx.QueuedRuns(req.SessionId)
		if err != nil {
			return err
		}
		sNow.QueuedRunCount = uint32(len(rem))
		if sNow.ActiveRunId == "" && len(rem) == 0 && sNow.Status == sessionv1.SessionStatus_SESSION_STATUS_QUEUED {
			sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		if len(updatedRuns) == 0 {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: req.SessionId,
				Type:      "queue.updated",
				Session:   sNow,
			}); err != nil {
				return err
			}
		} else {
			for i, ur := range updatedRuns {
				var it *sessionv1.TurnItem
				if i == 0 {
					it = startedItem
				}
				if err := tx.AppendEvent(&sessionv1.SessionEvent{
					SessionId: req.SessionId,
					Type:      "queue.updated",
					Session:   sNow,
					Run:       ur,
					Item:      it,
				}); err != nil {
					return err
				}
			}
		}
		res = &sessionv1.ManageQueueResponse{
			Session:    proto.Clone(sNow).(*sessionv1.Session),
			QueuedRuns: rem,
		}
		return tx.PutReceipt(req.CommandId, "ManageQueue", res)
	})
	unlock()
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	if interruptRunID != "" {
		who := "owner"
		if p.Actor != nil && p.Actor.Kind != sessionv1.ActorKind_ACTOR_KIND_OWNER && p.Actor.Id != "" {
			who = p.Actor.Id
		}
		e.interruptActiveRunForSteer(ctx, req.SessionId, interruptRunID, interruptPSID, interruptPendingReqID, "steered by "+who)
	}
	if startNow != nil {
		e.spawnRun(req.SessionId, startNow.RunId, startNow.Intent == sessionv1.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT)
	}
	return res, nil
}

func (e *Engine) interruptActiveRunForSteer(ctx context.Context, sessionID, activeRunID, psID, pendingReqID, reason string) {
	if cur, err := e.st.Session(sessionID); err != nil || cur.ActiveRunId != activeRunID {
		return
	}
	e.mu.Lock()
	cancel, stillActive := e.runCancels[activeRunID]
	if !stillActive {
		e.mu.Unlock()
		return
	}
	e.runInterruptedBy[activeRunID] = reason
	var waitCh chan runtime.ToolGateResult
	if pendingReqID != "" {
		waitCh = e.waiters[pendingReqID]
	}
	e.mu.Unlock()

	if waitCh != nil {
		select {
		case waitCh <- runtime.ToolGateResult{CancelTurn: true}:
		default:
		}
	}
	lease := e.sup.Get(psID)
	if lease == nil {
		if cancel != nil {
			cancel()
		}
		return
	}
	_ = lease.Session.Interrupt(ctx)
	targetSession := lease.Session
	time.AfterFunc(2*time.Second, func() {
		if e.bgCtx.Err() != nil {
			return
		}
		e.mu.Lock()
		cancelNow, activeNow := e.runCancels[activeRunID]
		e.mu.Unlock()
		if !activeNow {
			return
		}
		e.stopLeasePump(psID, lease)
		e.sup.DetachSession(psID, targetSession, true)
		if cancelNow != nil {
			cancelNow()
		}
	})
}

func (e *Engine) UpdateSession(ctx context.Context, p *Principal, req *sessionv1.UpdateSessionRequest) (*sessionv1.UpdateSessionResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached sessionv1.UpdateSessionResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "UpdateSession", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockSession(req.SessionId)
	defer unlock()

	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	nextSel := proto.Clone(sess.ModelSelection).(*sessionv1.ModelSelection)
	if req.ModelSelection != nil && req.ModelSelection.InstanceId != "" {
		instCfg, _, err := e.resolveInstanceConfig(p, sess.ProjectId, req.ModelSelection.InstanceId)
		if err != nil {
			return nil, err
		}
		if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit target provider"))
		}
		if err := e.projectAllowsInstance(sess.ProjectId, instCfg.InstanceId); err != nil {
			return nil, err
		}
		nextSel = proto.Clone(req.ModelSelection).(*sessionv1.ModelSelection)
	} else if req.ModelSelection != nil && (req.ModelSelection.Model != "" || len(req.ModelSelection.Options) > 0) {
		if nextSel == nil {
			nextSel = &sessionv1.ModelSelection{}
		}
		if req.ModelSelection.Model != "" {
			nextSel.Model = req.ModelSelection.Model
		}
		if len(req.ModelSelection.Options) > 0 {
			nextSel.Options = proto.Clone(req.ModelSelection).(*sessionv1.ModelSelection).Options
		}
	}

	// Pinning, renaming or archiving must not recompute the ceiling with the caller's grant.
	nextReqMode, nextEffMode, nextMaxMode := sess.RuntimeMode, sess.EffectiveRuntimeMode, sess.MaxRuntimeMode
	if req.RuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		nextReqMode = req.RuntimeMode
		nextEffMode = policy.EffectiveMode(e.machineMax(), p.GrantCeiling, nextReqMode)
		nextMaxMode = policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling)
	}

	nextIMode := sess.InteractionMode
	if req.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		nextIMode = req.InteractionMode
	}

	instCfg, drv, _ := e.resolveInstanceConfig(p, sess.ProjectId, nextSel.GetInstanceId())
	var caps *sessionv1.ProviderCapabilities
	if drv != nil && instCfg != nil {
		caps = drv.Capabilities(instCfg.AcpFlavor)
	}
	var currentPS *sessionv1.ProviderSession
	if sess.ActiveProviderSessionId != "" {
		pss, _ := e.st.ProviderSessions(req.SessionId)
		for _, ps := range pss {
			if ps.ProviderSessionId == sess.ActiveProviderSessionId {
				currentPS = ps
				break
			}
		}
	}
	lease := e.sup.Get(sess.ActiveProviderSessionId)
	plan := DecideTransition(TransitionInput{
		CurrentBinding:      currentPS,
		LiveLease:           lease,
		NextSelection:       nextSel,
		NextEffectiveMode:   nextEffMode,
		NextInteractionMode: nextIMode,
		Capabilities:        caps,
	})

	if sess.ActiveRunId == "" && plan.Action == TransitionSwitchInSession && lease != nil {
		if plan.ModelChanged || plan.OptionsChanged {
			if err := lease.Session.SetSelection(ctx, nextSel); err == nil {
				lease.Selection = proto.Clone(nextSel).(*sessionv1.ModelSelection)
			}
		}
		if plan.ModeChanged {
			if err := lease.Session.SetModes(ctx, nextEffMode, nextIMode); err == nil {
				lease.RuntimeMode = nextEffMode
				lease.InteractionMode = nextIMode
			}
		}
	}

	cleanTitle := runtime.CleanTitle(req.Title)
	if cleanTitle != "" || req.RegenerateTitle {
		e.cancelTitleGen(req.SessionId)
	}
	if cleanTitle != "" && lease != nil {
		if titler, ok := lease.Session.(runtime.Titler); ok {
			_ = titler.SetTitle(ctx, cleanTitle)
		}
	}

	archivingNow := req.HasArchived && req.Archived && sess.Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
	unarchivingNow := req.HasArchived && !req.Archived && sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
	if archivingNow && (sess.ActiveRunId != "" || sess.PendingRequestId != "" || sess.ActiveBackgroundCount > 0 ||
		sess.Status == sessionv1.SessionStatus_SESSION_STATUS_QUEUED ||
		sess.Status == sessionv1.SessionStatus_SESSION_STATUS_PREPARING ||
		sess.Status == sessionv1.SessionStatus_SESSION_STATUS_RUNNING ||
		sess.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot archive a session while a run is active; interrupt it first"))
	}
	if archivingNow && sess.ActiveDaemonCount > 0 && lease != nil {
		if bc, ok := lease.Session.(runtime.BackgroundController); ok {
			_ = bc.StopBackgroundTask(ctx, "")
		}
	}

	var res *sessionv1.UpdateSessionResponse
	var regenTitleSeq uint64
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(req.SessionId)
		if err != nil {
			return err
		}
		sNow.ModelSelection = nextSel
		sNow.RuntimeMode = nextReqMode
		sNow.EffectiveRuntimeMode = nextEffMode
		sNow.MaxRuntimeMode = nextMaxMode
		sNow.InteractionMode = nextIMode
		// The running turn's gate reads its run's mode: a mode set now applies to its next tool call,
		// still within the ceiling of the grant that started it.
		if req.RuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED && sNow.ActiveRunId != "" {
			rNow, err := tx.GetRun(sNow.ActiveRunId)
			if err != nil {
				return err
			}
			rNow.RuntimeMode = policy.EffectiveMode(e.machineMax(), e.st.RunGrantCeiling(rNow.RunId), nextEffMode)
			if err := tx.PutRun(rNow); err != nil {
				return err
			}
		}
		if cleanTitle != "" {
			sNow.Title = cleanTitle
			sNow.TitleRenamed = true
			sNow.TitleState = sessionv1.TitleState_TITLE_STATE_LOCKED
			sNow.TitleGenerating = false
			sNow.TitleSeq++
			if req.Locale != "" {
				sNow.TitleLocale = runtime.ResolveLocale(req.Locale, sNow.TitleLocale)
			}
		} else if req.RegenerateTitle && e.hasTitleGenerator() {
			sNow.TitleLocale = runtime.ResolveLocale(req.Locale, sNow.TitleLocale)
			sNow.TitleRenamed = false
			sNow.TitleState = sessionv1.TitleState_TITLE_STATE_SEED
			sNow.TitleGenerating = true
			sNow.TitleSeq++
			regenTitleSeq = sNow.TitleSeq
		}
		if req.HasPinned {
			sNow.Pinned = req.Pinned
		}
		var stoppedBg []*sessionv1.BackgroundTask
		var stoppedItems []*sessionv1.TurnItem
		if archivingNow && sNow.ActiveDaemonCount > 0 {
			stoppedBg, stoppedItems, err = tx.StopRunningBackgroundTasks(req.SessionId, "", "session archived")
			if err != nil {
				return err
			}
			_, _, _ = tx.RefreshSessionBackgroundCounts(sNow)
		}
		if req.HasArchived {
			if req.Archived {
				if sNow.Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
					sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
					sNow.ArchivedAt = timestamppb.Now()
				}
			} else if sNow.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
				sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
				sNow.ArchivedAt = nil
			}
		}
		pinOnly := req.HasPinned &&
			cleanTitle == "" &&
			!req.RegenerateTitle &&
			!req.HasArchived &&
			req.ModelSelection == nil &&
			req.RuntimeMode == identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED &&
			req.InteractionMode == sessionv1.InteractionMode_INTERACTION_MODE_UNSPECIFIED
		if pinOnly {
			syncSessionSummary(sNow)
			if err := tx.PutSessionPreserveUpdatedAt(sNow); err != nil {
				return err
			}
		} else if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		for _, it := range stoppedItems {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: req.SessionId,
				Type:      "item.updated",
				Session:   sNow,
				Item:      it,
			}); err != nil {
				return err
			}
		}
		for _, bt := range stoppedBg {
			if err := tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId:      req.SessionId,
				Type:           "background_task.updated",
				Session:        sNow,
				BackgroundTask: bt,
			}); err != nil {
				return err
			}
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: req.SessionId,
			Type:      "session.updated",
			Session:   sNow,
		}); err != nil {
			return err
		}
		res = &sessionv1.UpdateSessionResponse{
			Session:           proto.Clone(sNow).(*sessionv1.Session),
			TransitionApplied: string(plan.Action),
		}
		return tx.PutReceipt(req.CommandId, "UpdateSession", res)
	})
	if err != nil {
		return nil, err
	}
	nativeRef := currentPS.GetNativeSessionId()
	if archivingNow {
		if sess.ActiveProviderSessionId != "" {
			e.sup.Detach(sess.ActiveProviderSessionId, false)
		}
		if e.cfg.DataDir != "" {
			_ = os.RemoveAll(filepath.Join(e.cfg.DataDir, "sessions", sess.SessionId, "tmp"))
		}
		if nativeRef != "" {
			if arch, ok := drv.(runtime.Archiver); ok {
				_ = arch.Archive(ctx, nativeRef, true)
			}
		}
		if sess.GetWorkspaceStrategy().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE && sess.GetWorkspaceStrategy().GetWorktreePath() != "" {
			if e.cfg.ReleaseOnArchive != nil {
				if e.cfg.ReleaseOnArchive(ctx, res.Session) {
					res.TransitionApplied = "workspace_kept"
					if req.CommandId != "" {
						_, _ = e.st.Mutate(func(tx *store.Tx) error {
							return tx.PutReceipt(req.CommandId, "UpdateSession", res)
						})
					}
				}
			} else if ws, wErr := e.st.Workspace(sess.ProjectId); wErr == nil {
				_ = e.vcs.RemoveWorktree(ctx, ws.Path, sess.GetWorkspaceStrategy().GetWorktreePath())
			}
		}
	}
	if unarchivingNow {
		if e.cfg.DataDir != "" {
			_ = os.MkdirAll(filepath.Join(e.cfg.DataDir, "sessions", sess.SessionId, "tmp"), 0o700)
		}
		if nativeRef != "" {
			if arch, ok := drv.(runtime.Archiver); ok {
				_ = arch.Archive(ctx, nativeRef, false)
			}
		}
		if res != nil && res.Session != nil {
			_, _ = e.resolveWorkdirCtx(ctx, res.Session)
		}
	}
	e.publish(events, nil)
	if regenTitleSeq > 0 {
		e.spawnTitleGen(req.SessionId, regenTitleSeq, titleGenModeRegenerate, nil)
	}
	return res, nil
}

func (e *Engine) ListCheckpoints(_ context.Context, p *Principal, sessionID string) ([]*sessionv1.Checkpoint, error) {
	sess, err := e.st.Session(sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	return e.st.Checkpoints(sessionID)
}

func (e *Engine) GetDiff(ctx context.Context, p *Principal, req *sessionv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	workdir, err := e.resolveWorkdir(sess)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if !vcs.IsGitRepo(ctx, workdir) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session works in place without a Git repository"))
	}
	ckpts, _ := e.st.Checkpoints(req.SessionId)

	var fullDiff string
	var files []*sessionv1.CheckpointFile
	var baseCommit, headCommit string

	if req.Turn > 0 {
		var prevCp, turnCp *sessionv1.Checkpoint
		for _, c := range ckpts {
			if c.RunOrdinal < req.Turn {
				prevCp = c
			}
			if c.RunOrdinal == req.Turn {
				turnCp = c
				break
			}
		}
		if turnCp != nil && prevCp != nil {
			baseCommit = prevCp.HeadOid
			headCommit = turnCp.HeadOid
			fullDiff, files, err = e.vcs.DiffTrees(ctx, workdir, prevCp.GitTreeOid, turnCp.GitTreeOid)
			if err != nil {
				return nil, err
			}
		} else if turnCp != nil {
			files = turnCp.Files
			headCommit = turnCp.HeadOid
		}
	} else {
		baseTree := ""
		if req.CheckpointId != "" {
			for _, c := range ckpts {
				if c.CheckpointId == req.CheckpointId {
					baseTree = c.GitTreeOid
					baseCommit = c.HeadOid
					break
				}
			}
		} else if len(ckpts) > 0 && ckpts[0].RunOrdinal == 0 {
			baseTree = ckpts[0].GitTreeOid
			baseCommit = ckpts[0].HeadOid
		}
		fullDiff, files, err = e.vcs.DiffWorkingTree(ctx, workdir, baseTree)
		if err != nil {
			return nil, err
		}
	}

	stat := summarizeDiffFiles(files)
	if stat == nil {
		stat = &sessionv1.DiffStat{}
	}
	if req.StatOnly {
		return &sessionv1.GetDiffResponse{
			Stat:       stat,
			Files:      files,
			BaseCommit: baseCommit,
			HeadCommit: headCommit,
		}, nil
	}

	start := int(min(req.Offset, uint64(len(fullDiff))))
	rem := fullDiff[start:]
	truncated := false
	var nextOffset uint64
	if len(rem) > maxDiffPageBytes {
		cut := maxDiffPageBytes
		for cut > 0 && !utf8.RuneStart(rem[cut]) {
			cut--
		}
		rem = rem[:cut]
		truncated = true
		nextOffset = uint64(start + cut)
	}
	return &sessionv1.GetDiffResponse{
		Diff:       rem,
		Stat:       stat,
		Files:      files,
		Truncated:  truncated,
		NextOffset: nextOffset,
		BaseCommit: baseCommit,
		HeadCommit: headCommit,
	}, nil
}

func (e *Engine) RollbackCheckpoint(ctx context.Context, p *Principal, req *sessionv1.RollbackCheckpointRequest) (*sessionv1.RollbackCheckpointResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached sessionv1.RollbackCheckpointResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "RollbackCheckpoint", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockSession(req.SessionId)
	defer unlock()

	sess, err := e.st.Session(req.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	if sess.ActiveRunId != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot rollback while a run is active; stop it first"))
	}

	ckpts, _ := e.st.Checkpoints(req.SessionId)
	var target *sessionv1.Checkpoint
	for _, c := range ckpts {
		if c.CheckpointId == req.CheckpointId {
			target = c
			break
		}
	}
	if target == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("checkpoint not found"))
	}

	if req.RestoreFiles {
		workdir, err := e.resolveWorkdir(sess)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		if err := e.vcs.RestoreTree(ctx, workdir, target.GitTreeOid); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	if sess.ActiveProviderSessionId != "" {
		e.sup.Detach(sess.ActiveProviderSessionId, false)
	}
	var rbCp *sessionv1.Checkpoint
	if req.RestoreFiles {
		rbCp = target
	}
	gitStatus := e.inspectSessionGitStatus(ctx, sess, rbCp, nil)

	runs, _ := e.st.Runs(req.SessionId)
	var res *sessionv1.RollbackCheckpointResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(req.SessionId)
		if err != nil {
			return err
		}
		if sNow.Progress == nil {
			sNow.Progress = &sessionv1.SessionProgress{}
		}
		removedRuns := map[string]bool{}
		for _, r := range runs {
			if r.Ordinal > target.RunOrdinal && r.Status != sessionv1.RunStatus_RUN_STATUS_QUEUED {
				r.Status = sessionv1.RunStatus_RUN_STATUS_ROLLED_BACK
				_ = tx.PutRun(r)
				removedRuns[r.RunId] = true
			}
		}
		if err := tx.DeleteTurnItemsAfterRunIds(req.SessionId, removedRuns); err != nil {
			return err
		}
		_, _, _ = tx.StopRunningBackgroundTasks(req.SessionId, "", "rewound to earlier checkpoint")
		_, _, _ = tx.RefreshSessionBackgroundCounts(sNow)
		sNow.Progress.TurnCount = target.RunOrdinal
		sNow.Progress.LastFailure = nil
		sNow.ActiveProviderSessionId = ""
		sNow.Error = ""
		sNow.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
		sNow.GitStatus = gitStatus
		notice := &sessionv1.TurnItem{
			ItemId:       newID("rb"),
			SessionId:    req.SessionId,
			Kind:         sessionv1.TurnItemKind_TURN_ITEM_KIND_SYSTEM_NOTICE,
			Status:       "completed",
			CheckpointId: target.CheckpointId,
			Text:         fmt.Sprintf("Rewound session to checkpoint #%d (restore_files=%v)", target.RunOrdinal, req.RestoreFiles),
		}
		_ = tx.PutTurnItem(notice)
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId:  req.SessionId,
			Type:       "session.rolled_back",
			Session:    sNow,
			Checkpoint: target,
			Item:       notice,
		}); err != nil {
			return err
		}
		res = &sessionv1.RollbackCheckpointResponse{
			Session:    proto.Clone(sNow).(*sessionv1.Session),
			Checkpoint: target,
		}
		return tx.PutReceipt(req.CommandId, "RollbackCheckpoint", res)
	})
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	return res, nil
}

func (e *Engine) ForkSession(ctx context.Context, p *Principal, req *sessionv1.ForkSessionRequest) (*sessionv1.ForkSessionResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached sessionv1.ForkSessionResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "ForkSession", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	srcProj, err := e.st.Projection(req.SourceSessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("source session not found"))
	}
	if !p.canViewSession(srcProj.Session) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	upTo := req.UpToRunOrdinal
	if upTo == 0 {
		upTo = srcProj.Session.GetProgress().GetTurnCount()
	}
	includedRuns := map[string]bool{}
	var copiedRuns []*sessionv1.Run
	for _, r := range srcProj.Runs {
		if r.Ordinal > 0 && r.Ordinal <= upTo && r.Status != sessionv1.RunStatus_RUN_STATUS_ROLLED_BACK {
			includedRuns[r.RunId] = true
			copiedRuns = append(copiedRuns, r)
		}
	}

	newSessionID := e.newSessionID()
	wsStrategy := proto.Clone(req.Workspace).(*sessionv1.WorkspaceStrategy)
	if wsStrategy == nil {
		wsStrategy = &sessionv1.WorkspaceStrategy{
			Mode:    srcProj.Session.GetWorkspaceStrategy().GetMode(),
			BaseRef: srcProj.Session.GetWorkspaceStrategy().GetBranch(),
		}
	}
	ws, err := e.st.Workspace(srcProj.Session.ProjectId)
	if err != nil {
		return nil, err
	}
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_WORKTREE {
		baseRef := wsStrategy.BaseRef
		if baseRef == "" {
			baseRef = ws.DefaultBaseRef
		}
		wtPath, branch, wtErr := e.vcs.PrepareWorktree(ctx, ws.Path, newSessionID, baseRef, wsStrategy.Branch)
		if wtErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, wtErr)
		}
		wsStrategy.WorktreePath = wtPath
		wsStrategy.Branch = branch
		if cp := forkCheckpoint(srcProj.Checkpoints, includedRuns, upTo); cp != nil {
			if err := e.vcs.RestoreTree(ctx, wtPath, cp.GitTreeOid); err != nil {
				_ = e.vcs.RemoveWorktree(ctx, ws.Path, wtPath)
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("restore fork files: %w", err))
			}
		}
	}

	sel := proto.Clone(srcProj.Session.ModelSelection).(*sessionv1.ModelSelection)
	if req.ModelSelection != nil && req.ModelSelection.InstanceId != "" {
		instCfg, _, err := e.resolveInstanceConfig(p, srcProj.Session.ProjectId, req.ModelSelection.InstanceId)
		if err != nil {
			return nil, err
		}
		if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit target provider"))
		}
		sel = proto.Clone(req.ModelSelection).(*sessionv1.ModelSelection)
	} else if !e.principalAllowsInstance(p, sel.GetInstanceId()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit this session's provider"))
	}
	if err := e.projectAllowsInstance(srcProj.Session.ProjectId, sel.GetInstanceId()); err != nil {
		return nil, err
	}
	forkLocale := runtime.ResolveLocale(req.Locale, srcProj.Session.GetTitleLocale())
	title := runtime.CleanTitle(req.Title)
	forkTitleRenamed := title != ""
	forkTitleState := sessionv1.TitleState_TITLE_STATE_LOCKED
	var forkTitleSeq uint64 = 1
	if title == "" {
		title = runtime.LocalizedSeedFallback(forkLocale, "fork", srcProj.Session.Title)
		forkTitleState = sessionv1.TitleState_TITLE_STATE_SEED
		forkTitleSeq = 0
	}

	createdBy := p.Actor.GetId()
	if p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER {
		createdBy = "owner"
	}

	forked := &sessionv1.Session{
		SessionId:            newSessionID,
		MachineId:            e.currentMachineID(),
		ProjectId:            srcProj.Session.ProjectId,
		Title:                title,
		TitleRenamed:         forkTitleRenamed,
		TitleLocale:          forkLocale,
		TitleState:           forkTitleState,
		TitleSeq:             forkTitleSeq,
		Status:               sessionv1.SessionStatus_SESSION_STATUS_IDLE,
		CreatedBy:            createdBy,
		ModelSelection:       sel,
		RuntimeMode:          srcProj.Session.RuntimeMode,
		EffectiveRuntimeMode: policy.EffectiveMode(e.machineMax(), p.GrantCeiling, srcProj.Session.RuntimeMode),
		MaxRuntimeMode:       policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling),
		InteractionMode:      srcProj.Session.InteractionMode,
		WorkspaceStrategy:    wsStrategy,
		Actor:                proto.Clone(p.Actor).(*sessionv1.Actor),
		Progress:             &sessionv1.SessionProgress{TurnCount: uint32(len(copiedRuns))},
		Lineage: &sessionv1.SessionLineage{
			ParentSessionId:      req.SourceSessionId,
			ForkedFromRunOrdinal: upTo,
			Relationship:         "fork",
			Controllable:         true,
		},
	}

	var res *sessionv1.ForkSessionResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		if err := putSessionSynced(tx, forked); err != nil {
			return err
		}
		runIDMap := map[string]string{}
		for _, r := range copiedRuns {
			cp := proto.Clone(r).(*sessionv1.Run)
			cp.RunId = newID("run")
			cp.SessionId = newSessionID
			cp.ProviderSessionId = ""
			runIDMap[r.RunId] = cp.RunId
			if err := tx.PutRun(cp); err != nil {
				return err
			}
		}
		for _, it := range srcProj.Items {
			if newRunID, ok := runIDMap[it.RunId]; ok && it.Kind != sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST {
				cpIt := proto.Clone(it).(*sessionv1.TurnItem)
				cpIt.ItemId = newID("itm")
				cpIt.SessionId = newSessionID
				cpIt.RunId = newRunID
				cpIt.Seq = 0
				if err := tx.PutTurnItem(cpIt); err != nil {
					return err
				}
			}
		}
		if err := tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: newSessionID,
			Type:      "session.forked",
			Session:   forked,
		}); err != nil {
			return err
		}
		res = &sessionv1.ForkSessionResponse{Session: proto.Clone(forked).(*sessionv1.Session)}
		return tx.PutReceipt(req.CommandId, "ForkSession", res)
	})
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	return res, nil
}

func forkCheckpoint(ckpts []*sessionv1.Checkpoint, includedRuns map[string]bool, upTo uint32) *sessionv1.Checkpoint {
	var best *sessionv1.Checkpoint
	for _, c := range ckpts {
		if c.RunOrdinal > upTo || (c.RunOrdinal != 0 && !includedRuns[c.RunId]) {
			continue
		}
		if best == nil || c.RunOrdinal >= best.RunOrdinal {
			best = c
		}
	}
	return best
}

func (e *Engine) DeleteSession(ctx context.Context, p *Principal, sessionID string) error {
	if p == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	unlock := e.lockSession(sessionID)
	sess, err := e.st.Session(sessionID)
	if errors.Is(err, store.ErrNotFound) {
		unlock()
		return nil
	}
	if err != nil {
		unlock()
		return err
	}
	if !p.canViewSession(sess) {
		unlock()
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	e.cancelTitleGen(sessionID)
	if sess.ActiveRunId != "" {
		e.mu.Lock()
		if cancel, ok := e.runCancels[sess.ActiveRunId]; ok {
			cancel()
		}
		e.mu.Unlock()
	}
	if sess.ActiveProviderSessionId != "" {
		e.sup.Detach(sess.ActiveProviderSessionId, true)
	}
	// A provider that keeps the conversation itself (Her) deletes it there too.
	if _, drv, _ := e.resolveInstanceConfig(p, sess.ProjectId, sess.GetModelSelection().GetInstanceId()); drv != nil {
		if del, ok := drv.(runtime.Deleter); ok {
			pss, _ := e.st.ProviderSessions(sessionID)
			for _, ps := range pss {
				if ref := ps.GetNativeSessionId(); ref != "" {
					_ = del.Delete(ctx, ref)
				}
			}
		}
	}
	if sess.GetWorkspaceStrategy().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE && sess.GetWorkspaceStrategy().GetWorktreePath() != "" {
		if ws, wErr := e.st.Workspace(sess.ProjectId); wErr == nil {
			_ = e.vcs.RemoveWorktree(ctx, ws.Path, sess.GetWorkspaceStrategy().GetWorktreePath())
		}
	}
	// Its turns' files (the checkpoints' refs in the project's repository) go with it.
	if ws, wErr := e.st.Workspace(sess.ProjectId); wErr == nil && vcs.IsGitRepo(ctx, ws.Path) {
		_ = e.vcs.RemoveCheckpoints(ctx, ws.Path, sessionID)
	}
	if e.cfg.DataDir != "" {
		_ = os.RemoveAll(filepath.Join(e.cfg.DataDir, "sessions", sessionID))
	}
	var candidateBlobs []string
	if items, iErr := e.st.TurnItems(sessionID); iErr == nil {
		for _, it := range items {
			if it.InputBlobSha256 != "" {
				candidateBlobs = append(candidateBlobs, it.InputBlobSha256)
			}
			if it.OutputBlobSha256 != "" {
				candidateBlobs = append(candidateBlobs, it.OutputBlobSha256)
			}
			for _, a := range it.Attachments {
				if a.BlobSha256 != "" {
					candidateBlobs = append(candidateBlobs, a.BlobSha256)
				}
			}
		}
	}
	if runs, rErr := e.st.Runs(sessionID); rErr == nil {
		for _, r := range runs {
			for _, a := range r.Attachments {
				if a.BlobSha256 != "" {
					candidateBlobs = append(candidateBlobs, a.BlobSha256)
				}
			}
		}
	}
	candidateBlobs = append(candidateBlobs, e.st.SessionTurnImageDigests(sessionID)...)
	_, err = e.st.Mutate(func(tx *store.Tx) error {
		return tx.DeleteSession(sessionID)
	})
	unlock()
	if err == nil {
		e.st.PruneBlobs(candidateBlobs)
		e.publish(nil, []string{sessionID})
	}
	return err
}

// ---------------------------------------------------------------------------
// Reads & Streaming Subscriptions

func (e *Engine) GetSession(_ context.Context, p *Principal, sessionID string) (*sessionv1.SessionProjection, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	proj, err := e.st.Projection(sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, err
	}
	if !p.canViewSession(proj.Session) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	for _, ps := range proj.ProviderSessions {
		ps.ProcessAlive = e.sup.Get(ps.ProviderSessionId) != nil
	}
	return proj, nil
}

func (e *Engine) busyInPlace(projectID, except string) bool {
	sessions, err := e.st.ListSessions(projectID, false, 0)
	if err != nil {
		return true
	}
	for _, s := range sessions {
		if s.SessionId == except || s.GetWorkspaceStrategy().GetMode() != identityv1.Workspace_WORKSPACE_IN_PLACE {
			continue
		}
		switch s.GetStatus() {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED, sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			return true
		}
	}
	return false
}

func (e *Engine) ListSessions(_ context.Context, p *Principal, req *sessionv1.ListSessionsRequest) (*sessionv1.ListSessionsResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var since time.Time
	if req.Since != nil {
		since = req.Since.AsTime()
	}
	fetchLimit := req.Limit
	if fetchLimit > 0 {
		fetchLimit++
	}
	all, err := e.st.QuerySessions(store.SessionFilter{
		ProjectID:       req.ProjectId,
		Status:          req.Status,
		Since:           since,
		IncludeArchived: req.IncludeArchived,
		Before:          req.Before,
		Limit:           fetchLimit,
	})
	if err != nil {
		return nil, err
	}
	var out []*sessionv1.Session
	for _, s := range all {
		if p.canViewSession(s) {
			out = append(out, s)
		}
	}
	var next *sessionv1.SessionCursor
	if req.Limit > 0 && uint32(len(out)) > req.Limit {
		out = out[:req.Limit]
		last := out[len(out)-1]
		next = &sessionv1.SessionCursor{
			UpdatedAt: last.UpdatedAt,
			SessionId: last.SessionId,
		}
	}
	return &sessionv1.ListSessionsResponse{Sessions: out, Next: next}, nil
}

func (e *Engine) WatchSessions(ctx context.Context, p *Principal, includeArchived bool, send func(*sessionv1.WatchSessionsResponse) error) error {
	if p == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	sub := &shellSub{
		includeArchived: includeArchived,
		ch:              make(chan *sessionv1.WatchSessionsResponse, 64),
	}
	e.mu.Lock()
	e.shellSubs[sub] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.shellSubs, sub)
		e.mu.Unlock()
	}()

	initialRes, err := e.ListSessions(ctx, p, &sessionv1.ListSessionsRequest{IncludeArchived: includeArchived})
	if err != nil {
		return err
	}
	if err := send(&sessionv1.WatchSessionsResponse{Snapshot: true, Sessions: initialRes.Sessions}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-sub.ch:
			var visible []*sessionv1.Session
			for _, s := range msg.Sessions {
				if p.canViewSession(s) {
					visible = append(visible, s)
				}
			}
			if len(visible) == 0 && len(msg.DeletedSessionIds) == 0 {
				continue
			}
			if err := send(&sessionv1.WatchSessionsResponse{
				Sessions:          visible,
				DeletedSessionIds: msg.DeletedSessionIds,
			}); err != nil {
				return err
			}
		}
	}
}

func (e *Engine) SubscribeSession(ctx context.Context, p *Principal, sessionID string, afterSeq uint64, send func(*sessionv1.SessionEvent) error) error {
	if p == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	sess, err := e.st.Session(sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return err
	}
	if !p.canViewSession(sess) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	sub := &sessionSub{
		actorID: p.Actor.GetId(),
		ch:      make(chan *sessionv1.SessionEvent, 256),
	}
	e.mu.Lock()
	if e.sessionSubs[sessionID] == nil {
		e.sessionSubs[sessionID] = map[*sessionSub]struct{}{}
	}
	e.sessionSubs[sessionID][sub] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.sessionSubs[sessionID], sub)
		e.mu.Unlock()
	}()

	history, err := e.st.EventsAfter(sessionID, afterSeq)
	if err != nil {
		return err
	}
	highWater := afterSeq
	if len(history) == 0 {
		// Confirm subscription readiness immediately in 1 RTT when the client is
		// already caught up at afterSeq so callers never need a synthetic timer.
		if cur, sErr := e.st.Session(sessionID); sErr == nil && cur != nil {
			curSeq := cur.GetProgress().GetLastEventSeq()
			if curSeq > highWater {
				highWater = curSeq
			}
			if err := send(&sessionv1.SessionEvent{
				Seq:       curSeq,
				SessionId: sessionID,
				Type:      "session.subscribed",
				Session:   cur,
			}); err != nil {
				return err
			}
		}
	}
	for _, ev := range history {
		if ev.Seq > highWater {
			highWater = ev.Seq
			if err := send(ev); err != nil {
				return err
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-sub.ch:
			if ev.Seq <= highWater {
				continue
			}
			highWater = ev.Seq
			if err := send(ev); err != nil {
				return err
			}
		}
	}
}
