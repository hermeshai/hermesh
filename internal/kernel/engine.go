package kernel

import (
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
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/kernel/catalog"
	"github.com/hermeshai/hermesh/internal/kernel/policy"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/kernel/vcs"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// ScratchProjectID is the workspace the kernel makes on every machine for
// threads that name no project. It is not one of the machine's projects, so a
// grant's "*" does not cover it: a grant must list it by name.
const ScratchProjectID = "scratch"

// Principal represents the verified cryptographic identity calling the kernel.
type Principal struct {
	Actor             *threadv2.Actor
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
		Actor: &threadv2.Actor{
			Kind:  threadv2.ActorKind_ACTOR_KIND_OWNER,
			Id:    deviceID,
			Label: label,
		},
		CanApproveRequest: true,
		CanManageProvider: true,
	}
}

func GrantPrincipal(g *identityv1.Grant) *Principal {
	kind := threadv2.ActorKind_ACTOR_KIND_EXTERNAL_AGENT
	label := g.GetClientLabel()
	if g.GetHerMachineId() != "" {
		kind = threadv2.ActorKind_ACTOR_KIND_HER
		if label == "" {
			label = "Her"
		}
	}
	return &Principal{
		Actor: &threadv2.Actor{
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
	if p.Actor.GetKind() == threadv2.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == threadv2.ActorKind_ACTOR_KIND_HER {
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
	if p.Actor.GetKind() == threadv2.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == threadv2.ActorKind_ACTOR_KIND_HER {
		return true
	}
	return identity.Allows(p.AllowedAgents, instanceID) ||
		identity.Allows(p.AllowedAgents, driverKind) ||
		(acpFlavor != "" && identity.Allows(p.AllowedAgents, acpFlavor))
}

func (p *Principal) canViewThread(t *threadv2.Thread) bool {
	if p == nil {
		return false
	}
	if p.Actor.GetKind() == threadv2.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == threadv2.ActorKind_ACTOR_KIND_HER {
		return true
	}
	if !p.allowsProject(t.ProjectId) {
		return false
	}
	if p.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		return true
	}
	return t.GetCreatedBy().GetId() == p.Actor.GetId()
}

type NoticeHook func(thread *threadv2.Thread, ev *threadv2.ThreadEvent)

type Config struct {
	DataDir         string
	MachineID       string
	Logger          *slog.Logger
	Drivers         map[string]runtime.Driver
	IncludeScripted bool
	IdleTTL         time.Duration
	MachineMaxMode  identityv1.RuntimeMode
	IsPaused        func() bool
	OnNotice        NoticeHook
	// AgentModels: each coding agent's own list of models, as the machine
	// last read it; nil offers the manifest's.
	AgentModels catalog.AgentModels
}

type shellSub struct {
	includeArchived bool
	ch              chan *threadv2.SubscribeThreadsResponse
}

type Engine struct {
	cfg        Config
	maxMode    atomic.Int32 // the machine's ceiling (Config.MachineMaxMode, then SetMachineMaxMode)
	log        *slog.Logger
	st         *store.Store
	vcs        *vcs.Manager
	cat        *catalog.Service
	sup        *runtime.Supervisor
	scratchDir string
	bgCtx      context.Context
	bgCancel   context.CancelFunc

	mu          sync.Mutex
	wg          sync.WaitGroup
	threadLocks map[string]*sync.Mutex
	cmdLocks    map[string]*refLock                    // command_id -> in-flight idempotent command
	waiters     map[string]chan runtime.ToolGateResult // request_id -> waiter
	runCancels  map[string]context.CancelFunc          // run_id -> cancel
	threadSubs  map[string]map[chan *threadv2.ThreadEvent]struct{}
	shellSubs   map[*shellSub]struct{}
}

func Open(cfg Config) (*Engine, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "threads_v2.db"))
	if err != nil {
		return nil, err
	}
	scratchDir := filepath.Join(cfg.DataDir, "scratch_workspace")
	_ = os.MkdirAll(scratchDir, 0o755)

	bgCtx, bgCancel := context.WithCancel(context.Background())
	e := &Engine{
		cfg:         cfg,
		log:         cfg.Logger,
		st:          st,
		vcs:         vcs.NewManager(filepath.Join(cfg.DataDir, "worktrees_v2")),
		cat:         catalog.NewService(cfg.Drivers, cfg.AgentModels),
		sup:         runtime.NewSupervisor(cfg.IdleTTL),
		scratchDir:  scratchDir,
		bgCtx:       bgCtx,
		bgCancel:    bgCancel,
		threadLocks: map[string]*sync.Mutex{},
		cmdLocks:    map[string]*refLock{},
		waiters:     map[string]chan runtime.ToolGateResult{},
		runCancels:  map[string]context.CancelFunc{},
		threadSubs:  map[string]map[chan *threadv2.ThreadEvent]struct{}{},
		shellSubs:   map[*shellSub]struct{}{},
	}
	e.maxMode.Store(int32(cfg.MachineMaxMode))
	if err := e.seedDefaults(); err != nil {
		bgCancel()
		_ = st.Close()
		return nil, err
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

func (e *Engine) spawnRun(threadID, runID string, freshContext bool) {
	runCtx, cancelRun := context.WithCancel(e.bgCtx)
	e.mu.Lock()
	e.runCancels[runID] = cancelRun
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			cancelRun()
			e.mu.Lock()
			delete(e.runCancels, runID)
			e.mu.Unlock()
		}()
		e.driveRun(runCtx, threadID, runID, freshContext)
	}()
}

// machineMax is the machine's own permission-mode ceiling (UNSPECIFIED: none).
func (e *Engine) machineMax() identityv1.RuntimeMode { return identityv1.RuntimeMode(e.maxMode.Load()) }

// SetMachineMaxMode changes the machine's ceiling: launches and dispatches from
// now on are clamped to it (a run in flight keeps the mode it started with).
func (e *Engine) SetMachineMaxMode(m identityv1.RuntimeMode) { e.maxMode.Store(int32(m)) }

func (e *Engine) Store() *store.Store             { return e.st }
func (e *Engine) Supervisor() *runtime.Supervisor { return e.sup }

func (e *Engine) BusyCount() int {
	threads, err := e.st.ListThreads("", false, 0)
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range threads {
		switch t.Status {
		case threadv2.ThreadStatus_THREAD_STATUS_PREPARING,
			threadv2.ThreadStatus_THREAD_STATUS_QUEUED,
			threadv2.ThreadStatus_THREAD_STATUS_RUNNING,
			threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST:
			n++
		}
	}
	return n
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
		// Always register a default "scratch" workspace so ad-hoc threads work immediately.
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

// SyncWorkspace keeps v2 workspaces synchronized with Machine's configured projects.
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

// recoverOrphans reconciles threads interrupted by a daemon crash or power loss:
// in-flight runs become INTERRUPTED, LIVE_RPC requests downgrade to RESUME_TURN
// so they remain answerable, and QUEUED runs stay intact in queue order.
func (e *Engine) recoverOrphans() error {
	threads, err := e.st.ListThreads("", true, 0)
	if err != nil {
		return err
	}
	for _, t := range threads {
		if t.Status != threadv2.ThreadStatus_THREAD_STATUS_RUNNING &&
			t.Status != threadv2.ThreadStatus_THREAD_STATUS_PREPARING &&
			t.Status != threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST {
			continue
		}
		runs, _ := e.st.Runs(t.ThreadId)
		reqs, _ := e.st.RuntimeRequests(t.ThreadId)
		pts, _ := e.st.ProviderThreads(t.ThreadId)
		events, err := e.st.Mutate(func(tx *store.Tx) error {
			th, err := tx.GetThread(t.ThreadId)
			if err != nil {
				return nil
			}
			for _, pt := range pts {
				if pt.ProcessAlive {
					pt.ProcessAlive = false
					_ = tx.PutProviderThread(pt)
				}
			}
			hasPendingResumeReq := false
			for _, rq := range reqs {
				if rq.Status == threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
					if rq.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT ||
						rq.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW {
						rq.ResponseMode = threadv2.ResponseMode_RESPONSE_MODE_RESUME_TURN
						_ = tx.PutRuntimeRequest(rq)
						hasPendingResumeReq = true
					} else {
						rq.Status = threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_EXPIRED
						rq.ResolvedAt = timestamppb.Now()
						_ = tx.PutRuntimeRequest(rq)
					}
				}
			}
			for _, r := range runs {
				if r.Status == threadv2.RunStatus_RUN_STATUS_RUNNING ||
					r.Status == threadv2.RunStatus_RUN_STATUS_PREPARING ||
					r.Status == threadv2.RunStatus_RUN_STATUS_WAITING_REQUEST {
					r.Status = threadv2.RunStatus_RUN_STATUS_INTERRUPTED
					r.ErrorMessage = "hermeshd restarted while turn was active"
					r.CompletedAt = timestamppb.Now()
					_ = tx.PutRun(r)
					settled, _ := tx.SettleRunItems(th.ThreadId, r.RunId, "interrupted")
					for _, sit := range settled {
						_ = tx.AppendEvent(&threadv2.ThreadEvent{
							ThreadId: th.ThreadId,
							Type:     "item.updated",
							Item:     sit,
						})
					}
				}
			}
			queued, _ := tx.QueuedRuns(th.ThreadId)
			th.QueuedRunCount = uint32(len(queued))
			th.ActiveRunId = ""
			if hasPendingResumeReq {
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST
			} else if len(queued) > 0 {
				th.QueueHeld = true // Hold queue after unexpected crash so user can inspect or resume.
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_QUEUED
				th.PendingRequestId = ""
			} else {
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
				th.PendingRequestId = ""
			}
			if err := tx.PutThread(th); err != nil {
				return err
			}
			return tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: th.ThreadId,
				Type:     "thread.recovered",
				Thread:   th,
			})
		})
		if err == nil {
			e.publish(events, nil)
		}
	}
	return nil
}

func (e *Engine) lockThread(threadID string) func() {
	e.mu.Lock()
	lk := e.threadLocks[threadID]
	if lk == nil {
		lk = &sync.Mutex{}
		e.threadLocks[threadID] = lk
	}
	e.mu.Unlock()
	lk.Lock()
	return lk.Unlock
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

// ThreadPrefix returns the deterministic 8-char machine routing tag embedded in thread IDs.
func ThreadPrefix(machineID string) string {
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(machineID)), "m_")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

// OwnsThread reports whether threadID carries machineID's routing prefix.
func OwnsThread(machineID, threadID string) bool {
	parts := strings.SplitN(threadID, "_", 3)
	if len(parts) != 3 || parts[0] != "th" {
		return false
	}
	p := ThreadPrefix(machineID)
	return p != "" && p == parts[1]
}

func (e *Engine) newThreadID() string {
	if p := ThreadPrefix(e.cfg.MachineID); p != "" {
		return "th_" + p + "_" + strings.ToLower(ulid.Make().String())
	}
	return newID("th")
}

