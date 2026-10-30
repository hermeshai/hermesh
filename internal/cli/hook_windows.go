package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// lockFile waits for an exclusive lock on f; unlockFile releases it.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

func unlockFile(f *os.File) {
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}

// detach makes cmd outlive this process, without a console.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}

// hookCommand is how an agent's hook names this program, in words Git
// Bash, cmd.exe and PowerShell read alike (agents run hooks with any of
// them on Windows): its bare name when the PATH finds it, else its path
// with forward slashes, quoted only when it has a space.
func hookCommand(exe string) string {
	if p, err := exec.LookPath("hermesh"); err == nil && strings.EqualFold(filepath.Clean(p), filepath.Clean(exe)) {
		return "hermesh"
	}
	p := filepath.ToSlash(exe)
	if strings.Contains(p, " ") {
		return `"` + p + `"`
	}
	return p
}

// othersCanRead is false: Windows reports no modes, and the CLI's folder is
// in the user's own Local AppData.
func othersCanRead(fi os.FileInfo) bool { return false }
