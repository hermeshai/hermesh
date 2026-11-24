package machine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
)

// A machine's storage (docs/settings-environments.md §5.7): the checkouts it
// made for sessions, their size, and whether each can go now. Releasing one
// removes the directory; the session's branch and checkpoint refs stay, and its
// next message checks it out again and restores the latest checkpoint tree.
// In-place projects and a project's shared Git database belong to no session
// and are never listed.

const (
	autoReleaseAfter = 7 * 24 * time.Hour
	autoRetryAfter   = 24 * time.Hour
	scanWorkers      = 4
	kAutoRelease     = "workspace_auto_release"
)

var regenerable = map[string]bool{
	"node_modules": true, ".pnpm-store": true, "bower_components": true,
	".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".tox": true,
	".gradle": true, ".next": true, ".nuxt": true, ".svelte-kit": true, ".turbo": true, ".parcel-cache": true, ".angular": true, ".expo": true,
	"DerivedData": true, ".build": true, "Pods": true, ".dart_tool": true,
	".cache": true, "coverage": true,
}

func regenerableFile(name string) bool {
	return name == ".DS_Store" || strings.HasSuffix(name, ".pyc")
}

func regenerablePath(path string) bool {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	for _, p := range parts[:len(parts)-1] {
		if regenerable[p] {
			return true
		}
	}
	last := parts[len(parts)-1]
	return regenerable[last] || (!strings.HasSuffix(path, "/") && regenerableFile(last))
}

type keptError struct {
	block machinev1.WorkspaceBlock
	path  string
	msg   string
}

func (e *keptError) Error() string { return "workspace kept: " + e.msg }

func kept(code connect.Code, block machinev1.WorkspaceBlock, path, format string, a ...any) error {
	return connect.NewError(code, &keptError{block: block, path: path, msg: fmt.Sprintf(format, a...)})
}

type workspaceCache struct {
	mu          sync.Mutex
	rows        []*machinev1.Workspace
	at          time.Time
	autoKept    map[string]time.Time
	autoLast    time.Time
	autoRunning bool
	releasing   map[string]bool
}