func (e *Engine) publish(events []*threadv2.ThreadEvent, deletedThreadIDs []string) {
	if len(events) == 0 && len(deletedThreadIDs) == 0 {
		return
	}
	var updatedThreads []*threadv2.Thread
	seenThread := map[string]bool{}
	for _, ev := range events {
		e.mu.Lock()
		subs := e.threadSubs[ev.ThreadId]
		for ch := range subs {
			select {
			case ch <- ev:
			default:
			}
		}
		e.mu.Unlock()

		if ev.Thread != nil && !seenThread[ev.ThreadId] {
			seenThread[ev.ThreadId] = true
			updatedThreads = append(updatedThreads, ev.Thread)
		} else if !seenThread[ev.ThreadId] {
			if t, err := e.st.Thread(ev.ThreadId); err == nil {
				seenThread[ev.ThreadId] = true
				updatedThreads = append(updatedThreads, t)
			}
		}
		if e.cfg.OnNotice != nil && (ev.Type == "request.created" || ev.Type == "run.failed" || ev.Type == "run.completed") {
			if t, err := e.st.Thread(ev.ThreadId); err == nil {
				e.cfg.OnNotice(t, ev)
			}
		}
	}

	if len(updatedThreads) > 0 || len(deletedThreadIDs) > 0 {
		e.mu.Lock()
		for sub := range e.shellSubs {
			var filtered []*threadv2.Thread
			for _, t := range updatedThreads {
				if sub.includeArchived || t.Status != threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED {
					filtered = append(filtered, t)
				}
			}
			if len(filtered) == 0 && len(deletedThreadIDs) == 0 {
				continue
			}
			msg := &threadv2.SubscribeThreadsResponse{
				Threads:          filtered,
				DeletedThreadIds: deletedThreadIDs,
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

func (e *Engine) ListProviders(ctx context.Context, p *Principal, projectID string, refresh bool) (*threadv2.ListProvidersResponse, error) {
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
	var out []*threadv2.ProviderInstance
	for _, cfg := range cfgs {
		if !p.allowsAgent(cfg.InstanceId, cfg.Driver, cfg.AcpFlavor) {
			continue
		}
		out = append(out, e.cat.ResolveInstance(ctx, cfg, workdir, refresh))
	}
	return &threadv2.ListProvidersResponse{
		Providers:      out,
		MaxRuntimeMode: policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling),
	}, nil
}

func (e *Engine) UpsertProviderInstance(ctx context.Context, p *Principal, cfg *threadv2.ProviderInstanceConfig) (*threadv2.ProviderInstance, error) {
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

func (e *Engine) resolveInstanceConfig(p *Principal, instanceID string) (*threadv2.ProviderInstanceConfig, runtime.Driver, error) {
	cfgs, err := e.st.ProviderInstances()
	if err != nil {
		return nil, nil, err
	}
	var chosen *threadv2.ProviderInstanceConfig
	if instanceID != "" {
		for _, c := range cfgs {
			if c.InstanceId == instanceID {
				chosen = c
				break
			}
		}
	} else {
		preferred := []string{"scripted", "claude", "codex", "gemini", "antigravity", "cursor", "opencode"}
		for _, want := range preferred {
			for _, c := range cfgs {
				if c.InstanceId == want && c.Enabled && e.cat.Driver(c.Driver) != nil && (p == nil || p.allowsAgent(c.InstanceId, c.Driver, c.AcpFlavor)) {
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
				if c.Enabled && e.cat.Driver(c.Driver) != nil && (p == nil || p.allowsAgent(c.InstanceId, c.Driver, c.AcpFlavor)) {
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
// instance (the machine's Project.agents, as v1 sessions check it): by
// instance id, driver or ACP flavour. It holds for the owner too: it is the
// project's own setting, not a grant's.
func (e *Engine) projectAllowsInstance(projectID, instanceID string) error {
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
// Thread Launch & Dispatch

func (e *Engine) LaunchThread(ctx context.Context, p *Principal, req *threadv2.LaunchThreadRequest) (*threadv2.LaunchThreadResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	if e.cfg.IsPaused != nil && e.cfg.IsPaused() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("machine is paused"))
	}
	var cached threadv2.LaunchThreadResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "LaunchThread", &cached); err != nil {
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

	sel := proto.Clone(req.ModelSelection).(*threadv2.ModelSelection)
	if sel == nil {
		sel = &threadv2.ModelSelection{}
	}
	instCfg, _, err := e.resolveInstanceConfig(p, sel.InstanceId)
	if err != nil {
		return nil, err
	}
	if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("grant does not permit provider %q", instCfg.InstanceId))
	}
	if err := e.projectAllowsInstance(projectID, instCfg.InstanceId); err != nil {
		return nil, err
	}
	resolvedInst := e.cat.ResolveInstance(ctx, instCfg, ws.Path, false)
	if sel.InstanceId == "" {
		sel.InstanceId = instCfg.InstanceId
	}
	if sel.Model == "" {
		def := catalog.DefaultSelection(resolvedInst)
		sel.Model = def.Model
		if len(sel.Options) == 0 {
			sel.Options = def.Options
		}
	}

	reqRuntimeMode := policy.NormalizeRequested(req.RuntimeMode)
	maxMode := policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling)
	effMode := policy.EffectiveMode(e.machineMax(), p.GrantCeiling, reqRuntimeMode)

	imode := req.InteractionMode
	if imode == threadv2.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		imode = threadv2.InteractionMode_INTERACTION_MODE_CODE
	}

	wsStrategy := proto.Clone(req.Workspace).(*threadv2.WorkspaceStrategy)
	if wsStrategy == nil {
		wsStrategy = &threadv2.WorkspaceStrategy{}
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
	if wsStrategy.BaseRef == "" {
		wsStrategy.BaseRef = ws.DefaultBaseRef
	}

	threadID := e.newThreadID()
	title := strings.TrimSpace(req.Title)
	titleRenamed := title != ""
	if title == "" {
		title = deriveTitle(req.InitialPrompt)
	}

	// Prepare workspace directory before first run (fast for in-place, isolated git worktree for WORKTREE).
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_WORKTREE {
		if wsStrategy.FromRemote {
			base, err := vcs.FetchBase(ctx, ws.Path, wsStrategy.BaseRef)
			if err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			wsStrategy.BaseRef = base
		}
		wtPath, branch, wtErr := e.vcs.PrepareWorktree(ctx, ws.Path, threadID, wsStrategy.BaseRef, wsStrategy.Branch)
		if wtErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, wtErr)
		}
		wsStrategy.WorktreePath = wtPath
		wsStrategy.Branch = branch
	} else {
		wsStrategy.WorktreePath = ""
		if wsStrategy.Branch == "" {
			wsStrategy.Branch = vcs.CurrentBranch(ctx, ws.Path)
		} else if wsStrategy.Branch != vcs.CurrentBranch(ctx, ws.Path) {
			// Another branch in the project's own folder: switched to first, unless a session works there now.
			if e.busyInPlace(projectID) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("a session is working in %s now; switch branches once it is done", ws.Path))
			}
			if err := e.vcs.SwitchBranch(ctx, ws.Path, wsStrategy.Branch); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
		}
	}

	hasInitial := strings.TrimSpace(req.InitialPrompt) != "" || len(req.Attachments) > 0
	status := threadv2.ThreadStatus_THREAD_STATUS_IDLE
	if hasInitial {
		status = threadv2.ThreadStatus_THREAD_STATUS_RUNNING
	}

	lineage := &threadv2.ThreadLineage{Relationship: "root", Controllable: true}
	if req.ParentThreadId != "" {
		lineage = &threadv2.ThreadLineage{
			ParentThreadId: req.ParentThreadId,
			Relationship:   "subagent",
			Controllable:   true,
		}
	}

	th := &threadv2.Thread{
		ThreadId:             threadID,
		MachineId:            e.cfg.MachineID,
		ProjectId:            projectID,
		Title:                title,
		TitleRenamed:         titleRenamed,
		Status:               status,
		ModelSelection:       sel,
		RuntimeMode:          reqRuntimeMode,
		EffectiveRuntimeMode: effMode,
		MaxRuntimeMode:       maxMode,
		InteractionMode:      imode,
		Workspace:            wsStrategy,
		CreatedBy:            proto.Clone(p.Actor).(*threadv2.Actor),
		Lineage:              lineage,
	}

	var initRun *threadv2.Run
	var res *threadv2.LaunchThreadResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		if err := tx.PutThread(th); err != nil {
			return err
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: threadID,
			Type:     "thread.created",
			Thread:   th,
		}); err != nil {
			return err
		}
		if hasInitial {
			initRun = &threadv2.Run{
				RunId:           newID("run"),
				ThreadId:        threadID,
				Ordinal:         1,
				Status:          threadv2.RunStatus_RUN_STATUS_RUNNING,
				Author:          proto.Clone(p.Actor).(*threadv2.Actor),
				Prompt:          req.InitialPrompt,
				Attachments:     req.Attachments,
				Intent:          threadv2.DispatchIntent_DISPATCH_INTENT_START,
				ModelSelection:  proto.Clone(sel).(*threadv2.ModelSelection),
				RuntimeMode:     effMode,
				InteractionMode: imode,
				StartedAt:       timestamppb.Now(),
			}
			th.RunCount = 1
			th.ActiveRunId = initRun.RunId
			if err := tx.PutRun(initRun); err != nil {
				return err
			}
			if err := tx.SetRunGrantCeiling(initRun.RunId, p.GrantCeiling); err != nil {
				return err
			}
			userItem := &threadv2.TurnItem{
				ItemId:      newID("itm"),
				ThreadId:    threadID,
				RunId:       initRun.RunId,
				Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
				Status:      "completed",
				Actor:       proto.Clone(p.Actor).(*threadv2.Actor),
				Text:        req.InitialPrompt,
				Attachments: req.Attachments,
			}
			if err := tx.PutTurnItem(userItem); err != nil {
				return err
			}
			if err := tx.PutThread(th); err != nil {
				return err
			}
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: threadID,
				Type:     "item.updated",
				Thread:   th,
				Run:      initRun,
				Item:     userItem,
			}); err != nil {
				return err
			}
		}
		res = &threadv2.LaunchThreadResponse{
			Thread:     proto.Clone(th).(*threadv2.Thread),
			InitialRun: initRun,
		}
		return tx.PutReceipt(req.CommandId, "LaunchThread", res)
	})
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)

	if initRun != nil {
		e.spawnRun(threadID, initRun.RunId, false)
	}
	return res, nil
}

