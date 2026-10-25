package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

// The reply streams as drafts (item/agentMessage/delta, as the app-server
// sends it) that add up to the agent message of item/completed.
func TestDraftsAddUpToMessage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.jsonl")
	lines := `{"method":"item/started","params":{"item":{"type":"agentMessage","id":"m1","text":""}}}
{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"u","itemId":"m1","delta":"Hel"}}
{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"u","itemId":"m1","delta":"lo"}}
{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"m1","text":"Hello"}}}
`
	if err := os.WriteFile(out, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	proc, err := provider.StartProc(context.Background(), "cat", []string{out}, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	s := &session{proc: proc, events: make(chan provider.Event, 16), ctx: context.Background(), pending: map[int64]chan rpcResult{}}
	go s.read()
	var drafts, msg string
	for msg == "" {
		select {
		case e := <-s.events:
			switch e.Kind {
			case provider.EventAgentDraft:
				drafts += e.Text
			case provider.EventAgentMessage:
				msg = e.Text
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out")
		}
	}
	if drafts != "Hello" || msg != "Hello" {
		t.Fatalf("drafts %q, message %q", drafts, msg)
	}
}
