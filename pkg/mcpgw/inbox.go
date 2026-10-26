package mcpgw

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A connection's inbox (docs/agent-messages.md §6.3): the gateway holds its
// key, as it holds the connection's client key, opens its messages for the
// message tools, and rings the webhook the assistant registered whenever a
// message arrives. The ring carries no message: it says only that some wait.

// Inbox is a connection's inbox at the gateway.
type Inbox struct {
	ConnectionID string
	// SealedKey is the inbox's X25519 private key, sealed by the Vault.
	SealedKey []byte
	// Registered is how the post office was last told it takes messages
	// ("webhook" or "poll"); "" before it was told.
	Registered string
	// WebhookURL is where the doorbell rings; "" for none.
	WebhookURL string
	// SealedAuth is the value of the Authorization header the webhook
	// expects (a bearer key), sealed; nil for none.
	SealedAuth []byte
	// SealedSecret signs each ring (Standard Webhooks), sealed.
	SealedSecret []byte
	// Broken says why the webhook stopped ringing ("" while it works).
	Broken    string
	UpdatedAt time.Time
}

// InboxBackend keeps connections' inboxes. The SQLBackend does, in a table
// it makes itself; other backends keep them in memory.
type InboxBackend interface {
	GetInbox(ctx context.Context, connectionID string) (*Inbox, error)
	PutInbox(ctx context.Context, in *Inbox) error
	DeleteInbox(ctx context.Context, connectionID string) error
}

const inboxSchema = `CREATE TABLE IF NOT EXISTS mcp_inboxes (
  connection_id TEXT PRIMARY KEY,
  sealed_key BYTES NOT NULL,
  registered TEXT NOT NULL,
  webhook_url TEXT NOT NULL,
  webhook_auth BYTES,
  webhook_secret BYTES,
  webhook_broken TEXT NOT NULL,
  updated_at BIGINT NOT NULL
)`

// ensureInboxes makes the inbox table if it is missing.
func (b *SQLBackend) ensureInboxes(ctx context.Context) error {
	s := strings.ReplaceAll(inboxSchema, " BYTES", " BLOB")
	if b.Postgres {
		s = strings.ReplaceAll(inboxSchema, " BYTES", " BYTEA")
	}
	_, err := b.DB.ExecContext(ctx, s)
	return err
}

