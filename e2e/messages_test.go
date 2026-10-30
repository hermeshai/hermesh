package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"

	"github.com/hermeshai/hermesh/internal/localipc"
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

func (h *hook) reset() {
	h.mu.Lock()
	h.rings = nil
	h.mu.Unlock()
}

// selfAddress is an MCP connection's own address, as its contacts say.
func selfAddress(t *testing.T, session *mcp.ClientSession) string {
	t.Helper()
	var c struct {
		Contacts []struct {
			Address string `json:"address"`
			You     bool   `json:"you"`
		} `json:"contacts"`
	}
	json.Unmarshal(callTool(t, session, "message_contacts", nil).raw, &c)
	for _, x := range c.Contacts {
		if x.You {
			return x.Address
		}
	}
	t.Fatal("no contact is the connection itself")
	return ""
}

// doorbellOf is what hermesh_status says of a doorbell.
func doorbellOf(d cliDoc) string {
	var s struct {
		Messages struct {
			Doorbell string `json:"doorbell"`
			Reason   string `json:"reason"`
		} `json:"messages"`
	}
	json.Unmarshal(d.raw, &s)
	return s.Messages.Doorbell
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

	app := newOAuthApp(t, mcpAddress(t, w.app), "Assistant")
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

	// The assistant asks to be woken; the owner sets its webhook up, with
	// the key the webhook wants, on the Mac: the key never passes through
	// the model (docs/agent-names.md §7.2).
	d := callTool(t, session, "message_wake", nil)
	if !d.OK || !strings.Contains(string(d.raw), "Settings → Access") {
		t.Fatalf("message_wake on a relay without a console: %s", d.raw)
	}
	self := selfAddress(t, session)
	h := &hook{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	set, err := w.app.client.SetWake(ctx, &localv1.SetWakeRequest{ClientId: self, Url: srv.URL + "/wake", Authorization: "Bearer k1"})
	if err != nil || !set.Check.Ok || set.Wake.State != "on" || !strings.HasPrefix(set.SigningSecret, "whsec_") {
		t.Fatalf("the owner's SetWake: %v %v", set, err)
	}
	if h.count() != 1 || !strings.Contains(string(h.rings[0].body), `"type":"hermesh.check"`) {
		t.Fatalf("the check must ring once, asking for nothing: %d rings", h.count())
	}
	h.reset()
	if d := callTool(t, session, "hermesh_status", nil); doorbellOf(d) != "on" {
		t.Fatalf("hermesh_status, the doorbell set: %s", d.raw)
	}

	// A turn the assistant doesn't wait on: its news rings the webhook.
	d = callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "say from the mac"})
	if !d.OK {
		t.Fatalf("session_start: %s", d.raw)
	}
	// What to tell the owner, in a sentence: no ids.
	var started struct{ Summary string }
	if json.Unmarshal(d.raw, &started) != nil || !strings.Contains(started.Summary, "in project demo on ") ||
		!strings.Contains(started.Summary, "comes to you as a message") || strings.Contains(started.Summary, "s_") {
		t.Fatalf("session_start's summary: %s", d.raw)
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
	secret, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(set.SigningSecret, "whsec_"))
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

	// Two bots on one connection: a bot's news comes back to its own name
	// (docs/agent-names.md), however it writes the name.
	d = callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "say for tech demos", "as": "Tech Demos"})
	if !d.OK || !strings.Contains(string(d.raw), `"as":"tech-demos"`) {
		t.Fatalf("session_start as Tech Demos: %s", d.raw)
	}
	slowly(t, time.Minute, "Tech Demos's news", func() bool {
		d = callTool(t, session, "message_list", map[string]any{"as": "tech_demos"})
		json.Unmarshal(d.raw, &l)
		return len(l.Messages) == 1 && l.Messages[0].News != nil && l.Messages[0].News.Result == "for tech demos"
	})
	if d = callTool(t, session, "message_list", map[string]any{"as": "chief-of-staff"}); !strings.Contains(string(d.raw), `"messages":[]`) {
		t.Fatalf("another bot's list: %s", d.raw)
	}
	d = callTool(t, session, "message_list", nil)
	json.Unmarshal(d.raw, &l)
	if len(l.Messages) != 1 || l.Messages[0].News.Result != "again" {
		t.Fatalf("the agent's own list must hold only its own: %s", d.raw)
	}

	// A doorbell for one name rings for that name alone, saying whose
	// messages wait; other agents find the name in their contacts, as
	// @tech-demos.
	h2 := &hook{}
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	rangBefore := h.count()
	if set, err := w.app.client.SetWake(ctx, &localv1.SetWakeRequest{ClientId: self, Name: "Tech Demos", Url: srv2.URL + "/td"}); err != nil || !set.Check.Ok {
		t.Fatalf("Tech Demos's doorbell: %v %v", set, err)
	}
	h2.reset()
	if d, r := sendMessage(t, w, w.cliEnv, 30*time.Second, "--to", "@Tech Demos", "--message", "the review is ready"); !d.OK || d.Message.To != self+"/tech-demos" ||
		!strings.Contains(d.Message.Expect, "woken") {
		t.Fatalf("to @Tech Demos: %+v", r)
	}
	eventually(t, time.Minute, "Tech Demos's ring", func() bool { return h2.count() == 1 })
	if b := string(h2.rings[0].body); !strings.Contains(b, `"as":"tech-demos"`) || !strings.Contains(b, `with as \"tech-demos\"`) || strings.Contains(b, "the review is ready") {
		t.Fatalf("Tech Demos's ring: %s", b)
	}
	// A name without a doorbell of its own is the agent's own doorbell's to
	// wake; that one stopped (the 401 above), so nothing rings.
	if d, r := sendMessage(t, w, w.cliEnv, 30*time.Second, "--to", self+"/chief-of-staff", "--message", "for chief"); !d.OK || !strings.Contains(d.Message.Expect, "woken") {
		t.Fatalf("to Chief of Staff: %+v", r)
	}
	time.Sleep(3 * time.Second)
	if h2.count() != 1 || h.count() != rangBefore {
		t.Fatalf("a stopped doorbell or another name's rang: %d, %d", h2.count(), h.count())
	}
	if d := callTool(t, session, "hermesh_status", map[string]any{"as": "tech_demos"}); doorbellOf(d) != "on" {
		t.Fatalf("hermesh_status as Tech Demos: %s", d.raw)
	}
}

