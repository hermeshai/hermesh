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

func (f codingFixture) changeRoute(t *testing.T, settings string) {
	t.Helper()
	dir := filepath.Join(f.home, ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMobileCodingConsentWithRecordingACP(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	f := recordingACP(t)
	w := newOwnerMac(t, f.env, &localv1.GrantSpec{Agents: []string{"gemini"}})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	phone := joinMobile(t, w, st.RelayUrl, t.TempDir(), root)
	legacy := phone
	if bin := os.Getenv("HERMESH_TEST_LEGACY_MOBILE_CORE"); bin != "" {
		key, err := identity.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		legacy = joinMobileWithCore(t, w, st.RelayUrl, startMobileCoreFrom(t, bin, t.TempDir(), key))
	}
	start := func() string {
		id, r := w.startSession(20*time.Second, "--agent", "gemini", "--prompt", "ask Fictional color|Mint,Plum")
		if r.code != 0 || id.SessionID == "" {
			t.Fatalf("start: %+v", r)
		}
		eventually(t, 10*time.Second, "coding agent is waiting", func() bool {
			return w.get(id.SessionID).Status == "waiting_input"
		})
		return id.SessionID
	}
	ask := func(id string) *sessionv1.Question {
		t.Helper()
		var q *sessionv1.Question
		eventually(t, 10*time.Second, "phone sees pending question", func() bool {
			sessions, err := phone.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
			if err != nil {
				return false
			}
			for _, s := range sessions.Sessions {
				if s.SessionId == id {
					q = s.GetProgress().GetPendingQuestion()
				}
			}
			return q != nil
		})
		return q
	}
	id := start()
	q := ask(id)
	if q.LocalAnswerAvailable {
		t.Fatal("legacy mobile was offered an unapproved external answer")
	}
	reply := &localv1.AnswerQuestionRequest{SessionId: id, QuestionId: q.QuestionId,
		Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"Plum"}}}}
	refused := func(client *mobileCore, label string) {
		t.Helper()
		eventually(t, 10*time.Second, label, func() bool {
			_, err := client.client.AnswerQuestion(ctx, reply)
			if connect.CodeOf(err) == connect.CodeUnavailable || err != nil && strings.Contains(err.Error(), "400 Bad Request") {
				return false // The owner may be switching from relay to direct.
			}
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("%s: %v", label, err)
			}
			return true
		})
		if status := w.get(id).Status; status != "waiting_input" {
			t.Fatalf("%s changed question status to %s", label, status)
		}
	}
	refused(legacy, "legacy request before approval")
	req := &localv1.CodingConsentRequest{Consent: &sessionv1.CodingConsentRequest{SessionId: id,
		Scope: sessionv1.CodingConsentScope_CODING_CONSENT_SCOPE_MOBILE_ANSWER}}
	disclosure, err := phone.client.CodingConsent(ctx, req)
	if err != nil || disclosure.Recipient != "Google" || disclosure.Approved || disclosure.Version == "" ||
		disclosure.DataCategories == "" || disclosure.Purpose == "" || disclosure.MachineId != st.LocalMachineId {
		t.Fatalf("disclosure: %v %v", disclosure, err)
	}
	refused(phone, "disclosure without approval")
	req.Consent.ApproveVersion = disclosure.Version
	if grant, err := phone.client.CodingConsent(ctx, req); err != nil || !grant.Approved {
		t.Fatalf("approve: %v %v", grant, err)
	}
	reply.CodingConsentVersion = disclosure.Version
	if _, err := phone.client.AnswerQuestion(ctx, reply); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "typed answer reaches the recording ACP adapter", func() bool {
		return strings.Contains(w.get(id).Result, "answer: v-plum")
	})

	id = start()
	q = ask(id)
	if !q.LocalAnswerAvailable {
		t.Fatal("older iPhone should be offered the already approved route")
	}
	reply.SessionId, reply.QuestionId, reply.CodingConsentVersion = id, q.QuestionId, ""
	if _, err := legacy.client.AnswerQuestion(ctx, reply); err != nil {
		t.Fatalf("legacy iPhone answer with host approval: %v", err)
	}
	eventually(t, 10*time.Second, "legacy answer reaches the agent", func() bool {
		return strings.Contains(w.get(id).Result, "answer: v-plum")
	})

	id = start()
	q = ask(id)
	reply.SessionId, reply.QuestionId, reply.CodingConsentVersion = id, q.QuestionId, disclosure.Version
	req.Consent.SessionId, req.Consent.ApproveVersion, req.Consent.Revoke = id, "", true
	if _, err := w.app.client.CodingConsent(ctx, req); err != nil {
		t.Fatal(err)
	}
	refused(phone, "withdrawn grant")
	reply.CodingConsentVersion = ""
	refused(legacy, "legacy request after withdrawal")
	req.Consent.Revoke, req.Consent.ApproveVersion = false, disclosure.Version
	if _, err := phone.client.CodingConsent(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("withdrawn disclosure was accepted: %v", err)
	}

	req.Consent.ApproveVersion = ""
	disclosure, err = phone.client.CodingConsent(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Consent.ApproveVersion = disclosure.Version
	if _, err := phone.client.CodingConsent(ctx, req); err != nil {
		t.Fatal(err)
	}
	f.changeRoute(t, `{"endpoint":"https://other-recipient.invalid"}`)
	refused(phone, "route changed")
	if _, err := phone.client.CodingConsent(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unknown route was accepted: %v", err)
	}
	reply.Answers, reply.Decline = nil, true
	if _, err := phone.client.AnswerQuestion(ctx, reply); err != nil {
		t.Fatalf("decline should still reach the agent: %v", err)
	}
	if n := f.prompts(); n != 3 {
		t.Fatalf("recorded %d prompts, want 3", n)
	}
}

