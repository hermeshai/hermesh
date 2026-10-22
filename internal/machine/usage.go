package machine

import (
	"cmp"
	"database/sql"
	"slices"
	"time"
	_ "time/tzdata" // usage is counted in the caller's time zone, which a minimal Linux may not ship

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Usage: each turn's tokens, kept in turn_usage when its TurnCompleted
// event is written, so GetUsage can add them up by day without reading
// every stored event.

const usageBackfilled = "turn_usage_backfilled"

// putUsage records a TurnCompleted event's tokens (in the event's own
// transaction); other events are ignored.
func putUsage(tx *sql.Tx, sessionID string, ev *sessionv1.SessionEvent) error {
	tc := ev.GetTurnCompleted()
	if tc == nil {
		return nil
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO turn_usage(session_id, turn, ended_at, input_tokens, output_tokens) VALUES(?,?,?,?,?)`,
		sessionID, tc.Turn, ev.Ts.AsTime().Unix(), tc.Usage.GetInputTokens(), tc.Usage.GetOutputTokens())
	return err
}

// backfillUsage fills turn_usage from the turns stored before it existed,
// once.
func (s *Store) backfillUsage() error {
	if v, err := s.Get(usageBackfilled); err != nil || v != nil {
		return err
	}
	rows, err := s.rdb.Query(`SELECT session_id, payload FROM session_events`)
	if err != nil {
		return err
	}
	type turn struct {
		id string
		ev *sessionv1.SessionEvent
	}
	var turns []turn
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			rows.Close()
			return err
		}
		ev := &sessionv1.SessionEvent{}
		if proto.Unmarshal(b, ev) == nil && ev.GetTurnCompleted() != nil {
			turns = append(turns, turn{id, ev})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range turns {
		if err := putUsage(tx, t.id, t.ev); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, usageBackfilled, []byte("1")); err != nil {
		return err
	}
	return tx.Commit()
}

type usageRow struct {
	sessionID     string
	endedAt       int64
	input, output uint64
}

// TurnUsage returns the turns that ended at or after since (unix seconds).
func (s *Store) TurnUsage(since int64) ([]usageRow, error) {
	rows, err := s.rdb.Query(`SELECT session_id, ended_at, input_tokens, output_tokens FROM turn_usage WHERE ended_at >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []usageRow
	for rows.Next() {
		var r usageRow
		if err := rows.Scan(&r.sessionID, &r.endedAt, &r.input, &r.output); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Usage adds up the tokens of the turns in the sessions p can read that
// ended on or after req.Since, by day (in req.TimeZone) and coding agent.
func (s *Sessions) Usage(p *Principal, req *sessionv1.GetUsageRequest) (*sessionv1.GetUsageResponse, error) {
	loc := time.UTC
	if req.TimeZone != "" {
		l, err := time.LoadLocation(req.TimeZone)
		if err != nil {
			return nil, cerr(connect.CodeInvalidArgument, "unknown time zone %q", req.TimeZone)
		}
		loc = l
	}
	since, err := time.ParseInLocation(time.DateOnly, req.Since, loc)
	if err != nil {
		return nil, cerr(connect.CodeInvalidArgument, "since must be YYYY-MM-DD")
	}
	turns, err := s.st.TurnUsage(since.Unix())
	if err != nil {
		return nil, err
	}
	sessions, err := s.st.Sessions("")
	if err != nil {
		return nil, err
	}
	byID := map[string]*sessionRow{}
	for _, r := range sessions {
		if s.canRead(p, r) {
			byID[r.ID] = r
		}
	}
	type key struct{ date, agent string }
	days := map[key]*sessionv1.UsageDay{}
	seen := map[key]map[string]bool{}
	for _, t := range turns {
		r := byID[t.sessionID]
		if r == nil {
			continue
		}
		k := key{time.Unix(t.endedAt, 0).In(loc).Format(time.DateOnly), r.Agent}
		d := days[k]
		if d == nil {
			d = &sessionv1.UsageDay{Date: k.date, Agent: k.agent}
			days[k] = d
			seen[k] = map[string]bool{}
		}
		d.InputTokens += t.input
		d.OutputTokens += t.output
		d.Turns++
		if !seen[k][t.sessionID] {
			seen[k][t.sessionID] = true
			d.Sessions++
		}
	}
	out := &sessionv1.GetUsageResponse{}
	for _, d := range days {
		out.Days = append(out.Days, d)
	}
	SortUsage(out.Days)
	return out, nil
}

// SortUsage orders days by date, then agent.
func SortUsage(days []*sessionv1.UsageDay) {
	slices.SortFunc(days, func(a, b *sessionv1.UsageDay) int {
		return cmp.Or(cmp.Compare(a.Date, b.Date), cmp.Compare(a.Agent, b.Agent))
	})
}
