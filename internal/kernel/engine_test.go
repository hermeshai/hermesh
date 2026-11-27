package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
)

func initGitProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@hermesh.local")
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".claude", "commands"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", "commands", "review.md"), []byte("Review diff"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "skills", "deploy"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "skills", "deploy", "SKILL.md"), []byte("# Deploy"), 0o644)
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func openTestEngine(t *testing.T, dataDir string, idleTTL time.Duration) (*Engine, *scripted.Driver, *scripted.Driver) {
	t.Helper()
	drv1 := scripted.NewDriver()
	drv2 := &scripted.Driver{DisableHotSwitch: true, DisableResume: true}
	eng, err := Open(Config{
		DataDir:         dataDir,
		MachineID:       "m_test",
		Drivers:         map[string]runtime.Driver{"scripted": drv1, "alt": drv2},
		IncludeScripted: true,
		IdleTTL:         idleTTL,
	})
	if err != nil {
		t.Fatalf("Open engine: %v", err)
	}
	owner := OwnerPrincipal("dev_owner", "Owner Laptop")
	_, err = eng.UpsertProviderInstance(context.Background(), owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "alt-agent",
		Driver:      "alt",
		DisplayName: "Alt Unresumable Agent",
		Enabled:     true,
		Env:         map[string]string{"ALT_API_KEY": "sk-super-secret-value", "PUBLIC_ENV": "1"},
	})
	if err != nil {
		t.Fatalf("UpsertProviderInstance: %v", err)
	}
	return eng, drv1, drv2
}

