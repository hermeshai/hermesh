package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// A hook names hermesh in words every Windows shell reads.
func TestHookCommand(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "hermesh.exe")
	os.WriteFile(exe, nil, 0o755)
	t.Setenv("PATH", dir)
	if got := hookCommand(exe); got != "hermesh" {
		t.Errorf("on the PATH: %q", got)
	}
	t.Setenv("PATH", "")
	if got, want := hookCommand(`C:\Users\dev\bin\hermesh.exe`), "C:/Users/dev/bin/hermesh.exe"; got != want {
		t.Errorf("off the PATH: %q, want %q", got, want)
	}
	if got, want := hookCommand(`C:\Users\A B\bin\hermesh.exe`), `"C:/Users/A B/bin/hermesh.exe"`; got != want {
		t.Errorf("with a space: %q, want %q", got, want)
	}
}
