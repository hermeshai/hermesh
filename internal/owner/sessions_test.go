package owner

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/machine"
)

// machinePage is what a machine answers to ListSessions with a limit.
func machinePage(all []*sessionv1.Session, limit int, before *sessionv1.SessionCursor) ([]*sessionv1.Session, *sessionv1.SessionCursor) {
	var past []*sessionv1.Session
	for _, s := range all {
		if machine.Past(s, before) {
			past = append(past, s)
		}
	}
	slices.SortFunc(past, machine.NewestFirst)
	if len(past) <= limit {
		return past, nil
	}
	page := past[:limit:limit]
	if before == nil {
		for _, s := range past[limit:] {
			if machine.Unsettled(s) {
				page = append(page, s)
			}
		}
	}
	return page, machine.CursorAt(past[limit-1])
}

// Merged pages across machines list every session once, newest first, also
// with a machine too old to page; the first also carries every unsettled
// session, however old.
func TestOnePageAcrossMachines(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	session := func(m string, i int, at time.Duration, st sessionv1.SessionStatus) *sessionv1.Session {
		return &sessionv1.Session{SessionId: fmt.Sprintf("s_%s_%02d", m, i), MachineId: m, Status: st, UpdatedAt: timestamppb.New(t0.Add(at))}
	}
	idle := sessionv1.SessionStatus_SESSION_STATUS_IDLE
	running := sessionv1.SessionStatus_SESSION_STATUS_RUNNING
	// Machine a: busy lately; machine b: sessions spread out, two at the
	// very same time as a's (the id decides), and an old one still running.
	var a, b []*sessionv1.Session
	for i := range 9 {
		a = append(a, session("a", i, time.Duration(100+i)*time.Minute, idle))
	}
	for i := range 6 {
		b = append(b, session("b", i, time.Duration(i*30)*time.Minute, idle))
	}
	b = append(b, session("b", 6, 100*time.Minute, idle), session("b", 7, 101*time.Minute, idle))
	b[0].Status = running
	// Machine c is too old to page: it answers with all its sessions.
	var c []*sessionv1.Session
	for i := range 5 {
		c = append(c, session("c", i, time.Duration(95+i*3)*time.Minute, idle))
	}
	machines := [][]*sessionv1.Session{a, b}

	const limit = 4
	var got []*sessionv1.Session
	var before *sessionv1.SessionCursor
	for page := 0; page < 10; page++ {
		sessions := slices.Clone(c)
		more := false
		for _, m := range machines {
			list, next := machinePage(m, limit, before)
			sessions = append(sessions, list...)
			more = more || next != nil
		}
		list, next := onePage(sessions, limit, before, more)
		if page == 0 {
			if len(list) != limit+1 || list[limit].SessionId != b[0].SessionId {
				t.Fatalf("first page %v: wants %d, then the running %s", ids(list), limit, b[0].SessionId)
			}
			list = list[:limit]
		}
		got = append(got, list...)
		if next == nil {
			break
		}
		before = next
	}
	all := slices.Concat(a, b, c)
	slices.SortFunc(all, machine.NewestFirst)
	if !slices.Equal(ids(got), ids(all)) {
		t.Fatalf("pages %v\nall   %v", ids(got), ids(all))
	}
}

func ids(ss []*sessionv1.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.SessionId)
	}
	return out
}
