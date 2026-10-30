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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
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
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	sessions := desktopSessions(t, w)
	device := simulator(t)
	// Notifications: the relay spools them, and this test hands each to the
	// Simulator as APNs would (simctl push), where the app's notification
	// extension opens it.
	spool := filepath.Join(t.TempDir(), "push")
	w.relayArgs = append(w.relayArgs, "--push-spool", spool, "--mcp") // the MCP gateway keeps doorbells
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
		"-parallel-testing-enabled", "NO", "-only-testing:HermeshUITests/HermeshUITests/testTheIPhoneApp"}
	if g := os.Getenv("HERMESH_IPHONE_GREP"); g != "" {
		args = append(args[:len(args)-1], "-only-testing:HermeshUITests/HermeshUITests/"+g)
	}
	// xcodebuild hands TEST_RUNNER_-prefixed variables to the UI tests.
	r := run(t, []string{"TEST_RUNNER_HERMESH_E2E_CONTROL=" + control}, 30*time.Minute, "xcodebuild", args...)
	if r.code != 0 {
		summary := run(t, nil, time.Minute, "xcrun", "xcresulttool", "get", "test-results", "tests", "--path", results).stdout
		t.Fatalf("UI tests failed (result bundle %s):\n%s\n---- last output ----\n%s\n%s", results, failureMessages(summary), tail(r.stdout, 40), tail(r.stderr, 40))
	}
	t.Log(tail(r.stdout, 30))
	checkIPhoneStrings(t, dir, derived)
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
			s.Payload["Simulator Target Bundle"] = "com.hermesh.ios"
			p, _ := json.Marshal(s.Payload)
			f := n + ".apns"
			os.WriteFile(f, p, 0o600)
			out, err := exec.Command("xcrun", "simctl", "push", device, "com.hermesh.ios", f).CombinedOutput()
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
		return id
	}
	types := run(t, nil, time.Minute, "xcrun", "simctl", "list", "devicetypes", "-j").stdout
	var dt struct {
		DeviceTypes []struct{ Name, Identifier string } `json:"devicetypes"`
	}
	json.Unmarshal([]byte(types), &dt)
	kind := ""
	for _, d := range dt.DeviceTypes {
		if strings.HasPrefix(d.Name, "iPhone") && strings.Contains(d.Name, "Pro") && !strings.Contains(d.Name, "Max") {
			kind = d.Identifier // the newest comes last
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
	// A device booted for the first time is still settling: asked for
	// notifications then, iOS may never show the alert, and the request
	// never returns.
	time.Sleep(time.Minute)
	return id
}

// newIPhoneControl serves what the UI tests ask of the world around the
// iPhone, and plays the owner's Mac: it approves the iPhone's request to
// join as soon as it arrives. It returns the server's URL.
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
				if p.Kind != identityv1.PairingKind_PAIRING_KIND_DEVICE {
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
	// row (one with a long output), a reply in Markdown with code, and a
	// command that failed.
	mux.HandleFunc("POST /rich", func(rw http.ResponseWriter, r *http.Request) {
		title := r.URL.Query().Get("title")
		text := strings.Join([]string{"## 计划", "先看 **加粗的文字** 和 `main.go`。", "", "- 第一项", "- 第二项", "",
			"| 文件 | 改动 |", "|---|---|", "| a.go | 新增 |", "", "```go", "func main() {}", "```", "", "详见 https://example.com，然后继续。"}, `\n`)
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
	// A grant's days left, by its label.
	mux.HandleFunc("GET /grant", func(rw http.ResponseWriter, r *http.Request) {
		res, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		for _, c := range res.Clients {
			if c.Label == r.URL.Query().Get("label") && !c.Revoked {
				reply(rw, map[string]any{"days": int(time.Until(c.ExpiresAt.AsTime()).Hours() / 24), "version": c.GrantVersion})
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
