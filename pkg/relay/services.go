package relay

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

func errf(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

// ---------------------------------------------------------------------------
// Public API: info, claim, pairing rendezvous.

type publicService struct{ s *Server }

func (p *publicService) GetInfo(ctx context.Context, req *relayv1.GetInfoRequest) (*relayv1.GetInfoResponse, error) {
	s := p.s
	resp := &relayv1.GetInfoResponse{RelayId: s.relayID, ProtocolVersion: transport.ProtocolVersion, Hosted: s.cfg.Hosted, ConsoleUrl: s.cfg.ConsoleURL}
	if owner := s.singleOwner(); owner != "" {
		resp.OwnerId, resp.Claimed = owner, true
		if t, err := s.tenantFor(owner); err == nil {
			resp.RootChain = t.trust.RootChain()
		}
	} else if s.sqlite == nil && req.OwnerId != "" {
		// A hosted center tells whether it serves that owner, and its
		// current root cert (public; the owner id is the trust anchor).
		if t, err := s.loadTenant(req.OwnerId); err == nil && t != nil {
			resp.OwnerId, resp.RootChain = t.ownerID, t.trust.RootChain()
		}
	}
	return resp, nil
}

func (p *publicService) Claim(ctx context.Context, req *relayv1.ClaimRequest) (*relayv1.ClaimResponse, error) {
	s := p.s
	if s.sqlite == nil {
		return p.claimHosted(ctx, req)
	}
	if s.singleOwner() != "" {
		return nil, errf(connect.CodeAlreadyExists, "relay already claimed")
	}
	code, err := s.sqlite.ClaimCode()
	if err != nil {
		return nil, err
	}
	if !constantTimeEqual(strings.ToUpper(strings.TrimSpace(req.ClaimCode)), code) {
		return nil, errf(connect.CodePermissionDenied, "wrong claim code")
	}
	ownerID := identity.ChainOwner(req.RootChain)
	t := identity.NewTrust(ownerID)
	if _, err := t.AcceptRootChain(req.RootChain); err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if _, _, err := t.VerifyDeviceCert(req.DeviceCert); err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if err := s.sqlite.SaveClaim(ownerID, req.RootChain, req.DeviceCert); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.single = ownerID
	s.mu.Unlock()
	if _, err := s.tenantFor(ownerID); err != nil {
		return nil, err
	}
	s.log.Info("relay claimed", "owner", ownerID)
	return &relayv1.ClaimResponse{RelayId: s.relayID}, nil
}

// claimHosted links an owner to an account of a hosted center.
func (p *publicService) claimHosted(ctx context.Context, req *relayv1.ClaimRequest) (*relayv1.ClaimResponse, error) {
	s := p.s
	if s.cfg.Claim == nil {
		return nil, errf(connect.CodeFailedPrecondition, "this center does not accept links")
	}
	ownerID := identity.ChainOwner(req.RootChain)
	t := identity.NewTrust(ownerID)
	if _, err := t.AcceptRootChain(req.RootChain); err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if _, _, err := t.VerifyDeviceCert(req.DeviceCert); err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if err := s.cfg.Claim(ctx, req); err != nil {
		return nil, err
	}
	return &relayv1.ClaimResponse{RelayId: s.relayID}, nil
}

func clientIP(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func (p *publicService) SubmitPairing(ctx context.Context, req *relayv1.SubmitPairingRequest) (*relayv1.SubmitPairingResponse, error) {
	s := p.s
	ip := ""
	if ci, ok := connect.CallInfoForHandlerContext(ctx); ok {
		ip = clientIP(ci.Peer().Addr)
	}
	if !s.limiter.Allow(ip) {
		return nil, errf(connect.CodeResourceExhausted, "too many pairing requests")
	}
	return s.submitPairing(req.Request, false)
}

// SubmitCustodialPairing submits, from the embedding server's own MCP
// gateway, a client pairing request whose key that server holds: the
// request names its custodian, which a request from the network may not.
func (s *Server) SubmitCustodialPairing(request *identityv1.Signed) (string, time.Time, error) {
	res, err := s.submitPairing(request, true)
	if err != nil {
		return "", time.Time{}, err
	}
	return res.Code, res.ExpiresAt.AsTime(), nil
}

func (s *Server) submitPairing(request *identityv1.Signed, custodial bool) (*relayv1.SubmitPairingResponse, error) {
	pr := &identityv1.PairingRequest{}
	if _, err := identity.SelfSigned(request, pr, func(m *identityv1.PairingRequest) []byte { return m.SubjectPublicKey }); err != nil {
		return nil, errf(connect.CodeInvalidArgument, "bad pairing request: "+err.Error())
	}
	switch {
	case !custodial && pr.Custodian != "":
		return nil, errf(connect.CodeInvalidArgument, "a pairing request from the network cannot name a custodian")
	case custodial && (pr.Custodian == "" || pr.Kind != identityv1.PairingKind_PAIRING_KIND_CLIENT || pr.OwnerId == ""):
		return nil, errf(connect.CodeInvalidArgument, "a custodial pairing request is a client's, for a named owner, with its custodian")
	}
	switch {
	case s.cfg.Hosted:
		// An empty owner is claimed later by an account; a named one must
		// be known here.
		if pr.OwnerId != "" {
			if t, err := s.loadTenant(pr.OwnerId); err != nil || t == nil {
				return nil, errf(connect.CodeInvalidArgument, "this center does not serve that owner")
			}
		}
	case s.singleOwner() == "":
		return nil, errf(connect.CodeFailedPrecondition, "relay is not claimed yet")
	case pr.OwnerId != s.singleOwner():
		return nil, errf(connect.CodeInvalidArgument, "pairing request is for a different owner")
	}
	switch pr.Kind {
	case identityv1.PairingKind_PAIRING_KIND_MACHINE, identityv1.PairingKind_PAIRING_KIND_CLIENT:
	case identityv1.PairingKind_PAIRING_KIND_DEVICE:
		// A device asks one owner to trust it; nobody claims it by code.
		if pr.OwnerId == "" {
			return nil, errf(connect.CodeInvalidArgument, "a device joins a named owner")
		}
	default:
		return nil, errf(connect.CodeInvalidArgument, "bad pairing kind")
	}
	if len(pr.Label) > 64 || len(pr.Hostname) > 255 || len(pr.Platform) > 64 {
		return nil, errf(connect.CodeInvalidArgument, "pairing request fields too long")
	}
	now := time.Now()
	s.store.PurgePairings(now)
	code := identity.PairingCode(pr.Kind, pr.SubjectPublicKey)
	existing, err := s.store.GetPairing(code)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		if n, _ := s.store.PendingPairings(now); n >= s.cfg.MaxPendingPairings {
			return nil, errf(connect.CodeResourceExhausted, "too many pending pairing requests")
		}
		if pr.OwnerId != "" {
			if codes, _ := s.store.OwnerPendingPairings(pr.OwnerId, now); len(codes) >= s.cfg.MaxOwnerPendingPairings {
				return nil, errf(connect.CodeResourceExhausted, "too many pending pairing requests for this owner; ask the owner to answer them first")
			}
		}
	}
	exp := now.Add(s.cfg.PairingTTL)
	if existing != nil && (existing.Result != nil || existing.OwnerID != "" && pr.OwnerId == "") {
		// Re-running the command keeps a completed or claimed request.
		exp = existing.ExpiresAt
	} else if err := s.store.PutPairing(&Pairing{Code: code, Kind: pr.Kind, Request: request, OwnerID: pr.OwnerId, ExpiresAt: exp}); err != nil {
		return nil, err
	} else if existing == nil {
		go s.notifyPairing(pr.OwnerId, code, pr)
		s.wakeOwnerID(pr.OwnerId)
	}
	return &relayv1.SubmitPairingResponse{Code: code, ExpiresAt: timestamppb.New(exp)}, nil
}

func (p *publicService) WaitPairing(ctx context.Context, req *relayv1.WaitPairingRequest) (*relayv1.WaitPairingResponse, error) {
	s := p.s
	code := identity.NormalizePairingCode(req.Code)
	deadline := time.Now().Add(25 * time.Second)
	for {
		row, err := s.store.GetPairing(code)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return &relayv1.WaitPairingResponse{Expired: true}, nil
		}
		owner := s.pairingOwner(row)
		// Hosted: report the claim before anything else, so the requester
		// shows its verification code before it could see a result.
		if s.cfg.Hosted && owner != "" && req.KnownOwnerId != owner {
			t, err := s.tenantFor(owner)
			if err != nil {
				return nil, err
			}
			return &relayv1.WaitPairingResponse{Claimed: true, OwnerId: owner, RootChain: t.trust.RootChain(), AccountHint: row.AccountHint}, nil
		}
		if row.Result != nil {
			t, err := s.tenantFor(owner)
			if err != nil {
				return nil, err
			}
			// A device that joined finds its key in the chain itself.
			creds := &identityv1.Credentials{RootChain: t.trust.RootChain()}
			switch row.Kind {
			case identityv1.PairingKind_PAIRING_KIND_MACHINE:
				creds.Subject = &identityv1.Credentials_MachineCert{MachineCert: row.Result}
			case identityv1.PairingKind_PAIRING_KIND_CLIENT:
				creds.Subject = &identityv1.Credentials_Grant{Grant: row.Result}
			}
			return &relayv1.WaitPairingResponse{Done: true, Credentials: creds}, nil
		}
		if time.Now().After(row.ExpiresAt) {
			return &relayv1.WaitPairingResponse{Expired: true}, nil
		}
		if time.Now().After(deadline) {
			return &relayv1.WaitPairingResponse{}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// PairingResult is what became of a pairing request: the signed credential
// once the owner approved it, expired once it is gone or past its time
// (neither: still waiting).
func (s *Server) PairingResult(code string) (credential *identityv1.Signed, expired bool, err error) {
	row, err := s.store.GetPairing(identity.NormalizePairingCode(code))
	switch {
	case err != nil:
		return nil, false, err
	case row == nil:
		return nil, true, nil
	case row.Result != nil:
		return row.Result, false, nil
	}
	return nil, time.Now().After(row.ExpiresAt), nil
}

// Pairing returns the pairing request with this code as the relay stores it
// (whether claimed, answered or past its time), nil if there is none: a
// hosted center's pairing page shows it to the account that opens its link.
func (s *Server) Pairing(code string) (*Pairing, error) {
	return s.store.GetPairing(identity.NormalizePairingCode(code))
}

// RootChain is the owner's current root chain as this relay knows it.
func (s *Server) RootChain(ownerID string) (*identityv1.RootChain, error) {
	t, err := s.tenantFor(ownerID)
	if err != nil {
		return nil, err
	}
	return t.trust.RootChain(), nil
}

// pairingOwner is the owner a pairing is bound to.
func (s *Server) pairingOwner(p *Pairing) string {
	if p.OwnerID == "" && s.sqlite != nil {
		return s.singleOwner() // rows from before owners were stored
	}
	return p.OwnerID
}

// pendingPairing returns an unanswered, unexpired pairing request.
func (s *Server) pendingPairing(code string) (*Pairing, error) {
	row, err := s.store.GetPairing(identity.NormalizePairingCode(code))
	if err != nil {
		return nil, err
	}
	if row == nil || row.Result != nil || time.Now().After(row.ExpiresAt) {
		return nil, nil
	}
	return row, nil
}

// ClaimPairing binds an unclaimed pairing request to ownerID (hosted center:
// the owner typed the code in its app, or opened the request's pairing link
// signed in to their account). A machine does not trust the binding: the
// verification code checks it. A client claimed through its link accepts it.
func (s *Server) ClaimPairing(code, ownerID, accountHint string) (*Pairing, error) {
	if !s.cfg.Hosted {
		return nil, errors.New("only a hosted center claims pairing requests")
	}
	if _, err := s.tenantFor(ownerID); err != nil {
		return nil, err
	}
	row, err := s.pendingPairing(code)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errf(connect.CodeNotFound, "no pending pairing request with this code (it may have expired)")
	}
	if row.OwnerID != "" && row.OwnerID != ownerID {
		return nil, errf(connect.CodeNotFound, "no pending pairing request with this code (it may have expired)")
	}
	if row.OwnerID == "" {
		if err := s.store.ClaimPairing(row.Code, ownerID, accountHint); err != nil {
			return nil, err
		}
		row.OwnerID, row.AccountHint = ownerID, accountHint
		s.wakeOwnerID(ownerID)
	}
	return row, nil
}

// ---------------------------------------------------------------------------
// Machine control.

type machineControl struct{ s *Server }

func (n *machineControl) ReportStatus(ctx context.Context, req *relayv1.ReportStatusRequest) (*relayv1.ReportStatusResponse, error) {
	p := peerFrom(ctx)
	n.s.mu.Lock()
	p.t.reported[p.subject.ID] = &relayv1.ReportStatusRequest{Name: req.Name, Version: req.Version}
	n.s.wakeOwnerLocked(p.t)
	n.s.mu.Unlock()
	return &relayv1.ReportStatusResponse{}, nil
}

func (n *machineControl) Leave(ctx context.Context, _ *relayv1.LeaveRequest) (*relayv1.LeaveResponse, error) {
	p := peerFrom(ctx)
	n.s.log.Info("machine leaves its owner", "owner", p.t.ownerID, "machine", p.subject.ID)
	return &relayv1.LeaveResponse{}, n.s.applyRevocations(p.t, &identityv1.RevocationList{OwnerId: p.t.ownerID, RevokedMachineIds: []string{p.subject.ID}})
}

func (n *machineControl) WatchUpdates(ctx context.Context, _ *relayv1.WatchUpdatesRequest, stream *connect.ServerStream[relayv1.MachineUpdate]) error {
	s := n.s
	p := peerFrom(ctx)
	t := p.t
	ch := make(chan *relayv1.MachineUpdate, 16)
	s.mu.Lock()
	if t.watchers[p.subject.ID] == nil {
		t.watchers[p.subject.ID] = map[chan *relayv1.MachineUpdate]struct{}{}
	}
	t.watchers[p.subject.ID][ch] = struct{}{}
	rev := t.revocations
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(t.watchers[p.subject.ID], ch)
		s.mu.Unlock()
	}()
	if err := stream.Send(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_RootChain{RootChain: t.trust.RootChain()}}); err != nil {
		return err
	}
	if err := stream.Send(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Revocations{Revocations: rev}}); err != nil {
		return err
	}
	if certs, err := s.store.MachineCerts(t.ownerID); err == nil && certs[p.subject.ID] != nil {
		stream.Send(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_MachineCert{MachineCert: certs[p.subject.ID]}})
	}
	targets := &relayv1.PushTargets{}
	for _, pt := range s.pushTargets(t) {
		targets.Targets = append(targets.Targets, pt)
	}
	stream.Send(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_PushTargets{PushTargets: targets}})
	if g := s.machineGrants(t, p.subject.ID); g != nil {
		stream.Send(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Grants{Grants: g}})
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.sess.CloseChan():
			return nil
		case u := <-ch:
			if err := stream.Send(u); err != nil {
				return err
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Client control.

type clientControl struct{ s *Server }

func (c *clientControl) WhoAmI(ctx context.Context, _ *relayv1.WhoAmIRequest) (*relayv1.WhoAmIResponse, error) {
	p := peerFrom(ctx)
	return &relayv1.WhoAmIResponse{SubjectId: p.subject.ID, Role: p.role, OwnerId: p.t.ownerID}, nil
}

func (c *clientControl) ListMachines(ctx context.Context, req *relayv1.ListMachinesRequest) (*relayv1.ListMachinesResponse, error) {
	p := peerFrom(ctx)
	machines, err := c.s.machineEntries(p.t, func(id string) bool { return identity.Allows(p.subject.Grant.MachineIds, id) }, req.OnlineOnly)
	if err != nil {
		return nil, err
	}
	return &relayv1.ListMachinesResponse{Machines: machines, RootChain: p.t.trust.RootChain()}, nil
}

func (c *clientControl) GetLatestGrant(ctx context.Context, _ *relayv1.GetLatestGrantRequest) (*relayv1.GetLatestGrantResponse, error) {
	p := peerFrom(ctx)
	g, err := c.s.store.GrantForClient(p.t.ownerID, p.subject.ID)
	if err != nil {
		return nil, err
	}
	return &relayv1.GetLatestGrantResponse{Grant: g, RootChain: p.t.trust.RootChain(), Revoked: c.s.checkNotRevoked(p.t, p.subject) != nil,
		InboxWaiting: uint32(c.s.InboxWaiting(p.t.ownerID, p.subject.ID))}, nil
}

// ---------------------------------------------------------------------------
// Owner control. Everything the owner publishes is verified against the
// owner trust, so a stolen device key cannot mint credentials either.

type ownerControl struct{ s *Server }

// ownPairing returns a pending pairing bound to the calling owner.
func (o *ownerControl) ownPairing(ctx context.Context, code string) (*tenant, *Pairing, error) {
	p := peerFrom(ctx)
	row, err := o.s.store.GetPairing(identity.NormalizePairingCode(code))
	if err != nil {
		return nil, nil, err
	}
	if row == nil || time.Now().After(row.ExpiresAt) || o.s.pairingOwner(row) != p.t.ownerID {
		return nil, nil, errf(connect.CodeNotFound, "no pending pairing request with this code (it may have expired)")
	}
	return p.t, row, nil
}

func (o *ownerControl) GetPairingRequest(ctx context.Context, req *relayv1.GetPairingRequestRequest) (*relayv1.GetPairingRequestResponse, error) {
	_, row, err := o.ownPairing(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	if row.Result != nil {
		return nil, errf(connect.CodeAlreadyExists, "this pairing code has already been used")
	}
	return &relayv1.GetPairingRequestResponse{Request: row.Request, ExpiresAt: timestamppb.New(row.ExpiresAt)}, nil
}

func (o *ownerControl) ListPairingRequests(ctx context.Context, _ *relayv1.ListPairingRequestsRequest) (*relayv1.ListPairingRequestsResponse, error) {
	pairings, err := o.s.pendingPairings(peerFrom(ctx).t)
	if err != nil {
		return nil, err
	}
	return &relayv1.ListPairingRequestsResponse{Pairings: pairings}, nil
}

func (o *ownerControl) ClaimPairingRequest(ctx context.Context, req *relayv1.ClaimPairingRequestRequest) (*relayv1.ClaimPairingRequestResponse, error) {
	t := peerFrom(ctx).t
	code := identity.NormalizePairingCode(req.Code)
	if o.s.cfg.Hosted {
		var hint string
		if o.s.cfg.AccountHint != nil {
			hint = o.s.cfg.AccountHint(t.ownerID)
		}
		if _, err := o.s.ClaimPairing(code, t.ownerID, hint); err != nil {
			return nil, err
		}
	}
	_, row, err := o.ownPairing(ctx, code)
	if err != nil {
		return nil, err
	}
	if row.Result != nil {
		return nil, errf(connect.CodeAlreadyExists, "this pairing code has already been used")
	}
	return &relayv1.ClaimPairingRequestResponse{Request: row.Request, ExpiresAt: timestamppb.New(row.ExpiresAt)}, nil
}

func (o *ownerControl) CompletePairing(ctx context.Context, req *relayv1.CompletePairingRequest) (*relayv1.CompletePairingResponse, error) {
	s := o.s
	t, row, err := o.ownPairing(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	if row.Result != nil {
		return nil, errf(connect.CodeNotFound, "pairing request not pending")
	}
	pr := &identityv1.PairingRequest{}
	if err := identity.Unverified(row.Request, pr); err != nil {
		return nil, err
	}
	switch row.Kind {
	case identityv1.PairingKind_PAIRING_KIND_MACHINE:
		nc, _, err := t.trust.VerifyMachineCert(req.Credential)
		if err != nil {
			return nil, errf(connect.CodeInvalidArgument, err.Error())
		}
		if !bytes.Equal(nc.MachinePublicKey, pr.SubjectPublicKey) {
			return nil, errf(connect.CodeInvalidArgument, "machine cert is for a different key")
		}
		if err := s.store.PutMachineCert(t.ownerID, nc.MachineId, req.Credential); err != nil {
			return nil, err
		}
	case identityv1.PairingKind_PAIRING_KIND_CLIENT:
		g, _, err := t.trust.VerifyGrant(req.Credential, time.Now())
		if err != nil {
			return nil, errf(connect.CodeInvalidArgument, err.Error())
		}
		if !bytes.Equal(g.ClientPublicKey, pr.SubjectPublicKey) {
			return nil, errf(connect.CodeInvalidArgument, "grant is for a different key")
		}
		if err := s.store.PutGrant(t.ownerID, g, req.Credential); err != nil {
			return nil, err
		}
		defer s.pushGrants(t)
	case identityv1.PairingKind_PAIRING_KIND_DEVICE:
		// The device's root key is in the chain the approving device
		// published first; the credential is that chain's newest cert.
		chain := t.trust.RootChain().GetCerts()
		if len(chain) == 0 || !bytes.Equal(chain[len(chain)-1].GetBody(), req.Credential.GetBody()) {
			return nil, errf(connect.CodeInvalidArgument, "publish the root chain with the device first")
		}
		in := false
		for _, rk := range t.trust.Root().GetRootKeys() {
			in = in || bytes.Equal(rk.PublicKey, pr.SubjectPublicKey)
		}
		if !in {
			return nil, errf(connect.CodeInvalidArgument, "the root cert does not list this device")
		}
	default:
		return nil, errf(connect.CodeInvalidArgument, "bad pairing kind")
	}
	defer s.wakeOwner(t)
	return &relayv1.CompletePairingResponse{}, s.store.CompletePairing(row.Code, req.Credential)
}

func (o *ownerControl) ListMachines(ctx context.Context, req *relayv1.ListMachinesRequest) (*relayv1.ListMachinesResponse, error) {
	t := peerFrom(ctx).t
	machines, err := o.s.machineEntries(t, nil, req.OnlineOnly)
	if err != nil {
		return nil, err
	}
	return &relayv1.ListMachinesResponse{Machines: machines, RootChain: t.trust.RootChain()}, nil
}

func (o *ownerControl) ListClients(ctx context.Context, _ *relayv1.ListClientsRequest) (*relayv1.ListClientsResponse, error) {
	out, err := o.s.clientEntries(peerFrom(ctx).t)
	if err != nil {
		return nil, err
	}
	return &relayv1.ListClientsResponse{Clients: out}, nil
}

func (o *ownerControl) PublishGrant(ctx context.Context, req *relayv1.PublishGrantRequest) (*relayv1.PublishGrantResponse, error) {
	t := peerFrom(ctx).t
	g, _, err := t.trust.VerifyGrant(req.Grant, time.Now())
	if err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	defer o.s.wakeOwner(t)
	if err := o.s.store.PutGrant(t.ownerID, g, req.Grant); err != nil {
		return nil, err
	}
	o.s.pushGrants(t)
	return &relayv1.PublishGrantResponse{}, nil
}

func (o *ownerControl) PublishMachineCert(ctx context.Context, req *relayv1.PublishMachineCertRequest) (*relayv1.PublishMachineCertResponse, error) {
	t := peerFrom(ctx).t
	nc, _, err := t.trust.VerifyMachineCert(req.MachineCert)
	if err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if err := o.s.store.PutMachineCert(t.ownerID, nc.MachineId, req.MachineCert); err != nil {
		return nil, err
	}
	o.s.mu.Lock()
	o.s.wakeOwnerLocked(t)
	for ch := range t.watchers[nc.MachineId] {
		select {
		case ch <- &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_MachineCert{MachineCert: req.MachineCert}}:
		default:
		}
	}
	o.s.mu.Unlock()
	return &relayv1.PublishMachineCertResponse{}, nil
}

func (o *ownerControl) Revoke(ctx context.Context, req *relayv1.RevokeRequest) (*relayv1.RevokeResponse, error) {
	if req.Revocations == nil {
		return nil, errf(connect.CodeInvalidArgument, "missing revocations")
	}
	return &relayv1.RevokeResponse{}, o.s.applyRevocations(peerFrom(ctx).t, req.Revocations)
}

func (o *ownerControl) PublishRootChain(ctx context.Context, req *relayv1.PublishRootChainRequest) (*relayv1.PublishRootChainResponse, error) {
	if err := o.s.acceptRootChain(peerFrom(ctx).t, req.RootChain); err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	return &relayv1.PublishRootChainResponse{}, nil
}
