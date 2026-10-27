package mcpgw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A connection's inbox (docs/agent-messages.md §6.3): the gateway holds its
// key, as it holds the connection's client key, and opens its messages for
// the message tools. Its doorbells are in doorbells.go.

// Inbox is a connection's inbox at the gateway.
type Inbox struct {
	ConnectionID string
	// SealedKey is the inbox's X25519 private key, sealed by the Vault.
	SealedKey []byte
	// Registered is how the post office was last told it takes messages;
	// "" before it was told.
	Registered string
	UpdatedAt  time.Time
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
  updated_at BIGINT NOT NULL
)`

// ensureInboxes makes the inbox and doorbell tables if they are missing.
func (b *SQLBackend) ensureInboxes(ctx context.Context) error {
	for _, schema := range []string{inboxSchema, doorbellSchema, wakeLinkSchema} {
		s := strings.ReplaceAll(schema, " BYTES", " BLOB")
		if b.Postgres {
			s = strings.ReplaceAll(schema, " BYTES", " BYTEA")
		}
		if _, err := b.DB.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (b *SQLBackend) GetInbox(ctx context.Context, id string) (*Inbox, error) {
	in := &Inbox{}
	var updated int64
	err := b.DB.QueryRowContext(ctx, b.q(`SELECT connection_id, sealed_key, registered, updated_at FROM mcp_inboxes WHERE connection_id=?`), id).
		Scan(&in.ConnectionID, &in.SealedKey, &in.Registered, &updated)
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
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_inboxes(connection_id, sealed_key, registered, updated_at)
VALUES(?,?,?,?) ON CONFLICT(connection_id) DO UPDATE SET sealed_key=excluded.sealed_key, registered=excluded.registered, updated_at=excluded.updated_at`),
		in.ConnectionID, in.SealedKey, in.Registered, ms(in.UpdatedAt))
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

// takes is how the inbox takes its messages: the assistant asks for them,
// and its doorbells, if any, say so to senders by address (Woken).
func (in *Inbox) takes() string {
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
