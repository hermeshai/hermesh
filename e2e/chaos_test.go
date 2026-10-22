package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// Chaos tests drive this machine's real Codex through real binaries and
// inject real faults: killed agent and daemon processes, a relay restart,
// a provider outage, a missing login, concurrent sessions, and an agent
// that rewrites git history. After each one the invariants in
// checkSessions/finish must hold. Run with `make e2e-chaos` (costs a few
// Codex turns per test).

func chaosOwner(t *testing.T, env []string, args ...string) *ownerMac {
	t.Helper()
	if os.Getenv("HERMESH_CHAOS") != "1" {
		t.Skip("set HERMESH_CHAOS=1 to run against this machine's real Codex")
	}
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"codex"}}, args...)
	t.Cleanup(func() { w.finish(t) })
	return w
}

// bg runs the agent CLI in the background.
func (w *ownerMac) bg(name string, args ...string) *proc {
	p := start(w.t, name, w.cliEnv, filepath.Join(w.bin, "hermesh"), args...)
	w.logs = append(w.logs, p)
	return p
}

type osProc struct {
	pid int
	cmd string
	cwd string
}

// procsIn lists processes whose working directory is under dir.
func procsIn(dir string) []osProc {
	dir, _ = filepath.EvalSymlinks(dir)
	out, _ := exec.Command("lsof", "-w", "-d", "cwd", "-Fpcn").Output()
	var ps []osProc
	var cur osProc
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			cur = osProc{}
			cur.pid, _ = strconv.Atoi(l[1:])
		case 'c':
			cur.cmd = l[1:]
		case 'n':
			cur.cwd = l[1:]
			if cur.cwd == dir || strings.HasPrefix(cur.cwd, dir+"/") {
				ps = append(ps, cur)
			}
		}
	}
	return ps
}

func hasCmd(ps []osProc, cmd string) bool {
	for _, p := range ps {
		if p.cmd == cmd {
			return true
		}
	}
	return false
}

// eventJSON is the part of the contract's event the tests read.
type eventJSON struct {
	Seq     int       `json:"seq"`
	Ts      time.Time `json:"ts"`
	Turn    int       `json:"turn"`
	Type    string    `json:"type"`
	Status  string    `json:"status"`
	Name    string    `json:"name"`
	Summary string    `json:"summary"`
}

func (w *ownerMac) events(id string) []eventJSON {
	r := w.cli(time.Minute, "session", "events", id, "--limit", "1000", "-o", "json")
	var page struct{ Events []eventJSON }
	json.Unmarshal([]byte(r.stdout), &page)
	return page.Events
}

type diffJSON struct {
	Diff string `json:"diff"`
	Base string `json:"base_commit"`
	Head string `json:"head_commit"`
}

// checkSessions verifies what must hold for every session after any fault:
// a settled status, an event log with contiguous sequence numbers and a
// result for every turn that ran to the end, a diff equal to git's, and
// for idle sessions a worktree whose state is exactly the session head.
func (w *ownerMac) checkSessions(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		s := w.get(id)
		switch s.Status {
		case "running", "preparing", "queued":
			t.Errorf("%s: not settled: %s", id, s.Status)
		}
		evs := w.events(id)
		started, completed := map[int]bool{}, map[int]bool{}
		for i, e := range evs {
			if e.Seq != i+1 {
				t.Errorf("%s: event %d has seq %d (gap or duplicate)", id, i+1, e.Seq)
				break
			}
			if e.Type == "turn_started" {
				started[e.Turn] = true
			}
			if e.Type == "turn_completed" {
				if completed[e.Turn] {
					t.Errorf("%s: turn %d completed twice", id, e.Turn)
				}
				completed[e.Turn] = true
			}
		}
		last := 0
		for n := range started {
			last = max(last, n)
		}
		for n := range started {
			// Only the last turn may lack a result, when the daemon died
			// under it or it was canceled.
			if !completed[n] && (n != last || s.Status == "idle") {
				t.Errorf("%s: turn %d has no result", id, n)
			}
		}
		var d diffJSON
		r := w.cli(time.Minute, "session", "diff", id, "-o", "json")
		json.NewDecoder(strings.NewReader(r.stdout)).Decode(&d)
		if d.Base == "" || d.Head == "" {
			t.Errorf("%s: diff: %+v", id, r)
			continue
		}
		if g := gitOut(t, w.repo, "diff", "--binary", d.Base, d.Head); strings.TrimSpace(g) != strings.TrimSpace(d.Diff) {
			t.Errorf("%s: session diff differs from git diff %s..%s", id, d.Base[:8], d.Head[:8])
		}
		if s.Status == "idle" {
			wt := w.worktree(id)
			if st := gitOut(t, wt, "status", "--porcelain"); st != "" {
				t.Errorf("%s: idle worktree has changes outside the session's commits:\n%s", id, st)
			}
			// The agent may sit on another branch; its files must still be
			// exactly the session head's.
			if h, want := gitOut(t, wt, "rev-parse", "HEAD^{tree}"), gitOut(t, wt, "rev-parse", d.Head+"^{tree}"); h != want {
				t.Errorf("%s: worktree (on %s) differs from the session head %s: the agent's work is not on the session branch",
					id, gitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD"), d.Head[:8])
			}
		}
	}
	if h := gitOut(t, w.repo, "rev-parse", "HEAD"); h != w.repoHead {
		t.Errorf("the owner's HEAD moved: %s", h)
	}
	if st := gitOut(t, w.repo, "status", "--porcelain"); st != "" {
		t.Errorf("the owner's working tree changed:\n%s", st)
	}
}