func (c *workspaceCache) autoDue(now time.Time, every time.Duration) bool {
	if every == 0 {
		every = time.Hour
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoRunning || now.Sub(c.autoLast) < every {
		return false
	}
	c.autoRunning, c.autoLast = true, now
	if c.autoKept == nil {
		c.autoKept = map[string]time.Time{}
	}
	return true
}

func (n *Machine) worktreeRoot() string {
	return filepath.Join(n.cfg.DataDir, "worktrees")
}

func (n *Machine) expectedWorktreePath(sessionID string) string {
	return filepath.Join(n.worktreeRoot(), sessionID)
}

func (n *Machine) repoPathForProject(projectID string) string {
	if p, err := n.st.Project(projectID); err == nil && p != nil {
		return p.Path
	}
	if n.sessions != nil {
		if ws, err := n.sessions.Store().Workspace(projectID); err == nil && ws.Path != "" {
			return ws.Path
		}
	}
	return ""
}

// ListWorkspaces lists the sessions' checkouts, largest first: the last scan
// when cached and there is one, otherwise a new one.
func (n *Machine) ListWorkspaces(ctx context.Context, cached bool) (*machinev1.ListWorkspacesResponse, error) {
	c := &n.wsCache
	auto := n.AutoRelease()
	c.mu.Lock()
	if cached && !c.at.IsZero() {
		res := &machinev1.ListWorkspacesResponse{Workspaces: slices.Clone(c.rows), ScannedAt: timestamppb.New(c.at), AutoRelease: auto}
		c.mu.Unlock()
		return res, nil
	}
	c.mu.Unlock()
	rows, err := n.scanWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	c.mu.Lock()
	c.rows, c.at = rows, now
	c.mu.Unlock()
	return &machinev1.ListWorkspacesResponse{Workspaces: slices.Clone(rows), ScannedAt: timestamppb.New(now), AutoRelease: auto}, nil
}

func sessionUsesWorktree(s *sessionv1.Session) (string, bool) {
	if s == nil {
		return "", false
	}
	mode := cmp.Or(s.Workspace, s.GetWorkspaceStrategy().GetMode())
	if mode == identityv1.Workspace_WORKSPACE_IN_PLACE {
		return "", false
	}
	wt := s.GetWorkspaceStrategy().GetWorktreePath()
	if wt == "" {
		return "", false
	}
	return wt, true
}

func (n *Machine) scanWorkspaces(ctx context.Context) ([]*machinev1.Workspace, error) {
	if n.sessions == nil {
		return nil, nil
	}
	rows, err := n.sessions.ListAllSessions(false)
	if err != nil {
		return nil, err
	}
	repos := map[string]string{}
	var todo []*sessionv1.Session
	for _, s := range rows {
		if s.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
			continue
		}
		path, ok := sessionUsesWorktree(s)
		if !ok || path != n.expectedWorktreePath(s.SessionId) {
			continue
		}
		if st, err := os.Lstat(path); err != nil || !st.IsDir() {
			continue
		}
		if _, seen := repos[s.ProjectId]; !seen {
			repos[s.ProjectId] = n.repoPathForProject(s.ProjectId)
		}
		todo = append(todo, s)
	}
	out := make([]*machinev1.Workspace, len(todo))
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(scanWorkers, len(todo)) {
		wg.Go(func() {
			for i := range work {
				out[i] = n.inspectWorkspace(ctx, todo[i], repos[todo[i].ProjectId])
			}
		})
	}
	for i := range todo {
		work <- i
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out = slices.DeleteFunc(out, func(w *machinev1.Workspace) bool { return w == nil })
	slices.SortStableFunc(out, func(a, b *machinev1.Workspace) int {
		if a.SizeBytes != b.SizeBytes {
			if a.SizeBytes > b.SizeBytes {
				return -1
			}
			return 1
		}
		return strings.Compare(a.SessionId, b.SessionId)
	})
	return out, nil
}

func (n *Machine) inspectWorkspace(ctx context.Context, sess *sessionv1.Session, repo string) *machinev1.Workspace {
	path, _ := sessionUsesWorktree(sess)
	if fresh, err := n.sessions.Store().Session(sess.SessionId); err == nil && fresh != nil {
		sess = fresh
	}
	n.wsCache.mu.Lock()
	releasing := n.wsCache.releasing[sess.SessionId]
	n.wsCache.mu.Unlock()
	busy := releasing || sess.ActiveRunId != "" || sess.PendingRequestId != "" || sess.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE
	w := &machinev1.Workspace{
		SessionId:  sess.SessionId,
		ProjectId:  sess.ProjectId,
		Title:      sess.Title,
		Path:       path,
		Status:     sess.Status,
		LastActive: sess.UpdatedAt,
	}
	size, err := workspaceBytes(ctx, path)
	if err != nil {
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			return nil
		}
		w.SizeError = err.Error()
	}
	w.SizeBytes = size
	branch := cmp.Or(sess.Branch, sess.GetWorkspaceStrategy().GetBranch())
	switch {
	case busy:
		w.Block = machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY
	case repo == "" || branch == "":
		w.Block = machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED
	default:
		if err := checkSessionCheckout(ctx, n.sessions, sess, repo, path); err != nil {
			w.Block, w.BlockPath = blockOf(err)
		}
	}
	return w
}

func blockOf(err error) (machinev1.WorkspaceBlock, string) {
	var k *keptError
	if errors.As(err, &k) {
		return k.block, k.path
	}
	return machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, ""
}

// checkReleaseSession verifies whether sess's worktree can be safely removed
// (used before archiving a session).
func (n *Machine) checkReleaseSession(ctx context.Context, sess *sessionv1.Session) error {
	path, ok := sessionUsesWorktree(sess)
	if !ok {
		return nil
	}
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.IsDir() || path != n.expectedWorktreePath(sess.SessionId) {
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", "cannot verify this session's checkout")
	}
	repo := n.repoPathForProject(sess.ProjectId)
	if repo == "" {
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", "cannot open project %q", sess.ProjectId)
	}
	return checkSessionCheckout(ctx, n.sessions, sess, repo, path)
}

// ReleaseWorkspaces releases each session's checkout it can; the others
// stay and say why.
func (n *Machine) ReleaseWorkspaces(ctx context.Context, ids []string) *machinev1.ReleaseWorkspacesResponse {
	res := &machinev1.ReleaseWorkspacesResponse{}
	for _, id := range ids {
		r := &machinev1.WorkspaceRelease{SessionId: id}
		if err := n.releaseWorkspace(ctx, id); err != nil {
			var k *keptError
			if errors.As(err, &k) {
				r.Block, r.BlockPath = k.block, k.path
			} else {
				r.Error = err.Error()
			}
		} else {
			r.Released = true
		}
		res.Results = append(res.Results, r)
	}
	return res
}

