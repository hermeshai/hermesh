package machine

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func userProcesses() (procCount, error) {
	return procCount{}, errors.New("Windows sets no limit on a user's processes")
}

// diskSpace is the free (to this user) and the total space, in bytes, of
// the disk that holds dir.
func diskSpace(dir string) (free, size uint64, ok bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, false
	}
	var total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
		return 0, 0, false
	}
	return free, total, true
}

// openFiles: Windows has no open-file limit to come near.
func openFiles() (open, limit uint64, ok bool) { return 0, 0, false }

// Sum file sizes without traversing directory links on Windows.
func workspaceBytes(ctx context.Context, path string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

func providerLocalDisk(string) bool { return false }
