package runtime

import "syscall"

// On Windows a session's job object holds all of its descendants
// (docs/windows.md §4.3), so nothing samples the process table.

type tracked struct{}

func track(pid int) *tracked                  { return &tracked{} }
func (t *tracked) stop() int                  { return 0 }
func (t *tracked) running() [][]string        { return nil }
func (t *tracked) snapshotBaseline()          {}
func (t *tracked) hasActiveDescendants() bool { return false }

func (t *tracked) cpuTicks(rootPid int) (uint64, bool) {
	if rootPid <= 0 {
		return 0, false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(rootPid))
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(h)
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	k := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	u := uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	return k + u, true
}

// KillTree has nothing to do: when a daemon exits, its jobs end their
// processes.
func KillTree(pgid int) {}
