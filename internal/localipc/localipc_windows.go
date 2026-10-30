package localipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// On Windows an endpoint is a named pipe that only this user can open. Its
// name is random, and the file at the endpoint's path holds it: no other
// user can read that file (it is in this user's data folder), so no other
// user can learn the name, take it first or pose as the endpoint.

const pipePrefix = `\\.\pipe\`

// Listen serves a new pipe and writes its name into the file at path.
func Listen(path string) (net.Listener, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	var b [16]byte
	rand.Read(b[:])
	name := pipePrefix + "hermesh-" + hex.EncodeToString(b[:])
	// Only this user (go-winio also refuses remote clients).
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")"})
	if err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(name), 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Dial connects to the pipe named in the file at path, or to path itself
// when it is a pipe's name (another program's endpoint).
func Dial(ctx context.Context, path string) (net.Conn, error) {
	name := path
	if !strings.HasPrefix(path, pipePrefix) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if name = strings.TrimSpace(string(b)); !strings.HasPrefix(name, pipePrefix) {
			return nil, fmt.Errorf("%s names no pipe", path)
		}
	}
	return winio.DialPipeContext(ctx, name)
}

// Peer reports the process at the other end of c; ok is false when c is
// not a pipe's connection.
func Peer(c net.Conn) (p Process, ok bool) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return Process{}, false
	}
	var pid uint32
	if windows.GetNamedPipeClientProcessId(windows.Handle(f.Fd()), &pid) != nil {
		return Process{}, true
	}
	return Process{PID: int(pid), Executable: executable(pid)}, true
}

func executable(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func userSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
