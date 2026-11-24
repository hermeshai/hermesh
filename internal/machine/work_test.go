package machine

import (
	"bytes"
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/workstats"
)

type totals struct {
	sessions, done, turns, failed uint32
	tokens, busy                  uint64
	byBucket                      map[int]uint32
	byClient                      map[string]uint32
}

func countWork(t *testing.T, st *Store, tz, first string, days int, hourly bool) totals {
	t.Helper()
	buckets, whole, err := workstats.Buckets(tz, first, days, hourly)
	if err != nil {
		t.Fatal(err)
	}
	ss, ts, keys, err := st.CountWork(context.Background(), workstats.Ask{Buckets: buckets, Whole: whole})
	if err != nil {
		t.Fatal(err)
	}
	out := totals{byBucket: map[int]uint32{}, byClient: map[string]uint32{}}
	for _, r := range ss {
		if r.Bucket == -1 {
			out.sessions += r.Sessions
			out.done += r.Done
		} else {
			out.byBucket[r.Bucket] += r.Sessions
		}
	}
	for _, r := range ts {
		if r.Bucket == -1 {
			out.turns += r.Turns
			out.failed += r.FailedTurns
			out.tokens += r.InputTokens + r.OutputTokens
			out.busy += r.BusySeconds
			out.byClient[keys[r.K].Client] += r.Turns
		}
	}
	return out
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "hermeshd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Counted from unified kernel events as they are stored: a turn over midnight
// in Shanghai is at work in each quarter it touched and counts its busy
// seconds there, its turn and tokens where it ended, under the agent that
// asked for it; a replayed completion counts nothing again; a turn cut off by
// a crash is at work in the quarters marked as they began, and adds no turn.
func TestWorkFromEvents(t *testing.T) {
	st := openTestStore(t)
	sh, _ := time.LoadLocation("Asia/Shanghai")
	at := func(day, hour, min int) time.Time { return time.Date(2026, 3, day, hour, min, 0, 0, sh) }

	sessA := &sessionv1.Session{
		SessionId: "s_a",
		ProjectId: "demo",
		Agent:     "fake",
		CreatedBy: "c_creator",
		Status:    sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
		Progress:  &sessionv1.SessionProgress{TurnCount: 1},
	}
	runA := &sessionv1.Run{
		RunId:     "run_a1",
		SessionId: "s_a",
		Ordinal:   1,
		Status:    sessionv1.RunStatus_RUN_STATUS_RUNNING,
		Author:    &sessionv1.Actor{Id: "c_asker"},
		StartedAt: timestamppb.New(at(3, 23, 30)),
	}
	st.recordKernelWork(sessA, runA, &sessionv1.SessionEvent{
		SessionId:  "s_a",
		Type:       "run.started",
		OccurredAt: timestamppb.New(at(3, 23, 30)),
	})

	sessA.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
	runA.Status = sessionv1.RunStatus_RUN_STATUS_COMPLETED
	runA.CompletedAt = timestamppb.New(at(4, 2, 10))
	runA.Usage = &sessionv1.ContextWindowUsage{InputTokens: 1000, OutputTokens: 200}
	doneEv := &sessionv1.SessionEvent{
		SessionId:  "s_a",
		Type:       "run.completed",
		OccurredAt: timestamppb.New(at(4, 2, 10)),
	}
	st.recordKernelWork(sessA, runA, doneEv)
	st.recordKernelWork(sessA, runA, doneEv) // replayed

	sessB := &sessionv1.Session{
		SessionId: "s_b",
		ProjectId: "demo",
		Agent:     "fake",
		CreatedBy: "c_creator",
		Status:    sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
		Progress:  &sessionv1.SessionProgress{TurnCount: 1},
	}
	runB := &sessionv1.Run{
		RunId:     "run_b1",
		SessionId: "s_b",
		Ordinal:   1,
		Status:    sessionv1.RunStatus_RUN_STATUS_RUNNING,
		Author:    &sessionv1.Actor{Id: "c_creator"},
		StartedAt: timestamppb.New(at(4, 10, 0)),
	}
	st.recordKernelWork(sessB, runB, &sessionv1.SessionEvent{
		SessionId:  "s_b",
		Type:       "run.started",
		OccurredAt: timestamppb.New(at(4, 10, 0)),
	})
	if err := st.recordWorkTick(workstats.Quarter(at(4, 10, 15))); err != nil {
		t.Fatal(err)
	}
	sessB.Status = sessionv1.SessionStatus_SESSION_STATUS_CRASHED
	sessB.Progress.LastFailure = &sessionv1.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED}
	st.recordKernelWork(sessB, runB, &sessionv1.SessionEvent{
		SessionId:  "s_b",
		Type:       "session.recovered",
		OccurredAt: timestamppb.New(at(4, 12, 0)),
	})
	if err := st.recordWorkTick(workstats.Quarter(at(4, 10, 30))); err != nil {
		t.Fatal(err)
	}

	day3 := countWork(t, st, "Asia/Shanghai", "2026-03-03", 1, true)
	if day3.sessions != 1 || day3.byBucket[23] != 1 || day3.turns != 0 || day3.busy != 1800 {
		t.Fatalf("the 3rd: %+v", day3)
	}
	day4 := countWork(t, st, "Asia/Shanghai", "2026-03-04", 1, true)
	for _, h := range []int{0, 1, 2, 10} {
		if day4.byBucket[h] != 1 {
			t.Fatalf("the 4th at %d h: %+v", h, day4)
		}
	}
	if len(day4.byBucket) != 4 || day4.sessions != 2 || day4.done != 1 {
		t.Fatalf("the 4th: %+v", day4)
	}
	if day4.turns != 1 || day4.tokens != 1200 || day4.busy != 7800 || day4.byClient["c_asker"] != 1 {
		t.Fatalf("the 4th's turn (once, under who asked): %+v", day4)
	}
	both := countWork(t, st, "Asia/Shanghai", "2026-03-03", 2, false)
	if both.sessions != 2 || both.byBucket[0] != 1 || both.byBucket[1] != 2 || both.busy != 9600 {
		t.Fatalf("two days: %+v", both)
	}
	if utc := countWork(t, st, "UTC", "2026-03-03", 1, false); utc.sessions != 1 || utc.turns != 1 {
		t.Fatalf("UTC: %+v", utc)
	}

	// The span before a range is counted in the same pass and is what a
	// range of its own counts there; the range's own rows do not change.
	// With turns only the sessions are left out and the turns are the same.
	_, d3, _ := workstats.Buckets("Asia/Shanghai", "2026-03-03", 1, false)
	b4, d4, _ := workstats.Buckets("Asia/Shanghai", "2026-03-04", 1, true)
	count := func(a workstats.Ask) (ss, ts []workstats.Row) {
		t.Helper()
		ss, ts, _, err := st.CountWork(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		return ss, ts
	}
	// of is the rows of one bucket, as the whole range's, by key.
	of := func(rows []workstats.Row, bucket int) (out []workstats.Row) {
		for _, r := range rows {
			if r.Bucket == bucket {
				r.Bucket = workstats.Whole
				out = append(out, r)
			}
		}
		slices.SortFunc(out, func(a, b workstats.Row) int { return cmp.Compare(a.K, b.K) })
		return out
	}
	ss, ts := count(workstats.Ask{Buckets: b4, Whole: d4, Previous: d3})
	s3, t3 := count(workstats.Ask{Whole: d3})
	if len(s3) == 0 || len(t3) == 0 || !slices.Equal(of(ss, workstats.Previous), of(s3, workstats.Whole)) || !slices.Equal(of(ts, workstats.Previous), of(t3, workstats.Whole)) {
		t.Fatalf("the span before: %+v %+v, alone %+v %+v", of(ss, workstats.Previous), of(ts, workstats.Previous), s3, t3)
	}
	s4, t4 := count(workstats.Ask{Buckets: b4, Whole: d4})
	for _, b := range []int{workstats.Whole, 0, 10} {
		if !slices.Equal(of(ss, b), of(s4, b)) || !slices.Equal(of(ts, b), of(t4, b)) {
			t.Fatalf("bucket %d changed with the span before: %+v %+v, without %+v %+v", b, of(ss, b), of(ts, b), of(s4, b), of(t4, b))
		}
	}
	if len(of(s4, workstats.Previous)) != 0 {
		t.Fatalf("a span before that was not asked: %+v", s4)
	}
	so, to := count(workstats.Ask{Buckets: b4, Whole: d4, Previous: d3, TurnsOnly: true})
	if so != nil || !slices.Equal(to, ts) {
		t.Fatalf("turns only: %+v %+v, want %+v", so, to, ts)
	}
}

// What PullWork sends: everything to a caller holding another store; then
// only what changed; the keys a client may read; pages that never split
// the hours one transaction changed.
func TestPullWork(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	all := func(string, string) bool { return true }
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	who := &workWho{"c_a", "fake", "demo"}

	recordMany := func(id string, n int, singleTx bool) {
		t.Helper()
		if singleTx {
			st.workMu.Lock()
			tx, err := st.db.Begin()
			if err != nil {
				st.workMu.Unlock()
				t.Fatal(err)
			}
			_ = st.ensureWorkStoreLocked(tx)
			w := st.workTx(tx)
			for i := range n {
				start := base.Add(time.Duration(i) * time.Hour)
				end := start.Add(5 * time.Minute)
				if err := w.turnStart(id, who, who.client, start); err != nil {
					tx.Rollback()
					st.workMu.Unlock()
					t.Fatal(err)
				}
				if err := w.turnComplete(id, who, uint32(i+1), end, false, 10, 0); err != nil {
					tx.Rollback()
					st.workMu.Unlock()
					t.Fatal(err)
				}
			}
			if err := w.finish(); err != nil {
				tx.Rollback()
				st.workMu.Unlock()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				st.workMu.Unlock()
				t.Fatal(err)
			}
			w.done()
			st.workMu.Unlock()
			return
		}
		for i := range n {
			start := base.Add(time.Duration(i) * time.Hour)
			end := start.Add(5 * time.Minute)
			sess := &sessionv1.Session{SessionId: id, ProjectId: "demo", Agent: "fake", CreatedBy: "c_a", Status: sessionv1.SessionStatus_SESSION_STATUS_RUNNING}
			run := &sessionv1.Run{RunId: "r", SessionId: id, Ordinal: uint32(i + 1), Status: sessionv1.RunStatus_RUN_STATUS_RUNNING, Author: &sessionv1.Actor{Id: "c_a"}, StartedAt: timestamppb.New(start)}
			st.recordKernelWork(sess, run, &sessionv1.SessionEvent{SessionId: id, Type: "run.started", OccurredAt: timestamppb.New(start)})
			sess.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
			run.Status = sessionv1.RunStatus_RUN_STATUS_COMPLETED
			run.CompletedAt = timestamppb.New(end)
			run.Usage = &sessionv1.ContextWindowUsage{InputTokens: 10}
			st.recordKernelWork(sess, run, &sessionv1.SessionEvent{SessionId: id, Type: "run.completed", OccurredAt: timestamppb.New(end)})
		}
	}

	// One transaction touching 800 hours: never split across pages.
	recordMany("s_one", 800, true)
	first, err := st.PullWork(ctx, &machinev1.PullWorkRequest{Store: []byte("another")}, all)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Store, st.workStore) || first.More || len(first.Hours) != 800 || len(first.Keys) != 1 || len(first.Sessions) != 1 || int64(first.Seq) != st.WorkSeq() {
		t.Fatalf("first pull: store %x, more %v, %d hours, %d keys, %d sessions, seq %d (now %d)", first.Store, first.More, len(first.Hours), len(first.Keys), len(first.Sessions), first.Seq, st.WorkSeq())
	}
	if h := first.Hours[0]; len(h.Presence) != 1 || len(h.Measures) != 1 || h.Measures[0].Turns != 1 || h.Measures[0].InputTokens != 10 || h.Measures[0].BusySeconds != 300 {
		t.Fatalf("an hour: %v", h)
	}
	again, err := st.PullWork(ctx, &machinev1.PullWorkRequest{Store: first.Store, After: first.Seq}, all)
	if err != nil || len(again.Hours)+len(again.Keys)+len(again.Sessions) != 0 || again.Seq != first.Seq {
		t.Fatalf("nothing new: %v %v", again, err)
	}

	// 800 more hours, each its own transaction: pages of workPageHours.
	recordMany("s_two", 800, false)
	page, err := st.PullWork(ctx, &machinev1.PullWorkRequest{Store: first.Store, After: first.Seq}, all)
	if err != nil || !page.More || len(page.Hours) != workPageHours {
		t.Fatalf("a page: more %v, %d hours, %v", page.More, len(page.Hours), err)
	}
	rest, err := st.PullWork(ctx, &machinev1.PullWorkRequest{Store: first.Store, After: page.Seq}, all)
	if err != nil || rest.More || len(rest.Hours) != 800-workPageHours || int64(rest.Seq) != st.WorkSeq() {
		t.Fatalf("the rest: more %v, %d hours, %v", rest.More, len(rest.Hours), err)
	}
	since, err := st.PullWork(ctx, &machinev1.PullWorkRequest{FromHour: base.Add(700*time.Hour).Unix() / 3600}, all)
	if err != nil || len(since.Hours) != 100 {
		t.Fatalf("from an hour: %d hours, %v", len(since.Hours), err)
	}
	none, err := st.PullWork(ctx, &machinev1.PullWorkRequest{}, func(client, _ string) bool { return client == "c_other" })
	if err != nil || len(none.Keys)+len(none.Sessions) != 0 || len(none.Hours) == 0 || len(none.Hours[0].Presence)+len(none.Hours[0].Measures) != 0 {
		t.Fatalf("another client: %d keys, %d sessions, %v", len(none.Keys), len(none.Sessions), err)
	}
}

