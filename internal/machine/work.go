package machine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/workstats"
)

// Work counts (docs/work-stats.md §5): the work of this machine's sessions,
// counted once as it happens, in quarter hours. Presence says which session
// was at work (a turn in progress) in a quarter; measures add up what turns
// did there; keys name who did it. Owner devices copy them (PullWork) and
// count ranges from their copy.

const (
	kWorkStore = "work_store" // 16 random bytes, made with the counts
	kWorkSeq   = "work_seq"   // the last write transaction that changed them
	// workKeepDays of counts are kept; an owner device takes at most a year.
	workKeepDays = 730
	// workPageHours: at most this many changed hours in one PullWork answer
	// (a page may go over it to keep the hours one transaction changed together).
	workPageHours = 744
)

// workWho is who did work: a client, its coding agent and the project.
type workWho struct{ client, agent, project string }

// workTx records counts in a write transaction: each change takes the
// transaction's seq, and the hours it touched are marked with it.
type workTx struct {
	st      *Store
	tx      *sql.Tx
	seq     int64
	hours   map[int64]bool
	newKeys map[workWho]int64
	used    bool
}

func (s *Store) workTx(tx *sql.Tx) *workTx {
	return &workTx{st: s, tx: tx, seq: s.workSeq + 1, hours: map[int64]bool{}, newKeys: map[workWho]int64{}}
}

// key is who's key, made the first time.
func (w *workTx) key(who workWho) (int64, error) {
	if k, ok := w.st.workKeys[who]; ok {
		return k, nil
	}
	if k, ok := w.newKeys[who]; ok {
		return k, nil
	}
	var k int64
	err := w.tx.QueryRow(`SELECT k FROM work_keys WHERE client=? AND agent=? AND project_id=?`, who.client, who.agent, who.project).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := w.tx.Exec(`INSERT INTO work_keys(client, agent, project_id, seq) VALUES(?,?,?,?)`, who.client, who.agent, who.project, w.seq)
		if err != nil {
			return 0, err
		}
		k, _ = res.LastInsertId()
		w.used = true
	} else if err != nil {
		return 0, err
	}
	w.newKeys[who] = k
	return k, nil
}

type workSession struct {
	n, k, turns, turnStart, turnK int64
}

// session is a session's counts row, made the first time with its
// creator's key.
func (w *workTx) session(id string, who *workWho) (*workSession, error) {
	r := &workSession{}
	err := w.tx.QueryRow(`SELECT n, k, turns, turn_start, turn_k FROM work_sessions WHERE session_id=?`, id).Scan(&r.n, &r.k, &r.turns, &r.turnStart, &r.turnK)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) || who == nil {
		return nil, err
	}
	if r.k, err = w.key(*who); err != nil {
		return nil, err
	}
	res, err := w.tx.Exec(`INSERT INTO work_sessions(session_id, k, status, seq) VALUES(?,?,?,?)`,
		id, r.k, int(sessionv1.SessionStatus_SESSION_STATUS_RUNNING), w.seq)
	if err != nil {
		return nil, err
	}
	r.n, _ = res.LastInsertId()
	w.used = true
	return r, nil
}

func (w *workTx) presence(q, k, n int64) error {
	res, err := w.tx.Exec(`INSERT OR IGNORE INTO work_quarters(q, k, n) VALUES(?,?,?)`, q, k, n)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c > 0 {
		w.hours[workstats.Hour(q)], w.used = true, true
	}
	return nil
}

func (w *workTx) measure(q, k int64, turns, failed int, in, out uint64, busy uint32) error {
	_, err := w.tx.Exec(`INSERT INTO work_measures(q, k, turns, failed_turns, input_tokens, output_tokens, busy_seconds) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(q, k) DO UPDATE SET turns=turns+excluded.turns, failed_turns=failed_turns+excluded.failed_turns,
		input_tokens=input_tokens+excluded.input_tokens, output_tokens=output_tokens+excluded.output_tokens,
		busy_seconds=busy_seconds+excluded.busy_seconds`, q, k, turns, failed, in, out, busy)
	if err == nil {
		w.hours[workstats.Hour(q)], w.used = true, true
	}
	return err
}

