package mcpgw

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// What an account sees of its MCP connections, and what it can do with
// them: the console lists them, sends a waiting approval to the Mac again,
// and disconnects them.

// ConnectionInfo is one MCP connection as its account sees it.
type ConnectionInfo struct {
	ID string
	// Name is the app's name ("Claude"), ClientHost where its metadata
	// document lives ("claude.ai"; "" for a registered client).
	Name       string
	ClientHost string
	Verified   bool
	State      string
	// PairingCode is the code of the approval request waiting on the Mac.
	PairingCode string
	// GrantID once approved: the console's list of agents has it too.
	GrantID   string
	CreatedAt time.Time
	LastUsed  time.Time
}

// ErrNoConnection: no such live connection for this account.
var ErrNoConnection = errors.New("no such connection")

// Connections lists an account's live MCP connections, newest first.
func (g *Gateway) Connections(ctx context.Context, accountID string) ([]ConnectionInfo, error) {
	conns, err := g.db.ListConnections(ctx, ConnectionFilter{AccountID: accountID, States: []string{StateAwaitingSetup, StateAwaitingApproval, StateActive}})
	if err != nil {
		return nil, err
	}
	out := make([]ConnectionInfo, 0, len(conns))
	for i := len(conns) - 1; i >= 0; i-- {
		c := conns[i]
		// One just approved on the Mac shows as approved, not as waiting next
		// to its new grant.
		if c.State != StateActive {
			if cur, err := g.advance(ctx, c.ID); err == nil && cur != nil {
				if !cur.Live() {
					continue
				}
				c = cur
			}
		}
		info := ConnectionInfo{ID: c.ID, Name: c.ClientName, State: c.State, PairingCode: c.PairingCode, GrantID: c.GrantID, CreatedAt: c.CreatedAt, LastUsed: c.LastUsed}
		if cl, err := g.db.GetClient(ctx, c.ClientID); err == nil && cl != nil {
			info.Name, info.Verified = cl.Name, cl.Verified
			if u, err := url.Parse(cl.ID); err == nil && cl.Source == "cimd" {
				info.ClientHost = u.Hostname()
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// owned returns an account's live connection.
func (g *Gateway) owned(ctx context.Context, accountID, id string) (*Connection, error) {
	c, err := g.db.GetConnection(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil || c.AccountID != accountID || !c.Live() {
		return nil, ErrNoConnection
	}
	return c, nil
}

// Disconnect ends an account's connection: its tokens stop working, its
// key is destroyed and its grant revoked.
func (g *Gateway) Disconnect(ctx context.Context, accountID, id string) error {
	if _, err := g.owned(ctx, accountID, id); err != nil {
		return err
	}
	return g.endConnection(ctx, id, StateRevoked, "disconnected in the console")
}

// ResendApproval sends a waiting connection's approval request to the Mac
// again, now (or once it is online): the person asked.
func (g *Gateway) ResendApproval(ctx context.Context, accountID, id string) error {
	c, err := g.owned(ctx, accountID, id)
	if err != nil {
		return err
	}
	if c.State != StateAwaitingApproval {
		return nil
	}
	l := g.connLock(id)
	l.Lock()
	c, err = g.db.GetConnection(ctx, id)
	if err == nil && c != nil && c.State == StateAwaitingApproval {
		c.PairingCode, c.PairingExpires, c.Asked = "", time.Time{}, true
		err = g.db.PutConnection(ctx, c)
	}
	l.Unlock()
	if err != nil {
		return err
	}
	_, err = g.advance(ctx, id)
	return err
}
