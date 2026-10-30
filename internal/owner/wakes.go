package owner

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
)

// Doorbells (docs/agent-names.md §7): a trusted device sets up, for the
// owner's agents, the webhook rung when messages wait for an agent or a
// name under it. The relay's MCP gateway keeps them, keys in its vault.

// wakeControl is the relay, for a trusted device.
func (o *Owner) wakeControl(ctx context.Context) (relayv1connect.OwnerControlServiceClient, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	if o.machineOnly() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this computer approves nothing: set doorbells up on a trusted device"))
	}
	c, _, err := o.control(ctx)
	return c, err
}

func localWake(w *relayv1.Wake) *localv1.Wake {
	if w == nil {
		return nil
	}
	out := &localv1.Wake{ClientId: w.ClientId, Name: w.Name, Url: w.Url, State: w.State, Reason: w.Reason, LastRingAt: w.LastRingAt,
		LastStatus: w.LastStatus, Waiting: w.Waiting}
	for _, r := range w.Recent {
		out.Recent = append(out.Recent, &localv1.WakeRing{At: r.At, Check: r.Check, As: r.As, Status: r.Status})
	}
	return out
}

func localCheck(c *relayv1.WakeCheck) *localv1.WakeCheck {
	if c == nil {
		return nil
	}
	return &localv1.WakeCheck{Ok: c.Ok, Status: c.Status, Reason: c.Reason}
}

func (l *localService) ListWakes(ctx context.Context, req *localv1.ListWakesRequest) (*localv1.ListWakesResponse, error) {
	c, err := l.o().wakeControl(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.ListWakes(ctx, &relayv1.ListWakesRequest{ClientId: req.ClientId})
	if err != nil {
		return nil, err
	}
	out := &localv1.ListWakesResponse{}
	for _, w := range res.Wakes {
		out.Wakes = append(out.Wakes, localWake(w))
	}
	return out, nil
}

func (l *localService) SetWake(ctx context.Context, req *localv1.SetWakeRequest) (*localv1.SetWakeResponse, error) {
	c, err := l.o().wakeControl(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.SetWake(ctx, &relayv1.SetWakeRequest{ClientId: req.ClientId, Name: req.Name, Code: req.Code, Url: req.Url,
		Authorization: req.Authorization, Keep: req.Keep})
	if err != nil {
		return nil, err
	}
	return &localv1.SetWakeResponse{Wake: localWake(res.Wake), Check: localCheck(res.Check), SigningSecret: res.SigningSecret}, nil
}

func (l *localService) CheckWake(ctx context.Context, req *localv1.CheckWakeRequest) (*localv1.CheckWakeResponse, error) {
	c, err := l.o().wakeControl(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.CheckWake(ctx, &relayv1.CheckWakeRequest{ClientId: req.ClientId, Name: req.Name})
	if err != nil {
		return nil, err
	}
	return &localv1.CheckWakeResponse{Wake: localWake(res.Wake), Check: localCheck(res.Check)}, nil
}

func (l *localService) ClearWake(ctx context.Context, req *localv1.ClearWakeRequest) (*localv1.ClearWakeResponse, error) {
	c, err := l.o().wakeControl(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.ClearWake(ctx, &relayv1.ClearWakeRequest{ClientId: req.ClientId, Name: req.Name}); err != nil {
		return nil, err
	}
	return &localv1.ClearWakeResponse{}, nil
}

func (l *localService) OpenWakeLink(ctx context.Context, req *localv1.OpenWakeLinkRequest) (*localv1.OpenWakeLinkResponse, error) {
	c, err := l.o().wakeControl(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.OpenWakeLink(ctx, &relayv1.OpenWakeLinkRequest{Code: req.Code})
	if err != nil {
		return nil, err
	}
	return &localv1.OpenWakeLinkResponse{ClientId: res.ClientId, Name: res.Name, ExpiresAt: res.ExpiresAt}, nil
}
