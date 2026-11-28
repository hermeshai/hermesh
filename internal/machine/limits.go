package machine

import (
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
)

// Rate limits (docs/overview-usage.md §7): what each coding agent last said
// of its account's limits, in a session here or asked when the agents are
// checked (CheckAgents), kept in memory and in the
// store (one row an agent), and told with the machine's status, which a
// watch sends when it changed. The agents do not name the account, so it is
// one an agent: the latest.

// Machine.limits holds them by agent (under its mu); kLimits + an agent's
// name is where each is kept (AgentLimits).
const kLimits = "limits:"

// limitsEvery: limits said again unchanged are kept (and so told) no more
// often than this.
const limitsEvery = time.Minute

// loadLimits reads what the agents said before this start.
func (n *Machine) loadLimits() {
	n.limits = map[string]*machinev1.AgentLimits{}
	for _, name := range append(AgentNames(), "fake") {
		if name == "antigravity" {
			// Clear any snapshot previously fetched via `agy -p /usage --output-format json`,
			// which is disabled pending an official compliant quota query API.
			_ = n.st.Delete(kLimits + name)
			continue
		}
		if b, _ := n.st.Get(kLimits + name); len(b) > 0 {
			l := &machinev1.AgentLimits{}
			if proto.Unmarshal(b, l) == nil {
				n.limits[name] = l
			}
		}
	}
}

// noteLimits keeps what agent said of its limits now: at once when a window
// changed, else when what is kept is limitsEvery old.
func (n *Machine) noteLimits(agent string, said rt.Limits) {
	l := &machinev1.AgentLimits{Plan: said.Plan, ObservedAt: timestamppb.Now()}
	for _, w := range said.Windows {
		l.Windows = append(l.Windows, &machinev1.LimitWindow{Minutes: w.Minutes, UsedPercent: w.UsedPercent, ResetsAt: timestamppb.New(w.ResetsAt), Group: w.Group})
	}
	n.mu.Lock()
	old := n.limits[agent]
	if old != nil && old.Plan == l.Plan && slices.EqualFunc(old.Windows, l.Windows, func(a, b *machinev1.LimitWindow) bool { return proto.Equal(a, b) }) &&
		time.Since(old.ObservedAt.AsTime()) < limitsEvery {
		n.mu.Unlock()
		return
	}
	n.limits[agent] = l
	n.mu.Unlock()
	b, _ := proto.Marshal(l)
	if err := n.st.Set(kLimits+agent, b); err != nil {
		n.log.Warn("keeping an agent's limits", "agent", agent, "err", err)
	}
}

// noteAsked keeps the limits an agent answered when checked, if it did.
func (n *Machine) noteAsked(agent string, i rt.Info) {
	if i.Limits != nil {
		n.noteLimits(agent, *i.Limits)
	}
}
