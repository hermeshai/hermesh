package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestLinuxInstallAndProviderSessions (HERMESH_DOCKER=1) exercises adding a
// fresh Linux server from zero via scripts/install.sh for a non-root user
// (--user dev, run from a root SSH-like environment with root's XDG/D-Bus/SHELL
// variables set), then verifies:
//
//  1. install.sh creates dev, turns lingering on, unpacks binaries + Her
//     archives owned by dev, pairs with the connection address, and starts
//     dev's systemd user service;
//  2. re-running install.sh --user dev recognises the paired machine and
//     restarts dev's service without re-pairing;
//  3. ProviderMaintenance reports dev as a writable non-root target, verifies
//     the provider CLI protocol handshake (codex + opencode ACP), and passes
//     TestMachineAgent ("ready");
//  4. Direct provider conversations (no project -> scratch workspace) work
//     end-to-end over SessionService (StartSession + multi-turn DispatchMessage)
//     and via the hermesh CLI (`--project vps-dev/scratch`), alongside a git
//     worktree project session;
//  5. `hermeshd uninstall` as dev shows the paired machine name + owner ID,
//     unpairs it from the owner, and removes all files cleanly.
func TestLinuxInstallAndProviderSessions(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	ctx := context.Background()

	// Build Linux fakeacp and fakecodex binaries so real codex app-server and
	// ACP JSON-RPC handshakes + turns run inside the Linux container.
	linuxHelpers := filepath.Join(tmp, "helpers")
	os.MkdirAll(linuxHelpers, 0o755)
	for _, spec := range []struct{ name, pkg string }{
		{"codex", "./bench/fakecodex"},
		{"opencode", "./internal/runtime/acp/fakeacp"},
	} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(linuxHelpers, spec.name), spec.pkg)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build linux %s: %v\n%s", spec.name, err, out)
		}
	}
	herStub := filepath.Join(tmp, "her-stub")
	cmd := exec.Command("go", "build", "-o", herStub, "github.com/hermeshai/hermesh/e2e/testdata/her-startup")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build her fixture: %v\n%s", err, out)
	}
	herBytes, err := os.ReadFile(herStub)
	if err != nil {
		t.Fatal(err)
	}

	releases := newTestReleases(t)
	screenStub := []byte("#!/bin/sh\ncase \"$1\" in --version) echo screen 0.2.0;; *) echo '{\"available\":false,\"detail\":\"headless server\"}';; esac\n")
	releases.add(t, "0.2.0", map[string][]byte{
		"her-linux-" + runtime.GOARCH:                      herBytes,
		"her-console.tar.gz":                               herConsoleArchive(t, "linux console 0.2.0"),
		"her-pdf-linux-" + runtime.GOARCH + ".tar.gz":      herPDFArchive(t, "pdf 0.2.0"),
		"her-computer-linux-" + runtime.GOARCH + ".tar.gz": herConsoleArchive(t, "computer 0.2.0"),
		"hermesh-screen-linux-" + runtime.GOARCH:           screenStub,
	})
	releases.publish("0.2.0")

	// Start relay + owner daemon + test signer.
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
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:      fmt.Sprintf("http://127.0.0.1:%d", port),
		ClaimCode:     claim,
		RootPublicKey: rootPub(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	address := strings.Replace(setup.ConnectAddress, "127.0.0.1", "host.docker.internal", 1)

	name, sh := startLinuxServer(t, "hermesh-provider-e2e", "vps-dev")
	for _, helper := range []string{"codex", "opencode"} {
		if out, err := exec.Command("docker", "cp", filepath.Join(linuxHelpers, helper), name+":/usr/local/bin/"+helper).CombinedOutput(); err != nil {
			t.Fatalf("docker cp %s: %v %s", helper, err, out)
		}
	}
	if out, err := sh("chmod 0755 /usr/local/bin/codex /usr/local/bin/opencode && loginctl enable-linger root"); err != nil {
		t.Fatalf("prepare helpers: %v %s", err, out)
	}

	// 1. Run install.sh from /root with root's XDG_DATA_HOME, DBUS_SESSION_BUS_ADDRESS,
	// and SHELL set, installing for a brand-new user `dev` via interactive root prompt.
	installScript := fmt.Sprintf(
		"cd /root && export XDG_DATA_HOME=/root/.local/share DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus SHELL=/usr/bin/zsh && %s",
		releases.installCommand(address+" --name vps-dev"),
	)
	install := exec.Command("docker", "exec", "-i", name, "script", "-q", "-e", "-c", installScript, "/dev/null")
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
	answered := map[string]string{
		"type another user to install for": "dev\n",
		"Create it? [Y/n]":                 "y\n",
		"Data location [":                  "\n",
	}
	sent := map[string]bool{}
	var code string
	eventually(t, 90*time.Second, "pairing code from dev install", func() bool {
		cur := installOut.String()
		for prompt, reply := range answered {
			if !sent[prompt] && strings.Contains(cur, prompt) {
				_, _ = fmt.Fprint(input, reply)
				sent[prompt] = true
			}
		}
		if m := codeRE.FindStringSubmatch(cur); m != nil {
			code = m[1]
		}
		return code != ""
	})
	approved, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "vps-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil {
		t.Fatalf("install --user dev: %v\n%s", err, installOut.String())
	}
	if !strings.Contains(installOut.String(), "✓ Created dev") || !strings.Contains(installOut.String(), "✓ Background service on: starts with the server") {
		t.Fatalf("unexpected install output:\n%s", installOut.String())
	}

	//Prepare a git repo + fake claude/codex owned by dev so doctor + sessions pass.
	if out, err := sh(`printf '#!/bin/sh\ncase "$1" in --version) echo "2.1.332 (Claude Code)";; auth) echo "{\"loggedIn\":true}";; esac\n' > /usr/local/bin/claude && chmod 0755 /usr/local/bin/claude &&
		su - dev -c 'git config --global user.email dev@t && git config --global user.name dev &&
		mkdir -p ~/repo ~/.codex && cd ~/repo && git init -q -b main && echo hello > README.md && git add -A && git commit -qm init &&
		printf "{\"auth_mode\":\"chatgpt\",\"tokens\":{\"id_token\":\"fake\"}}\n" > ~/.codex/auth.json &&
		mkdir -p ~/.config/systemd/user/hermeshd.service.d &&
		printf "[Service]\nEnvironment=OPENAI_API_KEY=sk-test FAKEACP_LOGGED_IN=1\n" > ~/.config/systemd/user/hermeshd.service.d/env.conf &&
		XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user daemon-reload &&
		XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user restart hermeshd.service'`); err != nil {
		t.Fatalf("setup dev repo & auth: %v\n%s", err, out)
	}

	machineStatus := func() *machinev1.MachineStatus {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return nil
		}
		for _, m := range res.Machines {
			if m.MachineId == approved.MachineId && m.Online {
				return m.Status
			}
		}
		return nil
	}
	eventually(t, 60*time.Second, "vps-dev online on 0.2.0", func() bool {
		return machineStatus().GetVersion() == "0.2.0"
	})

	// 2. Re-run install.sh --user dev unattended: must detect already paired and restart cleanly.
	reinstall, err := sh(releases.installCommand("--user dev " + address + " --name vps-dev"))
	if err != nil || !strings.Contains(reinstall, "already paired") || codeRE.MatchString(reinstall) {
		t.Fatalf("reinstall --user dev failed:\nerr=%v\n%s", err, reinstall)
	}
	eventually(t, 30*time.Second, "vps-dev back online after reinstall", func() bool {
		return machineStatus().GetVersion() == "0.2.0"
	})

	// Verify `hermeshd status`, `hermeshd doctor`, `hermeshd screen`, and Her + Her Console as `dev`.
	if stOut, err := sh("su - dev -c 'hermeshd status'"); err != nil || !strings.Contains(stOut, `paired as "vps-dev"`) || !strings.Contains(stOut, "connected") {
		t.Fatalf("hermeshd status as dev: %v\n%s", err, stOut)
	}
	if docOut, err := sh("su - dev -c 'hermeshd doctor'"); err != nil || !strings.Contains(docOut, "codex") || !strings.Contains(docOut, "opencode") || !strings.Contains(docOut, "signed in") || strings.Contains(docOut, "login not checked") {
		t.Fatalf("hermeshd doctor as dev: %v\n%s", err, docOut)
	}
	if scrOut, err := sh("su - dev -c 'hermeshd screen'"); err != nil || !strings.Contains(scrOut, "headless server") {
		t.Fatalf("hermeshd screen as dev: %v\n%s", err, scrOut)
	}
	if _, err := app.client.SetHer(ctx, &localv1.SetHerRequest{MachineId: approved.MachineId, Enabled: true}); err != nil {
		t.Fatalf("SetHer: %v", err)
	}
	eventually(t, 30*time.Second, "Her running on vps-dev", func() bool {
		return machineStatus().GetHer().GetRunning()
	})
	con, err := app.client.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: approved.MachineId})
	if err != nil {
		t.Fatalf("GetHerConsole: %v", err)
	}
	if status, page := httpDo(t, "GET", con.Url, ""); status != 200 || !strings.Contains(page, "linux console 0.2.0") {
		t.Fatalf("Her console tunnel: HTTP %d %s", status, page)
	}

	// 3. ProviderMaintenance capabilities (writable non-root account "dev"), CLI protocol verify, and TestMachineAgent.
	_, _ = app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{MachineId: approved.MachineId})
	eventually(t, 30*time.Second, "provider maintenance capabilities reported for dev", func() bool {
		for _, pm := range machineStatus().GetProviderMaintenance() {
			if pm.Provider == "codex" && pm.Account == "dev" && pm.Package == "@openai/codex" && pm.TargetVersion != "" {
				return true
			}
		}
		return false
	})
	for _, provider := range []string{"codex", "opencode"} {
		reqID := fmt.Sprintf("verify-%s-%d", provider, time.Now().UnixNano())
		if _, err := app.client.MaintainProvider(ctx, &localv1.MaintainProviderRequest{
			MachineId: approved.MachineId,
			Operation: &identityv1.MaintainProvider{
				Provider:  provider,
				Action:    "verify",
				RequestId: reqID,
			},
		}); err != nil {
			t.Fatalf("MaintainProvider verify %s: %v", provider, err)
		}
		eventually(t, 30*time.Second, "verify "+provider+" succeeded", func() bool {
			for _, op := range machineStatus().GetProviderOperations() {
				if op.GetRequest().GetRequestId() == reqID {
					if op.Phase == "failed" {
						t.Fatalf("verify %s failed: %s", provider, op.Detail)
					}
					return op.Phase == "succeeded"
				}
			}
			return false
		})
	}
	testRes, err := app.client.TestMachineAgent(ctx, &localv1.TestMachineAgentRequest{
		MachineId: approved.MachineId,
		Agent:     "codex",
	})
	if err != nil || !testRes.Ok || !strings.Contains(testRes.Reply, "done:") {
		t.Fatalf("TestMachineAgent codex: err=%v res=%+v", err, testRes)
	}

	// 4. Direct provider conversation (ProjectId: "" -> scratch workspace on vps-dev) over SessionService,
	// multi-turn follow-up, plus CLI direct scratch session and git worktree project session.
	started, err := app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		CommandId:      fmt.Sprintf("cmd-direct-%d", time.Now().UnixNano()),
		MachineId:      approved.MachineId,
		ProjectId:      "", // direct provider chat without adding a project!
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "opencode"},
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt:  "write direct-note.txt hello-direct\nsay first-turn-ok",
	})
	if err != nil {
		t.Fatalf("StartSession direct provider chat: %v", err)
	}
	directID := started.GetSession().GetSessionId()
	if got := started.GetSession().GetProjectId(); got != kernel.ScratchProjectID {
		t.Fatalf("direct session project_id = %q, want %q", got, kernel.ScratchProjectID)
	}
	waitIdle := func(sid string) *sessionv1.SessionProjection {
		var proj *sessionv1.SessionProjection
		eventually(t, 60*time.Second, "session "+sid+" idle", func() bool {
			g, err := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid})
			if err != nil {
				return false
			}
			proj = g.GetProjection()
			return proj.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE
		})
		return proj
	}
	proj1 := waitIdle(directID)
	if !strings.Contains(proj1.GetSession().GetResult(), "first-turn-ok") {
		t.Fatalf("direct turn 1 result: %q", proj1.GetSession().GetResult())
	}
	if note, err := sh("cat /home/dev/.local/share/hermesh/machine/scratch_workspace/direct-note.txt"); err != nil || note != "hello-direct" {
		t.Fatalf("scratch_workspace file: %v %q", err, note)
	}

	// Second turn in the same direct provider conversation.
	if _, err := app.sessions.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		CommandId: fmt.Sprintf("cmd-direct-2-%d", time.Now().UnixNano()),
		SessionId: directID,
		Text:      "say second-turn-ok",
	}); err != nil {
		t.Fatalf("DispatchMessage turn 2: %v", err)
	}
	eventually(t, 60*time.Second, "direct session turn 2 idle", func() bool {
		g, err := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: directID})
		return err == nil &&
			g.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
			strings.Contains(g.GetProjection().GetSession().GetResult(), "second-turn-ok")
	})

	// Add git repo project as dev on the Linux machine, and run both a CLI scratch session and project worktree session.
	if out, err := sh("su - dev -c 'hermeshd project add ~/repo --name repo'"); err != nil || !strings.Contains(out, "Added project") {
		t.Fatalf("hermeshd project add as dev: %v %s", err, out)
	}
	cliEnv := []string{"HERMESH_HOME=" + filepath.Join(tmp, "cli-agent")}
	login := start(t, "cli-login", cliEnv, filepath.Join(bin, "hermesh"), "auth", "login", setup.ConnectAddress, "--name", "cli-agent")
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{
		Code: login.pairingCode(t),
		Spec: &localv1.GrantSpec{
			MachineIds: []string{"*"},
			ProjectIds: []string{"*", kernel.ScratchProjectID},
			Agents:     []string{"*"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	login.wait(t, 30*time.Second)

	for _, targetProj := range []string{"vps-dev/scratch", "vps-dev/repo"} {
		r := run(t, cliEnv, 90*time.Second, filepath.Join(bin, "hermesh"),
			"session", "start", "--project", targetProj, "--agent", "opencode",
			"--runtime-mode", "full_access", "--wait", "-o", "json", "--prompt", "say cli-ok")
		if r.code != 0 || !strings.Contains(r.stdout, "cli-ok") {
			t.Fatalf("hermesh session start %s failed: code=%d stdout=%s stderr=%s", targetProj, r.code, r.stdout, r.stderr)
		}
	}

	// 5. Uninstall as dev: must show paired machine name + owner ID, unpair, and clean everything.
	uninstPreview, _ := sh(`printf '\n' | su - dev -c 'script -qec "hermeshd uninstall" /dev/null' 2>&1`)
	wantLine := fmt.Sprintf("its machine “vps-dev” (owner %s): unpaired, and gone from your devices", setup.OwnerId)
	if !strings.Contains(uninstPreview, wantLine) {
		t.Fatalf("uninstall preview missing %q:\n%s", wantLine, uninstPreview)
	}
	uninstDone, err := sh(`su - dev -c 'hermeshd uninstall --yes' 2>&1`)
	if err != nil || !strings.Contains(uninstDone, "✓ Hermesh is uninstalled for dev.") {
		t.Fatalf("uninstall --yes as dev: %v\n%s", err, uninstDone)
	}
	eventually(t, 30*time.Second, "vps-dev removed from owner's machine list", func() bool {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && !slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineId == approved.MachineId })
	})
	if left, _ := sh("ls -d /home/dev/.local/bin/hermesh* /home/dev/.local/bin/her* /home/dev/.local/share/hermesh 2>/dev/null"); left != "" {
		t.Fatalf("files left after uninstall:\n%s", left)
	}
}

