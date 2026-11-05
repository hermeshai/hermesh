package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// launchdPriorityFloor is launchd's Standard job. A program the person runs
// has 31, and so must hermeshd and what it starts.
const launchdPriorityFloor = 20

// The plist the Mac app ships, run by launchd: hermeshd and what it starts
// have the priority of a program the person runs, so a busy Mac does not
// starve a session's start. As a Background job, four sessions on one project
// wrote a few files a second behind busy loops, hit StartTimeout after ten
// minutes and showed as crashed, though no agent had started.
func TestDesktopDaemonUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	domain := launchdDomain(t)
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	files := map[string]string{}
	for i := range 3000 {
		files[fmt.Sprintf("file%04d.txt", i)] = strings.Repeat("content\n", 128)
	}
	gitRepoAt(t, w.repo, files)

	w.owner.stop()
	w.app.cancel()
	pid := loadLaunchAgent(t, w, domain)
	w.app = newTestApp(t, w.sock, w.root)
	eventually(t, 30*time.Second, "the machine back under launchd", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
	})

	// The spawn helpers start git and the agents, which inherit it.
	for _, p := range append(childrenOf(t, pid), pid) {
		if pri := schedPriority(t, p); pri <= launchdPriorityFloor {
			t.Errorf("process %d runs at priority %d under launchd; a program the person runs has 31", p, pri)
		}
	}

	// Twice as many busy loops as cores, at the nice 5 a shell's & gives. Each
	// ends itself, so a killed test leaves none behind.
	loops := 2 * runtime.NumCPU()
	for range loops {
		loop := exec.Command("nice", "-n", "5", "perl", "-e", "alarm 180; 1 while 1")
		if err := loop.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			loop.Process.Kill()
			loop.Wait()
		})
	}

	const limit = 90 * time.Second
	begin := time.Now()
	var ids []string
	for range 4 {
		s, r := w.startSession(30*time.Second, "--agent", "codex", "--prompt", "say done")
		if s.SessionID == "" {
			t.Fatalf("session start: %s%s", r.stdout, r.stderr)
		}
		ids = append(ids, s.SessionID)
	}
	for _, id := range ids {
		eventually(t, limit-time.Since(begin), "session "+id+" to check out and answer behind busy loops", func() bool {
			return w.get(id).Status == "idle"
		})
	}
	t.Logf("four sessions checked out %d files each and answered in %s behind %d busy loops", len(files), time.Since(begin).Round(time.Millisecond), loops)
}

// `hermeshd service install`, the other way a Mac runs hermeshd, makes the
// same kind of job.
func TestDesktopServiceInstallPriority(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	const label = "com.hermesh.daemon"
	target := launchdDomain(t) + "/" + label
	if exec.Command("launchctl", "print", target).Run() == nil {
		t.Skip("this Mac runs the real service")
	}
	bin, home, data := filepath.Join(binaries(t), "hermeshd"), t.TempDir(), t.TempDir()
	env := homeEnv(home)
	r := run(t, env, 30*time.Second, bin, "service", "install", "--data-dir", data)
	t.Cleanup(func() { run(t, env, 30*time.Second, bin, "service", "uninstall", "--data-dir", data) })
	if r.code != 0 {
		t.Fatalf("service install: %s%s", r.stdout, r.stderr)
	}
	out, err := exec.Command("launchctl", "print", target).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "spawn type = interactive") {
		t.Fatalf("the installed service is not an interactive job:\n%s", out)
	}
}

