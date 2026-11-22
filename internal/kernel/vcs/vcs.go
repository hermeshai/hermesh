// Package vcs provides Git worktree isolation, shadow-index tree checkpoints
// (without polluting the user's index or commit log), unified diffs, and
// checkpoint file rollback for Hermesh v2 threads.
package vcs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
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

// PrepareWorktree creates or restores an isolated git worktree for threadID.
func (m *Manager) PrepareWorktree(ctx context.Context, repoPath, threadID, baseRef, branch string) (worktreePath, resolvedBranch string, err error) {
	if !IsGitRepo(ctx, repoPath) {
		return "", "", fmt.Errorf("project directory %q is not a git repository", repoPath)
	}
	unlock := m.lockRepo(repoPath)
	defer unlock()

	if err := os.MkdirAll(m.worktreesRoot, 0o755); err != nil {
		return "", "", err
	}
	wtPath := filepath.Join(m.worktreesRoot, threadID)
	if branch == "" {
		branch = "hermesh/" + threadID
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}

	// If the worktree already exists on disk and is valid, reuse it.
	if st, statErr := os.Stat(wtPath); statErr == nil && st.IsDir() && IsGitRepo(ctx, wtPath) {
		return wtPath, branch, nil
	}

	// Prune any stale worktree registrations first.
	_, _ = runGit(ctx, repoPath, nil, "worktree", "prune")
	_ = os.RemoveAll(wtPath)

	// Verify baseRef exists unless branch already exists (resuming missing worktree).
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
	if _, err := runGit(ctx, repoPath, nil, "rev-parse", "--verify", baseRef+"^{commit}"); err != nil {
		return "", "", fmt.Errorf("base ref %q not found in repository: %w", baseRef, err)
	}
	if _, err := runGit(ctx, repoPath, nil, "worktree", "add", "-b", branch, wtPath, baseRef); err != nil {
		return "", "", fmt.Errorf("create worktree from %s: %w", baseRef, err)
	}
	return wtPath, branch, nil
}

// RemoveWorktree detaches and deletes a thread's isolated worktree while leaving its branch intact.
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

func (m *Manager) snapshotWorkingTree(ctx context.Context, workdir string) (treeOID, headOID string, err error) {
	tmpDir, err := os.MkdirTemp("", "hermesh-idx-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmpDir)
	shadowIndex := filepath.Join(tmpDir, "index")

	// Seed shadow index from real index if HEAD exists so unchanged stat entries stay fast.
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

// CaptureCheckpoint snapshots the entire working tree (tracked + untracked,
// excluding .git and standard gitignore) into a Git tree object via a temporary
// GIT_INDEX_FILE, pinning it under refs/hermesh/checkpoints/<threadID>/<ordinal>.
// The user's real .git/index and commit history remain untouched.
func (m *Manager) CaptureCheckpoint(ctx context.Context, workdir, checkpointID, threadID, runID string, runOrdinal uint32, prevTreeOID string) (*threadv2.Checkpoint, error) {
	if !IsGitRepo(ctx, workdir) {
		return nil, nil
	}
	treeOID, headOID, err := m.snapshotWorkingTree(ctx, workdir)
	if err != nil {
		return nil, err
	}

	// Pin tree via a parentless commit under refs/hermesh/checkpoints/ so git gc
	// and git fsck keep all reachable blobs valid while the thread lives.
	refName := fmt.Sprintf("refs/hermesh/checkpoints/%s/%d", sanitizeRefSegment(threadID), runOrdinal)
	if commitOut, cErr := runGit(ctx, workdir, nil, "commit-tree", treeOID, "-m", "hermesh checkpoint"); cErr == nil {
		_, _ = runGit(ctx, workdir, nil, "update-ref", refName, strings.TrimSpace(commitOut))
	}

	compareBase := prevTreeOID
	if compareBase == "" && headOID != "" {
		if ht, hErr := runGit(ctx, workdir, nil, "rev-parse", headOID+"^{tree}"); hErr == nil {
			compareBase = strings.TrimSpace(ht)
		}
	}
	var files []*threadv2.CheckpointFile
	if compareBase != "" && compareBase != treeOID {
		files = diffTreesSummary(ctx, workdir, compareBase, treeOID)
	}

	return &threadv2.Checkpoint{
		CheckpointId: checkpointID,
		ThreadId:     threadID,
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
	// 1. Load target tree into index without failing on untracked collisions.
	if _, err := runGit(ctx, workdir, nil, "read-tree", "--reset", treeOID); err != nil {
		return fmt.Errorf("read-tree %s: %w", treeOID, err)
	}
	// 2. Force-write every file from treeOID to working directory.
	if _, err := runGit(ctx, workdir, nil, "checkout-index", "-f", "-a"); err != nil {
		return fmt.Errorf("checkout-index %s: %w", treeOID, err)
	}
	// 3. Remove any file/dir on disk not present in treeOID (excluding gitignored).
	if _, err := runGit(ctx, workdir, nil, "clean", "-fd"); err != nil {
		return fmt.Errorf("clean untracked files after restore: %w", err)
	}
	// 4. Restore real .git/index back to HEAD without modifying working tree files,
	// so `git status` / `git diff` stays clean relative to HEAD.
	if _, err := runGit(ctx, workdir, nil, "rev-parse", "--verify", "HEAD"); err == nil {
		_, _ = runGit(ctx, workdir, nil, "read-tree", "HEAD")
	}
	return nil
}

// DiffWorkingTree computes unified diff and file stats between baseTreeOID (or HEAD) and current working tree.
func (m *Manager) DiffWorkingTree(ctx context.Context, workdir, baseTreeOID string) (string, []*threadv2.CheckpointFile, error) {
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
		// Empty repo fallback: diff against Git's canonical empty tree.
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

func diffTreesSummary(ctx context.Context, workdir, baseTree, nextTree string) []*threadv2.CheckpointFile {
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
	var out []*threadv2.CheckpointFile
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
		out = append(out, &threadv2.CheckpointFile{
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
		return "thread"
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
