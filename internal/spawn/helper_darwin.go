//go:build darwin

package spawn

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// The kernel copies the parent's whole descriptor table on every spawn
// while holding its descriptor lock, so spawning from a daemon that holds
// three pipes per agent gets slower with every agent and stalls the
// daemon's other I/O meanwhile. A Helper is a small process with almost no
// descriptors that spawns on the daemon's behalf: the child's stdio
// travels over a socketpair (SCM_RIGHTS), and the helper, as the parent,
// reports each child's exit.
//
// Frames in both directions are a 4-byte big-endian length and JSON. A
// request carries its three descriptors on its header.

type request struct {
	ID   uint64   `json:"id"`
	Path string   `json:"path"`
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
}

type reply struct {
	ID     uint64 `json:"id,omitempty"`
	Pid    int    `json:"pid,omitempty"`
	Err    string `json:"err,omitempty"`
	Exit   bool   `json:"exit,omitempty"`
	Status uint32 `json:"status,omitempty"` // wait status
}

// sockBuf sizes both socket buffers (the macOS default is 8 KiB).
const sockBuf = 1 << 20

func frame(v any) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out
}

// Serve runs the helper on conn (fd 3 of the helper process) until the
// daemon closes it. Requests are handled one at a time, so a reply always
// precedes the next spawn and a reused pid cannot be confused with an
// earlier child.
func Serve(f *os.File) error {
	// The daemon decides when children stop; a Ctrl-C to the process
	// group must not take the helper (and with it exit reporting) first.
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	fc, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return err
	}
	c := fc.(*net.UnixConn)
	c.SetReadBuffer(sockBuf)
	c.SetWriteBuffer(sockBuf)
	var wmu sync.Mutex
	send := func(r reply) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := c.Write(frame(r))
		return err
	}
	kids := make(chan struct{}, 1<<20)
	go func() { // reap children; one token per live child
		for range kids {
			var ws syscall.WaitStatus
			for {
				pid, err := syscall.Wait4(-1, &ws, 0, nil)
				if err == syscall.EINTR {
					continue
				}
				if err == nil {
					send(reply{Exit: true, Pid: pid, Status: uint32(ws)})
				}
				break
			}
		}
	}()
	hdr := make([]byte, 4)
	oob := make([]byte, syscall.CmsgSpace(3*4))
	for {
		n, oobn, _, _, err := c.ReadMsgUnix(hdr, oob)
		if err != nil || n == 0 {
			return nil // the daemon is gone
		}
		if n < 4 {
			if _, err := io.ReadFull(c, hdr[n:]); err != nil {
				return nil
			}
		}
		fds, _ := parseRights(oob[:oobn])
		body := make([]byte, binary.BigEndian.Uint32(hdr))
		if _, err := io.ReadFull(c, body); err != nil {
			closeAll(fds)
			return nil
		}
		var req request
		r := reply{}
		if err := json.Unmarshal(body, &req); err != nil {
			r.Err = "bad request: " + err.Error()
		} else if len(fds) != 3 {
			r.ID, r.Err = req.ID, fmt.Sprintf("want 3 descriptors, got %d", len(fds))
		} else {
			r.ID = req.ID
			files := []*os.File{os.NewFile(uintptr(fds[0]), "stdin"), os.NewFile(uintptr(fds[1]), "stdout"), os.NewFile(uintptr(fds[2]), "stderr")}
			pid, err := Start(req.Path, req.Argv, req.Env, req.Dir, files[0], files[1], files[2])
			for _, f := range files {
				f.Close()
			}
			fds = nil
			if err != nil {
				r.Err = err.Error()
			} else {
				r.Pid = pid
			}
		}
		closeAll(fds)
		if err := send(r); err != nil {
			return nil
		}
		if r.Pid != 0 {
			kids <- struct{}{}
		}
	}
}

func parseRights(oob []byte) ([]int, error) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var fds []int
	for _, m := range msgs {
		f, err := syscall.ParseUnixRights(&m)
		if err == nil {
			fds = append(fds, f...)
		}
	}
	return fds, nil
}

func closeAll(fds []int) {
	for _, fd := range fds {
		syscall.Close(fd)
	}
}

type spawned struct {
	reply
	exited chan syscall.WaitStatus
}

// Helper is the daemon's side of a running helper process.
type Helper struct {
	c    *net.UnixConn
	proc *os.Process
	wmu  sync.Mutex
	dead chan struct{}

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan spawned
	waiters map[int]chan syscall.WaitStatus
	early   map[int]syscall.WaitStatus // exits that arrived before their reply
}

