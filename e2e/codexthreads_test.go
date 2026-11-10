package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestCodexThreads: the Codex apps list a session's thread by the
// session's title, not by its worktree's directory (s_…), and only while
// the session is in use. The title follows a rename, also while the agent
// runs; archiving the session archives the thread, and unarchiving brings
// it back. A thread the user archived in the Codex app still resumes, with
// its conversation, when the session gets a message.
func TestCodexThreads(t *testing.T) {
	unixOnly(t, "its fake codex is a shell script")
	if testing.Short() {
		t.Skip("e2e")
	}
	dir := t.TempDir()
	threads := filepath.Join(dir, "threads")
	os.Mkdir(threads, 0o755)
	codex := filepath.Join(dir, "codex")
	os.WriteFile(codex, []byte(`#!/bin/sh
case "$1" in
--version) echo "codex-cli 0.167.0";;
login) echo "Logged in using ChatGPT";;
*) FAKE_CODEX_THREADS=`+threads+` exec `+filepath.Join(binaries(t), "fakecodex")+` "$@";;
esac
`), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_CODEX_BIN=" + codex}, &localv1.GrantSpec{Agents: []string{"codex"}})
	calls := func() string { b, _ := os.ReadFile(filepath.Join(threads, "calls")); return string(b) }
	name := func(ref string) string { b, _ := os.ReadFile(filepath.Join(threads, ref+".name")); return string(b) }
	archived := func(ref string) bool { _, err := os.Stat(filepath.Join(threads, ref+".archived")); return err == nil }

	s, r := w.startSession(60*time.Second, "--agent", "codex", "--wait", "--prompt", "PR 1251 review: Feishu QR sign-in\nRead the diff and list what breaks.")
	if r.code != 0 || s.Status != "idle" || s.Ref == "" {
		t.Fatalf("start: %d %s %s", r.code, r.stdout, r.stderr)
	}
	ref := s.Ref
	if got := name(ref); got != "PR 1251 review: Feishu QR sign-in" || s.Title != got {
		t.Fatalf("thread named %q, session titled %q", got, s.Title)
	}

	// Renamed while its agent runs: the thread takes the name at once.
	if _, err := w.app.client.RenameSession(context.Background(), &localv1.RenameSessionRequest{SessionId: s.SessionID, Title: "Feishu QR review"}); err != nil {
		t.Fatal(err)
	}
	if got := name(ref); got != "Feishu QR review" {
		t.Fatalf("after rename, thread named %q; calls:\n%s", got, calls())
	}

	if r := w.cli(30*time.Second, "session", "archive", s.SessionID); r.code != 0 || !archived(ref) {
		t.Fatalf("archive: %d %s %s; thread archived %v", r.code, r.stdout, r.stderr, archived(ref))
	}
	if r := w.cli(30*time.Second, "session", "unarchive", s.SessionID); r.code != 0 || archived(ref) {
		t.Fatalf("unarchive: %d %s %s; thread archived %v", r.code, r.stdout, r.stderr, archived(ref))
	}

	// The user archives the thread in the Codex app; the session's next
	// message resumes the same thread, still named after the session.
	os.WriteFile(filepath.Join(threads, ref+".archived"), nil, 0o644)
	s2, r := w.send(60*time.Second, s.SessionID, "And the tests?")
	if r.code != 0 || s2.Status != "idle" || s2.Ref != ref || s2.LastFailure != nil {
		t.Fatalf("send after archiving in Codex: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if archived(ref) || name(ref) != "Feishu QR review" {
		t.Fatalf("thread archived %v, named %q", archived(ref), name(ref))
	}
	if c := calls(); !strings.Contains(c, "thread/unarchive "+ref+"\nthread/resume "+ref+"\n") {
		t.Fatalf("not unarchived before the resume:\n%s", c)
	}
}
