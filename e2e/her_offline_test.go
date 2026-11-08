package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// herServer is the hermeshd of env's machine, as newHerEnvironment started
// it.
func herServer(t *testing.T, env *herEnvironment) *proc {
	t.Helper()
	for _, p := range env.w.logs {
		if p.name == "her-server" {
			return p
		}
	}
	t.Fatal("no her-server")
	return nil
}

// keptConversation is what GetHerConversation gives: Her, and Her's
// conversation as her's SessionSnapshot.
type keptConversation struct {
	Bot struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	Snapshot struct {
		Session struct {
			SessionID string `json:"session_id"`
		} `json:"session"`
		Transcript struct {
			Before *string `json:"before"`
			Items  []struct {
				Type    string `json:"type"`
				CallID  string `json:"call_id"`
				Outcome string `json:"outcome"`
				Message struct {
					Role    string `json:"role"`
					Content []struct {
						Type      string `json:"type"`
						Text      string `json:"text"`
						ID        string `json:"id"`
						Name      string `json:"name"`
						Arguments struct {
							Text string `json:"text"`
						} `json:"arguments"`
					} `json:"content"`
				} `json:"message"`
			} `json:"items"`
		} `json:"transcript"`
	}
}

// said is the conversation as lines: "owner: <words>", "Her: <tool> <its
// text>" for each call of Her's messages and "Her: <type>" for anything
// else they hold, "result <call>", "end <outcome>".
func (k keptConversation) said() []string {
	var out []string
	for _, it := range k.Snapshot.Transcript.Items {
		switch {
		case it.Type == "message" && it.Message.Role == "user":
			var words []string
			for _, c := range it.Message.Content {
				words = append(words, c.Text)
			}
			out = append(out, "owner: "+strings.Join(words, " "))
		case it.Type == "message":
			for _, c := range it.Message.Content {
				if c.Type == "tool_call" {
					out = append(out, "Her: "+c.Name+" "+c.Arguments.Text)
				} else {
					out = append(out, "Her: "+c.Type)
				}
			}
		case it.Type == "tool_result":
			out = append(out, "result "+it.CallID)
		case it.Type == "task_end":
			out = append(out, "end "+it.Outcome)
		}
	}
	return out
}

// TestHerOfflineConversation: the owner talks to Her on another machine,
// which then goes away. Once this Mac's hermeshd has started again, with
// nothing of it in memory, the Her entry still finds there what was said,
// as the entry shows it, through LocalService; once the machine is removed,
// nothing (docs/her-server.md §7, §11 phase 1.6).
func TestHerOfflineConversation(t *testing.T) {
	env := newHerEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// A message on Her's conversation, as the Her entry sends it, and Her's
	// reply, followed to the end of its task.
	stream, op, session := openAssistant(t, ctx, env)
	herCall(t, env, op.ConnectionId, "session/op", map[string]any{"session_id": session,
		"op": map[string]any{"type": "prompt", "input": []any{map[string]any{"type": "text", "text": mockSend("kept for later")}}}})
	untilEvent(t, stream, "Her's reply", func(e herEvent) bool { return e.sentText("kept for later") })
	untilEvent(t, stream, "the reply's task ended", func(e herEvent) bool { return e.Type == "task_completed" })
	stream.Close()

	// The machine goes away (its hermeshd stops, and her with it), then this
	// Mac's hermeshd stops and starts again.
	herServer(t, env).stop()
	eventually(t, time.Minute, "her-server offline", func() bool {
		m := listedMachine(t, env.c, env.machine)
		return m != nil && !m.Online
	})
	env.w.restartOwner(false)
	c := env.w.app.client
	if m := listedMachine(t, c, env.machine); m == nil || m.Online || !m.GetHer().GetEnabled() {
		t.Fatalf("the environment after the restart: %v", m)
	}

	res, err := c.GetHerConversation(ctx, &localv1.GetHerConversationRequest{MachineId: env.machine})
	if err != nil {
		t.Fatal(err)
	}
	var kept keptConversation
	if err := json.Unmarshal(res.Bot, &kept.Bot); err != nil {
		t.Fatalf("Her: %v %s", err, res.Bot)
	}
	if err := json.Unmarshal(res.Snapshot, &kept.Snapshot); err != nil {
		t.Fatalf("Her's conversation: %v %s", err, res.Snapshot)
	}
	if kept.Bot.ID != env.bot.ID || kept.Bot.Name != env.bot.Name || !kept.Bot.Default {
		t.Fatalf("Her: %s", res.Bot)
	}
	said := kept.said()
	if kept.Snapshot.Session.SessionID != session || kept.Snapshot.Transcript.Before != nil {
		t.Fatalf("the conversation: %s", res.Snapshot)
	}
	// The owner's message, Her's reply alone of all her message held, its
	// result and the task's end; nothing of her own steps.
	want := []string{"owner: " + mockSend("kept for later"), "Her: send_to_user kept for later", "result ", "end completed"}
	at := 0
	for _, line := range said {
		if strings.HasPrefix(line, "Her: ") && !strings.HasPrefix(line, "Her: send_to_user ") && !strings.HasPrefix(line, "Her: manage_routine ") {
			t.Fatalf("kept beyond what the Her entry shows: %q in %q", line, said)
		}
		if at < len(want) && strings.HasPrefix(line, want[at]) {
			at++
		}
	}
	if at < len(want) {
		t.Fatalf("kept %q\nwant, in order, %q", said, want)
	}

	// Removed, the machine leaves nothing of its Her here.
	if _, err := c.RemoveMachine(ctx, &localv1.RemoveMachineRequest{MachineId: env.machine}); err != nil {
		t.Fatal(err)
	}
	if res, err := c.GetHerConversation(ctx, &localv1.GetHerConversationRequest{MachineId: env.machine}); err != nil || len(res.Bot) != 0 || len(res.Snapshot) != 0 {
		t.Fatalf("after the machine was removed: %v %v", res, err)
	}
}
