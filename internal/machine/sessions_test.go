package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/fake"
)

// recorder is the fake provider, remembering every start.
type recorder struct {
	fake.Provider
	mu     sync.Mutex
	starts []provider.StartSpec
}

func (r *recorder) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	r.mu.Lock()
	r.starts = append(r.starts, spec)
	r.mu.Unlock()
	return r.Provider.Start(ctx, spec)
}

func (r *recorder) spec(i int) provider.StartSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 {
		i += len(r.starts)
	}
	return r.starts[i]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

type harness struct {
	t    *testing.T
	n    *Machine
	s    *Sessions
	rec  *recorder
	p    *Principal
	stop context.CancelFunc
	// mark is the last stored event when a message was sent: wait looks
	// only at statuses after it (the previous turn's idle is stale).
	mark map[string]int
}

func newHarness(t *testing.T, lim Limits) *harness {
	return newHarnessWith(t, lim, nil)
}

// newHarnessWith also installs extra providers.
func newHarnessWith(t *testing.T, lim Limits, extra map[string]provider.Provider) *harness {
	rec := &recorder{}
	if lim.IdleProcessTTL == 0 {
		lim.IdleProcessTTL = time.Hour
	}
	providers := map[string]provider.Provider{"fake": rec}
	for k, v := range extra {
		providers[k] = v
	}
	n, err := Open(Config{DataDir: t.TempDir(), Providers: providers, Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.Discover(ctx)
	n.sessions.janitorEvery = 20 * time.Millisecond
	go n.sessions.Janitor(ctx)
	if err := n.st.PutProject(Project{ID: "demo", Path: testRepo(t), DefaultBaseRef: "HEAD"}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, n: n, s: n.sessions, rec: rec, stop: cancel, mark: map[string]int{},
		p: &Principal{Kind: PrincipalClient, ClientID: "c_test", Grant: &identityv1.Grant{GrantId: "g_test",
			MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"*"}}}}
	t.Cleanup(func() {
		cancel()
		n.Close()
	})
	return h
}

func (h *harness) start(prompt string) string {
	h.t.Helper()
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: prompt})
	if err != nil {
		h.t.Fatal(err)
	}
	return sess.SessionId
}

func (h *harness) send(id, text string) {
	h.t.Helper()
	h.mark[id] = len(h.events(id))
	if _, err := h.s.Send(context.Background(), h.p, id, text, nil); err != nil {
		h.t.Fatal(err)
	}
}

