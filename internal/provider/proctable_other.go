//go:build !darwin && !linux

package provider

func listProcs() []procInfo { return nil }
