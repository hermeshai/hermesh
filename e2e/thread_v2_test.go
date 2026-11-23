package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2/threadv2connect"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/pkg/identity"
)

func ownerThreadClient(sock string) threadv2connect.ThreadServiceClient {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}}
	return threadv2connect.NewThreadServiceClient(hc, "http://local")
}

// TestThreadV2EndToEnd exercises the full Hermesh v2 architecture across real
// compiled binaries (hermesh-relay + Owner hermeshd + Remote Machine hermeshd +
// hermesh CLI):
//  1. Native launch, continuation, live model/provider handoff, Git diff & file rollback
//  2. Cryptographic Grant max_runtime_mode clamping & external self-approval denial
//  3. Plan -> Approve -> Code flow, mid-turn Steer, and Queue management
//  4. Durable Offline Outbox queuing while remote machine is down + automatic FIFO replay on reconnect
func TestThreadV2EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	// Force fresh binary build in case v2 CLI/daemon code changed in this test session.
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

	tc := ownerThreadClient(sock)

	// 2. Owner native launch on remote VPS worktree + cross-provider handoff + checkpoint rollback!
	provRes, err := tc.ListProviders(ctx, &threadv2.ListProvidersRequest{MachineId: vpsID})
	if err != nil || len(provRes.Providers) == 0 {
		t.Fatalf("ListProviders on vps: err=%v count=%d", err, len(provRes.GetProviders()))
	}

	launchRes, err := tc.LaunchThread(ctx, &threadv2.LaunchThreadRequest{
		MachineId: vpsID,
		ProjectId: "demo",
		ModelSelection: &threadv2.ModelSelection{
			InstanceId: "claude",
			Model:      "claude-sonnet-4-6",
		},
		InitialPrompt: "sleep 2s\nwrite vps_hello.txt|hello_v1\nsay turn_1_complete",
	})
	if err != nil {
		t.Fatalf("LaunchThread on vps: %v", err)
	}
	vpsThreadID := launchRes.Thread.ThreadId
	// The thread list hears the remote turn finish by itself, with nobody
	// reading the thread: the owner follows each machine's threads.
	listCtx, stopList := context.WithTimeout(ctx, 20*time.Second)
	list, err := tc.SubscribeThreads(listCtx, &threadv2.SubscribeThreadsRequest{})
	if err != nil {
		t.Fatalf("SubscribeThreads: %v", err)
	}
	heardIdle := false
	for !heardIdle && list.Receive() {
		for _, th := range list.Msg().Threads {
			heardIdle = heardIdle || th.ThreadId == vpsThreadID && th.Status == threadv2.ThreadStatus_THREAD_STATUS_IDLE
		}
	}
	list.Close()
	stopList()
	if !heardIdle {
		t.Fatalf("the thread list never heard vps's turn finish: %v", list.Err())
	}
	eventually(t, 15*time.Second, "vps turn 1 idle", func() bool {
		g, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: vpsThreadID})
		return err == nil && g.Projection.GetThread().GetStatus() == threadv2.ThreadStatus_THREAD_STATUS_IDLE
	})

	// Switch provider from claude -> codex on Turn 2 and edit a 2nd file!
	_, err = tc.DispatchMessage(ctx, &threadv2.DispatchMessageRequest{
		ThreadId: vpsThreadID,
		ModelSelection: &threadv2.ModelSelection{
			InstanceId: "codex",
			Model:      "gpt-5.4",
		},
		Text: "write vps_second.txt|second_v1\ninspect",
	})
	if err != nil {
		t.Fatalf("DispatchMessage turn 2: %v", err)
	}
	var turn2Proj *threadv2.ThreadProjection
	eventually(t, 15*time.Second, "vps turn 2 idle", func() bool {
		g, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: vpsThreadID})
		if err == nil && g.Projection.GetThread().GetStatus() == threadv2.ThreadStatus_THREAD_STATUS_IDLE {
			turn2Proj = g.Projection
			return true
		}
		return false
	})
	var sawHandoff bool
	for _, it := range turn2Proj.Items {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_HANDOFF && strings.Contains(it.Text, "vps_hello.txt") {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("expected cross-provider HANDOFF item on remote VPS thread")
	}

	// Verify GetThreadDiff shows both files, then RollbackCheckpoint to Run #1!
	diffBefore, err := tc.GetThreadDiff(ctx, &threadv2.GetThreadDiffRequest{ThreadId: vpsThreadID})
	if err != nil || len(diffBefore.Files) != 2 {
		t.Fatalf("expected 2 changed files before rollback, got %d (err=%v)", len(diffBefore.GetFiles()), err)
	}
	ckpts, err := tc.ListCheckpoints(ctx, &threadv2.ListCheckpointsRequest{ThreadId: vpsThreadID})
	if err != nil {
		t.Fatal(err)
	}
	var cp1ID string
	for _, c := range ckpts.Checkpoints {
		if c.RunOrdinal == 1 {
			cp1ID = c.CheckpointId
		}
	}
	if _, err := tc.RollbackCheckpoint(ctx, &threadv2.RollbackCheckpointRequest{
		ThreadId:     vpsThreadID,
		CheckpointId: cp1ID,
		RestoreFiles: true,
	}); err != nil {
		t.Fatalf("RollbackCheckpoint: %v", err)
	}
	diffAfter, err := tc.GetThreadDiff(ctx, &threadv2.GetThreadDiffRequest{ThreadId: vpsThreadID})
	if err != nil || len(diffAfter.Files) != 1 || diffAfter.Files[0].Path != "vps_hello.txt" {
		t.Fatalf("expected only vps_hello.txt after file rollback, got %+v (err=%v)", diffAfter.GetFiles(), err)
	}

	// 3. Pair External CLI Client with Grant Ceiling `AUTO_EDITS` and verify cryptographic enforcement!
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

	// Bot starts a thread via `hermesh thread start` requesting `full-access` -> clamped to `AUTO_EDITS`!
	// Its shell command pauses on `WAITING_REQUEST`.
	startOut := run(t, botEnv, 30*time.Second, cliBin, "thread", "start",
		"--machine", "vps-1",
		"--project", "demo",
		"--provider", "claude",
		"--runtime-mode", "full-access",
		"--prompt", "write allowed.txt|ok\nshell npm publish|published",
		"--wait",
		"-o", "json",
	)
	if startOut.code != 0 {
		t.Fatalf("bot thread start failed (%d):\nstdout:%s\nstderr:%s", startOut.code, startOut.stdout, startOut.stderr)
	}
	doc := jsonDoc(t, startOut)
	thMap := doc["thread"].(map[string]any)
	botThreadID := thMap["thread_id"].(string)

	// Verify effective_runtime_mode was clamped to AUTO_EDITS (enum 2) and status is WAITING_REQUEST (enum 5)!
	botProjRes, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: botThreadID})
	if err != nil {
		t.Fatalf("GetThread botThread: %v", err)
	}
	if botProjRes.Projection.Thread.EffectiveRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("EffectiveRuntimeMode = %v, want AUTO_EDITS", botProjRes.Projection.Thread.EffectiveRuntimeMode)
	}
	if botProjRes.Projection.Thread.Status != threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST {
		t.Fatalf("bot thread status = %v, want WAITING_REQUEST", botProjRes.Projection.Thread.Status)
	}

	// Bot tries to self-approve its own blocked shell command -> MUST BE DENIED (exit code 3)!
	selfApprove := run(t, botEnv, 20*time.Second, cliBin, "thread", "respond", botThreadID, "--decision", "once")
	if selfApprove.code != 3 {
		t.Fatalf("SECURITY FAILURE: external bot self-approval returned code %d (want 3 / denied):\n%s\n%s", selfApprove.code, selfApprove.stdout, selfApprove.stderr)
	}

	// Owner declines the shell command with feedback via CLI `--socket` -> thread finishes cleanly!
	ownerDecline := run(t, nil, 20*time.Second, cliBin, "thread", "respond", botThreadID,
		"--socket", sock,
		"--decision", "decline",
		"--feedback", "do not publish packages",
	)
	if ownerDecline.code != 0 {
		t.Fatalf("owner decline failed (%d): %s %s", ownerDecline.code, ownerDecline.stdout, ownerDecline.stderr)
	}
	eventually(t, 15*time.Second, "bot thread settled after owner decline", func() bool {
		g, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: botThreadID})
		return err == nil && g.Projection.GetThread().GetStatus() == threadv2.ThreadStatus_THREAD_STATUS_IDLE
	})

	// 3b. Post-turn RESUME_TURN Plan Review (Antigravity ArtifactMetadata.RequestFeedback / Codex plan):
	// Launch an Antigravity thread in PLAN mode on remote vps-1 -> turn completes and parks on WAITING_REQUEST
	// with ActiveRunId=="" -> Owner approves via CLI `--approve-plan` -> switches to CODE and runs continuation!
	planLaunch, err := tc.LaunchThread(ctx, &threadv2.LaunchThreadRequest{
		MachineId:       vpsID,
		ProjectId:       "demo",
		InteractionMode: threadv2.InteractionMode_INTERACTION_MODE_PLAN,
		ModelSelection: &threadv2.ModelSelection{
			InstanceId: "antigravity",
			Model:      "gemini-3.1-pro",
		},
		InitialPrompt: "propose-artifact Refactor Plan|true|# Refactor Plan\\n\\n1. Unify adapter\nsay Ready, click Proceed.",
	})
	if err != nil {
		t.Fatalf("LaunchThread antigravity plan: %v", err)
	}
	planThreadID := planLaunch.Thread.ThreadId
	var planProj *threadv2.ThreadProjection
	eventually(t, 15*time.Second, "antigravity thread parked on post-turn WAITING_REQUEST", func() bool {
		g, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: planThreadID})
		if err == nil && g.Projection.GetThread().GetStatus() == threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST {
			planProj = g.Projection
			return true
		}
		return false
	})
	if planProj.Thread.ActiveRunId != "" || planProj.Thread.PendingRequestId == "" {
		t.Fatalf("expected post-turn plan review ActiveRunId=='' and non-empty PendingRequestId, got active=%q pending=%q",
			planProj.Thread.ActiveRunId, planProj.Thread.PendingRequestId)
	}
	approvePlanCLI := run(t, nil, 20*time.Second, cliBin, "thread", "respond", planThreadID,
		"--socket", sock,
		"--approve-plan",
	)
	if approvePlanCLI.code != 0 {
		t.Fatalf("hermesh thread respond --approve-plan failed (%d):\n%s\n%s", approvePlanCLI.code, approvePlanCLI.stdout, approvePlanCLI.stderr)
	}
	eventually(t, 15*time.Second, "antigravity thread switched to CODE and completed continuation turn", func() bool {
		g, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: planThreadID})
		if err != nil {
			return false
		}
		th := g.Projection.GetThread()
		return th.GetStatus() == threadv2.ThreadStatus_THREAD_STATUS_IDLE &&
			th.GetInteractionMode() == threadv2.InteractionMode_INTERACTION_MODE_CODE &&
			th.GetRunCount() == 2
	})

	// 4. Durable Offline Outbox: stop vps-1, launch thread + queue follow-up while offline, restart vps-1 -> auto-flushes FIFO!
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

	offLaunch, err := tc.LaunchThread(ctx, &threadv2.LaunchThreadRequest{
		MachineId:     vpsID,
		ProjectId:     "demo",
		Title:         "Offline Queued Thread",
		InitialPrompt: "say offline_run_1",
	})
	if err != nil || !offLaunch.QueuedOffline {
		t.Fatalf("expected QueuedOffline=true while vps-1 is stopped, got res=%+v err=%v", offLaunch, err)
	}
	offSend, err := tc.DispatchMessage(ctx, &threadv2.DispatchMessageRequest{
		ThreadId: offLaunch.Thread.ThreadId,
		Text:     "say offline_run_2",
	})
	if err != nil || !offSend.QueuedOffline {
		t.Fatalf("expected follow-up DispatchMessage to queue offline, got res=%+v err=%v", offSend, err)
	}
	// A launch the machine will reject (unknown project) plus its follow-up must not vanish silently.
	doomed, err := tc.LaunchThread(ctx, &threadv2.LaunchThreadRequest{
		MachineId:     vpsID,
		ProjectId:     "no-such-project",
		Title:         "Rejected Offline Thread",
		InitialPrompt: "say never_runs",
	})
	if err != nil || !doomed.QueuedOffline {
		t.Fatalf("expected rejected-later launch to queue offline, got res=%+v err=%v", doomed, err)
	}
	if _, err := tc.DispatchMessage(ctx, &threadv2.DispatchMessageRequest{ThreadId: doomed.Thread.ThreadId, Text: "say never_runs_2"}); err != nil {
		t.Fatalf("queue follow-up on rejected-later thread: %v", err)
	}
	outboxBefore, err := tc.ListOutbox(ctx, &threadv2.ListOutboxRequest{MachineId: vpsID})
	if err != nil || len(outboxBefore.Entries) != 4 {
		t.Fatalf("expected 4 entries in outbox while offline, got %d (err=%v)", len(outboxBefore.GetEntries()), err)
	}

	// Bring vps-1 back online -> Outbox flushes both LaunchThread and DispatchMessage automatically!
	vpsProc = startVPS()
	_ = vpsProc
	eventually(t, 30*time.Second, "outbox drained and offline thread completed 2 runs on vps-1", func() bool {
		ob, err := tc.ListOutbox(ctx, &threadv2.ListOutboxRequest{MachineId: vpsID})
		if err != nil || len(ob.Entries) != 0 {
			return false
		}
		lt, err := tc.ListThreads(ctx, &threadv2.ListThreadsRequest{MachineId: vpsID})
		if err != nil {
			return false
		}
		for _, th := range lt.Threads {
			if th.Title == "Offline Queued Thread" && th.Status == threadv2.ThreadStatus_THREAD_STATUS_IDLE && th.RunCount == 2 {
				return true
			}
		}
		return false
	})

	// The rejected launch stays visible as a failed local thread carrying the machine's reason.
	eventually(t, 15*time.Second, "rejected offline launch surfaced as failed thread", func() bool {
		g, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: doomed.Thread.ThreadId})
		if err != nil {
			return false
		}
		th := g.Projection.GetThread()
		return th.GetStatus() == threadv2.ThreadStatus_THREAD_STATUS_FAILED && strings.Contains(th.GetLastError(), "no-such-project")
	})
	if _, err := tc.DeleteThread(ctx, &threadv2.DeleteThreadRequest{ThreadId: doomed.Thread.ThreadId}); err != nil {
		t.Fatalf("delete rejected offline thread: %v", err)
	}
	lt, err := tc.ListThreads(ctx, &threadv2.ListThreadsRequest{MachineId: vpsID})
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range lt.Threads {
		if th.ThreadId == doomed.Thread.ThreadId {
			t.Fatalf("rejected offline thread still listed after delete")
		}
	}

	// 5. Verify multi-thread & multi-run assistant reply isolation end-to-end:
	// Earlier vpsThreadID AND the 2-run "Offline Queued Thread" on the same daemon must each
	// retain all of their own assistant messages without msg_1 collisions across threads or runs!
	var offlineRealID string
	for _, th := range lt.Threads {
		if th.Title == "Offline Queued Thread" {
			offlineRealID = th.ThreadId
		}
	}
	if offlineRealID == "" {
		t.Fatalf("Offline Queued Thread not found on vps-1")
	}
	offProj, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: offlineRealID})
	if err != nil {
		t.Fatalf("GetThread offlineRealID: %v", err)
	}
	var offReplies []string
	for _, it := range offProj.Projection.GetItems() {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			offReplies = append(offReplies, it.Text)
		}
	}
	if len(offReplies) != 2 || !strings.Contains(offReplies[0], "offline_run_1") || !strings.Contains(offReplies[1], "offline_run_2") {
		t.Fatalf("expected Offline Queued Thread to retain both runs' assistant messages, got %v", offReplies)
	}

	vpsProjAfter, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: vpsThreadID})
	if err != nil {
		t.Fatalf("GetThread vpsThreadID after later threads ran: %v", err)
	}
	var sawTurn1Reply bool
	for _, it := range vpsProjAfter.Projection.GetItems() {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && strings.Contains(it.Text, "turn_1_complete") {
			sawTurn1Reply = true
		}
	}
	if !sawTurn1Reply {
		t.Fatalf("vpsThreadID lost its turn_1_complete assistant message after subsequent threads ran: %+v", vpsProjAfter.Projection.GetItems())
	}
}
