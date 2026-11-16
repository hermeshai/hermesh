package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
)

// linuxDesktopImage is a fresh Ubuntu computer with a desktop to log in to:
// systemd, an X display (Xvfb) with a window manager, the accessibility bus
// and a calculator.
const linuxDesktopImage = `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y -o Acquire::Retries=8 --no-install-recommends systemd systemd-sysv dbus dbus-user-session curl ca-certificates openssl xvfb openbox at-spi2-core galculator && apt-get clean && rm -rf /var/lib/apt/lists/*
CMD ["/sbin/init"]
`

// TestLinuxHerComputer (HERMESH_DOCKER=1) is Her's computer use on a Linux
// computer with a desktop (docs/linux.md §5), installed as a person
// installs it:
//
//  1. the install command puts agent-desktop in her-computer/ beside her,
//     from a release that carries it;
//  2. Her is turned on before anyone logs in to the desktop, as after a
//     restart: hermeshd and her have no display of their own, and
//     desktop/status says computer use is off, as on a server;
//  3. someone logs in: without her starting again, desktop/status says it is
//     on and that agent-desktop reaches the apps, Her lists the calculator
//     that runs there and reads its window;
//  4. `hermeshd uninstall` removes her-computer/.
//
// The releases' her is HERMESH_HER_LINUX, else built in Docker (linuxHer);
// agent-desktop is HERMESH_AGENT_DESKTOP_LINUX (a folder with it and its
// clipboard helper), else taken from the pinned npm package, with node.
func TestLinuxHerComputer(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	computer := linuxAgentDesktop(t)
	// The calculator read without asking, as an owner allows an app in Her's
	// settings.
	c, id, _, sh, out := linuxHerMachine(t, "hermesh-e2e-linux-desktop:1", linuxDesktopImage, "desk-1",
		map[string][]byte{"her-computer-linux-" + runtime.GOARCH + ".tar.gz": computer}, "\n[desktop.apps]\ngalculator = \"allow\"\n")
	if !strings.Contains(out, "› downloading her-computer-linux-"+runtime.GOARCH+".tar.gz") {
		t.Fatalf("install:\n%s", out)
	}
	if s, err := sh("stat -c '%a %n' /root/.local/bin/her-computer /root/.local/bin/her-computer/*"); err != nil ||
		s != "755 /root/.local/bin/her-computer\n755 /root/.local/bin/her-computer/agent-desktop\n755 /root/.local/bin/her-computer/agent-desktop-linux-clipboard-owner" {
		t.Fatalf("her-computer as installed: %v\n%s", err, s)
	}

	// 2. Her is on, with nobody logged in to the desktop.
	type desktop struct {
		Enabled       bool   `json:"enabled"`
		Program       string `json:"program"`
		Accessibility string `json:"accessibility"`
	}
	status := func() (d desktop) {
		res, err := c.HerCall(context.Background(), &localv1.HerCallRequest{MachineId: id, Call: &herv1.CallRequest{Request: herSetup("desktop/status", nil)}})
		if err != nil || res.Response.GetError() != nil {
			t.Fatalf("desktop/status: %v %v", res, err)
		}
		json.Unmarshal(res.Response.Result, &d)
		return d
	}
	if d := status(); d.Enabled || d.Program != "/root/.local/bin/her-computer/agent-desktop" {
		t.Fatalf("with nobody logged in: %+v", d)
	}

	// 3. Someone logs in: the display, a window manager and a calculator,
	// and the display in the user's systemd, where the desktops put it.
	login := `export XDG_RUNTIME_DIR=/run/user/0 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus DISPLAY=:0
Xvfb :0 -screen 0 1280x800x24 -nolisten tcp >/dev/null 2>&1 &
sleep 2
openbox >/dev/null 2>&1 &
systemctl --user import-environment DISPLAY
galculator >/dev/null 2>&1 &
wait`
	if out, err := sh("nohup sh -c '" + login + "' >/dev/null 2>&1 &"); err != nil {
		t.Fatalf("logging in: %v %s", err, out)
	}
	eventually(t, time.Minute, "computer use on, agent-desktop reaching the apps", func() bool {
		d := status()
		return d.Enabled && d.Accessibility == "granted"
	})
	if env, _ := sh(`tr '\0' '\n' < /proc/$(pgrep -o -x her)/environ | grep -c '^DISPLAY='`); env != "0" {
		t.Fatalf("her has a display of its own (%s): this is not her started before the login", env)
	}
	bot := defaultBot(t, c, id)
	var apps string
	eventually(t, time.Minute, "the calculator among the apps", func() bool {
		apps = cua(t, c, id, bot.ID, "apps")
		return strings.Contains(apps, "galculator")
	})
	if read := cua(t, c, id, bot.ID, "snapshot:galculator"); !strings.Contains(read, "sqrt") {
		t.Fatalf("cua_snapshot of the calculator: %s\n(cua_apps: %s)", read, apps)
	}

	// 4. Uninstalled: her-computer goes with her.
	if s, err := sh("XDG_RUNTIME_DIR=/run/user/0 USER=root /root/.local/bin/hermeshd uninstall --yes 2>&1"); err != nil || !strings.Contains(s, "/root/.local/bin/her-computer") {
		t.Fatalf("uninstall: %v\n%s", err, s)
	}
	if left, _ := sh("ls -d /root/.local/bin/her-computer 2>/dev/null"); left != "" {
		t.Errorf("left after uninstalling: %s", left)
	}
}

