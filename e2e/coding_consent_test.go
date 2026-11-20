//go:build unix

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

type codingFixture struct {
	home, log string
	env       []string
}

func recordingACP(t *testing.T) codingFixture {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	return codingFixture{home: dir, log: log, env: append(geminiCodingFixtureEnv(t, dir), "FAKE_ACP_LOG="+log)}
}

func (f codingFixture) prompts() int {
	b, _ := os.ReadFile(f.log)
	return strings.Count(string(b), "session/prompt")
}

// TestHerConsentRefusalAllowsDirectCodingAndMobileAnswers verifies that
// withholding or revoking Her's third-party model consent blocks only Her
// inference and never blocks direct coding agent sessions or iPhone answers.
func TestHerConsentRefusalAllowsDirectCodingAndMobileAnswers(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	f := recordingACP(t)
	env := newHerEnvironmentWithCodingAgents(t, f.env)
	w, c := env.w, env.c
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	// Revoke Her model sharing so Her inference is unapproved.
	herCall(t, env, "", "ai_consent/revoke", map[string]any{})
	bot := defaultBot(t, c, env.machine)
	if _, err := c.HerSend(ctx, &localv1.HerSendRequest{
		MachineId: env.machine, BotId: bot.ID, Text: mockSend("fictional blocked Her prompt"),
	}); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "AI sharing is blocked") {
		t.Fatalf("Her inference should be blocked without consent: %v", err)
	}

	// Pair an iPhone owner device and verify direct coding agent sessions and
	// mobile answers work without any coding-specific consent gate.
	st, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	phone := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)

	id, r := w.startSession(20*time.Second, "--agent", "gemini", "--prompt", "ask Fictional color|Mint,Plum")
	if r.code != 0 || id.SessionID == "" {
		t.Fatalf("direct coding start while Her consent is revoked: %+v", r)
	}
	eventually(t, 10*time.Second, "coding agent is waiting", func() bool {
		return w.get(id.SessionID).Status == "waiting_input"
	})
	var q *sessionv1.Question
	eventually(t, 10*time.Second, "phone sees pending question", func() bool {
		sessions, err := phone.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, s := range sessions.Sessions {
			if s.SessionId == id.SessionID {
				q = s.GetProgress().GetPendingQuestion()
			}
		}
		return q != nil
	})
	if !q.LocalAnswerAvailable {
		t.Fatal("pending question should remain quick-answerable for v0.1.7 iPhone compatibility")
	}

	// Legacy v0.1.7 owner CodingConsent compat call reports approved cleanly.
	compat, err := phone.client.CodingConsent(ctx, &localv1.CodingConsentRequest{
		Consent: &sessionv1.CodingConsentRequest{
			SessionId: id.SessionID,
			Scope:     sessionv1.CodingConsentScope_CODING_CONSENT_SCOPE_MOBILE_ANSWER,
		},
	})
	if err != nil || !compat.Approved {
		t.Fatalf("legacy v0.1.7 CodingConsent compat: %v %v", compat, err)
	}

	// Decline with non-empty answer content is still rejected.
	badDecline := &localv1.AnswerQuestionRequest{
		SessionId: id.SessionID, QuestionId: q.QuestionId, Decline: true,
		Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"Plum"}}},
	}
	if _, err := phone.client.AnswerQuestion(ctx, badDecline); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("decline with content: %v", err)
	}

	// Direct mobile answer succeeds immediately without coding-specific consent.
	reply := &localv1.AnswerQuestionRequest{
		SessionId: id.SessionID, QuestionId: q.QuestionId,
		Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"Plum"}}},
	}
	if _, err := phone.client.AnswerQuestion(ctx, reply); err != nil {
		t.Fatalf("mobile answer while Her consent is revoked: %v", err)
	}
	eventually(t, 10*time.Second, "typed answer reaches the recording ACP adapter", func() bool {
		return strings.Contains(w.get(id.SessionID).Result, "answer: v-plum")
	})
	if n := f.prompts(); n != 1 {
		t.Fatalf("recorded %d prompts, want 1", n)
	}

	// Re-approve Her model inference, restart the owner daemon (and Her with it),
	// and verify the approval persists across restart for the same recipient.
	approveHerTestModel(t, c, env.machine, "mock/mock-1")
	w.restartOwner(false)
	c = w.app.client
	env.c = c
	eventually(t, 2*time.Minute, "Her running again after restart", func() bool {
		h := listedMachine(t, c, env.machine).GetHer()
		return h.GetRunning() && h.GetModelReady()
	})
	if _, err := c.HerSend(ctx, &localv1.HerSendRequest{
		MachineId: env.machine, BotId: bot.ID, Text: mockSend("fictional allowed after restart"),
	}); err != nil {
		t.Fatalf("Her consent did not persist across restart: %v", err)
	}
}

// TestHerCodingHandoffRespectsApprovalAndGrant verifies that Her delegates to
// coding agents without a separate coding-consent gate, while still strictly
// enforcing per-operation tool approval and Her's owner-signed mesh grant.
func TestHerCodingHandoffRespectsApprovalAndGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	f := recordingACP(t)
	env := newHerEnvironmentWithCodingAgents(t, f.env)
	ctx := t.Context()

	start := meshCall("session_start", map[string]any{"project": "owner-mac/demo", "agent": "gemini", "prompt": "say Fictional Her delegation"})

	// Denying per-operation tool approval must block the coding handoff.
	_, said := tellHer(t, env, start, "deny", "mesh ")
	if f.prompts() != 0 {
		t.Fatalf("denied per-operation approval reached ACP: %s, prompts %d", said, f.prompts())
	}

	// Approving the per-operation tool call dispatches directly under Her's signed grant.
	_, said = tellHer(t, env, start, "approve", "mesh ")
	id := sessionID.FindString(said)
	if id == "" {
		t.Fatalf("approved handoff failed: %s", said)
	}
	eventually(t, 15*time.Second, "approved Her delegation reaches ACP once", func() bool {
		res, err := env.c.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, s := range res.Sessions {
			if s.SessionId == id && s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
				return f.prompts() == 1 && strings.Contains(s.GetProgress().GetLastMessageExcerpt(), "Fictional Her delegation")
			}
		}
		return false
	})

	// Follow-up works with normal per-operation approval as well.
	send := meshCall("session_send", map[string]any{"session_id": id, "message": "say Fictional Her follow-up"})
	_, said = tellHer(t, env, send, "approve", "mesh ")
	if strings.HasPrefix(said, "mesh failed") {
		t.Fatalf("approved follow-up failed: %s", said)
	}
	eventually(t, 15*time.Second, "follow-up reaches ACP", func() bool {
		return f.prompts() == 2
	})

	// Revoking Her's owner-signed mesh grant blocks subsequent scheduling even
	// when per-operation approval is granted and Her model consent is active.
	her := herClient(t, env)
	if her == nil {
		t.Fatal("Her mesh grant not found")
	}
	if _, err := env.c.RevokeClient(ctx, &localv1.RevokeClientRequest{GrantId: her.GrantId}); err != nil {
		t.Fatalf("revoke Her grant: %v", err)
	}
	eventually(t, 15*time.Second, "Her grant revocation propagates", func() bool {
		return herClient(t, env) == nil
	})
	_, said = tellHer(t, env, start, "approve", "mesh ")
	if !strings.HasPrefix(said, "mesh failed") || f.prompts() != 2 {
		t.Fatalf("revoked Her grant bypassed authorization: %s, prompts %d", said, f.prompts())
	}
}
