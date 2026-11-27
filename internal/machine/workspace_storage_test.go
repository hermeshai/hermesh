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
			archRes, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: id, HasArchived: true, Archived: true})
			if err != nil {
				t.Fatalf("archive should succeed while keeping workspace: %v", err)
			}
			if archRes.TransitionApplied != "workspace_kept" || archRes.GetSession().GetStatus() != archived {
				t.Fatalf("archive response: %+v", archRes)
			}
			if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != "keep this" {
				t.Fatalf("local file changed: %q, %v", got, err)
			}
			if w := h.listed(id); w == nil || w.Block != blocks[kind] {
				t.Fatalf("archived kept workspace not listed in storage: %v", w)
			}
			if r := h.n.ReleaseWorkspaces(ctx, []string{id}).Results[0]; r.Released || r.Block != blocks[kind] {
				t.Fatalf("archived kept workspace released despite local changes: %v", r)
			}
			if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: id, HasArchived: true, Archived: false}); err != nil {
				t.Fatalf("unarchive: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != "keep this" {
				t.Fatalf("local file changed after unarchive: %q, %v", got, err)
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
		"packages/HermeshKit/.swiftpm/": true, "apps/ios/Hermesh.xcodeproj/xcuserdata/": true,
		".env": false, "dist/": false, "build/": false, "apps/ios/build/": false, "secrets/key.pem": false, "node_modules": true, "pyc/": false, "notes.pyc/": false,
	} {
		if regenerablePath(path) != want {
			t.Errorf("%s: want %t", path, want)
		}
	}
}

