package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/shellenv"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

// TestIPhone runs the iPhone app's end-to-end tests: XCUITest drives the
// real app (apps/ios) on a Simulator of its own, in Chinese, against an
// owner Mac with a session in every state, played by a scripted ACP agent
// through the real relay, owner daemon and agent CLI. The iPhone joins
// that owner by its connection address, as a person with their own server
// would; the Mac's side (approving the join, an agent asking to connect) is
// played by this test, which the UI tests reach through a small control
// server (HERMESH_E2E_CONTROL).
//
// It needs Xcode and takes a few minutes; it runs when HERMESH_IPHONE_E2E
// is set (CI leaves it out), with HERMESH_IPHONE_GREP to pick UI tests.
// With her built (cargo build in her/, or HERMESH_HER_BIN), the Mac can run
// Her too, with no model yet, for testHer (HERMESH_IPHONE_GREP=testHer).
func TestIPhone(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("e2e on macOS")
	}
	if os.Getenv("HERMESH_IPHONE_E2E") == "" {
		t.Skip("set HERMESH_IPHONE_E2E=1 to run the iPhone app's end-to-end tests")
	}
	if _, err := exec.LookPath("xcodebuild"); err != nil {
		t.Skip("Xcode not installed")
	}
	dir, _ := filepath.Abs(filepath.Join("apps", "ios"))
	if r := run(t, nil, 10*time.Minute, filepath.Join(dir, "scripts", "build-core.sh")); r.code != 0 {
		t.Fatalf("build core: %+v", r)
	}

	fake := filepath.Join(binaries(t), "fakeacp")
	her := iphoneHerEnv(t)
	screen, _ := screenEnv(t) // the Mac's screen for testComputerControl: the helper's test picture
	w := newOwnerMac(t, append(append([]string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp"), "FAKE_ACP_MODELS=gemini-3-pro",
		"HERMESH_ANTIGRAVITY_BIN=" + fake, "GEMINI_HOME=" + t.TempDir()}, her...), screen...),
		&localv1.GrantSpec{Agents: []string{"gemini"}}, "--retry-delay", "0") // its rate limit fails at once
	if her != nil {
		t.Cleanup(func() { w.owner.stop() }) // stopped as a service is, so that it stops her (the daemon of the moment)
	}
	sessions := desktopSessions(t, w)
	device := simulator(t)
	// Notifications: the relay spools them, and this test hands each to the
	// Simulator as APNs would (simctl push), where the app's notification
	// extension opens it.
	spool := filepath.Join(t.TempDir(), "push")
	w.relayArgs = append(w.relayArgs, "--push-spool", spool, "--mcp") // the MCP gateway keeps doorbells
	// STUN and TURN, for the screen that starts relayed (testComputerControl).
	w.relayArgs = append(w.relayArgs, "--turn-listen", fmt.Sprintf("127.0.0.1:%d", freePort(t)), "--turn-host", "127.0.0.1")
	w.relayEnv = []string{"HERMESH_MCP_LOCAL_WEBHOOKS=1"}
	w.restartRelay()
	eventually(t, 30*time.Second, "the machine is back on the relay", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
	})
	go deliverToSimulator(t, spool, device)
	control := newIPhoneControl(t, w, sessions)
	// HERMESH_IPHONE_PREVIEW=<seconds> keeps the world up instead, to look
	// at the app against it (build and run it on the Simulator it names).
	if secs, _ := strconv.Atoi(os.Getenv("HERMESH_IPHONE_PREVIEW")); secs > 0 {
		t.Logf("world ready: simulator %s, connection address %s, control %s", device, w.addr, control)
		time.Sleep(time.Duration(secs) * time.Second)
		return
	}

	derived := filepath.Join(t.TempDir(), "derived")
	// HERMESH_IPHONE_RESULTS keeps the result bundle (screenshots, the
	// accessibility tree at a failure) where it says.
	results := filepath.Join(t.TempDir(), "results.xcresult")
	if d := os.Getenv("HERMESH_IPHONE_RESULTS"); d != "" {
		os.RemoveAll(d)
		results = d
	}
	args := []string{"test", "-project", filepath.Join(dir, "Hermesh.xcodeproj"), "-scheme", "Hermesh",
		"-destination", "platform=iOS Simulator,id=" + device, "-derivedDataPath", derived, "-resultBundlePath", results,
		"-parallel-testing-enabled", "NO"}
	only := "testTheIPhoneApp"
	if g := os.Getenv("HERMESH_IPHONE_GREP"); g != "" {
		only = g
	}
	args = append(args, "-only-testing:HermeshUITests/HermeshUITests/"+only)
	// Folders for Add a Project to browse to: a repository by the name of
	// the project there already, with a second branch, and a plain folder.
	folders, _ := filepath.EvalSymlinks(t.TempDir())
	gitRepoAt(t, filepath.Join(folders, "demo"), map[string]string{"README.md": "# another demo\n"})
	if out, err := exec.Command("git", "-C", filepath.Join(folders, "demo"), "branch", "feature").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v %s", err, out)
	}
	os.MkdirAll(filepath.Join(folders, "notes"), 0o755)
	// xcodebuild hands TEST_RUNNER_-prefixed variables to the UI tests.
	runner := []string{"TEST_RUNNER_HERMESH_E2E_CONTROL=" + control, "TEST_RUNNER_HERMESH_E2E_FOLDERS=" + folders}
	if her != nil {
		runner = append(runner, "TEST_RUNNER_HERMESH_E2E_HER=1")
	}
	r := run(t, runner, 30*time.Minute, "xcodebuild", args...)
	if r.code != 0 {
		summary := run(t, nil, time.Minute, "xcrun", "xcresulttool", "get", "test-results", "tests", "--path", results).stdout
		t.Fatalf("UI tests failed (result bundle %s):\n%s\n---- last output ----\n%s\n%s", results, failureMessages(summary), tail(r.stdout, 40), tail(r.stderr, 40))
	}
	t.Log(tail(r.stdout, 30))
	// testTheIPhoneApp adds a project; the other tests none.
	if r := w.cli(30*time.Second, "project", "list"); only == "testTheIPhoneApp" && !strings.Contains(r.stdout, "owner-mac/demo-2") {
		t.Fatalf("the project added on the iPhone: %+v", r)
	}
	checkIPhoneStrings(t, dir, derived)
}

