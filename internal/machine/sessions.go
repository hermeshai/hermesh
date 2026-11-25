package machine

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	rtacp "github.com/hermeshai/hermesh/internal/runtime/acp"
	rtclaude "github.com/hermeshai/hermesh/internal/runtime/claude"
	rtcodex "github.com/hermeshai/hermesh/internal/runtime/codex"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

type Limits struct {
	MaxConcurrent      int
	StartTimeout       time.Duration
	FirstOutputTimeout time.Duration
	IdleTimeout        time.Duration
	ToolTimeout        time.Duration
	TurnTimeout        time.Duration
	IdleProcessTTL     time.Duration
	RetryDelay         time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		StartTimeout:       10 * time.Minute,
		FirstOutputTimeout: 3 * time.Minute,
		IdleTimeout:        15 * time.Minute,
		ToolTimeout:        2 * time.Hour,
		IdleProcessTTL:     10 * time.Minute,
		RetryDelay:         30 * time.Second,
	}
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

// AgentDiscoverer probes whether a coding agent is installed, logged in, and what models it offers.
type AgentDiscoverer interface {
	Discover(ctx context.Context) rt.Info
}

func DefaultDiscoverers() map[string]AgentDiscoverer {
	pathEnv := shellenv.Path()
	cl := rtclaude.NewDriver(pathEnv)
	if b := os.Getenv("HERMESH_CLAUDE_BIN"); b != "" {
		cl.Binary = b
	}
	cx := rtcodex.NewDriver(pathEnv)
	if b := os.Getenv("HERMESH_CODEX_BIN"); b != "" {
		cx.Binary = b
	}
	ds := map[string]AgentDiscoverer{
		"claude": cl,
		"codex":  cx,
	}
	for _, p := range rtacp.Providers(pathEnv) {
		ds[p.Name()] = p
	}
	return ds
}

// AgentNames lists the agents in display order: Claude Code and Codex, then the ACP agents.
func AgentNames() []string {
	names := []string{"claude", "codex"}
	for _, a := range rtacp.Agents {
		names = append(names, a.Name)
	}
	return names
}

func cerr(code connect.Code, format string, a ...any) error {
	return connect.NewError(code, fmt.Errorf(format, a...))
}

func buildRuntimeDrivers(cfg Config) (map[string]rt.Driver, bool) {
	if cfg.Drivers != nil {
		_, hasScripted := cfg.Drivers["scripted"]
		return cfg.Drivers, hasScripted
	}
	if os.Getenv("HERMESH_FAKE_PROVIDERS") == "1" {
		d := map[string]rt.Driver{
			"scripted": scripted.NewDriver(),
			"fake":     scripted.NewDriver(),
			"claude":   scripted.NewDriver(),
			"codex":    scripted.NewDriver(),
			"acp":      &scripted.Driver{DisableSteer: true},
		}
		for _, name := range strings.FieldsFunc(os.Getenv("HERMESH_FAKE_AGENTS"), func(r rune) bool { return r == ',' }) {
			d[name] = scripted.NewDriver()
		}
		return d, true
	}
	pathEnv := shellenv.Path()
	cl := rtclaude.NewDriver(pathEnv)
	if b := os.Getenv("HERMESH_CLAUDE_BIN"); b != "" {
		cl.Binary = b
	}
	cx := rtcodex.NewDriver(pathEnv)
	if b := os.Getenv("HERMESH_CODEX_BIN"); b != "" {
		cx.Binary = b
	}
	drivers := map[string]rt.Driver{
		"claude": cl,
		"codex":  cx,
		"acp":    rtacp.NewDriver(pathEnv),
	}
	includeScripted := os.Getenv("HERMESH_INCLUDE_SCRIPTED") == "1"
	if includeScripted {
		drivers["scripted"] = scripted.NewDriver()
	}
	return drivers, includeScripted
}

