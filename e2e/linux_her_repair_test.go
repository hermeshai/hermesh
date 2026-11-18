package e2e

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

const fedoraServerImage = `FROM fedora:43
RUN dnf -y install systemd dbus dbus-broker openssl procps-ng util-linux && dnf clean all
CMD ["/sbin/init"]
`

func TestLinuxHerRepair(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	tmp := filepath.Join(t.TempDir(), "her")
	build := exec.Command("go", "build", "-o", tmp, "github.com/hermeshai/hermesh/e2e/testdata/her-startup")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Her fixture: %v\n%s", err, b)
	}
	her, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}

	w := newOwnerMac(t, nil, &localv1.GrantSpec{})
	c := w.app.client
	r := newTestReleases(t)
	r.add(t, "0.1.5", nil)
	r.add(t, "0.1.6", map[string][]byte{
		"her-linux-" + runtime.GOARCH:                      her,
		"her-console.tar.gz":                               herConsoleArchive(t, "the repaired console"),
		"her-pdf-linux-" + runtime.GOARCH + ".tar.gz":      herPDFArchive(t, "the PDF worker"),
		"her-computer-linux-" + runtime.GOARCH + ".tar.gz": herConsoleArchive(t, "the computer tools"),
	})
	r.publish("0.1.5")

	name, sh := startLinuxImage(t, "hermesh-e2e-fedora-server:43", fedoraServerImage, "hermesh-her-repair", "her-repair")
	run := func(script string) string {
		t.Helper()
		out, err := sh(script)
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return out
	}
	address := strings.Replace(w.addr, "127.0.0.1", "host.docker.internal", 1)
	install := exec.Command("docker", "exec", name, "sh", "-c", r.installCommand(address+" --name her-repair"))
	var output syncBuffer
	install.Stdout, install.Stderr = &output, &output
	if err := install.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { install.Process.Kill(); install.Wait() })
	var code string
	eventually(t, time.Minute, "pairing code", func() bool {
		if m := codeRE.FindStringSubmatch(output.String()); m != nil {
			code = m[1]
		}
		return code != ""
	})
	ctx := context.Background()
	approved, err := c.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "her-repair"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil || !strings.Contains(output.String(), "✓ Background service on") {
		t.Fatalf("install 0.1.5: %v\n%s", err, output.String())
	}
	id := approved.MachineId
	listed := func() *localv1.Machine { return listedMachine(t, c, id) }
	eventually(t, 30*time.Second, "0.1.5 online", func() bool {
		m := listed()
		return m.GetOnline() && m.GetStatus().GetVersion() == "0.1.5"
	})
	if _, err := c.SetHer(ctx, &localv1.SetHerRequest{MachineId: id, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "the owner sees Her is missing", func() bool {
		h := listed().GetHer()
		return h.GetEnabled() && h.GetUnsupported() != ""
	})
	if err := listed().GetHer().GetError(); !strings.Contains(err, "not installed") {
		t.Errorf("the owner sees no startup error for missing Her: %q", err)
	}
	call := func(code connect.Code, message string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := c.HerCall(ctx, &localv1.HerCallRequest{MachineId: id, Call: &herv1.CallRequest{Request: herSetup("models/list", nil)}})
		if connect.CodeOf(err) != code || !strings.Contains(err.Error(), message) {
			t.Errorf("HerCall: %v", err)
		}
	}
	call(connect.CodeFailedPrecondition, "not installed")

	ready := func() bool {
		m := listed()
		return m.GetOnline() && m.GetStatus().GetVersion() == "0.1.6" && m.GetHer().GetRunning() && m.GetHer().GetError() == ""
	}
	checkFiles := func() {
		t.Helper()
		run("cd /root/.local/bin && test \"$(./hermesh version)\" = 0.1.6 && test \"$(./.hermesh.previous version)\" = 0.1.5 && ./her --version && ./her-pdf/her-doc-worker && test -f her-computer/index.html && grep -q 'the repaired console' her-console/index.html && test \"$(./.hermeshd.previous version)\" = 0.1.5")
	}
	r.publish("0.1.6")
	eventually(t, time.Minute, "Her running on 0.1.6 after the automatic upgrade", ready)
	checkFiles()

	// An installation already at the feed's version, with the files absent.
	systemctl := "XDG_RUNTIME_DIR=/run/user/0 systemctl --user "
	run(systemctl + "stop hermeshd.service")
	run("cd /root/.local/bin && rm -r her her-console her-pdf her-computer")
	run(systemctl + "start hermeshd.service")
	eventually(t, time.Minute, "same-version automatic repair restored the files", func() bool {
		_, err := sh("test -x /root/.local/bin/her && test -f /root/.local/bin/her-console/index.html")
		return err == nil
	})
	eventually(t, time.Minute, "Her running again after repair", ready)
	checkFiles()

	// A manual repair adds a missing console without replacing Her or the backups.
	run("/root/.local/bin/hermeshd update auto off && cd /root/.local/bin && mv her-console .her-console.previous")
	herInode := run("stat -c %i /root/.local/bin/her")
	run("test ! -e /root/.local/bin/her-console")
	console, err := c.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: id})
	if err != nil {
		t.Fatal(err)
	}
	if status, text := httpDo(t, http.MethodGet, console.Url, ""); status != http.StatusNotFound || !strings.Contains(text, "not installed") {
		t.Errorf("missing console: HTTP %d %s", status, text)
	}
	repairConsole := func() {
		t.Helper()
		if out := run("/root/.local/bin/hermeshd update --check"); !strings.Contains(out, "missing Her components") {
			t.Errorf("--check: %s", out)
		}
		if out := run("/root/.local/bin/hermeshd update --yes"); !strings.Contains(out, "verified her-console") || !strings.Contains(out, "service restarted on 0.1.6") {
			t.Errorf("manual repair: %s", out)
		}
	}
	repairConsole()
	checkFiles()

	// An empty console folder also needs repair, with the first backup intact.
	backupInode := run("stat -c %i /root/.local/bin/.her-console.previous/index.html")
	run("cd /root/.local/bin/her-console && rm index.html && test -z \"$(ls -A)\"")
	repairConsole()
	checkFiles()
	if got := run("stat -c %i /root/.local/bin/.her-console.previous/index.html"); got != backupInode {
		t.Errorf("manual repair replaced the console backup: inode %s → %s", backupInode, got)
	}
	if got := run("stat -c %i /root/.local/bin/her"); got != herInode {
		t.Errorf("manual repair replaced her: inode %s → %s", herInode, got)
	}
	console, err = c.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: id})
	if err != nil {
		t.Fatal(err)
	}
	if status, text := httpDo(t, http.MethodGet, console.Url, ""); status != http.StatusOK || !strings.Contains(text, "the repaired console") {
		t.Errorf("repaired console: HTTP %d %s", status, text)
	}
	if out := run("/root/.local/bin/hermeshd update --check"); !strings.Contains(out, "is the latest") {
		t.Errorf("complete console: %s", out)
	}

	// The kernel can answer initialize and then stop answering startup RPCs.
	run("mkdir -p /root/.her && touch /root/.her/stall-startup")
	run(systemctl + "restart hermeshd.service")
	eventually(t, 15*time.Second, "the owner sees the startup timeout", func() bool {
		h := listed().GetHer()
		return h.GetEnabled() && !h.GetRunning() && strings.Contains(h.GetError(), "bots/subscribe")
	})
	call(connect.CodeUnavailable, "bots/subscribe")
	run("rm /root/.her/stall-startup")
	eventually(t, 30*time.Second, "Her recovers on its next start", ready)
}
