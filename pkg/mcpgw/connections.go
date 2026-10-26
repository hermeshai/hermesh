package mcpgw

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/cli"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Connections: made on the consent page, they wait for Hermesh to be set up
// on a Mac and for the owner's approval there, as long as the app keeps
// using them, then act with their grant. See docs/mcp-gateway.md §5.7.

type cachedKey struct {
	key *identity.SoftwareKey
	exp time.Time
}

// errTooMany: the owner (or account) has as many connections as allowed.
var errTooMany = errors.New("too many connected apps: disconnect one in Hermesh first")

// createConnection makes the connection an authorization's consent asked
// for, with a new client key.
func (g *Gateway) createConnection(ctx context.Context, a *Authorization, acct Account, c *Client, ua string) (*Connection, error) {
	filter := ConnectionFilter{AccountID: acct.ID, States: []string{StateAwaitingSetup, StateAwaitingApproval, StateActive}}
	if acct.OwnerID != "" {
		filter = ConnectionFilter{OwnerID: acct.OwnerID, States: filter.States}
	}
	live, err := g.db.ListConnections(ctx, filter)
	if err != nil {
		return nil, err
	}
	// A connection of the same app that was never approved is replaced
	// (the app asked again); an approved one stays until this one is.
	n := 0
	for _, old := range live {
		if old.AccountID == acct.ID && old.ClientID == c.ID && old.State != StateActive {
			g.endConnection(ctx, old.ID, StateRevoked, "replaced by a new connection")
			continue
		}
		n++
	}
	if n >= maxConnections {
		return nil, errTooMany
	}
	key, err := identity.GenerateKey()
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key.Key)
	if err != nil {
		return nil, err
	}
	now := g.cfg.Now()
	conn := &Connection{ID: "mc_" + strings.ToLower(ulid.Make().String()), AccountID: acct.ID, AccountHint: acct.Hint, OwnerID: acct.OwnerID, ClientID: c.ID,
		ClientName: label(c.Name, a.RedirectURI), State: StateAwaitingSetup, ConsentUA: clip(ua, 200), CreatedAt: now, Asked: true}
	if acct.OwnerID != "" {
		conn.State = StateAwaitingApproval
	}
	if conn.SealedKey, err = g.cfg.Vault.Seal(der, keyAAD(conn.ID)); err != nil {
		return nil, err
	}
	if err := g.db.PutConnection(ctx, conn); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.keys[conn.ID] = cachedKey{key: key, exp: now.Add(keyCacheTTL)}
	g.mu.Unlock()
	c.LastUsed = now
	g.db.PutClient(ctx, c)
	g.log.Info("mcp: connected", "connection", conn.ID, "client", c.ID, "state", conn.State)
	return g.advance(ctx, conn.ID)
}

// label names a connection for the owner: the app and where it returns to,
// "Claude (claude.ai)", at most 64 bytes (the relay's limit).
func label(name, redirectURI string) string {
	host := redirectURI
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	if h, _, ok := strings.Cut(host, ":"); ok && !strings.HasPrefix(host, "[") {
		host = h
	}
	s := name + " (" + host + ")"
	for len(s) > 64 {
		r := []rune(name)
		if len(r) <= 1 {
			return s[:64]
		}
		name = string(r[:len(r)-1])
		s = name + "… (" + host + ")"
	}
	return s
}

func keyAAD(connectionID string) []byte { return []byte("hermesh-mcp-key-v1\x00" + connectionID) }

// key is a connection's client private key.
func (g *Gateway) key(c *Connection) (*identity.SoftwareKey, error) {
	now := g.cfg.Now()
	g.mu.Lock()
	ck, ok := g.keys[c.ID]
	g.mu.Unlock()
	if ok && now.Before(ck.exp) {
		return ck.key, nil
	}
	if c.SealedKey == nil {
		return nil, errors.New("the connection's key is gone")
	}
	der, err := g.cfg.Vault.Open(c.SealedKey, keyAAD(c.ID))
	if err != nil {
		return nil, fmt.Errorf("open the connection's key: %w", err)
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the connection's key is not ECDSA")
	}
	key := &identity.SoftwareKey{Key: ek}
	g.mu.Lock()
	g.keys[c.ID] = cachedKey{key: key, exp: now.Add(keyCacheTTL)}
	g.mu.Unlock()
	return key, nil
}

