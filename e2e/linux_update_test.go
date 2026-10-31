package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// linuxServerImage is a fresh Ubuntu server with systemd running, and git
// for a project.
const linuxServerImage = `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv dbus dbus-user-session curl ca-certificates openssl git && apt-get clean && rm -rf /var/lib/apt/lists/*
CMD ["/sbin/init"]
`

// TestLinuxUpdate (HERMESH_DOCKER=1) is docs/machine-updates.md §7 on a
// Linux server logged in as root, with its systemd user service, and three
// signed releases A < B < C served by the test as GitHub serves them:
//
//  1. the install command installs A and pairs the machine;
//  2. B is published while a session runs: the machine finds it and stays
//     on A; once the session ends it restarts itself on B, with the same
//     machine ID;
//  3. the install command runs again: no pairing, the service restarts;
//  4. with automatic updates off, C is published: nothing happens until
//     `hermeshd update --yes`, which installs C and restarts the service.
func TestLinuxUpdate(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	ctx := context.Background()

	// The releases, at an address the container reaches.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	releasesURL := fmt.Sprintf("http://host.docker.internal:%d/releases", ln.Addr().(*net.TCPAddr).Port)
	var mu sync.Mutex
	latest := ""
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		l := latest
		mu.Unlock()
		path := strings.Replace(r.URL.Path, "/releases/latest/download/", "/releases/download/v"+l+"/", 1)
		version, name, ok := strings.Cut(strings.TrimPrefix(path, "/releases/download/v"), "/")
		if !ok || l == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(tmp, "releases", version, filepath.Base(name)))
	}))
	t.Cleanup(func() { ln.Close() })
	publish := func(v string) {
		mu.Lock()
		latest = v
		mu.Unlock()
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	script, err := os.ReadFile("scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	pub := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	for _, v := range []string{"0.0.1", "0.0.2", "0.0.3"} {
		dir := filepath.Join(tmp, "releases", v)
		os.MkdirAll(dir, 0o755)
		// As scripts/release.sh builds a release, but naming the test's
		// releases and key.
		ldflags := "-X github.com/hermeshai/hermesh/internal/machine.Version=" + v +
			" -X github.com/hermeshai/hermesh/internal/update.releases=" + releasesURL +
			" -X github.com/hermeshai/hermesh/internal/update.publicKey=" + base64.StdEncoding.EncodeToString(der) +
			" -X github.com/hermeshai/hermesh/internal/update.testInterval=2s"
		var sums bytes.Buffer
		for _, b := range []string{"hermeshd", "hermesh"} {
			asset := b + "-linux-" + runtime.GOARCH
			cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", filepath.Join(dir, asset), "./cmd/"+b)
			cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build %s %s: %v %s", b, v, err, out)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, asset))
			h := sha256.Sum256(raw)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), asset)
		}
		h := sha256.Sum256([]byte(v + "\n"))
		fmt.Fprintf(&sums, "%s  version\n", hex.EncodeToString(h[:]))
		digest := sha256.Sum256(sums.Bytes())
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		for name, body := range map[string][]byte{
			"version":           []byte(v + "\n"),
			"checksums.txt":     sums.Bytes(),
			"checksums.txt.sig": sig,
			"install.sh":        bytes.Replace(script, []byte("__HERMESH_RELEASE_PUBKEY__"), []byte(pub), 1),
		} {
			os.WriteFile(filepath.Join(dir, name), body, 0o644)
		}
	}
	publish("0.0.1")

	// The owner, on a relay here.
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	start(t, "owner", nil, filepath.Join(bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(tmp, "owner"), "--socket", sock)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: fmt.Sprintf("http://127.0.0.1:%d", port), ClaimCode: claim, RootPublicKey: rootPub(root)})
	if err != nil {
		t.Fatal(err)
	}
	address := strings.Replace(setup.ConnectAddress, "127.0.0.1", "host.docker.internal", 1)

	// The server.
	build := exec.Command("docker", "build", "-t", "hermesh-e2e-linux-server:1", "-")
	build.Stdin = strings.NewReader(linuxServerImage)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v %s", err, out)
	}
	name := fmt.Sprintf("hermesh-update-%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "--hostname", "vps-1", "--privileged", "--cgroupns=host",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw", "--tmpfs", "/run", "--tmpfs", "/run/lock",
		"--add-host", "host.docker.internal:host-gateway", "hermesh-e2e-linux-server:1").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	sh := func(script string) (string, error) {
		out, err := exec.Command("docker", "exec", name, "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := sh("journalctl --no-pager -n 60; tail -n 80 /root/.local/share/hermesh/logs/hermeshd.log")
			t.Logf("==== journal and log ====\n%s", out)
		}
		exec.Command("docker", "rm", "-f", name).Run()
	})
	eventually(t, 60*time.Second, "systemd up", func() bool {
		out, _ := sh("systemctl is-system-running")
		return out == "running" || out == "degraded"
	})
	// A project, and the scripted coding agents for the service.
	if out, err := sh(`git config --global user.email t@t && git config --global user.name t &&
		mkdir -p /srv/demo && cd /srv/demo && git init -q -b main && echo hi > README.md && git add -A && git commit -qm init &&
		mkdir -p /root/.config/systemd/user/hermeshd.service.d &&
		printf '[Service]\nEnvironment=HERMESH_FAKE_PROVIDERS=1\n' > /root/.config/systemd/user/hermeshd.service.d/fake.conf`); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	// 1. The piped install command in a terminal: accept root and choose
	// a data folder with spaces. Later commands and updates must find it
	// without an environment variable or a --data-dir argument.
	installCmd := "curl -fsSL " + releasesURL + "/latest/download/install.sh | HERMESH_DOWNLOAD_BASE=" + releasesURL + "/latest/download sh -s -- " + address + " --name vps-1"
	install := exec.Command("docker", "exec", "-i", name, "script", "-q", "-e", "-c", installCmd, "/dev/null")
	input, err := install.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var installOut syncBuffer
	install.Stdout, install.Stderr = &installOut, &installOut
	if err := install.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(input, "\n/srv/hermesh data\n"); err != nil {
		t.Fatal(err)
	}
	var code string
	eventually(t, 60*time.Second, "pairing code", func() bool {
		if m := codeRE.FindStringSubmatch(installOut.String()); m != nil {
			code = m[1]
		}
		return code != ""
	})
	approved, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "vps-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil {
		t.Fatalf("install: %v\n%s", err, installOut.String())
	}
	if out, err := sh(`readlink /root/.local/share/hermesh; test -f '/srv/hermesh data/machine/hermeshd.db'`); err != nil || out != "/srv/hermesh data" {
		t.Fatalf("selected data directory: %v %s", err, out)
	}
	machineVersion := func() string {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return ""
		}
		for _, m := range res.Machines {
			if m.MachineId == approved.MachineId && m.Online && m.Status != nil {
				return m.Status.Version
			}
		}
		return ""
	}
	eventually(t, 60*time.Second, "the machine online on 0.0.1", func() bool { return machineVersion() == "0.0.1" })
	if _, err := app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: approved.MachineId, ProjectId: "demo", Path: "/srv/demo"}); err != nil {
		t.Fatal(err)
	}

	// 2. B while a session runs.
	home := []string{"HERMESH_HOME=" + filepath.Join(tmp, "agent")}
	login := start(t, "login", home, filepath.Join(bin, "hermesh"), "auth", "login", setup.ConnectAddress, "--name", "agent")
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login.pairingCode(t), Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	login.wait(t, 30*time.Second)
	started := run(t, home, time.Minute, filepath.Join(bin, "hermesh"), "session", "start", "--project", "vps-1/demo", "-o", "json", "--prompt", "sleep 40s\nsay done")
	var doc struct {
		Session struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		} `json:"session"`
	}
	if json.Unmarshal([]byte(started.stdout), &doc); started.code != 0 || doc.Session.SessionID == "" {
		t.Fatalf("session start: %+v", started)
	}
	sessionStatus := func() string {
		r := run(t, home, 30*time.Second, filepath.Join(bin, "hermesh"), "session", "get", doc.Session.SessionID, "-o", "json")
		json.Unmarshal([]byte(r.stdout), &doc)
		return doc.Session.Status
	}
	eventually(t, 30*time.Second, "the session running", func() bool { return sessionStatus() == "running" })
	publish("0.0.2")
	eventually(t, 30*time.Second, "the machine finds 0.0.2", func() bool {
		out, _ := sh("cat /root/.local/share/hermesh/update/update.json")
		return strings.Contains(out, `"available": "0.0.2"`)
	})
	time.Sleep(5 * time.Second) // more checks, still under the session
	if v, s := machineVersion(), sessionStatus(); v != "0.0.1" || s != "running" {
		t.Fatalf("while the session runs: machine on %q, session %q", v, s)
	}
	eventually(t, 90*time.Second, "the session done", func() bool { return sessionStatus() == "idle" })
	eventually(t, 60*time.Second, "the machine on 0.0.2", func() bool { return machineVersion() == "0.0.2" })
	if out, _ := sh("/root/.local/bin/hermesh version; ls -a /root/.local/bin"); !strings.HasPrefix(out, "0.0.2") || !strings.Contains(out, ".hermeshd.previous") {
		t.Errorf("the programs after the update: %s", out)
	}

	// 3. The install command again: it updates without pairing.
	again, err := sh(installCmd)
	if err != nil || !strings.Contains(again, "already paired") || !strings.Contains(again, "Keeping this location") || codeRE.MatchString(again) {
		t.Fatalf("install again: %v\n%s", err, again)
	}
	eventually(t, 60*time.Second, "the machine back on 0.0.2", func() bool { return machineVersion() == "0.0.2" })

	// 4. Automatic updates off.
	if out, err := sh("/root/.local/bin/hermeshd update auto off"); err != nil || !strings.Contains(out, "off") {
		t.Fatalf("auto off: %v %s", err, out)
	}
	publish("0.0.3")
	time.Sleep(8 * time.Second) // several checks' time
	if v := machineVersion(); v != "0.0.2" {
		t.Fatalf("updated to %q with automatic updates off", v)
	}
	if out, _ := sh("/root/.local/bin/hermeshd update --check"); !strings.Contains(out, "0.0.3 is available") {
		t.Errorf("--check: %s", out)
	}
	if out, err := sh("/root/.local/bin/hermeshd update --yes"); err != nil || !strings.Contains(out, "the service restarted on 0.0.3") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	eventually(t, 60*time.Second, "the machine on 0.0.3", func() bool { return machineVersion() == "0.0.3" })
	if out, _ := sh("/root/.local/bin/hermeshd status"); !strings.Contains(out, "updates      off") {
		t.Errorf("status: %s", out)
	}
	// A missing data volume must not create a new machine at the old
	// entry point. Restore it afterwards and keep the original identity.
	if out, err := sh(`XDG_RUNTIME_DIR=/run/user/0 systemctl --user stop hermeshd.service && mv '/srv/hermesh data' '/srv/hermesh data.offline'`); err != nil {
		t.Fatalf("take data offline: %v %s", err, out)
	}
	if out, err := sh("/root/.local/bin/hermeshd status"); err == nil {
		t.Fatalf("status succeeded without the data volume: %s", out)
	}
	if out, err := sh(`test ! -e '/srv/hermesh data' && mv '/srv/hermesh data.offline' '/srv/hermesh data' && XDG_RUNTIME_DIR=/run/user/0 systemctl --user start hermeshd.service`); err != nil {
		t.Fatalf("restore data: %v %s", err, out)
	}
	eventually(t, 60*time.Second, "the same machine back after its data returns", func() bool { return machineVersion() == "0.0.3" })
}

// syncBuffer is a bytes.Buffer written by a command and read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
