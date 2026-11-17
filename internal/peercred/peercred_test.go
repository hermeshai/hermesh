//go:build darwin || linux

package peercred

import (
	"os"
	"runtime"
	"testing"
)

// The local API takes its own user's connections, and on Linux root's (a
// trusted device's protected signer); never another user's.
func TestAdmitted(t *testing.T) {
	if !admitted(os.Getuid()) {
		t.Error("its own user is not admitted")
	}
	if admitted(os.Getuid() + 4242) {
		t.Error("another user is admitted")
	}
	if os.Getuid() != 0 && admitted(0) != (runtime.GOOS == "linux") {
		t.Errorf("root admitted: %v on %s", admitted(0), runtime.GOOS)
	}
}
