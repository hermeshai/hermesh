package machine

import (
	"context"
	"slices"
	"testing"
	"time"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// firstWatch is the first message of a Watch with req.
func (h *harness) firstWatch(req *machinev1.WatchRequest) *machinev1.WatchResponse {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan *machinev1.WatchResponse, 1)
	go h.n.Watch(ctx, &Principal{Kind: PrincipalOwner}, req, func(m *machinev1.WatchResponse) error {
		select {
		case got <- m:
		default:
		}
		return nil
	})
	select {
	case m := <-got:
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatal("no first message")
		return nil
	}
}

func ids(list []*sessionv1.Session) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.SessionId)
	}
	slices.Sort(out)
	return out
}

// A watcher that reconnects gets only what changed since its token, a
// deletion included, and nothing when nothing changed; after this machine
// started again its token is refused, and the fresh start says what was
// deleted since the watcher's last message.
func TestWatchResumes(t *testing.T) {
	h := newHarness(t, Limits{MaxConcurrent: 4})
	first := h.start("say one")
	h.wait(first, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	fresh := h.firstWatch(&machinev1.WatchRequest{View: sessionv1.SessionView_SESSION_VIEW_BASIC})
	if !fresh.Reset_ || !slices.Equal(ids(fresh.Sessions), []string{first}) || fresh.Status == nil || len(fresh.Projects) != 1 || len(fresh.ResumeToken) != 16 {
		t.Fatalf("fresh start: %v", fresh)
	}
	if fresh.Sessions[0].Result != "" {
		t.Error("BASIC carries the reply")
	}

	second := h.start("say two")
	h.wait(second, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if err := h.s.Delete(context.Background(), &Principal{Kind: PrincipalOwner}, first); err != nil {
		t.Fatal(err)
	}
	resumed := h.firstWatch(&machinev1.WatchRequest{ResumeToken: fresh.ResumeToken, HeldAt: fresh.SentAt})
	if resumed.Reset_ || !slices.Equal(ids(resumed.Sessions), []string{second}) || !slices.Equal(resumed.DeletedSessionIds, []string{first}) {
		t.Fatalf("resumed: reset %v, sessions %v, deleted %v", resumed.Reset_, ids(resumed.Sessions), resumed.DeletedSessionIds)
	}
	again := h.firstWatch(&machinev1.WatchRequest{ResumeToken: resumed.ResumeToken})
	if again.Reset_ || len(again.Sessions) != 0 || len(again.DeletedSessionIds) != 0 {
		t.Fatalf("resumed with nothing changed: %v", again)
	}

	h.s.changeLog = newChangeLog() // as after hermeshd started again
	restarted := h.firstWatch(&machinev1.WatchRequest{ResumeToken: again.ResumeToken, HeldAt: fresh.SentAt})
	if !restarted.Reset_ || !slices.Equal(ids(restarted.Sessions), []string{second}) || !slices.Equal(restarted.DeletedSessionIds, []string{first}) {
		t.Fatalf("after a restart: reset %v, sessions %v, deleted %v", restarted.Reset_, ids(restarted.Sessions), restarted.DeletedSessionIds)
	}
}
