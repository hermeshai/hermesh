package runtime

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
	bin, args, err := startable(bin, args)
	if err != nil {
		return nil, err
	}
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
	setGroup(cmd)
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
	if err := joinGroup(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		outR.Close()
		return nil, err
	}
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

// RunCommand runs bin to completion and returns its stdout and stderr,
// through the spawn helpers when they are in use (a fork of this large
// process costs milliseconds of CPU each). stdin is empty; ctx kills it.
func RunCommand(ctx context.Context, bin string, args []string, dir string, env []string) (stdout, stderr []byte, err error) {
	bin, args, err = startable(bin, args)
	if err != nil {
		return nil, nil, err
	}
	if hs := spawnHelpers.Load(); hs != nil && len(*hs) > 0 {
		return runSpawned(ctx, bin, args, dir, env)
	}
	if hasSpawnPool() {
		return runSpawned(ctx, bin, args, dir, env)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, env
	// Kill the whole group on ctx: a child of bin (sh -c "sleep 30")
	// would otherwise keep the output pipes, and Run, open.
	setGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = drainGrace
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	if err := joinGroup(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, nil, err
	}
	err = cmd.Wait()
	releaseGroup(cmd.Process.Pid)
	return out.Bytes(), errb.Bytes(), err
}

// spawnHelpers, when set, create agent processes in turn (see spawn.Helper;
// each serves one spawn at a time). Production daemons configure a lazy pool
// (ConfigureSpawnHelperPool) so zero helper processes run until a session
// actually spawns, scaling up to limit only under concurrent spawns.
var (
	spawnHelpers atomic.Pointer[[]*spawn.Helper]
	spawnNext    atomic.Uint64

	spawnPoolMu    sync.Mutex
	spawnPoolLimit int
	spawnPoolStart func() (*spawn.Helper, error)
	spawnPoolSlots []*spawnSlot
)

type spawnSlot struct {
	h    *spawn.Helper
	busy atomic.Bool
}

// UseSpawnHelpers makes StartProc spawn through hs (none: in-process).
func UseSpawnHelpers(hs ...*spawn.Helper) { spawnHelpers.Store(&hs) }

// ConfigureSpawnHelperPool configures an on-demand pool of up to limit spawn
// helpers created via start. The returned function closes all started helpers.
func ConfigureSpawnHelperPool(limit int, start func() (*spawn.Helper, error)) func() {
	spawnPoolMu.Lock()
	spawnPoolLimit = limit
	spawnPoolStart = start
	spawnPoolSlots = nil
	spawnPoolMu.Unlock()
	return func() {
		spawnPoolMu.Lock()
		slots := spawnPoolSlots
		spawnPoolSlots = nil
		spawnPoolLimit = 0
		spawnPoolStart = nil
		spawnPoolMu.Unlock()
		for _, s := range slots {
			s.h.Close()
		}
	}
}

func hasSpawnPool() bool {
	spawnPoolMu.Lock()
	ok := spawnPoolLimit > 0 && spawnPoolStart != nil
	spawnPoolMu.Unlock()
	return ok
}

func acquireSpawnHelper() (*spawn.Helper, func(dead bool)) {
	if hs := spawnHelpers.Load(); hs != nil && len(*hs) > 0 {
		h := (*hs)[spawnNext.Add(1)%uint64(len(*hs))]
		return h, func(bool) {}
	}
	spawnPoolMu.Lock()
	for _, s := range spawnPoolSlots {
		if s.busy.CompareAndSwap(false, true) {
			h := s.h
			spawnPoolMu.Unlock()
			return h, func(dead bool) {
				s.busy.Store(false)
				if dead {
					evictSpawnSlot(s)
				}
			}
		}
	}
	canGrow := spawnPoolStart != nil && len(spawnPoolSlots) < spawnPoolLimit
	start := spawnPoolStart
	var fallback *spawnSlot
	if !canGrow && len(spawnPoolSlots) > 0 {
		fallback = spawnPoolSlots[spawnNext.Add(1)%uint64(len(spawnPoolSlots))]
	}
	spawnPoolMu.Unlock()

	if canGrow {
		h, err := start()
		if err == nil && h != nil {
			slot := &spawnSlot{h: h}
			slot.busy.Store(true)
			spawnPoolMu.Lock()
			if spawnPoolStart == nil {
				spawnPoolMu.Unlock()
				h.Close()
				return nil, func(bool) {}
			}
			spawnPoolSlots = append(spawnPoolSlots, slot)
			spawnPoolMu.Unlock()
			return h, func(dead bool) {
				slot.busy.Store(false)
				if dead {
					evictSpawnSlot(slot)
				}
			}
		}
		spawnPoolMu.Lock()
		if len(spawnPoolSlots) > 0 {
			fallback = spawnPoolSlots[spawnNext.Add(1)%uint64(len(spawnPoolSlots))]
		}
		spawnPoolMu.Unlock()
	}
	if fallback != nil {
		return fallback.h, func(dead bool) {
			if dead {
				evictSpawnSlot(fallback)
			}
		}
	}
	return nil, func(bool) {}
}

func evictSpawnSlot(target *spawnSlot) {
	spawnPoolMu.Lock()
	for i, s := range spawnPoolSlots {
		if s == target {
			spawnPoolSlots = append(spawnPoolSlots[:i], spawnPoolSlots[i+1:]...)
			spawnPoolMu.Unlock()
			target.h.Close()
			return
		}
	}
	spawnPoolMu.Unlock()
}

// afterExit runs once the agent exited: it kills what is left of its
// process group, lets stderr (waitStderr) and the stdout reader reach the
// end within drainGrace, then closes stdout.
func (p *Proc) afterExit(waitStderr func(), stdout io.Closer) {
	killGroup(p.pid)
	p.tree.stop() // descendants in groups of their own (agents' tools)
	releaseGroup(p.pid)
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
		killGroup(p.pid)
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

func (p *Proc) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
}
func (p *Proc) Err() error         { return p.err }
func (p *Proc) StderrTail() string { return p.stderr.String() }

// Diagnosis is what an agent's process is doing, for the report of a
// stall: the programs still running under it, newest first (what it may
// be waiting for: an MCP server starting, a credential prompt, a
// download), and the last lines of its stderr. Windows lists no programs.
type Diagnosis struct {
	Running    []string
	Stderr     string
	LastOutput time.Time
	CPUTicks   uint64
	HasCPU     bool
}

// Diagnose looks at the process now.
func (p *Proc) Diagnose() Diagnosis {
	if p == nil {
		return Diagnosis{}
	}
	var d Diagnosis
	if p.tree != nil {
		for _, args := range p.tree.running() {
			cmd := strings.Join(append([]string{filepath.Base(args[0])}, args[1:]...), " ")
			d.Running = append(d.Running, clip(Redact(cmd), 200))
		}
	}
	d.Stderr = Redact(lastLines(strings.TrimSpace(p.stderr.String()), 10))
	d.LastOutput = p.LastOutput()
	d.CPUTicks, d.HasCPU = p.tree.cpuTicks(p.pid)
	return d
}

// SampleLiveness reads the process's last output timestamp and cumulative CPU
// counters without scanning the OS process table or redacting stderr.
func (p *Proc) SampleLiveness() (time.Time, uint64, bool) {
	if p == nil {
		return time.Time{}, 0, false
	}
	ticks, hasCPU := p.tree.cpuTicks(p.pid)
	return p.LastOutput(), ticks, hasCPU
}

// SnapshotBaseline records the agent's current descendant processes as its
// standing baseline (e.g. MCP stdio servers or detached daemons) so
// HasActiveDescendants only reports newly-spawned tool commands.
func (p *Proc) SnapshotBaseline() {
	if p == nil || p.tree == nil {
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	p.tree.snapshotBaseline()
}

// HasActiveDescendants reports whether the agent process currently has any
// live descendant process outside its recorded baseline.
func (p *Proc) HasActiveDescendants() bool {
	if p == nil || p.tree == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
	}
	return p.tree.hasActiveDescendants()
}

// TransientNoticeFromStderr extracts a concise single-line retry or rate-limit
// status hint from a provider's recent stderr lines when present.
func TransientNoticeFromStderr(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	parts := strings.Split(stderr, "|")
	for i := len(parts) - 1; i >= 0; i-- {
		line := strings.TrimSpace(parts[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		isRetry := strings.Contains(lower, "retrying") ||
			strings.Contains(lower, "retry in") ||
			strings.Contains(lower, "overloaded") ||
			strings.Contains(lower, "rate limit") ||
			strings.Contains(lower, "rate_limit") ||
			strings.Contains(lower, "503") ||
			strings.Contains(lower, "529") ||
			strings.Contains(lower, "temporarily unavailable") ||
			strings.Contains(lower, "service is currently unavailable")
		if isRetry {
			return clip(Redact(line), 160)
		}
	}
	return ""
}

// String is d on one line for a failure message: the newest programs and
// the end of stderr.
func (d Diagnosis) String() string {
	var parts []string
	if n := len(d.Running); n > 0 {
		s := "running: " + strings.Join(d.Running[:min(n, 5)], ", ")
		if n > 5 {
			s += fmt.Sprintf(" and %d more", n-5)
		}
		parts = append(parts, s)
	}
	if d.Stderr != "" {
		s := d.Stderr
		if i := len(s) - 300; i > 0 {
			for i < len(s) && !utf8.RuneStart(s[i]) {
				i++
			}
			s = "…" + s[i:]
		}
		parts = append(parts, "stderr: "+s)
	}
	return strings.Join(parts, "; ")
}

// clip cuts s to n bytes, marking the cut.
func clip(s string, n int) string {
	if t, cut := Truncate(s, n); cut {
		return t + "…"
	}
	return s
}

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

// KeptOutput bytes of a tool's output are kept at most.
const KeptOutput = 1 << 20

// KeepOutput is a tool's output as it is recorded: whole up to KeptOutput
// bytes; past that its first and last half, with a line between saying how
// much was left out (cut: true).
func KeepOutput(s string) (kept string, cut bool) {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) <= KeptOutput {
		return s, false
	}
	head, tail := KeptOutput/2, len(s)-KeptOutput/2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return s[:head] + fmt.Sprintf("\n… %d bytes left out …\n", tail-head) + s[tail:], true
}

// Command is exec.CommandContext for an agent's CLI, by this system's
// rules for what may run (on Windows npm's shims give way to the program
// they start, and no console window opens).
func Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	b, a, err := startable(bin, args)
	if err != nil {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Err = err
		return cmd
	}
	cmd := exec.CommandContext(ctx, b, a...)
	hideWindow(cmd)
	return cmd
}

// LookPath finds bin on PATH, then in extra locations.
func LookPath(bin string, path string, extra ...string) string {
	for _, dir := range filepath.SplitList(path) {
		if p := executable(filepath.Join(dir, bin)); p != "" {
			return p
		}
	}
	for _, p := range extra {
		if p := executable(p); p != "" {
			return p
		}
	}
	return ""
}

var errNotRunning = errors.New("provider process is not running")

// mSpawn is the time to create an agent process (with os/exec this grows
// under concurrency: Go serializes forks process-wide).
var (
	mSpawn         = metrics.Default.Histogram("hermesh_provider_spawn_seconds")
	mSpawnFallback = metrics.Default.Counter("hermesh_provider_spawn_fallback_total")
)
