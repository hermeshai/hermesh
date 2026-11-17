package herhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Talk is Her's conversation in a terminal (hermeshd her, docs/linux.md
// §8), on the her this machine runs, at sock: what Her sends shows as it
// comes, each line read from in goes to her, and an approval she asks for
// is asked in the terminal. It ends when in does, or ctx.
func Talk(ctx context.Context, sock, version string, in io.Reader, out io.Writer) error {
	c, err := dial(ctx, sock, version, true)
	if err != nil {
		return fmt.Errorf("her is not running here (%v): turn Her on for this machine in the Hermesh app", err)
	}
	defer c.Close()
	res, err := c.call(ctx, "bots/subscribe", nil)
	if err := answered("bots/subscribe", res, err); err != nil {
		return err
	}
	var bots struct {
		Bots []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Default bool   `json:"default"`
		} `json:"bots"`
	}
	json.Unmarshal(res.Result, &bots)
	var botID, name string
	for _, b := range bots.Bots {
		if b.Default {
			botID, name = b.ID, b.Name
		}
	}
	if botID == "" {
		return errors.New("her has no default assistant")
	}
	params, _ := json.Marshal(map[string]any{"bot_id": botID, "history_limit": 0,
		"events": map[string]any{"exclude": []string{"text_delta", "thinking_delta", "tool_call_delta", "tool_progress"}}})
	res, err = c.call(ctx, "bots/open", params)
	if err := answered("bots/open", res, err); err != nil {
		return err
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(res.Result, &open)
	op := func(o map[string]any) error {
		params, _ := json.Marshal(map[string]any{"session_id": open.SessionID, "op": o})
		res, err := c.call(ctx, "session/op", params)
		return answered("session/op", res, err)
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	fmt.Fprintf(out, "%s · a message and Enter sends it; Ctrl+D ends\n> ", name)
	busy := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				fmt.Fprintln(out)
				return nil
			}
			if strings.TrimSpace(line) == "" {
				fmt.Fprint(out, "> ")
				continue
			}
			id := "ht_" + strings.ToLower(ulid.Make().String())
			if err := op(map[string]any{"type": "prompt", "id": id, "input": []map[string]string{{"type": "text", "text": line}}}); err != nil {
				fmt.Fprintf(out, "✗ not sent: %v\n> ", err)
				continue
			}
			busy = true
		case n, ok := <-c.notes:
			if !ok {
				return errors.New("her stopped")
			}
			if n.method != "session/event" {
				continue
			}
			var p struct {
				Event struct {
					Type    string          `json:"type"`
					Name    string          `json:"name"`
					CallID  string          `json:"call_id"`
					Summary string          `json:"summary"`
					Args    json.RawMessage `json:"args"`
				} `json:"event"`
			}
			if json.Unmarshal(n.params, &p) != nil {
				continue
			}
			e := p.Event
			switch e.Type {
			case "tool_started":
				if e.Name == "send_to_user" {
					var a struct {
						Text string `json:"text"`
					}
					json.Unmarshal(e.Args, &a)
					fmt.Fprintf(out, "\r%s: %s\n", name, a.Text)
				} else {
					fmt.Fprintf(out, "\r  · %s\n", e.Name)
				}
			case "approval_requested":
				fmt.Fprintf(out, "\r%s asks: %s. Allow? [y/N] ", name, e.Summary)
				var answer string
				select {
				case answer = <-lines:
				case <-time.After(10 * time.Minute):
				case <-ctx.Done():
					return ctx.Err()
				}
				decision := "deny"
				if a := strings.ToLower(strings.TrimSpace(answer)); a == "y" || a == "yes" {
					decision = "approve"
				}
				if err := op(map[string]any{"type": "approval", "call_id": e.CallID, "decision": map[string]string{"type": decision}}); err != nil {
					fmt.Fprintf(out, "✗ %v\n", err)
				}
			case "task_completed", "task_failed":
				if busy {
					busy = false
					fmt.Fprint(out, "> ")
				}
			}
		}
	}
}
