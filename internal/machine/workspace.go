package machine

import (
	"context"
	"errors"
	"fmt"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Workspace manages one git worktree per session.
type Workspace struct {
	// none runs sessions in plain empty directories without git (for
	// benchmarking the daemon itself; no snapshots or diffs).
	none  bool
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewWorkspace(root string) *Workspace {
	return &Workspace{root: root, locks: map[string]*sync.Mutex{}}
}

func (w *Workspace) lock(repo string) func() {
	w.mu.Lock()
	l := w.locks[repo]
	if l == nil {
		l = &sync.Mutex{}
		w.locks[repo] = l
	}
	w.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// projectBase is what a new session's worktree starts from: the project's
// default base ref, with HEAD resolved to the branch checked out now (or
// its short commit when detached). In-place projects have none.
func projectBase(ctx context.Context, p Project) string {
	if p.InPlace() {
		return ""
	}
	if p.DefaultBaseRef != "HEAD" {
		return p.DefaultBaseRef
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if b, err := git(ctx, p.Path, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && strings.TrimSpace(b) != "" {
		return strings.TrimSpace(b)
	}
	if c, err := git(ctx, p.Path, "rev-parse", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(c)
	}
	return "HEAD"
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return gitUnlimited(ctx, dir, args...)
}

// gitUnlimited is git without its two minutes, for checkouts: a busy
// machine makes them slow, and stopping one only wastes it. The session's
// start limits them (Limits.StartTimeout).
func gitUnlimited(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	waitStart := time.Now()
	gitSem <- struct{}{}
	mGitWait.Since(waitStart)
	start := time.Now()
	out, errb, err := provider.RunCommand(ctx, gitBin(), full, dir, env)
	<-gitSem
	gitHistogram(args).Since(start)
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(errb)))
	}
	return string(out), nil
}

// gitBin is the git binary itself: on macOS /usr/bin/git is an xcrun shim
// that execs the real one, which doubles the cost of every git command
// (7.8 ms vs 3.8 ms here).
var gitBin = sync.OnceValue(func() string {
	out, _, err := provider.RunCommand(context.Background(), "git", []string{"--exec-path"}, "", os.Environ())
	if err != nil {
		return "git"
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "git")
	if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return p
	}
	return "git"
})

// ValidateDir checks that path is a directory and resolves it.
func ValidateDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return abs, nil
}

// ValidateRepo checks that path is the top level of a git repository.
func ValidateRepo(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}
	top, err := git(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository", path)
	}
	top = strings.TrimSpace(top)
	if t, err := filepath.EvalSymlinks(top); err == nil {
		top = t
	}
	if top != abs {
		return "", fmt.Errorf("%s is inside repository %s; register the repository root", path, top)
	}
	return abs, nil
}

func (w *Workspace) Path(sessionID string) string { return filepath.Join(w.root, sessionID) }

// gitSem bounds concurrent git processes: beyond the core count they only
// thrash the CPU and slow the daemon's own RPCs.
var (
	gitSem   = make(chan struct{}, max(2, runtime.NumCPU()))
	mGitWait = metrics.Default.Histogram("hermesh_machine_git_wait_seconds")
)

// readRef reads a loose ref straight from the repository (saving a git
// process); "" means fall back to git rev-parse.
func readRef(repo, ref string) string {
	d := filepath.Join(repo, ".git")
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(ref)))
	if err != nil {
		return ""
	}
	if h := strings.TrimSpace(string(b)); len(h) == 40 || len(h) == 64 {
		return h
	}
	return ""
}

