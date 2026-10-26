package machine

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// The sessions that worked today: one done, one with its turn in progress;
// each counted in the hour it worked, by the client that started it, for
// the callers that can read it.
func TestActivity(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	a := h.start("say one")
	h.wait(a, idle)
	b := h.start("sleep 3s\nsay done")

	// The store writes in batches: b's turn shows once written.
	var now time.Time
	var today string
	var res *sessionv1.GetActivityResponse
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		now = time.Now()
		today = now.In(time.UTC).Format(time.DateOnly)
		var err error
		if res, err = h.s.Activity(h.p, &sessionv1.GetActivityRequest{Date: today}, now); err != nil {
			t.Fatal(err)
		}
		if len(res.Clients) == 1 && res.Clients[0].Sessions == 2 || time.Now().After(deadline) {
			break
		}
	}
	if len(res.Clients) != 1 || res.Clients[0].CreatedBy != "c_test" || res.Clients[0].Sessions != 2 || res.Clients[0].Done != 1 {
		t.Fatalf("clients: %v", res.Clients)
	}
	hour := uint32(now.In(time.UTC).Hour())
	found := false
	for _, x := range res.Hours {
		if x.Hour == hour && x.CreatedBy == "c_test" {
			found = x.Sessions == 2
		}
	}
	if !found {
		t.Fatalf("this hour: %v", res.Hours)
	}

	// The turn that ended was kept with when it started.
	spans, err := h.n.st.TurnSpans(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].sessionID != a || spans[0].start > spans[0].end {
		t.Fatalf("spans: %+v", spans)
	}
	var started int64
	if err := h.n.st.rdb.QueryRow(`SELECT started_at FROM turn_usage WHERE session_id=?`, a).Scan(&started); err != nil || started == 0 {
		t.Fatalf("started_at: %d %v", started, err)
	}

	other := &Principal{Kind: PrincipalClient, ClientID: "c_other", Grant: &identityv1.Grant{GrantId: "g_other", ProjectIds: []string{"*"}}}
	if res, err := h.s.Activity(other, &sessionv1.GetActivityRequest{Date: today}, now); err != nil || len(res.Clients) != 0 || len(res.Hours) != 0 {
		t.Fatalf("another agent sees: %v %v", res, err)
	}
	tomorrow := now.In(time.UTC).AddDate(0, 0, 1).Format(time.DateOnly)
	if res, err := h.s.Activity(h.p, &sessionv1.GetActivityRequest{Date: tomorrow}, now); err != nil || len(res.Clients) != 0 {
		t.Fatalf("tomorrow: %v %v", res, err)
	}
	if _, err := h.s.Activity(h.p, &sessionv1.GetActivityRequest{Date: today, TimeZone: "Mars/Olympus"}, now); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad time zone: %v", err)
	}
	if _, err := h.s.Activity(h.p, &sessionv1.GetActivityRequest{Date: "today"}, now); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad date: %v", err)
	}
	h.wait(b, idle)
}

// A turn counts in every hour of the day it touched, in the caller's time
// zone: one from the night before from midnight on; a turn kept before
// start times were, in the hour it ended; one after the day, not at all.
func TestActivityHours(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one")
	h.wait(id, idle)
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, hour, min int) int64 { return time.Date(2026, 3, day, hour, min, 0, 0, sh).Unix() }
	for _, r := range [][3]int64{
		{100, at(3, 23, 30), at(4, 2, 10)}, // over midnight
		{101, 0, at(4, 9, 15)},             // kept before start times
		{102, at(3, 8, 0), at(3, 9, 0)},    // the day before
		{103, at(5, 1, 0), at(5, 2, 0)},    // the day after
	} {
		if _, err := h.n.st.db.Exec(`INSERT INTO turn_usage(session_id, turn, started_at, ended_at, input_tokens, output_tokens) VALUES(?,?,?,?,0,0)`, id, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	res, err := h.s.Activity(h.p, &sessionv1.GetActivityRequest{Date: "2026-03-04", TimeZone: "Asia/Shanghai"}, time.Date(2026, 3, 6, 0, 0, 0, 0, sh))
	if err != nil {
		t.Fatal(err)
	}
	var got []uint32
	for _, x := range res.Hours {
		if x.Sessions != 1 || x.CreatedBy != "c_test" {
			t.Fatalf("hour: %v", x)
		}
		got = append(got, x.Hour)
	}
	if want := []uint32{0, 1, 2, 9}; len(got) != len(want) || got[0] != 0 || got[1] != 1 || got[2] != 2 || got[3] != 9 {
		t.Fatalf("hours: %v, want %v", got, want)
	}
	if len(res.Clients) != 1 || res.Clients[0].Sessions != 1 || res.Clients[0].Done != 1 {
		t.Fatalf("clients: %v", res.Clients)
	}
}
