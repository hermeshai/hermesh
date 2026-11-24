//go:build unix

package runtime

import "syscall"

func running(pid int) bool { return syscall.Kill(pid, 0) == nil }
