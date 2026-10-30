package provider

// On Windows a session's job object holds all of its descendants
// (docs/windows.md §4.3), so nothing samples the process table.

type tracked struct{}

func track(pid int) *tracked { return &tracked{} }
func (t *tracked) stop() int { return 0 }

// KillTree has nothing to do: when a daemon exits, its jobs end their
// processes.
func KillTree(pgid int) {}