func (n *Machine) openSessions() error {
	drivers, includeScripted := buildRuntimeDrivers(n.cfg)
	idleTTL := n.cfg.Limits.IdleProcessTTL
	if idleTTL <= 0 {
		idleTTL = 10 * time.Minute
	}
	eng, err := kernel.Open(kernel.Config{
		DataDir:          n.cfg.DataDir,
		MachineID:        n.machineID,
		Logger:           n.log,
		Drivers:          drivers,
		IncludeScripted:  includeScripted,
		IdleTTL:          idleTTL,
		QuestionTimeout:  30 * time.Minute,
		MachineMaxMode:   n.maxRuntimeMode(),
		IsPaused:         n.isPaused,
		MCPServers:       n.mcpServers,
		AgentModels:      n.agentModelList,
		ResolveSelection: n.resolveModelSelection,
		CheckRelease:     n.checkReleaseSession,
		OnDeliverMessage: n.deliverMessageRPC,
		OnNotice: func(sess *sessionv1.Session, ev *sessionv1.SessionEvent) {
			n.sessionNotice(sess, ev)
			n.sessionNews(sess, ev)
			if n.testNotice != nil {
				n.testNotice(sess, ev)
			}
		},
		OnWork: func(sess *sessionv1.Session, run *sessionv1.Run, ev *sessionv1.SessionEvent) {
			n.st.recordKernelWork(sess, run, ev)
		},
		OnSessionChanged: func(sess *sessionv1.Session) {
			n.changes().session(sess.SessionId)
		},
		OnSessionDeleted: func(sessionID string) {
			n.changes().session(sessionID)
		},
		OnLimits: func(sess *sessionv1.Session, l rt.Limits) {
			n.noteLimits(cmp.Or(sess.Agent, sess.GetModelSelection().GetInstanceId()), l)
		},
	})
	if err != nil {
		return err
	}
	// Register any extra custom drivers (e.g. test drivers like "fake" or "flaky") as provider instances.
	existing, _ := eng.Store().ProviderInstances()
	known := map[string]string{}
	for _, inst := range existing {
		known[inst.InstanceId] = inst.Driver
	}
	for name := range drivers {
		if name == "acp" || known[name] == name {
			continue
		}
		_, _ = eng.UpsertProviderInstance(context.Background(), kernel.OwnerPrincipal("owner", "Owner"), &sessionv1.ProviderInstanceConfig{
			InstanceId:  name,
			Driver:      name,
			DisplayName: name,
			Enabled:     true,
		})
	}
	if projects, pErr := n.st.Projects(); pErr == nil {
		for _, pr := range projects {
			_ = eng.SyncWorkspace(pr.ID, pr.Path, pr.DefaultBaseRef, pr.Workspace, pr.Description, nil)
		}
	}
	n.sessions = eng
	// What an earlier Hermesh kept here moved into the kernel's store as it opened. A move that failed is said
	// aloud: in the log, and to the owner with the machine's health; it is tried again at the next start.
	for _, r := range eng.Store().Steps() {
		switch {
		case r.Err != nil:
			n.log.Error("moving this machine's earlier data failed; it is tried again at the next start", "move", r.Name, "moved", r.Moved, "err", r.Err)
			n.moveFailed = append(n.moveFailed, r.Name+": "+r.Err.Error())
		case r.Ran && r.Moved > 0:
			n.log.Info("moved this machine's earlier data", "move", r.Name, "moved", r.Moved)
		}
	}
	if n.st.needsWorkRebuild() {
		_ = n.st.RebuildWork(eng)
	}
	return nil
}

func (n *Machine) Sessions() *kernel.Engine { return n.sessions }

func (l *Local) Sessions() *kernel.Engine { return l.n.sessions }

func (n *Machine) sessionPrincipal(ctx context.Context) *kernel.Principal {
	return toKernelPrincipal(principalFrom(ctx))
}

