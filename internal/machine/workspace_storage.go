package machine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// ListLocalWorkspaces scans only checkouts recorded for this machine. In-place
// projects and the project's shared Git database do not belong to a session.
func (l *Local) ListLocalWorkspaces(ctx context.Context) (*localv1.ListLocalWorkspacesResponse, error) {
	s := l.n.sessions
	rows, err := s.st.Sessions("worktree != '' AND workspace != ?", int(identityv1.Workspace_WORKSPACE_IN_PLACE))
	if err != nil {
		return nil, err
	}
	res := &localv1.ListLocalWorkspacesResponse{}
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := s.ws.Path(r.ID)
		if r.Worktree != path {
			continue
		}
		row := &localv1.LocalWorkspace{SessionId: r.ID, ProjectId: r.ProjectID, Title: r.Title, Path: path, Status: r.Status}
		st, err := os.Lstat(path)
		if os.IsNotExist(err) || err == nil && !st.IsDir() {
			continue
		}
		if err == nil {
			row.SizeBytes, err = workspaceBytes(ctx, path)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
				continue // released while scanning
			}
			row.SizeError = err.Error()
		}
		res.Workspaces = append(res.Workspaces, row)
	}
	return res, nil
}

func (l *Local) ReleaseLocalWorkspace(ctx context.Context, id string) error {
	s := l.n.sessions
	session, _, err := s.find(l.p, id, false)
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
		return cerr(connect.CodeFailedPrecondition, "wait for the session to be idle before releasing its workspace")
	}
	if r.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE || s.ws.none {
		return cerr(connect.CodeFailedPrecondition, "only a managed Git worktree can be released")
	}
	return s.releaseCheckoutLocked(ctx, session)
}

// Called with l.mu held; release it during process shutdown and disk I/O
// so event delivery, including for other sessions, can keep draining.
func (s *Sessions) releaseCheckoutLocked(ctx context.Context, l *live) error {
	if l.releasing != nil {
		return cerr(connect.CodeUnavailable, "the workspace is already being released")
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
		return cerr(connect.CodeAborted, "workspace kept: cannot verify this session's checkout")
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
			return cerr(connect.CodeAborted, "workspace kept: cannot open project %q", projectID)
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

func checkCheckout(ctx context.Context, repo, path, branch string) error {
	marker, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !marker.Mode().IsRegular() {
		return cerr(connect.CodeAborted, "workspace kept: expected a linked worktree")
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
		return cerr(connect.CodeAborted, "workspace kept: Git selected a different working directory")
	}
	ref := "refs/heads/" + branch
	head, err := git(ctx, path, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(head) != ref {
		return cerr(connect.CodeAborted, "workspace kept: checkout is not on its session branch %s", branch)
	}
	// Compare both the shared repository directory and the commit. A stale Git
	// registration can refer to a path now occupied by another checkout.
	saved, err := git(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir", "--verify", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("workspace kept: cannot restore the session branch: %w", err)
	}
	head, err = git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir", "--verify", "HEAD^{commit}")
	if err != nil || head != saved {
		return cerr(connect.CodeAborted, "workspace kept: the checkout does not match the recorded project and branch")
	}
	// git status normally hides ignored files, and the index can tell Git to
	// skip checking individual tracked files. Neither is safe to discard.
	flags, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(flags, "\x00") {
		if len(entry) > 2 && (entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z') {
			return cerr(connect.CodeAborted, "workspace kept: Git skips checking %q", entry[2:])
		}
	}
	status, err := git(ctx, path, "-c", "core.quotepath=false", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain=v1", "--ignored=matching", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		first, _, _ := strings.Cut(status, "\n")
		return cerr(connect.CodeAborted, "workspace kept: save or move local files before releasing it (%s)", first)
	}
	return nil
}
