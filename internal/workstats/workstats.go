// Package workstats is what machines and owner devices share of counting
// work (docs/work-stats.md): quarter hours, the hours and days of a range in
// a time zone, and the queries that add up a range.
package workstats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// QuarterSeconds is the length of the buckets work is counted in: every time
// zone in use is a whole number of quarter hours from UTC, so any local hour
// or day is a run of them.
const QuarterSeconds = 900

// Quarter is the quarter hour t is in.
func Quarter(t time.Time) int64 { return t.Unix() / QuarterSeconds }

// QuarterStart is when quarter q begins.
func QuarterStart(q int64) time.Time { return time.Unix(q*QuarterSeconds, 0) }

// Hour is the hour quarter q is in (unix seconds / 3600).
func Hour(q int64) int64 { return q / 4 }

// MaxDays a range may cover, and MaxHourDays when it is counted by hour.
const (
	MaxDays     = 400
	MaxHourDays = 31
)

// Bound is a run of quarters, From included, To not.
type Bound struct{ From, To int64 }

// Buckets is each hour (hourly) or day of the days days from firstDay
// (YYYY-MM-DD) in the time zone tz, as runs of quarters, and the whole range.
// A day is from its midnight to the next, so a day that changes clocks has 23
// or 25 hours.
func Buckets(tz, firstDay string, days int, hourly bool) ([]Bound, Bound, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		return nil, Bound{}, fmt.Errorf("unknown time zone %q", tz)
	}
	if days < 1 || days > MaxDays || hourly && days > MaxHourDays {
		return nil, Bound{}, fmt.Errorf("days must be 1 to %d (%d by hour)", MaxDays, MaxHourDays)
	}
	first, err := time.ParseInLocation(time.DateOnly, firstDay, loc)
	if err != nil {
		return nil, Bound{}, errors.New("first_day must be YYYY-MM-DD")
	}
	end := time.Date(first.Year(), first.Month(), first.Day()+days, 0, 0, 0, 0, loc)
	whole := Bound{Quarter(first), Quarter(end)}
	var out []Bound
	if hourly {
		for q := whole.From; q < whole.To; q += 4 {
			out = append(out, Bound{q, min(q+4, whole.To)})
		}
		return out, whole, nil
	}
	for i := range days {
		a := time.Date(first.Year(), first.Month(), first.Day()+i, 0, 0, 0, 0, loc)
		b := time.Date(first.Year(), first.Month(), first.Day()+i+1, 0, 0, 0, 0, loc)
		out = append(out, Bound{Quarter(a), Quarter(b)})
	}
	return out, whole, nil
}

// Row is what the counting queries return for one bucket (-1: the whole
// range), machine (0 on a machine itself) and key.
type Row struct {
	Bucket                    int
	M, K                      int64
	Sessions, Done            uint32
	Turns, FailedTurns        uint32
	InputTokens, OutputTokens uint64
	BusySeconds               uint64
}

// Querier is a *sql.DB, *sql.Conn or *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Count adds up the work tables in db over each bucket and the whole range:
// sessions at work (each once a bucket) and of them those done now, by key;
// and turns, failed turns, tokens and busy seconds, by key. withMachine:
// db is an owner device's, its tables keyed by machine too.
func Count(ctx context.Context, db Querier, withMachine bool, buckets []Bound, whole Bound) (sessions, turns []Row, err error) {
	var b strings.Builder
	b.WriteString("WITH b(i, q0, q1) AS (VALUES ")
	for i, x := range append([]Bound{whole}, buckets...) {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%d,%d,%d)", i-1, x.From, x.To)
	}
	b.WriteString(") ")
	bounds := b.String()
	m, join, group := "0", "s.n = p.n", ""
	if withMachine {
		m, join, group = "p.m", "s.m = p.m AND s.n = p.n", ", p.m"
	}
	// CROSS JOIN keeps the bounds outside: each is one range of the
	// quarters' primary key.
	done := "(s.status = " + strconv.Itoa(int(sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED)) +
		" OR (s.status = " + strconv.Itoa(int(sessionv1.SessionStatus_SESSION_STATUS_IDLE)) + " AND s.failed = 0))"
	q := bounds + `SELECT b.i, ` + m + `, p.k, count(DISTINCT p.n), count(DISTINCT CASE WHEN ` + done + ` THEN p.n END)
		FROM b CROSS JOIN work_quarters p ON p.q >= b.q0 AND p.q < b.q1
		JOIN work_sessions s ON ` + join + `
		GROUP BY b.i` + group + `, p.k`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Bucket, &r.M, &r.K, &r.Sessions, &r.Done); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sessions = append(sessions, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	wm, wgroup := strings.ReplaceAll(m, "p.", "w."), strings.ReplaceAll(group, "p.", "w.")
	q = bounds + `SELECT b.i, ` + wm + `, w.k, sum(w.turns), sum(w.failed_turns), sum(w.input_tokens), sum(w.output_tokens), sum(w.busy_seconds)
		FROM b CROSS JOIN work_measures w ON w.q >= b.q0 AND w.q < b.q1
		GROUP BY b.i` + wgroup + `, w.k`
	if rows, err = db.QueryContext(ctx, q); err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Bucket, &r.M, &r.K, &r.Turns, &r.FailedTurns, &r.InputTokens, &r.OutputTokens, &r.BusySeconds); err != nil {
			return nil, nil, err
		}
		turns = append(turns, r)
	}
	return sessions, turns, rows.Err()
}

// Spread is how the seconds of a turn from start to end fall into quarters:
// for each quarter it was in progress in, the seconds inside it. A turn that
// took no time is in the quarter it ended in, with 0; one that ended at a
// quarter's first instant is not in that quarter.
func Spread(start, end time.Time) map[int64]uint32 {
	if start.After(end) {
		start = end
	}
	last := Quarter(end)
	if start.Before(end) && end.Unix()%QuarterSeconds == 0 {
		last--
	}
	out := map[int64]uint32{}
	for q := Quarter(start); q <= last; q++ {
		a, b := max(start.Unix(), q*QuarterSeconds), min(end.Unix(), (q+1)*QuarterSeconds)
		out[q] += uint32(max(b-a, 0))
	}
	return out
}