func toKernelPrincipal(p *Principal) *kernel.Principal {
	if p == nil {
		return nil
	}
	if p.Kind == PrincipalOwner {
		return kernel.OwnerPrincipal("owner", "Owner")
	}
	if p.Grant != nil {
		return kernel.GrantPrincipal(p.Grant)
	}
	return nil
}

func (n *Machine) agentModelList(agent string) ([]rt.Model, rt.Settings, bool) {
	i := n.providerInfo(agent)
	if i.ModelsState != rt.ModelsKnown {
		return nil, rt.Settings{}, false
	}
	d := n.agentDefaults(agent)
	return i.Models, rt.Settings{Model: cmp.Or(d.Model, i.Own.Model), Effort: cmp.Or(d.Effort, i.Own.Effort)}, true
}

func (n *Machine) resolveModelSelection(instanceID string, sel *sessionv1.ModelSelection) (*sessionv1.ModelSelection, error) {
	if sel == nil {
		sel = &sessionv1.ModelSelection{InstanceId: instanceID}
	} else {
		sel = proto.Clone(sel).(*sessionv1.ModelSelection)
	}
	if why := n.outOfProcesses(); why != "" {
		return nil, cerr(connect.CodeResourceExhausted, "%s", why)
	}
	info := n.providerInfo(instanceID)
	reqModel := sel.Model
	reqEffort := ""
	if sel.Options != nil {
		reqEffort = sel.Options["effort"].GetStringValue()
	}
	def := n.agentDefaults(instanceID)
	runs := cmp.Or(reqModel, def.Model, info.Own.Model)
	if len(info.Models) > 0 || info.AllModels || reqEffort != "" {
		if _, hasDisc := n.cfg.Discoverers[instanceID]; hasDisc {
			if err := checkSettings(instanceID, info, reqModel, reqEffort, runs); err != nil {
				return nil, err
			}
		}
	}
	if sel.Model == "" {
		sel.Model = cmp.Or(def.Model, info.Own.Model)
	}
	eff := reqEffort
	if eff == "" && takesEffort(info, sel.Model, def.Effort) {
		eff = def.Effort
	}
	if eff == "" && takesEffort(info, sel.Model, info.Own.Effort) {
		eff = info.Own.Effort
	}
	if eff != "" {
		if sel.Options == nil {
			sel.Options = map[string]*sessionv1.OptionValue{}
		}
		sel.Options["effort"] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: eff}}
	}
	if def.Speed != identityv1.AgentSpeed_AGENT_SPEED_UNSPECIFIED {
		if sel.Options == nil {
			sel.Options = map[string]*sessionv1.OptionValue{}
		}
		if _, set := sel.Options["speed"]; !set {
			speedStr := "standard"
			if def.Speed == identityv1.AgentSpeed_AGENT_SPEED_FAST {
				speedStr = "fast"
			}
			sel.Options["speed"] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: speedStr}}
		}
		if def.Speed == identityv1.AgentSpeed_AGENT_SPEED_FAST {
			if _, set := sel.Options["fast_mode"]; !set {
				sel.Options["fast_mode"] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_BoolValue{BoolValue: true}}
			}
		}
	}
	return sel, nil
}

// BasicSession is s as lists show it: without result,
// progress.last_message_excerpt and provider_session_ref.
func BasicSession(s *sessionv1.Session) *sessionv1.Session {
	if s == nil {
		return nil
	}
	if s.Result == "" && s.ProviderSessionRef == "" && s.GetProgress().GetLastMessageExcerpt() == "" {
		return s
	}
	b := proto.CloneOf(s)
	b.Result, b.ProviderSessionRef = "", ""
	if b.Progress != nil {
		b.Progress.LastMessageExcerpt = ""
	}
	return b
}

// BasicSessions is BasicSession of each.
func BasicSessions(list []*sessionv1.Session) []*sessionv1.Session {
	out := make([]*sessionv1.Session, len(list))
	for i, s := range list {
		out[i] = BasicSession(s)
	}
	return out
}

