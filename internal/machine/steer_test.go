package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/fake"
)

// waitFor waits until an event matches.
func (h *harness) waitFor(id, what string, match func(*sessionv1.SessionEvent) bool) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, e := range h.events(id) {
			if match(e) {
				return
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no %s; events:\n%s", what, h.dump(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) said(id, text string) {
	h.t.Helper()
	h.waitFor(id, "agent message "+text, func(e *sessionv1.SessionEvent) bool { return e.GetAgentMessage().GetText() == text })
}

// sendNow sends without moving the wait mark: the caller waits for the
// turn already running.
func (h *harness) sendNow(id, text string) {
	h.t.Helper()
	if _, err := h.s.Send(context.Background(), h.p, id, text, nil); err != nil {
		h.t.Fatal(err)
	}
}

// turns lists the completed turns and the status changes.
func (h *harness) turns(id string) (completed []*sessionv1.TurnCompleted, statuses []sessionv1.SessionStatus) {
	for _, e := range h.events(id) {
		if tc := e.GetTurnCompleted(); tc != nil {
			completed = append(completed, tc)
		}
		if st := e.GetStatus(); st != nil {
			statuses = append(statuses, st.Status)
		}
	}
	return completed, statuses
}

// A message sent while the agent works joins the running turn.
func TestSendWhileRunningSteersTheTurn(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("say one\nsleep 300ms\nsay two")
	h.said(id, "one")
	h.sendNow(id, "write steered.txt from the steer\nsay steered")
	sess := h.wait(id, idle)
	completed, statuses := h.turns(id)
	if len(completed) != 1 || completed[0].Turn != 1 || completed[0].Result != "steered" || completed[0].DiffStat.GetFilesChanged() != 1 {
		t.Fatalf("turns: %v\n%s", completed, h.dump(id))
	}
	if strings.Count(statusNames(statuses), "IDLE") != 1 || sess.Progress.TurnCount != 1 {
		t.Fatalf("statuses %v, session %v", statuses, sess)
	}
	var users []string
	for _, e := range h.events(id) {
		if u := e.GetUserMessage(); u != nil {
			users = append(users, u.Text)
		}
	}
	if len(users) != 2 || !strings.HasPrefix(users[1], "write steered.txt") {
		t.Fatalf("user messages: %q", users)
	}
}

// An agent that cannot take input mid-turn (ACP) gets the message as the
// next turn, with no idle in between: a caller waiting for idle gets the
// answer to its message.
func TestSendWhileRunningQueuesNextTurn(t *testing.T) {
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"nosteer": noSteer{}})
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "nosteer", Prompt: "say one\nsleep 300ms\nsay first"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.SessionId
	h.said(id, "one")
	h.sendNow(id, "say second")
	h.sendNow(id, "say third")
	sess = h.wait(id, idle)
	completed, statuses := h.turns(id)
	if len(completed) != 2 || completed[0].Result != "first" || completed[1].Turn != 2 || completed[1].Result != "third" {
		t.Fatalf("turns: %v\n%s", completed, h.dump(id))
	}
	if strings.Count(statusNames(statuses), "IDLE") != 1 || sess.Progress.TurnCount != 2 {
		t.Fatalf("an idle between the turns: %v", statuses)
	}
	if !strings.Contains(h.dump(id), "say second\\n\\nsay third") {
		t.Fatalf("both messages are the second turn's prompt:\n%s", h.dump(id))
	}
}

// Messages sent before the session starts join its first prompt.
func TestSendWhileQueuedJoinsThePrompt(t *testing.T) {
	h := newHarness(t, Limits{MaxConcurrent: 1, IdleProcessTTL: time.Hour})
	busy := h.start("sleep 400ms\nsay busy")
	id := h.start("say queued")
	if s, _ := h.s.Get(h.p, id); s.Status != sessionv1.SessionStatus_SESSION_STATUS_QUEUED {
		t.Fatalf("second session is %v", s.Status)
	}
	h.sendNow(id, "say extra")
	h.wait(busy, idle)
	h.wait(id, idle)
	completed, _ := h.turns(id)
	if len(completed) != 1 || completed[0].Result != "extra" {
		t.Fatalf("turns: %v\n%s", completed, h.dump(id))
	}
}

// A failed turn takes the messages waiting for it along, and says so.
func TestFailedTurnDropsFollowUps(t *testing.T) {
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"nosteer": noSteer{}})
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "nosteer", Prompt: "say one\nsleep 300ms\nfail boom"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.SessionId
	h.said(id, "one")
	h.sendNow(id, "say never")
	h.wait(id, idle)
	completed, _ := h.turns(id)
	if len(completed) != 1 || !completed[0].IsError || !strings.Contains(h.dump(id), "1 message(s) sent during the turn were not delivered") {
		t.Fatalf("turns: %v\n%s", completed, h.dump(id))
	}
	h.send(id, "say again")
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Result != "again" || tc.Turn != 2 {
		t.Fatalf("turn: %v", tc)
	}
}

func statusNames(st []sessionv1.SessionStatus) string {
	var b strings.Builder
	for _, s := range st {
		b.WriteString(s.String() + " ")
	}
	return b.String()
}

// noSteer is the fake provider without mid-turn input, like ACP agents.
type noSteer struct{ fake.Provider }

func (p noSteer) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	s, err := p.Provider.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return noSteerSession{s}, nil
}

type noSteerSession struct{ provider.Session }

func (noSteerSession) Steer(context.Context, provider.Input) error {
	return errors.New("takes input only between turns")
}
