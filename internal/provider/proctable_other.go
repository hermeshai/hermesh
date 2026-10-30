//go:build unix && !darwin && !linux

package provider

func listProcs() []procInfo { return nil }
