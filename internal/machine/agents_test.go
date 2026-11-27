package machine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
)

// probe is a coding agent whose check counts how often it ran, and waits
// for release while held.
type probe struct {
	name      string
	installed atomic.Bool
	runs      atomic.Int32
	hold      sync.Mutex
}

func (p *probe) Discover(context.Context) rt.Info {
	p.hold.Lock()
	defer p.hold.Unlock()
	p.runs.Add(1)
	if !p.installed.Load() {
		return rt.Info{Name: p.name, Detail: p.name + " not found"}
	}
	return rt.Info{Name: p.name, Available: true, Version: "1.0.0", LoggedIn: true}
}

func agentsMachine(t *testing.T, ps ...*probe) *Machine {
	discoverers := map[string]AgentDiscoverer{}
	for _, p := range ps {
		discoverers[p.name] = p
	}
	n, err := Open(Config{DataDir: t.TempDir(), Discoverers: discoverers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func agent(st *machinev1.MachineStatus, name string) *machinev1.AgentStatus {
	for _, a := range st.Agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func TestAgentsCheckedOnlyWhenAsked(t *testing.T) {
	claude := &probe{name: "claude"}
	claude.installed.Store(true)
	n := agentsMachine(t, claude)

	// Nothing checks them on its own, nor does reading the status.
	time.Sleep(50 * time.Millisecond)
	n.Status()
	if got := claude.runs.Load(); got != 0 {
		t.Fatalf("checked %d times before anything asked", got)
	}

	// The app reading this Mac's state starts the first check, once.
	<-n.CheckAgents(false, AgentsOnce)
	for range 3 {
		n.Local().Info()
	}
	if got := claude.runs.Load(); got != 1 {
		t.Fatalf("checked %d times, want 1", got)
	}
	if a := agent(n.Status(), "claude"); a == nil || !a.Available || a.Checking {
		t.Fatalf("claude after the check: %+v", a)
	}

	// Asked again (the app showing its agents), it checks again.
	n.Local().CheckAgents()
	<-n.CheckAgents(false, AgentsOnce)
	if got := claude.runs.Load(); got != 2 {
		t.Fatalf("checked %d times after asking again, want 2", got)
	}

	// A fresh check stands for agents asking where they can work.
	<-n.CheckAgents(false, AgentsFresh)
	if got := claude.runs.Load(); got != 2 {
		t.Fatalf("a fresh check ran again: %d", got)
	}
}

func TestAgentsShowAsTheirChecksEnd(t *testing.T) {
	claude, qoder := &probe{name: "claude"}, &probe{name: "qoder"}
	claude.installed.Store(true)
	qoder.installed.Store(true)
	n := agentsMachine(t, claude, qoder)

	qoder.hold.Lock() // a slow CLI
	done := n.CheckAgents(false, AgentsOnce)
	deadline := time.Now().Add(5 * time.Second)
	for agent(n.Status(), "claude").GetChecking() {
		if time.Now().After(deadline) {
			t.Fatal("claude still checking")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if a := agent(n.Status(), "claude"); !a.Available {
		t.Fatalf("claude: %+v", a)
	}
	// Not checked yet: listed as checking, not as missing.
	if a := agent(n.Status(), "qoder"); a == nil || !a.Checking || a.Available {
		t.Fatalf("qoder while its check runs: %+v", a)
	}
	qoder.hold.Unlock()
	<-done
	if a := agent(n.Status(), "qoder"); a == nil || a.Checking || !a.Available {
		t.Fatalf("qoder after its check: %+v", a)
	}

	// Checked again, an agent keeps what it had until its check ends.
	qoder.hold.Lock()
	done = n.CheckAgents(true, 0)
	if a := agent(n.Status(), "qoder"); a == nil || !a.Checking || !a.Available || a.Version != "1.0.0" {
		t.Fatalf("qoder while checked again: %+v", a)
	}
	qoder.hold.Unlock()
	<-done
}

func TestAgentMissingIsCheckedAgainBeforeASession(t *testing.T) {
	codex := &probe{name: "codex"}
	n := agentsMachine(t, codex)
	<-n.CheckAgents(false, AgentsOnce)
	if n.providerInfo("codex").Available {
		t.Fatal("codex available before it is installed")
	}
	codex.installed.Store(true) // installed since the check
	if i := n.providerInfo("codex"); !i.Available {
		t.Fatalf("codex installed since the check: %+v", i)
	}
	runs := codex.runs.Load()
	n.providerInfo("codex") // known to be there: not checked again
	if codex.runs.Load() != runs {
		t.Fatal("an available agent was checked again")
	}
}

type limitsProbe struct {
	name      string
	installed atomic.Bool
	loggedIn  atomic.Bool
	runs      atomic.Int32
	limitRuns atomic.Int32
	usedPct   atomic.Uint32
	hold      sync.Mutex
}

func (p *limitsProbe) Discover(context.Context) rt.Info {
	p.hold.Lock()
	defer p.hold.Unlock()
	p.runs.Add(1)
	if !p.installed.Load() {
		return rt.Info{Name: p.name, Detail: p.name + " not found"}
	}
	return rt.Info{Name: p.name, Available: true, Version: "1.5.16", LoggedIn: p.loggedIn.Load()}
}

func (p *limitsProbe) ReadLimits(context.Context) *rt.Limits {
	p.limitRuns.Add(1)
	return &rt.Limits{
		Windows: []rt.LimitWindow{{
			Minutes:     300,
			UsedPercent: p.usedPct.Load(),
			ResetsAt:    time.Now().Add(4 * time.Hour).UTC().Truncate(time.Second),
			Group:       "Gemini Models",
		}},
	}
}

func TestCheckLimitsOnlyWhenAsked(t *testing.T) {
	agy := &limitsProbe{name: "antigravity"}
	agy.installed.Store(true)
	agy.usedPct.Store(17)
	n, err := Open(Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{"antigravity": agy}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })

	// Routine checks (startup, forced recheck, fresh check) never invoke ReadLimits.
	agy.loggedIn.Store(false)
	<-n.CheckAgents(false, AgentsOnce)
	<-n.CheckLimits()
	if got := agy.limitRuns.Load(); got != 0 {
		t.Fatalf("ReadLimits called %d times while signed out, want 0", got)
	}

	agy.loggedIn.Store(true)
	<-n.CheckAgents(true, 0)
	<-n.CheckAgents(false, AgentsFresh)
	if got := agy.limitRuns.Load(); got != 0 {
		t.Fatalf("ReadLimits called %d times during routine CheckAgents, want 0", got)
	}
	if a := agent(n.Status(), "antigravity"); a == nil || a.Limits != nil {
		t.Fatalf("limits populated before CheckLimits: %+v", a)
	}

	// Opening Limits calls ReadLimits and records the snapshot.
	<-n.CheckLimits()
	if got := agy.limitRuns.Load(); got != 1 {
		t.Fatalf("ReadLimits called %d times, want 1", got)
	}
	if a := agent(n.Status(), "antigravity"); a == nil || a.Limits == nil || len(a.Limits.Windows) != 1 || a.Limits.Windows[0].UsedPercent != 17 {
		t.Fatalf("limits after CheckLimits: %+v", a)
	}

	// Even if a routine CheckAgents is already in flight, CheckLimits still queries ReadLimits.
	agy.usedPct.Store(29)
	agy.hold.Lock()
	inFlight := n.CheckAgents(true, 0)
	limitsDone := n.CheckLimits()
	agy.hold.Unlock()
	<-inFlight
	<-limitsDone
	if got := agy.limitRuns.Load(); got != 2 {
		t.Fatalf("ReadLimits called %d times while CheckAgents in flight, want 2", got)
	}
	if a := agent(n.Status(), "antigravity"); a == nil || a.Limits == nil || a.Limits.Windows[0].UsedPercent != 29 {
		t.Fatalf("limits after concurrent CheckLimits: %+v", a)
	}
}
