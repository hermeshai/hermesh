package e2e

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// On Windows a test process is the root of a tree that taskkill ends as a
// whole; there is no gentler stop to send it.

func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func killGroup(pid int) { exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run() }

func stopGroup(pid int) { killGroup(pid) }

func killProcess(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

// shortTemp is a temp folder for the tests' endpoints.
var shortTemp = os.TempDir()

// exe ends a program's file name (os/exec finds it without it too).
const exe = ".exe"
