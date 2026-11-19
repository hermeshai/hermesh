//go:build unix

package machine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// diskSpace is the free and the total space, in bytes, of the disk that
// holds dir.
func diskSpace(dir string) (free, size uint64, ok bool) {
	// The iPhone cannot run an execution machine or its disk diagnostics.
	if runtime.GOOS == "ios" {
		return 0, 0, false
	}
	var fs unix.Statfs_t
	if unix.Statfs(dir, &fs) != nil {
		return 0, 0, false
	}
	return uint64(fs.Bavail) * uint64(fs.Bsize), uint64(fs.Blocks) * uint64(fs.Bsize), true
}

// openFiles is how many files this process has open, and its limit.
func openFiles() (open, limit uint64, ok bool) {
	var rl unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &rl) != nil {
		return 0, 0, false
	}
	fds, err := os.ReadDir(fdDir())
	if err != nil {
		return 0, 0, false
	}
	return uint64(len(fds)), rl.Cur, true
}

func fdDir() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/fd"
	}
	return "/dev/fd"
}

// du counts allocated blocks, including directory entries, and accounts
// for hard links without following symlinks into other checkouts.
func workspaceBytes(ctx context.Context, path string) (uint64, error) {
	out, err := exec.CommandContext(ctx, "du", "-sk", path).Output()
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("du returned no size")
	}
	kib, err := strconv.ParseUint(fields[0], 10, 64)
	return kib * 1024, err
}
