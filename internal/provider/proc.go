package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/hermeshai/hermesh/internal/spawn"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

// MaxLine is the longest stdout line we accept (Codex can put a whole
// thread on one line).
const MaxLine = 32 << 20

// drainGrace is how long, after the agent exits, its output may take to
// reach the end (a descendant can hold the pipes open forever).
const drainGrace = 2 * time.Second

// Proc is a child process in its own process group.
type Proc struct {
	pid    int
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *ring

	writeMu sync.Mutex
	done    chan struct{}
	err     error

	tree      *tracked      // descendants, wherever they moved
	linesDone chan struct{} // Lines reached the end of stdout
	lastOut   atomic.Int64  // unix nanos of the last stdout line
	scanMu    sync.Mutex
	scanErr   error // why Lines stopped early (overrides the exit error)
}

// useSpawn selects posix_spawn where supported (tests force os/exec, the
// path Linux uses).
var useSpawn = spawn.Supported

func StartProc(ctx context.Context, bin string, args []string, dir string, env []string) (*Proc, error) {
	return startProc(bin, args, dir, env)
}

func startProc(bin string, args []string, dir string, env []string) (*Proc, error) {
	if useSpawn {
		p, err := startSpawn(bin, args, dir, env)
		if !errors.Is(err, spawn.ErrFDRange) {
			return p, err
		}
		mSpawnFallback.Inc()
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Our own stdout pipe: cmd.Wait would close a StdoutPipe as soon as the
	// process exits, losing lines not read yet.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = outW
	p := &Proc{stdin: stdin, stdout: outR, stderr: newRing(64 << 10), done: make(chan struct{}), linesDone: make(chan struct{})}
	cmd.Stderr = p.stderr
	cmd.WaitDelay = drainGrace // bounds the stderr copy
	start := time.Now()
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	outW.Close()
	mSpawn.Since(start)
	p.pid = cmd.Process.Pid
	p.tree = track(p.pid)
	go p.wait(func() error {
		err := cmd.Wait()
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		p.afterExit(nil, outR)
		return err
	})
	return p, nil
}

// startSpawn starts the process with posix_spawn (see package spawn).
func startSpawn(bin string, args []string, dir string, env []string) (*Proc, error) {
	if !strings.Contains(bin, "/") {
		lp, err := exec.LookPath(bin)
		if err != nil {
			return nil, err
		}
		bin = lp
	}
	if env == nil {
		env = os.Environ()
	}
	inW, inR, err := spawn.Pipe(false)
	if err != nil {
		return nil, err
	}
	outR, outW, err := spawn.Pipe(true)
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := spawn.Pipe(true)
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	start := time.Now()
	argv := append([]string{bin}, args...)
	pid, exited, err := 0, (<-chan syscall.WaitStatus)(nil), spawn.ErrHelperGone
	if hs := spawnHelpers.Load(); hs != nil && len(*hs) > 0 {
		h := (*hs)[spawnNext.Add(1)%uint64(len(*hs))]
		pid, exited, err = h.Start(bin, argv, env, dir, inR, outW, errW)
	}
	if errors.Is(err, spawn.ErrHelperGone) {
		pid, err = spawn.Start(bin, argv, env, dir, inR, outW, errW)
		exited = waitPid(pid)
	}
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, err
	}
	mSpawn.Since(start)
	p := &Proc{pid: pid, stdin: inW, stdout: outR, stderr: newRing(64 << 10), done: make(chan struct{}), linesDone: make(chan struct{})}
	p.tree = track(pid)
	copied := make(chan struct{})
	go func() {
		io.Copy(p.stderr, errR)
		close(copied)
	}()
	go p.wait(func() error {
		ws := <-exited
		p.afterExit(func() { // like exec.Cmd.Wait: stderr is complete once Wait returns
			select {
			case <-copied:
			case <-time.After(drainGrace):
				errR.Close()
				<-copied
			}
			errR.Close()
		}, outR)
		return spawn.ExitErr(ws)
	})
	return p, nil
}

// RunCommand runs bin to completion and returns its stdout and stderr,
// through the spawn helpers when they are in use (a fork of this large
// process costs milliseconds of CPU each). stdin is empty; ctx kills it.
func RunCommand(ctx context.Context, bin string, args []string, dir string, env []string) (stdout, stderr []byte, err error) {
	hs := spawnHelpers.Load()
	if hs == nil || len(*hs) == 0 {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = dir, env
		// Kill the whole group on ctx: a child of bin (sh -c "sleep 30")
		// would otherwise keep the output pipes, and Run, open.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = drainGrace
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.Bytes(), errb.Bytes(), err
	}
	if !strings.Contains(bin, "/") {
		if bin, err = exec.LookPath(bin); err != nil {
			return nil, nil, err
		}
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		return nil, nil, err
	}
	defer null.Close()
	outR, outW, err := spawn.Pipe(true)
	if err != nil {
		return nil, nil, err
	}
	errR, errW, err := spawn.Pipe(true)
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, nil, err
	}
	h := (*hs)[spawnNext.Add(1)%uint64(len(*hs))]
	pid, exited, err := h.Start(bin, append([]string{bin}, args...), env, dir, null, outW, errW)
	if errors.Is(err, spawn.ErrHelperGone) {
		pid, err = spawn.Start(bin, append([]string{bin}, args...), env, dir, null, outW, errW)
		exited = waitPid(pid)
	}
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { syscall.Kill(-pid, syscall.SIGKILL) })
	defer stop()
	var errBuf []byte
	done := make(chan struct{})
	go func() { errBuf, _ = io.ReadAll(errR); close(done) }()
	outBuf, _ := io.ReadAll(outR)
	<-done
	outR.Close()
	errR.Close()
	err = spawn.ExitErr(<-exited)
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return outBuf, errBuf, err
}

