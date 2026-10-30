package e2e

import (
	"os"
	"os/exec"
)

// The end-to-end suite runs on Windows from phase W1 (docs/windows.md §9),
// with job objects in place of process groups.

func ownGroup(cmd *exec.Cmd) {}

func killGroup(pid int) { killProcess(pid) }

func stopGroup(pid int) { killProcess(pid) }

func killProcess(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}
