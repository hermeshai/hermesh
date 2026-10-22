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
	if _, err := w.app.client.AnswerQuestion(ctx, &localv1.AnswerQuestionRequest{SessionId: s.SessionID, QuestionId: q.QuestionId,
		Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"红"}}}}); err != nil {
		t.Fatalf("answer without the optional note: %v", err)
	}
	eventually(t, time.Minute, "the turn finishes with the answer", func() bool {
		g := w.get(s.SessionID)
		return g.Status == "idle" && strings.Contains(g.Result, "answer: v-红")
	})
}