// TestNames: two bots behind one agent, each with its own name
// (docs/agent-names.md). News comes back to the name that asked, another
// name's message waits for a turn of its own, and reading one name's
// messages leaves the other's waiting.
func TestNames(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	helper := w.addAgent("helper", &localv1.GrantSpec{})
	news := func(as, session, kind string) (messageDoc, bool) {
		l, _ := w.messages(30*time.Second, "--as", as)
		for _, m := range l.Messages {
			if m.News != nil && m.News.SessionID == session && m.News.Kind == kind {
				return m, true
			}
		}
		return messageDoc{}, false
	}

	// Tech Demos starts a session that asks; Chief of Staff writes into it.
	s, r := w.startSession(30*time.Second, "--as", "Tech Demos", "--prompt", "ask Color|Which color?|Red,Blue\nsay done")
	if r.code != 0 || !strings.Contains(r.stdout, `"as": "tech-demos"`) {
		t.Fatalf("start as Tech Demos: %+v", r)
	}
	eventually(t, 30*time.Second, "the question", func() bool { return w.get(s.SessionID).Status == "waiting_input" })
	d, r := sendMessage(t, w, append(slices.Clone(w.cliEnv), "HERMESH_AS=Chief of Staff"), 30*time.Second,
		"--to", s.SessionID, "--message", "say from chief")
	if !d.OK || d.Message.Delivered != "queued" {
		t.Fatalf("another name's message must wait for a turn of its own: %+v", r)
	}

	// The question is Tech Demos's news alone, however it writes its name.
	eventually(t, 30*time.Second, "Tech Demos's question", func() bool { _, ok := news("tech_demos", s.SessionID, "question"); return ok })
	if l, r := w.messages(30*time.Second, "--as", "chief-of-staff"); len(l.Messages) != 0 {
		t.Fatalf("Chief of Staff's list: %+v", r)
	}
	if l, r := w.messages(30 * time.Second); len(l.Messages) != 0 {
		t.Fatalf("the agent's own list: %+v", r)
	}

	// A message to Chief of Staff stays waiting while Tech Demos reads.
	hd, r := sendMessage(t, w, helper, 30*time.Second, "--to", contactAddress(t, w, helper, "bot")+"/Chief of Staff", "--message", "for chief")
	if !hd.OK || !strings.HasSuffix(hd.Message.To, "/chief-of-staff") || strings.Contains(hd.Message.Expect, "machine") {
		t.Fatalf("helper's message: %+v", r)
	}
	// By the agent's name too.
	if d, r := sendMessage(t, w, helper, 30*time.Second, "--to", "bot/Tech Demos", "--message", "for tech demos"); !d.OK || d.Message.Name != "bot/tech-demos" {
		t.Fatalf("to bot/Tech Demos: %+v", r)
	}
	w.messages(30*time.Second, "--as", "tech-demos")
	if st := messageState(t, w, helper, hd.Message.ID); st != "waiting" {
		t.Fatalf("a message nobody was shown: %s", st)
	}

	// Tech Demos's answer ends its turn; then Chief of Staff's turn runs,
	// and that turn's news is Chief of Staff's.
	if r := w.cli(30*time.Second, "session", "answer", s.SessionID, "--choose", "1", "--as", "Tech Demos"); r.code != 0 {
		t.Fatalf("answer: %+v", r)
	}
	var chief messageDoc
	eventually(t, time.Minute, "Chief of Staff's news", func() bool {
		m, ok := news("Chief of Staff", s.SessionID, "turn_done")
		chief = m
		return ok
	})
	if chief.News.Result != "from chief" {
		t.Fatalf("Chief of Staff's turn: %+v", chief)
	}
	if st := messageState(t, w, helper, hd.Message.ID); st != "taken" {
		t.Fatalf("a message Chief of Staff was shown: %s", st)
	}
	eventually(t, 30*time.Second, "Tech Demos's own turn's news", func() bool {
		m, ok := news("tech-demos", s.SessionID, "turn_done")
		return ok && m.News.Result == "done"
	})
}

