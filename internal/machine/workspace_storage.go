package machine

import (
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
)

// A machine's storage (docs/settings-environments.md §5.7): the checkouts it
// made for sessions, their size, and whether each can go now. Releasing one
// removes the directory; the session's branch stays, and its next message
// checks it out again. In-place projects and a project's shared Git
// database belong to no session and are never listed.

const (
	// autoReleaseAfter is how long a session stays idle before its checkout
	// is released by itself, when the owner turned that on.
	autoReleaseAfter = 7 * 24 * time.Hour
	// autoRetryAfter is how long a checkout auto-release kept waits before
	// it is tried again.
	autoRetryAfter = 24 * time.Hour
	// scanWorkers is how many checkouts are measured at once.
	scanWorkers = 4
	// kAutoRelease keeps the owner's choice ("1" or "0").
	kAutoRelease = "workspace_auto_release"
)

// regenerable are the directory names whose ignored contents a package
// manager or build makes again, so they never keep a checkout. Anything
// else Git ignores (.env, keys, a hand-made dist) does: `git worktree
// remove` deletes ignored files without asking, so this is their only guard.
var regenerable = map[string]bool{
	"node_modules": true, ".pnpm-store": true, "bower_components": true,
	".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".tox": true,
	".gradle": true, ".next": true, ".nuxt": true, ".svelte-kit": true, ".turbo": true, ".parcel-cache": true, ".angular": true, ".expo": true,
	"DerivedData": true, ".build": true, "Pods": true, ".dart_tool": true,
	".cache": true, "coverage": true,
}

// regenerableFile: an ignored file of these is made again too.
func regenerableFile(name string) bool {
	return name == ".DS_Store" || strings.HasSuffix(name, ".pyc")
}

// regenerablePath: every part of an ignored path Git reports ("a/node_modules/")
// lies under a regenerable directory, or it is a regenerable file.
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

// keptError says why a checkout stays.
type keptError struct {
	block machinev1.WorkspaceBlock
	path  string
	msg   string
}

func (e *keptError) Error() string { return "workspace kept: " + e.msg }

func kept(code connect.Code, block machinev1.WorkspaceBlock, path, format string, a ...any) error {
	return connect.NewError(code, &keptError{block: block, path: path, msg: fmt.Sprintf(format, a...)})
}

// workspaceCache is the last scan, so a page can show a total at once.
type workspaceCache struct {
	mu   sync.Mutex
	rows []*machinev1.Workspace
	at   time.Time
	// autoKept are the sessions auto-release last left alone, and when.
	autoKept    map[string]time.Time
	autoLast    time.Time
	autoRunning bool
}

// autoDue claims the next auto-release run: none is running, and the last
// started at least every ago (default an hour).
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

// ListWorkspaces lists the sessions' checkouts, largest first: the last scan
// when cached and there is one, otherwise a new one.
func (n *Machine) ListWorkspaces(ctx context.Context, cached bool) (*machinev1.ListWorkspacesResponse, error) {
	s := n.sessions
	c := &s.wsCache
	auto := n.AutoRelease()
	c.mu.Lock()
	if cached && !c.at.IsZero() {
		res := &machinev1.ListWorkspacesResponse{Workspaces: slices.Clone(c.rows), ScannedAt: timestamppb.New(c.at), AutoRelease: auto}
		c.mu.Unlock()
		return res, nil
	}
	c.mu.Unlock()
	rows, err := s.scanWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	c.mu.Lock()
	c.rows, c.at = rows, now
	c.mu.Unlock()
	return &machinev1.ListWorkspacesResponse{Workspaces: slices.Clone(rows), ScannedAt: timestamppb.New(now), AutoRelease: auto}, nil
}

