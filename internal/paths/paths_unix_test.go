//go:build unix

package paths

import (
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