// wait returns the session once it reaches status.
func (h *harness) wait(id string, status sessionv1.SessionStatus) *sessionv1.Session {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		sess, err := h.s.Get(h.p, id)
		if err != nil {
			h.t.Fatal(err)
		}
		if sess.Status == status && h.lastStatus(id) == status { // and its events are stored
			return sess
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("session is %v, waiting for %v; events:\n%s", sess.Status, status, h.dump(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lastStatus is the last stored status after the mark.
func (h *harness) lastStatus(id string) sessionv1.SessionStatus {
	var st sessionv1.SessionStatus
	for _, e := range h.events(id) {
		if x := e.GetStatus(); x != nil && int(e.Seq) > h.mark[id] {
			st = x.Status
		}
	}
	return st
}

func (h *harness) events(id string) []*sessionv1.SessionEvent {
	var out []*sessionv1.SessionEvent
	err := h.s.Stream(context.Background(), h.p, &sessionv1.StreamEventsRequest{SessionId: id}, func(e *sessionv1.SessionEvent) error {
		out = append(out, e)
		return nil
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) dump(id string) string {
	var b strings.Builder
	for _, e := range h.events(id) {
		b.WriteString(e.String() + "\n")
	}
	return b.String()
}

// lastTurn is the last TurnCompleted event.
func (h *harness) lastTurn(id string) *sessionv1.TurnCompleted {
	h.t.Helper()
	var tc *sessionv1.TurnCompleted
	for _, e := range h.events(id) {
		if t := e.GetTurnCompleted(); t != nil {
			tc = t
		}
	}
	if tc == nil {
		h.t.Fatalf("no completed turn:\n%s", h.dump(id))
	}
	return tc
}

// stopProcess stops the session's agent process, as the idle TTL does.
func (h *harness) stopProcess(id string) {
	h.s.mu.Lock()
	l := h.s.live[id]
	h.s.mu.Unlock()
	l.mu.Lock()
	ps := l.sess
	l.sess = nil
	l.mu.Unlock()
	ps.Close()
}

const (
	idle    = sessionv1.SessionStatus_SESSION_STATUS_IDLE
	crashed = sessionv1.SessionStatus_SESSION_STATUS_CRASHED
)

// A missing checkout is restored from the session branch, not its original
// base. Cover both checkout implementations and stale git registration.
func TestResumeMissingWorktree(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("fallback=%v/stale=%v", fallback, stale), func(t *testing.T) {
				h := newHarness(t, DefaultLimits())
				proj, _ := h.n.st.Project("demo")
				if fallback {
					gitOut(t, proj.Path, "config", "extensions.worktreeConfig", "true")
				}
				id := h.start("write previous.txt keep this work\nsay first")
				ref := h.wait(id, idle).ProviderSessionRef
				h.stopProcess(id)
				row, _ := h.n.st.Session(id)
				branchHead := strings.TrimSpace(gitOut(t, proj.Path, "rev-parse", row.Branch))
				if stale {
					if err := os.RemoveAll(h.s.ws.Path(id)); err != nil {
						t.Fatal(err)
					}
				} else if err := h.s.ws.Remove(context.Background(), proj.Path, id); err != nil {
					t.Fatal(err)
				}
				h.send(id, "resumed")
				h.wait(id, idle)
				if got := h.lastTurn(id).Result; got != "resumed: "+ref {
					t.Fatalf("conversation not resumed: %q", got)
				}
				if b, err := os.ReadFile(filepath.Join(h.s.ws.Path(id), "previous.txt")); err != nil || !strings.Contains(string(b), "keep this work") {
					t.Fatalf("previous turn lost: %q %v", b, err)
				}
				after, _ := h.n.st.Session(id)
				if after.BaseCommit != row.BaseCommit || after.Branch != row.Branch || after.Progress.TurnCount != 2 {
					t.Fatalf("session metadata changed: before %+v, after %+v", row, after)
				}
				if got := strings.TrimSpace(gitOut(t, proj.Path, "rev-parse", row.Branch)); got != branchHead {
					t.Fatalf("session branch moved from %s to %s", branchHead, got)
				}
			})
		}
	}
}

// A crash in the middle of a turn keeps the session; the next message
// resumes the same provider session.
func TestCrashMidTurnIsResumable(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one")
	ref := h.wait(id, idle).ProviderSessionRef
	h.send(id, "write half.txt partial work\ncrash")
	sess := h.wait(id, crashed)
	tc := h.lastTurn(id)
	if !tc.IsError || tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED || !tc.Failure.Retryable || tc.Turn != 2 {
		t.Fatalf("turn: %v", tc)
	}
	if tc.DiffStat.GetFilesChanged() != 1 || sess.Progress.LastFailure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED {
		t.Fatalf("the interrupted turn's work must be snapshotted: %v / %v", tc.DiffStat, sess.Progress)
	}
	h.send(id, "resumed")
	sess = h.wait(id, idle)
	if got := h.lastTurn(id).Result; got != "resumed: "+ref {
		t.Fatalf("resumed with %q, want ref %s", got, ref)
	}
	if sess.Progress.LastFailure != nil {
		t.Fatalf("a successful turn clears the failure: %v", sess.Progress.LastFailure)
	}
}

func TestStalledTurnIsStopped(t *testing.T) {
	h := newHarness(t, Limits{FirstOutputTimeout: time.Hour, IdleTimeout: 300 * time.Millisecond, ToolTimeout: time.Hour})
	id := h.start("say working\nhang")
	h.wait(id, crashed)
	tc := h.lastTurn(id)
	if tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_STALLED || !strings.Contains(tc.Failure.Message, "no output from the agent") {
		t.Fatalf("turn: %v", tc)
	}
	h.send(id, "say back")
	h.wait(id, idle)
	if h.lastTurn(id).Result != "back" {
		t.Fatal("session did not continue after a stall")
	}
}

// An agent that keeps retrying (Codex waiting for the network) makes no
// progress: it is stopped after the idle window, reported as what it was
// stuck on.
func TestRetryingAgentIsStoppedAsItsError(t *testing.T) {
	h := newHarness(t, Limits{FirstOutputTimeout: time.Hour, IdleTimeout: 400 * time.Millisecond, ToolTimeout: time.Hour})
	id := h.start("say working\nretry-forever Reconnecting... waiting for network: Connection failed: error sending request")
	h.wait(id, crashed)
	tc := h.lastTurn(id)
	if tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_NETWORK || !tc.Failure.Retryable ||
		!strings.Contains(tc.Failure.Message, "no output from the agent") || !strings.Contains(tc.Failure.Message, "waiting for network") {
		t.Fatalf("turn: %v", tc)
	}
}

// A silent tool gets the tool window, not the idle window; output that is
// not an event (thinking) also counts as progress.
func TestLivenessWindows(t *testing.T) {
	h := newHarness(t, Limits{FirstOutputTimeout: time.Hour, IdleTimeout: 300 * time.Millisecond, ToolTimeout: 0})
	id := h.start("tool-sleep 1s\nchatter 1s\nsay done")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.IsError || tc.Result != "done" {
		t.Fatalf("turn: %v", tc)
	}

	h = newHarness(t, Limits{FirstOutputTimeout: time.Hour, IdleTimeout: time.Hour, ToolTimeout: 300 * time.Millisecond})
	id = h.start("tool-sleep 30s")
	h.wait(id, crashed)
	if tc := h.lastTurn(id); tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_STALLED || !strings.Contains(tc.Failure.Message, "while running Bash: sleep 30s") {
		t.Fatalf("turn: %v", tc)
	}

	h = newHarness(t, Limits{FirstOutputTimeout: 300 * time.Millisecond, IdleTimeout: time.Hour, ToolTimeout: time.Hour})
	id = h.start("hang")
	h.wait(id, crashed)
	if tc := h.lastTurn(id); !strings.Contains(tc.Failure.GetMessage(), "after starting") {
		t.Fatalf("turn: %v", tc)
	}

	h = newHarness(t, Limits{FirstOutputTimeout: time.Hour, IdleTimeout: time.Hour, ToolTimeout: time.Hour, TurnTimeout: 300 * time.Millisecond})
	id = h.start("chatter 30s")
	h.wait(id, crashed)
	if tc := h.lastTurn(id); tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_TIMEOUT {
		t.Fatalf("turn: %v", tc)
	}
}

// Claude Code reports a missing transcript inside the first turn of the
// resumed process: the turn is re-run once in a fresh provider session,
// told that the earlier conversation is lost.
func TestRefusedResumeInTurnRestartsFresh(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one")
	ref := h.wait(id, idle).ProviderSessionRef
	h.stopProcess(id)
	h.send(id, "refuse-resume\nnotice\nresumed")
	sess := h.wait(id, idle)
	tc := h.lastTurn(id)
	if tc.IsError || tc.Turn != 2 || tc.Result != "resumed: " {
		t.Fatalf("turn: %v\n%s", tc, h.dump(id))
	}
	if h.rec.count() != 3 || h.rec.spec(1).ResumeRef != ref || h.rec.spec(2).ResumeRef != "" ||
		strings.Count(h.rec.spec(2).Prompt, "[Hermesh] The earlier conversation") != 1 {
		t.Fatalf("starts: %+v", h.rec.starts)
	}
	if sess.ProviderSessionRef == "" || sess.ProviderSessionRef == ref {
		t.Fatalf("ref after the fresh start: %q (was %q)", sess.ProviderSessionRef, ref)
	}
	var starts, notes int
	for _, e := range h.events(id) {
		if e.GetTurnStarted() != nil {
			starts++
		}
		if e.GetError().GetFailure().GetKind() == sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED {
			notes++
		}
	}
	if starts != 2 || notes != 1 {
		t.Fatalf("turn starts %d, refused-resume notes %d:\n%s", starts, notes, h.dump(id))
	}
}

// Codex / ACP refuse the resume when the process starts.
func TestRefusedResumeAtStartRestartsFresh(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one")
	h.wait(id, idle)
	h.stopProcess(id)
	h.send(id, "resume-start-reject\nnotice")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Result != "notice: yes" {
		t.Fatalf("turn: %v", tc)
	}
}

// Any other resume failure (login, network) keeps the provider session for
// a later message instead of throwing the conversation away.
func TestResumeErrorKeepsSession(t *testing.T) {
	p := &startTrouble{}
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"flaky": p})
	id := h.startAgent("flaky", "say one")
	ref := h.wait(id, idle).ProviderSessionRef
	h.stopProcess(id)
	p.set(1, errors.New("API Error: 401 Invalid authentication credentials"), false)
	h.send(id, "say two")
	sess := h.wait(id, crashed)
	if f := sess.Progress.LastFailure; f.GetKind() != sessionv1.FailureKind_FAILURE_KIND_AUTH || sess.ProviderSessionRef != ref {
		t.Fatalf("session: %v", sess)
	}
	h.send(id, "resumed")
	h.wait(id, idle)
	if got := h.lastTurn(id).Result; got != "resumed: "+ref || p.spec(-1).Prompt != "say two\n\nresumed" {
		t.Fatalf("got %q, from %q", got, p.spec(-1).Prompt)
	}
}

// startTrouble is the fake agent, except that its next fails starts fail
// with err, and that while hang is set a start waits until it is stopped.
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

func (p *startTrouble) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
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
	return p.recorder.Start(ctx, spec)
}

