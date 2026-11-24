package machine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
)

const (
	idle     = sessionv1.SessionStatus_SESSION_STATUS_IDLE
	running  = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
	waiting  = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
	crashed  = sessionv1.SessionStatus_SESSION_STATUS_CRASHED
	archived = sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
)

type recordedSpec struct {
	WorkDir   string
	Model     string
	Effort    string
	Speed     identityv1.AgentSpeed
	ResumeRef string
	Prompt    string
}

type recorder struct {
	*scripted.Driver
	mu     sync.Mutex
	starts []*recordedSpec
}

func newRecorder(name string) *recorder {
	return &recorder{Driver: scripted.NewNamed(name)}
}

func (r *recorder) ensureInit(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Driver == nil {
		if name == "" {
			name = "fake"
		}
		r.Driver = scripted.NewNamed(name)
	}
}

func (r *recorder) Kind() string {
	r.ensureInit("")
	return r.Driver.Kind()
}

func (r *recorder) Discover(ctx context.Context) rt.Info {
	r.ensureInit("")
	return r.Driver.Discover(ctx)
}

func (r *recorder) Capabilities(flavor string) *sessionv1.ProviderCapabilities {
	r.ensureInit("")
	return r.Driver.Capabilities(flavor)
}

func (r *recorder) Probe(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, workdir string) rt.ProbeResult {
	r.ensureInit("")
	return r.Driver.Probe(ctx, cfg, workdir)
}

func (r *recorder) StartSession(ctx context.Context, spec rt.StartSpec) (rt.Session, error) {
	r.ensureInit(spec.Instance.GetDriver())
	var speed identityv1.AgentSpeed
	effort := ""
	if opts := spec.Selection.GetOptions(); opts != nil {
		effort = opts["effort"].GetStringValue()
		switch opts["speed"].GetStringValue() {
		case "standard":
			speed = identityv1.AgentSpeed_AGENT_SPEED_STANDARD
		case "fast":
			speed = identityv1.AgentSpeed_AGENT_SPEED_FAST
		}
	}
	rec := &recordedSpec{
		WorkDir:   spec.Workdir,
		Model:     spec.Selection.GetModel(),
		Effort:    effort,
		Speed:     speed,
		ResumeRef: spec.ResumeCursor,
	}
	r.mu.Lock()
	r.starts = append(r.starts, rec)
	r.mu.Unlock()
	sess, err := r.Driver.StartSession(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &recordedSession{Session: sess, rec: rec, mu: &r.mu}, nil
}

func (r *recorder) spec(i int) recordedSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 {
		i += len(r.starts)
	}
	return *r.starts[i]
}

type recordedSession struct {
	rt.Session
	rec *recordedSpec
	mu  *sync.Mutex
}

func (s *recordedSession) StartTurn(ctx context.Context, in rt.TurnInput) error {
	s.mu.Lock()
	s.rec.Prompt = in.Prompt
	s.mu.Unlock()
	return s.Session.StartTurn(ctx, in)
}

type startTrouble struct {
	recorder
	tmu   sync.Mutex
	fails int
	err   error
	hang  bool
}

func (p *startTrouble) set(fails int, err error, hang bool) {
	p.tmu.Lock()
	defer p.tmu.Unlock()
	p.fails, p.err, p.hang = fails, err, hang
}

func (p *startTrouble) StartSession(ctx context.Context, spec rt.StartSpec) (rt.Session, error) {
	p.tmu.Lock()
	fail, err, hang := p.fails > 0, p.err, p.hang
	if fail {
		p.fails--
	}
	p.tmu.Unlock()
	switch {
	case fail:
		return nil, err
	case hang:
		<-ctx.Done()
		return nil, fmt.Errorf("codex thread/start: %w", ctx.Err())
	}
	return p.recorder.StartSession(ctx, spec)
}

type harness struct {
	t    *testing.T
	n    *Machine
	e    *kernel.Engine
	rec  *recorder
	p    *Principal
	mark map[string]uint64
}

func newHarness(t *testing.T, lim Limits) *harness {
	return newHarnessWith(t, lim, nil)
}

