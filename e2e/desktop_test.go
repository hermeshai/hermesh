package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/runtime/acp"
	"github.com/hermeshai/hermesh/internal/shellenv"
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
	// The address of a connector's service that signs in with OAuth, for
	// Her to ask for on a secure card (empty when none).
	Connector string `json:"connector,omitempty"`
	// This Mac's her home, for the spec to change a file Her sent as her
	// model would (empty when none).
	HerHome string `json:"herHome,omitempty"`
	// her-mock's model service, http://127.0.0.1:<port>, where a service
	// added by its address answers under /gateway/v1 (empty when none).
	ModelService string `json:"modelService,omitempty"`
	// her's ChatGPT fixture, standing in for OpenAI, for the app to open
	// its sign-in page (empty when none).
	ChatGPT string `json:"chatgpt,omitempty"`
	// The file of the methods this Mac's her acts as if it lacked
	// (HER_TEST_WITHOUT), and the socket of this fixture's Her instance.
	HerWithout string `json:"herWithout,omitempty"`
	HerSocket  string `json:"herSocket,omitempty"`
	// old-box, a machine from before Her (TestDesktopHerBeforeHer): the file
	// that says so while it holds 1, and the start of its hermeshd's command
	// line, for the spec to stop it so that it starts again as it is now.
	BeforeHer       string `json:"beforeHer,omitempty"`
	BeforeHerDaemon string `json:"beforeHerDaemon,omitempty"`
	owner           *ownerMac
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
var windowsSpecs = map[string]bool{"windows": true, "shell": true, "motion": true, "search": true, "overview": true, "sheets": true, "settings": true, "session": true, "sessionlist": true, "thread-v2": true, "title-i18n": true}

// linuxSpecs are the specs that run on Linux too (docs/linux.md §7.1), under
// a display such as Xvfb's and an init that reaps orphans (docker --init).
var linuxSpecs = map[string]bool{"shell": true, "motion": true, "search": true, "overview": true, "sheets": true, "settings": true, "session": true, "sessionlist": true,
	"conversation": true, "model-catalogs": true, "onboarding": true, "language": true, "menus": true, "frame": true, "health": true, "team": true, "thread-v2": true, "title-i18n": true}

