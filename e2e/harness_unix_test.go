//go:build unix

package e2e

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, which killGroup and
// stopGroup end as a whole.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(pid int) { syscall.Kill(-pid, syscall.SIGKILL) }

func stopGroup(pid int) { syscall.Kill(-pid, syscall.SIGTERM) }

func killProcess(pid int) { syscall.Kill(pid, syscall.SIGKILL) }
