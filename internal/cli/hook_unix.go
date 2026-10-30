//go:build unix

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// lockFile waits for an exclusive lock on f; unlockFile releases it.
func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func unlockFile(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// detach makes cmd outlive this process: a session of its own.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