func (e *Engine) DispatchMessage(ctx context.Context, p *Principal, req *threadv2.DispatchMessageRequest) (*threadv2.DispatchMessageResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached threadv2.DispatchMessageResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "DispatchMessage", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("message text or attachment is required"))
	}

	unlock := e.lockThread(req.ThreadId)
	th, err := e.st.Thread(req.ThreadId)
	if errors.Is(err, store.ErrNotFound) {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if err != nil {
		unlock()
		return nil, err
	}
	if !p.canViewThread(th) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied for thread"))
	}
	if th.Status == threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("archived thread is read-only; unarchive first"))
	}

	intent := req.Intent
	if intent == threadv2.DispatchIntent_DISPATCH_INTENT_UNSPECIFIED {
		intent = threadv2.DispatchIntent_DISPATCH_INTENT_AUTO
	}

	// Resolve effective selection & modes (applying optional per-dispatch overrides).
	sel := proto.Clone(th.ModelSelection).(*threadv2.ModelSelection)
	if req.ModelSelection != nil && req.ModelSelection.InstanceId != "" {
		instCfg, _, err := e.resolveInstanceConfig(p, req.ModelSelection.InstanceId)
		if err != nil {
			unlock()
			return nil, err
		}
		if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
			unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit target provider"))
		}
		sel = proto.Clone(req.ModelSelection).(*threadv2.ModelSelection)
	} else if !e.principalAllowsInstance(p, sel.GetInstanceId()) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit this thread's provider"))
	}
	if err := e.projectAllowsInstance(th.ProjectId, sel.GetInstanceId()); err != nil {
		unlock()
		return nil, err
	}
	reqMode := th.RuntimeMode
	if req.RuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		reqMode = req.RuntimeMode
	}
	effMode := policy.EffectiveMode(e.machineMax(), p.GrantCeiling, reqMode)
	imode := th.InteractionMode
	if req.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		imode = req.InteractionMode
	}

	isActive := th.ActiveRunId != "" && (th.Status == threadv2.ThreadStatus_THREAD_STATUS_RUNNING || th.Status == threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)

	// Mid-turn STEER path when an active run is running!
	if isActive && intent != threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE && intent != threadv2.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT {
		lease := e.sup.Get(th.ActiveProviderThreadId)
		canLiveSteer := th.Status == threadv2.ThreadStatus_THREAD_STATUS_RUNNING &&
			lease != nil && SelectionsEqual(lease.Selection, sel)
		if canLiveSteer {
			atts := e.loadTurnAttachments(req.Attachments)
			if sErr := lease.Session.Steer(ctx, req.Text, atts); sErr == nil {
				var res *threadv2.DispatchMessageResponse
				events, mErr := e.st.Mutate(func(tx *store.Tx) error {
					tNow, err := tx.GetThread(th.ThreadId)
					if err != nil {
						return err
					}
					activeRun, err := tx.GetRun(tNow.ActiveRunId)
					if err != nil {
						return err
					}
					item := &threadv2.TurnItem{
						ItemId:      newID("itm"),
						ThreadId:    tNow.ThreadId,
						RunId:       activeRun.RunId,
						Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
						Status:      "steered",
						Actor:       proto.Clone(p.Actor).(*threadv2.Actor),
						Text:        req.Text,
						Attachments: req.Attachments,
					}
					if err := tx.PutTurnItem(item); err != nil {
						return err
					}
					if err := tx.PutThread(tNow); err != nil {
						return err
					}
					if err := tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: tNow.ThreadId,
						Type:     "item.updated",
						Thread:   tNow,
						Run:      activeRun,
						Item:     item,
					}); err != nil {
						return err
					}
					res = &threadv2.DispatchMessageResponse{
						Thread:           proto.Clone(tNow).(*threadv2.Thread),
						Run:              proto.Clone(activeRun).(*threadv2.Run),
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
		// If explicit STEER was requested on a driver that doesn't support live steer,
		// interrupt the active run without holding the queue so this message takes over next!
		if intent == threadv2.DispatchIntent_DISPATCH_INTENT_STEER && lease != nil {
			_ = lease.Session.Interrupt(ctx)
		}
	}

	// Otherwise, either start immediately (if idle and queue not held) or enqueue!
	var startedRun *threadv2.Run
	var res *threadv2.DispatchMessageResponse
	freshCtx := intent == threadv2.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT

	events, err := e.st.Mutate(func(tx *store.Tx) error {
		tNow, err := tx.GetThread(th.ThreadId)
		if err != nil {
			return err
		}
		if tNow.ActiveRunId == "" {
			// While a run is active the thread fields describe that run; a queued
			// message's selection and modes are applied when it starts.
			tNow.ModelSelection = sel
			tNow.RuntimeMode = reqMode
			tNow.EffectiveRuntimeMode = effMode
			tNow.InteractionMode = imode
		}

		queued, err := tx.QueuedRuns(tNow.ThreadId)
		if err != nil {
			return err
		}
		// QUEUE means "after what is running": with nothing running and the queue not held, that is now.
		// (Parked behind nothing, it would never start, and the next message would pass it.)
		queueIntent := intent == threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE
		shouldQueue := tNow.ActiveRunId != "" ||
			(tNow.QueueHeld && (len(queued) > 0 || queueIntent)) ||
			(queueIntent && len(queued) > 0)

		if shouldQueue {
			qOrder := 1000.0
			if len(queued) > 0 {
				if intent == threadv2.DispatchIntent_DISPATCH_INTENT_STEER {
					qOrder = queued[0].QueueOrder - 1000.0
				} else {
					qOrder = queued[len(queued)-1].QueueOrder + 1000.0
				}
			}
			qRun := &threadv2.Run{
				RunId:           newID("run"),
				ThreadId:        tNow.ThreadId,
				Ordinal:         0,
				Status:          threadv2.RunStatus_RUN_STATUS_QUEUED,
				QueueOrder:      qOrder,
				Author:          proto.Clone(p.Actor).(*threadv2.Actor),
				Prompt:          req.Text,
				Attachments:     req.Attachments,
				Intent:          intent,
				ModelSelection:  proto.Clone(sel).(*threadv2.ModelSelection),
				RuntimeMode:     effMode,
				InteractionMode: imode,
			}
			if err := tx.PutRun(qRun); err != nil {
				return err
			}
			if err := tx.SetRunGrantCeiling(qRun.RunId, p.GrantCeiling); err != nil {
				return err
			}
			tNow.QueuedRunCount = uint32(len(queued) + 1)
			if tNow.ActiveRunId == "" {
				tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_QUEUED
			}
			if err := tx.PutThread(tNow); err != nil {
				return err
			}
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: tNow.ThreadId,
				Type:     "run.queued",
				Thread:   tNow,
				Run:      qRun,
			}); err != nil {
				return err
			}
			res = &threadv2.DispatchMessageResponse{
				Thread: proto.Clone(tNow).(*threadv2.Thread),
				Run:    proto.Clone(qRun).(*threadv2.Run),
			}
			return tx.PutReceipt(req.CommandId, "DispatchMessage", res)
		}

		// Start immediately! If the thread was parked on a post-turn resumable
		// request (such as PLAN_REVIEW or USER_INPUT) and the user replied in the
		// composer instead of clicking the card, supersede that request cleanly.
		supersededReq, supersededItem, err := dismissPendingRequestTx(tx, tNow, p.Actor)
		if err != nil {
			return err
		}

		tNow.QueueHeld = false
		tNow.LastError = ""
		tNow.RunCount++
		startedRun = &threadv2.Run{
			RunId:           newID("run"),
			ThreadId:        tNow.ThreadId,
			Ordinal:         tNow.RunCount,
			Status:          threadv2.RunStatus_RUN_STATUS_RUNNING,
			Author:          proto.Clone(p.Actor).(*threadv2.Actor),
			Prompt:          req.Text,
			Attachments:     req.Attachments,
			Intent:          intent,
			ModelSelection:  proto.Clone(sel).(*threadv2.ModelSelection),
			RuntimeMode:     effMode,
			InteractionMode: imode,
			StartedAt:       timestamppb.Now(),
		}
		tNow.ActiveRunId = startedRun.RunId
		tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_RUNNING
		if err := tx.PutRun(startedRun); err != nil {
			return err
		}
		if err := tx.SetRunGrantCeiling(startedRun.RunId, p.GrantCeiling); err != nil {
			return err
		}
		userItem := &threadv2.TurnItem{
			ItemId:      newID("itm"),
			ThreadId:    tNow.ThreadId,
			RunId:       startedRun.RunId,
			Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
			Status:      "completed",
			Actor:       proto.Clone(p.Actor).(*threadv2.Actor),
			Text:        req.Text,
			Attachments: req.Attachments,
		}
		if err := tx.PutTurnItem(userItem); err != nil {
			return err
		}
		if err := tx.PutThread(tNow); err != nil {
			return err
		}
		if supersededReq != nil {
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: tNow.ThreadId,
				Type:     "request.resolved",
				Thread:   tNow,
				Item:     supersededItem,
				Request:  supersededReq,
			}); err != nil {
				return err
			}
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: tNow.ThreadId,
			Type:     "item.updated",
			Thread:   tNow,
			Run:      startedRun,
			Item:     userItem,
		}); err != nil {
			return err
		}
		res = &threadv2.DispatchMessageResponse{
			Thread: proto.Clone(tNow).(*threadv2.Thread),
			Run:    proto.Clone(startedRun).(*threadv2.Run),
		}
		return tx.PutReceipt(req.CommandId, "DispatchMessage", res)
	})
	unlock()
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	if startedRun != nil {
		e.spawnRun(th.ThreadId, startedRun.RunId, freshCtx)
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Core Run Execution Loop (TransitionPolicy + Checkpoints + RuntimeRequest Gate + Queue Drain)

func (e *Engine) resolveWorkdir(th *threadv2.Thread) (string, error) {
	ws, err := e.st.Workspace(th.ProjectId)
	if err != nil {
		return "", err
	}
	if th.GetWorkspace().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE {
		wtPath := th.GetWorkspace().GetWorktreePath()
		if wtPath != "" {
			if st, err := os.Stat(wtPath); err == nil && st.IsDir() {
				return wtPath, nil
			}
		}
		// Restore missing worktree transparently!
		restored, branch, err := e.vcs.PrepareWorktree(e.bgCtx, ws.Path, th.ThreadId, th.GetWorkspace().GetBaseRef(), th.GetWorkspace().GetBranch())
		if err != nil {
			return "", err
		}
		_, _ = e.st.Mutate(func(tx *store.Tx) error {
			tNow, err := tx.GetThread(th.ThreadId)
			if err != nil {
				return err
			}
			tNow.Workspace.WorktreePath = restored
			tNow.Workspace.Branch = branch
			return tx.PutThread(tNow)
		})
		return restored, nil
	}
	return ws.Path, nil
}

func (e *Engine) loadTurnAttachments(refs []*threadv2.AttachmentRef) []runtime.TurnAttachment {
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

func (e *Engine) driveRun(runCtx context.Context, threadID, runID string, freshContext bool) {
	if runCtx.Err() != nil {
		return
	}

	th, err := e.st.Thread(threadID)
	if err != nil {
		return
	}
	runs, _ := e.st.Runs(threadID)
	var curRun *threadv2.Run
	for _, r := range runs {
		if r.RunId == runID {
			curRun = r
			break
		}
	}
	if curRun == nil || curRun.Status != threadv2.RunStatus_RUN_STATUS_RUNNING {
		return
	}

	workdir, err := e.resolveWorkdir(th)
	if err != nil {
		e.finishRunFailed(threadID, runID, "workspace_error", err.Error())
		return
	}

	// Capture baseline checkpoint (ordinal 0) before the very first run so user can rewind even Run 1!
	existingCkpts, _ := e.st.Checkpoints(threadID)
	prevTreeOID := ""
	if len(existingCkpts) == 0 {
		if baseCp, cpErr := e.vcs.CaptureCheckpoint(runCtx, workdir, newID("ckpt"), threadID, "", 0, ""); cpErr == nil && baseCp != nil {
			prevTreeOID = baseCp.GitTreeOid
			_, _ = e.st.Mutate(func(tx *store.Tx) error { return tx.PutCheckpoint(baseCp) })
		}
	} else {
		prevTreeOID = existingCkpts[len(existingCkpts)-1].GitTreeOid
	}

	instCfg, drv, err := e.resolveInstanceConfig(nil, curRun.ModelSelection.GetInstanceId())
	if err != nil {
		e.finishRunFailed(threadID, runID, "provider_unavailable", err.Error())
		return
	}
	caps := drv.Capabilities(instCfg.AcpFlavor)

	var currentPT *threadv2.ProviderThread
	if th.ActiveProviderThreadId != "" {
		pts, _ := e.st.ProviderThreads(threadID)
		for _, pt := range pts {
			if pt.ProviderThreadId == th.ActiveProviderThreadId {
				currentPT = pt
				break
			}
		}
	}
	var liveLease *runtime.LeaseEntry
	if currentPT != nil {
		liveLease = e.sup.Get(currentPT.ProviderThreadId)
	}

	plan := DecideTransition(TransitionInput{
		CurrentBinding:      currentPT,
		LiveLease:           liveLease,
		NextSelection:       curRun.ModelSelection,
		NextEffectiveMode:   curRun.RuntimeMode,
		NextInteractionMode: curRun.InteractionMode,
		FreshContext:        freshContext,
		Capabilities:        caps,
	})

	var handoffPrefix string
	var activeLease *runtime.LeaseEntry
	var activePT *threadv2.ProviderThread

	switch plan.Action {
	case TransitionReuseInPlace:
		activeLease = liveLease
		activePT = currentPT

	case TransitionSwitchInSession:
		prevSel := proto.Clone(liveLease.Selection).(*threadv2.ModelSelection)
		if plan.ModelChanged || plan.OptionsChanged {
			if err := liveLease.Session.SetSelection(runCtx, curRun.ModelSelection); err != nil {
				// Fallback to restart if hot switch failed dynamically.
				e.sup.Detach(currentPT.ProviderThreadId, false)
				liveLease = nil
			} else {
				liveLease.Selection = proto.Clone(curRun.ModelSelection).(*threadv2.ModelSelection)
			}
		}
		if liveLease != nil && plan.ModeChanged {
			if err := liveLease.Session.SetModes(runCtx, curRun.RuntimeMode, curRun.InteractionMode); err != nil {
				e.sup.Detach(currentPT.ProviderThreadId, false)
				liveLease = nil
			} else {
				liveLease.RuntimeMode = curRun.RuntimeMode
				liveLease.InteractionMode = curRun.InteractionMode
			}
		}
		if liveLease != nil {
			activeLease = liveLease
			activePT = currentPT
			if plan.ModelChanged || plan.OptionsChanged {
				e.recordSwitchItem(threadID, runID, prevSel, curRun.ModelSelection, "", "")
			}
		}
	}

	if activeLease == nil {
		if runCtx.Err() != nil {
			return
		}
		// Detach any old live session before spawning the replacement.
		if currentPT != nil {
			e.sup.Detach(currentPT.ProviderThreadId, false)
		}
		buildHandoff := func() string {
			if freshContext {
				return ""
			}
			priorItems, _ := e.st.TurnItems(threadID)
			// Exclude current run's just-added user message from handoff history!
			var historyItems []*threadv2.TurnItem
			for _, it := range priorItems {
				if it.RunId != runID {
					historyItems = append(historyItems, it)
				}
			}
			return BuildContextHandoff(historyItems, existingCkpts, defaultHandoffBudgetChars)
		}

		resumeCursor := ""
		if plan.Action == TransitionSpawnAndResume && currentPT != nil {
			resumeCursor = currentPT.ResumeCursor
			activePT = currentPT
		} else {
			handoffPrefix = buildHandoff()
			activePT = &threadv2.ProviderThread{
				ProviderThreadId: newID("pt"),
				ThreadId:         threadID,
				InstanceId:       instCfg.InstanceId,
				Driver:           instCfg.Driver,
				ProcessAlive:     true,
			}
		}

		sess, startErr := drv.StartSession(e.bgCtx, runtime.StartSpec{
			ThreadID:        threadID,
			Workdir:         workdir,
			Instance:        instCfg,
			Selection:       curRun.ModelSelection,
			EffectiveMode:   curRun.RuntimeMode,
			InteractionMode: curRun.InteractionMode,
			ResumeCursor:    resumeCursor,
			Gate:            e.makeGate(threadID, workdir),
		})
		if startErr != nil && resumeCursor != "" && runCtx.Err() == nil && e.bgCtx.Err() == nil {
			// Native session cursor expired or cannot be resumed; self-heal by spawning a
			// fresh ProviderThread carrying the Hermesh transcript handoff.
			if currentPT != nil {
				_, _ = e.st.Mutate(func(tx *store.Tx) error {
					if pt, err := tx.GetProviderThread(currentPT.ProviderThreadId); err == nil {
						pt.ProcessAlive = false
						_ = tx.PutProviderThread(pt)
					}
					return nil
				})
			}
			handoffPrefix = buildHandoff()
			plan.HandoffStrategy = "transcript_pack"
			activePT = &threadv2.ProviderThread{
				ProviderThreadId: newID("pt"),
				ThreadId:         threadID,
				InstanceId:       instCfg.InstanceId,
				Driver:           instCfg.Driver,
				ProcessAlive:     true,
			}
			sess, startErr = drv.StartSession(e.bgCtx, runtime.StartSpec{
				ThreadID:        threadID,
				Workdir:         workdir,
				Instance:        instCfg,
				Selection:       curRun.ModelSelection,
				EffectiveMode:   curRun.RuntimeMode,
				InteractionMode: curRun.InteractionMode,
				Gate:            e.makeGate(threadID, workdir),
			})
		}
		if startErr != nil {
			e.finishRunFailed(threadID, runID, "session_start_failed", startErr.Error())
			return
		}
		if runCtx.Err() != nil {
			_ = sess.ForceStop()
			return
		}
		activePT.ProcessAlive = true
		events, _ := e.st.Mutate(func(tx *store.Tx) error {
			tNow, err := tx.GetThread(threadID)
			if err != nil {
				return err
			}
			tNow.ActiveProviderThreadId = activePT.ProviderThreadId
			if err := tx.PutProviderThread(activePT); err != nil {
				return err
			}
			return tx.PutThread(tNow)
		})
		e.publish(events, nil)

		if handoffPrefix != "" || (currentPT != nil && currentPT.ProviderThreadId != activePT.ProviderThreadId) {
			var fromSel *threadv2.ModelSelection
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
			e.recordSwitchItem(threadID, runID, fromSel, curRun.ModelSelection, plan.HandoffStrategy, handoffPrefix)
		}

		activeLease = &runtime.LeaseEntry{
			ProviderThreadID: activePT.ProviderThreadId,
			ThreadID:         threadID,
			InstanceID:       instCfg.InstanceId,
			DriverKind:       instCfg.Driver,
			Selection:        proto.Clone(curRun.ModelSelection).(*threadv2.ModelSelection),
			RuntimeMode:      curRun.RuntimeMode,
			InteractionMode:  curRun.InteractionMode,
			Session:          sess,
		}
		e.sup.Register(activeLease)
	}

	activeLease.SetInTurn(true)
	defer activeLease.SetInTurn(false)

	// Record provider_thread_id and handoff summary on the run.
	_, _ = e.st.Mutate(func(tx *store.Tx) error {
		rNow, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		rNow.ProviderThreadId = activePT.ProviderThreadId
		rNow.HandoffSummary = handoffPrefix
		return tx.PutRun(rNow)
	})

	promptToSend := curRun.Prompt
	if handoffPrefix != "" {
		promptToSend = handoffPrefix + "\n\n" + curRun.Prompt
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
		e.sup.DetachSession(activePT.ProviderThreadId, activeLease.Session, false)
		e.finishRunFailed(threadID, runID, "turn_start_failed", err.Error())
		return
	}

	// Consume events for this turn until terminal event or session exit!
	msgBufs := map[string]*strings.Builder{}
	rsnBufs := map[string]*strings.Builder{}
	var pendingPlan *threadv2.PlanReviewPayload
	for {
		select {
		case <-runCtx.Done():
			if e.bgCtx.Err() != nil {
				// Daemon is shutting down: preserve durable in-flight state for recoverOrphans on next boot.
				return
			}
			e.finishRunInterrupted(threadID, runID, "interrupted")
			return
		case ev, ok := <-activeLease.Session.Events():
			if !ok {
				if e.bgCtx.Err() != nil {
					return
				}
				e.sup.DetachSession(activePT.ProviderThreadId, activeLease.Session, false)
				if runCtx.Err() != nil {
					e.finishRunInterrupted(threadID, runID, "interrupted")
					return
				}
				e.finishRunFailed(threadID, runID, "session_closed", "provider session closed mid-turn")
				return
			}
			activeLease.Touch()
			switch ev.Kind {
			case runtime.EventNativeSession:
				_, _ = e.st.Mutate(func(tx *store.Tx) error {
					pt, err := tx.GetProviderThread(activePT.ProviderThreadId)
					if err != nil {
						return err
					}
					if ev.NativeID != "" {
						pt.NativeSessionId = ev.NativeID
					}
					if ev.ResumeCursor != "" {
						pt.ResumeCursor = ev.ResumeCursor
					}
					return tx.PutProviderThread(pt)
				})

			case runtime.EventAssistantDelta:
				itemID := scopeRunItemID(runID, ev.ItemID, "msg")
				buf := msgBufs[itemID]
				if buf == nil {
					buf = &strings.Builder{}
					msgBufs[itemID] = buf
				}
				buf.WriteString(ev.Text)
				fullText := buf.String()
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &threadv2.TurnItem{
						ItemId:    itemID,
						ThreadId:  threadID,
						RunId:     runID,
						Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
						Status:    "running",
						Text:      fullText,
						Streaming: true,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId:     threadID,
						Type:         "item.delta",
						Item:         it,
						TextDelta:    ev.Text,
						TargetItemId: itemID,
					})
				})
				e.publish(evs, nil)

			case runtime.EventAssistantMessage:
				itemID := scopeRunItemID(runID, ev.ItemID, "msg")
				text := ev.Text
				if text == "" && msgBufs[itemID] != nil {
					text = msgBufs[itemID].String()
				}
				delete(msgBufs, itemID)
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &threadv2.TurnItem{
						ItemId:    itemID,
						ThreadId:  threadID,
						RunId:     runID,
						Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
						Status:    "completed",
						Text:      text,
						Streaming: false,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: threadID,
						Type:     "item.updated",
						Item:     it,
					})
				})
				e.publish(evs, nil)

			case runtime.EventReasoningDelta:
				itemID := scopeRunItemID(runID, ev.ItemID, "rsn")
				buf := rsnBufs[itemID]
				if buf == nil {
					buf = &strings.Builder{}
					rsnBufs[itemID] = buf
				}
				buf.WriteString(ev.Text)
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &threadv2.TurnItem{
						ItemId:   itemID,
						ThreadId: threadID,
						RunId:    runID,
						Kind:     threadv2.TurnItemKind_TURN_ITEM_KIND_REASONING,
						Status:   "completed",
						Text:     buf.String(),
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: threadID,
						Type:     "item.updated",
						Item:     it,
					})
				})
				e.publish(evs, nil)

			case runtime.EventToolStarted, runtime.EventToolCompleted:
				status := "running"
				if ev.Kind == runtime.EventToolCompleted {
					status = "completed"
					if ev.ExitCode != 0 {
						status = "failed"
					}
				}
				var itemID string
				if ev.ItemID != "" {
					itemID = scopeRunItemID(runID, ev.ItemID, "tool")
				} else {
					itemID = newID("tool")
				}
				output := ev.Output
				truncated := false
				if len(output) > 32768 {
					cut := 32768
					for cut > 0 && !utf8.RuneStart(output[cut]) {
						cut--
					}
					output = output[:cut] + "\n...[output truncated]..."
					truncated = true
				}
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					prev, _ := tx.GetTurnItem(threadID, itemID)
					toolName := ev.ToolName
					cmd := ev.Command
					filePath := ev.FilePath
					if prev != nil {
						if toolName == "" {
							toolName = prev.ToolName
						}
						if cmd == "" {
							cmd = prev.Command
						}
						if filePath == "" {
							filePath = prev.FilePath
						}
						if output == "" && prev.Output != "" {
							output = prev.Output
							truncated = prev.OutputTruncated
						}
					}
					kind := threadv2.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL
					if cmd != "" || (prev != nil && prev.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION) {
						kind = threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION
					}
					it := &threadv2.TurnItem{
						ItemId:          itemID,
						ThreadId:        threadID,
						RunId:           runID,
						Kind:            kind,
						Status:          status,
						ToolName:        toolName,
						Command:         cmd,
						FilePath:        filePath,
						ExitCode:        ev.ExitCode,
						Output:          output,
						OutputTruncated: truncated,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: threadID,
						Type:     "item.updated",
						Item:     it,
					})
				})
				e.publish(evs, nil)

			case runtime.EventFileChanged:
				var itemID string
				if ev.ItemID != "" {
					itemID = scopeRunItemID(runID, ev.ItemID, "file")
				} else {
					itemID = newID("file")
				}
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &threadv2.TurnItem{
						ItemId:    itemID,
						ThreadId:  threadID,
						RunId:     runID,
						Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE,
						Status:    "completed",
						FilePath:  ev.FilePath,
						Diff:      ev.Diff,
						Additions: ev.Additions,
						Deletions: ev.Deletions,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: threadID,
						Type:     "item.updated",
						Item:     it,
					})
				})
				e.publish(evs, nil)

			case runtime.EventPlanProposed:
				var itemID string
				if ev.ItemID != "" {
					itemID = scopeRunItemID(runID, ev.ItemID, "plan")
				} else {
					itemID = newID("plan")
				}
				if ev.RequestReview {
					pendingPlan = &threadv2.PlanReviewPayload{
						PlanId:   itemID,
						Title:    ev.PlanTitle,
						Markdown: ev.Text,
					}
				} else if pendingPlan != nil && pendingPlan.PlanId == itemID {
					pendingPlan = nil
				}
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &threadv2.TurnItem{
						ItemId:    itemID,
						ThreadId:  threadID,
						RunId:     runID,
						Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_PROPOSED_PLAN,
						Status:    "completed",
						PlanTitle: ev.PlanTitle,
						Text:      ev.Text,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: threadID,
						Type:     "item.updated",
						Item:     it,
					})
				})
				e.publish(evs, nil)

			case runtime.EventTodoUpdated:
				itemID := scopeRunItemID(runID, ev.ItemID, "todo")
				evs, _ := e.st.Mutate(func(tx *store.Tx) error {
					it := &threadv2.TurnItem{
						ItemId:    itemID,
						ThreadId:  threadID,
						RunId:     runID,
						Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_TODO_LIST,
						Status:    "completed",
						TodoSteps: ev.TodoSteps,
					}
					if err := tx.PutTurnItem(it); err != nil {
						return err
					}
					return tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: threadID,
						Type:     "item.updated",
						Item:     it,
					})
				})
				e.publish(evs, nil)

			case runtime.EventUsage:
				if ev.Usage != nil {
					_, _ = e.st.Mutate(func(tx *store.Tx) error {
						tNow, err := tx.GetThread(threadID)
						if err != nil {
							return err
						}
						rNow, err := tx.GetRun(runID)
						if err != nil {
							return err
						}
						rNow.Usage = ev.Usage
						tNow.Usage = ev.Usage
						_ = tx.PutRun(rNow)
						return tx.PutThread(tNow)
					})
				}

			case runtime.EventTurnCompleted:
				// Finalize any open streaming message buffer that did not emit EventAssistantMessage.
				for itemID, buf := range msgBufs {
					if buf.Len() > 0 {
						evs, _ := e.st.Mutate(func(tx *store.Tx) error {
							it := &threadv2.TurnItem{
								ItemId:    itemID,
								ThreadId:  threadID,
								RunId:     runID,
								Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
								Status:    "completed",
								Text:      buf.String(),
								Streaming: false,
							}
							if err := tx.PutTurnItem(it); err != nil {
								return err
							}
							return tx.AppendEvent(&threadv2.ThreadEvent{
								ThreadId: threadID,
								Type:     "item.updated",
								Item:     it,
							})
						})
						e.publish(evs, nil)
					}
				}
				// Capture post-run Git Checkpoint!
				var capturedCp *threadv2.Checkpoint
				if cp, cpErr := e.vcs.CaptureCheckpoint(runCtx, workdir, newID("ckpt"), threadID, runID, curRun.Ordinal, prevTreeOID); cpErr == nil && cp != nil {
					capturedCp = cp
				}
				e.finishRunCompleted(threadID, runID, activePT.ProviderThreadId, capturedCp, pendingPlan)
				return

			case runtime.EventTurnInterrupted:
				if e.bgCtx.Err() != nil {
					return
				}
				for itemID, buf := range msgBufs {
					if buf.Len() > 0 {
						evs, _ := e.st.Mutate(func(tx *store.Tx) error {
							it := &threadv2.TurnItem{
								ItemId:    itemID,
								ThreadId:  threadID,
								RunId:     runID,
								Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
								Status:    "interrupted",
								Text:      buf.String(),
								Streaming: false,
							}
							if err := tx.PutTurnItem(it); err != nil {
								return err
							}
							return tx.AppendEvent(&threadv2.ThreadEvent{
								ThreadId: threadID,
								Type:     "item.updated",
								Item:     it,
							})
						})
						e.publish(evs, nil)
					}
				}
				e.finishRunInterrupted(threadID, runID, "interrupted")
				return

			case runtime.EventTurnFailed:
				e.finishRunFailed(threadID, runID, ev.ErrorCode, ev.ErrorMessage)
				return
			}
		}
	}
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

