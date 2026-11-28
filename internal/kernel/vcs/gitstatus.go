package vcs

import (
	"cmp"
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

type targetBranchCacheKey struct {
	isWorktree bool
	repo       string
	baseRef    string
}

type targetBranchCacheEntry struct {
	display    string
	candidates []string
	oids       map[string]string
}

// BatchCache memoizes read-only repository probes across a single
// RefreshGitStatus pass so sessions sharing a repository do not repeatedly
// spawn identical git rev-parse / symbolic-ref / for-each-ref subprocesses.
// It must never be retained across separate RPC calls.
type BatchCache struct {
	mu      sync.Mutex
	repos   map[string]bool
	targets map[targetBranchCacheKey]targetBranchCacheEntry
}

func NewBatchCache() *BatchCache {
	return &BatchCache{
		repos:   make(map[string]bool),
		targets: make(map[targetBranchCacheKey]targetBranchCacheEntry),
	}
}

func (b *BatchCache) isGitRepo(ctx context.Context, dir string) bool {
	if b == nil {
		return IsGitRepo(ctx, dir)
	}
	b.mu.Lock()
	if ok, exists := b.repos[dir]; exists {
		b.mu.Unlock()
		return ok
	}
	b.mu.Unlock()
	ok := IsGitRepo(ctx, dir)
	b.mu.Lock()
	b.repos[dir] = ok
	b.mu.Unlock()
	return ok
}

func (b *BatchCache) IsRepo(ctx context.Context, dir string) bool {
	return b.isGitRepo(ctx, dir)
}

func (b *BatchCache) resolveTarget(ctx context.Context, gitDir, repoPath, baseRef string) (string, []string, map[string]string) {
	if b == nil {
		return resolveTargetBranchWithOIDs(ctx, gitDir, repoPath, baseRef)
	}
	sharedRepo := gitDir
	if repoPath != "" && dirExists(repoPath) && b.isGitRepo(ctx, repoPath) {
		sharedRepo = repoPath
	}
	key := targetBranchCacheKey{
		isWorktree: gitDir != repoPath,
		repo:       sharedRepo,
		baseRef:    strings.TrimSpace(baseRef),
	}
	b.mu.Lock()
	if e, exists := b.targets[key]; exists {
		b.mu.Unlock()
		return e.display, slices.Clone(e.candidates), maps.Clone(e.oids)
	}
	b.mu.Unlock()

	probeDir := gitDir
	if sharedRepo == repoPath {
		probeDir = repoPath
	}
	display, candidates, oids := resolveTargetBranchWithOIDs(ctx, probeDir, repoPath, baseRef)
	b.mu.Lock()
	b.targets[key] = targetBranchCacheEntry{
		display:    display,
		candidates: slices.Clone(candidates),
		oids:       maps.Clone(oids),
	}
	b.mu.Unlock()
	return display, candidates, oids
}

// GitStatusInput describes the session workspace and baseline references needed
// to classify a session's local Git working tree and branch delivery state
// without making network requests.
type GitStatusInput struct {
	Workdir           string
	RepoPath          string
	SessionBranch     string
	BaseRef           string
	InitialHeadOID    string
	InitialTreeOID    string
	PreExistingDirty  map[string]bool
	LockSessionBranch bool
	HasChangesHint    bool
	Batch             *BatchCache
}

// SyncRemoteTargetBranch checks whether origin's target branch moved ahead of
// local refs/remotes/origin/<target> via a zero-write ls-remote probe and, only
// when it advanced, fetches that single ref without writing FETCH_HEAD or tags.
func (m *Manager) SyncRemoteTargetBranch(ctx context.Context, repoPath, baseRef string) (bool, error) {
	if !dirExists(repoPath) || !IsGitRepo(ctx, repoPath) {
		return false, nil
	}
	targetName, _ := resolveTargetBranch(ctx, repoPath, repoPath, baseRef)
	if targetName == "" {
		return false, nil
	}
	if _, err := runGit(ctx, repoPath, nil, "remote", "get-url", "origin"); err != nil {
		return false, nil
	}
	noPromptEnv := append(shellenv.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GIT_ASKPASS=true",
		"SSH_ASKPASS=true",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=5",
	)
	remoteRef := "refs/remotes/origin/" + targetName
	localOID := resolveCommitOID(ctx, repoPath, remoteRef)

	lsOut, err := runGit(ctx, repoPath, noPromptEnv, "ls-remote", "--heads", "origin", "refs/heads/"+targetName)
	if err != nil {
		return false, err
	}
	fields := strings.Fields(lsOut)
	if len(fields) == 0 || !commitIDRE.MatchString(fields[0]) || fields[0] == localOID {
		return false, nil
	}

	if _, err := runGit(ctx, repoPath, noPromptEnv,
		"fetch", "--quiet", "--no-write-fetch-head", "--no-tags", "--no-recurse-submodules",
		"origin", "+refs/heads/"+targetName+":"+remoteRef,
	); err != nil {
		return false, err
	}
	newOID := resolveCommitOID(ctx, repoPath, remoteRef)
	return newOID != "" && newOID != localOID, nil
}

// InspectGitStatus evaluates the local Git delivery state of a session's
// worktree (or its surviving branch in RepoPath after worktree release).
// It returns nil when neither Workdir nor RepoPath is a Git repository.
func (m *Manager) InspectGitStatus(ctx context.Context, in GitStatusInput) *sessionv1.GitStatus {
	hasWorktree := dirExists(in.Workdir) && in.Batch.isGitRepo(ctx, in.Workdir)
	if hasWorktree && in.LockSessionBranch && in.SessionBranch != "" {
		if cur := CurrentBranch(ctx, in.Workdir); cur != "" && cur != in.SessionBranch {
			hasWorktree = false
		}
	}
	gitDir := in.Workdir
	if !hasWorktree {
		if in.SessionBranch == "" || !dirExists(in.RepoPath) || !in.Batch.isGitRepo(ctx, in.RepoPath) {
			return nil
		}
		gitDir = in.RepoPath
	}

	var (
		branch        string
		headOID       string
		upstream      string
		upstreamAhead uint32
		dirtyFiles    uint32
	)

	if hasWorktree {
		out, err := runGit(ctx, in.Workdir, nil, "-c", "core.quotepath=false", "status", "--porcelain=v2", "--branch", "-unormal")
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "# ") {
				switch {
				case strings.HasPrefix(line, "# branch.oid "):
					if v := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid ")); v != "(initial)" {
						headOID = v
					}
				case strings.HasPrefix(line, "# branch.head "):
					if v := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head ")); v != "(detached)" {
						branch = v
					}
				case strings.HasPrefix(line, "# branch.upstream "):
					upstream = strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream "))
				case strings.HasPrefix(line, "# branch.ab "):
					fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
					if len(fields) >= 1 && strings.HasPrefix(fields[0], "+") {
						if n, nErr := strconv.Atoi(fields[0][1:]); nErr == nil && n > 0 {
							upstreamAhead = uint32(n)
						}
					}
				}
				continue
			}
			switch line[0] {
			case '1', '2', 'u', '?':
				if len(line) > 1 && line[1] == ' ' {
					p := porcelainPath(line)
					if p != "" && len(in.PreExistingDirty) > 0 && in.InitialTreeOID != "" &&
						unchangedFromBaseline(ctx, in.Workdir, in.InitialTreeOID, p, in.PreExistingDirty) {
						continue
					}
					dirtyFiles++
				}
			}
		}
		if branch == "" {
			branch = cmp.Or(in.SessionBranch, shortCommitOID(headOID))
		}
	} else {
		branch = in.SessionBranch
		out, err := runGit(ctx, in.RepoPath, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
		if err != nil || strings.TrimSpace(out) == "" {
			return nil
		}
		headOID = strings.TrimSpace(out)
		if up, uErr := runGit(ctx, in.RepoPath, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}"); uErr == nil {
			upstream = strings.TrimSpace(up)
		}
		if upstream != "" {
			if cnt, cErr := runGit(ctx, in.RepoPath, nil, "rev-list", "--count", upstream+"..refs/heads/"+branch); cErr == nil {
				if n, nErr := strconv.Atoi(strings.TrimSpace(cnt)); nErr == nil && n > 0 {
					upstreamAhead = uint32(n)
				}
			}
		}
	}

	targetDisplay, targetCandidates, targetOIDs := in.Batch.resolveTarget(ctx, gitDir, in.RepoPath, in.BaseRef)

	if headOID == "" {
		if dirtyFiles > 0 {
			return &sessionv1.GitStatus{
				State:      sessionv1.GitState_GIT_STATE_DIRTY,
				Branch:     branch,
				TargetRef:  targetDisplay,
				DirtyFiles: dirtyFiles,
			}
		}
		return &sessionv1.GitStatus{
			State:     sessionv1.GitState_GIT_STATE_CLEAN,
			Branch:    branch,
			TargetRef: targetDisplay,
		}
	}

	candidateOID := func(cand string) string {
		if oid := targetOIDs[cand]; oid != "" {
			return oid
		}
		oid := resolveCommitOID(ctx, gitDir, cand)
		if oid != "" {
			if targetOIDs == nil {
				targetOIDs = map[string]string{}
			}
			targetOIDs[cand] = oid
		}
		return oid
	}

	hasSessionCommits := true
	if in.InitialHeadOID != "" && headOID == in.InitialHeadOID {
		hasSessionCommits = false
	} else if in.InitialHeadOID == "" && !in.HasChangesHint && len(targetCandidates) > 0 {
		for _, cand := range targetCandidates {
			if candidateOID(cand) == headOID {
				hasSessionCommits = false
				break
			}
		}
	}

	var headCommit, headSubject string
	if hasSessionCommits {
		headCommit, headSubject = readCommitSummary(ctx, gitDir, headOID)
	}

	if dirtyFiles > 0 {
		return &sessionv1.GitStatus{
			State:       sessionv1.GitState_GIT_STATE_DIRTY,
			Branch:      branch,
			TargetRef:   targetDisplay,
			DirtyFiles:  dirtyFiles,
			HeadCommit:  headCommit,
			HeadSubject: headSubject,
		}
	}

	if !hasSessionCommits {
		return &sessionv1.GitStatus{
			State:     sessionv1.GitState_GIT_STATE_CLEAN,
			Branch:    branch,
			TargetRef: targetDisplay,
		}
	}

	// 1. Check whether the session's commits were merged (or cherry-picked / rebased / squash-merged)
	// into the target base branch, when working on a distinct branch.
	if targetDisplay != "" && branch != targetDisplay {
		notRewound := in.InitialHeadOID == "" || isAncestorCommit(ctx, gitDir, in.InitialHeadOID, headOID)
		if notRewound {
			checkedOIDs := make(map[string]bool, len(targetCandidates))
			for _, cand := range targetCandidates {
				if oid := candidateOID(cand); oid != "" {
					if checkedOIDs[oid] {
						continue
					}
					checkedOIDs[oid] = true
				}
				if isAncestorCommit(ctx, gitDir, headOID, cand) ||
					isPatchMerged(ctx, gitDir, cand, headOID, in.InitialHeadOID) ||
					isSquashMerged(ctx, gitDir, cand, headOID, in.InitialHeadOID) {
					return &sessionv1.GitStatus{
						State:       sessionv1.GitState_GIT_STATE_MERGED,
						Branch:      branch,
						TargetRef:   targetDisplay,
						HeadCommit:  headCommit,
						HeadSubject: headSubject,
					}
				}
			}
		}
	}

	// 2. Check whether HEAD is pushed to its upstream or any remote-tracking branch.
	if upstream != "" {
		if upstreamAhead == 0 {
			return &sessionv1.GitStatus{
				State:       sessionv1.GitState_GIT_STATE_PUSHED,
				Branch:      branch,
				TargetRef:   upstream,
				HeadCommit:  headCommit,
				HeadSubject: headSubject,
			}
		}
		return &sessionv1.GitStatus{
			State:        sessionv1.GitState_GIT_STATE_UNPUSHED,
			Branch:       branch,
			TargetRef:    upstream,
			CommitsAhead: upstreamAhead,
			HeadCommit:   headCommit,
			HeadSubject:  headSubject,
		}
	}

	if remoteRef := findRemoteContaining(ctx, gitDir, headOID, branch); remoteRef != "" {
		return &sessionv1.GitStatus{
			State:       sessionv1.GitState_GIT_STATE_PUSHED,
			Branch:      branch,
			TargetRef:   remoteRef,
			HeadCommit:  headCommit,
			HeadSubject: headSubject,
		}
	}

	// 3. Clean working tree with local unpushed commits.
	ahead := countCommitsAhead(ctx, gitDir, headOID, in.InitialHeadOID, targetCandidates)
	return &sessionv1.GitStatus{
		State:        sessionv1.GitState_GIT_STATE_UNPUSHED,
		Branch:       branch,
		TargetRef:    cmp.Or(targetDisplay, "origin"),
		CommitsAhead: ahead,
		HeadCommit:   headCommit,
		HeadSubject:  headSubject,
	}
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func shortCommitOID(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}

