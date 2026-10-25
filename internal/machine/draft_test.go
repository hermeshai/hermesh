package machine

import (
	"context"
	"strings"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// follow streams a session's events, drafts too when partial, until the
// test ends.
func (h *harness) follow(id string, partial bool) <-chan *sessionv1.SessionEvent {
	ch := make(chan *sessionv1.SessionEvent, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	go h.s.Stream(ctx, h.p, &sessionv1.StreamEventsRequest{SessionId: id, Follow: true, Partial: partial}, func(e *sessionv1.SessionEvent) error {
		ch <- e
		return nil
	})
	return ch
}

// screen is what a client shows of a stream: the stored events, and the
// draft after them.
type screen struct {
	stored []*sessionv1.SessionEvent
	draft  string
	drafts []*sessionv1.AgentDraft
}

func (s *screen) apply(e *sessionv1.SessionEvent) {
	if d := e.GetAgentDraft(); d != nil {
		s.drafts = append(s.drafts, d)
		if d.Whole {
			s.draft = d.Text
		} else {
			s.draft += d.Text
		}
		return
	}
	s.stored = append(s.stored, e)
	s.draft = ""
}

// until applies a stream's events until ok says so.
func (h *harness) until(ch <-chan *sessionv1.SessionEvent, s *screen, what string, ok func(e *sessionv1.SessionEvent) bool) {
	h.t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case e := <-ch:
			s.apply(e)
			if ok(e) {
				return
			}
		case <-timeout:
			h.t.Fatalf("waiting for %s; draft %q, %d stored", what, s.draft, len(s.stored))
		}
	}
}

func turnDone(e *sessionv1.SessionEvent) bool { return e.GetTurnCompleted() != nil }

func messages(evs []*sessionv1.SessionEvent) []string {
	var out []string
	for _, e := range evs {
		if m := e.GetAgentMessage(); m != nil {
			out = append(out, m.Text)
		}
	}
	return out
}

// A reply shows as it is written to the streams that ask for drafts, and
// only there; the stored events are what they would be without drafts.
func TestDraftsReachPartialStreamsOnly(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("sleep 300ms\ndraft He|llo |wor|ld|, a|nd |more\nsleep 300ms\nsay Hello world, and more")
	partial, plain := h.follow(id, true), h.follow(id, false)
	var p, q screen
	sawDraft := ""
	h.until(partial, &p, "the turn's end", func(e *sessionv1.SessionEvent) bool {
		if e.GetAgentDraft() != nil {
			sawDraft = p.draft
		}
		return turnDone(e)
	})
	h.until(plain, &q, "the turn's end", turnDone)
	if sawDraft != "Hello world, and more" {
		t.Fatalf("the draft grew to %q, want the whole reply", sawDraft)
	}
	if len(p.drafts) < 2 || len(p.drafts) > 4 || !p.drafts[0].Whole || p.drafts[1].Whole {
		t.Fatalf("drafts: %v (paced at 100 ms, whole first, then additions)", p.drafts)
	}
	if len(q.drafts) != 0 {
		t.Fatalf("a stream that did not ask got drafts: %v", q.drafts)
	}
	stored := h.events(id)
	for _, e := range stored {
		if e.GetAgentDraft() != nil {
			t.Fatalf("a draft was stored: %v", e)
		}
	}
	if got := messages(stored); len(got) != 1 || got[0] != "Hello world, and more" {
		t.Fatalf("stored messages: %q", got)
	}
	if len(p.stored) != len(q.stored) {
		t.Fatalf("both streams show the same stored events: %d / %d", len(p.stored), len(q.stored))
	}
}

// A stream that starts in the middle of a reply gets the draft so far.
func TestDraftJoinsMidReply(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("sleep 300ms\ndraft Hal|f way\nsleep 1s\nsay Half way there")
	first := h.follow(id, true)
	var a screen
	h.until(first, &a, "the draft", func(*sessionv1.SessionEvent) bool { return a.draft == "Half way" })
	var b screen
	h.until(h.follow(id, true), &b, "the draft", func(e *sessionv1.SessionEvent) bool { return e.GetAgentDraft() != nil })
	if d := b.drafts[0]; !d.Whole || d.Text != "Half way" {
		t.Fatalf("joined mid-reply with %v", d)
	}
	h.until(first, &a, "the turn's end", turnDone)
	if got := messages(a.stored); got[len(got)-1] != "Half way there" || a.draft != "" {
		t.Fatalf("the stored message replaces the draft: %q, draft %q", got, a.draft)
	}
}

// A reply the agent never finished stays as far as it got: when the turn
// is interrupted, and when the agent moves on to a tool.
func TestUnfinishedDraftIsStored(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("sleep 300ms\ndraft Half a rep|ly\nsleep 30s")
	var s screen
	h.until(h.follow(id, true), &s, "the draft", func(*sessionv1.SessionEvent) bool { return s.draft == "Half a reply" })
	if _, err := h.s.Interrupt(context.Background(), h.p, id); err != nil {
		t.Fatal(err)
	}
	h.wait(id, idle)
	if got := messages(h.events(id)); len(got) != 1 || got[0] != "Half a reply" {
		t.Fatalf("an interrupted reply is stored as shown: %q", got)
	}

	h.send(id, "draft Before |the tool\nshell ls\nsay done")
	h.wait(id, idle)
	var kinds []string
	for _, e := range h.events(id)[h.mark[id]:] {
		switch {
		case e.GetAgentMessage() != nil:
			kinds = append(kinds, "message:"+e.GetAgentMessage().Text)
		case e.GetToolCall() != nil:
			kinds = append(kinds, "tool")
		}
	}
	if strings.Join(kinds, ",") != "message:Before the tool,tool,message:done" {
		t.Fatalf("the draft is stored before the tool call: %v", kinds)
	}
}

// When a stored event comes in the middle of a reply (an agent warning),
// the reply goes on: the draft is sent whole again after it.
func TestDraftGoesOnAfterStoredEvent(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	id := h.start("sleep 300ms\ndraft Hel\nsleep 300ms\nwarn retrying\ndraft lo\nsleep 300ms\nsay Hello")
	var s screen
	shown := ""
	h.until(h.follow(id, true), &s, "the turn's end", func(e *sessionv1.SessionEvent) bool {
		if e.GetAgentDraft() != nil {
			shown = s.draft
		}
		return turnDone(e)
	})
	if shown != "Hello" {
		t.Fatalf("after the warning the draft showed %q, want the whole reply", shown)
	}
}
