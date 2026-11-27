package runtime

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// procCPU returns pid's cumulative utime + stime clock ticks from /proc/<pid>/stat.
func procCPU(pid int) (uint64, bool) {
	if pid <= 0 {
		return 0, false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3 (state); utime=14 (f[11]), stime=15 (f[12])
	if len(f) < 13 {
		return 0, false
	}
	utime, uErr := strconv.ParseUint(f[11], 10, 64)
	stime, sErr := strconv.ParseUint(f[12], 10, 64)
	if uErr != nil || sErr != nil {
		return 0, false
	}
	return utime + stime, true
}

// procArgs returns pid's arguments (nil when they cannot be read).
func procArgs(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// listProcs returns this user's processes (/proc/<pid>/stat: ppid is
// field 4, the start time field 22, counted after the command).
func listProcs() []procInfo {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	uid := uint32(os.Getuid())
	var ps []procInfo
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if fi, err := os.Stat("/proc/" + e.Name()); err != nil || fi.Sys().(*syscall.Stat_t).Uid != uid {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:]) // f[0] is field 3 (state)
		if len(f) < 20 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		start, _ := strconv.ParseInt(f[19], 10, 64)
		ps = append(ps, procInfo{pid: pid, ppid: ppid, start: start})
	}
	return ps
}
