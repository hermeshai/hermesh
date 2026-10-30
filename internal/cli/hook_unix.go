//go:build unix

package cli

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// lockFile waits for an exclusive lock on f; unlockFile releases it.
func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func unlockFile(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// detach makes cmd outlive this process: a session of its own.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// hookCommand is how an agent's hook names this program.
func hookCommand(exe string) string { return strconv.Quote(exe) }

// othersCanRead reports whether a file's mode lets other users read it.
func othersCanRead(fi os.FileInfo) bool { return fi.Mode().Perm()&0o077 != 0 }
