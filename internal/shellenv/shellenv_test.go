package shellenv

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Without drops a variable by its name, and on Windows whatever its case.
func TestWithout(t *testing.T) {
	env := []string{"PATH=/a", "Path=/b", "HOME=/h", "PATHX=/c"}
	got := Without(env, "PATH")
	want := []string{"Path=/b", "HOME=/h", "PATHX=/c"}
	if runtime.GOOS == "windows" {
		want = []string{"HOME=/h", "PATHX=/c"}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

// A session's environment has one PATH, the resolved one, and its own
// temp folder.
func TestEnv(t *testing.T) {
	env := Env("/tmp/session")
	var paths, temps int
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if sameName(name, "PATH") {
			paths++
			if value != Path() {
				t.Fatalf("PATH %q, want %q", value, Path())
			}
		}
		for _, v := range tempVars {
			if sameName(name, v) {
				temps++
				if value != "/tmp/session" {
					t.Fatalf("%s", kv)
				}
			}
		}
	}
	if paths != 1 || temps != len(tempVars) {
		t.Fatalf("%d PATH, %d temp variables in %q", paths, temps, env)
	}
}