// advance moves a connection as far as it can go now, and returns it.
func (g *Gateway) advance(ctx context.Context, id string) (*Connection, error) {
	return g.move(ctx, id, false)
}

// ask is advance for a use of the connection: while it waits for approval,
// an approval request goes to the owner's Mac (now, or once it is online).
// Requests are sent only when the app is used, never on a timer.
func (g *Gateway) ask(ctx context.Context, id string) (*Connection, error) {
	return g.move(ctx, id, true)
}

func (g *Gateway) move(ctx context.Context, id string, ask bool) (*Connection, error) {
	l := g.connLock(id)
	l.Lock()
	defer l.Unlock()
	c, err := g.db.GetConnection(ctx, id)
	if err != nil || c == nil {
		return c, err
	}
	if !c.Live() || c.State == StateActive {
		return c, nil
	}
	now := g.cfg.Now()
	// Unused as long as its tokens last: the app is gone.
	if now.Sub(lastUse(c)) > refreshIdle {
		return g.end(ctx, c, StateExpired, "not used in 30 days")
	}
	before := *c
	if ask {
		c.Asked = true
	}
	if c.State == StateAwaitingSetup {
		owner, err := g.cfg.Accounts.Owner(ctx, c.AccountID)
		if err != nil {
			return nil, err
		}
		if owner != "" {
			c.OwnerID, c.State = owner, StateAwaitingApproval
		}
	}
	if c.State == StateAwaitingApproval {
		if err := g.checkApproval(ctx, c); err != nil {
			g.log.Warn("mcp: approval", "connection", c.ID, "err", err)
		}
	}
	if c.State == StateAwaitingApproval && c.Asked && c.PairingCode == "" && g.cfg.Relay.OwnerOnline(c.OwnerID) {
		if err := g.requestApproval(c); err != nil {
			g.log.Warn("mcp: send the approval request", "connection", c.ID, "err", err)
		} else {
			c.Asked = false
		}
	}
	if c.State == before.State && c.PairingCode == before.PairingCode && c.Asked == before.Asked && c.OwnerID == before.OwnerID {
		return c, nil
	}
	if err := g.db.PutConnection(ctx, c); err != nil {
		return nil, err
	}
	if c.State == StateActive {
		g.replaceOlder(ctx, c)
	}
	return c, nil
}

// lastUse is when the connection was last used (or made).
func lastUse(c *Connection) time.Time {
	if c.LastUsed.After(c.CreatedAt) {
		return c.LastUsed
	}
	return c.CreatedAt
}

// requestApproval sends the owner's Mac a pairing request for the
// connection's key, naming this server as the key's custodian.
func (g *Gateway) requestApproval(c *Connection) error {
	key, err := g.key(c)
	if err != nil {
		return err
	}
	pr := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_CLIENT, SubjectPublicKey: identity.MarshalPublicKey(key.Public()),
		Label: c.ClientName, Hostname: g.issuer.Hostname(), Platform: "mcp", OwnerId: c.OwnerID, CreatedAt: timestamppb.Now(),
		Custodian: g.issuer.String()}
	signed, err := identity.Sign(pr, key)
	if err != nil {
		return err
	}
	code, exp, err := g.cfg.Relay.SubmitCustodialPairing(signed)
	if err != nil {
		return err
	}
	c.PairingCode, c.PairingExpires = code, exp
	g.log.Info("mcp: approval requested", "connection", c.ID, "code", code)
	return nil
}

