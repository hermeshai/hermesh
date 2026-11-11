package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestLinuxHer (HERMESH_DOCKER=1) is the first step of phase 4 of
// docs/her-server.md (§11): a fresh Ubuntu server with systemd becomes an
// environment with the install command, run as root, and stays one across
// an update. Two signed releases A < B carry her and her console:
//
//  1. the install command installs hermeshd, hermesh, her and the console
//     from A, and pairs the machine;
//  2. the owner turns Her on there (SetHer, signed by the test's signer):
//     hermeshd finds her and its console beside itself, with no
//     HERMESH_HER_BIN or HERMESH_HER_CONSOLE, and Her answers, her-mock its
//     model;
//  3. B is published while Her is at work: the machine waits for her, then
//     updates hermeshd, her and the console together, and Her answers on B;
//  4. `hermeshd uninstall` removes her and the console, and leaves ~/.her.
//
// The releases' her is HERMESH_HER_LINUX, else built in Docker (linuxHer).
func TestLinuxHer(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	mock := startHerMock(t, herBinary(t, "HERMESH_HER_MOCK", "her-mock"))
	her, err := os.ReadFile(linuxHer(t))
	if err != nil {
		t.Fatal(err)
	}
	w := newOwnerMac(t, nil, &localv1.GrantSpec{})
	c := w.app.client
	ctx := context.Background()

	// A and B, B's her another file: A's with a byte more.
	releases := newTestReleases(t)
	herAsset := "her-linux-" + runtime.GOARCH
	herSum := map[string]string{}
	for i, v := range []string{"0.0.1", "0.0.2"} {
		b := append(slices.Clip(her), bytes.Repeat([]byte{0}, i)...)
		h := sha256.Sum256(b)
		herSum[v] = hex.EncodeToString(h[:])
		releases.add(t, v, map[string][]byte{herAsset: b, "her-console.tar.gz": herConsoleArchive(t, "the console of "+v)})
	}
	releases.publish("0.0.1")

	name, sh := startLinuxServer(t, "hermesh-her", "her-1")
	put := func(path, content string) {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", name, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path)
		cmd.Stdin = strings.NewReader(content)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("writing %s: %v %s", path, err, out)
		}
	}
	// her-mock's models in Her's home, as a model the owner added in the
	// console, and her's settings for a test in the service's environment.
	put("/root/.her/models.toml", strings.ReplaceAll(mock.models, "127.0.0.1", "host.docker.internal"))
	put("/root/.her/config.toml", "model = \"mock/mock-1\"\n")
	put("/root/.config/systemd/user/hermeshd.service.d/her.conf", "[Service]\nEnvironment=HER_NO_BROWSER=1 HER_LANG=en HER_MODEL_CATALOG_URL="+
		strings.Replace(mock.base, "127.0.0.1", "host.docker.internal", 1)+"/catalog/api.json\n")

	// 1. The install command, as root without a terminal.
	install := exec.Command("docker", "exec", name, "sh", "-c", releases.installCommand(strings.Replace(w.addr, "127.0.0.1", "host.docker.internal", 1)+" --name her-1"))
	var out syncBuffer
	install.Stdout, install.Stderr = &out, &out
	if err := install.Start(); err != nil {
		t.Fatal(err)
	}
	var code string
	eventually(t, 2*time.Minute, "pairing code", func() bool {
		if m := codeRE.FindStringSubmatch(out.String()); m != nil {
			code = m[1]
		}
		return code != ""
	})
	approved, err := c.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "her-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil || !strings.Contains(out.String(), "› downloading "+herAsset) || !strings.Contains(out.String(), "✓ Background service on") {
		t.Fatalf("install: %v\n%s", err, out.String())
	}
	id := approved.MachineId
	machineVersion := func() string {
		if m := listedMachine(t, c, id); m != nil && m.Online && m.Status != nil {
			return m.Status.Version
		}
		return ""
	}
	eventually(t, time.Minute, "her-1 online on 0.0.1", func() bool { return machineVersion() == "0.0.1" })
	status := func(want string) {
		t.Helper()
		eventually(t, time.Minute, "status: "+want, func() bool {
			s, _ := sh("/root/.local/bin/hermeshd status")
			return strings.Contains(s, want)
		})
	}
	status("her          0.2.0, off")

	// 2. Her on: she answers, and her console is A's. A Chrome the owner
	// has used on this machine starts her as the Hermesh extension's host,
	// and the extension her carries is where the console's guide says.
	if _, err := sh("mkdir -p /root/.config/google-chrome"); err != nil {
		t.Fatal(err)
	}
	turnHerOn(t, c, id)
	eventually(t, time.Minute, "her registered with Chrome, and the extension in its folder", func() bool {
		host, _ := sh("cat /root/.config/google-chrome/NativeMessagingHosts/com.hermesh.browser.json")
		extension, _ := sh("cat /root/.local/share/hermesh/chrome-extension/manifest.json")
		return strings.Contains(host, "/root/.her/browser/native-host") && strings.Contains(extension, `"name": "Hermesh"`)
	})
	if s, err := sh("cat /root/.her/browser/native-host"); err != nil || !strings.Contains(s, "/root/.local/bin/her' browser native-host --socket ") {
		t.Fatalf("the host Chrome starts: %v\n%s", err, s)
	}
	bot := defaultBot(t, c, id)
	talk(t, c, id, bot.ID, "hello from her-1")
	console := func(want string) {
		t.Helper()
		con, err := c.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: id})
		if err != nil {
			t.Fatal(err)
		}
		if code, page := httpDo(t, http.MethodGet, con.Url, ""); code != 200 || !strings.Contains(page, want) {
			t.Fatalf("the console: %d %s", code, page)
		}
	}
	console("the console of 0.0.1")
	status("her          0.2.0, on, running")
	if env, _ := sh(`tr '\0' '\n' < /proc/$(pgrep -o -x hermeshd)/environ | grep HERMESH_HER`); env != "" {
		t.Fatalf("hermeshd runs with %s", env)
	}

	// 3. B while Her is at work, for half a minute: the machine waits.
	typing := func() bool {
		bots, _ := herBots(c, id)
		return slices.ContainsFunc(bots, func(b herBot) bool { return b.ID == bot.ID && b.Typing })
	}
	if res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: id, BotId: bot.ID, Text: "[[mock:slow:300]] Take your time."}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	eventually(t, 30*time.Second, "Her at work", typing)
	releases.publish("0.0.2")
	eventually(t, 30*time.Second, "the machine finds 0.0.2", func() bool {
		s, _ := sh("cat /root/.local/share/hermesh/update/update.json")
		return strings.Contains(s, `"available": "0.0.2"`)
	})
	time.Sleep(5 * time.Second) // more checks, while she works
	if v := machineVersion(); v != "0.0.1" || !typing() {
		t.Fatalf("while Her works: the machine on %q, Her typing %v", v, typing())
	}
	eventually(t, 2*time.Minute, "the machine on 0.0.2 once Her is done", func() bool { return machineVersion() == "0.0.2" })
	if s, _ := sh("sha256sum /root/.local/bin/her; ls -a /root/.local/bin"); !strings.HasPrefix(s, herSum["0.0.2"]) || !strings.Contains(s, ".her.previous") || !strings.Contains(s, ".her-console.previous") {
		t.Errorf("her after the update: %s", s)
	}
	eventually(t, 2*time.Minute, "Her running again", func() bool {
		h := listedMachine(t, c, id).GetHer()
		return h.GetRunning() && h.GetModelReady()
	})
	talk(t, c, id, bot.ID, "hello again")
	console("the console of 0.0.2")

	// 4. Uninstalled: her goes, her data stays.
	s, err := sh("XDG_RUNTIME_DIR=/run/user/0 USER=root /root/.local/bin/hermeshd uninstall --yes 2>&1")
	if err != nil || !strings.Contains(s, "/root/.local/bin/hermeshd, /root/.local/bin/her, /root/.local/bin/her-console") ||
		!strings.Contains(s, "Her's assistants, conversations and settings, in /root/.her") || !strings.Contains(s, "✓ Hermesh is uninstalled for root.") {
		t.Fatalf("uninstall: %v\n%s", err, s)
	}
	if left, _ := sh("pgrep -l her; ls -d /root/.local/bin/her /root/.local/bin/her-console /root/.config/google-chrome/NativeMessagingHosts/* /root/.local/share/hermesh/chrome-extension 2>/dev/null"); left != "" {
		t.Errorf("left after uninstalling:\n%s", left)
	}
	if _, err := sh("test -f /root/.her/models.toml"); err != nil {
		t.Error("Her's data went with Hermesh")
	}
}

