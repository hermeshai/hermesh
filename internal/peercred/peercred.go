//go:build darwin || linux

// Package peercred restricts Unix socket connections to the daemon's own
// user.
package peercred

import (
	"net"
	"os"
)

// SameUserListener drops connections from other users.
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
		if err != nil || uid != os.Getuid() {
			c.Close()
			continue
		}
		return c, nil
	}
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