func newHarnessWith(t *testing.T, lim Limits, extra map[string]rt.Driver) *harness {
	t.Helper()
	rec := newRecorder("fake")
	if lim.IdleProcessTTL == 0 {
		lim.IdleProcessTTL = time.Hour
	}
	drivers := map[string]rt.Driver{
		"scripted": scripted.NewDriver(),
		"fake":     rec,
		"claude":   scripted.NewNamed("claude"),
		"codex":    scripted.NewNamed("codex"),
	}
	discoverers := map[string]AgentDiscoverer{
		"fake":   rec,
		"claude": scripted.NewNamed("claude"),
		"codex":  scripted.NewNamed("codex"),
	}
	for k, v := range extra {
		drivers[k] = v
		if d, ok := v.(AgentDiscoverer); ok {
			discoverers[k] = d
		}
	}
	n, err := Open(Config{
		DataDir:     t.TempDir(),
		Drivers:     drivers,
		Discoverers: discoverers,
		Limits:      lim,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-n.CheckAgents(true, 0)
	demoRepo := testRepo(t)
	if err := n.st.PutProject(Project{ID: "demo", Path: demoRepo, DefaultBaseRef: "HEAD", Workspace: identityv1.Workspace_WORKSPACE_WORKTREE}); err != nil {
		t.Fatal(err)
	}
	if err := n.sessions.SyncWorkspace("demo", demoRepo, "HEAD", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t:    t,
		n:    n,
		e:    n.sessions,
		rec:  rec,
		mark: map[string]uint64{},
		p: &Principal{
			Kind:     PrincipalClient,
			ClientID: "c_test",
			Grant: &identityv1.Grant{
				GrantId:    "g_test",
				ClientId:   "c_test",
				MachineIds: []string{"*"},
				ProjectIds: []string{"*"},
				Agents:     []string{"*"},
			},
		},
	}
	t.Cleanup(func() { _ = n.Close() })
	return h
}

func (h *harness) kp() *kernel.Principal { return toKernelPrincipal(h.p) }

func (h *harness) start(prompt string) string {
	return h.startAgent("fake", prompt)
}

func (h *harness) startAgent(agent, prompt string) string {
	h.t.Helper()
	res, err := h.e.StartSession(context.Background(), h.kp(), &sessionv1.StartSessionRequest{
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: agent},
		InitialPrompt:  prompt,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Session.SessionId
}

func (h *harness) send(id, text string) {
	h.t.Helper()
	evs, _ := h.e.Store().EventsAfter(id, 0)
	if len(evs) > 0 {
		h.mark[id] = evs[len(evs)-1].Seq
	}
	if _, err := h.e.DispatchMessage(context.Background(), h.kp(), &sessionv1.DispatchMessageRequest{
		SessionId: id,
		Text:      text,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) wait(id string, status sessionv1.SessionStatus) *sessionv1.Session {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		proj, err := h.e.GetSession(context.Background(), h.kp(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		sess := proj.GetSession()
		if sess.GetStatus() == status && h.sawStatusAfterMark(id, status) {
			return sess
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("session %s is %v (error=%q), waiting for %v", id, sess.GetStatus(), sess.GetError(), status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) sawStatusAfterMark(id string, status sessionv1.SessionStatus) bool {
	evs, err := h.e.Store().EventsAfter(id, h.mark[id])
	if err != nil {
		return false
	}
	var last sessionv1.SessionStatus
	for _, ev := range evs {
		if ev.GetSession() != nil {
			last = ev.GetSession().GetStatus()
		}
	}
	return last == status
}

func (h *harness) said(id, substr string) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		proj, err := h.e.GetSession(context.Background(), h.kp(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, it := range proj.GetItems() {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && strings.Contains(it.Text, substr) {
				return
			}
		}
		if strings.Contains(proj.GetSession().GetResult(), substr) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("session %s never said %q (result=%q)", id, substr, proj.GetSession().GetResult())
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (h *harness) stopProcess(id string) {
	h.t.Helper()
	sess, err := h.e.Store().Session(id)
	if err != nil {
		h.t.Fatal(err)
	}
	if sess.ActiveProviderSessionId != "" {
		h.e.Supervisor().Detach(sess.ActiveProviderSessionId, false)
	}
}

func (h *harness) worktreePath(id string) string {
	return h.n.expectedWorktreePath(id)
}

func (h *harness) worktreeExists(id string) bool {
	st, err := os.Stat(h.worktreePath(id))
	return err == nil && st.IsDir()
}
