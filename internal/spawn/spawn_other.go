//go:build !darwin

package spawn

import (
	"errors"
	"os"
	"syscall"
)

// Supported reports whether Start is available on this platform. Elsewhere
// os/exec is used (Linux already spawns with a vfork-style clone).
const Supported = false

func Start(path string, argv, env []string, dir string, stdin, stdout, stderr *os.File) (int, error) {
	return 0, errors.New("spawn: unsupported platform")
}

var ErrFDRange = errors.New("spawn: descriptor above OPEN_MAX")

func Pipe(parentReads bool) (parent, child *os.File, err error) {
	r, w, err := os.Pipe()
	if parentReads {
		return r, w, err
	}
	return w, r, err
}

// Helper is darwin-only; elsewhere StartProc uses os/exec.
type Helper struct{}

func StartHelper(exe string, args ...string) (*Helper, error) { return nil, errors.ErrUnsupported }
func Serve(f *os.File) error                                  { return errors.ErrUnsupported }
func (h *Helper) Pid() int                                    { return 0 }
func (h *Helper) Close() error                                { return nil }
func (h *Helper) Start(path string, argv, env []string, dir string, stdin, stdout, stderr *os.File) (int, <-chan syscall.WaitStatus, error) {
	return 0, nil, ErrHelperGone
}
