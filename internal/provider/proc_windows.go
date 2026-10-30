package provider

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/internal/spawn"
)

// Each agent runs in a job object of its own (docs/windows.md §4.3). It
// starts suspended and joins its job before it runs, so none of its
// descendants ever runs outside the job. Ending the job ends them all, and
// so does hermeshd's exit: its jobs' last handles close.

// jobs holds the job of each process tree this daemon runs, by the pid of
// its first process (the "pgid" the shared code passes around).
var jobs sync.Map // int → windows.Handle

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// setGroup starts the child suspended, with a console no window shows.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

// joinGroup puts the suspended child into a new job, then lets it run.
func joinGroup(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("job object: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("job object: %w", err)
	}
	if st, _, _ := ntResumeProcess.Call(uintptr(h)); st != 0 {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		return fmt.Errorf("resuming process %d: NTSTATUS %#x", pid, st)
	}
	jobs.Store(pid, job)
	return nil
}

// killGroup ends the whole tree now.
func killGroup(pgid int) error {
	if j, ok := jobs.Load(pgid); ok {
		return windows.TerminateJobObject(j.(windows.Handle), 1)
	}
	return nil
}

// releaseGroup ends what is left of the tree and forgets its job.
func releaseGroup(pgid int) {
	if j, ok := jobs.LoadAndDelete(pgid); ok {
		windows.TerminateJobObject(j.(windows.Handle), 1)
		windows.CloseHandle(j.(windows.Handle))
	}
}

// KillGroup ends a process tree this daemon started. Stop has closed the
// agent's stdin and waited already; Windows has no gentler signal to send.
func KillGroup(pgid int) { killGroup(pgid) }

// There are no spawn helpers on Windows: CreateProcess copies no
// descriptor table.

func startSpawn(bin string, args []string, dir string, env []string) (*Proc, error) {
	return nil, errors.ErrUnsupported
}

func runSpawned(ctx context.Context, hs []*spawn.Helper, bin string, args []string, dir string, env []string) (stdout, stderr []byte, err error) {
	return nil, nil, errors.ErrUnsupported
}