// These specs read back what they copied. The clipboard is the whole
// computer's, so they run one at a time, before the specs that run side by
// side (a parallel subtest starts only when the serial ones are done).
var clipboardSpecs = map[string]bool{"health": true, "session": true, "settings": true, "sheets": true}

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
		if name == "provider-maintenance" { // Uses the separately paired TestProviderMaintenanceReal target.
			continue
		}
		if name == "packaged" || name == "update" || name == "her" || name == "her-notifications" || name == "her-chatgpt" || name == "her-older" || name == "her-before" || name == "her-reply" || name == "progress" || name == "her-deliveries" || name == "her-page" || name == "her-conversations" || name == "her-long" || name == "coldstart" { // TestDesktopPackaged, TestDesktopUpdate, TestDesktopHer, TestDesktopHerChatGPT, TestDesktopHerOlder, TestDesktopHerBeforeHer, TestDesktopHerReply, TestDesktopHerProgress, TestDesktopHerDeliveries, TestDesktopHerPage, TestDesktopHerLong, TestDesktopColdStart
			continue
		}
		// Windows runs its own spec and those ported to it (docs/windows.md §11).
		if runtime.GOOS == "windows" && !windowsSpecs[name] || runtime.GOOS != "windows" && name == "windows" || runtime.GOOS == "linux" && !linuxSpecs[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if !clipboardSpecs[name] {
				t.Parallel()
			}
			f := desktopWorld(t, name == "onboarding", name == "health", name == "settings", name == "title-i18n")
			if name == "session" {
				f.OtherMac = otherMacSession(t, f)
				inPlaceSessions(t, f)
			}
			if name == "thread-composer" {
				f.OtherMac = otherMacThreads(t, f)
			}
			if name == "session-files" {
				f.OtherMac = otherMacFiles(t, f)
			}
			if name == "limits" {
				otherMacLimits(t, f)
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

// TestDesktopHer runs the Her entry's end-to-end test (apps/desktop/e2e/
// her.spec.ts) against her itself, with her-mock as its model service: the
// first run on this Mac, a chat, work-mac added as a second environment,
// given a model through Her's onboarding and switched to, and work-mac going
// offline. It needs her built (cargo build in her/, or HERMESH_HER_BIN), so
// it is not one of TestDesktop's specs. HERMESH_DESKTOP_GREP picks tests by
// name.
//
//	go test ./e2e -run TestDesktopHer -v -timeout 30m
//
// With HERMESH_DESKTOP_PREVIEW=<seconds> it keeps the world up instead, for
// a person to try the Her entry against it; HERMESH_HER_MODEL (e.g.
// deepseek/deepseek-flash) makes this Mac's her use a real model, its key
// taken from the environment (e.g. DEEPSEEK_API_KEY), while work-mac's
// starts with none, for the onboarding:
//
//	HERMESH_DESKTOP_PREVIEW=3600 go test ./e2e -run TestDesktopHer -v -timeout 70m
//	HERMESH_SOCKET=<socket it prints> pnpm --dir apps/desktop dev
func TestDesktopHer(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	f := desktopHerWorld(t)
	if secs, _ := strconv.Atoi(os.Getenv("HERMESH_DESKTOP_PREVIEW")); secs > 0 {
		t.Logf("Her's world is ready (this Mac and work-mac, Her on for neither); open the app with:\nHERMESH_SOCKET=%s pnpm --dir apps/desktop dev", f.Socket)
		time.Sleep(time.Duration(secs) * time.Second)
		return
	}
	herSpec(t, f, "her.spec.ts")
}

// TestDesktopHerNotifications exercises the Mac window’s focus, visibility and Her notifications.
func TestDesktopHerNotifications(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("e2e on macOS")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	herSpec(t, desktopHerWorld(t), "her-notifications.spec.ts")
}

// TestDesktopHerProgress runs the Progress page's end-to-end test (apps/
// desktop/e2e/progress.spec.ts, docs/her-team.md §15) in the same world:
// Her on this Mac hands work to a sub-agent and starts a coding session, and
// the page shows both as cards with their records.
//
//	go test ./e2e -run TestDesktopHerProgress -v -timeout 30m
func TestDesktopHerProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	herSpec(t, desktopHerWorld(t), "progress.spec.ts")
}

// TestDesktopHerReply runs the end-to-end test of replying to a message of
// Her's conversation (apps/desktop/e2e/her-reply.spec.ts, docs/her-reply.md)
// in the same world: the owner replies to a message of Her's, to words
// selected in it and to one of their own, and Her is told what each replies
// to.
//
//	go test ./e2e -run TestDesktopHerReply -v -timeout 30m
func TestDesktopHerReply(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	herSpec(t, desktopHerWorld(t), "her-reply.spec.ts")
}

// TestDesktopHerDeliveries runs the end-to-end test of the files Her sent, in
// a list of their own (apps/desktop/e2e/her-deliveries.spec.ts,
// docs/her-deliverables.md) in the same world: Her sends a file, it is
// changed and sent again, and the owner finds both versions in the panel,
// opens each as it was sent, goes to its message and replies to it.
//
//	go test ./e2e -run TestDesktopHerDeliveries -v -timeout 30m
func TestDesktopHerDeliveries(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	herSpec(t, desktopHerWorld(t), "her-deliveries.spec.ts")
}

// TestDesktopHerPage runs Her's page (apps/desktop/e2e/her-page.spec.ts,
// docs/her-panel-demo.html) in the same world: her tabs, and the pages her
// cards and rows open beside the conversation (skills, web search, the
// browsers, her model, her environment and its storage), read and changed
// against her itself.
//
//	go test ./e2e -run TestDesktopHerPage -v -timeout 30m
func TestDesktopHerPage(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	herSpec(t, desktopHerWorld(t), "her-page.spec.ts")
}

// TestDesktopHerConversations runs the end-to-end test of conversations with
// Her besides her main one (apps/desktop/e2e/her-conversations.spec.ts,
// docs/her-conversations.md) in the same world: a new session given to Her,
// the coding session she starts nested under it in the task list, and its
// news back in that conversation.
//
//	go test ./e2e -run TestDesktopHerConversations -v -timeout 30m
func TestDesktopHerConversations(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	herSpec(t, desktopHerWorld(t), "her-conversations.spec.ts")
}

// TestDesktopHerChatGPT runs apps/desktop/e2e/her-chatgpt.spec.ts: on this
// Mac, whose her has no model yet, Her's onboarding signs in to ChatGPT all
// the way. her's ChatGPT fixture stands in for OpenAI, as in
// TestHerChatGPTSignIn: her takes its address only as a debug build.
//
//	go test ./e2e -run TestDesktopHerChatGPT -v -timeout 30m
func TestDesktopHerChatGPT(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	chatgpt := chatGPTFixture(t)
	env := append(herEnv(her, herConsole(t), t.TempDir(), mock.base), "HER_TEST_CHATGPT_BASE="+chatgpt)
	// No model or provider key from this computer, as for work-mac in desktopHerWorld.
	for _, kv := range shellenv.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasSuffix(name, "_API_KEY") || name == "HER_MODEL" {
			env = append(env, name+"=")
		}
	}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	t.Cleanup(w.owner.stop) // stopped as a service is, so that it stops her
	herSpec(t, desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv, ChatGPT: chatgpt, owner: w}, "her-chatgpt.spec.ts")
}

