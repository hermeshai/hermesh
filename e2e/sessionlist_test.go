package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/machine"
)

// TestSessionList: the owner's app pages through the sessions of two
// machines newest first, without gaps or repeats, and WatchSessions tells
// it at once of a new session and of its status changes, and of a machine
// going away and coming back.
func TestSessionList(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	ctx := context.Background()

	// A second machine, vps-1, with a project demo as well.
	daemon := filepath.Join(w.bin, "hermeshd")
	data := filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", env, daemon, "pair", w.addr, "--name", "vps-1", "--no-service", "--data-dir", data)
	if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	runVPS := func() *proc {
		p := start(t, "vps-1", env, daemon, "run", "--data-dir", data)
		w.logs = append(w.logs, p)
		return p
	}
	vps := runVPS()
	var vpsID string
	eventually(t, 30*time.Second, "both machines online", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		on := 0
		for _, m := range res.GetMachines() {
			if m.Online && m.Status != nil {
				on++
			}
			if m.MachineName == "vps-1" {
				vpsID = m.MachineId
			}
		}
		return err == nil && on == 2
	})
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: gitRepo(t, map[string]string{"README.md": "# vps\n"})}); err != nil {
		t.Fatal(err)
	}

	// Sessions on both machines: first one that failed (unsettled, so it
	// comes with the first page however old), then 15 on each.
	if _, r := w.startSession(time.Minute, "--project", "vps-1/demo", "--prompt", "fail boom", "--wait"); r.code == 0 {
		t.Fatalf("the failing session: %+v", r)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range 30 {
		project := []string{"owner-mac/demo", "vps-1/demo"}[i%2]
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if s, r := w.startSession(time.Minute, "--project", project, "--prompt", fmt.Sprintf("say %d", i), "--wait"); s.Status != "idle" {
				t.Errorf("session %d: %+v", i, r)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	// The owner's replica hears of the last ones from vps-1's watch, a
	// moment after the agent saw them finish.
	var all *localv1.ListSessionsResponse
	var err error
	eventually(t, 15*time.Second, "all sessions listed", func() bool {
		all, err = w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		return len(all.GetSessions()) == 31
	})
	if err != nil || len(all.Sessions) != 31 || len(all.Unreachable) != 0 {
		t.Fatalf("all sessions: %d %v %v", len(all.GetSessions()), all.GetUnreachable(), err)
	}
	var failed string
	for _, s := range all.Sessions {
		if s.GetProgress().GetLastFailure() != nil {
			failed = s.SessionId
		}
	}

	const limit = 7
	var got []*sessionv1.Session
	var before *sessionv1.SessionCursor
	for page := 0; ; page++ {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: limit, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		list := res.Sessions
		if page == 0 {
			if len(list) != limit+1 || list[limit].SessionId != failed {
				t.Fatalf("first page: %d sessions, the last %s; want %d and then the failed %s", len(list), list[len(list)-1].SessionId, limit, failed)
			}
			list = list[:limit]
		}
		got = append(got, list...)
		if res.Next == nil {
			break
		}
		if len(list) != limit {
			t.Fatalf("page %d has %d sessions and a next one", page, len(list))
		}
		before = res.Next
	}
	slices.SortFunc(all.Sessions, machine.NewestFirst)
	if !slices.EqualFunc(got, all.Sessions, func(a, b *sessionv1.Session) bool { return a.SessionId == b.SessionId }) {
		t.Fatalf("paged %d sessions, not the %d newest first", len(got), len(all.Sessions))
	}

	// Watching: changes arrive within a second of happening.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := w.app.client.WatchSessions(wctx, &localv1.WatchSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	type arrival struct {
		m  *localv1.WatchSessionsResponse
		at time.Time
	}
	msgs := make(chan arrival, 1000)
	go func() {
		for stream.Receive() {
			msgs <- arrival{stream.Msg(), time.Now()}
		}
	}()
	next := func(what string, ok func(*localv1.WatchSessionsResponse) bool) arrival {
		t.Helper()
		deadline := time.After(30 * time.Second)
		for {
			select {
			case a := <-msgs:
				if ok(a.m) {
					return a
				}
			case <-deadline:
				t.Fatalf("watch: no %s", what)
			}
		}
	}
	if a := next("first message", func(*localv1.WatchSessionsResponse) bool { return true }); !a.m.MachinesChanged || len(a.m.Unreachable) != 0 {
		t.Fatalf("first message %v", a.m)
	}
	s, r := w.startSession(time.Minute, "--project", "vps-1/demo", "--prompt", "sleep 2s\nsay done")
	if s.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	with := func(status sessionv1.SessionStatus) func(*localv1.WatchSessionsResponse) bool {
		return func(m *localv1.WatchSessionsResponse) bool {
			return slices.ContainsFunc(m.Sessions, func(x *sessionv1.Session) bool {
				return x.SessionId == s.SessionID && (status == 0 || x.Status == status)
			})
		}
	}
	a := next("new session", with(0))
	created := a.m.Sessions[slices.IndexFunc(a.m.Sessions, func(x *sessionv1.Session) bool { return x.SessionId == s.SessionID })].CreatedAt.AsTime()
	if d := a.at.Sub(created); d > time.Second {
		t.Fatalf("the new session arrived %v after it started", d)
	}
	a = next("idle session", with(sessionv1.SessionStatus_SESSION_STATUS_IDLE))
	idle := a.m.Sessions[slices.IndexFunc(a.m.Sessions, func(x *sessionv1.Session) bool { return x.SessionId == s.SessionID })]
	if d := a.at.Sub(idle.UpdatedAt.AsTime()); d > time.Second {
		t.Fatalf("the session's idle status arrived %v after it changed", d)
	}

	// A machine going away, and coming back.
	vps.stop()
	unreachable := func(m *localv1.WatchSessionsResponse) bool {
		return m.MachinesChanged && slices.ContainsFunc(m.Unreachable, func(u *localv1.Unreachable) bool { return u.MachineName == "vps-1" })
	}
	next("vps-1 unreachable", unreachable)
	runVPS()
	next("vps-1 back", func(m *localv1.WatchSessionsResponse) bool { return m.MachinesChanged && len(m.Unreachable) == 0 })
}
