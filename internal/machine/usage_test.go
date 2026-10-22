package machine

import (
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Each turn's tokens add up by day and coding agent, for the sessions the
// caller can read.
func TestUsage(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	a := h.start("say one")
	h.wait(a, idle)
	h.send(a, "say two")
	h.wait(a, idle)
	b := h.start("say three")
	h.wait(b, idle)

	today := time.Now().In(time.UTC).Format(time.DateOnly)
	res, err := h.s.Usage(h.p, &sessionv1.GetUsageRequest{Since: today})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Days) != 1 {
		t.Fatalf("days: %v", res.Days)
	}
	d := res.Days[0]
	// The fake agent reports 10 input and 5 output tokens a turn.
	if d.Date != today || d.Agent != "fake" || d.InputTokens != 30 || d.OutputTokens != 15 || d.Sessions != 2 || d.Turns != 3 {
		t.Fatalf("day: %v", d)
	}

	tomorrow := time.Now().In(time.UTC).AddDate(0, 0, 1).Format(time.DateOnly)
	if res, err := h.s.Usage(h.p, &sessionv1.GetUsageRequest{Since: tomorrow}); err != nil || len(res.Days) != 0 {
		t.Fatalf("since tomorrow: %v %v", res, err)
	}
	if res, err := h.s.Usage(&Principal{Kind: PrincipalOwner}, &sessionv1.GetUsageRequest{Since: today, TimeZone: "Asia/Shanghai"}); err != nil || len(res.Days) != 1 {
		t.Fatalf("owner in Shanghai: %v %v", res, err)
	}
	other := &Principal{Kind: PrincipalClient, ClientID: "c_other", Grant: &identityv1.Grant{GrantId: "g_other", ProjectIds: []string{"*"}}}
	if res, err := h.s.Usage(other, &sessionv1.GetUsageRequest{Since: today}); err != nil || len(res.Days) != 0 {
		t.Fatalf("another agent sees: %v %v", res, err)
	}
	if _, err := h.s.Usage(h.p, &sessionv1.GetUsageRequest{Since: today, TimeZone: "Mars/Olympus"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad time zone: %v", err)
	}
	if _, err := h.s.Usage(h.p, &sessionv1.GetUsageRequest{Since: "yesterday"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad since: %v", err)
	}
}

// Turns stored before turn_usage existed are counted once the store opens.
func TestUsageBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermeshd.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	for seq, ev := range []*sessionv1.SessionEvent{
		{Kind: &sessionv1.SessionEvent_TurnStarted{TurnStarted: &sessionv1.TurnStarted{Turn: 1}}},
		{Kind: &sessionv1.SessionEvent_TurnCompleted{TurnCompleted: &sessionv1.TurnCompleted{Turn: 1, Usage: &sessionv1.Usage{InputTokens: 7, OutputTokens: 3}}}},
	} {
		ev.Seq, ev.Ts = uint64(seq+1), timestamppb.New(ended)
		b, _ := proto.Marshal(ev)
		if _, err := st.db.Exec(`INSERT INTO session_events(session_id, seq, payload) VALUES(?,?,?)`, "s_old", ev.Seq, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`DELETE FROM kv WHERE key=?`, usageBackfilled); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.TurnUsage(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0] != (usageRow{"s_old", ended.Unix(), 7, 3}) {
		t.Fatalf("backfilled: %+v", rows)
	}
}

// A session says when its turn in progress started, and not once it is over.
func TestTurnStartedAt(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	before := time.Now().Add(-time.Second)
	id := h.start("sleep 1s\nsay done")
	var running *sessionv1.Session
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		sess, err := h.s.Get(h.p, id)
		if err != nil {
			t.Fatal(err)
		}
		if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
			running = sess
			break
		}
	}
	if running == nil {
		t.Fatal("never ran")
	}
	if at := running.Progress.GetTurnStartedAt(); at == nil || at.AsTime().Before(before) || at.AsTime().After(time.Now()) {
		t.Fatalf("running turn started at %v", at)
	}
	done := h.wait(id, idle)
	if at := done.Progress.GetTurnStartedAt(); at != nil {
		t.Fatalf("idle session says its turn started at %v", at)
	}
}
