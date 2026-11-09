package machine

import (
	"context"
	"strings"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// capacity is Codex's message for a model at capacity, which Codex itself
// does not retry.
const capacity = "Selected model is at capacity. Please try a different model."

func retryLimits(delay time.Duration) Limits {
	lim := DefaultLimits()
	lim.RetryDelay = delay
	return lim
}

// retryNotes are the error events that say the turn retries.
func (h *harness) retryNotes(id string) []string {
	var out []string
	for _, e := range h.events(id) {
		if x := e.GetError(); x != nil && strings.Contains(x.Message, "retrying in") {
			out = append(out, x.Message)
		}
	}
	return out
}

func (h *harness) count(id string, is func(*sessionv1.SessionEvent) bool) int {
	n := 0
	for _, e := range h.events(id) {
		if is(e) {
			n++
		}
	}
	return n
}

func turnStarted(e *sessionv1.SessionEvent) bool   { return e.GetTurnStarted() != nil }
func turnCompleted(e *sessionv1.SessionEvent) bool { return e.GetTurnCompleted() != nil }

// A turn stopped by a model at capacity goes on by itself in the same
// conversation, still the same turn, and ends well.
func TestRateLimitRetriesInTheSameTurn(t *testing.T) {
	h := newHarness(t, retryLimits(20*time.Millisecond))
	id := h.start("say one")
	ref := h.wait(id, idle).ProviderSessionRef
	h.send(id, "say working\nfail-times 2 "+capacity+"\nsay done")
	sess := h.wait(id, idle)
	tc := h.lastTurn(id)
	if tc.IsError || tc.Turn != 2 || tc.Result != "done" || sess.Progress.LastFailure != nil || sess.ProviderSessionRef != ref {
		t.Fatalf("turn %v, session %v\n%s", tc, sess, h.dump(id))
	}
	notes := h.retryNotes(id)
	if len(notes) != 2 || !strings.HasPrefix(notes[0], capacity+" (retrying in 20ms, 1 of 3)") || !strings.HasPrefix(notes[1], capacity+" (retrying in 80ms, 2 of 3)") {
		t.Fatalf("retry notes: %q", notes)
	}
	if n := h.count(id, turnStarted); n != 2 {
		t.Fatalf("%d turns started", n)
	}
	if n := h.count(id, turnCompleted); n != 2 {
		t.Fatalf("%d turns completed", n)
	}
	// One process for the first two turns, then one resumed per retry.
	if h.rec.count() != 3 || h.rec.spec(1).ResumeRef != ref || h.rec.spec(2).ResumeRef != ref ||
		!strings.HasPrefix(h.rec.spec(2).Prompt, "[Hermesh] Your last reply was stopped by a rate limit") {
		t.Fatalf("starts: %+v", h.rec.starts)
	}
	for _, e := range h.events(id) {
		if strings.Contains(e.GetUserMessage().GetText(), "[Hermesh]") {
			t.Fatalf("the retry note is shown as a message: %v", e)
		}
	}
}

// When the retries run out, the turn fails as a rate limit; without a
// retry delay it fails at once.
func TestRateLimitRetriesRunOut(t *testing.T) {
	h := newHarness(t, retryLimits(5*time.Millisecond))
	id := h.start("fail-times 9 " + capacity)
	h.wait(id, idle)
	tc := h.lastTurn(id)
	if !tc.IsError || tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT || !tc.Failure.Retryable || tc.Failure.Message != capacity {
		t.Fatalf("turn: %v", tc)
	}
	if notes := h.retryNotes(id); len(notes) != 3 || h.rec.count() != 4 || h.count(id, turnCompleted) != 1 {
		t.Fatalf("notes %q, %d starts\n%s", notes, h.rec.count(), h.dump(id))
	}

	h = newHarness(t, retryLimits(0))
	id = h.start("fail API Error: 429 Too Many Requests")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT || len(h.retryNotes(id)) != 0 || h.rec.count() != 1 {
		t.Fatalf("turn without retries: %v", tc)
	}
}

// waitRetry waits until the session's turn waits to retry.
func (h *harness) waitRetry(id string) {
	h.t.Helper()
	waitFor(h.t, "a retry note", func() bool { return len(h.retryNotes(id)) > 0 })
	if st := h.lastStatus(id); st != sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
		h.t.Fatalf("waiting to retry, the session is %v", st)
	}
}

// A message while the turn waits to retry: it goes on now, with the message.
func TestMessageDuringRetryWaitGoesOnNow(t *testing.T) {
	h := newHarness(t, retryLimits(time.Hour))
	id := h.start("say one")
	h.wait(id, idle)
	h.send(id, "fail-times 1 "+capacity)
	h.waitRetry(id)
	h.send(id, "say with the message")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.IsError || tc.Turn != 2 || tc.Result != "with the message" {
		t.Fatalf("turn: %v\n%s", tc, h.dump(id))
	}
	if p := h.rec.spec(-1).Prompt; !strings.Contains(p, "automatic retry") || !strings.HasSuffix(p, "\n\nsay with the message") {
		t.Fatalf("the agent got %q", p)
	}
	var shown []string
	for _, e := range h.events(id) {
		if m := e.GetUserMessage(); m != nil {
			shown = append(shown, m.Text)
		}
	}
	if len(shown) != 3 || shown[2] != "say with the message" || h.count(id, turnStarted) != 2 {
		t.Fatalf("messages shown: %q\n%s", shown, h.dump(id))
	}
}

// Stopped while it waits to retry, the turn ends interrupted and does not
// retry; the next message goes on as usual.
func TestInterruptDuringRetryWait(t *testing.T) {
	h := newHarness(t, retryLimits(time.Hour))
	id := h.start("fail-times 1 " + capacity)
	h.waitRetry(id)
	if _, err := h.s.Interrupt(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
		t.Fatalf("turn: %v", tc)
	}
	h.s.mu.Lock()
	l := h.s.live[id]
	h.s.mu.Unlock()
	l.mu.Lock()
	waiting := l.retryTimer != nil
	l.mu.Unlock()
	if waiting || h.rec.count() != 1 {
		t.Fatalf("still waiting to retry: %v, %d starts", waiting, h.rec.count())
	}
	h.send(id, "say next")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.IsError || tc.Result != "next" || len(h.retryNotes(id)) != 1 {
		t.Fatalf("next turn: %v", tc)
	}
}
