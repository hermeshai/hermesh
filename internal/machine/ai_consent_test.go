package machine

import (
	"context"
	"testing"

	"connectrpc.com/connect"

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
