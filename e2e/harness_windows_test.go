package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

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

// unixOnly skips a test that cannot run on Windows, saying why.
func unixOnly(t *testing.T, why string) { t.Skip("Unix only: " + why) }

// homeEnv gives a process home as its home folder, and its AppData there.
func homeEnv(home string) []string {
	return []string{"HOME=" + home, "USERPROFILE=" + home, "LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"), "APPDATA=" + filepath.Join(home, "AppData", "Roaming")}
}
