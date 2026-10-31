package machine

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/workstats"
)

// totals adds up a CountWork answer's whole-range rows (bucket -1), and its
// per-bucket session counts.
type totals struct {
	sessions, done, turns, failed uint32
	tokens, busy                  uint64
	byBucket                      map[int]uint32
	byClient                      map[string]uint32 // turns, by who asked
}

func countWork(t *testing.T, st *Store, tz, first string, days int, hourly bool) totals {
	t.Helper()
	buckets, whole, err := workstats.Buckets(tz, first, days, hourly)
	if err != nil {
		t.Fatal(err)
	}
	ss, ts, keys, err := st.CountWork(context.Background(), buckets, whole)
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

func ev(at time.Time, kind any) *sessionv1.SessionEvent {
	e := &sessionv1.SessionEvent{Ts: timestamppb.New(at)}
	switch k := kind.(type) {
	case *sessionv1.TurnStarted:
		e.Kind = &sessionv1.SessionEvent_TurnStarted{TurnStarted: k}
	case *sessionv1.TurnCompleted:
		e.Kind = &sessionv1.SessionEvent_TurnCompleted{TurnCompleted: k}
	case *sessionv1.StatusChanged:
		e.Kind = &sessionv1.SessionEvent_Status{Status: k}
	}
	return e
}

// Counted from events as they are stored: a turn over midnight in Shanghai
// is at work in each quarter it touched and counts its busy seconds there,
// its turn and tokens where it ended, under the agent that asked for it
// (another than the one that started the session); a replayed completion
// counts nothing again; a turn cut off by a crash is at work in the
// quarters marked as they began, and adds no turn.
func TestWorkFromEvents(t *testing.T) {
	st := openTestStore(t)
	sh, _ := time.LoadLocation("Asia/Shanghai")
	at := func(day, hour, min int) time.Time { return time.Date(2026, 3, day, hour, min, 0, 0, sh) }
	who := &workWho{"c_creator", "fake", "demo"}
	write := func(ops ...writeOp) {
		t.Helper()
		if err := st.WriteBatch(ops); err != nil {
			t.Fatal(err)
		}
	}
	a, b := &live{id: "s_a"}, &live{id: "s_b"}
	write(writeOp{l: a, who: who, ev: ev(at(3, 23, 30), &sessionv1.TurnStarted{Turn: 1, Asker: "c_asker"})})
	done := ev(at(4, 2, 10), &sessionv1.TurnCompleted{Turn: 1, Usage: &sessionv1.Usage{InputTokens: 1000, OutputTokens: 200}})
	write(writeOp{l: a, who: who, ev: done})
	write(writeOp{l: a, who: who, ev: done}) // replayed
	write(writeOp{l: a, row: &sessionRow{ID: "s_a", Status: sessionv1.SessionStatus_SESSION_STATUS_IDLE, Progress: &sessionv1.SessionProgress{}}})

	write(writeOp{l: b, who: who, ev: ev(at(4, 10, 0), &sessionv1.TurnStarted{Turn: 1})})
	write(writeOp{workTick: workstats.Quarter(at(4, 10, 15))})
	write(writeOp{l: b, ev: ev(at(4, 12, 0), &sessionv1.StatusChanged{Status: sessionv1.SessionStatus_SESSION_STATUS_CRASHED})})
	write(writeOp{workTick: workstats.Quarter(at(4, 10, 30))}) // nothing in progress any more

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
	// In UTC the turn over Shanghai's midnight is all on the 3rd, and the
	// one that crashed (10:00 in Shanghai) on the 4th.
	if utc := countWork(t, st, "UTC", "2026-03-03", 1, false); utc.sessions != 1 || utc.turns != 1 {
		t.Fatalf("UTC: %+v", utc)
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
	turn := func(l *live, n uint32, start time.Time, mins int) []writeOp {
		return []writeOp{
			{l: l, who: who, ev: ev(start, &sessionv1.TurnStarted{Turn: n})},
			{l: l, who: who, ev: ev(start.Add(time.Duration(mins)*time.Minute), &sessionv1.TurnCompleted{Turn: n, Usage: &sessionv1.Usage{InputTokens: 10}})},
		}
	}
	// One transaction touching 800 hours: one session's turns, an hour apart.
	var ops []writeOp
	l := &live{id: "s_one"}
	for i := range 800 {
		ops = append(ops, turn(l, uint32(i+1), base.Add(time.Duration(i)*time.Hour), 5)...)
	}
	if err := st.WriteBatch(ops); err != nil {
		t.Fatal(err)
	}
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
	l2 := &live{id: "s_two"}
	for i := range 800 {
		if err := st.WriteBatch(turn(l2, uint32(i+1), base.Add(time.Duration(i)*time.Hour), 5)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.PullWork(ctx, &machinev1.PullWorkRequest{Store: first.Store, After: first.Seq}, all)
	if err != nil || !page.More || len(page.Hours) != workPageHours {
		t.Fatalf("a page: more %v, %d hours, %v", page.More, len(page.Hours), err)
	}
	rest, err := st.PullWork(ctx, &machinev1.PullWorkRequest{Store: first.Store, After: page.Seq}, all)
	if err != nil || rest.More || len(rest.Hours) != 800-workPageHours || int64(rest.Seq) != st.WorkSeq() {
		t.Fatalf("the rest: more %v, %d hours, %v", rest.More, len(rest.Hours), err)
	}
	// A first pull from an hour on takes only those.
	since, err := st.PullWork(ctx, &machinev1.PullWorkRequest{FromHour: base.Add(700*time.Hour).Unix() / 3600}, all)
	if err != nil || len(since.Hours) != 100 {
		t.Fatalf("from an hour: %d hours, %v", len(since.Hours), err)
	}
	// A client that may read nothing of c_a's gets nothing.
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
	owner, err := h.s.PullWork(context.Background(), &Principal{Kind: PrincipalOwner}, &machinev1.PullWorkRequest{})
	if err != nil || len(owner.Sessions) != 2 || len(owner.Keys) != 1 || owner.Keys[0].Client != "c_test" || owner.Keys[0].Agent != "fake" {
		t.Fatalf("the owner pulls: %v %v", owner, err)
	}
	other := &Principal{Kind: PrincipalClient, ClientID: "c_other", Grant: &identityv1.Grant{GrantId: "g_other", ProjectIds: []string{"*"}}}
	if res, err := h.s.PullWork(context.Background(), other, &machinev1.PullWorkRequest{}); err != nil || len(res.Sessions)+len(res.Keys) != 0 {
		t.Fatalf("another agent pulls: %v %v", res, err)
	}
}

// Counting again from the stored events gives what was counted live, under
// a new store.
func TestRebuildWork(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	for _, p := range []string{"say one", "say two\nsay three"} {
		h.wait(h.start(p), idle)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	live := countWork(t, h.n.st, "UTC", today, 1, true)
	store := h.n.st.workStore
	if err := h.n.st.rebuildWork(); err != nil {
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