func resolveRef(ctx context.Context, repo, ref string) (string, error) {
	if h := readRef(repo, ref); h != "" {
		return h, nil
	}
	out, err := git(ctx, repo, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

// Create adds a worktree for sessionID at baseRef and returns the base
// commit and branch name. Sessions write disjoint paths and refs, so
// concurrent adds are safe; git's own lock files guard the shared bits and
// a contended lock is retried.
func (w *Workspace) Create(ctx context.Context, repo, baseRef, sessionID string) (base, branch string, err error) {
	if w.none {
		return "", "", os.MkdirAll(w.Path(sessionID), 0o700)
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if strings.HasPrefix(baseRef, "-") {
		return "", "", fmt.Errorf("invalid base ref %q", baseRef)
	}
	branch = "hermesh/" + sessionID
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return "", "", err
	}
	if common := plainGitDir(repo); common != "" {
		err = w.addWorktree(ctx, repo, common, baseRef, branch, sessionID, false)
	} else {
		err = w.gitWorktreeAdd(ctx, repo, baseRef, branch, sessionID, false)
	}
	if err != nil {
		return "", "", fmt.Errorf("create worktree at %q: %w", baseRef, err)
	}
	base, err = resolveRef(ctx, repo, "refs/heads/"+branch)
	return base, branch, err
}

// Restore checks the session's branch out again at its worktree path,
// after Remove or a missing directory: every turn's work is in the branch.
func (w *Workspace) Restore(ctx context.Context, repo, sessionID string) error {
	if w.none {
		return os.MkdirAll(w.Path(sessionID), 0o700)
	}
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return err
	}
	branch := "hermesh/" + sessionID
	// A removed directory may still be registered with git. Prune its stale
	// metadata before either checkout path tries to register it again.
	w.Prune(ctx, repo)
	if common := plainGitDir(repo); common != "" {
		return w.addWorktree(ctx, repo, common, branch, branch, sessionID, true)
	}
	return w.gitWorktreeAdd(ctx, repo, branch, branch, sessionID, true)
}

// plainGitDir returns repo's .git directory when a worktree can be laid out
// directly (see addWorktree), or "" to leave it to git worktree add: .git
// is a file (repo is itself a worktree or submodule), or the config uses
// per-worktree config or sparse checkout, which git copies to new
// worktrees.
func plainGitDir(repo string) string {
	d := filepath.Join(repo, ".git")
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		return ""
	}
	cfg, err := os.ReadFile(filepath.Join(d, "config"))
	if err != nil {
		return ""
	}
	if c := strings.ToLower(string(cfg)); strings.Contains(c, "worktreeconfig") || strings.Contains(c, "sparsecheckout") {
		return ""
	}
	return d
}

// addWorktree does what git worktree add does, in the documented on-disk
// layout (gitrepository-layout(5), "worktrees/<id>"): git worktree add
// reads every existing worktree's metadata first, so with a thousand
// sessions on one repository each add cost ~0.3 s and all of them O(n^2).
func (w *Workspace) addWorktree(ctx context.Context, repo, common, baseRef, branch, id string, existing bool) error {
	base, err := resolveCommit(ctx, repo, baseRef)
	if err != nil {
		return err
	}
	wt := w.Path(id)
	admin := filepath.Join(common, "worktrees", id)
	err = func() error {
		if err := os.Mkdir(wt, 0o700); err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(wt)
		if err != nil {
			return err
		}
		realCommon, err := filepath.EvalSymlinks(common)
		if err != nil {
			return err
		}
		admin = filepath.Join(realCommon, "worktrees", id)
		if err := os.MkdirAll(filepath.Dir(admin), 0o755); err != nil {
			return err
		}
		if err := os.Mkdir(admin, 0o755); err != nil {
			return err
		}
		// "locked" keeps git worktree prune off a half-made worktree. Paths
		// as git writes them, with forward slashes on Windows too: git
		// takes "/.git" off gitdir to know the worktree's path.
		for _, f := range [][2]string{
			{filepath.Join(admin, "locked"), "initializing\n"},
			{filepath.Join(admin, "gitdir"), filepath.ToSlash(filepath.Join(real, ".git")) + "\n"},
			{filepath.Join(admin, "commondir"), "../..\n"},
			{filepath.Join(admin, "HEAD"), "ref: refs/heads/" + branch + "\n"},
			{filepath.Join(wt, ".git"), "gitdir: " + filepath.ToSlash(admin) + "\n"},
		} {
			if err := os.WriteFile(f[0], []byte(f[1]), 0o644); err != nil {
				return err
			}
		}
		// HEAD names the (unborn) session branch, so this creates it at
		// base and checks it out in one git process.
		if _, err := gitUnlimited(ctx, wt, "reset", "--hard", "-q", "--no-recurse-submodules", base, "--"); err != nil {
			return err
		}
		return os.Remove(filepath.Join(admin, "locked"))
	}()
	if err != nil {
		os.RemoveAll(wt)
		os.RemoveAll(admin)
		ref := filepath.Join(common, "refs", "heads", filepath.FromSlash(branch))
		// A stop kills git outright, so the lock it held while moving the
		// branch stays and would fail the retry.
		os.Remove(ref + ".lock")
		if !existing { // an existing branch holds the session's work
			os.Remove(ref)
		}
	}
	return err
}

