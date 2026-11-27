package kernel

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
)

func openLivenessTestEngine(t *testing.T, cfg Config) (*Engine, *Principal, string) {
	t.Helper()
	repo := initGitProject(t)
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
	}
	if cfg.MachineID == "" {
		cfg.MachineID = "m_liveness"
	}
	if cfg.IdleTTL == 0 {
		cfg.IdleTTL = time.Minute
	}
	eng, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil)
	owner := OwnerPrincipal("dev_owner", "Owner")
	return eng, owner, repo
}

// 1. Streaming Reasoning and AssistantMessage items settle Streaming=false when
// deltas go silent (e.g. while waiting on an async tool or generating a large
// tool-call payload), preventing stuck spinning cursors.
func TestStreamingLeaseSettlesIdleDeltas(t *testing.T) {
	ctx := context.Background()
	drv := scripted.NewDriver()
	eng, owner, _ := openLivenessTestEngine(t, Config{
		Drivers:          map[string]runtime.Driver{"scripted": drv},
		IncludeScripted:  true,
		StreamIdleSettle: 40 * time.Millisecond,
		QuietThreshold:   80 * time.Millisecond,
		StallTimeout:     5 * time.Second,
		WatchdogInterval: 15 * time.Millisecond,
	})
	defer eng.Close()

	res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "repo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "think planning big html\nsleep 250ms\nsay running long build\nsleep 250ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := res.Session.SessionId

	// Wait for Reasoning to appear and settle its Streaming flag while the turn is still RUNNING.
	deadline := time.Now().Add(3 * time.Second)
	sawSettledRsn := false
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		for _, it := range p.GetItems() {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_REASONING && !it.Streaming && it.Status == "completed" &&
				p.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
				sawSettledRsn = true
				break
			}
		}
		if sawSettledRsn {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawSettledRsn {
		t.Fatal("expected REASONING item to settle Streaming=false during mid-turn silence")
	}

	// Wait for AssistantMessage to appear and settle its Streaming flag while the turn is still RUNNING.
	sawSettledMsg := false
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		for _, it := range p.GetItems() {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && !it.Streaming && it.Status == "completed" &&
				p.GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
				sawSettledMsg = true
				break
			}
		}
		if sawSettledMsg {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawSettledMsg {
		t.Fatal("expected ASSISTANT_MESSAGE item to settle Streaming=false during mid-turn silence")
	}

	settled := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if settled.GetSession().GetResult() != "running long build" {
		t.Fatalf("unexpected final result %q", settled.GetSession().GetResult())
	}
}

// 2. Active child processes surface in SessionProgress.CurrentTool without setting QuietSince,
// and active CPU ticks (slow 60KB tool-call generation) set QuietSince without triggering FAILURE_KIND_STALLED.
func TestQuietIntrospectionDistinguishesChildExecAndActiveCPUFromStall(t *testing.T) {
	ctx := context.Background()
	var (
		mu      sync.Mutex
		running []string
		stderr  string
		cpu     atomic.Uint64
	)
	drv := scripted.NewDriver()
	drv.DiagnoseFunc = func(_ string) runtime.Diagnosis {
		mu.Lock()
		r := append([]string(nil), running...)
		se := stderr
		mu.Unlock()
		return runtime.Diagnosis{
			Running:  r,
			Stderr:   se,
			CPUTicks: cpu.Add(100),
			HasCPU:   true,
		}
	}

	eng, owner, _ := openLivenessTestEngine(t, Config{
		Drivers:          map[string]runtime.Driver{"scripted": drv},
		IncludeScripted:  true,
		StreamIdleSettle: 30 * time.Millisecond,
		QuietThreshold:   50 * time.Millisecond,
		StallTimeout:     160 * time.Millisecond,
		WatchdogInterval: 15 * time.Millisecond,
	})
	defer eng.Close()

	// Phase A: child OS process running ("cargo build --workspace").
	mu.Lock()
	running = []string{"cargo build --workspace"}
	mu.Unlock()

	res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "repo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "sleep 450ms\nsay finished_slow_work",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := res.Session.SessionId

	deadline := time.Now().Add(2 * time.Second)
	sawChildTool := false
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		if p.GetSession().GetProgress().GetCurrentTool() == "cargo build --workspace" {
			if p.GetSession().GetProgress().GetQuietSince() != nil {
				t.Fatal("QuietSince must stay nil while a child OS process is actively running")
			}
			sawChildTool = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawChildTool {
		t.Fatal("expected live child command to surface in SessionProgress.CurrentTool")
	}

	// Phase B: child finishes, now provider is silently streaming a huge tool call (CPU advancing, stderr retry hint).
	mu.Lock()
	running = nil
	stderr = "attempt 1 failed (HTTP 503 Unavailable), retrying in 4s"
	mu.Unlock()

	sawQuietAndRetry := false
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		prog := p.GetSession().GetProgress()
		if prog.GetQuietSince() != nil && prog.GetCurrentTool() != "" && prog.GetCurrentTool() != "cargo build --workspace" {
			sawQuietAndRetry = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawQuietAndRetry {
		t.Fatal("expected QuietSince and transient stderr retry hint to surface while waiting on model")
	}

	// Even though total sleep (450ms) exceeds StallTimeout (160ms), advancing CPU ticks keep it alive!
	settled := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if settled.GetSession().GetProgress().GetLastFailure() != nil {
		t.Fatalf("CPU-active slow stream must not stall, got failure: %+v", settled.GetSession().GetProgress().GetLastFailure())
	}
	if settled.GetSession().GetProgress().GetQuietSince() != nil {
		t.Fatal("QuietSince must be cleared once turn completes")
	}
}

// 3. Truly wedged turn (zero events, zero child processes, zero CPU progress) fails with
// FAILURE_KIND_STALLED, preserves workspace checkpoint, and automatically advances queued runs.
func TestStallWatchdogTripsCheckpointAndAdvancesQueuedRun(t *testing.T) {
	ctx := context.Background()
	drv := scripted.NewDriver()
	drv.DiagnoseFunc = func(_ string) runtime.Diagnosis {
		return runtime.Diagnosis{CPUTicks: 500, HasCPU: true} // frozen CPU counter
	}

	eng, owner, _ := openLivenessTestEngine(t, Config{
		Drivers:          map[string]runtime.Driver{"scripted": drv},
		IncludeScripted:  true,
		StreamIdleSettle: 25 * time.Millisecond,
		QuietThreshold:   40 * time.Millisecond,
		StallTimeout:     130 * time.Millisecond,
		WatchdogInterval: 15 * time.Millisecond,
	})
	defer eng.Close()

	// Subcase A: single wedged turn with no queue -> settles IDLE with FAILURE_KIND_STALLED and saved checkpoint.
	s1, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "repo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "write partial.txt|saved_before_stall\nhang",
	})
	if err != nil {
		t.Fatal(err)
	}
	settled1 := waitForSessionStatus(t, eng, owner, s1.Session.SessionId, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if settled1.GetSession().GetProgress().GetLastFailure().GetKind() != sessionv1.FailureKind_FAILURE_KIND_STALLED {
		t.Fatalf("expected FAILURE_KIND_STALLED, got %+v", settled1.GetSession().GetProgress().GetLastFailure())
	}
	if len(settled1.GetCheckpoints()) < 2 {
		t.Fatalf("expected baseline + stalled-turn checkpoint, got %d checkpoints", len(settled1.GetCheckpoints()))
	}

	// Subcase B: wedged turn with a queued follow-up -> marks wedged run FAILED(STALLED) and runs queued follow-up on fresh process!
	s2, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "repo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "hang",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid2 := s2.Session.SessionId
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid2,
		Text:      "say recovered_after_stall",
		Intent:    sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	}); err != nil {
		t.Fatal(err)
	}

	settled2 := waitForSessionStatus(t, eng, owner, sid2, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if settled2.GetSession().GetResult() != "recovered_after_stall" {
		t.Fatalf("expected queued follow-up to run and recover after stall, got result=%q err=%q", settled2.GetSession().GetResult(), settled2.GetSession().GetError())
	}
	if settled2.GetRuns()[0].GetFailure().GetKind() != sessionv1.FailureKind_FAILURE_KIND_STALLED {
		t.Fatalf("expected run 1 to record FAILURE_KIND_STALLED, got %+v", settled2.GetRuns()[0].GetFailure())
	}
}

