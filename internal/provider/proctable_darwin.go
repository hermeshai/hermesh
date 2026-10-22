package provider

import (
	"os"

	"golang.org/x/sys/unix"
)

// listProcs returns this user's processes.
func listProcs() []procInfo {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	uid := uint32(os.Getuid())
	ps := make([]procInfo, 0, len(kps))
	for _, kp := range kps {
		if kp.Eproc.Ucred.Uid != uid {
			continue
		}
		st := kp.Proc.P_starttime
		ps = append(ps, procInfo{pid: int(kp.Proc.P_pid), ppid: int(kp.Eproc.Ppid), start: int64(st.Sec)*1e6 + int64(st.Usec)})
	}
	return ps
}