// Preview is s's first 3 lines, at most 512 bytes, cut at a UTF-8 boundary.
func Preview(s string) string {
	const previewLines = 3
	const previewBytes = 512
	end := len(s)
	for i, n := 0, 0; i < len(s); i++ {
		if s[i] == '\n' {
			if n++; n == previewLines && i < len(s)-1 {
				end = i
				break
			}
		}
	}
	if end > previewBytes {
		end = previewBytes
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
	}
	return s[:end]
}

// Unsettled sessions need the owner or have a turn in progress.
func Unsettled(s *sessionv1.Session) bool {
	switch s.Status {
	case sessionv1.SessionStatus_SESSION_STATUS_QUEUED,
		sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
		sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
		sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT,
		sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
		return true
	case sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED:
		return false
	}
	f := s.GetProgress().GetLastFailure()
	return f != nil && f.Kind != sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED
}

// NewestFirst orders sessions as lists page them.
func NewestFirst(a, b *sessionv1.Session) int {
	if c := b.UpdatedAt.AsTime().Compare(a.UpdatedAt.AsTime()); c != 0 {
		return c
	}
	return strings.Compare(b.SessionId, a.SessionId)
}

// CursorAt names s as where a page ends.
func CursorAt(s *sessionv1.Session) *sessionv1.SessionCursor {
	return &sessionv1.SessionCursor{UpdatedAt: s.UpdatedAt, SessionId: s.SessionId}
}

// Past says whether s comes after the cursor c (always, without one).
func Past(s *sessionv1.Session, c *sessionv1.SessionCursor) bool {
	return c == nil || NewestFirst(&sessionv1.Session{UpdatedAt: c.UpdatedAt, SessionId: c.SessionId}, s) < 0
}

func (n *Machine) ListSessionsPage(ctx context.Context, p *Principal, req *sessionv1.ListSessionsRequest) ([]*sessionv1.Session, *sessionv1.SessionCursor, error) {
	if n.sessions == nil {
		return nil, nil, nil
	}
	incArch := req.IncludeArchived || req.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
	listRes, err := n.sessions.ListSessions(ctx, toKernelPrincipal(p), &sessionv1.ListSessionsRequest{
		ProjectId:       req.ProjectId,
		IncludeArchived: incArch,
	})
	if err != nil {
		return nil, nil, err
	}
	all := listRes.GetSessions()
	var filtered []*sessionv1.Session
	for _, s := range all {
		if req.Status != 0 && s.Status != req.Status {
			continue
		}
		if req.Since != nil && s.UpdatedAt.AsTime().Before(req.Since.AsTime()) {
			continue
		}
		filtered = append(filtered, s)
	}
	slices.SortFunc(filtered, NewestFirst)

	var next *sessionv1.SessionCursor
	out := filtered
	if req.Limit > 0 {
		limit := int(req.Limit)
		var page, unsettled []*sessionv1.Session
		for _, s := range filtered {
			if !Past(s, req.Before) {
				continue
			}
			if len(page) < limit {
				page = append(page, s)
				continue
			}
			if req.Before == nil && Unsettled(s) {
				unsettled = append(unsettled, s)
			}
		}
		if len(page) == limit {
			// Check whether anything past cursor was cut.
			pastCount := 0
			for _, s := range filtered {
				if Past(s, req.Before) {
					pastCount++
				}
			}
			if pastCount > limit {
				next = CursorAt(page[limit-1])
			}
		}
		out = append(page, unsettled...)
	}
	if req.View == sessionv1.SessionView_SESSION_VIEW_BASIC {
		out = BasicSessions(out)
	}
	return out, next, nil
}

