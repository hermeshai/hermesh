package provider

import (
	"bytes"
	"encoding/binary"
	"os"

	"golang.org/x/sys/unix"
)

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