func waitForSessionStatus(t *testing.T, eng *Engine, p *Principal, sessionID string, want ...sessionv1.SessionStatus) *sessionv1.SessionProjection {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		proj, err := eng.GetSession(context.Background(), p, sessionID)
		if err == nil {
			for _, w := range want {
				if proj.Session.Status == w {
					return proj
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	proj, _ := eng.GetSession(context.Background(), p, sessionID)
	t.Fatalf("timed out waiting for session %s status %v (current=%v)", sessionID, want, proj.GetSession().GetStatus())
	return nil
}

func TestProviderCatalogEnvRedactionAndIdempotency(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	eng, _, _ := openTestEngine(t, dataDir, time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	if err := eng.SyncWorkspace("demo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "Demo repo", nil); err != nil {
		t.Fatal(err)
	}

	owner := OwnerPrincipal("dev_owner", "Owner")
	providers, err := eng.ListProviders(ctx, owner, "demo", true)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	var alt *sessionv1.ProviderInstance
	for _, p := range providers.Providers {
		if p.InstanceId == "alt-agent" {
			alt = p
		}
	}
	if alt == nil {
		t.Fatalf("alt-agent missing from ListProviders")
	}
	if alt.RedactedEnv["ALT_API_KEY"] != "••••••••" || alt.RedactedEnv["PUBLIC_ENV"] != "1" {
		t.Fatalf("unexpected redacted env: %+v", alt.RedactedEnv)
	}
	if len(alt.SlashCommands) == 0 || len(alt.Skills) == 0 {
		t.Fatalf("expected workspace commands & skills scanned, got cmds=%v skills=%v", alt.SlashCommands, alt.Skills)
	}

	// Updating instance with redacted placeholder preserves original secret in store!
	_, err = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "alt-agent",
		Driver:      "alt",
		DisplayName: "Alt Renamed",
		Enabled:     true,
		Env:         map[string]string{"ALT_API_KEY": "••••••••"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := eng.Store().ProviderInstances()
	for _, c := range stored {
		if c.InstanceId == "alt-agent" && c.Env["ALT_API_KEY"] != "sk-super-secret-value" {
			t.Fatalf("secret env value was clobbered by placeholder: %q", c.Env["ALT_API_KEY"])
		}
	}

	// Idempotent StartSession with same command_id returns identical session_id & run_id.
	req := &sessionv1.StartSessionRequest{
		CommandId:     "cmd_launch_1",
		ProjectId:     "demo",
		InitialPrompt: "say Hello world",
	}
	r1, err := eng.StartSession(ctx, owner, req)
	if err != nil {
		t.Fatalf("StartSession #1: %v", err)
	}
	r2, err := eng.StartSession(ctx, owner, req)
	if err != nil {
		t.Fatalf("StartSession #2 (replay): %v", err)
	}
	if r1.Session.SessionId != r2.Session.SessionId || r1.InitialRun.RunId != r2.InitialRun.RunId {
		t.Fatalf("idempotent replay created duplicate session/run: %v vs %v", r1.Session.SessionId, r2.Session.SessionId)
	}
	// Reusing command_id for a different RPC method must reject with InvalidArgument.
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		CommandId: "cmd_launch_1",
		SessionId: r1.Session.SessionId,
		Text:      "hi",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected InvalidArgument on cross-method command_id reuse, got %v", err)
	}
	waitForSessionStatus(t, eng, owner, r1.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
}

func TestMultiPrincipalGrantCeilingAndApprovalBoundary(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	_ = eng.SyncWorkspace("allowed-proj", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	_ = eng.SyncWorkspace("secret-proj", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)

	owner := OwnerPrincipal("dev_owner", "Owner")
	extGrant := GrantPrincipal(&identityv1.Grant{
		GrantId:        "gr_ext_1",
		ClientId:       "cli_bot",
		ClientLabel:    "External Orchestrator",
		ProjectIds:     []string{"allowed-proj"},
		Agents:         []string{"scripted"},
		MaxRuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
		TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_OWN,
	})

	// 1. Grant cannot launch in unauthorized project or unauthorized provider.
	if _, err := eng.StartSession(ctx, extGrant, &sessionv1.StartSessionRequest{ProjectId: "secret-proj"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied on secret-proj, got %v", err)
	}
	if _, err := eng.StartSession(ctx, extGrant, &sessionv1.StartSessionRequest{
		ProjectId:      "allowed-proj",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "alt-agent"},
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied on alt-agent, got %v", err)
	}

	// 2. Grant requesting FULL_ACCESS is clamped to AUTO_EDITS!
	//    Workspace file edit auto-succeeds, while `npm install` command pauses for TOOL_APPROVAL.
	launched, err := eng.StartSession(ctx, extGrant, &sessionv1.StartSessionRequest{
		ProjectId:   "allowed-proj",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: strings.Join([]string{
			"write auto_allowed.txt|hello from auto_edits",
			"shell npm install leftpad|installed 1 pkg",
			"shell npm install rightpad|installed 2nd pkg",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if launched.Session.EffectiveRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("EffectiveRuntimeMode = %v, want AUTO_EDITS", launched.Session.EffectiveRuntimeMode)
	}

	proj := waitForSessionStatus(t, eng, extGrant, launched.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	reqID := proj.Session.PendingRequestId
	if reqID == "" {
		t.Fatalf("expected pending request ID")
	}

	// 3. External Grant MUST NOT be able to self-approve TOOL_APPROVAL!
	_, err = eng.RespondRuntimeRequest(ctx, extGrant, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: launched.Session.SessionId,
		RequestId: reqID,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied when external grant attempts self-approval, got %v", err)
	}

	// 4. Owner approves with ACCEPT_FOR_SESSION ("cmd:npm install"):
	//    The second `npm install rightpad` command in the same turn must auto-allow WITHOUT pausing again!
	_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		CommandId: "approve_npm",
		SessionId: launched.Session.SessionId,
		RequestId: reqID,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_SESSION,
	})
	if err != nil {
		t.Fatalf("Owner approve: %v", err)
	}

	finalProj := waitForSessionStatus(t, eng, owner, launched.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if len(finalProj.Requests) != 1 {
		t.Fatalf("expected second npm install to auto-allow via session rule, got %d requests", len(finalProj.Requests))
	}
}

func TestDeclineFeedbackUserInputPlanModeAndZeroPersistenceSecrets(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	eng, _, _ := openTestEngine(t, dataDir, time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	_ = eng.SyncWorkspace("proj", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	// Upload an attachment blob for structured question answer.
	blobRef, err := eng.Store().PutBlob("spec.txt", "text/plain", []byte("spec-contents"))
	if err != nil {
		t.Fatal(err)
	}

	// Start in PLAN mode: propose a plan, ask a question, request a secret, then edit a file!
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:       "proj",
		RuntimeMode:     identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
		InteractionMode: sessionv1.InteractionMode_INTERACTION_MODE_PLAN,
		InitialPrompt: strings.Join([]string{
			"propose-plan Unified Refactor|1. Build kernel\\n2. Test everything",
			"ask db_choice|Database|Which DB?|SQLite,Postgres",
			"ask-secret PROD_TOKEN|Deploy verification",
			"write plan_implemented.txt|built after plan approval",
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	// 1. Wait for PLAN_REVIEW and approve it -> session atomically switches PLAN -> CODE!
	p1 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId:   sid,
		RequestId:   p1.Session.PendingRequestId,
		ApprovePlan: true,
	})
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	// 2. Wait for USER_INPUT and answer with option + custom text + attachment.
	p2 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: sid,
		RequestId: p2.Session.PendingRequestId,
		Answers: map[string]*sessionv1.QuestionAnswer{
			"db_choice": {
				SelectedOptions: []string{"SQLite"},
				CustomText:      "WAL mode",
				Attachments:     []*sessionv1.AttachmentRef{blobRef},
			},
		},
	})
	if err != nil {
		t.Fatalf("Answer USER_INPUT: %v", err)
	}

	// 3. Wait for SECRET_INPUT and provide a sensitive token.
	const topSecret = "sec_99887766_NEVER_PERSIST_ME"
	p3 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId:   sid,
		RequestId:   p3.Session.PendingRequestId,
		SecretValue: topSecret,
	})
	if err != nil {
		t.Fatalf("Provide SECRET_INPUT: %v", err)
	}

	// 4. Wait for TOOL_APPROVAL on write (since RuntimeMode is SUPERVISED, and InteractionMode is now CODE!),
	//    and DECLINE it with steering feedback!
	p4 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	if p4.Session.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("expected InteractionMode to switch to CODE after plan approval, got %v", p4.Session.InteractionMode)
	}
	_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: sid,
		RequestId: p4.Session.PendingRequestId,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_DECLINE,
		Feedback:  "use another filename instead",
	})
	if err != nil {
		t.Fatalf("Decline write: %v", err)
	}

	finalProj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	var assistantText string
	for _, it := range finalProj.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			assistantText += it.Text + "\n"
		}
	}
	for _, wantSub := range []string{
		"plan_approved=true",
		"answer=SQLite,WAL mode,files=1",
		"secret_received_len=29",
		"[write declined: use another filename instead]",
	} {
		if !strings.Contains(assistantText, wantSub) {
			t.Fatalf("assistant output missing %q; got:\n%s", wantSub, assistantText)
		}
	}

	// Verify topSecret NEVER appears anywhere in SQLite DB or WAL files!
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(filepath.Join(dataDir, "sessions.db"+suffix))
		if err == nil && strings.Contains(string(raw), topSecret) {
			t.Fatalf("SECURITY VIOLATION: secret value persisted in sessions.db%s!", suffix)
		}
	}
}

func TestSteerQueueReorderEditCancelPromoteAndForceStop(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	owner := OwnerPrincipal("dev_owner", "Owner")
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		InitialPrompt: "think starting_turn\nsleep 350ms\nsay turn1_done",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		if p.GetSession().GetActiveProviderSessionId() != "" && len(p.GetItems()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Live mid-turn STEER injects into Run 1!
	steerRes, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "focus on edge cases",
		Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_STEER,
	})
	if err != nil || !steerRes.SteeredActiveRun {
		t.Fatalf("expected live steer on active run, err=%v res=%+v", err, steerRes)
	}

	// 2. Enqueue 3 follow-up messages while Run 1 is running.
	q1, _ := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say q1_original", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	q2, _ := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say q2_cancel_me", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	q3, _ := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say q3_move_first", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})

	// Edit q1, Cancel q2, Reorder q3 before q1!
	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid, Operation: sessionv1.QueueOperation_QUEUE_OPERATION_EDIT, RunId: q1.Run.RunId, Text: "say q1_edited",
	}); err != nil {
		t.Fatalf("Edit queue: %v", err)
	}
	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid, Operation: sessionv1.QueueOperation_QUEUE_OPERATION_CANCEL, RunId: q2.Run.RunId,
	}); err != nil {
		t.Fatalf("Cancel queue: %v", err)
	}
	mq, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid, Operation: sessionv1.QueueOperation_QUEUE_OPERATION_REORDER, RunId: q3.Run.RunId, BeforeRunId: q1.Run.RunId,
	})
	if err != nil {
		t.Fatalf("Reorder queue: %v", err)
	}
	if len(mq.QueuedRuns) != 2 || mq.QueuedRuns[0].RunId != q3.Run.RunId || mq.QueuedRuns[1].RunId != q1.Run.RunId {
		t.Fatalf("unexpected queue order: %+v", mq.QueuedRuns)
	}

	// Wait for Run 1 + q3 + q1 to drain automatically!
	proj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if proj.Session.GetProgress().GetTurnCount() != 3 {
		t.Fatalf("expected 3 completed runs, got %d", proj.Session.GetProgress().GetTurnCount())
	}
	var ordinals []string
	for _, r := range proj.Runs {
		if r.Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			ordinals = append(ordinals, r.Prompt)
		}
	}
	if len(ordinals) != 3 || ordinals[1] != "say q3_move_first" || ordinals[2] != "say q1_edited" {
		t.Fatalf("unexpected run execution order: %v", ordinals)
	}

	// 3. Test unresponsive `hang` turn + queued follow-up + `InterruptSession(Force=true, HoldQueue=true)` + `RESUME_QUEUE`!
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "hang", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say after_force_stop", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{
		SessionId: sid, Force: true, HoldQueue: true,
	}); err != nil {
		t.Fatalf("InterruptSession(Force): %v", err)
	}
	heldProj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_QUEUED)
	if !heldProj.Session.QueueHeld || heldProj.Session.QueuedRunCount != 1 {
		t.Fatalf("expected held queue with 1 run, got held=%v count=%d", heldProj.Session.QueueHeld, heldProj.Session.QueuedRunCount)
	}

	if _, err := eng.ManageQueue(ctx, owner, &sessionv1.ManageQueueRequest{
		SessionId: sid, Operation: sessionv1.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE,
	}); err != nil {
		t.Fatalf("RESUME_QUEUE: %v", err)
	}
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
}

