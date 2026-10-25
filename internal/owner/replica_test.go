package owner

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func at(id string, seq uint64, t time.Time, status sessionv1.SessionStatus) *sessionv1.Session {
	return &sessionv1.Session{SessionId: id, Status: status, UpdatedAt: timestamppb.New(t), Progress: &sessionv1.SessionProgress{LastEventSeq: seq}}
}

// A later state of a session replaces the kept one, by its events, then by
// its time; an earlier one, as a snapshot read before a change came, does not.
func TestKeepLater(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	idle, running := sessionv1.SessionStatus_SESSION_STATUS_IDLE, sessionv1.SessionStatus_SESSION_STATUS_RUNNING
	for _, c := range []struct {
		name      string
		kept, got *sessionv1.Session
		want      sessionv1.SessionStatus
	}{
		{"more events", at("s", 3, t0, running), at("s", 4, t0, idle), idle},
		{"fewer events", at("s", 4, t0, running), at("s", 3, t0.Add(time.Hour), idle), running},
		{"same events, later", at("s", 4, t0, running), at("s", 4, t0.Add(time.Second), idle), idle},
		{"same events, earlier", at("s", 4, t0.Add(time.Second), running), at("s", 4, t0, idle), running},
		{"same state", at("s", 4, t0, running), at("s", 4, t0, idle), idle},
	} {
		m := map[string]*sessionv1.Session{"s": c.kept}
		keepLater(m, c.got)
		if m["s"].Status != c.want {
			t.Errorf("%s: kept %v, want %v", c.name, m["s"].Status, c.want)
		}
	}
	m := map[string]*sessionv1.Session{}
	keepLater(m, at("new", 1, t0, idle))
	if m["new"] == nil {
		t.Error("a new session is not kept")
	}
}

// Trimming keeps a machine's newest sessions and every unsettled one, and
// says when it dropped some (the machine has more than the replica holds).
func TestTrimSessions(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	m := map[string]*sessionv1.Session{}
	for i := range 10 {
		status := sessionv1.SessionStatus_SESSION_STATUS_IDLE
		if i == 0 {
			status = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT // the oldest, still asking
		}
		s := at(fmt.Sprintf("s%d", i), 1, t0.Add(time.Duration(i)*time.Minute), status)
		m[s.SessionId] = s
	}
	if trimSessions(m, 10) {
		t.Error("dropped sessions at the limit")
	}
	if !trimSessions(m, 4) {
		t.Error("said nothing was dropped")
	}
	var kept []string
	for id := range m {
		kept = append(kept, id)
	}
	slices.Sort(kept)
	if want := []string{"s0", "s6", "s7", "s8", "s9"}; !slices.Equal(kept, want) {
		t.Errorf("kept %v, want %v", kept, want)
	}
}

// A poll that started earlier never overwrites what a later one found: a
// refresh after a write answers before the poll that was under way.
func TestPolledKeepsTheNewest(t *testing.T) {
	var p polled[*localv1.Client]
	if !p.set(2, []*localv1.Client{{Label: "after the write"}}, nil) {
		t.Fatal("the first answer was not kept")
	}
	if p.set(1, nil, nil) {
		t.Error("an older poll overwrote a newer one")
	}
	if got, _ := listed(&p); len(got) != 1 || got[0].Label != "after the write" {
		t.Errorf("listed %v", got)
	}
	got, _ := listed(&p)
	got[0].Label = "changed by a caller"
	if again, _ := listed(&p); again[0].Label != "after the write" {
		t.Error("a caller changed the replica")
	}
}

func TestOwnsSession(t *testing.T) {
	for _, c := range []struct {
		machine, session string
		want             bool
	}{
		{"m_abcdef0123", "s_abcdef_01j", true},
		{"m_abcdef0123", "s_abcdeg_01j", false},
		{"m_abcdef0123", "x_abcdef_01j", false},
		{"m_abcdef0123", "s_abcdef", false},
	} {
		if got := ownsSession(c.machine, c.session); got != c.want {
			t.Errorf("ownsSession(%s, %s) = %v", c.machine, c.session, got)
		}
	}
}
