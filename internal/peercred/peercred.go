//go:build darwin || linux

// Package peercred restricts Unix socket connections to the daemon's own
// user.
package peercred

import (
	"net"
	"os"
	"runtime"
)

// SameUserListener drops connections from other users. On Linux it also
// takes root's: a Linux trusted device's protected signer runs as root
// (docs/linux.md §7.3), and root reads the user's files anyway.
type SameUserListener struct {
	*net.UnixListener
}

func (l *SameUserListener) Accept() (net.Conn, error) {
	for {
		c, err := l.UnixListener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := PeerUID(c)
		if err != nil || !admitted(uid) {
			c.Close()
			continue
		}
		return c, nil
	}
}

// admitted: this user, and on Linux root.
func admitted(uid int) bool {
	return uid == os.Getuid() || runtime.GOOS == "linux" && uid == 0
}

// Process describes the local process at the other end of a connection: a
// hint for the person (which program asked), not proof of anything.
type Process struct {
	PID        int
	Executable string
}

// PeerProcess reports the process on the other end of c, as far as the OS
// tells; the zero value if it cannot.
func PeerProcess(c *net.UnixConn) Process {
	pid, err := peerPID(c)
	if err != nil || pid <= 0 {
		return Process{}
	}
	return Process{PID: pid, Executable: executable(pid)}
}
