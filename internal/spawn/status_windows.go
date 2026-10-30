package spawn

import "syscall"

// unknownStatus marks an exit observed without a wait status.
var unknownStatus = syscall.WaitStatus{ExitCode: 0xffffffff}
