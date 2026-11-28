package her

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

// fakeBridge stands in for the Her service: conversations whose events the test writes and whose
// ops it reads.
type fakeBridge struct {
	mu      sync.Mutex
	made    []string // model/thinking of each new conversation
	convs   map[string]*fakeConv
	ops     []any // one-shot ops
	deleted []string
}

func newFakeBridge() *fakeBridge { return &fakeBridge{convs: map[string]*fakeConv{}} }

func (b *fakeBridge) Available() bool { return true }
func (b *fakeBridge) Models(context.Context) ([]*sessionv1.ModelCatalogEntry, error) {
	return []*sessionv1.ModelCatalogEntry{{Slug: "tc/auto", Name: "Auto", IsDefault: true}, {Slug: "tc/glm", Name: "GLM-5.3"}}, nil
}
func (b *fakeBridge) NewConversation(_ context.Context, model, thinking string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.made = append(b.made, model+"/"+thinking)
	return "her_conv_1", nil
}
func (b *fakeBridge) Attach(_ context.Context, id string) (Conversation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &fakeConv{events: make(chan json.RawMessage, 16), ops: make(chan map[string]any, 16)}
	b.convs[id] = c
	return c, nil
}
func (b *fakeBridge) Op(_ context.Context, id string, op any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ops = append(b.ops, op)
	return nil
}
func (b *fakeBridge) DeleteConversation(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, id)
	return nil
}

type fakeConv struct {
	events chan json.RawMessage
	ops    chan map[string]any
	once   sync.Once
}

func (c *fakeConv) Events() <-chan json.RawMessage { return c.events }
func (c *fakeConv) Op(_ context.Context, op any) error {
	b, _ := json.Marshal(op)
	var m map[string]any
	json.Unmarshal(b, &m)
	c.ops <- m
	return nil
}
func (c *fakeConv) Close() error { c.once.Do(func() { close(c.events) }); return nil }

func (c *fakeConv) say(t *testing.T, ev string) {
	t.Helper()
	c.events <- json.RawMessage(ev)
}