func (n *Machine) releaseWorkspace(ctx context.Context, id string) error {
	if n.sessions == nil {
		return cerr(connect.CodeNotFound, "session not found")
	}
	sess, err := n.sessions.Store().Session(id)
	if err != nil || sess == nil {
		return cerr(connect.CodeNotFound, "session %q not found", id)
	}
	if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		return cerr(connect.CodeFailedPrecondition, "session is archived")
	}
	if sess.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || sess.ActiveRunId != "" || sess.PendingRequestId != "" {
		return kept(connect.CodeFailedPrecondition, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY, "", "wait for the session to be idle before releasing its workspace")
	}
	path, ok := sessionUsesWorktree(sess)
	if !ok || n.cfg.WorkspaceMode == "none" {
		return cerr(connect.CodeFailedPrecondition, "only a managed Git worktree can be released")
	}

	n.wsCache.mu.Lock()
	if n.wsCache.releasing == nil {
		n.wsCache.releasing = map[string]bool{}
	}
	if n.wsCache.releasing[id] {
		n.wsCache.mu.Unlock()
		return kept(connect.CodeUnavailable, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY, "", "the workspace is already being released")
	}
	n.wsCache.releasing[id] = true
	n.wsCache.mu.Unlock()
	defer func() {
		n.wsCache.mu.Lock()
		delete(n.wsCache.releasing, id)
		n.wsCache.mu.Unlock()
	}()

	st, err := os.Lstat(path)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return err
	}
	branch := cmp.Or(sess.Branch, sess.GetWorkspaceStrategy().GetBranch())
	if !missing && (!st.IsDir() || path != n.expectedWorktreePath(id) || branch == "") {
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", "cannot verify this session's checkout")
	}

	var repo string
	if !missing {
		repo = n.repoPathForProject(sess.ProjectId)
		if repo == "" {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", "cannot open project %q", sess.ProjectId)
		}
		if err := checkSessionCheckout(ctx, n.sessions, sess, repo, path); err != nil {
			return err
		}
	}

	hadLiveProc := sess.ActiveProviderSessionId != "" && n.sessions.Supervisor().Get(sess.ActiveProviderSessionId) != nil
	if hadLiveProc {
		n.sessions.Supervisor().Detach(sess.ActiveProviderSessionId, false)
	}
	if missing {
		n.forgetWorkspace(id)
		return nil
	}
	if hadLiveProc {
		// Re-check in case the provider flushed files while shutting down.
		if err := checkSessionCheckout(ctx, n.sessions, sess, repo, path); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := gitUnlimited(context.WithoutCancel(ctx), repo, "worktree", "remove", "--force", "--", filepath.ToSlash(path)); err != nil {
		return err
	}
	n.forgetWorkspace(id)
	n.log.Info("workspace released", "session", id)
	return nil
}

func (n *Machine) forgetWorkspace(id string) {
	c := &n.wsCache
	c.mu.Lock()
	c.rows = slices.DeleteFunc(slices.Clone(c.rows), func(w *machinev1.Workspace) bool { return w.SessionId == id })
	c.mu.Unlock()
}

// AutoRelease is whether idle checkouts are released by themselves.
func (n *Machine) AutoRelease() bool {
	v, _ := n.st.Get(kAutoRelease)
	return string(v) == "1"
}

func (n *Machine) setAutoRelease(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return n.st.Set(kAutoRelease, []byte(v))
}

func (n *Machine) autoRelease(ctx context.Context, now time.Time) {
	c := &n.wsCache
	defer func() {
		c.mu.Lock()
		c.autoRunning = false
		c.mu.Unlock()
	}()
	if !n.AutoRelease() || n.cfg.WorkspaceMode == "none" || n.sessions == nil {
		return
	}
	rows, err := n.sessions.ListAllSessions(false)
	if err != nil {
		n.log.Warn("auto-release: listing sessions", "err", err)
		return
	}
	cutoff := now.Add(-autoReleaseAfter)
	for _, s := range rows {
		if ctx.Err() != nil {
			return
		}
		if s.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || !s.GetUpdatedAt().AsTime().Before(cutoff) {
			continue
		}
		path, ok := sessionUsesWorktree(s)
		if !ok || path != n.expectedWorktreePath(s.SessionId) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		c.mu.Lock()
		last, tried := c.autoKept[s.SessionId]
		c.mu.Unlock()
		if tried && now.Sub(last) < autoRetryAfter {
			continue
		}
		if err := n.releaseWorkspace(ctx, s.SessionId); err != nil {
			c.mu.Lock()
			if c.autoKept == nil {
				c.autoKept = map[string]time.Time{}
			}
			c.autoKept[s.SessionId] = now
			c.mu.Unlock()
			n.log.Info("auto-release kept a workspace", "session", s.SessionId, "err", err)
			continue
		}
		c.mu.Lock()
		delete(c.autoKept, s.SessionId)
		c.mu.Unlock()
	}
}

