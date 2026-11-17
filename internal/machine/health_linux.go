package machine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// userProcesses counts what Linux's RLIMIT_NPROC counts: the user's
// threads (tasks), not their processes; each zombie is one.
func userProcesses() (procCount, error) {
	pc, err := userProcessesIn("/proc", uint32(os.Getuid()))
	if err != nil {
		return pc, err
	}
	var rl unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur != unix.RLIM_INFINITY {
		pc.limit = rl.Cur
	}
	return pc, nil
}

func userProcessesIn(procDir string, uid uint32) (procCount, error) {
	var pc procCount
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return pc, err
	}
	names := map[int]string{}
	zombies := map[int]uint32{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.Stat(filepath.Join(procDir, e.Name()))
		if err != nil || st.Sys().(*syscall.Stat_t).Uid != uid {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procDir, e.Name(), "stat"))
		if err != nil {
			continue
		}
		if tasks, err := os.ReadDir(filepath.Join(procDir, e.Name(), "task")); err == nil && len(tasks) > 0 {
			pc.used += uint64(len(tasks))
		} else {
			pc.used++
		}
		// "pid (comm) state ppid …"; comm may hold spaces and parentheses.
		s := string(b)
		open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || end < open {
			continue
		}
		names[pid] = s[open+1 : end]
		f := strings.Fields(s[end+1:])
		if len(f) >= 2 && f[0] == "Z" {
			if ppid, err := strconv.Atoi(f[1]); err == nil {
				zombies[ppid]++
			}
		}
	}
	for ppid, c := range zombies {
		if c > pc.culpritCount {
			pc.culprit, pc.culpritCount = names[ppid], c
		}
	}
	return pc, nil
}
