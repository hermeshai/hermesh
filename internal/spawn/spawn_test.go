//go:build darwin

package spawn

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStart(t *testing.T) {
	if !Supported {
		t.Skip("unsupported")
	}
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	pid, err := Start("/bin/sh", []string{"sh", "-c", "pwd -P; cat; echo $X; echo oops >&2; ls /dev/fd | wc -l"}, []string{"X=1"}, dir, inR, outW, errW)
	if err != nil {
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	errW.Close()
	inW.WriteString("hello\n")
	inW.Close()
	out, _ := io.ReadAll(outR)
	serr, _ := io.ReadAll(errR)
	p, _ := os.FindProcess(pid)
	st, err := p.Wait()
	if err != nil || !st.Success() {
		t.Fatal(err, st)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 4 || lines[0] != dir || lines[1] != "hello" || lines[2] != "1" {
		t.Fatalf("out=%q", out)
	}
	if lines[3] != "5" { // 0,1,2 plus the two ls opens itself: nothing leaked
		t.Fatalf("child saw %s fds, want 5", lines[3])
	}
	if strings.TrimSpace(string(serr)) != "oops" {
		t.Fatalf("stderr=%q", serr)
	}
	if _, err := Start("/nonexistent", []string{"x"}, nil, "", inR, outW, errW); err == nil {
		t.Fatal("want error")
	}
}

func TestPipeAboveOpenMax(t *testing.T) {
	if !Supported {
		t.Skip("unsupported")
	}
	var l syscall.Rlimit
	syscall.Getrlimit(syscall.RLIMIT_NOFILE, &l)
	if l.Max < 20000 {
		t.Skip("hard limit too low")
	}
	// The kernel caps descriptors per process too (GitHub's macOS runners
	// allow fewer than OPEN_MAX); then Pipe keeps both ends low, by design.
	if max, err := unix.SysctlUint32("kern.maxfilesperproc"); err == nil && max <= openMax {
		t.Skipf("kern.maxfilesperproc %d is not above OPEN_MAX", max)
	}
	n := l
	n.Cur = 20000
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &n); err != nil {
		t.Fatal(err)
	}
	defer syscall.Setrlimit(syscall.RLIMIT_NOFILE, &l)
	outR, outW, err := Pipe(true)
	if err != nil {
		t.Fatal(err)
	}
	if outR.Fd() < openMax || outW.Fd() >= openMax {
		t.Fatalf("parent fd %d, child fd %d", outR.Fd(), outW.Fd())
	}
	null, _ := os.Open(os.DevNull)
	pid, err := Start("/bin/echo", []string{"echo", "hi"}, nil, "", null, outW, outW)
	if err != nil {
		t.Fatal(err)
	}
	outW.Close()
	b, _ := io.ReadAll(outR)
	p, _ := os.FindProcess(pid)
	p.Wait()
	if string(b) != "hi\n" {
		t.Fatalf("got %q", b)
	}
	// a child end above OPEN_MAX is refused up front, not by libc
	if _, err := Start("/bin/echo", []string{"echo"}, nil, "", null, outR, outR); err != ErrFDRange {
		t.Fatalf("err = %v", err)
	}
}
