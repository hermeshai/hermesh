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

// TestStandaloneColdStartAndLifecycle verifies the full cold-start -> hot-upgrade ->
// smooth-return-to-standalone lifecycle across real hermeshd, hermesh-relay,
// hermesh CLI, and fakeacp binaries:
//  1. Cold start with zero login, zero relay, and zero signer connected.
//  2. Local CLI commands (status, project add, doctor, audit-verify) + SessionService
//     interactive question/answer + QueryWork completely offline.
//  3. Lock-free Setup hot-upgrade while a coding session is actively WAITING_INPUT:
//     single signing batch, zero interruption to the running session, visible over Relay.
//  4. Smooth in-place Leave back to standalone while another session is WAITING_INPUT:
//     relay owner revocation, root key deletion, live machine key rotation, audit chain
//     continuity, and zero interruption to the running session.
func TestStandaloneColdStartAndLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	daemonBin := filepath.Join(bin, "hermeshd"+exe)
	relayBin := filepath.Join(bin, "hermesh-relay"+exe)
	cliBin := filepath.Join(bin, "hermesh"+exe)
	fakeACP := filepath.Join(bin, "fakeacp"+exe)

	tmp := t.TempDir()
	sockDir, err := os.MkdirTemp(shortTemp, "hmsolo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	dataDir := filepath.Join(tmp, "owner-mac")
	sock := filepath.Join(sockDir, "o.sock")
	daemonEnv := []string{
		"HERMESH_FAKE_PROVIDERS=1",
		"HERMESH_GEMINI_BIN=" + fakeACP,
		"FAKE_ACP_STATE=" + filepath.Join(tmp, "fakeacp"),
	}

	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s ====\n%s", p.name, p.output())
			}
		}
	})

	ownerProc := start(t, "owner-standalone", daemonEnv, daemonBin, "run", "--owner", "--data-dir", dataDir, "--socket", sock)
	logs = append(logs, ownerProc)

	// Connect LocalService + SessionService clients WITHOUT starting any signer!
	lc := localClient(t, sock)
	sc := sessionClient(sock)
	ctx := context.Background()

	var initialID string
	eventually(t, 20*time.Second, "standalone local machine ready", func() bool {
		st, sErr := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		if sErr != nil || !st.LocalMachineEnabled || st.LocalMachineId == "" {
			return false
		}
		initialID = st.LocalMachineId
		return !st.Initialized && !st.MachineOnly && !st.GetSigner().GetConnected() && st.GetRelayHealth().GetPhase() == "local"
	})

	// Watch streams standalone snapshot immediately without Relay.
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	watch, err := lc.Watch(wctx, &localv1.WatchRequest{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if !watch.Receive() {
		t.Fatalf("Watch Receive: %v", watch.Err())
	}
	snap := watch.Msg()
	watch.Close()
	wcancel()
	if !snap.Reset_ || snap.GetStatus().GetRelayHealth().GetPhase() != "local" || len(snap.Machines) != 1 || snap.Machines[0].GetTunnel().GetPath() != "local" {
		t.Fatalf("unexpected standalone Watch snapshot: %+v", snap)
	}

	// Local hermeshd CLI subcommands find the embedded standalone profile machine automatically.
	repo := gitRepo(t, map[string]string{"README.md": "# Standalone Demo\n"})
	if r := run(t, nil, 15*time.Second, daemonBin, "project", "add", repo, "--name", "demo", "--data-dir", dataDir); r.code != 0 {
		t.Fatalf("hermeshd project add failed (%d):\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := run(t, nil, 15*time.Second, daemonBin, "status", "--data-dir", dataDir); r.code != 0 || !strings.Contains(r.stdout, initialID) {
		t.Fatalf("hermeshd status failed (%d):\n%s\n%s", r.code, r.stdout, r.stderr)
	}

	projs, err := lc.ListProjects(ctx, &localv1.ListProjectsRequest{})
	if err != nil || len(projs.Projects) != 1 || projs.Projects[0].ProjectId != "demo" {
		t.Fatalf("ListProjects: %+v err=%v", projs, err)
	}

	// Start an interactive fakeacp session completely offline, answer its question, and check QueryWork.
	s1, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId: "demo",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "gemini",
			Model:      "gemini-2.5-pro",
		},
		InitialPrompt: "ask Pick color|Red,Blue",
	})
	if err != nil {
		t.Fatalf("StartSession s1: %v", err)
	}
	s1ID := s1.GetSession().GetSessionId()

	waitQuestion := func(sessID string) *sessionv1.Question {
		var q *sessionv1.Question
		eventually(t, 30*time.Second, "question on "+sessID, func() bool {
			res, lErr := lc.ListSessions(ctx, &localv1.ListSessionsRequest{})
			if lErr != nil {
				return false
			}
			for _, x := range res.Sessions {
				if x.SessionId == sessID && x.Progress.GetPendingQuestion() != nil {
					q = x.Progress.PendingQuestion
					return true
				}
			}
			return false
		})
		return q
	}
	q1 := waitQuestion(s1ID)
	if _, err := sc.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: s1ID,
		RequestId: q1.QuestionId,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		Answers:   map[string]*sessionv1.QuestionAnswer{q1.Items[0].Id: {SelectedOptions: []string{"Red"}}},
	}); err != nil {
		t.Fatalf("RespondRuntimeRequest s1: %v", err)
	}
	eventually(t, 30*time.Second, "s1 idle", func() bool {
		g, gErr := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: s1ID})
		return gErr == nil && g.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	})
	workRes, err := lc.QueryWork(ctx, &localv1.QueryWorkRequest{
		TimeZone: "UTC",
		FirstDay: time.Now().UTC().Format(time.DateOnly),
		Days:     1,
		Bucket:   localv1.WorkBucket_WORK_BUCKET_HOUR,
		GroupBy:  []localv1.WorkDimension{localv1.WorkDimension_WORK_DIMENSION_AGENT},
	})
	if err != nil || len(workRes.Sources) != 1 || !workRes.Sources[0].Live {
		t.Fatalf("QueryWork in standalone: %+v err=%v", workRes, err)
	}

	// 2. Start s2 and leave it WAITING_INPUT while hot-upgrading to an account/relay via Setup!
	s2, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId: "demo",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "gemini",
			Model:      "gemini-2.5-pro",
		},
		InitialPrompt: "ask Upgrade check|KeepGoing,Stop",
	})
	if err != nil {
		t.Fatalf("StartSession s2: %v", err)
	}
	s2ID := s2.GetSession().GetSessionId()
	q2 := waitQuestion(s2ID)

	port := freePort(t)
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	relayProc := start(t, "relay", nil, relayBin, "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relayProc)
	eventually(t, 20*time.Second, "relay listening", func() bool {
		return strings.Contains(relayProc.output(), "relay listening")
	})
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)

	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:         relayURL,
		ClaimCode:        claim,
		RootPublicKey:    rootPub(root),
		RootLabel:        "solo-laptop",
		RootPlatform:     "macos",
		LocalMachineName: "my-mac",
	})
	if err != nil {
		t.Fatalf("Setup hot-upgrade: %v", err)
	}
	wantSigned := []string{"hermesh.identity.v1.RootCert", "hermesh.identity.v1.OwnerDeviceCert", "hermesh.identity.v1.MachineCert"}
	if got := app.signedTypes(); !slices.Equal(got, wantSigned) {
		t.Fatalf("Setup signed %v, want single batch %v", got, wantSigned)
	}

	// Verify machine comes online over Relay with the SAME MachineId, and s2 is uninterrupted!
	eventually(t, 30*time.Second, "my-mac online on relay", func() bool {
		ms, mErr := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return mErr == nil && len(ms.Machines) == 1 && ms.Machines[0].MachineId == initialID && ms.Machines[0].MachineName == "my-mac" && ms.Machines[0].Online
	})
	if _, err := app.sessions.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: s2ID,
		RequestId: q2.QuestionId,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		Answers:   map[string]*sessionv1.QuestionAnswer{q2.Items[0].Id: {SelectedOptions: []string{"KeepGoing"}}},
	}); err != nil {
		t.Fatalf("RespondRuntimeRequest s2 after Setup: %v", err)
	}
	eventually(t, 30*time.Second, "s2 idle after Setup", func() bool {
		g, gErr := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: s2ID})
		return gErr == nil && g.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
			strings.Contains(g.GetProjection().GetSession().GetProgress().GetLastMessageExcerpt(), "KeepGoing")
	})

	// Pair an external CLI agent to verify Relay visibility, and verify Leave revokes it later.
	cliHome := filepath.Join(tmp, "ext-cli")
	cliEnv := []string{"HERMESH_HOME=" + cliHome, "HERMESH_LINK=off"}
	loginProc := start(t, "cli-login", cliEnv, cliBin, "auth", "login", setup.ConnectAddress, "--name", "ext-bot")
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{
		Code: loginProc.pairingCode(t),
		Spec: &localv1.GrantSpec{ProjectIds: []string{"*"}, Agents: []string{"*"}, MaxRuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS},
	}); err != nil {
		t.Fatalf("ApproveClientPairing: %v", err)
	}
	if code := loginProc.wait(t, 30*time.Second); code != 0 {
		t.Fatalf("cli login failed: %s", loginProc.output())
	}
	if r := run(t, cliEnv, 20*time.Second, cliBin, "project", "list"); r.code != 0 || !strings.Contains(r.stdout, "my-mac/demo") {
		t.Fatalf("external cli project list failed (%d):\n%s\n%s", r.code, r.stdout, r.stderr)
	}

	// 3. Start s3 and leave it WAITING_INPUT while smoothly returning to standalone via Leave!
	s3, err := app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId: "demo",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "gemini",
			Model:      "gemini-2.5-pro",
		},
		InitialPrompt: "ask Downgrade check|StayAlive,Abort",
	})
	if err != nil {
		t.Fatalf("StartSession s3: %v", err)
	}
	s3ID := s3.GetSession().GetSessionId()
	q3 := waitQuestion(s3ID)

	if _, err := app.client.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatalf("Leave back to standalone: %v", err)
	}

	leftSt, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if leftSt.Initialized || !leftSt.LocalMachineEnabled || leftSt.GetRelayHealth().GetPhase() != "local" {
		t.Fatalf("unexpected status after Leave: %+v", leftSt)
	}
	rotatedID := leftSt.LocalMachineId
	if rotatedID == "" || rotatedID == initialID {
		t.Fatalf("expected rotated LocalMachineId after Leave, got %q (was %q)", rotatedID, initialID)
	}
	if got := app.deletedKeys(); !slices.Equal(got, []string{identity.KeyID(root.Public())}) {
		t.Fatalf("expected root key deleted on Leave, got %v", got)
	}
	// External CLI's owner was erased on the relay when the sole trusted device left.
	if r := run(t, cliEnv, 20*time.Second, cliBin, "project", "list"); r.code == 0 {
		t.Fatalf("expected external cli to be refused after solo-device Leave, got stdout:\n%s", r.stdout)
	}

	// Active session s3 was NOT interrupted by Leave; answer it in standalone mode!
	if _, err := app.sessions.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: s3ID,
		RequestId: q3.QuestionId,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		Answers:   map[string]*sessionv1.QuestionAnswer{q3.Items[0].Id: {SelectedOptions: []string{"StayAlive"}}},
	}); err != nil {
		t.Fatalf("RespondRuntimeRequest s3 after Leave: %v", err)
	}
	eventually(t, 30*time.Second, "s3 idle after Leave", func() bool {
		g, gErr := app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: s3ID})
		return gErr == nil && g.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
			g.GetProjection().GetSession().GetMachineId() == rotatedID
	})

	// Audit log verifies cleanly across initial key -> retired checkpoint -> rotated key!
	ownerProc.stop()
	if r := run(t, nil, 15*time.Second, daemonBin, "audit-verify", "--data-dir", dataDir); r.code != 0 {
		t.Fatalf("hermeshd audit-verify failed after Leave (%d):\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}

// TestStandaloneHerFullLifecycle verifies Her in zero-login standalone mode,
// Her orchestrating local coding agents + receiving session_news in-process,
// single-batch Setup upgrade (bundling HerGrant), Relay-down local Her
// resilience while logged in, and seamless Her continuity across Leave.
func TestStandaloneHerFullLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	bin := binaries(t)
	daemonBin := filepath.Join(bin, "hermeshd"+exe)
	relayBin := filepath.Join(bin, "hermesh-relay"+exe)

	tmp := t.TempDir()
	sockDir, err := os.MkdirTemp(shortTemp, "hmher")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	dataDir := filepath.Join(tmp, "owner-mac")
	sock := filepath.Join(sockDir, "o.sock")
	home := herHome(t, mock.models)
	console := herConsole(t)
	daemonEnv := append(herEnv(her, console, home, mock.base), "HERMESH_FAKE_PROVIDERS=1")

	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s ====\n%s", p.name, p.output())
			}
		}
	})

	ownerProc := start(t, "owner-her-standalone", daemonEnv, daemonBin, "run", "--owner", "--data-dir", dataDir, "--socket", sock)
	logs = append(logs, ownerProc)
	t.Cleanup(ownerProc.stop)

	lc := localClient(t, sock)
	ctx := context.Background()

	var initialID string
	eventually(t, 20*time.Second, "standalone machine up", func() bool {
		st, sErr := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		if sErr != nil || !st.LocalMachineEnabled || st.LocalMachineId == "" {
			return false
		}
		initialID = st.LocalMachineId
		return st.GetRelayHealth().GetPhase() == "local"
	})
	if _, err := lc.RenameMachine(ctx, &localv1.RenameMachineRequest{MachineId: initialID, MachineName: "solo-mac"}); err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t, map[string]string{"README.md": "# Her Local\n"})
	if _, err := lc.AddProject(ctx, &localv1.AddProjectRequest{MachineId: initialID, ProjectId: "demo", Path: repo}); err != nil {
		t.Fatal(err)
	}

	// 1. Enable Her in standalone mode WITHOUT any signer or relay!
	turnHerOn(t, lc, initialID)
	eventually(t, 30*time.Second, "Her local mesh ready", func() bool {
		m := listedMachine(t, lc, initialID)
		return m != nil && m.GetHer().GetMesh()
	})
	clients1, err := lc.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients1.Clients) != 1 || clients1.Clients[0].GrantId != "g_local_her" {
		t.Fatalf("standalone ListClients: %+v err=%v", clients1, err)
	}
	herClientID := clients1.Clients[0].ClientId

	bot := defaultBot(t, lc, initialID)
	talk(t, lc, initialID, bot.ID, "hello completely offline")

	env := &herEnvironment{c: lc, machine: initialID, bot: bot}
	asked, said := tellHer(t, env, meshCall("session_start", map[string]any{"project": "solo-mac/demo", "prompt": "sleep 3s\nsay built offline"}), "approve_for_session", "mesh ")
	if len(asked) != 1 || !strings.HasPrefix(said, "mesh done") {
		t.Fatalf("standalone Her session_start: asked=%+v said=%s", asked, said)
	}
	firstSess := sessionID.FindString(said)
	herSaid(t, env, "offline session_news turn_done", `<session_news`, `kind="turn_done"`, firstSess)

	// 2. Hot-upgrade via Setup: HerGrant is bundled into the SINGLE signing batch!
	port := freePort(t)
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	relayProc := start(t, "relay", nil, relayBin, "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relayProc)
	eventually(t, 20*time.Second, "relay up", func() bool {
		return strings.Contains(relayProc.output(), "relay listening")
	})
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)

	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	if _, err := app.client.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:         relayURL,
		ClaimCode:        claim,
		RootPublicKey:    rootPub(root),
		RootLabel:        "solo-laptop",
		RootPlatform:     "macos",
		LocalMachineName: "solo-mac",
	}); err != nil {
		t.Fatalf("Setup with Her enabled: %v", err)
	}
	wantBatch := []string{
		"hermesh.identity.v1.RootCert",
		"hermesh.identity.v1.OwnerDeviceCert",
		"hermesh.identity.v1.MachineCert",
		"hermesh.identity.v1.Grant",
	}
	if got := app.signedTypes(); !slices.Equal(got, wantBatch) {
		t.Fatalf("Setup signed %v, want single 4-item batch %v", got, wantBatch)
	}
	env.c = app.client
	var upgradedHer *localv1.Client
	eventually(t, 30*time.Second, "Her published on relay with same ClientId", func() bool {
		upgradedHer = herClient(t, env)
		return upgradedHer != nil && upgradedHer.GrantId != "g_local_her" && listedMachine(t, app.client, initialID).GetHer().GetMesh()
	})
	if upgradedHer.ClientId != herClientID {
		t.Fatalf("Her ClientId changed across Setup: got %s, want %s", upgradedHer.ClientId, herClientID)
	}

	// Stop the Relay while logged in: Her STILL orchestrates solo-mac locally via InProcess fallback!
	relayProc.stop()
	_, said = tellHer(t, env, meshCall("session_start", map[string]any{"project": "solo-mac/demo", "prompt": "sleep 3s\nsay built with relay down"}), "approve", "mesh ")
	if !strings.HasPrefix(said, "mesh done") {
		t.Fatalf("Her session_start with relay down: %s", said)
	}
	herSaid(t, env, "relay-down session_news", `kind="turn_done"`, sessionID.FindString(said))

	// 3. Leave with Relay still down (solo device probe times out -> clean local downgrade) -> Her keeps working!
	if _, err := app.client.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatalf("Leave with relay down: %v", err)
	}
	leftSt, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || leftSt.Initialized || !leftSt.LocalMachineEnabled || leftSt.LocalMachineId == initialID {
		t.Fatalf("status after Leave: %+v err=%v", leftSt, err)
	}
	env.machine = leftSt.LocalMachineId
	eventually(t, 30*time.Second, "Her mesh ready under rotated MachineId", func() bool {
		m := listedMachine(t, app.client, env.machine)
		return m != nil && m.GetHer().GetRunning() && m.GetHer().GetMesh()
	})
	_, said = tellHer(t, env, meshCall("session_start", map[string]any{"project": "solo-mac/demo", "prompt": "sleep 3s\nsay built after leave"}), "approve", "mesh ")
	if !strings.HasPrefix(said, "mesh done") {
		t.Fatalf("Her session_start after Leave: %s", said)
	}
	herSaid(t, env, "post-Leave session_news", `kind="turn_done"`, sessionID.FindString(said))
}
