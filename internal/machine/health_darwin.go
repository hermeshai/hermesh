package machine

import (
	"os"

	"golang.org/x/sys/unix"
)

// szomb is a process that ended and was not reaped (sys/proc.h).
const szomb = 5

func userProcesses() (procCount, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return procCount{}, err
	}
	pc := procCount{used: uint64(len(procs))}
	if v, err := unix.SysctlUint32("kern.maxprocperuid"); err == nil {
		pc.limit = uint64(v)
	}
	var rl unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur > 0 && (pc.limit == 0 || rl.Cur < pc.limit) {
		pc.limit = rl.Cur
	}
	names := map[int32]string{}
	zombies := map[int32]uint32{}
	for _, p := range procs {
		names[p.Proc.P_pid] = unix.ByteSliceToString(p.Proc.P_comm[:])
		if p.Proc.P_stat == szomb {
			zombies[p.Eproc.Ppid]++
		}
	}
	for ppid, c := range zombies {
		if c > pc.culpritCount {
			pc.culprit, pc.culpritCount = names[ppid], c
		}
	}
	return pc, nil
}