// checkApproval picks up the owner's answer to the pairing request.
func (g *Gateway) checkApproval(ctx context.Context, c *Connection) error {
	if c.PairingCode == "" {
		return nil
	}
	cred, expired, err := g.cfg.Relay.PairingResult(c.PairingCode)
	if err != nil {
		return err
	}
	if expired {
		c.PairingCode, c.PairingExpires = "", time.Time{}
		return nil
	}
	if cred == nil {
		return nil
	}
	key, err := g.key(c)
	if err != nil {
		return err
	}
	rc, err := g.cfg.Relay.RootChain(c.OwnerID)
	if err != nil {
		return err
	}
	trust := identity.NewTrust(c.OwnerID)
	if _, err := trust.AcceptRootChain(rc); err != nil {
		return err
	}
	grant, pub, err := trust.VerifyGrant(cred, g.cfg.Now())
	if err != nil {
		return fmt.Errorf("the approved grant: %w", err)
	}
	if !pub.Equal(key.Public()) {
		return errors.New("the approved grant is for another key")
	}
	c.Grant, _ = proto.Marshal(cred)
	c.RootChain, _ = proto.Marshal(trust.RootChain())
	c.GrantID, c.State, c.ApprovedAt = grant.GrantId, StateActive, g.cfg.Now()
	c.PairingCode, c.PairingExpires = "", time.Time{}
	g.log.Info("mcp: approved", "connection", c.ID, "grant", grant.GrantId)
	return nil
}

// replaceOlder ends the account's other connections of the same app once
// a new one is approved: the app reconnected.
func (g *Gateway) replaceOlder(ctx context.Context, c *Connection) {
	conns, err := g.db.ListConnections(ctx, ConnectionFilter{AccountID: c.AccountID, States: []string{StateActive}})
	if err != nil {
		return
	}
	for _, old := range conns {
		if old.ID != c.ID && old.ClientID == c.ClientID {
			g.endConnection(ctx, old.ID, StateRevoked, "replaced by a new connection")
		}
	}
}

// endConnection ends a connection: its tokens stop working, its key is
// destroyed and its grant revoked.
func (g *Gateway) endConnection(ctx context.Context, id, state, reason string) error {
	l := g.connLock(id)
	l.Lock()
	defer l.Unlock()
	c, err := g.db.GetConnection(ctx, id)
	if err != nil || c == nil {
		return err
	}
	_, err = g.end(ctx, c, state, reason)
	return err
}

// end is endConnection with the connection's lock held.
func (g *Gateway) end(ctx context.Context, c *Connection, state, reason string) (*Connection, error) {
	if !c.Live() {
		return c, nil
	}
	now := g.cfg.Now()
	if err := g.db.RevokeTokens(ctx, c.ID, "", now); err != nil {
		return nil, err
	}
	if c.GrantID != "" && c.OwnerID != "" {
		if err := g.cfg.Relay.ApplyRevocations(c.OwnerID, &identityv1.RevocationList{RevokedGrantIds: []string{c.GrantID}}); err != nil {
			g.log.Warn("mcp: revoke the grant", "connection", c.ID, "err", err)
		}
	}
	c.State, c.EndedAt, c.EndReason = state, now, reason
	c.SealedKey, c.PairingCode = nil, ""
	if err := g.db.PutConnection(ctx, c); err != nil {
		return nil, err
	}
	g.mu.Lock()
	delete(g.keys, c.ID)
	g.mu.Unlock()
	if err := g.inboxes.DeleteInbox(ctx, c.ID); err != nil {
		g.log.Warn("mcp: delete the inbox", "connection", c.ID, "err", err)
	}
	g.log.Info("mcp: connection ended", "connection", c.ID, "state", state, "reason", reason)
	return c, nil
}

// grantExpiry is when a connection's grant expires.
func (g *Gateway) grantExpiry(c *Connection) (time.Time, bool) {
	s := &identityv1.Signed{}
	gr := &identityv1.Grant{}
	if proto.Unmarshal(c.Grant, s) != nil || identity.Unverified(s, gr) != nil || gr.ExpiresAt == nil {
		return time.Time{}, false
	}
	return gr.ExpiresAt.AsTime(), true
}

// identity is the client identity CLI commands run with.
func (g *Gateway) identity(c *Connection) (cli.Identity, error) {
	key, err := g.key(c)
	if err != nil {
		return cli.Identity{}, err
	}
	rc := &identityv1.RootChain{}
	grant := &identityv1.Signed{}
	if err := proto.Unmarshal(c.RootChain, rc); err != nil {
		return cli.Identity{}, err
	}
	if err := proto.Unmarshal(c.Grant, grant); err != nil {
		return cli.Identity{}, err
	}
	id := cli.Identity{RelayURL: relayURL, OwnerID: c.OwnerID, Key: key, RootChain: rc, Grant: grant}
	if in, priv, err := g.inbox(context.Background(), c); err == nil {
		id.InboxKey, id.TakesMessages, id.InboxRegistered = priv, in.takes(), in.Registered
	} else {
		g.log.Warn("mcp: the connection's inbox", "connection", c.ID, "err", err)
	}
	return id, nil
}

