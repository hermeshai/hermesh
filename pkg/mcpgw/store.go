package mcpgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Client is an OAuth client: an agent product, known by its Client ID
// Metadata Document (its client_id is that document's URL) or registered
// dynamically.
type Client struct {
	ID string
	// Source is "cimd" or "dcr".
	Source       string
	Name         string
	URI          string
	RedirectURIs []string
	Verified     bool
	// CacheUntil is when a CIMD document is fetched again.
	CacheUntil time.Time
	CreatedAt  time.Time
	LastUsed   time.Time
}

// Authorization is one run of the authorization endpoint, from the agent
// product's request to the code it gets back.
type Authorization struct {
	// IDHash is the hash of the id the browser holds.
	IDHash        []byte
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Resource      string
	AccountID     string
	ConnectionID  string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// Connection states.
const (
	// The account has no owner yet: Hermesh isn't set up on a Mac.
	StateAwaitingSetup = "awaiting_setup"
	// The owner hasn't approved the connection on a Mac yet.
	StateAwaitingApproval = "awaiting_approval"
	// Approved: the connection has a grant.
	StateActive  = "active"
	StateRevoked = "revoked"
	StateExpired = "expired"
)

// Connection is one (account, agent product) pair: a Hermesh client
// identity whose key the gateway holds, and, once the owner approved it,
// its grant.
type Connection struct {
	ID        string
	AccountID string
	// AccountHint names the account, masked, in what the app is told.
	AccountHint string
	OwnerID     string
	ClientID    string
	// ClientName is the connection's label: the app and where it returns
	// to, "Claude (claude.ai)".
	ClientName string
	State      string
	// SealedKey is the client's private key, sealed by the Vault; nil once
	// the connection ended.
	SealedKey []byte
	// GrantID, Grant (a Signed Grant) and RootChain once approved.
	GrantID   string
	Grant     []byte
	RootChain []byte
	// The pairing request waiting for the owner, if any.
	PairingCode    string
	PairingExpires time.Time
	// Asked: the app was used while waiting for approval, so an approval
	// request goes to the owner's Mac once it is online (stored in the
	// column requests, 1 or 0; requests_day is no longer used).
	Asked      bool
	ConsentUA  string
	CreatedAt  time.Time
	ApprovedAt time.Time
	LastUsed   time.Time
	EndedAt    time.Time
	EndReason  string
}

// Live reports whether the connection has not ended.
func (c *Connection) Live() bool {
	return c.State == StateAwaitingSetup || c.State == StateAwaitingApproval || c.State == StateActive
}

// Code is an authorization code.
type Code struct {
	Hash          []byte
	ConnectionID  string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	ExpiresAt     time.Time
	UsedAt        time.Time
}

// Token kinds.
const (
	KindAccess  = "access"
	KindRefresh = "refresh"
)

// Token is an access or refresh token, known by its hash.
type Token struct {
	Hash         []byte
	Kind         string
	ConnectionID string
	ClientID     string
	// FamilyID groups the tokens issued from one authorization code.
	FamilyID  string
	CreatedAt time.Time
	ExpiresAt time.Time
	RotatedAt time.Time
	RevokedAt time.Time
}

// ConnectionFilter selects connections; empty fields match all.
type ConnectionFilter struct {
	AccountID string
	OwnerID   string
	States    []string
}

// Backend stores the gateway's state.
type Backend interface {
	GetClient(ctx context.Context, id string) (*Client, error)
	PutClient(ctx context.Context, c *Client) error

	GetAuthorization(ctx context.Context, idHash []byte) (*Authorization, error)
	PutAuthorization(ctx context.Context, a *Authorization) error

	GetConnection(ctx context.Context, id string) (*Connection, error)
	PutConnection(ctx context.Context, c *Connection) error
	ListConnections(ctx context.Context, f ConnectionFilter) ([]*Connection, error)

	PutCode(ctx context.Context, c *Code) error
	// UseCode marks a code used and returns it; first is false when it was
	// used before (nil, false, nil: no such code).
	UseCode(ctx context.Context, hash []byte, now time.Time) (c *Code, first bool, err error)

	GetToken(ctx context.Context, hash []byte) (*Token, error)
	PutToken(ctx context.Context, t *Token) error
	// RotateToken marks a refresh token rotated; false when it already was.
	RotateToken(ctx context.Context, hash []byte, now time.Time) (bool, error)
	// RevokeTokens revokes every token of a connection (family "") or of
	// one family.
	RevokeTokens(ctx context.Context, connectionID, familyID string, now time.Time) error

	// Sweep deletes what is no longer needed.
	Sweep(ctx context.Context, now time.Time) error
}

// SQLBackend is a Backend on database/sql, for SQLite (a self-hosted relay)
// or PostgreSQL (the hosted center). Times are stored as Unix milliseconds,
// so the same statements serve both.
type SQLBackend struct {
	DB       *sql.DB
	Postgres bool
}

var _ Backend = (*SQLBackend)(nil)

// Schema is the gateway's tables, for SQLite or PostgreSQL. Stores apply it
// as one migration: it never changes, and later changes to the tables come
// as further statements.
func Schema(postgres bool) string {
	s := `
CREATE TABLE mcp_clients (
  client_id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  name TEXT NOT NULL,
  uri TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,
  verified BOOLEAN NOT NULL,
  cache_until BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  last_used BIGINT NOT NULL
);
CREATE TABLE mcp_authorizations (
  id_hash BYTES PRIMARY KEY,
  client_id TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  state TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  resource TEXT NOT NULL,
  account_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL
);
CREATE TABLE mcp_connections (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  account_hint TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  client_id TEXT NOT NULL,
  client_name TEXT NOT NULL,
  state TEXT NOT NULL,
  sealed_key BYTES,
  grant_id TEXT NOT NULL,
  grant_signed BYTES,
  root_chain BYTES,
  pairing_code TEXT NOT NULL,
  pairing_expires BIGINT NOT NULL,
  requests_day TEXT NOT NULL,
  requests INTEGER NOT NULL,
  consent_ua TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  approved_at BIGINT NOT NULL,
  last_used BIGINT NOT NULL,
  ended_at BIGINT NOT NULL,
  end_reason TEXT NOT NULL
);
CREATE INDEX mcp_connections_account ON mcp_connections(account_id);
CREATE INDEX mcp_connections_owner ON mcp_connections(owner_id);
CREATE INDEX mcp_connections_state ON mcp_connections(state);
CREATE TABLE mcp_codes (
  hash BYTES PRIMARY KEY,
  connection_id TEXT NOT NULL,
  client_id TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  resource TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  used_at BIGINT NOT NULL
);
CREATE TABLE mcp_tokens (
  hash BYTES PRIMARY KEY,
  kind TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  client_id TEXT NOT NULL,
  family_id TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  rotated_at BIGINT NOT NULL,
  revoked_at BIGINT NOT NULL
);
CREATE INDEX mcp_tokens_connection ON mcp_tokens(connection_id);
CREATE INDEX mcp_tokens_family ON mcp_tokens(family_id);
`
	if postgres {
		return strings.ReplaceAll(s, " BYTES", " BYTEA")
	}
	return strings.ReplaceAll(s, " BYTES", " BLOB")
}

// q rewrites ? placeholders as $n for PostgreSQL.
func (b *SQLBackend) q(query string) string {
	if !b.Postgres {
		return query
	}
	var out strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func (b *SQLBackend) GetClient(ctx context.Context, id string) (*Client, error) {
	c := &Client{}
	var uris string
	var cacheUntil, created, used int64
	err := b.DB.QueryRowContext(ctx, b.q(`SELECT client_id, source, name, uri, redirect_uris, verified, cache_until, created_at, last_used FROM mcp_clients WHERE client_id=?`), id).
		Scan(&c.ID, &c.Source, &c.Name, &c.URI, &uris, &c.Verified, &cacheUntil, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, err
	}
	c.CacheUntil, c.CreatedAt, c.LastUsed = fromMS(cacheUntil), fromMS(created), fromMS(used)
	return c, nil
}

func (b *SQLBackend) PutClient(ctx context.Context, c *Client) error {
	uris, _ := json.Marshal(c.RedirectURIs)
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_clients(client_id, source, name, uri, redirect_uris, verified, cache_until, created_at, last_used)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(client_id) DO UPDATE SET source=excluded.source, name=excluded.name, uri=excluded.uri,
redirect_uris=excluded.redirect_uris, verified=excluded.verified, cache_until=excluded.cache_until, last_used=excluded.last_used`),
		c.ID, c.Source, c.Name, c.URI, string(uris), c.Verified, ms(c.CacheUntil), ms(c.CreatedAt), ms(c.LastUsed))
	return err
}

func (b *SQLBackend) GetAuthorization(ctx context.Context, idHash []byte) (*Authorization, error) {
	a := &Authorization{}
	var created, expires int64
	err := b.DB.QueryRowContext(ctx, b.q(`SELECT id_hash, client_id, redirect_uri, state, code_challenge, resource, account_id, connection_id, created_at, expires_at
FROM mcp_authorizations WHERE id_hash=?`), idHash).
		Scan(&a.IDHash, &a.ClientID, &a.RedirectURI, &a.State, &a.CodeChallenge, &a.Resource, &a.AccountID, &a.ConnectionID, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt, a.ExpiresAt = fromMS(created), fromMS(expires)
	return a, nil
}

func (b *SQLBackend) PutAuthorization(ctx context.Context, a *Authorization) error {
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_authorizations(id_hash, client_id, redirect_uri, state, code_challenge, resource, account_id, connection_id, created_at, expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id_hash) DO UPDATE SET account_id=excluded.account_id, connection_id=excluded.connection_id, expires_at=excluded.expires_at`),
		a.IDHash, a.ClientID, a.RedirectURI, a.State, a.CodeChallenge, a.Resource, a.AccountID, a.ConnectionID, ms(a.CreatedAt), ms(a.ExpiresAt))
	return err
}

