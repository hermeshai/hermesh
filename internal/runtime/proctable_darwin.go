package runtime

import (
	"bytes"
	"encoding/binary"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	procInfoCallPidInfo    = 2
	procPidTaskInfo        = 4
	procTaskInfoSize       = 96
	procCPUNanosecondExact = true
)

// procCPU returns pid's cumulative user + system CPU time in nanoseconds via
// Darwin's proc_pidinfo(PROC_PIDTASKINFO).
func procCPU(pid int) (uint64, bool) {
	if pid <= 0 {
		return 0, false
	}
	var buf [procTaskInfoSize]byte
	n, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO,
		procInfoCallPidInfo,
		uintptr(pid),
		procPidTaskInfo,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		procTaskInfoSize,
	)
	if errno != 0 || int(n) < 32 {
		return 0, false
	}
	userNS := binary.NativeEndian.Uint64(buf[16:24])
	sysNS := binary.NativeEndian.Uint64(buf[24:32])
	return userNS + sysNS, true
}

// procArgs returns pid's arguments (nil when they cannot be read). The
// kernel gives argc, the executable's path, padding, then the arguments.
func procArgs(pid int) []string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return nil
	}
	argc := int(binary.NativeEndian.Uint32(b))
	_, b, _ = bytes.Cut(b[4:], []byte{0})
	b = bytes.TrimLeft(b, "\x00")
	var args []string
	for len(args) < argc && len(b) > 0 {
		var arg []byte
		arg, b, _ = bytes.Cut(b, []byte{0})
		args = append(args, string(arg))
	}
	return args
}

// listProcs returns this user's processes.
func listProcs() []procInfo {
	uid := os.Getuid()
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.uid", uid)
	if err != nil {
		return nil
	}
	return extractProcInfos(kps, uint32(uid))
}

func extractProcInfos(kps []unix.KinfoProc, uid uint32) []procInfo {
	ps := make([]procInfo, 0, len(kps))
	for i := range kps {
		kp := &kps[i]
		if kp.Eproc.Ucred.Uid != uid {
			continue
		}
		st := kp.Proc.P_starttime
		ps = append(ps, procInfo{
			pid:   int(kp.Proc.P_pid),
			ppid:  int(kp.Eproc.Ppid),
			start: int64(st.Sec)*1e6 + int64(st.Usec),
		})
	}
	return ps
}
