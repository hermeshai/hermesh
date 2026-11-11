package herhost

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// Erasing her data deletes her home, as her environment names it, and on a
// Mac her Keychain items, unless she was kept off the Keychain; never her
// home while she runs, nor a home that is the user's own.
func TestErase(t *testing.T) {
	var deleted []string
	old := deleteKeychainItems
	deleteKeychainItems = func(service string) error { deleted = append(deleted, service); return nil }
	t.Cleanup(func() { deleteKeychainItems = old })

	home := filepath.Join(t.TempDir(), "her-home")
	os.MkdirAll(filepath.Join(home, "bots", "b1"), 0o700)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"x\"\n"), 0o600)
	env := []string{"HER_HOME=" + home}
	h := New(Config{Env: func() []string { return env }})

	h.mu.Lock()
	h.stop = func() {}
	h.mu.Unlock()
	if err := h.Erase(); err == nil {
		t.Fatal("erased while her runs")
	}
	h.mu.Lock()
	h.stop = nil
	h.mu.Unlock()

	if err := h.Erase(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("her home stays: %v", err)
	}
	if want := []string{"her"}; runtime.GOOS == "darwin" && !slices.Equal(deleted, want) {
		t.Fatalf("Keychain services erased: %q, want %q", deleted, want)
	}

	deleted = nil
	env = []string{"HER_HOME=" + home, "HER_CREDENTIAL_STORE=file"}
	if err := h.Erase(); err != nil || len(deleted) != 0 {
		t.Fatalf("with a credentials file: %v, Keychain %q", err, deleted)
	}

	userHome, _ := os.UserHomeDir()
	env = []string{"HER_HOME=" + userHome}
	if err := h.Erase(); err == nil {
		t.Fatal("erased the user's home")
	}
}
