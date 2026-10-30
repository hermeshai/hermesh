package localipc_test

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/localipc"
)

// An endpoint takes this user's connections, tells who is at the other end,
// and can be served again at the same path after a restart.
func TestEndpoint(t *testing.T) {
	dir, err := os.MkdirTemp("", "ipc") // short: socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run", "test.sock")
	for round := 0; round < 2; round++ {
		ln, err := localipc.Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		peer := make(chan localipc.Process, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			p, _ := localipc.Peer(c)
			peer <- p
			line, _ := bufio.NewReader(c).ReadString('\n')
			c.Write([]byte(line))
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := localipc.Dial(ctx, path)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("ping\n"))
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if got, err := bufio.NewReader(c).ReadString('\n'); err != nil || got != "ping\n" {
			t.Fatalf("round %d: %q, %v", round, got, err)
		}
		c.Close()
		if p := <-peer; p.PID != os.Getpid() {
			t.Fatalf("round %d: peer %+v, want pid %d", round, p, os.Getpid())
		}
		if runtime.GOOS != "windows" {
			if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
				t.Fatalf("round %d: socket mode %v, %v", round, st.Mode(), err)
			}
		}
		ln.Close()
	}
}