// A Mac whose service an older version installed gets the new job by itself:
// an update replaces only the program, and launchd keeps the job it loaded, so
// the new program rewrites the plist and has launchd load it again.
func TestDesktopServiceReloadsOldJob(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	const label = "com.hermesh.daemon"
	domain := launchdDomain(t)
	target := domain + "/" + label
	if exec.Command("launchctl", "print", target).Run() == nil {
		t.Skip("this Mac runs the real service")
	}
	bin, home, data := filepath.Join(binaries(t), "hermeshd"), t.TempDir(), t.TempDir()
	log := filepath.Join(data, "launchd.log")
	// As versions before ae3256f wrote it, but for the home folder: launchd
	// gives the job the real one, where it would not find this plist.
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	old := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string><string>--data-dir</string><string>%s</string></array>
  <key>EnvironmentVariables</key><dict><key>HOME</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, bin, data, home, log, log)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		t.Fatalf("launchctl bootstrap: %v: %s", err, out)
	}
	t.Cleanup(func() {
		run(t, homeEnv(home), 30*time.Second, bin, "service", "uninstall", "--data-dir", data)
		if t.Failed() {
			b, _ := os.ReadFile(log)
			t.Logf("==== hermeshd under launchd ====\n%s", b)
		}
	})
	eventually(t, time.Minute, "launchd running the job as interactive", func() bool {
		out, _ := exec.Command("launchctl", "print", target).Output()
		return strings.Contains(string(out), "spawn type = interactive")
	})
	b, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(old, "<string>Background</string>", "<string>Interactive</string>", 1); string(b) != want {
		t.Fatalf("the plist:\n%s\nwant only its ProcessType changed:\n%s", b, want)
	}
}

// launchdDomain is where launchd runs this user's LaunchAgents.
func launchdDomain(t *testing.T) string {
	t.Helper()
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	if exec.Command("launchctl", "print", domain).Run() != nil {
		t.Skip("no login session, so launchd runs no LaunchAgents here")
	}
	return domain
}

var launchdPid = regexp.MustCompile(`(?m)^\s*pid = (\d+)$`)

// loadLaunchAgent gives w's owner daemon to launchd, in the plist the app
// ships: only what names the bundle and this run's places differ. It returns
// the daemon's process id.
func loadLaunchAgent(t *testing.T, w *ownerMac, domain string) (pid int) {
	t.Helper()
	const label = "com.hermesh.test.daemon"
	target := domain + "/" + label
	exec.Command("launchctl", "bootout", target).Run() // what a killed run left
	shipped := filepath.Join("apps", "desktop", "build", "com.hermesh.app.daemon.plist")
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", shipped).Output()
	if err != nil {
		t.Fatalf("plutil %s: %v", shipped, err)
	}
	var job map[string]any
	if err := json.Unmarshal(out, &job); err != nil {
		t.Fatal(err)
	}
	delete(job, "BundleProgram")
	delete(job, "AssociatedBundleIdentifiers")
	env := map[string]string{}
	for _, kv := range w.daemonEnv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	log := filepath.Join(w.tmp, "launchd.log")
	job["Label"], job["Program"], job["EnvironmentVariables"] = label, filepath.Join(w.bin, "hermeshd"), env
	job["ProgramArguments"] = []string{"hermeshd", "run", "--owner", "--data-dir", filepath.Join(w.tmp, "owner-mac"), "--socket", w.sock}
	job["StandardOutPath"], job["StandardErrorPath"] = log, log
	text, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(w.tmp, label+".plist")
	if err := os.WriteFile(plist, text, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-convert", "xml1", plist).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v: %s", err, out)
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		t.Fatalf("launchctl bootstrap: %v: %s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("launchctl", "bootout", target).Run()
		if t.Failed() {
			b, _ := os.ReadFile(log)
			t.Logf("==== hermeshd under launchd ====\n%s", b)
		}
	})
	eventually(t, 30*time.Second, "launchd starting hermeshd", func() bool {
		out, _ := exec.Command("launchctl", "print", target).Output()
		m := launchdPid.FindSubmatch(out)
		if m == nil {
			return false
		}
		pid, _ = strconv.Atoi(string(m[1]))
		return true
	})
	return pid
}

// schedPriority is the process's scheduling priority, as ps shows it.
func schedPriority(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "pri=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps %d: %v", pid, err)
	}
	pri, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps %d: %q: %v", pid, out, err)
	}
	return pri
}

// childrenOf lists pid's child processes.
func childrenOf(t *testing.T, pid int) []int {
	t.Helper()
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output() // exits 1 with none
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		p, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, p)
	}
	return pids
}
