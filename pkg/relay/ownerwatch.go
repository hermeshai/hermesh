package relay

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// OwnerControlService.WatchOwner (docs/state-sync.md §3.2): an owner's
// devices are told what ListMachines, ListClients and ListPairingRequests
// return whenever it changes, instead of asking every few seconds. Where
// presence, names, grants and pairing requests change, the tenant's
// watchers are woken; each then builds the three lists as those calls do
// and sends the ones that changed.

const (
	// ownerWatchEvery spaces a watcher's messages.
	ownerWatchEvery = 200 * time.Millisecond
	// ownerWatchCheck: a watcher also looks this often, for a change made
	// where no watcher is woken.
	ownerWatchCheck = 30 * time.Second
)

// wakeOwnerLocked wakes t's WatchOwner streams (s.mu held).
func (s *Server) wakeOwnerLocked(t *tenant) {
	for c := range t.ownerWatchers {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (s *Server) wakeOwner(t *tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wakeOwnerLocked(t)
}

// wakeOwnerID wakes the watchers of ownerID's tenant, if it is loaded.
func (s *Server) wakeOwnerID(ownerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tenants[ownerID]; t != nil {
		s.wakeOwnerLocked(t)
	}
}

func (o *ownerControl) WatchOwner(ctx context.Context, _ *relayv1.WatchOwnerRequest, stream *connect.ServerStream[relayv1.OwnerUpdate]) error {
	s := o.s
	p := peerFrom(ctx)
	t := p.t
	wake := make(chan struct{}, 1)
	s.mu.Lock()
	t.ownerWatchers[wake] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(t.ownerWatchers, wake)
		s.mu.Unlock()
	}()
	var sent relayv1.OwnerUpdate // as last sent
	for {
		u, err := s.ownerUpdate(t, &sent)
		if err != nil {
			return err
		}
		if u != nil {
			if err := stream.Send(u); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-p.sess.CloseChan():
			return nil
		case <-wake:
		case <-time.After(ownerWatchCheck):
		}
		select { // what changes meanwhile comes in the same message
		case <-ctx.Done():
			return nil
		case <-time.After(ownerWatchEvery):
		}
	}
}

// ownerUpdate is t's lists that differ from those sent (all of them the
// first time), which it then holds as sent; nil when none does.
func (s *Server) ownerUpdate(t *tenant, sent *relayv1.OwnerUpdate) (*relayv1.OwnerUpdate, error) {
	machines, err := s.machineEntries(t, nil, false)
	if err != nil {
		return nil, err
	}
	clients, err := s.clientEntries(t)
	if err != nil {
		return nil, err
	}
	pairings, err := s.pendingPairings(t)
	if err != nil {
		return nil, err
	}
	// In a set order, so an unchanged list compares equal.
	slices.SortFunc(machines, func(a, b *relayv1.MachineEntry) int {
		return bytes.Compare(a.MachineCert.GetBody(), b.MachineCert.GetBody())
	})
	slices.SortFunc(clients, func(a, b *relayv1.ClientEntry) int { return bytes.Compare(a.Grant.GetBody(), b.Grant.GetBody()) })
	slices.SortFunc(pairings, func(a, b *relayv1.PendingPairing) int { return strings.Compare(a.Code, b.Code) })
	u := &relayv1.OwnerUpdate{}
	if m := (&relayv1.ListMachinesResponse{Machines: machines, RootChain: t.trust.RootChain()}); sent.Machines == nil || !proto.Equal(m, sent.Machines) {
		sent.Machines, u.Machines = m, m
	}
	if c := (&relayv1.ListClientsResponse{Clients: clients}); sent.Clients == nil || !proto.Equal(c, sent.Clients) {
		sent.Clients, u.Clients = c, c
	}
	if p := (&relayv1.ListPairingRequestsResponse{Pairings: pairings}); sent.Pairings == nil || !proto.Equal(p, sent.Pairings) {
		sent.Pairings, u.Pairings = p, p
	}
	if u.Machines == nil && u.Clients == nil && u.Pairings == nil {
		return nil, nil
	}
	return u, nil
}

// pendingPairings is the pairing requests waiting for t's owner, as
// ListPairingRequests lists them.
func (s *Server) pendingPairings(t *tenant) ([]*relayv1.PendingPairing, error) {
	now := time.Now()
	codes, err := s.store.OwnerPendingPairings(t.ownerID, now)
	if err != nil {
		return nil, err
	}
	var out []*relayv1.PendingPairing
	for _, code := range codes {
		row, err := s.store.GetPairing(code)
		if err != nil {
			return nil, err
		}
		if row == nil || row.Result != nil || now.After(row.ExpiresAt) || s.pairingOwner(row) != t.ownerID {
			continue
		}
		out = append(out, &relayv1.PendingPairing{Code: row.Code, Request: row.Request, ExpiresAt: timestamppb.New(row.ExpiresAt)})
	}
	return out, nil
}
