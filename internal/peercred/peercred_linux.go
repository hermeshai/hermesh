package peercred

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func PeerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var serr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil {
			uid = int(cred.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, serr
}

func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid := 0
	var serr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil {
			pid = int(cred.Pid)
		}
	})
	if err != nil {
		return 0, err
	}
	return pid, serr
}

func executable(pid int) string {
	p, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	return p
}
