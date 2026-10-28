package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// InstalledFor finds the service by the data directory Install gave it, and
// not by another one that only starts the same.
func TestInstalledFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := filepath.Join(home, "hermesh")
	if InstalledFor(data) {
		t.Fatal("found with nothing installed")
	}
	path, text := launchdPlist(), plistText("/bin/hermeshd", data, filepath.Join(data, "logs"), nil)
	if runtime.GOOS == "linux" {
		path, text = systemdUnit(), unitText("/bin/hermeshd", data, nil)
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if !InstalledFor(data) {
		t.Fatal("not found for its data directory")
	}
	for _, other := range []string{filepath.Join(data, "machine"), data[:len(data)-1], filepath.Dir(data)} {
		if InstalledFor(other) {
			t.Errorf("found for %s", other)
		}
	}
}