func (e *Engine) recordSwitchItem(threadID, runID string, fromSel, toSel *threadv2.ModelSelection, handoffStrategy, summary string) {
	kind := threadv2.TurnItemKind_TURN_ITEM_KIND_MODEL_SWITCH
	if handoffStrategy != "" {
		kind = threadv2.TurnItemKind_TURN_ITEM_KIND_HANDOFF
	}
	evs, _ := e.st.Mutate(func(tx *store.Tx) error {
		it := &threadv2.TurnItem{
			ItemId:          newID("sw"),
			ThreadId:        threadID,
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
		return tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: threadID,
			Type:     "item.updated",
			Item:     it,
		})
	})
	e.publish(evs, nil)
}

func (e *Engine) finishRunCompleted(threadID, runID, ptID string, cp *threadv2.Checkpoint, pendingPlan *threadv2.PlanReviewPayload) {
	unlock := e.lockThread(threadID)
	if pendingPlan != nil {
		if existing, rErr := e.st.RuntimeRequests(threadID); rErr == nil {
			for _, rq := range existing {
				if rq.RunId == runID && rq.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW {
					pendingPlan = nil
					break
				}
			}
		}
	}
	var nextQueued *threadv2.Run
	var nextItem *threadv2.TurnItem
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		th, err := tx.GetThread(threadID)
		if err != nil {
			return err
		}
		r, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		if r.Status != threadv2.RunStatus_RUN_STATUS_RUNNING && r.Status != threadv2.RunStatus_RUN_STATUS_WAITING_REQUEST {
			return nil
		}
		settled, _ := tx.SettleRunItems(threadID, runID, "completed")
		for _, sit := range settled {
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: threadID,
				Type:     "item.updated",
				Item:     sit,
			}); err != nil {
				return err
			}
		}
		r.Status = threadv2.RunStatus_RUN_STATUS_COMPLETED
		r.CompletedAt = timestamppb.Now()
		var cpItem *threadv2.TurnItem
		if cp != nil {
			r.CheckpointId = cp.CheckpointId
			if err := tx.PutCheckpoint(cp); err != nil {
				return err
			}
			cpItem = &threadv2.TurnItem{
				ItemId:       "ckpt_" + cp.CheckpointId,
				ThreadId:     threadID,
				RunId:        runID,
				Kind:         threadv2.TurnItemKind_TURN_ITEM_KIND_CHECKPOINT,
				Status:       "completed",
				CheckpointId: cp.CheckpointId,
			}
			_ = tx.PutTurnItem(cpItem)
		}
		if err := tx.PutRun(r); err != nil {
			return err
		}
		if pt, ptErr := tx.GetProviderThread(ptID); ptErr == nil {
			pt.SyncedRunOrdinal = r.Ordinal
			_ = tx.PutProviderThread(pt)
		}

		// Automatically dequeue the next queued run unless queue is held!
		queued, _ := tx.QueuedRuns(threadID)
		var resumeReq *threadv2.RuntimeRequest
		var resumeReqItem *threadv2.TurnItem
		if len(queued) > 0 && !th.QueueHeld {
			nextQueued = queued[0]
			th.RunCount++
			nextQueued.Ordinal = th.RunCount
			nextQueued.Status = threadv2.RunStatus_RUN_STATUS_RUNNING
			nextQueued.StartedAt = timestamppb.Now()
			if err := tx.PutRun(nextQueued); err != nil {
				return err
			}
			nextItem = &threadv2.TurnItem{
				ItemId:      newID("itm"),
				ThreadId:    threadID,
				RunId:       nextQueued.RunId,
				Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
				Status:      "completed",
				Actor:       nextQueued.Author,
				Text:        nextQueued.Prompt,
				Attachments: nextQueued.Attachments,
			}
			_ = tx.PutTurnItem(nextItem)
			th.ActiveRunId = nextQueued.RunId
			th.QueuedRunCount = uint32(len(queued) - 1)
			th.Status = threadv2.ThreadStatus_THREAD_STATUS_RUNNING
			th.ModelSelection = nextQueued.ModelSelection
			th.EffectiveRuntimeMode = nextQueued.RuntimeMode
			th.InteractionMode = nextQueued.InteractionMode
			th.PendingRequestId = ""
		} else if pendingPlan != nil {
			reqID := newID("req")
			resumeReq = &threadv2.RuntimeRequest{
				RequestId:    reqID,
				ThreadId:     threadID,
				RunId:        runID,
				Kind:         threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
				Status:       threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING,
				ResponseMode: threadv2.ResponseMode_RESPONSE_MODE_RESUME_TURN,
				PlanReview:   pendingPlan,
			}
			if err := tx.PutRuntimeRequest(resumeReq); err != nil {
				return err
			}
			resumeReqItem = &threadv2.TurnItem{
				ItemId:    "reqitm_" + reqID,
				ThreadId:  threadID,
				RunId:     runID,
				Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
				Status:    "waiting",
				RequestId: reqID,
			}
			if err := tx.PutTurnItem(resumeReqItem); err != nil {
				return err
			}
			th.ActiveRunId = ""
			th.QueuedRunCount = uint32(len(queued))
			th.Status = threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST
			th.PendingRequestId = reqID
		} else {
			th.ActiveRunId = ""
			th.QueuedRunCount = uint32(len(queued))
			if len(queued) > 0 {
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_QUEUED
			} else {
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
			}
			th.PendingRequestId = ""
		}
		if err := tx.PutThread(th); err != nil {
			return err
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId:   threadID,
			Type:       "run.completed",
			Thread:     th,
			Run:        r,
			Item:       cpItem,
			Checkpoint: cp,
		}); err != nil {
			return err
		}
		if resumeReq != nil {
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: threadID,
				Type:     "request.created",
				Thread:   th,
				Run:      r,
				Item:     resumeReqItem,
				Request:  resumeReq,
			}); err != nil {
				return err
			}
		}
		return appendRunStarted(tx, th, nextQueued, nextItem)
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
		if nextQueued != nil {
			e.spawnRun(threadID, nextQueued.RunId, nextQueued.Intent == threadv2.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT)
		}
	}
}

