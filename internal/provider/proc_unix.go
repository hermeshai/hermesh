//go:build unix

package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/hermeshai/hermesh/internal/spawn"
)

// setGroup puts the child in a process group of its own, so that the
// whole group can be ended at once.
func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// joinGroup has nothing to do: the group exists from the start.
func joinGroup(cmd *exec.Cmd) error { return nil }

// killGroup kills a process group now.
func killGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

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

// runSpawned is RunCommand through the spawn helpers.
func runSpawned(ctx context.Context, hs []*spawn.Helper, bin string, args []string, dir string, env []string) (stdout, stderr []byte, err error) {
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
	h := hs[spawnNext.Add(1)%uint64(len(hs))]
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