// 4. Direct DISPATCH_INTENT_STEER on MidTurnSteer=false (ACP) and dispatching while suspended on
// WAITING_INPUT (ask_question) both preempt cleanly instead of deadlocking in the queue.
func TestDirectSteerOnNonLiveSteerAndWaitingInputUnblock(t *testing.T) {
	ctx := context.Background()
	acpLike := scripted.NewNamed("acp")
	acpLike.DisableSteer = true

	eng, owner, _ := openLivenessTestEngine(t, Config{
		Drivers:         map[string]runtime.Driver{"acp": acpLike},
		IncludeScripted: false,
	})
	defer eng.Close()
	_, _ = eng.UpsertProviderInstance(ctx, owner, &sessionv1.ProviderInstanceConfig{
		InstanceId:  "antigravity",
		Driver:      "acp",
		AcpFlavor:   "antigravity",
		DisplayName: "Antigravity",
		Enabled:     true,
	})
	sel := &sessionv1.ModelSelection{InstanceId: "antigravity", Model: "gemini-4-carbon"}

	// 4a. Direct DISPATCH_INTENT_STEER jumps ahead of queued items and cancels active ACP turn.
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:      "repo",
		ModelSelection: sel,
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt:  "sleep 15s\nsay never_reaches_end",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := launched.Session.SessionId
	_, _ = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say queued_later", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_QUEUE,
	})
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid, Text: "say steered_immediately", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_STEER,
	}); err != nil {
		t.Fatalf("direct DISPATCH_INTENT_STEER failed: %v", err)
	}
	settled := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	var completed []string
	for _, r := range settled.GetRuns() {
		if r.Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED {
			completed = append(completed, r.Prompt)
		}
	}
	if len(completed) != 2 || completed[0] != "say steered_immediately" || completed[1] != "say queued_later" {
		t.Fatalf("expected [say steered_immediately, say queued_later], got %v", completed)
	}

	// 4b. Session blocked in WAITING_INPUT (mid-turn ask_question): sending a message with AUTO
	// cancels the blocked gate and runs the new prompt instead of sitting deadlocked in QUEUE.
	asking, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:      "repo",
		ModelSelection: sel,
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt:  "ask Scope|Which layout?|Compact,Spacious\nsay after_question",
	})
	if err != nil {
		t.Fatal(err)
	}
	sidAsk := asking.Session.SessionId
	waitForSessionStatus(t, eng, owner, sidAsk, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)

	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sidAsk, Text: "say skip_question_do_this", Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_AUTO,
	}); err != nil {
		t.Fatalf("DispatchMessage(AUTO) during WAITING_INPUT failed: %v", err)
	}
	settledAsk := waitForSessionStatus(t, eng, owner, sidAsk, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if settledAsk.GetSession().GetResult() != "skip_question_do_this" {
		t.Fatalf("expected WAITING_INPUT turn to unblock and run new message, got result=%q", settledAsk.GetSession().GetResult())
	}
}
