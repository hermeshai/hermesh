package kernel_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func waitForSessionPred(t *testing.T, eng *kernel.Engine, p *kernel.Principal, sessionID string, pred func(*sessionv1.Session) bool) *sessionv1.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		proj, err := eng.GetSession(context.Background(), p, sessionID)
		if err == nil && pred(proj.GetSession()) {
			return proj.GetSession()
		}
		time.Sleep(15 * time.Millisecond)
	}
	proj, _ := eng.GetSession(context.Background(), p, sessionID)
	t.Fatalf("timed out waiting for session %s; current=%+v", sessionID, proj.GetSession())
	return nil
}

func TestTitleGenerationCrossLocaleAndShortCircuit(t *testing.T) {
	var calls atomic.Int32
	var lastPrompt atomic.Value
	drv := scripted.NewDriver()
	drv.TitleFunc = func(_ context.Context, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
		calls.Add(1)
		lastPrompt.Store(req.Prompt)
		if strings.Contains(req.Prompt, "Target locale: zh-CN") {
			return runtime.TitleGenResult{Title: "修复 WebSocket 重连竞态", Provisional: false}, nil
		}
		if strings.Contains(req.Prompt, "Target locale: en") {
			return runtime.TitleGenResult{Title: "Fix Tray Icon Flicker", Provisional: false}, nil
		}
		return runtime.TitleGenResult{Title: "Generic Title", Provisional: false}, nil
	}

	repo := initTestRepo(t)
	eng, err := kernel.Open(kernel.Config{
		DataDir:         t.TempDir(),
		Drivers:         map[string]runtime.Driver{"scripted": drv},
		IncludeScripted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.SyncWorkspace("demo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil); err != nil {
		t.Fatal(err)
	}
	owner := kernel.OwnerPrincipal("owner", "Owner")

	// Case 1: Clean Chinese prompt on zh-CN client -> 0ms short-circuit, zero model calls!
	s1, err := eng.StartSession(context.Background(), owner, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_sc_zh",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		InitialPrompt:  "修复登录页按钮错位",
		Locale:         "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s1.Session.Title != "修复登录页按钮错位" || s1.Session.TitleGenerating || s1.Session.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("expected immediate final short-circuit title, got %+v", s1.Session)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 title generator calls on short-circuit, got %d", calls.Load())
	}

	// Case 2: English prompt on zh-CN client -> MUST run Pass 1 and produce Chinese title!
	s2, err := eng.StartSession(context.Background(), owner, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_en_to_zh",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		InitialPrompt:  "Fix WebSocket reconnect race",
		Locale:         "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	got2 := waitForSessionPred(t, eng, owner, s2.Session.SessionId, func(s *sessionv1.Session) bool {
		return !s.TitleGenerating && s.Title == "修复 WebSocket 重连竞态"
	})
	if got2.TitleLocale != "zh-CN" || got2.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("unexpected session state: %+v", got2)
	}
	// Nearby active titles must include s1's title for disambiguation!
	if p, _ := lastPrompt.Load().(string); !strings.Contains(p, "修复登录页按钮错位") {
		t.Fatalf("expected nearby active title in prompt, got:\n%s", p)
	}

	// Case 3: Chinese prompt on English (en) client -> MUST run Pass 1 and produce English title!
	s3, err := eng.StartSession(context.Background(), owner, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_zh_to_en",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		InitialPrompt:  "修复托盘图标闪烁",
		Locale:         "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionPred(t, eng, owner, s3.Session.SessionId, func(s *sessionv1.Session) bool {
		return !s.TitleGenerating && s.Title == "Fix Tray Icon Flicker"
	})

	// Case 4: Switching client language to zh-CN and clicking Regenerate Title switches title language!
	_, err = eng.UpdateSession(context.Background(), owner, &sessionv1.UpdateSessionRequest{
		CommandId:       "cmd_regen_zh",
		SessionId:       s3.Session.SessionId,
		RegenerateTitle: true,
		Locale:          "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	gotRegen := waitForSessionPred(t, eng, owner, s3.Session.SessionId, func(s *sessionv1.Session) bool {
		return !s.TitleGenerating && s.Title == "修复 WebSocket 重连竞态"
	})
	if gotRegen.TitleLocale != "zh-CN" {
		t.Fatalf("expected TitleLocale=zh-CN after regenerate, got %q", gotRegen.TitleLocale)
	}
}

