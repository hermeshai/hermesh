//go:build unix

package localipc

import (
	"context"
	"net"
	"os"
	"path/filepath"

	"github.com/hermeshai/hermesh/internal/peercred"
)

// Listen serves the Unix socket at path: 0600, in a folder made 0700 when
// missing, replacing a stale socket, and dropping connections from other
// users.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	return &peercred.SameUserListener{UnixListener: ln.(*net.UnixListener)}, nil
}

// Dial connects to the socket at path: one of ours, or another program's
// (Claude Code's inbox).
func Dial(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

// Peer reports the process at the other end of c; ok is false when c is
// not a Unix socket's connection.
func Peer(c net.Conn) (p Process, ok bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Process{}, false
	}
	pp := peercred.PeerProcess(uc)
	return Process{PID: pp.PID, Executable: pp.Executable}, true
}
