package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
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
	// The process group of a second Mac, a machine only with a finished
	// session on it, for the spec to take offline (0 when none).
	OtherMac int `json:"otherMac"`
	// On Windows, the folder of an older signer, as if set up, for the app
	// to offer its own (empty elsewhere).
	OldSigner string `json:"oldSigner,omitempty"`
	owner     *ownerMac
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

// windowsSpecs are the specs that run on Windows too (docs/windows.md §14);
// the others are the Mac's (its menu bar, Touch ID, moving to
// Applications, the process limit).
var windowsSpecs = map[string]bool{"windows": true, "shell": true, "motion": true, "search": true, "overview": true, "sheets": true, "settings": true, "session": true, "sessionlist": true}

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
		// Windows runs its own spec and those ported to it (docs/windows.md §11).
		if runtime.GOOS == "windows" && !windowsSpecs[name] || runtime.GOOS != "windows" && name == "windows" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := desktopWorld(t, name == "onboarding", name == "health", name == "settings")
			if name == "session" {
				f.OtherMac = otherMacSession(t, f)
				inPlaceSessions(t, f)
			}
			if name == "windows" {
				f.OldSigner = t.TempDir()
				bin := filepath.Join(f.OldSigner, "hermesh-signer.exe")
				if r := run(t, nil, 5*time.Minute, "go", "build", "-ldflags", "-X main.version=0.0.1", "-o", bin, "./cmd/hermesh-signer"); r.code != 0 {
					t.Fatalf("an older signer: %+v", r)
				}
			}
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
	env := []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp"), "FAKE_ACP_MODELS=gemini-3-pro"}
	if runtime.GOOS != "windows" { // a shell script
		env = append(env, "HERMESH_CODEX_BIN="+codex)
	}
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"gemini"}})
	if wake {
		w.relay.stop()
		w.relayArgs = append(w.relayArgs, "--mcp")
		w.relayEnv = []string{"HERMESH_MCP_LOCAL_WEBHOOKS=1"}
		w.startRelay()
		eventually(t, 30*time.Second, "the machine back online", func() bool {
			return !strings.Contains(w.cli(30*time.Second, "project", "list").stdout, "offline")
		})
	}
	f := desktopFixture{Socket: w.sock, Sessions: desktopSessions(t, w), CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv, owner: w}
	if limited {
		w.nproc = userProcesses(t) + 80
		w.restartOwner(false)
	}
	for i := 0; fresh && i < 12; i++ {
		f.Fresh = append(f.Fresh, newFreshOwner(t, w, fmt.Sprintf("fresh-%d", i), i == 0, i == 8))
	}
	return f
}

// otherMacSession joins a second Mac, work-mac, as a machine only, runs two
// sessions there (在另一台 Mac 上 and 也在另一台 Mac 上, added to f's
// sessions), and returns its daemon's process group.
func otherMacSession(t *testing.T, f desktopFixture) int {
	w := f.owner
	ctx := context.Background()
	home, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	work, _ := w.otherMac("work-mac", []string{"HERMESH_GEMINI_BIN=" + filepath.Join(binaries(t), "fakeacp"), "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")})
	daemon := w.logs[len(w.logs)-1] // the one otherMac started
	st := w.joinTyped(work, home, "work-mac")
	if _, err := work.AddProject(ctx, &localv1.AddProjectRequest{MachineId: st.LocalMachineId, ProjectId: "billing", Path: gitRepo(t, map[string]string{"README.md": "# billing\n"})}); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"在另一台 Mac 上", "也在另一台 Mac 上"} {
		var s sessionJSON
		eventually(t, time.Minute, "a session on work-mac", func() bool {
			s = firstSession(w.cli(2*time.Minute, "session", "start", "--project", "work-mac/billing", "-o", "json", "--agent", "gemini", "--title", title, "--prompt", "say 在那边", "--wait").stdout)
			return s.SessionID != ""
		})
		f.Sessions[title] = s.SessionID
	}
	return daemon.cmd.Process.Pid
}

// inPlaceSessions adds two projects worked on in place on this Mac, a
// plain folder (notes) and a git repository on branch feature/card (live),
// and runs a session in each (不在仓库里 and 就地), added to f's sessions.
func inPlaceSessions(t *testing.T, f desktopFixture) {
	w := f.owner
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	notes := t.TempDir()
	os.WriteFile(filepath.Join(notes, "notes.md"), []byte("notes\n"), 0o644)
	live := gitRepo(t, map[string]string{"README.md": "# live\n"})
	if out, err := exec.Command("git", "-C", live, "checkout", "-q", "-b", "feature/card").CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v %s", err, out)
	}
	for _, p := range []*localv1.AddProjectRequest{{MachineId: st.LocalMachineId, ProjectId: "notes", Path: notes},
		{MachineId: st.LocalMachineId, ProjectId: "live", Path: live, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE}} {
		if _, err := w.app.client.AddProject(ctx, p); err != nil {
			t.Fatalf("add in-place project: %v", err)
		}
	}
	for title, project := range map[string]string{"不在仓库里": "notes", "就地": "live"} {
		s := firstSession(w.cli(2*time.Minute, "session", "start", "--project", project, "-o", "json", "--agent", "gemini", "--title", title, "--prompt", "say 好", "--wait").stdout)
		if s.SessionID == "" {
			t.Fatalf("%s: no session", title)
		}
		f.Sessions[title] = s.SessionID
	}
}

// desktopSessions starts a session in every state the app shows and
// returns their ids by title. Its agent must take the model gemini-3-pro
// (FAKE_ACP_MODELS).
func desktopSessions(t *testing.T, w *ownerMac) map[string]string {
	ids := map[string]string{}
	start := func(title, prompt string, wait bool, more ...string) string {
		t.Helper()
		args := append([]string{"--agent", "gemini", "--title", title, "--prompt", prompt}, more...)
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
	// The one named a model (for its context card); the others follow the agent's own.
	done := start("写 hello", "say 先写文件\nwrite hello.txt hello\nshell echo ran > ran.txt\nsay 好了", true, "--model", "gemini-3-pro")
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
	antigravity := stub("antigravity", wait("antigravity")+fmt.Sprintf("exec %q \"$@\"\n", filepath.Join(w.bin, "fakeacp")))
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	env := []string{"HERMESH_CLAUDE_BIN=" + claude, "HERMESH_CODEX_BIN=" + codex,
		"HERMESH_GEMINI_BIN=" + gemini, "FAKE_ACP_STATE=" + filepath.Join(tmp, "fakeacp"),
		"HERMESH_ANTIGRAVITY_BIN=" + antigravity, "GEMINI_HOME=" + filepath.Join(tmp, "gemini")}
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