func (e *Engine) finishRunInterrupted(threadID, runID, reason string) {
	unlock := e.lockThread(threadID)
	var nextQueued *threadv2.Run
	var nextItem *threadv2.TurnItem
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		th, err := tx.GetThread(threadID)
		if err != nil {
			return err
		}
		r, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		if r.Status == threadv2.RunStatus_RUN_STATUS_INTERRUPTED || r.Status == threadv2.RunStatus_RUN_STATUS_COMPLETED {
			return nil
		}
		settled, _ := tx.SettleRunItems(threadID, runID, "interrupted")
		for _, sit := range settled {
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: threadID,
				Type:     "item.updated",
				Item:     sit,
			}); err != nil {
				return err
			}
		}
		r.Status = threadv2.RunStatus_RUN_STATUS_INTERRUPTED
		r.ErrorMessage = reason
		r.CompletedAt = timestamppb.Now()
		if err := tx.PutRun(r); err != nil {
			return err
		}
		queued, _ := tx.QueuedRuns(threadID)
		if len(queued) > 0 && !th.QueueHeld {
			nextQueued = queued[0]
			th.RunCount++
			nextQueued.Ordinal = th.RunCount
			nextQueued.Status = threadv2.RunStatus_RUN_STATUS_RUNNING
			nextQueued.StartedAt = timestamppb.Now()
			_ = tx.PutRun(nextQueued)
			nextItem = &threadv2.TurnItem{
				ItemId:      newID("itm"),
				ThreadId:    threadID,
				RunId:       nextQueued.RunId,
				Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
				Status:      "completed",
				Actor:       nextQueued.Author,
				Text:        nextQueued.Prompt,
				Attachments: nextQueued.Attachments,
			}
			_ = tx.PutTurnItem(nextItem)
			th.ActiveRunId = nextQueued.RunId
			th.QueuedRunCount = uint32(len(queued) - 1)
			th.Status = threadv2.ThreadStatus_THREAD_STATUS_RUNNING
			th.ModelSelection = nextQueued.ModelSelection
			th.EffectiveRuntimeMode = nextQueued.RuntimeMode
			th.InteractionMode = nextQueued.InteractionMode
		} else {
			th.ActiveRunId = ""
			th.QueuedRunCount = uint32(len(queued))
			if len(queued) > 0 {
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_QUEUED
			} else {
				th.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
			}
		}
		th.PendingRequestId = ""
		if err := tx.PutThread(th); err != nil {
			return err
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: threadID,
			Type:     "run.interrupted",
			Thread:   th,
			Run:      r,
		}); err != nil {
			return err
		}
		return appendRunStarted(tx, th, nextQueued, nextItem)
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
		if nextQueued != nil {
			e.spawnRun(threadID, nextQueued.RunId, nextQueued.Intent == threadv2.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT)
		}
	}
}

// appendRunStarted tells subscribers that a queued run left the queue and
// started (its run, and its message in the conversation), when one did.
func appendRunStarted(tx *store.Tx, th *threadv2.Thread, run *threadv2.Run, item *threadv2.TurnItem) error {
	if run == nil {
		return nil
	}
	return tx.AppendEvent(&threadv2.ThreadEvent{
		ThreadId: th.ThreadId,
		Type:     "run.started",
		Thread:   th,
		Run:      run,
		Item:     item,
	})
}

func (e *Engine) finishRunFailed(threadID, runID, code, msg string) {
	unlock := e.lockThread(threadID)
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		th, err := tx.GetThread(threadID)
		if err != nil {
			return err
		}
		r, err := tx.GetRun(runID)
		if err != nil {
			return err
		}
		if r.Status != threadv2.RunStatus_RUN_STATUS_RUNNING && r.Status != threadv2.RunStatus_RUN_STATUS_WAITING_REQUEST {
			return nil
		}
		settled, _ := tx.SettleRunItems(threadID, runID, "interrupted")
		for _, sit := range settled {
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: threadID,
				Type:     "item.updated",
				Item:     sit,
			}); err != nil {
				return err
			}
		}
		r.Status = threadv2.RunStatus_RUN_STATUS_FAILED
		r.ErrorCode = code
		r.ErrorMessage = msg
		r.CompletedAt = timestamppb.Now()
		if err := tx.PutRun(r); err != nil {
			return err
		}
		errItem := &threadv2.TurnItem{
			ItemId:   newID("err"),
			ThreadId: threadID,
			RunId:    runID,
			Kind:     threadv2.TurnItemKind_TURN_ITEM_KIND_ERROR,
			Status:   "failed",
			Text:     msg,
		}
		_ = tx.PutTurnItem(errItem)
		queued, _ := tx.QueuedRuns(threadID)
		if th.ActiveRunId == runID {
			th.ActiveRunId = ""
			th.PendingRequestId = ""
			th.LastError = msg
			th.QueuedRunCount = uint32(len(queued))
			if len(queued) > 0 {
				// Hold queue on failure so queued follow-ups are never lost AND never blindly run on broken state!
				th.QueueHeld = true
			}
			th.Status = threadv2.ThreadStatus_THREAD_STATUS_FAILED
		}
		if err := tx.PutThread(th); err != nil {
			return err
		}
		return tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: threadID,
			Type:     "run.failed",
			Thread:   th,
			Run:      r,
			Item:     errItem,
		})
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
	}
}

// ---------------------------------------------------------------------------
// Policy Gate & Durable RuntimeRequest Lifecycle

func (e *Engine) makeGate(threadID, workdir string) runtime.GateFunc {
	return func(ctx context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
		th, err := e.st.Thread(threadID)
		if err != nil {
			return runtime.ToolGateResult{}, err
		}
		runID := th.ActiveRunId

		if req.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL && req.Approval != nil {
			// Judge the call by the run that is executing it: its mode was capped by its
			// author's grant at dispatch, and later queued messages or config edits must
			// not widen (or narrow) a turn that is already in flight.
			mode, imode := th.EffectiveRuntimeMode, th.InteractionMode
			if run, rErr := e.st.Run(runID); rErr == nil {
				mode, imode = run.RuntimeMode, run.InteractionMode
			}
			rules, _ := e.st.AllowRules(threadID)
			dec := policy.EvaluateTool(policy.ToolEvalInput{
				Category:        req.Approval.ToolCategory,
				ToolName:        req.Approval.ToolName,
				Command:         req.Approval.Command,
				TargetPath:      req.Approval.TargetPath,
				EffectiveMode:   mode,
				GrantCeiling:    e.st.RunGrantCeiling(runID),
				InteractionMode: imode,
				Sandbox:         policy.SandboxScope{WorkspaceRoot: workdir},
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

		rr := &threadv2.RuntimeRequest{
			RequestId:    reqID,
			ThreadId:     threadID,
			RunId:        runID,
			Kind:         req.Kind,
			Status:       threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING,
			ResponseMode: threadv2.ResponseMode_RESPONSE_MODE_LIVE_RPC,
			Approval:     req.Approval,
			UserInput:    req.UserInput,
			PlanReview:   req.Plan,
			SecretInput:  req.Secret,
		}
		events, err := e.st.Mutate(func(tx *store.Tx) error {
			tNow, err := tx.GetThread(threadID)
			if err != nil {
				return err
			}
			rNow, err := tx.GetRun(runID)
			if err != nil {
				return err
			}
			rNow.Status = threadv2.RunStatus_RUN_STATUS_WAITING_REQUEST
			tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST
			tNow.PendingRequestId = reqID
			if err := tx.PutRuntimeRequest(rr); err != nil {
				return err
			}
			item := &threadv2.TurnItem{
				ItemId:    "reqitm_" + reqID,
				ThreadId:  threadID,
				RunId:     runID,
				Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
				Status:    "waiting",
				RequestId: reqID,
			}
			if err := tx.PutTurnItem(item); err != nil {
				return err
			}
			if err := tx.PutRun(rNow); err != nil {
				return err
			}
			if err := tx.PutThread(tNow); err != nil {
				return err
			}
			return tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: threadID,
				Type:     "request.created",
				Thread:   tNow,
				Run:      rNow,
				Item:     item,
				Request:  rr,
			})
		})
		if err != nil {
			return runtime.ToolGateResult{}, err
		}
		e.publish(events, nil)

		select {
		case res := <-waitCh:
			return res, nil
		case <-ctx.Done():
			if e.bgCtx.Err() == nil {
				_, _ = e.st.Mutate(func(tx *store.Tx) error {
					cur, err := tx.GetRuntimeRequest(reqID)
					if err == nil && cur.Status == threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
						cur.Status = threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_EXPIRED
						cur.ResolvedAt = timestamppb.Now()
						_ = tx.PutRuntimeRequest(cur)
					}
					return nil
				})
			}
			return runtime.ToolGateResult{CancelTurn: true}, ctx.Err()
		}
	}
}

