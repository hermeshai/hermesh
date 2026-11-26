package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// TestOptionalQuestionItem: an agent's form question with a field it does
// not require (ACP elicitation) can be answered without that field, from
// the owner's app as from the desktop app's question panel.
func TestOptionalQuestionItem(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	s, r := w.startSession(2*time.Minute, "--agent", "gemini", "--wait", "--prompt", "ask 选哪个颜色|红,蓝")
	if s.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	ctx := context.Background()
	var q *sessionv1.Question
	eventually(t, time.Minute, "the question", func() bool {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, x := range res.Sessions {
			if x.SessionId == s.SessionID && x.Progress.GetPendingQuestion() != nil {
				q = x.Progress.PendingQuestion
			}
		}
		return q != nil
	})
	if len(q.Items) != 2 || q.Items[0].Optional || !q.Items[1].Optional {
		t.Fatalf("items: %+v", q.Items)
	}
	if _, err := w.app.sessions.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: s.SessionID, RequestId: q.QuestionId,
		Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		Answers:  map[string]*sessionv1.QuestionAnswer{q.Items[0].Id: {SelectedOptions: []string{"红"}}},
	}); err != nil {
		t.Fatalf("answer without the optional note: %v", err)
	}
	eventually(t, time.Minute, "the turn finishes with the answer", func() bool {
		g := w.get(s.SessionID)
		return g.Status == "idle" && strings.Contains(g.Result, "answer: v-红")
	})
}

func TestMultiQuestionElicitation(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	s, r := w.startSession(2*time.Minute, "--agent", "gemini", "--wait", "--prompt", "ask3")
	if s.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	ctx := context.Background()
	var q *sessionv1.Question
	eventually(t, time.Minute, "the 3-item question", func() bool {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, x := range res.Sessions {
			if x.SessionId == s.SessionID && x.Progress.GetPendingQuestion() != nil {
				q = x.Progress.PendingQuestion
			}
		}
		return q != nil
	})
	if len(q.Items) != 3 {
		t.Fatalf("expected 3 merged question items, got %d: %+v", len(q.Items), q.Items)
	}
	if q.Items[0].Text != "推进节奏选哪种？" || !q.Items[0].AllowOther || q.Items[0].Optional {
		t.Fatalf("unexpected item 0: %+v", q.Items[0])
	}
	if _, err := w.app.sessions.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: s.SessionID,
		RequestId: q.QuestionId,
		Decision:  sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
		Answers: map[string]*sessionv1.QuestionAnswer{
			q.Items[0].Id: {CustomText: "先做第一阶段"},
			q.Items[1].Id: {SelectedOptions: []string{"自动喂给模型"}},
			q.Items[2].Id: {SelectedOptions: []string{"(Recommended) 自动探测运行中终端"}},
		},
	}); err != nil {
		t.Fatalf("respond multi-question: %v", err)
	}
	eventually(t, time.Minute, "turn finishes with merged answers", func() bool {
		g := w.get(s.SessionID)
		return g.Status == "idle" && strings.Contains(g.Result, "answers3: q0=<nil>|q0_other=先做第一阶段|q1=自动喂给模型|q2=auto")
	})
}
