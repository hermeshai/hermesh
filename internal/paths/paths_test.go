package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

// Two machines of one user never share a control or tools endpoint,
// however long their data directories.
func TestMachineEndpointsArePerDataDir(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	a, b := filepath.Join(long, "a"), filepath.Join(long, "b")
	for _, f := range []func(string) string{Control, func(d string) string { return Tools(d, "m_"+filepath.Base(d)+"0123456789abcdef") }} {
		if f(a) == f(b) {
			t.Fatalf("%s is shared", f(a))
		}
	}
}

// A machine's endpoint for its own user's agents is named by the machine
// alone, short enough for a Unix socket, movable, and none when turned off.
func TestLocalEndpoint(t *testing.T) {
	const a, b = "m_abcdefghijklmnopqrstuvwx", "m_zyxwvutsrqponmlkjihgfedc"
	t.Setenv("HERMESH_LOCAL", "")
	t.Setenv("HERMESH_LOCAL_DIR", "")
	if Local(a) == Local(b) || Local(a) != Local(a) || !strings.Contains(Local(a), a) || len(Local(a)) > 100 {
		t.Fatalf("%s, %s", Local(a), Local(b))
	}
	if Local("") != "" {
		t.Fatal("an endpoint for no machine")
	}
	t.Setenv("HERMESH_LOCAL_DIR", "/tmp/elsewhere")
	if got := Local(a); filepath.Dir(got) != filepath.FromSlash("/tmp/elsewhere") {
		t.Fatal(got)
	}
	t.Setenv("HERMESH_LOCAL", "off")
	if got := Local(a); got != "" {
		t.Fatalf("%s while off", got)
	}
}
