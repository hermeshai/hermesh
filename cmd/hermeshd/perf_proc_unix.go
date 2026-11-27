//go:build unix

package main

import (
	"bytes"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type osProcSample struct {
	PID     int
	PPID    int
	RSSKB   int64
	CPUSec  float64
	Command string
}

// sampleOSProcs snapshots all processes visible to this user using POSIX ps.
func sampleOSProcs() map[int]osProcSample {
	out, err := exec.Command("ps", "-ax", "-o", "pid=,ppid=,rss=,time=,comm=").Output()
	if err != nil {
		return nil
	}
	res := map[int]osProcSample{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		rss, err3 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || pid <= 0 {
			continue
		}
		cmd := strings.Join(f[4:], " ")
		res[pid] = osProcSample{
			PID:     pid,
			PPID:    ppid,
			RSSKB:   rss,
			CPUSec:  parsePSTime(f[3]),
			Command: cmd,
		}
	}
	return res
}

// parsePSTime parses ps's [[dd-]hh:]mm:ss[.cc] CPU time into seconds.
func parsePSTime(s string) float64 {
	var days float64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		v, _ := strconv.ParseFloat(d, 64)
		days = v
		s = rest
	}
	parts := strings.Split(s, ":")
	var sec float64
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		sec = sec*60 + v
	}
	return days*86400 + sec
}

var footprintRe = regexp.MustCompile(`\[(\d+)\].*?Footprint:\s+(\d+)\s+B`)

// sampleFootprintBytes returns macOS phys_footprint (Activity Monitor's Memory
// column) in bytes for pids; nil on non-darwin systems or if footprint fails.
func sampleFootprintBytes(pids []int) map[int]int64 {
	if runtime.GOOS != "darwin" || len(pids) == 0 {
		return nil
	}
	args := []string{"-f", "bytes"}
	for _, pid := range pids {
		if pid > 0 {
			args = append(args, "-p", strconv.Itoa(pid))
		}
	}
	out, err := exec.Command("footprint", args...).Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	res := map[int]int64{}
	for _, m := range footprintRe.FindAllSubmatch(out, -1) {
		pid, _ := strconv.Atoi(string(m[1]))
		b, _ := strconv.ParseInt(string(m[2]), 10, 64)
		res[pid] = b
	}
	return res
}

// sampleDarwinWakeups samples idle wakeups/sec on macOS over 1 second via top
// for the requested PIDs; returns nil on non-darwin or error.
func sampleDarwinWakeups(pids []int) map[int]float64 {
	if runtime.GOOS != "darwin" || len(pids) == 0 {
		return nil
	}
	want := map[int]bool{}
	for _, pid := range pids {
		if pid > 0 {
			want[pid] = true
		}
	}
	out, err := exec.Command("top", "-l", "2", "-s", "1", "-stats", "pid,idlew").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	// The second sample in `top -l 2 -s 1` reports delta idle wakeups over 1s.
	blocks := bytes.Split(out, []byte("PID"))
	if len(blocks) < 2 {
		return nil
	}
	last := string(blocks[len(blocks)-1])
	res := map[int]float64{}
	for _, line := range strings.Split(last, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || !want[pid] {
			continue
		}
		raw := strings.TrimRight(f[1], "+-")
		if w, err := strconv.ParseFloat(raw, 64); err == nil {
			res[pid] = w
		}
	}
	return res
}
