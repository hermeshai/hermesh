package shellenv

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// A folder goes on the user's PATH once, at the end, and comes off, the
// rest kept as written.
func TestOnUserPath(t *testing.T) {
	read := func() string {
		k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
		if err != nil {
			t.Fatal(err)
		}
		defer k.Close()
		v, _, _ := k.GetStringValue("Path")
		return v
	}
	before := read()
	dir := t.TempDir()
	t.Cleanup(func() { OnUserPath(dir, false) })
	for range 2 {
		if err := OnUserPath(dir, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(); strings.Count(got, dir) != 1 || !strings.HasSuffix(got, ";"+dir) && got != dir {
		t.Fatalf("after add: %q", got)
	}
	if err := OnUserPath(strings.ToUpper(dir), false); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != strings.TrimSuffix(before, ";") && got != before {
		t.Fatalf("after remove: %q, was %q", got, before)
	}
}
