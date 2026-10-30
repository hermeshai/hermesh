package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/internal/flock"
)

// On Windows hermeshd runs from a Task Scheduler task of this user's, at
// logon, so no administrator is needed (docs/windows.md §4.4). The task
// runs hermeshdw.exe, hermeshd's windowless twin (a console program
// started at logon would open a console window), with --supervise: Task
// Scheduler restarts a task that fails to start, not a program that exits,
// so the supervisor restarts the daemon itself.

// taskName is this user's task: tasks share one namespace per computer.
func taskName() string {
	u := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, os.Getenv("USERNAME"))
	return "Hermesh (" + u + ")"
}

// definition is where Install keeps the task it registered.
func definition() string {
	dir, _ := os.UserCacheDir() // %LocalAppData%
	return filepath.Join(dir, "Hermesh", "service", "hermeshd.xml")
}

// windowless is hermeshdw.exe next to bin, when there is one.
func windowless(bin string) string {
	w := filepath.Join(filepath.Dir(bin), "hermeshdw.exe")
	if _, err := os.Stat(w); err == nil {
		return w
	}
	return bin
}

func schtasks(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks %s: %v: %s", args[0], err, bytes.TrimSpace(out))
	}
	return nil
}

// Install registers and starts the task running `bin run`, with args
// before its data directory (a trusted device: --owner --no-machine). The
// daemon writes its own log, under dataDir.
func Install(bin, dataDir, logDir string, args ...string) error {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	argv := append(append([]string{"run", "--supervise"}, args...), "--data-dir", dataDir)
	path := definition()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, utf16File(taskXML(windowless(bin), argv, u.User.Sid.String())), 0o600); err != nil {
		return err
	}
	schtasks("/End", "/TN", taskName()) // an older one, still running
	if err := schtasks("/Create", "/TN", taskName(), "/XML", path, "/F"); err != nil {
		return err
	}
	return schtasks("/Run", "/TN", taskName())
}

func Uninstall() error {
	schtasks("/End", "/TN", taskName())
	os.Remove(definition())
	if !Installed() {
		return nil
	}
	return schtasks("/Delete", "/TN", taskName(), "/F")
}

// Restart restarts the installed task, and with it the daemon.
func Restart() error {
	schtasks("/End", "/TN", taskName())
	return schtasks("/Run", "/TN", taskName())
}

// Running reports whether the installed task is running now (its state,
// which schtasks would print in the system's language).
func Running() bool {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-ScheduledTask -TaskName '"+strings.ReplaceAll(taskName(), "'", "''")+"' -ErrorAction Stop).State")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "Running"
}

// AtBoot is whether the service starts by itself: at logon, as a
// LaunchAgent does.
func AtBoot() bool { return Installed() }

func Installed() bool { return schtasks("/Query", "/TN", taskName()) == nil }

// Supervised: a Windows machine does not update itself yet
// (docs/machine-updates.md), so its daemon never stops for one.
func Supervised(string) bool { return false }

// InstalledFor reports whether the installed service runs the machine in
// dataDir.
func InstalledFor(dataDir string) bool {
	b, err := os.ReadFile(definition())
	return err == nil && Installed() && strings.Contains(fromUTF16(b), xmlEscape(" --data-dir "+syscall.EscapeArg(dataDir))+"</Arguments>")
}

// taskXML is the task Install registers: at this user's logon, as this
// user, with no time limit, even on battery.
func taskXML(bin string, argv []string, sid string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = syscall.EscapeArg(a)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Hermesh: runs coding agent sessions for your agents.</Description></RegistrationInfo>
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>%[1]s</UserId></LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author"><UserId>%[1]s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>%[2]s</Command><Arguments>%[3]s</Arguments></Exec>
  </Actions>
</Task>
`, xmlEscape(sid), xmlEscape(bin), xmlEscape(strings.Join(quoted, " ")))
}

// utf16File is s as Task Scheduler reads a task file: UTF-16LE, with a BOM.
func utf16File(s string) []byte {
	u := utf16.Encode([]rune("\uFEFF" + s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return b
}

func fromUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return strings.TrimPrefix(string(utf16.Decode(u)), "\uFEFF")
}

// Supervise runs exe with args, the daemon, in a job of its own (it ends
// with the supervisor), and starts it again whenever it exits but for
// a clean stop (exit status 0): soon at first, then backing off. One
// supervisor runs per data directory: the logon task's and the app's
// may start together, and the second one leaves.
func Supervise(dataDir, exe string, args []string) error {
	lock, err := flock.Acquire(filepath.Join(dataDir, "run", "supervisor"))
	if err != nil {
		return err
	}
	defer lock.Release()
	backoff := time.Second
	for {
		began := time.Now()
		code, err := runInJob(exe, args)
		if err == nil && code == 0 {
			return nil
		}
		if time.Since(began) > time.Minute {
			backoff = time.Second
		}
		time.Sleep(backoff)
		backoff = min(2*backoff, time.Minute)
	}
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

func runInJob(exe string, args []string) (int, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job) // ends the daemon if it is still there
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err == nil {
		if err = windows.AssignProcessToJobObject(job, h); err == nil {
			if st, _, _ := ntResumeProcess.Call(uintptr(h)); st != 0 {
				err = fmt.Errorf("resuming the daemon: NTSTATUS %#x", st)
			}
		}
		windows.CloseHandle(h)
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return 0, err
	}
	cmd.Wait()
	return cmd.ProcessState.ExitCode(), nil
}
