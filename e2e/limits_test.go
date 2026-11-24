//go:build unix

package e2e

import (
	"context"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestLimitsReachDevices (docs/overview-usage.md §7): a coding agent says
// its account's rate limits in a session's turn (the fake agent, as Claude
// Code and Codex do), the machine keeps the last and tells it with its
// status. The Mac reads it in process; an iPhone that joined has it from the
// machine's watch, and has the change when the agent says it again, as the
// window runs low.
func TestLimitsReachDevices(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	ctx := context.Background()
	limits := func(c localv1connect.LocalServiceClient) *machinev1.AgentLimits {
		t.Helper()
		res, err := c.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range res.Machines {
			for _, a := range m.Status.GetAgents() {
				if a.Name == "claude" { // the fake agent, in its place
					return a.Limits
				}
			}
		}
		return nil
	}
	if l := limits(w.app.client); l != nil {
		t.Fatalf("limits before the agent said any: %v", l)
	}
	say := func(lim string) {
		t.Helper()
		if s, r := w.startSession(time.Minute, "--prompt", "limits "+lim+"\nsay ok", "--wait"); s.Status != "idle" {
			t.Fatalf("session: %+v", r)
		}
	}
	say("300:62,10080:40,plan=pro")
	l := limits(w.app.client)
	if l == nil || l.Plan != "pro" || len(l.Windows) != 2 || l.Windows[0].Minutes != 300 || l.Windows[0].UsedPercent != 62 || l.Windows[1].UsedPercent != 40 ||
		time.Until(l.Windows[0].ResetsAt.AsTime()) < 4*time.Hour || time.Since(l.ObservedAt.AsTime()) > time.Minute {
		t.Fatalf("the Mac's: %v", l)
	}

	// An iPhone joins: the machine's status reaches it with what was said.
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.GenerateKey()
	m := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)
	front, leave := context.WithCancel(ctx)
	defer leave()
	watchScreenOf(front, t, m.client)
	eventually(t, 60*time.Second, "the limits on the iPhone", func() bool {
		l := limits(m.client)
		return l != nil && len(l.Windows) == 2 && l.Windows[0].UsedPercent == 62
	})

	// The five hours run low: the iPhone has it within the watch's second.
	say("300:85,10080:41,plan=pro")
	if l := limits(w.app.client); l.Windows[0].UsedPercent != 85 {
		t.Fatalf("the Mac, later: %v", l)
	}
	if within(10*time.Second, func() bool {
		l := limits(m.client)
		return l != nil && l.Windows[0].UsedPercent == 85 && l.Windows[1].UsedPercent == 41
	}) < 0 {
		t.Fatalf("the iPhone, later: %v", limits(m.client))
	}
}
