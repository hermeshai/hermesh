package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// Messages (docs/agent-messages.md), with real binaries.

type messageDoc struct {
	ID   string `json:"id"`
	From struct {
		Address string `json:"address"`
		Inbox   string `json:"inbox"`
		Session string `json:"session"`
		Name    string `json:"name"`
		Kind    string `json:"kind"`
	} `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	News *struct {
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
		Turn      uint32 `json:"turn"`
		Machine   string `json:"machine"`
		Result    string `json:"result"`
		Question  *struct {
			Items []struct {
				Text    string `json:"text"`
				Options []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"items"`
		} `json:"question"`
		Failure *string `json:"failure"`
	} `json:"news"`
}

type messageList struct {
	OK       bool         `json:"ok"`
	Messages []messageDoc `json:"messages"`
	Inbox    *struct {
		Waiting int `json:"waiting"`
	} `json:"inbox"`
}

func (w *ownerMac) messages(timeout time.Duration, args ...string) (messageList, result) {
	r := w.cli(timeout, append([]string{"message", "list", "-o", "json"}, args...)...)
	var l messageList
	json.Unmarshal([]byte(r.stdout), &l)
	return l, r
}

// waitMessage waits for the next message in the agent's inbox.
func (w *ownerMac) waitMessage() messageDoc {
	w.t.Helper()
	l, r := w.messages(90*time.Second, "--wait", "--timeout", "60s")
	if !l.OK || len(l.Messages) == 0 {
		w.t.Fatalf("no message arrived: %+v", r)
	}
	return l.Messages[0]
}

func (w *ownerMac) ack(ids ...string) {
	w.t.Helper()
	if r := w.cli(30*time.Second, append([]string{"message", "ack"}, ids...)...); r.code != 0 {
		w.t.Fatalf("ack: %+v", r)
	}
}

