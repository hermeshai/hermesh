package e2e

import (
	"bufio"
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// ownerRPCs is how many control calls the relay served owner devices so far
// (hermesh_relay_owner_rpcs_total, all methods).
func ownerRPCs(t *testing.T, w *ownerMac) int {
	res, err := http.Get("http://" + w.relayDebug + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	n := 0
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if name, v, ok := strings.Cut(sc.Text(), " "); ok && strings.HasPrefix(name, "hermesh_relay_owner_rpcs_total{") {
			c, _ := strconv.Atoi(v)
			n += c
		}
	}
	return n
}

// TestOwnerReplica (docs/state-sync.md, step 1): with the relay 300 ms away,
// the owner's app lists machines, agents, pairing requests, devices,
// projects and the sessions of this Mac and of another machine in a few
// milliseconds, from hermeshd's replica; three windows polling as the app
// does cost the relay no more than one device's polls; and what changes
// shows: a session started on the other machine within a moment, and the
// app's own changes (a project added, a session archived) at once.
func TestOwnerReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	const oneWay = 150 * time.Millisecond
	w := newFarOwnerMac(t, oneWay, env, &localv1.GrantSpec{})
	ctx := context.Background()

	// A second machine, vps-1.
	daemon := filepath.Join(w.bin, "hermeshd")
	data := filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", env, daemon, "pair", w.addr, "--name", "vps-1", "--no-service", "--data-dir", data)
	if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	w.logs = append(w.logs, start(t, "vps-1", env, daemon, "run", "--data-dir", data))
	var vpsID string
	eventually(t, 30*time.Second, "both machines online, with their state", func() bool {
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
		return err == nil && on == 2 && vpsID != ""
	})

	// The app's own change shows in the next list, without waiting for a poll.
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: gitRepo(t, map[string]string{"README.md": "# vps\n"})}); err != nil {
		t.Fatal(err)
	}
	ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{})
	if err != nil || !slices.ContainsFunc(ps.Projects, func(p *localv1.Project) bool { return p.MachineId == vpsID && p.ProjectId == "demo" }) {
		t.Fatalf("the project just added is not listed: %v %v", ps.GetProjects(), err)
	}

	// A session on each machine, started by the agent.
	for _, project := range []string{"owner-mac/demo", "vps-1/demo"} {
		if s, r := w.startSession(time.Minute, "--project", project, "--prompt", "say hi", "--wait"); s.Status != "idle" {
			t.Fatalf("%s: %+v", project, r)
		}
	}
	var onVPS string
	eventually(t, 5*time.Second, "both sessions listed", func() bool {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50})
		onVPS = ""
		for _, s := range res.GetSessions() {
			if strings.HasPrefix(strings.TrimPrefix(vpsID, "m_"), strings.Split(s.SessionId, "_")[1]) {
				onVPS = s.SessionId
			}
		}
		return err == nil && len(res.Sessions) == 2 && onVPS != ""
	})

	// Every list, and the status, answers without the relay.
	calls := map[string]func() error{
		"GetStatus":           func() error { _, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{}); return err },
		"ListMachines":        func() error { _, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); return err },
		"ListClients":         func() error { _, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{}); return err },
		"ListPairingRequests": func() error { _, err := w.app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{}); return err },
		"ListDevices":         func() error { _, err := w.app.client.ListDevices(ctx, &localv1.ListDevicesRequest{}); return err },
		"ListProjects":        func() error { _, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{}); return err },
		"ListSessions": func() error {
			_, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50})
			return err
		},
	}
	for name, call := range calls {
		var took []time.Duration
		for range 21 {
			began := time.Now()
			if err := call(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			took = append(took, time.Since(began))
		}
		slices.Sort(took)
		t.Logf("%-20s p50 %v, max %v", name, took[10].Round(10*time.Microsecond), took[20].Round(10*time.Microsecond))
		if took[10] > 5*time.Millisecond {
			t.Errorf("%s: p50 %v, want at most 5ms with the relay %v away", name, took[10], 2*oneWay)
		}
	}

	// Three windows polling as the app's screens do (pairing requests every
	// second, the rest every three) cost the relay one device's polls.
	const span = 30 * time.Second
	before := ownerRPCs(t, w)
	pollCtx, stop := context.WithTimeout(ctx, span)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for i := 0; pollCtx.Err() == nil; i++ {
				calls["ListPairingRequests"]()
				if i%3 == 0 {
					for _, name := range []string{"GetStatus", "ListMachines", "ListClients", "ListProjects", "ListDevices", "ListSessions"} {
						calls[name]()
					}
				}
				select {
				case <-pollCtx.Done():
				case <-time.After(time.Second):
				}
			}
		})
	}
	wg.Wait()
	stop()
	n := ownerRPCs(t, w) - before
	perMinute := float64(n) * float64(time.Minute) / float64(span)
	t.Logf("relay control calls with three windows: %d in %v (%.0f a minute)", n, span, perMinute)
	if perMinute > 64 { // 60 a minute, and a poll of each kind more at the edges
		t.Errorf("%.0f relay control calls a minute, want at most 60", perMinute)
	}

	// A session started on the other machine shows in a moment (pushed by
	// its watch); one the app archives shows archived at once.
	s, r := w.startSession(time.Minute, "--project", "vps-1/demo", "--prompt", "say again")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	eventually(t, 3*time.Second, "the new session listed", func() bool {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50})
		return err == nil && slices.ContainsFunc(res.Sessions, func(x *sessionv1.Session) bool { return x.SessionId == s.SessionID })
	})
	if _, err := w.app.client.ArchiveSession(ctx, &localv1.ArchiveSessionRequest{SessionId: onVPS}); err != nil {
		t.Fatal(err)
	}
	res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(res.Sessions, func(x *sessionv1.Session) bool { return x.SessionId == onVPS }); i < 0 || res.Sessions[i].Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		t.Errorf("the session just archived is not listed archived: %v", res.Sessions)
	}
}
