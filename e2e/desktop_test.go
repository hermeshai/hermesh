package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/provider/acp"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// desktopFixture is what the desktop app's Playwright tests get
// (HERMESH_E2E_FIXTURE): the owner daemon's socket, the sessions by title,
// and the agent CLI to change things while the app looks.
type desktopFixture struct {
	Socket   string            `json:"socket"`
	Sessions map[string]string `json:"sessions"`
	CLI      string            `json:"cli"`
	CLIEnv   []string          `json:"cliEnv"`
	// Owner daemons nobody has set up yet, for the first-run tests (each
	// can be set up once: that claims its relay).
	Fresh []freshOwner `json:"fresh"`
}

// freshOwner is an owner daemon not set up yet: its socket, a relay of its
// own and that relay's claim code. A test signer watches it: it creates the
// root key when asked and approves every signature.
type freshOwner struct {
	Socket    string `json:"socket"`
	RelayURL  string `json:"relayURL"`
	ClaimCode string `json:"claimCode"`
	// Gate, when set, is a directory: each coding agent's version check
	// waits there for a file named after it, so a test sees the agents
	// being checked and lets each check end when it chooses.
	Gate string `json:"gate,omitempty"`
}

// TestDesktop runs the desktop app's end-to-end tests: Playwright drives
// the real Electron app (apps/desktop/e2e) against sessions in every state
// the app shows, played by a scripted ACP agent through the real relay,
// owner daemon and agent CLI. Each spec file gets a world of its own (the
// tests change it), and they run side by side. HERMESH_DESKTOP_GREP picks
// tests by name.
//
// With HERMESH_DESKTOP_PREVIEW=<seconds> it keeps one world up instead, so
// the app can be looked at against it:
//
//	HERMESH_DESKTOP_PREVIEW=1800 go test ./e2e -run TestDesktop -v -timeout 60m
//	HERMESH_SOCKET=<socket it prints> pnpm --dir apps/desktop dev
func TestDesktop(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	if secs, _ := strconv.Atoi(os.Getenv("HERMESH_DESKTOP_PREVIEW")); secs > 0 {
		f := desktopWorld(t, false, false, false)
		t.Logf("sessions ready; open the app with:\nHERMESH_SOCKET=%s pnpm --dir apps/desktop dev", f.Socket)
		time.Sleep(time.Duration(secs) * time.Second)
		return
	}
	dir, _ := filepath.Abs(filepath.Join("apps", "desktop")) // TestMain runs from the repository root
	out := filepath.Join(t.TempDir(), "app")
	// The app bundles the release notes up to VERSION: the oldest release's
	// alone, so the tests read the same history however many releases follow.
	if r := run(t, []string{"HERMESH_DESKTOP_OUT=" + out, "VERSION=0.1.0-rc7"}, 5*time.Minute, "pnpm", "--dir", dir, "build"); r.code != 0 {
		t.Fatalf("build: %+v", r)
	}
	// Electron fetches its binary on first use; do it once, not from every spec at the same time.
	if r := run(t, nil, 5*time.Minute, "node", filepath.Join(dir, "node_modules", "electron", "install.js")); r.code != 0 {
		t.Fatalf("electron install: %+v", r)
	}
	// Its first run is slow (macOS checks a newly downloaded app): get that over with too.
	if r := run(t, nil, 5*time.Minute, "pnpm", "--dir", dir, "exec", "electron", "--version"); r.code != 0 {
		t.Fatalf("electron --version: %+v", r)
	}
	specs, _ := filepath.Glob(filepath.Join(dir, "e2e", "*.spec.ts"))
	for _, spec := range specs {
		name := strings.TrimSuffix(filepath.Base(spec), ".spec.ts")
		if name == "packaged" || name == "update" { // TestDesktopPackaged, TestDesktopUpdate
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := desktopWorld(t, name == "onboarding", name == "health", name == "settings")
			file := filepath.Join(t.TempDir(), "fixture.json")
			b, _ := json.Marshal(f)
			os.WriteFile(file, b, 0o644)
			args := []string{"--dir", dir, "exec", "playwright", "test", filepath.Base(spec), "--pass-with-no-tests"}
			if g := os.Getenv("HERMESH_DESKTOP_GREP"); g != "" {
				args = append(args, "--grep", g)
			}
			env := []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_DESKTOP_OUT=" + out, "HERMESH_PW_OUT=" + filepath.Join(t.TempDir(), "results")}
			if r := run(t, env, 10*time.Minute, "pnpm", args...); r.code != 0 {
				t.Fatalf("playwright:\n%s\n%s", r.stdout, r.stderr)
			} else {
				t.Log(r.stdout)
			}
		})
	}
}

