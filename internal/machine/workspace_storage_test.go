package machine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/hermeshai/hermesh/internal/provider"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// release releases one checkout as the owner's settings do.
func (h *harness) release(id string) error { return h.s.releaseWorkspace(context.Background(), id) }

func (h *harness) exclude(patterns string) {
	h.t.Helper()
	proj, _ := h.n.st.Project("demo")
	if err := os.WriteFile(filepath.Join(proj.Path, ".git", "info", "exclude"), []byte(patterns), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// listed is the session's row in a fresh scan, or nil.
func (h *harness) listed(id string) *machinev1.Workspace {
	h.t.Helper()
	res, err := h.n.ListWorkspaces(context.Background(), false)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, w := range res.Workspaces {
		if w.SessionId == id {
			return w
		}
	}
	return nil
}

func TestReleaseWorkspaceProtectsFiles(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	h.exclude("build/\n")
	blocks := map[string]machinev1.WorkspaceBlock{
		"modified": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, "staged": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES,
		"untracked": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, "ignored": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES,
		"skip-worktree": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, "assume-unchanged": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES,
		"other branch": machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED,
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
			if w := h.listed(id); w == nil || w.Block != blocks[kind] {
				t.Fatalf("listed: %v", w)
			}
			res := h.n.ReleaseWorkspaces(ctx, []string{id})
			if r := res.Results[0]; r.Released || r.Block != blocks[kind] {
				t.Fatalf("release: %v", r)
			}
			if err := h.release(id); connect.CodeOf(err) != connect.CodeAborted {
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

func TestWorkspaceUsageAndResume(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	local := h.n.Local()
	dir := t.TempDir()
	h.inPlaceProject("notes", dir)
	inPlace := h.startIn("notes", "write mine.txt original")
	h.wait(inPlace, idle)
	if err := h.release(inPlace); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("in-place release: %v", err)
	}
	id := h.start("write preserved.txt saved")
	ref := h.wait(id, idle).ProviderSessionRef
	rows, err := local.ListWorkspaces(ctx, false)
	if err != nil || len(rows.Workspaces) != 1 || rows.ScannedAt == nil || rows.AutoRelease {
		t.Fatalf("list: %v %v", rows, err)
	}
	row := rows.Workspaces[0]
	if row.ProjectId != "demo" || row.SessionId != id || row.Path != h.s.ws.Path(id) || row.SizeBytes == 0 ||
		row.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNSPECIFIED || row.LastActive == nil || row.Status != idle {
		t.Fatal(row)
	}
	for range 2 {
		if res := local.ReleaseWorkspaces(ctx, []string{id}); !res.Results[0].Released {
			t.Fatal(res)
		}
	}
	// The last scan no longer has it, at once.
	if rows, err = local.ListWorkspaces(ctx, true); err != nil || len(rows.Workspaces) != 0 {
		t.Fatalf("cached: %v %v", rows, err)
	}
	if rows, err = local.ListWorkspaces(ctx, false); err != nil || len(rows.Workspaces) != 0 {
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
	if w := h.listed(busy); w == nil || w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY {
		t.Fatalf("busy listed: %v", w)
	}
	if r := local.ReleaseWorkspaces(ctx, []string{busy}).Results[0]; r.Released || r.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY || !h.s.ws.Exists(busy) {
		t.Fatalf("busy release: %v", r)
	}
}

// Ignored files a package manager makes again go with the checkout; any
// other ignored file keeps it, since `git worktree remove` would delete it.
func TestReleaseWorkspaceRegenerable(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	h.exclude("node_modules/\n.venv/\n__pycache__/\n.env\ndist/\n")
	write := func(dir, file string) {
		t.Helper()
		p := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ok := h.start("say ready")
	h.wait(ok, idle)
	dir := h.s.ws.Path(ok)
	for _, f := range []string{"node_modules/left-pad/index.js", "web/node_modules/a/b.js", ".venv/bin/python", "pkg/__pycache__/m.pyc"} {
		write(dir, f)
	}
	for file, want := range map[string]string{".env": ".env", "dist/app.js": "dist/"} {
		id := h.start("say ready")
		h.wait(id, idle)
		write(h.s.ws.Path(id), "node_modules/x.js")
		write(h.s.ws.Path(id), file)
		if w := h.listed(id); w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES || w.BlockPath != want {
			t.Fatalf("%s listed: %v", file, w)
		}
		r := h.n.ReleaseWorkspaces(ctx, []string{id, ok}).Results
		if r[0].Released || r[0].Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES || r[0].BlockPath != want {
			t.Fatalf("%s kept: %v", file, r[0])
		}
		if _, err := os.Stat(filepath.Join(h.s.ws.Path(id), file)); err != nil {
			t.Fatalf("%s lost: %v", file, err)
		}
		if !r[1].Released {
			t.Fatalf("regenerable kept: %v", r[1])
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("released checkout still there: %v", err)
	}
}

func TestRegenerablePath(t *testing.T) {
	for path, want := range map[string]bool{
		"node_modules/": true, "web/node_modules/": true, ".venv/": true, "a/__pycache__/": true, "m.pyc": true, ".DS_Store": true,
		".env": false, "dist/": false, "build/": false, "secrets/key.pem": false, "node_modules": true, "pyc/": false, "notes.pyc/": false,
	} {
		if regenerablePath(path) != want {
			t.Errorf("%s: want %t", path, want)
		}
	}
}

func TestAutoReleaseIdleWorkspaces(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	old := h.start("say ready")
	h.wait(old, idle)
	dirty := h.start("say ready")
	h.wait(dirty, idle)
	recent := h.start("say ready")
	h.wait(recent, idle)
	if err := os.WriteFile(filepath.Join(h.s.ws.Path(dirty), "draft.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	weekAgo := time.Now().Add(-autoReleaseAfter - time.Hour).Unix()
	for _, id := range []string{old, dirty} {
		if _, err := h.n.st.db.Exec(`UPDATE sessions SET updated_at = ? WHERE session_id = ?`, weekAgo, id); err != nil {
			t.Fatal(err)
		}
	}
	// Off: nothing goes.
	now := time.Now()
	h.s.autoRelease(ctx, now)
	if !h.s.ws.Exists(old) {
		t.Fatal("released while off")
	}
	if _, err := h.n.ApplySettings(ctx, &identityv1.AdminCommand{OwnerId: h.n.OwnerID(), CommandId: "c1", TargetMachineId: h.n.machineID,
		ExpiresAt: timestamppb.New(now.Add(time.Minute)), Command: &identityv1.AdminCommand_SetWorkspacePolicy{SetWorkspacePolicy: &identityv1.SetWorkspacePolicy{AutoRelease: true}}}); err != nil {
		t.Fatal(err)
	}
	if res, err := h.n.ListWorkspaces(ctx, true); err != nil || !res.AutoRelease {
		t.Fatalf("policy: %v %v", res, err)
	}
	// The Janitor took its first run when it started, with this off.
	h.s.autoRelease(ctx, now)
	if h.s.ws.Exists(old) || !h.s.ws.Exists(dirty) || !h.s.ws.Exists(recent) {
		t.Fatalf("after: old %t dirty %t recent %t", h.s.ws.Exists(old), h.s.ws.Exists(dirty), h.s.ws.Exists(recent))
	}
	kept := h.s.wsCache.autoKept[dirty]
	if kept.IsZero() {
		t.Fatal("dirty not remembered")
	}
	// Within a day it does not retry.
	h.s.autoRelease(ctx, now.Add(2*time.Hour))
	if h.s.wsCache.autoKept[dirty] != kept {
		t.Fatal("retried within a day")
	}
	h.s.autoRelease(ctx, now.Add(autoRetryAfter+time.Hour))
	if h.s.wsCache.autoKept[dirty] == kept || !h.s.ws.Exists(dirty) {
		t.Fatal("not retried after a day, or released with local files")
	}
	h.send(old, "resumed")
	h.wait(old, idle)
	if !h.s.ws.Exists(old) {
		t.Fatal("not checked out again")
	}
}

func TestAutoReleaseDue(t *testing.T) {
	var c workspaceCache
	now := time.Now()
	if !c.autoDue(now, 0) || c.autoDue(now.Add(2*time.Hour), 0) {
		t.Fatal("a run is claimed once while it runs")
	}
	c.autoRunning = false
	if c.autoDue(now.Add(time.Minute), 0) || !c.autoDue(now.Add(time.Hour), 0) {
		t.Fatal("not hourly")
	}
}

func TestStorageIsTheOwners(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	a := &adminService{n: h.n}
	ctx := context.WithValue(context.Background(), principalKey{}, h.p)
	if _, err := a.ListWorkspaces(ctx, &machinev1.ListWorkspacesRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("list by a client: %v", err)
	}
	if _, err := a.ReleaseWorkspaces(ctx, &machinev1.ReleaseWorkspacesRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("release by a client: %v", err)
	}
	if !slices.Contains(h.n.Status().Methods, "ListWorkspaces") || !slices.Contains(h.n.Status().Methods, "ReleaseWorkspaces") {
		t.Fatal(h.n.Status().Methods)
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
	go func() { released <- h.s.releaseWorkspace(ctx, id) }()
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
