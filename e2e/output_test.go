package e2e

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestCLIOutputContract holds every command's JSON to docs/cli-output.md:
// one document starting with ok, exit_code and error; an error with a
// kind that matches the exit code; the documented session and event
// shapes; lowercase values, numbers as numbers, nothing escaped.
func TestCLIOutputContract(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	bin := filepath.Join(w.bin, "hermesh")
	kinds := map[int][]string{1: {"internal"}, 2: {"usage", "not_found", "invalid_state"}, 3: {"not_paired", "revoked", "denied"}, 4: {"unreachable"},
		5: {"archived", "auth", "quota", "context_overflow", "model_unavailable", "invalid_request", "refused", "limit_reached", "resume_rejected", "timeout", "start_failed", "interrupted", "unknown"},
		6: {"wait_timeout"}, 7: {"rate_limit", "provider_error", "network", "agent_crashed", "stalled", "busy"}, 8: {"needs_input"}}
	protoEnum := regexp.MustCompile(`"(SESSION_STATUS|FAILURE_KIND|WORKSPACE|QUESTION_RESOLUTION|TASK_VISIBILITY)_`)
	// doc runs a command with -o json and checks the envelope.
	doc := func(env []string, wantCode int, args ...string) map[string]any {
		t.Helper()
		r := run(t, env, time.Minute, bin, append(args, "-o", "json")...)
		if r.code != wantCode {
			t.Fatalf("%v: exit %d, want %d\n%s%s", args, r.code, wantCode, r.stdout, r.stderr)
		}
		if !strings.HasPrefix(r.stdout, "{\n  \"ok\": ") || !strings.Contains(r.stdout, "\n  \"exit_code\": ") {
			t.Fatalf("%v: the document does not start with ok, exit_code:\n%s", args, r.stdout)
		}
		if protoEnum.MatchString(r.stdout) || strings.Contains(r.stdout, `\u003c`) {
			t.Fatalf("%v: a raw enum or an escape in:\n%s", args, r.stdout)
		}
		m := jsonDoc(t, r)
		e, isErr := m["error"].(map[string]any)
		switch {
		case wantCode == 0 && (m["ok"] != true || m["error"] != nil):
			t.Fatalf("%v: success with ok=%v error=%v", args, m["ok"], m["error"])
		case wantCode != 0 && (m["ok"] != false || !isErr):
			t.Fatalf("%v: failure with ok=%v error=%v", args, m["ok"], m["error"])
		case isErr:
			for _, k := range []string{"kind", "message", "hint", "retryable"} {
				if _, ok := e[k]; !ok {
					t.Fatalf("%v: error without %s: %v", args, k, e)
				}
			}
			if !slices.Contains(kinds[wantCode], e["kind"].(string)) {
				t.Fatalf("%v: kind %q with exit code %d", args, e["kind"], wantCode)
			}
		}
		return m
	}
	sessionKeys := []string{"session_id", "title", "project", "machine", "machine_id", "project_id", "agent", "agent_session_id", "workspace", "branch", "status",
		"turns", "current_tool", "last_message", "result", "question", "last_failure", "diff_stat", "usage", "last_event_seq", "created_by", "created_at", "updated_at"}
	checkSession := func(what string, s any) map[string]any {
		t.Helper()
		m, _ := s.(map[string]any)
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		want := slices.Clone(sessionKeys)
		slices.Sort(want)
		if !slices.Equal(keys, want) {
			t.Fatalf("%s: session keys %v, want %v", what, keys, want)
		}
		for _, k := range []string{"turns", "last_event_seq"} {
			if _, ok := m[k].(float64); !ok {
				t.Fatalf("%s: %s is not a number: %v", what, k, m[k])
			}
		}
		if _, ok := m["usage"].(map[string]any)["input_tokens"].(float64); !ok {
			t.Fatalf("%s: usage.input_tokens is not a number: %v", what, m["usage"])
		}
		return m
	}
	env := w.cliEnv

	doc(env, 0, "version")
	if r := run(t, append(env, "HERMESH_OUTPUT=json"), time.Minute, bin, "version"); !strings.HasPrefix(r.stdout, "{") {
		t.Fatalf("HERMESH_OUTPUT=json: %q", r.stdout)
	}
	if r := run(t, env, time.Minute, bin, "version", "-o", "yaml"); r.code != 2 {
		t.Fatalf("-o yaml: %+v", r)
	}
	doc(env, 0, "auth", "status")
	if pl := doc(env, 0, "project", "list"); len(pl["offline"].([]any)) != 0 {
		t.Fatalf("offline with every machine online: %v", pl["offline"])
	}
	if ps := doc(env, 0, "project", "list")["projects"].([]any); len(ps) != 1 || ps[0].(map[string]any)["project"] != "owner-mac/demo" {
		t.Fatalf("projects: %v", ps)
	}

	// Text: --wait prints the session exactly as get does; stdout has the
	// data, stderr only the session ID and the error.
	r := run(t, env, time.Minute, bin, "session", "start", "--project", "demo", "--wait", "--prompt", "write t.txt t\nsay Wrote t.txt.")
	tid := strings.Fields(r.stderr)[1]
	want := "session " + tid + " · idle · owner-mac/demo · claude · 1 turn\nchanges: 1 file changed, +1 -0 (`hermesh session diff " + tid + "`)\n\nWrote t.txt.\n"
	if r.code != 0 || r.stdout != want || r.stderr != "session "+tid+" in owner-mac/demo\n" {
		t.Fatalf("start --wait text:\n%s---\n%s", r.stdout, r.stderr)
	}
	if g := run(t, env, time.Minute, bin, "session", "get", tid); g.stdout != want || withoutInboxNote(g.stderr) != "" {
		t.Fatalf("get differs from --wait:\n%s---\n%s", g.stdout, g.stderr)
	}
	r = run(t, env, time.Minute, bin, "session", "send", tid, "--wait", "--message", "fail 429 rate limit")
	if r.code != 7 || !strings.Contains(r.stdout, "\nfailure: rate_limit: ") || !strings.HasPrefix(r.stderr, "error: session "+tid+": rate_limit: ") || strings.Count(r.stderr, "\n") != 1 {
		t.Fatalf("failed turn text:\n%s---\n%s", r.stdout, r.stderr)
	}
	r = run(t, env, time.Minute, bin, "session", "send", tid, "--wait", "--message", "ask Env|Where?|staging,prod\nsay ok")
	if r.code != 8 || !strings.HasPrefix(r.stdout, "session "+tid+" · waiting_input ·") || !strings.Contains(r.stdout, "? [Env] Where?\n  1. staging\n") || strings.Contains(r.stdout, "last: ") || !strings.HasPrefix(r.stderr, "error: ") {
		t.Fatalf("question text:\n%s---\n%s", r.stdout, r.stderr)
	}
	if r = run(t, env, time.Minute, bin, "session", "answer", tid, "--choose", "prod", "--wait"); r.code != 0 || !strings.HasSuffix(r.stdout, "\n\nok\n") {
		t.Fatalf("answer text:\n%s---\n%s", r.stdout, r.stderr)
	}
	if r = run(t, env, time.Minute, bin, "session", "list"); !strings.Contains(r.stdout, tid) || !strings.Contains(r.stdout, "   ok\n") {
		t.Fatalf("list shows the result of an idle session:\n%s", r.stdout)
	}
	if v := doc(env, 0, "session", "get", tid)["session"].(map[string]any); v["last_message"] != "" || v["result"] != "ok" {
		t.Fatalf("idle: last_message %q, result %q", v["last_message"], v["result"])
	}

	s := checkSession("start --wait", doc(env, 0, "session", "start", "--project", "demo", "--wait", "--prompt", "write a.txt a\nsay <done> & ok")["session"])
	id := s["session_id"].(string)
	if s["status"] != "idle" || s["result"] != "<done> & ok" || s["project"] != "owner-mac/demo" || s["workspace"] != "worktree" || s["agent_session_id"] == "" {
		t.Fatalf("session: %v", s)
	}
	checkSession("start", doc(env, 0, "session", "start", "--project", "demo", "--prompt", "say bg")["session"])
	checkSession("get", doc(env, 0, "session", "get", id)["session"])
	for _, x := range doc(env, 0, "session", "list")["sessions"].([]any) {
		checkSession("list", x)
	}
	failed := checkSession("failed turn", doc(env, 7, "session", "send", id, "--wait", "--message", "fail 429 rate limit")["session"])
	if f := failed["last_failure"].(map[string]any); f["kind"] != "rate_limit" || f["retryable"] != true {
		t.Fatalf("last_failure: %v", f)
	}
	asked := checkSession("question", doc(env, 8, "session", "send", id, "--wait", "--message", "ask Env|Where?|staging,prod\nsay ok")["session"])
	if q := asked["question"].(map[string]any); len(q["items"].([]any)) != 1 || asked["status"] != "waiting_input" || asked["last_message"] != "" {
		t.Fatalf("question: %v", asked)
	}
	checkSession("answer", doc(env, 0, "session", "answer", id, "--choose", "prod", "--wait")["session"])
	evs := doc(env, 0, "session", "events", id, "--limit", "1000")
	types := map[string]bool{"status": true, "turn_started": true, "user_message": true, "agent_message": true, "tool_call": true, "tool_result": true,
		"turn_completed": true, "error": true, "question_asked": true, "question_resolved": true}
	for _, e := range evs["events"].([]any) {
		e := e.(map[string]any)
		if _, ok := e["seq"].(float64); !ok || e["ts"] == nil || e["turn"] == nil || !types[e["type"].(string)] {
			t.Fatalf("event: %v", e)
		}
	}
	if p := evs["page"].(map[string]any); p["has_more_before"] != false || p["has_more_after"] != false {
		t.Fatalf("page: %v", p)
	}
	if d := doc(env, 0, "session", "diff", id)["diff"].(map[string]any); !strings.Contains(d["patch"].(string), "a.txt") || d["stat"].(map[string]any)["files_changed"] != float64(1) {
		t.Fatalf("diff: %v", d)
	}
	doc(env, 2, "session", "get")                 // usage
	doc(env, 2, "session", "get", "s_zzzzzzzz_x") // not_found
	if e := doc(env, 2, "session", "start", "--project", "nosuch", "--prompt", "x")["error"].(map[string]any); e["kind"] != "not_found" {
		t.Fatalf("no such project, every machine online: %v", e)
	}
	doc(env, 2, "session", "answer", id, "--text", "x") // invalid_state: no question
	checkSession("archive", doc(env, 0, "session", "archive", id)["session"])
	if e := doc(env, 2, "session", "send", id, "--message", "x")["error"].(map[string]any); e["kind"] != "invalid_state" || !strings.Contains(e["hint"].(string), "unarchive") {
		t.Fatalf("send to archived: %v", e)
	}
	checkSession("unarchive", doc(env, 0, "session", "unarchive", id)["session"])
	if l := doc(env, 0, "auth", "logout")["logout"].(map[string]any); l["removed"] != true || l["identity"] == nil {
		t.Fatalf("logout: %v", l)
	}
	if e := doc(env, 3, "project", "list")["error"].(map[string]any); e["kind"] != "not_paired" || !strings.Contains(e["hint"].(string), "hermesh auth login") {
		t.Fatalf("not paired: %v", e)
	}
}
