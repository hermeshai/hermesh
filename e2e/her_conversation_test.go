package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Her's conversations besides her main one are sessions (docs/her-conversations.md), as the owner
// meets them: the owner opens one with Her as the provider; Her asks before she starts a coding
// agent, and the session waits for the owner's answer; the agent she starts, on the owner's Mac
// while she runs on another machine, names the conversation it was sent from; its news wakes the
// conversation, not her main one; a question she ends her turn on waits for the owner, whose
// answer reaches her as their words.
func TestHerConversationIsATask(t *testing.T) {
	env := newHerEnvironment(t)
	eventually(t, time.Minute, "Her on the mesh", func() bool {
		return herClient(t, env) != nil && listedMachine(t, env.c, env.machine).GetHer().GetMesh()
	})
	ctx := context.Background()
	sc := env.w.app.sessions
	get := func(id string) *sessionv1.Session {
		res, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{MachineId: env.machine, SessionId: id})
		if err != nil {
			return nil
		}
		return res.GetProjection().GetSession()
	}
	waitFor := func(what, id string, ok func(*sessionv1.Session) bool) *sessionv1.Session {
		t.Helper()
		var s *sessionv1.Session
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			if s = get(id); s != nil && ok(s) {
				return s
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s: status %v, result %q, pending %v", what, s.GetStatus(), s.GetResult(), s.GetProgress().GetPendingQuestion())
		return nil
	}

	// The owner opens a conversation with Her, on Her's machine, asking her to start a coding agent.
	started, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		CommandId:      "her-conv-1",
		MachineId:      env.machine,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "her"},
		InitialPrompt:  meshCall("session_start", map[string]any{"project": "owner-mac/demo", "prompt": "sleep 4s\nsay built it"}),
	})
	if err != nil {
		t.Fatalf("StartSession with Her: %v", err)
	}
	conv := started.GetSession().GetSessionId()

	// Her asks first, as she does in her own chat: the session waits on the owner.
	s := waitFor("Her's approval request", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
	})
	herConv := s.ProviderSessionRef
	if herConv == "" {
		t.Fatal("the session names no conversation of Her's")
	}
	if _, err := sc.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		CommandId: "her-conv-approve", MachineId: env.machine, SessionId: conv, RequestId: s.PendingRequestId,
		Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
	}); err != nil {
		t.Fatalf("approving: %v", err)
	}

	// The agent she starts names the conversation, across machines.
	var child *sessionv1.Session
	eventually(t, 2*time.Minute, "the session Her started", func() bool {
		res, err := env.c.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, x := range res.Sessions {
			if x.GetLineage().GetParentSessionId() == conv {
				child = x
				return true
			}
		}
		return false
	})
	if l := child.GetLineage(); l.GetRelationship() != "her" || l.GetControllable() || child.MachineId == env.machine {
		t.Fatalf("the child: lineage %v on %s", l, child.MachineId)
	}

	// Her's turn ends; then the agent's news wakes this conversation.
	waitFor("Her's turn after starting it", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && strings.HasPrefix(s.Result, "mesh done")
	})
	s = waitFor("the news in this conversation", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && strings.Contains(s.Result, "<session_news") && strings.Contains(s.Result, child.SessionId)
	})
	if s.GetProgress().GetTurnCount() < 2 {
		t.Fatalf("the news did not make a turn: %v", s.GetProgress())
	}

	// Her's main conversation never heard of it.
	stream, opened, mainID := openAssistant(t, ctx, env)
	if strings.Contains(string(opened.Responses[0].Result), child.SessionId) {
		stream.Close()
		t.Fatalf("the main conversation heard of %s", child.SessionId)
	}

	// Changing Her's main conversation model & thinking updates the default offered by ListProviders
	// even though config.toml still names mock/mock-1.
	herCall(t, env, opened.ConnectionId, "session/op", map[string]any{
		"session_id": mainID,
		"op":         map[string]any{"type": "set_model", "model": "mock/mock-2", "thinking": "high"},
	})
	stream.Close()
	eventually(t, 20*time.Second, "ListProviders reflects Her's main conversation model and thinking", func() bool {
		res, err := sc.ListProviders(ctx, &sessionv1.ListProvidersRequest{MachineId: env.machine})
		if err != nil {
			return false
		}
		for _, p := range res.Providers {
			if p.InstanceId == "her" && len(p.Models) > 0 && p.Models[0].Slug == "mock/mock-2" && p.Models[0].IsDefault {
				for _, opt := range p.Models[0].OptionDescriptors {
					if opt.Id == "effort" && opt.GetCurrentValue().GetStringValue() == "high" {
						return true
					}
				}
			}
		}
		return false
	})

	// A question she ends her turn on waits for the owner; the answer reaches her as their words.
	if _, err := sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		CommandId: "her-conv-ask", MachineId: env.machine, SessionId: conv, Text: "[[mock:ask:Ship it today?|Today|Tomorrow]]",
		Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START,
	}); err != nil {
		t.Fatalf("message: %v", err)
	}
	s = waitFor("Her's question", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT && s.GetProgress().GetPendingQuestion() != nil
	})
	q := s.GetProgress().GetPendingQuestion()
	if len(q.Items) != 1 || q.Items[0].Text != "Ship it today?" || len(q.Items[0].Options) != 2 {
		t.Fatalf("question: %v", q)
	}
	// The owner answers in their own words (her-mock needs a directive in them to reply).
	const answer = "[[mock:send:OK, tomorrow then.]] Tomorrow"
	if _, err := sc.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		CommandId: "her-conv-answer", MachineId: env.machine, SessionId: conv, RequestId: q.QuestionId,
		Answers: map[string]*sessionv1.QuestionAnswer{q.Items[0].Id: {CustomText: answer}},
	}); err != nil {
		t.Fatalf("answering: %v", err)
	}
	waitFor("Her's reply to the answer", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && s.Result == "OK, tomorrow then."
	})
	// Her read the owner's words, not the kernel's report of them.
	herOpened := herCall(t, env, "", "bots/open", map[string]any{"bot_id": env.bot.ID, "conversation": herConv})
	if transcript := string(herOpened); !strings.Contains(transcript, `"[[mock:send:OK, tomorrow then.]] Tomorrow"`) || strings.Contains(transcript, "Answers to previous questions") {
		t.Fatalf("what Her read of the answer: %s", transcript)
	}

	// A sub-agent Her spawns keeps the conversation active (active_background_count > 0) after Her's
	// own turn ends, puts its approval to the owner via agent_approval, and settles once Her reports back.
	if _, err := sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		CommandId: "her-conv-subagent", MachineId: env.machine, SessionId: conv, Text: "[[mock:relay_run:touch sub_made.txt]] 派子智能体建文件",
		Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START,
	}); err != nil {
		t.Fatalf("sub-agent message: %v", err)
	}
	s = waitFor("sub-agent's approval while active_background_count > 0", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT && s.ActiveBackgroundCount > 0 && s.PendingRequestId != ""
	})
	if _, err := sc.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{
		CommandId: "her-conv-subagent-approve", MachineId: env.machine, SessionId: conv, RequestId: s.PendingRequestId,
		Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
	}); err != nil {
		t.Fatalf("approving sub-agent: %v", err)
	}
	waitFor("Her's summary after sub-agent finishes", conv, func(s *sessionv1.Session) bool {
		return s.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE && s.ActiveBackgroundCount == 0 && strings.Contains(s.Result, "<agent_report")
	})

	// Deleting the session deletes Her's conversation too.
	if _, err := sc.DeleteSession(ctx, &sessionv1.DeleteSessionRequest{MachineId: env.machine, SessionId: conv}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	eventually(t, time.Minute, "the conversation gone from Her", func() bool {
		res := herCall(t, env, "", "bots/conversations", map[string]any{"bot_id": env.bot.ID})
		return !strings.Contains(string(res), herConv)
	})
}
