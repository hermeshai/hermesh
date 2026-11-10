package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// screenHelper is hermesh-screen for this Mac: $HERMESH_SCREEN_BIN, else
// built from apps/screen/macos.
func screenHelper(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("HERMESH_SCREEN_BIN"); bin != "" {
		return bin
	}
	dir, err := filepath.Abs("apps/screen/macos") // the tests run from the module's root
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("swift", "build", "-c", "release", "--product", "hermesh-screen")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hermesh-screen: %v\n%s", err, out)
	}
	out, err := exec.Command("swift", "build", "-c", "release", "--show-bin-path", "--package-path", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "hermesh-screen")
}

// screenEnv is what a Mac's hermeshd needs for Computer Control in a test:
// the helper, showing its test picture and writing the input it gets to the
// file returned. With HERMESH_SCREEN_REAL set it shows this Mac's screen and
// posts the input instead, for a look by hand (macOS asks for Screen
// Recording and Accessibility for whatever started the test).
func screenEnv(t *testing.T) (env []string, inputLog string) {
	t.Helper()
	env = []string{"HERMESH_SCREEN_BIN=" + screenHelper(t)}
	if os.Getenv("HERMESH_SCREEN_REAL") != "" {
		return env, ""
	}
	inputLog = filepath.Join(t.TempDir(), "screen-input.jsonl")
	return append(env, "HERMESH_SCREEN_ARGS=--test-source --input-log "+inputLog), inputLog
}
