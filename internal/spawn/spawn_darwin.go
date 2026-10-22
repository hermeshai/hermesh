//go:build darwin

// Package spawn starts child processes. On macOS it uses posix_spawn(2)
// through libSystem: Go's os/exec forks under a process-wide lock, and a
// fork from a large daemon gets slower as the daemon grows, which caps
// how fast thousands of coding-agent processes can be started.
package spawn

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:cgo_import_dynamic libc_posix_spawn posix_spawn "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_init posix_spawn_file_actions_init "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_destroy posix_spawn_file_actions_destroy "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_adddup2 posix_spawn_file_actions_adddup2 "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_addchdir_np posix_spawn_file_actions_addchdir_np "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_init posix_spawnattr_init "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_destroy posix_spawnattr_destroy "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setflags posix_spawnattr_setflags "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setpgroup posix_spawnattr_setpgroup "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setsigdefault posix_spawnattr_setsigdefault "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setsigmask posix_spawnattr_setsigmask "/usr/lib/libSystem.B.dylib"

var (
	libc_posix_spawn_trampoline_addr                          uintptr
	libc_posix_spawn_file_actions_init_trampoline_addr        uintptr
	libc_posix_spawn_file_actions_destroy_trampoline_addr     uintptr
	libc_posix_spawn_file_actions_adddup2_trampoline_addr     uintptr
	libc_posix_spawn_file_actions_addchdir_np_trampoline_addr uintptr
	libc_posix_spawnattr_init_trampoline_addr                 uintptr
	libc_posix_spawnattr_destroy_trampoline_addr              uintptr
	libc_posix_spawnattr_setflags_trampoline_addr             uintptr
	libc_posix_spawnattr_setpgroup_trampoline_addr            uintptr
	libc_posix_spawnattr_setsigdefault_trampoline_addr        uintptr
	libc_posix_spawnattr_setsigmask_trampoline_addr           uintptr
)

const (
	posixSpawnSetPgroup      = 0x0002
	posixSpawnSetSigDef      = 0x0004
	posixSpawnSetSigMask     = 0x0008
	posixSpawnCloexecDefault = 0x4000 // Apple: close every fd not set up by file actions
)

// openMax is OPEN_MAX: libc's file actions reject descriptors at or above it.
const openMax = 10240

// ErrFDRange: a child's descriptor is at or above OPEN_MAX; use os/exec.
var ErrFDRange = errors.New("spawn: descriptor above OPEN_MAX")

// Pipe returns a pipe for a child's stdio. The parent's end is moved at or
// above OPEN_MAX when the rlimit allows, so thousands of long-lived
// children leave the low descriptors that file actions accept free.
func Pipe(parentReads bool) (parent, child *os.File, err error) {
	var p [2]int
	syscall.ForkLock.RLock()
	err = syscall.Pipe(p[:])
	if err == nil {
		syscall.CloseOnExec(p[0])
		syscall.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, os.NewSyscallError("pipe", err)
	}
	pi, ci := 1, 0 // the parent writes the child's stdin
	if parentReads {
		pi, ci = 0, 1
	}
	pfd := p[pi]
	if hi, err := unix.FcntlInt(uintptr(pfd), unix.F_DUPFD_CLOEXEC, openMax); err == nil {
		syscall.Close(pfd)
		pfd = hi
	}
	syscall.SetNonblock(pfd, true)
	return os.NewFile(uintptr(pfd), "|parent"), os.NewFile(uintptr(p[ci]), "|child"), nil
}

func call(fn uintptr, args ...uintptr) error {
	var a [6]uintptr
	copy(a[:], args)
	r, _, _ := syscall_syscall6(fn, a[0], a[1], a[2], a[3], a[4], a[5])
	if r != 0 {
		return syscall.Errno(r) // posix_spawn* return the error number
	}
	return nil
}

func cstr(s string) (*byte, error) { return syscall.BytePtrFromString(s) }

func cstrv(ss []string) ([]*byte, error) {
	out := make([]*byte, len(ss)+1)
	for i, s := range ss {
		p, err := cstr(s)
		if err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// Start spawns path with argv (argv[0] included) in a new process group,
// with stdio set to the given files and every other descriptor closed.
func Start(path string, argv, env []string, dir string, stdin, stdout, stderr *os.File) (int, error) {
	cpath, err := cstr(path)
	if err != nil {
		return 0, err
	}
	cargv, err := cstrv(argv)
	if err != nil {
		return 0, err
	}
	cenv, err := cstrv(env)
	if err != nil {
		return 0, err
	}
	for _, f := range []*os.File{stdin, stdout, stderr} {
		if f.Fd() >= openMax {
			return 0, ErrFDRange
		}
	}
	var fa, attr uintptr
	if err := call(libc_posix_spawn_file_actions_init_trampoline_addr, uintptr(unsafe.Pointer(&fa))); err != nil {
		return 0, fmt.Errorf("file_actions_init: %w", err)
	}
	defer call(libc_posix_spawn_file_actions_destroy_trampoline_addr, uintptr(unsafe.Pointer(&fa)))
	for i, f := range []*os.File{stdin, stdout, stderr} {
		if err := call(libc_posix_spawn_file_actions_adddup2_trampoline_addr, uintptr(unsafe.Pointer(&fa)), f.Fd(), uintptr(i)); err != nil {
			return 0, fmt.Errorf("adddup2: %w", err)
		}
	}
	if dir != "" {
		cdir, err := cstr(dir)
		if err != nil {
			return 0, err
		}
		if err := call(libc_posix_spawn_file_actions_addchdir_np_trampoline_addr, uintptr(unsafe.Pointer(&fa)), uintptr(unsafe.Pointer(cdir))); err != nil {
			return 0, fmt.Errorf("addchdir: %w", err)
		}
	}
	if err := call(libc_posix_spawnattr_init_trampoline_addr, uintptr(unsafe.Pointer(&attr))); err != nil {
		return 0, fmt.Errorf("attr_init: %w", err)
	}
	defer call(libc_posix_spawnattr_destroy_trampoline_addr, uintptr(unsafe.Pointer(&attr)))
	allSignals := ^uint32(0)
	noSignals := uint32(0)
	call(libc_posix_spawnattr_setsigdefault_trampoline_addr, uintptr(unsafe.Pointer(&attr)), uintptr(unsafe.Pointer(&allSignals)))
	call(libc_posix_spawnattr_setsigmask_trampoline_addr, uintptr(unsafe.Pointer(&attr)), uintptr(unsafe.Pointer(&noSignals)))
	call(libc_posix_spawnattr_setpgroup_trampoline_addr, uintptr(unsafe.Pointer(&attr)), 0)
	flags := uintptr(posixSpawnSetPgroup | posixSpawnSetSigDef | posixSpawnSetSigMask | posixSpawnCloexecDefault)
	if err := call(libc_posix_spawnattr_setflags_trampoline_addr, uintptr(unsafe.Pointer(&attr)), flags); err != nil {
		return 0, fmt.Errorf("setflags: %w", err)
	}
	var pid int32
	err = call(libc_posix_spawn_trampoline_addr, uintptr(unsafe.Pointer(&pid)), uintptr(unsafe.Pointer(cpath)),
		uintptr(unsafe.Pointer(&fa)), uintptr(unsafe.Pointer(&attr)), uintptr(unsafe.Pointer(&cargv[0])), uintptr(unsafe.Pointer(&cenv[0])))
	if err != nil {
		return 0, &os.PathError{Op: "posix_spawn", Path: path, Err: err}
	}
	return int(pid), nil
}

// Supported reports whether Start is available on this platform.
const Supported = true