// TestDesktopHerOlder runs apps/desktop/e2e/her-older.spec.ts: this Mac's
// her stands in for one older than the app (HER_TEST_WITHOUT,
// docs/version-skew.md §8), first one from before her listed her methods,
// then one that lists them without bots/memory, then one that has it; the
// spec restarts her between, as an update does.
//
//	go test ./e2e -run TestDesktopHerOlder -v -timeout 30m
func TestDesktopHerOlder(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	without := filepath.Join(t.TempDir(), "her-without")
	if err := os.WriteFile(without, []byte("bots/memory\nno-list\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(herEnv(her, herConsole(t), herHome(t, mock.models), mock.base), "HER_TEST_WITHOUT="+without)
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	t.Cleanup(w.owner.stop) // stopped as a service is, so that it stops her
	ownerDir := filepath.Join(w.tmp, "owner-mac", "owner")
	current, err := os.ReadFile(filepath.Join(ownerDir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	sock := paths.Her(filepath.Join(ownerDir, "profiles", string(current), "machine"))
	herSpec(t, desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv,
		HerWithout: without, HerSocket: sock, owner: w}, "her-older.spec.ts")
}

// TestDesktopHerBeforeHer runs apps/desktop/e2e/her-before.spec.ts: old-box,
// a second Mac whose hermeshd is from before Her while a file says so
// (HERMESH_TEST_BEFORE_HER, docs/version-skew.md §6.7). A loop starts it
// again when the spec stops it, as an update does, as it is then.
//
//	go test ./e2e -run TestDesktopHerBeforeHer -v -timeout 30m
func TestDesktopHerBeforeHer(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	w := newOwnerMac(t, herEnv(her, herConsole(t), herHome(t, mock.models), mock.base), &localv1.GrantSpec{})
	t.Cleanup(w.owner.stop) // stopped as a service is, so that it stops her
	home, err := w.app.client.GetStatus(context.Background(), &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	flag := filepath.Join(t.TempDir(), "before-her")
	if err := os.WriteFile(flag, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock, data, bin := filepath.Join(sockDir, "o.sock"), filepath.Join(w.tmp, "old-box"), filepath.Join(w.bin, "hermeshd")
	loop := `while :; do HERMESH_TEST_BEFORE_HER="$(cat "$1")" "$2" run --owner --data-dir "$3" --socket "$4"; sleep 0.2; done`
	w.logs = append(w.logs, start(t, "owner old-box", herEnv(her, herConsole(t), t.TempDir(), mock.base), "/bin/sh", "-c", loop, "sh", flag, bin, data, sock))
	w.joinTyped(localClient(t, sock), home, "old-box")
	herSpec(t, desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv,
		BeforeHer: flag, BeforeHerDaemon: bin + " run --owner --data-dir " + data, owner: w}, "her-before.spec.ts")
}

// herSpec builds the app and runs one of the Her entry's specs against f.
// HERMESH_DESKTOP_GREP picks tests by name.
func herSpec(t *testing.T, f desktopFixture, spec string) {
	t.Helper()
	dir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	out := filepath.Join(t.TempDir(), "app")
	if r := run(t, []string{"HERMESH_DESKTOP_OUT=" + out, "VERSION=0.1.0-rc7"}, 5*time.Minute, "pnpm", "--dir", dir, "build"); r.code != 0 {
		t.Fatalf("build: %+v", r)
	}
	if r := run(t, nil, 5*time.Minute, "node", filepath.Join(dir, "node_modules", "electron", "install.js")); r.code != 0 {
		t.Fatalf("electron install: %+v", r)
	}
	file := filepath.Join(t.TempDir(), "fixture.json")
	b, _ := json.Marshal(f)
	os.WriteFile(file, b, 0o644)
	args := []string{"--dir", dir, "exec", "playwright", "test", spec}
	if g := os.Getenv("HERMESH_DESKTOP_GREP"); g != "" {
		args = append(args, "--grep", g)
	}
	env := []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_DESKTOP_OUT=" + out, "HERMESH_PW_OUT=" + filepath.Join(t.TempDir(), "results")}
	if r := run(t, env, 15*time.Minute, "pnpm", args...); r.code != 0 {
		t.Fatalf("playwright:\n%s\n%s", r.stdout, r.stderr)
	} else {
		t.Log(r.stdout)
	}
}

// desktopHerWorld is an owner Mac whose hermeshd can run Her (her, her-mock
// serving its models, the e2e console), and work-mac, a second Mac that is a
// machine only and can run it too, each with a home of its own for her: two
// machines to become environments, Her on for neither yet. This Mac's her
// has her-mock's model; work-mac's has none that works, as a new server's:
// her-mock answers there in DeepSeek's place and takes a key, as DeepSeek
// does (the onboarding offers the providers people know best, all in her's
// own catalog, so a provider named after her-mock would not be among them),
// and in Tencent Cloud Token Plan's, and nothing names a model. OtherMac is work-mac's daemon, for the spec to
// take offline. Connector is a connector's service on this computer, for
// Her to ask for on a secure card: with OAuth, it registers a client for the
// redirect and approves at once, standing in for the person.
func desktopHerWorld(t *testing.T) desktopFixture {
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	console := herConsole(t)
	tracker := start(t, "tracker", nil, herMCPServer(t), "--http", "127.0.0.1:0", "--oauth", "--no-metadata-document", "--name", "tracker")
	var connector string
	eventually(t, 20*time.Second, "the tracker's address", func() bool {
		connector = regexp.MustCompile(`http://\S+/mcp`).FindString(tracker.output())
		return connector != ""
	})
	macHome, workHome := herHome(t, mock.models), t.TempDir()
	// This Mac's Her uses the owner's own Chrome, whose extension never connects here: a step that
	// asks for it waits, and the chat says so (her.spec.ts).
	config := "[browser]\nextension = true\n"
	if model := os.Getenv("HERMESH_HER_MODEL"); model != "" {
		config = fmt.Sprintf("model = %q\n\n", model) + config
	}
	if err := os.WriteFile(filepath.Join(macHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	base := regexp.MustCompile(`base_url = "([^"]+)"`).FindStringSubmatch(mock.models)[1]
	// And a service of the owner's own, added by its address and given no key yet: it
	// connects no model, and her-page.spec.ts finds it on the Model page and removes it.
	models := fmt.Sprintf("[providers.deepseek]\nbase_url = %q\n\n[providers.tencent-token-plan]\nbase_url = %q\n\n[providers.acme]\nname = \"Acme Gateway\"\napi = \"openai-chat\"\nbase_url = %q\n", base, base, mock.base+"/gateway/v1")
	if err := os.WriteFile(filepath.Join(workHome, "models.toml"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nor a key from this computer: work-mac's her has none of the provider
	// keys, or the model, this process or the login shell (which hermeshd
	// reads for her) may have.
	workEnv := herEnv(her, console, workHome, mock.base)
	for _, kv := range shellenv.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasSuffix(name, "_API_KEY") || name == "HER_MODEL" {
			workEnv = append(workEnv, name+"=")
		}
	}
	// Scripted coding agents on this Mac, for the sessions Her starts (docs/her-team.md §14.3).
	w := newOwnerMac(t, append(herEnv(her, console, macHome, mock.base), "HERMESH_FAKE_PROVIDERS=1"), &localv1.GrantSpec{})
	t.Cleanup(w.owner.stop) // stopped as a service is, so that it stops her
	home, err := w.app.client.GetStatus(context.Background(), &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	work, _ := w.otherMac("work-mac", workEnv)
	daemon := w.logs[len(w.logs)-1] // the one otherMac started
	t.Cleanup(daemon.stop)
	w.joinTyped(work, home, "work-mac")
	return desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv, OtherMac: daemon.cmd.Process.Pid, Connector: connector, HerHome: macHome, ModelService: mock.base, owner: w}
}

// desktopWorld is an owner Mac with a session in every state the app
// shows, and (fresh) owner daemons nobody has set up yet for the first run.
// limited: its hermeshd may start only 80 more processes than the user runs
// (health.spec.ts takes them). wake: its relay runs the MCP gateway, which
// keeps agents' doorbells, and may call webhooks on this computer
// (settings.spec.ts sets one up).
func desktopWorld(t *testing.T, fresh, limited, wake bool, titleGen ...bool) desktopFixture {
	fake := filepath.Join(binaries(t), "fakeacp")
	// Codex installed, not logged in, offering fakecodex's models: the same
	// whatever this machine has.
	codex := filepath.Join(t.TempDir(), "codex")
	os.WriteFile(codex, []byte(fmt.Sprintf("#!/bin/sh\ncase \"$1\" in --version) echo \"codex-cli 0.48.0\";; login) echo \"Not logged in\"; exit 1;; app-server) exec %q;; esac\n",
		filepath.Join(binaries(t), "fakecodex"))), 0o755)
	env := []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp"), "FAKE_ACP_MODELS=gemini-3-pro", "HERMESH_INCLUDE_SCRIPTED=1"}
	if len(titleGen) > 0 && titleGen[0] {
		env = append(env, "HERMESH_FAKE_TITLE_GEN=1")
	}
	// Not this machine's Claude Code and Antigravity, which would tell its
	// owner's limits: not installed, as on CI.
	missing := filepath.Join(t.TempDir(), "not-installed")
	env = append(env, "HERMESH_CLAUDE_BIN="+missing, "HERMESH_ANTIGRAVITY_BIN="+missing)
	// The provider list fixture has Gemini installed and two folded, missing CLIs.
	env = append(env, "HERMESH_AUGGIE_BIN="+missing, "HERMESH_DEEPSEEK_BIN="+missing)
	if runtime.GOOS != "windows" { // a shell script
		env = append(env, "HERMESH_CODEX_BIN="+codex)
	}
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"gemini"}}, "--retry-delay", "0") // its rate limit fails at once
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
		if runtime.GOOS == "linux" { // the limit counts threads there, hermeshd's own among them
			w.nproc += 220
		}
		w.restartOwner(false)
	}
	for i := 0; fresh && i < 12; i++ {
		f.Fresh = append(f.Fresh, newFreshOwner(t, w, fmt.Sprintf("fresh-%d", i), i == 0, i == 8))
	}
	return f
}

// otherMacThreads joins a second Mac, work-mac, as a machine only, with the
// scripted agent for its threads, a home folder of its own and no project:
// the spec adds them from the app (thread-composer.spec.ts); and old-mac, a
// machine from before threads. It returns work-mac's daemon's process group,
// for the spec to take it offline.
func otherMacThreads(t *testing.T, f desktopFixture) int {
	w := f.owner
	home, err := w.app.client.GetStatus(context.Background(), &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Its own home folder: what Add a Project lists, browses and makes folders in. ~/code/recent-app and
	// ~/code/mono are repositories; ~/code/mono/web is a folder inside one.
	workHome := t.TempDir()
	for _, name := range []string{"recent-app", "mono"} {
		dir := filepath.Join(workHome, "code", name)
		os.MkdirAll(filepath.Join(dir, "web"), 0o755)
		os.WriteFile(filepath.Join(dir, "web", "README.md"), []byte("# "+name+"\n"), 0o644)
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	work, _ := w.otherMac("work-mac", []string{"HERMESH_INCLUDE_SCRIPTED=1", "HOME=" + workHome})
	daemon := w.logs[len(w.logs)-1] // the one otherMac started
	w.joinTyped(work, home, "work-mac")
	return daemon.cmd.Process.Pid
}

// otherMacLimits joins a second Mac, limits-mac, as a machine only, whose
// coding agents are the fake, and runs a session there in which the agent
// says its account's rate limits (limits.spec.ts): five hours 86% used, a
// week 52%, on Pro. The grant's agent is gemini, so the fake plays it.
func otherMacLimits(t *testing.T, f desktopFixture) {
	w := f.owner
	ctx := context.Background()
	home, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	mac, _ := w.otherMac("limits-mac", []string{"HERMESH_FAKE_PROVIDERS=1", "HERMESH_FAKE_AGENTS=gemini"})
	st := w.joinTyped(mac, home, "limits-mac")
	if _, err := mac.AddProject(ctx, &localv1.AddProjectRequest{MachineId: st.LocalMachineId, ProjectId: "quota", Path: gitRepo(t, map[string]string{"README.md": "# quota\n"})}); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Minute, "a session on limits-mac", func() bool {
		s := firstSession(w.cli(2*time.Minute, "session", "start", "--project", "limits-mac/quota", "-o", "json", "--agent", "gemini", "--title", "用掉一些额度",
			"--prompt", "limits 300:86,10080:52,plan=pro\nsay 好了", "--wait").stdout)
		return s.Status == "idle"
	})

	// agy-mac: Antigravity alone, as the agy CLI, asked its quota when its
	// agents are checked (Check Again, from this Mac): two groups of models,
	// none low; the second's five hours not started yet, which agy says as a
	// reset in 1970.
	at := func(hours float64) string {
		return time.Now().Add(time.Duration(hours * float64(time.Hour))).UTC().Format(time.RFC3339)
	}
	usage := fmt.Sprintf(`{"command":{"name":"usage","data":{"groups":[`+
		`{"name":"Gemini Models","buckets":[{"window":"weekly","remaining_fraction":0.83,"reset_time":%q},{"window":"5h","remaining_fraction":0.97,"reset_time":%q}]},`+
		`{"name":"Claude and GPT models","buckets":[{"window":"weekly","remaining_fraction":0.35,"reset_time":%q},{"window":"5h","remaining_fraction":1,"reset_time":"1970-01-01T00:00:00Z"}]}]}}}`,
		at(146), at(4.5), at(18))
	missing := filepath.Join(t.TempDir(), "not-installed")
	env := append(fakeAgy(t, []byte(usage)), "HERMESH_CLAUDE_BIN="+missing, "HERMESH_CODEX_BIN="+missing)
	for _, a := range acp.Agents {
		if a.Name != "antigravity" {
			env = append(env, "HERMESH_"+strings.ToUpper(a.Name)+"_BIN="+missing)
		}
	}
	agy, _ := w.otherMac("agy-mac", env)
	joined := w.joinTyped(agy, home, "agy-mac")
	eventually(t, time.Minute, "agy-mac checked again", func() bool {
		_, err := w.app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{MachineId: joined.LocalMachineId, Limits: true})
		return err == nil
	})
	eventually(t, time.Minute, "Antigravity's quota from agy-mac", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		for _, m := range res.Machines {
			for _, a := range m.Status.GetAgents() {
				if a.Name == "antigravity" && len(a.Limits.GetWindows()) == 4 {
					return true
				}
			}
		}
		return false
	})
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

// otherMacFiles joins a second Mac, files-mac, with a project whose session
// (文件预览) writes a page with its script beside it and a file of 40 lines,
// and links them in its reply; its second turn changes the page
// (apps/desktop/e2e/session-files.spec.ts, docs/session-files.md). Returns
// files-mac's hermeshd's process, for the spec to take the machine away.
func otherMacFiles(t *testing.T, f desktopFixture) int {
	w := f.owner
	ctx := context.Background()
	home, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	work, _ := w.otherMac("files-mac", []string{"HERMESH_GEMINI_BIN=" + filepath.Join(binaries(t), "fakeacp"), "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")})
	daemon := w.logs[len(w.logs)-1]
	st := w.joinTyped(work, home, "files-mac")
	if _, err := work.AddProject(ctx, &localv1.AddProjectRequest{MachineId: st.LocalMachineId, ProjectId: "site", Path: gitRepo(t, map[string]string{"README.md": "# site\n"})}); err != nil {
		t.Fatal(err)
	}
	page := `<html><body><p id=t>第一版</p><button id=b onclick="document.getElementById('t').textContent='点过了'">点我</button><script src="demo.js"></script></body></html>`
	prompt := strings.Join([]string{
		"write docs/demo.html " + page,
		"write docs/demo.js document.title = 'demo.js 读到了';",
		`shell mkdir -p src && seq 1 40 | sed 's/^/line /' > src/lines.txt`,
		"say 原型：[demo.html](docs/demo.html)，代码在 `src/lines.txt:30`，旧的 [gone.md](gone.md)，还有 [外面](/etc/hosts)。",
	}, "\n")
	var s sessionJSON
	eventually(t, time.Minute, "a session on files-mac", func() bool {
		s = firstSession(w.cli(2*time.Minute, "session", "start", "--project", "files-mac/site", "-o", "json", "--agent", "gemini", "--title", "文件预览", "--prompt", prompt, "--wait").stdout)
		return s.SessionID != ""
	})
	resolved, err := w.app.sessions.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{SessionId: s.SessionID})
	if err != nil || resolved.Folder == "" {
		t.Fatalf("resolve folder: %+v %v", resolved, err)
	}
	turn2 := "write docs/demo.html <html><body><p id=t>第二版</p></body></html>\nsay 改好了：[新版](file://" + filepath.ToSlash(filepath.Join(resolved.Folder, "docs", "demo.html")) + ")"
	if s2, r := w.send(2*time.Minute, s.SessionID, turn2); s2.Status != "idle" {
		t.Fatalf("second turn: %+v", r)
	}
	extShots := t.TempDir()
	shotPath := filepath.Join(extShots, "ios_simulator_screenshot.png")
	beforePath := filepath.Join(extShots, "before.png")
	afterPath := filepath.Join(extShots, "after.png")
	if err := os.WriteFile(shotPath, makeE2EPNG(t, 120, 260, 45, 125, 240), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beforePath, makeE2EPNG(t, 160, 120, 220, 95, 75), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(afterPath, makeE2EPNG(t, 160, 120, 65, 175, 115), 0o644); err != nil {
		t.Fatal(err)
	}
	turn3 := "say 截图验证：\\n![Hermesh iOS 模拟器截图](" + shotPath + ")\\n\\n截图文件：[ios_simulator_screenshot.png](file://" + filepath.ToSlash(shotPath) + ")\\n\\n对比：[修复前](" + beforePath + ")、[修复后](" + afterPath + ")。"
	if s3, r := w.send(2*time.Minute, s.SessionID, turn3); s3.Status != "idle" {
		t.Fatalf("third turn: %+v", r)
	}
	// Delete the external simulator screenshot after Turn 3 so E2E also proves turn-end blob pinning.
	if err := os.Remove(shotPath); err != nil {
		t.Fatal(err)
	}
	f.Sessions["文件预览"] = s.SessionID

	brainDir := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain", "conv-walkthrough")
	if err := os.MkdirAll(brainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	slideDark := filepath.Join(brainDir, "carousel_dark.png")
	slideLight := filepath.Join(brainDir, "carousel_light.png")
	walkPath := filepath.Join(brainDir, "walkthrough.md")
	if err := os.WriteFile(slideDark, makeE2EPNG(t, 150, 220, 35, 55, 95), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slideLight, makeE2EPNG(t, 150, 220, 235, 240, 250), 0o644); err != nil {
		t.Fatal(err)
	}
	walkMD := strings.Join([]string{
		"# 链路图走查",
		"",
		"> [!NOTE]",
		"> 已合并同源连线并渲染轮播对比。",
		"",
		"````carousel",
		"![深色预览](carousel_dark.png)",
		"<!-- slide -->",
		"![浅色预览](carousel_light.png)",
		"````",
		"",
	}, "\n")
	if err := os.WriteFile(walkPath, []byte(walkMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walkPath+".metadata.json", []byte(`{"userFacing":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	walkPrompt := "say 走查结果：\\n\\n> [!NOTE]\\n> 兼容 Antigravity 轮播与提示框\\n\\n````carousel\\n![深色预览](" + slideDark + ")\\n<!-- slide -->\\n![浅色预览](" + slideLight + ")\\n````\\n\\n报告文件：[walkthrough.md](file://" + filepath.ToSlash(walkPath) + ")"
	var walkSession sessionJSON
	eventually(t, time.Minute, "walkthrough session on files-mac", func() bool {
		walkSession = firstSession(w.cli(2*time.Minute, "session", "start", "--project", "files-mac/site", "-o", "json", "--agent", "gemini", "--title", "走查预览", "--prompt", walkPrompt, "--wait").stdout)
		return walkSession.SessionID != ""
	})
	stagePaths := make([]string, 4)
	for i := range stagePaths {
		p := filepath.Join(brainDir, fmt.Sprintf("stage_%d.png", i+1))
		if err := os.WriteFile(p, makeE2EPNG(t, 240, 135, uint8(40+i*45), uint8(90+i*25), uint8(210-i*35)), 0o644); err != nil {
			t.Fatal(err)
		}
		stagePaths[i] = p
	}
	stageTurn := "say 多图舞台：\\n\\n![方案一 [默认收起]：连体抽屉托盘](" + stagePaths[0] + ")\\n\\n![方案二：点击展开完整步骤](" + stagePaths[1] + ")\\n\\n![方案三：浅色模式对比](" + stagePaths[2] + ")\\n\\n![方案四：无框刻度条](" + stagePaths[3] + ")"
	if sw, r := w.send(2*time.Minute, walkSession.SessionID, stageTurn); sw.Status != "idle" {
		t.Fatalf("walkthrough stage turn: %+v", r)
	}
	f.Sessions["走查预览"] = walkSession.SessionID
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
// at all (each one's check fails). extra: more of the daemon's environment
// (her's, for a computer that can run Her).
func newFreshOwner(t *testing.T, w *ownerMac, name string, gated, bare bool, extra ...string) freshOwner {
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
	env = append(env, extra...)
	owner := start(t, name+" owner", env, filepath.Join(w.bin, "hermeshd"),
		"run", "--owner", "--data-dir", filepath.Join(tmp, "owner"), "--socket", sock)
	w.logs = append(w.logs, owner)
	root, _ := identity.GenerateKey()
	newTestApp(t, sock, root)
	return freshOwner{Socket: sock, RelayURL: fmt.Sprintf("http://127.0.0.1:%d", port), ClaimCode: claim, Gate: gate}
}
