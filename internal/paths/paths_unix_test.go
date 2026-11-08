//go:build unix

package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

// Sockets have a short path limit: a long data directory's control socket
// moves to the temp folder.
func TestControlSocketIsPerMachine(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 120)
	a, b := Control(long+"/a"), Control(long+"/b")
	if a == b || len(a) > 100 {
		t.Fatalf("%s, %s", a, b)
	}
	if got := Control("/home/dev/.local/share/hermesh/machine"); got != "/home/dev/.local/share/hermesh/machine/control.sock" {
		t.Fatal(got)
	}
}

// her's socket is in a directory of its own (her makes it private), under
// her's limit of 103 bytes whatever the data directory.
func TestHerSocket(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 120)
	a, b := Her(long+"/a"), Her(long+"/b")
	if a == b || len(a) > 103 || filepath.Base(a) != "her.sock" || strings.HasPrefix(a, long) {
		t.Fatalf("%s, %s", a, b)
	}
	if got := Her("/home/dev/.local/share/hermesh/machine"); got != "/home/dev/.local/share/hermesh/machine/her/her.sock" {
		t.Fatal(got)
	}
}
