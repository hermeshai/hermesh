package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// coldStartRelay starts a self-hosted relay and returns its URL and claim code.
func coldStartRelay(t *testing.T, relayBin, dir string, logs *[]*proc) (string, string, *proc) {
	t.Helper()
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	p := start(t, "relay", nil, relayBin, "serve", "--data-dir", dir, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	*logs = append(*logs, p)
	eventually(t, 20*time.Second, "relay listening", func() bool { return strings.Contains(p.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", dir).stdout)
	return url, claim, p
}

// TestColdStartSignOutKeepsWorking: a Mac that started without an account,
// signed in, then signed out keeps its machine (docs/cold-start-tech.md
// §2.1): a session waiting for an answer is answered and finishes, while
// the agents outside reach it no more; signing in again needs no new
// signature and the same machine is back on the relay.
func TestColdStartSignOutKeepsWorking(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	daemonBin := filepath.Join(bin, "hermeshd"+exe)
	relayBin := filepath.Join(bin, "hermesh-relay"+exe)
	cliBin := filepath.Join(bin, "hermesh"+exe)
	fakeACP := filepath.Join(bin, "fakeacp"+exe)

	tmp := t.TempDir()
	sockDir, err := os.MkdirTemp(shortTemp, "hmout")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s ====\n%s", p.name, p.output())
			}
		}
	})

	dataDir := filepath.Join(tmp, "mac")
	sock := filepath.Join(sockDir, "o.sock")
	env := []string{"HERMESH_FAKE_PROVIDERS=1", "HERMESH_GEMINI_BIN=" + fakeACP, "FAKE_ACP_STATE=" + filepath.Join(tmp, "fakeacp")}
	mac := start(t, "mac", env, daemonBin, "run", "--owner", "--data-dir", dataDir, "--socket", sock)
	logs = append(logs, mac)
	t.Cleanup(mac.stop)
	ctx := context.Background()

	// The first run: no account, this Mac's agents and a project.
	lc := localClient(t, sock)
	var localID string
	eventually(t, 20*time.Second, "standalone machine", func() bool {
		st, err := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		localID = st.GetLocalMachineId()
		return err == nil && localID != "" && st.GetRelayHealth().GetPhase() == "local"
	})
	repo := gitRepo(t, map[string]string{"README.md": "# Cold start\n"})
	if _, err := lc.AddProject(ctx, &localv1.AddProjectRequest{MachineId: localID, ProjectId: "demo", Path: repo}); err != nil {
		t.Fatal(err)
	}

	// Signed in on the sign-in step: a self-hosted relay, this Mac's machine kept.
	relayURL, claim, _ := coldStartRelay(t, relayBin, filepath.Join(tmp, "relay"), &logs)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayURL, ClaimCode: claim, RootPublicKey: rootPub(root),
		RootLabel: "mac", RootPlatform: "macos", LocalMachineName: "mac"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	eventually(t, 30*time.Second, "mac online on the relay", func() bool { return machineOnlineID(app, localID) })

	// An agent outside, as the person would connect it.
	cliEnv := []string{"HERMESH_HOME=" + filepath.Join(tmp, "agent"), "HERMESH_LINK=off", "HERMESH_LOCAL=off"}
	login := start(t, "cli-login", cliEnv, cliBin, "auth", "login", setup.ConnectAddress, "--name", "agent")
	logs = append(logs, login)
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login.pairingCode(t),
		Spec: &localv1.GrantSpec{ProjectIds: []string{"*"}, Agents: []string{"*"}, MaxRuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS}}); err != nil {
		t.Fatal(err)
	}
	if code := login.wait(t, 30*time.Second); code != 0 {
		t.Fatalf("cli login: %s", login.output())
	}
	agentSees := func() bool {
		r := run(t, cliEnv, 20*time.Second, cliBin, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "mac/demo")
	}
	if !agentSees() {
		t.Fatal("the agent does not see mac/demo")
	}

	// A session that waits for an answer, across signing out.
	s, err := app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{ProjectId: "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "gemini", Model: "gemini-2.5-pro"}, InitialPrompt: "ask Keep going|Yes,No"})
	if err != nil {
		t.Fatal(err)
	}
	sid := s.GetSession().GetSessionId()
	var q *sessionv1.Question
	eventually(t, 30*time.Second, "the session asks", func() bool {
		res, err := app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, x := range res.Sessions {
			if x.SessionId == sid && x.Progress.GetPendingQuestion() != nil {
				q = x.Progress.PendingQuestion
				return true
			}
		}
		return false
	})

	signedBefore := app.signedBatches()
	if _, err := app.client.SignOut(ctx, &localv1.SignOutRequest{}); err != nil {
		t.Fatal(err)
	}
	st, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !st.SignedOut || !st.LocalMachineEnabled || st.LocalMachineId != localID || st.GetRelayHealth().GetPhase() != "local" {
		t.Fatalf("status signed out: %+v %v", st, err)
	}
	if ms, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); err != nil || len(ms.Machines) != 1 || ms.Machines[0].MachineId != localID {
		t.Fatalf("machines signed out: %v %v", ms, err)
	}
	if ps, err := app.client.ListProjects(ctx, &localv1.ListProjectsRequest{}); err != nil || len(ps.Projects) != 1 || ps.Projects[0].ProjectId != "demo" {
		t.Fatalf("projects signed out: %v %v", ps, err)
	}
	eventually(t, 30*time.Second, "the agent outside loses the Mac", func() bool { return !agentSees() })

	// The session goes on here.
	if _, err := app.sessions.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{SessionId: sid, RequestId: q.QuestionId,
		Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		Answers:  map[string]*sessionv1.QuestionAnswer{q.Items[0].Id: {SelectedOptions: []string{"Yes"}}}}); err != nil {
		t.Fatalf("answering signed out: %v", err)
	}
	eventually(t, 30*time.Second, "the session finishes signed out", func() bool {
		g, err := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid})
		return err == nil && g.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	})

	// Signing in again: no signature, the same machine back on the relay.
	if _, err := app.client.SignIn(ctx, &localv1.SignInRequest{}); err != nil {
		t.Fatal(err)
	}
	if n := app.signedBatches(); n != signedBefore {
		t.Fatalf("signing in again asked for %d confirmations", n-signedBefore)
	}
	eventually(t, 30*time.Second, "mac back on the relay", func() bool { return machineOnlineID(app, localID) })
	eventually(t, 30*time.Second, "the agent outside sees the Mac again", agentSees)
}

