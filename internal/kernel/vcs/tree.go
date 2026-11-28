package vcs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// A checkpoint's tree read back (docs/session-files.md §6.2): a turn's files as it
// left them, from the repository's objects, so a released worktree is not needed.

// TreeEntry is one path in a tree.
type TreeEntry struct {
	// From the tree's root, with '/'.
	Path string
	// "blob", "tree" or "commit" (a submodule).
	Type string
	// "100644", "100755", "120000" (a symlink), "040000"...
	Mode string
	OID  string
	// A blob's size; 0 otherwise.
	Size int64
}

// Link reports whether the entry is a symbolic link (its blob is the link's text).
func (e TreeEntry) Link() bool { return e.Mode == "120000" }

// TreeEntries looks up paths in tree, run in dir (a work tree or the repository).
// A path ending in "/" lists that folder's children; "" lists the root. Paths not
// in the tree are left out.
func TreeEntries(ctx context.Context, dir, tree string, paths []string) ([]TreeEntry, error) {
	args := []string{"ls-tree", "-l", "-z", "--full-tree", tree}
	if len(paths) > 0 && !(len(paths) == 1 && paths[0] == "") {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := runGit(ctx, dir, nil, args...)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for rec := range strings.SplitSeq(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		entries = append(entries, TreeEntry{Path: path, Mode: f[0], Type: f[1], OID: f[2], Size: size})
	}
	return entries, nil
}

// ReadBlob reads a blob's bytes.
func ReadBlob(ctx context.Context, dir, oid string) ([]byte, error) {
	if !isOID(oid) {
		return nil, fmt.Errorf("not an object id: %q", oid)
	}
	out, err := runGit(ctx, dir, nil, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Prefix is dir's place within its repository's work tree ("" at its root,
// else "sub/dir/"); "" when it is not in one.
func Prefix(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, nil, "rev-parse", "--show-prefix")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type cachedWorktreePrefix struct {
	prefix string
	ok     bool
}

var (
	worktreePrefixMu    sync.Mutex
	worktreePrefixCache = map[string]cachedWorktreePrefix{}
)

// WorktreePrefix reports whether dir is inside a Git work tree and returns its
// slash-terminated prefix within that work tree ("" at the work tree root).
// Root repositories and worktrees with a valid .git entry resolve without
// spawning a git subprocess; subdirectory lookups are cached by canonical path.
func WorktreePrefix(ctx context.Context, dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	if st, err := os.Stat(dotGit); err == nil {
		if st.IsDir() {
			if _, hErr := os.Stat(filepath.Join(dotGit, "HEAD")); hErr == nil {
				return "", true
			}
		} else if b, rErr := os.ReadFile(dotGit); rErr == nil && strings.HasPrefix(string(b), "gitdir:") {
			return "", true
		}
	}
	key := filepath.Clean(dir)
	if real, err := filepath.EvalSymlinks(key); err == nil && real != "" {
		key = real
	}
	worktreePrefixMu.Lock()
	if hit, exists := worktreePrefixCache[key]; exists {
		worktreePrefixMu.Unlock()
		return hit.prefix, hit.ok
	}
	worktreePrefixMu.Unlock()

	out, err := runGit(ctx, dir, nil, "rev-parse", "--is-inside-work-tree", "--show-prefix")
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "true" {
		return "", false
	}
	var prefix string
	if len(lines) > 1 {
		prefix = strings.TrimSpace(lines[1])
	}
	worktreePrefixMu.Lock()
	if len(worktreePrefixCache) >= 256 {
		clear(worktreePrefixCache)
	}
	worktreePrefixCache[key] = cachedWorktreePrefix{prefix: prefix, ok: true}
	worktreePrefixMu.Unlock()
	return prefix, true
}

// Ignored reports whether Git ignores rel (relative to dir) there.
func Ignored(ctx context.Context, dir, rel string) bool {
	_, err := runGit(ctx, dir, nil, "check-ignore", "-q", "--no-index", "--", rel)
	return err == nil
}

func isOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// RemoveCheckpoints drops a session's checkpoint refs (refs/hermesh/checkpoints/<session>/*)
// from the repository at dir: its turns' files go with the session (docs/session-files.md §6.2).
func (m *Manager) RemoveCheckpoints(ctx context.Context, dir, sessionID string) error {
	out, err := runGit(ctx, dir, nil, "for-each-ref", "--format=%(refname)", "refs/hermesh/checkpoints/"+sanitizeRefSegment(sessionID)+"/")
	if err != nil {
		return err
	}
	for ref := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if ref != "" {
			if _, err := runGit(ctx, dir, nil, "update-ref", "-d", ref); err != nil {
				return err
			}
		}
	}
	return nil
}