func resolveCommitOID(ctx context.Context, dir, ref string) string {
	out, err := runGit(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func resolveTreeOID(ctx context.Context, dir, ref string) string {
	if ref == "" {
		return ""
	}
	out, err := runGit(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{tree}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func porcelainPath(line string) string {
	switch line[0] {
	case '?':
		return line[2:]
	case '1':
		if fields := strings.SplitN(line, " ", 9); len(fields) == 9 {
			return fields[8]
		}
	case '2':
		if fields := strings.SplitN(line, " ", 10); len(fields) == 10 {
			p, _, _ := strings.Cut(fields[9], "\t")
			return p
		}
	case 'u':
		if fields := strings.SplitN(line, " ", 11); len(fields) == 11 {
			return fields[10]
		}
	}
	return ""
}

func unchangedFromBaseline(ctx context.Context, workdir, initialTreeOID, relPath string, preDirty map[string]bool) bool {
	if strings.HasSuffix(relPath, "/") {
		prefix := relPath
		wantCount := 0
		for k := range preDirty {
			if strings.HasPrefix(k, prefix) {
				wantCount++
			}
		}
		if wantCount == 0 {
			return false
		}
		root := filepath.Join(workdir, filepath.FromSlash(strings.TrimSuffix(relPath, "/")))
		seen := 0
		allMatch := true
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				allMatch = false
				return filepath.SkipAll
			}
			if d.IsDir() {
				return nil
			}
			seen++
			if seen > wantCount {
				allMatch = false
				return filepath.SkipAll
			}
			rel, rErr := filepath.Rel(workdir, p)
			if rErr != nil {
				allMatch = false
				return filepath.SkipAll
			}
			slashRel := filepath.ToSlash(rel)
			if !preDirty[slashRel] || !unchangedFileFromBaseline(ctx, workdir, initialTreeOID, slashRel) {
				allMatch = false
				return filepath.SkipAll
			}
			return nil
		})
		return allMatch && seen == wantCount
	}
	if !preDirty[relPath] {
		return false
	}
	return unchangedFileFromBaseline(ctx, workdir, initialTreeOID, relPath)
}

func unchangedFileFromBaseline(ctx context.Context, workdir, initialTreeOID, relPath string) bool {
	wantOut, wantErr := runGit(ctx, workdir, nil, "rev-parse", "--verify", "--quiet", initialTreeOID+":"+relPath)
	full := filepath.Join(workdir, filepath.FromSlash(relPath))
	st, statErr := os.Lstat(full)
	if wantErr != nil {
		return os.IsNotExist(statErr)
	}
	if statErr != nil || st.IsDir() {
		return false
	}
	gotOut, gotErr := runGit(ctx, workdir, nil, "hash-object", "--", full)
	return gotErr == nil && strings.TrimSpace(wantOut) != "" && strings.TrimSpace(wantOut) == strings.TrimSpace(gotOut)
}

func isSquashMerged(ctx context.Context, dir, targetRef, headOID, initialHeadOID string) bool {
	var headTree, candTree, baseTree string
	baseCommit := initialHeadOID
	if baseCommit != "" {
		headTree, candTree, baseTree = resolveThreeTreeOIDs(ctx, dir, headOID, targetRef, baseCommit)
	} else {
		headTree = resolveTreeOID(ctx, dir, headOID)
		candTree = resolveTreeOID(ctx, dir, targetRef)
		if headTree == "" || candTree == "" {
			return false
		}
		mb, err := runGit(ctx, dir, nil, "merge-base", targetRef, headOID)
		if err != nil {
			return false
		}
		baseCommit = strings.TrimSpace(mb)
		baseTree = resolveTreeOID(ctx, dir, baseCommit)
	}
	if headTree == "" || candTree == "" || baseTree == "" || headTree == baseTree {
		return false
	}
	out, err := runGit(ctx, dir, nil, "merge-tree", "--write-tree", targetRef, headOID)
	if err != nil {
		return false
	}
	mergedTree, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(mergedTree) == candTree
}

func resolveThreeTreeOIDs(ctx context.Context, dir, a, b, c string) (string, string, string) {
	if a == "" || b == "" || c == "" {
		return "", "", ""
	}
	out, err := runGit(ctx, dir, nil, "rev-parse", "--end-of-options", a+"^{tree}", b+"^{tree}", c+"^{tree}")
	if err == nil {
		lines := strings.Fields(out)
		if len(lines) == 3 && commitIDRE.MatchString(lines[0]) && commitIDRE.MatchString(lines[1]) && commitIDRE.MatchString(lines[2]) {
			return lines[0], lines[1], lines[2]
		}
	}
	return resolveTreeOID(ctx, dir, a), resolveTreeOID(ctx, dir, b), resolveTreeOID(ctx, dir, c)
}

func isAncestorCommit(ctx context.Context, dir, ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" {
		return false
	}
	_, err := runGit(ctx, dir, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func isPatchMerged(ctx context.Context, dir, targetRef, headOID, initialHeadOID string) bool {
	if initialHeadOID == "" || targetRef == "" || headOID == "" {
		return false
	}
	out, err := runGit(ctx, dir, nil, "cherry", targetRef, headOID, initialHeadOID)
	if err != nil {
		return false
	}
	sawApplied := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+") {
			return false
		}
		if strings.HasPrefix(line, "-") {
			sawApplied = true
		}
	}
	return sawApplied
}

