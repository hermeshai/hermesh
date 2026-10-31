package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestDesktopUpdate updates the desktop app the way owners get updates:
// it builds two Developer ID–signed Hermesh.app versions (A < B) whose
// feed is a local server, serves B's zip and feed as a release would, and
// has Playwright drive A through finding, downloading and installing B
// (apps/desktop/e2e/update.spec.ts). Opt-in: it needs the Developer ID
// certificate in the keychain, and takes 4 to 6 minutes (two builds of 2
// to 3 minutes each, then the update in under a minute).
//
//	HERMESH_UPDATE_E2E=1 go test ./e2e -run TestDesktopUpdate -v -timeout 30m
func TestDesktopUpdate(t *testing.T) {
	if os.Getenv("HERMESH_UPDATE_E2E") != "1" {
		t.Skip("set HERMESH_UPDATE_E2E=1 (needs a Developer ID certificate)")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	if out, _ := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output(); !strings.Contains(string(out), "Developer ID Application") {
		t.Skip("no Developer ID Application identity in the keychain")
	}
	const from, to = "0.0.1", "0.0.2"
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]

	// The release server: B's feed and zip, the zip held back a few seconds
	// so the app's 正在下载 state can be seen.
	release := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			time.Sleep(3 * time.Second)
		}
		http.FileServer(http.Dir(release)).ServeHTTP(w, r)
	}))
	defer srv.Close()

	// The apps' data directory, baked into the test builds, points at the
	// fixture's daemon (short: the socket path must fit), so the version
	// Squirrel relaunches finds it too.
	data, err := os.MkdirTemp("", "hmupd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	os.MkdirAll(filepath.Join(data, "run"), 0o700)

	build := func(version string) string {
		out := filepath.Join(t.TempDir(), version)
		env := []string{"VERSION=" + version, "HERMESH_ARCH=" + arch, "HERMESH_DIST=" + out,
			"HERMESH_UPDATE_FEED=" + srv.URL, "HERMESH_RELEASE_URL=" + srv.URL, "HERMESH_TEST_DATA_DIR=" + data,
			// Signed, not notarized: Squirrel checks the signature, not the notarization.
			"HERMESH_NOTARY_PROFILE=", "HERMESH_NOTARY_KEY_PATH="}
		if r := run(t, env, 15*time.Minute, "node", filepath.Join("apps", "desktop", "scripts", "build-app.mjs")); r.code != 0 {
			t.Fatalf("build %s:\n%s\n%s", version, r.stdout, r.stderr)
		}
		return out
	}
	a := build(from)
	b := build(to)
	// Whatever version Squirrel left running, should the test fail.
	defer exec.Command("pkill", "-f", filepath.Join(a, "Hermesh.app", "Contents", "MacOS", "Hermesh")).Run()
	for _, f := range []string{"Hermesh-macos-" + arch + ".zip", "RELEASES-darwin-" + arch + ".json"} {
		if r := run(t, nil, time.Minute, "cp", filepath.Join(b, f), release); r.code != 0 {
			t.Fatalf("cp %s: %+v", f, r)
		}
	}

	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp"), "FAKE_ACP_MODELS=gemini-3-pro"},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	f := desktopFixture{Socket: w.sock, Sessions: desktopSessions(t, w), CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv}
	file := filepath.Join(t.TempDir(), "fixture.json")
	js, _ := json.Marshal(f)
	os.WriteFile(file, js, 0o644)
	if err := os.Symlink(w.sock, filepath.Join(data, "run", "hermeshd.sock")); err != nil {
		t.Fatal(err)
	}

	dir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	env := []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_UPDATE_APP=" + filepath.Join(a, "Hermesh.app"),
		"HERMESH_UPDATE_FROM=" + from, "HERMESH_UPDATE_TO=" + to, "HERMESH_UPDATE_DATA=" + data,
		"HERMESH_PW_OUT=" + filepath.Join(t.TempDir(), "results")}
	if r := run(t, env, 10*time.Minute, "pnpm", "--dir", dir, "exec", "playwright", "test", "update.spec.ts"); r.code != 0 {
		t.Fatalf("playwright:\n%s\n%s", r.stdout, r.stderr)
	} else {
		t.Log(r.stdout)
	}
}
