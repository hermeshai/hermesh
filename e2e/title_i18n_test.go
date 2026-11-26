package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/herhost/hertest"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
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

// TestHerSmallestModelSessionTitleGeneration verifies end-to-end with the real
// Rust `her` server and `her-mock` that when Her is turned on and has an
// available model, coding sessions on hermeshd generate their titles via Her's
// stateless `models/title` RPC even when the coding CLI itself has no title
// generator enabled.
func TestHerSmallestModelSessionTitleGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := hertest.Bin(t)
	hertest.Home(t, bin)
	console := t.TempDir()
	_ = os.WriteFile(filepath.Join(console, "index.html"), []byte("<title>Her</title>"), 0o644)

	w := newWorld(t, func(c *machine.Config) {
		c.HerBin, c.HerConsole = bin, console
	})
	alpha := w.machines[0]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dev, _ := identity.GenerateKey()
	cert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         w.ownerID,
		DevicePublicKey: identity.MarshalPublicKey(dev.Public()),
		Label:           "title-e2e-device",
		IssuedAt:        timestamppb.Now(),
	}, w.app.root)
	if err != nil {
		t.Fatal(err)
	}
	creds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: w.rootChain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: cert}}
	}
	relayConn, err := (&transport.Dialer{RelayURL: w.relayURL, Role: relayv1.Role_ROLE_OWNER, Key: dev, Credentials: creds, HTTPClient: http.DefaultClient}).Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer relayConn.Close()
	raw, err := relayConn.OpenTunnel(alpha.MachineID())
	if err != nil {
		t.Fatal(err)
	}
	tc, _, err := transport.ClientHandshake(ctx, raw, dev.CryptoSigner(), creds, w.trust, alpha.MachineID())
	if err != nil {
		t.Fatal(err)
	}
	h2, err := transport.NewH2Client(tc)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	const base = "https://machine"
	info := machinev1connect.NewMachineServiceClient(h2, base, transport.ClientNoCompression)
	admin := machinev1connect.NewMachineAdminServiceClient(h2, base, transport.ClientNoCompression)
	sessions := sessionv1connect.NewSessionServiceClient(h2, base, transport.ClientNoCompression)

	signedSetHer, err := identity.Sign(&identityv1.AdminCommand{
		OwnerId:         w.ownerID,
		TargetMachineId: alpha.MachineID(),
		CommandId:       identity.RandomCode(16),
		ExpiresAt:       timestamppb.New(time.Now().Add(5 * time.Minute)),
		Command:         &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Enabled: true}},
	}, w.app.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Command: signedSetHer}); err != nil {
		t.Fatalf("SetHer(true): %v", err)
	}
	eventually(t, 30*time.Second, "her model ready", func() bool {
		res, iErr := info.GetInfo(ctx, &machinev1.GetInfoRequest{})
		return iErr == nil && res.GetStatus().GetHer().GetRunning() && res.GetStatus().GetHer().GetModelReady()
	})

	// Start a coding session on alpha (whose scripted claude/codex drivers have
	// TitleGenerator disabled): title generation MUST route through Her's models/title!
	started, err := sessions.StartSession(ctx, &sessionv1.StartSessionRequest{
		CommandId:      "cmd_e2e_her_title",
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "claude"},
		InitialPrompt:  "say [[mock:say:{\"title\":\"Her 极速小模型标题\",\"provisional\":false}]] Investigate why long ongoing sessions stayed untitled",
		Locale:         "zh-CN",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	eventually(t, 15*time.Second, "session titled by Her smallest model", func() bool {
		res, gErr := sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: started.GetSession().GetSessionId()})
		if gErr != nil || res.GetProjection().GetSession() == nil {
			return false
		}
		s := res.GetProjection().GetSession()
		return !s.TitleGenerating && s.Title == "Her 极速小模型标题" && s.TitleState == sessionv1.TitleState_TITLE_STATE_FINAL
	})
}
