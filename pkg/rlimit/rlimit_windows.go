package rlimit

// RaiseNoFile does nothing: Windows has no open-file limit to raise. It
// returns 0.
func RaiseNoFile() uint64 { return 0 }
