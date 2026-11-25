package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// TestInternationalizedSessionTitles verifies end-to-end across real hermeshd,
// Owner LocalAPI, and CLI that:
//  1. Client UI locale governs generated titles even when prompts are in another language.
//  2. Clean short prompts already in the client's UI locale short-circuit at T0.
//  3. Vague prompts refine automatically on Turn 1 completion using touched files.
//  4. Regenerate Title (both RPC and CLI) re-generates in the active client locale
//     and persists across daemon restarts.
func TestInternationalizedSessionTitles(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1", "HERMESH_FAKE_TITLE_GEN=1"}, &localv1.GrantSpec{
		TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL,
	})
	ctx := context.Background()

	waitSettled := func(sessionID string, wantTitle string) *sessionv1.Session {
		t.Helper()
		var got *sessionv1.Session
		eventually(t, 20*time.Second, "session "+sessionID+" titled "+wantTitle, func() bool {
			res, err := w.app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sessionID})
			if err != nil || res.GetProjection().GetSession() == nil {
				return false
			}
			s := res.GetProjection().GetSession()
			got = s
			return s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && !s.TitleGenerating && s.Title == wantTitle
		})
		return got
	}

	// 1. English prompt on a zh-CN client -> Chinese title.
	enPromptToZH, err := w.app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId:     "demo",
		InitialPrompt: "say Fix WebSocket reconnect bug in ws_client.go",
		Locale:        "zh-CN",
	})
	if err != nil {
		t.Fatalf("start en->zh-CN: %v", err)
	}
	s1 := waitSettled(enPromptToZH.Session.SessionId, "修复 ws_client.go 问题")
	if s1.TitleLocale != "zh-CN" || s1.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("unexpected en->zh-CN state: %+v", s1)
	}

	// 2. Chinese prompt on an en client -> English title.
	zhPromptToEN, err := w.app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId:     "demo",
		InitialPrompt: "say 排查 payment_gateway.go 里的超时异常",
		Locale:        "en",
	})
	if err != nil {
		t.Fatalf("start zh->en: %v", err)
	}
	s2 := waitSettled(zhPromptToEN.Session.SessionId, "Fix payment_gateway.go issue")
	if s2.TitleLocale != "en" || s2.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("unexpected zh->en state: %+v", s2)
	}

	// 3. Clean short prompt already in zh-CN -> 0ms short-circuit at T0.
	shortRes, err := w.app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId:     "demo",
		InitialPrompt: "修复登录按钮样式",
		Locale:        "zh-CN",
	})
	if err != nil {
		t.Fatalf("start short-circuit: %v", err)
	}
	if shortRes.Session.Title != "修复登录按钮样式" || shortRes.Session.TitleGenerating || shortRes.Session.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("expected T0 short-circuit, got %+v", shortRes.Session)
	}

	// 4. Two-pass refinement: vague prompt edits token_refresh.go during Turn 1 ->
	// Pass 2 refines the provisional title using Checkpoint.files!
	twoPassRes, err := w.app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId:     "demo",
		RuntimeMode:   identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt: "say 修一下这个报错\nwrite token_refresh.go package auth\nsay done",
		Locale:        "zh-CN",
	})
	if err != nil {
		t.Fatalf("start two-pass: %v", err)
	}
	s4 := waitSettled(twoPassRes.Session.SessionId, "修复 token_refresh.go 问题")
	if s4.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("expected Pass 2 FINAL state, got %+v", s4)
	}

	// 5. CLI `--regenerate-title --locale en` switches the title into English and persists across restart!
	cfgOut := run(t, nil, 30*time.Second, w.bin+"/hermesh", "session", "config", twoPassRes.Session.SessionId, "--socket", w.sock, "--regenerate-title", "--locale", "en", "-o", "json")
	if cfgOut.code != 0 {
		t.Fatalf("cli regenerate-title failed: %+v", cfgOut)
	}
	s5 := waitSettled(twoPassRes.Session.SessionId, "Fix token_refresh.go issue")
	if s5.TitleLocale != "en" || s5.TitleState != sessionv1.TitleState_TITLE_STATE_FINAL {
		t.Fatalf("unexpected regenerated state: %+v", s5)
	}

	w.restartOwner(false)

	var listDoc struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(w.cli(30*time.Second, "session", "list", "-o", "json").stdout), &listDoc); err != nil {
		t.Fatalf("session list json: %v", err)
	}
	byID := map[string]string{}
	for _, s := range listDoc.Sessions {
		byID[s.SessionID] = s.Title
	}
	for id, want := range map[string]string{
		enPromptToZH.Session.SessionId: "修复 ws_client.go 问题",
		zhPromptToEN.Session.SessionId: "Fix payment_gateway.go issue",
		shortRes.Session.SessionId:     "修复登录按钮样式",
		twoPassRes.Session.SessionId:   "Fix token_refresh.go issue",
	} {
		if byID[id] != want {
			t.Fatalf("after restart, session %s title = %q, want %q", id, byID[id], want)
		}
	}
}