func next(t *testing.T, s runtime.Session) runtime.Event {
	t.Helper()
	select {
	case ev := <-s.Events():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return runtime.Event{}
}

func op(t *testing.T, c *fakeConv) map[string]any {
	t.Helper()
	select {
	case o := <-c.ops:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("no op")
	}
	return nil
}

func start(t *testing.T, b *fakeBridge, gate runtime.GateFunc, resume string) (runtime.Session, *fakeConv) {
	t.Helper()
	sel := &sessionv1.ModelSelection{InstanceId: "her", Model: "tc/glm", Options: map[string]*sessionv1.OptionValue{"effort": {Kind: &sessionv1.OptionValue_StringValue{StringValue: "low"}}}}
	s, err := NewDriver(b).StartSession(context.Background(), runtime.StartSpec{Selection: sel, Gate: gate, ResumeCursor: resume})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ev := next(t, s); ev.Kind != runtime.EventNativeSession || ev.NativeID == "" {
		t.Fatalf("first event: %+v", ev)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.convs {
		return s, c
	}
	t.Fatal("not attached")
	return nil, nil
}

// A new session starts a conversation on the model and thinking chosen; one resumed follows its own.
func TestStartAndResume(t *testing.T) {
	b := newFakeBridge()
	start(t, b, nil, "")
	if len(b.made) != 1 || b.made[0] != "tc/glm/low" {
		t.Fatalf("new conversation: %v", b.made)
	}
	b2 := newFakeBridge()
	start(t, b2, nil, "her_conv_9")
	if len(b2.made) != 0 || b2.convs["her_conv_9"] == nil {
		t.Fatalf("resumed: made %v, attached %v", b2.made, b2.convs)
	}
}

// A turn: the prompt goes to her as the owner's words; what she sends becomes the session's
// messages, her tools its tool items, and her task's end the turn's.
func TestTurn(t *testing.T) {
	b := newFakeBridge()
	s, c := start(t, b, nil, "")
	if err := s.StartTurn(context.Background(), runtime.TurnInput{RunID: "run_01", Prompt: "plan v2.4"}); err != nil {
		t.Fatal(err)
	}
	if o := op(t, c); o["type"] != "prompt" || o["id"] != "hm_run_01" || o["input"].([]any)[0].(map[string]any)["text"] != "plan v2.4" {
		t.Fatalf("prompt op: %v", o)
	}
	c.say(t, `{"type":"task_started","task_id":"t1"}`)
	c.say(t, `{"type":"tool_started","call_id":"c1","name":"call_tool","title":"session_start","args":{}}`)
	c.say(t, `{"type":"tool_completed","call_id":"c1","result":{"tool_name":"call_tool","is_error":false}}`)
	c.say(t, `{"type":"tool_started","call_id":"c2","name":"send_to_user","args":{"text":"Started it."}}`)
	c.say(t, `{"type":"tool_completed","call_id":"c2","result":{"tool_name":"send_to_user"}}`)
	c.say(t, `{"type":"task_completed","task_id":"t1"}`)
	want := []runtime.EventKind{runtime.EventToolStarted, runtime.EventToolCompleted, runtime.EventAssistantMessage, runtime.EventTurnCompleted}
	for i, k := range want {
		ev := next(t, s)
		if ev.Kind != k {
			t.Fatalf("event %d: %+v, want kind %d", i, ev, k)
		}
		if k == runtime.EventAssistantMessage && ev.Text != "Started it." {
			t.Fatalf("message: %q", ev.Text)
		}
	}
	c.say(t, `{"type":"task_failed","task_id":"t2","error":{"kind":"quota_exceeded","message":"out of tokens"}}`)
	if ev := next(t, s); ev.Kind != runtime.EventTurnFailed || ev.ErrorMessage != "out of tokens" {
		t.Fatalf("failure: %+v", ev)
	}
}

// A question with answers to tap ends the turn on it; the answer comes back as the owner's words.
func TestQuestion(t *testing.T) {
	b := newFakeBridge()
	s, c := start(t, b, nil, "")
	c.say(t, `{"type":"tool_started","call_id":"c3","name":"send_to_user","args":{"type":"question","text":"Now or later?","options":[{"label":"Now"},{"label":"Later"}]}}`)
	if ev := next(t, s); ev.Kind != runtime.EventAssistantMessage {
		t.Fatalf("the question's words: %+v", ev)
	}
	ev := next(t, s)
	if ev.Kind != runtime.EventQuestionAsked || len(ev.Question.Questions) != 1 {
		t.Fatalf("question: %+v", ev)
	}
	q := ev.Question.Questions[0]
	if q.Text != "Now or later?" || len(q.Options) != 2 || q.Options[0].Label != "Now" || !q.AllowOther {
		t.Fatalf("question item: %v", q)
	}
	// Words alone are no question.
	c.say(t, `{"type":"tool_started","call_id":"c4","name":"send_to_user","args":{"text":"Done."}}`)
	c.say(t, `{"type":"task_completed","task_id":"t1"}`)
	if next(t, s).Kind != runtime.EventAssistantMessage || next(t, s).Kind != runtime.EventTurnCompleted {
		t.Fatal("a plain message asked something")
	}
	// The kernel's report of the answer reaches her as what the owner tapped.
	s.StartTurn(context.Background(), runtime.TurnInput{RunID: "r2", Prompt: "Answers to previous questions:\nq: Now"})
	if o := op(t, c); o["input"].([]any)[0].(map[string]any)["text"] != "Now" {
		t.Fatalf("answer: %v", o)
	}
}

// An approval Her asks for is put to the owner as delegated; their answer goes back to her. One
// answered in her own chat ends the wait.
func TestApproval(t *testing.T) {
	asked := make(chan runtime.ToolGateRequest, 2)
	answer := make(chan runtime.ToolGateResult, 2)
	gate := func(ctx context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
		asked <- req
		select {
		case r := <-answer:
			return r, nil
		case <-ctx.Done():
			return runtime.ToolGateResult{CancelTurn: true}, ctx.Err()
		}
	}
	b := newFakeBridge()
	_, c := start(t, b, gate, "")
	c.say(t, `{"type":"approval_requested","call_id":"c5","name":"call_tool","args":{},"summary":"Start Claude Code in mini/app\n\nfix the test"}`)
	req := <-asked
	if !req.Delegated || req.Approval.GetCommand() != "Start Claude Code in mini/app" || req.Approval.GetDiffPreview() != "fix the test" || req.Approval.GetToolCategory() != "her" {
		t.Fatalf("request: %+v", req)
	}
	answer <- runtime.ToolGateResult{Allowed: true}
	if o := op(t, c); o["type"] != "approval" || o["call_id"] != "c5" || o["decision"].(map[string]any)["type"] != "approve" {
		t.Fatalf("approve: %v", o)
	}
	c.say(t, `{"type":"approval_requested","call_id":"c6","name":"call_tool","args":{},"summary":"Write to session s_1"}`)
	<-asked
	answer <- runtime.ToolGateResult{Allowed: false, Feedback: "not now"}
	if o := op(t, c); o["decision"].(map[string]any)["type"] != "deny" || o["decision"].(map[string]any)["feedback"] != "not now" {
		t.Fatalf("deny: %v", o)
	}
	// Answered in Her's chat: the wait ends and nothing more is sent to her.
	c.say(t, `{"type":"approval_requested","call_id":"c7","name":"call_tool","args":{},"summary":"x"}`)
	<-asked
	c.say(t, `{"type":"approval_resolved","call_id":"c7","decision":{"type":"approve"}}`)
	select {
	case o := <-c.ops:
		t.Fatalf("sent after her own answer: %v", o)
	case <-time.After(300 * time.Millisecond):
	}
}

// Deleting and archiving the session reach her conversation.
func TestDeleteAndArchive(t *testing.T) {
	b := newFakeBridge()
	d := NewDriver(b)
	d.Delete(context.Background(), "her_conv_1")
	d.Archive(context.Background(), "her_conv_1", true)
	if len(b.deleted) != 1 || len(b.ops) != 1 || b.ops[0].(map[string]any)["type"] != "set_archived" {
		t.Fatalf("deleted %v, ops %v", b.deleted, b.ops)
	}
	if r := d.Probe(context.Background(), nil, ""); !r.Installed || len(r.Models) != 2 || !r.Models[0].IsDefault {
		t.Fatalf("probe: %+v", r)
	}
}

func TestAsOwner(t *testing.T) {
	for in, want := range map[string]string{
		"plain words":                              "plain words",
		"Answers to previous questions:\nq: Now":   "Now",
		"Answers to previous questions:\nq: A, B":  "A, B",
		"Answers to previous questions:\nnonsense": "Answers to previous questions:\nnonsense",
	} {
		if got := asOwner(in); got != want {
			t.Errorf("asOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sub-agents Her spawns keep the session active as awaited background tasks across her turn's end,
// forward their approvals via agent_approval, and settle without a Done flash when their report
// wakes her (or on idle when stopped).
func TestSubAgentsAndApproval(t *testing.T) {
	asked := make(chan runtime.ToolGateRequest, 2)
	answer := make(chan runtime.ToolGateResult, 2)
	gate := func(ctx context.Context, req runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
		asked <- req
		select {
		case r := <-answer:
			return r, nil
		case <-ctx.Done():
			return runtime.ToolGateResult{CancelTurn: true}, ctx.Err()
		}
	}
	b := newFakeBridge()
	s, c := start(t, b, gate, "")
	if err := s.StartTurn(context.Background(), runtime.TurnInput{RunID: "r_sub", Prompt: "research"}); err != nil {
		t.Fatal(err)
	}
	_ = op(t, c) // prompt

	c.say(t, `{"type":"task_started","task_id":"t1"}`)
	c.say(t, `{"type":"agent_started","agent_id":"ag_1","name":"researcher","description":"查资料","call_id":"c_sp1"}`)
	c.say(t, `{"type":"tool_started","call_id":"c_say","name":"send_to_user","args":{"text":"交给子智能体了。"}}`)
	c.say(t, `{"type":"task_completed","task_id":"t1"}`)

	bgRun := next(t, s)
	if bgRun.Kind != runtime.EventBackgroundTask || bgRun.TaskID != "ag_1" || bgRun.ToolName != "查资料" ||
		bgRun.BackgroundKind != sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT ||
		bgRun.BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING || !bgRun.BackgroundAwaited {
		t.Fatalf("agent_started event: %+v", bgRun)
	}
	if next(t, s).Kind != runtime.EventAssistantMessage || next(t, s).Kind != runtime.EventTurnCompleted {
		t.Fatal("expected assistant message and turn completion while sub-agent runs")
	}

	// Sub-agent asks for approval while Her is between turns -> routed as agent_approval.
	c.say(t, `{"type":"agent_event","agent_id":"ag_1","event":{"type":"approval_requested","call_id":"c_sub_1","name":"bash","summary":"touch a.txt"}}`)
	req := <-asked
	if !req.Delegated || req.Approval.GetCommand() != "touch a.txt" {
		t.Fatalf("sub-agent approval: %+v", req)
	}
	answer <- runtime.ToolGateResult{Allowed: true}
	if o := op(t, c); o["type"] != "agent_approval" || o["agent"] != "ag_1" || o["call_id"] != "c_sub_1" {
		t.Fatalf("agent_approval op: %v", o)
	}

	// Sub-agent finishes while !inTask: held until Her's wakeup task_started so active_background_count
	// never drops to 0 before Her's continuation turn is running.
	c.say(t, `{"type":"agent_status","agent_id":"ag_1","name":"researcher","status":"completed","summary":"查完了"}`)
	select {
	case ev := <-s.Events():
		t.Fatalf("premature settlement before task_started/idle: %+v", ev)
	case <-time.After(80 * time.Millisecond):
	}
	c.say(t, `{"type":"task_started","task_id":"t2","trigger":"mail"}`)
	if ev := next(t, s); ev.Kind != runtime.EventReasoningDelta {
		t.Fatalf("wakeup should open continuation run first, got %+v", ev)
	}
	bgDone := next(t, s)
	if bgDone.Kind != runtime.EventBackgroundTask || bgDone.TaskID != "ag_1" ||
		bgDone.BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED || bgDone.Text != "查完了" {
		t.Fatalf("settled sub-agent event: %+v", bgDone)
	}
	c.say(t, `{"type":"tool_started","call_id":"c_fin","name":"send_to_user","args":{"text":"汇总好了。"}}`)
	c.say(t, `{"type":"task_completed","task_id":"t2"}`)
	c.say(t, `{"type":"idle"}`)
	if next(t, s).Kind != runtime.EventAssistantMessage || next(t, s).Kind != runtime.EventTurnCompleted {
		t.Fatal("expected wakeup reply and turn completion")
	}

	// Stopping a running sub-agent sends agent_interrupt, and idle flushes its stopped state.
	c.say(t, `{"type":"agent_started","agent_id":"ag_2","name":"runner","call_id":"c_sp2"}`)
	_ = next(t, s)
	bc := s.(runtime.BackgroundController)
	if err := bc.StopBackgroundTask(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if o := op(t, c); o["type"] != "agent_interrupt" || o["agent"] != "ag_2" {
		t.Fatalf("agent_interrupt op: %v", o)
	}
	c.say(t, `{"type":"agent_status","agent_id":"ag_2","name":"runner","status":"interrupted"}`)
	c.say(t, `{"type":"idle"}`)
	if ev := next(t, s); ev.Kind != runtime.EventBackgroundTask || ev.BackgroundStatus != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED {
		t.Fatalf("stopped sub-agent on idle: %+v", ev)
	}
}

func TestSteer(t *testing.T) {
	b := newFakeBridge()
	drv := NewDriver(b)
	if !drv.Capabilities("").GetMidTurnSteer() {
		t.Fatal("expected Her driver to advertise MidTurnSteer")
	}
	s, c := start(t, b, nil, "")
	if err := s.Steer(context.Background(), "too early", nil); err == nil {
		t.Fatal("expected Steer to fail when no turn is active")
	}
	if err := s.StartTurn(context.Background(), runtime.TurnInput{RunID: "run_s1", Prompt: "run suite"}); err != nil {
		t.Fatal(err)
	}
	_ = op(t, c)
	c.say(t, `{"type":"task_started","task_id":"t_s1"}`)
	if err := s.Steer(context.Background(), "还没测完吗", nil); err != nil {
		t.Fatalf("Steer during active Her task: %v", err)
	}
	steeredOp := op(t, c)
	if steeredOp["type"] != "prompt" || steeredOp["mode"] != "steer" || steeredOp["input"].([]any)[0].(map[string]any)["text"] != "还没测完吗" {
		t.Fatalf("unexpected steer op: %v", steeredOp)
	}
	c.say(t, `{"type":"tool_started","call_id":"c_rep","name":"send_to_user","args":{"text":"还在跑，马上好。"}}`)
	c.say(t, `{"type":"task_completed","task_id":"t_s1"}`)
	if ev := next(t, s); ev.Kind != runtime.EventAssistantMessage || ev.Text != "还在跑，马上好。" {
		t.Fatalf("expected steered reply, got %+v", ev)
	}
	if ev := next(t, s); ev.Kind != runtime.EventTurnCompleted {
		t.Fatalf("expected clean turn completion without interruption, got %+v", ev)
	}
}