func TestArchiveSessionWorktreeLifecycle(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	h.exclude("/apps/ios/build/\n.swiftpm/\nxcuserdata/\n")

	// 1. A session with only regenerable caches (.swiftpm, xcuserdata) releases its worktree and updates cached storage on archive.
	cleanID := h.start("say ready")
	h.wait(cleanID, idle)
	cleanDir := h.worktreePath(cleanID)
	for _, rel := range []string{"packages/HermeshKit/.swiftpm/xcode/state.json", "apps/ios/xcuserdata/user.plist"} {
		full := filepath.Join(cleanDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("cache"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if w := h.listed(cleanID); w == nil || w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNSPECIFIED {
		t.Fatalf("clean listed: %v", w)
	}
	resClean, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{
		CommandId: "cmd_arch_clean", SessionId: cleanID, HasArchived: true, Archived: true,
	})
	if err != nil || resClean.GetSession().GetStatus() != archived || resClean.TransitionApplied == "workspace_kept" {
		t.Fatalf("archive clean: %v %+v", err, resClean)
	}
	if h.worktreeExists(cleanID) {
		t.Fatal("expected clean worktree removed on archive")
	}
	if cached, cErr := h.n.ListWorkspaces(ctx, true); cErr != nil || slices.ContainsFunc(cached.Workspaces, func(w *machinev1.Workspace) bool { return w.SessionId == cleanID }) {
		t.Fatalf("archived released session still in cached storage: %v %v", cached, cErr)
	}

	// 2. A session with ignored non-regenerable build artifacts (apps/ios/build/) archives cleanly, keeps the files,
	// stays visible in Storage, and can be released once cleaned up.
	buildID := h.start("say ready")
	h.wait(buildID, idle)
	buildDir := h.worktreePath(buildID)
	xcf := filepath.Join(buildDir, "apps/ios/build/HermeshCore.xcframework/Info.plist")
	if err := os.MkdirAll(filepath.Dir(xcf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xcf, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resKept, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{
		CommandId: "cmd_arch_kept", SessionId: buildID, HasArchived: true, Archived: true,
	})
	if err != nil {
		t.Fatalf("archive with apps/ios/build failed: %v", err)
	}
	if resKept.GetSession().GetStatus() != archived || resKept.TransitionApplied != "workspace_kept" {
		t.Fatalf("expected archived with workspace_kept, got %+v", resKept)
	}
	// Idempotent retry with same CommandId returns cached workspace_kept receipt.
	retryKept, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{
		CommandId: "cmd_arch_kept", SessionId: buildID, HasArchived: true, Archived: true,
	})
	if err != nil || retryKept.TransitionApplied != "workspace_kept" {
		t.Fatalf("idempotent archive receipt: %v %+v", err, retryKept)
	}
	if b, err := os.ReadFile(xcf); err != nil || string(b) != "<plist/>" {
		t.Fatalf("apps/ios/build file lost: %q %v", b, err)
	}
	if w := h.listed(buildID); w == nil || w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES || w.BlockPath != "apps/ios/build/" {
		t.Fatalf("expected kept archived worktree listed with apps/ios/build/, got %v", w)
	}
	if err := os.RemoveAll(filepath.Join(buildDir, "apps/ios/build")); err != nil {
		t.Fatal(err)
	}
	rel := h.n.ReleaseWorkspaces(ctx, []string{buildID})
	if len(rel.Results) != 1 || !rel.Results[0].Released || h.worktreeExists(buildID) {
		t.Fatalf("releasing cleaned archived worktree failed: %+v", rel)
	}

	// 3. A file flushed by the live provider process during shutdown on Archive is preserved (not deleted).
	flushID := h.start("say ready")
	flushSess := h.wait(flushID, idle)
	lease := h.e.Supervisor().Get(flushSess.ActiveProviderSessionId)
	if lease == nil {
		t.Fatal("expected live provider lease")
	}
	flushedPath := filepath.Join(h.worktreePath(flushID), "shutdown-flush.txt")
	ps := &workspaceClosingAgent{Session: lease.Session, started: make(chan struct{}), finish: make(chan struct{})}
	lease.Session = ps
	doneArch := make(chan *sessionv1.UpdateSessionResponse, 1)
	errArch := make(chan error, 1)
	go func() {
		r, aErr := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: flushID, HasArchived: true, Archived: true})
		if aErr != nil {
			errArch <- aErr
			return
		}
		doneArch <- r
	}()
	<-ps.started
	if err := os.WriteFile(flushedPath, []byte("flushed on close"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(ps.finish)
	select {
	case aErr := <-errArch:
		t.Fatalf("archive with shutdown flush failed: %v", aErr)
	case r := <-doneArch:
		if r.GetSession().GetStatus() != archived || r.TransitionApplied != "workspace_kept" {
			t.Fatalf("expected workspace_kept after shutdown flush, got %+v", r)
		}
	}
	if b, err := os.ReadFile(flushedPath); err != nil || string(b) != "flushed on close" {
		t.Fatalf("shutdown-flushed file lost on archive: %q %v", b, err)
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

func TestArchiveExtremeFaultsAndRaces(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	h.exclude("node_modules/\nbuild/\n")

	// 1. Read-only (0o555 / 0o444) subdirectories inside regenerable caches (like Go modcache):
	// git worktree remove --force fails mid-delete on 0o555 dirs; verify archive & release still
	// remove the entire worktree cleanly without leaving a half-deleted directory, and resume heals.
	roID := h.start("write kept-in-ckpt.txt checkpointed")
	h.wait(roID, idle)
	roDir := h.worktreePath(roID)
	roSub := filepath.Join(roDir, "node_modules", "ro-mod", "v1.0.0")
	if err := os.MkdirAll(roSub, 0o755); err != nil {
		t.Fatal(err)
	}
	roFile := filepath.Join(roSub, "mod.go")
	if err := os.WriteFile(roFile, []byte("package mod"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roSub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roSub, 0o755) })

	archRO, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: roID, HasArchived: true, Archived: true})
	if err != nil || archRO.GetSession().GetStatus() != archived || archRO.TransitionApplied == "workspace_kept" {
		t.Fatalf("archive with 0o555 cache dir: %v %+v", err, archRO)
	}
	if h.worktreeExists(roID) {
		t.Fatal("0o555 cache dir left a half-deleted worktree behind")
	}
	// Duplicate archive preserves original ArchivedAt and is an idempotent no-op.
	firstArchivedAt := archRO.GetSession().GetArchivedAt().AsTime()
	time.Sleep(5 * time.Millisecond)
	dupArch, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: roID, HasArchived: true, Archived: true})
	if err != nil || !dupArch.GetSession().GetArchivedAt().AsTime().Equal(firstArchivedAt) {
		t.Fatalf("duplicate archive mutated ArchivedAt: %v %v vs %v", err, dupArch.GetSession().GetArchivedAt().AsTime(), firstArchivedAt)
	}
	// Unarchive restores the worktree and checkpointed file intact.
	if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: roID, HasArchived: true, Archived: false}); err != nil {
		t.Fatalf("unarchive after 0o555 removal: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(roDir, "kept-in-ckpt.txt")); err != nil || !strings.Contains(string(b), "checkpointed") {
		t.Fatalf("checkpointed file missing after unarchive: %q %v", b, err)
	}

	// 2. Corrupted worktree (.git deleted leaving a broken non-git directory) & Symlink escape guard:
	// First: if .git is missing, Archive safely keeps the directory without deleting unknown contents;
	// if the directory is emptied/broken and the user sends a follow-up, resolveWorkdir self-heals from checkpoint!
	corruptID := h.start("write heal-me.txt intact")
	h.wait(corruptID, idle)
	corruptDir := h.worktreePath(corruptID)
	if err := os.Remove(filepath.Join(corruptDir, ".git")); err != nil {
		t.Fatal(err)
	}
	archCorrupt, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: corruptID, HasArchived: true, Archived: true})
	if err != nil || archCorrupt.TransitionApplied != "workspace_kept" {
		t.Fatalf("archive with missing .git should keep directory safely: %v %+v", err, archCorrupt)
	}
	// Simulate partial wipe leaving an empty non-git folder at corruptDir; unarchiving must self-heal and restore heal-me.txt!
	if err := os.Remove(filepath.Join(corruptDir, "heal-me.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: corruptID, HasArchived: true, Archived: false}); err != nil {
		t.Fatalf("unarchive broken dir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(corruptDir, "heal-me.txt")); err != nil || !strings.Contains(string(b), "intact") {
		t.Fatalf("resolveWorkdir failed to self-heal broken non-git worktree: %q %v", b, err)
	}

	// Symlink escape guard: replacing worktree with a symlink to an external victim dir never deletes the victim!
	symID := h.start("say ready")
	h.wait(symID, idle)
	symDir := h.worktreePath(symID)
	victim := t.TempDir()
	victimFile := filepath.Join(victim, "precious.txt")
	if err := os.WriteFile(victimFile, []byte("never delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(symDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, symDir); err != nil {
		t.Fatal(err)
	}
	archSym, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: symID, HasArchived: true, Archived: true})
	if err != nil || archSym.TransitionApplied != "workspace_kept" {
		t.Fatalf("archive symlink worktree: %v %+v", err, archSym)
	}
	if r := h.n.ReleaseWorkspaces(ctx, []string{symID}).Results[0]; r.Released {
		t.Fatalf("ReleaseWorkspaces followed symlink: %+v", r)
	}
	if b, err := os.ReadFile(victimFile); err != nil || string(b) != "never delete" {
		t.Fatalf("symlink victim file damaged: %q %v", b, err)
	}

	// 3. Crashed session with uncheckpointed edits, special Unicode/quote filenames, and background task/daemon guards:
	specialID := h.start("say ready")
	h.wait(specialID, idle)
	specialRel := "build/报告 (v1) 'draft'.bin"
	specialFull := filepath.Join(h.worktreePath(specialID), specialRel)
	if err := os.MkdirAll(filepath.Dir(specialFull), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specialFull, []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := h.listed(specialID); w == nil || w.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES {
		t.Fatalf("special filename listed: %+v", w)
	}
	// Simulate crashed session + active awaited background task vs daemon task:
	if _, err := h.e.Store().Mutate(func(tx *store.Tx) error {
		s, err := tx.GetSession(specialID)
		if err != nil {
			return err
		}
		s.ActiveBackgroundCount = 1
		return tx.PutSession(s)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: specialID, HasArchived: true, Archived: true}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected FailedPrecondition when ActiveBackgroundCount > 0, got %v", err)
	}
	if r := h.n.ReleaseWorkspaces(ctx, []string{specialID}).Results[0]; r.Released || r.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY {
		t.Fatalf("expected BUSY when ActiveBackgroundCount > 0, got %+v", r)
	}
	// Switch to CRASHED with ActiveBackgroundCount=0 and an uncheckpointed edit: Archive must succeed and keep edits!
	crashedEdit := filepath.Join(h.worktreePath(specialID), "partial-crash.txt")
	if err := os.WriteFile(crashedEdit, []byte("half written before crash"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Store().Mutate(func(tx *store.Tx) error {
		s, err := tx.GetSession(specialID)
		if err != nil {
			return err
		}
		s.Status = sessionv1.SessionStatus_SESSION_STATUS_CRASHED
		s.ActiveBackgroundCount = 0
		return tx.PutSession(s)
	}); err != nil {
		t.Fatal(err)
	}
	archCrashed, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: specialID, HasArchived: true, Archived: true})
	if err != nil || archCrashed.GetSession().GetStatus() != archived || archCrashed.TransitionApplied != "workspace_kept" {
		t.Fatalf("archive crashed session failed: %v %+v", err, archCrashed)
	}
	if b, err := os.ReadFile(crashedEdit); err != nil || string(b) != "half written before crash" {
		t.Fatalf("crashed session partial edit lost: %q %v", b, err)
	}

	// 4. High-concurrency Race Storm: hammer Archive, Unarchive, ReleaseWorkspaces, ListWorkspaces, and DispatchMessage concurrently!
	stormID := h.start("write storm.txt v1")
	h.wait(stormID, idle)
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iter := range 6 {
				switch (worker + iter) % 5 {
				case 0:
					_, _ = h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: stormID, HasArchived: true, Archived: true})
				case 1:
					_, _ = h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: stormID, HasArchived: true, Archived: false})
				case 2:
					_ = h.n.ReleaseWorkspaces(ctx, []string{stormID})
				case 3:
					_, _ = h.n.ListWorkspaces(ctx, iter%2 == 0)
				case 4:
					_, _ = h.e.DispatchMessage(ctx, h.kp(), &sessionv1.DispatchMessageRequest{SessionId: stormID, Text: "say step"})
				}
			}
		}(g)
	}
	wg.Wait()

	// Settle any in-flight run from the storm, ensure unarchived, send a final turn, and verify storm.txt survived!
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		proj, gErr := h.e.GetSession(ctx, h.kp(), stormID)
		if gErr != nil {
			t.Fatal(gErr)
		}
		st := proj.GetSession().GetStatus()
		if st == idle || st == archived || st == sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: stormID, HasArchived: true, Archived: false}); err != nil {
		t.Fatalf("final unarchive after storm: %v", err)
	}
	evs, _ := h.e.Store().EventsAfter(stormID, 0)
	if len(evs) > 0 {
		h.mark[stormID] = evs[len(evs)-1].Seq
	}
	h.send(stormID, "write after-storm.txt ok")
	h.wait(stormID, idle)
	if b, err := os.ReadFile(filepath.Join(h.worktreePath(stormID), "storm.txt")); err != nil || !strings.Contains(string(b), "v1") {
		t.Fatalf("storm corrupted initial checkpointed file: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(h.worktreePath(stormID), "after-storm.txt")); err != nil || !strings.Contains(string(b), "ok") {
		t.Fatalf("storm broke final turn execution: %q %v", b, err)
	}
}

