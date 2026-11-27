//go:build unix && !darwin && !linux

package runtime

func listProcs() []procInfo { return nil }

func procArgs(pid int) []string { return nil }

func procCPU(pid int) (uint64, bool) { return 0, false }
