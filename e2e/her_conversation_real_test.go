package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// TestHerConversationReal is TestHerConversationIsATask with a real model: Her
// runs from a home of her own that is signed in to a model service
// (HERMESH_REAL_HER_HOME, e.g. ~/.her-test), and the owner opens a
// conversation with her on the model they pick (HERMESH_REAL_HER_MODEL). She
// answers in it; asked in plain words, she starts a coding agent, asking the
// owner first; the agent names the conversation; its news comes back to it,
// and her main conversation never hears of it. The conversation is deleted at
// the end, from her home too.
//
//	HERMESH_REAL_HER_HOME=$HOME/.her-test HERMESH_REAL_HER_MODEL=<provider/model> go test ./e2e -run TestHerConversationReal -v -timeout 30m
func TestHerConversationReal(t *testing.T) {
	home, model := os.Getenv("HERMESH_REAL_HER_HOME"), os.Getenv("HERMESH_REAL_HER_MODEL")
	if home == "" || model == "" {
		t.Skip("HERMESH_REAL_HER_HOME and HERMESH_REAL_HER_MODEL are not set")
	}
	// The home's own keys, where her keeps them on this computer.
	env := newHerEnvironment(t, "HER_HOME="+home, "HER_CREDENTIAL_STORE=keychain")
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
	idle := func(what, id string, turns uint32) *sessionv1.Session {
		t.Helper()
		var s *sessionv1.Session
		for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			if s = get(id); s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE && s.GetProgress().GetTurnCount() >= turns {
				return s
			}
			if s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
				t.Fatalf("%s: crashed: %s", what, s.GetResult())
			}
		}
		t.Fatalf("timed out waiting for %s: %v %q", what, s.GetStatus(), s.GetResult())
		return nil
	}

	// The owner opens a conversation with Her on the model they picked.
	started, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		CommandId:      "her-real-1",
		MachineId:      env.machine,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "her", Model: model},
		InitialPrompt:  "请只回答一个阿拉伯数字：2 加 3 等于几？",
	})
	if err != nil {
		t.Fatalf("StartSession with Her: %v", err)
	}
	conv := started.GetSession().GetSessionId()
	t.Cleanup(func() { sc.DeleteSession(context.Background(), &sessionv1.DeleteSessionRequest{MachineId: env.machine, SessionId: conv}) })
	s := idle("Her's answer", conv, 1)
	t.Logf("Her answered: %q (title %q)", s.Result, s.Title)
	if !strings.Contains(s.Result, "5") {
		t.Fatalf("Her's answer: %q", s.Result)
	}
	herConv := s.ProviderSessionRef
	opened := string(herCall(t, env, "", "bots/open", map[string]any{"bot_id": env.bot.ID, "conversation": herConv}))
	if !strings.Contains(opened, `"model":"`+model+`"`) {
		t.Fatalf("the conversation is not on %s: %.400s", model, opened)
	}

	// Asked in plain words, she starts a coding agent on the owner's Mac; she asks first.
	if _, err := sc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		CommandId: "her-real-2", MachineId: env.machine, SessionId: conv, Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START,
		Text: "请在 owner-mac/demo 项目里开一个 Claude Code 会话，提示词原样写成两行：第一行 sleep 3s，第二行 say 做完了。直接开，不用再问我。",
	}); err != nil {
		t.Fatalf("message: %v", err)
	}
	var child *sessionv1.Session
	approved := 0
	for deadline := time.Now().Add(5 * time.Minute); child == nil && time.Now().Before(deadline); time.Sleep(time.Second) {
		s = get(conv)
		if s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
			if q := s.GetProgress().GetPendingQuestion(); q != nil {
				answers := map[string]*sessionv1.QuestionAnswer{}
				for _, it := range q.Items {
					answers[it.Id] = &sessionv1.QuestionAnswer{CustomText: "可以，直接开。"}
				}
				sc.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{CommandId: "her-real-q-" + q.QuestionId, MachineId: env.machine, SessionId: conv, RequestId: q.QuestionId, Answers: answers})
			} else if s.PendingRequestId != "" {
				approved++
				t.Logf("approving Her's request %s", s.PendingRequestId)
				sc.RespondRuntimeRequest(ctx, &sessionv1.RespondRuntimeRequestRequest{CommandId: "her-real-a-" + s.PendingRequestId, MachineId: env.machine, SessionId: conv, RequestId: s.PendingRequestId, Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE})
			}
		}
		res, err := env.c.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			continue
		}
		for _, x := range res.Sessions {
			if x.GetLineage().GetParentSessionId() == conv {
				child = x
			}
		}
	}
	if child == nil {
		t.Fatalf("Her started nothing: %v %q", s.GetStatus(), s.GetResult())
	}
	t.Cleanup(func() { sc.DeleteSession(context.Background(), &sessionv1.DeleteSessionRequest{MachineId: child.MachineId, SessionId: child.SessionId}) })
	if approved == 0 {
		t.Fatal("Her started a coding agent without asking the owner")
	}
	if l := child.GetLineage(); l.GetRelationship() != "her" || l.GetControllable() {
		t.Fatalf("the child's lineage: %v", l)
	}
	t.Logf("Her started %s (%s) on %s", child.SessionId, child.Agent, child.MachineId)

	// Its news wakes this conversation: a turn of Her's own after the agent is done.
	before := get(conv).GetProgress().GetTurnCount()
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		s = get(conv)
		if s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE && s.GetProgress().GetTurnCount() > before && strings.Contains(s.Result+string(herCall(t, env, "", "bots/open", map[string]any{"bot_id": env.bot.ID, "conversation": herConv})), "session_news") {
			break
		}
	}
	if s.GetProgress().GetTurnCount() <= before {
		t.Fatalf("the news woke nothing: %v", s.GetProgress())
	}
	t.Logf("after the news Her said: %q", s.Result)

	// Her's main conversation never heard of it.
	stream, main, _ := openAssistant(t, ctx, env)
	stream.Close()
	if strings.Contains(string(main.Responses[0].Result), child.SessionId) {
		t.Fatalf("the main conversation heard of %s", child.SessionId)
	}
}
