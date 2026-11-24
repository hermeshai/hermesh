// Package vcs provides Git worktree isolation, shadow-index tree checkpoints
// (without polluting the user's index or commit log), unified diffs, and
// checkpoint file rollback for Hermesh sessions.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

type Manager struct {
	worktreesRoot string
	mu            sync.Mutex
	repoLocks     map[string]*sync.Mutex
}

func NewManager(worktreesRoot string) *Manager {
	return &Manager{
		worktreesRoot: worktreesRoot,
		repoLocks:     map[string]*sync.Mutex{},
	}
}

func (m *Manager) WorktreesRoot() string { return m.worktreesRoot }

func (m *Manager) lockRepo(repoPath string) func() {
	m.mu.Lock()
	lk := m.repoLocks[repoPath]
	if lk == nil {
		lk = &sync.Mutex{}
		m.repoLocks[repoPath] = lk
	}
	m.mu.Unlock()
	lk.Lock()
	return lk.Unlock
}

func IsGitRepo(ctx context.Context, dir string) bool {
	out, err := runGit(ctx, dir, nil, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func CurrentBranch(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, nil, "symbolic-ref", "--short", "-q", "HEAD")
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	out, err = runGit(ctx, dir, nil, "rev-parse", "--short", "HEAD")
	if err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

func (m *Manager) SwitchBranch(ctx context.Context, dir, branch string) error {
	unlock := m.lockRepo(dir)
	defer unlock()
	if CurrentBranch(ctx, dir) == branch {
		return nil
	}
	out, err := runGit(ctx, dir, nil, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("read the status of %s: %w", dir, err)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("%s has uncommitted changes; commit or stash them before switching to %s", dir, branch)
	}
	if _, err := runGit(ctx, dir, nil, "switch", branch); err != nil {
		return fmt.Errorf("switch %s to %s: %w", dir, branch, err)
	}
	return nil
}

func FetchBase(ctx context.Context, dir, base string) (string, error) {
	if base == "" || base == "HEAD" {
		base = CurrentBranch(ctx, dir)
	}
	if _, err := runGit(ctx, dir, nil, "fetch", "origin", base); err != nil {
		return "", fmt.Errorf("fetch %s from origin: %w", base, err)
	}
	return "origin/" + base, nil
}

// PrepareWorktree creates or restores an isolated git worktree for sessionID.
func (m *Manager) PrepareWorktree(ctx context.Context, repoPath, sessionID, baseRef, branch string) (worktreePath, resolvedBranch string, err error) {
	if !IsGitRepo(ctx, repoPath) {
		return "", "", fmt.Errorf("project directory %q is not a git repository", repoPath)
	}
	unlock := m.lockRepo(repoPath)
	defer unlock()

	if err := os.MkdirAll(m.worktreesRoot, 0o755); err != nil {
		return "", "", err
	}
	wtPath := filepath.Join(m.worktreesRoot, sessionID)
	if branch == "" {
		branch = "hermesh/" + sessionID
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}

	if st, statErr := os.Stat(wtPath); statErr == nil && st.IsDir() && IsGitRepo(ctx, wtPath) {
		return wtPath, branch, nil
	}

	_, _ = runGit(ctx, repoPath, nil, "worktree", "prune")
	_ = os.RemoveAll(wtPath)

	branchExists := false
	if _, bErr := runGit(ctx, repoPath, nil, "rev-parse", "--verify", "refs/heads/"+branch); bErr == nil {
		branchExists = true
	}
	if branchExists {
		if _, err := runGit(ctx, repoPath, nil, "worktree", "add", wtPath, branch); err != nil {
			return "", "", fmt.Errorf("restore worktree on branch %s: %w", branch, err)
		}
		return wtPath, branch, nil
	}
	if _, err := runGit(ctx, repoPath, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", baseRef+"^{commit}"); err != nil {
		return "", "", &BaseRefError{Ref: baseRef}
	}
	if _, err := runGit(ctx, repoPath, nil, "worktree", "add", "-b", branch, wtPath, baseRef); err != nil {
		return "", "", fmt.Errorf("create worktree from %s: %w", baseRef, err)
	}
	return wtPath, branch, nil
}

type BaseRefError struct {
	Ref     string
	Looked  bool
	Remotes []string
	Fetch   error
}

func (e *BaseRefError) Error() string {
	return fmt.Sprintf("base ref %q does not resolve to a commit in the local repository%s", e.Ref, e.Upstream())
}

func (e *BaseRefError) Upstream() string {
	switch {
	case !e.Looked:
		return ""
	case e.Fetch != nil:
		return ", and fetching it failed: " + strings.ReplaceAll(e.Fetch.Error(), "\n", "; ")
	case len(e.Remotes) == 0:
		return ", which has no remote to fetch it from"
	case len(e.Remotes) == 1:
		return " or on its remote " + e.Remotes[0]
	}
	return " or on its remotes " + strings.Join(e.Remotes, ", ")
}

type upstreamRef struct{ src, dst string }

var commitIDRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func upstreamRefs(baseRef, r string, remotes []string) []upstreamRef {
	switch {
	case commitIDRE.MatchString(baseRef):
		return []upstreamRef{{baseRef, ""}}
	case strings.HasPrefix(baseRef, "refs/tags/"):
		return []upstreamRef{{baseRef, baseRef}}
	case strings.HasPrefix(baseRef, "refs/heads/"):
		return []upstreamRef{{baseRef, "refs/remotes/" + r + "/" + strings.TrimPrefix(baseRef, "refs/heads/")}}
	case strings.HasPrefix(baseRef, "refs/"):
		return nil
	}
	for _, other := range remotes {
		if b, ok := strings.CutPrefix(baseRef, other+"/"); ok {
			if other != r {
				return nil
			}
			return []upstreamRef{{"refs/heads/" + b, "refs/remotes/" + r + "/" + b}}
		}
	}
	return []upstreamRef{{"refs/tags/" + baseRef, "refs/tags/" + baseRef}, {"refs/heads/" + baseRef, "refs/remotes/" + r + "/" + baseRef}}
}

func validRefs(ctx context.Context, repo string, names ...string) bool {
	for _, n := range names {
		if _, err := runGit(ctx, repo, nil, "check-ref-format", n); err != nil {
			return false
		}
	}
	return true
}

func isCommit(ctx context.Context, repo, ref string) bool {
	_, err := runGit(ctx, repo, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	return err == nil
}

// FetchRef fetches a missing base ref alone from the repository's remotes (origin first).
func (m *Manager) FetchRef(ctx context.Context, repo, baseRef string) (local, remote string, err error) {
	if strings.HasPrefix(baseRef, "-") || strings.TrimSpace(baseRef) == "" {
		return "", "", &BaseRefError{Ref: baseRef}
	}
	unlock := m.lockRepo("fetch:" + repo)
	defer unlock()
	if isCommit(ctx, repo, baseRef) {
		return baseRef, "", nil
	}
	if _, err := runGit(ctx, repo, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", baseRef+"^{object}"); err == nil {
		return "", "", &BaseRefError{Ref: baseRef}
	}
	out, err := runGit(ctx, repo, nil, "remote")
	if err != nil {
		return "", "", err
	}
	remotes := strings.Fields(out)
	if i := slices.Index(remotes, "origin"); i > 0 {
		remotes = append([]string{"origin"}, slices.Delete(remotes, i, i+1)...)
	}
	missing := &BaseRefError{Ref: baseRef, Looked: true, Remotes: remotes}
	var failed []error
	gitRemote := func(args ...string) (string, error) {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return runGit(rctx, repo, shellenv.Environ(), args...)
	}
	fetch := func(r, refspec string) bool {
		_, fErr := gitRemote("fetch", "--quiet", "--no-tags", "--no-recurse-submodules", r, refspec)
		if fErr != nil {
			failed = append(failed, fmt.Errorf("%s: %w", r, fErr))
		}
		return fErr == nil
	}
	for _, r := range remotes {
		wants := slices.DeleteFunc(upstreamRefs(baseRef, r, remotes), func(u upstreamRef) bool {
			return u.dst != "" && !validRefs(ctx, repo, u.src, u.dst)
		})
		switch {
		case len(wants) == 0:
			continue
		case wants[0].dst == "":
			if fetch(r, baseRef) && isCommit(ctx, repo, baseRef) {
				return baseRef, r, nil
			}
			continue
		}
		args := []string{"ls-remote", r}
		for _, u := range wants {
			args = append(args, u.src)
		}
		lsOut, lsErr := gitRemote(args...)
		if lsErr != nil {
			failed = append(failed, fmt.Errorf("%s: %w", r, lsErr))
			continue
		}
		there := map[string]bool{}
		for _, line := range strings.Split(lsOut, "\n") {
			if _, name, ok := strings.Cut(line, "\t"); ok {
				there[strings.TrimSpace(name)] = true
			}
		}
		for _, u := range wants {
			if !there[u.src] {
				continue
			}
			refspec := u.src + ":" + u.dst
			if strings.HasPrefix(u.dst, "refs/remotes/") {
				refspec = "+" + refspec
			}
			if fetch(r, refspec) && isCommit(ctx, repo, u.dst) {
				return u.dst, r, nil
			}
			break
		}
	}
	missing.Fetch = errors.Join(failed...)
	return "", "", missing
}

// RemoveWorktree detaches and deletes a session's isolated worktree while leaving its branch intact.
func (m *Manager) RemoveWorktree(ctx context.Context, repoPath, worktreePath string) error {
	if worktreePath == "" {
		return nil
	}
	if repoPath != "" {
		unlock := m.lockRepo(repoPath)
		defer unlock()
		_, _ = runGit(ctx, repoPath, nil, "worktree", "remove", "--force", worktreePath)
		_, _ = runGit(ctx, repoPath, nil, "worktree", "prune")
	}
	return os.RemoveAll(worktreePath)
}

// SnapshotWorkingTree computes the git tree OID of workdir's current files (tracked + untracked,
// excluding standard gitignore) via a throwaway shadow index without touching the real index.
func (m *Manager) SnapshotWorkingTree(ctx context.Context, workdir string) (treeOID, headOID string, err error) {
	return m.snapshotWorkingTree(ctx, workdir)
}

func (m *Manager) snapshotWorkingTree(ctx context.Context, workdir string) (treeOID, headOID string, err error) {
	tmpDir, err := os.MkdirTemp("", "hermesh-idx-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmpDir)
	shadowIndex := filepath.Join(tmpDir, "index")

	hOut, headErr := runGit(ctx, workdir, nil, "rev-parse", "--verify", "HEAD")
	headOID = strings.TrimSpace(hOut)
	env := []string{"GIT_INDEX_FILE=" + shadowIndex}
	if headErr == nil && headOID != "" {
		if realGitDir, gErr := runGit(ctx, workdir, nil, "rev-parse", "--git-dir"); gErr == nil {
			realIndex := strings.TrimSpace(realGitDir)
			if !filepath.IsAbs(realIndex) {
				realIndex = filepath.Join(workdir, realIndex)
			}
			realIndex = filepath.Join(realIndex, "index")
			_ = copyFileIfExists(realIndex, shadowIndex)
		}
	}

	if _, err := runGit(ctx, workdir, env, "add", "-A"); err != nil {
		return "", "", fmt.Errorf("shadow git add: %w", err)
	}
	treeOut, err := runGit(ctx, workdir, env, "write-tree")
	if err != nil {
		return "", "", fmt.Errorf("shadow git write-tree: %w", err)
	}
	return strings.TrimSpace(treeOut), headOID, nil
}

// CaptureCheckpoint snapshots the entire working tree into a Git tree object via a temporary
// GIT_INDEX_FILE, pinning it under refs/hermesh/checkpoints/<sessionID>/<ordinal>.
func (m *Manager) CaptureCheckpoint(ctx context.Context, workdir, checkpointID, sessionID, runID string, runOrdinal uint32, prevTreeOID string) (*sessionv1.Checkpoint, error) {
	if !IsGitRepo(ctx, workdir) {
		return nil, nil
	}
	treeOID, headOID, err := m.snapshotWorkingTree(ctx, workdir)
	if err != nil {
		return nil, err
	}

	refName := fmt.Sprintf("refs/hermesh/checkpoints/%s/%d", sanitizeRefSegment(sessionID), runOrdinal)
	if commitOut, cErr := runGit(ctx, workdir, nil, "commit-tree", treeOID, "-m", "hermesh checkpoint"); cErr == nil {
		_, _ = runGit(ctx, workdir, nil, "update-ref", refName, strings.TrimSpace(commitOut))
	}

	compareBase := prevTreeOID
	if compareBase == "" && headOID != "" {
		if ht, hErr := runGit(ctx, workdir, nil, "rev-parse", headOID+"^{tree}"); hErr == nil {
			compareBase = strings.TrimSpace(ht)
		}
	}
	var files []*sessionv1.CheckpointFile
	if compareBase != "" && compareBase != treeOID {
		files = diffTreesSummary(ctx, workdir, compareBase, treeOID)
	}

	return &sessionv1.Checkpoint{
		CheckpointId: checkpointID,
		SessionId:    sessionID,
		RunId:        runID,
		RunOrdinal:   runOrdinal,
		GitTreeOid:   treeOID,
		HeadOid:      headOID,
		Files:        files,
	}, nil
}

// RestoreTree resets tracked and untracked files in workdir to match treeOID.
func (m *Manager) RestoreTree(ctx context.Context, workdir, treeOID string) error {
	if !IsGitRepo(ctx, workdir) || treeOID == "" {
		return fmt.Errorf("cannot restore git tree %q in %q", treeOID, workdir)
	}
	tmpDir, err := os.MkdirTemp("", "hermesh-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}
	if _, err := runGit(ctx, workdir, env, "read-tree", "--reset", treeOID); err != nil {
		return fmt.Errorf("read-tree %s: %w", treeOID, err)
	}
	if _, err := runGit(ctx, workdir, env, "checkout-index", "-f", "-a"); err != nil {
		return fmt.Errorf("checkout-index %s: %w", treeOID, err)
	}
	if _, err := runGit(ctx, workdir, env, "clean", "-fd"); err != nil {
		return fmt.Errorf("clean untracked files after restore: %w", err)
	}
	return nil
}

// DiffTrees computes unified diff and file summary between two git tree OIDs in repoOrWorkdir.
func (m *Manager) DiffTrees(ctx context.Context, dir, baseTreeOID, targetTreeOID string) (string, []*sessionv1.CheckpointFile, error) {
	if !IsGitRepo(ctx, dir) || baseTreeOID == "" || targetTreeOID == "" || baseTreeOID == targetTreeOID {
		return "", nil, nil
	}
	patch, err := runGit(ctx, dir, nil, "diff-tree", "-p", "--no-commit-id", "-r", baseTreeOID, targetTreeOID)
	if err != nil {
		return "", nil, err
	}
	return patch, diffTreesSummary(ctx, dir, baseTreeOID, targetTreeOID), nil
}

// DiffWorkingTree computes unified diff and file stats between baseTreeOID (or HEAD) and current working tree.
func (m *Manager) DiffWorkingTree(ctx context.Context, workdir, baseTreeOID string) (string, []*sessionv1.CheckpointFile, error) {
	if !IsGitRepo(ctx, workdir) {
		return "", nil, nil
	}
	treeOID, headOID, err := m.snapshotWorkingTree(ctx, workdir)
	if err != nil {
		return "", nil, err
	}
	base := baseTreeOID
	if base == "" && headOID != "" {
		if ht, hErr := runGit(ctx, workdir, nil, "rev-parse", headOID+"^{tree}"); hErr == nil {
			base = strings.TrimSpace(ht)
		}
	}
	if base == "" {
		emptyOut, _ := runGit(ctx, workdir, nil, "hash-object", "-t", "tree", "/dev/null")
		base = strings.TrimSpace(emptyOut)
		if base == "" {
			base = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
		}
	}
	if base == treeOID {
		return "", nil, nil
	}
	patch, err := runGit(ctx, workdir, nil, "diff-tree", "-p", "--no-commit-id", "-r", base, treeOID)
	if err != nil {
		return "", nil, err
	}
	return patch, diffTreesSummary(ctx, workdir, base, treeOID), nil
}

func diffTreesSummary(ctx context.Context, workdir, baseTree, nextTree string) []*sessionv1.CheckpointFile {
	statusMap := map[string]string{}
	if nameStatus, err := runGit(ctx, workdir, nil, "diff-tree", "--name-status", "--no-commit-id", "-r", baseTree, nextTree); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(nameStatus), "\n") {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) == 2 {
				statusMap[parts[1]] = parts[0]
			}
		}
	}
	numstat, err := runGit(ctx, workdir, nil, "diff-tree", "--numstat", "--no-commit-id", "-r", baseTree, nextTree)
	if err != nil {
		return nil
	}
	var out []*sessionv1.CheckpointFile
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		st := statusMap[parts[2]]
		if st == "" {
			st = "M"
		}
		out = append(out, &sessionv1.CheckpointFile{
			Path:      parts[2],
			Status:    st,
			Additions: uint32(max(0, add)),
			Deletions: uint32(max(0, del)),
		})
	}
	return out
}

func copyFileIfExists(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func sanitizeRefSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func runGit(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	fullArgs := append([]string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgsign=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
	}, args...)
	cmd := exec.CommandContext(cctx, "git", fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
