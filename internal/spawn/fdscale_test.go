//go:build darwin

package spawn

import (
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
	"time"
)

// TestSpawnCostVsOpenFDs (manual, -run SpawnCost -v): posix_spawn latency as
// the parent holds more descriptors, in-process and through a Helper.
func TestSpawnCostVsOpenFDs(t *testing.T) {
	if os.Getenv("HERMESH_FDSCALE") == "" {
		t.Skip("set HERMESH_FDSCALE=1")
	}
	var l syscall.Rlimit
	syscall.Getrlimit(syscall.RLIMIT_NOFILE, &l)
	n := l
	n.Cur = 60000
	syscall.Setrlimit(syscall.RLIMIT_NOFILE, &n)
	null, _ := os.Open(os.DevNull)
	h := startTestHelper(t)
	defer h.Close()
	var held []*os.File
	for _, target := range []int{100, 2000, 6000, 15000, 30000} {
		for len(held) < target {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, r, w)
		}
		var ds, hs []time.Duration
		for i := 0; i < 200; i++ {
			s := time.Now()
			pid, err := Start("/usr/bin/true", []string{"true"}, nil, "", null, null, null)
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(s))
			p, _ := os.FindProcess(pid)
			p.Wait()
			s = time.Now()
			_, exited, err := h.Start("/usr/bin/true", []string{"true"}, nil, "", null, null, null)
			if err != nil {
				t.Fatal(err)
			}
			hs = append(hs, time.Since(s))
			<-exited
		}
		for _, d := range [][]time.Duration{ds, hs} {
			sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		}
		t.Logf("fds=%6d in-process p50=%v p90=%v | helper p50=%v p90=%v", len(held), ds[100], ds[180], hs[100], hs[180])
	}
}

// TestSpawnRate (manual): how many processes per second this machine can
// create and run to exit, with P parallel spawners (HERMESH_SPAWNRATE_BIN).
func TestSpawnRate(t *testing.T) {
	bin := os.Getenv("HERMESH_SPAWNRATE_BIN")
	if bin == "" {
		t.Skip("set HERMESH_SPAWNRATE_BIN")
	}
	null, _ := os.Open(os.DevNull)
	for _, par := range []int{1, 4, 8, 16} {
		const n = 2000
		jobs := make(chan struct{}, n)
		for i := 0; i < n; i++ {
			jobs <- struct{}{}
		}
		close(jobs)
		start := time.Now()
		done := make(chan struct{})
		for p := 0; p < par; p++ {
			go func() {
				for range jobs {
					pid, err := Start(bin, []string{bin}, nil, "", null, null, null)
					if err != nil {
						t.Error(err)
						continue
					}
					var ws syscall.WaitStatus
					syscall.Wait4(pid, &ws, 0, nil)
				}
				done <- struct{}{}
			}()
		}
		for p := 0; p < par; p++ {
			<-done
		}
		t.Logf("%s parallel=%2d: %.0f processes/s", filepath.Base(bin), par, n/time.Since(start).Seconds())
	}
}
