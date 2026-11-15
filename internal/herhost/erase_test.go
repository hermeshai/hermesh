package herhost

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Erasing her data deletes her home, as her environment names it, and on a
// Mac her Keychain items, unless she was kept off the Keychain; never her
// home while she runs, nor a home that may hold the user's own files: one
// her did not make, ~/.her aside.
func TestErase(t *testing.T) {
	var deleted []string
	old := deleteKeychainItems
	deleteKeychainItems = func(service string) error { deleted = append(deleted, service); return nil }
	t.Cleanup(func() { deleteKeychainItems = old })

	home := filepath.Join(t.TempDir(), "her-home")
	os.MkdirAll(filepath.Join(home, "bots", "b1"), 0o700)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"x\"\n"), 0o600)
	os.WriteFile(filepath.Join(home, mark), nil, 0o600) // as her marks a home she makes
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

	// Gone already: nothing to erase.
	if err := h.Erase(); err != nil {
		t.Fatalf("a home already gone: %v", err)
	}

	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome) // os.UserHomeDir on Windows
	env = []string{"HER_HOME=" + userHome}
	if err := h.Erase(); err == nil {
		t.Fatal("erased the user's home")
	}

	// A folder the owner keeps their own files in, her's beside them.
	docs := filepath.Join(userHome, "Documents")
	os.MkdirAll(filepath.Join(docs, "bots", "b1"), 0o700)
	os.WriteFile(filepath.Join(docs, "taxes.pdf"), []byte("x"), 0o600)
	env = []string{"HER_HOME=" + docs, "HER_CREDENTIAL_STORE=file"}
	if err := h.Erase(); err == nil || !strings.Contains(err.Error(), "not made by her") {
		t.Fatalf("erased a folder her did not make: %v", err)
	}
	for _, p := range []string{"taxes.pdf", "bots"} {
		if _, err := os.Stat(filepath.Join(docs, p)); err != nil {
			t.Fatalf("%s after a refused erase: %v", p, err)
		}
	}

	// ~/.her is hers by its name, marked or not.
	dotHer := filepath.Join(userHome, ".her")
	os.MkdirAll(filepath.Join(dotHer, "sessions"), 0o700)
	env = []string{"HER_CREDENTIAL_STORE=file"}
	if err := h.Erase(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dotHer); !os.IsNotExist(err) {
		t.Fatalf("~/.her stays: %v", err)
	}
}
