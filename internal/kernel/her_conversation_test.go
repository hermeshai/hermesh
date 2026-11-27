package kernel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/her"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
)

// herSide stands in for the Her service behind the her driver: one conversation, whose events the
// test writes and whose ops it reads.
type herSide struct {
	mu       sync.Mutex
	attached chan *herConv
}

func (h *herSide) Available() bool { return true }
func (h *herSide) Models(context.Context) ([]*sessionv1.ModelCatalogEntry, error) {
	return []*sessionv1.ModelCatalogEntry{{Slug: "tc/auto", Name: "Auto", IsDefault: true}}, nil
}
func (h *herSide) NewConversation(context.Context, string, string) (string, error) {
	return "her_c1", nil
}
func (h *herSide) Attach(_ context.Context, id string) (her.Conversation, error) {
	c := &herConv{id: id, events: make(chan json.RawMessage, 32), ops: make(chan map[string]any, 32)}
	h.attached <- c
	return c, nil
}
func (h *herSide) Op(context.Context, string, any) error            { return nil }
func (h *herSide) DeleteConversation(context.Context, string) error { return nil }

type herConv struct {
	id     string
	events chan json.RawMessage
	ops    chan map[string]any
	once   sync.Once
}

func (c *herConv) Events() <-chan json.RawMessage { return c.events }
func (c *herConv) Op(_ context.Context, op any) error {
	b, _ := json.Marshal(op)
	var m map[string]any
	json.Unmarshal(b, &m)
	c.ops <- m
	return nil
}
func (c *herConv) Close() error { c.once.Do(func() { close(c.events) }); return nil }
func (c *herConv) say(evs ...string) {
	for _, ev := range evs {
		c.events <- json.RawMessage(ev)
	}
}
func (c *herConv) op(t *testing.T) map[string]any {
	t.Helper()
	select {
	case o := <-c.ops:
		return o
	case <-time.After(8 * time.Second):
		t.Fatal("no op reached her")
	}
	return nil
}

