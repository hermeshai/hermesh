//go:build unix

package e2e

import (
	"os/exec"
	"syscall"
	"testing"
)

// ownGroup starts cmd in a process group of its own, which killGroup and
// stopGroup end as a whole.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(pid int) { syscall.Kill(-pid, syscall.SIGKILL) }

func stopGroup(pid int) { syscall.Kill(-pid, syscall.SIGTERM) }

func killProcess(pid int) { syscall.Kill(pid, syscall.SIGKILL) }

// shortTemp is a temp folder whose paths stay short enough for sockets.
const shortTemp = "/tmp"

// exe ends a program's file name.
const exe = ""

// unixOnly skips a test on Windows (nothing to do here).
func unixOnly(t *testing.T, why string) {}

// homeEnv gives a process home as its home folder.
func homeEnv(home string) []string { return []string{"HOME=" + home} }
