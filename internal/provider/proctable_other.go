//go:build unix && !darwin && !linux

package provider

func listProcs() []procInfo { return nil }

func procArgs(pid int) []string { return nil }
