package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// An MCP client continues the same session after a daemon restart and a
// lost checkout. The existing branch, conversation, and diff must survive.
func TestMCPResumeMissingWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	w.relay.stop()
	w.relayArgs = append(w.relayArgs, "--mcp")
	w.startRelay()
	app := newOAuthApp(t, mcpAddress(t, w.app), "Resume test")
	app.connect(app.authorize())
	var req *localv1.PairingDetails
	eventually(t, 30*time.Second, "MCP approval request", func() bool {
		l, err := w.app.client.ListPairingRequests(context.Background(), &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		req = l.Pairings[0]
		return true
	})
	if _, err := w.app.client.ApproveClientPairing(context.Background(), &localv1.ApproveClientPairingRequest{Code: req.Code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "MCP approved", func() bool { return app.consentState() == "active" })
	client := app.mcpSession(app.finish(app.get(app.consentURL)).Access)
	gitOut(t, w.repo, "branch", "task-base", w.repoHead)
	gitOut(t, w.repo, "checkout", "-b", "project-default")
	if err := os.WriteFile(filepath.Join(w.repo, "default-only.txt"), []byte("not in task-base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, w.repo, "add", "default-only.txt")
	gitOut(t, w.repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "project default")

	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			if fallback {
				gitOut(t, w.repo, "config", "extensions.worktreeConfig", "true")
			}
			// Explicit base overrides a different project default.
			d := callTool(t, client, "session_start", map[string]any{
				"project": "demo", "agent": "codex", "base": "task-base",
				"prompt": "write previous.txt preserved\nsay first", "wait": true, "timeout": "30s",
			})
			s := firstSession(string(d.raw))
			if !d.OK || s.Status != "idle" {
				t.Fatalf("start: %s", d.raw)
			}
			head := gitOut(t, w.repo, "rev-parse", "hermesh/"+s.SessionID)
			w.restartOwner(false)
			if err := os.RemoveAll(w.worktree(s.SessionID)); err != nil {
				t.Fatal(err)
			}
			d = callTool(t, client, "message_send", map[string]any{
				"to": s.SessionID, "message": "resumed", "wait": true, "timeout": "30s",
			})
			if !d.OK {
				t.Fatalf("resume: %s", d.raw)
			}
			d = callTool(t, client, "session_get", map[string]any{"session_id": s.SessionID})
			got := firstSession(string(d.raw))
			if !d.OK || got.Status != "idle" || got.Ref != s.Ref || got.Result != "resumed: "+s.Ref {
				t.Fatalf("resumed session: %s", d.raw)
			}
			if b, err := os.ReadFile(filepath.Join(w.worktree(s.SessionID), "previous.txt")); err != nil || !strings.Contains(string(b), "preserved") {
				t.Fatalf("previous work lost: %q %v", b, err)
			}
			if _, err := os.Stat(filepath.Join(w.worktree(s.SessionID), "default-only.txt")); !os.IsNotExist(err) {
				t.Fatalf("resumed from the project default instead of the explicit base: %v", err)
			}
			if got := gitOut(t, w.repo, "rev-parse", "hermesh/"+s.SessionID); got != head {
				t.Fatalf("branch moved: %s, want %s", got, head)
			}
			d = callTool(t, client, "session_diff", map[string]any{"session_id": s.SessionID})
			if !d.OK || !strings.Contains(string(d.raw), "previous.txt") {
				t.Fatalf("original diff lost: %s", d.raw)
			}
			for _, tool := range []string{"session_archive", "session_unarchive"} {
				if d := callTool(t, client, tool, map[string]any{"session_id": s.SessionID}); !d.OK {
					t.Fatalf("%s: %s", tool, d.raw)
				}
			}
			if d := callTool(t, client, "message_send", map[string]any{
				"to": s.SessionID, "message": "say after archive", "wait": true, "timeout": "30s",
			}); !d.OK {
				t.Fatalf("message after unarchive: %s", d.raw)
			}
			d = callTool(t, client, "session_diff", map[string]any{"session_id": s.SessionID})
			if !d.OK || !strings.Contains(string(d.raw), "previous.txt") {
				t.Fatalf("diff after unarchive lost: %s", d.raw)
			}
		})
	}
}
