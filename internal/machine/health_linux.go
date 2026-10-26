package machine

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func userProcesses() (procCount, error) {
	var pc procCount
	var rl unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur != unix.RLIM_INFINITY {
		pc.limit = rl.Cur
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return pc, err
	}
	uid := uint32(os.Getuid())
	names := map[int]string{}
	zombies := map[int]uint32{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.Stat("/proc/" + e.Name())
		if err != nil || st.Sys().(*syscall.Stat_t).Uid != uid {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		pc.used++
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
