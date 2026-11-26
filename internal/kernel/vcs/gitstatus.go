package vcs

import (
	"cmp"
	"context"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// GitStatusInput describes the session workspace and baseline references needed
// to classify a session's local Git working tree and branch delivery state
// without making network requests.
type GitStatusInput struct {
	Workdir        string
	RepoPath       string
	SessionBranch  string
	BaseRef        string
	InitialHeadOID string
	HasChangesHint bool
}

// InspectGitStatus evaluates the local Git delivery state of a session's
// worktree (or its surviving branch in RepoPath after worktree release).
// It returns nil when neither Workdir nor RepoPath is a Git repository.
func (m *Manager) InspectGitStatus(ctx context.Context, in GitStatusInput) *sessionv1.GitStatus {
	hasWorktree := dirExists(in.Workdir) && IsGitRepo(ctx, in.Workdir)
	gitDir := in.Workdir
	if !hasWorktree {
		if in.SessionBranch == "" || !dirExists(in.RepoPath) || !IsGitRepo(ctx, in.RepoPath) {
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

	targetDisplay, targetCandidates := resolveTargetBranch(ctx, gitDir, in.RepoPath, in.BaseRef)

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

	hasSessionCommits := true
	if in.InitialHeadOID != "" && headOID == in.InitialHeadOID {
		hasSessionCommits = false
	} else if in.InitialHeadOID == "" && !in.HasChangesHint && len(targetCandidates) > 0 {
		for _, cand := range targetCandidates {
			if resolveCommitOID(ctx, gitDir, cand) == headOID {
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

	// 1. Check whether the session's commits were merged (or cherry-picked / rebased)
	// into the target base branch, when working on a distinct branch.
	if targetDisplay != "" && branch != targetDisplay {
		notRewound := in.InitialHeadOID == "" || isAncestorCommit(ctx, gitDir, in.InitialHeadOID, headOID)
		if notRewound {
			for _, cand := range targetCandidates {
				if isAncestorCommit(ctx, gitDir, headOID, cand) || isPatchMerged(ctx, gitDir, cand, headOID, in.InitialHeadOID) {
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
	addCandidate := func(ref string) {
		if ref != "" && isCommit(ctx, gitDir, ref) {
			for _, existing := range candidates {
				if existing == ref {
					return
				}
			}
			candidates = append(candidates, ref)
		}
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
		addCandidate("refs/heads/" + name)
		addCandidate("refs/remotes/origin/" + name)
		addCandidate(clean)
		if len(candidates) > 0 {
			return display, candidates
		}
	}

	if sym, err := runGit(ctx, gitDir, nil, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		sym = strings.TrimSpace(sym)
		if _, b, ok := strings.Cut(sym, "/"); ok && b != "" {
			display = b
			addCandidate("refs/heads/" + b)
			addCandidate("refs/remotes/" + sym)
			if len(candidates) > 0 {
				return display, candidates
			}
		}
	}

	for _, b := range []string{"main", "master"} {
		addCandidate("refs/heads/" + b)
		addCandidate("refs/remotes/origin/" + b)
		if len(candidates) > 0 {
			return b, candidates
		}
	}

	if repoPath != "" && repoPath != gitDir && dirExists(repoPath) {
		if cur := CurrentBranch(ctx, repoPath); cur != "" && !commitIDRE.MatchString(cur) {
			addCandidate("refs/heads/" + cur)
			addCandidate("refs/remotes/origin/" + cur)
			if len(candidates) > 0 {
				return cur, candidates
			}
		}
	}

	return "", nil
}
