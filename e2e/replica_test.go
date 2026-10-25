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
	return metric(t, w.relayDebug, "hermesh_relay_owner_rpcs_total{")
}

// metric adds up the values of the metrics named by prefix that the process
// serving /metrics at addr has.
func metric(t *testing.T, addr, prefix string) int {
	res, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	n := 0
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if name, v, ok := strings.Cut(sc.Text(), " "); ok && strings.HasPrefix(name, prefix) {
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
// did cost the relay no call (hermeshd follows WatchOwner); and what changes
// shows: a session started on the other machine within a moment, and the
// app's own changes (a project added, a session archived) at once.
func TestOwnerReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	const oneWay = 150 * time.Millisecond
	w, vpsID, _ := farOwnerWithVPS(t, oneWay)
	ctx := context.Background()

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
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
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
		"GetStatus":    func() error { _, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{}); return err },
		"ListMachines": func() error { _, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); return err },
		"ListClients":  func() error { _, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{}); return err },
		"ListPairingRequests": func() error {
			_, err := w.app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
			return err
		},
		"ListDevices":  func() error { _, err := w.app.client.ListDevices(ctx, &localv1.ListDevicesRequest{}); return err },
		"ListProjects": func() error { _, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{}); return err },
		"ListSessions": func() error {
			_, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
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

	// Three windows polling as the app's screens did (pairing requests
	// every second, the rest every three) cost the relay nothing: hermeshd
	// follows its WatchOwner.
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
	if n > 0 {
		t.Errorf("%d relay control calls while nothing changed, want none", n)
	}

	// A session started on the other machine shows in a moment (pushed by
	// its watch); one the app archives shows archived at once.
	s, r := w.startSession(time.Minute, "--project", "vps-1/demo", "--prompt", "say again")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	eventually(t, 3*time.Second, "the new session listed", func() bool {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
		return err == nil && slices.ContainsFunc(res.Sessions, func(x *sessionv1.Session) bool { return x.SessionId == s.SessionID })
	})
	if _, err := w.app.client.ArchiveSession(ctx, &localv1.ArchiveSessionRequest{SessionId: onVPS}); err != nil {
		t.Fatal(err)
	}
	res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(res.Sessions, func(x *sessionv1.Session) bool { return x.SessionId == onVPS }); i < 0 || res.Sessions[i].Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		t.Errorf("the session just archived is not listed archived: %v", res.Sessions)
	}
}

// farOwnerWithVPS is an owner Mac with the relay oneWay away and a second
// machine, vps-1, both online; restart stops vps-1 and, unless stopped is
// true, starts it again.
func farOwnerWithVPS(t *testing.T, oneWay time.Duration) (w *ownerMac, vpsID string, restart func(stopped bool)) {
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w = newFarOwnerMac(t, oneWay, env, &localv1.GrantSpec{})
	ctx := context.Background()
	daemon := filepath.Join(w.bin, "hermeshd")
	data := filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", env, daemon, "pair", w.addr, "--name", "vps-1", "--no-service", "--data-dir", data)
	if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	run := func() *proc {
		p := start(t, "vps-1", env, daemon, "run", "--data-dir", data)
		w.logs = append(w.logs, p)
		return p
	}
	vps := run()
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
	return w, vpsID, func(stopped bool) {
		vps.stop()
		if !stopped {
			vps = run()
		}
	}
}

// screen is what a screen following LocalService.Watch holds, applied as
// the apps apply it.
type screen struct {
	mu       sync.Mutex
	messages int
	// withSessions: messages that carried sessions or deletions.
	withSessions int
	status       *localv1.GetStatusResponse
	machines     map[string]*localv1.Machine
	clients      map[string]*localv1.Client
	projects     map[string]*localv1.Project
	devices      map[string]*localv1.Device
	pairings     map[string]*localv1.PairingDetails
	sessions     map[string]*sessionv1.Session
	err          error
}

