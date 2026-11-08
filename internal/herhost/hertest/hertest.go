// Package hertest runs a private her for tests: her and her-mock from the
// repository's debug build (her/target/debug), or from $HERMESH_HER_BIN's
// folder. Tests that need her are skipped without them.
package hertest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Bin is the her to test with: $HERMESH_HER_BIN, else her/target/debug/her
// in this repository, with her-mock beside it; the test is skipped without.
func Bin(t testing.TB) string {
	bin := os.Getenv("HERMESH_HER_BIN")
	if bin == "" {
		dir, _ := os.Getwd()
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				break
			}
			if dir == filepath.Dir(dir) {
				t.Skip("no her to test with: not in the repository")
			}
			dir = filepath.Dir(dir)
		}
		bin = filepath.Join(dir, "her", "target", "debug", "her")
	}
	for _, p := range []string{bin, filepath.Join(filepath.Dir(bin), "her-mock")} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("no her to test with (%v): build her/ or set HERMESH_HER_BIN", err)
		}
	}
	return bin
}

var baseURL = regexp.MustCompile(`base_url = "(http://[^"]+)/v1"`)

// Home starts her-mock, makes a HER_HOME whose model is its mock/mock-1,
// and sets in the test's environment what her reads: that home, a
// credential store in a file, no browser, English and her-mock's model
// catalog. A prompt with "[[mock:send:TEXT]]" has an assistant answer TEXT
// (her/crates/her-mock).
func Home(t testing.TB, bin string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(filepath.Dir(bin), "her-mock"), "127.0.0.1:0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	// "# models.toml (model: mock/mock-1)", then the file, ending with the
	// model's cost.
	r := bufio.NewReader(out)
	var models strings.Builder
	for first := true; ; first = false {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("her-mock: %v (so far %q)", err, models.String())
		}
		if first {
			continue
		}
		models.WriteString(line)
		if strings.HasPrefix(line, "cost") {
			break
		}
	}
	m := baseURL.FindStringSubmatch(models.String())
	if m == nil {
		t.Fatalf("her-mock printed no base_url: %s", models.String())
	}
	home := t.TempDir()
	for name, text := range map[string]string{"models.toml": models.String(), "config.toml": "model = \"mock/mock-1\"\n"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"HER_HOME": home, "HER_CREDENTIAL_STORE": "file", "HER_NO_BROWSER": "1", "HER_LANG": "en",
		"HER_MODEL_CATALOG_URL": m[1] + "/catalog/api.json"} {
		t.Setenv(k, v)
	}
	return home
}
