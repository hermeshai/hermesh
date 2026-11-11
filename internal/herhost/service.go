package herhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// HerService (proto/hermesh/her/v1/her.proto) on her: each Open stream is a
// connection of its own, which Calls and the console's requests may name.

// Open opens a connection, sends its setup requests, then sends the
// `opened` event and her's notifications on it until ctx ends or the
// connection fails.
func (h *Host) Open(ctx context.Context, req *herv1.OpenRequest, send func(*herv1.OpenEvent) error) error {
	setup := make([]json.RawMessage, len(req.GetSetup()))
	for i, r := range req.GetSetup() {
		p, err := check(r.GetMethod(), r.GetParams())
		if err != nil {
			return err
		}
		setup[i] = p
	}
	c, err := h.dial(ctx, true)
	if err != nil {
		return err
	}
	id := h.hold(c)
	defer h.release(id, c)
	opened := &herv1.Opened{ConnectionId: id}
	for i, r := range req.GetSetup() {
		res, err := c.call(ctx, r.GetMethod(), setup[i])
		if err != nil {
			return h.lost(ctx, err)
		}
		opened.Responses = append(opened.Responses, res)
	}
	if err := send(&herv1.OpenEvent{Kind: &herv1.OpenEvent_Opened{Opened: opened}}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.done:
			return h.lost(ctx, c.err)
		case n := <-c.notes:
			if err := send(&herv1.OpenEvent{Kind: &herv1.OpenEvent_Notification{Notification: &herv1.Notification{Method: n.method, Params: n.params}}}); err != nil {
				return err
			}
		}
	}
}

// Call sends one request: on the connection req names, or on one of its own.
func (h *Host) Call(ctx context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	r := req.GetRequest()
	params, err := check(r.GetMethod(), r.GetParams())
	if err != nil {
		return nil, err
	}
	c := h.held(req.GetConnectionId())
	switch {
	case c == nil && req.GetConnectionId() != "":
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no her connection %s: its stream ended", req.GetConnectionId()))
	case c == nil:
		if c, err = h.dial(ctx, false); err != nil {
			return nil, err
		}
		defer c.Close()
	}
	res, err := c.call(ctx, r.GetMethod(), params)
	if err != nil {
		return nil, h.lost(ctx, err)
	}
	return &herv1.CallResponse{Response: res}, nil
}

// AssistantMode sets how freely an assistant acts (SetHerAssistantMode):
// on a connection of its own, the assistant's conversation is opened and
// its permission mode set.
func (h *Host) AssistantMode(ctx context.Context, botID, mode string) error {
	switch mode {
	case "manual", "accept-edits", "bypass-permissions":
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown permission mode %q (manual, accept-edits or bypass-permissions)", mode))
	}
	c, err := h.dial(ctx, false)
	if err != nil {
		return err
	}
	defer c.Close()
	params, _ := json.Marshal(map[string]any{"bot_id": botID, "history_limit": 0})
	res, err := c.call(ctx, "bots/open", params)
	if err := answered("bots/open", res, err); err != nil {
		return err
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(res.Result, &open)
	params, _ = json.Marshal(map[string]any{"session_id": open.SessionID, "op": map[string]string{"type": "set_permission_mode", "mode": mode}})
	res, err = c.call(ctx, "session/op", params)
	return answered("session/op", res, err)
}

// connectorName is a name her takes for an MCP server.
var connectorName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// CommandConnector adds, changes or removes a connector her starts as a
// command (SetHerCommandConnector): mcp_servers.<name> in her's user
// configuration, its other settings kept.
func (h *Host) CommandConnector(ctx context.Context, cc *identityv1.SetHerCommandConnector) error {
	if !connectorName.MatchString(cc.GetName()) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a connector's name is 1 to 32 letters, digits, '_' or '-'"))
	}
	edit := map[string]any{"key_path": "mcp_servers." + cc.GetName(), "value": nil}
	if !cc.GetRemove() {
		if cc.GetCommand() == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("a connector started as a command needs its command"))
		}
		// Empty fields go: null removes a field in an upsert.
		server := map[string]any{"command": cc.GetCommand(), "args": nil, "env": nil}
		if len(cc.GetArgs()) > 0 {
			server["args"] = cc.GetArgs()
		}
		if len(cc.GetEnv()) > 0 {
			server["env"] = cc.GetEnv()
		}
		edit["value"], edit["merge"] = server, "upsert"
	}
	c, err := h.dial(ctx, false)
	if err != nil {
		return err
	}
	defer c.Close()
	params, _ := json.Marshal(map[string]any{"layer": "user", "edits": []any{edit}})
	res, err := c.call(ctx, "config/write", params)
	return answered("config/write", res, err)
}

// dial opens a connection to her while it runs.
func (h *Host) dial(ctx context.Context, notes bool) (*conn, error) {
	h.mu.Lock()
	on, running, why := h.stop != nil, h.snap != nil, h.err
	h.mu.Unlock()
	switch {
	case !on:
		return nil, errOff
	case !running && why != "":
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("her is not running: %s", why))
	case !running:
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("her is starting"))
	}
	dctx, cancel := context.WithTimeout(ctx, dialWait)
	defer cancel()
	c, err := dial(dctx, h.cfg.Socket, h.cfg.Version, notes)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("her does not answer: %w", err))
	}
	return c, nil
}

// hold keeps c under a new id for Calls and console requests to name.
func (h *Host) hold(c *conn) string {
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	h.mu.Lock()
	h.conns[id] = c
	h.mu.Unlock()
	return id
}

func (h *Host) held(id string) *conn {
	if id == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[id]
}

func (h *Host) release(id string, c *conn) {
	h.mu.Lock()
	delete(h.conns, id)
	h.mu.Unlock()
	c.Close()
}

// lost is a connection that failed under a request: ctx ending, Her turned
// off (the machine's answer, not the tunnel failing: the owner's device
// keeps the tunnel, and what else it carries, such as the command turning
// her off), or her going away.
func (h *Host) lost(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	h.mu.Lock()
	off := h.off
	h.mu.Unlock()
	if off {
		return errOff
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("lost her: %w", err))
}

// answered is what a request of hermeshd's own came back with, as an error.
func answered(method string, res *herv1.Response, err error) error {
	switch {
	case err != nil:
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("lost her: %w", err))
	case res.Error != nil:
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("her's %s: %s", method, res.Error.Message))
	}
	return nil
}