func (b *SQLBackend) GetInbox(ctx context.Context, id string) (*Inbox, error) {
	in := &Inbox{}
	var updated int64
	err := b.DB.QueryRowContext(ctx, b.q(`SELECT connection_id, sealed_key, registered, webhook_url, webhook_auth, webhook_secret, webhook_broken, updated_at
FROM mcp_inboxes WHERE connection_id=?`), id).
		Scan(&in.ConnectionID, &in.SealedKey, &in.Registered, &in.WebhookURL, &in.SealedAuth, &in.SealedSecret, &in.Broken, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.UpdatedAt = fromMS(updated)
	return in, nil
}

func (b *SQLBackend) PutInbox(ctx context.Context, in *Inbox) error {
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_inboxes(connection_id, sealed_key, registered, webhook_url, webhook_auth, webhook_secret, webhook_broken, updated_at)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(connection_id) DO UPDATE SET sealed_key=excluded.sealed_key, registered=excluded.registered,
webhook_url=excluded.webhook_url, webhook_auth=excluded.webhook_auth, webhook_secret=excluded.webhook_secret, webhook_broken=excluded.webhook_broken,
updated_at=excluded.updated_at`),
		in.ConnectionID, in.SealedKey, in.Registered, in.WebhookURL, in.SealedAuth, in.SealedSecret, in.Broken, ms(in.UpdatedAt))
	return err
}

func (b *SQLBackend) DeleteInbox(ctx context.Context, id string) error {
	_, err := b.DB.ExecContext(ctx, b.q(`DELETE FROM mcp_inboxes WHERE connection_id=?`), id)
	return err
}

// memoryInboxes is the InboxBackend of a Backend without one.
type memoryInboxes struct {
	mu sync.Mutex
	m  map[string]*Inbox
}

func (m *memoryInboxes) GetInbox(_ context.Context, id string) (*Inbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in := m.m[id]; in != nil {
		c := *in
		return &c, nil
	}
	return nil, nil
}

func (m *memoryInboxes) PutInbox(_ context.Context, in *Inbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]*Inbox{}
	}
	c := *in
	m.m[in.ConnectionID] = &c
	return nil
}

func (m *memoryInboxes) DeleteInbox(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, id)
	return nil
}

func inboxAAD(connectionID, what string) []byte {
	return []byte("hermesh-mcp-inbox-v1\x00" + what + "\x00" + connectionID)
}

// inbox returns a connection's inbox, made on first use.
func (g *Gateway) inbox(ctx context.Context, c *Connection) (*Inbox, []byte, error) {
	in, err := g.inboxes.GetInbox(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	if in == nil {
		priv, _, err := identity.NewInboxKey()
		if err != nil {
			return nil, nil, err
		}
		sealed, err := g.cfg.Vault.Seal(priv, inboxAAD(c.ID, "key"))
		if err != nil {
			return nil, nil, err
		}
		in = &Inbox{ConnectionID: c.ID, SealedKey: sealed, UpdatedAt: g.cfg.Now()}
		if err := g.inboxes.PutInbox(ctx, in); err != nil {
			return nil, nil, err
		}
		return in, priv, nil
	}
	priv, err := g.cfg.Vault.Open(in.SealedKey, inboxAAD(c.ID, "key"))
	if err != nil {
		return nil, nil, fmt.Errorf("open the inbox key: %w", err)
	}
	return in, priv, nil
}

// takes is how the inbox takes its messages.
func (in *Inbox) takes() string {
	if in.WebhookURL != "" && in.Broken == "" {
		return "webhook"
	}
	return "poll"
}

// inboxRegistered records what the post office was told.
func (g *Gateway) inboxRegistered(id string) func(string) {
	return func(takes string) {
		ctx := context.Background()
		in, err := g.inboxes.GetInbox(ctx, id)
		if err != nil || in == nil {
			return
		}
		in.Registered = takes
		g.inboxes.PutInbox(ctx, in)
	}
}

// hermeshClientID is the Hermesh client id of a connection (its grant's).
func hermeshClientID(c *Connection) string {
	sg := &identityv1.Signed{}
	g := &identityv1.Grant{}
	if proto.Unmarshal(c.Grant, sg) != nil || identity.Unverified(sg, g) != nil {
		return ""
	}
	return g.ClientId
}

// ---------------------------------------------------------------------------
// The doorbell.

// ringer coalesces rings: at most one a minute per connection, and again
// after 15 minutes and an hour while messages still wait.
type ringer struct {
	mu    sync.Mutex
	state map[string]*ringState
}

type ringState struct {
	last    time.Time
	pending bool // a ring waits for the minute to pass
	again   int  // rings since the last new message
}

var ringAgain = []time.Duration{15 * time.Minute, time.Hour}

// onMessage hears of every message the post office takes in.
func (g *Gateway) onMessage(ownerID, inbox string) {
	if !strings.HasPrefix(inbox, "c_") {
		return
	}
	ctx := context.Background()
	conns, err := g.db.ListConnections(ctx, ConnectionFilter{OwnerID: ownerID, States: []string{StateActive}})
	if err != nil {
		return
	}
	for _, c := range conns {
		if hermeshClientID(c) == inbox {
			g.ringSoon(c.ID, true)
		}
	}
}

// ringSoon rings a connection's webhook now, or once a minute has passed
// since its last ring.
func (g *Gateway) ringSoon(id string, fresh bool) {
	r := &g.rings
	r.mu.Lock()
	if r.state == nil {
		r.state = map[string]*ringState{}
	}
	st := r.state[id]
	if st == nil {
		st = &ringState{}
		r.state[id] = st
	}
	if fresh {
		st.again = 0
	}
	if st.pending {
		r.mu.Unlock()
		return
	}
	wait := time.Minute - g.cfg.Now().Sub(st.last)
	st.pending = true
	r.mu.Unlock()
	if wait < 0 {
		wait = 0
	}
	time.AfterFunc(wait, func() {
		r.mu.Lock()
		st.pending, st.last = false, g.cfg.Now()
		r.mu.Unlock()
		g.ring(id)
	})
}

// ringAgainDue rings the webhooks of connections whose messages still wait
// 15 minutes, then an hour, after their last ring (the gateway's loop).
func (g *Gateway) ringAgainDue() {
	r := &g.rings
	now := g.cfg.Now()
	var due []string
	r.mu.Lock()
	for id, st := range r.state {
		if !st.pending && st.again < len(ringAgain) && now.Sub(st.last) >= ringAgain[st.again] {
			st.again++
			due = append(due, id)
		}
	}
	r.mu.Unlock()
	for _, id := range due {
		g.ringSoon(id, false)
	}
}

// ringBody is what a ring says, the same every time: written by Hermesh,
// with nothing a message says.
type ringBody struct {
	Type    string `json:"type"`
	Inbox   string `json:"inbox"`
	Waiting int    `json:"waiting"`
	Text    string `json:"text"`
}

const ringText = "Messages are waiting for you in Hermesh: call message_list, handle each, then message_ack it."

// ring posts to a connection's webhook if messages wait for it.
func (g *Gateway) ring(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := g.db.GetConnection(ctx, id)
	if err != nil || c == nil || c.State != StateActive {
		return
	}
	in, err := g.inboxes.GetInbox(ctx, id)
	if err != nil || in == nil || in.WebhookURL == "" || in.Broken != "" {
		return
	}
	inbox := hermeshClientID(c)
	waiting := g.cfg.Relay.InboxWaiting(c.OwnerID, inbox)
	if waiting == 0 {
		g.rings.mu.Lock()
		delete(g.rings.state, id)
		g.rings.mu.Unlock()
		return
	}
	body, _ := json.Marshal(ringBody{Type: "hermesh.message", Inbox: inbox, Waiting: waiting, Text: ringText})
	secret, err := g.cfg.Vault.Open(in.SealedSecret, inboxAAD(id, "secret"))
	if err != nil {
		return
	}
	var auth []byte
	if in.SealedAuth != nil {
		if auth, err = g.cfg.Vault.Open(in.SealedAuth, inboxAAD(id, "auth")); err != nil {
			return
		}
	}
	for attempt := range 3 {
		code, err := g.post(ctx, in.WebhookURL, body, secret, auth)
		switch {
		case err == nil && code < 300:
			return
		case err == nil && code >= 400 && code < 500:
			g.webhookBroken(id, fmt.Sprintf("the webhook answered %d", code))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	g.log.Warn("mcp: a webhook did not answer", "connection", id)
}

// post sends one ring, signed as Standard Webhooks.
func (g *Gateway) post(ctx context.Context, target string, body, secret, auth []byte) (int, error) {
	msgID := "ring_" + strings.ToLower(ulid.Make().String())
	ts := strconv.FormatInt(g.cfg.Now().Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msgID + "." + ts + "." + string(body)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", msgID)
	req.Header.Set("webhook-timestamp", ts)
	req.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if auth != nil {
		req.Header.Set("Authorization", string(auth))
	}
	res, err := g.cfg.WebhookClient.Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}

func (g *Gateway) webhookBroken(id, why string) {
	ctx := context.Background()
	in, err := g.inboxes.GetInbox(ctx, id)
	if err != nil || in == nil {
		return
	}
	in.Broken, in.UpdatedAt = why, g.cfg.Now()
	g.inboxes.PutInbox(ctx, in)
	g.log.Info("mcp: webhook broken", "connection", id, "why", why)
}

// checkWebhookURL accepts https URLs on public hosts (http, and any host,
// only with a webhook client that may reach private addresses: tests).
func (g *Gateway) checkWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return errors.New("not a URL a webhook can have")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || isSafeTransport(g.cfg.WebhookClient)) {
		return errors.New("a webhook URL is https")
	}
	return nil
}

// ---------------------------------------------------------------------------
// The message_notify tool.

const notifyDescription = `Register the webhook Hermesh rings when messages arrive in this connection's inbox, so you don't have to poll message_list. Each ring is a POST whose body says only how many messages wait and to call message_list: never what they say. Hermesh rings at most once a minute, again after 15 minutes and an hour while messages still wait, and stops when the webhook answers 4xx (hermesh_status then says so; register it again to resume). Pass authorization if the webhook wants one, as the whole header value (for example "Bearer <key>"). Rings are signed as Standard Webhooks (webhook-id, webhook-timestamp, webhook-signature) with the secret this tool returns. Pass an empty url to stop ringing. For a Grok Bot routine: create a routine whose instruction is to call message_list, handle each message (report results to your user, answer questions), then message_ack it; add a Webhook trigger; call this tool with its URL and key.`

func (g *Gateway) notifyTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c, err := g.connectionOf(ctx, req)
	if err != nil {
		return docResult(failure(1, "internal", err.Error(), "", true)), nil
	}
	if c.State != StateActive {
		return docResult(g.notReady(c, "message_notify")), nil
	}
	var args struct {
		URL           string `json:"url"`
		Authorization string `json:"authorization"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return docResult(failure(2, "usage", "the arguments are not a JSON object", "", false)), nil
		}
	}
	in, _, err := g.inbox(ctx, c)
	if err != nil {
		return docResult(failure(1, "internal", err.Error(), "", true)), nil
	}
	out := map[string]any{"ok": true, "exit_code": 0, "error": nil}
	if args.URL == "" {
		in.WebhookURL, in.SealedAuth, in.SealedSecret, in.Broken = "", nil, nil, ""
		out["webhook"] = nil
	} else {
		if err := g.checkWebhookURL(args.URL); err != nil {
			return docResult(failure(2, "usage", err.Error(), "", false)), nil
		}
		secret := make([]byte, 24)
		rand.Read(secret)
		if in.SealedSecret, err = g.cfg.Vault.Seal(secret, inboxAAD(c.ID, "secret")); err != nil {
			return docResult(failure(1, "internal", err.Error(), "", true)), nil
		}
		in.SealedAuth = nil
		if args.Authorization != "" {
			if len(args.Authorization) > 4096 || strings.ContainsAny(args.Authorization, "\r\n") {
				return docResult(failure(2, "usage", "authorization is one header value", "", false)), nil
			}
			if in.SealedAuth, err = g.cfg.Vault.Seal([]byte(args.Authorization), inboxAAD(c.ID, "auth")); err != nil {
				return docResult(failure(1, "internal", err.Error(), "", true)), nil
			}
		}
		in.WebhookURL, in.Broken = args.URL, ""
		out["webhook"] = map[string]any{"url": args.URL, "signing_secret": "whsec_" + base64.StdEncoding.EncodeToString(secret)}
	}
	in.UpdatedAt = g.cfg.Now()
	if err := g.inboxes.PutInbox(ctx, in); err != nil {
		return docResult(failure(1, "internal", err.Error(), "", true)), nil
	}
	if in.WebhookURL != "" && g.cfg.Relay.InboxWaiting(c.OwnerID, hermeshClientID(c)) > 0 {
		g.ringSoon(c.ID, true)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return docResult(b), nil
}

// doorbellStatus is what hermesh_status says of the webhook.
func (g *Gateway) doorbellStatus(ctx context.Context, c *Connection) map[string]any {
	in, err := g.inboxes.GetInbox(ctx, c.ID)
	if err != nil || in == nil || in.WebhookURL == "" {
		return map[string]any{"webhook": false, "waiting": g.cfg.Relay.InboxWaiting(c.OwnerID, hermeshClientID(c))}
	}
	host := in.WebhookURL
	if u, err := url.Parse(in.WebhookURL); err == nil {
		host = u.Host
	}
	return map[string]any{"webhook": true, "host": host, "broken": in.Broken, "waiting": g.cfg.Relay.InboxWaiting(c.OwnerID, hermeshClientID(c))}
}