// checkSessionCheckout returns nil when the checkout at path belongs to sess, is
// on its branch, and holds no unsaved changes beyond its latest checkpoint tree
// (or branch HEAD when no checkpoint exists) and no non-regenerable ignored files.
func checkSessionCheckout(ctx context.Context, eng *kernel.Engine, sess *sessionv1.Session, repo, path string) error {
	unverified := func(format string, a ...any) error {
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", format, a...)
	}
	branch := cmp.Or(sess.Branch, sess.GetWorkspaceStrategy().GetBranch())
	if branch == "" {
		return unverified("missing session branch")
	}
	marker, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !marker.Mode().IsRegular() {
		return unverified("expected a linked worktree")
	}
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	topInfo, err := os.Stat(strings.TrimSpace(top))
	if err != nil || !os.SameFile(st, topInfo) {
		return unverified("Git selected a different working directory")
	}
	ref := "refs/heads/" + branch
	head, err := git(ctx, path, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(head) != ref {
		return unverified("checkout is not on its session branch %s", branch)
	}
	saved, err := git(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir", "--verify", ref+"^{commit}")
	if err != nil {
		return unverified("cannot restore the session branch: %v", err)
	}
	head, err = git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir", "--verify", "HEAD^{commit}")
	if err != nil || head != saved {
		return unverified("the checkout does not match the recorded project and branch")
	}
	flags, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(flags, "\x00") {
		if len(entry) > 2 && (entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z') {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, entry[2:], "Git skips checking %q", entry[2:])
		}
	}
	staged, err := git(ctx, path, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return err
	}
	for f := range strings.SplitSeq(staged, "\x00") {
		if f != "" {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, f, "save or move staged changes before releasing it (%s)", f)
		}
	}

	// Verify working tree (tracked + untracked non-ignored files) matches latest checkpoint tree (or HEAD^{tree}).
	var expectedTree string
	if eng != nil {
		if ckpts, cErr := eng.Store().Checkpoints(sess.SessionId); cErr == nil && len(ckpts) > 0 {
			expectedTree = ckpts[len(ckpts)-1].GitTreeOid
		}
	}
	if expectedTree == "" {
		ht, err := git(ctx, path, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return err
		}
		expectedTree = strings.TrimSpace(ht)
	}
	var curTree string
	if eng != nil {
		curTree, _, err = eng.VCS().SnapshotWorkingTree(ctx, path)
		if err != nil {
			return err
		}
	}
	if curTree != "" && curTree != expectedTree {
		changedPath := firstDiffTreePath(ctx, path, expectedTree, curTree)
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, changedPath, "save or move local changes before releasing it (%s)", changedPath)
	}

	// Check ignored files that are not regenerable build caches.
	ignoredStatus, err := git(ctx, path, "-c", "core.quotepath=false", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain=v1", "--ignored=matching", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(ignoredStatus, "\n") {
		if len(line) < 4 || line[:2] != "!!" {
			continue
		}
		file := line[3:]
		if !regenerablePath(file) {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES, file, "save or move local files before releasing it (%s)", line)
		}
	}
	return nil
}

func firstDiffTreePath(ctx context.Context, path, fromTree, toTree string) string {
	out, err := git(ctx, path, "diff-tree", "-r", "--name-only", "-z", fromTree, toTree)
	if err == nil {
		for f := range strings.SplitSeq(out, "\x00") {
			if f != "" {
				return f
			}
		}
	}
	return "."
}

var adminMethods = sync.OnceValue(func() []string {
	ms := machinev1.File_hermesh_machine_v1_machine_proto.Services().ByName("MachineAdminService").Methods()
	out := make([]string, ms.Len())
	for i := range ms.Len() {
		out[i] = string(ms.Get(i).Name())
	}
	return out
})

func (s *adminService) ListWorkspaces(ctx context.Context, req *machinev1.ListWorkspacesRequest) (*machinev1.ListWorkspacesResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	return s.n.ListWorkspaces(ctx, req.Cached)
}

func (s *adminService) ReleaseWorkspaces(ctx context.Context, req *machinev1.ReleaseWorkspacesRequest) (*machinev1.ReleaseWorkspacesResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	return s.n.ReleaseWorkspaces(ctx, req.SessionIds), nil
}

func (l *Local) ListWorkspaces(ctx context.Context, cached bool) (*machinev1.ListWorkspacesResponse, error) {
	return l.n.ListWorkspaces(ctx, cached)
}

func (l *Local) ReleaseWorkspaces(ctx context.Context, ids []string) *machinev1.ReleaseWorkspacesResponse {
	return l.n.ReleaseWorkspaces(ctx, ids)
}