func TestArchiveGraceUndoAndDeferredCleanup(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()

	// 1. Undo within grace window is 100% lossless: TMPDIR, regenerable caches (node_modules),
	// and live provider lease all survive untouched.
	undoID := h.start("write undo-keep.txt v1")
	undoSess := h.wait(undoID, idle)
	undoWT := h.worktreePath(undoID)
	cacheFile := filepath.Join(undoWT, "node_modules/pkg/index.js")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpMarker := filepath.Join(h.n.cfg.DataDir, "sessions", undoID, "tmp", "scratch.txt")
	if err := os.MkdirAll(filepath.Dir(tmpMarker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpMarker, []byte("tmp-alive"), 0o600); err != nil {
		t.Fatal(err)
	}

	archUndo, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{
		SessionId: undoID, HasArchived: true, Archived: true, ArchiveGraceSeconds: 1,
	})
	if err != nil {
		t.Fatalf("archive with grace: %v", err)
	}
	if archUndo.GetSession().GetStatus() != archived || !archUndo.GetSession().GetWorkspaceStrategy().GetArchiveCleanupPending() {
		t.Fatalf("expected archived with cleanup pending, got %+v", archUndo.GetSession())
	}
	if !h.worktreeExists(undoID) || h.e.Supervisor().Get(undoSess.ActiveProviderSessionId) == nil {
		t.Fatal("irreversible cleanup ran before grace period expired")
	}

	// Undo before the 1s grace expires.
	unarchRes, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{
		SessionId: undoID, HasArchived: true, Archived: false,
	})
	if err != nil {
		t.Fatalf("undo archive: %v", err)
	}
	if unarchRes.GetSession().GetStatus() != idle || unarchRes.GetSession().GetWorkspaceStrategy().GetArchiveCleanupPending() {
		t.Fatalf("expected idle without pending cleanup, got %+v", unarchRes.GetSession())
	}
	// Sleep past the original 1s grace window and verify nothing was destroyed.
	time.Sleep(1200 * time.Millisecond)
	if b, err := os.ReadFile(cacheFile); err != nil || string(b) != "cached" {
		t.Fatalf("node_modules cache destroyed despite undo: %q %v", b, err)
	}
	if b, err := os.ReadFile(tmpMarker); err != nil || string(b) != "tmp-alive" {
		t.Fatalf("TMPDIR file destroyed despite undo: %q %v", b, err)
	}
	if h.e.Supervisor().Get(undoSess.ActiveProviderSessionId) == nil {
		t.Fatal("live provider session was detached despite undo")
	}

	// 2. After grace expires without undo: clean session releases worktree quietly;
	// session with local files keeps worktree and publishes WorktreeKept=true via Watch/Session.
	cleanID := h.start("say clean")
	h.wait(cleanID, idle)
	keptID := h.start("say kept")
	h.wait(keptID, idle)
	localEnv := filepath.Join(h.worktreePath(keptID), ".env.local")
	if err := os.WriteFile(localEnv, []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{cleanID, keptID} {
		if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{
			SessionId: id, HasArchived: true, Archived: true, ArchiveGraceSeconds: 1,
		}); err != nil {
			t.Fatalf("archive %s: %v", id, err)
		}
	}

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		pClean, _ := h.e.GetSession(ctx, h.kp(), cleanID)
		pKept, _ := h.e.GetSession(ctx, h.kp(), keptID)
		if !pClean.GetSession().GetWorkspaceStrategy().GetArchiveCleanupPending() &&
			!pKept.GetSession().GetWorkspaceStrategy().GetArchiveCleanupPending() {
			break
		}
	}
	if h.worktreeExists(cleanID) {
		t.Fatal("clean worktree was not released after grace period")
	}
	pClean, _ := h.e.GetSession(ctx, h.kp(), cleanID)
	if pClean.GetSession().GetWorkspaceStrategy().GetWorktreeKept() {
		t.Fatal("clean session unexpectedly marked WorktreeKept")
	}
	if !h.worktreeExists(keptID) {
		t.Fatal("worktree with .env.local was deleted instead of kept")
	}
	pKept, _ := h.e.GetSession(ctx, h.kp(), keptID)
	if !pKept.GetSession().GetWorkspaceStrategy().GetWorktreeKept() {
		t.Fatalf("expected WorktreeKept=true on kept session, got %+v", pKept.GetSession().GetWorkspaceStrategy())
	}

	// Releasing the kept worktree once cleaned clears WorktreeKept.
	if err := os.Remove(localEnv); err != nil {
		t.Fatal(err)
	}
	if rel := h.n.ReleaseWorkspaces(ctx, []string{keptID}); len(rel.Results) != 1 || !rel.Results[0].Released {
		t.Fatalf("expected manual release to succeed, got %+v", rel)
	}
	pKeptAfter, _ := h.e.GetSession(ctx, h.kp(), keptID)
	if pKeptAfter.GetSession().GetWorkspaceStrategy().GetWorktreeKept() {
		t.Fatal("expected WorktreeKept cleared after manual ReleaseWorkspaces")
	}
}
