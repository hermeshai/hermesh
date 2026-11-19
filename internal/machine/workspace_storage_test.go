package machine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/hermeshai/hermesh/internal/provider"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestReleaseLocalWorkspaceProtectsFiles(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	proj, _ := h.n.st.Project("demo")
	exclusions := filepath.Join(proj.Path, ".git", "info", "exclude")
	if err := os.WriteFile(exclusions, []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"modified", "staged", "untracked", "ignored", "skip-worktree", "assume-unchanged", "other branch"} {
		t.Run(kind, func(t *testing.T) {
			id := h.start("say ready")
			h.wait(id, idle)
			dir := h.s.ws.Path(id)
			file := "a.txt"
			switch kind {
			case "ignored":
				file = "build/notes"
				if err := os.Mkdir(filepath.Join(dir, "build"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "untracked":
				file = "draft.txt"
			case "skip-worktree", "assume-unchanged":
				gitOut(t, dir, "update-index", "--"+kind, file)
			case "other branch":
				gitOut(t, dir, "switch", "-c", "hand-edited")
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte("keep this"), 0o644); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" || kind == "other branch" {
				gitOut(t, dir, "add", file)
			}
			if kind == "other branch" {
				gitOut(t, dir, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "-m", "manual work")
			}
			if err := h.n.Local().ReleaseLocalWorkspace(ctx, id); connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("release: %v", err)
			}
			if _, err := h.s.Archive(ctx, h.p, id); connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("archive: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != "keep this" {
				t.Fatalf("local file changed: %q, %v", got, err)
			}
			if sess, err := h.s.Get(h.p, id); err != nil || sess.Status != idle {
				t.Fatalf("session no longer idle: %v, %v", sess, err)
			}
		})
	}
}

func TestLocalWorkspaceUsageAndResume(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	local := h.n.Local()
	dir := t.TempDir()
	h.inPlaceProject("notes", dir)
	inPlace := h.startIn("notes", "write mine.txt original")
	h.wait(inPlace, idle)
	if err := local.ReleaseLocalWorkspace(ctx, inPlace); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("in-place release: %v", err)
	}
	id := h.start("write preserved.txt saved")
	ref := h.wait(id, idle).ProviderSessionRef
	rows, err := local.ListLocalWorkspaces(ctx)
	if err != nil || len(rows.Workspaces) != 1 {
		t.Fatalf("list: %v %v", rows, err)
	}
	row := rows.Workspaces[0]
	if row.ProjectId != "demo" || row.SessionId != id || row.Path != h.s.ws.Path(id) || row.SizeBytes == 0 {
		t.Fatal(row)
	}
	for range 2 {
		if err := local.ReleaseLocalWorkspace(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if rows, err = local.ListLocalWorkspaces(ctx); err != nil || len(rows.Workspaces) != 0 {
		t.Fatalf("released: %v %v", rows, err)
	}
	h.send(id, "resumed")
	after := h.wait(id, idle)
	if after.ProviderSessionRef != ref || after.Result != "resumed: "+ref || after.Progress.TurnCount != 2 {
		t.Fatalf("lost original conversation: %v", after)
	}
	if b, err := os.ReadFile(filepath.Join(row.Path, "preserved.txt")); err != nil || !strings.Contains(string(b), "saved") {
		t.Fatalf("restore: %q %v", b, err)
	}
	busy := h.start("sleep 60s")
	h.wait(busy, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)
	if err := local.ReleaseLocalWorkspace(ctx, busy); connect.CodeOf(err) != connect.CodeFailedPrecondition || !h.s.ws.Exists(busy) {
		t.Fatalf("busy release: %v", err)
	}
}

// An agent can write final output when asked to close. Hold it there to race
// a follow-up against the release, then leave an ignored/local file on disk.
type workspaceClosingAgent struct {
	provider.Session
	started, finish chan struct{}
}

func (p *workspaceClosingAgent) Close() error {
	close(p.started)
	<-p.finish
	return p.Session.Close()
}

func TestReleaseRechecksAfterStoppingAgent(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say ready")
	ref := h.wait(id, idle).ProviderSessionRef
	l, _, err := h.s.find(h.p, id, false)
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	ps := &workspaceClosingAgent{Session: l.sess, started: make(chan struct{}), finish: make(chan struct{})}
	l.sess = ps
	l.mu.Unlock()
	unblock := sync.OnceFunc(func() { close(ps.finish) })
	defer unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	released := make(chan error, 1)
	go func() { released <- h.n.Local().ReleaseLocalWorkspace(ctx, id) }()
	select {
	case <-ps.started:
	case <-ctx.Done():
		t.Fatal("release never stopped the agent")
	}
	h.mark[id] = len(h.events(id))
	sent := make(chan error, 1)
	go func() {
		_, err := h.s.Send(ctx, h.p, id, "resumed", nil)
		sent <- err
	}()
	note := filepath.Join(h.s.ws.Path(id), "last-write.txt")
	if err := os.WriteFile(note, []byte("not snapshotted"), 0o600); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-released; connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("release discarded a file written at shutdown: %v", err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	h.wait(id, idle)
	if got := h.lastTurn(id); got.Turn != 2 || got.Result != "resumed: "+ref {
		t.Fatalf("concurrent follow-up: %v", got)
	}
	if b, err := os.ReadFile(note); err != nil || string(b) != "not snapshotted" {
		t.Fatalf("file written at shutdown: %q, %v", b, err)
	}
}
