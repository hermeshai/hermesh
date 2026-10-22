package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestMachineOnlyMac: a Mac joins its owner as a machine only (a work Mac):
// approved on a trusted device and, because the owner id came from the
// center, confirmed with the two digits that device shows. Agents run
// sessions on it; its own app sees only this Mac's projects and sessions,
// knows the agents by name, adds projects, answers and stops sessions as a
// trusted device would, and approves nothing. Removed by the owner, it says so and leaves. A Mac given the
// connection address by the person needs no digits.
func TestMachineOnlyMac(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"HERMESH_GEMINI_BIN=" + filepath.Join(binaries(t), "fakeacp"), "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")}
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"gemini"}}) // all machines, as the app's default
	ctx := context.Background()
	home, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// The work Mac: the app's hermeshd, nobody's yet, with no signer (it
	// never signs anything).
	work, _ := w.otherMac("work-mac", env)
	join, err := work.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: home.RelayUrl, OwnerId: home.OwnerId, RootLabel: "Work Mac", RootPlatform: "macos", MachineOnly: true})
	if err != nil {
		t.Fatalf("request join: %v", err)
	}
	// It reaches the trusted device as a machine's request, with the same code.
	var asked *localv1.PairingDetails
	eventually(t, 15*time.Second, "the request on the trusted device", func() bool {
		res, err := w.app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		for _, p := range res.GetPairings() {
			if p.Code == join.Code {
				asked = p
			}
		}
		return err == nil && asked != nil
	})
	if asked.Kind != identityv1.PairingKind_PAIRING_KIND_MACHINE || asked.VerificationCode != join.VerificationCode || asked.Label != "Work Mac" {
		t.Fatalf("the trusted device shows %+v; the work Mac asked %+v", asked, join)
	}
	// The signer is asked to show the two digits of the cert it signs.
	w.app.mu.Lock()
	w.app.check = func(r *localv1.SigningRequest) error {
		if r.Type == "hermesh.identity.v1.MachineCert" && !r.ShowMatch {
			return errors.New("the signer was not asked to show the digits")
		}
		return nil
	}
	w.app.mu.Unlock()
	approval, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: join.Code})
	if err != nil || len(approval.MatchCode) != 2 {
		t.Fatalf("approve: %v %v", approval, err)
	}
	w.app.mu.Lock()
	w.app.check = nil
	w.app.mu.Unlock()
	var waited *localv1.WaitJoinResponse
	eventually(t, time.Minute, "the approval reaches the work Mac", func() bool {
		waited, err = work.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && waited.Done
	})
	if !waited.MatchRequired || waited.ApprovedBy != "test-mac" {
		t.Fatalf("wait: %+v", waited)
	}
	if st, _ := work.GetStatus(ctx, &localv1.GetStatusRequest{}); st.GetMachineOnly() {
		t.Fatal("the work Mac kept the owner before the person typed the digits")
	}
	wrong := "00"
	if approval.MatchCode == wrong {
		wrong = "01"
	}
	if _, err := work.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: wrong}); err == nil {
		t.Fatal("a wrong number was accepted")
	}
	if _, err := work.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: approval.MatchCode}); err != nil {
		t.Fatal(err)
	}
	st, err := work.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !st.MachineOnly || st.Initialized || st.OwnerId != home.OwnerId || st.LocalMachineName != "work-mac" || st.DeviceKeyId != "" {
		t.Fatalf("work Mac status: %v %v", st, err)
	}
	eventually(t, 30*time.Second, "the owner lists the work Mac online", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineId == st.LocalMachineId {
				return err == nil && m.Online && m.MachineName == "work-mac" && m.DeviceKeyId == ""
			}
		}
		return false
	})

	// Its own app: this Mac only, a project added here, nothing to approve.
	repo := gitRepo(t, map[string]string{"README.md": "# billing\n"})
	if _, err := work.AddProject(ctx, &localv1.AddProjectRequest{MachineId: st.LocalMachineId, ProjectId: "billing", Path: repo}); err != nil {
		t.Fatalf("add a project on the work Mac: %v", err)
	}
	ms, err := work.ListMachines(ctx, &localv1.ListMachinesRequest{})
	if err != nil || len(ms.Machines) != 1 || !ms.Machines[0].Local || !ms.Machines[0].Online || ms.Machines[0].Status == nil {
		t.Fatalf("the work Mac's machines: %v %v", ms, err)
	}
	if ps, err := work.ListProjects(ctx, &localv1.ListProjectsRequest{}); err != nil || len(ps.Projects) != 1 || ps.Projects[0].ProjectId != "billing" {
		t.Fatalf("the work Mac's projects: %v %v", ps, err)
	}
	if _, err := work.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a machine only lists pairing requests: %v", err)
	}
	watch, err := work.WatchSessions(ctx, &localv1.WatchSessionsRequest{})
	if err != nil || !watch.Receive() || !watch.Msg().MachinesChanged {
		t.Fatalf("watch sessions: %v", err)
	}
	defer watch.Close()

	// The agent, granted all machines, runs a session there.
	eventually(t, time.Minute, "the agent sees the work Mac's project", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "work-mac/billing")
	})
	r := w.cli(2*time.Minute, "session", "start", "--project", "work-mac/billing", "--agent", "gemini", "--wait", "-o", "json", "--prompt", "ask 选哪个颜色|红,蓝")
	s := firstSession(r.stdout)
	if s.SessionID == "" {
		t.Fatalf("start on the work Mac: %+v", r)
	}
	var q *sessionv1.Question
	eventually(t, time.Minute, "the work Mac's app sees the session and its question", func() bool {
		res, err := work.ListSessions(ctx, &localv1.ListSessionsRequest{})
		for _, x := range res.GetSessions() {
			if x.SessionId == s.SessionID {
				q = x.Progress.GetPendingQuestion()
			}
		}
		return err == nil && q != nil
	})
	if !watch.Receive() || len(watch.Msg().Sessions) == 0 {
		t.Fatal("the watch did not report the session")
	}
	// It knows the agent by the name its grant gave it.
	if cs, err := work.ListClients(ctx, &localv1.ListClientsRequest{}); err != nil || len(cs.Clients) != 1 || cs.Clients[0].Label != "bot" {
		t.Fatalf("the agents the work Mac knows: %v %v", cs, err)
	}
	// Its own sessions are its to settle, as on a trusted device: it answers…
	if _, err := work.AnswerQuestion(ctx, &localv1.AnswerQuestionRequest{SessionId: s.SessionID, QuestionId: q.QuestionId,
		Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"红"}}}}); err != nil {
		t.Fatalf("answer on the work Mac: %v", err)
	}
	eventually(t, time.Minute, "the turn finishes with the answer", func() bool {
		g := w.get(s.SessionID)
		return g.Status == "idle" && strings.Contains(g.Result, "answer: v-红")
	})
	// …and stops.
	long := firstSession(w.cli(2*time.Minute, "session", "start", "--project", "work-mac/billing", "--agent", "gemini", "-o", "json", "--prompt", "shell sleep 1800").stdout)
	eventually(t, time.Minute, "the long session runs", func() bool { return w.get(long.SessionID).Status == "running" })
	if _, err := work.InterruptSession(ctx, &localv1.InterruptSessionRequest{SessionId: long.SessionID}); err != nil {
		t.Fatalf("stop the session on the work Mac: %v", err)
	}
	eventually(t, time.Minute, "the session stopped", func() bool { return w.get(long.SessionID).Status == "idle" })

	// The owner removes it: the work Mac says so, and leaves.
	if _, err := w.app.client.RemoveMachine(ctx, &localv1.RemoveMachineRequest{MachineId: st.LocalMachineId}); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Minute, "the work Mac learns it was removed", func() bool {
		st, err := work.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && st.MachineOnly && st.Removed
	})
	if _, err := work.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatal(err)
	}
	if st, err := work.GetStatus(ctx, &localv1.GetStatusRequest{}); err != nil || st.MachineOnly || st.Initialized || st.Removed {
		t.Fatalf("after leaving: %v %v", st, err)
	}
	listed := func(id string) *localv1.Machine {
		res, _ := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineId == id {
				return m
			}
		}
		return nil
	}

	// It joins the same owner again, under a new id (a removed one never
	// comes back); the person typed the connection address, so no digits.
	again := w.joinTyped(work, home, "Work Mac")
	if again.LocalMachineId == st.LocalMachineId {
		t.Fatal("it joined again under the id that was removed")
	}
	eventually(t, 30*time.Second, "it is back online", func() bool { m := listed(again.LocalMachineId); return m != nil && m.Online })
	// It leaves by itself, and so leaves the owner's machines too.
	if _, err := work.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatal(err)
	}
	if listed(again.LocalMachineId) != nil {
		t.Fatal("a Mac that left is still among the owner's machines")
	}

	// A Mac called like one the owner has gets the next free name.
	typed, typedSock := w.otherMac("typed-mac", env)
	typedSt := w.joinTyped(typed, home, "Owner Mac")
	eventually(t, 30*time.Second, "it is named owner-mac-2", func() bool {
		m := listed(typedSt.LocalMachineId)
		return m != nil && m.MachineName == "owner-mac-2"
	})

	// It becomes a trusted device too, as any Mac joins; its machine stays.
	root, _ := identity.GenerateKey()
	newTestApp(t, typedSock, root)
	join, err = typed.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: home.RelayUrl, OwnerId: home.OwnerId, RootPublicKey: rootPub(root), RootLabel: "typed-mac", RootPlatform: "macos"})
	if err != nil {
		t.Fatal(err)
	}
	var device *localv1.ApproveDevicePairingResponse
	eventually(t, 15*time.Second, "its device request on the trusted device", func() bool {
		device, err = w.app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
		return err == nil
	})
	eventually(t, time.Minute, "the device approval reaches it", func() bool {
		waited, err = typed.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && waited.Done
	})
	if _, err := typed.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: device.MatchCode}); err != nil {
		t.Fatal(err)
	}
	up, err := typed.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !up.Initialized || up.MachineOnly || !up.LocalMachineEnabled || up.LocalMachineId != typedSt.LocalMachineId {
		t.Fatalf("after becoming a trusted device: %v %v", up, err)
	}
	if ms, err := typed.ListMachines(ctx, &localv1.ListMachinesRequest{}); err != nil || len(ms.Machines) != 2 {
		t.Fatalf("a trusted device lists the owner's machines: %v %v", ms, err)
	}
	// Its machine is now that device's own: removing the device removes it too.
	eventually(t, 30*time.Second, "its machine is the device's own", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineId == typedSt.LocalMachineId {
				return err == nil && m.DeviceKeyId == up.DeviceKeyId && m.Online
			}
		}
		return false
	})
	if _, err := w.app.client.RemoveDevice(ctx, &localv1.RemoveDeviceRequest{KeyId: up.DeviceKeyId}); err != nil {
		t.Fatal(err)
	}
	ms, err = w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
	if err != nil || slices.ContainsFunc(ms.Machines, func(m *localv1.Machine) bool { return m.MachineId == typedSt.LocalMachineId }) {
		t.Fatalf("the removed device's machine is still listed: %v %v", ms, err)
	}
}