// adopted stores the grant (or root chain) a command picked up.
func (g *Gateway) adopted(id string) func(*identityv1.RootChain, *identityv1.Signed) {
	return func(rc *identityv1.RootChain, grant *identityv1.Signed) {
		ctx := context.Background()
		l := g.connLock(id)
		l.Lock()
		defer l.Unlock()
		c, err := g.db.GetConnection(ctx, id)
		if err != nil || c == nil || c.State != StateActive {
			return
		}
		c.RootChain, _ = proto.Marshal(rc)
		c.Grant, _ = proto.Marshal(grant)
		g.db.PutConnection(ctx, c)
	}
}

// touch records that a connection was used (at most once a minute).
func (g *Gateway) touch(ctx context.Context, c *Connection) {
	now := g.cfg.Now()
	if now.Sub(c.LastUsed) < time.Minute {
		return
	}
	l := g.connLock(c.ID)
	l.Lock()
	defer l.Unlock()
	cur, err := g.db.GetConnection(ctx, c.ID)
	if err != nil || cur == nil {
		return
	}
	cur.LastUsed = now
	g.db.PutConnection(ctx, cur)
}

// ---------------------------------------------------------------------------
// What a connection waits for, in words.

// Status is a connection's state and what to do next.
type Status struct {
	State string `json:"state"`
	// Next is what the person should do, "" when nothing.
	Next string `json:"next"`
	// PairingCode is the code the owner's Mac shows with the request, while
	// one is waiting there.
	PairingCode string `json:"pairing_code"`
	SetupURL    string `json:"setup_url"`
	// OwnerOnline: one of the owner's devices is connected.
	OwnerOnline bool `json:"owner_online"`
}

func (g *Gateway) status(c *Connection) Status {
	hint := c.AccountHint
	s := Status{State: c.State, SetupURL: g.cfg.SetupURL, PairingCode: c.PairingCode}
	if c.OwnerID != "" {
		s.OwnerOnline = g.cfg.Relay.OwnerOnline(c.OwnerID)
	}
	name := c.ClientName
	if i := strings.LastIndex(name, " ("); i > 0 {
		name = name[:i]
	}
	switch c.State {
	case StateAwaitingSetup:
		who := "with the same account"
		if hint != "" {
			who = "with the same account (" + hint + ")"
		}
		if g.selfHosted() {
			who = "with this server (" + g.issuer.Host + ")"
		}
		s.Next = fmt.Sprintf("Hermesh isn't set up yet. Install Hermesh for Mac from %s, set it up %s, and approve “%s” when it asks. "+
			"Then try again; there is no need to reconnect.", g.cfg.SetupURL, who, name)
	case StateAwaitingApproval:
		switch {
		case c.PairingCode != "":
			s.Next = fmt.Sprintf("Approve “%s” in Hermesh on your Mac: the request is waiting there now. Then try again.", name)
		case s.OwnerOnline:
			s.Next = fmt.Sprintf("Approve “%s” in Hermesh on your Mac: open Hermesh there to see the request, or disconnect and connect the app again.", name)
		default:
			s.Next = fmt.Sprintf("Open Hermesh on your Mac and approve “%s” there: the request goes to your Mac as soon as it is online. Then try again.", name)
		}
	case StateRevoked:
		s.Next = "This connection was disconnected (" + c.EndReason + "). Connect the app to Hermesh again to ask the owner again."
	case StateExpired:
		s.Next = "This connection ended (" + c.EndReason + "). Connect the app to Hermesh again."
	}
	return s
}

// selfHosted: a self-hosted relay serves its one owner, with no accounts.
func (g *Gateway) selfHosted() bool {
	_, ok := g.cfg.Accounts.(SingleOwner)
	return ok
}