// desktopWorld is an owner Mac with a session in every state the app
// shows, and (fresh) owner daemons nobody has set up yet for the first run.
// limited: its hermeshd may start only 80 more processes than the user runs
// (health.spec.ts takes them). wake: its relay runs the MCP gateway, which
// keeps agents' doorbells, and may call webhooks on this computer
// (settings.spec.ts sets one up).
func desktopWorld(t *testing.T, fresh, limited, wake bool) desktopFixture {
	fake := filepath.Join(binaries(t), "fakeacp")
	// Codex installed, not logged in, offering fakecodex's models: the same
	// whatever this machine has.
	codex := filepath.Join(t.TempDir(), "codex")
	os.WriteFile(codex, []byte(fmt.Sprintf("#!/bin/sh\ncase \"$1\" in --version) echo \"codex-cli 0.48.0\";; login) echo \"Not logged in\"; exit 1;; app-server) exec %q;; esac\n",
		filepath.Join(binaries(t), "fakecodex"))), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp"), "HERMESH_CODEX_BIN=" + codex},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	if wake {
		w.relay.stop()
		w.relayArgs = append(w.relayArgs, "--mcp")
		w.relayEnv = []string{"HERMESH_MCP_LOCAL_WEBHOOKS=1"}
		w.startRelay()
		eventually(t, 30*time.Second, "the machine back online", func() bool {
			return !strings.Contains(w.cli(30*time.Second, "project", "list").stdout, "offline")
		})
	}
	f := desktopFixture{Socket: w.sock, Sessions: desktopSessions(t, w), CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv}
	if limited {
		w.nproc = userProcesses(t) + 80
		w.restartOwner(false)
	}
	for i := 0; fresh && i < 9; i++ {
		f.Fresh = append(f.Fresh, newFreshOwner(t, w, fmt.Sprintf("fresh-%d", i), i == 0, i == 8))
	}
	return f
}

// desktopSessions starts a session in every state the app shows and
// returns their ids by title.
func desktopSessions(t *testing.T, w *ownerMac) map[string]string {
	ids := map[string]string{}
	start := func(title, prompt string, wait bool) string {
		t.Helper()
		args := []string{"--agent", "gemini", "--title", title, "--prompt", prompt}
		if wait {
			args = append(args, "--wait")
		}
		s, r := w.startSession(2*time.Minute, args...)
		if s.SessionID == "" {
			t.Fatalf("%s: %+v", title, r)
		}
		ids[title] = s.SessionID
		return s.SessionID
	}
	done := start("写 hello", "say 先写文件\nwrite hello.txt hello\nshell echo ran > ran.txt\nsay 好了", true)
	if s, r := w.send(time.Minute, done, "write hello.txt hello again\nsay 改好了"); s.Status != "idle" {
		t.Fatalf("second turn: %+v", r)
	}
	start("限流", "fail rate limited", true)
	start("崩溃", "say 开始\ncrash", true)
	start("选颜色", "ask 选哪个颜色|红,蓝", true)
	running := start("跑很久", "shell sleep 1800", false)
	archived := start("归档的", "say hi", true)
	if r := w.cli(30*time.Second, "session", "archive", archived); r.code != 0 {
		t.Fatalf("archive: %+v", r)
	}
	eventually(t, time.Minute, "the long session runs its command", func() bool {
		return w.get(running).Status == "running" && strings.Contains(w.cli(30*time.Second, "session", "events", running).stdout, "sleep 1800")
	})
	return ids
}

// newFreshOwner starts a relay and an owner daemon that is not set up, with
// a test signer watching it. Its coding agents are stand-ins, so the first
// run shows the same on every computer: Claude Code logged in, Codex
// installed but not logged in, and Gemini CLI played by the scripted ACP
// agent (the one that can run sessions). gated: its agents' checks wait
// for the test (freshOwner.Gate). bare: a computer with no coding agent
// at all (each one's check fails).
func newFreshOwner(t *testing.T, w *ownerMac, name string, gated, bare bool) freshOwner {
	t.Helper()
	tmp := t.TempDir()
	relayBin := filepath.Join(w.bin, "hermesh-relay")
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, name+" relay", nil, relayBin, "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	w.logs = append(w.logs, relay)
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)

	stub := func(file, script string) string {
		p := filepath.Join(tmp, file)
		os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755)
		return p
	}
	gate, wait := "", func(string) string { return "" }
	if gated {
		gate = filepath.Join(tmp, "gate")
		os.Mkdir(gate, 0o755)
		wait = func(agent string) string {
			return fmt.Sprintf(`[ "$1" = --version ] && while [ ! -e %q ]; do sleep 0.1; done`+"\n", filepath.Join(gate, agent))
		}
	}
	claude := stub("claude", wait("claude")+`case "$1" in --version) echo "2.1.4 (Claude Code)";; auth) echo '{"loggedIn":true}';; esac`+"\n")
	codex := stub("codex", wait("codex")+fmt.Sprintf(`case "$1" in --version) echo "codex-cli 0.48.0";; login) echo "Not logged in"; exit 1;; app-server) exec %q;; esac`, filepath.Join(w.bin, "fakecodex"))+"\n")
	gemini := stub("gemini", wait("gemini")+fmt.Sprintf("exec %q \"$@\"\n", filepath.Join(w.bin, "fakeacp")))
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	env := []string{"HERMESH_CLAUDE_BIN=" + claude, "HERMESH_CODEX_BIN=" + codex,
		"HERMESH_GEMINI_BIN=" + gemini, "FAKE_ACP_STATE=" + filepath.Join(tmp, "fakeacp")}
	if bare {
		missing := filepath.Join(tmp, "not-installed") // no such file
		env = []string{"HERMESH_CLAUDE_BIN=" + missing, "HERMESH_CODEX_BIN=" + missing}
		for _, a := range acp.Agents {
			env = append(env, "HERMESH_"+strings.ToUpper(a.Name)+"_BIN="+missing)
		}
	}
	owner := start(t, name+" owner", env, filepath.Join(w.bin, "hermeshd"),
		"run", "--owner", "--data-dir", filepath.Join(tmp, "owner"), "--socket", sock)
	w.logs = append(w.logs, owner)
	root, _ := identity.GenerateKey()
	newTestApp(t, sock, root)
	return freshOwner{Socket: sock, RelayURL: fmt.Sprintf("http://127.0.0.1:%d", port), ClaimCode: claim, Gate: gate}
}
