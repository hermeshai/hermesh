package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestRateLimitRetry: Codex stops a turn on a model at capacity and does
// not retry it itself. The machine goes on with the turn by itself after a
// wait, in the same Codex thread, and the agent that started it hears of
// the turn once, when it is done; the failure appears once per retry. When
// the retries run out, the turn fails as a rate limit.
func TestRateLimitRetry(t *testing.T) {
	unixOnly(t, "its fake codex is a shell script")
	if testing.Short() {
		t.Skip("e2e")
	}
	dir := t.TempDir()
	busy, threads := filepath.Join(dir, "busy"), filepath.Join(dir, "threads")
	codex := filepath.Join(dir, "codex")
	os.WriteFile(codex, []byte(`#!/bin/sh
case "$1" in
--version) echo "codex-cli 0.164.0";;
login) echo "Logged in using ChatGPT";;
*) FAKE_CODEX_OVERLOADED=`+busy+` FAKE_CODEX_LOG=`+threads+` exec `+filepath.Join(binaries(t), "fakecodex")+` "$@";;
esac
`), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_CODEX_BIN=" + codex}, &localv1.GrantSpec{Agents: []string{"codex"}}, "--retry-delay", "300ms")

	type event struct {
		Turn    int
		Type    string
		Message string
		IsError bool `json:"is_error"`
		Failure *struct{ Kind string }
	}
	turnEvents := func(id string, turn int) (errors []string, started, completed []event) {
		var page struct{ Events []event }
		json.Unmarshal([]byte(w.cli(time.Minute, "session", "events", id, "--limit", "1000", "-o", "json").stdout), &page)
		for _, e := range page.Events {
			switch {
			case e.Turn != turn:
			case e.Type == "error":
				if e.Failure == nil || e.Failure.Kind != "rate_limit" {
					t.Fatalf("error event: %+v", e)
				}
				errors = append(errors, e.Message)
			case e.Type == "turn_started":
				started = append(started, e)
			case e.Type == "turn_completed":
				completed = append(completed, e)
			}
		}
		return errors, started, completed
	}
	const capacity = "Selected model is at capacity. Please try a different model."

	// Twice at capacity, then the turn goes through: one turn, news once.
	os.WriteFile(busy, []byte("2"), 0o644)
	s, r := w.startSession(time.Minute, "--agent", "codex", "--prompt", "fix the flaky test")
	if s.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	m := w.waitMessage()
	if m.News == nil || m.News.Kind != "turn_done" || m.News.SessionID != s.SessionID || m.News.Turn != 1 ||
		!strings.HasPrefix(m.News.Result, "done: [Hermesh] Your last reply was stopped by a rate limit") {
		t.Fatalf("the first news is not the retried turn's: %+v", m)
	}
	w.ack(m.ID)
	errs, started, completed := turnEvents(s.SessionID, 1)
	if len(errs) != 2 || errs[0] != capacity+" (retrying in 300ms, 1 of 3)" || errs[1] != capacity+" (retrying in 1.2s, 2 of 3)" {
		t.Fatalf("errors shown: %q", errs)
	}
	if len(started) != 1 || len(completed) != 1 || completed[0].IsError {
		t.Fatalf("turn started %+v, completed %+v", started, completed)
	}
	if b, _ := os.ReadFile(threads); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("the retries started new Codex threads, not resumed the one:\n%s", b)
	}
	if got := w.get(s.SessionID); got.Status != "idle" || got.LastFailure != nil {
		t.Fatalf("after the retried turn: %+v", got)
	}

	// At capacity until the retries run out: the turn fails as a rate limit.
	os.WriteFile(busy, []byte("9"), 0o644)
	start := time.Now()
	got, r := w.send(time.Minute, s.SessionID, "try once more")
	if r.code != 7 || got.LastFailure == nil || got.LastFailure.Kind != "rate_limit" || got.LastFailure.Message != capacity || !got.LastFailure.Retryable {
		t.Fatalf("retries run out: %+v", r)
	}
	if took := time.Since(start); took < 4500*time.Millisecond {
		t.Fatalf("failed after %s, before the three waits (0.3s, 1.2s, 3s)", took)
	}
	errs, _, completed = turnEvents(s.SessionID, 2)
	if len(errs) != 3 || !strings.HasSuffix(errs[2], "(retrying in 3s, 3 of 3)") || len(completed) != 1 || !completed[0].IsError {
		t.Fatalf("errors %q, completed %+v", errs, completed)
	}
}
