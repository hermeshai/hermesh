package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// setup, elevated, makes this program the signer of the user sid: it
// checks the PC can keep the line between elevated and other programs,
// copies itself into Program Files, makes the folder for the keys' PINs,
// and registers and starts a task that runs it at that user's logon with
// the highest privileges (docs/windows.md §7.3).
func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	user := fs.String("user", "", "the SID of the user whose signer this is")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := refusals(*user); err != nil {
		return err
	}
	prov, err := openProvider()
	if err != nil {
		return err
	}
	free(prov)
	dir := programDir()
	schtasks("/End", "/TN", signerTask()) // an older one, still running
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := copySelf(filepath.Join(dir, "hermesh-signer.exe")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "hermesh-signer.exe.manifest"), []byte(manifest), 0o644); err != nil {
		return err
	}
	if err := privateDir(keysDir(*user)); err != nil {
		return err
	}
	def := filepath.Join(dir, "task.xml")
	if err := os.WriteFile(def, utf16File(signerTaskXML(filepath.Join(dir, "hermesh-signer.exe"), *user)), 0o644); err != nil {
		return err
	}
	defer os.Remove(def)
	if err := schtasks("/Create", "/TN", signerTask(), "/XML", def, "/F"); err != nil {
		return err
	}
	return schtasks("/Run", "/TN", signerTask())
}

// remove, elevated, stops the signer and takes away its task and program;
// the keys stay, for a later setup.
func remove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	user := fs.String("user", "", "the SID of the user whose signer this is")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := refusals(*user); err != nil && !errors.Is(err, errUAC) {
		return err
	}
	schtasks("/End", "/TN", signerTask())
	schtasks("/Delete", "/TN", signerTask(), "/F")
	return os.RemoveAll(programDir())
}

// refusals: this must run elevated, as the user whose signer it sets up
// (an administrator: a standard account's elevation runs as another
// account), with User Account Control on.
func refusals(user string) error {
	if user == "" {
		return errors.New("--user: whose signer")
	}
	me, err := userSID()
	if err != nil {
		return err
	}
	if !windows.GetCurrentProcessToken().IsElevated() || me != user {
		return errNotAdmin
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if v, _, err := k.GetIntegerValue("EnableLUA"); err == nil && v == 0 {
			return errUAC
		}
	}
	return nil
}

func programDir() string {
	return filepath.Join(os.Getenv("ProgramFiles"), "Hermesh Signer")
}

// signerTask is this user's signer task: tasks share one namespace per computer.
func signerTask() string {
	u := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, os.Getenv("USERNAME"))
	return "Hermesh Signer (" + u + ")"
}

func copySelf(to string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(exe), filepath.Clean(to)) {
		return nil
	}
	in, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}

// privateDir makes dir readable and writable by the system and
// administrators only: an elevated program of the user's reads it, the
// user's other programs cannot.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	owner, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	for _, d := range []string{filepath.Dir(dir), dir} { // the keys folder, and this user's in it
		if err := windows.SetNamedSecurityInfo(d, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION, owner, nil, dacl, nil); err != nil {
			return err
		}
	}
	return nil
}

func schtasks(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks %s: %v: %s", args[0], err, bytes.TrimSpace(out))
	}
	return nil
}

// signerTaskXML runs the signer at the user's logon, as them, with the
// highest privileges they have; they may start it too (their read and
// execute rights on the task), so the app can when it isn't running.
func signerTaskXML(bin, sid string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Hermesh Signer: shows what Hermesh asks to sign with this PC's key, and signs what you approve.</Description>
    <SecurityDescriptor>D:(A;;FA;;;SY)(A;;FA;;;BA)(A;;GRGX;;;%[1]s)</SecurityDescriptor>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>%[1]s</UserId></LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author"><UserId>%[1]s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>3</Count></RestartOnFailure>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>%[2]s</Command><Arguments>run</Arguments></Exec>
  </Actions>
</Task>
`, xmlEscape(sid), xmlEscape(bin))
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
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

// manifest has the signer use version 6 of the common controls (task
// dialogs; panel_windows.go).
const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
</assembly>
`
