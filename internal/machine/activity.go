package machine

import (
	"cmp"
	"slices"
	"time"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Activity: the sessions that worked on a day, by hour and by the client
// that started them and the coding agent they ran, with the turns that
// ended that day. A session works while a turn is in progress: the
// turns kept in turn_usage (when they started and ended), and the turn a
// session working now has in progress (its progress.turn_started_at; a
// session that crashed mid-turn keeps it, but works no more).

type turnSpan struct {
	sessionID  string
	start, end int64
	tokens     uint64
}

// TurnSpans returns the turns that ended at or after since (unix seconds),
// with their tokens (input and output). A turn kept before its start was
// written starts when it ended.
func (s *Store) TurnSpans(since int64) ([]turnSpan, error) {
	rows, err := s.rdb.Query(`SELECT session_id, started_at, ended_at, input_tokens + output_tokens FROM turn_usage WHERE ended_at >= ? AND ended_at > 0`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []turnSpan
	for rows.Next() {
		var t turnSpan
		if err := rows.Scan(&t.sessionID, &t.start, &t.end, &t.tokens); err != nil {
			return nil, err
		}
		if t.start == 0 || t.start > t.end {
			t.start = t.end
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// doneNow: idle without a failure (a turn stopped on purpose did not
// fail), or archived.
func workingNow(r *sessionRow) bool {
	switch r.Status {
	case sessionv1.SessionStatus_SESSION_STATUS_QUEUED, sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
		sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
		return true
	}
	return false
}

func doneNow(r *sessionRow) bool {
	switch r.Status {
	case sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED:
		return true
	case sessionv1.SessionStatus_SESSION_STATUS_IDLE:
		f := r.Progress.GetLastFailure()
		return f == nil || f.Kind == sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED
	}
	return false
}

// Activity counts the sessions p can read that worked on req.Date (in
// req.TimeZone) up to now: by hour, client and agent, and by client and
// agent with those now done and the turns that ended that day.
func (s *Sessions) Activity(p *Principal, req *sessionv1.GetActivityRequest, now time.Time) (*sessionv1.GetActivityResponse, error) {
	loc := time.UTC
	if req.TimeZone != "" {
		l, err := time.LoadLocation(req.TimeZone)
		if err != nil {
			return nil, cerr(connect.CodeInvalidArgument, "unknown time zone %q", req.TimeZone)
		}
		loc = l
	}
	day, err := time.ParseInLocation(time.DateOnly, req.Date, loc)
	if err != nil {
		return nil, cerr(connect.CodeInvalidArgument, "date must be YYYY-MM-DD")
	}
	from, to := day, day.AddDate(0, 0, 1)
	if now.Before(to) {
		to = now
	}
	out := &sessionv1.GetActivityResponse{}
	if to.Before(from) {
		return out, nil
	}
	turns, err := s.st.TurnSpans(from.Unix())
	if err != nil {
		return nil, err
	}
	// A turn in progress may have written nothing today: those sessions too.
	rows, err := s.st.Sessions(`updated_at >= ? OR status IN (?,?,?,?)`, from.Unix(),
		int(sessionv1.SessionStatus_SESSION_STATUS_QUEUED), int(sessionv1.SessionStatus_SESSION_STATUS_PREPARING),
		int(sessionv1.SessionStatus_SESSION_STATUS_RUNNING), int(sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT))
	if err != nil {
		return nil, err
	}
	byID := map[string]*sessionRow{}
	spans := map[string][][2]time.Time{}
	type ended struct {
		turns  uint32
		tokens uint64
	}
	ends := map[string]*ended{}
	for _, r := range rows {
		if !s.canRead(p, r) {
			continue
		}
		byID[r.ID] = r
		if st := r.Progress.GetTurnStartedAt(); st != nil && workingNow(r) {
			spans[r.ID] = append(spans[r.ID], [2]time.Time{st.AsTime(), now})
		}
	}
	for _, t := range turns {
		if byID[t.sessionID] == nil {
			continue
		}
		spans[t.sessionID] = append(spans[t.sessionID], [2]time.Time{time.Unix(t.start, 0), time.Unix(t.end, 0)})
		if e := time.Unix(t.end, 0); e.After(from) && !e.After(to) {
			if ends[t.sessionID] == nil {
				ends[t.sessionID] = &ended{}
			}
			ends[t.sessionID].turns++
			ends[t.sessionID].tokens += t.tokens
		}
	}

	type hourKey struct {
		hour      uint32
		by, agent string
	}
	type clientKey struct{ by, agent string }
	hours := map[hourKey]map[string]bool{}
	clients := map[clientKey]*sessionv1.ActivityClient{}
	for id, list := range spans {
		r := byID[id]
		worked := false
		for _, sp := range list {
			// Outside the day: it ended before it began, or began once it
			// was over (a turn ending at its first instant was the day before).
			if !sp[0].Before(to) || sp[1].Before(from) || sp[1].Equal(from) && sp[0].Before(from) {
				continue
			}
			a, b := sp[0], sp[1]
			if a.Before(from) {
				a = from
			}
			if b.After(to) {
				b = to
			}
			worked = true
			// Each hour of the day the span touches, in loc.
			for t := a; ; {
				lt := t.In(loc)
				k := hourKey{uint32(lt.Hour()), r.CreatedBy, r.Agent}
				if hours[k] == nil {
					hours[k] = map[string]bool{}
				}
				hours[k][id] = true
				next := time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour()+1, 0, 0, 0, loc)
				if !next.Before(b) {
					break
				}
				t = next
			}
		}
		if !worked {
			continue
		}
		k := clientKey{r.CreatedBy, r.Agent}
		c := clients[k]
		if c == nil {
			c = &sessionv1.ActivityClient{CreatedBy: r.CreatedBy, Agent: r.Agent}
			clients[k] = c
		}
		c.Sessions++
		if doneNow(r) {
			c.Done++
		}
		if e := ends[id]; e != nil {
			c.Turns += e.turns
			c.Tokens += e.tokens
		}
	}
	for k, ids := range hours {
		out.Hours = append(out.Hours, &sessionv1.ActivityHour{Hour: k.hour, CreatedBy: k.by, Agent: k.agent, Sessions: uint32(len(ids))})
	}
	for _, c := range clients {
		out.Clients = append(out.Clients, c)
	}
	SortActivity(out)
	return out, nil
}

// SortActivity orders hours by hour, then client and agent, and clients by
// client, then agent.
func SortActivity(a interface {
	GetHours() []*sessionv1.ActivityHour
	GetClients() []*sessionv1.ActivityClient
}) {
	slices.SortFunc(a.GetHours(), func(x, y *sessionv1.ActivityHour) int {
		return cmp.Or(cmp.Compare(x.Hour, y.Hour), cmp.Compare(x.CreatedBy, y.CreatedBy), cmp.Compare(x.Agent, y.Agent))
	})
	slices.SortFunc(a.GetClients(), func(x, y *sessionv1.ActivityClient) int {
		return cmp.Or(cmp.Compare(x.CreatedBy, y.CreatedBy), cmp.Compare(x.Agent, y.Agent))
	})
}
