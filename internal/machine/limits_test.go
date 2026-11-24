package machine

import (
	"testing"
	"time"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

// What an agent says of its rate limits is in the machine's status, for its
// owner only; said again unchanged it is kept no more than once a minute
// (so a watch is not told it again for nothing), changed at once; and the
// machine still has it after a restart.
func TestAgentLimits(t *testing.T) {
	dir := t.TempDir()
	open := func() *Machine {
		t.Helper()
		n, err := Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
		if err != nil {
			t.Fatal(err)
		}
		n.mu.Lock()
		n.infos = map[string]provider.Info{"codex": {Available: true}, "claude": {Available: true}}
		n.mu.Unlock()
		return n
	}
	limitsOf := func(st *machinev1.MachineStatus, agent string) *machinev1.AgentLimits {
		for _, a := range st.Agents {
			if a.Name == agent {
				return a.Limits
			}
		}
		t.Fatalf("%s is not in the status", agent)
		return nil
	}
	n := open()
	if l := limitsOf(n.Status(), "codex"); l != nil {
		t.Fatalf("limits before any were said: %v", l)
	}
	resets := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	week := provider.LimitWindow{Minutes: 10080, UsedPercent: 61, ResetsAt: resets.Add(72 * time.Hour)}
	n.noteLimits("codex", provider.Limits{Plan: "pro", Windows: []provider.LimitWindow{{Minutes: 300, UsedPercent: 82, ResetsAt: resets}, week}})
	got := limitsOf(n.statusFor(&Principal{Kind: PrincipalOwner}), "codex")
	if got == nil || got.Plan != "pro" || len(got.Windows) != 2 || got.Windows[0].Minutes != 300 || got.Windows[0].UsedPercent != 82 ||
		!got.Windows[0].ResetsAt.AsTime().Equal(resets) || got.Windows[1].UsedPercent != 61 || time.Since(got.ObservedAt.AsTime()) > time.Minute {
		t.Fatalf("the owner's: %v", got)
	}
	if l := limitsOf(n.Status(), "claude"); l != nil {
		t.Fatalf("another agent's: %v", l)
	}
	if l := limitsOf(n.statusFor(&Principal{Kind: PrincipalClient}), "codex"); l != nil {
		t.Fatalf("an agent with a grant reads the owner's account: %v", l)
	}

	// Unchanged within the minute: kept as it was. Changed: at once.
	first := got.ObservedAt.AsTime()
	n.noteLimits("codex", provider.Limits{Plan: "pro", Windows: []provider.LimitWindow{{Minutes: 300, UsedPercent: 82, ResetsAt: resets}, week}})
	if at := limitsOf(n.Status(), "codex").ObservedAt.AsTime(); !at.Equal(first) {
		t.Fatalf("said again unchanged: kept at %v, was %v", at, first)
	}
	n.noteLimits("codex", provider.Limits{Plan: "pro", Windows: []provider.LimitWindow{{Minutes: 300, UsedPercent: 84, ResetsAt: resets}, week}})
	if l := limitsOf(n.Status(), "codex"); l.Windows[0].UsedPercent != 84 {
		t.Fatalf("a window moved: %v", l)
	}
	// A minute on, unchanged is kept again (when it was said moves).
	n.mu.Lock()
	n.limits["codex"].ObservedAt.Seconds -= 61
	old := n.limits["codex"].ObservedAt.AsTime()
	n.mu.Unlock()
	n.noteLimits("codex", provider.Limits{Plan: "pro", Windows: []provider.LimitWindow{{Minutes: 300, UsedPercent: 84, ResetsAt: resets}, week}})
	if at := limitsOf(n.Status(), "codex").ObservedAt.AsTime(); !at.After(old) {
		t.Fatalf("a minute on: kept at %v", at)
	}
	n.Close()

	// After a restart: what it last kept.
	n = open()
	defer n.Close()
	if l := limitsOf(n.Status(), "codex"); l == nil || len(l.Windows) != 2 || l.Windows[0].UsedPercent != 84 || l.Plan != "pro" {
		t.Fatalf("after a restart: %v", l)
	}
}
