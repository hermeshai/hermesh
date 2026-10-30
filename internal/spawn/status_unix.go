//go:build unix

package spawn

import "syscall"

// unknownStatus marks an exit observed without a wait status (the child
// outlived its helper).
const unknownStatus = syscall.WaitStatus(0xffffffff)
