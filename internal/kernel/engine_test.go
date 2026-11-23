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
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
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
	_, err = eng.UpsertProviderInstance(context.Background(), owner, &threadv2.ProviderInstanceConfig{
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

func waitForThreadStatus(t *testing.T, eng *Engine, p *Principal, threadID string, want ...threadv2.ThreadStatus) *threadv2.ThreadProjection {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		proj, err := eng.GetThread(context.Background(), p, threadID)
		if err == nil {
			for _, w := range want {
				if proj.Thread.Status == w {
					return proj
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	proj, _ := eng.GetThread(context.Background(), p, threadID)
	t.Fatalf("timed out waiting for thread %s status %v (current=%v)", threadID, want, proj.GetThread().GetStatus())
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
	var alt *threadv2.ProviderInstance
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
	_, err = eng.UpsertProviderInstance(ctx, owner, &threadv2.ProviderInstanceConfig{
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

	// Idempotent LaunchThread with same command_id returns identical thread_id & run_id.
	req := &threadv2.LaunchThreadRequest{
		CommandId:     "cmd_launch_1",
		ProjectId:     "demo",
		InitialPrompt: "say Hello world",
	}
	r1, err := eng.LaunchThread(ctx, owner, req)
	if err != nil {
		t.Fatalf("LaunchThread #1: %v", err)
	}
	r2, err := eng.LaunchThread(ctx, owner, req)
	if err != nil {
		t.Fatalf("LaunchThread #2 (replay): %v", err)
	}
	if r1.Thread.ThreadId != r2.Thread.ThreadId || r1.InitialRun.RunId != r2.InitialRun.RunId {
		t.Fatalf("idempotent replay created duplicate thread/run: %v vs %v", r1.Thread.ThreadId, r2.Thread.ThreadId)
	}
	// Reusing command_id for a different RPC method must reject with InvalidArgument.
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		CommandId: "cmd_launch_1",
		ThreadId:  r1.Thread.ThreadId,
		Text:      "hi",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected InvalidArgument on cross-method command_id reuse, got %v", err)
	}
	waitForThreadStatus(t, eng, owner, r1.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
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
	if _, err := eng.LaunchThread(ctx, extGrant, &threadv2.LaunchThreadRequest{ProjectId: "secret-proj"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied on secret-proj, got %v", err)
	}
	if _, err := eng.LaunchThread(ctx, extGrant, &threadv2.LaunchThreadRequest{
		ProjectId:      "allowed-proj",
		ModelSelection: &threadv2.ModelSelection{InstanceId: "alt-agent"},
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied on alt-agent, got %v", err)
	}

	// 2. Grant requesting FULL_ACCESS is clamped to AUTO_EDITS!
	//    Workspace file edit auto-succeeds, while `npm install` command pauses for TOOL_APPROVAL.
	launched, err := eng.LaunchThread(ctx, extGrant, &threadv2.LaunchThreadRequest{
		ProjectId:   "allowed-proj",
		RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: strings.Join([]string{
			"write auto_allowed.txt|hello from auto_edits",
			"shell npm install leftpad|installed 1 pkg",
			"shell npm install rightpad|installed 2nd pkg",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("LaunchThread: %v", err)
	}
	if launched.Thread.EffectiveRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("EffectiveRuntimeMode = %v, want AUTO_EDITS", launched.Thread.EffectiveRuntimeMode)
	}

	proj := waitForThreadStatus(t, eng, extGrant, launched.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	reqID := proj.Thread.PendingRequestId
	if reqID == "" {
		t.Fatalf("expected pending request ID")
	}

	// 3. External Grant MUST NOT be able to self-approve TOOL_APPROVAL!
	_, err = eng.RespondRuntimeRequest(ctx, extGrant, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:  launched.Thread.ThreadId,
		RequestId: reqID,
		Decision:  threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied when external grant attempts self-approval, got %v", err)
	}

	// 4. Owner approves with ACCEPT_FOR_THREAD ("cmd:npm install"):
	//    The second `npm install rightpad` command in the same turn must auto-allow WITHOUT pausing again!
	_, err = eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		CommandId: "approve_npm",
		ThreadId:  launched.Thread.ThreadId,
		RequestId: reqID,
		Decision:  threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_THREAD,
	})
	if err != nil {
		t.Fatalf("Owner approve: %v", err)
	}

	finalProj := waitForThreadStatus(t, eng, owner, launched.Thread.ThreadId, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if len(finalProj.Requests) != 1 {
		t.Fatalf("expected second npm install to auto-allow via thread rule, got %d requests", len(finalProj.Requests))
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
	launched, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ProjectId:       "proj",
		RuntimeMode:     identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
		InteractionMode: threadv2.InteractionMode_INTERACTION_MODE_PLAN,
		InitialPrompt: strings.Join([]string{
			"propose-plan v2 Refactor|1. Build kernel\\n2. Test everything",
			"ask db_choice|Database|Which DB?|SQLite,Postgres",
			"ask-secret PROD_TOKEN|Deploy verification",
			"write plan_implemented.txt|built after plan approval",
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := launched.Thread.ThreadId

	// 1. Wait for PLAN_REVIEW and approve it -> thread atomically switches PLAN -> CODE!
	p1 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:    tid,
		RequestId:   p1.Thread.PendingRequestId,
		ApprovePlan: true,
	})
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	// 2. Wait for USER_INPUT and answer with option + custom text + attachment.
	p2 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:  tid,
		RequestId: p2.Thread.PendingRequestId,
		Answers: map[string]*threadv2.QuestionAnswer{
			"db_choice": {
				SelectedOptions: []string{"SQLite"},
				CustomText:      "WAL mode",
				Attachments:     []*threadv2.AttachmentRef{blobRef},
			},
		},
	})
	if err != nil {
		t.Fatalf("Answer USER_INPUT: %v", err)
	}

	// 3. Wait for SECRET_INPUT and provide a sensitive token.
	const topSecret = "sec_99887766_NEVER_PERSIST_ME"
	p3 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	_, err = eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:    tid,
		RequestId:   p3.Thread.PendingRequestId,
		SecretValue: topSecret,
	})
	if err != nil {
		t.Fatalf("Provide SECRET_INPUT: %v", err)
	}

	// 4. Wait for TOOL_APPROVAL on write (since RuntimeMode is SUPERVISED, and InteractionMode is now CODE!),
	//    and DECLINE it with steering feedback!
	p4 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	if p4.Thread.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("expected InteractionMode to switch to CODE after plan approval, got %v", p4.Thread.InteractionMode)
	}
	_, err = eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:  tid,
		RequestId: p4.Thread.PendingRequestId,
		Decision:  threadv2.ApprovalDecision_APPROVAL_DECISION_DECLINE,
		Feedback:  "use another filename instead",
	})
	if err != nil {
		t.Fatalf("Decline write: %v", err)
	}

	finalProj := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	var assistantText string
	for _, it := range finalProj.Items {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
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
		raw, err := os.ReadFile(filepath.Join(dataDir, "threads_v2.db"+suffix))
		if err == nil && strings.Contains(string(raw), topSecret) {
			t.Fatalf("SECURITY VIOLATION: secret value persisted in threads_v2.db%s!", suffix)
		}
	}
}

func TestSteerQueueReorderEditCancelPromoteAndForceStop(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	owner := OwnerPrincipal("dev_owner", "Owner")
	launched, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		InitialPrompt: "think starting_turn\nsleep 350ms\nsay turn1_done",
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := launched.Thread.ThreadId

	// Wait until session has actively entered the turn (emitted `think`) before steering!
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := eng.GetThread(ctx, owner, tid)
		if p.GetThread().GetActiveProviderThreadId() != "" && len(p.GetItems()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Live mid-turn STEER injects into Run 1!
	steerRes, err := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid,
		Text:     "focus on edge cases",
		Intent:   threadv2.DispatchIntent_DISPATCH_INTENT_STEER,
	})
	if err != nil || !steerRes.SteeredActiveRun {
		t.Fatalf("expected live steer on active run, err=%v res=%+v", err, steerRes)
	}

	// 2. Enqueue 3 follow-up messages while Run 1 is running.
	q1, _ := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "say q1_original", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	q2, _ := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "say q2_cancel_me", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	q3, _ := eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "say q3_move_first", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})

	// Edit q1, Cancel q2, Reorder q3 before q1!
	if _, err := eng.ManageQueue(ctx, owner, &threadv2.ManageQueueRequest{
		ThreadId: tid, Operation: threadv2.QueueOperation_QUEUE_OPERATION_EDIT, RunId: q1.Run.RunId, Text: "say q1_edited",
	}); err != nil {
		t.Fatalf("Edit queue: %v", err)
	}
	if _, err := eng.ManageQueue(ctx, owner, &threadv2.ManageQueueRequest{
		ThreadId: tid, Operation: threadv2.QueueOperation_QUEUE_OPERATION_CANCEL, RunId: q2.Run.RunId,
	}); err != nil {
		t.Fatalf("Cancel queue: %v", err)
	}
	mq, err := eng.ManageQueue(ctx, owner, &threadv2.ManageQueueRequest{
		ThreadId: tid, Operation: threadv2.QueueOperation_QUEUE_OPERATION_REORDER, RunId: q3.Run.RunId, BeforeRunId: q1.Run.RunId,
	})
	if err != nil {
		t.Fatalf("Reorder queue: %v", err)
	}
	if len(mq.QueuedRuns) != 2 || mq.QueuedRuns[0].RunId != q3.Run.RunId || mq.QueuedRuns[1].RunId != q1.Run.RunId {
		t.Fatalf("unexpected queue order: %+v", mq.QueuedRuns)
	}

	// Wait for Run 1 + q3 + q1 to drain automatically!
	proj := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if proj.Thread.RunCount != 3 {
		t.Fatalf("expected 3 completed runs, got %d", proj.Thread.RunCount)
	}
	// Verify q3 ran as Ordinal 2 and q1_edited ran as Ordinal 3!
	var ordinals []string
	for _, r := range proj.Runs {
		if r.Status == threadv2.RunStatus_RUN_STATUS_COMPLETED {
			ordinals = append(ordinals, r.Prompt)
		}
	}
	if len(ordinals) != 3 || ordinals[1] != "say q3_move_first" || ordinals[2] != "say q1_edited" {
		t.Fatalf("unexpected run execution order: %v", ordinals)
	}

	// 3. Test unresponsive `hang` turn + queued follow-up + `InterruptRun(Force=true, HoldQueue=true)` + `RESUME_QUEUE`!
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "hang", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_START,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_RUNNING)
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "say after_force_stop", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Polite interrupt will not stop `hang`; Force=true with HoldQueue=true kills it and holds queue!
	if _, err := eng.InterruptRun(ctx, owner, &threadv2.InterruptRunRequest{
		ThreadId: tid, Force: true, HoldQueue: true,
	}); err != nil {
		t.Fatalf("InterruptRun(Force): %v", err)
	}
	heldProj := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_QUEUED)
	if !heldProj.Thread.QueueHeld || heldProj.Thread.QueuedRunCount != 1 {
		t.Fatalf("expected held queue with 1 run, got held=%v count=%d", heldProj.Thread.QueueHeld, heldProj.Thread.QueuedRunCount)
	}

	// Resume queue -> drains `after_force_stop`!
	if _, err := eng.ManageQueue(ctx, owner, &threadv2.ManageQueueRequest{
		ThreadId: tid, Operation: threadv2.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE,
	}); err != nil {
		t.Fatalf("RESUME_QUEUE: %v", err)
	}
	waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
}

