package machine

import (
	"context"
	"slices"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Pages of sessions come newest first, without gaps or repeats, across
// live sessions and archived ones read from the store (whose updated_at is
// in whole seconds); the first page also carries the unsettled ones.
func TestListPages(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	failed := h.start("fail boom")
	h.wait(failed, idle)
	var ids []string
	for range 12 {
		ids = append(ids, h.start("say hi"))
	}
	for _, id := range ids {
		h.wait(id, idle)
	}
	for _, id := range ids[3:8] {
		if _, err := h.s.Archive(context.Background(), h.p, id); err != nil {
			t.Fatal(err)
		}
	}
	all, _, err := h.s.List(h.p, &sessionv1.ListSessionsRequest{})
	if err != nil || len(all) != 13 {
		t.Fatalf("all: %d %v", len(all), err)
	}

	const limit = 4
	var got []*sessionv1.Session
	var before *sessionv1.SessionCursor
	for page := 0; ; page++ {
		list, next, err := h.s.List(h.p, &sessionv1.ListSessionsRequest{Limit: limit, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 {
			if len(list) != limit+1 || list[limit].SessionId != failed {
				t.Fatalf("first page %v: wants %d and then the failed session %s", sessionIDs(list), limit, failed)
			}
			list = list[:limit]
		}
		got = append(got, list...)
		if next == nil {
			break
		}
		if len(list) != limit || next.SessionId != list[limit-1].SessionId {
			t.Fatalf("page %d: %v, next %v", page, sessionIDs(list), next)
		}
		before = next
	}
	if !slices.IsSortedFunc(got, NewestFirst) {
		t.Fatalf("not newest first: %v", sessionIDs(got))
	}
	slices.SortFunc(all, NewestFirst)
	if !slices.Equal(sessionIDs(got), sessionIDs(all)) {
		t.Fatalf("pages %v\nall   %v", sessionIDs(got), sessionIDs(all))
	}
}

// A watch starts with an empty message, then sends each session the
// caller can read as it is started and changes; not others'.
func TestWatchSessions(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch := func(p *Principal) chan *sessionv1.WatchSessionsResponse {
		ch := make(chan *sessionv1.WatchSessionsResponse, 100)
		go h.s.Watch(ctx, p, func(m *sessionv1.WatchSessionsResponse) error {
			ch <- m
			return nil
		})
		if m := <-ch; len(m.Sessions) != 0 {
			t.Fatalf("first message %v", m)
		}
		return ch
	}
	mine := watch(h.p)
	other := watch(&Principal{Kind: PrincipalClient, ClientID: "c_other", Grant: &identityv1.Grant{GrantId: "g_other", ProjectIds: []string{"*"}}})

	id := h.start("say hi")
	seen := map[sessionv1.SessionStatus]bool{}
	deadline := time.After(5 * time.Second)
	for !seen[idle] {
		select {
		case m := <-mine:
			for _, s := range m.Sessions {
				if s.SessionId != id {
					t.Fatalf("watched %s", s.SessionId)
				}
				seen[s.Status] = true
			}
		case <-deadline:
			t.Fatalf("saw %v, not idle", seen)
		}
	}
	if !seen[sessionv1.SessionStatus_SESSION_STATUS_QUEUED] && !seen[sessionv1.SessionStatus_SESSION_STATUS_PREPARING] && !seen[sessionv1.SessionStatus_SESSION_STATUS_RUNNING] {
		t.Fatalf("the session's start was not watched: %v", seen)
	}
	select {
	case m := <-other:
		t.Fatalf("another client watched %v", m)
	case <-time.After(3 * watchEvery):
	}
}

func sessionIDs(ss []*sessionv1.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.SessionId)
	}
	return out
}