// scanWorkspaces measures every checkout recorded for this machine and
// says whether each can be released.
func (s *Sessions) scanWorkspaces(ctx context.Context) ([]*machinev1.Workspace, error) {
	rows, err := s.st.Sessions("worktree != '' AND workspace != ?", int(identityv1.Workspace_WORKSPACE_IN_PLACE))
	if err != nil {
		return nil, err
	}
	repos := map[string]string{}
	var todo []*sessionRow
	for _, r := range rows {
		path := s.ws.Path(r.ID)
		if r.Worktree != path || r.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
			continue
		}
		if st, err := os.Lstat(path); err != nil || !st.IsDir() {
			continue
		}
		if _, ok := repos[r.ProjectID]; !ok {
			if p, err := s.st.Project(r.ProjectID); err == nil && p != nil {
				repos[r.ProjectID] = p.Path
			} else {
				repos[r.ProjectID] = ""
			}
		}
		todo = append(todo, r)
	}
	out := make([]*machinev1.Workspace, len(todo))
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(scanWorkers, len(todo)) {
		wg.Go(func() {
			for i := range work {
				out[i] = s.inspectWorkspace(ctx, todo[i], repos[todo[i].ProjectID])
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

// inspectWorkspace is one row: its size, and whether releasing it now
// would lose anything (nil: released meanwhile).
func (s *Sessions) inspectWorkspace(ctx context.Context, r *sessionRow, repo string) *machinev1.Workspace {
	path := s.ws.Path(r.ID)
	status, busy := r.Status, false
	s.mu.Lock()
	l := s.live[r.ID]
	s.mu.Unlock()
	if l != nil {
		l.mu.Lock()
		status, busy = l.row.Status, len(l.asked) > 0 || l.releasing != nil
		l.mu.Unlock()
	}
	w := &machinev1.Workspace{SessionId: r.ID, ProjectId: r.ProjectID, Title: r.Title, Path: path, Status: status, LastActive: timestamppb.New(r.UpdatedAt)}
	size, err := workspaceBytes(ctx, path)
	if err != nil {
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			return nil // released while scanning
		}
		w.SizeError = err.Error()
	}
	w.SizeBytes = size
	switch {
	case busy || status != sessionv1.SessionStatus_SESSION_STATUS_IDLE:
		w.Block = machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY
	case repo == "" || r.Branch != "hermesh/"+r.ID:
		w.Block = machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED
	default:
		if err := checkCheckout(ctx, repo, path, r.Branch); err != nil {
			w.Block, w.BlockPath = blockOf(err)
		}
	}
	return w
}

// blockOf is why an error kept a checkout.
func blockOf(err error) (machinev1.WorkspaceBlock, string) {
	var k *keptError
	if errors.As(err, &k) {
		return k.block, k.path
	}
	return machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, ""
}

// ReleaseWorkspaces releases each session's checkout it can; the others
// stay and say why.
func (n *Machine) ReleaseWorkspaces(ctx context.Context, ids []string) *machinev1.ReleaseWorkspacesResponse {
	res := &machinev1.ReleaseWorkspacesResponse{}
	for _, id := range ids {
		r := &machinev1.WorkspaceRelease{SessionId: id}
		if err := n.sessions.releaseWorkspace(ctx, id); err != nil {
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

// releaseWorkspace releases one idle session's checkout, as the owner.
func (s *Sessions) releaseWorkspace(ctx context.Context, id string) error {
	session, _, err := s.find(&Principal{Kind: PrincipalOwner}, id, false)
	if err != nil {
		return err
	}
	if session == nil {
		return cerr(connect.CodeFailedPrecondition, "session is archived")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	r := session.row
	if r.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || len(session.asked) > 0 {
		return kept(connect.CodeFailedPrecondition, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY, "", "wait for the session to be idle before releasing its workspace")
	}
	if r.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE || s.ws.none {
		return cerr(connect.CodeFailedPrecondition, "only a managed Git worktree can be released")
	}
	if err := s.releaseCheckoutLocked(ctx, session); err != nil {
		return err
	}
	s.forgetWorkspace(id)
	s.log.Info("workspace released", "session", id)
	return nil
}

// forgetWorkspace drops a released checkout from the last scan.
func (s *Sessions) forgetWorkspace(id string) {
	c := &s.wsCache
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

// autoRelease releases the checkouts of sessions idle for a week, when the
// owner turned that on: at most once per autoReleaseEvery, and a checkout it
// had to keep (local changes) is not tried again for a day.
func (s *Sessions) autoRelease(ctx context.Context, now time.Time) {
	c := &s.wsCache
	defer func() {
		c.mu.Lock()
		c.autoRunning = false
		c.mu.Unlock()
	}()
	if s.autoReleaseOn == nil || !s.autoReleaseOn() || s.ws.none {
		return
	}
	rows, err := s.st.Sessions("worktree != '' AND workspace != ? AND status = ? AND updated_at < ?",
		int(identityv1.Workspace_WORKSPACE_IN_PLACE), int(sessionv1.SessionStatus_SESSION_STATUS_IDLE), now.Add(-autoReleaseAfter).Unix())
	if err != nil {
		s.log.Warn("auto-release: listing sessions", "err", err)
		return
	}
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if r.Worktree != s.ws.Path(r.ID) || !s.ws.Exists(r.ID) {
			continue
		}
		c.mu.Lock()
		last, tried := c.autoKept[r.ID]
		c.mu.Unlock()
		if tried && now.Sub(last) < autoRetryAfter {
			continue
		}
		if err := s.releaseWorkspace(ctx, r.ID); err != nil {
			c.mu.Lock()
			c.autoKept[r.ID] = now
			c.mu.Unlock()
			s.log.Info("auto-release kept a workspace", "session", r.ID, "err", err)
			continue
		}
		c.mu.Lock()
		delete(c.autoKept, r.ID)
		c.mu.Unlock()
	}
}

// Called with l.mu held; release it during process shutdown and disk I/O
// so event delivery, including for other sessions, can keep draining.
func (s *Sessions) releaseCheckoutLocked(ctx context.Context, l *live) error {
	if l.releasing != nil {
		return kept(connect.CodeUnavailable, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_BUSY, "", "the workspace is already being released")
	}
	r := l.row
	path := s.ws.Path(r.ID)
	branchName, projectID := r.Branch, r.ProjectID
	st, err := os.Lstat(path)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return err
	}
	if !missing && (!st.IsDir() || path != r.Worktree || branchName != "hermesh/"+r.ID) {
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", "cannot verify this session's checkout")
	}
	l.releasing = make(chan struct{})
	ps, idleClose := l.sess, l.idleClose
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		close(l.releasing)
		l.releasing = nil
	}()

	if idleClose != nil {
		select {
		case <-idleClose:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var repo string
	if !missing {
		proj, err := s.st.Project(projectID)
		if err != nil || proj == nil {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", "cannot open project %q", projectID)
		}
		repo = proj.Path
		if err := checkCheckout(ctx, repo, path, branchName); err != nil {
			return err // leave even the idle agent running on a refused release
		}
	}
	if ps != nil {
		l.mu.Lock()
		l.sess = nil // late provider events cannot start or finish another turn
		l.mu.Unlock()
		if err := ps.Close(); err != nil {
			l.mu.Lock()
			l.sess = ps
			l.mu.Unlock()
			return fmt.Errorf("stopping idle agent: %w", err)
		}
	}
	l.mu.Lock()
	persist := r.Pgid != 0 || ps != nil
	r.Pgid = 0
	var stopped *sessionRow
	if persist {
		stopped = snapshotRow(r)
	}
	l.mu.Unlock()
	if stopped != nil {
		if err := s.w.sync(writeOp{row: stopped}); err != nil {
			return err
		}
	}
	if missing {
		return nil
	}
	if ps != nil {
		// The provider may have flushed files while shutting down.
		if err := checkCheckout(ctx, repo, path, branchName); err != nil {
			return err
		}
	}
	// Git rechecks registration, locks, submodules and cleanliness on removal.
	if err := ctx.Err(); err != nil {
		return err
	}
	// Once deletion starts, closing the settings window must not interrupt it
	// and leave a partially populated checkout for the next turn.
	_, err = gitUnlimited(context.WithoutCancel(ctx), repo, "worktree", "remove", "--", filepath.ToSlash(path))
	return err
}

// checkCheckout is nil when the checkout at path is this session's, on its
// branch, and holds nothing its branch could not give back.
func checkCheckout(ctx context.Context, repo, path, branch string) error {
	unverified := func(format string, a ...any) error {
		return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNVERIFIED, "", format, a...)
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
	// Compare both the shared repository directory and the commit. A stale Git
	// registration can refer to a path now occupied by another checkout.
	saved, err := git(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir", "--verify", ref+"^{commit}")
	if err != nil {
		return unverified("cannot restore the session branch: %v", err)
	}
	head, err = git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir", "--verify", "HEAD^{commit}")
	if err != nil || head != saved {
		return unverified("the checkout does not match the recorded project and branch")
	}
	// git status normally hides ignored files, and the index can tell Git to
	// skip checking individual tracked files. Neither is safe to discard.
	flags, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(flags, "\x00") {
		if len(entry) > 2 && (entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z') {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, entry[2:], "Git skips checking %q", entry[2:])
		}
	}
	status, err := git(ctx, path, "-c", "core.quotepath=false", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain=v1", "--ignored=matching", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) < 4 {
			continue
		}
		file := line[3:]
		if line[:2] != "!!" {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_CHANGES, file, "save or move local files before releasing it (%s)", line)
		}
		if !regenerablePath(file) {
			return kept(connect.CodeAborted, machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES, file, "save or move local files before releasing it (%s)", line)
		}
	}
	return nil
}

// adminMethods are the MachineAdminService methods this hermeshd answers:
// all of them, as adminService implements the generated interface.
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

// ListWorkspaces lists this device's own machine's checkouts.
func (l *Local) ListWorkspaces(ctx context.Context, cached bool) (*machinev1.ListWorkspacesResponse, error) {
	return l.n.ListWorkspaces(ctx, cached)
}

// ReleaseWorkspaces releases checkouts on this device's own machine.
func (l *Local) ReleaseWorkspaces(ctx context.Context, ids []string) *machinev1.ReleaseWorkspacesResponse {
	return l.n.ReleaseWorkspaces(ctx, ids)
}
