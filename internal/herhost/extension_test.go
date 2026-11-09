package herhost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The app's extension is copied where the console's guide says, once: a
// copy that is the same is left alone, and one that isn't is replaced whole.
func TestSyncFolder(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "chrome-extension"), ExtensionFolder(filepath.Join(dir, "home"))
	write := func(name, text string) {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", `{"version":"0.2.1"}`)
	write("src/background.js", "one")
	if err := syncFolder(src, dst); err != nil {
		t.Fatal(err)
	}
	if !sameFolder(src, dst) {
		t.Fatal("the copy differs from the app's")
	}

	// The same: left alone.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dst, old, old); err != nil {
		t.Fatal(err)
	}
	if err := syncFolder(src, dst); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); !st.ModTime().Equal(old) {
		t.Fatal("a copy that was the same was written again")
	}

	// An update changed a file and dropped another: the copy follows, and nothing is left beside it.
	write("src/background.js", "two")
	if err := os.WriteFile(filepath.Join(dst, "src", "stale.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncFolder(src, dst); err != nil {
		t.Fatal(err)
	}
	if !sameFolder(src, dst) {
		t.Fatal("the copy did not follow the app's")
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Fatalf("the copy's draft is left: %v", err)
	}
}