// StartHelper starts exe with args as a helper (the command must call
// Serve on fd 3).
func StartHelper(exe string, args ...string) (*Helper, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, os.NewSyscallError("socketpair", err)
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	ours, theirs := os.NewFile(uintptr(fds[0]), "helper"), os.NewFile(uintptr(fds[1]), "helper-child")
	cmd := exec.Command(exe, args...)
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	err = cmd.Start()
	theirs.Close()
	if err != nil {
		ours.Close()
		return nil, err
	}
	fc, err := net.FileConn(ours)
	ours.Close()
	if err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	uc := fc.(*net.UnixConn)
	uc.SetReadBuffer(sockBuf)
	uc.SetWriteBuffer(sockBuf)
	h := &Helper{c: uc, proc: cmd.Process, dead: make(chan struct{}),
		pending: map[uint64]chan spawned{}, waiters: map[int]chan syscall.WaitStatus{}, early: map[int]syscall.WaitStatus{}}
	go func() { cmd.Wait() }()
	go h.read()
	return h, nil
}

// Pid is the helper's process id.
func (h *Helper) Pid() int { return h.proc.Pid }

// Dead is closed once the helper has exited.
func (h *Helper) Dead() <-chan struct{} { return h.dead }

func (h *Helper) Close() error {
	err := h.c.Close()
	<-h.dead
	return err
}

func (h *Helper) read() {
	br := bufio.NewReaderSize(h.c, 64<<10)
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			break
		}
		body := make([]byte, binary.BigEndian.Uint32(hdr))
		if _, err := io.ReadFull(br, body); err != nil {
			break
		}
		var r reply
		if json.Unmarshal(body, &r) != nil {
			continue
		}
		h.mu.Lock()
		if r.Exit {
			ws := syscall.WaitStatus(r.Status)
			if w := h.waiters[r.Pid]; w != nil {
				w <- ws
				delete(h.waiters, r.Pid)
			} else {
				h.early[r.Pid] = ws
			}
		} else if ch := h.pending[r.ID]; ch != nil {
			delete(h.pending, r.ID)
			sp := spawned{reply: r}
			if r.Pid != 0 {
				// Register the waiter here, in stream order, before the
				// caller can see the pid.
				sp.exited = make(chan syscall.WaitStatus, 1)
				if ws, ok := h.early[r.Pid]; ok {
					sp.exited <- ws
					delete(h.early, r.Pid)
				} else {
					h.waiters[r.Pid] = sp.exited
				}
			}
			ch <- sp
		}
		h.mu.Unlock()
	}
	// The helper is gone: fail pending spawns, and watch the children it
	// left behind until they exit (their status is lost).
	h.mu.Lock()
	close(h.dead)
	for id, ch := range h.pending {
		ch <- spawned{reply: reply{ID: id, Err: ErrHelperGone.Error()}}
	}
	h.pending = nil
	for pid, w := range h.waiters {
		go watchGone(pid, w)
	}
	h.waiters = nil
	h.mu.Unlock()
}

func watchGone(pid int, w chan syscall.WaitStatus) {
	for syscall.Kill(pid, 0) == nil {
		time.Sleep(500 * time.Millisecond)
	}
	w <- unknownStatus
}

// Start spawns like the package-level Start, through the helper. exited
// receives the child's wait status once it exits.
func (h *Helper) Start(path string, argv, env []string, dir string, stdin, stdout, stderr *os.File) (pid int, exited <-chan syscall.WaitStatus, err error) {
	for _, f := range []*os.File{stdin, stdout, stderr} {
		if f.Fd() >= openMax { // SCM_RIGHTS works, but keep one rule for both paths
			return 0, nil, ErrFDRange
		}
	}
	ch := make(chan spawned, 1)
	h.mu.Lock()
	if h.pending == nil {
		h.mu.Unlock()
		return 0, nil, ErrHelperGone
	}
	h.nextID++
	id := h.nextID
	h.pending[id] = ch
	h.mu.Unlock()
	b := frame(request{ID: id, Path: path, Argv: argv, Env: env, Dir: dir})
	oob := syscall.UnixRights(int(stdin.Fd()), int(stdout.Fd()), int(stderr.Fd()))
	// The descriptors ride on the 4-byte header alone: a sendmsg carrying
	// control data must fit the socket buffer whole (EMSGSIZE otherwise),
	// and the body (the environment) can be larger than that.
	h.wmu.Lock()
	_, _, err = h.c.WriteMsgUnix(b[:4], oob, nil)
	if err == nil {
		_, err = h.c.Write(b[4:])
	}
	h.wmu.Unlock()
	if err != nil {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
		return 0, nil, fmt.Errorf("spawn helper: %w", err)
	}
	r := <-ch // read() answers every pending request, also when the helper dies
	if r.Err == ErrHelperGone.Error() {
		return 0, nil, ErrHelperGone
	}
	if r.Err != "" {
		return 0, nil, &os.PathError{Op: "posix_spawn", Path: path, Err: errors.New(r.Err)}
	}
	return r.Pid, r.exited, nil
}
