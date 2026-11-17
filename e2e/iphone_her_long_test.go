package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestIPhoneHerLong runs the iPhone app's testHerLong (apps/ios/
// HermeshUITests/HerLongUITests.swift) against an owner Mac whose Her has had
// the long conversation in English (longChatWith): the iPhone joins, opens
// on its end and is scrolled up to its first message, reading the earlier
// pages as it goes. It needs Xcode and her built, and runs when
// HERMESH_IPHONE_E2E is set, on a Simulator of its own (or
// HERMESH_IPHONE_SIMULATOR's).
//
//	HERMESH_IPHONE_E2E=1 go test ./e2e -run TestIPhoneHerLong -v -timeout 40m
//
// With HERMESH_IPHONE_PREVIEW=<seconds> it keeps the world up instead, for a
// person to join it from the app on a Simulator and scroll the conversation.
func TestIPhoneHerLong(t *testing.T) {
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
	env, _ := longHer(t)
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	t.Cleanup(func() { w.owner.stop() }) // stopped as a service is, so that it stops her
	// The iPhone's control server reads the owner's MCP address: the relay's gateway.
	w.relayArgs = append(w.relayArgs, "--mcp")
	w.restartRelay()
	eventually(t, 30*time.Second, "the machine is back on the relay", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
	})
	longChatWith(t, w)
	device := simulator(t)
	control := newIPhoneControl(t, w, map[string]string{})
	if secs, _ := strconv.Atoi(os.Getenv("HERMESH_IPHONE_PREVIEW")); secs > 0 {
		t.Logf("world ready: Her's long conversation (%d messages, in English); simulator %s, connection address %s, control %s", longChat, device, w.addr, control)
		time.Sleep(time.Duration(secs) * time.Second)
		return
	}

	derived := filepath.Join(t.TempDir(), "derived")
	results := filepath.Join(t.TempDir(), "results.xcresult")
	if d := os.Getenv("HERMESH_IPHONE_RESULTS"); d != "" {
		os.RemoveAll(d)
		results = d
	}
	args := []string{"test", "-project", filepath.Join(dir, "Hermesh.xcodeproj"), "-scheme", "Hermesh",
		"-destination", "platform=iOS Simulator,id=" + device, "-derivedDataPath", derived, "-resultBundlePath", results,
		"-parallel-testing-enabled", "NO", "-jobs", "4", "-only-testing:HermeshUITests/HermeshUITests/testHerLong"}
	r := run(t, []string{"TEST_RUNNER_HERMESH_E2E_CONTROL=" + control, "TEST_RUNNER_HERMESH_E2E_HER_LONG=" + strconv.Itoa(longChat)}, 30*time.Minute, "xcodebuild", args...)
	if r.code != 0 {
		summary := run(t, nil, time.Minute, "xcrun", "xcresulttool", "get", "test-results", "tests", "--path", results).stdout
		t.Fatalf("UI tests failed (result bundle %s):\n%s\n---- last output ----\n%s\n%s", results, failureMessages(summary), tail(r.stdout, 40), tail(r.stderr, 40))
	}
	// Xcode exits successfully when the test was skipped: it must have passed.
	summary := run(t, nil, time.Minute, "xcrun", "xcresulttool", "get", "test-results", "summary", "--path", results)
	var counts struct{ PassedTests int }
	if err := json.Unmarshal([]byte(summary.stdout), &counts); summary.code != 0 || err != nil || counts.PassedTests == 0 {
		t.Fatalf("testHerLong did not pass (result bundle %s):\n%s\n%s", results, summary.stdout, summary.stderr)
	}
	t.Log(tail(r.stdout, 30))
}
