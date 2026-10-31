//go:build unix

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	today := func(c localv1connect.LocalServiceClient) (sessions, turns uint32, res *localv1.QueryWorkResponse) {
		t.Helper()
		res, err := c.QueryWork(ctx, &localv1.QueryWorkRequest{TimeZone: "UTC", FirstDay: time.Now().UTC().Format(time.DateOnly), Days: 1,
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
	if s, n, res := today(w.app.client); s != 3 || n != 3 || len(res.Sources) != 1 || !res.Sources[0].Live || len(res.Sessions) == 0 {
		t.Fatalf("the Mac's own: %d sessions, %d turns, %+v", s, n, res)
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
	var res *localv1.QueryWorkResponse
	eventually(t, 60*time.Second, "the Mac's work on the iPhone", func() bool {
		var s, n uint32
		s, n, res = today(m.client)
		return s == 3 && n == 3 && len(res.Sources) == 1 && res.Sources[0].Live && !res.Sources[0].Syncing
	})
	if len(res.RangeTurns) != 1 || res.RangeTurns[0].Group.Client == "" || res.RangeTurns[0].Group.Agent == "" || res.RangeTurns[0].Group.MachineId != "" {
		t.Fatalf("grouped by who asked and the agent: %+v", res.RangeTurns)
	}
	eventually(t, 10*time.Second, "the screen told the counts moved", scr.has(func(s *screen) bool { return s.work > 0 }))

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
