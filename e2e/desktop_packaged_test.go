package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestDesktopPackaged checks a packaged Hermesh.app (HERMESH_DESKTOP_APP,
// built by apps/desktop/scripts/build-app.mjs) against sessions played by a
// scripted agent: it starts, shows them, and runs its bundled signer. With
// HERMESH_DESKTOP_DMG, also mount the signed disk image, check its layout
// and start its app from the read-only volume.
//
//	HERMESH_DESKTOP_APP=$PWD/dist/Hermesh.app go test ./e2e -run TestDesktopPackaged -v
func TestDesktopPackaged(t *testing.T) {
	bundle := os.Getenv("HERMESH_DESKTOP_APP")
	if bundle == "" {
		t.Skip("set HERMESH_DESKTOP_APP to a packaged Hermesh.app")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp"), "FAKE_ACP_MODELS=gemini-3-pro"},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	f := desktopFixture{Socket: w.sock, Sessions: desktopSessions(t, w), CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv}
	file := filepath.Join(t.TempDir(), "fixture.json")
	b, _ := json.Marshal(f)
	os.WriteFile(file, b, 0o644)
	dir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	env := []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_DESKTOP_APP=" + bundle}
	if r := run(t, env, 5*time.Minute, "pnpm", "--dir", dir, "exec", "playwright", "test", "packaged.spec.ts"); r.code != 0 {
		t.Fatalf("playwright:\n%s\n%s", r.stdout, r.stderr)
	} else {
		t.Log(r.stdout)
	}
}