const connectionColumns = `id, account_id, account_hint, owner_id, client_id, client_name, state, sealed_key, grant_id, grant_signed, root_chain, pairing_code, pairing_expires,
requests_day, requests, consent_ua, created_at, approved_at, last_used, ended_at, end_reason`

func scanConnection(row interface{ Scan(...any) error }) (*Connection, error) {
	c := &Connection{}
	var pairingExpires, created, approved, used, ended int64
	var requestsDay string
	var asked int
	err := row.Scan(&c.ID, &c.AccountID, &c.AccountHint, &c.OwnerID, &c.ClientID, &c.ClientName, &c.State, &c.SealedKey, &c.GrantID, &c.Grant, &c.RootChain,
		&c.PairingCode, &pairingExpires, &requestsDay, &asked, &c.ConsentUA, &created, &approved, &used, &ended, &c.EndReason)
	if err != nil {
		return nil, err
	}
	c.Asked = asked != 0
	c.PairingExpires, c.CreatedAt, c.ApprovedAt, c.LastUsed, c.EndedAt = fromMS(pairingExpires), fromMS(created), fromMS(approved), fromMS(used), fromMS(ended)
	return c, nil
}

func (b *SQLBackend) GetConnection(ctx context.Context, id string) (*Connection, error) {
	c, err := scanConnection(b.DB.QueryRowContext(ctx, b.q(`SELECT `+connectionColumns+` FROM mcp_connections WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (b *SQLBackend) PutConnection(ctx context.Context, c *Connection) error {
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_connections(`+connectionColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET owner_id=excluded.owner_id, client_name=excluded.client_name,
state=excluded.state, sealed_key=excluded.sealed_key, grant_id=excluded.grant_id, grant_signed=excluded.grant_signed, root_chain=excluded.root_chain,
pairing_code=excluded.pairing_code, pairing_expires=excluded.pairing_expires, requests_day=excluded.requests_day, requests=excluded.requests,
approved_at=excluded.approved_at, last_used=excluded.last_used, ended_at=excluded.ended_at, end_reason=excluded.end_reason`),
		c.ID, c.AccountID, c.AccountHint, c.OwnerID, c.ClientID, c.ClientName, c.State, c.SealedKey, c.GrantID, c.Grant, c.RootChain, c.PairingCode, ms(c.PairingExpires),
		"", boolInt(c.Asked), c.ConsentUA, ms(c.CreatedAt), ms(c.ApprovedAt), ms(c.LastUsed), ms(c.EndedAt), c.EndReason)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (b *SQLBackend) ListConnections(ctx context.Context, f ConnectionFilter) ([]*Connection, error) {
	query := `SELECT ` + connectionColumns + ` FROM mcp_connections WHERE 1=1`
	var args []any
	if f.AccountID != "" {
		query += ` AND account_id=?`
		args = append(args, f.AccountID)
	}
	if f.OwnerID != "" {
		query += ` AND owner_id=?`
		args = append(args, f.OwnerID)
	}
	if len(f.States) > 0 {
		query += ` AND state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.States)), ",") + `)`
		for _, s := range f.States {
			args = append(args, s)
		}
	}
	rows, err := b.DB.QueryContext(ctx, b.q(query+` ORDER BY created_at`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (b *SQLBackend) PutCode(ctx context.Context, c *Code) error {
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_codes(hash, connection_id, client_id, redirect_uri, code_challenge, resource, expires_at, used_at)
VALUES(?,?,?,?,?,?,?,?)`), c.Hash, c.ConnectionID, c.ClientID, c.RedirectURI, c.CodeChallenge, c.Resource, ms(c.ExpiresAt), ms(c.UsedAt))
	return err
}

func (b *SQLBackend) UseCode(ctx context.Context, hash []byte, now time.Time) (*Code, bool, error) {
	res, err := b.DB.ExecContext(ctx, b.q(`UPDATE mcp_codes SET used_at=? WHERE hash=? AND used_at=0`), ms(now), hash)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	c := &Code{}
	var expires, used int64
	err = b.DB.QueryRowContext(ctx, b.q(`SELECT hash, connection_id, client_id, redirect_uri, code_challenge, resource, expires_at, used_at FROM mcp_codes WHERE hash=?`), hash).
		Scan(&c.Hash, &c.ConnectionID, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.Resource, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c.ExpiresAt, c.UsedAt = fromMS(expires), fromMS(used)
	return c, n == 1, nil
}

func (b *SQLBackend) GetToken(ctx context.Context, hash []byte) (*Token, error) {
	t := &Token{}
	var created, expires, rotated, revoked int64
	err := b.DB.QueryRowContext(ctx, b.q(`SELECT hash, kind, connection_id, client_id, family_id, created_at, expires_at, rotated_at, revoked_at FROM mcp_tokens WHERE hash=?`), hash).
		Scan(&t.Hash, &t.Kind, &t.ConnectionID, &t.ClientID, &t.FamilyID, &created, &expires, &rotated, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt, t.ExpiresAt, t.RotatedAt, t.RevokedAt = fromMS(created), fromMS(expires), fromMS(rotated), fromMS(revoked)
	return t, nil
}

func (b *SQLBackend) PutToken(ctx context.Context, t *Token) error {
	_, err := b.DB.ExecContext(ctx, b.q(`INSERT INTO mcp_tokens(hash, kind, connection_id, client_id, family_id, created_at, expires_at, rotated_at, revoked_at)
VALUES(?,?,?,?,?,?,?,?,?)`), t.Hash, t.Kind, t.ConnectionID, t.ClientID, t.FamilyID, ms(t.CreatedAt), ms(t.ExpiresAt), ms(t.RotatedAt), ms(t.RevokedAt))
	return err
}

func (b *SQLBackend) RotateToken(ctx context.Context, hash []byte, now time.Time) (bool, error) {
	res, err := b.DB.ExecContext(ctx, b.q(`UPDATE mcp_tokens SET rotated_at=? WHERE hash=? AND rotated_at=0`), ms(now), hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (b *SQLBackend) RevokeTokens(ctx context.Context, connectionID, familyID string, now time.Time) error {
	var err error
	if familyID != "" {
		_, err = b.DB.ExecContext(ctx, b.q(`UPDATE mcp_tokens SET revoked_at=? WHERE family_id=? AND revoked_at=0`), ms(now), familyID)
	} else {
		_, err = b.DB.ExecContext(ctx, b.q(`UPDATE mcp_tokens SET revoked_at=? WHERE connection_id=? AND revoked_at=0`), ms(now), connectionID)
	}
	return err
}

func (b *SQLBackend) Sweep(ctx context.Context, now time.Time) error {
	n := ms(now)
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM mcp_authorizations WHERE expires_at < ?`, []any{n}},
		{`DELETE FROM mcp_codes WHERE expires_at < ?`, []any{n - int64(time.Hour/time.Millisecond)}},
		{`DELETE FROM mcp_tokens WHERE expires_at < ?`, []any{n}},
		// Ended connections are kept a while for the owner's list.
		{`DELETE FROM mcp_connections WHERE ended_at > 0 AND ended_at < ?`, []any{n - int64(30*24*time.Hour/time.Millisecond)}},
		// Registered clients never used for a connection.
		{`DELETE FROM mcp_clients WHERE source='dcr' AND ((last_used=0 AND created_at < ?) OR (last_used > 0 AND last_used < ?))
AND client_id NOT IN (SELECT client_id FROM mcp_connections)`,
			[]any{n - int64(7*24*time.Hour/time.Millisecond), n - int64(90*24*time.Hour/time.Millisecond)}},
	} {
		if _, err := b.DB.ExecContext(ctx, b.q(stmt.query), stmt.args...); err != nil {
			return err
		}
	}
	return nil
}