// herConsoleArchive is her-console.tar.gz as scripts/release.sh makes it
// (tar -C dist .), with an index.html that says text.
func herConsoleArchive(t *testing.T, text string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	page := "<!doctype html><title>Her console</title>" + text + "\n"
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./", Mode: 0o755})
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "./index.html", Mode: 0o644, Size: int64(len(page))})
	tw.Write([]byte(page))
	if tw.Close() != nil || zw.Close() != nil {
		t.Fatal("console archive")
	}
	return b.Bytes()
}

// linuxHer is her for Linux on this computer's architecture, as a release
// carries it: HERMESH_HER_LINUX, else built in Docker as the release
// workflow builds it, unoptimized and stripped, with cargo's caches kept in
// Docker volumes for the next run.
func linuxHer(t *testing.T) string {
	t.Helper()
	if her := os.Getenv("HERMESH_HER_LINUX"); her != "" {
		return her
	}
	toml, err := os.ReadFile("her/rust-toolchain.toml")
	if err != nil {
		t.Fatal(err)
	}
	toolchain := regexp.MustCompile(`channel = "(.+)"`).FindSubmatch(toml)[1]
	triple := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH] + "-unknown-linux-musl"
	src, _ := filepath.Abs("her")
	out := t.TempDir()
	start := time.Now()
	build := exec.Command("docker", "run", "--rm", "-v", src+":/src:ro", "-v", out+":/out", "-v", "hermesh-e2e-her-target:/target",
		"-v", "hermesh-e2e-cargo-registry:/usr/local/cargo/registry", "-v", "hermesh-e2e-cargo-git:/usr/local/cargo/git",
		"-e", "CARGO_TARGET_DIR=/target", "-e", fmt.Sprintf("RUSTUP_TOOLCHAIN=%s-%s", toolchain, triple), "-w", "/src", fmt.Sprintf("rust:%s-alpine", toolchain),
		"sh", "-c", "apk add --no-cache build-base perl linux-headers git >/dev/null && cargo build --locked -p her-cli --bin her --no-default-features && strip -o /out/her /target/debug/her")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building her for Linux: %v\n%s", err, b)
	}
	t.Logf("her for Linux built in %v", time.Since(start).Round(time.Second))
	return filepath.Join(out, "her")
}
