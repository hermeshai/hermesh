package machine

import (
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

// The machine's health: what would keep it from working, read from the
// system without starting a process (the very thing that may fail) or
// reading the user's files, and sent with its status to every screen of
// the owner. It is read only while its status is asked for (a screen or a
// machine watching it), at most every healthEvery: nothing runs in the
// background. When the processes are no longer at their limit, coding
// agents whose last check failed for want of them are checked again.

const (
	healthEvery = 15 * time.Second
	// diskLow: free space under which the disk with Hermesh's data is a problem.
	diskLow = 2 << 30
	// culpritMin: ended, unreaped processes of one parent worth naming it for.
	culpritMin = 50
)

// procCount is the user's processes, their limit, and the program holding
// the most ended processes nobody reaped.
type procCount struct {
	used, limit  uint64
	culprit      string
	culpritCount uint32
}

// nearLimit: 90% or more of a limit is in use.
func nearLimit(used, limit uint64) bool { return limit > 0 && used*10 >= limit*9 }

func (n *Machine) checkHealth() []*machinev1.MachineProblem {
	var out []*machinev1.MachineProblem
	if pc, err := userProcesses(); err == nil && nearLimit(pc.used, pc.limit) {
		p := &machinev1.MachineProblem{Kind: machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_PROCESSES, Used: pc.used, Limit: pc.limit}
		if pc.culpritCount >= culpritMin {
			p.Culprit, p.CulpritCount = pc.culprit, pc.culpritCount
		}
		out = append(out, p)
	}
	if free, size, ok := diskSpace(n.cfg.DataDir); ok && free < diskLow {
		out = append(out, &machinev1.MachineProblem{Kind: machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_DISK, Used: free, Limit: size})
	}
	if open, limit, ok := openFiles(); ok && nearLimit(open, limit) {
		out = append(out, &machinev1.MachineProblem{Kind: machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_FILES, Used: open, Limit: limit})
	}
	// Out of processes, the shell can't start either: that says it already.
	if why := shellenv.LastProblem(); why != "" && !hasProblem(out, machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_PROCESSES) {
		out = append(out, &machinev1.MachineProblem{Kind: machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_SHELL_PATH, Detail: why})
	}
	return out
}

// refreshHealth checks the health now; a change goes to the screens, and
// processes coming back check the coding agents again.
func (n *Machine) refreshHealth() {
	now := n.checkHealth()
	n.mu.Lock()
	before := n.health
	n.health = now
	n.mu.Unlock()
	if slices.EqualFunc(before, now, func(a, b *machinev1.MachineProblem) bool { return proto.Equal(a, b) }) {
		return
	}
	for _, p := range now {
		if !hasProblem(before, p.Kind) {
			n.log.Warn("machine problem", "kind", p.Kind.String(), "used", p.Used, "limit", p.Limit, "culprit", p.Culprit, "detail", p.Detail)
		}
	}
	for _, p := range before {
		if !hasProblem(now, p.Kind) {
			n.log.Info("machine problem gone", "kind", p.Kind.String())
		}
	}
	if outOf(before) && !outOf(now) {
		n.mu.Lock()
		short := false
		for _, i := range n.infos {
			short = short || i.Problem == provider.ProblemNoProcesses
		}
		n.mu.Unlock()
		if short {
			n.CheckAgents(true, 0)
		}
	}
	n.kickStatus()
}

// freshHealth reads the health again when it is older than healthEvery.
func (n *Machine) freshHealth() {
	n.mu.Lock()
	stale := time.Since(n.healthAt) >= healthEvery
	if stale {
		n.healthAt = time.Now()
	}
	n.mu.Unlock()
	if stale {
		n.refreshHealth()
	}
}

// outOfProcesses says, when the user's processes are at their limit, how
// many there are; "" otherwise. It reads the health again if it is stale.
func (n *Machine) outOfProcesses() string {
	n.freshHealth()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.health {
		if outOf([]*machinev1.MachineProblem{p}) {
			why := fmt.Sprintf("the user's processes are at their limit (%d / %d)", p.Used, p.Limit)
			if p.Culprit != "" {
				why += fmt.Sprintf(", %d of them ended processes %s never reaped", p.CulpritCount, p.Culprit)
			}
			return why
		}
	}
	return ""
}

// outOf: the user's processes are at their limit.
func outOf(ps []*machinev1.MachineProblem) bool {
	return slices.ContainsFunc(ps, func(p *machinev1.MachineProblem) bool {
		return p.Kind == machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_PROCESSES && p.Used >= p.Limit
	})
}

func hasProblem(ps []*machinev1.MachineProblem, k machinev1.MachineProblemKind) bool {
	return slices.ContainsFunc(ps, func(p *machinev1.MachineProblem) bool { return p.Kind == k })
}