// Live: sessions as they run are counted (each once, those done now), a
// turn in progress is at work, and the machine's Watch says where the
// counts are; another agent pulls none of them.
func TestWorkCounted(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	a := h.start("say one")
	h.wait(a, idle)
	b := h.start("sleep 3s\nsay done")
	today := time.Now().UTC().Format(time.DateOnly)
	var got totals
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if got = countWork(t, h.n.st, "UTC", today, 1, true); got.sessions == 2 || time.Now().After(deadline) {
			break
		}
	}
	if got.sessions != 2 || got.done != 1 || got.turns != 1 || got.byBucket[time.Now().UTC().Hour()] == 0 || got.byClient["c_test"] != 1 {
		t.Fatalf("one done, one at work: %+v", got)
	}
	h.wait(b, idle)
	if got = countWork(t, h.n.st, "UTC", today, 1, false); got.sessions != 2 || got.done != 2 || got.turns != 2 {
		t.Fatalf("both done: %+v", got)
	}
	if w := h.firstWatch(&machinev1.WatchRequest{}); w.WorkSeq == 0 || int64(w.WorkSeq) != h.n.st.WorkSeq() {
		t.Fatalf("watch's work seq %d, counts at %d", w.WorkSeq, h.n.st.WorkSeq())
	}
	owner, err := h.n.PullWork(context.Background(), &Principal{Kind: PrincipalOwner}, &machinev1.PullWorkRequest{})
	if err != nil || len(owner.Sessions) != 2 || len(owner.Keys) != 1 || owner.Keys[0].Client != "c_test" || owner.Keys[0].Agent != "fake" {
		t.Fatalf("the owner pulls: %v %v", owner, err)
	}
	other := &Principal{Kind: PrincipalClient, ClientID: "c_other", Grant: &identityv1.Grant{GrantId: "g_other", ProjectIds: []string{"*"}}}
	if res, err := h.n.PullWork(context.Background(), other, &machinev1.PullWorkRequest{}); err != nil || len(res.Sessions)+len(res.Keys) != 0 {
		t.Fatalf("another agent pulls: %v %v", res, err)
	}
}

// Counting again from the unified kernel's stored runs gives what was counted
// live, under a new store.
func TestRebuildWork(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	for _, p := range []string{"say one", "say two\nsay three"} {
		h.wait(h.start(p), idle)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	live := countWork(t, h.n.st, "UTC", today, 1, true)
	store := h.n.st.workStore
	if err := h.n.st.RebuildWork(h.e); err != nil {
		t.Fatal(err)
	}
	rebuilt := countWork(t, h.n.st, "UTC", today, 1, true)
	if rebuilt.sessions != live.sessions || rebuilt.done != live.done || rebuilt.turns != live.turns || rebuilt.tokens != live.tokens || live.turns != 2 {
		t.Fatalf("rebuilt %+v, live %+v", rebuilt, live)
	}
	if bytes.Equal(store, h.n.st.workStore) {
		t.Fatal("the same store after a rebuild")
	}
}
