package owner

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/workstats"
)

// Work counts (docs/work-stats.md §6, §7): this device keeps a copy of
// every other machine's counts in its store, pulled end to end while it
// follows the machine, and answers QueryWork from it. Its own machine's are
// read in process, never copied.

var workMigrations = `
CREATE TABLE work_machines (
  m INTEGER PRIMARY KEY,
  machine_id TEXT NOT NULL UNIQUE,
  store BLOB NOT NULL,
  seq INTEGER NOT NULL,
  synced_at INTEGER NOT NULL,
  history INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE work_keys (
  m INTEGER NOT NULL,
  k INTEGER NOT NULL,
  client TEXT NOT NULL,
  agent TEXT NOT NULL,
  project_id TEXT NOT NULL,
  PRIMARY KEY (m, k)
) WITHOUT ROWID;
CREATE TABLE work_sessions (
  m INTEGER NOT NULL,
  n INTEGER NOT NULL,
  k INTEGER NOT NULL,
  status INTEGER NOT NULL,
  failed INTEGER NOT NULL,
  PRIMARY KEY (m, n)
) WITHOUT ROWID;
CREATE TABLE work_quarters (
  q INTEGER NOT NULL,
  m INTEGER NOT NULL,
  k INTEGER NOT NULL,
  n INTEGER NOT NULL,
  PRIMARY KEY (q, m, k, n)
) WITHOUT ROWID;
CREATE TABLE work_measures (
  q INTEGER NOT NULL,
  m INTEGER NOT NULL,
  k INTEGER NOT NULL,
  turns INTEGER NOT NULL,
  failed_turns INTEGER NOT NULL,
  input_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  busy_seconds INTEGER NOT NULL,
  PRIMARY KEY (q, m, k)
) WITHOUT ROWID;
`

const (
	// workEvery: after catching up, a machine whose counts moved is pulled
	// at most this often.
	workEvery = time.Minute
	// workRetry: a pull that failed is tried again after this.
	workRetry = 30 * time.Second
)

// workKeepDays of counts a device keeps: an iPhone what its screens show
// (18 weeks and a margin), a computer a year and a margin.
func (o *Owner) workKeepDays() int {
	if o.cfg.NoLocalMachine {
		return 130
	}
	return 400
}

type workHeld struct {
	m       int64
	store   []byte
	seq     uint64
	synced  time.Time
	history bool
}

// workHeldOf is what this device holds of a machine's counts; ok false: none.
func (s *store) workHeldOf(machineID string) (h workHeld, ok bool, err error) {
	var synced int64
	err = s.db.QueryRow(`SELECT m, store, seq, synced_at, history FROM work_machines WHERE machine_id=?`, machineID).Scan(&h.m, &h.store, &h.seq, &synced, &h.history)
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	h.synced = time.Unix(synced, 0)
	return h, err == nil, err
}

