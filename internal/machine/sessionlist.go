package machine

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Session lists page newest first by (updated_at, session_id) and are kept
// current by watches (SessionService.ListSessions and WatchSessions).

// Unsettled sessions need the owner or have a turn in progress: queued,
// preparing, running, waiting for an answer or crashed, or their last turn
// failed (not on purpose). The first page of a list carries all of them.
func Unsettled(s *sessionv1.Session) bool {
	switch s.Status {
	case sessionv1.SessionStatus_SESSION_STATUS_QUEUED, sessionv1.SessionStatus_SESSION_STATUS_PREPARING, sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
		sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT, sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
		return true
	case sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED:
		return false
	}
	f := s.GetProgress().GetLastFailure()
	return f != nil && f.Kind != sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED
}

// NewestFirst orders sessions as lists page them.
func NewestFirst(a, b *sessionv1.Session) int {
	if c := b.UpdatedAt.AsTime().Compare(a.UpdatedAt.AsTime()); c != 0 {
		return c
	}
	return strings.Compare(b.SessionId, a.SessionId)
}

// CursorAt names s as where a page ends.
func CursorAt(s *sessionv1.Session) *sessionv1.SessionCursor {
	return &sessionv1.SessionCursor{UpdatedAt: s.UpdatedAt, SessionId: s.SessionId}
}

// Past says whether s comes after the cursor c (always, without one).
func Past(s *sessionv1.Session, c *sessionv1.SessionCursor) bool {
	return c == nil || NewestFirst(&sessionv1.Session{UpdatedAt: c.UpdatedAt, SessionId: c.SessionId}, s) < 0
}

// listPage is List with a limit: live sessions come from memory, archived
// ones from the store (their stored updated_at is in whole seconds).
func (s *Sessions) listPage(p *Principal, req *sessionv1.ListSessionsRequest) ([]*sessionv1.Session, *sessionv1.SessionCursor, error) {
	keep := func(r *sessionRow) bool {
		return s.canRead(p, r) && (req.Status == 0 || r.Status == req.Status) && (req.Since == nil || !r.UpdatedAt.Before(req.Since.AsTime()))
	}
	limit := int(req.Limit)
	var page, unsettled []*sessionv1.Session
	liveIDs := map[string]bool{}
	s.mu.Lock()
	ls := make([]*live, 0, len(s.live))
	for _, l := range s.live {
		ls = append(ls, l)
	}
	s.mu.Unlock()
	for _, l := range ls {
		liveIDs[l.id] = true
		l.mu.Lock()
		var sess *sessionv1.Session
		if keep(l.row) {
			sess = s.toProto(l.row)
		}
		l.mu.Unlock()
		if sess == nil {
			continue
		}
		if Past(sess, req.Before) {
			page = append(page, sess)
		}
		if req.Before == nil && Unsettled(sess) {
			unsettled = append(unsettled, sess)
		}
	}
	// The stored sessions past the cursor, newest first, until limit+1 of
	// them are not live (those are read from memory above).
	q, args := `1`, []any{}
	if c := req.Before; c != nil {
		t := c.UpdatedAt.AsTime()
		if t.Nanosecond() > 0 {
			q, args = `updated_at <= ?`, []any{t.Unix()}
		} else {
			q, args = `updated_at < ? OR (updated_at = ? AND session_id < ?)`, []any{t.Unix(), t.Unix(), c.SessionId}
		}
	}
	stored := 0
	err := s.st.EachSession(`(`+q+`) ORDER BY updated_at DESC, session_id DESC`, args, func(r *sessionRow) bool {
		if !liveIDs[r.ID] && keep(r) {
			page = append(page, s.toProto(r))
			stored++
		}
		return stored <= limit
	})
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(page, NewestFirst)
	var next *sessionv1.SessionCursor
	if len(page) > limit {
		page = page[:limit]
		next = CursorAt(page[limit-1])
	}
	for _, u := range unsettled {
		if !slices.ContainsFunc(page, func(x *sessionv1.Session) bool { return x.SessionId == u.SessionId }) {
			page = append(page, u)
		}
	}
	return page, next, nil
}

// watcher is one WatchSessions stream: the sessions changed since it last
// sent, and a kick when that set grows.
type watcher struct {
	mu    sync.Mutex
	dirty map[string]bool
	kick  chan struct{}
}

// watchEvery spaces a watch's messages, so a busy session costs one
// message per interval, not one per event.
const watchEvery = 100 * time.Millisecond

// changed tells the watches that session id changed (after it was stored).
func (s *Sessions) changed(id string) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	for w := range s.watchers {
		w.mu.Lock()
		w.dirty[id] = true
		w.mu.Unlock()
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
}

// Watch sends the sessions p can read as they change (see
// SessionService.WatchSessions) until ctx is done.
func (s *Sessions) Watch(ctx context.Context, p *Principal, send func(*sessionv1.WatchSessionsResponse) error) error {
	w := &watcher{dirty: map[string]bool{}, kick: make(chan struct{}, 1)}
	s.watchMu.Lock()
	if s.watchers == nil {
		s.watchers = map[*watcher]bool{}
	}
	s.watchers[w] = true
	s.watchMu.Unlock()
	defer func() {
		s.watchMu.Lock()
		delete(s.watchers, w)
		s.watchMu.Unlock()
	}()
	if err := send(&sessionv1.WatchSessionsResponse{}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.kick:
		}
		w.mu.Lock()
		ids := w.dirty
		w.dirty = map[string]bool{}
		w.mu.Unlock()
		res := &sessionv1.WatchSessionsResponse{}
		for id := range ids {
			if sess, err := s.Get(p, id); err == nil { // not found: not the caller's to read
				res.Sessions = append(res.Sessions, sess)
			}
		}
		if len(res.Sessions) > 0 {
			slices.SortFunc(res.Sessions, NewestFirst)
			if err := send(res); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(watchEvery):
		}
	}
}