func openHerEngine(t *testing.T) (*Engine, *herSide) {
	t.Helper()
	side := &herSide{attached: make(chan *herConv, 4)}
	eng, err := Open(Config{
		DataDir:         t.TempDir(),
		MachineID:       "m_test",
		Drivers:         map[string]runtime.Driver{"her": her.NewDriver(side), "scripted": scripted.NewDriver()},
		IncludeScripted: true,
		IdleTTL:         time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	owner := OwnerPrincipal("owner", "Owner")
	if _, err := eng.UpsertProviderInstance(context.Background(), owner, &sessionv1.ProviderInstanceConfig{InstanceId: "her", Driver: "her", DisplayName: "Her", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return eng, side
}

// A conversation with Her is a session (docs/her-conversations.md §5): her messages are its own, a
// question she ends her turn on makes it wait for the owner and the answer reaches her as their
// words; an approval she asks for is the owner's to give even in full access, and one given in her
// own chat lets the session go on.
func TestHerConversationIsASession(t *testing.T) {
	ctx := context.Background()
	eng, side := openHerEngine(t)
	owner := OwnerPrincipal("owner", "Owner")
	watchCh := make(chan *sessionv1.WatchSessionsResponse, 32)
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	go func() {
		_ = eng.WatchSessions(wctx, owner, false, func(m *sessionv1.WatchSessionsResponse) error {
			watchCh <- m
			return nil
		})
	}()
	// Wait for initial watch snapshot before starting the session.
	select {
	case <-watchCh:
	case <-time.After(5 * time.Second):
		t.Fatal("initial WatchSessions snapshot timed out")
	}
	res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "her"},
		RuntimeMode:    identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InitialPrompt:  "split the payments migration",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Session.SessionId
	if res.Session.Agent != "her" && res.Session.GetModelSelection().GetInstanceId() != "her" {
		t.Fatalf("not Her's: %v", res.Session)
	}
	var conv *herConv
	select {
	case conv = <-side.attached:
	case <-time.After(8 * time.Second):
		t.Fatal("the conversation was not followed")
	}
	if o := conv.op(t); o["type"] != "prompt" || o["input"].([]any)[0].(map[string]any)["text"] != "split the payments migration" {
		t.Fatalf("first message: %v", o)
	}
	// Watch sees Her's conversation id as soon as it is made, before she answers.
	watchedRef := ""
	for deadline := time.After(8 * time.Second); watchedRef == ""; {
		select {
		case msg := <-watchCh:
			for _, s := range msg.Sessions {
				if s.SessionId == id && s.ProviderSessionRef != "" {
					watchedRef = s.ProviderSessionRef
				}
			}
		case <-deadline:
			t.Fatal("Watch never received Her's conversation id")
		}
	}
	if watchedRef != "her_c1" {
		t.Fatalf("watched conversation id = %q", watchedRef)
	}

	// Her answers, then ends her turn on a question.
	conv.say(`{"type":"task_started","task_id":"t1"}`,
		`{"type":"tool_started","call_id":"c1","name":"send_to_user","args":{"type":"question","text":"Now or later?","options":[{"label":"Now"},{"label":"Later"}]}}`,
		`{"type":"task_completed","task_id":"t1"}`)
	proj := waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	q := proj.Session.GetProgress().GetPendingQuestion()
	if q == nil || len(q.Items) != 1 || q.Items[0].Text != "Now or later?" || len(q.Items[0].Options) != 2 {
		t.Fatalf("pending question: %v", q)
	}
	if proj.Session.Result != "Now or later?" {
		t.Fatalf("result: %q", proj.Session.Result)
	}
	if _, err := eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: id, RequestId: q.QuestionId, Answers: map[string]*sessionv1.QuestionAnswer{"q": {SelectedOptions: []string{"Now"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if o := conv.op(t); o["type"] != "prompt" || o["input"].([]any)[0].(map[string]any)["text"] != "Now" {
		t.Fatalf("the answer: %v", o)
	}
	waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)

	// An approval, in full access: still the owner's.
	conv.say(`{"type":"task_started","task_id":"t2"}`,
		`{"type":"approval_requested","call_id":"c2","name":"call_tool","args":{},"summary":"Start Codex in mini/payments\n\nmigrate checkout"}`)
	proj = waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	reqs, _ := eng.Store().RuntimeRequests(id)
	var pending *sessionv1.RuntimeRequest
	for _, r := range reqs {
		if r.RequestId == proj.Session.PendingRequestId {
			pending = r
		}
	}
	if pending == nil || pending.Kind != sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL || pending.GetApproval().GetCommand() != "Start Codex in mini/payments" {
		t.Fatalf("approval request: %v", pending)
	}
	if _, err := eng.RespondRuntimeRequest(ctx, owner, &sessionv1.RespondRuntimeRequestRequest{
		SessionId: id, RequestId: pending.RequestId, Decision: sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE,
	}); err != nil {
		t.Fatal(err)
	}
	if o := conv.op(t); o["type"] != "approval" || o["call_id"] != "c2" || o["decision"].(map[string]any)["type"] != "approve" {
		t.Fatalf("the approval: %v", o)
	}
	waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)

	// Answered in Her's own chat: the session goes on, and nothing more is sent.
	conv.say(`{"type":"approval_requested","call_id":"c3","name":"call_tool","args":{},"summary":"Write to session s_9"}`)
	waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	conv.say(`{"type":"approval_resolved","call_id":"c3","decision":{"type":"approve"}}`)
	waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)
	conv.say(`{"type":"tool_started","call_id":"c4","name":"send_to_user","args":{"text":"Both started."}}`, `{"type":"task_completed","task_id":"t2"}`)
	proj = waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if proj.Session.Result != "Both started." {
		t.Fatalf("result: %q", proj.Session.Result)
	}
	select {
	case o := <-conv.ops:
		t.Fatalf("sent after her own answer: %v", o)
	default:
	}

	// Her wakes on her own (news of the work she sent): the session runs, and settles on what she said.
	conv.say(`{"type":"task_started","task_id":"t3","trigger":"mail"}`,
		`{"type":"tool_started","call_id":"c5","name":"call_tool","title":"session_get","args":{}}`)
	waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)
	conv.say(`{"type":"tool_completed","call_id":"c5","result":{"tool_name":"call_tool"}}`,
		`{"type":"tool_started","call_id":"c6","name":"send_to_user","args":{"text":"Codex finished the migration."}}`,
		`{"type":"task_completed","task_id":"t3"}`)
	proj = waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	if proj.Session.Result != "Codex finished the migration." {
		t.Fatalf("result after waking: %q", proj.Session.Result)
	}

	// Left idle, its provider session is reaped; news then wakes Her, and Wake follows her again.
	eng.Supervisor().Detach(proj.Session.ActiveProviderSessionId, true)
	eng.onLeaseReaped(nil)
	if err := eng.Wake(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case conv = <-side.attached:
	case <-time.After(8 * time.Second):
		t.Fatal("not followed again")
	}
	if conv.id != "her_c1" {
		t.Fatalf("resumed %q", conv.id)
	}
	if err := eng.Wake(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-side.attached:
		t.Fatal("followed twice")
	case <-time.After(200 * time.Millisecond):
	}
	conv.say(`{"type":"task_started","task_id":"t4","trigger":"mail"}`,
		`{"type":"tool_started","call_id":"c7","name":"send_to_user","args":{"text":"Claude added the tests too."}}`,
		`{"type":"task_completed","task_id":"t4"}`)
	proj = waitForSessionStatus(t, eng, owner, id, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	deadline := time.Now().Add(8 * time.Second)
	for proj.Session.Result != "Claude added the tests too." && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		proj, _ = eng.GetSession(ctx, owner, id)
	}
	if proj.Session.Result != "Claude added the tests too." {
		t.Fatalf("result after waking again: %q", proj.Session.Result)
	}

	// Work Her sent from it names it, without being its sub-agent.
	child, err := eng.StartSession(ctx, OwnerPrincipal("owner", "Owner"), &sessionv1.StartSessionRequest{
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"}, ParentSessionId: id, ParentRelationship: "her", Title: "migrate checkout",
	})
	if err != nil {
		t.Fatal(err)
	}
	if l := child.Session.GetLineage(); l.GetParentSessionId() != id || l.GetRelationship() != "her" || l.GetControllable() {
		t.Fatalf("lineage: %v", l)
	}
}

// No agent opens a conversation with Her, sees one or is offered Her as a provider: it is the
// owner's own. Her's own tools see none either.
func TestHerConversationIsTheOwners(t *testing.T) {
	ctx := context.Background()
	eng, _ := openHerEngine(t)
	agent := GrantPrincipal(&identityv1.Grant{GrantId: "gr_1", ClientId: "cli_1", ProjectIds: []string{"*", ScratchProjectID}, Agents: []string{"*"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL})
	_, err := eng.StartSession(ctx, agent, &sessionv1.StartSessionRequest{ModelSelection: &sessionv1.ModelSelection{InstanceId: "her"}, Title: "x"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent opened one: %v", err)
	}
	owner := OwnerPrincipal("owner", "Owner")
	res, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{ModelSelection: &sessionv1.ModelSelection{InstanceId: "her"}, Title: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	herself := GrantPrincipal(&identityv1.Grant{GrantId: "gr_h", ClientId: "cli_h", HerMachineId: "m_test", ProjectIds: []string{"*"}, Agents: []string{"*"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL})
	for _, p := range []*Principal{agent, herself} {
		if _, err := eng.GetSession(ctx, p, res.Session.SessionId); err == nil {
			t.Fatalf("%v sees the owner's conversation with Her", p.Actor)
		}
		if provs, err := eng.ListProviders(ctx, p, ScratchProjectID, false); err == nil {
			for _, pr := range provs.Providers {
				if pr.Driver == "her" {
					t.Fatalf("%v is offered Her", p.Actor)
				}
			}
		}
	}
	if _, err := eng.GetSession(ctx, owner, res.Session.SessionId); err != nil {
		t.Fatalf("the owner: %v", err)
	}
	// Only Her says that work is hers.
	child := &sessionv1.StartSessionRequest{ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted"}, ParentSessionId: res.Session.SessionId, ParentRelationship: "her", Title: "y"}
	if _, err := eng.StartSession(ctx, agent, child); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent claimed Her's work: %v", err)
	}
	child.CommandId = "c_her"
	if got, err := eng.StartSession(ctx, herself, child); err != nil || got.Session.GetLineage().GetRelationship() != "her" {
		t.Fatalf("Her's work: %v %v", got, err)
	}
}

func TestIsSubagent(t *testing.T) {
	for _, c := range []struct {
		req  *sessionv1.StartSessionRequest
		want bool
	}{
		{&sessionv1.StartSessionRequest{}, false},
		{&sessionv1.StartSessionRequest{ParentSessionId: "s_1"}, true},
		{&sessionv1.StartSessionRequest{ParentSessionId: "s_1", ParentRelationship: "her"}, false},
	} {
		if got := isSubagent(c.req); got != c.want {
			t.Errorf("isSubagent(%v) = %v", c.req, got)
		}
	}
}
