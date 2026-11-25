package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hermeshai/hermesh/internal/shellenv"
)

// TestDesktopColdStart runs apps/desktop/e2e/coldstart.spec.ts: the first run
// on a computer that can run Her and belongs to no one (docs/cold-start-prd.md
// §5): this Mac's agent providers, Her turned on without Touch ID and given a
// model by its key (her-mock answers in DeepSeek's place), the sign-in step
// skipped, and the main window without an account. Her has no model to begin
// with, as on a new computer.
//
//	go test ./e2e -run TestDesktopColdStart -count=1 -v -timeout 30m
func TestDesktopColdStart(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	home := t.TempDir()
	base := regexp.MustCompile(`base_url = "([^"]+)"`).FindStringSubmatch(mock.models)[1]
	if err := os.WriteFile(filepath.Join(home, "models.toml"), []byte(fmt.Sprintf("[providers.deepseek]\nbase_url = %q\n", base)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := herEnv(her, herConsole(t), home, mock.base)
	// No model or provider key from this computer: her starts with none.
	for _, kv := range shellenv.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasSuffix(name, "_API_KEY") || name == "HER_MODEL" {
			env = append(env, name+"=")
		}
	}
	w := &ownerMac{t: t, bin: binaries(t)}
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range w.logs {
				t.Logf("==== %s ====\n%s", p.name, p.output())
			}
		}
	})
	fresh := newFreshOwner(t, w, "cold", false, false, env...)
	t.Cleanup(func() {
		for _, p := range w.logs {
			p.stop() // as a service stops, so that it stops her
		}
	})
	herSpec(t, desktopFixture{Socket: fresh.Socket, Sessions: map[string]string{}, Fresh: []freshOwner{fresh}}, "coldstart.spec.ts")
}
