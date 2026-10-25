package machine

import (
	"strings"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestPreview(t *testing.T) {
	long := strings.Repeat("界", 300) // 900 bytes, 3 bytes a rune
	for _, c := range []struct{ in, want string }{
		{"ok\n", "ok\n"},
		{"a\nb\nc", "a\nb\nc"},
		{"a\nb\nc\n", "a\nb\nc\n"},
		{"a\nb\nc\nd\ne", "a\nb\nc"},
		{long, strings.Repeat("界", 170)}, // 510 bytes: 512 would cut a rune
		{"x\n" + long, "x\n" + strings.Repeat("界", 170)},
	} {
		if got := Preview(c.in); got != c.want {
			t.Errorf("Preview(%.20q) = %.40q (%d bytes), want %d bytes", c.in, got, len(got), len(c.want))
		}
	}
}

// BASIC cuts a long tool result to its preview with its size, and leaves
// short ones, other events and sessions' list fields as lists need them.
func TestBasicViews(t *testing.T) {
	result := func(s string, size uint64) *sessionv1.SessionEvent {
		return &sessionv1.SessionEvent{Seq: 7, Kind: &sessionv1.SessionEvent_ToolResult{ToolResult: &sessionv1.ToolResult{CallId: "c", Summary: s, Size: size}}}
	}
	short := result("ok\n", 0)
	if BasicEvent(short) != short {
		t.Error("a short result changed")
	}
	full := result(strings.Repeat("line\n", 4000), 0)
	b := BasicEvent(full).GetToolResult()
	if !b.Preview || b.Summary != "line\nline\nline" || b.Size != 20000 {
		t.Errorf("basic: %q preview=%v size=%d", b.Summary, b.Preview, b.Size)
	}
	if full.GetToolResult().Preview {
		t.Error("the event itself changed")
	}
	inline := BasicEvent(result(strings.Repeat("y", 16<<10), 3<<20)).GetToolResult() // a 3 MiB output's event holds 16 KiB
	if inline.Size != 3<<20 || len(inline.Summary) != 512 {
		t.Errorf("an output kept beside its event: size %d, %d bytes", inline.Size, len(inline.Summary))
	}

	s := &sessionv1.Session{SessionId: "s", Title: "t", Result: "a long reply", ProviderSessionRef: "ref", Progress: &sessionv1.SessionProgress{LastMessageExcerpt: "said", TurnCount: 3}}
	bs := BasicSession(s)
	if bs.Result != "" || bs.ProviderSessionRef != "" || bs.Progress.LastMessageExcerpt != "" || bs.Progress.TurnCount != 3 || bs.Title != "t" {
		t.Errorf("basic session: %v", bs)
	}
	if s.Result == "" {
		t.Error("the session itself changed")
	}
}