// TestWake: a bot that reads its messages with the CLI, such as a Grok bot
// on its cloud computer, is woken by its routine's webhook when messages
// wait for its name (docs/agent-names.md §7). It asks for the doorbell; the
// owner sets it up with the routine's key, which the bot never sees.
func TestWake(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	ctx := context.Background()
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	w.relay.stop()
	w.relayArgs = append(w.relayArgs, "--mcp")
	w.relayEnv = []string{"HERMESH_MCP_LOCAL_WEBHOOKS=1"}
	w.startRelay()
	grok := w.addAgent("grok", &localv1.GrantSpec{})
	hermesh := filepath.Join(w.bin, "hermesh")
	eventually(t, 30*time.Second, "the machine back online", func() bool {
		return !strings.Contains(w.cli(30*time.Second, "project", "list").stdout, "offline")
	})

	// The bot asks; with no console on this relay, it's told the Mac app.
	r := run(t, append(slices.Clone(grok), "HERMESH_AS=Tech Demos"), 30*time.Second, hermesh, "message", "wake", "-o", "json")
	if r.code != 0 || !strings.Contains(r.stdout, "Settings → Access → grok → Wake") || !strings.Contains(r.stdout, `\"tech-demos\"`) {
		t.Fatalf("message wake: %+v", r)
	}

	// The routine: a webhook that reads, with the CLI, the messages of the
	// name the ring is for.
	type woken struct{ as, list string }
	runs := make(chan woken, 10)
	tries := make(chan string, 10)
	routine := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var body struct{ Type, As, Text string }
		json.NewDecoder(req.Body).Decode(&body)
		if req.Header.Get("Authorization") != "Bearer crsr_key" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		if body.Type == "hermesh.check" {
			tries <- body.Text
		}
		if body.Type == "hermesh.message" {
			go func() {
				// A ring for no name is for whichever name the bot goes by.
				as := body.As
				if as == "" {
					as = "research"
				}
				out := run(t, grok, time.Minute, hermesh, "message", "list", "--as", as, "-o", "json").stdout
				runs <- woken{body.As, out}
			}()
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer routine.Close()
	grokID := contactAddress(t, w, w.cliEnv, "grok")
	bad, err := w.app.client.SetWake(ctx, &localv1.SetWakeRequest{ClientId: grokID, Name: "Tech Demos", Url: routine.URL, Authorization: "Bearer wrong"})
	if err != nil || bad.Check.Ok || bad.Wake != nil || !strings.Contains(bad.Check.Reason, "the key is wrong") {
		t.Fatalf("a wrong key must fail the check and not be kept: %v %v", bad, err)
	}
	set, err := w.app.client.SetWake(ctx, &localv1.SetWakeRequest{ClientId: grokID, Name: "Tech Demos", Url: routine.URL, Authorization: "Bearer crsr_key"})
	if err != nil || !set.Check.Ok || set.Wake.Name != "tech-demos" {
		t.Fatalf("SetWake: %v %v", set, err)
	}
	if l, err := w.app.client.ListWakes(ctx, &localv1.ListWakesRequest{ClientId: grokID}); err != nil || len(l.Wakes) != 1 || l.Wakes[0].State != "on" {
		t.Fatalf("ListWakes: %v %v", l, err)
	}
	if out := run(t, w.cliEnv, 30*time.Second, hermesh, "message", "contacts").stdout; !strings.Contains(out, "@tech-demos") {
		t.Fatalf("contacts must list the woken name: %s", out)
	}

	// Tech Demos starts a session; its news rings Tech Demos's routine,
	// which finds it.
	if r := run(t, grok, 30*time.Second, hermesh, "--as", "tech-demos", "session", "start", "--project", "demo", "--prompt", "say built"); r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	select {
	case got := <-runs:
		if got.as != "tech-demos" || !strings.Contains(got.list, `"result": "built"`) {
			t.Fatalf("the woken routine read: %+v", got)
		}
	case <-time.After(time.Minute):
		t.Fatal("Tech Demos's routine was not woken")
	}
	// Another name of the same bot has no doorbell: nothing rings.
	if r := run(t, grok, 30*time.Second, hermesh, "--as", "research", "session", "start", "--project", "demo", "--prompt", "say found"); r.code != 0 {
		t.Fatalf("start as research: %+v", r)
	}
	eventually(t, 30*time.Second, "Research's news", func() bool {
		return strings.Contains(run(t, grok, 30*time.Second, hermesh, "--as", "research", "message", "list", "-o", "json").stdout, `"result": "found"`)
	})
	select {
	case got := <-runs:
		t.Fatalf("a name without a doorbell rang: %+v", got)
	case <-time.After(3 * time.Second):
	}

	// The key appears nowhere but the vault.
	for _, p := range w.logs {
		if strings.Contains(p.output(), "crsr_key") {
			t.Fatalf("the key is in %s's output", p.name)
		}
	}
	if _, err := w.app.client.ClearWake(ctx, &localv1.ClearWakeRequest{ClientId: grokID, Name: "tech-demos"}); err != nil {
		t.Fatal(err)
	}
	if out := run(t, w.cliEnv, 30*time.Second, hermesh, "message", "contacts").stdout; strings.Contains(out, "@tech-demos") {
		t.Fatalf("a cleared doorbell's name is still listed: %s", out)
	}

	// The doorbell most owners set: the agent's own, which wakes it for every
	// name. The owner pasted the whole header Grok shows; the try, the first
	// ring, says to read whatever waits.
	for len(tries) > 0 {
		<-tries
	}
	own, err := w.app.client.SetWake(ctx, &localv1.SetWakeRequest{ClientId: grokID, Url: routine.URL, Authorization: "Bearer Authorization: Bearer crsr_key"})
	if err != nil || !own.Check.Ok || own.Wake.Name != "" {
		t.Fatalf("SetWake for the agent itself: %v %v", own, err)
	}
	if text := <-tries; !strings.Contains(text, "Read any messages waiting for you") || !strings.Contains(text, "message_list") {
		t.Fatalf("the try: %q", text)
	}
	// Research, a name without a doorbell of its own, is woken by it, though
	// the machine may not know of the doorbell yet (it looks again within a
	// minute): the ring then names no one, and the bot reads its own name.
	if r := run(t, grok, 30*time.Second, hermesh, "--as", "research", "session", "start", "--project", "demo", "--prompt", "say found again"); r.code != 0 {
		t.Fatalf("start as research: %+v", r)
	}
	select {
	case got := <-runs:
		if (got.as != "research" && got.as != "") || !strings.Contains(got.list, `"result": "found again"`) {
			t.Fatalf("the agent's routine read: %+v", got)
		}
	case <-time.After(2 * time.Minute): // at most one ring a minute: the try was one
		t.Fatal("the agent's own doorbell did not wake Research")
	}
	// The owner sees it: what waits (Research's two news and Tech Demos's,
	// whose own doorbell is gone), and the last rings, newest first.
	l, err := w.app.client.ListWakes(ctx, &localv1.ListWakesRequest{ClientId: grokID})
	if err != nil || len(l.Wakes) != 1 {
		t.Fatalf("ListWakes: %v %v", l, err)
	}
	wk := l.Wakes[0]
	if wk.Waiting != 3 || len(wk.Recent) != 2 || wk.Recent[0].Check || wk.Recent[0].Status != 200 ||
		!wk.Recent[1].Check || wk.Recent[1].Status != 200 {
		t.Fatalf("the agent's doorbell, as the owner sees it: %v", wk)
	}
	// The session says which name started it.
	ss, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 10})
	if err != nil || len(ss.Sessions) == 0 || ss.Sessions[0].CreatedAs != "research" {
		t.Fatalf("the session's created_as: %v %v", ss, err)
	}
}

