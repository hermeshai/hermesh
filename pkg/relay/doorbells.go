package relay

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// Doorbells are the webhooks rung when messages wait for an address
// (docs/agent-names.md §7): an agent, or a name under it. The MCP gateway
// keeps them, with their keys in its vault, for every kind of agent; a
// relay without it has none, and answers their calls Unimplemented.
type Doorbells interface {
	// Woken lists, by agent, the names with a doorbell ("" for the agent
	// itself): senders put the name on the envelope.
	Woken(ctx context.Context, ownerID string) (map[string][]string, error)
	// WakeLink makes a one-time link for the owner to set up the doorbell
	// of an agent's name.
	WakeLink(ctx context.Context, ownerID, inbox, name string) (*relayv1.WakeLinkResponse, error)
	ListWakes(ctx context.Context, ownerID, inbox string) ([]*relayv1.Wake, error)
	SetWake(ctx context.Context, ownerID string, req *relayv1.SetWakeRequest) (*relayv1.SetWakeResponse, error)
	CheckWake(ctx context.Context, ownerID, inbox, name string) (*relayv1.CheckWakeResponse, error)
	ClearWake(ctx context.Context, ownerID, inbox, name string) error
	OpenWakeLink(ctx context.Context, ownerID, code string) (*relayv1.OpenWakeLinkResponse, error)
	// Forget drops the doorbells of an agent that ended.
	Forget(ctx context.Context, ownerID, inbox string)
}

// IsAgent reports whether inbox is one of the owner's live agents.
func (s *Server) IsAgent(ownerID, inbox string) bool {
	t, err := s.tenantFor(ownerID)
	return err == nil && s.liveClients(t)[inbox]
}

// SetDoorbells hands the doorbells' calls to d (the MCP gateway).
func (s *Server) SetDoorbells(d Doorbells) {
	s.mu.Lock()
	s.doorbells = d
	s.mu.Unlock()
}

func (s *Server) doorbellService() Doorbells {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doorbells
}

var errNoDoorbells = errf(connect.CodeUnimplemented, "this relay keeps no doorbells: it runs without the MCP gateway")

// ownerDoorbells is the doorbells, for a trusted device of the owner.
func (o *ownerControl) ownerDoorbells(ctx context.Context) (Doorbells, string, error) {
	p := peerFrom(ctx)
	if p.deviceCert == nil {
		return nil, "", errf(connect.CodePermissionDenied, "only a trusted device sets up doorbells")
	}
	d := o.s.doorbellService()
	if d == nil {
		return nil, "", errNoDoorbells
	}
	return d, p.t.ownerID, nil
}

func (o *ownerControl) ListWakes(ctx context.Context, req *relayv1.ListWakesRequest) (*relayv1.ListWakesResponse, error) {
	d, owner, err := o.ownerDoorbells(ctx)
	if err != nil {
		return nil, err
	}
	wakes, err := d.ListWakes(ctx, owner, req.ClientId)
	if err != nil {
		return nil, err
	}
	return &relayv1.ListWakesResponse{Wakes: wakes}, nil
}

func (o *ownerControl) SetWake(ctx context.Context, req *relayv1.SetWakeRequest) (*relayv1.SetWakeResponse, error) {
	d, owner, err := o.ownerDoorbells(ctx)
	if err != nil {
		return nil, err
	}
	if req.Code == "" && !strings.HasPrefix(req.ClientId, "c_") {
		return nil, errf(connect.CodeInvalidArgument, "a doorbell is an agent's (c_…), or a wake link's")
	}
	return d.SetWake(ctx, owner, req)
}

func (o *ownerControl) CheckWake(ctx context.Context, req *relayv1.CheckWakeRequest) (*relayv1.CheckWakeResponse, error) {
	d, owner, err := o.ownerDoorbells(ctx)
	if err != nil {
		return nil, err
	}
	return d.CheckWake(ctx, owner, req.ClientId, req.Name)
}

func (o *ownerControl) ClearWake(ctx context.Context, req *relayv1.ClearWakeRequest) (*relayv1.ClearWakeResponse, error) {
	d, owner, err := o.ownerDoorbells(ctx)
	if err != nil {
		return nil, err
	}
	return &relayv1.ClearWakeResponse{}, d.ClearWake(ctx, owner, req.ClientId, req.Name)
}

func (o *ownerControl) OpenWakeLink(ctx context.Context, req *relayv1.OpenWakeLinkRequest) (*relayv1.OpenWakeLinkResponse, error) {
	d, owner, err := o.ownerDoorbells(ctx)
	if err != nil {
		return nil, err
	}
	return d.OpenWakeLink(ctx, owner, req.Code)
}

// WakeLink is an agent asking for its own doorbell (`message wake`).
func (m *messageService) WakeLink(ctx context.Context, req *relayv1.WakeLinkRequest) (*relayv1.WakeLinkResponse, error) {
	p := peerFrom(ctx)
	if !strings.HasPrefix(p.subject.ID, "c_") {
		return nil, errf(connect.CodePermissionDenied, "agents ask for doorbells")
	}
	d := m.s.doorbellService()
	if d == nil {
		return nil, errNoDoorbells
	}
	return d.WakeLink(ctx, p.t.ownerID, p.subject.ID, req.Name)
}
