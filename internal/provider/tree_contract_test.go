package provider

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Stopping an agent ends its whole tree, its tools included, on every
// system (process groups on Unix, job objects on Windows).
func TestStopEndsTheTree(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartProc(context.Background(), exe, []string{"-test.run=^$"}, "", append(os.Environ(), "HERMESH_TEST_TREE=parent"))
	if err != nil {
		t.Fatal(err)
	}
	child := make(chan int, 1)
	go p.Lines(func(b []byte) {
		if pid, ok := strings.CutPrefix(string(b), "child "); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(pid))
			child <- n
		}
	})
	var pid int
	select {
	case pid = <-child:
	case <-p.Done():
		t.Fatalf("the agent exited: %v, %s", p.Err(), p.StderrTail())
	case <-time.After(20 * time.Second):
		t.Fatal("no child")
	}
	if !running(pid) {
		t.Fatalf("child %d is not running", pid)
	}
	p.Stop(time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for running(pid) || running(p.Pgid()) {
		if time.Now().After(deadline) {
			t.Fatalf("still running: agent %d %v, child %d %v", p.Pgid(), running(p.Pgid()), pid, running(pid))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
