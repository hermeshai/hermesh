package machine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	inboxv1 "github.com/hermeshai/hermesh/gen/go/hermesh/inbox/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/herhost/hertest"
)

type stubHerLink struct {
	HerLink
}

func (stubHerLink) Render(m *inboxv1.Message) (bot, conversation, from, text string, ok bool) {
	sn := m.GetSessionNews()
	if sn == nil {
		return "", "", "", "", false
	}
	return "b1", m.GetTo().GetSession(), sn.GetAgent() + " in " + sn.GetProject(), "<session_news session=\"" + sn.GetSessionId() + "\" kind=\"" + sn.GetKind() + "\">" + sn.GetResult() + "</session_news>", true
}

func (stubHerLink) Run(ctx context.Context) { <-ctx.Done() }
func (stubHerLink) Close()                  {}

// Owner-driven coding turns do not wake Her via CC, while agent-driven turns do.
func TestSessionNewsSkipsOwnerCC(t *testing.T) {
	dir := t.TempDir()
	n := openHerMachine(t, dir, "")
	defer n.Close()

	n.mu.Lock()
	n.mesh.grant = &identityv1.Grant{ClientId: "c_her"}
	n.mesh.link = stubHerLink{}
	n.mu.Unlock()

	sess := &sessionv1.Session{
		SessionId: "s_card_1",
		Title:     "Fix auth",
		ProjectId: "gateway",
		Agent:     "claude",
		CreatedBy: "c_her",
		Result:    "owner tweaked a log line",
	}
	// 1. Owner personally sends a turn in the coding session created by Her -> must NOT queue news for c_her.
	n.sessionNews(sess, &sessionv1.SessionEvent{
		Type: "run.completed",
		Run:  &sessionv1.Run{Ordinal: 2, Author: &sessionv1.Actor{Id: "owner"}},
	})
	time.Sleep(80 * time.Millisecond)
	n.mu.Lock()
	ownerQueue := len(n.herNewsLocked())
	n.mu.Unlock()
	if ownerQueue != 0 {
		t.Fatalf("owner turn unexpectedly queued %d news items for Her", ownerQueue)
	}

	// 2. Another agent (c_other) drives a turn -> MUST queue news for c_her.
	n.sessionNews(sess, &sessionv1.SessionEvent{
		Type: "run.completed",
		Run:  &sessionv1.Run{Ordinal: 3, Author: &sessionv1.Actor{Id: "c_other"}},
	})
	waitFor(t, "agent-driven turn news queued for Her", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return len(n.herNewsLocked()) == 1
	})
}