func TestTwoPassProvisionalRefinementBeforeNoticeAndManualRenameGuard(t *testing.T) {
	var mu sync.Mutex
	var refinePrompts []string
	var completedNoticeTitle string
	slowCh := make(chan struct{})

	drv := scripted.NewDriver()
	drv.TitleFunc = func(ctx context.Context, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
		if strings.Contains(req.Prompt, "WAIT_FOR_RENAME_RACE") {
			select {
			case <-slowCh:
				return runtime.TitleGenResult{Title: "Should Be Discarded", Provisional: false}, nil
			case <-ctx.Done():
				return runtime.TitleGenResult{}, ctx.Err()
			}
		}
		if strings.Contains(req.Prompt, "Mode: initial") {
			return runtime.TitleGenResult{Title: "排查挂掉的单测", Provisional: true}, nil
		}
		mu.Lock()
		refinePrompts = append(refinePrompts, req.Prompt)
		mu.Unlock()
		return runtime.TitleGenResult{Title: "修复 reconnect_test.go 重连单测", Provisional: false}, nil
	}

	repo := initTestRepo(t)
	eng, err := kernel.Open(kernel.Config{
		DataDir:         t.TempDir(),
		Drivers:         map[string]runtime.Driver{"scripted": drv},
		IncludeScripted: true,
		OnNotice: func(sess *sessionv1.Session, ev *sessionv1.SessionEvent) {
			if ev.Type == "run.completed" {
				mu.Lock()
				completedNoticeTitle = sess.Title
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.SyncWorkspace("demo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil); err != nil {
		t.Fatal(err)
	}
	owner := kernel.OwnerPrincipal("owner", "Owner")

	// Start a session with a vague prompt ("修一下这个单测") that edits a file during Turn 1!
	started, err := eng.StartSession(context.Background(), owner, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_twopass",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		Workspace:      &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_WORKTREE},
		InitialPrompt:  "say Investigating修一下这个单测\nwrite reconnect_test.go|package main\n\nsay Root cause was WebSocket backoff",
		Locale:         "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for Turn 1 to finish and verify Pass 2 refined the provisional title using touched files
	// BEFORE OnNotice("run.completed") fired!
	finalSess := waitForSessionPred(t, eng, owner, started.Session.SessionId, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && !s.TitleGenerating
	})
	if finalSess.Title != "修复 reconnect_test.go 重连单测" || finalSess.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("expected Pass 2 refined title, got %+v", finalSess)
	}
	mu.Lock()
	noticeTitle := completedNoticeTitle
	prompts := append([]string(nil), refinePrompts...)
	mu.Unlock()
	if noticeTitle != "修复 reconnect_test.go 重连单测" {
		t.Fatalf("expected run.completed notice to carry Pass 2 refined title, got %q", noticeTitle)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "reconnect_test.go") {
		t.Fatalf("expected Pass 2 prompt to include touched file reconnect_test.go, got: %v", prompts)
	}

	// Verify manual rename while Pass 1 is in flight is NEVER overwritten!
	raceSess, err := eng.StartSession(context.Background(), owner, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_race",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		InitialPrompt:  "WAIT_FOR_RENAME_RACE some long ambiguous description that triggers async gen",
		Locale:         "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.UpdateSession(context.Background(), owner, &sessionv1.UpdateSessionRequest{
		CommandId: "cmd_manual_rename",
		SessionId: raceSess.Session.SessionId,
		Title:     "My Custom Locked Title",
	})
	if err != nil {
		t.Fatal(err)
	}
	close(slowCh)
	time.Sleep(50 * time.Millisecond)
	locked := waitForSessionPred(t, eng, owner, raceSess.Session.SessionId, func(s *sessionv1.Session) bool {
		return !s.TitleGenerating
	})
	if locked.Title != "My Custom Locked Title" || !locked.TitleRenamed || locked.TitleState != sessionv1.TitleState_TITLE_STATE_LOCKED {
		t.Fatalf("manual rename was clobbered: %+v", locked)
	}

	// Verify ForkSession gets localized seed and auto-generates a distinct title on first post-fork message!
	forkRes, err := eng.ForkSession(context.Background(), owner, &sessionv1.ForkSessionRequest{
		CommandId:       "cmd_fork_zh",
		SourceSessionId: started.Session.SessionId,
		Locale:          "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(forkRes.Session.Title, "（分支）") || forkRes.Session.TitleRenamed {
		t.Fatalf("expected localized unlocked fork seed, got %+v", forkRes.Session)
	}
	_, err = eng.DispatchMessage(context.Background(), owner, &sessionv1.DispatchMessageRequest{
		CommandId: "cmd_fork_msg1",
		SessionId: forkRes.Session.SessionId,
		Text:      "改用指数退避策略",
		Locale:    "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	forkAfter := waitForSessionPred(t, eng, owner, forkRes.Session.SessionId, func(s *sessionv1.Session) bool {
		return !s.TitleGenerating && s.TitleState != sessionv1.TitleState_TITLE_STATE_SEED
	})
	if strings.HasSuffix(forkAfter.Title, "（分支）") {
		t.Fatalf("expected fork seed to be replaced after first post-fork message, got %q", forkAfter.Title)
	}
}

func TestHerForeignAgentTitleTranslatedToClientLocale(t *testing.T) {
	var calls atomic.Int32
	drv := scripted.NewDriver()
	drv.EnableTitleGen = true
	drv.TitleFunc = func(ctx context.Context, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
		calls.Add(1)
		return runtime.FakeGenerateTitle(req), nil
	}

	repo := initTestRepo(t)
	eng, err := kernel.Open(kernel.Config{
		DataDir:         t.TempDir(),
		Drivers:         map[string]runtime.Driver{"scripted": drv},
		IncludeScripted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.SyncWorkspace("demo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil); err != nil {
		t.Fatal(err)
	}

	her := kernel.GrantPrincipal(&identityv1.Grant{
		GrantId:        "g_her",
		ClientId:       "c_her",
		HerMachineId:   "m_local",
		ProjectIds:     []string{"*"},
		Agents:         []string{"*"},
		TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL,
	})

	// 1. Her passes an English title while client UI locale is zh-CN -> translated in Pass 1!
	foreign, err := eng.StartSession(context.Background(), her, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_her_foreign",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		Title:          "Fix nil pointer panic",
		InitialPrompt:  "say Fix nil pointer panic in auth_handler.go",
		Locale:         "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	settled := waitForSessionPred(t, eng, her, foreign.Session.SessionId, func(s *sessionv1.Session) bool {
		return !s.TitleGenerating
	})
	if settled.Title != "修复 auth_handler.go 问题" || settled.TitleLocale != "zh-CN" {
		t.Fatalf("expected Her foreign title translated to zh-CN, got %+v", settled)
	}

	// 2. Her passes a title already matching zh-CN -> locked immediately with 0 extra LLM calls!
	beforeCalls := calls.Load()
	matching, err := eng.StartSession(context.Background(), her, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_her_matching",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"},
		Title:          "修复登录鉴权崩溃",
		InitialPrompt:  "say Fix auth crash",
		Locale:         "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if matching.Session.Title != "修复登录鉴权崩溃" || matching.Session.TitleState != sessionv1.TitleState_TITLE_STATE_LOCKED || matching.Session.TitleGenerating {
		t.Fatalf("expected matching Her title to stay locked, got %+v", matching.Session)
	}
	if calls.Load() != beforeCalls {
		t.Fatalf("expected 0 extra LLM calls for matching Her title, got %d", calls.Load()-beforeCalls)
	}
}
