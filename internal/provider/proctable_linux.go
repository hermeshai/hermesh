package provider

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

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
