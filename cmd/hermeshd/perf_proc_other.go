//go:build !unix

package main

type osProcSample struct {
	PID     int
	PPID    int
	RSSKB   int64
	CPUSec  float64
	Command string
}

func sampleOSProcs() map[int]osProcSample            { return nil }
func sampleFootprintBytes(pids []int) map[int]int64  { return nil }
func sampleDarwinWakeups(pids []int) map[int]float64 { return nil }