func (h *harness) startAgent(agent, prompt string) string {
	h.t.Helper()
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: agent, Prompt: prompt})
	if err != nil {
		h.t.Fatal(err)
	}
	return sess.SessionId
}

// saidAll is what the agent said in the session, in order.
func (h *harness) saidAll(id string) []string {
	var out []string
	for _, e := range h.events(id) {
		if m := e.GetAgentMessage(); m != nil {
			out = append(out, m.Text)
		}
	}
	return out
}

// An agent that could not start never got the prompt: the next message
// starts it with that prompt too.
func TestStartFailureKeepsPrompt(t *testing.T) {
	p := &startTrouble{}
	p.set(1, errors.New("codex initialize: codex exited: exit status 1"), false)
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"flaky": p})
	id := h.startAgent("flaky", "say one")
	if f := h.wait(id, crashed).Progress.LastFailure; f.GetKind() != sessionv1.FailureKind_FAILURE_KIND_START_FAILED {
		t.Fatalf("failure: %v", f)
	}
	h.send(id, "say two")
	h.wait(id, idle)
	if got := p.spec(-1).Prompt; got != "say one\n\nsay two" {
		t.Fatalf("the agent got %q", got)
	}
	if got := strings.Join(h.saidAll(id), ","); got != "one,two" {
		t.Fatalf("said %q:\n%s", got, h.dump(id))
	}
}

