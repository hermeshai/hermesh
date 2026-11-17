package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// A long conversation with Her, in English: hundreds of messages, short and
// long, a paragraph or many, with headings, lists, code and tables, read in
// pages as the owner scrolls up (docs/desktop-performance.md §7). Kinds come
// at random, the same every run: a few very long replies close together
// after short ones, as in a real conversation, are what a guess of a row's
// height gets most wrong.

// longChat is how many messages the owner sends Her.
const longChat = 240

// longWords are the words the conversation is made of.
var longWords = strings.Fields(`we should check whether the numbers still line up with what finance sent last week and make sure
the team knows which version is final before anyone shares it outside the company because the last draft had a few
gaps that nobody caught until the review`)

var longTopics = []string{"the release checklist", "the billing migration", "our onboarding emails", "the Q3 budget",
	"the flaky login test", "the mobile layout", "the data export", "the customer survey", "the API rate limits",
	"the weekly report", "the offsite agenda", "the design review", "the cache invalidation bug", "the hiring plan"}

// longConversation is the owner's messages and Her's replies, message n at
// n-1. The owner's start "Msg n:", Her's "Reply n:".
var longConversation = func() (owner, reply []string) {
	r := rand.New(rand.NewPCG(7, 7))
	between := func(lo, hi int) int { return lo + r.IntN(hi-lo+1) }
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	sentence := func() string {
		w := make([]string, between(8, 22))
		for i := range w {
			w[i] = longWords[r.IntN(len(longWords))]
		}
		return strings.ToUpper(w[0][:1]) + strings.Join(w, " ")[1:] + "."
	}
	paragraph := func(k int) string {
		s := make([]string, k)
		for i := range s {
			s[i] = sentence()
		}
		return strings.Join(s, " ")
	}
	paragraphs := func(n, lo, hi int) string {
		p := make([]string, n)
		for i := range p {
			p[i] = paragraph(between(lo, hi))
		}
		return strings.Join(p, "\n\n")
	}
	for n := 1; n <= longChat; n++ {
		topic := pick(longTopics...)
		var o string
		switch x := r.Float64(); {
		case x < 0.35:
			o = pick("ok", "thanks!", "sounds good", "yes please", "do it", "got it, thanks", "perfect", "not yet")
		case x < 0.7:
			o = fmt.Sprintf("Can you look at %s? %s", topic, sentence())
		case x < 0.9:
			o = fmt.Sprintf("About %s: %s", topic, paragraph(between(2, 4)))
		default:
			o = fmt.Sprintf("A few things on %s:\n\n%s\n\n- %s\n- %s", topic, paragraphs(2, 1, 2), sentence(), sentence())
		}
		owner = append(owner, fmt.Sprintf("Msg %d: %s", n, o))
		topic = pick(longTopics...)
		var a string
		switch x := r.Float64(); {
		case x < 0.3:
			a = pick("Done.", "On it.", "Sure, I'll take care of it.", "Noted.", "Got it.", "Will do, I'll report back shortly.")
		case x < 0.45:
			a = fmt.Sprintf("I looked at %s. %s", topic, paragraph(between(1, 4)))
		case x < 0.6:
			a = paragraphs(between(2, 5), 1, 3)
		case x < 0.72:
			steps := make([]string, between(3, 8))
			for i := range steps {
				steps[i] = fmt.Sprintf("%d. %s", i+1, sentence())
			}
			a = fmt.Sprintf("## Summary of %s\n\n%s\n\n### Next steps\n\n%s", topic, paragraph(2), strings.Join(steps, "\n"))
		case x < 0.82:
			code := make([]string, between(3, 12))
			for i := range code {
				code[i] = fmt.Sprintf("def step_%d(x):\n    return x * %d + %d", i, i, between(1, 99))
			}
			a = fmt.Sprintf("Here is the script for %s:\n\n```python\n%s\n```\n\n%s", topic, strings.Join(code, "\n"), sentence())
		case x < 0.9:
			rows := make([]string, between(3, 9))
			for i := range rows {
				rows[i] = fmt.Sprintf("| %s | %d | %s |", pick(longTopics...), between(1, 900), pick("done", "open", "blocked"))
			}
			a = fmt.Sprintf("Status of %s:\n\n| Item | Count | Status |\n|---|---:|---|\n%s\n\n%s", topic, strings.Join(rows, "\n"), sentence())
		default:
			a = paragraphs(between(8, 14), 3, 6)
		}
		reply = append(reply, fmt.Sprintf("Reply %d: %s", n, a))
	}
	return owner, reply
}

var longOwners, longReplies = longConversation()

var longAsked = regexp.MustCompile(`Msg (\d+):`)