func watchScreen(ctx context.Context, t *testing.T, w *ownerMac) *screen {
	stream, err := w.app.client.Watch(ctx, &localv1.WatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	s := &screen{}
	go func() {
		for stream.Receive() {
			m := stream.Msg()
			s.mu.Lock()
			s.messages++
			if len(m.Sessions) > 0 || len(m.DeletedSessionIds) > 0 {
				s.withSessions++
			}
			if m.Reset_ {
				s.machines, s.clients, s.projects, s.devices = map[string]*localv1.Machine{}, map[string]*localv1.Client{}, map[string]*localv1.Project{}, map[string]*localv1.Device{}
				s.pairings = map[string]*localv1.PairingDetails{}
				if s.sessions == nil {
					s.sessions = map[string]*sessionv1.Session{}
				}
			}
			if m.Status != nil {
				s.status = m.Status
			}
			for _, x := range m.Machines {
				s.machines[x.MachineId] = x
			}
			for _, id := range m.RemovedMachineIds {
				delete(s.machines, id)
			}
			for _, x := range m.Clients {
				s.clients[x.GrantId] = x
			}
			for _, id := range m.RemovedGrantIds {
				delete(s.clients, id)
			}
			for _, x := range m.Projects {
				s.projects[x.MachineId+"/"+x.ProjectId] = x
			}
			for _, r := range m.RemovedProjects {
				delete(s.projects, r.MachineId+"/"+r.ProjectId)
			}
			for _, x := range m.Devices {
				s.devices[x.KeyId] = x
			}
			for _, id := range m.RemovedDeviceKeyIds {
				delete(s.devices, id)
			}
			for _, x := range m.Pairings {
				s.pairings[x.Code] = x
			}
			for _, code := range m.RemovedPairingCodes {
				delete(s.pairings, code)
			}
			for _, x := range m.Sessions {
				if old := s.sessions[x.SessionId]; old == nil || old.GetProgress().GetLastEventSeq() <= x.GetProgress().GetLastEventSeq() {
					s.sessions[x.SessionId] = x
				}
			}
			for _, id := range m.DeletedSessionIds {
				delete(s.sessions, id)
			}
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.err = stream.Err()
		s.mu.Unlock()
	}()
	return s
}

func (s *screen) has(f func(*screen) bool) func() bool {
	return func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.machines != nil && f(s)
	}
}

// TestOwnerWatch (docs/state-sync.md, step 2): a screen that follows
// LocalService.Watch gets the owner's world at once, and then each change
// without asking: a project added, a session started on another machine
// and its turn ending, a pairing request (the relay pushes it), that
// machine going offline (its sessions stay, the machine marked unreachable)
// and coming back. Nothing comes while nothing changes.
func TestOwnerWatch(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w, vpsID, restart := farOwnerWithVPS(t, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	scr := watchScreen(ctx, t, w)
	eventually(t, 10*time.Second, "the world at once", scr.has(func(s *screen) bool {
		return s.status.GetInitialized() && len(s.machines) == 2 && len(s.clients) == 1 && len(s.devices) == 1 && s.projects[st.LocalMachineId+"/demo"] != nil
	}))

	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: gitRepo(t, map[string]string{"README.md": "# vps\n"})}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "the project added", scr.has(func(s *screen) bool { return s.projects[vpsID+"/demo"] != nil }))

	sess, r := w.startSession(time.Minute, "--project", "vps-1/demo", "--prompt", "say hi", "--wait")
	if sess.Status != "idle" {
		t.Fatalf("start: %+v", r)
	}
	eventually(t, 3*time.Second, "the session, its turn done", scr.has(func(s *screen) bool {
		x := s.sessions[sess.SessionID]
		return x != nil && x.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	}))

	// A pairing request reaches the screen as the relay pushes it: nobody
	// asks the relay for pairing requests.
	asked := metric(t, w.relayDebug, `hermesh_relay_owner_rpcs_total{method="ListPairingRequests"}`)
	began := time.Now()
	login := start(t, "login2", []string{"HERMESH_HOME=" + filepath.Join(w.tmp, "agent2")}, filepath.Join(w.bin, "hermesh"), "auth", "login", w.addr, "--name", "bot2")
	code := login.pairingCode(t)
	eventually(t, 5*time.Second, "the pairing request on the screen", scr.has(func(s *screen) bool { return s.pairings[code] != nil }))
	t.Logf("pairing request on the screen %v after the agent asked", time.Since(began).Round(time.Millisecond))
	if got := metric(t, w.relayDebug, `hermesh_relay_owner_rpcs_total{method="ListPairingRequests"}`); got != asked {
		t.Errorf("the relay was asked for pairing requests %d times", got-asked)
	}
	login.stop()

	// Idle: nothing to send.
	time.Sleep(2 * time.Second) // what settles after the turn (the machine's counts)
	scr.mu.Lock()
	before := scr.messages
	scr.mu.Unlock()
	time.Sleep(6 * time.Second)
	scr.mu.Lock()
	idle := scr.messages - before
	scr.mu.Unlock()
	if idle != 0 {
		t.Errorf("%d messages while nothing changed", idle)
	}

	restart(true)
	eventually(t, 20*time.Second, "vps-1 unreachable, its session kept", scr.has(func(s *screen) bool {
		return s.machines[vpsID].GetUnreachable() != "" && s.sessions[sess.SessionID] != nil
	}))
	restart(false)
	eventually(t, 40*time.Second, "vps-1 back", scr.has(func(s *screen) bool {
		m := s.machines[vpsID]
		return m.GetOnline() && m.GetUnreachable() == "" && m.GetStatus() != nil && s.sessions[sess.SessionID] != nil
	}))
	scr.mu.Lock()
	defer scr.mu.Unlock()
	if scr.err != nil {
		t.Errorf("watch ended: %v", scr.err)
	}
}