// A start that takes too long is stopped by the watchdog, as stalled, and
// keeps the prompt for the next message.
func TestStalledStartStops(t *testing.T) {
	lim := DefaultLimits()
	lim.StartTimeout = 300 * time.Millisecond
	p := &startTrouble{}
	p.set(0, nil, true)
	h := newHarnessWith(t, lim, map[string]provider.Provider{"flaky": p})
	id := h.startAgent("flaky", "say one")
	f := h.wait(id, crashed).Progress.LastFailure
	if f.GetKind() != sessionv1.FailureKind_FAILURE_KIND_STALLED || !strings.Contains(f.GetMessage(), "did not finish starting in 300ms") {
		t.Fatalf("failure: %v", f)
	}
	p.set(0, nil, false)
	h.send(id, "say two")
	h.wait(id, idle)
	if got := p.spec(-1).Prompt; got != "say one\n\nsay two" {
		t.Fatalf("the agent got %q", got)
	}
}

// Interrupting a start that is taking long stops it at once; what it was
// to run never runs.
func TestInterruptStopsStart(t *testing.T) {
	p := &startTrouble{}
	p.set(0, nil, true)
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"flaky": p})
	id := h.startAgent("flaky", "say one")
	h.wait(id, sessionv1.SessionStatus_SESSION_STATUS_PREPARING)
	h.mark[id] = len(h.events(id))
	at := time.Now()
	if _, err := h.s.Interrupt(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	if f := h.wait(id, idle).Progress.LastFailure; f.GetKind() != sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED || time.Since(at) > 5*time.Second {
		t.Fatalf("failure %v after %s", f, time.Since(at))
	}
	p.set(0, nil, false)
	h.send(id, "say two")
	h.wait(id, idle)
	if got := p.spec(-1).Prompt; got != "say two" {
		t.Fatalf("the agent got %q", got)
	}
}

