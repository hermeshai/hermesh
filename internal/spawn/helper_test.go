//go:build darwin

package spawn

import (
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("HERMESH_SPAWN_HELPER_TEST") == "1" {
		Serve(os.NewFile(3, "helper"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startTestHelper(t *testing.T) *Helper {
	t.Helper()
	exe, _ := os.Executable()
	os.Setenv("HERMESH_SPAWN_HELPER_TEST", "1")
	h, err := StartHelper(exe)
	os.Unsetenv("HERMESH_SPAWN_HELPER_TEST")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func run(t *testing.T, h *Helper, script, input string) (out, errOut string, pid int, ws syscall.WaitStatus) {
	t.Helper()
	inW, inR, _ := Pipe(false)
	outR, outW, _ := Pipe(true)
	errR, errW, _ := Pipe(true)
	pid, exited, err := h.Start("/bin/sh", []string{"sh", "-c", script}, []string{"X=1"}, "/", inR, outW, errW)
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		t.Fatal(err)
	}
	inW.WriteString(input)
	inW.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { b, _ := io.ReadAll(errR); errOut = string(b); wg.Done() }()
	b, _ := io.ReadAll(outR)
	wg.Wait()
	select {
	case ws = <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("no exit")
	}
	return string(b), errOut, pid, ws
}

func TestHelper(t *testing.T) {
	h := startTestHelper(t)
	defer h.Close()
	out, errOut, pid, ws := run(t, h, `cat; echo $X; pwd; echo oops >&2; ps -o ppid=,pgid= -p $$`, "hello\n")
	f := strings.Fields(out)
	if len(f) != 5 || f[0] != "hello" || f[1] != "1" || f[2] != "/" || strings.TrimSpace(errOut) != "oops" || !ws.Exited() || ws.ExitStatus() != 0 {
		t.Fatalf("out=%q err=%q ws=%v", out, errOut, ws)
	}
	if f[3] != strconv.Itoa(h.Pid()) || f[4] != strconv.Itoa(pid) {
		t.Fatalf("ppid/pgid = %s/%s, want helper %d / own pid %d", f[3], f[4], h.Pid(), pid)
	}
	if _, _, _, ws := run(t, h, "exit 3", ""); ws.ExitStatus() != 3 {
		t.Fatalf("exit status %v", ws)
	}
	if _, _, _, ws := run(t, h, "kill -TERM $$", ""); !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("signal status %v", ws)
	}
	if _, _, err := h.Start("/nonexistent", []string{"x"}, nil, "", os.Stdin, os.Stdout, os.Stderr); err == nil {
		t.Fatal("want error")
	}
	// many concurrent spawns, children that exit at once included
	var wg sync.WaitGroup
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			null, _ := os.Open(os.DevNull)
			defer null.Close()
			_, exited, err := h.Start("/usr/bin/true", []string{"true"}, nil, "", null, null, null)
			if err != nil {
				t.Error(err)
				return
			}
			select {
			case ws := <-exited:
				if ws.ExitStatus() != 0 {
					t.Errorf("status %v", ws)
				}
			case <-time.After(20 * time.Second):
				t.Error("lost an exit")
			}
		}()
	}
	wg.Wait()
}

func TestHelperDies(t *testing.T) {
	h := startTestHelper(t)
	null, _ := os.Open(os.DevNull)
	defer null.Close()
	pid, exited, err := h.Start("/bin/sleep", []string{"sleep", "1"}, nil, "", null, null, null)
	if err != nil {
		t.Fatal(err)
	}
	h.proc.Kill()
	<-h.Dead()
	if _, _, err := h.Start("/usr/bin/true", []string{"true"}, nil, "", null, null, null); err == nil {
		t.Fatal("spawn after helper death should fail")
	}
	select {
	case ws := <-exited:
		if ws != unknownStatus {
			t.Fatalf("status %v", ws)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("orphan %d not noticed", pid)
	}
}

// TestHelperLargeRequests: environments far beyond the socket buffer, from
// many goroutines at once.
func TestHelperLargeRequests(t *testing.T) {
	h := startTestHelper(t)
	defer h.Close()
	env := []string{"BIG=" + strings.Repeat("x", 256<<10)}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			null, _ := os.Open(os.DevNull)
			defer null.Close()
			_, exited, err := h.Start("/bin/sh", []string{"sh", "-c", `[ ${#BIG} -eq 262144 ]`}, env, "", null, null, null)
			if err != nil {
				t.Error(err)
				return
			}
			if ws := <-exited; ws.ExitStatus() != 0 {
				t.Errorf("child saw a truncated environment: %v", ws)
			}
		}()
	}
	wg.Wait()
}
