package provider

import (
	"sync"
	"syscall"
	"time"
)

// Agents run their tools in process groups (or sessions) of their own, so
// killing the agent's group leaves them running, and when the agent dies
// they are reparented to init. A tracker samples the process table and
// remembers every descendant of each running agent (pid and start time,
// so a reused pid is never killed), to stop them when the agent ends.

type procInfo struct {
	pid, ppid int
	start     int64
}

type tracked struct {
	root  procInfo
	known map[int]int64 // pid → start time
}

var tracker struct {
	mu      sync.Mutex
	roots   map[*tracked]struct{}
	running bool
}

const trackEvery = time.Second

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
		procs := listProcs()
		for _, t := range ts {
			t.update(procs)
		}
	}
}

// update adds the current descendants of the root and of every known
// descendant that is still the same process.
func (t *tracked) update(procs []procInfo) {
	children := map[int][]procInfo{}
	alive := map[int]int64{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p)
		alive[p.pid] = p.start
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	queue := []int{t.root.pid}
	for pid, st := range t.known {
		if alive[pid] == st {
			queue = append(queue, pid)
		} else {
			delete(t.known, pid)
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if c.pid == t.root.pid {
				continue
			}
			if _, ok := t.known[c.pid]; !ok && c.start >= t.root.start {
				t.known[c.pid] = c.start
				queue = append(queue, c.pid)
			}
		}
	}
}

// stop kills every remembered descendant still alive and stops tracking.
func (t *tracked) stop() int {
	tracker.mu.Lock()
	delete(tracker.roots, t)
	tracker.mu.Unlock()
	killed := 0
	for i := 0; i < 3; i++ { // catch processes forked meanwhile
		procs := listProcs()
		t.update(procs)
		alive := map[int]int64{}
		for _, p := range procs {
			alive[p.pid] = p.start
		}
		n := 0
		tracker.mu.Lock()
		for pid, st := range t.known {
			if alive[pid] == st && syscall.Kill(pid, syscall.SIGKILL) == nil {
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
	procs := listProcs()
	for _, p := range procs {
		if p.pid == pgid {
			t.root = p
		}
	}
	t.update(procs)
	KillGroup(pgid)
	t.stop()
}
