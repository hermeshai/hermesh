package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestACPAgents drives ACP agents (Gemini and Cursor, played by a scripted
// agent that speaks real ACP) through the real relay, owner daemon and
// agent CLI: discovery, tool calls into the worktree, follow-up turns,
// cancel, and resume after a daemon restart.
func TestACPAgents(t *testing.T) {
	unixOnly(t, "its fake Cursor is a shell script")
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	fake := filepath.Join(bin, "fakeacp")
	// Cursor needs `authenticate` and accepts one model, like the real CLI's
	// login and model checks.
	scripts := t.TempDir()
	cursor := filepath.Join(scripts, "cursor-agent")
	if err := os.WriteFile(cursor, []byte("#!/bin/sh\nFAKE_ACP_AUTH=cursor_login FAKE_ACP_MODELS=fast-model exec "+fake+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The owner grants this agent Gemini and Cursor only.
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "HERMESH_CURSOR_BIN=" + cursor, "FAKE_ACP_STATE=" + filepath.Join(scripts, "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini", "cursor"}})
	cli := w.cli

	// Discovery: the project lists the installed ACP agents this agent may
	// use, and not the ones outside its grant.
	if r := cli(30*time.Second, "project", "list"); !strings.Contains(r.stdout, "owner-mac/demo") || !strings.Contains(r.stdout, "gemini") ||
		!strings.Contains(r.stdout, "cursor") || strings.Contains(r.stdout, "claude") {
		t.Fatalf("projects: %+v", r)
	}

	// Turn 1: a message, a file write and a shell command in the worktree.
	s1, r := w.startSession(time.Minute, "--agent", "gemini", "--wait",
		"--prompt", "say working on it\nwrite hello.txt hello from gemini\nshell echo shell-ran > ran.txt\nsay done")
	if r.code != 0 || s1.Status != "idle" {
		t.Fatalf("gemini session: %+v", r)
	}
	if !strings.Contains(r.stdout, "done") {
		t.Fatalf("turn result: %s", r.stdout)
	}
	d := cli(30*time.Second, "session", "diff", s1.SessionID)
	if !strings.Contains(d.stdout, "+hello from gemini") || !strings.Contains(d.stdout, "+shell-ran") {
		t.Fatalf("diff: %+v", d)
	}
	if _, err := os.Stat(filepath.Join(w.repo, "hello.txt")); err == nil {
		t.Fatal("the agent wrote into the owner's working tree")
	}
	logsOut := cli(30*time.Second, "session", "events", s1.SessionID, "--limit", "1000")
	for _, want := range []string{"working on it", "Write hello.txt", "echo shell-ran"} {
		if !strings.Contains(logsOut.stdout, want) {
			t.Fatalf("logs missing %q:\n%s", want, logsOut.stdout)
		}
	}

	// Turn 2 continues the same ACP session.
	if r := cli(time.Minute, "session", "send", s1.SessionID, "--wait", "--message", "history"); r.code != 0 || !strings.Contains(r.stdout, "history: 2 turns") {
		t.Fatalf("turn 2: %+v", r)
	}

	// Cursor: authenticate + model selection.
	if s, r := w.startSession(time.Minute, "--agent", "cursor", "--model", "fast-model", "--wait", "--prompt", "say from cursor"); r.code != 0 || s.Status != "idle" {
		t.Fatalf("cursor session: %+v", r)
	}
	if s, r := w.startSession(time.Minute, "--agent", "cursor", "--model", "no-such-model", "--wait", "--prompt", "say x"); r.code != 5 ||
		!strings.Contains(r.stdout, "no-such-model") || s.LastFailure == nil || s.LastFailure.Kind != "model_unavailable" {
		t.Fatalf("unknown model must fail as model_unavailable: %+v", r)
	}
	if _, r := w.startSession(time.Minute, "--agent", "codex", "--prompt", "say x"); r.code != 3 {
		t.Fatalf("agent outside the grant: %+v", r)
	}

	// Interrupt a running turn: the session stays, idle.
	long, _ := w.startSession(30*time.Second, "--agent", "gemini", "--prompt", "say starting\nsleep 60s")
	eventually(t, 30*time.Second, "long session running", func() bool { return w.get(long.SessionID).Status == "running" })
	if c := cli(30*time.Second, "session", "interrupt", long.SessionID); c.code != 0 {
		t.Fatalf("interrupt: %+v", c)
	}
	eventually(t, 30*time.Second, "idle after the interrupt", func() bool { return w.get(long.SessionID).Status == "idle" })

	// The agent process dies in the middle of a turn: the turn is
	// interrupted (exit 7, retryable) and the next message resumes the same
	// ACP session (session/load) with its history.
	_, r = w.send(time.Minute, s1.SessionID, "write partial.txt half done\ncrash")
	if r.code != 7 || !strings.Contains(r.stdout, "agent_crashed") {
		t.Fatalf("crash: %+v", r)
	}
	if d := cli(30*time.Second, "session", "diff", s1.SessionID, "--turn", "3"); !strings.Contains(d.stdout, "+half done") {
		t.Fatalf("the interrupted turn's work: %+v", d)
	}
	if r := cli(time.Minute, "session", "send", s1.SessionID, "--wait", "--message", "history"); r.code != 0 || !strings.Contains(r.stdout, "history: 4 turns") {
		t.Fatalf("after the crash: %+v", r)
	}

	// Restart the daemon: the idle session resumes its ACP session
	// (session/load) on the next message, with its history intact.
	w.restartOwner(false)
	if r := cli(time.Minute, "session", "send", s1.SessionID, "--wait", "--message", "history"); r.code != 0 || !strings.Contains(r.stdout, "history: 5 turns") {
		t.Fatalf("after restart: %+v\n%s", r, w.owner.output())
	}
	if r := cli(30*time.Second, "session", "events", s1.SessionID, "--limit", "1000"); strings.Contains(r.stdout, "replayed") {
		t.Fatalf("the replayed history was logged as new events:\n%s", r.stdout)
	}
}

// TestDeepSeekFromDesktopApp: someone with only the DSH Desktop app (no
// `dsh` on PATH) can use DeepSeek Harness: hermeshd runs the dsh the app
// keeps, as the app does (its executable as Node on its CLI script).
func TestDeepSeekFromDesktopApp(t *testing.T) {
	unixOnly(t, "it reads a macOS app bundle")
	if testing.Short() {
		t.Skip("e2e")
	}
	for _, p := range []string{"/opt/homebrew/bin/dsh", "/usr/local/bin/dsh"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s is installed, and is found first", p)
		}
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	home := t.TempDir()
	app := filepath.Join(home, "Applications/DSH Desktop.app/Contents")
	script := filepath.Join(app, "Resources/app.asar.unpacked/lib/desktop-cli.js")
	os.MkdirAll(filepath.Dir(script), 0o755)
	os.WriteFile(script, nil, 0o644)
	os.MkdirAll(filepath.Join(app, "MacOS"), 0o755)
	os.WriteFile(filepath.Join(app, "MacOS/DSH Desktop"), []byte(fmt.Sprintf("#!/bin/sh\n"+
		"[ \"$ELECTRON_RUN_AS_NODE\" = 1 ] && [ \"$1\" = %q ] || { echo 'not run as Node on its CLI' >&2; exit 3; }\n"+
		"shift\n[ \"$1 $2\" = '--profile acp' ] && shift 2\nexec %q \"$@\"\n", script, fake)), 0o755)

	w := newOwnerMac(t, []string{"HOME=" + home, "PATH=/usr/bin:/bin", "SHELL=/bin/sh", "FAKE_ACP_STATE=" + t.TempDir()},
		&localv1.GrantSpec{Agents: []string{"deepseek"}})
	if r := w.cli(30*time.Second, "project", "list"); !strings.Contains(r.stdout, "deepseek") {
		t.Fatalf("projects: %+v", r)
	}
	s, r := w.startSession(time.Minute, "--agent", "deepseek", "--wait", "--prompt", "say hello from deepseek")
	if r.code != 0 || s.Status != "idle" || !strings.Contains(r.stdout, "hello from deepseek") {
		t.Fatalf("deepseek session: %+v", r)
	}
}

// TestCodeBuddy: CodeBuddy Code installed as its npm package puts
// `codebuddy` on PATH; hermeshd finds it there and starts it as an ACP agent.
func TestCodeBuddy(t *testing.T) {
	unixOnly(t, "its fake CodeBuddy is a shell script")
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codebuddy"), []byte(fmt.Sprintf("#!/bin/sh\n"+
		"[ \"$1\" = --acp ] && shift\nexec %q \"$@\"\n", filepath.Join(binaries(t), "fakeacp"))), 0o755)
	w := newOwnerMac(t, []string{"PATH=" + bin + ":/usr/bin:/bin", "SHELL=/bin/sh", "FAKE_ACP_STATE=" + t.TempDir()},
		&localv1.GrantSpec{Agents: []string{"codebuddy"}})
	if r := w.cli(30*time.Second, "project", "list"); !strings.Contains(r.stdout, "codebuddy") {
		t.Fatalf("projects: %+v", r)
	}
	s, r := w.startSession(time.Minute, "--agent", "codebuddy", "--wait", "--prompt", "say hello from codebuddy")
	if r.code != 0 || s.Status != "idle" || !strings.Contains(r.stdout, "hello from codebuddy") {
		t.Fatalf("codebuddy session: %+v", r)
	}
}
