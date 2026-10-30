package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/flock"
)

// The task runs the windowless daemon with its arguments quoted for the
// command line, and its file reads back as written.
func TestTaskXML(t *testing.T) {
	x := taskXML(`C:\Users\A B\AppData\Local\Programs\Hermesh\bin\hermeshdw.exe`, []string{"run", "--supervise", "--data-dir", `C:\Users\A B\AppData\Local\Hermesh`}, "S-1-5-21-1")
	for _, want := range []string{
		`<Command>C:\Users\A B\AppData\Local\Programs\Hermesh\bin\hermeshdw.exe</Command>`,
		`<Arguments>run --supervise --data-dir "C:\Users\A B\AppData\Local\Hermesh"</Arguments>`,
		`<UserId>S-1-5-21-1</UserId>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("no %s in\n%s", want, x)
		}
	}
	if got := fromUTF16(utf16File(x)); got != x {
		t.Fatalf("read back as %q", got)
	}
}

// A second supervisor of a data directory leaves at once; the only one
// ends when the daemon stops cleanly.
func TestSuperviseOnce(t *testing.T) {
	dir := t.TempDir()
	supervise := func(code string) error {
		done := make(chan error, 1)
		go func() { done <- Supervise(dir, "cmd.exe", []string{"/c", "exit", code}) }()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("still supervising")
			return nil
		}
	}
	lock, err := flock.Acquire(filepath.Join(dir, "run", "supervisor"))
	if err != nil {
		t.Fatal(err)
	}
	if err := supervise("1"); err == nil {
		t.Fatal("a second supervisor ran")
	}
	lock.Release()
	if err := supervise("0"); err != nil {
		t.Fatal(err)
	}
}
