package herhost

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime/her"
)

// Her's conversations besides her main one, as the her runtime driver reaches them
// (internal/runtime/her, docs/her-conversations.md §5): each is a session of the machine's.

// Conversations is the Host as the her driver's Bridge.
func (h *Host) Conversations() her.Bridge { return conversations{h} }

type conversations struct{ h *Host }

// convEvents are the events left out of a followed conversation: the driver reads what was said
// and done, not how it streamed.
var convEvents = map[string]any{"exclude": []string{"text_delta", "thinking_delta", "tool_call_delta", "tool_progress"}}

func (c conversations) Available() bool {
	h := c.h
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stop != nil && h.snap != nil && len(h.snap.bots) > 0
}

// bot is the assistant the conversations are Her's: the default one.
func (c conversations) bot() (string, error) {
	h := c.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.snap == nil {
		return "", errors.New("Her is not running")
	}
	var ids []string
	for id, b := range h.snap.bots {
		if b.Default {
			return id, nil
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", errors.New("Her has no assistant")
	}
	slices.Sort(ids)
	return ids[0], nil
}

func (c conversations) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	conn, err := c.h.dial(ctx, false)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return callOn(ctx, conn, method, params)
}

func callOn(ctx context.Context, conn *conn, method string, params any) (json.RawMessage, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	res, err := conn.call(ctx, method, b)
	if err := answered(method, res, err); err != nil {
		return nil, err
	}
	return res.Result, nil
}