// TestRenameAgent: the owner gives an agent another name (docs/agent-names.md
// §14). Its grant is signed again with that alone changed, so every device
// and the agent's contacts show it, and the agent keeps working, as long as
// before. Then another icon: an app's, or a picture of the owner's, which
// the grant carries re-encoded.
func TestRenameAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	ctx := context.Background()
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	grant := func(id string) *localv1.Client {
		l, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range l.Clients {
			if c.Label == id || c.GrantId == id {
				return c
			}
		}
		t.Fatalf("no grant %s in %v", id, l.Clients)
		return nil
	}
	before := grant("bot")
	res, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: before.GrantId, Label: "  Grok   Bot "})
	if err != nil || res.GrantVersion != before.GrantVersion+1 {
		t.Fatalf("RenameClient: %v %v", res, err)
	}
	after := grant(before.GrantId)
	if after.Label != "Grok Bot" || after.GrantVersion != res.GrantVersion || !proto.Equal(after.ExpiresAt, before.ExpiresAt) || !proto.Equal(after.Spec, withLabel(before.Spec, "Grok Bot")) {
		t.Fatalf("renamed, the grant must change its name alone: %v, was %v", after, before)
	}
	eventually(t, time.Minute, "the agent's contacts saying its new name", func() bool {
		return strings.Contains(w.cli(30*time.Second, "message", "contacts").stdout, "Grok Bot")
	})
	if r := w.cli(time.Minute, "session", "start", "--project", "demo", "--prompt", "say renamed", "--wait"); r.code != 0 || !strings.Contains(r.stdout, "renamed") {
		t.Fatalf("the renamed agent's session: %+v", r)
	}
	if _, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: before.GrantId, Label: strings.Repeat("x", 65)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a name too long: %v", err)
	}

	// An icon: an app's, then the owner's own picture, which is re-encoded.
	icon := func(v string) *string { return &v }
	if _, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: before.GrantId, Label: "Grok Bot", Icon: icon("app:grokbot")}); err != nil {
		t.Fatal(err)
	}
	if c := grant(before.GrantId); c.Icon != "app:grokbot" || len(c.Image) != 0 {
		t.Fatalf("an app's icon: %q %d bytes", c.Icon, len(c.Image))
	}
	pic := squarePNG(t, 128)
	if _, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: before.GrantId, Label: "Grok Bot", Icon: icon("image"), Image: append(pic, "trailing bytes"...)}); err != nil {
		t.Fatal(err)
	}
	if c := grant(before.GrantId); c.Icon != "image" || !bytes.HasPrefix(c.Image, []byte("\x89PNG")) || bytes.Contains(c.Image, []byte("trailing")) {
		t.Fatalf("the owner's picture: %q %d bytes", c.Icon, len(c.Image))
	}
	// Renaming alone (the iPhone) and changing the access keep the icon.
	if _, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: before.GrantId, Label: "Grok"}); err != nil {
		t.Fatal(err)
	}
	c := grant(before.GrantId)
	if _, err := w.app.client.UpdateClient(ctx, &localv1.UpdateClientRequest{GrantId: c.GrantId, Spec: c.Spec}); err != nil {
		t.Fatal(err)
	}
	if c := grant(before.GrantId); c.Label != "Grok" || c.Icon != "image" || len(c.Image) == 0 {
		t.Fatalf("renamed and changed, the icon must stay: %q %q %d bytes", c.Label, c.Icon, len(c.Image))
	}
	for _, bad := range []*localv1.RenameClientRequest{
		{GrantId: before.GrantId, Label: "Grok", Icon: icon("app:Grok Bot")},
		{GrantId: before.GrantId, Label: "Grok", Icon: icon("image")},
		{GrantId: before.GrantId, Label: "Grok", Icon: icon("image"), Image: []byte("<svg/>")},
		{GrantId: before.GrantId, Label: "Grok", Icon: icon("image"), Image: squarePNG(t, 512)},
	} {
		if _, err := w.app.client.RenameClient(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("a bad icon %q (%d bytes): %v", bad.GetIcon(), len(bad.Image), err)
		}
	}
	// The machines take the grant with its picture: the agent still works.
	if r := w.cli(time.Minute, "session", "start", "--project", "demo", "--prompt", "say pictured", "--wait"); r.code != 0 || !strings.Contains(r.stdout, "pictured") {
		t.Fatalf("the agent's session after its new icon: %+v", r)
	}
}

