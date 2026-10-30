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
