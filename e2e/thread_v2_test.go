package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestThreadV2EndToEnd exercises the unified Hermesh session architecture across
// real compiled binaries (hermesh-relay + Owner hermeshd + Remote Machine hermeshd +
// hermesh CLI):
//  1. Native launch, continuation, live model/provider handoff, Git diff & file rollback
//  2. Cryptographic Grant max_runtime_mode clamping, external self-approval denial,
//     and seamless cross-actor continuity (external agent starts -> Owner dispatches -> agent reads)
//  3. Plan -> Approve -> Code flow, mid-turn Steer, and Queue management
//  4. Durable Offline Outbox queuing while remote machine is down + automatic FIFO replay on reconnect
func TestThreadV2EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	binTmp := t.TempDir()
	relayBin := filepath.Join(binTmp, "hermesh-relay"+exe)
	daemonBin := filepath.Join(binTmp, "hermeshd"+exe)
	cliBin := filepath.Join(binTmp, "hermesh"+exe)
	for dst, pkg := range map[string]string{
		relayBin:  "./cmd/hermesh-relay",
		daemonBin: "./cmd/hermeshd",
		cliBin:    "./cmd/hermesh",
	} {
		r := run(t, nil, 60*time.Second, "go", "build", "-o", dst, pkg)
		if r.code != 0 {
			t.Fatalf("build %s failed:\n%s\n%s", pkg, r.stdout, r.stderr)
		}
	}

	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp(shortTemp, "hmv2")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	agentEnv := []string{"HERMESH_FAKE_PROVIDERS=1"}
	ctx := context.Background()

	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s ====\n%s", p.name, p.output())
			}
		}
	})

	// 1. Start Relay + Owner hermeshd + Remote VPS hermeshd
	port := freePort(t)
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, relayBin, "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relay)
	eventually(t, 20*time.Second, "relay listening", func() bool {
		return strings.Contains(relay.output(), "relay listening")
	})
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)

	ownerData := filepath.Join(tmp, "owner")
	sock := filepath.Join(sockDir, "o.sock")
	ownerProc := start(t, "owner", agentEnv, daemonBin, "run", "--owner", "--data-dir", ownerData, "--socket", sock)
	logs = append(logs, ownerProc)

	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:      relayURL,
		ClaimCode:     claim,
		RootPublicKey: rootPub(root),
		RootLabel:     "owner-laptop",
		RootPlatform:  "macos",
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	localMac, err := app.client.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: "owner-mac"})
	if err != nil {
		t.Fatalf("SetLocalMachine: %v", err)
	}

	vpsData := filepath.Join(tmp, "vps-1")
	pairProc := start(t, "pair-vps", agentEnv, daemonBin, "pair", setup.ConnectAddress, "--name", "vps-1", "--no-service", "--data-dir", vpsData)
	pairCode := pairProc.pairingCode(t)
	if _, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pairCode, MachineName: "vps-1"}); err != nil {
		t.Fatalf("ApproveMachinePairing: %v", err)
	}
	if code := pairProc.wait(t, 30*time.Second); code != 0 {
		t.Fatalf("pair exited %d", code)
	}
	startVPS := func() *proc {
		p := start(t, "vps-1", agentEnv, daemonBin, "run", "--data-dir", vpsData)
		logs = append(logs, p)
		return p
	}
	vpsProc := startVPS()

	var vpsID string
	eventually(t, 30*time.Second, "both machines online", func() bool {
		ms, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		online := 0
		for _, m := range ms.Machines {
			if m.Online {
				online++
			}
			if m.MachineName == "vps-1" {
				vpsID = m.MachineId
			}
		}
		return online == 2 && vpsID != ""
	})

	// Add Git projects on both machines.
	vpsRepo := gitRepo(t, map[string]string{"README.md": "# VPS Repo\n"})
	macRepo := gitRepo(t, map[string]string{"README.md": "# Mac Repo\n"})
	if _, err := app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: vpsRepo}); err != nil {
		t.Fatalf("AddProject vps: %v", err)
	}
	if _, err := app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: localMac.MachineId, ProjectId: "localdemo", Path: macRepo, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE}); err != nil {
		t.Fatalf("AddProject mac: %v", err)
	}

	sc := app.sessions

	// 2. Owner native launch on remote VPS worktree + cross-provider handoff + checkpoint rollback!
	provRes, err := sc.ListProviders(ctx, &sessionv1.ListProvidersRequest{MachineId: vpsID})
	if err != nil || len(provRes.Providers) == 0 {
		t.Fatalf("ListProviders on vps: err=%v count=%d", err, len(provRes.GetProviders()))
	}

	launchRes, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId: vpsID,
		ProjectId: "demo",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "claude",
			Model:      "claude-sonnet-4-6",
		},
		InitialPrompt: "sleep 2s\nwrite vps_hello.txt|hello_v1\nsay turn_1_complete",
	})
	if err != nil {
		t.Fatalf("StartSession on vps: %v", err)
	}
	vpsSessionID := launchRes.Session.SessionId
	// Unified WatchSessions hears the remote turn finish by itself, with nobody
	// reading the session: the owner follows each machine's sessions.
	listCtx, stopList := context.WithTimeout(ctx, 20*time.Second)
	list, err := sc.WatchSessions(listCtx, &sessionv1.WatchSessionsRequest{})
	if err != nil {
		t.Fatalf("WatchSessions: %v", err)
	}
	heardIdle := false
	for !heardIdle && list.Receive() {
		for _, s := range list.Msg().Sessions {
			heardIdle = heardIdle || s.SessionId == vpsSessionID && s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE
		}
	}
	list.Close()
	stopList()
	if !heardIdle {
		t.Fatalf("the session list never heard vps's turn finish: %v", list.Err())
	}
	eventually(t, 15*time.Second, "vps turn 1 idle", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: vpsSessionID})
		return err == nil && g.Projection.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	})

	// Switch provider from claude -> codex on Turn 2 and edit a 2nd file!
	_, err = sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		SessionId: vpsSessionID,
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "codex",
			Model:      "gpt-5.4",
		},
		Text: "write vps_second.txt|second_v1\ninspect",
	})
	if err != nil {
		t.Fatalf("DispatchMessage turn 2: %v", err)
	}
	var turn2Proj *sessionv1.SessionProjection
	eventually(t, 15*time.Second, "vps turn 2 idle", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: vpsSessionID})
		if err == nil && g.Projection.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
			turn2Proj = g.Projection
			return true
		}
		return false
	})
	var sawHandoff bool
	for _, it := range turn2Proj.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_HANDOFF && strings.Contains(it.Text, "vps_hello.txt") {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("expected cross-provider HANDOFF item on remote VPS session")
	}

	// Verify GetDiff shows both files, then RollbackCheckpoint to Run #1!
	diffBefore, err := sc.GetDiff(ctx, &sessionv1.GetDiffRequest{SessionId: vpsSessionID})
	if err != nil || len(diffBefore.Files) != 2 {
		t.Fatalf("expected 2 changed files before rollback, got %d (err=%v)", len(diffBefore.GetFiles()), err)
	}
	ckpts, err := sc.ListCheckpoints(ctx, &sessionv1.ListCheckpointsRequest{SessionId: vpsSessionID})
	if err != nil {
		t.Fatal(err)
	}
	var cp1ID string
	for _, c := range ckpts.Checkpoints {
		if c.RunOrdinal == 1 {
			cp1ID = c.CheckpointId
		}
	}
	if _, err := sc.RollbackCheckpoint(ctx, &sessionv1.RollbackCheckpointRequest{
		SessionId:    vpsSessionID,
		CheckpointId: cp1ID,
		RestoreFiles: true,
	}); err != nil {
		t.Fatalf("RollbackCheckpoint: %v", err)
	}
	diffAfter, err := sc.GetDiff(ctx, &sessionv1.GetDiffRequest{SessionId: vpsSessionID})
	if err != nil || len(diffAfter.Files) != 1 || diffAfter.Files[0].Path != "vps_hello.txt" {
		t.Fatalf("expected only vps_hello.txt after file rollback, got %+v (err=%v)", diffAfter.GetFiles(), err)
	}

	// 3. Pair External CLI Client with Grant Ceiling `AUTO_EDITS` and verify cryptographic enforcement + cross-actor continuity!
	botHome := filepath.Join(tmp, "bot-cli")
	botEnv := []string{"HERMESH_HOME=" + botHome, "HERMESH_LINK=off"}
	loginProc := start(t, "bot-login", botEnv, cliBin, "auth", "login", setup.ConnectAddress, "--name", "ci-bot")
	botCode := loginProc.pairingCode(t)
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{
		Code: botCode,
		Spec: &localv1.GrantSpec{
			ProjectIds:     []string{"demo"},
			Agents:         []string{"claude"},
			MaxRuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
		},
	}); err != nil {
		t.Fatalf("ApproveClientPairing with max_runtime_mode: %v", err)
	}
	if code := loginProc.wait(t, 30*time.Second); code != 0 {
		t.Fatalf("bot login exited %d:\n%s", code, loginProc.output())
	}

	// Bot starts a session via `hermesh session start` requesting `full_access` -> clamped to `AUTO_EDITS`!
	// Its shell command pauses on `WAITING_INPUT` (exit code 8 for pending runtime request).
	startOut := run(t, botEnv, 30*time.Second, cliBin, "session", "start",
		"--project", "vps-1/demo",
		"--agent", "claude",
		"--runtime-mode", "full_access",
		"--prompt", "write allowed.txt|ok\nshell npm publish|published",
		"--wait",
		"-o", "json",
	)
	if startOut.code != 0 && startOut.code != 8 {
		t.Fatalf("bot session start failed (%d):\nstdout:%s\nstderr:%s", startOut.code, startOut.stdout, startOut.stderr)
	}
	doc := jsonDoc(t, startOut)
	sMap := doc["session"].(map[string]any)
	botSessionID := sMap["session_id"].(string)

	// Verify effective_runtime_mode was clamped to AUTO_EDITS and status is WAITING_INPUT!
	botProjRes, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: botSessionID})
	if err != nil {
		t.Fatalf("GetSession botSession: %v", err)
	}
	if botProjRes.Projection.Session.EffectiveRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("EffectiveRuntimeMode = %v, want AUTO_EDITS", botProjRes.Projection.Session.EffectiveRuntimeMode)
	}
	if botProjRes.Projection.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
		t.Fatalf("bot session status = %v, want WAITING_INPUT", botProjRes.Projection.Session.Status)
	}
	pendingReqID := botProjRes.Projection.Session.PendingRequestId

	// Bot tries to self-approve its own blocked shell command -> MUST BE DENIED (exit code 3)!
	selfApprove := run(t, botEnv, 20*time.Second, cliBin, "session", "respond", botSessionID, pendingReqID, "--decision", "once")
	if selfApprove.code != 3 {
		t.Fatalf("SECURITY FAILURE: external bot self-approval returned code %d (want 3 / denied):\n%s\n%s", selfApprove.code, selfApprove.stdout, selfApprove.stderr)
	}

	// Owner declines the shell command with feedback via CLI `--socket` -> session finishes cleanly!
	ownerDecline := run(t, nil, 20*time.Second, cliBin, "session", "respond", botSessionID, pendingReqID,
		"--socket", sock,
		"--decision", "decline",
		"--feedback", "do not publish packages",
	)
	if ownerDecline.code != 0 {
		t.Fatalf("owner decline failed (%d): %s %s", ownerDecline.code, ownerDecline.stdout, ownerDecline.stderr)
	}
	eventually(t, 15*time.Second, "bot session settled after owner decline", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: botSessionID})
		return err == nil && g.Projection.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE
	})

	// Cross-actor continuity: Owner dispatches a follow-up turn on ci-bot's session,
	// and ci-bot inspects the updated session and unified diff!
	if _, err := sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		SessionId: botSessionID,
		Text:      "write owner_guided.txt|owner_steered\nsay owner_turn_done",
	}); err != nil {
		t.Fatalf("owner DispatchMessage on botSession: %v", err)
	}
	eventually(t, 15*time.Second, "owner turn on botSession completed", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: botSessionID})
		return err == nil && g.Projection.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
			g.Projection.GetSession().GetProgress().GetTurnCount() == 2
	})
	botDiff := run(t, botEnv, 20*time.Second, cliBin, "session", "diff", botSessionID)
	if botDiff.code != 0 || !strings.Contains(botDiff.stdout, "allowed.txt") || !strings.Contains(botDiff.stdout, "owner_guided.txt") {
		t.Fatalf("ci-bot reading unified diff after owner turn: %+v", botDiff)
	}

	// 3b. Post-turn RESUME_TURN Plan Review (Antigravity ArtifactMetadata.RequestFeedback / Codex plan):
	// Launch an Antigravity session in PLAN mode on remote vps-1 -> turn completes and parks on WAITING_INPUT
	// with ActiveRunId=="" -> Owner approves via CLI `--approve-plan` -> switches to CODE and runs continuation!
	planLaunch, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId:       vpsID,
		ProjectId:       "demo",
		InteractionMode: sessionv1.InteractionMode_INTERACTION_MODE_PLAN,
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "antigravity",
			Model:      "gemini-3.1-pro",
		},
		InitialPrompt: "propose-artifact Refactor Plan|true|# Refactor Plan\\n\\n1. Unify adapter\nsay Ready, click Proceed.",
	})
	if err != nil {
		t.Fatalf("StartSession antigravity plan: %v", err)
	}
	planSessionID := planLaunch.Session.SessionId
	var planProj *sessionv1.SessionProjection
	eventually(t, 15*time.Second, "antigravity session parked on post-turn WAITING_INPUT", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: planSessionID})
		if err == nil && g.Projection.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
			planProj = g.Projection
			return true
		}
		return false
	})
	if planProj.Session.ActiveRunId != "" || planProj.Session.PendingRequestId == "" {
		t.Fatalf("expected post-turn plan review ActiveRunId=='' and non-empty PendingRequestId, got active=%q pending=%q",
			planProj.Session.ActiveRunId, planProj.Session.PendingRequestId)
	}
	approvePlanCLI := run(t, nil, 20*time.Second, cliBin, "session", "respond", planSessionID, planProj.Session.PendingRequestId,
		"--socket", sock,
		"--approve-plan",
	)
	if approvePlanCLI.code != 0 {
		t.Fatalf("hermesh session respond --approve-plan failed (%d):\n%s\n%s", approvePlanCLI.code, approvePlanCLI.stdout, approvePlanCLI.stderr)
	}
	eventually(t, 15*time.Second, "antigravity session switched to CODE and completed continuation turn", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: planSessionID})
		if err != nil {
			return false
		}
		s := g.Projection.GetSession()
		return s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE &&
			s.GetInteractionMode() == sessionv1.InteractionMode_INTERACTION_MODE_CODE &&
			s.GetProgress().GetTurnCount() == 2
	})

	// 4. Durable Offline Outbox: stop vps-1, launch session + queue follow-up while offline, restart vps-1 -> auto-flushes FIFO!
	vpsProc.stop()
	eventually(t, 15*time.Second, "vps-1 marked offline", func() bool {
		ms, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		for _, m := range ms.Machines {
			if m.MachineId == vpsID && !m.Online {
				return true
			}
		}
		return false
	})

	offLaunch, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId:     vpsID,
		ProjectId:     "demo",
		Title:         "Offline Queued Session",
		InitialPrompt: "say offline_run_1",
	})
	if err != nil || !offLaunch.QueuedOffline {
		t.Fatalf("expected QueuedOffline=true while vps-1 is stopped, got res=%+v err=%v", offLaunch, err)
	}
	offSend, err := sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		SessionId: offLaunch.Session.SessionId,
		Text:      "say offline_run_2",
	})
	if err != nil || !offSend.QueuedOffline {
		t.Fatalf("expected follow-up DispatchMessage to queue offline, got res=%+v err=%v", offSend, err)
	}
	// A launch the machine will reject (unknown project) plus its follow-up must not vanish silently.
	doomed, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId:     vpsID,
		ProjectId:     "no-such-project",
		Title:         "Rejected Offline Session",
		InitialPrompt: "say never_runs",
	})
	if err != nil || !doomed.QueuedOffline {
		t.Fatalf("expected rejected-later launch to queue offline, got res=%+v err=%v", doomed, err)
	}
	if _, err := sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{SessionId: doomed.Session.SessionId, Text: "say never_runs_2"}); err != nil {
		t.Fatalf("queue follow-up on rejected-later session: %v", err)
	}
	outboxBefore, err := sc.ListOutbox(ctx, &sessionv1.ListOutboxRequest{MachineId: vpsID})
	if err != nil || len(outboxBefore.Entries) != 4 {
		t.Fatalf("expected 4 entries in outbox while offline, got %d (err=%v)", len(outboxBefore.GetEntries()), err)
	}

	// Bring vps-1 back online -> Outbox flushes both StartSession and DispatchMessage automatically!
	vpsProc = startVPS()
	_ = vpsProc
	eventually(t, 30*time.Second, "outbox drained and offline session completed 2 runs on vps-1", func() bool {
		ob, err := sc.ListOutbox(ctx, &sessionv1.ListOutboxRequest{MachineId: vpsID})
		if err != nil || len(ob.Entries) != 0 {
			return false
		}
		ls, err := sc.ListSessions(ctx, &sessionv1.ListSessionsRequest{MachineId: vpsID})
		if err != nil {
			return false
		}
		for _, s := range ls.Sessions {
			if s.Title == "Offline Queued Session" && s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && s.GetProgress().GetTurnCount() == 2 {
				return true
			}
		}
		return false
	})

	// The rejected launch stays visible as a crashed local session carrying the machine's reason.
	eventually(t, 15*time.Second, "rejected offline launch surfaced as crashed session", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: doomed.Session.SessionId})
		if err != nil {
			return false
		}
		s := g.Projection.GetSession()
		return s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_CRASHED && strings.Contains(s.GetError(), "no-such-project")
	})
	if _, err := sc.DeleteSession(ctx, &sessionv1.DeleteSessionRequest{SessionId: doomed.Session.SessionId}); err != nil {
		t.Fatalf("delete rejected offline session: %v", err)
	}
	ls, err := sc.ListSessions(ctx, &sessionv1.ListSessionsRequest{MachineId: vpsID})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ls.Sessions {
		if s.SessionId == doomed.Session.SessionId {
			t.Fatalf("rejected offline session still listed after delete")
		}
	}

	// 5. Verify multi-session & multi-run assistant reply isolation end-to-end:
	// Earlier vpsSessionID AND the 2-run "Offline Queued Session" on the same daemon must each
	// retain all of their own assistant messages without msg_1 collisions across sessions or runs!
	var offlineRealID string
	for _, s := range ls.Sessions {
		if s.Title == "Offline Queued Session" {
			offlineRealID = s.SessionId
		}
	}
	if offlineRealID == "" {
		t.Fatalf("Offline Queued Session not found on vps-1")
	}
	offProj, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: offlineRealID})
	if err != nil {
		t.Fatalf("GetSession offlineRealID: %v", err)
	}
	var offReplies []string
	for _, it := range offProj.Projection.GetItems() {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			offReplies = append(offReplies, it.Text)
		}
	}
	if len(offReplies) != 2 || !strings.Contains(offReplies[0], "offline_run_1") || !strings.Contains(offReplies[1], "offline_run_2") {
		t.Fatalf("expected Offline Queued Session to retain both runs' assistant messages, got %v", offReplies)
	}

	vpsProjAfter, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: vpsSessionID})
	if err != nil {
		t.Fatalf("GetSession vpsSessionID after later sessions ran: %v", err)
	}
	var sawTurn1Reply bool
	for _, it := range vpsProjAfter.Projection.GetItems() {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && strings.Contains(it.Text, "turn_1_complete") {
			sawTurn1Reply = true
		}
	}
	if !sawTurn1Reply {
		t.Fatalf("vpsSessionID lost its turn_1_complete assistant message after subsequent sessions ran: %+v", vpsProjAfter.Projection.GetItems())
	}

	// 6. Remote Background Tasks + Automatic Continuation Run + Remote StopBackgroundTask over Relay!
	bgLaunch, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId:   vpsID,
		ProjectId:   "demo",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "claude",
			Model:      "claude-sonnet-4-6",
		},
		InitialPrompt: "bg-daemon npm run dev|ready on :3000\nbg-shell 800ms|make e2e|15 passed|Remote background suite passed!\nsay Started remote background suite",
	})
	if err != nil {
		t.Fatalf("StartSession remote background: %v", err)
	}
	bgSessionID := bgLaunch.Session.SessionId

	// Wait for automatic Continuation Run #2 to settle ActiveBackgroundCount -> 0 while ActiveDaemonCount stays 1.
	var bgAfterCont *sessionv1.SessionProjection
	eventually(t, 20*time.Second, "remote background continuation completed on vps-1", func() bool {
		g, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: bgSessionID})
		if err != nil {
			return false
		}
		s := g.Projection.GetSession()
		if s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE && s.GetProgress().GetTurnCount() >= 2 && s.GetActiveBackgroundCount() == 0 && s.GetActiveDaemonCount() == 1 {
			bgAfterCont = g.Projection
			return true
		}
		return false
	})
	var sawRemoteBgReply bool
	for _, it := range bgAfterCont.GetItems() {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && strings.Contains(it.Text, "Remote background suite passed!") {
			sawRemoteBgReply = true
		}
	}
	if !sawRemoteBgReply {
		t.Fatalf("missing remote continuation reply in items: %+v", bgAfterCont.GetItems())
	}

	// Stop the remote daemon over Relay via Owner SessionService.StopBackgroundTask!
	stopDaemonRes, err := sc.StopBackgroundTask(ctx, &sessionv1.StopBackgroundTaskRequest{
		SessionId: bgSessionID,
	})
	if err != nil {
		t.Fatalf("remote StopBackgroundTask failed: %v", err)
	}
	if stopDaemonRes.GetSession().GetActiveDaemonCount() != 0 || len(stopDaemonRes.GetStoppedTasks()) != 1 {
		t.Fatalf("expected remote daemon stopped cleanly, got %+v", stopDaemonRes)
	}
}