// finish stops the daemon and checks nothing it started outlives it and
// the audit log verifies.
func (w *ownerMac) finish(t *testing.T) {
	w.owner.stop()
	w.app.cancel()
	deadline := time.Now().Add(15 * time.Second)
	for {
		ps := procsIn(filepath.Join(w.tmp, "owner-mac"))
		if len(ps) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("processes left after the daemon stopped: %+v", ps)
			for _, p := range ps {
				syscall.Kill(p.pid, syscall.SIGKILL)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	r := run(t, nil, time.Minute, filepath.Join(w.bin, "hermeshd"), "audit-verify", "--data-dir", filepath.Join(w.tmp, "owner-mac"))
	if r.code != 0 || !strings.Contains(r.stdout, "audit log ok") {
		t.Errorf("audit-verify: %+v", r)
	}
	t.Logf("audit: %s", strings.TrimSpace(r.stdout))
}

func (w *ownerMac) toolCalls(id string) []string {
	var out []string
	for _, e := range w.events(id) {
		if e.Type == "tool_call" {
			out = append(out, e.Name+": "+e.Summary)
		}
	}
	return out
}

// codexSession starts a Codex session and waits for its first turn.
func (w *ownerMac) codexSession(t *testing.T, prompt string) string {
	t.Helper()
	s, r := w.startSession(10*time.Minute, "--agent", "codex", "--wait", "--prompt", prompt)
	if r.code != 0 || s.Status != "idle" {
		t.Fatalf("start: %+v", r)
	}
	return s.SessionID
}

// 1. The Codex process is killed while one of its tools runs.
func TestChaosAgentKilledMidTool(t *testing.T) {
	w := chaosOwner(t, nil)
	id := w.codexSession(t, "Create a file named note.txt containing the word alpha. Then reply with: ready")
	wt := w.worktree(id)
	waiter := w.bg("waiting cli", "session", "send", id, "--wait", "-o", "json", "--message",
		"Run exactly this shell command and wait for it to finish: sleep 120 ; then reply with: slept")
	eventually(t, 5*time.Minute, "sleep running in the worktree", func() bool { return hasCmd(procsIn(wt), "sleep") })
	for _, p := range procsIn(wt) {
		if p.cmd == "codex" {
			t.Logf("kill -9 codex pid %d", p.pid)
			syscall.Kill(p.pid, syscall.SIGKILL)
		}
	}
	if c := waiter.wait(t, 2*time.Minute); c != 7 || !strings.Contains(waiter.output(), "agent_crashed") {
		t.Fatalf("waiting CLI exit %d:\n%s", c, waiter.output())
	}
	eventually(t, 15*time.Second, "the killed agent's tools are gone", func() bool { return len(procsIn(wt)) == 0 })
	if s := w.get(id); s.Status != "crashed" {
		t.Fatalf("status %s", s.Status)
	}
	_, r := w.send(10*time.Minute, id, "Your previous turn was cut off. Which file did you create in the first turn? Reply with only the file name.")
	if r.code != 0 || !strings.Contains(r.stdout, "note.txt") {
		t.Fatalf("resume after the crash: %+v", r)
	}
	w.checkSessions(t, id)
}

// 2. A tool runs silently for longer than the idle window.
func TestChaosLongSilentTool(t *testing.T) {
	w := chaosOwner(t, nil, "--idle-timeout", "40s")
	start := time.Now()
	s, r := w.startSession(10*time.Minute, "--agent", "codex", "--wait", "--prompt",
		"Run exactly this shell command and wait for it to finish (it takes about 70 seconds): sleep 70 && echo tool-finished . Then reply with the command's output only.")
	t.Logf("took %v; tools: %q", time.Since(start), w.toolCalls(s.SessionID))
	if r.code != 0 || !strings.Contains(r.stdout, "tool-finished") || time.Since(start) < 65*time.Second {
		t.Fatalf("long tool: %+v", r)
	}
	w.checkSessions(t, s.SessionID)
}

// 3. The provider cannot be reached (all HTTPS through a dead proxy), then
// comes back. Codex 0.162 never gives up: it keeps reporting
// "Reconnecting... waiting for network". Those retries are not progress, so
// the idle window (shortened here from 15m) ends the turn as a network
// failure.
func TestChaosProviderOutage(t *testing.T) {
	w := chaosOwner(t, []string{"HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9"}, "--idle-timeout", "3m")
	start := time.Now()
	s, r := w.startSession(15*time.Minute, "--agent", "codex", "--wait", "--prompt", "Reply with exactly: ok")
	t.Logf("outage turn ended after %v with exit %d: %+v", time.Since(start), r.code, s.LastFailure)
	if r.code != 7 || s.LastFailure == nil || s.LastFailure.Kind != "network" || time.Since(start) > 6*time.Minute {
		t.Fatalf("outage: %+v", r)
	}
	if l := w.cli(time.Minute, "session", "events", s.SessionID, "--limit", "1000"); !strings.Contains(l.stdout, "(network) Reconnecting") {
		t.Errorf("the retries are not visible in the logs:\n%s", l.stdout)
	}
	w.daemonEnv = nil // the network is back
	w.restartOwner(false)
	if _, r := w.send(10*time.Minute, s.SessionID, "Reply with exactly: back"); r.code != 0 || !strings.Contains(r.stdout, "back") {
		t.Fatalf("after the outage: %+v", r)
	}
	w.checkSessions(t, s.SessionID)
}

// 4. The daemon is killed (-9) while Codex is editing files.
func TestChaosDaemonKilledMidEdit(t *testing.T) {
	w := chaosOwner(t, nil)
	id := w.codexSession(t, "Reply with exactly: ready")
	wt := w.worktree(id)
	waiter := w.bg("waiting cli", "session", "send", id, "--wait", "-o", "json", "--message",
		"Create five files f1.txt to f5.txt. For each i from 1 to 5, write the number i into fi.txt with a shell command, then run sleep 5 before the next file. Then reply with: all done")
	eventually(t, 5*time.Minute, "f1.txt written", func() bool { _, err := os.Stat(filepath.Join(wt, "f1.txt")); return err == nil })
	before := procsIn(wt)
	t.Logf("kill -9 the daemon; agent processes: %+v", before)
	w.restartOwner(true)
	eventually(t, 20*time.Second, "the old daemon's agent processes are gone", func() bool { return len(procsIn(wt)) == 0 })
	if c := waiter.wait(t, 3*time.Minute); c != 7 {
		t.Fatalf("waiting CLI exit %d:\n%s", c, waiter.output())
	}
	if s := w.get(id); s.Status != "crashed" {
		t.Fatalf("status %s", s.Status)
	}
	_, r := w.send(10*time.Minute, id, "Your previous turn was cut off. Finish it: make sure f1.txt to f5.txt all exist and each contains its number, then reply with: all done")
	if r.code != 0 {
		t.Fatalf("resume: %+v", r)
	}
	for i := 1; i <= 5; i++ {
		b, err := os.ReadFile(filepath.Join(wt, fmt.Sprintf("f%d.txt", i)))
		if err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(i) {
			t.Errorf("f%d.txt: %q %v", i, b, err)
		}
	}
	if d := w.cli(time.Minute, "session", "diff", id); strings.Count(d.stdout, "+++ b/f") != 5 {
		t.Errorf("diff has %d of the 5 files", strings.Count(d.stdout, "+++ b/f"))
	}
	w.checkSessions(t, id)
}

// 5. The agent commits, switches branches and rewrites history itself.
func TestChaosAgentRewritesGit(t *testing.T) {
	w := chaosOwner(t, nil)
	id := w.codexSession(t, "Create x.txt containing one, then commit it yourself with git (message: agent commit). Then reply with: done")
	w.checkSessions(t, id)
	if _, r := w.send(10*time.Minute, id, "Create a new git branch named feature and switch to it, then create y.txt containing two. Do not commit. Reply with: done"); r.code != 0 {
		t.Fatalf("turn 2: %+v", r)
	}
	w.checkSessions(t, id)
	if _, r := w.send(10*time.Minute, id, "On the current branch run: git reset --hard HEAD~1 . Then create z.txt containing three. Reply with: done"); r.code != 0 {
		t.Fatalf("turn 3: %+v", r)
	}
	w.checkSessions(t, id)
	t.Logf("worktree log:\n%s", gitOut(t, w.worktree(id), "log", "--oneline", "--all", "--graph"))
}

// 6. More sessions than slots on one repository at once.
func TestChaosConcurrentSessions(t *testing.T) {
	w := chaosOwner(t, nil, "--max-sessions", "2")
	var ids []string
	for i := 1; i <= 4; i++ {
		s, r := w.startSession(time.Minute, "--agent", "codex", "--prompt", fmt.Sprintf("Create a file named c%d.txt containing %d. Then reply with: done", i, i))
		if r.code != 0 {
			t.Fatalf("start %d: %+v", i, r)
		}
		ids = append(ids, s.SessionID)
	}
	eventually(t, 15*time.Minute, "all four sessions idle", func() bool {
		for _, id := range ids {
			if w.get(id).Status != "idle" {
				return false
			}
		}
		return true
	})
	// How many held a slot (preparing or running) at once, from the
	// daemon's own status timestamps.
	type edge struct {
		at time.Time
		d  int
	}
	var edges []edge
	for _, id := range ids {
		var since time.Time
		for _, e := range w.events(id) {
			if e.Type != "status" {
				continue
			}
			slot := e.Status == "preparing" || e.Status == "running"
			if slot && since.IsZero() {
				since = e.Ts
				edges = append(edges, edge{e.Ts, 1})
			} else if !slot && !since.IsZero() {
				since = time.Time{}
				edges = append(edges, edge{e.Ts, -1})
			}
		}
	}
	slices.SortFunc(edges, func(a, b edge) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return a.d - b.d // a slot freed at the same instant comes first
	})
	held, maxHeld := 0, 0
	for _, e := range edges {
		held += e.d
		maxHeld = max(maxHeld, held)
	}
	t.Logf("at most %d sessions held a slot at once", maxHeld)
	if maxHeld > 2 {
		t.Errorf("%d sessions ran at once with --max-sessions 2", maxHeld)
	}
	for i, id := range ids {
		d := w.cli(time.Minute, "session", "diff", id)
		if strings.Count(d.stdout, "+++ b/") != 1 || !strings.Contains(d.stdout, fmt.Sprintf("+++ b/c%d.txt", i+1)) {
			t.Errorf("session %d diff:\n%s", i+1, d.stdout)
		}
	}
	w.checkSessions(t, ids...)
}

// 7. The relay restarts while a CLI follows a running turn.
func TestChaosRelayRestart(t *testing.T) {
	w := chaosOwner(t, nil)
	s, r := w.startSession(time.Minute, "--agent", "codex", "--prompt",
		"Run exactly this shell command and wait for it: sleep 25 . Then create r.txt containing done. Then reply with: finished")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	eventually(t, 5*time.Minute, "sleep running", func() bool { return hasCmd(procsIn(w.worktree(s.SessionID)), "sleep") })
	t.Log("restarting the relay")
	restarted := time.Now()
	w.restartRelay()
	eventually(t, 10*time.Minute, "turn 1 done", func() bool { return w.get(s.SessionID).Status == "idle" })
	t.Logf("turn done %v after the relay restart", time.Since(restarted).Round(time.Second))
	// Every event stored once, none missing, across the restart.
	evs := w.events(s.SessionID)
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Errorf("event %d has seq %d", i+1, e.Seq)
		}
	}
	done := 0
	for _, e := range evs {
		if e.Type == "turn_completed" {
			done++
		}
	}
	if done != 1 {
		t.Errorf("turn 1 completed %d times", done)
	}
	w.checkSessions(t, s.SessionID)
}

// 8. Codex is not logged in for the daemon's user.
func TestChaosNotLoggedIn(t *testing.T) {
	w := chaosOwner(t, []string{"CODEX_HOME=" + t.TempDir()})
	s, r := w.startSession(10*time.Minute, "--agent", "codex", "--wait", "--prompt", "Reply with exactly: ok")
	t.Logf("exit %d, failure %+v", r.code, s.LastFailure)
	if r.code != 5 || s.LastFailure == nil || s.LastFailure.Kind != "auth" || !strings.Contains(r.stdout+r.stderr, "log in") {
		t.Fatalf("not logged in: %+v", r)
	}
	w.checkSessions(t, s.SessionID)
}
