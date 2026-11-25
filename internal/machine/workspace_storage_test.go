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

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	rt "github.com/hermeshai/hermesh/internal/runtime"
)

func (h *harness) release(id string) error { return h.n.releaseWorkspace(context.Background(), id) }

func (h *harness) exclude(patterns string) {
	h.t.Helper()
	proj, _ := h.n.st.Project("demo")
	if err := os.WriteFile(filepath.Join(proj.Path, ".git", "info", "exclude"), []byte(patterns), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

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
			dir := h.worktreePath(id)
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
			if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: id, HasArchived: true, Archived: true}); connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("archive: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != "keep this" {
				t.Fatalf("local file changed: %q, %v", got, err)
			}
			if proj, err := h.e.GetSession(ctx, h.kp(), id); err != nil || proj.GetSession().GetStatus() != idle {
				t.Fatalf("session no longer idle: %v, %v", proj.GetSession(), err)
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
	if row.ProjectId != "demo" || row.SessionId != id || row.Path != h.worktreePath(id) || row.SizeBytes == 0 ||
		row.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNSPECIFIED || row.LastActive == nil || row.Status != idle {
		t.Fatal(row)
	}
	for range 2 {
		if res := local.ReleaseWorkspaces(ctx, []string{id}); !res.Results[0].Released {
			t.Fatal(res)
		}
	}
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
	// Its first turn makes the worktree, a moment after the session starts running.
	w := h.listed(busy)
	for deadline := time.Now().Add(5 * time.Second); w == nil && time.Now().Before(deadline); w = h.listed(busy) {
		time.Sleep(20 * time.Millisecond)
	}
	if w == nil || w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY {
		t.Fatalf("busy listed: %v", w)
	}
	if r := local.ReleaseWorkspaces(ctx, []string{busy}).Results[0]; r.Released || r.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY || !h.worktreeExists(busy) {
		t.Fatalf("busy release: %v", r)
	}
}

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
	dir := h.worktreePath(ok)
	for _, f := range []string{"node_modules/left-pad/index.js", "web/node_modules/a/b.js", ".venv/bin/python", "pkg/__pycache__/m.pyc"} {
		write(dir, f)
	}
	for file, want := range map[string]string{".env": ".env", "dist/app.js": "dist/"} {
		id := h.start("say ready")
		h.wait(id, idle)
		write(h.worktreePath(id), "node_modules/x.js")
		write(h.worktreePath(id), file)
		if w := h.listed(id); w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES || w.BlockPath != want {
			t.Fatalf("%s listed: %v", file, w)
		}
		r := h.n.ReleaseWorkspaces(ctx, []string{id, ok}).Results
		if r[0].Released || r[0].Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES || r[0].BlockPath != want {
			t.Fatalf("%s kept: %v", file, r[0])
		}
		if _, err := os.Stat(filepath.Join(h.worktreePath(id), file)); err != nil {
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
	if err := os.WriteFile(filepath.Join(h.worktreePath(dirty), "draft.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	weekAgo := timestamppb.New(time.Now().Add(-autoReleaseAfter - time.Hour))
	for _, id := range []string{old, dirty} {
		if _, err := h.e.Store().Mutate(func(tx *store.Tx) error {
			s, err := tx.GetSession(id)
			if err != nil {
				return err
			}
			s.UpdatedAt = weekAgo
			return tx.PutSessionPreserveUpdatedAt(s)
		}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	h.n.autoRelease(ctx, now)
	if !h.worktreeExists(old) {
		t.Fatal("released while off")
	}
	if _, err := h.n.ApplySettings(ctx, &identityv1.AdminCommand{OwnerId: h.n.OwnerID(), CommandId: "c1", TargetMachineId: h.n.machineID,
		ExpiresAt: timestamppb.New(now.Add(time.Minute)), Command: &identityv1.AdminCommand_SetWorkspacePolicy{SetWorkspacePolicy: &identityv1.SetWorkspacePolicy{AutoRelease: true}}}); err != nil {
		t.Fatal(err)
	}
	if res, err := h.n.ListWorkspaces(ctx, true); err != nil || !res.AutoRelease {
		t.Fatalf("policy: %v %v", res, err)
	}
	h.n.autoRelease(ctx, now)
	if h.worktreeExists(old) || !h.worktreeExists(dirty) || !h.worktreeExists(recent) {
		t.Fatalf("after: old %t dirty %t recent %t", h.worktreeExists(old), h.worktreeExists(dirty), h.worktreeExists(recent))
	}
	kept := h.n.wsCache.autoKept[dirty]
	if kept.IsZero() {
		t.Fatal("dirty not remembered")
	}
	h.n.autoRelease(ctx, now.Add(2*time.Hour))
	if h.n.wsCache.autoKept[dirty] != kept {
		t.Fatal("retried within a day")
	}
	h.n.autoRelease(ctx, now.Add(autoRetryAfter+time.Hour))
	if h.n.wsCache.autoKept[dirty] == kept || !h.worktreeExists(dirty) {
		t.Fatal("not retried after a day, or released with local files")
	}
	h.send(old, "resumed")
	h.wait(old, idle)
	if !h.worktreeExists(old) {
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

type workspaceClosingAgent struct {
	rt.Session
	started, finish chan struct{}
	once            sync.Once
}

func (p *workspaceClosingAgent) Close() error {
	p.once.Do(func() { close(p.started) })
	<-p.finish
	return p.Session.Close()
}

func TestReleaseRechecksAfterStoppingAgent(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say ready")
	sess := h.wait(id, idle)
	ref := sess.ProviderSessionRef
	lease := h.e.Supervisor().Get(sess.ActiveProviderSessionId)
	if lease == nil {
		t.Fatal("expected live provider session lease")
	}
	ps := &workspaceClosingAgent{Session: lease.Session, started: make(chan struct{}), finish: make(chan struct{})}
	lease.Session = ps
	unblock := sync.OnceFunc(func() { close(ps.finish) })
	defer unblock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	released := make(chan error, 1)
	go func() { released <- h.n.releaseWorkspace(ctx, id) }()
	select {
	case <-ps.started:
	case <-ctx.Done():
		t.Fatal("release never stopped the agent")
	}
	evs, _ := h.e.Store().EventsAfter(id, 0)
	if len(evs) > 0 {
		h.mark[id] = evs[len(evs)-1].Seq
	}
	sent := make(chan error, 1)
	go func() {
		_, err := h.e.DispatchMessage(ctx, h.kp(), &sessionv1.DispatchMessageRequest{SessionId: id, Text: "resumed"})
		sent <- err
	}()
	note := filepath.Join(h.worktreePath(id), "last-write.txt")
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
	after := h.wait(id, idle)
	if after.Progress.TurnCount != 2 || after.Result != "resumed: "+ref {
		t.Fatalf("concurrent follow-up: %v", after)
	}
	if b, err := os.ReadFile(note); err != nil || string(b) != "not snapshotted" {
		t.Fatalf("file written at shutdown: %q, %v", b, err)
	}
}
