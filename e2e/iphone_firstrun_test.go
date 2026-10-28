package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestIPhoneFirstRun is first use with only the iPhone (docs/onboarding.md
// §4, §5): XCUITest drives the real app on a Simulator of its own, in
// Chinese, against a relay nobody has claimed. The phone sets the server up
// with its claim code and is the only trusted device. Get Started then
// leads it through a Linux-like machine, played by this test with a home
// of its own and fake coding agents:
//
//   - the machine's pinned command prints a pairing code; the phone opens
//     it as a pairing link, finds nothing to compare, names the machine
//     and sees it join;
//   - Check Again and Test on the machine's coding agent;
//   - a project picked from the repositories the machine found;
//   - the row that says this phone is the only one that can approve;
//   - an agent connecting, and its first task: the session it starts
//     opens by itself, and Get Started is done.
//
// It runs when HERMESH_IPHONE_E2E is set, as TestIPhone does.
func TestIPhoneFirstRun(t *testing.T) {
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
	bin := binaries(t)
	tmp := t.TempDir()

	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)

	device := simulator(t)
	// A Simulator kept between runs (HERMESH_IPHONE_SIMULATOR) starts without the app.
	exec.Command("xcrun", "simctl", "uninstall", device, "com.hermesh.ios").Run()
	control := newFirstRunControl(t, bin, tmp, base, claim)

	derived := filepath.Join(t.TempDir(), "derived")
	results := filepath.Join(t.TempDir(), "results.xcresult")
	if d := os.Getenv("HERMESH_IPHONE_RESULTS"); d != "" {
		os.RemoveAll(d)
		results = d
	}
	r := run(t, []string{"TEST_RUNNER_HERMESH_E2E_CONTROL=" + control}, 30*time.Minute, "xcodebuild", "test",
		"-project", filepath.Join(dir, "Hermesh.xcodeproj"), "-scheme", "Hermesh",
		"-destination", "platform=iOS Simulator,id="+device, "-derivedDataPath", derived, "-resultBundlePath", results,
		"-parallel-testing-enabled", "NO", "-only-testing:HermeshUITests/HermeshUITests/testFirstRun")
	if r.code != 0 {
		summary := run(t, nil, time.Minute, "xcrun", "xcresulttool", "get", "test-results", "tests", "--path", results).stdout
		t.Fatalf("UI tests failed (result bundle %s):\n%s\n---- last output ----\n%s\n%s", results, failureMessages(summary), tail(r.stdout, 40), tail(r.stderr, 40))
	}
	t.Log(tail(r.stdout, 30))
	checkIPhoneStrings(t, dir, derived)
}

// newFirstRunControl serves what testFirstRun asks of the world around the
// phone: the relay to claim, a machine that installs itself, and an agent
// that connects and starts its first task. It returns the server's URL.
func newFirstRunControl(t *testing.T, bin, tmp, relayURL, claim string) string {
	t.Helper()
	var mu sync.Mutex
	var agent *proc
	agentHome := filepath.Join(tmp, "agent")
	reply := func(rw http.ResponseWriter, v any) {
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(v)
	}
	// The machine's code, from a terminal's output.
	pairingCode := func(p *proc) (string, error) {
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if m := codeRE.FindStringSubmatch(p.output()); m != nil {
				return m[1], nil
			}
		}
		return "", fmt.Errorf("no pairing code:\n%s", p.output())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /first-run", func(rw http.ResponseWriter, _ *http.Request) {
		reply(rw, map[string]string{"relay": relayURL, "claim": claim})
	})
	// vps-1 runs the command the phone shows (its connection address in
	// it), with two repositories in its home; once paired, its service.
	mux.HandleFunc("POST /machine", func(rw http.ResponseWriter, r *http.Request) {
		address := r.URL.Query().Get("address")
		home := filepath.Join(tmp, "vps-home")
		gitRepoAt(t, filepath.Join(home, "app"), map[string]string{"README.md": "# app\n"})
		gitRepoAt(t, filepath.Join(home, "code", "api"), map[string]string{"README.md": "# api\n"})
		env := []string{"HOME=" + home, "HERMESH_FAKE_PROVIDERS=1"}
		data := filepath.Join(tmp, "vps-1")
		daemon := filepath.Join(bin, "hermeshd")
		pair := start(t, "pair", env, daemon, "pair", address, "--name", "vps-1", "--no-service", "--data-dir", data)
		code, err := pairingCode(pair)
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		go func() {
			if pair.wait(t, 5*time.Minute) == 0 {
				start(t, "vps-1", env, daemon, "run", "--data-dir", data)
			}
		}()
		reply(rw, map[string]string{"code": code})
	})
	// An agent asks to connect, with the address it was sent.
	mux.HandleFunc("POST /agent", func(rw http.ResponseWriter, r *http.Request) {
		p := start(t, "agent login", []string{"HERMESH_HOME=" + agentHome}, filepath.Join(bin, "hermesh"), "auth", "login", r.URL.Query().Get("address"), "--name", "Cursor")
		mu.Lock()
		agent = p
		mu.Unlock()
		code, err := pairingCode(p)
		if err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		reply(rw, map[string]string{"code": code})
	})
	// The agent does the first task: a session on vps-1.
	mux.HandleFunc("POST /task", func(rw http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		p := agent
		mu.Unlock()
		if p == nil || p.wait(t, 30*time.Second) != 0 {
			http.Error(rw, "the agent isn't connected", 500)
			return
		}
		r := run(t, []string{"HERMESH_HOME=" + agentHome}, 2*time.Minute, filepath.Join(bin, "hermesh"),
			"session", "start", "--project", "vps-1/app", "--agent", "claude", "--prompt", "say 你好", "--wait", "-o", "json")
		s := firstSession(r.stdout)
		if s.SessionID == "" {
			http.Error(rw, "start: "+r.stdout+r.stderr, 500)
			return
		}
		reply(rw, map[string]string{"id": s.SessionID})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return (&url.URL{Scheme: "http", Host: ln.Addr().String()}).String()
}
