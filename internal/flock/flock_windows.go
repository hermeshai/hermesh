package flock

import (
	"errors"
	"fmt"
)

type Lock struct{}

// Acquire is not supported on Windows yet (docs/windows.md, phase W1).
func Acquire(dir string) (*Lock, error) {
	return nil, fmt.Errorf("locking %s: %w on Windows yet", dir, errors.ErrUnsupported)
}

func (l *Lock) Release() {}