// squarePNG is a size × size PNG of a gradient.
func squarePNG(t *testing.T, size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withLabel is spec with another label.
func withLabel(spec *localv1.GrantSpec, label string) *localv1.GrantSpec {
	s := proto.Clone(spec).(*localv1.GrantSpec)
	s.Label = label
	return s
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

// addAgent pairs another agent CLI with the owner, as newOwnerMac pairs
// "bot", and returns its environment.
func (w *ownerMac) addAgent(name string, grant *localv1.GrantSpec) []string {
	w.t.Helper()
	env := []string{"HERMESH_HOME=" + filepath.Join(w.tmp, "agent-"+name)}
	login := start(w.t, "login "+name, env, filepath.Join(w.bin, "hermesh"), "auth", "login", w.addr, "--name", name)
	if _, err := w.app.client.ApproveClientPairing(context.Background(), &localv1.ApproveClientPairingRequest{Code: login.pairingCode(w.t), Spec: grant}); err != nil {
		w.t.Fatal(err)
	}
	if c := login.wait(w.t, 30*time.Second); c != 0 {
		w.t.Fatalf("login %s: %s", name, login.output())
	}
	return env
}

type sentDoc struct {
	OK      bool `json:"ok"`
	Message struct {
		ID, To, Name, Delivered, Expect string
	} `json:"message"`
	Session sessionJSON `json:"session"`
	Reply   *messageDoc `json:"reply"`
	Error   *struct{ Kind, Hint string }
}

func sendMessage(t *testing.T, w *ownerMac, env []string, timeout time.Duration, args ...string) (sentDoc, result) {
	t.Helper()
	r := run(t, env, timeout, filepath.Join(w.bin, "hermesh"), append([]string{"message", "send", "-o", "json"}, args...)...)
	var d sentDoc
	json.Unmarshal([]byte(r.stdout), &d)
	return d, r
}

func listMessages(t *testing.T, w *ownerMac, env []string) messageList {
	t.Helper()
	r := run(t, env, time.Minute, filepath.Join(w.bin, "hermesh"), "message", "list", "-o", "json")
	var l messageList
	json.Unmarshal([]byte(r.stdout), &l)
	return l
}

// TestMessages: agents write to each other and to sessions, the one way to
// tell a session anything. A message to a session joins its sender's own
// turn, or waits for a turn of its own; one to an offline machine waits
// for it at the post office.
func TestMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	other := w.addAgent("helper", &localv1.GrantSpec{TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL})

	// --- Agent to agent ---------------------------------------------------
	var d sentDoc
	slowly(t, 30*time.Second, "helper reachable", func() bool {
		d, _ = sendMessage(t, w, w.cliEnv, 30*time.Second, "--to", "helper", "--subject", "a question", "--message", "Which branch is the release on?")
		return d.OK
	})
	if d.Message.Delivered != "queued" || !strings.HasPrefix(d.Message.To, "c_") || d.Message.Name != "helper" {
		t.Fatalf("message to an agent: %+v", d)
	}
	asked := d.Message.ID
	l := listMessages(t, w, other)
	if len(l.Messages) != 1 || l.Messages[0].ID != asked || l.Messages[0].From.Name != "bot" || l.Messages[0].From.Kind != "cli" ||
		l.Messages[0].Text != "Which branch is the release on?" {
		t.Fatalf("helper's inbox: %+v", l)
	}
	botAddress := l.Messages[0].From.Address
	var status struct {
		States []struct{ ID, State string } `json:"states"`
	}
	json.Unmarshal([]byte(w.cli(30*time.Second, "message", "status", asked, "-o", "json").stdout), &status)
	if len(status.States) != 1 || status.States[0].State != "taken" {
		t.Fatalf("status once read: %+v", status)
	}
	// The reply, which bot waits for.
	waiting := start(t, "bot waits", w.cliEnv, filepath.Join(w.bin, "hermesh"), "message", "send", "-o", "json", "--to", "helper", "--wait", "--timeout", "60s",
		"--message", "And the tag?")
	var second string
	slowly(t, 30*time.Second, "the second message", func() bool {
		for _, m := range listMessages(t, w, other).Messages {
			if m.Text == "And the tag?" {
				second = m.ID
			}
		}
		return second != ""
	})
	if d, r := sendMessage(t, w, other, 30*time.Second, "--to", botAddress, "--reply-to", second, "--message", "release-7, tagged v7.0"); !d.OK {
		t.Fatalf("reply: %+v", r)
	}
	if c := waiting.wait(t, time.Minute); c != 0 {
		t.Fatalf("bot's --wait: %s", waiting.output())
	}
	if !strings.Contains(waiting.output(), "release-7, tagged v7.0") {
		t.Fatalf("bot's --wait must show the reply: %s", waiting.output())
	}
	if r := run(t, other, 30*time.Second, filepath.Join(w.bin, "hermesh"), "message", "ack", asked, second); r.code != 0 {
		t.Fatalf("ack: %+v", r)
	}
	json.Unmarshal([]byte(w.cli(30*time.Second, "message", "status", asked, "-o", "json").stdout), &status)
	if status.States[0].State != "handled" {
		t.Fatalf("status once handled: %+v", status)
	}
	for _, m := range listMessages(t, w, w.cliEnv).Messages { // the reply bot's --wait read
		w.ack(m.ID)
	}

	// --- The creator, to its own session ------------------------------------
	s, r := w.startSession(time.Minute, "--wait", "--prompt", "say first")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	d, r = sendMessage(t, w, w.cliEnv, time.Minute, "--to", s.SessionID, "--wait", "--message", "say second")
	if !d.OK || d.Message.Delivered != "turn" || d.Session.Result != "second" {
		t.Fatalf("to its own session: %+v", r)
	}

	// --- A session writes to agents, through its message_send tool ------------
	ws, r := w.startSession(time.Minute, "--wait", "--prompt", "message |Halfway: the tests pass\nmessage helper|Can you check the staging deploy?\nsay done")
	if r.code != 0 || ws.Result != "done" {
		t.Fatalf("a session writing: %+v", r)
	}
	var fromSession messageDoc
	for _, m := range listMessages(t, w, w.cliEnv).Messages {
		if m.From.Session == ws.SessionID {
			fromSession = m
		}
	}
	if fromSession.Text != "Halfway: the tests pass" || fromSession.From.Kind != "machine" || fromSession.News != nil {
		t.Fatalf("the creator's message from its session: %+v", fromSession)
	}
	w.ack(fromSession.ID)
	var toHelper messageDoc
	for _, m := range listMessages(t, w, other).Messages {
		if m.From.Session == ws.SessionID {
			toHelper = m
		}
	}
	if toHelper.Text != "Can you check the staging deploy?" {
		t.Fatalf("helper's message from the session: %+v", listMessages(t, w, other))
	}
	if ev := w.cli(30*time.Second, "session", "events", ws.SessionID, "--limit", "200", "-o", "json").stdout; strings.Count(ev, `"message_sent"`) != 2 ||
		!strings.Contains(ev, `"to_name": "helper"`) {
		t.Fatalf("the session records what it wrote: %s", ev)
	}
	run(t, other, 30*time.Second, filepath.Join(w.bin, "hermesh"), "message", "ack", toHelper.ID)

	// --- Another agent's message waits for a turn of its own -----------------
	q, _ := w.startSession(30*time.Second, "--prompt", "ask Color|Which color?|Red,Blue\nsay done")
	slowly(t, 30*time.Second, "the question", func() bool { return w.get(q.SessionID).Status == "waiting_input" })
	d, r = sendMessage(t, w, other, 30*time.Second, "--to", q.SessionID, "--message", "say from helper")
	if !d.OK || d.Message.Delivered != "queued" {
		t.Fatalf("helper's message while bot's question waits: %+v", r)
	}
	if g := w.get(q.SessionID); g.Status != "waiting_input" {
		t.Fatalf("another agent's message must not answer the question: %+v", g)
	}
	if r := w.cli(30*time.Second, "session", "answer", q.SessionID, "--choose", "1"); r.code != 0 {
		t.Fatalf("answer: %+v", r)
	}
	// Bot's turn ends, then helper's runs: its news goes to helper.
	var news messageDoc
	slowly(t, time.Minute, "helper's news", func() bool {
		for _, m := range listMessages(t, w, other).Messages {
			if m.News != nil && m.News.SessionID == q.SessionID && m.News.Kind == "turn_done" {
				news = m
			}
		}
		return news.ID != ""
	})
	if news.News.Result != "from helper" {
		t.Fatalf("helper's turn: %+v", news)
	}
	ev := w.cli(30*time.Second, "session", "events", q.SessionID, "--limit", "200", "-o", "json").stdout
	if !strings.Contains(ev, `"message_received"`) || !strings.Contains(ev, `"from_name": "helper"`) || !strings.Contains(ev, `"delivered": "queued"`) {
		t.Fatalf("the session records helper's message: %s", ev)
	}

	// --- An offline machine: the message waits for it ------------------------
	// Also another agent's message that waits for a turn of its own: it
	// outlasts the restart. The machine comes back paused, and keeps what
	// waits at the post office until it resumes.
	q2, _ := w.startSession(30*time.Second, "--prompt", "ask Color|Which color?|Red,Blue\nsay done")
	slowly(t, 30*time.Second, "the question", func() bool { return w.get(q2.SessionID).Status == "waiting_input" })
	if d, r := sendMessage(t, w, other, 30*time.Second, "--to", q2.SessionID, "--message", "say after the restart"); !d.OK || d.Message.Delivered != "queued" {
		t.Fatalf("helper's message while bot's question waits: %+v", r)
	}
	ms, err := w.app.client.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	pause := func(paused bool) {
		t.Helper()
		if _, err := w.app.client.SetMachinePaused(context.Background(), &localv1.SetMachinePausedRequest{MachineId: ms.Machines[0].MachineId, Paused: paused}); err != nil {
			t.Fatal(err)
		}
	}
	pause(true)
	w.owner.stop()
	w.app.cancel()
	eventually(t, 30*time.Second, "the machine offline", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return strings.Contains(r.stdout, "offline")
	})
	notes := filepath.Join(w.tmp, "notes.txt")
	os.WriteFile(notes, []byte("x"), 0o644)
	if _, r := sendMessage(t, w, w.cliEnv, 30*time.Second, "--to", s.SessionID, "-f", notes, "--message", "say with a file"); r.code != 4 {
		t.Fatalf("files to an offline machine: %+v", r)
	}
	d, r = sendMessage(t, w, w.cliEnv, 30*time.Second, "--to", s.SessionID, "--message", "say while you were away")
	if !d.OK || d.Message.Delivered != "queued" || !strings.Contains(d.Message.Expect, "offline") {
		t.Fatalf("to an offline machine: %+v", r)
	}
	w.startOwner()
	w.app = newTestApp(t, w.sock, w.root)
	eventually(t, 30*time.Second, "the machine online", func() bool {
		return !strings.Contains(w.cli(30*time.Second, "project", "list").stdout, "offline")
	})
	time.Sleep(3 * time.Second)
	if st := messageState(t, w, w.cliEnv, d.Message.ID); st != "taken" {
		t.Fatalf("a message to a paused machine: %s", st)
	}
	pause(false)
	slowly(t, 30*time.Second, "the turn the waiting message started", func() bool {
		return w.get(s.SessionID).Result == "while you were away"
	})
	slowly(t, 30*time.Second, "helper's turn after the restart", func() bool {
		return w.get(q2.SessionID).Result == "after the restart"
	})

	// --- Archived: refused at once --------------------------------------------
	if r := w.cli(30*time.Second, "session", "archive", s.SessionID); r.code != 0 {
		t.Fatalf("archive: %+v", r)
	}
	if d, r := sendMessage(t, w, w.cliEnv, 30*time.Second, "--to", s.SessionID, "--message", "say no"); d.OK || r.code != 2 || d.Error == nil || d.Error.Kind != "invalid_state" {
		t.Fatalf("to an archived session: %+v", r)
	}
}

