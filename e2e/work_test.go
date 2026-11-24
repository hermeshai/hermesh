//go:build unix

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestWorkOnIPhone (docs/work-stats.md): the Mac's machine counts its
// sessions' work as it happens, and the Mac answers QueryWork from it in
// process. An iPhone that joins takes a copy end to end while the app is in
// front, answers from it (today by hour, who asked which coding agent), says
// the Mac is followed live, and tells its screen the counts moved. Work done
// while the app was away is there as soon as it comes back to the front.
// Both answer what the statistics ask (docs/overview-usage.md §6): only one
// machine's project, the span before the range, turns alone, and the range's
// totals without its hours.
func TestWorkOnIPhone(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	for i := range 3 {
		if s, r := w.startSession(time.Minute, "--project", "demo", "--prompt", fmt.Sprintf("say %d", i), "--wait"); s.Status != "idle" {
			t.Fatalf("session: %+v", r)
		}
	}
	ctx := context.Background()
	sh, _ := time.LoadLocation("Asia/Shanghai")
	today := func(c localv1connect.LocalServiceClient) (sessions, turns uint32, res *localv1.QueryWorkResponse) {
		t.Helper()
		res, err := c.QueryWork(ctx, &localv1.QueryWorkRequest{TimeZone: "Asia/Shanghai", FirstDay: time.Now().In(sh).Format(time.DateOnly), Days: 1,
			Bucket: localv1.WorkBucket_WORK_BUCKET_HOUR, GroupBy: []localv1.WorkDimension{localv1.WorkDimension_WORK_DIMENSION_CLIENT, localv1.WorkDimension_WORK_DIMENSION_AGENT}})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range res.RangeSessions {
			sessions += x.Sessions
		}
		for _, x := range res.RangeTurns {
			turns += x.Turns
		}
		return sessions, turns, res
	}

	// The Mac: its own machine, read in process.
	s, n, res := today(w.app.client)
	if s != 3 || n != 3 || len(res.Sources) != 1 || !res.Sources[0].Live || len(res.Sessions) == 0 {
		t.Fatalf("the Mac's own: %d sessions, %d turns, %+v", s, n, res)
	}
	mac := res.Sources[0].MachineId
	// What the statistics ask of a device: the range's totals under a filter,
	// with the span before it (yesterday, when nothing ran).
	stats := func(c localv1connect.LocalServiceClient, f *localv1.WorkGroup, turnsOnly bool) *localv1.QueryWorkResponse {
		t.Helper()
		res, err := c.QueryWork(ctx, &localv1.QueryWorkRequest{TimeZone: "Asia/Shanghai", FirstDay: time.Now().In(sh).Format(time.DateOnly), Days: 1,
			Bucket: localv1.WorkBucket_WORK_BUCKET_NONE, Filter: f, Previous: true, TurnsOnly: turnsOnly})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	filtered := func(c localv1connect.LocalServiceClient, who string, want uint32) {
		t.Helper()
		in := stats(c, &localv1.WorkGroup{MachineId: mac, ProjectId: "demo"}, false)
		if len(in.RangeSessions) != 1 || in.RangeSessions[0].Sessions != want || len(in.RangeTurns) != 1 || in.RangeTurns[0].Turns != want ||
			len(in.Sessions)+len(in.Turns) != 0 || len(in.PreviousSessions)+len(in.PreviousTurns) != 0 {
			t.Fatalf("%s, the Mac's demo, totals only: %+v", who, in)
		}
		// Another machine's project of the same name, another project and another agent are not this work.
		for _, f := range []*localv1.WorkGroup{{MachineId: "m_other", ProjectId: "demo"}, {MachineId: mac, ProjectId: "other"}, {Agent: "another"}} {
			if out := stats(c, f, false); len(out.RangeSessions)+len(out.RangeTurns) != 0 {
				t.Fatalf("%s, %+v: %+v", who, f, out)
			}
		}
		if only := stats(c, &localv1.WorkGroup{MachineId: mac}, true); len(only.RangeSessions) != 0 || len(only.RangeTurns) != 1 || only.RangeTurns[0].Turns != want {
			t.Fatalf("%s, turns only: %+v", who, only)
		}
		if _, err := c.QueryWork(ctx, &localv1.QueryWorkRequest{TimeZone: "Asia/Shanghai", FirstDay: time.Now().In(sh).Format(time.DateOnly), Days: 1,
			Bucket: localv1.WorkBucket_WORK_BUCKET_DAY, Filter: &localv1.WorkGroup{ProjectId: "demo"}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s, a project without its machine: %v", who, err)
		}
	}
	filtered(w.app.client, "the Mac", 3)
	// Tomorrow's range has today before it: the span before is today's work.
	tomorrow, err := w.app.client.QueryWork(ctx, &localv1.QueryWorkRequest{TimeZone: "Asia/Shanghai", FirstDay: time.Now().In(sh).AddDate(0, 0, 1).Format(time.DateOnly), Days: 1,
		Bucket: localv1.WorkBucket_WORK_BUCKET_DAY, Previous: true, Filter: &localv1.WorkGroup{MachineId: mac, ProjectId: "demo"}})
	if err != nil || len(tomorrow.RangeSessions) != 0 || len(tomorrow.PreviousSessions) != 1 || tomorrow.PreviousSessions[0].Sessions != 3 || tomorrow.PreviousTurns[0].Turns != 3 {
		t.Fatalf("today as the span before tomorrow: %+v, %v", tomorrow, err)
	}

	// The iPhone, in front: it pulls the Mac's counts and answers from its copy.
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, _ := identity.GenerateKey()
	m := joinMobile(t, w, st.RelayUrl, dir, root)
	front, leave := context.WithCancel(ctx)
	scr := watchScreenOf(front, t, m.client)
	eventually(t, 60*time.Second, "the Mac's work on the iPhone", func() bool {
		var s, n uint32
		s, n, res = today(m.client)
		return s == 3 && n == 3 && len(res.Sources) == 1 && res.Sources[0].Live && !res.Sources[0].Syncing
	})
	if len(res.RangeTurns) != 1 || res.RangeTurns[0].Group.Client == "" || res.RangeTurns[0].Group.Agent == "" || res.RangeTurns[0].Group.MachineId != "" {
		t.Fatalf("grouped by who asked and the agent: %+v", res.RangeTurns)
	}
	eventually(t, 10*time.Second, "the screen told the counts moved", scr.has(func(s *screen) bool { return s.work > 0 }))
	filtered(m.client, "the iPhone", 3)

	// Away: the Mac works on. Back in front, the iPhone catches up at once.
	leave()
	m.do("pause")
	if s, r := w.startSession(time.Minute, "--project", "demo", "--prompt", "say away", "--wait"); s.Status != "idle" {
		t.Fatalf("session: %+v", r)
	}
	m.do("resume")
	back, gone := context.WithCancel(ctx)
	defer gone()
	watchScreenOf(back, t, m.client)
	if within(15*time.Second, func() bool { s, n, _ := today(m.client); return s == 4 && n == 4 }) < 0 {
		s, n, res := today(m.client)
		t.Fatalf("not caught up: %d sessions, %d turns, %+v", s, n, res.Sources)
	}
}
