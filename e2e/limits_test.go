//go:build unix

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// agentLimits is what the first machine's status has of each agent's limits.
func agentLimits(t *testing.T, c localv1connect.LocalServiceClient) map[string]*machinev1.AgentLimits {
	t.Helper()
	res, err := c.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
	if err != nil || len(res.Machines) == 0 {
		return nil
	}
	out := map[string]*machinev1.AgentLimits{}
	for _, a := range res.Machines[0].Status.GetAgents() {
		if a.Limits != nil {
			out[a.Name] = a.Limits
		}
	}
	return out
}

// windows reads limits as "plan 300:39,10080:64".
func windows(l *machinev1.AgentLimits) string {
	var ws []string
	for _, w := range l.GetWindows() {
		ws = append(ws, fmt.Sprintf("%d:%d", w.Minutes, w.UsedPercent))
	}
	return l.GetPlan() + " " + strings.Join(ws, ",")
}

// TestLimitsAskedWhenChecked (docs/overview-usage.md §7): Claude Code and
// Codex are asked their accounts' rate limits when the coding agents are
// checked, without a session: the machine's first check has them, and the
// app asking again (the Limits page opening) has them as they are now. The
// fakes answer as Claude Code 2.1.330 and Codex 0.171 answered (recorded).
func TestLimitsAskedWhenChecked(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	dir := t.TempDir()
	usage, err := os.ReadFile(filepath.Join("internal", "runtime", "claude", "testdata", "claude-2.1.330-get-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	answer := filepath.Join(dir, "usage.json")
	os.WriteFile(answer, usage, 0o644)
	claude := filepath.Join(dir, "claude")
	os.WriteFile(claude, []byte(`#!/bin/sh
case "$1" in
--version) echo "2.1.330 (Claude Code)";;
auth) echo '{"loggedIn":true}';;
-p) while read -r line; do case "$line" in
  *hermesh-models*) echo '{"type":"control_response","response":{"subtype":"success","request_id":"hermesh-models","response":{"models":[]}}}';;
  *hermesh-usage*) tr -d '\n' < `+answer+`; echo;;
  esac; done;;
esac
`), 0o755)
	read, err := os.ReadFile(filepath.Join("internal", "runtime", "codex", "testdata", "codex-0.171-rate-limits-read.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct{ Result json.RawMessage }
	json.Unmarshal(read, &recorded)
	os.WriteFile(filepath.Join(dir, "rate-limits.json"), recorded.Result, 0o644)
	codex := filepath.Join(dir, "codex")
	os.WriteFile(codex, []byte(`#!/bin/sh
case "$1" in
--version) echo "codex-cli 0.171.0";;
login) echo "Logged in using ChatGPT";;
*) FAKE_CODEX_RATE_LIMITS="$(cat `+filepath.Join(dir, "rate-limits.json")+`)" exec `+filepath.Join(binaries(t), "fakecodex")+` "$@";;
esac
`), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_CLAUDE_BIN=" + claude, "HERMESH_CODEX_BIN=" + codex}, &localv1.GrantSpec{Agents: []string{"claude", "codex"}})

	var got map[string]*machinev1.AgentLimits
	eventually(t, 60*time.Second, "both agents' limits from the first check", func() bool {
		got = agentLimits(t, w.app.client)
		return got["claude"] != nil && got["codex"] != nil
	})
	if s := windows(got["claude"]); s != "max 300:39,10080:64" {
		t.Fatalf("Claude Code: %s", s)
	}
	if at := got["claude"].Windows[0].ResetsAt.AsTime(); !at.Equal(time.Date(2026, 11, 24, 20, 40, 0, 0, time.UTC)) {
		t.Fatalf("Claude Code's five hours reset at %v", at)
	}
	if s := windows(got["codex"]); s != "pro 10080:61" {
		t.Fatalf("Codex: %s", s)
	}
	if res, err := w.app.client.ListMachines(context.Background(), &localv1.ListMachinesRequest{}); err != nil || len(res.Machines[0].Status.GetSessions()) != 0 {
		t.Fatalf("sessions: %v %v", res.Machines[0].Status.GetSessions(), err)
	}

	// Used more since: once that check is over, the app asks again, and has
	// it at once.
	eventually(t, 2*time.Minute, "the first check over", func() bool {
		res, err := w.app.client.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
		if err != nil || len(res.Machines) == 0 {
			return false
		}
		for _, a := range res.Machines[0].Status.GetAgents() {
			if a.Checking {
				return false
			}
		}
		return true
	})
	if !strings.Contains(string(usage), `"five_hour":{"utilization":39`) {
		t.Fatal("the recorded answer changed shape")
	}
	os.WriteFile(answer, []byte(strings.Replace(string(usage), `"five_hour":{"utilization":39`, `"five_hour":{"utilization":52`, 1)), 0o644)
	if _, err := w.app.client.CheckAgents(context.Background(), &localv1.CheckAgentsRequest{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the five hours at 52%", func() bool { return windows(agentLimits(t, w.app.client)["claude"]) == "max 300:52,10080:64" })
}

// TestRealLimitsAsked: the Claude Code and Codex installed here, and agy
// when it is, answer their accounts' limits when checked (HERMESH_REAL_CLI=1;
// no turn, no tokens).
func TestRealLimitsAsked(t *testing.T) {
	if testing.Short() || os.Getenv("HERMESH_REAL_CLI") != "1" {
		t.Skip("real Claude Code and Codex only: HERMESH_REAL_CLI=1")
	}
	for _, bin := range []string{"claude", "codex"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	_, agy := exec.LookPath("agy")
	w := newOwnerMac(t, []string{"PATH=" + os.Getenv("PATH")}, &localv1.GrantSpec{})
	var got map[string]*machinev1.AgentLimits
	eventually(t, 90*time.Second, "the agents' limits", func() bool {
		got = agentLimits(t, w.app.client)
		return got["claude"] != nil && got["codex"] != nil && (agy != nil || got["antigravity"] != nil)
	})
	for name, l := range got {
		if len(l.Windows) == 0 || (l.Plan == "") != (name == "antigravity") || !l.Windows[0].ResetsAt.AsTime().After(time.Now()) {
			t.Fatalf("%s: %v", name, l)
		}
		t.Logf("%s: %s", name, windows(l))
	}
}

// TestAntigravityLimitsAsked (docs/overview-usage.md §7): Antigravity, as
// the agy CLI, is asked its quota when the agents are checked, without a
// session: its own /usage, a group of models at a time, which the machine
// tells as it was said (recorded from agy 1.5.16).
func TestAntigravityLimitsAsked(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	usage, err := os.ReadFile(filepath.Join("internal", "runtime", "acp", "testdata", "agy-1.5.16-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	w := newOwnerMac(t, fakeAgy(t, usage), &localv1.GrantSpec{Agents: []string{"antigravity"}})
	var l *machinev1.AgentLimits
	eventually(t, time.Minute, "Antigravity's quota from the first check", func() bool {
		l = agentLimits(t, w.app.client)["antigravity"]
		return l != nil
	})
	var got []string
	for _, w := range l.Windows {
		got = append(got, fmt.Sprintf("%s %d:%d %s", w.Group, w.Minutes, w.UsedPercent, w.ResetsAt.AsTime().Format(time.RFC3339)))
	}
	want := []string{
		"Gemini Models 300:3 2026-11-24T22:18:29Z",
		"Gemini Models 10080:17 2026-11-30T20:03:19Z",
		"Claude and GPT models 300:0 2026-11-24T22:37:18Z",
		"Claude and GPT models 10080:88 2026-11-25T12:22:09Z",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || l.Plan != "" {
		t.Fatalf("windows:\n%s", strings.Join(got, "\n"))
	}
}
