package owner

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
)

// Her's conversations kept on this device (docs/her-server.md §7): for each
// machine, Her as bots/subscribe last listed the assistants, and the end of
// Her's conversation as the Her entry shows it, so that the entry opens at
// once and an offline environment still shows what was said, after a
// restart too. Both are taken from the HerOpen streams this device passes
// on while the Her entry follows them: her is asked nothing more.

// herKeptMessages is how many of a conversation's messages are kept: the
// owner's and the assistant's, with what goes with them.
const herKeptMessages = 100

// herSends are the tools whose calls the Her entry shows: a message to the
// owner, and a change to a routine (packages/her-client's botChat).
var herSends = map[string]bool{"send_to_user": true, "manage_routine": true}

// herHistory: the session events that add to a conversation's history, as
// the item each adds (her's TranscriptItem): its type, a task's outcome, and
// the event's fields it holds.
var herHistory = map[string]struct {
	item, outcome string
	fields        []string
}{
	"message_completed": {"message", "", []string{"message_id", "message", "skills", "mail"}},
	"tool_completed":    {"tool_result", "", []string{"call_id", "result"}},
	"task_completed":    {"task_end", "completed", []string{"task_id", "reason", "usage", "ts"}},
	"task_failed":       {"task_end", "failed", []string{"task_id", "error", "usage", "ts"}},
	"task_interrupted":  {"task_end", "interrupted", []string{"task_id", "usage", "ts"}},
}

// herFollower keeps what one HerOpen stream on a machine says of Her.
type herFollower struct {
	o       *Owner
	machine string
	setup   []*herv1.Request
	conv    *herConversation // Her's, once the stream opened it (bots/open)
}

// see keeps what e says of Her.
func (f *herFollower) see(e *herv1.OpenEvent) {
	if op := e.GetOpened(); op != nil {
		f.opened(op)
	} else if n := e.GetNotification(); n != nil {
		f.notified(n)
	}
}

func (f *herFollower) opened(op *herv1.Opened) {
	for i, r := range f.setup {
		if i >= len(op.Responses) || op.Responses[i].Error != nil {
			continue
		}
		result := op.Responses[i].Result
		switch r.Method {
		case "bots/subscribe":
			var list struct {
				Bots []json.RawMessage `json:"bots"`
			}
			if json.Unmarshal(result, &list) == nil {
				f.keepBot(list.Bots)
			}
		case "bots/open":
			var p struct {
				BotID string `json:"bot_id"`
			}
			if json.Unmarshal(r.Params, &p) != nil || p.BotID == "" || p.BotID != f.o.st.herBotID(f.machine) {
				continue // not Her's
			}
			if f.conv = openedConversation(result); f.conv != nil {
				f.keep()
			}
		}
	}
}

func (f *herFollower) notified(n *herv1.Notification) {
	switch {
	case n.Method == "bots/changed":
		var changed struct {
			Bots []json.RawMessage `json:"bots"`
		}
		if json.Unmarshal(n.Params, &changed) == nil {
			f.keepBot(changed.Bots)
		}
	case n.Method == "session/event" && f.conv != nil:
		var p struct {
			SessionID string          `json:"session_id"`
			Event     json.RawMessage `json:"event"`
		}
		if json.Unmarshal(n.Params, &p) == nil && p.SessionID == f.conv.session && f.conv.apply(p.Event) {
			f.keep()
		}
	}
}

// keepBot keeps Her, the default assistant, when bots holds Her.
func (f *herFollower) keepBot(bots []json.RawMessage) {
	for _, b := range bots {
		var bot struct {
			Default bool `json:"default"`
		}
		if json.Unmarshal(b, &bot) != nil || !bot.Default {
			continue
		}
		if _, err := f.o.st.db.Exec(`INSERT INTO her_bots(machine_id,bot) VALUES(?,?) ON CONFLICT(machine_id) DO UPDATE SET bot=excluded.bot WHERE bot<>excluded.bot`, f.machine, []byte(b)); err != nil {
			f.o.log.Warn("Her not kept", "machine", f.machine, "err", err)
		}
	}
}

// keep keeps Her's conversation as it is now.
func (f *herFollower) keep() {
	if _, err := f.o.st.db.Exec(`INSERT INTO her_conversations(machine_id,snapshot) VALUES(?,?) ON CONFLICT(machine_id) DO UPDATE SET snapshot=excluded.snapshot`, f.machine, f.conv.snapshot()); err != nil {
		f.o.log.Warn("Her's conversation not kept", "machine", f.machine, "err", err)
	}
}

// herConversation is the end of an assistant's conversation, as kept here:
// the parts of her's SessionSnapshot the Her entry draws, its history only
// what the entry shows (shown).
type herConversation struct {
	session string
	snap    herSnapshot
	items   []herItem
	calls   map[string]bool // the calls kept, whose results are
}

// herSnapshot is what is kept of her's SessionSnapshot.
type herSnapshot struct {
	Session    json.RawMessage `json:"session"`
	LastSeq    int64           `json:"last_seq"`
	Transcript struct {
		Items []json.RawMessage `json:"items"`
	} `json:"transcript"`
	Usage json.RawMessage `json:"usage"`
}

type herItem struct {
	raw       json.RawMessage
	messageID string // a message's: the user's or the assistant's
}

// openedConversation is the conversation bots/open answered with, or nil.
func openedConversation(result []byte) *herConversation {
	var r struct {
		SessionID string      `json:"session_id"`
		Snapshot  herSnapshot `json:"snapshot"`
	}
	if json.Unmarshal(result, &r) != nil || r.SessionID == "" {
		return nil
	}
	c := &herConversation{session: r.SessionID, snap: r.Snapshot, calls: map[string]bool{}}
	for _, item := range r.Snapshot.Transcript.Items {
		c.add(item)
	}
	c.trim()
	return c
}

