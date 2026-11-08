//go:build unix

package herhost

import (
	"encoding/json"
	"io"
	"os"
	"syscall"
	"time"
)

// stopStale stops a her still holding lock, the lock file her keeps beside
// its socket while it runs ({"pid": …}): one an earlier hermeshd left
// running, which would keep the socket from a new her. A lock nobody holds
// is no her's, whatever pid it names.
func stopStale(lock string) {
	f, err := os.Open(lock)
	if err != nil {
		return
	}
	defer f.Close()
	held := func() bool {
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			return true
		}
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	if !held() {
		return
	}
	var rec struct {
		PID int `json:"pid"`
	}
	b, _ := io.ReadAll(f)
	if json.Unmarshal(b, &rec) != nil || rec.PID <= 1 {
		return
	}
	// her has a process group of its own, with what it started.
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		syscall.Kill(-rec.PID, sig)
		syscall.Kill(rec.PID, sig)
		for range 50 {
			if !held() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}