// longModel is a model service in the OpenAI chat-completions API that
// answers each of the owner's messages in Her's conversation with
// longConversation's replies, sent with send_to_user and end_turn, and anything after that
// with nothing.
func longModel(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		delta, finish := map[string]any{"role": "assistant", "content": ""}, "stop"
		if last := len(req.Messages) - 1; last >= 0 && req.Messages[last].Role == "user" {
			// The owner's latest message: her puts reminders of her own after it.
			for i := last; i >= 0; i-- {
				m := longAsked.FindSubmatch(req.Messages[i].Content)
				if req.Messages[i].Role != "user" || m == nil {
					continue
				}
				if n, _ := strconv.Atoi(string(m[1])); n >= 1 && n <= longChat {
					args, _ := json.Marshal(map[string]any{"text": longReplies[n-1], "end_turn": true})
					delta = map[string]any{"role": "assistant", "tool_calls": []map[string]any{{"index": 0, "id": fmt.Sprintf("call_long_%d", n), "type": "function",
						"function": map[string]any{"name": "send_to_user", "arguments": string(args)}}}}
					finish = "tool_calls"
				}
				break
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(v map[string]any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		chunk(map[string]any{"id": "long", "object": "chat.completion.chunk", "model": "long-1", "choices": []map[string]any{{"index": 0, "delta": delta}}})
		chunk(map[string]any{"id": "long", "object": "chat.completion.chunk", "model": "long-1", "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// longHer is what an owner Mac's hermeshd needs to run Her on longModel,
// and her home.
func longHer(t *testing.T) (env []string, home string) {
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin) // her's model catalog
	home = t.TempDir()
	models := fmt.Sprintf(`[providers.long]
name = "Long"
api = "openai-chat"
base_url = "%s/v1"
auth = "none"
currency = "USD"

[[providers.long.models]]
id = "long-1"
name = "Long conversation model"
context_window = 2000000
max_output_tokens = 32000
input = ["text"]
cost = { input = 0.0, output = 0.0 }
`, longModel(t))
	for name, content := range map[string]string{"models.toml": models, "config.toml": "model = \"long/long-1\"\n", ".her-home": ""} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return herEnv(her, herConsole(t), home, mock.base), home
}

// longChatWith turns Her on on the owner Mac and has the long conversation with
// her: the owner's messages go in as the apps send them (HerSend), each after
// Her answered the one before.
func longChatWith(t *testing.T, w *ownerMac) {
	c := w.app.client
	mac := localMachine(t, c)
	turnHerOn(t, c, mac)
	bot := defaultBot(t, c, mac)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	stream, _ := herOpen(t, ctx, c, mac, herSetup("bots/open", map[string]string{"bot_id": bot.ID}))
	defer stream.Close()
	for n := 1; n <= longChat; n++ {
		if res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: mac, BotId: bot.ID, Text: longOwners[n-1]}); err != nil || !res.Delivered {
			t.Fatalf("HerSend %d: %v %v", n, res, err)
		}
		answer, answered := fmt.Sprintf(`"Reply %d:`, n), false
		for {
			if !stream.Receive() {
				t.Fatalf("Her's answer to message %d: %v", n, stream.Err())
			}
			m := stream.Msg().GetNotification()
			if m == nil || m.Method != "session/event" {
				continue
			}
			answered = answered || strings.Contains(string(m.Params), answer)
			if strings.Contains(string(m.Params), `"task_completed"`) {
				if !answered {
					t.Fatalf("message %d ended without Her's answer: %s", n, m.Params)
				}
				break
			}
		}
	}
}

// longChatWorld is an owner Mac whose Her, on longModel, has had the long
// conversation.
func longChatWorld(t *testing.T) desktopFixture {
	env, home := longHer(t)
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	t.Cleanup(w.owner.stop) // stopped as a service is, so that it stops her
	longChatWith(t, w)
	return desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv, HerHome: home, owner: w}
}

// TestDesktopHerLong runs apps/desktop/e2e/her-long.spec.ts: the long
// conversation, read from its end to its first message with the pointer on
// it, as the app reads earlier pages and draws only the rows near what is in
// sight. It needs her built (cargo build in her/).
//
//	go test ./e2e -run TestDesktopHerLong -v -timeout 30m
//
// With HERMESH_DESKTOP_PREVIEW=<seconds> it keeps the world up instead, for
// a person to scroll the conversation themselves:
//
//	HERMESH_DESKTOP_PREVIEW=3600 go test ./e2e -run TestDesktopHerLong -v -timeout 70m
//	HERMESH_SOCKET=<socket it prints> pnpm --dir apps/desktop dev
func TestDesktopHerLong(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	f := longChatWorld(t)
	if secs, _ := strconv.Atoi(os.Getenv("HERMESH_DESKTOP_PREVIEW")); secs > 0 {
		t.Logf("Her's long conversation is ready on this Mac (%d messages, in English); open the app with:\nHERMESH_SOCKET=%s pnpm --dir apps/desktop dev", longChat, f.Socket)
		time.Sleep(time.Duration(secs) * time.Second)
		return
	}
	herSpec(t, f, "her-long.spec.ts")
}
