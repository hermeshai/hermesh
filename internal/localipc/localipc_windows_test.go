package localipc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/localipc"
)

// The endpoint's file holds a pipe's name, which Dial takes too; once the
// endpoint is closed, dialing fails instead of waiting.
func TestPipeNameFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "test.pipe")
	ln, err := localipc.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(b), `\\.\pipe\hermesh-`) {
		t.Fatalf("%q, %v", b, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := localipc.Dial(ctx, string(b))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	ln.Close()
	if c, err := localipc.Dial(ctx, path); err == nil {
		c.Close()
		t.Fatal("dialed a closed endpoint")
	}
}
