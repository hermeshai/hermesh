package owner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// herScript is a machine whose her answers each Open with the results the
// test gives for its setup's methods, then the test's notifications.
type herScript struct {
	herMachine // only HerOpen is called
	results    map[string]string
	notes      []*herv1.Notification
}

func (h *herScript) HerOpen(_ context.Context, req *herv1.OpenRequest, send func(*herv1.OpenEvent) error) error {
	op := &herv1.Opened{ConnectionId: "c1"}
	for _, r := range req.Setup {
		op.Responses = append(op.Responses, &herv1.Response{Result: []byte(h.results[r.Method])})
	}
	if err := send(&herv1.OpenEvent{Kind: &herv1.OpenEvent_Opened{Opened: op}}); err != nil {
		return err
	}
	for _, n := range h.notes {
		if err := send(&herv1.OpenEvent{Kind: &herv1.OpenEvent_Notification{Notification: n}}); err != nil {
			return err
		}
	}
	return nil
}

// scriptedOwner is an owner whose machine m_1 is h, its store in dir.
func scriptedOwner(t *testing.T, dir string, h *herScript) (*Device, *Owner) {
	t.Helper()
	d, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	o := d.owner()
	o.herReach = func(context.Context, string) (herMachine, error) { return h, nil }
	return d, o
}