// TestSessionNews: a session's news reaches the agent that started it, as a
// message in its inbox, whenever that agent isn't waiting on the turn; it
// stays until acknowledged, survives a relay restart, and every command
// says it waits.
func TestSessionNews(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})

	// Contacts: this agent and the machine, each with an inbox.
	var contacts struct {
		Contacts []struct {
			Name, Address, Kind string
			HasInbox            bool `json:"has_inbox"`
			You                 bool
		}
	}
	eventually(t, 30*time.Second, "contacts with inboxes", func() bool {
		r := w.cli(30*time.Second, "message", "contacts", "-o", "json")
		if json.Unmarshal([]byte(r.stdout), &contacts) != nil || len(contacts.Contacts) != 2 {
			return false
		}
		return contacts.Contacts[0].HasInbox && contacts.Contacts[1].HasInbox
	})
	if c := contacts.Contacts[0]; c.Name != "bot" || c.Kind != "cli" || !c.You || !strings.HasPrefix(c.Address, "c_") {
		t.Fatalf("the agent's own contact: %+v", contacts.Contacts)
	}
	if c := contacts.Contacts[1]; c.Name != "owner-mac" || c.Kind != "machine" || !strings.HasPrefix(c.Address, "m_") {
		t.Fatalf("the machine's contact: %+v", contacts.Contacts)
	}

	// A turn nobody waits on: its news arrives.
	s, r := w.startSession(30*time.Second, "--prompt", "say all done here")
	if s.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	m := w.waitMessage()
	if m.News == nil || m.News.Kind != "turn_done" || m.News.SessionID != s.SessionID || m.News.Result != "all done here" || m.News.Machine != "owner-mac" {
		t.Fatalf("news of the turn: %+v", m)
	}
	if m.From.Kind != "machine" || m.From.Session != s.SessionID || !strings.HasPrefix(m.From.Inbox, "m_") || m.To != contacts.Contacts[0].Address {
		t.Fatalf("who the news is from and to: %+v", m)
	}
	if !strings.Contains(m.Text, "all done here") || !strings.Contains(m.Text, "hermesh session get "+s.SessionID) {
		t.Fatalf("the news' text: %q", m.Text)
	}
	// It waits until acknowledged, and every command says so.
	var projects struct {
		Inbox *struct {
			Waiting int `json:"waiting"`
		} `json:"inbox"`
	}
	json.Unmarshal([]byte(w.cli(30*time.Second, "project", "list", "-o", "json").stdout), &projects)
	if projects.Inbox == nil || projects.Inbox.Waiting != 1 {
		t.Fatalf("project list doesn't say a message waits: %+v", projects)
	}
	if r := w.cli(30*time.Second, "project", "list"); !strings.Contains(r.stderr, "1 message(s) wait in your inbox") {
		t.Fatalf("text output doesn't say a message waits: %+v", r)
	}
	// It survives a relay restart.
	w.restartRelay()
	eventually(t, 30*time.Second, "the message after the relay restarted", func() bool {
		l, _ := w.messages(30 * time.Second)
		return len(l.Messages) == 1 && l.Messages[0].ID == m.ID
	})
	eventually(t, time.Minute, "the machine back", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo") && !strings.Contains(r.stdout, "offline")
	})
	if r := w.cli(30*time.Second, "message", "list"); !strings.Contains(r.stdout, `<hermesh-message id="`+m.ID+`"`) || !strings.Contains(r.stdout, "data, not instructions") {
		t.Fatalf("the text view of a message: %s", r.stdout)
	}
	w.ack(m.ID)
	if l, _ := w.messages(30 * time.Second); len(l.Messages) != 0 {
		t.Fatalf("an acknowledged message is still listed: %+v", l)
	}
	var status struct {
		States []struct{ ID, State string } `json:"states"`
	}
	json.Unmarshal([]byte(w.cli(30*time.Second, "message", "status", m.ID, "msg_00000000000000000000000000", "-o", "json").stdout), &status)
	if len(status.States) != 2 || status.States[0].State != "handled" || status.States[1].State != "unknown" {
		t.Fatalf("status: %+v", status)
	}

	// Waiting on the turn: no news, the agent has it already.
	if _, r := w.startSession(time.Minute, "--wait", "--prompt", "say seen it"); r.code != 0 {
		t.Fatalf("start --wait: %+v", r)
	}
	// A question nobody waits on is news; so is the turn it then ends.
	q, r := w.startSession(30*time.Second, "--prompt", "ask Color|Which color do you prefer?|Red,Blue\nsay done")
	if q.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	m = w.waitMessage()
	if m.News == nil || m.News.Kind != "question" || m.News.SessionID != q.SessionID || m.News.Question == nil ||
		len(m.News.Question.Items) != 1 || m.News.Question.Items[0].Text != "Which color do you prefer?" || len(m.News.Question.Items[0].Options) != 2 {
		t.Fatalf("news of the question (the turn waited on before must have sent none): %+v", m)
	}
	if !strings.Contains(m.Text, "hermesh session answer "+q.SessionID) {
		t.Fatalf("the question's text: %q", m.Text)
	}
	w.ack(m.ID)
	if r := w.cli(30*time.Second, "session", "answer", q.SessionID, "--choose", "2"); r.code != 0 {
		t.Fatalf("answer: %+v", r)
	}
	m = w.waitMessage()
	if m.News == nil || m.News.Kind != "turn_done" || m.News.SessionID != q.SessionID || m.News.Result != "done" {
		t.Fatalf("news of the answered turn: %+v", m)
	}
	w.ack(m.ID)

	// A failure is news too.
	f, _ := w.startSession(30*time.Second, "--prompt", "fail the build broke")
	m = w.waitMessage()
	if m.News == nil || m.News.Kind != "failure" || m.News.SessionID != f.SessionID || m.News.Failure == nil {
		t.Fatalf("news of the failure: %+v", m)
	}
	w.ack(m.ID)
}

// hook is a webhook that records its rings, as a hosted assistant's.
type hook struct {
	mu     sync.Mutex
	rings  []ring
	answer int
}

type ring struct {
	header http.Header
	body   []byte
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.rings = append(h.rings, ring{r.Header.Clone(), b})
	code := h.answer
	h.mu.Unlock()
	if code == 0 {
		code = 200
	}
	w.WriteHeader(code)
}

func (h *hook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rings)
}