func (n *Machine) GetSessionFor(ctx context.Context, p *Principal, id string) (*sessionv1.Session, error) {
	if n.sessions == nil {
		return nil, cerr(connect.CodeNotFound, "session not found")
	}
	proj, err := n.sessions.GetSession(ctx, toKernelPrincipal(p), id)
	if err != nil {
		return nil, err
	}
	return proj.GetSession(), nil
}

func (n *Machine) Running() int {
	if n.sessions == nil {
		return 0
	}
	all, err := n.sessions.ListAllSessions(false)
	if err != nil {
		return 0
	}
	r := 0
	for _, s := range all {
		switch s.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
			sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			r++
		}
	}
	return r
}

func (n *Machine) Counts() map[string]uint32 {
	c := map[string]uint32{}
	if n.sessions == nil {
		return c
	}
	all, err := n.sessions.ListAllSessions(false)
	if err != nil {
		return c
	}
	for _, s := range all {
		switch s.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED:
			c["queued"]++
		case sessionv1.SessionStatus_SESSION_STATUS_PREPARING:
			c["preparing"]++
		case sessionv1.SessionStatus_SESSION_STATUS_RUNNING:
			c["running"]++
		case sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			c["waiting_input"]++
		case sessionv1.SessionStatus_SESSION_STATUS_IDLE:
			c["idle"]++
		case sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
			c["crashed"]++
		}
	}
	return c
}

func (n *Machine) ActiveByProject() map[string]uint32 {
	out := map[string]uint32{}
	if n.sessions == nil {
		return out
	}
	all, err := n.sessions.ListAllSessions(false)
	if err != nil {
		return out
	}
	for _, s := range all {
		switch s.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED,
			sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
			sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			out[s.ProjectId]++
		}
	}
	return out
}

func (n *Machine) canReadDeleted(p *Principal, projectID, createdBy string) bool {
	if p == nil {
		return false
	}
	if p.Kind == PrincipalOwner {
		return true
	}
	kp := toKernelPrincipal(p)
	if kp == nil {
		return false
	}
	dummy := &sessionv1.Session{
		ProjectId: projectID,
		CreatedBy: createdBy,
		Actor:     &sessionv1.Actor{Id: createdBy},
	}
	// Owner & Her see all; External Grant checks project + TaskVisibility.
	if kp.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER {
		return true
	}
	if p.Grant == nil {
		return false
	}
	if dummy.ProjectId == kernel.ScratchProjectID {
		if !slices.Contains(p.Grant.ProjectIds, kernel.ScratchProjectID) {
			return false
		}
	} else if !slices.Contains(p.Grant.ProjectIds, "*") && !slices.Contains(p.Grant.ProjectIds, dummy.ProjectId) {
		return false
	}
	if p.Grant.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		return true
	}
	return createdBy == p.ClientID
}

func (n *Machine) deletedSince(p *Principal, t time.Time) []string {
	if n.sessions == nil {
		return nil
	}
	rows, err := n.sessions.Store().DeletedSessionRecordsSince(t)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range rows {
		if n.canReadDeleted(p, r.ProjectID, r.CreatedBy) {
			out = append(out, r.SessionID)
		}
	}
	return out
}

func (n *Machine) deletedFor(p *Principal, id string) bool {
	if n.sessions == nil {
		return false
	}
	if _, err := n.sessions.Store().Session(id); err == nil {
		return false
	}
	r, err := n.sessions.Store().DeletedSession(id)
	if err != nil || r == nil {
		return false
	}
	return n.canReadDeleted(p, r.ProjectID, r.CreatedBy)
}

func (n *Machine) Janitor(ctx context.Context) {
	// Mark startup so autoRelease waits autoReleaseEvery before first background sweep.
	n.wsCache.autoDue(time.Now(), n.autoReleaseEvery)
	n.wsCache.mu.Lock()
	n.wsCache.autoRunning = false
	n.wsCache.mu.Unlock()

	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if n.wsCache.autoDue(now, n.autoReleaseEvery) {
				go n.autoRelease(ctx, now)
			}
		}
	}
}