func (e *Engine) RespondRuntimeRequest(ctx context.Context, p *Principal, req *threadv2.RespondRuntimeRequestRequest) (*threadv2.RespondRuntimeRequestResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached threadv2.RespondRuntimeRequestResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "RespondRuntimeRequest", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockThread(req.ThreadId)
	th, err := e.st.Thread(req.ThreadId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	reqs, _ := e.st.RuntimeRequests(req.ThreadId)
	var target *threadv2.RuntimeRequest
	for _, r := range reqs {
		if r.RequestId == req.RequestId {
			target = r
			break
		}
	}
	if target == nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("runtime request not found"))
	}
	if target.Status != threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("runtime request is already resolved"))
	}

	// Enforce cryptographic security boundary: external Grants CANNOT approve
	// tool executions, plan mode exits, or supply secrets! Only Owner can.
	if (target.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL ||
		target.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW ||
		target.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT) && !p.CanApproveRequest {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only a trusted owner device may approve permissions, plans, or secrets"))
	}

	e.mu.Lock()
	waitCh := e.waiters[req.RequestId]
	e.mu.Unlock()

	var gateRes runtime.ToolGateResult
	var resumePrompt string

	switch target.Kind {
	case threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL:
		dec := req.Decision
		if dec == threadv2.ApprovalDecision_APPROVAL_DECISION_UNSPECIFIED {
			dec = threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
		}
		target.ResolvedDecision = dec
		target.ResolvedFeedback = req.Feedback
		switch dec {
		case threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
			threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_THREAD:
			gateRes = runtime.ToolGateResult{Allowed: true}
		case threadv2.ApprovalDecision_APPROVAL_DECISION_DECLINE:
			gateRes = runtime.ToolGateResult{Allowed: false, Feedback: req.Feedback}
		case threadv2.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN:
			gateRes = runtime.ToolGateResult{Allowed: false, CancelTurn: true}
		}

	case threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT:
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

	case threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW:
		target.ResolvedFeedback = req.Feedback
		if req.ApprovePlan {
			target.ResolvedDecision = threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
			gateRes = runtime.ToolGateResult{Allowed: true, PlanApproved: true}
			if th.InteractionMode == threadv2.InteractionMode_INTERACTION_MODE_PLAN {
				resumePrompt = "Plan approved. Proceed with implementation in Code mode."
			} else {
				resumePrompt = "Approved. Proceed."
			}
		} else if req.Decision == threadv2.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN {
			target.ResolvedDecision = threadv2.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN
			gateRes = runtime.ToolGateResult{Allowed: false, PlanApproved: false, CancelTurn: true}
		} else {
			target.ResolvedDecision = threadv2.ApprovalDecision_APPROVAL_DECISION_DECLINE
			gateRes = runtime.ToolGateResult{Allowed: false, PlanApproved: false, Feedback: req.Feedback}
			if fb := strings.TrimSpace(req.Feedback); fb != "" {
				resumePrompt = "Please revise the plan: " + fb
			} else {
				resumePrompt = "Please revise the plan."
			}
		}

	case threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT:
		// NEVER persist req.SecretValue onto target!
		target.ResolvedDecision = threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
		gateRes = runtime.ToolGateResult{Allowed: true, SecretValue: req.SecretValue}
	}

	target.Status = threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_RESOLVED
	target.ResolvedBy = proto.Clone(p.Actor).(*threadv2.Actor)
	target.ResolvedAt = timestamppb.Now()

	var res *threadv2.RespondRuntimeRequestResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		tNow, err := tx.GetThread(req.ThreadId)
		if err != nil {
			return err
		}
		if target.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL &&
			req.Decision == threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_THREAD &&
			target.Approval != nil && target.Approval.RuleKey != "" {
			if err := tx.AddAllowRule(req.ThreadId, target.Approval.RuleKey); err != nil {
				return err
			}
		}
		if target.Kind == threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW && req.ApprovePlan {
			tNow.InteractionMode = threadv2.InteractionMode_INTERACTION_MODE_CODE
			if rNow, rErr := tx.GetRun(target.RunId); rErr == nil {
				rNow.InteractionMode = threadv2.InteractionMode_INTERACTION_MODE_CODE
				if err := tx.PutRun(rNow); err != nil {
					return err
				}
			}
			if lease := e.sup.Get(tNow.ActiveProviderThreadId); lease != nil {
				_ = lease.Session.SetModes(ctx, tNow.EffectiveRuntimeMode, threadv2.InteractionMode_INTERACTION_MODE_CODE)
				lease.InteractionMode = threadv2.InteractionMode_INTERACTION_MODE_CODE
			}
		}
		if err := tx.PutRuntimeRequest(target); err != nil {
			return err
		}
		reqItem := &threadv2.TurnItem{
			ItemId:    "reqitm_" + target.RequestId,
			ThreadId:  req.ThreadId,
			RunId:     target.RunId,
			Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
			Status:    "completed",
			RequestId: target.RequestId,
		}
		_ = tx.PutTurnItem(reqItem)
		if waitCh != nil {
			if rNow, rErr := tx.GetRun(target.RunId); rErr == nil {
				rNow.Status = threadv2.RunStatus_RUN_STATUS_RUNNING
				_ = tx.PutRun(rNow)
			}
			tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_RUNNING
			tNow.PendingRequestId = ""
		} else {
			tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
			tNow.PendingRequestId = ""
		}
		if err := tx.PutThread(tNow); err != nil {
			return err
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: req.ThreadId,
			Type:     "request.resolved",
			Thread:   tNow,
			Item:     reqItem,
			Request:  target,
		}); err != nil {
			return err
		}
		res = &threadv2.RespondRuntimeRequestResponse{
			Thread:  proto.Clone(tNow).(*threadv2.Thread),
			Request: proto.Clone(target).(*threadv2.RuntimeRequest),
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
		// Resumable request answered after turn completion or daemon restart -> automatically continue!
		if dispRes, dErr := e.DispatchMessage(ctx, p, &threadv2.DispatchMessageRequest{
			ThreadId: req.ThreadId,
			Text:     resumePrompt,
			Intent:   threadv2.DispatchIntent_DISPATCH_INTENT_START,
		}); dErr == nil && dispRes.GetThread() != nil {
			res.Thread = dispRes.Thread
		}
	}
	return res, nil
}

func dismissPendingRequestTx(tx *store.Tx, tNow *threadv2.Thread, actor *threadv2.Actor) (*threadv2.RuntimeRequest, *threadv2.TurnItem, error) {
	if tNow.PendingRequestId == "" {
		return nil, nil, nil
	}
	var supersededReq *threadv2.RuntimeRequest
	var supersededItem *threadv2.TurnItem
	if prevReq, rErr := tx.GetRuntimeRequest(tNow.PendingRequestId); rErr == nil &&
		prevReq.Status == threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
		prevReq.Status = threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_DISMISSED
		if actor != nil {
			prevReq.ResolvedBy = proto.Clone(actor).(*threadv2.Actor)
		}
		prevReq.ResolvedAt = timestamppb.Now()
		if err := tx.PutRuntimeRequest(prevReq); err != nil {
			return nil, nil, err
		}
		supersededItem = &threadv2.TurnItem{
			ItemId:    "reqitm_" + prevReq.RequestId,
			ThreadId:  tNow.ThreadId,
			RunId:     prevReq.RunId,
			Kind:      threadv2.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST,
			Status:    "dismissed",
			RequestId: prevReq.RequestId,
		}
		_ = tx.PutTurnItem(supersededItem)
		supersededReq = prevReq
	}
	tNow.PendingRequestId = ""
	return supersededReq, supersededItem, nil
}

// ---------------------------------------------------------------------------
// Interrupt, Queue Management, Config Updates, Checkpoints & Fork

func (e *Engine) InterruptRun(ctx context.Context, p *Principal, req *threadv2.InterruptRunRequest) (*threadv2.InterruptRunResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	unlock := e.lockThread(req.ThreadId)
	th, err := e.st.Thread(req.ThreadId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	if req.RunId != "" && req.RunId != th.ActiveRunId {
		unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("run is not this thread's active run"))
	}
	activeRunID := th.ActiveRunId
	ptID := th.ActiveProviderThreadId
	var dismissEvents []*threadv2.ThreadEvent

	if req.HoldQueue || (activeRunID == "" && th.PendingRequestId != "") {
		dismissEvents, _ = e.st.Mutate(func(tx *store.Tx) error {
			tNow, err := tx.GetThread(req.ThreadId)
			if err != nil {
				return err
			}
			if req.HoldQueue {
				tNow.QueueHeld = true
			}
			var supReq *threadv2.RuntimeRequest
			var supItem *threadv2.TurnItem
			if activeRunID == "" && tNow.PendingRequestId != "" {
				supReq, supItem, err = dismissPendingRequestTx(tx, tNow, p.Actor)
				if err != nil {
					return err
				}
				queued, _ := tx.QueuedRuns(req.ThreadId)
				tNow.QueuedRunCount = uint32(len(queued))
				if len(queued) > 0 {
					tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_QUEUED
				} else {
					tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
				}
			}
			if err := tx.PutThread(tNow); err != nil {
				return err
			}
			if supReq != nil {
				return tx.AppendEvent(&threadv2.ThreadEvent{
					ThreadId: req.ThreadId,
					Type:     "request.resolved",
					Thread:   tNow,
					Item:     supItem,
					Request:  supReq,
				})
			}
			return nil
		})
	}
	unlock()
	if len(dismissEvents) > 0 {
		e.publish(dismissEvents, nil)
	}

	if req.Force && activeRunID != "" {
		e.mu.Lock()
		if cancel, ok := e.runCancels[activeRunID]; ok {
			cancel()
		}
		e.mu.Unlock()
	}
	if lease := e.sup.Get(ptID); lease != nil {
		if req.Force {
			e.sup.DetachSession(ptID, lease.Session, true)
		} else {
			_ = lease.Session.Interrupt(ctx)
		}
	}
	if req.Force && activeRunID != "" {
		e.finishRunInterrupted(req.ThreadId, activeRunID, "force stopped")
	}

	updated, err := e.st.Thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	return &threadv2.InterruptRunResponse{Thread: updated}, nil
}