// linuxHerMachine is a fresh Linux computer (image tag, built from
// dockerfile, hostname host) that became a machine with the install
// command, as root without a terminal, from a release that carries her, her
// console and files, and on which Her is on, her-mock its model; config is
// more of her config.toml there. It returns the owner's client, the
// machine's id, the container's name, a shell there and what the install
// printed.
func linuxHerMachine(t *testing.T, tag, dockerfile, host string, files map[string][]byte, config string) (localv1connect.LocalServiceClient, string, string, func(string) (string, error), string) {
	t.Helper()
	mock := startHerMock(t, herBinary(t, "HERMESH_HER_MOCK", "her-mock"))
	her, err := os.ReadFile(linuxHer(t))
	if err != nil {
		t.Fatal(err)
	}
	w := newOwnerMac(t, nil, &localv1.GrantSpec{})
	c := w.app.client
	releases := newTestReleases(t)
	files = maps.Clone(files)
	files["her-linux-"+runtime.GOARCH], files["her-console.tar.gz"] = her, herConsoleArchive(t, "the console")
	releases.add(t, "0.0.1", files)
	releases.publish("0.0.1")

	name, sh := startLinuxImage(t, tag, dockerfile, "hermesh-her-"+host, host)
	put := func(path, content string) {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", name, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path)
		cmd.Stdin = strings.NewReader(content)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("writing %s: %v %s", path, err, out)
		}
	}
	// her-mock's models as a model the owner added, and her's settings for a
	// test in the service's environment.
	put("/root/.her/models.toml", strings.ReplaceAll(mock.models, "127.0.0.1", "host.docker.internal"))
	put("/root/.her/config.toml", "model = \"mock/mock-1\"\n"+config)
	put("/root/.config/systemd/user/hermeshd.service.d/her.conf", "[Service]\nEnvironment=HER_NO_BROWSER=1 HER_LANG=en HER_MODEL_CATALOG_URL="+
		strings.Replace(mock.base, "127.0.0.1", "host.docker.internal", 1)+"/catalog/api.json\n")

	install := exec.Command("docker", "exec", name, "sh", "-c", releases.installCommand(strings.Replace(w.addr, "127.0.0.1", "host.docker.internal", 1)+" --name "+host))
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
	approved, err := c.ApproveMachinePairing(context.Background(), &localv1.ApproveMachinePairingRequest{Code: code, MachineName: host})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil {
		t.Fatalf("install: %v\n%s", err, out.String())
	}
	turnHerOn(t, c, approved.MachineId)
	return c, approved.MachineId, name, sh, out.String()
}

// linuxAgentDesktop is her-computer-linux-<arch>.tar.gz as scripts/release.sh
// makes it: agent-desktop for Linux and its clipboard helper, from the
// folder HERMESH_AGENT_DESKTOP_LINUX names, else from the pinned npm package
// (apps/desktop/scripts/agent-desktop.mjs).
func linuxAgentDesktop(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv("HERMESH_AGENT_DESKTOP_LINUX")
	if dir == "" {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("no node to take agent-desktop from its npm package: set HERMESH_AGENT_DESKTOP_LINUX")
		}
		dir = t.TempDir()
		arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
		if out, err := exec.Command(node, "apps/desktop/scripts/agent-desktop.mjs", "linux", arch, dir).CombinedOutput(); err != nil {
			t.Fatalf("agent-desktop for Linux: %v\n%s", err, out)
		}
	}
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./", Mode: 0o755})
	for _, name := range []string{"agent-desktop", "agent-desktop-linux-clipboard-owner"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "./" + name, Mode: 0o755, Size: int64(len(body))})
		tw.Write(body)
	}
	if tw.Close() != nil || zw.Close() != nil {
		t.Fatal("her-computer archive")
	}
	return b.Bytes()
}
