//go:build unix

package runtime

import (
	"cmp"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/hermeshai/hermesh/pkg/metrics"
)

var mProcTableScans = metrics.Default.Counter("hermesh_runtime_proctable_scans_total")

// Agents run their tools in process groups (or sessions) of their own, so
// killing the agent's group leaves them running, and when the agent dies
// they are reparented to init. A tracker samples the process table and
// remembers every descendant of each running agent (pid and start time,
// so a reused pid is never killed), to stop them when the agent ends.

type procInfo struct {
	pid, ppid int
	start     int64
}

type procSnap struct {
	procs []procInfo // sorted by pid
}

func (s *procSnap) startOf(pid int) (int64, bool) {
	i, ok := slices.BinarySearchFunc(s.procs, pid, func(p procInfo, target int) int {
		return cmp.Compare(p.pid, target)
	})
	if !ok {
		return 0, false
	}
	return s.procs[i].start, true
}

func scanProcs() *procSnap {
	procs := listProcs()
	slices.SortFunc(procs, func(a, b procInfo) int { return cmp.Compare(a.pid, b.pid) })
	return &procSnap{procs: procs}
}

type tracked struct {
	root       procInfo
	known      map[int]int64 // pid → start time
	baseline   map[int]int64 // pid → start time of long-lived sidecars/daemons
	seeded     bool
	lastAllCPU uint64
}

var tracker struct {
	mu      sync.Mutex
	roots   map[*tracked]struct{}
	running bool
}

const trackEvery = 3 * time.Second

