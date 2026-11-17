package machine

import (
	"runtime"
	"sync"
	"testing"
)

// The user's processes and their limit are read without starting one.
func TestUserProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows sets no limit on a user's processes")
	}
	pc, err := userProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if pc.used == 0 {
		t.Fatalf("no processes counted: %+v", pc)
	}
	if pc.limit != 0 && pc.used > pc.limit*2 {
		t.Fatalf("more processes than the limit allows: %+v", pc)
	}
}

func TestNearLimit(t *testing.T) {
	for _, c := range []struct {
		used, limit uint64
		near        bool
	}{{2669, 2666, true}, {2400, 2666, true}, {2300, 2666, false}, {5, 0, false}} {
		if got := nearLimit(c.used, c.limit); got != c.near {
			t.Errorf("nearLimit(%d, %d) = %v", c.used, c.limit, got)
		}
	}
}

// On Linux the limit on a user's processes counts their threads: the count
// it is held against does too, so a computer full of threads is seen full.
func TestUserProcessesCountThreads(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux's RLIMIT_NPROC")
	}
	before, err := userProcesses()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var started sync.WaitGroup
	for i := 0; i < 50; i++ {
		started.Add(1)
		go func() {
			runtime.LockOSThread() // a thread of its own, held
			started.Done()
			<-stop
		}()
	}
	started.Wait()
	defer close(stop)
	after, err := userProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if after.used < before.used+50 {
		t.Fatalf("50 more threads, counted %d then %d", before.used, after.used)
	}
}
