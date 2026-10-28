package machine

import (
	"strings"
	"testing"
)

// Two machines of one user never share a control socket, however long
// their data directories.
func TestControlSocketIsPerMachine(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 120)
	a, b := ControlSocket(long+"/a"), ControlSocket(long+"/b")
	if a == b || len(a) > 100 {
		t.Fatalf("%s, %s", a, b)
	}
	if got := ControlSocket("/home/dev/.local/share/hermesh/machine"); got != "/home/dev/.local/share/hermesh/machine/control.sock" {
		t.Fatal(got)
	}
}