func TestTransitionsIdleReapCrossProviderHandoffRollbackAndFork(t *testing.T) {
	ctx := context.Background()
	eng, drv1, _ := openTestEngine(t, t.TempDir(), 40*time.Millisecond)
	defer eng.Close()

	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	// Launch in isolated git worktree!
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId: "repo",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "scripted",
			Model:      "scripted-fast",
			Options: map[string]*sessionv1.OptionValue{
				"reasoningEffort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "medium"}},
			},
		},
		InitialPrompt: "write step1.txt|step1_v1\ninspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId
	// The worktree is made by the first turn.
	wtPath := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE).Session.WorkspaceStrategy.WorktreePath
	if wtPath == "" || wtPath == repo {
		t.Fatalf("expected isolated worktree path, got %q", wtPath)
	}
	if drv1.StartCount() != 1 {
		t.Fatalf("expected 1 session spawn, got %d", drv1.StartCount())
	}

	// 1. Hot in-session model + option switch WITHOUT spawning a 2nd process!
	cfgRes, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{
		SessionId: sid,
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "scripted",
			Model:      "scripted-pro",
			Options: map[string]*sessionv1.OptionValue{
				"reasoningEffort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "high"}},
				"fastMode":        {Kind: &sessionv1.OptionValue_BoolValue{BoolValue: true}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfgRes.TransitionApplied != string(TransitionSwitchInSession) {
		t.Fatalf("TransitionApplied = %q, want switch_in_session", cfgRes.TransitionApplied)
	}
	_, _ = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "write step2.txt|step2_v1\ninspect",
	})
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if drv1.StartCount() != 1 {
		t.Fatalf("hot switch must reuse live session, StartCount=%d", drv1.StartCount())
	}

	// 2. Wait for IdleTTL reaper to close idle session -> next run seamlessly does SpawnAndResume!
	time.Sleep(100 * time.Millisecond)
	eng.Supervisor().ReapIdle(time.Now().Add(time.Second))
	if eng.Supervisor().ActiveCount() != 0 {
		t.Fatalf("expected idle session to be reaped")
	}
	_, _ = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "inspect",
	})
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if drv1.StartCount() != 2 {
		t.Fatalf("expected SpawnAndResume after idle reap, StartCount=%d", drv1.StartCount())
	}

	// 3. Switch to `alt-agent` (cross-provider) -> must build ContextHandoff including modified files!
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId:      sid,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "alt-agent", Model: "alt-1"},
		Text:           "inspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	proj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	var sawHandoff bool
	for _, it := range proj.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_HANDOFF && strings.Contains(it.Text, "step1.txt") && strings.Contains(it.Text, "step2.txt") {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("expected HANDOFF item summarizing prior turns and modified files")
	}

	// 4. Fork session at Run #1!
	forkRes, err := eng.ForkSession(ctx, owner, &sessionv1.ForkSessionRequest{
		SourceSessionId: sid,
		UpToRunOrdinal:  1,
		Title:           "Forked After Step 1",
	})
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkProj, err := eng.GetSession(ctx, owner, forkRes.Session.SessionId)
	if err != nil || forkProj.Session.GetProgress().GetTurnCount() != 1 {
		t.Fatalf("expected forked session to have 1 run, got %v (err=%v)", forkProj.GetSession().GetProgress().GetTurnCount(), err)
	}

	// 5. Rollback original session to Checkpoint #1 with RestoreFiles=true -> step2.txt disappears, step1.txt remains!
	ckpts, err := eng.ListCheckpoints(ctx, owner, sid)
	if err != nil {
		t.Fatal(err)
	}
	var cp1 *sessionv1.Checkpoint
	for _, c := range ckpts {
		if c.RunOrdinal == 1 {
			cp1 = c
		}
	}
	if cp1 == nil {
		t.Fatalf("checkpoint #1 not found in %+v", ckpts)
	}
	if _, err := eng.RollbackCheckpoint(ctx, owner, &sessionv1.RollbackCheckpointRequest{
		SessionId:    sid,
		CheckpointId: cp1.CheckpointId,
		RestoreFiles: true,
	}); err != nil {
		t.Fatalf("RollbackCheckpoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "step1.txt")); err != nil {
		t.Fatalf("step1.txt must remain after rollback to run #1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "step2.txt")); !os.IsNotExist(err) {
		t.Fatalf("step2.txt must be removed after rollback to run #1")
	}
}

func TestDaemonCrashOrphanRecoveryAndStreamingReplay(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	eng1, _, _ := openTestEngine(t, dataDir, time.Minute)
	owner := OwnerPrincipal("dev_owner", "Owner")

	// Start a session that pauses on USER_INPUT, and enqueue a follow-up message behind it!
	launched, err := eng1.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		InitialPrompt: "ask q1|Env|Target?|staging,prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId
	p1 := waitForSessionStatus(t, eng1, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	reqID := p1.Session.PendingRequestId

	_, err = eng1.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say queued_survived_crash", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Abruptly close eng1 while Run 1 is WAITING_REQUEST and Run 2 is QUEUED!
	_ = eng1.Close()

	// Reopen Engine on the same DataDir -> recoverOrphans runs!
	eng2, _, _ := openTestEngine(t, dataDir, time.Minute)
	defer eng2.Close()

	recProj, err := eng2.GetSession(ctx, owner, sid)
	if err != nil {
		t.Fatal(err)
	}
	if recProj.Session.Status != sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
		t.Fatalf("expected WAITING_INPUT preserved for resumable USER_INPUT, got %v", recProj.Session.Status)
	}
	if recProj.Session.QueuedRunCount != 1 {
		t.Fatalf("expected queued run to survive daemon restart, got %d", recProj.Session.QueuedRunCount)
	}
	var pendingReq *sessionv1.RuntimeRequest
	for _, r := range recProj.Requests {
		if r.RequestId == reqID {
			pendingReq = r
		}
	}
	if pendingReq == nil || pendingReq.ResponseMode != sessionv1.ResponseMode_RESPONSE_MODE_RESUME_TURN {
		t.Fatalf("expected pending request downgraded to RESUME_TURN, got %+v", pendingReq)
	}

	// SubscribeSession from afterSeq=0 replays full event log seamlessly!
	subCtx, cancelSub := context.WithCancel(ctx)
	var replayed int
	go func() {
		_ = eng2.SubscribeSession(subCtx, owner, sid, 0, func(ev *sessionv1.SessionEvent) error {
			replayed++
			if ev.Type == "session.recovered" {
				cancelSub()
			}
			return nil
		})
	}()
	<-subCtx.Done()
	if replayed < 3 {
		t.Fatalf("expected replayed events >= 3, got %d", replayed)
	}

	// Answering the recovered USER_INPUT automatically dispatches a continuation turn AND drains the queued run!
	_, err = eng2.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: sid,
		RequestId: reqID,
		Answers: map[string]*sessionv1.QuestionAnswer{
			"q1": {SelectedOptions: []string{"staging"}},
		},
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest after crash recovery: %v", err)
	}
	afterProj := waitForSessionStatus(t, eng2, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if afterProj.Session.QueuedRunCount != 0 {
		t.Fatalf("expected queued run to drain after answering recovered request, remaining=%d", afterProj.Session.QueuedRunCount)
	}
}

func TestPostTurnArtifactPlanReviewReviseApproveAndSupersede(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	_ = eng.SyncWorkspace("proj", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	// 1. Run 1 in PLAN mode proposes a RequestFeedback=true artifact and ends its turn.
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:       "proj",
		InteractionMode: sessionv1.InteractionMode_INTERACTION_MODE_PLAN,
		InitialPrompt: strings.Join([]string{
			"propose-artifact Implementation Plan|true|# Plan v1\\n\\n1. Draft architecture",
			"say Click Proceed when ready.",
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	p1 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	if p1.Session.ActiveRunId != "" {
		t.Fatalf("expected ActiveRunId to be empty for post-turn RESUME_TURN plan review, got %q", p1.Session.ActiveRunId)
	}
	if len(p1.Runs) != 1 || p1.Runs[0].Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED {
		t.Fatalf("expected Run 1 COMPLETED, got %+v", p1.Runs)
	}
	req1ID := p1.Session.PendingRequestId
	var req1 *sessionv1.RuntimeRequest
	for _, r := range p1.Requests {
		if r.RequestId == req1ID {
			req1 = r
		}
	}
	if req1 == nil ||
		req1.Kind != sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW ||
		req1.ResponseMode != sessionv1.ResponseMode_RESPONSE_MODE_RESUME_TURN ||
		!strings.Contains(req1.GetPlanReview().GetMarkdown(), "# Plan v1") {
		t.Fatalf("unexpected post-turn PLAN_REVIEW request: %+v", req1)
	}

	// 2. Ask for revision (ApprovePlan=false) -> stays in PLAN mode and dispatches continuation Run 2!
	revRes, err := eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId:   sid,
		RequestId:   req1ID,
		ApprovePlan: false,
		Feedback:    "include rollback verification",
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest(revise): %v", err)
	}
	if revRes.Session.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_PLAN {
		t.Fatalf("expected PLAN mode retained on revision, got %v", revRes.Session.InteractionMode)
	}
	p2 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if len(p2.Runs) != 2 || p2.Runs[1].Prompt != "Please revise the plan: include rollback verification" {
		t.Fatalf("expected Run 2 revision prompt, got runs=%+v", p2.Runs)
	}

	// 3. Run 3 emits both a RequestFeedback=true revised plan AND a RequestFeedback=false deliverable;
	//    only the RequestFeedback=true artifact parks the session, and ApprovePlan=true switches PLAN -> CODE!
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text: strings.Join([]string{
			"propose-artifact Implementation Plan|true|# Plan v2\\n\\n1. With rollback verification",
			"propose-artifact Notes|false|# Notes\\n\\nSupplementary walkthrough",
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	p3 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	req3ID := p3.Session.PendingRequestId
	appRes, err := eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId:   sid,
		RequestId:   req3ID,
		ApprovePlan: true,
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest(approve): %v", err)
	}
	if appRes.Session.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("expected RespondRuntimeRequest response Session to reflect CODE mode, got %v", appRes.Session.InteractionMode)
	}
	p4 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if p4.Session.InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("expected session in CODE mode, got %v", p4.Session.InteractionMode)
	}
	if len(p4.Runs) != 4 || p4.Runs[3].Prompt != "Plan approved. Proceed with implementation in Code mode." || p4.Runs[3].InteractionMode != sessionv1.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("unexpected Run 4 continuation after plan approval: %+v", p4.Runs)
	}

	// 4. Superseding: when parked on a post-turn RESUME_TURN proposal,
	//    sending a normal message in the composer dismisses the pending request and runs immediately!
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "propose-artifact Learning Proposal|true|# Learnings\\n\\nPersist rule X",
	})
	if err != nil {
		t.Fatal(err)
	}
	p5 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	req5ID := p5.Session.PendingRequestId
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "say superseded_cleanly",
	})
	if err != nil {
		t.Fatalf("DispatchMessage while parked on RESUME_TURN request: %v", err)
	}
	p6 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if p6.Session.PendingRequestId != "" || len(p6.Runs) != 6 {
		t.Fatalf("expected PendingRequestId cleared and Run 6 completed, pending=%q runs=%d", p6.Session.PendingRequestId, len(p6.Runs))
	}
	var req5After *sessionv1.RuntimeRequest
	for _, r := range p6.Requests {
		if r.RequestId == req5ID {
			req5After = r
		}
	}
	if req5After == nil || req5After.Status != sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_DISMISSED {
		t.Fatalf("expected superseded request %s to be DISMISSED, got %+v", req5ID, req5After)
	}
}

