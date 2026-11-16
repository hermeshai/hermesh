package machine

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	inboxv1 "github.com/hermeshai/hermesh/gen/go/hermesh/inbox/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

// A session whose agent could not start is news of that failure, with what
// the agent said, each time it fails; a turn that crashed is news once, not
// again for the crashed status that follows it.
func TestCrashNews(t *testing.T) {
	refused := errors.New("codex thread/resume: thread 01a1 already has an active writer (code -32600)")
	p := &startTrouble{}
	p.set(1, refused, false)
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"flaky": p})
	var mu sync.Mutex
	var news []*inboxv1.SessionNews
	h.s.notice = func(s noticeSession, e *sessionv1.SessionEvent) {
		if m := h.n.newsMessage(&inboxv1.Address{Inbox: "c_asker"}, s, e); m != nil {
			mu.Lock()
			news = append(news, m.SessionNews)
			mu.Unlock()
		}
	}
	// told waits for the n-th news, and for none to follow it.
	told := func(n int) *inboxv1.SessionNews {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			got := len(news)
			mu.Unlock()
			if got >= n || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(news) != n {
			t.Fatalf("%d news, want %d: %v", len(news), n, news)
		}
		return news[n-1]
	}
	startFailed := func(n *inboxv1.SessionNews) {
		t.Helper()
		if n.Kind != "failure" || n.Failure != sessionv1.FailureKind_FAILURE_KIND_START_FAILED || !strings.Contains(n.FailureMessage, "already has an active writer") {
			t.Fatalf("news of the failed start: %v", n)
		}
	}

	id := h.startAgent("flaky", "say one")
	h.wait(id, crashed)
	startFailed(told(1))

	p.set(1, refused, false)
	h.send(id, "say two")
	h.wait(id, crashed)
	startFailed(told(2))

	h.send(id, "crash")
	h.wait(id, crashed)
	if n := told(3); n.Kind != "failure" || n.Failure != sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED || n.FailureMessage == "" || n.Turn != 1 {
		t.Fatalf("news of the crashed turn: %v", n)
	}
}