func TestHerCodingConsentWithRecordingACP(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	f := recordingACP(t)
	env := newHerEnvironmentWithCodingAgents(t, f.env)
	ctx := t.Context()
	status, err := env.c.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	req := &localv1.CodingConsentRequest{MachineId: status.LocalMachineId,
		Consent: &sessionv1.CodingConsentRequest{Agent: "gemini", ProjectId: "demo", Scope: sessionv1.CodingConsentScope_CODING_CONSENT_SCOPE_HER}}
	disclosure, err := env.c.CodingConsent(ctx, req)
	if err != nil || disclosure.Approved || disclosure.Recipient != "Google" || disclosure.Purpose == "" {
		t.Fatalf("disclosure: %v %v", disclosure, err)
	}
	start := meshCall("session_start", map[string]any{"project": "owner-mac/demo", "agent": "gemini", "prompt": "say Fictional Her delegation"})
	_, said := tellHer(t, env, start, "approve", "mesh ")
	if !strings.Contains(said, "coding_consent_required") || f.prompts() != 0 {
		t.Fatalf("action approval was mistaken for coding consent: %s, prompts %d", said, f.prompts())
	}
	req.Consent.ApproveVersion = disclosure.Version
	if granted, err := env.c.CodingConsent(ctx, req); err != nil || !granted.Approved {
		t.Fatalf("approve: %v %v", granted, err)
	}
	_, said = tellHer(t, env, start, "approve", "mesh ")
	id := sessionID.FindString(said)
	if id == "" {
		t.Fatalf("approved handoff: %s", said)
	}
	eventually(t, 15*time.Second, "approved Her text reaches ACP once", func() bool {
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
	req.Consent.ApproveVersion, req.Consent.Revoke = "", true
	if _, err := env.c.CodingConsent(ctx, req); err != nil {
		t.Fatal(err)
	}
	send := meshCall("session_send", map[string]any{"session_id": id, "message": "Fictional withdrawn Her follow-up"})
	_, said = tellHer(t, env, send, "approve", "mesh ")
	if !strings.Contains(said, "coding_consent_required") || f.prompts() != 1 {
		t.Fatalf("withdrawn follow-up reached the agent: %s, prompts %d", said, f.prompts())
	}
	req.Consent.Revoke = false
	again, err := env.c.CodingConsent(ctx, req)
	if err != nil || again.Version == disclosure.Version {
		t.Fatalf("new disclosure: %v %v", again, err)
	}
	req.Consent.ApproveVersion = again.Version
	if _, err := env.c.CodingConsent(ctx, req); err != nil {
		t.Fatal(err)
	}
	// A follow-up delivered while someone else's turn is answering waits
	// for its own turn. Its original approval must not survive revocation.
	busy, started := env.w.startSession(20*time.Second, "--agent", "gemini", "--prompt", "ask Fictional queue|Wait,Go")
	if started.code != 0 || busy.SessionID == "" {
		t.Fatalf("start waiting turn: %+v", started)
	}
	eventually(t, 10*time.Second, "ordinary session is waiting", func() bool {
		return env.w.get(busy.SessionID).Status == "waiting_input"
	})
	queued := meshCall("session_send", map[string]any{"session_id": busy.SessionID, "message": "say Fictional revoked queued follow-up"})
	_, said = tellHer(t, env, queued, "approve", "mesh ")
	if !strings.Contains(said, "queued") || f.prompts() != 2 {
		t.Fatalf("Her follow-up was not held: %s, prompts %d", said, f.prompts())
	}
	req.Consent.ApproveVersion, req.Consent.Revoke = "", true
	if _, err := env.c.CodingConsent(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Consent.Revoke = false
	disclosure, err = env.c.CodingConsent(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Consent.ApproveVersion = disclosure.Version
	if _, err := env.c.CodingConsent(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := env.c.AnswerQuestion(ctx, &localv1.AnswerQuestionRequest{SessionId: busy.SessionID, Decline: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "ordinary agent completed after decline", func() bool {
		return env.w.get(busy.SessionID).Status == "idle"
	})
	if n := f.prompts(); n != 2 {
		t.Fatalf("revoked queued text replayed after approval: %d prompts", n)
	}
	f.changeRoute(t, `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
	_, said = tellHer(t, env, start, "approve", "mesh ")
	if !strings.Contains(said, "coding_consent_required") || f.prompts() != 2 {
		t.Fatalf("a changed Gemini route reused consent: %s, prompts %d", said, f.prompts())
	}
	f.changeRoute(t, `{"endpoint":"https://other-recipient.invalid"}`)
	_, said = tellHer(t, env, start, "approve", "mesh ")
	if !strings.Contains(said, "cannot identify") || f.prompts() != 2 {
		t.Fatalf("unknown recipient: %s, prompts %d", said, f.prompts())
	}
}
