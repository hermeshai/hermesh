//go:build unix

package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/spawn"
)

func TestRunCommand(t *testing.T) {
	modes := []string{"in-process"}
	if spawn.Supported {
		modes = append(modes, "helper")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			if mode == "helper" {
				exe, _ := os.Executable()
				os.Setenv("HERMESH_TEST_SPAWN_HELPER", "1")
				h, err := spawn.StartHelper(exe)
				os.Unsetenv("HERMESH_TEST_SPAWN_HELPER")
				if err != nil {
					t.Fatal(err)
				}
				UseSpawnHelpers(h)
				defer func() { UseSpawnHelpers(); h.Close() }()
			}
			ctx := context.Background()
			dir := t.TempDir()
			out, errOut, err := RunCommand(ctx, "sh", []string{"-c", "pwd -P; echo $X; echo warn >&2"}, dir, []string{"X=7", "PATH=/usr/bin:/bin"})
			if err != nil || !strings.HasSuffix(strings.Fields(string(out))[0], strings.TrimPrefix(dir, "/private")) || strings.Fields(string(out))[1] != "7" || strings.TrimSpace(string(errOut)) != "warn" {
				t.Fatalf("out=%q err=%q %v", out, errOut, err)
			}
			var exit interface{ ExitCode() int }
			if _, _, err := RunCommand(ctx, "sh", []string{"-c", "exit 4"}, "", nil); !errors.As(err, &exit) || exit.ExitCode() != 4 || !strings.Contains(err.Error(), "exit status 4") {
				t.Fatalf("exit: %v", err)
			}
			tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			if _, _, err := RunCommand(tctx, "sh", []string{"-c", "sleep 30; true"}, "", nil); err == nil || time.Since(start) > 5*time.Second {
				t.Fatalf("timeout: %v after %v", err, time.Since(start))
			}
		})
	}
}

// procModes runs f with each way StartProc can create a process.
func procModes(t *testing.T, f func(t *testing.T)) {
	modes := []string{"exec"}
	if spawn.Supported {
		modes = append(modes, "spawn", "helper")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			defer func(v bool) { useSpawn = v }(useSpawn)
			useSpawn = mode != "exec"
			if mode == "helper" {
				exe, _ := os.Executable()
				os.Setenv("HERMESH_TEST_SPAWN_HELPER", "1")
				h, err := spawn.StartHelper(exe)
				os.Unsetenv("HERMESH_TEST_SPAWN_HELPER")
				if err != nil {
					t.Fatal(err)
				}
				UseSpawnHelpers(h)
				defer func() { UseSpawnHelpers(); h.Close() }()
			}
			f(t)
		})
	}
}

func startSh(t *testing.T, script string) *Proc {
	t.Helper()
	p, err := StartProc(context.Background(), "/bin/sh", []string{"-c", script}, t.TempDir(), []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waitDone(t *testing.T, p *Proc, within time.Duration) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(within):
		t.Fatalf("process not done after %v", within)
	}
}

// Every line written before exit is read, even when the agent exits right
// after writing (closing stdout at exit used to drop the tail).
func TestProcReadsAllOutputBeforeExit(t *testing.T) {
	procModes(t, func(t *testing.T) {
		p := startSh(t, "i=0; while [ $i -lt 3000 ]; do echo line$i; i=$((i+1)); done")
		n := 0
		p.Lines(func([]byte) { n++ })
		waitDone(t, p, 10*time.Second)
		if n != 3000 || p.LastOutput().IsZero() {
			t.Fatalf("read %d lines, last output %v", n, p.LastOutput())
		}
	})
}

// A descendant that escaped the process group and keeps the pipes open
// must not keep the session open past the drain grace.
func TestProcDescendantHoldingPipes(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("needs python3 for setsid")
	}
	procModes(t, func(t *testing.T) {
		p := startSh(t, `/usr/bin/python3 -c 'import os,time; os.setsid(); time.sleep(30)' & echo started`)
		var got []string
		start := time.Now()
		p.Lines(func(b []byte) { got = append(got, string(b)) })
		waitDone(t, p, 10*time.Second)
		if len(got) != 1 || got[0] != "started" || time.Since(start) > 6*time.Second {
			t.Fatalf("lines %q after %v", got, time.Since(start))
		}
	})
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// pidOf reads the first line the process prints (a pid).
func pidOf(t *testing.T, p *Proc) int {
	t.Helper()
	ch := make(chan int, 1)
	go p.Lines(func(b []byte) {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			select {
			case ch <- n:
			default:
			}
		}
	})
	select {
	case n := <-ch:
		return n
	case <-time.After(10 * time.Second):
		t.Fatal("no pid printed")
		return 0
	}
}

// A tool that left the agent's process group (setsid, as agents run their
// tools) dies with the agent, even when the agent is killed first.
func TestEscapedDescendantsDieWithAgent(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("needs python3 for setsid")
	}
	procModes(t, func(t *testing.T) {
		p := startSh(t, `/usr/bin/python3 -c 'import os,time; os.setsid(); time.sleep(60)' & echo $!; sleep 60`)
		tool := pidOf(t, p)
		bystander := exec.Command("/bin/sleep", "60")
		bystander.Start()
		defer bystander.Process.Kill()
		time.Sleep(trackEvery + 500*time.Millisecond) // the agent ran for a while
		syscall.Kill(p.Pgid(), syscall.SIGKILL)       // the agent crashes (not its group)
		waitDone(t, p, 10*time.Second)
		if alive(tool) {
			syscall.Kill(tool, syscall.SIGKILL)
			t.Fatal("the agent's tool outlived it")
		}
		if !alive(bystander.Process.Pid) {
			t.Fatal("an unrelated process was killed")
		}
	})
}

// KillTree (recovery after a daemon crash) reaches descendants that left
// the group of an agent it never tracked.
func TestKillTree(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("needs python3 for setsid")
	}
	c := exec.Command("/bin/sh", "-c", `/usr/bin/python3 -c 'import os,time; os.setsid(); time.sleep(60)' & echo $!; sleep 60`)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := c.StdoutPipe()
	c.Start()
	go c.Wait()
	var line [32]byte
	n, _ := out.Read(line[:])
	tool, _ := strconv.Atoi(strings.TrimSpace(string(line[:n])))
	KillTree(c.Process.Pid)
	time.Sleep(200 * time.Millisecond)
	if alive(tool) || alive(c.Process.Pid) {
		syscall.Kill(tool, syscall.SIGKILL)
		t.Fatalf("left alive: tool %v, agent %v", alive(tool), alive(c.Process.Pid))
	}
}

// An output up to 1 MiB is kept whole; a longer one keeps its first and
// last half (where a build log's error is), saying how much went.
func TestKeepOutput(t *testing.T) {
	if s, cut := KeepOutput("ok\x00\n"); s != "ok\n" || cut {
		t.Errorf("short: %q %v", s, cut)
	}
	big := strings.Repeat("a", 2<<20) + "ERROR at the end\n"
	s, cut := KeepOutput(big)
	if !cut || !strings.HasSuffix(s, "ERROR at the end\n") || !strings.HasPrefix(s, "aaa") || !strings.Contains(s, "bytes left out") || len(s) > KeptOutput+64 {
		t.Errorf("long: %d bytes, cut=%v, ends %q", len(s), cut, s[len(s)-20:])
	}
}