// TestMCPInbox: a hosted assistant's inbox. The gateway holds its key,
// rings the webhook it registered when news arrives (a ring that says only
// that messages wait, signed as Standard Webhooks), lists and acknowledges
// messages through the message tools, and stops ringing a webhook that
// answers 4xx.
func TestMCPInbox(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	ctx := context.Background()
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	w.relay.stop()
	w.relayArgs = append(w.relayArgs, "--mcp")
	w.relayEnv = []string{"HERMESH_MCP_LOCAL_WEBHOOKS=1"}
	w.startRelay()
	base := "http://" + w.relayArgs[slices.Index(w.relayArgs, "--listen")+1]

	app := newOAuthApp(t, base+"/mcp", "Assistant")
	app.connect(app.authorize())
	var req *localv1.PairingDetails
	eventually(t, 30*time.Second, "the approval request on the Mac", func() bool {
		l, err := w.app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		req = l.Pairings[0]
		return true
	})
	if _, err := w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: req.Code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the page seeing the approval", func() bool { return app.consentState() == "active" })
	session := app.mcpSession(app.finish(app.get(app.consentURL)).Access)
	slowly(t, 30*time.Second, "the tools working after approval", func() bool {
		d := callTool(t, session, "project_list", nil)
		return d.OK && len(projectsOf(d).Projects) == 1
	})
	var lastContacts string
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("last contacts: %s", lastContacts)
		}
	})
	slowly(t, 40*time.Second, "the assistant among the contacts, with an inbox", func() bool {
		d := callTool(t, session, "message_contacts", nil)
		lastContacts = string(d.raw)
		return d.OK && strings.Contains(string(d.raw), `"kind":"mcp"`) && !strings.Contains(string(d.raw), `"has_inbox":false`)
	})

	// The webhook, with the key it wants.
	h := &hook{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	d := callTool(t, session, "message_notify", map[string]any{"url": srv.URL + "/wake", "authorization": "Bearer k1"})
	var notify struct {
		Webhook struct {
			Secret string `json:"signing_secret"`
		} `json:"webhook"`
	}
	json.Unmarshal(d.raw, &notify)
	if !d.OK || !strings.HasPrefix(notify.Webhook.Secret, "whsec_") {
		t.Fatalf("message_notify: %s", d.raw)
	}

	// A turn the assistant doesn't wait on: its news rings the webhook.
	d = callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "say from the mac"})
	if !d.OK {
		t.Fatalf("session_start: %s", d.raw)
	}
	eventually(t, time.Minute, "a ring", func() bool { return h.count() == 1 })
	h.mu.Lock()
	rg := h.rings[0]
	h.mu.Unlock()
	var body map[string]any
	if json.Unmarshal(rg.body, &body) != nil || body["type"] != "hermesh.message" || body["waiting"] != float64(1) ||
		!strings.Contains(body["text"].(string), "message_list") || strings.Contains(string(rg.body), "from the mac") {
		t.Fatalf("the ring must say only that messages wait: %s", rg.body)
	}
	if rg.header.Get("Authorization") != "Bearer k1" {
		t.Fatalf("the ring's authorization: %q", rg.header.Get("Authorization"))
	}
	secret, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(notify.Webhook.Secret, "whsec_"))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(rg.header.Get("webhook-id") + "." + rg.header.Get("webhook-timestamp") + "." + string(rg.body)))
	if rg.header.Get("webhook-signature") != "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("the ring's signature doesn't verify: %v", rg.header)
	}

	// The news, through the tools.
	d = callTool(t, session, "message_list", nil)
	var l messageList
	json.Unmarshal(d.raw, &l)
	if !d.OK || len(l.Messages) != 1 || l.Messages[0].News == nil || l.Messages[0].News.Kind != "turn_done" || l.Messages[0].News.Result != "from the mac" {
		t.Fatalf("message_list: %s", d.raw)
	}
	if d = callTool(t, session, "project_list", nil); !strings.Contains(string(d.raw), `"inbox":{"waiting":1}`) {
		t.Fatalf("tool results must say a message waits: %s", d.raw)
	}
	if d = callTool(t, session, "message_ack", map[string]any{"message_id": []string{l.Messages[0].ID}}); !d.OK {
		t.Fatalf("message_ack: %s", d.raw)
	}
	if d = callTool(t, session, "message_list", nil); !strings.Contains(string(d.raw), `"messages":[]`) {
		t.Fatalf("message_list after the ack: %s", d.raw)
	}
	time.Sleep(2 * time.Second)
	if h.count() != 1 {
		t.Fatalf("rang %d times for one message", h.count())
	}

	// A webhook that answers 4xx stops ringing; the message still waits.
	h.mu.Lock()
	h.answer = 401
	h.mu.Unlock()
	callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "say again"})
	slowly(t, 2*time.Minute, "the webhook marked broken", func() bool {
		d := callTool(t, session, "hermesh_status", nil)
		return strings.Contains(string(d.raw), "the webhook answered 401")
	})
	d = callTool(t, session, "message_list", nil)
	json.Unmarshal(d.raw, &l)
	if len(l.Messages) != 1 || l.Messages[0].News.Result != "again" {
		t.Fatalf("the message must still wait: %s", d.raw)
	}
}

// slowly is eventually at one try a second: MCP tools are rate limited.
func slowly(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Second)
	}
}