func (e *Engine) ManageQueue(ctx context.Context, p *Principal, req *threadv2.ManageQueueRequest) (*threadv2.ManageQueueResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached threadv2.ManageQueueResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "ManageQueue", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockThread(req.ThreadId)
	th, err := e.st.Thread(req.ThreadId)
	if err != nil {
		unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	// Promote-to-steer while an active run is running:
	if req.Operation == threadv2.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER && th.ActiveRunId != "" {
		runs, _ := e.st.Runs(req.ThreadId)
		var qRun *threadv2.Run
		for _, r := range runs {
			if r.RunId == req.RunId && r.Status == threadv2.RunStatus_RUN_STATUS_QUEUED {
				qRun = r
				break
			}
		}
		if qRun == nil {
			unlock()
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
		}
		lease := e.sup.Get(th.ActiveProviderThreadId)
		if lease != nil {
			if err := lease.Session.Steer(ctx, qRun.Prompt, e.loadTurnAttachments(qRun.Attachments)); err == nil {
				var res *threadv2.ManageQueueResponse
				events, mErr := e.st.Mutate(func(tx *store.Tx) error {
					tNow, _ := tx.GetThread(req.ThreadId)
					qRun.Status = threadv2.RunStatus_RUN_STATUS_CANCELLED
					_ = tx.PutRun(qRun)
					item := &threadv2.TurnItem{
						ItemId:      newID("itm"),
						ThreadId:    req.ThreadId,
						RunId:       tNow.ActiveRunId,
						Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
						Status:      "promoted_to_steer",
						Actor:       qRun.Author,
						Text:        qRun.Prompt,
						Attachments: qRun.Attachments,
					}
					_ = tx.PutTurnItem(item)
					rem, _ := tx.QueuedRuns(req.ThreadId)
					tNow.QueuedRunCount = uint32(len(rem))
					_ = tx.PutThread(tNow)
					_ = tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: req.ThreadId,
						Type:     "queue.promoted",
						Thread:   tNow,
						Run:      qRun,
						Item:     item,
					})
					res = &threadv2.ManageQueueResponse{Thread: proto.Clone(tNow).(*threadv2.Thread), QueuedRuns: rem}
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

	var startNow *threadv2.Run
	var res *threadv2.ManageQueueResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		tNow, err := tx.GetThread(req.ThreadId)
		if err != nil {
			return err
		}
		queued, err := tx.QueuedRuns(req.ThreadId)
		if err != nil {
			return err
		}

		var updatedRuns []*threadv2.Run
		var startedItem *threadv2.TurnItem

		switch req.Operation {
		case threadv2.QueueOperation_QUEUE_OPERATION_EDIT:
			var target *threadv2.Run
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
			updatedRuns = []*threadv2.Run{target}

		case threadv2.QueueOperation_QUEUE_OPERATION_CANCEL:
			var target *threadv2.Run
			for _, r := range queued {
				if r.RunId == req.RunId {
					target = r
					break
				}
			}
			if target == nil {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			target.Status = threadv2.RunStatus_RUN_STATUS_CANCELLED
			target.CompletedAt = timestamppb.Now()
			if err := tx.PutRun(target); err != nil {
				return err
			}
			updatedRuns = []*threadv2.Run{target}

		case threadv2.QueueOperation_QUEUE_OPERATION_REORDER:
			idx := slices.IndexFunc(queued, func(r *threadv2.Run) bool { return r.RunId == req.RunId })
			if idx < 0 {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("run is no longer queued"))
			}
			moving := queued[idx]
			rest := slices.Delete(slices.Clone(queued), idx, idx+1)
			insertAt := len(rest)
			if req.BeforeRunId != "" {
				if bIdx := slices.IndexFunc(rest, func(r *threadv2.Run) bool { return r.RunId == req.BeforeRunId }); bIdx >= 0 {
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

		case threadv2.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE,
			threadv2.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER:
			tNow.QueueHeld = false
			if tNow.ActiveRunId == "" && len(queued) > 0 {
				supReq, supItem, dErr := dismissPendingRequestTx(tx, tNow, p.Actor)
				if dErr != nil {
					return dErr
				}
				pickIdx := 0
				if req.RunId != "" {
					if idx := slices.IndexFunc(queued, func(r *threadv2.Run) bool { return r.RunId == req.RunId }); idx >= 0 {
						pickIdx = idx
					}
				}
				startNow = queued[pickIdx]
				tNow.RunCount++
				startNow.Ordinal = tNow.RunCount
				startNow.Status = threadv2.RunStatus_RUN_STATUS_RUNNING
				startNow.StartedAt = timestamppb.Now()
				if err := tx.PutRun(startNow); err != nil {
					return err
				}
				startedItem = &threadv2.TurnItem{
					ItemId:      newID("itm"),
					ThreadId:    req.ThreadId,
					RunId:       startNow.RunId,
					Kind:        threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE,
					Status:      "completed",
					Actor:       startNow.Author,
					Text:        startNow.Prompt,
					Attachments: startNow.Attachments,
				}
				_ = tx.PutTurnItem(startedItem)
				tNow.ActiveRunId = startNow.RunId
				tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_RUNNING
				tNow.ModelSelection = startNow.ModelSelection
				tNow.EffectiveRuntimeMode = startNow.RuntimeMode
				tNow.InteractionMode = startNow.InteractionMode
				updatedRuns = []*threadv2.Run{startNow}
				if supReq != nil {
					_ = tx.AppendEvent(&threadv2.ThreadEvent{
						ThreadId: req.ThreadId,
						Type:     "request.resolved",
						Thread:   tNow,
						Item:     supItem,
						Request:  supReq,
					})
				}
			}

		default:
			return connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported queue operation"))
		}

		rem, err := tx.QueuedRuns(req.ThreadId)
		if err != nil {
			return err
		}
		tNow.QueuedRunCount = uint32(len(rem))
		if tNow.ActiveRunId == "" && len(rem) == 0 && tNow.Status == threadv2.ThreadStatus_THREAD_STATUS_QUEUED {
			tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
		}
		if err := tx.PutThread(tNow); err != nil {
			return err
		}
		if len(updatedRuns) == 0 {
			if err := tx.AppendEvent(&threadv2.ThreadEvent{
				ThreadId: req.ThreadId,
				Type:     "queue.updated",
				Thread:   tNow,
			}); err != nil {
				return err
			}
		} else {
			for i, ur := range updatedRuns {
				var it *threadv2.TurnItem
				if i == 0 {
					it = startedItem
				}
				if err := tx.AppendEvent(&threadv2.ThreadEvent{
					ThreadId: req.ThreadId,
					Type:     "queue.updated",
					Thread:   tNow,
					Run:      ur,
					Item:     it,
				}); err != nil {
					return err
				}
			}
		}
		res = &threadv2.ManageQueueResponse{
			Thread:     proto.Clone(tNow).(*threadv2.Thread),
			QueuedRuns: rem,
		}
		return tx.PutReceipt(req.CommandId, "ManageQueue", res)
	})
	unlock()
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	if startNow != nil {
		e.spawnRun(req.ThreadId, startNow.RunId, startNow.Intent == threadv2.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT)
	}
	return res, nil
}

func (e *Engine) UpdateThreadConfig(ctx context.Context, p *Principal, req *threadv2.UpdateThreadConfigRequest) (*threadv2.UpdateThreadConfigResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached threadv2.UpdateThreadConfigResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "UpdateThreadConfig", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockThread(req.ThreadId)
	defer unlock()

	th, err := e.st.Thread(req.ThreadId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	nextSel := proto.Clone(th.ModelSelection).(*threadv2.ModelSelection)
	if req.ModelSelection != nil && req.ModelSelection.InstanceId != "" {
		instCfg, _, err := e.resolveInstanceConfig(p, req.ModelSelection.InstanceId)
		if err != nil {
			return nil, err
		}
		if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit target provider"))
		}
		if err := e.projectAllowsInstance(th.ProjectId, instCfg.InstanceId); err != nil {
			return nil, err
		}
		nextSel = proto.Clone(req.ModelSelection).(*threadv2.ModelSelection)
	}

	// Pinning, renaming or archiving must not recompute the ceiling with the caller's grant.
	nextReqMode, nextEffMode, nextMaxMode := th.RuntimeMode, th.EffectiveRuntimeMode, th.MaxRuntimeMode
	if req.RuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		nextReqMode = req.RuntimeMode
		nextEffMode = policy.EffectiveMode(e.machineMax(), p.GrantCeiling, nextReqMode)
		nextMaxMode = policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling)
	}

	nextIMode := th.InteractionMode
	if req.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_UNSPECIFIED {
		nextIMode = req.InteractionMode
	}

	instCfg, drv, _ := e.resolveInstanceConfig(p, nextSel.GetInstanceId())
	var caps *threadv2.ProviderCapabilities
	if drv != nil && instCfg != nil {
		caps = drv.Capabilities(instCfg.AcpFlavor)
	}
	var currentPT *threadv2.ProviderThread
	if th.ActiveProviderThreadId != "" {
		pts, _ := e.st.ProviderThreads(req.ThreadId)
		for _, pt := range pts {
			if pt.ProviderThreadId == th.ActiveProviderThreadId {
				currentPT = pt
				break
			}
		}
	}
	lease := e.sup.Get(th.ActiveProviderThreadId)
	plan := DecideTransition(TransitionInput{
		CurrentBinding:      currentPT,
		LiveLease:           lease,
		NextSelection:       nextSel,
		NextEffectiveMode:   nextEffMode,
		NextInteractionMode: nextIMode,
		Capabilities:        caps,
	})

	// Apply in-session hot switch immediately if idle!
	if th.ActiveRunId == "" && plan.Action == TransitionSwitchInSession && lease != nil {
		if plan.ModelChanged || plan.OptionsChanged {
			if err := lease.Session.SetSelection(ctx, nextSel); err == nil {
				lease.Selection = proto.Clone(nextSel).(*threadv2.ModelSelection)
			}
		}
		if plan.ModeChanged {
			if err := lease.Session.SetModes(ctx, nextEffMode, nextIMode); err == nil {
				lease.RuntimeMode = nextEffMode
				lease.InteractionMode = nextIMode
			}
		}
	}

	// Archiving detaches any idle live process and cleans up isolated worktree directory (leaving branch intact).
	archivingNow := req.HasArchived && req.Archived && th.Status != threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED
	if archivingNow && th.ActiveRunId != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot archive a thread while a run is active; stop it first"))
	}

	var res *threadv2.UpdateThreadConfigResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		tNow, err := tx.GetThread(req.ThreadId)
		if err != nil {
			return err
		}
		tNow.ModelSelection = nextSel
		tNow.RuntimeMode = nextReqMode
		tNow.EffectiveRuntimeMode = nextEffMode
		tNow.MaxRuntimeMode = nextMaxMode
		tNow.InteractionMode = nextIMode
		if strings.TrimSpace(req.Title) != "" {
			tNow.Title = strings.TrimSpace(req.Title)
			tNow.TitleRenamed = true
		}
		if req.HasPinned {
			tNow.Pinned = req.Pinned
		}
		if req.HasArchived {
			if req.Archived {
				tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED
				tNow.ArchivedAt = timestamppb.Now()
			} else if tNow.Status == threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED {
				tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
				tNow.ArchivedAt = nil
			}
		}
		if err := tx.PutThread(tNow); err != nil {
			return err
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: req.ThreadId,
			Type:     "thread.config_updated",
			Thread:   tNow,
		}); err != nil {
			return err
		}
		res = &threadv2.UpdateThreadConfigResponse{
			Thread:            proto.Clone(tNow).(*threadv2.Thread),
			TransitionApplied: string(plan.Action),
		}
		return tx.PutReceipt(req.CommandId, "UpdateThreadConfig", res)
	})
	if err != nil {
		return nil, err
	}
	if archivingNow {
		if th.ActiveProviderThreadId != "" {
			e.sup.Detach(th.ActiveProviderThreadId, false)
		}
		if th.GetWorkspace().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE && th.GetWorkspace().GetWorktreePath() != "" {
			if ws, wErr := e.st.Workspace(th.ProjectId); wErr == nil {
				_ = e.vcs.RemoveWorktree(ctx, ws.Path, th.GetWorkspace().GetWorktreePath())
			}
		}
	}
	e.publish(events, nil)
	return res, nil
}