func findRemoteContaining(ctx context.Context, dir, headOID, branch string) string {
	out, err := runGit(ctx, dir, nil, "for-each-ref", "--format=%(refname:short)", "--contains="+headOID, "refs/remotes/")
	if err != nil {
		return ""
	}
	var first string
	preferred := "origin/" + branch
	for _, r := range strings.Split(strings.TrimSpace(out), "\n") {
		r = strings.TrimSpace(r)
		if r == "" || r == "origin" || strings.HasSuffix(r, "/HEAD") || !strings.Contains(r, "/") {
			continue
		}
		if r == preferred {
			return r
		}
		if first == "" {
			first = r
		}
	}
	return first
}

func countCommitsAhead(ctx context.Context, dir, headOID, initialHeadOID string, targetCandidates []string) uint32 {
	for _, base := range append([]string{initialHeadOID}, targetCandidates...) {
		if base == "" {
			continue
		}
		out, err := runGit(ctx, dir, nil, "rev-list", "--count", base+".."+headOID)
		if err == nil {
			if n, nErr := strconv.Atoi(strings.TrimSpace(out)); nErr == nil && n > 0 {
				return uint32(n)
			}
		}
	}
	return 1
}

func readCommitSummary(ctx context.Context, dir, oid string) (shortHash, subject string) {
	out, err := runGit(ctx, dir, nil, "log", "-1", "--format=%h%x09%s", oid)
	if err != nil {
		return shortCommitOID(oid), ""
	}
	line := strings.TrimSpace(out)
	h, s, ok := strings.Cut(line, "\t")
	if !ok {
		return shortCommitOID(oid), truncateRunes(line, 120)
	}
	return strings.TrimSpace(h), truncateRunes(strings.TrimSpace(s), 120)
}

