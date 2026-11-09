package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// herCard is a secure card as send_to_user's details carry it
// (docs/her-secure-cards.md §3.1), with a connect card's body.
type herCard struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	AskedBy   string            `json:"asked_by"`
	ExpiresAt time.Time         `json:"expires_at"`
	Ops       []json.RawMessage `json:"ops"`
	Body      struct {
		Name      string `json:"name"`
		URL       string `json:"url"`
		Reconnect bool   `json:"reconnect"`
		SignIn    string `json:"sign_in"`
	} `json:"body"`
}

// TestHerCards: Her asks the owner for a connector on a secure card
// (docs/her-secure-cards.md, step 1), on an environment that is not this
// Mac, and the owner's device answers through this Mac's hermeshd, the relay
// and the end-to-end tunnel. Her prepares the card with connector_connect
// and shows it with send_to_user; the device sees it, and the machine's
// status says it waits. An agent can't answer it; an answer whose ops do
// more than the console could is refused by the machine, one whose ops
// aren't the card's by her. The device approves, signs in to the connector
// as the console does, and the card ends done: Her passes it on, the
// connector works, and the card can't be answered again. The connector's
// service is her-mock's her-mcp-server, with OAuth.
func TestHerCards(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") == "1" {
		t.Skip("her takes a connector's http address on loopback only, and the tracker is not on the Linux machine's")
	}
	mcpServer := herMCPServer(t)
	env := newHerEnvironment(t)
	tracker := start(t, "tracker", nil, mcpServer, "--http", "127.0.0.1:0", "--public-host", env.mac, "--oauth", "--no-metadata-document", "--name", "tracker")
	var address string
	eventually(t, 20*time.Second, "the tracker's address", func() bool {
		address = regexp.MustCompile(`http://\S+/mcp`).FindString(tracker.output())
		return address != ""
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()

	// Asked by the owner, Her shows a card for the tracker, made from its
	// address as her checked it.
	if res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID,
		Text: "[[mock:connect:tracker|" + address + "]] Connect my tracker."}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	var shown struct {
		Card herCard `json:"card"`
		Text string  `json:"text"`
	}
	untilEvent(t, stream, "the card", func(e herEvent) bool {
		return e.Type == "tool_completed" && e.Result.ToolName == "send_to_user" && !e.Result.IsError &&
			json.Unmarshal(e.Result.Details, &shown) == nil && shown.Card.ID != ""
	})
	card := shown.Card
	if !strings.HasPrefix(card.ID, "c_") || card.Kind != "connect" || card.AskedBy != "owner" || card.Body.Name != "tracker" ||
		card.Body.URL != address || card.Body.SignIn != "oauth" || card.Body.Reconnect || len(card.Ops) == 0 || time.Until(card.ExpiresAt) < 23*time.Hour {
		t.Fatalf("the card: %+v", card)
	}
	waits := shown.Text
	if waits == "" {
		waits = card.Title
	}
	eventually(t, 30*time.Second, "the card waiting, in the machine's status", func() bool {
		l := listedMachine(t, env.c, env.machine).GetHer().GetLatest()
		return l.GetBotId() == env.bot.ID && l.GetKind() == machinev1.HerLatestKind_HER_LATEST_KIND_APPROVAL && l.GetText() == waits
	})

	approve := func(ops ...json.RawMessage) *herv1.Request {
		return herSetup("cards/answer", map[string]any{"session_id": session, "card_id": card.ID, "answer": map[string]any{"decision": "approve", "ops": ops}})
	}
	answer := func(r *herv1.Request) (*herv1.Response, error) {
		res, err := env.c.HerCall(ctx, &localv1.HerCallRequest{MachineId: env.machine, Call: &herv1.CallRequest{ConnectionId: op.ConnectionId, Request: r}})
		return res.GetResponse(), err
	}

	// An agent reaches none of her's methods, cards/answer included.
	if _, err := agentHer(t, env.w, env.machine).Call(ctx, &herv1.CallRequest{Request: approve(card.Ops...)}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent's answer: %v", err)
	}
	// One op more than the console could do refuses the whole answer, on the
	// machine.
	evil := json.RawMessage(`{"method":"config/write","params":{"layer":"user","edits":[{"key_path":"tools.bash.shell","value":"/tmp/evil"}]}}`)
	if _, err := answer(approve(append(slices.Clip(card.Ops), evil)...)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an answer with an op off the allowlist: %v", err)
	}
	// One op more that the console may do, but that isn't the card's: her
	// refuses it, and the card waits on.
	more := json.RawMessage(`{"method":"config/write","params":{"layer":"user","edits":[{"key_path":"web_search.enabled","value":false}]}}`)
	if res, err := answer(approve(append(slices.Clip(card.Ops), more)...)); err != nil || res.GetError().GetCode() != -32602 {
		t.Fatalf("an answer whose ops aren't the card's: %v %v", res, err)
	}

	// Approved: her writes the connector, and the card waits for its sign-in.
	res, err := answer(approve(card.Ops...))
	var answered struct {
		State string `json:"state"`
	}
	if err != nil || res.GetError() != nil || json.Unmarshal(res.GetResult(), &answered) != nil || answered.State != "working" && answered.State != "sign_in" {
		t.Fatalf("approving: %v %v", res, err)
	}
	cardState(t, stream, card.ID, "sign_in")

	// The device signs in as the console does, from the Mac app's main
	// process (apps/desktop's herSignin.ts): this Mac's hermeshd catches the
	// browser's redirect, and her on the machine exchanges what it carried.
	code, body := httpDo(t, http.MethodPost, env.console+"signin", `{"path":"/callback"}`)
	var signin struct {
		ID          string `json:"id"`
		RedirectURI string `json:"redirect_uri"`
	}
	if json.Unmarshal([]byte(body), &signin); code != http.StatusOK || signin.ID == "" {
		t.Fatalf("signin: %d %s", code, body)
	}
	var login struct {
		AuthorizeURL string `json:"authorize_url"`
		LoginID      string `json:"login_id"`
	}
	json.Unmarshal(herCall(t, env, op.ConnectionId, "mcp/login", map[string]string{"name": "tracker", "redirect_uri": signin.RedirectURI}), &login)
	landed, err := browser.Get(login.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	landed.Body.Close()
	code, body = httpDo(t, http.MethodGet, env.console+"signin/"+signin.ID, "")
	var caught struct {
		Code  string `json:"code"`
		State string `json:"state"`
		Iss   string `json:"iss"`
	}
	if json.Unmarshal([]byte(body), &caught); code != http.StatusOK || caught.Code == "" {
		t.Fatalf("what the redirect brought: %d %s", code, body)
	}
	herCall(t, env, op.ConnectionId, "mcp/login_complete", map[string]string{"login_id": login.LoginID, "code": caught.Code, "state": caught.State, "iss": caught.Iss})

	// Done: Her hears how it went and passes it on, and the machine's status
	// no longer says the card waits.
	done := cardState(t, stream, card.ID, "done")
	if done.Note == "" {
		t.Fatal("the card ended done without her words on it")
	}
	said := untilEvent(t, stream, "Her passing on how it went", func(e herEvent) bool { return e.sentText(done.Note) })
	eventually(t, 30*time.Second, "Her's message, not the card, in the machine's status", func() bool {
		l := listedMachine(t, env.c, env.machine).GetHer().GetLatest()
		return l.GetBotId() == env.bot.ID && l.GetKind() == machinev1.HerLatestKind_HER_LATEST_KIND_MESSAGE && l.GetText() != "" &&
			strings.HasPrefix(said.Args.Text, l.GetText())
	})
	var checked mcpChecked
	json.Unmarshal(herCall(t, env, "", "mcp/check", map[string]string{"name": "tracker"}), &checked)
	if checked.Server.Status.State != "ok" || checked.Server.AuthState != "logged_in" || !checked.has("echo") {
		t.Fatalf("the connector: %+v", checked)
	}

	// Answered once.
	if res, err := answer(approve(card.Ops...)); err != nil || !bytes.Contains(res.GetError().GetData(), []byte(`"card_ended"`)) {
		t.Fatalf("answering an ended card: %v %v", res, err)
	}
}

// cardState reads a conversation's events until the card is in state want.
// A card that ends otherwise, or wants another try, fails the test.
func cardState(t *testing.T, stream *connect.ServerStreamForClient[herv1.OpenEvent], id, want string) herEvent {
	t.Helper()
	return untilEvent(t, stream, "card "+id+" "+want, func(e herEvent) bool {
		if e.Type != "card_changed" || e.CardID != id {
			return false
		}
		switch e.State {
		case want:
			return true
		case "open", "working", "sign_in":
			return false
		}
		t.Fatalf("card %s went %s: %s", id, e.State, e.Note)
		return false
	})
}

// agentHer is HerService on a machine as the agent paired to the owner Mac
// reaches it: the CLI's key and grant, through the relay and an end-to-end
// tunnel of its own.
func agentHer(t *testing.T, w *ownerMac, machineID string) herv1connect.HerServiceClient {
	t.Helper()
	home := filepath.Join(w.tmp, "agent")
	var st struct {
		RelayURL  string `json:"relay_url"`
		OwnerID   string `json:"owner_id"`
		RootChain []byte `json:"root_chain"`
		Grant     []byte `json:"grant"`
	}
	b, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	key, kerr := identity.LoadKey(filepath.Join(home, "client.key"))
	rc, grant := &identityv1.RootChain{}, &identityv1.Signed{}
	trust := identity.NewTrust(st.OwnerID)
	for _, e := range []error{err, kerr, proto.Unmarshal(st.RootChain, rc), proto.Unmarshal(st.Grant, grant)} {
		if e != nil {
			t.Fatalf("the agent's identity: %v", e)
		}
	}
	if _, err := trust.AcceptRootChain(rc); err != nil {
		t.Fatal(err)
	}
	c, err := (&peer.Dialer{RelayURL: st.RelayURL, Role: relayv1.Role_ROLE_CLIENT, Key: key, Trust: trust, HTTPClient: http.DefaultClient,
		Credentials: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: rc, Subject: &identityv1.Credentials_Grant{Grant: grant}}
		}}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	m, err := c.Machine(context.Background(), machineID)
	if err != nil {
		t.Fatal(err)
	}
	return m.Her
}
