package machine

import (
	"context"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// A session keeps the title its client gives; without one it is titled by
// the first line of its prompt. The title is stored.
func TestSessionTitles(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	for _, c := range []struct{ title, prompt, want string }{
		{"  “修复 Safari 登录”。\nignored", "say one", "修复 Safari 登录"},
		{"", "\n  say 修复登录页  \nsay done", "say 修复登录页"},
	} {
		s, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: c.prompt, Title: c.title})
		if err != nil {
			t.Fatal(err)
		}
		if s.Title != c.want {
			t.Fatalf("title %q, want %q", s.Title, c.want)
		}
		h.wait(s.SessionId, idle)
		if r, _ := h.s.st.Session(s.SessionId); r == nil || r.Title != c.want {
			t.Fatalf("stored title %+v, want %q", r, c.want)
		}
	}
}
