//go:build unix

// Package rlimit raises the open-file limit: launchd starts agents with a
// soft limit of 256, and every coding-agent session needs several pipes.
package rlimit

import "syscall"

// RaiseNoFile lifts the soft RLIMIT_NOFILE as far as the hard limit (and
// the kernel) allow, and returns the resulting soft limit.
func RaiseNoFile() uint64 {
	var l syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &l) != nil {
		return 0
	}
	for _, want := range []uint64{l.Max, 1 << 20, 245760, 65536, 10240} {
		if want <= l.Cur || want > l.Max {
			continue
		}
		n := l
		n.Cur = want
		if syscall.Setrlimit(syscall.RLIMIT_NOFILE, &n) == nil {
			return want
		}
	}
	return l.Cur
}
