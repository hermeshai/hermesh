package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestAgentDrivesCLI runs the CLI the way an agent's harness does: stdin is
// a pipe the harness keeps open (Machine's child_process default) and stdout
// is parsed as JSON. Whatever happens, `-o json` prints exactly one JSON
// document whose exit_code is the process's.
func TestAgentDrivesCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	agent := func(stdin string, timeout time.Duration, args ...string) result {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(w.bin, "hermesh"), args...)
		cmd.Env = append(os.Environ(), w.cliEnv...)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		in, _ := cmd.StdinPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		io.WriteString(in, stdin) // and left open, unless the agent pipes a prompt
		if stdin != "" {
			in.Close()
		}
		cmd.Wait()
		in.Close()
		if ctx.Err() != nil {
			t.Fatalf("hermesh %v hung (stdin open)\nstdout:%s\nstderr:%s", args, so.String(), se.String())
		}
		return result{stdout: so.String(), stderr: se.String(), code: cmd.ProcessState.ExitCode()}
	}
	one := func(r result) map[string]any {
		t.Helper()
		return jsonDoc(t, r)
	}
	// sess is the session of a document; errOf its error.
	sess := func(m map[string]any) map[string]any { s, _ := m["session"].(map[string]any); return s }
	errOf := func(m map[string]any) map[string]any { e, _ := m["error"].(map[string]any); return e }

	// A forgotten prompt fails at once instead of reading the open stdin.
	r := agent("", 20*time.Second, "session", "start", "--project", "demo", "-o", "json")
	if m := one(r); r.code != 2 || errOf(m)["kind"] != "usage" || !strings.Contains(errOf(m)["message"].(string), "--prompt-file (- reads stdin)") {
		t.Fatalf("no prompt: %+v", r)
	}
	r = agent("", 20*time.Second, "session", "send", "s_x_y", "-o", "json")
	if m := one(r); r.code != 2 || !strings.Contains(errOf(m)["message"].(string), "--message") {
		t.Fatalf("no message: %+v", r)
	}
	// Asked for, the prompt comes from stdin.
	r = agent("say piped in\n", time.Minute, "session", "start", "--project", "demo", "--prompt-file", "-", "--wait", "-o", "json")
	if m := one(r); r.code != 0 || m["ok"] != true || m["error"] != nil || sess(m)["result"] != "piped in" {
		t.Fatalf("prompt from stdin: %+v", r)
	}

	// The full final reply is in the JSON, not only an excerpt.
	report := strings.Repeat("A long final report line. ", 40) + "END-OF-REPORT"
	r = agent("", time.Minute, "session", "start", "--project", "demo", "--wait", "-o", "json", "--prompt", "write a.txt a\nsay "+report)
	s := sess(one(r))
	if r.code != 0 || s["result"] != report || s["status"] != "idle" {
		t.Fatalf("long result: %+v", r)
	}
	id := s["session_id"].(string)

	// A retryable failure: the session, the failure and the exit code.
	r = agent("", time.Minute, "session", "send", id, "--wait", "-o", "json", "--message", "fail 429 rate limit exceeded")
	m := one(r)
	if r.code != 7 || sess(m)["session_id"] != id || errOf(m)["kind"] != "rate_limit" || errOf(m)["retryable"] != true || errOf(m)["hint"] == "" ||
		sess(m)["last_failure"].(map[string]any)["retryable"] != true {
		t.Fatalf("rate limit: %+v", r)
	}

	// A question: the pending question and exit code 8.
	r = agent("", time.Minute, "session", "send", id, "--wait", "-o", "json", "--message", "ask Env|Deploy where?|staging,prod\nsay ok")
	m = one(r)
	if q, _ := sess(m)["question"].(map[string]any); r.code != 8 || q == nil || m["exit_code"] != float64(8) || errOf(m)["kind"] != "needs_input" {
		t.Fatalf("question: %+v", r)
	}
	if r := agent("", time.Minute, "session", "answer", id, "--choose", "prod", "--wait", "-o", "json"); sess(one(r))["result"] != "ok" || r.code != 0 {
		t.Fatalf("answer: %+v", r)
	}

	// Timing out on a new session still says which session it is (its id
	// also came first on stderr), and how to check on it.
	r = agent("", time.Minute, "session", "start", "--project", "demo", "--wait", "--timeout", "2s", "-o", "json", "--prompt", "sleep 30s\nsay late")
	m = one(r)
	slow, _ := sess(m)["session_id"].(string)
	if r.code != 6 || !strings.HasPrefix(slow, "s_") || sess(m)["status"] != "running" || errOf(m)["kind"] != "wait_timeout" ||
		!strings.HasPrefix(r.stderr, "session "+slow) || !strings.Contains(errOf(m)["hint"].(string), "hermesh session get "+slow) {
		t.Fatalf("timeout: %+v", r)
	}
	if r := w.cli(30*time.Second, "session", "interrupt", slow); r.code != 0 {
		t.Fatalf("interrupt: %+v", r)
	}

	// In the background: the command returns at once and says how to check;
	// session get shows the progress and, once idle, the full result.
	r = agent("", time.Minute, "session", "send", id, "-o", "json", "--message", "sleep 2s\nsay "+report)
	if m := one(r); r.code != 0 || sess(m)["session_id"] != id || !strings.Contains(r.stderr, "hermesh session get "+id) {
		t.Fatalf("send in the background: %+v", r)
	}
	eventually(t, 30*time.Second, "the background turn's result", func() bool {
		g := sess(one(agent("", 30*time.Second, "session", "get", id, "-o", "json")))
		return g["status"] == "idle" && g["result"] == report
	})
	if g := agent("", 30*time.Second, "session", "get", id); !strings.Contains(g.stdout, "END-OF-REPORT") {
		t.Fatalf("get shows the result: %+v", g)
	}
	if r := agent("", 30*time.Second, "session", "send", id, "--follow", "--message", "x"); r.code != 2 {
		t.Fatalf("--follow is gone: %+v", r)
	}

	// Events, a page at a time: the latest by default, then only what is
	// new (--after the last seq seen), or older ones (--before).
	page := func(args ...string) map[string]any { // the page's cursors, and its events
		t.Helper()
		d := one(agent("", 30*time.Second, append([]string{"session", "events", id, "-o", "json"}, args...)...))
		p := d["page"].(map[string]any)
		p["events"] = d["events"]
		return p
	}
	p := page("--limit", "3")
	first, last := p["first_seq"].(float64), p["last_seq"].(float64)
	if len(p["events"].([]any)) != 3 || last-first != 2 || p["has_more_before"] != true || p["has_more_after"] != false {
		t.Fatalf("latest page: %v", p)
	}
	if p := page("--after", fmt.Sprint(last)); len(p["events"].([]any)) != 0 || p["last_seq"] != last {
		t.Fatalf("nothing new yet: %v", p)
	}
	agent("", time.Minute, "session", "send", id, "--wait", "--message", "say paging works")
	p = page("--after", fmt.Sprint(last))
	if evs := p["events"].([]any); len(evs) == 0 || !strings.Contains(fmt.Sprint(evs), "paging works") || p["has_more_after"] != false || p["has_more_before"] != true {
		t.Fatalf("what is new: %v", p)
	}
	if p := page("--before", fmt.Sprint(first), "--limit", "2"); len(p["events"].([]any)) != 2 || p["last_seq"] != first-1 {
		t.Fatalf("older: %v", p)
	}
	if r := agent("", 30*time.Second, "session", "events", id, "--limit", "2"); r.code != 0 || !strings.Contains(r.stderr, "earlier: --before") {
		t.Fatalf("events, text: %+v", r)
	}
	for _, args := range [][]string{{"session", "events", id, "--follow"}, {"session", "logs", id}, {"session", "events", id, "--after", "1", "--before", "3"}} {
		if r := agent("", 30*time.Second, args...); r.code != 2 {
			t.Fatalf("%v: %+v", args, r)
		}
	}

	// Sessions updated just now are listed --since, with their update time.
	r = agent("", 30*time.Second, "session", "list", "--since", "5m", "-o", "json")
	var listed []any
	if all, _ := one(r)["sessions"].([]any); r.code == 0 {
		listed = all
	}
	found := false
	for _, x := range listed {
		if x := x.(map[string]any); x["session_id"] == id {
			found = !strings.HasPrefix(x["updated_at"].(string), "0001")
		}
	}
	if !found {
		t.Fatalf("session list --since 5m: %+v", r)
	}

	// Every machine offline: the lists still answer (exit 0) and name it
	// in offline; what needs the machine fails with unreachable, which
	// may work later.
	w.owner.stop()
	var off map[string]any
	eventually(t, 30*time.Second, "owner-mac offline", func() bool {
		r := agent("", 30*time.Second, "project", "list", "-o", "json")
		m := one(r)
		o, _ := m["offline"].([]any)
		if r.code != 0 || len(o) != 1 {
			return false
		}
		off = o[0].(map[string]any)
		return off["error"] == ""
	})
	if off["machine"] != "owner-mac" || off["last_seen"] == nil {
		t.Fatalf("offline: %v", off)
	}
	if r := agent("", 30*time.Second, "project", "list"); r.code != 0 || !regexp.MustCompile(`(?m)^owner-mac/\*\s+-\s+-\s+-\s+offline\s+last seen .* ago$`).MatchString(r.stdout) || withoutInboxNote(r.stderr) != "" {
		t.Fatalf("project list text, owner-mac offline: %+v", r)
	}
	if m := one(agent("", 30*time.Second, "session", "list", "-o", "json")); m["exit_code"] != float64(0) || len(m["sessions"].([]any)) != 0 || len(m["offline"].([]any)) != 1 {
		t.Fatalf("session list, owner-mac offline: %v", m)
	}
	if r := agent("", 30*time.Second, "session", "list"); r.code != 0 || !strings.Contains(r.stdout, "offline: owner-mac (last seen ") {
		t.Fatalf("session list text, owner-mac offline: %+v", r)
	}
	for _, args := range [][]string{{"session", "get", id}, {"session", "start", "--project", "owner-mac/demo", "--prompt", "x"}, {"session", "start", "--project", "demo", "--prompt", "x"}} {
		r := agent("", 30*time.Second, append(args, "-o", "json")...)
		if e := errOf(one(r)); r.code != 4 || e["kind"] != "unreachable" || e["retryable"] != true || !strings.Contains(e["message"].(string), "offline") || !strings.Contains(e["hint"].(string), "once it is back") {
			t.Fatalf("%v, owner-mac offline: %+v", args, r)
		}
	}

	// Usage errors report the exit code the process exits with.
	if r := agent("", 20*time.Second, "session", "get", "-o", "json"); r.code != 2 || one(r)["exit_code"] != float64(2) {
		t.Fatalf("missing argument: %+v", r)
	}
}