func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes-1]) + "…"
}

func resolveTargetBranch(ctx context.Context, gitDir, repoPath, baseRef string) (display string, candidates []string) {
	d, c, _ := resolveTargetBranchWithOIDs(ctx, gitDir, repoPath, baseRef)
	return d, c
}

func resolveTargetBranchWithOIDs(ctx context.Context, gitDir, repoPath, baseRef string) (display string, candidates []string, oids map[string]string) {
	oids = map[string]string{}
	addResolved := func(ref, oid string) {
		if ref == "" || oid == "" {
			return
		}
		for _, existing := range candidates {
			if existing == ref {
				return
			}
		}
		candidates = append(candidates, ref)
		oids[ref] = oid
	}
	addCandidate := func(ref string) {
		if ref == "" {
			return
		}
		if oid := resolveCommitOID(ctx, gitDir, ref); oid != "" {
			addResolved(ref, oid)
		}
	}
	lookupFullRefs := func(refs ...string) map[string]string {
		found := make(map[string]string, len(refs))
		args := append([]string{"for-each-ref", "--format=%(refname)\t%(objectname)"}, refs...)
		out, err := runGit(ctx, gitDir, nil, args...)
		if err != nil {
			return found
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			ref, oid, ok := strings.Cut(strings.TrimSpace(line), "\t")
			if ok && ref != "" && commitIDRE.MatchString(oid) {
				found[ref] = oid
			}
		}
		return found
	}

	clean := strings.TrimSpace(baseRef)
	if clean != "" && clean != "HEAD" && !commitIDRE.MatchString(clean) {
		name := strings.TrimPrefix(clean, "refs/heads/")
		if after, ok := strings.CutPrefix(name, "refs/remotes/"); ok {
			if _, b, hasSlash := strings.Cut(after, "/"); hasSlash {
				name = b
			}
		} else if afterOrigin, ok := strings.CutPrefix(name, "origin/"); ok {
			name = afterOrigin
		}
		display = name
		headRef := "refs/heads/" + name
		remoteRef := "refs/remotes/origin/" + name
		found := lookupFullRefs(headRef, remoteRef)
		addResolved(headRef, found[headRef])
		addResolved(remoteRef, found[remoteRef])
		if clean != name && clean != headRef && clean != remoteRef && clean != "origin/"+name {
			addCandidate(clean)
		} else if len(candidates) == 0 {
			// Fallback for tags or unusual ref layouts not under refs/heads or refs/remotes/origin.
			addCandidate(clean)
		}
		if len(candidates) > 0 {
			return display, candidates, oids
		}
	}

	if sym, err := runGit(ctx, gitDir, nil, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		sym = strings.TrimSpace(sym)
		if _, b, ok := strings.Cut(sym, "/"); ok && b != "" {
			display = b
			headRef := "refs/heads/" + b
			remoteRef := "refs/remotes/" + sym
			found := lookupFullRefs(headRef, remoteRef)
			addResolved(headRef, found[headRef])
			addResolved(remoteRef, found[remoteRef])
			if len(candidates) > 0 {
				return display, candidates, oids
			}
		}
	}

	foundDefaults := lookupFullRefs(
		"refs/heads/main", "refs/remotes/origin/main",
		"refs/heads/master", "refs/remotes/origin/master",
	)
	for _, b := range []string{"main", "master"} {
		headRef := "refs/heads/" + b
		remoteRef := "refs/remotes/origin/" + b
		addResolved(headRef, foundDefaults[headRef])
		addResolved(remoteRef, foundDefaults[remoteRef])
		if len(candidates) > 0 {
			return b, candidates, oids
		}
	}

	if repoPath != "" && repoPath != gitDir && dirExists(repoPath) {
		if cur := CurrentBranch(ctx, repoPath); cur != "" && !commitIDRE.MatchString(cur) {
			headRef := "refs/heads/" + cur
			remoteRef := "refs/remotes/origin/" + cur
			found := lookupFullRefs(headRef, remoteRef)
			addResolved(headRef, found[headRef])
			addResolved(remoteRef, found[remoteRef])
			if len(candidates) > 0 {
				return cur, candidates, oids
			}
		}
	}

	return "", nil, nil
}
