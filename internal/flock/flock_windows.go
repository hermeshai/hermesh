package flock

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

type Lock struct{ f *os.File }

// Acquire locks dir/lock without blocking.
func Acquire(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// A daemon that just stopped may hold it a moment longer: Windows
	// releases a dead process's locks as it lets go of its handles.
	for tries := 30; ; tries-- {
		err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
		if err == nil {
			break
		}
		if tries == 0 {
			f.Close()
			return nil, fmt.Errorf("another hermeshd is already using %s", dir)
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return &Lock{f: f}, nil
}

func (l *Lock) Release() {
	if l != nil && l.f != nil {
		windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, &windows.Overlapped{})
		l.f.Close()
	}
}