// resolveCommit resolves ref in repo to a commit id, reading loose refs
// directly when it can.
func resolveCommit(ctx context.Context, repo, ref string) (string, error) {
	if ref == "HEAD" {
		if b, err := os.ReadFile(filepath.Join(repo, ".git", "HEAD")); err == nil {
			if t, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: "); ok {
				if h := readRef(repo, t); h != "" {
					return h, nil
				}
			}
		}
	} else if h := readRef(repo, "refs/heads/"+ref); h != "" {
		return h, nil
	}
	out, err := git(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

func (w *Workspace) gitWorktreeAdd(ctx context.Context, repo, baseRef, branch, id string, existing bool) error {
	// The branch first, so that retrying the add is idempotent.
	if !existing {
		if _, err := git(ctx, repo, "branch", "--no-track", branch, baseRef); err != nil {
			return err
		}
	}
	var err error
	for attempt := 0; ; attempt++ {
		_, err = gitUnlimited(ctx, repo, "worktree", "add", "-q", w.Path(id), branch)
		// Concurrent adds can see each other's lock files or half-created
		// metadata; both are transient.
		if err == nil || attempt >= 4 || !(strings.Contains(err.Error(), ".lock") || strings.Contains(err.Error(), "failed to read .git/worktrees")) {
			break
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	if err != nil {
		// A stopped or failed add leaves a half checkout (locked, maybe
		// without an index) and the branch, which would fail the retry.
		bg := context.WithoutCancel(ctx)
		w.Remove(bg, repo, id)
		os.RemoveAll(w.Path(id))
		if !existing { // a restored branch holds earlier turns
			git(bg, repo, "branch", "-D", branch)
		}
	}
	return err
}

// Snapshot commits all changes in the worktree and returns HEAD and this
// turn's diff stat (parsed from git commit's own summary).
func (w *Workspace) Snapshot(ctx context.Context, repo, sessionID string, turn uint32) (string, *sessionv1.DiffStat, error) {
	wt := w.Path(sessionID)
	if _, err := git(ctx, wt, "add", "-A"); err != nil {
		return "", nil, err
	}
	msg := fmt.Sprintf("hermesh %s turn %d", sessionID, turn)
	out, err := git(ctx, wt, "-c", "user.name=Hermesh", "-c", "user.email=hermesh@localhost", "commit", "--no-verify", "-m", msg)
	stat := &sessionv1.DiffStat{}
	if err != nil {
		if !strings.Contains(out+err.Error(), "nothing to commit") {
			return "", nil, err
		}
	} else {
		stat = parseShortstat(out)
	}
	branch := "refs/heads/hermesh/" + sessionID
	head, err := resolveRef(ctx, repo, branch)
	if err != nil {
		return "", nil, err
	}
	// The agent may have moved HEAD off the session branch (checkout,
	// switch, detached HEAD): the commit above went there. The session
	// branch is what is delivered, so record the worktree's state on it.
	cur, err := git(ctx, wt, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	if cur = strings.TrimSpace(cur); cur != head {
		out, err := git(ctx, wt, "rev-parse", cur+"^{tree}", head+"^{tree}")
		if err != nil {
			return "", nil, err
		}
		if trees := strings.Fields(out); len(trees) == 2 && trees[0] != trees[1] {
			c, err := git(ctx, wt, "-c", "user.name=Hermesh", "-c", "user.email=hermesh@localhost", "commit-tree", trees[0], "-p", head, "-m", msg+" (from "+short(cur)+")")
			if err != nil {
				return "", nil, err
			}
			c = strings.TrimSpace(c)
			if _, err := git(ctx, wt, "update-ref", branch, c, head); err != nil {
				return "", nil, err
			}
			if stat, err = diffStat(ctx, wt, head, c); err != nil {
				return "", nil, err
			}
			head = c
		}
	}
	return head, stat, nil
}

func short(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	return c
}

var shortstatRE = regexp.MustCompile(`(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?`)

func (w *Workspace) DiffStat(ctx context.Context, sessionID, from, to string) (*sessionv1.DiffStat, error) {
	return diffStat(ctx, w.Path(sessionID), from, to)
}

func diffStat(ctx context.Context, dir, from, to string) (*sessionv1.DiffStat, error) {
	out, err := git(ctx, dir, "diff", "--shortstat", from, to)
	if err != nil {
		return nil, err
	}
	return parseShortstat(out), nil
}

func parseShortstat(out string) *sessionv1.DiffStat {
	m := shortstatRE.FindStringSubmatch(out)
	st := &sessionv1.DiffStat{}
	if m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		c, _ := strconv.Atoi(m[3])
		st.FilesChanged, st.Insertions, st.Deletions = uint32(a), uint32(b), uint32(c)
	}
	return st
}

func (w *Workspace) Diff(ctx context.Context, sessionID, from, to string) (string, error) {
	return git(ctx, w.Path(sessionID), "diff", "--binary", from, to)
}

// CopyIncluded copies into the session's worktree the ignored files that
// the repository's .worktreeinclude names (gitignore syntax, as Claude
// Code reads it): local settings such as signing configuration or .env,
// which a checkout cannot bring. A named file that git does not ignore is
// left out, so a snapshot never commits it.
func (w *Workspace) CopyIncluded(ctx context.Context, repo, sessionID string) error {
	if w.none {
		return nil
	}
	include := filepath.Join(repo, ".worktreeinclude")
	b, err := os.ReadFile(include)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Both listings walk only the named paths when every pattern has one;
	// a bare name such as .env needs the whole tree.
	var specs []string
	if s := includePathspecs(string(b)); s != nil {
		specs = append([]string{"--"}, s...)
	}
	out, err := git(ctx, repo, append([]string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"}, specs...)...)
	if err != nil || out == "" {
		return err
	}
	ignored := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") // a directory ends with /
	out, err = git(ctx, repo, append([]string{"ls-files", "-z", "--others", "--ignored", "--exclude-from=" + include}, specs...)...)
	if err != nil {
		return err
	}
	var errs []error
	for _, rel := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if rel == "" || !slices.ContainsFunc(ignored, func(p string) bool { return p == rel || strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p) }) {
			continue
		}
		if err := copyIncluded(filepath.Join(repo, filepath.FromSlash(rel)), filepath.Join(w.Path(sessionID), filepath.FromSlash(rel))); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// includePathspecs turns .worktreeinclude patterns into the paths they
// can match under, or nil when a pattern can match anywhere.
func includePathspecs(patterns string) []string {
	var specs []string
	for _, l := range strings.Split(patterns, "\n") {
		l = strings.TrimRight(l, " \r")
		if l == "" || l[0] == '#' || l[0] == '!' { // a negation only narrows
			continue
		}
		p := strings.TrimSuffix(l, "/")
		if !strings.Contains(p, "/") {
			return nil
		}
		var dirs []string
		for _, c := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
			if strings.ContainsAny(c, `*?[\`) {
				break
			}
			dirs = append(dirs, c)
		}
		if len(dirs) == 0 {
			return nil
		}
		specs = append(specs, ":(literal)"+strings.Join(dirs, "/"))
	}
	return specs
}

// copyIncluded copies one file or symlink, keeping one already there.
func copyIncluded(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		to, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(to, dst); err != nil && !os.IsExist(err) {
			return err
		}
		return nil
	}
	if !st.Mode().IsRegular() {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (w *Workspace) Exists(sessionID string) bool {
	_, err := os.Stat(w.Path(sessionID))
	return err == nil
}

// Remove deletes the worktree but keeps the branch. It deletes the
// worktree and its metadata directly (what git worktree prune does for a
// vanished worktree) because git worktree remove also reads every
// worktree first.
func (w *Workspace) Remove(ctx context.Context, repo, sessionID string) error {
	wt := w.Path(sessionID)
	if w.none {
		return os.RemoveAll(wt)
	}
	b, err := os.ReadFile(filepath.Join(wt, ".git"))
	admin := filepath.FromSlash(strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))) // git writes / on Windows too
	common, cerr := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if err != nil || cerr != nil || !strings.HasPrefix(admin, filepath.Join(common, "worktrees")+string(filepath.Separator)) {
		unlock := w.lock(repo)
		defer unlock()
		_, err := git(ctx, repo, "worktree", "remove", "--force", filepath.ToSlash(wt)) // see moveWorktree
		return err
	}
	if err := os.RemoveAll(wt); err != nil {
		return err
	}
	return os.RemoveAll(admin)
}

func (w *Workspace) Prune(ctx context.Context, repo string) {
	unlock := w.lock(repo)
	defer unlock()
	git(ctx, repo, "worktree", "prune")
}

var gitHists sync.Map

// gitHistogram returns the latency histogram for a git subcommand.
func gitHistogram(args []string) *metrics.Histogram {
	sub := "?"
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" || args[i] == "-C" {
			i++
			continue
		}
		if !strings.HasPrefix(args[i], "-") {
			sub = args[i]
			break
		}
	}
	if h, ok := gitHists.Load(sub); ok {
		return h.(*metrics.Histogram)
	}
	h := metrics.Default.Histogram(`hermesh_machine_git_seconds{cmd="` + sub + `"}`)
	gitHists.Store(sub, h)
	return h
}
