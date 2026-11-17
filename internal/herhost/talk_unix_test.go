//go:build unix

package herhost

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/herhost/hertest"
)

// syncBuffer is what Talk writes, read while it writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// Her's conversation in a terminal, with a real her (her-mock its model): a
// message goes and her answer shows; an approval she asks for is asked
// there, and the answer typed goes back.
func TestTalk(t *testing.T) {
	bin := hertest.Bin(t)
	hertest.Home(t, bin)
	sock := shortSocket(t)
	her := exec.Command(bin, "serve", "--listen", "unix://"+sock)
	if err := her.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { her.Process.Kill(); her.Wait() })
	eventually(t, func() bool { _, err := os.Stat(sock); return err == nil })

	in, typed := io.Pipe()
	var out syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Talk(ctx, sock, "test", in, &out) }()
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !strings.Contains(out.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("no %q in:\n%s", want, out.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	wait("a message and Enter sends it")
	io.WriteString(typed, "[[mock:send:hello from a terminal]] hi\n")
	wait("Her: hello from a terminal")

	// A command she wants to run asks first, in the terminal.
	io.WriteString(typed, "[[mock:confirm]] run it\n")
	wait("Allow? [y/N]")
	io.WriteString(typed, "y\n")
	wait("做完了：")
	if !strings.Contains(out.String(), "Exit code: 0") {
		t.Fatalf("the command's result:\n%s", out.String())
	}
	typed.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