// event counts what a stored event says: a turn that started (with who
// asked), one that completed, or a crash that ended a turn in progress.
func (w *workTx) event(id string, who *workWho, ev *sessionv1.SessionEvent) error {
	at := ev.GetTs().AsTime()
	switch {
	case ev.GetTurnStarted() != nil:
		r, err := w.session(id, who)
		if err != nil || r == nil {
			return err
		}
		asker := ev.GetTurnStarted().GetAsker()
		if asker == "" {
			asker = who.client
		}
		tk, err := w.key(workWho{asker, who.agent, who.project})
		if err != nil {
			return err
		}
		if _, err := w.tx.Exec(`UPDATE work_sessions SET turn_start=?, turn_k=?, seq=? WHERE n=?`, at.Unix(), tk, w.seq, r.n); err != nil {
			return err
		}
		w.used = true
		return w.presence(workstats.Quarter(at), r.k, r.n)
	case ev.GetTurnCompleted() != nil:
		tc := ev.GetTurnCompleted()
		r, err := w.session(id, who)
		if err != nil || r == nil || r.turns >= int64(tc.Turn) { // counted already: a replayed event
			return err
		}
		start, tk := at, r.k
		if r.turnStart > 0 {
			start = time.Unix(r.turnStart, 0)
		}
		if r.turnK > 0 {
			tk = r.turnK
		}
		for q, secs := range workstats.Spread(start, at) {
			if err := w.presence(q, r.k, r.n); err != nil {
				return err
			}
			if err := w.measure(q, tk, 0, 0, 0, 0, secs); err != nil {
				return err
			}
		}
		failed := 0
		if tc.IsError && tc.GetFailure().GetKind() != sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
			failed = 1
		}
		if err := w.measure(workstats.Quarter(at), tk, 1, failed, tc.GetUsage().GetInputTokens(), tc.GetUsage().GetOutputTokens(), 0); err != nil {
			return err
		}
		_, err = w.tx.Exec(`UPDATE work_sessions SET turns=?, turn_start=0, turn_k=0, seq=? WHERE n=?`, tc.Turn, w.seq, r.n)
		return err
	case ev.GetStatus().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
		// Its turn ends here without a result: the quarters it was at work
		// in were marked as they began; it adds no measures.
		res, err := w.tx.Exec(`UPDATE work_sessions SET turn_start=0, turn_k=0, seq=? WHERE session_id=? AND turn_start != 0`, w.seq, id)
		if err == nil {
			if c, _ := res.RowsAffected(); c > 0 {
				w.used = true
			}
		}
		return err
	}
	return nil
}

// state keeps a session's status and whether its last turn failed, for
// "done now": idle without a failure, or archived.
func (w *workTx) state(r *sessionRow) error {
	failed := 0
	if f := r.Progress.GetLastFailure(); f != nil && f.Kind != sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
		failed = 1
	}
	res, err := w.tx.Exec(`UPDATE work_sessions SET status=?, failed=?, seq=? WHERE session_id=? AND (status != ? OR failed != ?)`,
		int(r.Status), failed, w.seq, r.ID, int(r.Status), failed)
	if err == nil {
		if c, _ := res.RowsAffected(); c > 0 {
			w.used = true
		}
	}
	return err
}

// tick marks the sessions with a turn in progress at work in quarter q.
func (w *workTx) tick(q int64) error {
	res, err := w.tx.Exec(`INSERT OR IGNORE INTO work_quarters(q, k, n) SELECT ?, k, n FROM work_sessions WHERE turn_start != 0`, q)
	if err == nil {
		if c, _ := res.RowsAffected(); c > 0 {
			w.hours[workstats.Hour(q)], w.used = true, true
		}
	}
	return err
}

