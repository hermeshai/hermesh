//go:build unix

package shellenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A daemon's child gets what the login shell exports and the daemon's
// environment lacks, the daemon's own winning, a value with a newline
// whole, and the shell's own variables not at all.
func TestEnvironLoginShell(t *testing.T) {
	home := t.TempDir()
	profile := "export HERMESH_T_LOGIN=from-profile\nexport HERMESH_T_BOTH=from-profile\nexport HERMESH_T_LINES='a\nb'\necho noise\n"
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("ENV", "")
	t.Setenv("HERMESH_T_BOTH", "from-daemon")
	reset := func() {
		mu.Lock()
		path, login, problem = "", nil, ""
		mu.Unlock()
	}
	reset()
	t.Cleanup(reset)

	env := Environ()
	for _, want := range []string{"HERMESH_T_LOGIN=from-profile", "HERMESH_T_BOTH=from-daemon", "HERMESH_T_LINES=a\nb"} {
		if !slices.Contains(env, want) {
			t.Errorf("no %q in %q", want, env)
		}
	}
	paths := 0
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains([]string{"PWD", "SHLVL", "_"}, name) && os.Getenv(name) == "" {
			t.Errorf("the shell's own %q", kv)
		}
		if name == "PATH" {
			paths++
		}
	}
	if paths != 1 {
		t.Errorf("%d PATH variables", paths)
	}
}