// hookRun runs `hermesh message hook <agent>` as the agent does: the event
// on stdin, its JSON on stdout.
func hookRun(t *testing.T, w *ownerMac, env []string, agent string, in map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(in)
	cmd := exec.Command(filepath.Join(w.bin, "hermesh"), "message", "hook", agent)
	cmd.Env = append(append(os.Environ(), w.cliEnv...), env...)
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook %s %v: %v %s", agent, in, err, out)
	}
	var v map[string]any
	if len(bytes.TrimSpace(out)) > 0 {
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("hook output: %q", out)
		}
	}
	return v
}

func additionalContext(v map[string]any) string {
	h, _ := v["hookSpecificOutput"].(map[string]any)
	s, _ := h["additionalContext"].(string)
	return s
}

// claudeInbox plays a Claude Code conversation's inbox socket: it records
// what is posted to it.
type claudeInbox struct {
	path  string
	mu    sync.Mutex
	lines []string
}

func newClaudeInbox(t *testing.T) *claudeInbox {
	dir, _ := os.MkdirTemp(shortTemp, "cc")
	t.Cleanup(func() { os.RemoveAll(dir) })
	c := &claudeInbox{path: filepath.Join(dir, "inbox.sock")}
	ln, err := localipc.Listen(c.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b, _ := io.ReadAll(conn)
			conn.Close()
			c.mu.Lock()
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				c.lines = append(c.lines, l)
			}
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *claudeInbox) posted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lines)
}