// finish marks the hours changed and the transaction's seq; done, after
// the commit, makes them current.
func (w *workTx) finish() error {
	if !w.used {
		return nil
	}
	for h := range w.hours {
		if _, err := w.tx.Exec(`INSERT INTO work_hours(h, seq) VALUES(?,?) ON CONFLICT(h) DO UPDATE SET seq=excluded.seq`, h, w.seq); err != nil {
			return err
		}
	}
	_, err := w.tx.Exec(`INSERT INTO kv(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, kWorkSeq, encodeSeq(w.seq))
	return err
}

func (w *workTx) done() {
	if !w.used {
		return
	}
	for who, k := range w.newKeys {
		w.st.workKeys[who] = k
	}
	w.st.workSeq = w.seq
	w.st.workSeqNow.Store(w.seq)
}

func encodeSeq(n int64) []byte { return []byte(strconv.FormatInt(n, 10)) }

func decodeSeq(b []byte) int64 {
	n, _ := strconv.ParseInt(string(b), 10, 64)
	return n
}

// openWork loads the counts' store and seq, or counts this machine's stored
// sessions afresh when there are none yet (the schema step that made them,
// or a rebuild).
func (s *Store) openWork() error {
	s.workKeys = map[workWho]int64{}
	store, err := s.Get(kWorkStore)
	if err != nil {
		return err
	}
	if store == nil {
		return s.rebuildWork()
	}
	s.workStore = store
	// Kept apart from the rows, so it never goes back when old ones are forgotten.
	b, err := s.Get(kWorkSeq)
	if err != nil {
		return err
	}
	seq := decodeSeq(b)
	s.workSeq = seq
	s.workSeqNow.Store(seq)
	return nil
}

// rebuildWork counts every stored session's turns again from its events,
// into empty tables, under a new store: owner devices then take everything
// again. The work of deleted sessions, whose events are gone, is not kept.
func (s *Store) rebuildWork() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"work_keys", "work_sessions", "work_quarters", "work_measures", "work_hours"} {
		if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	s.workKeys, s.workSeq = map[workWho]int64{}, 0
	w := s.workTx(tx)
	rows, err := tx.Query(`SELECT ` + sessionCols + ` FROM sessions ORDER BY created_at`)
	if err != nil {
		return err
	}
	var sessions []*sessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, r)
	}
	rows.Close()
	for _, r := range sessions {
		who := &workWho{r.CreatedBy, r.Agent, r.ProjectID}
		evs, err := tx.Query(`SELECT payload FROM session_events WHERE session_id=? ORDER BY seq`, r.ID)
		if err != nil {
			return err
		}
		var list []*sessionv1.SessionEvent
		for evs.Next() {
			var b []byte
			if err := evs.Scan(&b); err != nil {
				evs.Close()
				return err
			}
			e := &sessionv1.SessionEvent{}
			if proto.Unmarshal(b, e) == nil && (e.GetTurnStarted() != nil || e.GetTurnCompleted() != nil || e.GetStatus() != nil) {
				list = append(list, e)
			}
		}
		evs.Close()
		for _, e := range list {
			if err := w.event(r.ID, who, e); err != nil {
				return err
			}
		}
		if err := w.state(r); err != nil {
			return err
		}
		// A turn still in progress was ended by the restart that follows.
		if _, err := tx.Exec(`UPDATE work_sessions SET turn_start=0, turn_k=0 WHERE session_id=?`, r.ID); err != nil {
			return err
		}
	}
	if err := w.finish(); err != nil {
		return err
	}
	// Each hour its own seq, in order, so a first pull can page through them.
	if _, err := tx.Exec(`UPDATE work_hours SET seq = (SELECT count(*) FROM work_hours h2 WHERE h2.h <= work_hours.h)`); err != nil {
		return err
	}
	var seq int64
	if err := tx.QueryRow(`SELECT coalesce(max(seq), 1) FROM work_hours`).Scan(&seq); err != nil {
		return err
	}
	store := make([]byte, 16)
	rand.Read(store)
	for k, v := range map[string][]byte{kWorkStore: store, kWorkSeq: encodeSeq(seq)} {
		if _, err := tx.Exec(`INSERT INTO kv(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.done()
	s.workStore, s.workSeq = store, seq
	s.workSeqNow.Store(seq)
	return nil
}

// WorkSeq is where the counts are now (MachineService.Watch's work_seq).
func (s *Store) WorkSeq() int64 { return s.workSeqNow.Load() }

// PullWork is what changed in the counts since after, of the keys may
// allows (MachineService.PullWork).
func (s *Store) PullWork(ctx context.Context, req *machinev1.PullWorkRequest, may func(client, project string) bool) (*machinev1.PullWorkResponse, error) {
	tx, err := s.rdb.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	store, err := txGet(tx, kWorkStore)
	if err != nil {
		return nil, err
	}
	out := &machinev1.PullWorkResponse{Store: store}
	after := int64(req.After)
	fresh := string(req.Store) != string(store)
	if fresh {
		after = 0
	}
	b, err := txGet(tx, kWorkSeq)
	if err != nil {
		return nil, err
	}
	now := decodeSeq(b)
	allowed := map[int64]bool{}
	keys, err := tx.QueryContext(ctx, `SELECT k, client, agent, project_id, seq FROM work_keys`)
	if err != nil {
		return nil, err
	}
	for keys.Next() {
		var k, seq int64
		var key machinev1.WorkKey
		if err := keys.Scan(&k, &key.Client, &key.Agent, &key.ProjectId, &seq); err != nil {
			keys.Close()
			return nil, err
		}
		if !may(key.Client, key.ProjectId) {
			continue
		}
		allowed[k] = true
		if seq > after {
			key.Key = uint32(k)
			out.Keys = append(out.Keys, &key)
		}
	}
	keys.Close()
	sess, err := tx.QueryContext(ctx, `SELECT n, k, status, failed FROM work_sessions WHERE seq > ?`, after)
	if err != nil {
		return nil, err
	}
	for sess.Next() {
		var n, k, status, failed int64
		if err := sess.Scan(&n, &k, &status, &failed); err != nil {
			sess.Close()
			return nil, err
		}
		if allowed[k] {
			out.Sessions = append(out.Sessions, &machinev1.WorkSession{Session: uint32(n), Key: uint32(k), Status: sessionv1.SessionStatus(status), Failed: failed != 0})
		}
	}
	sess.Close()
	from := int64(0)
	if after == 0 {
		from = req.FromHour
	}
	type mark struct{ h, seq int64 }
	var marks []mark
	hours, err := tx.QueryContext(ctx, `SELECT h, seq FROM work_hours WHERE seq > ? AND h >= ? ORDER BY seq, h LIMIT ?`, after, from, workPageHours)
	if err != nil {
		return nil, err
	}
	for hours.Next() {
		var m mark
		if err := hours.Scan(&m.h, &m.seq); err != nil {
			hours.Close()
			return nil, err
		}
		marks = append(marks, m)
	}
	hours.Close()
	out.Seq = uint64(now)
	if len(marks) == workPageHours {
		// The rest of the last transaction's hours come too; then there may be more.
		last := marks[len(marks)-1]
		rest, err := tx.QueryContext(ctx, `SELECT h, seq FROM work_hours WHERE seq = ? AND h > ? ORDER BY h`, last.seq, last.h)
		if err != nil {
			return nil, err
		}
		for rest.Next() {
			var m mark
			if err := rest.Scan(&m.h, &m.seq); err != nil {
				rest.Close()
				return nil, err
			}
			marks = append(marks, m)
		}
		rest.Close()
		var more int
		if err := tx.QueryRow(`SELECT count(*) FROM work_hours WHERE seq > ? AND h >= ?`, last.seq, from).Scan(&more); err != nil {
			return nil, err
		}
		if more > 0 {
			out.Seq, out.More = uint64(last.seq), true
		}
	}
	for _, m := range marks {
		h := &machinev1.WorkHour{Hour: m.h}
		q0, q1 := m.h*4, m.h*4+4
		pr, err := tx.QueryContext(ctx, `SELECT q, k, n FROM work_quarters WHERE q >= ? AND q < ?`, q0, q1)
		if err != nil {
			return nil, err
		}
		for pr.Next() {
			var q, k, n int64
			if err := pr.Scan(&q, &k, &n); err != nil {
				pr.Close()
				return nil, err
			}
			if allowed[k] {
				h.Presence = append(h.Presence, &machinev1.WorkPresence{Quarter: uint32(q - q0), Key: uint32(k), Session: uint32(n)})
			}
		}
		pr.Close()
		ms, err := tx.QueryContext(ctx, `SELECT q, k, turns, failed_turns, input_tokens, output_tokens, busy_seconds FROM work_measures WHERE q >= ? AND q < ?`, q0, q1)
		if err != nil {
			return nil, err
		}
		for ms.Next() {
			var q, k int64
			x := &machinev1.WorkMeasure{}
			if err := ms.Scan(&q, &k, &x.Turns, &x.FailedTurns, &x.InputTokens, &x.OutputTokens, &x.BusySeconds); err != nil {
				ms.Close()
				return nil, err
			}
			if allowed[k] {
				x.Quarter, x.Key = uint32(q-q0), uint32(k)
				h.Measures = append(h.Measures, x)
			}
		}
		ms.Close()
		out.Hours = append(out.Hours, h)
	}
	return out, nil
}

func txGet(tx *sql.Tx, key string) ([]byte, error) {
	var v []byte
	err := tx.QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// WorkKey names a key of this machine's counts.
type WorkKey struct{ Client, Agent, Project string }

// CountWork adds up this machine's counts over buckets and the whole range
// (workstats.Count), with the keys they name.
func (s *Store) CountWork(ctx context.Context, buckets []workstats.Bound, whole workstats.Bound) (sessions, turns []workstats.Row, keys map[int64]WorkKey, err error) {
	if sessions, turns, err = workstats.Count(ctx, s.rdb, false, buckets, whole); err != nil {
		return nil, nil, nil, err
	}
	rows, err := s.rdb.QueryContext(ctx, `SELECT k, client, agent, project_id FROM work_keys`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	keys = map[int64]WorkKey{}
	for rows.Next() {
		var k int64
		var w WorkKey
		if err := rows.Scan(&k, &w.Client, &w.Agent, &w.Project); err != nil {
			return nil, nil, nil, err
		}
		keys[k] = w
	}
	return sessions, turns, keys, rows.Err()
}

// PullWork is what changed in the work counts that p may read
// (MachineService.PullWork): the keys of the sessions it may read.
func (s *Sessions) PullWork(ctx context.Context, p *Principal, req *machinev1.PullWorkRequest) (*machinev1.PullWorkResponse, error) {
	return s.st.PullWork(ctx, req, func(client, project string) bool {
		return s.canRead(p, &sessionRow{CreatedBy: client, ProjectID: project})
	})
}

// pruneWork forgets counts from before workKeepDays.
func (s *Store) pruneWork(now time.Time) error {
	q := workstats.Quarter(now.AddDate(0, 0, -workKeepDays))
	for _, stmt := range []string{
		`DELETE FROM work_quarters WHERE q < ?`,
		`DELETE FROM work_measures WHERE q < ?`,
		`DELETE FROM work_hours WHERE h < ? / 4`,
	} {
		if _, err := s.db.Exec(stmt, q); err != nil {
			return err
		}
	}
	// Sessions with nothing left: not in progress, no quarter of theirs kept.
	_, err := s.db.Exec(`DELETE FROM work_sessions WHERE turn_start = 0 AND NOT EXISTS (SELECT 1 FROM work_quarters p WHERE p.n = work_sessions.n)`)
	return err
}

// CountWork keeps the counts current while hermeshd runs: at each quarter
// hour it marks the sessions with a turn in progress at work in the new
// quarter, and once a day forgets old counts.
func (s *Sessions) CountWork(ctx context.Context) {
	pruned := time.Time{}
	for {
		now := time.Now()
		next := workstats.QuarterStart(workstats.Quarter(now) + 1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		s.w.put(writeOp{workTick: workstats.Quarter(next)})
		if time.Since(pruned) > 24*time.Hour {
			pruned = time.Now()
			if err := s.st.pruneWork(pruned); err != nil {
				s.log.Warn("forgetting old work counts", "err", err)
			}
		}
	}
}
