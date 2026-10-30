//go:build unix

// Package flock takes an exclusive, process-lifetime lock on a data
// directory so two daemons never share one.
package flock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another hermeshd is already using %s", dir)
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return &Lock{f: f}, nil
}

func (l *Lock) Release() {
	if l != nil && l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
	}
}
