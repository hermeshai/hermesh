package machine

import "errors"

// Windows reads these in phase W1 (docs/windows.md §4.7).

func userProcesses() (procCount, error) { return procCount{}, errors.ErrUnsupported }

func diskSpace(dir string) (free, size uint64, ok bool) { return 0, 0, false }

func openFiles() (open, limit uint64, ok bool) { return 0, 0, false }
