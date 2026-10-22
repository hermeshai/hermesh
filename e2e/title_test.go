package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestSessionTitles: a personal agent names a session with --title, or the
// session is titled by its prompt's first line. The owner's app and
// `session list` show the same titles, also after a restart.
func TestSessionTitles(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	ctx := context.Background()
	ownerTitles := func() map[string]string {
		res, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, s := range res.Sessions {
			m[s.SessionId] = s.Title
		}
		return m
	}
	listTitles := func() map[string]string {
		var doc struct {
			Sessions []struct {
				SessionID string `json:"session_id"`
				Title     string `json:"title"`
			}
		}
		json.Unmarshal([]byte(w.cli(30*time.Second, "session", "list", "-o", "json").stdout), &doc)
		m := map[string]string{}
		for _, s := range doc.Sessions {
			m[s.SessionID] = s.Title
		}
		return m
	}
	want := map[string]string{}
	for _, c := range []struct {
		args  []string
		title string
	}{
		{[]string{"--title", "登录修复", "--prompt", "say hi"}, "登录修复"},
		{[]string{"--prompt", "say 修复 Safari 下登录按钮没反应\nsay done"}, "say 修复 Safari 下登录按钮没反应"},
	} {
		var v struct {
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
		}
		_, r := w.startSession(time.Minute, c.args...)
		if r.code != 0 || unmarshalSession(r.stdout, &v) != nil || v.Title != c.title {
			t.Fatalf("start %v: title %q, want %q\n%+v", c.args, v.Title, c.title, r)
		}
		want[v.SessionID] = c.title
	}
	check := func(when string) {
		owner, list := ownerTitles(), listTitles()
		for id, title := range want {
			if owner[id] != title || list[id] != title {
				t.Fatalf("%s: owner shows %q, session list %q; want %q", when, owner[id], list[id], title)
			}
		}
	}
	check("started")
	w.restartOwner(false)
	check("after restart")
}