// TestColdStartJoinSignsOnce: a Mac that runs Her without an account joins
// an owner that already exists with one confirmation, Her's grant in it
// (docs/cold-start-tech.md §2.3); Her keeps its key and its machine its id.
func TestColdStartJoinSignsOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	bin := binaries(t)
	daemonBin := filepath.Join(bin, "hermeshd"+exe)
	relayBin := filepath.Join(bin, "hermesh-relay"+exe)

	tmp := t.TempDir()
	sockDir, err := os.MkdirTemp(shortTemp, "hmjoin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s ====\n%s", p.name, p.output())
			}
		}
	})
	ctx := context.Background()
	relayURL, claim, _ := coldStartRelay(t, relayBin, filepath.Join(tmp, "relay"), &logs)

	// A: the owner's first device.
	sockA := filepath.Join(sockDir, "a.sock")
	macA := start(t, "mac-a", []string{"HERMESH_FAKE_PROVIDERS=1"}, daemonBin, "run", "--owner", "--data-dir", filepath.Join(tmp, "a"), "--socket", sockA)
	logs = append(logs, macA)
	t.Cleanup(macA.stop)
	rootA, _ := identity.GenerateKey()
	appA := newTestApp(t, sockA, rootA)
	setup, err := appA.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayURL, ClaimCode: claim, RootPublicKey: rootPub(rootA),
		RootLabel: "mac-a", RootPlatform: "macos", LocalMachineName: "mac-a"})
	if err != nil {
		t.Fatalf("Setup A: %v", err)
	}

	// B: started without an account, Her on.
	sockB := filepath.Join(sockDir, "b.sock")
	envB := append(herEnv(her, herConsole(t), herHome(t, mock.models), mock.base), "HERMESH_FAKE_PROVIDERS=1")
	macB := start(t, "mac-b", envB, daemonBin, "run", "--owner", "--data-dir", filepath.Join(tmp, "b"), "--socket", sockB)
	logs = append(logs, macB)
	t.Cleanup(macB.stop)
	lcB := localClient(t, sockB)
	var idB string
	eventually(t, 20*time.Second, "B standalone", func() bool {
		st, err := lcB.GetStatus(ctx, &localv1.GetStatusRequest{})
		idB = st.GetLocalMachineId()
		return err == nil && idB != ""
	})
	turnHerOn(t, lcB, idB)
	var herID string
	eventually(t, 30*time.Second, "Her's client on B", func() bool {
		cs, err := lcB.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil || len(cs.Clients) != 1 {
			return false
		}
		herID = cs.Clients[0].ClientId
		return true
	})

	// B joins A: one confirmation for the request, its cert, its machine's and Her's grant.
	rootB, _ := identity.GenerateKey()
	appB := newTestApp(t, sockB, rootB)
	join, err := appB.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: relayURL, OwnerId: setup.OwnerId,
		RootPublicKey: rootPub(rootB), RootLabel: "mac-b", RootPlatform: "macos", LocalMachineName: "mac-b"})
	if err != nil {
		t.Fatalf("RequestJoin: %v", err)
	}
	want := []string{"hermesh.identity.v1.PairingRequest", "hermesh.identity.v1.OwnerDeviceCert", "hermesh.identity.v1.MachineCert", "hermesh.identity.v1.Grant"}
	if got := appB.signedTypes(); !slices.Equal(got, want) || appB.signedBatches() != 1 {
		t.Fatalf("RequestJoin signed %v in %d confirmations, want %v in one", got, appB.signedBatches(), want)
	}
	eventually(t, 15*time.Second, "B's request on A", func() bool {
		res, err := appA.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		return err == nil && slices.ContainsFunc(res.Pairings, func(p *localv1.PairingDetails) bool { return p.Kind == identityv1.PairingKind_PAIRING_KIND_DEVICE })
	})
	approval, err := appA.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "approval reaches B", func() bool {
		r, err := appB.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && r.Done
	})
	if _, err := appB.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: approval.MatchCode}); err != nil {
		t.Fatalf("ConfirmJoin: %v", err)
	}
	if n := appB.signedBatches(); n != 1 {
		t.Fatalf("joining asked for %d confirmations, want 1 (%v)", n, appB.signedTypes())
	}

	// B's machine is on the relay under its id, Her under its key.
	eventually(t, 30*time.Second, "B's machine online on A", func() bool { return machineOnlineID(appA, idB) })
	eventually(t, 30*time.Second, "Her published", func() bool {
		cs, err := appA.client.ListClients(ctx, &localv1.ListClientsRequest{})
		return err == nil && slices.ContainsFunc(cs.Clients, func(c *localv1.Client) bool { return c.ClientId == herID && c.HerMachineId == idB })
	})
}

// machineOnlineID: the relay lists the machine as online.
func machineOnlineID(a *testApp, id string) bool {
	ms, err := a.client.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
	if err != nil {
		return false
	}
	return slices.ContainsFunc(ms.Machines, func(m *localv1.Machine) bool { return m.MachineId == id && m.Online })
}
