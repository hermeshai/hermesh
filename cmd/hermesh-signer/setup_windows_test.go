package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCopySelfOverRunning: an update puts the new signer in place while
// the old one still runs from there.
func TestCopySelfOverRunning(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "hermesh-signer.exe")
	ping, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, ping, 0o755); err != nil {
		t.Fatal(err)
	}
	old := exec.Command(to, "-n", "30", "127.0.0.1")
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { old.Process.Kill(); old.Wait() }()

	for range 2 { // and again, with the first one's leftover still held
		if err := copySelf(to); err != nil {
			t.Fatal(err)
		}
	}
	self, _ := os.Executable()
	want, _ := os.ReadFile(self)
	if got, _ := os.ReadFile(to); !bytes.Equal(got, want) {
		t.Fatal("the new signer is not in place")
	}
	if left, _ := filepath.Glob(to + ".old-*"); len(left) != 1 {
		t.Fatalf("left aside: %v (want only the running one's)", left)
	}
}
