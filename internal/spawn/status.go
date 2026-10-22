package spawn

import (
	"errors"
	"fmt"
	"syscall"
)

// ErrHelperGone: the spawn helper has exited; spawn in-process instead.
var ErrHelperGone = errors.New("spawn helper exited")

// unknownStatus marks an exit observed without a wait status (the child
// outlived its helper).
const unknownStatus = syscall.WaitStatus(0xffffffff)

// ExitErr turns a wait status into nil (success) or an error worded like
// os/exec's.
func ExitErr(ws syscall.WaitStatus) error {
	switch {
	case ws == unknownStatus:
		return errors.New("exit status unknown (spawn helper exited)")
	case ws.Signaled():
		return fmt.Errorf("signal: %v", ws.Signal())
	case ws.Exited() && ws.ExitStatus() == 0:
		return nil
	default:
		return fmt.Errorf("exit status %d", ws.ExitStatus())
	}
}