func TestTransitionsIdleReapCrossProviderHandoffRollbackAndFork(t *testing.T) {
	ctx := context.Background()
	eng, drv1, _ := openTestEngine(t, t.TempDir(), 40*time.Millisecond)
	defer eng.Close()

	repo := initGitProject(t)
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")

	// Launch in isolated git worktree!
	launched, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ProjectId: "repo",
		ModelSelection: &threadv2.ModelSelection{
			InstanceId: "scripted",
			Model:      "scripted-fast",
			Options: map[string]*threadv2.OptionValue{
				"reasoningEffort": {Kind: &threadv2.OptionValue_StringValue{StringValue: "medium"}},
			},
		},
		InitialPrompt: "write step1.txt|step1_v1\ninspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := launched.Thread.ThreadId
	wtPath := launched.Thread.Workspace.WorktreePath
	if wtPath == "" || wtPath == repo {
		t.Fatalf("expected isolated worktree path, got %q", wtPath)
	}
	waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if drv1.StartCount() != 1 {
		t.Fatalf("expected 1 session spawn, got %d", drv1.StartCount())
	}

	// 1. Hot in-session model + option switch WITHOUT spawning a 2nd process!
	cfgRes, err := eng.UpdateThreadConfig(ctx, owner, &threadv2.UpdateThreadConfigRequest{
		ThreadId: tid,
		ModelSelection: &threadv2.ModelSelection{
			InstanceId: "scripted",
			Model:      "scripted-pro",
			Options: map[string]*threadv2.OptionValue{
				"reasoningEffort": {Kind: &threadv2.OptionValue_StringValue{StringValue: "high"}},
				"fastMode":        {Kind: &threadv2.OptionValue_BoolValue{BoolValue: true}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfgRes.TransitionApplied != string(TransitionSwitchInSession) {
		t.Fatalf("TransitionApplied = %q, want switch_in_session", cfgRes.TransitionApplied)
	}
	_, _ = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "write step2.txt|step2_v1\ninspect",
	})
	waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if drv1.StartCount() != 1 {
		t.Fatalf("hot switch must reuse live session, StartCount=%d", drv1.StartCount())
	}

	// 2. Wait for IdleTTL reaper to close idle session -> next run seamlessly does SpawnAndResume!
	time.Sleep(100 * time.Millisecond)
	eng.Supervisor().ReapIdle(time.Now().Add(time.Second))
	if eng.Supervisor().ActiveCount() != 0 {
		t.Fatalf("expected idle session to be reaped")
	}
	_, _ = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "inspect",
	})
	waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if drv1.StartCount() != 2 {
		t.Fatalf("expected SpawnAndResume after idle reap, StartCount=%d", drv1.StartCount())
	}

	// 3. Switch to `alt-agent` (cross-provider) -> must build ContextHandoff including modified files!
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId:       tid,
		ModelSelection: &threadv2.ModelSelection{InstanceId: "alt-agent", Model: "alt-1"},
		Text:           "inspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	proj := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	var sawHandoff bool
	for _, it := range proj.Items {
		if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_HANDOFF && strings.Contains(it.Text, "step1.txt") && strings.Contains(it.Text, "step2.txt") {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("expected HANDOFF item summarizing prior turns and modified files")
	}

	// 4. Fork thread at Run #1!
	forkRes, err := eng.ForkThread(ctx, owner, &threadv2.ForkThreadRequest{
		SourceThreadId: tid,
		UpToRunOrdinal: 1,
		Title:          "Forked After Step 1",
	})
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	forkProj, err := eng.GetThread(ctx, owner, forkRes.Thread.ThreadId)
	if err != nil || forkProj.Thread.RunCount != 1 {
		t.Fatalf("expected forked thread to have 1 run, got %v (err=%v)", forkProj.GetThread().GetRunCount(), err)
	}

	// 5. Rollback original thread to Checkpoint #1 with RestoreFiles=true -> step2.txt disappears, step1.txt remains!
	ckpts, err := eng.ListCheckpoints(ctx, owner, tid)
	if err != nil {
		t.Fatal(err)
	}
	var cp1 *threadv2.Checkpoint
	for _, c := range ckpts {
		if c.RunOrdinal == 1 {
			cp1 = c
		}
	}
	if cp1 == nil {
		t.Fatalf("checkpoint #1 not found in %+v", ckpts)
	}
	if _, err := eng.RollbackCheckpoint(ctx, owner, &threadv2.RollbackCheckpointRequest{
		ThreadId:     tid,
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

	// Start a thread that pauses on USER_INPUT, and enqueue a follow-up message behind it!
	launched, err := eng1.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		InitialPrompt: "ask q1|Env|Target?|staging,prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := launched.Thread.ThreadId
	p1 := waitForThreadStatus(t, eng1, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	reqID := p1.Thread.PendingRequestId

	_, err = eng1.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid, Text: "say queued_survived_crash", Intent: threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Abruptly close eng1 while Run 1 is WAITING_REQUEST and Run 2 is QUEUED!
	_ = eng1.Close()

	// Reopen Engine on the same DataDir -> recoverOrphans runs!
	eng2, _, _ := openTestEngine(t, dataDir, time.Minute)
	defer eng2.Close()

	recProj, err := eng2.GetThread(ctx, owner, tid)
	if err != nil {
		t.Fatal(err)
	}
	if recProj.Thread.Status != threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST {
		t.Fatalf("expected WAITING_REQUEST preserved for resumable USER_INPUT, got %v", recProj.Thread.Status)
	}
	if recProj.Thread.QueuedRunCount != 1 {
		t.Fatalf("expected queued run to survive daemon restart, got %d", recProj.Thread.QueuedRunCount)
	}
	var pendingReq *threadv2.RuntimeRequest
	for _, r := range recProj.Requests {
		if r.RequestId == reqID {
			pendingReq = r
		}
	}
	if pendingReq == nil || pendingReq.ResponseMode != threadv2.ResponseMode_RESPONSE_MODE_RESUME_TURN {
		t.Fatalf("expected pending request downgraded to RESUME_TURN, got %+v", pendingReq)
	}

	// SubscribeThread from afterSeq=0 replays full event log seamlessly!
	subCtx, cancelSub := context.WithCancel(ctx)
	var replayed int
	go func() {
		_ = eng2.SubscribeThread(subCtx, owner, tid, 0, func(ev *threadv2.ThreadEvent) error {
			replayed++
			if ev.Type == "thread.recovered" {
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
	_, err = eng2.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:  tid,
		RequestId: reqID,
		Answers: map[string]*threadv2.QuestionAnswer{
			"q1": {SelectedOptions: []string{"staging"}},
		},
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest after crash recovery: %v", err)
	}
	afterProj := waitForThreadStatus(t, eng2, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if afterProj.Thread.QueuedRunCount != 0 {
		t.Fatalf("expected queued run to drain after answering recovered request, remaining=%d", afterProj.Thread.QueuedRunCount)
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
	launched, err := eng.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{
		ProjectId:       "proj",
		InteractionMode: threadv2.InteractionMode_INTERACTION_MODE_PLAN,
		InitialPrompt: strings.Join([]string{
			"propose-artifact Implementation Plan|true|# Plan v1\\n\\n1. Draft architecture",
			"say Click Proceed when ready.",
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := launched.Thread.ThreadId

	p1 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	if p1.Thread.ActiveRunId != "" {
		t.Fatalf("expected ActiveRunId to be empty for post-turn RESUME_TURN plan review, got %q", p1.Thread.ActiveRunId)
	}
	if len(p1.Runs) != 1 || p1.Runs[0].Status != threadv2.RunStatus_RUN_STATUS_COMPLETED {
		t.Fatalf("expected Run 1 COMPLETED, got %+v", p1.Runs)
	}
	req1ID := p1.Thread.PendingRequestId
	var req1 *threadv2.RuntimeRequest
	for _, r := range p1.Requests {
		if r.RequestId == req1ID {
			req1 = r
		}
	}
	if req1 == nil ||
		req1.Kind != threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW ||
		req1.ResponseMode != threadv2.ResponseMode_RESPONSE_MODE_RESUME_TURN ||
		!strings.Contains(req1.GetPlanReview().GetMarkdown(), "# Plan v1") {
		t.Fatalf("unexpected post-turn PLAN_REVIEW request: %+v", req1)
	}

	// 2. Ask for revision (ApprovePlan=false) -> stays in PLAN mode and dispatches continuation Run 2!
	revRes, err := eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:    tid,
		RequestId:   req1ID,
		ApprovePlan: false,
		Feedback:    "include rollback verification",
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest(revise): %v", err)
	}
	if revRes.Thread.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_PLAN {
		t.Fatalf("expected PLAN mode retained on revision, got %v", revRes.Thread.InteractionMode)
	}
	p2 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if len(p2.Runs) != 2 || p2.Runs[1].Prompt != "Please revise the plan: include rollback verification" {
		t.Fatalf("expected Run 2 revision prompt, got runs=%+v", p2.Runs)
	}

	// 3. Run 3 emits both a RequestFeedback=true revised plan AND a RequestFeedback=false deliverable;
	//    only the RequestFeedback=true artifact parks the thread, and ApprovePlan=true switches PLAN -> CODE!
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid,
		Text: strings.Join([]string{
			"propose-artifact Implementation Plan|true|# Plan v2\\n\\n1. With rollback verification",
			"propose-artifact Notes|false|# Notes\\n\\nSupplementary walkthrough",
		}, "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	p3 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	req3ID := p3.Thread.PendingRequestId
	appRes, err := eng.RespondRuntimeRequest(ctx, owner, &threadv2.RespondRuntimeRequestRequest{
		ThreadId:    tid,
		RequestId:   req3ID,
		ApprovePlan: true,
	})
	if err != nil {
		t.Fatalf("RespondRuntimeRequest(approve): %v", err)
	}
	if appRes.Thread.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("expected RespondRuntimeRequest response Thread to reflect CODE mode, got %v", appRes.Thread.InteractionMode)
	}
	p4 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if p4.Thread.InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("expected thread in CODE mode, got %v", p4.Thread.InteractionMode)
	}
	if len(p4.Runs) != 4 || p4.Runs[3].Prompt != "Plan approved. Proceed with implementation in Code mode." || p4.Runs[3].InteractionMode != threadv2.InteractionMode_INTERACTION_MODE_CODE {
		t.Fatalf("unexpected Run 4 continuation after plan approval: %+v", p4.Runs)
	}

	// 4. Superseding: when parked on a post-turn RESUME_TURN proposal (e.g. /learn proposal in CODE mode),
	//    sending a normal message in the composer dismisses the pending request and runs immediately!
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid,
		Text:     "propose-artifact Learning Proposal|true|# Learnings\\n\\nPersist rule X",
	})
	if err != nil {
		t.Fatal(err)
	}
	p5 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST)
	req5ID := p5.Thread.PendingRequestId
	_, err = eng.DispatchMessage(ctx, owner, &threadv2.DispatchMessageRequest{
		ThreadId: tid,
		Text:     "say superseded_cleanly",
	})
	if err != nil {
		t.Fatalf("DispatchMessage while parked on RESUME_TURN request: %v", err)
	}
	p6 := waitForThreadStatus(t, eng, owner, tid, threadv2.ThreadStatus_THREAD_STATUS_IDLE)
	if p6.Thread.PendingRequestId != "" || len(p6.Runs) != 6 {
		t.Fatalf("expected PendingRequestId cleared and Run 6 completed, pending=%q runs=%d", p6.Thread.PendingRequestId, len(p6.Runs))
	}
	var req5After *threadv2.RuntimeRequest
	for _, r := range p6.Requests {
		if r.RequestId == req5ID {
			req5After = r
		}
	}
	if req5After == nil || req5After.Status != threadv2.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_DISMISSED {
		t.Fatalf("expected superseded request %s to be DISMISSED, got %+v", req5ID, req5After)
	}
}