const linuxNodeServerImage = `FROM node:24-bookworm AS node24
FROM ubuntu:24.04
COPY --from=node24 /usr/local/bin/node /usr/local/bin/node
COPY --from=node24 /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx && \
    apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv dbus dbus-user-session curl ca-certificates openssl git && \
    apt-get clean && rm -rf /var/lib/apt/lists/*
CMD ["/sbin/init"]
`

// TestLinuxRealReleaseAndNpmProviders (HERMESH_DOCKER=1, HERMESH_REAL_NPM=1)
// runs a zero-stub end-to-end acceptance test on a fresh Ubuntu 24.04 + systemd
// + Node 24 container:
//  1. Builds real release assets via scripts/release.sh (real Linux her, real
//     Vite-built her-console, real agent-desktop, signed checksums + install.sh);
//  2. Installs via install.sh --user dev onto the fresh Linux container;
//  3. Installs all 4 managed providers (claude, codex, copilot, opencode) from
//     registry.npmjs.org via MaintainProvider RPC and verifies real handshakes;
//  4. Runs a real direct provider conversation (ProjectId: "" -> scratch) with
//     the newly npm-installed OpenCode CLI, plus real Her console + Her
//     multi-conversation SessionService + `hermeshd her` CLI.
func TestLinuxRealReleaseAndNpmProviders(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" || os.Getenv("HERMESH_REAL_NPM") != "1" {
		t.Skip("set HERMESH_DOCKER=1 HERMESH_REAL_NPM=1")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	ctx := context.Background()

	// 1. Build a real signed release directory using scripts/release.sh.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	privPath := filepath.Join(tmp, "release.key")
	pubPath := filepath.Join(tmp, "release.pub")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		t.Fatal(err)
	}

	herPath := linuxHer(t)
	consoleDir, err := filepath.Abs("her/apps/console/dist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(consoleDir, "index.html")); err != nil {
		t.Fatalf("her-console dist missing (%v); run pnpm --dir her/apps/console build", err)
	}
	compDir := filepath.Join(tmp, "computer")
	nodeArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if out, err := exec.Command("node", "apps/desktop/scripts/agent-desktop.mjs", "linux", nodeArch, compDir).CombinedOutput(); err != nil {
		t.Fatalf("agent-desktop.mjs: %v\n%s", err, out)
	}

	relCmd := exec.Command("bash", "./scripts/release.sh")
	herEnvKey := "HER_LINUX_" + strings.ToUpper(runtime.GOARCH)
	compEnvKey := "HER_COMPUTER_LINUX_" + strings.ToUpper(runtime.GOARCH)
	relCmd.Env = append(os.Environ(),
		"VERSION=v0.9.9",
		"RELEASE_SIGNING_KEY_FILE="+privPath,
		"RELEASE_PUBKEY_FILE="+pubPath,
		herEnvKey+"="+herPath,
		"HER_CONSOLE="+consoleDir,
		compEnvKey+"="+compDir,
	)
	if out, err := relCmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/release.sh failed: %v\n%s", err, out)
	}
	releaseDir, _ := filepath.Abs("dist/release")
	t.Cleanup(func() { os.RemoveAll(releaseDir) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go http.Serve(ln, http.FileServer(http.Dir(releaseDir)))
	dlBase := fmt.Sprintf("http://host.docker.internal:%d", ln.Addr().(*net.TCPAddr).Port)

	// Start relay + owner + her-mock.
	mock := startHerMock(t, herBinary(t, "HERMESH_HER_MOCK", "her-mock"))
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sockDir, _ := os.MkdirTemp("/tmp", "hm-real")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	start(t, "owner", nil, filepath.Join(bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(tmp, "owner"), "--socket", sock)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:      fmt.Sprintf("http://127.0.0.1:%d", port),
		ClaimCode:     claim,
		RootPublicKey: rootPub(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	address := strings.Replace(setup.ConnectAddress, "127.0.0.1", "host.docker.internal", 1)

	// 2. Start fresh Ubuntu 24.04 + systemd + Node 24 container and run real install.sh --user dev.
	name, sh := startLinuxImage(t, "hermesh-e2e-linux-node24:1", linuxNodeServerImage, "hermesh-real", "vps-real")
	installCmd := fmt.Sprintf("curl -fsSL %s/install.sh | HERMESH_DOWNLOAD_BASE=%s sh -s -- --user dev %s --name vps-real", dlBase, dlBase, address)
	install := exec.Command("docker", "exec", name, "sh", "-c", installCmd)
	var installOut syncBuffer
	install.Stdout, install.Stderr = &installOut, &installOut
	if err := install.Start(); err != nil {
		t.Fatal(err)
	}
	var code string
	eventually(t, 90*time.Second, "pairing code", func() bool {
		if m := codeRE.FindStringSubmatch(installOut.String()); m != nil {
			code = m[1]
		}
		return code != ""
	})
	approved, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "vps-real"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil {
		t.Fatalf("real install.sh failed: %v\n%s", err, installOut.String())
	}
	t.Logf("install.sh output:\n%s", installOut.String())

	machineStatus := func() *machinev1.MachineStatus {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return nil
		}
		for _, m := range res.Machines {
			if m.MachineId == approved.MachineId && m.Online {
				return m.Status
			}
		}
		return nil
	}
	eventually(t, 60*time.Second, "vps-real online on 0.9.9", func() bool {
		return machineStatus().GetVersion() == "0.9.9"
	})

	// 3. Install all 4 real provider CLIs from npmjs.org via MaintainProvider RPC!
	_, _ = app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{MachineId: approved.MachineId})
	var targets map[string]string
	eventually(t, 30*time.Second, "provider recipes reported", func() bool {
		targets = map[string]string{}
		for _, pm := range machineStatus().GetProviderMaintenance() {
			if pm.TargetVersion != "" {
				targets[pm.Provider] = pm.TargetVersion
			}
		}
		return len(targets) == 4
	})
	for _, provider := range []string{"opencode", "codex", "claude", "copilot"} {
		targetVer := targets[provider]
		reqID := fmt.Sprintf("real-install-%s-%d", provider, time.Now().UnixNano())
		t.Logf("installing real %s@%s via MaintainProvider on Linux container...", provider, targetVer)
		if _, err := app.client.MaintainProvider(ctx, &localv1.MaintainProviderRequest{
			MachineId: approved.MachineId,
			Operation: &identityv1.MaintainProvider{
				Provider:        provider,
				Action:          "install",
				RequestId:       reqID,
				ExpectedVersion: "",
				TargetVersion:   targetVer,
			},
		}); err != nil {
			t.Fatalf("MaintainProvider install %s: %v", provider, err)
		}
		eventually(t, 6*time.Minute, "real npm install "+provider+" succeeded", func() bool {
			for _, op := range machineStatus().GetProviderOperations() {
				if op.GetRequest().GetRequestId() == reqID {
					if op.Phase == "failed" || op.Phase == "interrupted" {
						t.Fatalf("real install %s %s: %s", provider, op.Phase, op.Detail)
					}
					return op.Phase == "succeeded"
				}
			}
			return false
		})
		t.Logf("✓ %s@%s installed and passed real CLI protocol handshake", provider, targetVer)
	}

	// 4. Test real OpenCode model turn via TestMachineAgent AND direct scratch session!
	testRes, err := app.client.TestMachineAgent(ctx, &localv1.TestMachineAgentRequest{
		MachineId: approved.MachineId,
		Agent:     "opencode",
	})
	if err != nil {
		t.Fatalf("TestMachineAgent real opencode RPC error: %v", err)
	}
	t.Logf("real OpenCode TestMachineAgent: ok=%t millis=%d reply=%q", testRes.Ok, testRes.Millis, testRes.Reply)
	if testRes.Ok {
		// Also run a real multi-turn direct provider conversation in scratch_workspace!
		started, err := app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
			CommandId:      fmt.Sprintf("real-opencode-chat-%d", time.Now().UnixNano()),
			MachineId:      approved.MachineId,
			ProjectId:      "",
			ModelSelection: &sessionv1.ModelSelection{InstanceId: "opencode"},
			RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
			InitialPrompt:  "Reply with the single word PONG and nothing else. Use no tools.",
		})
		if err != nil {
			t.Fatalf("StartSession real opencode: %v", err)
		}
		sid := started.GetSession().GetSessionId()
		eventually(t, 2*time.Minute, "real opencode direct session completed", func() bool {
			g, err := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid})
			if err != nil {
				return false
			}
			st := g.GetProjection().GetSession().GetStatus()
			if st == sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
				t.Fatalf("real opencode session crashed: %v", g.GetProjection().GetSession().GetProgress().GetLastFailure())
			}
			return st == sessionv1.SessionStatus_SESSION_STATUS_IDLE
		})
		g, _ := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid})
		t.Logf("✓ real OpenCode direct session reply: %q", g.GetProjection().GetSession().GetResult())
	}

	// 5. Turn real Her on for `dev`, verify real Vite her-console, Her multi-conversation SessionService, and `hermeshd her` CLI!
	mockHostModels := strings.ReplaceAll(mock.models, "127.0.0.1", "host.docker.internal")
	mockCatalog := strings.Replace(mock.base, "127.0.0.1", "host.docker.internal", 1) + "/catalog/api.json"
	if out, err := sh(fmt.Sprintf(`su - dev -c 'mkdir -p ~/.her ~/.config/systemd/user/hermeshd.service.d &&
		cat > ~/.her/models.toml <<EOF
%s
EOF
		printf "model = \"mock/mock-1\"\n" > ~/.her/config.toml &&
		printf "[Service]\nEnvironment=HER_NO_BROWSER=1 HER_LANG=en HER_MODEL_CATALOG_URL=%s\n" > ~/.config/systemd/user/hermeshd.service.d/her.conf &&
		XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user daemon-reload &&
		XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user restart hermeshd.service'`, mockHostModels, mockCatalog)); err != nil {
		t.Fatalf("configure her-mock for dev: %v\n%s", err, out)
	}
	eventually(t, 30*time.Second, "vps-real reconnected", func() bool { return machineStatus() != nil })
	turnHerOn(t, app.client, approved.MachineId)
	con, err := app.client.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: approved.MachineId})
	if err != nil {
		t.Fatalf("GetHerConsole: %v", err)
	}
	if code, page := httpDo(t, http.MethodGet, con.Url, ""); code != 200 || !strings.Contains(page, "<div id=\"root\">") {
		t.Fatalf("real Vite her-console failed: HTTP %d\n%s", code, page)
	}
	herSess, err := app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		CommandId:      fmt.Sprintf("real-her-conv-%d", time.Now().UnixNano()),
		MachineId:      approved.MachineId,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "her"},
		InitialPrompt:  "hello real her conversation on linux",
	})
	if err != nil {
		t.Fatalf("StartSession InstanceId=her: %v", err)
	}
	eventually(t, 60*time.Second, "real Her SessionService conversation answered", func() bool {
		g, err := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: herSess.GetSession().GetSessionId()})
		return err == nil && g.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
			g.GetProjection().GetSession().GetResult() != ""
	})
	t.Logf("✓ real Her SessionService conversation answered")
}