// End-to-end: expired questions are dropped before queueing, and concurrent turn_done items
// coalesce into a single mesh/news wakeup in Her's real conversation.
func TestHerNewsDropsExpiredQuestionAndBatchesTurnDone(t *testing.T) {
	bin := hertest.Bin(t)
	_ = hertest.Home(t, bin)
	dir := t.TempDir()
	n := openHerMachine(t, dir, bin)
	defer n.Close()

	ctx := context.Background()
	if _, err := n.execAdmin(ctx, setHer(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "her running", func() bool { st := n.Status().Her; return st.Running && st.ModelReady })

	l := n.Local()
	events := make(chan *herv1.OpenEvent, 4096)
	octx, cancelOpen := context.WithCancel(ctx)
	defer cancelOpen()
	go l.HerOpen(octx, &herv1.OpenRequest{Setup: []*herv1.Request{{Method: "bots/subscribe"}}}, func(e *herv1.OpenEvent) error {
		events <- e
		return nil
	})
	opened := (<-events).GetOpened()
	var list struct {
		Bots []struct {
			ID string `json:"id"`
		} `json:"bots"`
	}
	_ = json.Unmarshal(opened.GetResponses()[0].GetResult(), &list)
	if len(list.Bots) == 0 {
		t.Fatal("no default bot")
	}
	botID := list.Bots[0].ID

	res, err := l.HerCall(ctx, &herv1.CallRequest{
		ConnectionId: opened.ConnectionId,
		Request:      &herv1.Request{Method: "bots/open", Params: []byte(`{"bot_id":"` + botID + `"}`)},
	})
	if err != nil || res.GetResponse().GetError() != nil {
		t.Fatalf("bots/open: %v %v", err, res.GetResponse().GetError())
	}

	var mu sync.Mutex
	var newsDelivered []string
	var mailTasks int
	go func() {
		for ev := range events {
			notif := ev.GetNotification()
			if notif.GetMethod() != "session/event" {
				continue
			}
			var p struct {
				Event struct {
					Type    string `json:"type"`
					Trigger string `json:"trigger"`
					Mail    *struct {
						Kind string `json:"kind"`
					} `json:"mail"`
					Message *struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"message"`
				} `json:"event"`
			}
			if json.Unmarshal(notif.Params, &p) != nil {
				continue
			}
			mu.Lock()
			if p.Event.Type == "task_started" && p.Event.Trigger == "mail" {
				mailTasks++
			}
			if p.Event.Type == "message_completed" && p.Event.Mail != nil && p.Event.Mail.Kind == "news" && p.Event.Message != nil && len(p.Event.Message.Content) > 0 {
				newsDelivered = append(newsDelivered, p.Event.Message.Content[0].Text)
			}
			mu.Unlock()
		}
	}()

	n.mu.Lock()
	n.mesh.link = stubHerLink{}
	n.mesh.batchWait = 150 * time.Millisecond
	n.mu.Unlock()

	dctx, cancelDeliver := context.WithCancel(ctx)
	defer cancelDeliver()
	go n.deliverHerNews(dctx)

	// 1. Expired question is dropped immediately without queueing or waking Her.
	expiredMsg := &inboxv1.Message{
		MessageId: "msg_exp_q",
		From:      &inboxv1.Address{Session: "s_exp"},
		SessionNews: &inboxv1.SessionNews{
			Kind:      "question",
			SessionId: "s_exp",
			Question:  &sessionv1.Question{QuestionId: "q1", ExpiresAt: timestamppb.New(time.Now().Add(-time.Minute))},
		},
	}
	if !n.takeHerNews(expiredMsg) {
		t.Fatal("expired question rejected")
	}
	n.mu.Lock()
	if got := len(n.herNewsLocked()); got != 0 {
		n.mu.Unlock()
		t.Fatalf("expired question was queued: %d", got)
	}
	n.mu.Unlock()

	// 2. Two concurrent turn_done items coalesce into ONE mail wakeup in Her's session.
	n.takeHerNews(&inboxv1.Message{
		MessageId:   "msg_done_1",
		From:        &inboxv1.Address{Session: "s_1"},
		SessionNews: &inboxv1.SessionNews{Kind: "turn_done", SessionId: "s_1", Agent: "claude", Project: "p1", Result: "[[mock:send:merged report]] first done"},
	})
	time.Sleep(25 * time.Millisecond)
	n.takeHerNews(&inboxv1.Message{
		MessageId:   "msg_done_2",
		From:        &inboxv1.Address{Session: "s_2"},
		SessionNews: &inboxv1.SessionNews{Kind: "turn_done", SessionId: "s_2", Agent: "codex", Project: "p2", Result: "second done"},
	})

	waitFor(t, "coalesced news delivered to Her", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(newsDelivered) >= 1
	})
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(newsDelivered) != 1 || mailTasks != 1 {
		t.Fatalf("expected 1 coalesced mail wakeup, got news=%d tasks=%d (%q)", len(newsDelivered), mailTasks, newsDelivered)
	}
	if !strings.Contains(newsDelivered[0], "first done") || !strings.Contains(newsDelivered[0], "second done") {
		t.Fatalf("coalesced text missing parts: %q", newsDelivered[0])
	}
}
