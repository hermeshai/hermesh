package cli

import (
	"errors"
	"os"
	"os/exec"
)

// Phase W1 (docs/windows.md §4.7) brings these to Windows.

func lockFile(f *os.File) error { return errors.ErrUnsupported }

func unlockFile(f *os.File) {}

func processAlive(pid int) bool { return false }

func detach(cmd *exec.Cmd) {}
