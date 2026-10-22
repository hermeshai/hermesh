package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// TestSessionLifecycle: a session never ends by itself. A turn can be
// interrupted and the session goes on in the same conversation; archiving
// makes it read-only and frees its worktree, keeping its work; unarchiving
// brings both back.
func TestSessionLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	ctx := context.Background()
	cli := func(args ...string) result { return w.cli(time.Minute, args...) }
	status := func(id string) string {
		return jsonDoc(t, cli("session", "get", id, "-o", "json"))["session"].(map[string]any)["status"].(string)
	}

	s, r := w.startSession(time.Minute, "--wait", "--prompt", "write one.txt first\nsay one")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	id, ref := s.SessionID, s.Ref

	// Interrupt: a waiting caller learns who stopped the turn; the session is
	// idle and the next message goes on in the same agent conversation.
	waiter := start(t, "waiter", w.cliEnv, filepath.Join(w.bin, "hermesh"), "session", "send", id, "--wait", "--message", "sleep 60s\nsay late")
	eventually(t, 30*time.Second, "turn running", func() bool { return status(id) == "running" })
	r = cli("session", "interrupt", id)
	if r.code != 0 {
		t.Fatalf("interrupt: %+v", r)
	}
	if c := waiter.wait(t, 30*time.Second); c != 5 || !strings.Contains(waiter.output(), "interrupted by") {
		t.Fatalf("waiting caller of the interrupted turn: exit %d\n%s", c, waiter.output())
	}
	if st := status(id); st != "idle" {
		t.Fatalf("after interrupt: %s", st)
	}
	if r := cli("session", "send", id, "--wait", "--message", "resumed"); r.code != 0 || !strings.Contains(r.stdout, "resumed: "+ref) {
		t.Fatalf("after interrupt, same conversation: %+v", r)
	}
	// The owner can interrupt too.
	if r := cli("session", "send", id, "--message", "sleep 60s"); r.code != 0 {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, 30*time.Second, "turn running", func() bool { return status(id) == "running" })
	if _, err := w.app.client.InterruptSession(ctx, &localv1.InterruptSessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "idle after the owner's interrupt", func() bool { return status(id) == "idle" })

	// Archiving waits for no turn: one in progress must be interrupted first.
	if r := cli("session", "send", id, "--message", "sleep 60s"); r.code != 0 {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, 30*time.Second, "turn running", func() bool { return status(id) == "running" })
	if r := cli("session", "archive", id); r.code != 2 || !strings.Contains(r.stderr, "interrupt") {
		t.Fatalf("archive during a turn: %+v", r)
	}
	cli("session", "interrupt", id)
	eventually(t, 30*time.Second, "idle", func() bool { return status(id) == "idle" })

	// Archive: read-only, out of the default list, worktree gone, work kept.
	r = cli("session", "archive", id)
	if r.code != 0 || status(id) != "archived" {
		t.Fatalf("archive: %+v", r)
	}
	if _, err := os.Stat(w.worktree(id)); !os.IsNotExist(err) {
		t.Fatalf("worktree after archive: %v", err)
	}
	if out, err := exec.Command("git", "-C", w.repo, "show", "hermesh/"+id+":one.txt").Output(); err != nil || string(out) != "first\n" {
		t.Fatalf("the session branch keeps its work: %q %v", out, err)
	}
	if d := cli("session", "diff", id); d.code != 0 || !strings.Contains(d.stdout, "+first") {
		t.Fatalf("diff of an archived session: %+v", d)
	}
	if r := cli("session", "send", id, "--message", "say hi"); r.code != 2 || !strings.Contains(r.stderr, "session unarchive") {
		t.Fatalf("send to an archived session: %+v", r)
	}
	if r := cli("session", "list"); strings.Contains(r.stdout, id) {
		t.Fatalf("archived session in the default list: %+v", r)
	}
	if r := cli("session", "list", "--archived"); !strings.Contains(r.stdout, id) {
		t.Fatalf("archived session not in --archived: %+v", r)
	}

	// Unarchive: the worktree is back with the earlier turns' work, and the
	// agent goes on in the same conversation.
	if r := cli("session", "unarchive", id); r.code != 0 || status(id) != "idle" {
		t.Fatalf("unarchive: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(w.worktree(id), "one.txt")); string(b) != "first\n" {
		t.Fatalf("worktree after unarchive: %q", b)
	}
	if r := cli("session", "send", id, "--wait", "--message", "write two.txt second\nresumed"); r.code != 0 || !strings.Contains(r.stdout, "resumed: "+ref) {
		t.Fatalf("after unarchive, same conversation: %+v", r)
	}
	if d := cli("session", "diff", id); !strings.Contains(d.stdout, "+first") || !strings.Contains(d.stdout, "+second") {
		t.Fatalf("diff after unarchive: %+v", d)
	}

	// The old verbs are gone.
	for _, args := range [][]string{{"session", "cancel", id}, {"status"}} {
		if r := cli(args...); r.code != 2 {
			t.Fatalf("%v: %+v", args, r)
		}
	}

	// Revoking the agent stops its running turns; the sessions stay for the owner.
	if r := cli("session", "send", id, "--message", "sleep 60s"); r.code != 0 {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, 30*time.Second, "turn running", func() bool { return status(id) == "running" })
	clients, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	if _, err := w.app.client.RevokeClient(ctx, &localv1.RevokeClientRequest{GrantId: clients.Clients[0].GrantId}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "turn stopped by the revocation", func() bool {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		return err == nil && len(res.Sessions) == 1 && res.Sessions[0].Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	})
}