// After a context overflow the next message continues in a fresh provider
// session, told that the earlier conversation is lost.
func TestContextOverflowContinuesFresh(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one")
	h.wait(id, idle)
	h.send(id, "overflow")
	sess := h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW || sess.ProviderSessionRef != "" {
		t.Fatalf("turn: %v, ref %q", tc, sess.ProviderSessionRef)
	}
	h.send(id, "notice\nresumed")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Result != "resumed: " || !strings.Contains(h.dump(id), "notice: yes") {
		t.Fatalf("turn: %v", tc)
	}
}

// A first turn whose agent cannot start keeps the session: it is crashed,
// with the reason, and the next message tries again.
func TestFirstTurnStartFailureKeepsSession(t *testing.T) {
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"broken": brokenProvider{}})
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "broken", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(sess.SessionId, crashed)
	if got.Progress.LastFailure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_AUTH {
		t.Fatalf("session: %v", got)
	}
	h.send(sess.SessionId, "x again")
	if got := h.wait(sess.SessionId, crashed); got.Progress.LastFailure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_AUTH {
		t.Fatalf("retried: %v", got)
	}
}

// A session never ends by itself: its idle agent process goes, the session
// stays idle, and the next message resumes the same conversation.
func TestIdleSessionStaysAndResumes(t *testing.T) {
	lim := DefaultLimits()
	lim.IdleProcessTTL = 50 * time.Millisecond
	h := newHarness(t, lim)
	id := h.start("say one")
	ref := h.wait(id, idle).ProviderSessionRef
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.s.mu.Lock()
		l := h.s.live[id]
		h.s.mu.Unlock()
		l.mu.Lock()
		gone := l.sess == nil
		l.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle agent process was not reclaimed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if st := h.lastStatus(id); st != idle {
		t.Fatalf("an idle session changed to %v", st)
	}
	h.send(id, "resumed")
	h.wait(id, idle)
	if got := h.lastTurn(id).Result; got != "resumed: "+ref {
		t.Fatalf("resumed with %q, want ref %s", got, ref)
	}
}

type brokenProvider struct{ fake.Provider }

func (brokenProvider) Start(context.Context, provider.StartSpec) (provider.Session, error) {
	return nil, provider.Fail(sessionv1.FailureKind_FAILURE_KIND_AUTH, "Not logged in · Please run /login")
}

