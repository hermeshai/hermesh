package owner

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Contact cards (docs/agent-messages.md §4). This device signs, with its
// device key, a card for each of the owner's agents that lacks a current
// one, so the others can write to it: its name, kind and key, nothing of
// its grant. A card grants nothing, so it needs no Touch ID; any trusted
// device may sign it, and the relay keeps the latest.

// refreshCards lists the owner's agents and signs the cards they lack.
func (o *Owner) refreshCards() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctl, _, err := o.control(ctx)
	if err != nil {
		return
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return
	}
	o.syncCards(res.Clients)
}

// syncCards signs and publishes the cards the listed agents lack, or whose
// card says something else or no longer verifies.
func (o *Owner) syncCards(entries []*relayv1.ClientEntry) {
	o.cardsMu.Lock()
	defer o.cardsMu.Unlock()
	o.mu.Lock()
	trust, dev, ownerID := o.trust, o.device, o.ownerID
	o.mu.Unlock()
	if trust == nil || dev == nil {
		return
	}
	var cards []*identityv1.Signed
	for _, e := range entries {
		g, _, err := trust.VerifyGrant(e.Grant, time.Now())
		if err != nil {
			continue // expired: it reaches nobody until renewed
		}
		want := &identityv1.ContactCard{OwnerId: ownerID, ClientId: g.ClientId, ClientPublicKey: g.ClientPublicKey, Name: g.ClientLabel, Kind: "cli"}
		if g.Custodian != "" {
			want.Kind = "mcp"
		}
		if e.Card != nil {
			if have, _, err := trust.VerifyCard(e.Card, e.CardDeviceCert); err == nil && identity.SameCard(have, want) {
				continue
			}
		}
		want.IssuedAt = timestamppb.Now()
		signed, err := identity.Sign(want, dev)
		if err != nil {
			continue
		}
		cards = append(cards, signed)
	}
	if len(cards) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctl, _, err := o.control(ctx)
	if err != nil {
		return
	}
	if _, err := ctl.PublishCards(ctx, &relayv1.PublishCardsRequest{Cards: cards}); err != nil {
		o.log.Warn("publishing contact cards", "err", err)
	}
}
