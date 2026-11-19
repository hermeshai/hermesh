package machine

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestHerCannotSendToAnUnverifiedCodingAgent(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := context.Background()
	h.p.Grant.HerMachineId = "m_her"
	start := &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: "fictional private task"}
	if _, err := h.s.Start(ctx, h.p, start); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Her start: %v", err)
	}
	if h.rec.count() != 0 {
		t.Fatal("the coding recipient received a start")
	}

	// A coding session predating consent can still be read. All paths by
	// which Her can hand it content refuse before the agent is called.
	h.p.Grant.HerMachineId = ""
	id := h.start("ask Color|Which color?|Red,Blue")
	h.wait(id, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	h.p.Grant.HerMachineId = "m_her"
	const secret = "fictional private answer"
	if _, err := h.s.Send(ctx, h.p, id, secret, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Her send: %v", err)
	}
	if _, _, err := h.s.Deliver(ctx, h.p, id, "", secret, &sessionv1.MessageReceived{Text: secret}, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Her inbox delivery: %v", err)
	}
	if _, err := h.s.AnswerQuestion(ctx, h.p, &sessionv1.AnswerQuestionRequest{SessionId: id, Answers: []*sessionv1.Answer{{Text: secret}}}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Her answer: %v", err)
	}
	got, err := h.s.Get(h.p, id)
	if err != nil || got.GetStatus() != sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT || h.rec.count() != 1 {
		t.Fatalf("coding session changed after refusal: %v (%v)", got, err)
	}
}

func TestPersistedHerCodingMessageStaysHeld(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	ctx := t.Context()
	id := h.start("say started")
	h.wait(id, idle)
	if err := h.s.st.Set(kClients, []byte(`{"c_legacy_her":{"label":"Her on old-host"}}`)); err != nil {
		t.Fatal(err)
	}
	messages := `[{"by":"c_legacy_her","text":"say fictional old Her handoff"},{"by":"c_test","text":"say fictional ordinary handoff"}]`
	if err := h.s.st.Set(askedKey(id), []byte(messages)); err != nil {
		t.Fatal(err)
	}
	l, _, err := h.s.find(h.p, id, false)
	if err != nil {
		t.Fatal(err)
	}
	h.mark[id] = len(h.events(id))
	l.mu.Lock()
	l.asked = h.s.loadAsked(id)
	l.mu.Unlock()
	h.s.startAsked(l)
	h.said(id, "fictional ordinary handoff")
	h.wait(id, idle)
	if kept := h.s.loadAsked(id); len(kept) != 1 || kept[0].by != "c_legacy_her" {
		t.Fatalf("persisted Her message should stay held, got %v", kept)
	}
	for _, event := range h.events(id) {
		if strings.Contains(event.String(), "fictional old Her handoff") {
			t.Fatal("persisted Her message was sent")
		}
	}
	her := &Principal{Kind: PrincipalClient, ClientID: "c_legacy_her", Grant: &identityv1.Grant{HerMachineId: "m_her",
		ProjectIds: []string{"*"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL}}
	if _, err := h.s.Archive(ctx, her, id); err != nil {
		t.Fatalf("Her could not archive an existing session: %v", err)
	}
}