// TestAgentsOnAComputer: Claude Code and Codex on the user's computer take
// their messages through their hooks: each conversation gets the news and
// replies of what it started or wrote, Claude Code between tool calls and
// while idle through its inbox socket, Codex when a turn would end. A
// message addressed to no conversation is only counted.
func TestAgentsOnAComputer(t *testing.T) {
	unixOnly(t, "it reads processes with ps")
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	helper := w.addAgent("helper", &localv1.GrantSpec{})

	// --- Claude Code --------------------------------------------------------
	inbox := newClaudeInbox(t)
	envFile := filepath.Join(w.tmp, "claude-env")
	claudeEnv := []string{"CLAUDE_ENV_FILE=" + envFile, "CLAUDE_CODE_MESSAGING_SOCKET=" + inbox.path, "CLAUDE_CODE_MESSAGING_TOKEN=tok-1"}
	start := hookRun(t, w, claudeEnv, "claude", map[string]any{"session_id": "conv-1", "hook_event_name": "SessionStart", "source": "startup"})
	if ctx := additionalContext(start); !strings.Contains(ctx, "connected to Hermesh as bot") || !strings.Contains(ctx, "conv-1") {
		t.Fatalf("SessionStart's context: %v", start)
	}
	if b, _ := os.ReadFile(envFile); !strings.Contains(string(b), `export HERMESH_AS="conv-1"`) {
		t.Fatalf("the conversation's environment: %q", b)
	}
	pid := filepath.Join(w.tmp, "agent", "watch-conv-1.pid")
	eventually(t, 10*time.Second, "the watch running", func() bool { _, err := os.Stat(pid); return err == nil })

	// A session this conversation starts: its news comes back to it.
	r := run(t, append(slices.Clone(w.cliEnv), "HERMESH_AS=conv-1"), 30*time.Second, filepath.Join(w.bin, "hermesh"),
		"session", "start", "--project", "demo", "--prompt", "say the build is green")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	slowly(t, time.Minute, "the news in Claude Code's inbox", func() bool { return len(inbox.posted()) >= 2 })
	posted := inbox.posted()
	if posted[0] != `{"token":"tok-1","type":"auth"}` && posted[0] != `{"type":"auth","token":"tok-1"}` {
		t.Fatalf("the auth line: %q", posted[0])
	}
	var user struct {
		Type    string
		Message struct{ Role, Content string }
	}
	json.Unmarshal([]byte(posted[1]), &user)
	if user.Type != "user" || !strings.Contains(user.Message.Content, "<hermesh-message") || !strings.Contains(user.Message.Content, "the build is green") {
		t.Fatalf("what the watch posted: %q", posted[1])
	}
	if l := listMessages(t, w, w.cliEnv); len(l.Messages) != 0 {
		t.Fatalf("a delivered message must be acknowledged: %+v", l)
	}
	// What the session writes to the agent that started it comes back to
	// the conversation too.
	if r := run(t, append(slices.Clone(w.cliEnv), "HERMESH_AS=conv-1"), 30*time.Second, filepath.Join(w.bin, "hermesh"),
		"session", "start", "--project", "demo", "--prompt", "message |the tests pass"); r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	slowly(t, time.Minute, "the session's message in Claude Code's inbox", func() bool {
		return slices.ContainsFunc(inbox.posted(), func(p string) bool { return strings.Contains(p, "the tests pass") })
	})
	eventually(t, 30*time.Second, "the inbox empty", func() bool { return len(listMessages(t, w, w.cliEnv).Messages) == 0 })
	// One addressed to no conversation is only counted, and the watch
	// waits for the next one instead of fetching it again and again.
	posts := len(inbox.posted())
	if d, r := sendMessage(t, w, helper, 30*time.Second, "--to", "bot", "--message", "for whoever reads it"); !d.OK {
		t.Fatalf("helper's message: %+v", r)
	}
	time.Sleep(2 * time.Second)
	if n := len(inbox.posted()); n != posts {
		t.Fatalf("a message to no conversation was pushed: %q", inbox.posted())
	}
	before := cpuTime(t, pid)
	time.Sleep(3 * time.Second)
	if used := cpuTime(t, pid) - before; used > 300*time.Millisecond {
		t.Fatalf("the watch used %v of CPU in 3s with a message it leaves waiting", used)
	}
	if ctx := additionalContext(hookRun(t, w, claudeEnv, "claude", map[string]any{"session_id": "conv-1", "hook_event_name": "UserPromptSubmit"})); !strings.Contains(ctx, "1 message(s) addressed to no conversation") {
		t.Fatalf("UserPromptSubmit's note: %q", ctx)
	}
	// The conversation ends: so does its watch.
	os.Remove(inbox.path)
	eventually(t, 20*time.Second, "the watch gone", func() bool { _, err := os.Stat(pid); return err != nil })

	// --- Codex: when a turn would end --------------------------------------
	if ctx := additionalContext(hookRun(t, w, nil, "codex", map[string]any{"session_id": "thr-1", "hook_event_name": "SessionStart"})); !strings.Contains(ctx, "--as thr-1") {
		t.Fatalf("Codex's SessionStart context: %q", ctx)
	}
	// More than a page of others' messages wait ahead of the
	// conversation's own: the hooks still find it.
	helper2 := w.addAgent("helper2", &localv1.GrantSpec{})
	for i := range 53 {
		from := helper
		if i >= 27 { // at most 30 an hour from one agent to another
			from = helper2
		}
		if d, r := sendMessage(t, w, from, 30*time.Second, "--to", "bot", "--message", fmt.Sprintf("for whoever reads it, %d", i)); !d.OK {
			t.Fatalf("message %d: %+v", i, r)
		}
	}
	if d, r := sendMessage(t, w, helper, 30*time.Second, "--to", contactAddress(t, w, helper, "bot")+"/thr-1", "--message", "your review is done"); !d.OK {
		t.Fatalf("helper's message to the Codex conversation: %+v", r)
	}
	stop := hookRun(t, w, nil, "codex", map[string]any{"session_id": "thr-1", "hook_event_name": "Stop", "stop_hook_active": false})
	if stop["decision"] != "block" || !strings.Contains(fmt.Sprint(stop["reason"]), "your review is done") {
		t.Fatalf("Codex's Stop: %v", stop)
	}
	if again := hookRun(t, w, nil, "codex", map[string]any{"session_id": "thr-1", "hook_event_name": "Stop", "stop_hook_active": true}); again != nil {
		t.Fatalf("a Stop already continued must let the turn end: %v", again)
	}

	// --- Hermesh's own sessions --------------------------------------------
	// Their coding agents are told so, and the hooks the user installed
	// for Claude Code on this computer leave them alone: their machine gives
	// them their messages.
	es, r := w.startSession(30*time.Second, "--wait", "--prompt", "env HERMESH_SESSION")
	if r.code != 0 || es.Result != "HERMESH_SESSION="+es.SessionID {
		t.Fatalf("a session's coding agent: %+v %+v", es, r)
	}
	if out := hookRun(t, w, append(slices.Clone(claudeEnv), "HERMESH_SESSION="+es.SessionID), "claude",
		map[string]any{"session_id": "conv-2", "hook_event_name": "SessionStart", "source": "startup"}); out != nil {
		t.Fatalf("the hook in a Hermesh session: %v", out)
	}

	// --- Installing the hooks ------------------------------------------------
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model": "opus", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]}}`), 0o600)
	for range 2 {
		if r := run(t, append(slices.Clone(w.cliEnv), "HOME="+home), 30*time.Second, filepath.Join(w.bin, "hermesh"), "message", "hook", "claude", "--install"); r.code != 0 {
			t.Fatalf("install: %+v", r)
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	var settings struct {
		Model string
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	json.Unmarshal(b, &settings)
	if settings.Model != "opus" || len(settings.Hooks["Stop"]) != 2 || settings.Hooks["Stop"][0].Hooks[0].Command != "say done" ||
		len(settings.Hooks["SessionStart"]) != 1 || !strings.HasSuffix(settings.Hooks["SessionStart"][0].Hooks[0].Command, " message hook claude") {
		t.Fatalf("the settings after installing twice: %s", b)
	}
	if !strings.HasPrefix(string(b), "{\n  \"model\"") {
		t.Fatalf("the settings' own keys must keep their place: %s", b)
	}
}

// messageState is what `message status` says of a message.
func messageState(t *testing.T, w *ownerMac, env []string, id string) string {
	t.Helper()
	var status struct {
		States []struct{ ID, State string } `json:"states"`
	}
	json.Unmarshal([]byte(run(t, env, 30*time.Second, filepath.Join(w.bin, "hermesh"), "message", "status", id, "-o", "json").stdout), &status)
	if len(status.States) != 1 {
		t.Fatalf("status of %s: %+v", id, status)
	}
	return status.States[0].State
}

// cpuTime is the CPU time a process has used so far.
func cpuTime(t *testing.T, pidFile string) time.Duration {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ps", "-o", "time=", "-p", strings.TrimSpace(string(b))).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	// [[dd-]hh:]mm:ss.ss on macOS, [dd-]hh:mm:ss on Linux
	f := strings.Split(strings.TrimSpace(string(out)), ":")
	var d time.Duration
	for _, p := range f {
		if _, rest, ok := strings.Cut(p, "-"); ok {
			p = rest
		}
		v, _ := strconv.ParseFloat(p, 64)
		d = d*60 + time.Duration(v*float64(time.Second))
	}
	return d
}

// contactAddress is an agent's address, as another agent's contacts list it.
func contactAddress(t *testing.T, w *ownerMac, env []string, name string) string {
	t.Helper()
	var c struct {
		Contacts []struct{ Name, Address string }
	}
	json.Unmarshal([]byte(run(t, env, 30*time.Second, filepath.Join(w.bin, "hermesh"), "message", "contacts", "-o", "json").stdout), &c)
	for _, x := range c.Contacts {
		if x.Name == name {
			return x.Address
		}
	}
	t.Fatalf("no contact %s: %+v", name, c)
	return ""
}
