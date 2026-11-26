package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	stdruntime "runtime"
	"time"
)

// RunShellStreaming executes command in the machine's shell in dir with merged
// stdout/stderr streamed to onChunk. stdin is /dev/null (non-interactive), and
// cancelling ctx terminates the whole process group / Windows job object.
func RunShellStreaming(ctx context.Context, command, dir string, env []string, onChunk func([]byte)) (int, error) {
	var bin string
	var args []string
	if stdruntime.GOOS == "windows" {
		bin = "powershell.exe"
		args = []string{"-NoProfile", "-NonInteractive", "-Command", command}
	} else {
		bin = os.Getenv("SHELL")
		if bin == "" {
			bin = "/bin/sh"
		}
		args = []string{"-c", command}
	}
	var err error
	bin, args, err = startable(bin, args)
	if err != nil {
		return -1, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = outW
	cmd.Stderr = outW
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		return -1, err
	}
	outW.Close()
	if err := joinGroup(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		outR.Close()
		return -1, err
	}
	pid := cmd.Process.Pid
	if ctx.Err() != nil {
		_ = killGroup(pid)
	}
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = killGroup(pid)
		case <-stopWatch:
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 8192)
		for {
			n, rErr := outR.Read(buf)
			if n > 0 && onChunk != nil {
				onChunk(buf[:n])
			}
			if rErr != nil {
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	close(stopWatch)
	_ = killGroup(pid)
	releaseGroup(pid)
	select {
	case <-readDone:
	case <-time.After(drainGrace):
		_ = outR.Close()
		<-readDone
	}
	_ = outR.Close()
	if waitErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, waitErr
}
