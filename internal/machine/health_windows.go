package machine

import (
	"errors"

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