func TestUpdatedAtTracksLiveSessions(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	before := time.Now().Add(-time.Second)
	id := h.start("say one")
	sess := h.wait(id, idle)
	if u := sess.UpdatedAt.AsTime(); u.Before(before) || u.Before(sess.CreatedAt.AsTime()) {
		t.Fatalf("updated_at %v (created %v)", u, sess.CreatedAt.AsTime())
	}
	list := func(since time.Time) int {
		got, _, err := h.s.List(h.p, &sessionv1.ListSessionsRequest{Since: timestamppb.New(since)})
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if n := list(before); n != 1 {
		t.Fatalf("a session updated just now is not listed since %v: %d", before, n)
	}
	if n := list(time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("listed a session as updated in the future: %d", n)
	}
}

func TestActiveSessionsCountTurnsInProgress(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("sleep 2s\nsay done")
	deadline := time.Now().Add(5 * time.Second)
	for h.s.ActiveByProject()["demo"] != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("a running turn is not counted: %v", h.s.ActiveByProject())
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.wait(id, idle)
	if n := h.s.ActiveByProject()["demo"]; n != 0 {
		t.Fatalf("an idle session is counted: %d", n)
	}
}

// When Archive returns, the session is archived for everyone: retired from
// the live table and stored, so an unarchive right after it works.
func TestArchiveIsDoneWhenItReturns(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("write previous.txt preserved\nsay one")
	h.wait(id, idle)
	commit := h.lastTurn(id).Commit
	if _, err := h.s.Archive(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	_, live := h.s.live[id]
	h.s.mu.Unlock()
	if r, _ := h.s.st.Session(id); live || r.Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		t.Fatalf("after Archive: live %v, stored %v", live, r.Status)
	}
	if _, err := h.s.Unarchive(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	h.send(id, "say two")
	h.wait(id, idle)
	if got := h.lastTurn(id); got.Commit != commit || got.DiffStat.GetFilesChanged() != 0 {
		t.Fatalf("a reply after unarchive lost the previous snapshot: %v", got)
	}
	diff, err := h.s.Diff(context.Background(), h.p, &sessionv1.GetDiffRequest{SessionId: id})
	if err != nil || !strings.Contains(diff.GetDiff(), "previous.txt") {
		t.Fatalf("cumulative diff after unarchive: %v %v", diff, err)
	}
}

// Archiving removes the agent's temporary directory and keeps the
// attachments; the next start after unarchiving makes it again.
func TestArchiveRemovesTempDir(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one")
	h.wait(id, idle)
	tmp := filepath.Join(h.s.dataDir, "sessions", id, "tmp")
	if err := os.WriteFile(filepath.Join(tmp, "scratch"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Archive(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temp dir after archive: %v", err)
	}
	if _, err := os.Stat(h.s.attachmentsDir(id)); err != nil {
		t.Fatalf("attachments after archive: %v", err)
	}
	if _, err := h.s.Unarchive(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	h.send(id, "say two")
	h.wait(id, idle)
	if st, err := os.Stat(tmp); err != nil || !st.IsDir() {
		t.Fatalf("temp dir after unarchive and a turn: %v", err)
	}
}

// ListEvents pages by seq: the latest by default, after or before a cursor,
// oldest first, and says whether there is more on either side.
func TestListEventsPages(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one\nsay two\nsay three")
	h.wait(id, idle)
	all := h.events(id)
	n := uint64(len(all))
	if n < 8 || all[n-1].Seq != n {
		t.Fatalf("events: %d, last seq %d", n, all[n-1].Seq)
	}
	page := func(after, before uint64, limit uint32) (seqs []uint64, more [2]bool) {
		t.Helper()
		res, err := h.s.ListEvents(h.p, &sessionv1.ListEventsRequest{SessionId: id, AfterSeq: after, BeforeSeq: before, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range res.Events {
			seqs = append(seqs, e.Seq)
		}
		return seqs, [2]bool{res.HasMoreBefore, res.HasMoreAfter}
	}
	check := func(what string, got []uint64, more [2]bool, want []uint64, wantMore [2]bool) {
		t.Helper()
		if !slices.Equal(got, want) || more != wantMore {
			t.Fatalf("%s: %v %v, want %v %v", what, got, more, want, wantMore)
		}
	}
	got, more := page(0, 0, 3)
	check("latest 3", got, more, []uint64{n - 2, n - 1, n}, [2]bool{true, false})
	got, more = page(2, 0, 3)
	check("3 after 2", got, more, []uint64{3, 4, 5}, [2]bool{true, true})
	got, more = page(0, 4, 10)
	check("before 4", got, more, []uint64{1, 2, 3}, [2]bool{false, true})
	got, more = page(n, 0, 10)
	check("nothing new", got, more, nil, [2]bool{true, false})
	got, more = page(0, 0, 0)
	if uint64(len(got)) != min(n, 50) {
		t.Fatalf("default page: %d events", len(got))
	}
	if _, err := h.s.ListEvents(h.p, &sessionv1.ListEventsRequest{SessionId: id, AfterSeq: 1, BeforeSeq: 3}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("after and before: %v", err)
	}
	if _, err := h.s.ListEvents(h.p, &sessionv1.ListEventsRequest{SessionId: id, Limit: 1001}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("limit 1001: %v", err)
	}
}

// With no cap, the default, every session starts at once: none waits queued.
func TestNoSessionCapByDefault(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	var ids []string
	for range 6 {
		ids = append(ids, h.start("sleep 300ms\nsay done"))
	}
	for _, id := range ids {
		if s, _ := h.s.Get(h.p, id); s.Status == sessionv1.SessionStatus_SESSION_STATUS_QUEUED {
			t.Fatalf("session %s waits queued", id)
		}
	}
	for _, id := range ids {
		h.wait(id, idle)
	}
}

// slowSend hands messages to the agent and returns late, after the agent
// may have answered.
type slowSend struct{ fake.Provider }
type slowSendSession struct{ provider.Session }

func (p *slowSend) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	ps, err := p.Provider.Start(ctx, spec)
	return slowSendSession{ps}, err
}

func (s slowSendSession) Send(ctx context.Context, in provider.Input) error {
	err := s.Session.Send(ctx, in)
	time.Sleep(50 * time.Millisecond)
	return err
}

// A message to an idle session whose agent is still running starts a turn
// that ends idle, however fast the agent answers it.
func TestFastFollowUpsEndIdle(t *testing.T) {
	h := newHarnessWith(t, Limits{}, map[string]provider.Provider{"slow": &slowSend{}})
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "slow", Prompt: "say hi"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.SessionId
	h.wait(id, idle)
	for i := 0; i < 3; i++ {
		h.send(id, "say again")
		h.wait(id, idle)
	}
}

// Only the owner deletes a session: its record, events and worktree go
// (a turn in progress is interrupted first), its branch and its tokens
// stay, and watchers hear it is gone.
func TestDeleteSession(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	owner := &Principal{Kind: PrincipalOwner}
	done := h.start("say one")
	h.wait(done, idle)
	busy := h.start("sleep 30s")
	h.wait(busy, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)

	if err := h.s.Delete(ctx, h.p, done); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent deletes: %v", err)
	}
	today := time.Now().In(time.UTC).Format(time.DateOnly)
	before, err := h.s.Usage(owner, &sessionv1.GetUsageRequest{Since: today})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watched := make(chan *sessionv1.WatchSessionsResponse, 100)
	go h.s.Watch(wctx, owner, func(m *sessionv1.WatchSessionsResponse) error {
		watched <- m
		return nil
	})
	<-watched

	r, _ := h.s.st.Session(done)
	proj, _ := h.s.st.Project("demo")
	for _, id := range []string{done, busy} {
		if err := h.s.Delete(ctx, owner, id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
		if r, _ := h.s.st.Session(id); r != nil {
			t.Fatalf("%s is still stored", id)
		}
		if _, err := h.s.Get(owner, id); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("get %s after delete: %v", id, err)
		}
		if evs, _ := h.s.st.Events(id, 0, 100); len(evs) != 0 {
			t.Fatalf("%s keeps %d events", id, len(evs))
		}
		if h.s.ws.Exists(id) {
			t.Fatalf("%s keeps its worktree", id)
		}
	}
	if out, err := exec.Command("git", "-C", proj.Path, "rev-parse", "--verify", r.Branch).CombinedOutput(); err != nil {
		t.Fatalf("the branch %s went: %v %s", r.Branch, err, out)
	}
	if list, _, _ := h.s.List(owner, &sessionv1.ListSessionsRequest{}); len(list) != 0 {
		t.Fatalf("listed after delete: %v", sessionIDs(list))
	}
	after, err := h.s.Usage(owner, &sessionv1.GetUsageRequest{Since: today})
	if err != nil || len(after.Days) != 1 || after.Days[0].InputTokens != before.Days[0].InputTokens {
		t.Fatalf("usage after delete: %v (before %v) %v", after, before, err)
	}
	gone := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for !gone[done] || !gone[busy] {
		select {
		case m := <-watched:
			for _, id := range m.DeletedSessionIds {
				gone[id] = true
			}
		case <-deadline:
			t.Fatalf("watched deleted: %v", gone)
		}
	}
	if err := h.s.Delete(ctx, owner, done); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete again: %v", err)
	}
}

// Only the owner renames a session; renaming isn't activity, so it keeps
// its place in lists. An archived session can be renamed too.
func TestRenameSession(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	owner := &Principal{Kind: PrincipalOwner}
	id := h.start("say one")
	was := h.wait(id, idle)

	if _, err := h.s.Rename(ctx, h.p, id, "mine"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent renames: %v", err)
	}
	if _, err := h.s.Rename(ctx, owner, id, "  \n "); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty title: %v", err)
	}
	s, err := h.s.Rename(ctx, owner, id, "  修好登录页 ")
	if err != nil || s.Title != "修好登录页" {
		t.Fatalf("rename: %v %v", s, err)
	}
	if got, _ := h.s.Get(owner, id); got.Title != "修好登录页" || !got.UpdatedAt.AsTime().Equal(was.UpdatedAt.AsTime()) {
		t.Fatalf("after rename: %q updated %v (was %v)", got.Title, got.UpdatedAt.AsTime(), was.UpdatedAt.AsTime())
	}
	if _, err := h.s.Archive(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Rename(ctx, owner, id, "归档后改名"); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.s.st.Session(id); r.Title != "归档后改名" {
		t.Fatalf("stored title %q", r.Title)
	}
}