func TestCrossProviderHandoffUTF8MultibyteQACommandAndStateReset(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), 5*time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	// Upload a blob attachment on Turn 1 so we verify attachment path preservation in handoff.
	blobRes, err := eng.Store().PutBlob("mockup.png", "image/png", []byte("PNGDATA"))
	if err != nil {
		t.Fatal(err)
	}
	// Long multibyte Chinese prompt (> 2500 UTF-8 bytes) + question + failing command + long Chinese reply (> 3000 bytes).
	longZhPrompt := strings.Repeat("请检查跨模型切换时的多字节中文字符截断边界是否安全。", 40) // > 3000 bytes
	longZhReply := strings.Repeat("已完成第一阶段架构梳理，所有编码边界均需保持合法。", 45)    // > 3200 bytes

	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:      "repo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted", Model: "scripted-fast"},
		Attachments:    []*sessionv1.AttachmentRef{blobRes},
		InitialPrompt: strings.Join([]string{
			"ask Runtime|Which target runtime?|Go,Rust",
			"say " + longZhReply + " " + longZhPrompt,
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId

	// Answer the LIVE_RPC question during Turn 1:
	waiting := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: sid,
		RequestId: waiting.Session.PendingRequestId,
		Answers: map[string]*sessionv1.QuestionAnswer{
			"q1": {SelectedOptions: []string{"Go"}},
		},
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest: %v", err)
	}

	p1 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if p1.Session.GetContextUsage().GetContextUsedTokens() == 0 {
		t.Fatalf("expected non-zero ContextUsage after Turn 1")
	}

	// Run a failing owner shell shortcut (!...) so a failed COMMAND_EXECUTION with error output is in history:
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "!sh -c 'echo undefined symbol foo_bar in module_x.go:42; exit 1'",
	})
	if err != nil {
		t.Fatalf("DispatchMessage failing shell: %v", err)
	}
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	// UpdateSession to alt-agent WITHOUT specifying Model: clears stale Model & ContextUsage!
	upd, err := eng.UpdateSession(ctx, owner, &sessionv1.UpdateSessionRequest{
		SessionId:      sid,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "alt-agent"},
	})
	if err != nil {
		t.Fatalf("UpdateSession cross-provider: %v", err)
	}
	if upd.Session.Agent != "alt-agent" || upd.Session.Model != "" {
		t.Fatalf("expected Agent=alt-agent and cleared Model, got agent=%q model=%q", upd.Session.Agent, upd.Session.Model)
	}
	if upd.Session.ContextUsage != nil {
		t.Fatalf("expected ContextUsage cleared on cross-provider switch, got %+v", upd.Session.ContextUsage)
	}

	// Turn 3 on alt-agent: must build valid UTF-8 handoff, persist ProviderSessionId on Run 3, and persist HANDOFF item!
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId:      sid,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "alt-agent", Model: "alt-1"},
		Text:           "inspect",
	})
	if err != nil {
		t.Fatalf("DispatchMessage cross-provider: %v", err)
	}
	p2 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	lastRun := p2.Runs[len(p2.Runs)-1]
	if lastRun.ProviderSessionId == "" || lastRun.HandoffSummary == "" {
		t.Fatalf("expected final Run to persist ProviderSessionId and HandoffSummary, got %+v", lastRun)
	}
	var handoffItem *sessionv1.TurnItem
	for _, it := range p2.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_HANDOFF {
			handoffItem = it
		}
	}
	if handoffItem == nil {
		t.Fatalf("expected persisted TURN_ITEM_KIND_HANDOFF item on UTF-8 multibyte history")
	}
	for _, want := range []string{
		"mockup.png",
		"[Q&A]: Which target runtime? -> Go",
		"undefined symbol foo_bar",
		"...[truncated]...",
	} {
		if !strings.Contains(handoffItem.Text, want) {
			t.Fatalf("handoff summary missing %q:\n%s", want, handoffItem.Text)
		}
	}
}
