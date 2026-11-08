package herhost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// bots are BotSummary rows as her sends them.
func bots(t *testing.T, rows string) []bot {
	var out []bot
	if err := json.Unmarshal([]byte(rows), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The unread count is over every assistant; the newest message is an
// assistant's (never the owner's own), first by time, then whatever changes
// last; an approval waiting is the newest when it comes; a rename follows;
// a deleted assistant's message gives way to the newest left.
func TestSnapshot(t *testing.T) {
	s := newSnapshot("0.2.0", bots(t, `[
		{"id":"her","name":"Her","avatar":{"kind":"mark"},"default":true,"unread":1,"last":{"from":"bot","text":"older","ts":"2026-11-08T09:00:00Z"},"created_at":"x"},
		{"id":"w","name":"Writer","unread":2,"last":{"from":"bot","text":"newer","ts":"2026-11-08T10:00:00Z"}},
		{"id":"q","name":"Quiet","last":{"from":"user","text":"the owner's own","ts":"2026-11-08T11:00:00Z"}}]`))
	latest := func(id, name, text string, kind machinev1.HerLatestKind) {
		t.Helper()
		l := s.latest
		if l == nil || l.BotId != id || l.BotName != name || l.Text != text || l.Kind != kind {
			t.Fatalf("latest %v, want %s %s %q %v", l, id, name, text, kind)
		}
	}
	msg, approval := machinev1.HerLatestKind_HER_LATEST_KIND_MESSAGE, machinev1.HerLatestKind_HER_LATEST_KIND_APPROVAL
	if s.unread() != 3 {
		t.Fatalf("unread %d", s.unread())
	}
	latest("w", "Writer", "newer", msg)
	if at := s.latest.At.AsTime(); !at.Equal(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("at %v", at)
	}

	now := time.Now()
	long := strings.Repeat("字", 300)
	s.changed(bots(t, `[{"id":"her","name":"Her","unread":2,"last":{"from":"bot","text":"`+long+`","ts":"2026-11-08T09:30:00Z"}}]`), nil, now)
	latest("her", "Her", strings.Repeat("字", 200), msg)
	if s.unread() != 4 {
		t.Fatalf("unread %d", s.unread())
	}

	waiting := `{"id":"w","name":"Writer","unread":3,"waiting":"Run rm -rf build","last":{"from":"bot","text":"newer","ts":"2026-11-08T10:00:00Z"}}`
	s.changed(bots(t, `[`+waiting+`]`), nil, now)
	latest("w", "Writer", "Run rm -rf build", approval)
	if !s.latest.At.AsTime().Equal(now) {
		t.Fatalf("an approval's time is when it came: %v", s.latest.At.AsTime())
	}
	s.changed(bots(t, `[`+strings.Replace(waiting, "Writer", "Editor", 1)+`]`), nil, now)
	latest("w", "Editor", "Run rm -rf build", approval)
	s.changed(bots(t, `[{"id":"q","name":"Quiet","last":{"from":"user","text":"hello?","ts":"2026-11-08T12:00:00Z"}}]`), nil, now)
	latest("w", "Editor", "Run rm -rf build", approval)

	s.changed(nil, []string{"w"}, now)
	latest("her", "Her", strings.Repeat("字", 200), msg)
	if s.unread() != 2 {
		t.Fatalf("unread %d", s.unread())
	}
	s.changed(bots(t, `[{"id":"q","name":"Quiet","unread":1,"last":{"from":"bot","text":"","file":"report.pdf","ts":"2026-11-08T12:01:00Z"}}]`), nil, now)
	latest("q", "Quiet", "report.pdf", msg)
	if s.latest.Muted {
		t.Fatal("an assistant the owner didn't mute isn't muted")
	}

	// Muted: what it sends says so, so no device alerts; muting the one that
	// sent the newest message marks that message too.
	s.changed(bots(t, `[{"id":"q","name":"Quiet","unread":1,"muted":true,"last":{"from":"bot","text":"","file":"report.pdf","ts":"2026-11-08T12:01:00Z"}}]`), nil, now)
	latest("q", "Quiet", "report.pdf", msg)
	if !s.latest.Muted {
		t.Fatal("muting the newest message's assistant marks it muted")
	}
	s.changed(bots(t, `[{"id":"q","name":"Quiet","unread":2,"muted":true,"last":{"from":"bot","text":"again","ts":"2026-11-08T12:02:00Z"}}]`), nil, now)
	latest("q", "Quiet", "again", msg)
	if !s.latest.Muted {
		t.Fatal("a muted assistant's new message is muted")
	}
}

func TestModelReady(t *testing.T) {
	for result, want := range map[string]bool{
		`{"models":[{"id":"a/x","available":false},{"id":"b/y","available":true}]}`: true,
		`{"models":[{"id":"a/x","available":false}]}`:                               false,
		`{"models":[]}`: false,
		``:              false,
	} {
		if modelReady([]byte(result)) != want {
			t.Errorf("modelReady(%s) != %v", result, want)
		}
	}
}
