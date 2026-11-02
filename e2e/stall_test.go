package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// A coding agent that never answers (a stand-in for Claude Code stuck while
// an MCP server starts) is stopped as stalled. The failure and the
// machine's log say what it was running and what it printed last, and
// nothing it started outlives it.
func TestStallSaysWhatTheAgentWasDoing(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows lists no programs")
	}
	marker := fmt.Sprintf("hermesh-stall-%d", time.Now().UnixNano())
	claude := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
case "$1" in
--version) echo "2.1.312 (Claude Code)"; exit 0;;
auth) echo '{"loggedIn": true}'; exit 0;;
esac
echo "connecting to MCP server slow-mcp" >&2
/bin/sh -c 'sleep 600; :' ` + marker + ` &
wait
`
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w := newOwnerMac(t, []string{"HERMESH_CLAUDE_BIN=" + claude}, &localv1.GrantSpec{}, "--idle-timeout", "2s")

	s, r := w.startSession(2*time.Minute, "--agent", "claude", "--wait", "--prompt", "hello")
	if r.code != 7 || s.LastFailure == nil || s.LastFailure.Kind != "stalled" {
		t.Fatalf("stall: %+v\n%s", s, r.stdout+r.stderr)
	}
	for _, want := range []string{"no output from the agent for 2s", "; running: sleep 600, sh -c sleep 600; : " + marker, "; stderr: connecting to MCP server slow-mcp"} {
		if !strings.Contains(s.LastFailure.Message, want) {
			t.Fatalf("failure without %q: %s", want, s.LastFailure.Message)
		}
	}
	log := w.owner.output()
	for _, want := range []string{`msg="agent started"`, `msg="stopping a stalled turn"`, marker, `msg="turn failed"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("machine log without %q:\n%s", want, log)
		}
	}
	eventually(t, 10*time.Second, "the agent's programs gone", func() bool {
		return exec.Command("pgrep", "-f", marker).Run() != nil
	})
}