// joinTyped has mac join the owner of home as a machine only, named label,
// with the connection address the person typed (no digits), approved on the
// owner's trusted device.
func (w *ownerMac) joinTyped(mac localv1connect.LocalServiceClient, home *localv1.GetStatusResponse, label string) *localv1.GetStatusResponse {
	t := w.t
	t.Helper()
	ctx := context.Background()
	join, err := mac.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: home.RelayUrl, OwnerId: home.OwnerId, RootLabel: label, MachineOnly: true, OwnerTyped: true})
	if err != nil {
		t.Fatal(err)
	}
	var approval *localv1.ApproveMachinePairingResponse
	eventually(t, 15*time.Second, "its request on the trusted device", func() bool {
		approval, err = w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: join.Code, MachineName: label})
		return err == nil
	})
	if approval.MatchCode != "" {
		t.Fatalf("digits for a Mac that pinned the owner itself: %q", approval.MatchCode)
	}
	var waited *localv1.WaitJoinResponse
	eventually(t, time.Minute, "the approval reaches it", func() bool {
		waited, err = mac.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && waited.Done
	})
	if waited.MatchRequired {
		t.Fatal("digits required of a Mac that pinned the owner itself")
	}
	if _, err := mac.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true}); err != nil {
		t.Fatal(err)
	}
	st, err := mac.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !st.MachineOnly || st.OwnerId != home.OwnerId {
		t.Fatalf("status once joined: %v %v", st, err)
	}
	return st
}

// otherMac starts another Mac's hermeshd (as the app runs it) that belongs
// to nobody yet, and returns its local API and socket.
func (w *ownerMac) otherMac(name string, env []string) (localv1connect.LocalServiceClient, string) {
	w.t.Helper()
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	w.t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	p := start(w.t, "owner "+name, env, filepath.Join(w.bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(w.tmp, name), "--socket", sock)
	w.logs = append(w.logs, p)
	return localClient(w.t, sock), sock
}
