//go:build !darwin && !linux && !windows

package runtime

const procCPUNanosecondExact = false

func procCPU(pid int) (uint64, bool) { return 0, false }
func procArgs(pid int) []string      { return nil }
func listProcs() []procInfo          { return nil }
