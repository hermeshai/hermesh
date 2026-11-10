package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestLinuxUninstall (HERMESH_DOCKER=1): a Linux server paired as root, as
// the install command pairs it, with its data on another disk (a Synology
// volume's link), its systemd user service and the agents' CLI. In a
// terminal, `hermeshd uninstall` says what goes and asks: Enter keeps
// everything; y unpairs it (its owner's devices stop listing it) and
// leaves nothing of Hermesh's for root: no service, process, data, link,
// CLI settings or programs.
func TestLinuxUninstall(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	ctx := context.Background()
	for _, c := range []string{"hermeshd", "hermesh"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(tmp, c), "./cmd/"+c)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}

	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
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

	name := fmt.Sprintf("hermesh-uninstall-%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "--hostname", "srv-u", "--privileged", "--cgroupns=host",
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
			t.Logf("==== journal and log ====\n%s", sh("journalctl --no-pager -n 60; tail -n 80 /volume1/@hermesh-0/logs/hermeshd.log"))
		}
		exec.Command("docker", "rm", "-f", name).Run()
	})
	eventually(t, 60*time.Second, "systemd up", func() bool {
		out := sh("systemctl is-system-running")
		return out == "running" || out == "degraded"
	})
	for _, c := range []string{"hermeshd", "hermesh"} {
		if out, err := exec.Command("docker", "cp", filepath.Join(tmp, c), name+":/root/"+c).CombinedOutput(); err != nil {
			t.Fatalf("docker cp: %v %s", err, out)
		}
	}
	// As the installer leaves it: the programs in ~/.local/bin, the data
	// entry linked to a volume, the CLI's settings.
	sh("mkdir -p /root/.local/bin /root/.local/share /volume1/@hermesh-0 /root/.config/hermesh && mv /root/hermeshd /root/hermesh /root/.local/bin/ && " +
		"ln -s /volume1/@hermesh-0 /root/.local/share/hermesh && echo '{}' > /root/.config/hermesh/state.json")
	p := exec.Command("docker", "exec", name, "sh", "-lc", "loginctl enable-linger root && export USER=root LOGNAME=root && /root/.local/bin/hermeshd pair "+address+" --name srv-u 2>&1")
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
	approved, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "srv-u"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil || !strings.Contains(out.String(), "✓ Background service on") {
		t.Fatalf("pair: %v\n%s", err, out.String())
	}
	listed := func(online bool) bool {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineId == approved.MachineId && (m.Online || !online) })
	}
	eventually(t, 30*time.Second, "srv-u online", func() bool { return listed(true) })

	// uninstall answers in a terminal, as a person at an SSH prompt.
	uninstall := func(answer string) string {
		return sh(fmt.Sprintf(`printf '%s\n' | XDG_RUNTIME_DIR=/run/user/0 USER=root script -qec '/root/.local/bin/hermeshd uninstall' /dev/null 2>&1`, answer))
	}
	const user = "XDG_RUNTIME_DIR=/run/user/0 systemctl --user "

	// Enter: nothing changes.
	got := uninstall("")
	for _, want := range []string{
		"This removes Hermesh for root on this computer:",
		"its machine “srv-u” (owner " + setup.OwnerId + "): unpaired, and gone from your devices",
		"the background service",
		"its data: /root/.local/share/hermesh → /volume1/@hermesh-0",
		"the agents' CLI key and settings: /root/.config/hermesh",
		"stays listed on your devices until you revoke it there",
		"the programs: /root/.local/bin/hermesh, /root/.local/bin/hermeshd",
		"Worktrees with uncommitted changes move to /root/hermesh-kept-worktrees",
		"This can't be undone. Uninstall? [y/N]",
		"not uninstalled",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("uninstall, Enter: lacks %q:\n%s", want, got)
		}
	}
	if s := sh(user + "is-active hermeshd.service"); s != "active" {
		t.Fatalf("after Enter the service is %q", s)
	}
	if !listed(true) {
		t.Fatal("after Enter srv-u is offline")
	}

	// y: all of it goes.
	got = uninstall("y")
	if !strings.Contains(got, "✓ Hermesh is uninstalled for root.") || !strings.Contains(got, "Lingering stays on for root") || strings.Contains(got, "warning") {
		t.Fatalf("uninstall, y:\n%s", got)
	}
	eventually(t, 30*time.Second, "srv-u gone from the owner's machines", func() bool { return !listed(false) })
	if s := sh(user + "is-active hermeshd.service"); s != "inactive" {
		t.Errorf("the service after uninstalling: %s", s)
	}
	left := sh("pgrep -l hermeshd; ls -d /root/.config/systemd/user/hermeshd.service /volume1/@hermesh-0 /root/.local/share/hermesh " +
		"/root/.config/hermesh /root/.local/bin/hermeshd /root/.local/bin/hermesh /root/hermesh-kept-worktrees 2>/dev/null")
	if left != "" {
		t.Errorf("left after uninstalling:\n%s", left)
	}
}

// TestUninstallTrustedDevice: on a trusted device `hermeshd uninstall`
// removes nothing: its key may be what holds the owner's Hermesh.
func TestUninstallTrustedDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if runtime.GOOS == "windows" {
		t.Skip("on Windows, Hermesh is uninstalled in Settings › Apps")
	}
	w := newOwnerMac(t, nil, &localv1.GrantSpec{})
	// A copy: uninstall would remove the hermeshd it runs as.
	daemon := filepath.Join(t.TempDir(), "hermeshd")
	b, err := os.ReadFile(filepath.Join(w.bin, "hermeshd"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(daemon, b, 0o755)
	data := filepath.Join(w.tmp, "owner-mac")
	r := run(t, nil, 30*time.Second, daemon, "uninstall", "--yes", "--data-dir", data)
	if r.code == 0 || !strings.Contains(r.stderr, "trusted device") {
		t.Fatalf("uninstall on a trusted device: %+v", r)
	}
	if _, err := os.Stat(daemon); err != nil {
		t.Fatalf("hermeshd removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "owner")); err != nil {
		t.Fatalf("the owner's data removed: %v", err)
	}
}