func (e *Engine) ListCheckpoints(_ context.Context, p *Principal, threadID string) ([]*threadv2.Checkpoint, error) {
	th, err := e.st.Thread(threadID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	return e.st.Checkpoints(threadID)
}

func (e *Engine) GetThreadDiff(ctx context.Context, p *Principal, threadID, checkpointID string) (*threadv2.GetThreadDiffResponse, error) {
	th, err := e.st.Thread(threadID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	workdir, err := e.resolveWorkdir(th)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	baseTree := ""
	ckpts, _ := e.st.Checkpoints(threadID)
	if checkpointID != "" {
		for _, c := range ckpts {
			if c.CheckpointId == checkpointID {
				baseTree = c.GitTreeOid
				break
			}
		}
	} else if len(ckpts) > 0 && ckpts[0].RunOrdinal == 0 {
		baseTree = ckpts[0].GitTreeOid
	}
	diff, files, err := e.vcs.DiffWorkingTree(ctx, workdir, baseTree)
	if err != nil {
		return nil, err
	}
	return &threadv2.GetThreadDiffResponse{UnifiedDiff: diff, Files: files}, nil
}

func (e *Engine) RollbackCheckpoint(ctx context.Context, p *Principal, req *threadv2.RollbackCheckpointRequest) (*threadv2.RollbackCheckpointResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached threadv2.RollbackCheckpointResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "RollbackCheckpoint", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	unlock := e.lockThread(req.ThreadId)
	defer unlock()

	th, err := e.st.Thread(req.ThreadId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if !p.canViewThread(th) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	if th.ActiveRunId != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot rollback while a run is active; stop it first"))
	}

	ckpts, _ := e.st.Checkpoints(req.ThreadId)
	var target *threadv2.Checkpoint
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
		workdir, err := e.resolveWorkdir(th)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		if err := e.vcs.RestoreTree(ctx, workdir, target.GitTreeOid); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	// Detach current provider binding so the next run cleanly rebuilds context from surviving history!
	if th.ActiveProviderThreadId != "" {
		e.sup.Detach(th.ActiveProviderThreadId, false)
	}

	runs, _ := e.st.Runs(req.ThreadId)
	var res *threadv2.RollbackCheckpointResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		tNow, err := tx.GetThread(req.ThreadId)
		if err != nil {
			return err
		}
		removedRuns := map[string]bool{}
		for _, r := range runs {
			if r.Ordinal > target.RunOrdinal && r.Status != threadv2.RunStatus_RUN_STATUS_QUEUED {
				r.Status = threadv2.RunStatus_RUN_STATUS_ROLLED_BACK
				_ = tx.PutRun(r)
				removedRuns[r.RunId] = true
			}
		}
		if err := tx.DeleteTurnItemsAfterRunIds(req.ThreadId, removedRuns); err != nil {
			return err
		}
		tNow.RunCount = target.RunOrdinal
		tNow.ActiveProviderThreadId = ""
		tNow.LastError = ""
		tNow.Status = threadv2.ThreadStatus_THREAD_STATUS_IDLE
		notice := &threadv2.TurnItem{
			ItemId:       newID("rb"),
			ThreadId:     req.ThreadId,
			Kind:         threadv2.TurnItemKind_TURN_ITEM_KIND_SYSTEM_NOTICE,
			Status:       "completed",
			CheckpointId: target.CheckpointId,
			Text:         fmt.Sprintf("Rewound thread to checkpoint #%d (restore_files=%v)", target.RunOrdinal, req.RestoreFiles),
		}
		_ = tx.PutTurnItem(notice)
		if err := tx.PutThread(tNow); err != nil {
			return err
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId:   req.ThreadId,
			Type:       "thread.rolled_back",
			Thread:     tNow,
			Checkpoint: target,
			Item:       notice,
		}); err != nil {
			return err
		}
		res = &threadv2.RollbackCheckpointResponse{
			Thread:     proto.Clone(tNow).(*threadv2.Thread),
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

func (e *Engine) ForkThread(ctx context.Context, p *Principal, req *threadv2.ForkThreadRequest) (*threadv2.ForkThreadResponse, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	var cached threadv2.ForkThreadResponse
	defer e.lockCommand(req.CommandId)()
	if ok, err := e.st.GetReceipt(req.CommandId, "ForkThread", &cached); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if ok {
		return &cached, nil
	}

	srcProj, err := e.st.Projection(req.SourceThreadId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("source thread not found"))
	}
	if !p.canViewThread(srcProj.Thread) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	upTo := req.UpToRunOrdinal
	if upTo == 0 {
		upTo = srcProj.Thread.RunCount
	}
	includedRuns := map[string]bool{}
	var copiedRuns []*threadv2.Run
	for _, r := range srcProj.Runs {
		if r.Ordinal > 0 && r.Ordinal <= upTo && r.Status != threadv2.RunStatus_RUN_STATUS_ROLLED_BACK {
			includedRuns[r.RunId] = true
			copiedRuns = append(copiedRuns, r)
		}
	}

	newThreadID := e.newThreadID()
	wsStrategy := proto.Clone(req.Workspace).(*threadv2.WorkspaceStrategy)
	if wsStrategy == nil {
		wsStrategy = &threadv2.WorkspaceStrategy{Mode: srcProj.Thread.GetWorkspace().GetMode(), BaseRef: srcProj.Thread.GetWorkspace().GetBranch()}
	}
	ws, err := e.st.Workspace(srcProj.Thread.ProjectId)
	if err != nil {
		return nil, err
	}
	if wsStrategy.Mode == identityv1.Workspace_WORKSPACE_WORKTREE {
		baseRef := wsStrategy.BaseRef
		if baseRef == "" {
			baseRef = ws.DefaultBaseRef
		}
		wtPath, branch, wtErr := e.vcs.PrepareWorktree(ctx, ws.Path, newThreadID, baseRef, wsStrategy.Branch)
		if wtErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, wtErr)
		}
		wsStrategy.WorktreePath = wtPath
		wsStrategy.Branch = branch
		// The branch HEAD lacks the source thread's uncommitted edits: lay down the
		// files of the last checkpoint at or before the fork point.
		if cp := forkCheckpoint(srcProj.Checkpoints, includedRuns, upTo); cp != nil {
			if err := e.vcs.RestoreTree(ctx, wtPath, cp.GitTreeOid); err != nil {
				_ = e.vcs.RemoveWorktree(ctx, ws.Path, wtPath)
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("restore fork files: %w", err))
			}
		}
	}

	sel := proto.Clone(srcProj.Thread.ModelSelection).(*threadv2.ModelSelection)
	if req.ModelSelection != nil && req.ModelSelection.InstanceId != "" {
		instCfg, _, err := e.resolveInstanceConfig(p, req.ModelSelection.InstanceId)
		if err != nil {
			return nil, err
		}
		if !p.allowsAgent(instCfg.InstanceId, instCfg.Driver, instCfg.AcpFlavor) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit target provider"))
		}
		sel = proto.Clone(req.ModelSelection).(*threadv2.ModelSelection)
	} else if !e.principalAllowsInstance(p, sel.GetInstanceId()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant does not permit this thread's provider"))
	}
	if err := e.projectAllowsInstance(srcProj.Thread.ProjectId, sel.GetInstanceId()); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = srcProj.Thread.Title + " (fork)"
	}

	forked := &threadv2.Thread{
		ThreadId:             newThreadID,
		MachineId:            e.cfg.MachineID,
		ProjectId:            srcProj.Thread.ProjectId,
		Title:                title,
		TitleRenamed:         true,
		Status:               threadv2.ThreadStatus_THREAD_STATUS_IDLE,
		ModelSelection:       sel,
		RuntimeMode:          srcProj.Thread.RuntimeMode,
		EffectiveRuntimeMode: policy.EffectiveMode(e.machineMax(), p.GrantCeiling, srcProj.Thread.RuntimeMode),
		MaxRuntimeMode:       policy.EffectiveCeiling(e.machineMax(), p.GrantCeiling),
		InteractionMode:      srcProj.Thread.InteractionMode,
		Workspace:            wsStrategy,
		CreatedBy:            proto.Clone(p.Actor).(*threadv2.Actor),
		RunCount:             uint32(len(copiedRuns)),
		Lineage: &threadv2.ThreadLineage{
			ParentThreadId:       req.SourceThreadId,
			ForkedFromRunOrdinal: upTo,
			Relationship:         "fork",
			Controllable:         true,
		},
	}

	var res *threadv2.ForkThreadResponse
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		if err := tx.PutThread(forked); err != nil {
			return err
		}
		runIDMap := map[string]string{}
		for _, r := range copiedRuns {
			cp := proto.Clone(r).(*threadv2.Run)
			cp.RunId = newID("run")
			cp.ThreadId = newThreadID
			cp.ProviderThreadId = ""
			runIDMap[r.RunId] = cp.RunId
			if err := tx.PutRun(cp); err != nil {
				return err
			}
		}
		for _, it := range srcProj.Items {
			if newRunID, ok := runIDMap[it.RunId]; ok && it.Kind != threadv2.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST {
				cpIt := proto.Clone(it).(*threadv2.TurnItem)
				cpIt.ItemId = newID("itm")
				cpIt.ThreadId = newThreadID
				cpIt.RunId = newRunID
				cpIt.Seq = 0
				if err := tx.PutTurnItem(cpIt); err != nil {
					return err
				}
			}
		}
		if err := tx.AppendEvent(&threadv2.ThreadEvent{
			ThreadId: newThreadID,
			Type:     "thread.forked",
			Thread:   forked,
		}); err != nil {
			return err
		}
		res = &threadv2.ForkThreadResponse{Thread: proto.Clone(forked).(*threadv2.Thread)}
		return tx.PutReceipt(req.CommandId, "ForkThread", res)
	})
	if err != nil {
		return nil, err
	}
	e.publish(events, nil)
	return res, nil
}

// forkCheckpoint picks the latest checkpoint at or before run ordinal upTo that
// belongs to a surviving run (or the ordinal-0 baseline).
func forkCheckpoint(ckpts []*threadv2.Checkpoint, includedRuns map[string]bool, upTo uint32) *threadv2.Checkpoint {
	var best *threadv2.Checkpoint
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

func (e *Engine) DeleteThread(ctx context.Context, p *Principal, threadID string) error {
	if p == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	unlock := e.lockThread(threadID)
	th, err := e.st.Thread(threadID)
	if errors.Is(err, store.ErrNotFound) {
		unlock()
		return nil
	}
	if err != nil {
		unlock()
		return err
	}
	if !p.canViewThread(th) {
		unlock()
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	if th.ActiveRunId != "" {
		e.mu.Lock()
		if cancel, ok := e.runCancels[th.ActiveRunId]; ok {
			cancel()
		}
		e.mu.Unlock()
	}
	if th.ActiveProviderThreadId != "" {
		e.sup.Detach(th.ActiveProviderThreadId, true)
	}
	if th.GetWorkspace().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE && th.GetWorkspace().GetWorktreePath() != "" {
		if ws, wErr := e.st.Workspace(th.ProjectId); wErr == nil {
			_ = e.vcs.RemoveWorktree(ctx, ws.Path, th.GetWorkspace().GetWorktreePath())
		}
	}
	_, err = e.st.Mutate(func(tx *store.Tx) error {
		return tx.DeleteThread(threadID)
	})
	unlock()
	if err == nil {
		e.publish(nil, []string{threadID})
	}
	return err
}

// ---------------------------------------------------------------------------
// Reads & Streaming Subscriptions

func (e *Engine) GetThread(_ context.Context, p *Principal, threadID string) (*threadv2.ThreadProjection, error) {
	proj, err := e.st.Projection(threadID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if err != nil {
		return nil, err
	}
	if !p.canViewThread(proj.Thread) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	for _, pt := range proj.ProviderThreads {
		pt.ProcessAlive = e.sup.Get(pt.ProviderThreadId) != nil
	}
	return proj, nil
}

// busyInPlace says whether a session works in a project's own folder now
// (queued, preparing, running or waiting): its branch is not to be switched
// under it.
func (e *Engine) busyInPlace(projectID string) bool {
	threads, err := e.st.ListThreads(projectID, false, 0)
	if err != nil {
		return true
	}
	for _, th := range threads {
		if th.GetWorkspace().GetMode() != identityv1.Workspace_WORKSPACE_IN_PLACE {
			continue
		}
		switch th.GetStatus() {
		case threadv2.ThreadStatus_THREAD_STATUS_QUEUED, threadv2.ThreadStatus_THREAD_STATUS_PREPARING,
			threadv2.ThreadStatus_THREAD_STATUS_RUNNING, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST:
			return true
		}
	}
	return false
}

func (e *Engine) ListThreads(_ context.Context, p *Principal, projectID string, includeArchived bool, limit uint32) ([]*threadv2.Thread, error) {
	all, err := e.st.ListThreads(projectID, includeArchived, limit)
	if err != nil {
		return nil, err
	}
	var out []*threadv2.Thread
	for _, t := range all {
		if p.canViewThread(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (e *Engine) SubscribeThreads(ctx context.Context, p *Principal, includeArchived bool, send func(*threadv2.SubscribeThreadsResponse) error) error {
	sub := &shellSub{
		includeArchived: includeArchived,
		ch:              make(chan *threadv2.SubscribeThreadsResponse, 64),
	}
	e.mu.Lock()
	e.shellSubs[sub] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.shellSubs, sub)
		e.mu.Unlock()
	}()

	initial, err := e.ListThreads(ctx, p, "", includeArchived, 0)
	if err != nil {
		return err
	}
	if err := send(&threadv2.SubscribeThreadsResponse{Snapshot: true, Threads: initial}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-sub.ch:
			var visible []*threadv2.Thread
			for _, t := range msg.Threads {
				if p.canViewThread(t) {
					visible = append(visible, t)
				}
			}
			if len(visible) == 0 && len(msg.DeletedThreadIds) == 0 {
				continue
			}
			if err := send(&threadv2.SubscribeThreadsResponse{
				Threads:          visible,
				DeletedThreadIds: msg.DeletedThreadIds,
			}); err != nil {
				return err
			}
		}
	}
}

func (e *Engine) SubscribeThread(ctx context.Context, p *Principal, threadID string, afterSeq uint64, send func(*threadv2.ThreadEvent) error) error {
	th, err := e.st.Thread(threadID)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if err != nil {
		return err
	}
	if !p.canViewThread(th) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}

	ch := make(chan *threadv2.ThreadEvent, 256)
	e.mu.Lock()
	if e.threadSubs[threadID] == nil {
		e.threadSubs[threadID] = map[chan *threadv2.ThreadEvent]struct{}{}
	}
	e.threadSubs[threadID][ch] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.threadSubs[threadID], ch)
		e.mu.Unlock()
	}()

	history, err := e.st.EventsAfter(threadID, afterSeq)
	if err != nil {
		return err
	}
	highWater := afterSeq
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
		case ev := <-ch:
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

func deriveTitle(prompt string) string {
	s := strings.TrimSpace(prompt)
	if s == "" {
		return "New Thread"
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	runes := []rune(s)
	if len(runes) > 48 {
		return string(runes[:48]) + "…"
	}
	return s
}