// waitPid reaps a child of this process.
func waitPid(pid int) <-chan syscall.WaitStatus {
	ch := make(chan syscall.WaitStatus, 1)
	go func() {
		var ws syscall.WaitStatus
		for {
			if _, err := syscall.Wait4(pid, &ws, 0, nil); err != syscall.EINTR {
				break
			}
		}
		ch <- ws
	}()
	return ch
}

// spawnHelpers, when set, create agent processes in turn (see spawn.Helper;
// each serves one spawn at a time).
var (
	spawnHelpers atomic.Pointer[[]*spawn.Helper]
	spawnNext    atomic.Uint64
)

// UseSpawnHelpers makes StartProc spawn through hs (none: in-process).
func UseSpawnHelpers(hs ...*spawn.Helper) { spawnHelpers.Store(&hs) }

// afterExit runs once the agent exited: it kills what is left of its
// process group, lets stderr (waitStderr) and the stdout reader reach the
// end within drainGrace, then closes stdout.
func (p *Proc) afterExit(waitStderr func(), stdout io.Closer) {
	syscall.Kill(-p.pid, syscall.SIGKILL)
	p.tree.stop() // descendants in groups of their own (agents' tools)
	if waitStderr != nil {
		waitStderr()
	}
	select {
	case <-p.linesDone:
	case <-time.After(drainGrace):
	}
	stdout.Close()
}

func (p *Proc) wait(wait func() error) {
	err := wait()
	p.scanMu.Lock()
	if p.scanErr != nil {
		err = p.scanErr
	}
	p.scanMu.Unlock()
	if err != nil {
		if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
			err = fmt.Errorf("%w: %s", err, lastLines(tail, 5))
		}
	}
	p.err = err
	close(p.done)
}

func (p *Proc) Pgid() int { return p.pid }

// WriteLine writes one line to stdin.
func (p *Proc) WriteLine(b []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.stdin.Write(append(b, '\n'))
	return err
}

// Lines calls fn for every stdout line until EOF. A line longer than
// MaxLine stops the agent: nothing would read its output any more.
func (p *Proc) Lines(fn func([]byte)) error {
	defer close(p.linesDone)
	sc := bufio.NewScanner(p.stdout)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	for sc.Scan() {
		p.lastOut.Store(time.Now().UnixNano())
		fn(sc.Bytes())
	}
	err := sc.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		p.scanMu.Lock()
		p.scanErr = fmt.Errorf("the agent wrote an output line longer than %d MiB", MaxLine>>20)
		p.scanMu.Unlock()
		syscall.Kill(-p.pid, syscall.SIGKILL)
	}
	return err
}

// LastOutput is when the agent last wrote a line (zero: never).
func (p *Proc) LastOutput() time.Time {
	if n := p.lastOut.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

func (p *Proc) Done() <-chan struct{} { return p.done }
func (p *Proc) Err() error            { return p.err }
func (p *Proc) StderrTail() string    { return p.stderr.String() }

// Stop closes stdin, then terminates the process group: SIGTERM, wait,
// SIGKILL, and a few extra sweeps for stragglers.
func (p *Proc) Stop(grace time.Duration) {
	p.writeMu.Lock()
	p.stdin.Close()
	p.writeMu.Unlock()
	select {
	case <-p.done:
	case <-time.After(grace):
	}
	KillGroup(p.Pgid())
	<-p.done
}

// KillGroup terminates a process group (also used for orphans after a
// daemon restart).
func KillGroup(pgid int) {
	if pgid <= 1 {
		return
	}
	syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		syscall.Kill(-pgid, syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
	}
}

type ring struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, b...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(b), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(bytes.ToValidUTF8(r.buf, nil))
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Truncate cuts s to at most n bytes on a rune boundary and strips NULs.
func Truncate(s string, n int) (string, bool) {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// LookPath finds bin on PATH, then in extra locations.
func LookPath(bin string, path string, extra ...string) string {
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, bin)
		if isExec(p) {
			return p
		}
	}
	for _, p := range extra {
		if isExec(p) {
			return p
		}
	}
	return ""
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

var errNotRunning = errors.New("provider process is not running")

// mSpawn is the time to create an agent process (with os/exec this grows
// under concurrency: Go serializes forks process-wide).
var (
	mSpawn         = metrics.Default.Histogram("hermesh_provider_spawn_seconds")
	mSpawnFallback = metrics.Default.Counter("hermesh_provider_spawn_fallback_total")
)
