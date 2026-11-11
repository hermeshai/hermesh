package machine

import (
	"fmt"
	"testing"
	"time"
)

// Notices the relay did not take wait per thread: the newest of a thread
// replaces the one waiting, a late older one does not; an answered
// question's notice is dropped; and no more than noticesKept threads wait,
// the oldest giving way.
func TestWaitingNotices(t *testing.T) {
	n := &Machine{}
	waiting := func(thread string) string {
		n.notices.mu.Lock()
		defer n.notices.mu.Unlock()
		return n.notices.waiting[thread].kind
	}
	base := time.Now()
	n.keepNotice("s_1", waitingNotice{at: base, kind: "question"})
	n.keepNotice("s_1", waitingNotice{at: base.Add(time.Second), kind: "turn_done"})
	n.keepNotice("s_1", waitingNotice{at: base.Add(-time.Second), kind: "session_started"}) // sent late
	if got := waiting("s_1"); got != "turn_done" {
		t.Fatalf("waiting for s_1: %q, want the newest (turn_done)", got)
	}

	n.keepNotice("s_2", waitingNotice{at: base, kind: "question"})
	n.dropWaitingNotice("s_1", "question") // not a question: stays
	n.dropWaitingNotice("s_2", "question")
	if waiting("s_1") != "turn_done" || waiting("s_2") != "" {
		t.Fatalf("after the answer: s_1 %q, s_2 %q", waiting("s_1"), waiting("s_2"))
	}

	for i := range noticesKept + 5 {
		n.keepNotice(fmt.Sprintf("t_%d", i), waitingNotice{at: base.Add(time.Duration(i+2) * time.Second), kind: "failure"})
	}
	n.notices.mu.Lock()
	count := len(n.notices.waiting)
	n.notices.mu.Unlock()
	if count != noticesKept {
		t.Fatalf("%d threads wait, want %d", count, noticesKept)
	}
	if waiting("s_1") != "" || waiting("t_0") != "" || waiting(fmt.Sprintf("t_%d", noticesKept+4)) != "failure" {
		t.Fatal("the oldest threads did not give way to the newest")
	}
}
