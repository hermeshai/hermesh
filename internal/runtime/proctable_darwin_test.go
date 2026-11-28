package runtime

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestListProcsMatchesSysctlUIDAndIdleSkip(t *testing.T) {
	uid := os.Getuid()
	ps := listProcs()
	if len(ps) == 0 {
		t.Fatal("expected non-empty process list for current uid")
	}

	selfPID := os.Getpid()
	var selfInfo procInfo
	foundSelf := false
	for _, p := range ps {
		if p.pid == selfPID {
			selfInfo = p
			foundSelf = true
			break
		}
	}
	if !foundSelf || selfInfo.start <= 0 {
		t.Fatalf("expected self pid %d with positive start time, got found=%v info=%+v", selfPID, foundSelf, selfInfo)
	}

	kps, err := unix.SysctlKinfoProcSlice("kern.proc.uid", uid)
	if err != nil {
		t.Fatalf("SysctlKinfoProcSlice: %v", err)
	}
	ref := extractProcInfos(kps, uint32(uid))
	for _, p := range ref {
		if p.pid == selfPID && (p.ppid != selfInfo.ppid || p.start != selfInfo.start) {
			t.Fatalf("self procInfo mismatch: got=%+v ref=%+v", selfInfo, p)
		}
	}

	snap := scanProcs()
	if st, found := snap.startOf(selfPID); !found || st != selfInfo.start {
		t.Fatalf("expected snap.startOf(%d)=%d, got found=%v st=%d", selfPID, selfInfo.start, found, st)
	}

	// Verify SampleLiveness works without scanning the process table and tracks sleeping proc quiescent state.
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	p, err := StartProc(context.Background(), sleepBin, []string{"10"}, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatalf("StartProc: %v", err)
	}
	defer p.Stop(500 * time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	_, ticks, hasCPU := p.SampleLiveness()
	if !hasCPU || ticks == 0 {
		t.Fatalf("expected SampleLiveness to report non-zero CPU ticks on darwin, got hasCPU=%v ticks=%d", hasCPU, ticks)
	}

	// Verify quiescent tracked tree produces zero redundant scans once seeded.
	tr := p.tree
	snap2 := scanProcs()
	preCPU, preOK := tr.allCPUTicks()
	if !preOK {
		t.Fatal("expected allCPUTicks ok=true for live sleep process")
	}
	if changed := tr.update(snap2); changed {
		preCPU, preOK = tr.allCPUTicks()
	}
	cpuAgain, okAgain := tr.allCPUTicks()
	if !okAgain || cpuAgain != preCPU {
		t.Fatalf("expected quiescent sleeping process to report identical nanosecond CPU (%d vs %d)", preCPU, cpuAgain)
	}
}
