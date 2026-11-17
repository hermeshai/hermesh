package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLinuxPackage (HERMESH_DOCKER=1, HERMESH_LINUX_DEB=<a .deb>) is the
// Linux app's package (apps/desktop/scripts/package-linux.mjs) on a fresh
// Debian computer, installed with apt as a person installs it:
//
//  1. the app's folder in /opt/Hermesh, everyone's to read, Chromium's
//     sandbox helper root's and setuid; hermesh and hermeshd on PATH; the
//     desktop entry takes hermesh: links; Her's programs beside hermeshd;
//  2. the signer's socket listens and polkit knows its action;
//  3. a user sets Hermesh up there with the installed hermeshd and signer:
//     her owner key made, a signature asked through polkit;
//  4. removing the package stops the signer and keeps her key; purging it
//     takes the key too.
func TestLinuxPackage(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	deb := os.Getenv("HERMESH_LINUX_DEB")
	if deb == "" {
		t.Skip("set HERMESH_LINUX_DEB to a .deb for this computer's architecture (package-linux.mjs)")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	helper := filepath.Join(tmp, "linuxowner")
	cmd := exec.Command("go", "build", "-o", helper, "./e2e/linuxowner")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)

	name, sh := startLinuxImage(t, "hermesh-e2e-linux-signer:1", linuxSignerImage, "hermesh-package", "desk-2")
	for from, to := range map[string]string{deb: "/opt/hermesh.deb", helper: "/usr/local/bin/linuxowner"} {
		if out, err := exec.Command("docker", "cp", from, name+":"+to).CombinedOutput(); err != nil {
			t.Fatalf("docker cp: %v %s", err, out)
		}
	}
	put := func(path, content string) {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", name, "sh", "-c", `cat > "$1"`, "sh", path)
		cmd.Stdin = strings.NewReader(content)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("writing %s: %v %s", path, err, out)
		}
	}
	put("/usr/local/bin/agent.py", signerAgent)

	// 1, 2. Installed as a person installs it.
	if out, err := sh("DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -o Acquire::Retries=8 /opt/hermesh.deb 2>&1 && dpkg -s hermesh | grep -q 'ok installed'"); err != nil {
		t.Fatalf("apt-get install: %v\n%s", err, out)
	}
	checks := map[string]string{
		"stat -c '%a %U' /opt/Hermesh /opt/Hermesh/bin /opt/Hermesh/resources":                                                              "755 root\n755 root\n755 root",
		"stat -c '%a %U' /opt/Hermesh/chrome-sandbox":                                                                                       "4755 root",
		"readlink /usr/bin/hermesh /usr/bin/hermeshd":                                                                                       "/opt/Hermesh/bin/hermesh\n/opt/Hermesh/bin/hermeshd",
		"grep -c 'x-scheme-handler/hermesh' /usr/share/applications/hermesh.desktop":                                                        "1",
		"systemctl is-active hermesh-signer.socket":                                                                                         "active",
		"pkaction --action-id io.hermesh.signer.sign":                                                                                       "io.hermesh.signer.sign",
		"ls /opt/Hermesh/bin/her /opt/Hermesh/bin/her-console/index.html /opt/Hermesh/bin/her-computer/agent-desktop >/dev/null && echo ok": "ok",
		"test -s /usr/share/icons/hicolor/512x512/apps/hermesh.png && echo ok":                                                              "ok",
	}
	for script, want := range checks {
		if out, err := sh(script); err != nil || out != want {
			t.Errorf("%s: %v\n%q, want %q", script, err, out, want)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// 3. A user sets Hermesh up there: the owner key in the installed signer's keeping.
	if out, err := sh(`set -e
echo 'polkit.addRule(function(a, s) { if (a.id == "io.hermesh.signer.sign") return polkit.Result.AUTH_SELF; });' > /etc/polkit-1/rules.d/50-test.rules
echo 'alice:open-sesame-42' | chpasswd && chmod 755 /usr/local/bin/linuxowner && systemctl restart polkit`); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	alice := func(script string) (string, error) {
		out, err := exec.Command("docker", "exec", "-u", "alice", "-e", "HOME=/home/alice", "-e", "USER=alice", name, "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const sock = "/home/alice/.hm/o.sock"
	if out, err := alice("mkdir -p /home/alice/.hm && nohup hermeshd run --owner --data-dir /home/alice/.hm --socket " + sock + " > /home/alice/.hm/owner.log 2>&1 &"); err != nil {
		t.Fatalf("hermeshd: %v %s", err, out)
	}
	owner := func(args string) (string, error) { return alice("linuxowner -sock " + sock + " " + args) }
	eventually(t, 20*time.Second, "hermeshd answering", func() bool { _, err := owner("signer"); return err == nil })
	if out, err := alice("nohup python3 /usr/local/bin/agent.py " + sock + " open-sesame-42 /home/alice/agent.log > /dev/null 2>&1 &"); err != nil {
		t.Fatalf("agent: %v %s", err, out)
	}
	eventually(t, 20*time.Second, "the installed signer watching", func() bool { s, _ := owner("signer"); return s == "connected=true busy=false" })
	if out, err := owner(fmt.Sprintf("setup http://host.docker.internal:%d %s", port, claim)); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
	if out, err := owner("machine desk-2"); err != nil {
		log, _ := alice("cat /home/alice/agent.log")
		t.Fatalf("machine: %v %s\n%s", err, out, log)
	}
	if log, _ := alice("cat /home/alice/agent.log"); !strings.Contains(log, "Create your owner identity") || !strings.Contains(log, "AUTHENTICATION COMPLETE") {
		t.Fatalf("polkit's dialog:\n%s", log)
	}
	keys := "ls /var/lib/hermesh-signer/*/*.pem | wc -l"
	if n, _ := sh(keys); n != "1" {
		t.Fatalf("the owner key: %s", n)
	}

	// 4. Removed: the signer stops, the key stays; purged: it goes.
	if out, err := sh("DEBIAN_FRONTEND=noninteractive apt-get remove -y hermesh 2>&1 | tail -1; systemctl is-active hermesh-signer.socket; ls /opt/Hermesh 2>&1 | head -1"); !strings.Contains(out, "inactive") || err != nil {
		t.Fatalf("removed: %v\n%s", err, out)
	}
	if n, _ := sh(keys); n != "1" {
		t.Fatalf("removing the package took the key: %s", n)
	}
	if out, _ := sh("DEBIAN_FRONTEND=noninteractive apt-get purge -y hermesh 2>&1 | tail -1; test -e /var/lib/hermesh-signer && echo kept || echo gone"); !strings.HasSuffix(out, "gone") {
		t.Fatalf("purged: %s", out)
	}
}
