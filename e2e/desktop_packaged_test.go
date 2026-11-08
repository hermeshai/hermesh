package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestDesktopPackagedHer is Her in a packaged Hermesh.app (HERMESH_DESKTOP_APP),
// without its windows: the app's own hermeshd, given no HERMESH_HER_BIN, runs
// the her beside it as signed, which answers a message, and serves the console
// the app carries. her-mock is the model service (HERMESH_HER_MOCK, else
// her/target/debug).
//
//	HERMESH_DESKTOP_APP=$PWD/dist/Hermesh.app go test ./e2e -run TestDesktopPackagedHer -v
func TestDesktopPackagedHer(t *testing.T) {
	bundle := os.Getenv("HERMESH_DESKTOP_APP")
	if bundle == "" {
		t.Skip("set HERMESH_DESKTOP_APP to a packaged Hermesh.app")
	}
	app := filepath.Join(bundle, "Contents")
	mock := startHerMock(t, herBinary(t, "HERMESH_HER_MOCK", "her-mock"))
	var env []string
	for _, kv := range herEnv("", "", herHome(t, mock.models), mock.base) {
		if !strings.HasPrefix(kv, "HERMESH_HER_") {
			env = append(env, kv)
		}
	}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	w.hermeshd = filepath.Join(app, "Helpers", "hermeshd")
	w.restartOwner(false)
	c := w.app.client
	mac := localMachine(t, c)
	turnHerOn(t, c, mac)
	talk(t, c, mac, defaultBot(t, c, mac).ID, "hello from the app's her")

	// The app's console (Contents/Resources/console) once it carries one;
	// until then hermeshd says it is not installed.
	con, err := c.GetHerConsole(context.Background(), &localv1.GetHerConsoleRequest{MachineId: mac})
	if err != nil {
		t.Fatal(err)
	}
	code, page := httpDo(t, http.MethodGet, con.Url, "")
	switch index, err := os.ReadFile(filepath.Join(app, "Resources", "console", "index.html")); {
	case err == nil && (code != http.StatusOK || page != string(index)):
		t.Fatalf("the app's console: %d %q", code, page)
	case err != nil && code != http.StatusNotFound:
		t.Fatalf("an app without a console: %d %q", code, page)
	}
}
