package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// fakeClaude is a claude CLI signed in only through the CLAUDE_CONFIG_DIR
// ~/.profile exports, as on a server whose Claude lives in another folder.
const fakeClaude = `#!/bin/sh
case "$1" in
--version) echo "2.1.0 (Claude Code)" ;;
auth) if [ -n "$CLAUDE_CONFIG_DIR" ]; then echo '{"loggedIn": true}'; else echo '{"loggedIn": false}'; fi ;;
esac
`

// TestLinuxFirstInstall (HERMESH_DOCKER=1) pairs a fresh Linux server as
// root from a login shell, twice:
//
//  1. ~/.profile exports what signs claude in: the background service's
//     claude is signed in, and pairing says so as the service finds it;
//  2. the service can't be enabled (as `enable --now` on systemd 219):
//     the machine is online all the same, and the last lines say it has
//     no background service and why.
func TestLinuxFirstInstall(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	ctx := context.Background()
	linuxBin := filepath.Join(tmp, "hermeshd-linux")
	cmd := exec.Command("go", "build", "-o", linuxBin, "./cmd/hermeshd")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

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
	build := exec.Command("docker", "build", "-t", "hermesh-e2e-linux-server:1", "-")
	build.Stdin = strings.NewReader(linuxServerImage)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v %s", err, out)
	}

	// pair starts a server, prepares it, and pairs it as root from a login
	// shell, as the install command does; it returns what pairing printed and a shell on the server.
	pair := func(host, prepare string) (string, string, func(string) string) {
		name := fmt.Sprintf("hermesh-first-%s-%d", host, time.Now().UnixNano())
		if out, err := exec.Command("docker", "run", "-d", "--name", name, "--hostname", host, "--privileged", "--cgroupns=host",
			"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw", "--tmpfs", "/run", "--tmpfs", "/run/lock",
			"--add-host", "host.docker.internal:host-gateway", "hermesh-e2e-linux-server:1").CombinedOutput(); err != nil {
			t.Fatalf("docker run: %v %s", err, out)
		}
		sh := func(script string) string {
			out, _ := exec.Command("docker", "exec", name, "sh", "-c", script).CombinedOutput()
			return strings.TrimSpace(string(out))
		}
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("==== %s: journal and log ====\n%s", host, sh("journalctl --no-pager -n 60; tail -n 80 /root/.local/share/hermesh/logs/hermeshd.log"))
			}
			exec.Command("docker", "rm", "-f", name).Run()
		})
		eventually(t, 60*time.Second, "systemd up", func() bool {
			out := sh("systemctl is-system-running")
			return out == "running" || out == "degraded"
		})
		if out, err := exec.Command("docker", "cp", linuxBin, name+":/root/hermeshd").CombinedOutput(); err != nil {
			t.Fatalf("docker cp: %v %s", err, out)
		}
		os.WriteFile(filepath.Join(tmp, "claude"), []byte(fakeClaude), 0o755)
		if out, err := exec.Command("docker", "cp", filepath.Join(tmp, "claude"), name+":/usr/local/bin/claude").CombinedOutput(); err != nil {
			t.Fatalf("docker cp: %v %s", err, out)
		}
		sh("mkdir -p /root/.local/bin && mv /root/hermeshd /root/.local/bin/hermeshd && " + prepare)
		// As scripts/install.sh pairs for root; one stream, in order.
		p := exec.Command("docker", "exec", name, "sh", "-lc", "loginctl enable-linger root && export USER=root LOGNAME=root && /root/.local/bin/hermeshd pair "+address+" --name "+host+" 2>&1")
		var out syncBuffer
		p.Stdout, p.Stderr = &out, &out
		if err := p.Start(); err != nil {
			t.Fatal(err)
		}
		var code string
		eventually(t, 60*time.Second, "pairing code", func() bool {
			if m := codeRE.FindStringSubmatch(out.String()); m != nil {
				code = m[1]
			}
			return code != ""
		})
		approved, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: host})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Wait(); err != nil {
			t.Fatalf("pair: %v\n%s", err, out.String())
		}
		return out.String(), approved.MachineId, sh
	}
	online := func(id string) bool {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		for _, m := range res.Machines {
			if m.MachineId == id && m.Online {
				return true
			}
		}
		return false
	}

	// 1. What ~/.profile exports reaches the service's agents.
	out, id, sh := pair("srv-1", "mkdir -p /srv/claude && echo 'export CLAUDE_CONFIG_DIR=/srv/claude' >> /root/.profile")
	if !strings.Contains(out, "✓ Background service on") || !strings.Contains(out, "  claude     2.1.0, signed in\n") || strings.Contains(out, "NOT") {
		t.Fatalf("pairing with claude signed in from ~/.profile:\n%s", out)
	}
	eventually(t, 30*time.Second, "srv-1 online", func() bool { return online(id) })
	if doctor := sh("/root/.local/bin/hermeshd doctor"); !strings.Contains(doctor, "claude     2.1.0, signed in") {
		t.Errorf("doctor, from the service:\n%s", doctor)
	}

	// 2. No service: online now, and said last.
	out, id, sh = pair("srv-2", `printf '#!/bin/sh\ncase " $* " in *" enable "*) echo "Failed to enable unit: simulated" >&2; exit 1 ;; esac\nexec /usr/bin/systemctl "$@"\n' > /usr/local/sbin/systemctl && chmod +x /usr/local/sbin/systemctl`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.Join(lines[max(0, len(lines)-2):], "\n")
	if !strings.Contains(last, "⚠ No background service: systemctl --user enable hermeshd.service: exit status 1: Failed to enable unit: simulated") ||
		!strings.Contains(last, "hermeshd runs now (pid ") || !strings.Contains(out, "  claude     2.1.0, NOT SIGNED IN") {
		t.Fatalf("pairing without a service:\n%s", out)
	}
	eventually(t, 30*time.Second, "srv-2 online", func() bool { return online(id) })
	time.Sleep(3 * time.Second) // the terminal it was paired from is gone
	if !online(id) {
		t.Fatal("srv-2 went offline after pairing")
	}
	if s := sh("XDG_RUNTIME_DIR=/run/user/0 /usr/bin/systemctl --user is-enabled hermeshd.service"); s == "enabled" {
		t.Errorf("the service is %s", s)
	}
}
