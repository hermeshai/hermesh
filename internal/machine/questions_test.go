package machine

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

const waiting = sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT

func (h *harness) startQ(prompt string, timeout *uint32) string {
	h.t.Helper()
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: prompt, QuestionTimeoutSeconds: timeout})
	if err != nil {
		h.t.Fatal(err)
	}
	return sess.SessionId
}

func (h *harness) resolved(id string) []*sessionv1.QuestionResolved {
	var out []*sessionv1.QuestionResolved
	for _, e := range h.events(id) {
		if r := e.GetQuestionResolved(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func secs(n uint32) *uint32 { return &n }

// The agent's question pauses the turn; the answer resumes it.
func TestQuestionIsAnswered(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say hi\nask Color|Which color?|Red,Blue\nsay done")
	sess := h.wait(id, waiting)
	q := sess.Progress.PendingQuestion
	if q == nil || len(q.Items) != 1 || q.Items[0].Header != "Color" || len(q.Items[0].Options) != 2 || q.ExpiresAt == nil ||
		q.ExpiresAt.AsTime().Sub(q.AskedAt.AsTime()) != DefaultQuestionTimeout {
		t.Fatalf("pending question: %v", q)
	}
	ctx := context.Background()
	for _, bad := range []*sessionv1.Answer{{Selected: []string{"Green"}}, {}} {
		if _, err := h.s.AnswerQuestion(ctx, h.p, &sessionv1.AnswerQuestionRequest{SessionId: id, Answers: []*sessionv1.Answer{bad}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("answer %v: %v", bad, err)
		}
	}
	h.mark[id] = len(h.events(id))
	if _, err := h.s.AnswerQuestion(ctx, h.p, &sessionv1.AnswerQuestionRequest{SessionId: id, Answers: []*sessionv1.Answer{{Selected: []string{"blue"}}}}); err != nil {
		t.Fatal(err)
	}
	sess = h.wait(id, idle)
	h.said(id, "answer: Blue")
	if tc := h.lastTurn(id); tc.Result != "done" || tc.Turn != 1 || sess.Progress.PendingQuestion != nil {
		t.Fatalf("turn %v, pending %v", tc, sess.Progress.PendingQuestion)
	}
	r := h.resolved(id)
	if len(r) != 1 || r[0].Resolution != sessionv1.QuestionResolution_QUESTION_RESOLUTION_ANSWERED || r[0].By != "c_test" || r[0].Answers[0].Selected[0] != "Blue" {
		t.Fatalf("resolved: %v", r)
	}
	if _, err := h.s.AnswerQuestion(ctx, h.p, &sessionv1.AnswerQuestionRequest{SessionId: id, Answers: []*sessionv1.Answer{{Text: "x"}}}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("answer with no question: %v", err)
	}
}

// A message sent while the agent waits is the answer (an option it names,
// or free text); the owner may answer too.
func TestMessageAnswersTheQuestion(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("ask Color|Which color?|Red,Blue\nask Size|Which size?|S,M")
	h.wait(id, waiting)
	h.sendNow(id, "red")
	h.said(id, "answer: Red")
	h.wait(id, waiting)
	owner := &Principal{Kind: PrincipalOwner}
	if _, err := h.s.AnswerQuestion(context.Background(), owner, &sessionv1.AnswerQuestionRequest{SessionId: id, Answers: []*sessionv1.Answer{{Text: "extra large"}}}); err != nil {
		t.Fatal(err)
	}
	h.said(id, "answer: extra large")
	h.wait(id, idle)
	if r := h.resolved(id); len(r) != 2 || r[1].By != "owner" {
		t.Fatalf("resolved: %v", r)
	}
	if completed, _ := h.turns(id); len(completed) != 1 {
		t.Fatalf("the answers are no turns of their own: %v", completed)
	}
}

// Nobody answering in time: the agent is told to go on on its own.
// Timeout 0: nobody will answer, so the agent is told at once.
func TestUnansweredQuestion(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.startQ("ask Color|Which color?|Red,Blue", secs(1))
	h.wait(id, waiting)
	h.wait(id, idle)
	if tc := h.lastTurn(id); !strings.HasPrefix(tc.Result, "declined: No one answered within 1s") {
		t.Fatalf("turn: %v", tc)
	}
	if r := h.resolved(id); len(r) != 1 || r[0].Resolution != sessionv1.QuestionResolution_QUESTION_RESOLUTION_TIMED_OUT {
		t.Fatalf("resolved: %v", r)
	}

	id = h.startQ("ask Color|Which color?|Red,Blue", secs(0))
	h.wait(id, idle)
	if tc := h.lastTurn(id); !strings.HasPrefix(tc.Result, "declined: No one can answer questions") {
		t.Fatalf("turn: %v", tc)
	}
	if _, st := h.turns(id); strings.Contains(statusNames(st), "WAITING") {
		t.Fatalf("waited although nobody answers: %v", st)
	}

	id = h.start("ask Color|Which color?|Red,Blue")
	h.wait(id, waiting)
	if _, err := h.s.AnswerQuestion(context.Background(), h.p, &sessionv1.AnswerQuestionRequest{SessionId: id, Decline: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(id, idle)
	if tc := h.lastTurn(id); !strings.HasPrefix(tc.Result, "declined: The user declined to answer") {
		t.Fatalf("turn: %v", tc)
	}
}

// Waiting for an answer is not a stall, nor time spent on the turn.
func TestWaitingIsNotAStall(t *testing.T) {
	h := newHarness(t, Limits{MaxConcurrent: 4, FirstOutputTimeout: time.Hour, IdleTimeout: 200 * time.Millisecond, ToolTimeout: time.Hour,
		TurnTimeout: 400 * time.Millisecond, IdleProcessTTL: time.Hour})
	id := h.start("ask Color|Which color?|Red,Blue\nsay done")
	h.wait(id, waiting)
	time.Sleep(700 * time.Millisecond)
	if s, _ := h.s.Get(h.p, id); s.Status != waiting {
		t.Fatalf("stopped while waiting: %v\n%s", s.Status, h.dump(id))
	}
	h.sendNow(id, "Blue")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.IsError || tc.Result != "done" {
		t.Fatalf("turn: %v", tc)
	}
}

// A secret answer reaches the agent but is not recorded.
func TestSecretAnswerIsNotRecorded(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("ask-secret Token|Paste the deploy token")
	sess := h.wait(id, waiting)
	if !sess.Progress.PendingQuestion.Items[0].Secret {
		t.Fatal("not marked secret")
	}
	h.sendNow(id, "hunter2")
	h.said(id, "answer: hunter2")
	if r := h.resolved(id); len(r) != 1 || r[0].Answers[0].Text != "[secret]" {
		t.Fatalf("resolved: %v", r)
	}
}

// Interrupting a waiting session withdraws its question; the session stays.
func TestInterruptWithdrawsQuestion(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("ask Color|Which color?|Red,Blue")
	h.wait(id, waiting)
	if _, err := h.s.Interrupt(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	h.wait(id, idle)
	if r := h.resolved(id); len(r) != 1 || r[0].Resolution != sessionv1.QuestionResolution_QUESTION_RESOLUTION_CANCELLED {
		t.Fatalf("resolved: %v", r)
	}
}