// track starts remembering pid's descendants.
func track(pid int) *tracked {
	t := &tracked{root: procInfo{pid: pid}, known: map[int]int64{}}
	for _, p := range listProcs() {
		if p.pid == pid {
			t.root = p
		}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.roots == nil {
		tracker.roots = map[*tracked]struct{}{}
	}
	tracker.roots[t] = struct{}{}
	if !tracker.running {
		tracker.running = true
		go trackLoop()
	}
	return t
}

func trackLoop() {
	type sample struct {
		t   *tracked
		cpu uint64
		ok  bool
	}
	for {
		time.Sleep(trackEvery)
		tracker.mu.Lock()
		if len(tracker.roots) == 0 {
			tracker.running = false
			tracker.mu.Unlock()
			return
		}
		ts := make([]*tracked, 0, len(tracker.roots))
		for t := range tracker.roots {
			ts = append(ts, t)
		}
		tracker.mu.Unlock()

		samples := make([]sample, len(ts))
		needScan := !procCPUNanosecondExact
		for i, t := range ts {
			cpu, ok := t.allCPUTicks()
			samples[i] = sample{t: t, cpu: cpu, ok: ok}
			tracker.mu.Lock()
			quiescent := procCPUNanosecondExact && ok && t.seeded && cpu == t.lastAllCPU
			tracker.mu.Unlock()
			if !quiescent {
				needScan = true
			}
		}
		if !needScan {
			continue
		}

		mProcTableScans.Inc()
		snap := scanProcs()
		for _, s := range samples {
			changed := s.t.update(snap)
			tracker.mu.Lock()
			if procCPUNanosecondExact && s.ok && !changed {
				s.t.lastAllCPU = s.cpu
				s.t.seeded = true
			} else {
				s.t.seeded = false
			}
			tracker.mu.Unlock()
		}
	}
}

// allCPUTicks sums cumulative CPU counters across the root process and every
// currently remembered descendant (including baseline sidecars). It returns
// ok=false if any remembered PID's CPU counter cannot be read (e.g. exited).
func (t *tracked) allCPUTicks() (uint64, bool) {
	if !procCPUNanosecondExact || t == nil || t.root.pid <= 0 {
		return 0, false
	}
	total, ok := procCPU(t.root.pid)
	if !ok {
		return 0, false
	}
	tracker.mu.Lock()
	pids := make([]int, 0, len(t.known))
	for pid := range t.known {
		if pid != t.root.pid {
			pids = append(pids, pid)
		}
	}
	tracker.mu.Unlock()
	for _, pid := range pids {
		v, pOK := procCPU(pid)
		if !pOK {
			return 0, false
		}
		total += v
	}
	return total, true
}

// update adds the current descendants of the root and of every known
// descendant that is still the same process, reporting whether t.known changed.
func (t *tracked) update(snap *procSnap) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	changed := false
	queue := []int{t.root.pid}
	for pid, st := range t.known {
		if cur, ok := snap.startOf(pid); ok && cur == st {
			queue = append(queue, pid)
		} else {
			delete(t.known, pid)
			changed = true
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range snap.procs {
			if c.ppid != pid || c.pid == t.root.pid {
				continue
			}
			if _, ok := t.known[c.pid]; !ok && c.start >= t.root.start {
				t.known[c.pid] = c.start
				queue = append(queue, c.pid)
				changed = true
			}
		}
	}
	return changed
}

// running returns the arguments of the non-baseline descendants alive now,
// the newest first.
func (t *tracked) running() [][]string {
	snap := scanProcs()
	t.update(snap)
	var live []procInfo
	tracker.mu.Lock()
	for pid, st := range t.known {
		if cur, ok := snap.startOf(pid); ok && cur == st && t.baseline[pid] != st {
			live = append(live, procInfo{pid: pid, start: st})
		}
	}
	tracker.mu.Unlock()
	slices.SortFunc(live, func(a, b procInfo) int { return cmp.Or(cmp.Compare(b.start, a.start), cmp.Compare(b.pid, a.pid)) })
	var cmds [][]string
	for _, p := range live {
		if args := procArgs(p.pid); len(args) > 0 {
			cmds = append(cmds, args)
		}
	}
	return cmds
}

// snapshotBaseline marks every descendant alive right now as part of the
// session's standing baseline (MCP servers, language servers, or promoted
// daemon processes) so HasActiveDescendants ignores them.
func (t *tracked) snapshotBaseline() {
	snap := scanProcs()
	t.update(snap)
	tracker.mu.Lock()
	if t.baseline == nil {
		t.baseline = map[int]int64{}
	}
	for pid, st := range t.known {
		if cur, ok := snap.startOf(pid); ok && cur == st {
			t.baseline[pid] = st
		}
	}
	for pid, st := range t.baseline {
		if cur, ok := snap.startOf(pid); !ok || cur != st {
			delete(t.baseline, pid)
		}
	}
	tracker.mu.Unlock()
}

// hasActiveDescendants reports whether any non-baseline descendant process of
// the agent is currently alive.
func (t *tracked) hasActiveDescendants() bool {
	snap := scanProcs()
	t.update(snap)
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for pid, st := range t.known {
		if cur, ok := snap.startOf(pid); ok && cur == st && t.baseline[pid] != st {
			return true
		}
	}
	return false
}

// cpuTicks sums cumulative CPU counters across the root process and any
// currently alive non-baseline tracked descendants.
func (t *tracked) cpuTicks(rootPid int) (uint64, bool) {
	var total uint64
	var anyOK bool
	if rootPid > 0 {
		if v, ok := procCPU(rootPid); ok {
			total += v
			anyOK = true
		}
	}
	if t == nil {
		return total, anyOK
	}
	var pids []int
	tracker.mu.Lock()
	for pid, st := range t.known {
		if pid != rootPid && t.baseline[pid] != st {
			pids = append(pids, pid)
		}
	}
	tracker.mu.Unlock()
	for _, pid := range pids {
		if v, ok := procCPU(pid); ok {
			total += v
			anyOK = true
		}
	}
	return total, anyOK
}

// stop kills every remembered descendant still alive and stops tracking.
func (t *tracked) stop() int {
	tracker.mu.Lock()
	delete(tracker.roots, t)
	tracker.mu.Unlock()
	killed := 0
	for i := 0; i < 3; i++ { // catch processes forked meanwhile
		snap := scanProcs()
		t.update(snap)
		n := 0
		tracker.mu.Lock()
		for pid, st := range t.known {
			if cur, ok := snap.startOf(pid); ok && cur == st && syscall.Kill(pid, syscall.SIGKILL) == nil {
				n++
			}
		}
		tracker.mu.Unlock()
		killed += n
		if n == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return killed
}

// KillTree kills a process group left by a previous daemon and every
// descendant of its leader, wherever they moved (used at recovery, when
// the old agent may still be running).
func KillTree(pgid int) {
	if pgid <= 1 {
		return
	}
	t := &tracked{root: procInfo{pid: pgid}, known: map[int]int64{}}
	snap := scanProcs()
	for _, p := range snap.procs {
		if p.pid == pgid {
			t.root = p
		}
	}
	t.update(snap)
	KillGroup(pgid)
	t.stop()
}