// TestMachineResume (docs/state-sync.md, step 5): the owner follows another
// machine with MachineService.Watch; when the connection drops (the relay
// restarts) it resumes where it was: nothing changed, so nothing is listed
// again or sent to the screens, and the machine is followed again.
func TestMachineResume(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w, vpsID, _ := farOwnerWithVPS(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: gitRepo(t, map[string]string{"README.md": "# vps\n"})}); err != nil {
		t.Fatal(err)
	}
	sess, r := w.startSession(time.Minute, "--project", "vps-1/demo", "--prompt", "say hi", "--wait")
	if sess.Status != "idle" {
		t.Fatalf("start: %+v", r)
	}
	scr := watchScreen(ctx, t, w)
	eventually(t, 5*time.Second, "the session on the screen", scr.has(func(s *screen) bool { return s.sessions[sess.SessionID] != nil }))
	fresh := metric(t, w.ownerDebug, `hermesh_owner_machine_watches_total{start="fresh"}`)
	resumed := metric(t, w.ownerDebug, `hermesh_owner_machine_watches_total{start="resumed"}`)
	if fresh < 1 {
		t.Fatalf("vps-1 is not followed by its Watch (fresh %d)", fresh)
	}
	time.Sleep(2 * time.Second) // what settles after the turn
	scr.mu.Lock()
	before := scr.withSessions
	scr.mu.Unlock()

	w.restartRelay()
	eventually(t, 40*time.Second, "vps-1 followed again, resumed", func() bool {
		return metric(t, w.ownerDebug, `hermesh_owner_machine_watches_total{start="resumed"}`) > resumed
	})
	eventually(t, 10*time.Second, "vps-1 reachable on the screen", scr.has(func(s *screen) bool { return s.machines[vpsID].GetUnreachable() == "" }))
	if got := metric(t, w.ownerDebug, `hermesh_owner_machine_watches_total{start="fresh"}`); got != fresh {
		t.Errorf("vps-1 was listed again: %d fresh starts, %d before", got, fresh)
	}
	scr.mu.Lock()
	defer scr.mu.Unlock()
	if scr.withSessions != before {
		t.Errorf("%d session messages to the screen after a reconnect that changed nothing", scr.withSessions-before)
	}
}