// applyWork keeps a page of a machine's counts: whole hours replaced, keys
// and sessions upserted, and where it brings this device to. A page of
// another store first drops what was held of the machine. done: the last
// page of a pull.
func (s *store) applyWork(machineID string, page *machinev1.PullWorkResponse, done bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var m int64
	var store []byte
	err = tx.QueryRow(`SELECT m, store FROM work_machines WHERE machine_id=?`, machineID).Scan(&m, &store)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.Exec(`INSERT INTO work_machines(machine_id, store, seq, synced_at) VALUES(?,?,0,0)`, machineID, page.Store)
		if err != nil {
			return err
		}
		m, _ = res.LastInsertId()
	case err != nil:
		return err
	case !bytes.Equal(store, page.Store):
		for _, t := range []string{"work_keys", "work_sessions", "work_quarters", "work_measures"} {
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE m=?`, m); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE work_machines SET store=?, history=0 WHERE m=?`, page.Store, m); err != nil {
			return err
		}
	}
	for _, k := range page.Keys {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO work_keys(m, k, client, agent, project_id) VALUES(?,?,?,?,?)`, m, k.Key, k.Client, k.Agent, k.ProjectId); err != nil {
			return err
		}
	}
	for _, x := range page.Sessions {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO work_sessions(m, n, k, status, failed) VALUES(?,?,?,?,?)`, m, x.Session, x.Key, int(x.Status), x.Failed); err != nil {
			return err
		}
	}
	for _, h := range page.Hours {
		q0 := h.Hour * 4
		for _, t := range []string{"work_quarters", "work_measures"} {
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE q >= ? AND q < ? AND m = ?`, q0, q0+4, m); err != nil {
				return err
			}
		}
		for _, p := range h.Presence {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO work_quarters(q, m, k, n) VALUES(?,?,?,?)`, q0+int64(p.Quarter), m, p.Key, p.Session); err != nil {
				return err
			}
		}
		for _, x := range h.Measures {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO work_measures(q, m, k, turns, failed_turns, input_tokens, output_tokens, busy_seconds) VALUES(?,?,?,?,?,?,?,?)`,
				q0+int64(x.Quarter), m, x.Key, x.Turns, x.FailedTurns, x.InputTokens, x.OutputTokens, x.BusySeconds); err != nil {
				return err
			}
		}
	}
	history := ""
	if done {
		history = ", history=1"
	}
	if _, err := tx.Exec(`UPDATE work_machines SET seq=?, synced_at=?`+history+` WHERE m=?`, page.Seq, time.Now().Unix(), m); err != nil {
		return err
	}
	return tx.Commit()
}

// pruneWork forgets counts from before keepDays.
func (s *store) pruneWork(now time.Time, keepDays int) error {
	q := workstats.Quarter(now.AddDate(0, 0, -keepDays))
	for _, stmt := range []string{`DELETE FROM work_quarters WHERE q < ?`, `DELETE FROM work_measures WHERE q < ?`} {
		if _, err := s.db.Exec(stmt, q); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`DELETE FROM work_sessions WHERE NOT EXISTS (SELECT 1 FROM work_quarters p WHERE p.m = work_sessions.m AND p.n = work_sessions.n)`)
	return err
}

// workSource is a machine whose copy this device holds.
type workSource struct {
	m         int64
	machineID string
	synced    time.Time
	history   bool
}

// countWork adds up the copy over a range, with the keys and machines its
// rows name.
func (s *store) countWork(ctx context.Context, buckets []workstats.Bound, whole workstats.Bound) (sessions, turns []workstats.Row, keys map[[2]int64]machine.WorkKey, sources []workSource, err error) {
	if sessions, turns, err = workstats.Count(ctx, s.db, true, buckets, whole); err != nil {
		return nil, nil, nil, nil, err
	}
	keys = map[[2]int64]machine.WorkKey{}
	rows, err := s.db.QueryContext(ctx, `SELECT m, k, client, agent, project_id FROM work_keys`)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for rows.Next() {
		var m, k int64
		var w machine.WorkKey
		if err := rows.Scan(&m, &k, &w.Client, &w.Agent, &w.Project); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		keys[[2]int64{m, k}] = w
	}
	rows.Close()
	ms, err := s.db.QueryContext(ctx, `SELECT m, machine_id, synced_at, history FROM work_machines`)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer ms.Close()
	for ms.Next() {
		var x workSource
		var synced int64
		if err := ms.Scan(&x.m, &x.machineID, &synced, &x.history); err != nil {
			return nil, nil, nil, nil, err
		}
		x.synced = time.Unix(synced, 0)
		sources = append(sources, x)
	}
	return sessions, turns, keys, sources, ms.Err()
}

// The pull loop, per followed machine (replica.go's feeds).

// workSeen notes where a machine's counts are, from its Watch, and pulls it
// if this device is behind: at once the first time this feed sees it, then
// at most every workEvery (r.mu held).
func (r *replica) workSeenLocked(s *sources, f *feed, machineID string, seq uint64) {
	if seq != 0 {
		f.workSeen = seq
	}
	r.pullWorkLocked(s, f, machineID)
}

func (r *replica) pullWorkLocked(s *sources, f *feed, machineID string) {
	if f.local || f.conn == nil || f.workPulling || f.workNone || f.workSeen == 0 || f.workSeen == f.workHeld && f.workCaught {
		return
	}
	wait := time.Duration(0)
	if f.workCaught {
		wait = max(0, workEvery-time.Since(f.workLast))
	}
	f.workPulling = true
	go r.pullWork(s, f, machineID, f.conn.Info, wait)
}

// pullWork takes a machine's changed counts, page by page, into the store.
func (r *replica) pullWork(s *sources, f *feed, machineID string, n interface {
	PullWork(context.Context, *machinev1.PullWorkRequest) (*machinev1.PullWorkResponse, error)
}, wait time.Duration) {
	select {
	case <-s.ctx.Done():
		return
	case <-time.After(wait):
	}
	err := r.pullWorkPages(s.ctx, machineID, n)
	r.mu.Lock()
	defer r.mu.Unlock()
	f.workPulling, f.workLast = false, time.Now()
	switch {
	case connect.CodeOf(err) == connect.CodeUnimplemented:
		f.workNone = true // a machine from before counts were pulled
	case err != nil:
		if s.ctx.Err() == nil {
			r.o.log.Debug("pulling work counts", "machine", machineID, "err", err)
			time.AfterFunc(workRetry, func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if r.src == s {
					r.pullWorkLocked(s, f, machineID)
				}
			})
		}
	default:
		if h, ok, _ := r.o.st.workHeldOf(machineID); ok {
			f.workHeld = h.seq
		}
		f.workCaught = true
		r.workVersion++
		r.kickLocked()
		if r.src == s {
			r.pullWorkLocked(s, f, machineID) // moved on meanwhile
		}
	}
}

func (r *replica) pullWorkPages(ctx context.Context, machineID string, n interface {
	PullWork(context.Context, *machinev1.PullWorkRequest) (*machinev1.PullWorkResponse, error)
}) error {
	held, ok, err := r.o.st.workHeldOf(machineID)
	if err != nil {
		return err
	}
	req := &machinev1.PullWorkRequest{}
	if ok {
		req.Store, req.After = held.store, held.seq
	}
	if !ok || !held.history {
		req.FromHour = workstats.Hour(workstats.Quarter(time.Now().AddDate(0, 0, -r.o.workKeepDays())))
	}
	for {
		page, err := n.PullWork(ctx, req)
		if err != nil {
			return err
		}
		if err := r.o.st.applyWork(machineID, page, !page.More); err != nil {
			return err
		}
		if !page.More {
			r.pruneWork()
			return nil
		}
		req = &machinev1.PullWorkRequest{Store: page.Store, After: page.Seq}
	}
}

// pruneWork forgets old counts, once a day at most.
func (r *replica) pruneWork() {
	r.mu.Lock()
	due := time.Since(r.workPruned) > 24*time.Hour
	if due {
		r.workPruned = time.Now()
	}
	r.mu.Unlock()
	if due {
		if err := r.o.st.pruneWork(time.Now(), r.o.workKeepDays()); err != nil {
			r.o.log.Warn("forgetting old work counts", "err", err)
		}
	}
}

// workVersion moves when the counts this device answers from changed: a
// page kept, or its own machine's counts (LocalService.Watch's work_version).
func (o *Owner) workVersion() uint64 {
	r := o.rep
	l := o.local()
	r.mu.Lock()
	defer r.mu.Unlock()
	if l != nil {
		if seq := l.WorkSeq(); seq != r.localWork {
			r.localWork = seq
			r.workVersion++
		}
	}
	return r.workVersion
}

// screenWorkVersion is workVersion for a screen: on a computer that is only
// a machine, its own machine's seq.
func (o *Owner) screenWorkVersion(mode string) uint64 {
	if mode == "machine" {
		if l, _, err := o.onlyMachine(); err == nil && l != nil {
			return uint64(l.WorkSeq())
		}
		return 0
	}
	return o.workVersion()
}

// QueryWork counts the work over a range (LocalService.QueryWork): the
// other machines from this device's copy, its own in process.
func (o *Owner) QueryWork(ctx context.Context, req *localv1.QueryWorkRequest) (*localv1.QueryWorkResponse, error) {
	buckets, whole, err := workBuckets(req)
	if err != nil {
		return nil, err
	}
	var parts []workPart
	sessions, turns, keys, sources, err := o.st.countWork(ctx, buckets, whole)
	if err != nil {
		return nil, err
	}
	l := o.local()
	localID := ""
	if l != nil {
		localID = l.MachineID()
	}
	machines := map[int64]string{}
	for _, x := range sources {
		machines[x.m] = x.machineID
	}
	key := func(r workstats.Row) (machine.WorkKey, string, bool) {
		id := machines[r.M]
		return keys[[2]int64{r.M, r.K}], id, id != localID
	}
	parts = append(parts, workPart{sessions, turns, key})
	if l != nil {
		ls, lt, lk, err := l.CountWork(ctx, buckets, whole)
		if err != nil {
			return nil, err
		}
		parts = append(parts, workPart{ls, lt, func(r workstats.Row) (machine.WorkKey, string, bool) { return lk[r.K], localID, true }})
	}
	out := groupWork(req.GroupBy, parts)
	r := o.rep
	r.mu.Lock()
	for _, x := range sources {
		if x.machineID == localID {
			continue
		}
		src := &localv1.WorkSource{MachineId: x.machineID, SyncedAt: timestamppb.New(x.synced), Syncing: !x.history}
		if r.src != nil {
			if f := r.src.feeds[x.machineID]; f != nil {
				src.Live = f.ready && f.workCaught && !f.workPulling && f.workSeen == f.workHeld
			}
		}
		out.Sources = append(out.Sources, src)
	}
	r.mu.Unlock()
	if l != nil {
		out.Sources = append(out.Sources, &localv1.WorkSource{MachineId: localID, Live: true})
	}
	return out, nil
}

// QueryWorkOnly is QueryWork for a computer that is only a machine: its own
// counts.
func QueryWorkOnly(ctx context.Context, l *machine.Local, req *localv1.QueryWorkRequest) (*localv1.QueryWorkResponse, error) {
	buckets, whole, err := workBuckets(req)
	if err != nil {
		return nil, err
	}
	ls, lt, lk, err := l.CountWork(ctx, buckets, whole)
	if err != nil {
		return nil, err
	}
	id := l.MachineID()
	out := groupWork(req.GroupBy, []workPart{{ls, lt, func(r workstats.Row) (machine.WorkKey, string, bool) { return lk[r.K], id, true }}})
	out.Sources = []*localv1.WorkSource{{MachineId: id, Live: true}}
	return out, nil
}

func workBuckets(req *localv1.QueryWorkRequest) ([]workstats.Bound, workstats.Bound, error) {
	if req.Bucket != localv1.WorkBucket_WORK_BUCKET_HOUR && req.Bucket != localv1.WorkBucket_WORK_BUCKET_DAY {
		return nil, workstats.Bound{}, connect.NewError(connect.CodeInvalidArgument, errors.New("bucket must be HOUR or DAY"))
	}
	buckets, whole, err := workstats.Buckets(req.TimeZone, req.FirstDay, int(req.Days), req.Bucket == localv1.WorkBucket_WORK_BUCKET_HOUR)
	if err != nil {
		return nil, workstats.Bound{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return buckets, whole, nil
}

// workPart is counted rows from one store, and what each names: its key,
// its machine, and whether to keep it.
type workPart struct {
	sessions, turns []workstats.Row
	name            func(workstats.Row) (machine.WorkKey, string, bool)
}

// groupWork adds rows up by the dimensions asked (exact: a session has one
// client, agent, machine and project).
func groupWork(dims []localv1.WorkDimension, parts []workPart) *localv1.QueryWorkResponse {
	type sk struct {
		bucket                          int
		client, agent, machine, project string
	}
	keyOf := func(bucket int, w machine.WorkKey, machineID string) sk {
		k := sk{bucket: bucket}
		for _, d := range dims {
			switch d {
			case localv1.WorkDimension_WORK_DIMENSION_CLIENT:
				k.client = w.Client
			case localv1.WorkDimension_WORK_DIMENSION_AGENT:
				k.agent = w.Agent
			case localv1.WorkDimension_WORK_DIMENSION_MACHINE:
				k.machine = machineID
			case localv1.WorkDimension_WORK_DIMENSION_PROJECT:
				k.project = w.Project
			}
		}
		return k
	}
	groupOf := func(k sk) *localv1.WorkGroup {
		return &localv1.WorkGroup{Client: k.client, Agent: k.agent, MachineId: k.machine, ProjectId: k.project}
	}
	sessions, turns := map[sk]*localv1.SessionCount{}, map[sk]*localv1.TurnCount{}
	for _, p := range parts {
		for _, r := range p.sessions {
			w, id, ok := p.name(r)
			if !ok {
				continue
			}
			k := keyOf(r.Bucket, w, id)
			x := sessions[k]
			if x == nil {
				x = &localv1.SessionCount{Bucket: uint32(max(r.Bucket, 0)), Group: groupOf(k)}
				sessions[k] = x
			}
			x.Sessions += r.Sessions
			x.Done += r.Done
		}
		for _, r := range p.turns {
			w, id, ok := p.name(r)
			if !ok {
				continue
			}
			k := keyOf(r.Bucket, w, id)
			x := turns[k]
			if x == nil {
				x = &localv1.TurnCount{Bucket: uint32(max(r.Bucket, 0)), Group: groupOf(k)}
				turns[k] = x
			}
			x.Turns += r.Turns
			x.FailedTurns += r.FailedTurns
			x.InputTokens += r.InputTokens
			x.OutputTokens += r.OutputTokens
			x.BusySeconds += r.BusySeconds
		}
	}
	out := &localv1.QueryWorkResponse{}
	for k, x := range sessions {
		if k.bucket < 0 {
			out.RangeSessions = append(out.RangeSessions, x)
		} else {
			out.Sessions = append(out.Sessions, x)
		}
	}
	for k, x := range turns {
		if k.bucket < 0 {
			out.RangeTurns = append(out.RangeTurns, x)
		} else {
			out.Turns = append(out.Turns, x)
		}
	}
	order := func(ab, bb uint32, a, b *localv1.WorkGroup) int {
		return cmp.Or(cmp.Compare(ab, bb), cmp.Compare(a.Client, b.Client), cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.MachineId, b.MachineId), cmp.Compare(a.ProjectId, b.ProjectId))
	}
	for _, l := range [][]*localv1.SessionCount{out.Sessions, out.RangeSessions} {
		slices.SortFunc(l, func(a, b *localv1.SessionCount) int { return order(a.Bucket, b.Bucket, a.Group, b.Group) })
	}
	for _, l := range [][]*localv1.TurnCount{out.Turns, out.RangeTurns} {
		slices.SortFunc(l, func(a, b *localv1.TurnCount) int { return order(a.Bucket, b.Bucket, a.Group, b.Group) })
	}
	return out
}