// herFollow opens a her connection on m_1 with setup, as the Her entry
// does, and takes all it sends.
func herFollow(t *testing.T, o *Owner, setup ...*herv1.Request) {
	t.Helper()
	if err := o.HerOpen(context.Background(), "m_1", &herv1.OpenRequest{Setup: setup}, func(*herv1.OpenEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

var herSubscribe = &herv1.Request{Method: "bots/subscribe"}

func herOpenBot(id string) *herv1.Request {
	return &herv1.Request{Method: "bots/open", Params: []byte(`{"bot_id":"` + id + `"}`)}
}

const (
	herBotJSON   = `{"id":"her","name":"Her","default":true,"avatar":{"kind":"mark"},"created_at":"2026-11-01T00:00:00Z"}`
	otherBotJSON = `{"id":"other","name":"Other","avatar":{"kind":"mark"},"created_at":"2026-11-01T00:00:00Z"}`
)

// Items of her's history, and the session events that add them.

func herUserMsg(id, text string) string {
	return fmt.Sprintf(`{"type":"message","message_id":%q,"message":{"role":"user","content":[{"type":"text","text":%q}],"ts":1}}`, id, text)
}

// herAssistantMsg is an assistant's message: its thinking, words for
// itself, and a call for each "id:tool".
func herAssistantMsg(id string, calls ...string) string {
	content := []string{`{"type":"thinking","text":"hmm"}`, `{"type":"text","text":"for myself"}`}
	for _, c := range calls {
		callID, tool, _ := strings.Cut(c, ":")
		content = append(content, fmt.Sprintf(`{"type":"tool_call","id":%q,"name":%q,"arguments":{"text":"to the owner"}}`, callID, tool))
	}
	return fmt.Sprintf(`{"type":"message","message_id":%q,"message":{"role":"assistant","content":[%s],"ts":2,"usage":{"input":1,"output":1}}}`, id, strings.Join(content, ","))
}

func herToolResult(callID string) string {
	return fmt.Sprintf(`{"type":"tool_result","call_id":%q,"result":{"tool_name":"t","content":[],"is_error":false}}`, callID)
}

func herTaskEnd(id, outcome string) string {
	return fmt.Sprintf(`{"type":"task_end","task_id":%q,"outcome":%q,"ts":3}`, id, outcome)
}

// herOpened is what bots/open answers with: the conversation conv, the end
// of its history, and a cursor to what came before.
func herOpened(conv string, lastSeq int, items ...string) string {
	return fmt.Sprintf(`{"session_id":%q,"snapshot":{"session":{"session_id":%q,"title":"Her"},"last_seq":%d,"transcript":{"items":[%s],"before":"earlier"},"usage":{"per_model":{}},"active_task":null}}`,
		conv, conv, lastSeq, strings.Join(items, ","))
}

// herSessionEvent is session/event on conv: the event's seq, its type and
// its other fields.
func herSessionEvent(conv string, seq int, typ, fields string) *herv1.Notification {
	if fields != "" {
		fields = "," + fields
	}
	return &herv1.Notification{Method: "session/event", Params: []byte(fmt.Sprintf(`{"session_id":%q,"event":{"seq":%d,"ts":9,"type":%q%s}}`, conv, seq, typ, fields))}
}

// herMessageEvent is message_completed for a message item.
func herMessageEvent(conv string, seq int, item string) *herv1.Notification {
	var it map[string]json.RawMessage
	json.Unmarshal([]byte(item), &it)
	return herSessionEvent(conv, seq, "message_completed", fmt.Sprintf(`"message_id":%s,"message":%s`, it["message_id"], it["message"]))
}

// keptOf is what GetHerConversation gives for m_1: Her's id and name, each
// item of Her's conversation ("user u1", "assistant a1 c1:send_to_user"
// with any other content's type, "result c1", "end t1 completed"), and the
// snapshot.
func keptOf(t *testing.T, d *Device) (bot string, items []string, snapshot map[string]json.RawMessage) {
	t.Helper()
	res, err := (&localService{d: d}).GetHerConversation(context.Background(), &localv1.GetHerConversationRequest{MachineId: "m_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bot) > 0 {
		var b struct{ ID, Name string }
		if err := json.Unmarshal(res.Bot, &b); err != nil {
			t.Fatal(err)
		}
		bot = b.ID + " " + b.Name
	}
	if len(res.Snapshot) == 0 {
		return bot, nil, nil
	}
	if err := json.Unmarshal(res.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	var transcript struct {
		Items []struct {
			Type      string `json:"type"`
			MessageID string `json:"message_id"`
			CallID    string `json:"call_id"`
			TaskID    string `json:"task_id"`
			CardID    string `json:"card_id"`
			Outcome   string `json:"outcome"`
			Message   struct {
				Role    string `json:"role"`
				Content []struct{ Type, ID, Name string }
			} `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(snapshot["transcript"], &transcript); err != nil {
		t.Fatal(err)
	}
	items = []string{}
	for _, it := range transcript.Items {
		switch {
		case it.Type == "message" && it.Message.Role == "assistant":
			s := "assistant " + it.MessageID
			for _, c := range it.Message.Content {
				if c.Type == "tool_call" {
					s += " " + c.ID + ":" + c.Name
				} else {
					s += " " + c.Type
				}
			}
			items = append(items, s)
		case it.Type == "message":
			items = append(items, it.Message.Role+" "+it.MessageID)
		case it.Type == "tool_result":
			items = append(items, "result "+it.CallID)
		case it.Type == "task_end":
			items = append(items, "end "+it.TaskID+" "+it.Outcome)
		case it.Type == "card_resolved":
			items = append(items, "card "+it.CardID+" "+it.Outcome)
		}
	}
	return bot, items, snapshot
}

// The Her entry's streams leave here Her, as bots/subscribe and
// bots/changed list the assistants, and the end of Her's conversation as
// the entry shows it: the owner's messages, Her's with only the calls that
// sent the owner something and their results, and how each task ended;
// none of her's own steps, no earlier history, nothing of another
// assistant. Events after the conversation opened add to it in order, as
// the entry's own view applies them; a restart keeps it.
func TestHerConversationKept(t *testing.T) {
	dir := t.TempDir()
	h := &herScript{results: map[string]string{"bots/subscribe": `{"bots":[` + otherBotJSON + `,` + herBotJSON + `]}`}}
	d, o := scriptedOwner(t, dir, h)
	if bot, items, _ := keptOf(t, d); bot != "" || items != nil {
		t.Fatalf("kept before anything was opened: %q %q", bot, items)
	}
	// Whose conversation it is can't be told before Her was listed.
	h.results["bots/open"] = herOpened("conv-her", 4, herUserMsg("u0", "too early"))
	herFollow(t, o, herOpenBot("her"))
	if bot, items, _ := keptOf(t, d); bot != "" || items != nil {
		t.Fatalf("kept before Her was listed: %q %q", bot, items)
	}

	herFollow(t, o, herSubscribe)
	h.results["bots/open"] = herOpened("conv-her", 4,
		herUserMsg("u1", "hello"),
		herAssistantMsg("a1", "c1:read_file", "c2:send_to_user"),
		herToolResult("c1"),
		herToolResult("c2"),
		herAssistantMsg("a2"), // her's own words, which the entry doesn't show
		herTaskEnd("t1", "completed"),
	)
	sent := `{"role":"assistant","content":[{"type":"tool_call","id":"c3","name":"send_to_user","arguments":{"text":"a routine"}},{"type":"tool_call","id":"c4","name":"manage_routine","arguments":{}}],"ts":5}`
	h.notes = []*herv1.Notification{
		herSessionEvent("conv-her", 4, "task_completed", `"task_id":"old","reason":"done","usage":{}`), // in the snapshot already
		herMessageEvent("conv-her", 5, herUserMsg("u2", "a routine, please")),
		herSessionEvent("conv-her", 6, "tool_started", `"call_id":"c3","name":"send_to_user"`),
		herSessionEvent("conv-her", 7, "message_completed", `"message_id":"a3","message":`+sent),
		herSessionEvent("conv-her", 8, "tool_completed", `"call_id":"c3","result":{"tool_name":"send_to_user","content":[],"is_error":false}`),
		herSessionEvent("conv-her", 9, "tool_completed", `"call_id":"c4","result":{"tool_name":"manage_routine","content":[],"is_error":false}`),
		herSessionEvent("conv-other", 10, "task_failed", `"task_id":"elsewhere","error":{"kind":"x","message":"x"}`), // another conversation's
		herSessionEvent("conv-her", 10, "task_failed", `"task_id":"t2","error":{"kind":"provider","message":"down"},"usage":{},"returned_input":[]`),
		{Method: "bots/changed", Params: []byte(`{"bots":[` + strings.Replace(herBotJSON, `"Her"`, `"Her 2"`, 1) + `]}`)},
	}
	herFollow(t, o, herOpenBot("her"))
	want := strings.Join([]string{"user u1", "assistant a1 c2:send_to_user", "result c2", "end t1 completed",
		"user u2", "assistant a3 c3:send_to_user c4:manage_routine", "result c3", "result c4", "end t2 failed"}, "|")
	bot, items, snapshot := keptOf(t, d)
	if bot != "her Her 2" || strings.Join(items, "|") != want {
		t.Fatalf("kept %q %q\nwant %s", bot, items, want)
	}
	var transcript struct {
		Before *string `json:"before"`
		Items  []struct {
			Error struct{ Message string }
			TS    int
		}
	}
	json.Unmarshal(snapshot["transcript"], &transcript)
	if string(snapshot["last_seq"]) != "10" || transcript.Before != nil || snapshot["active_task"] != nil || !strings.Contains(string(snapshot["session"]), `"conv-her"`) {
		t.Fatalf("snapshot: %s", snapshot)
	}
	if last := transcript.Items[len(transcript.Items)-1]; last.Error.Message != "down" || last.TS != 9 {
		t.Fatalf("the failed task's end: %+v", last)
	}

	// Another assistant's conversation leaves Her's as it was.
	h.notes = nil
	h.results["bots/open"] = herOpened("conv-other", 3, herUserMsg("x1", "not Her"))
	herFollow(t, o, herOpenBot("other"))
	if _, again, _ := keptOf(t, d); strings.Join(again, "|") != want {
		t.Fatalf("after another assistant's: %q", again)
	}

	d.Close()
	d, _ = scriptedOwner(t, dir, h)
	defer d.Close()
	if bot, again, _ := keptOf(t, d); bot != "her Her 2" || strings.Join(again, "|") != want {
		t.Fatalf("after a restart: %q %q", bot, again)
	}
}

// A conversation is kept to its last 100 messages, the owner's and Her's,
// with what came after the first of them; going back in it drops what came
// from where it went back to.
func TestHerConversationTrimmed(t *testing.T) {
	h := &herScript{results: map[string]string{"bots/subscribe": `{"bots":[` + herBotJSON + `]}`}}
	d, o := scriptedOwner(t, t.TempDir(), h)
	defer d.Close()
	herFollow(t, o, herSubscribe)
	var history []string
	for i := range 60 {
		history = append(history, herUserMsg(fmt.Sprint("u", i), "hi"), herAssistantMsg(fmt.Sprint("a", i), fmt.Sprint("c", i, ":send_to_user")),
			herToolResult(fmt.Sprint("c", i)), herTaskEnd(fmt.Sprint("t", i), "completed"))
	}
	h.results["bots/open"] = herOpened("conv-her", 240, history...)
	h.notes = []*herv1.Notification{herMessageEvent("conv-her", 241, herUserMsg("u60", "one more"))}
	herFollow(t, o, herOpenBot("her"))
	// 121 messages: a10 is the 100th from the end. It stays with its result
	// and task's end, then 49 exchanges of four items, then u60.
	_, items, _ := keptOf(t, d)
	if len(items) != 3+49*4+1 || items[0] != "assistant a10 c10:send_to_user" || items[1] != "result c10" || items[len(items)-1] != "user u60" {
		t.Fatalf("kept %d: %q … %q", len(items), items[:min(3, len(items))], items[len(items)-1])
	}

	h.notes = []*herv1.Notification{
		herSessionEvent("conv-her", 241, "session_rewound", `"message_id":"u58"`),
		herMessageEvent("conv-her", 242, herUserMsg("u58b", "said again")),
	}
	herFollow(t, o, herOpenBot("her"))
	_, items, _ = keptOf(t, d)
	if tail := strings.Join(items[len(items)-3:], "|"); tail != "result c57|end t57 completed|user u58b" {
		t.Fatalf("after going back: … %s", tail)
	}
	h.notes = []*herv1.Notification{herSessionEvent("conv-her", 241, "session_rewound", `"message_id":"long-gone"`)}
	herFollow(t, o, herOpenBot("her"))
	if _, items, snapshot := keptOf(t, d); len(items) != 0 || snapshot == nil {
		t.Fatalf("after going back past all that is kept: %q", items)
	}
}

// What is kept of a machine's Her goes when Her is turned off there, and
// when the machine is removed.
func TestHerConversationForgotten(t *testing.T) {
	h := &herScript{results: map[string]string{"bots/subscribe": `{"bots":[` + herBotJSON + `]}`, "bots/open": herOpened("conv-her", 1, herUserMsg("u1", "hello"))}}
	d, o := scriptedOwner(t, t.TempDir(), h)
	defer d.Close()
	keep := func() {
		t.Helper()
		herFollow(t, o, herSubscribe)
		herFollow(t, o, herOpenBot("her"))
		if bot, items, _ := keptOf(t, d); bot == "" || len(items) != 1 {
			t.Fatalf("not kept: %q %q", bot, items)
		}
	}
	status := func(her *machinev1.HerStatus) {
		o.withHer([]*localv1.Machine{{MachineId: "m_1", Status: &machinev1.MachineStatus{Her: her}}})
	}
	on := &machinev1.HerStatus{Enabled: true, Running: true, ModelReady: true}
	status(on)
	keep()
	status(on)
	status(&machinev1.HerStatus{Enabled: true, Running: true}) // the model's login expired
	if bot, _, _ := keptOf(t, d); bot == "" {
		t.Fatal("forgotten while Her is on")
	}
	status(&machinev1.HerStatus{Version: "0.3.0"})
	if bot, items, _ := keptOf(t, d); bot != "" || items != nil {
		t.Fatalf("Her turned off: %q %q", bot, items)
	}

	keep()
	o.forgetHer("m_1")
	if bot, items, _ := keptOf(t, d); bot != "" || items != nil {
		t.Fatalf("the machine removed: %q %q", bot, items)
	}
	if _, err := (&localService{d: d}).GetHerConversation(context.Background(), &localv1.GetHerConversationRequest{}); err == nil {
		t.Fatal("no machine: no error")
	}
}

// A secure card's end is kept, as her's history has it or as a
// card_changed says it live, so a device opening Her's conversation
// offline draws the card ended; its steps on the way aren't.
func TestHerConversationCardEnded(t *testing.T) {
	h := &herScript{results: map[string]string{"bots/subscribe": `{"bots":[` + herBotJSON + `]}`}}
	d, o := scriptedOwner(t, t.TempDir(), h)
	defer d.Close()
	herFollow(t, o, herSubscribe)
	h.results["bots/open"] = herOpened("conv-her", 4,
		herUserMsg("u1", "connect notion"),
		herAssistantMsg("a1", "c1:send_to_user"),
		herToolResult("c1"),
		`{"type":"card_resolved","card_id":"c_old","outcome":"declined","note":""}`,
	)
	h.notes = []*herv1.Notification{
		herSessionEvent("conv-her", 5, "card_changed", `"card_id":"c_new","state":"sign_in"`),
		herSessionEvent("conv-her", 6, "card_changed", `"card_id":"c_new","state":"done","note":"Notion connected: 14 tools"`),
	}
	herFollow(t, o, herOpenBot("her"))
	want := "user u1|assistant a1 c1:send_to_user|result c1|card c_old declined|card c_new done"
	_, items, snapshot := keptOf(t, d)
	if strings.Join(items, "|") != want {
		t.Fatalf("kept %q\nwant %s", items, want)
	}
	if !strings.Contains(string(snapshot["transcript"]), `"note":"Notion connected: 14 tools"`) {
		t.Fatalf("the card's note is not kept: %s", snapshot["transcript"])
	}
}
