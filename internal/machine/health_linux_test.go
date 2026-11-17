package machine

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

// Count real Linux tasks, restricted to this process so other processes ending
// and the Go runtime reusing idle threads cannot change the expected count.
func TestUserProcessesCountThreads(t *testing.T) {
	procDir := t.TempDir()
	pid := strconv.Itoa(os.Getpid())
	if err := os.Symlink(filepath.Join("/proc", pid), filepath.Join(procDir, pid)); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	var started sync.WaitGroup
	for i := 0; i < 50; i++ {
		started.Add(1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			started.Done()
			<-stop
		}()
	}
	started.Wait()
	pc, err := userProcessesIn(procDir, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	if pc.used < 50 {
		t.Fatalf("50 held threads in one process, counted %d", pc.used)
	}
}