// apply adds to the conversation what a session event added to its history,
// as the Her entry's own view does (packages/her-client's session.ts), and
// says whether what is kept changed.
func (c *herConversation) apply(event json.RawMessage) bool {
	var e struct {
		Type      string `json:"type"`
		Seq       int64  `json:"seq"`
		MessageID string `json:"message_id"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(event, &e) != nil || json.Unmarshal(event, &fields) != nil || e.Seq <= c.snap.LastSeq {
		return false
	}
	c.snap.LastSeq = e.Seq
	if e.Type == "session_rewound" {
		// Back to before that message: what came after it goes, all of it
		// when the message is not kept.
		at := slices.IndexFunc(c.items, func(i herItem) bool { return i.messageID == e.MessageID })
		c.items = c.items[:max(at, 0)]
		return true
	}
	h, ok := herHistory[e.Type]
	if !ok {
		return false
	}
	item := map[string]any{"type": h.item}
	if h.outcome != "" {
		item["outcome"] = h.outcome
	}
	for _, k := range h.fields {
		if v, ok := fields[k]; ok {
			item[k] = v
		}
	}
	b, err := json.Marshal(item)
	if err != nil || !c.add(b) {
		return false
	}
	c.trim()
	return true
}

// add adds item as far as the Her entry shows it, and says whether it does.
func (c *herConversation) add(item json.RawMessage) bool {
	shown, messageID := c.shown(item)
	if shown != nil {
		c.items = append(c.items, herItem{shown, messageID})
	}
	return shown != nil
}

// shown is a history item as far as the Her entry draws it, with its
// message's id, or nil (packages/her-client's botChat): the owner's
// messages and what reached them from sub-agents and routines; the
// assistant's with only the calls that sent the owner something (herSends),
// and those calls' results; how each task ended.
func (c *herConversation) shown(item json.RawMessage) (json.RawMessage, string) {
	var it struct {
		Type      string `json:"type"`
		MessageID string `json:"message_id"`
		CallID    string `json:"call_id"`
		Message   struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(item, &it) != nil {
		return nil, ""
	}
	switch {
	case it.Type == "task_end", it.Type == "tool_result" && c.calls[it.CallID]:
		return item, ""
	case it.Type == "message" && it.Message.Role == "user":
		return item, it.MessageID
	case it.Type != "message" || it.Message.Role != "assistant":
		return nil, ""
	}
	var sent []json.RawMessage
	for _, b := range it.Message.Content {
		var call struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &call) == nil && call.Type == "tool_call" && herSends[call.Name] {
			sent = append(sent, b)
			c.calls[call.ID] = true
		}
	}
	var whole, message map[string]json.RawMessage
	if len(sent) == 0 || json.Unmarshal(item, &whole) != nil || json.Unmarshal(whole["message"], &message) != nil {
		return nil, ""
	}
	message["content"], _ = json.Marshal(sent)
	whole["message"], _ = json.Marshal(message)
	out, err := json.Marshal(whole)
	if err != nil {
		return nil, ""
	}
	return out, it.MessageID
}

// trim keeps the last herKeptMessages messages, and what came after the
// first of them.
func (c *herConversation) trim() {
	n := 0
	for i := len(c.items) - 1; i > 0; i-- {
		if c.items[i].messageID == "" {
			continue
		}
		if n++; n == herKeptMessages {
			c.items = slices.Delete(c.items, 0, i)
			return
		}
	}
}

// snapshot is the conversation as her's SessionSnapshot, as JSON.
func (c *herConversation) snapshot() []byte {
	s := c.snap
	s.Transcript.Items = make([]json.RawMessage, len(c.items))
	for i, it := range c.items {
		s.Transcript.Items[i] = it.raw
	}
	b, _ := json.Marshal(s)
	return b
}

// ---------------------------------------------------------------------------
// The owner's store: Her and Her's conversation, by machine.

var herKeptMigrations = `
CREATE TABLE her_bots (
  machine_id TEXT PRIMARY KEY,
  bot BLOB NOT NULL
);
CREATE TABLE her_conversations (
  machine_id TEXT PRIMARY KEY,
  snapshot BLOB NOT NULL
);
`

// herBotID is the id of the machine's Her as kept here, or empty.
func (s *store) herBotID(machineID string) string {
	var b []byte
	if s.db.QueryRow(`SELECT bot FROM her_bots WHERE machine_id=?`, machineID).Scan(&b) != nil {
		return ""
	}
	var bot struct {
		ID string `json:"id"`
	}
	json.Unmarshal(b, &bot)
	return bot.ID
}

// herKept is what is kept of the machine's Her: Her (her's BotSummary) and
// Her's conversation (her's SessionSnapshot), each empty when none is.
func (s *store) herKept(machineID string) (bot, snapshot []byte, err error) {
	err = s.db.QueryRow(`SELECT b.bot, c.snapshot FROM her_bots b LEFT JOIN her_conversations c USING(machine_id) WHERE b.machine_id=?`, machineID).Scan(&bot, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	return bot, snapshot, err
}

// dropHerKept forgets what is kept of the machine's Her.
func (s *store) dropHerKept(machineID string) error {
	for _, q := range []string{`DELETE FROM her_bots WHERE machine_id=?`, `DELETE FROM her_conversations WHERE machine_id=?`} {
		if _, err := s.db.Exec(q, machineID); err != nil {
			return err
		}
	}
	return nil
}