// iphoneHerEnv is what the owner Mac's hermeshd needs to run Her for the
// iPhone (testHer): her, the e2e console, and a home for her with no model
// that works, as a new server's (work-mac in desktopHerWorld): her-mock
// answers in DeepSeek's place and in Tencent Cloud Token Plan's and takes a
// key, so Her's onboarding connects one. Nil, and testHer skipped, without
// her built.
func iphoneHerEnv(t *testing.T) []string {
	her := os.Getenv("HERMESH_HER_BIN")
	if her == "" {
		her, _ = filepath.Abs(filepath.Join("her", "target", "debug", "her"))
	}
	mockBin := os.Getenv("HERMESH_HER_MOCK")
	if mockBin == "" {
		mockBin = filepath.Join(filepath.Dir(her), "her-mock")
	}
	for _, p := range []string{her, mockBin} {
		if _, err := os.Stat(p); err != nil {
			t.Logf("Her is left out (%v): build her (cargo build in her/) for testHer", err)
			return nil
		}
	}
	mock := startHerMock(t, mockBin)
	home := t.TempDir()
	base := regexp.MustCompile(`base_url = "([^"]+)"`).FindStringSubmatch(mock.models)[1]
	if err := os.WriteFile(filepath.Join(home, "models.toml"), []byte(fmt.Sprintf("[providers.deepseek]\nbase_url = %q\n\n[providers.tencent-token-plan]\nbase_url = %q\n", base, base)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := herEnv(her, herConsole(t), home, mock.base)
	// Nor a key or model from this computer, which the login shell may have.
	for _, kv := range shellenv.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasSuffix(name, "_API_KEY") || name == "HER_MODEL" {
			env = append(env, name+"=")
		}
	}
	return env
}

// checkIPhoneStrings: every string the build found in the app and its
// notification extension has its Chinese in the target's String Catalog.
func checkIPhoneStrings(t *testing.T, dir, derived string) {
	for _, target := range []string{"Hermesh", "HermeshNotifications"} {
		var catalog struct {
			Strings map[string]struct {
				ShouldTranslate *bool `json:"shouldTranslate"`
				Localizations   map[string]struct {
					StringUnit struct{ Value string } `json:"stringUnit"`
				} `json:"localizations"`
			} `json:"strings"`
		}
		b, err := os.ReadFile(filepath.Join(dir, target, "Localizable.xcstrings"))
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(b, &catalog)
		files, _ := filepath.Glob(filepath.Join(derived, "Build", "Intermediates.noindex", "Hermesh.build", "Debug-iphonesimulator", target+".build", "Objects-normal", "*", "*.stringsdata"))
		if len(files) == 0 {
			t.Fatalf("%s: no extracted strings", target)
		}
		for _, f := range files {
			var data struct {
				Tables map[string][]struct{ Key string } `json:"tables"`
			}
			b, _ := os.ReadFile(f)
			json.Unmarshal(b, &data)
			for _, entries := range data.Tables {
				for _, e := range entries {
					c, ok := catalog.Strings[e.Key]
					if ok && (c.ShouldTranslate != nil && !*c.ShouldTranslate || c.Localizations["zh-Hans"].StringUnit.Value != "") {
						continue
					}
					t.Errorf("%s: no Chinese for %q", target, e.Key)
				}
			}
		}
	}
}

// failureMessages picks the failures out of xcresulttool's JSON.
func failureMessages(doc string) string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["nodeType"] == "Failure Message" {
				out = append(out, fmt.Sprint(x["name"]))
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	var v any
	json.Unmarshal([]byte(doc), &v)
	walk(v)
	if len(out) == 0 {
		return tail(doc, 60)
	}
	return strings.Join(out, "\n")
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// pushed counts the notifications handed to the Simulator.
var pushed atomic.Int32

// deliverToSimulator pushes each notification the relay spools to the
// Simulator, until the test ends.
func deliverToSimulator(t *testing.T, spool, device string) {
	seen := map[string]bool{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for ctx.Err() == nil {
		time.Sleep(300 * time.Millisecond)
		names, _ := filepath.Glob(filepath.Join(spool, "*.json"))
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			var s struct {
				Payload map[string]any `json:"payload"`
			}
			b, _ := os.ReadFile(n)
			if json.Unmarshal(b, &s) != nil {
				continue
			}
			s.Payload["Simulator Target Bundle"] = "io.hermesh.ios"
			p, _ := json.Marshal(s.Payload)
			f := n + ".apns"
			os.WriteFile(f, p, 0o600)
			out, err := exec.Command("xcrun", "simctl", "push", device, "io.hermesh.ios", f).CombinedOutput()
			t.Logf("simctl push %s: %v %s", filepath.Base(n), err, out)
			if err == nil {
				pushed.Add(1)
			}
		}
	}
}

// simulator creates an iPhone Simulator for this run (deleted afterwards),
// boots it and returns its id; HERMESH_IPHONE_SIMULATOR uses one that
// exists instead.
func simulator(t *testing.T) string {
	t.Helper()
	if id := os.Getenv("HERMESH_IPHONE_SIMULATOR"); id != "" {
		run(t, nil, 3*time.Minute, "xcrun", "simctl", "bootstatus", id, "-b")
		pinyinKeyboard(t, id)
		return id
	}
	types := run(t, nil, time.Minute, "xcrun", "simctl", "list", "devicetypes", "-j").stdout
	var dt struct {
		DeviceTypes []struct{ Name, Identifier string } `json:"devicetypes"`
	}
	json.Unmarshal([]byte(types), &dt)
	// The newest "iPhone N Pro", by its number: the list's order changes
	// between Xcode versions (Xcode 27 lists the newest first), and an old
	// model on a new iOS never shows the notification permission alert.
	kind, newest := "", 0
	for _, d := range dt.DeviceTypes {
		f := strings.Fields(d.Name)
		if len(f) != 3 || f[0] != "iPhone" || f[2] != "Pro" {
			continue
		}
		if n, err := strconv.Atoi(f[1]); err == nil && n > newest {
			kind, newest = d.Identifier, n
		}
	}
	if kind == "" {
		t.Skip("no iPhone Simulator device type")
	}
	r := run(t, nil, 2*time.Minute, "xcrun", "simctl", "create", "Hermesh E2E", kind)
	if r.code != 0 {
		t.Fatalf("simctl create: %+v", r)
	}
	id := strings.TrimSpace(r.stdout)
	t.Cleanup(func() {
		exec.Command("xcrun", "simctl", "shutdown", id).Run()
		exec.Command("xcrun", "simctl", "delete", id).Run()
	})
	if r := run(t, nil, 3*time.Minute, "xcrun", "simctl", "bootstatus", id, "-b"); r.code != 0 {
		t.Fatalf("boot: %+v", r)
	}
	pinyinKeyboard(t, id)
	// A device booted for the first time shows the notification permission
	// alert only once SpringBoard has loaded its notification sources, two
	// minutes and more later: allowNotifications in the UI tests waits.
	return id
}

// pinyinKeyboard gives the Simulator the Pinyin keyboard first, as the
// owner types (testHer types Chinese into Her's composer). The keyboards
// are read at boot: written, the Simulator boots again.
func pinyinKeyboard(t *testing.T, id string) {
	t.Helper()
	if r := run(t, nil, time.Minute, "xcrun", "simctl", "spawn", id, "defaults", "read", "-g", "AppleKeyboards"); strings.Contains(r.stdout, "Pinyin") {
		return
	}
	run(t, nil, time.Minute, "xcrun", "simctl", "spawn", id, "defaults", "write", "-g", "AppleKeyboards", "-array",
		"zh_Hans-Pinyin@sw=Pinyin10;hw=Automatic", "en_US@sw=QWERTY;hw=Automatic")
	run(t, nil, 2*time.Minute, "xcrun", "simctl", "shutdown", id)
	run(t, nil, time.Minute, "xcrun", "simctl", "boot", id)
	if r := run(t, nil, 3*time.Minute, "xcrun", "simctl", "bootstatus", id, "-b"); r.code != 0 {
		t.Fatalf("boot again for the Pinyin keyboard: %+v", r)
	}
}

// newIPhoneControl serves what the UI tests ask of the world around the
// iPhone, and plays the owner's Mac: it approves the iPhone's request to
// join as soon as it arrives (and no device's after it: those are the
// iPhone's to approve). It returns the server's URL.
func newIPhoneControl(t *testing.T, w *ownerMac, sessions map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	digits := ""
	joined := ""
	go func() {
		for ctx.Err() == nil {
			time.Sleep(300 * time.Millisecond)
			res, err := w.app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
			if err != nil {
				continue
			}
			for _, p := range res.Pairings {
				mu.Lock()
				done := joined != ""
				mu.Unlock()
				if done || p.Kind != identityv1.PairingKind_PAIRING_KIND_DEVICE {
					continue
				}
				a, err := w.app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: p.Code})
				if err != nil {
					t.Logf("approving the iPhone: %v", err)
					continue
				}
				mu.Lock()
				digits, joined = a.MatchCode, p.Label
				mu.Unlock()
			}
		}
	}()

	var agentLogin *proc
	mux := http.NewServeMux()
	reply := func(rw http.ResponseWriter, v any) {
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(v)
	}
	mux.HandleFunc("GET /address", func(rw http.ResponseWriter, _ *http.Request) { reply(rw, map[string]string{"address": w.addr}) })
	mcp := mcpAddress(t, w.app)
	mux.HandleFunc("GET /mcp", func(rw http.ResponseWriter, _ *http.Request) { reply(rw, map[string]string{"address": mcp}) })
	// A bot's routine for a doorbell: it takes one key, and says what called it.
	var hookCalls []string
	hook := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var b struct{ Type, As string }
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		hookCalls = append(hookCalls, b.Type+" "+b.As+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer crsr_ok" {
			rw.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(hook.Close)
	mux.HandleFunc("GET /webhook", func(rw http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reply(rw, map[string]any{"url": hook.URL + "/routine", "calls": append([]string{}, hookCalls...)})
	})
	// The bot asks to be woken, and its owner opens the link on the Mac, as
	// the Mac app does, with the routine's URL and key.
	mux.HandleFunc("POST /wake", func(rw http.ResponseWriter, _ *http.Request) {
		var asked struct{ Wake struct{ Link string } }
		json.Unmarshal([]byte(run(t, w.cliEnv, 30*time.Second, filepath.Join(w.bin, "hermesh"), "message", "wake", "-o", "json").stdout), &asked)
		code, _ := strings.CutPrefix(asked.Wake.Link, "hermesh://wake?code=")
		set, err := w.app.client.SetWake(context.Background(), &localv1.SetWakeRequest{Code: code, Url: hook.URL + "/routine", Authorization: "Bearer crsr_ok"})
		if err != nil || !set.Check.Ok {
			http.Error(rw, fmt.Sprintf("wake: %v %v", set, err), 500)
			return
		}
		reply(rw, map[string]string{})
	})
	// The relay down (up=0) or back (up=1): the app shows what it kept.
	mux.HandleFunc("POST /relay", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("up") == "1" {
			w.startRelay()
			// The Mac back on it too (it retries with a backoff): a notice
			// it raises while away is not sent.
			eventually(t, 60*time.Second, "the machine is back on the relay", func() bool {
				r := w.cli(30*time.Second, "project", "list")
				return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
			})
		} else {
			w.relay.stop()
		}
		reply(rw, map[string]string{})
	})
	// The Mac's daemon down (up=0) or back (up=1): its machine offline while the relay stays up.
	mux.HandleFunc("POST /machine", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("up") == "1" {
			w.startOwner()
			w.app = newTestApp(w.t, w.sock, w.root)
			eventually(t, 60*time.Second, "the machine is back on the relay", func() bool {
				r := w.cli(30*time.Second, "project", "list")
				return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
			})
		} else {
			w.owner.stop()
			w.app.cancel()
		}
		reply(rw, map[string]string{})
	})
	// Files for Her to send (testHer), where her assistant keeps its own: a
	// note in Markdown and a Go file, as a model writes them there.
	mux.HandleFunc("POST /her-files", func(rw http.ResponseWriter, _ *http.Request) {
		var home string
		for _, kv := range w.daemonEnv {
			if v, ok := strings.CutPrefix(kv, "HER_HOME="); ok {
				home = v
			}
		}
		bots, _ := filepath.Glob(filepath.Join(home, "bots", "*", "bot.json"))
		if home == "" || len(bots) == 0 {
			http.Error(rw, "no assistant in "+home, 500)
			return
		}
		files := map[string]string{
			"notes.md": "# 周报\n\n本周完成了 **三件事**：\n\n- 修好登录\n- 加了 `--retry`\n\n| 事项 | 状态 |\n|---|---|\n| 登录 | 已完成 |\n\n```go\nfunc main() {}\n```\n",
			"main.go":  "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"你好\")\n}\n",
		}
		for _, bot := range bots {
			dir := filepath.Join(filepath.Dir(bot), "files")
			os.MkdirAll(dir, 0o700)
			for name, text := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
					http.Error(rw, err.Error(), 500)
					return
				}
			}
		}
		reply(rw, map[string]int{"assistants": len(bots)})
	})
	// Files the owner sends Her from the Mac (testHer): a PDF and a note,
	// uploaded into her assistant's folder and named in the message, as the
	// Mac app sends them, for the iPhone to open from the owner's bubble.
	mux.HandleFunc("POST /her-owner-files", func(rw http.ResponseWriter, _ *http.Request) {
		ctx := context.Background()
		var home, machine string
		for _, kv := range w.daemonEnv {
			if v, ok := strings.CutPrefix(kv, "HER_HOME="); ok {
				home = v
			}
		}
		bots, _ := filepath.Glob(filepath.Join(home, "bots", "*", "bot.json"))
		if res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); err == nil {
			for _, m := range res.Machines {
				if m.GetHer().GetRunning() {
					machine = m.MachineId
				}
			}
		}
		if machine == "" || len(bots) == 0 {
			http.Error(rw, "no Her running", 500)
			return
		}
		dir := w.t.TempDir()
		files := map[string][]byte{"report.pdf": onePagePDF("Quarterly report"), "plan.md": []byte("# 计划\n\n第一步\n第二步\n")}
		var paths []string
		for name, b := range files {
			paths = append(paths, filepath.Join(dir, name))
			os.WriteFile(paths[len(paths)-1], b, 0o600)
		}
		sent, err := w.app.client.HerSend(ctx, &localv1.HerSendRequest{MachineId: machine, BotId: filepath.Base(filepath.Dir(bots[0])), Text: "[[mock:send:收到]] 看看这两份", LocalPaths: paths})
		if err != nil || !sent.Delivered {
			http.Error(rw, fmt.Sprintf("HerSend: %v %v", sent, err), 500)
			return
		}
		reply(rw, map[string]bool{"delivered": true})
	})
	// What the Mac's screen helper was sent (testComputerControl): the input
	// it would have posted, a JSON object a line (screenEnv).
	inputLog := func() string {
		for _, kv := range w.daemonEnv {
			if args, ok := strings.CutPrefix(kv, "HERMESH_SCREEN_ARGS="); ok {
				if _, rest, ok := strings.Cut(args, "--input-log "); ok {
					return strings.Fields(rest)[0]
				}
			}
		}
		return ""
	}
	mux.HandleFunc("GET /screen", func(rw http.ResponseWriter, _ *http.Request) {
		b, _ := os.ReadFile(inputLog())
		reply(rw, map[string]string{"input": string(b)})
	})
	// Text copied on the Mac (testComputerControl): the helper, given
	// --test-clipboard, sends what is written to this file beside its input
	// log as the machine's clipboard.
	mux.HandleFunc("POST /screen-clipboard", func(rw http.ResponseWriter, r *http.Request) {
		log := inputLog()
		if log == "" {
			http.Error(rw, "no screen helper input log", 500)
			return
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(log), "screen-clipboard.txt"), []byte(r.URL.Query().Get("text")), 0o600); err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		reply(rw, map[string]string{})
	})
	mux.HandleFunc("GET /digits", func(rw http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reply(rw, map[string]string{"digits": digits, "label": joined})
	})
	// A session's state, by its title.
	mux.HandleFunc("GET /session", func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		id := sessions[r.URL.Query().Get("title")]
		mu.Unlock()
		if id == "" {
			reply(rw, map[string]string{"status": ""})
			return
		}
		s := w.get(id)
		reply(rw, map[string]string{"status": s.Status, "title": s.Title}) // a deleted one: no status
	})
	// A new session that says one thing (好, or say) and is done (to rename or delete).
	mux.HandleFunc("POST /start", func(rw http.ResponseWriter, r *http.Request) {
		title, say := r.URL.Query().Get("title"), r.URL.Query().Get("say")
		if say == "" {
			say = "好"
		}
		s, res := w.startSession(2*time.Minute, "--agent", "gemini", "--title", title, "--prompt", "say "+say, "--wait")
		if s.SessionID == "" {
			t.Logf("start session %q failed: exit=%d stdout=%s stderr=%s", title, res.code, res.stdout, res.stderr)
			http.Error(rw, "start: "+res.stdout+res.stderr, 500)
			return
		}
		mu.Lock()
		sessions[title] = s.SessionID
		mu.Unlock()
		reply(rw, map[string]string{"id": s.SessionID})
	})
	// A new session whose reply is written slowly, then waits: to watch the
	// reply grow (docs/reply-streaming.md).
	mux.HandleFunc("POST /stream", func(rw http.ResponseWriter, r *http.Request) {
		title := r.URL.Query().Get("title")
		s, res := w.startSession(2*time.Minute, "--agent", "gemini", "--title", title, "--prompt",
			"sleep 3s\nstream 这是一段慢慢写出来的回复，写完以后还要等一会儿，这一轮才结束。\nsleep 8s")
		if s.SessionID == "" {
			http.Error(rw, "start: "+res.stdout+res.stderr, 500)
			return
		}
		mu.Lock()
		sessions[title] = s.SessionID
		mu.Unlock()
		reply(rw, map[string]string{"id": s.SessionID})
	})
	// A finished session with what a conversation shows: two commands in a
	// row (one with a long output), a reply in Markdown with code, math and
	// a diagram, and a command that failed.
	mux.HandleFunc("POST /rich", func(rw http.ResponseWriter, r *http.Request) {
		title := r.URL.Query().Get("title")
		text := strings.Join([]string{"## 计划", "先看 **加粗的文字** 和 `main.go`。", "", "- 第一项", "- 第二项", "",
			"| 文件 | 改动 |", "|---|---|", "| a.go | 新增 |", "", "```go", "func main() {}", "```", "",
			"质能关系 \\(E = mc^2\\) 成立。", "", "$$", "x^2 + y^2 = z^2", "$$", "",
			"```mermaid", "flowchart LR", "    A[开始] --> B[结束]", "```", "", "详见 https://example.com，然后继续。"}, `\n`)
		s, res := w.startSession(2*time.Minute, "--agent", "gemini", "--title", title, "--prompt",
			"shell seq 1 3000\nshell echo 改好了\nsay "+text+"\nshell ls no-such-file", "--wait")
		if s.SessionID == "" {
			http.Error(rw, "start: "+res.stdout+res.stderr, 500)
			return
		}
		mu.Lock()
		sessions[title] = s.SessionID
		mu.Unlock()
		reply(rw, map[string]string{"id": s.SessionID})
	})
	// A finished session much taller than the screen, its last words "这是最后一段".
	mux.HandleFunc("POST /long", func(rw http.ResponseWriter, r *http.Request) {
		title := r.URL.Query().Get("title")
		var lines []string
		for i := 1; i <= 40; i++ {
			lines = append(lines, fmt.Sprintf(`say 第 %d 段：这一段话写得很长，好让整个对话比屏幕高得多，打开时应当直接看到最后一段。\n\n`, i)) // a paragraph each
		}
		s, res := w.startSession(2*time.Minute, "--agent", "gemini", "--title", title, "--prompt", strings.Join(append(lines, "say 这是最后一段"), "\n"), "--wait")
		if s.SessionID == "" {
			http.Error(rw, "start: "+res.stdout+res.stderr, 500)
			return
		}
		mu.Lock()
		sessions[title] = s.SessionID
		mu.Unlock()
		reply(rw, map[string]string{"id": s.SessionID})
	})
	// An agent asks to connect (hermesh auth login), for the iPhone to approve.
	mux.HandleFunc("POST /agent", func(rw http.ResponseWriter, r *http.Request) {
		home := filepath.Join(t.TempDir(), "agent")
		p := start(t, "agent login", []string{"HERMESH_HOME=" + home}, filepath.Join(w.bin, "hermesh"), "auth", "login", w.addr, "--name", "Cursor")
		w.logs = append(w.logs, p)
		mu.Lock()
		agentLogin = p
		mu.Unlock()
		deadline := time.After(30 * time.Second)
		for {
			select {
			case l := <-p.lines:
				if m := codeRE.FindStringSubmatch(l); m != nil {
					reply(rw, map[string]string{"code": m[1]})
					return
				}
			case <-deadline:
				http.Error(rw, "no pairing code:\n"+p.output(), 500)
				return
			}
		}
	})
	mux.HandleFunc("GET /agent", func(rw http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		p := agentLogin
		mu.Unlock()
		connected := false
		if p != nil {
			select {
			case <-p.done:
				connected = p.cmd.ProcessState.ExitCode() == 0
			default:
			}
		}
		reply(rw, map[string]bool{"connected": connected})
	})
	// A Windows computer asks to become a trusted device, for the iPhone to
	// approve; then (POST /device/confirm?digits=) it types the two digits.
	var deviceApp *testApp
	var deviceCode string
	mux.HandleFunc("POST /device", func(rw http.ResponseWriter, _ *http.Request) {
		st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		dir := t.TempDir()
		sock := filepath.Join(dir, "d.sock")
		w.logs = append(w.logs, start(t, "owner studio-pc", nil, filepath.Join(w.bin, "hermeshd"), "run", "--owner", "--no-machine", "--data-dir", dir, "--socket", sock))
		root, _ := identity.GenerateKey()
		a := newTestApp(t, sock, root)
		join, err := a.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: st.RelayUrl, OwnerId: st.OwnerId,
			RootPublicKey: rootPub(root), RootLabel: "STUDIO-PC", RootPlatform: "windows"})
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		mu.Lock()
		deviceApp, deviceCode = a, join.Code
		mu.Unlock()
		reply(rw, map[string]string{"code": join.Code, "verification": join.VerificationCode})
	})
	mux.HandleFunc("POST /device/confirm", func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		a, code := deviceApp, deviceCode
		mu.Unlock()
		for {
			res, err := a.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: code})
			if err != nil {
				http.Error(rw, err.Error(), 500)
				return
			}
			if res.Expired {
				http.Error(rw, "the request expired", 500)
				return
			}
			if res.Done {
				break
			}
		}
		if _, err := a.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: code, Accept: true, MatchCode: r.URL.Query().Get("digits")}); err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		reply(rw, map[string]bool{"joined": true})
	})
	// An agent asks a new question (for a notification).
	mux.HandleFunc("POST /ask", func(rw http.ResponseWriter, r *http.Request) {
		title := r.URL.Query().Get("title")
		go func() {
			s, _ := w.startSession(2*time.Minute, "--agent", "gemini", "--title", title, "--prompt", "ask "+title+"|发布,先不", "--wait")
			mu.Lock()
			sessions[title] = s.SessionID
			mu.Unlock()
		}()
		reply(rw, map[string]string{})
	})
	// How many devices registered for notifications (the relay's store).
	mux.HandleFunc("GET /registered", func(rw http.ResponseWriter, _ *http.Request) {
		st, err := relay.OpenStore(filepath.Join(w.tmp, "relay", "relay.db"))
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		defer st.Close()
		targets, _ := st.PushTargets("")
		reply(rw, map[string]int{"devices": len(targets)})
	})
	// How many notifications reached the Simulator.
	mux.HandleFunc("GET /pushed", func(rw http.ResponseWriter, _ *http.Request) {
		reply(rw, map[string]int{"pushed": int(pushed.Load())})
	})
	// A grant's days left and coding agents, by its label.
	mux.HandleFunc("GET /grant", func(rw http.ResponseWriter, r *http.Request) {
		res, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		for _, c := range res.Clients {
			if c.Label == r.URL.Query().Get("label") && !c.Revoked {
				reply(rw, map[string]any{"days": int(time.Until(c.ExpiresAt.AsTime()).Hours() / 24), "version": c.GrantVersion, "agents": c.Spec.GetAgents()})
				return
			}
		}
		http.NotFound(rw, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return fmt.Sprintf("http://%s", ln.Addr())
}

// onePagePDF is a PDF of one page with line on it, in Helvetica.
func onePagePDF(line string) []byte {
	text := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (%s) Tj ET", line)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(text), text),
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(b.String())
}