// Models are those Her can use now, her default first, with her thinking levels as the effort
// option. Her's default is her main conversation's model and thinking (matching bots/conversation_new),
// falling back to models/list's defaults when she has no configured main conversation yet.
func (c conversations) Models(ctx context.Context) ([]*sessionv1.ModelCatalogEntry, error) {
	conn, err := c.h.dial(ctx, false)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	raw, err := callOn(ctx, conn, "models/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var botModel, botThinking string
	if len(herModels(raw, "", "")) > 0 {
		if botID, err := c.bot(); err == nil {
			if opened, err := callOn(ctx, conn, "bots/open", map[string]any{
				"bot_id":        botID,
				"events":        convEvents,
				"history_limit": 0,
			}); err == nil {
				botModel, botThinking = mainSessionSettings(opened)
			}
		}
	}
	return herModels(raw, botModel, botThinking), nil
}

func mainSessionSettings(raw json.RawMessage) (model, thinking string) {
	var r struct {
		Snapshot struct {
			Session struct {
				Model    string `json:"model"`
				Thinking string `json:"thinking"`
			} `json:"session"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", ""
	}
	return r.Snapshot.Session.Model, r.Snapshot.Session.Thinking
}

func herModels(raw json.RawMessage, botModel, botThinking string) []*sessionv1.ModelCatalogEntry {
	var r struct {
		Models []struct {
			ID              string   `json:"id"`
			Name            string   `json:"name"`
			Available       bool     `json:"available"`
			Shown           bool     `json:"shown"`
			Deprecated      bool     `json:"deprecated"`
			ThinkingLevels  []string `json:"thinking_levels"`
			DefaultThinking string   `json:"default_thinking"`
		} `json:"models"`
		Defaults struct {
			Model string `json:"model"`
		} `json:"defaults"`
	}
	json.Unmarshal(raw, &r)
	defModel := r.Defaults.Model
	if botModel != "" {
		for _, m := range r.Models {
			if m.ID == botModel && m.Available && !m.Deprecated {
				defModel = botModel
				break
			}
		}
	}
	var out []*sessionv1.ModelCatalogEntry
	for _, m := range r.Models {
		if !m.Available || m.Deprecated || (!m.Shown && m.ID != defModel) {
			continue
		}
		name := cmp.Or(m.Name, m.ID)
		isDef := m.ID == defModel
		e := &sessionv1.ModelCatalogEntry{Slug: m.ID, Name: name, ShortName: name, IsDefault: isDef}
		if levels := slices.DeleteFunc(slices.Clone(m.ThinkingLevels), func(l string) bool { return l == "off" }); len(levels) > 0 {
			cur := m.DefaultThinking
			if isDef && botThinking != "" && slices.Contains(levels, botThinking) {
				cur = botThinking
			}
			if !slices.Contains(levels, cur) {
				cur = levels[len(levels)/2]
			}
			choices := make([]*sessionv1.OptionChoice, 0, len(levels))
			for _, l := range levels {
				choices = append(choices, &sessionv1.OptionChoice{Id: l, Label: l, IsDefault: l == cur})
			}
			e.OptionDescriptors = []*sessionv1.OptionDescriptor{{
				Id: "effort", Label: "Reasoning", Type: sessionv1.OptionDescriptorType_OPTION_DESCRIPTOR_TYPE_SELECT, Role: "reasoning",
				Choices: choices, CurrentValue: &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: cur}},
			}}
		}
		out = append(out, e)
	}
	// Her's default first.
	slices.SortStableFunc(out, func(a, b *sessionv1.ModelCatalogEntry) int {
		switch {
		case a.IsDefault == b.IsDefault:
			return 0
		case a.IsDefault:
			return -1
		}
		return 1
	})
	return out
}

func (c conversations) NewConversation(ctx context.Context, model, thinking string) (string, error) {
	bot, err := c.bot()
	if err != nil {
		return "", err
	}
	params := map[string]any{"bot_id": bot}
	if model != "" {
		params["model"] = model
	}
	if thinking != "" {
		params["thinking"] = thinking
	}
	raw, err := c.call(ctx, "bots/conversation_new", params)
	if err != nil {
		return "", err
	}
	var r struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &r); r.SessionID == "" {
		return "", fmt.Errorf("bots/conversation_new: no session in %s", raw)
	}
	return r.SessionID, nil
}

// open attaches conn to the conversation, loading it and returning bots/open's reply.
func (c conversations) open(ctx context.Context, conn *conn, id string) (json.RawMessage, error) {
	bot, err := c.bot()
	if err != nil {
		return nil, err
	}
	return callOn(ctx, conn, "bots/open", map[string]any{"bot_id": bot, "conversation": id, "history_limit": 0, "events": convEvents})
}

func (c conversations) Attach(ctx context.Context, id string) (her.Conversation, error) {
	conn, err := c.h.dial(ctx, true)
	if err != nil {
		return nil, err
	}
	opened, err := c.open(ctx, conn, id)
	if err != nil {
		conn.Close()
		return nil, err
	}
	seed := snapshotEvents(opened)
	f := &followed{id: id, conn: conn, events: make(chan json.RawMessage, max(64, len(seed)+64))}
	for _, ev := range seed {
		f.events <- ev
	}
	go f.relay()
	return f, nil
}

// snapshotEvents turns work already running in bots/open's snapshot (an active turn, running
// sub-agents, or pending approvals) into initial events so a resumed lease sees them too.
func snapshotEvents(raw json.RawMessage) []json.RawMessage {
	type approval struct {
		CallID  string          `json:"call_id"`
		Name    string          `json:"name"`
		Args    json.RawMessage `json:"args"`
		Summary string          `json:"summary"`
		Reason  string          `json:"reason"`
	}
	type activeTask struct {
		TaskID    string     `json:"task_id"`
		Approvals []approval `json:"approvals"`
	}
	var r struct {
		Snapshot struct {
			ActiveTask *activeTask `json:"active_task"`
			Agents     []struct {
				AgentID     string      `json:"agent_id"`
				Name        string      `json:"name"`
				Description string      `json:"description"`
				CallID      string      `json:"call_id"`
				Status      string      `json:"status"`
				ActiveTask  *activeTask `json:"active_task"`
			} `json:"agents"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	var out []json.RawMessage
	emit := func(v any) {
		if b, err := json.Marshal(v); err == nil {
			out = append(out, b)
		}
	}
	askPayload := func(a approval) map[string]any {
		m := map[string]any{"type": "approval_requested", "call_id": a.CallID, "name": a.Name, "summary": a.Summary}
		if len(a.Args) > 0 {
			m["args"] = a.Args
		}
		if a.Reason != "" {
			m["reason"] = a.Reason
		}
		return m
	}
	if t := r.Snapshot.ActiveTask; t != nil {
		emit(map[string]any{"type": "task_started", "task_id": t.TaskID})
		for _, a := range t.Approvals {
			emit(askPayload(a))
		}
	}
	for _, ag := range r.Snapshot.Agents {
		if ag.Status != "running" || ag.AgentID == "" {
			continue
		}
		emit(map[string]any{
			"type":        "agent_started",
			"agent_id":    ag.AgentID,
			"name":        ag.Name,
			"description": ag.Description,
			"call_id":     ag.CallID,
		})
		if ag.ActiveTask != nil {
			for _, a := range ag.ActiveTask.Approvals {
				emit(map[string]any{"type": "agent_event", "agent_id": ag.AgentID, "event": askPayload(a)})
			}
		}
	}
	return out
}

func (c conversations) Op(ctx context.Context, id string, op any) error {
	conn, err := c.h.dial(ctx, false)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := c.open(ctx, conn, id); err != nil {
		return err
	}
	_, err = callOn(ctx, conn, "session/op", map[string]any{"session_id": id, "op": op})
	return err
}

func (c conversations) DeleteConversation(ctx context.Context, id string) error {
	bot, err := c.bot()
	if err != nil {
		return err
	}
	_, err = c.call(ctx, "bots/conversation_delete", map[string]any{"bot_id": bot, "session_id": id})
	return err
}

// followed is one conversation on a connection of its own.
type followed struct {
	id     string
	conn   *conn
	events chan json.RawMessage
}

func (f *followed) Events() <-chan json.RawMessage { return f.events }

func (f *followed) Op(ctx context.Context, op any) error {
	_, err := callOn(ctx, f.conn, "session/op", map[string]any{"session_id": f.id, "op": op})
	return err
}

func (f *followed) Close() error {
	f.conn.Close()
	return nil
}

// relay hands on the conversation's events until the connection ends.
func (f *followed) relay() {
	defer close(f.events)
	for {
		select {
		case n := <-f.conn.notes:
			if n.method != "session/event" {
				continue
			}
			var p struct {
				SessionID string          `json:"session_id"`
				Event     json.RawMessage `json:"event"`
			}
			if json.Unmarshal(n.params, &p) != nil || p.SessionID != f.id {
				continue
			}
			f.events <- p.Event
		case <-f.conn.done:
			return
		}
	}
}
