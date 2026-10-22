package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestReliability walks the failure paths an agent meets in practice, with
// real binaries and a scripted coding agent (docs/reliability-plan.md):
// classified failures and exit codes, a stalled turn, a silent long tool,
// and the daemon being killed while a CLI waits for a turn.
func TestReliability(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{}, "--idle-timeout", "1500ms")

	s, r := w.startSession(time.Minute, "--agent", "claude", "--wait", "--prompt", "say hello")
	if r.code != 0 || s.Status != "idle" || s.Ref == "" {
		t.Fatalf("start: %+v", r)
	}
	id, ref := s.SessionID, s.Ref

	// A provider rate limit: retryable (exit 7), classified, with advice.
	got, r := w.send(time.Minute, id, `fail API Error: 429 {"type":"error","error":{"type":"rate_limit_error"}}`)
	if r.code != 7 || got.LastFailure == nil || got.LastFailure.Kind != "rate_limit" || !got.LastFailure.Retryable {
		t.Fatalf("rate limit: %+v", r)
	}
	text := w.cli(time.Minute, "session", "send", id, "--wait", "--message", "fail API Error: 401 Invalid authentication credentials")
	if text.code != 5 || !strings.Contains(text.stderr, "auth: API Error: 401") || !strings.Contains(text.stderr, "log in") {
		t.Fatalf("auth failure, text output: %+v", text)
	}
	if g := w.cli(30*time.Second, "session", "get", id); !strings.Contains(g.stdout, "failure: auth:") {
		t.Fatalf("get: %+v", g)
	}

	// The agent goes silent: stopped after the idle window, and the next
	// message resumes the same provider session.
	start := time.Now()
	if got, r := w.send(time.Minute, id, "say thinking\nhang"); r.code != 7 || got.LastFailure == nil || got.LastFailure.Kind != "stalled" {
		t.Fatalf("stall: %+v", r)
	} else if time.Since(start) > 30*time.Second {
		t.Fatalf("stall detected after %v", time.Since(start))
	}
	if r := w.cli(time.Minute, "session", "send", id, "--wait", "--message", "resumed"); r.code != 0 || !strings.Contains(r.stdout, "resumed: "+ref) {
		t.Fatalf("after the stall: %+v", r)
	}

	// A tool that runs silently past the idle window is not a stall.
	if r := w.cli(time.Minute, "session", "send", id, "--wait", "--message", "tool-sleep 4s\nsay tool done"); r.code != 0 || !strings.Contains(r.stdout, "tool done") {
		t.Fatalf("long tool: %+v", r)
	}

	// The relay restarts while a CLI waits for a turn: it reconnects and
	// gets the result, and every event is stored once.
	waiting := start2(t, w, "session", "send", id, "--wait", "--message", "chatter 6s\nsay across the relay restart")
	eventually(t, 30*time.Second, "turn running", func() bool { return w.get(id).Status == "running" })
	w.restartRelay()
	if c := waiting.wait(t, 2*time.Minute); c != 0 || !strings.Contains(waiting.output(), "across the relay restart") {
		t.Fatalf("waiting CLI after the relay restart exited %d:\n%s", c, waiting.output())
	}
	evs, turns := w.events(id), map[int]int{}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Fatalf("event %d has seq %d", i+1, e.Seq)
		}
		if e.Type == "turn_started" {
			turns[e.Turn]++
		}
	}
	if turns[1] != 1 || turns[7] != 1 {
		t.Fatalf("turns started: %v", turns)
	}

	// kill -9 the daemon while a CLI waits for a turn: the waiting CLI ends
	// with exit 7 once the machine is back, the session is crashed with
	// a reason, and the next message resumes it.
	waiter := start2(t, w, "session", "send", id, "--wait", "--message", "chatter 120s")
	eventually(t, 30*time.Second, "turn running", func() bool { return w.get(id).Status == "running" })
	w.restartOwner(true)
	if c := waiter.wait(t, 2*time.Minute); c != 7 {
		t.Fatalf("waiting CLI exited %d:\n%s", c, waiter.output())
	}
	if g := w.get(id); g.Status != "crashed" || g.LastFailure == nil || !strings.Contains(g.LastFailure.Message, "hermeshd restarted") {
		t.Fatalf("after the restart: %+v", g)
	}
	if r := w.cli(time.Minute, "session", "send", id, "--wait", "--message", "say back again"); r.code != 0 || !strings.Contains(r.stdout, "back again") {
		t.Fatalf("resume after the restart: %+v", r)
	}
}

// start2 starts the agent CLI in the background.
func start2(t *testing.T, w *ownerMac, args ...string) *proc {
	p := start(t, "waiting cli", w.cliEnv, filepath.Join(w.bin, "hermesh"), args...)
	w.logs = append(w.logs, p)
	return p
}
