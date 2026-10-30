// Package signer answers the owner role's signing requests
// (hermesh.local.v1 WatchSigningRequests): it shows the owner what each
// batch signs (internal/preview), asks them, and signs with a root key it
// holds. A Linux trusted device's terminal (hermeshd approve) and the
// Windows signer share it; Apple's devices keep the Swift signer
// (docs/windows.md §7.3).
package signer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// ErrRefused: the owner did not approve.
var ErrRefused = errors.New("refused")

// Keys are a device's root keys, one per profile.
type Keys interface {
	// Create makes a new root key.
	Create(ctx context.Context) (*ecdsa.PublicKey, error)
	// Delete removes the root key keyID (the device left its owner).
	Delete(keyID string) error
	// Sign signs each message with the root key keyID: ECDSA P-256 over
	// SHA-256, ASN.1 (identity.VerifyRaw). It may ask the owner, and
	// returns ErrRefused when they decline.
	Sign(ctx context.Context, keyID string, messages [][]byte) ([][]byte, error)
}

// Owner is the person the signer asks.
type Owner interface {
	// Approve shows what a batch signs, and whether the owner approves it.
	Approve(ctx context.Context, cards []preview.Card) (bool, error)
	// Failed tells that a batch was not signed, and why.
	Failed(err error)
	// Show brings an approval that is open forward (ShowSigner).
	Show()
}

// Run answers c's signing requests until ctx ends, a batch at a time.
func Run(ctx context.Context, c localv1connect.LocalServiceClient, keys Keys, owner Owner) {
	reqs := make(chan *localv1.SigningRequest, 16)
	go func() {
		defer close(reqs)
		for ctx.Err() == nil {
			stream, err := c.WatchSigningRequests(ctx, &localv1.WatchSigningRequestsRequest{Show: true})
			if err != nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			for stream.Receive() {
				if stream.Msg().Show {
					owner.Show()
					continue
				}
				reqs <- stream.Msg()
			}
		}
	}()
	var next *localv1.SigningRequest
	for {
		r := next
		if next = nil; r == nil {
			var ok bool
			if r, ok = <-reqs; !ok {
				return
			}
		}
		// The rest of its batch comes right after it.
		batch := []*localv1.SigningRequest{r}
	gather:
		for r.BatchId != "" {
			select {
			case n, ok := <-reqs:
				if !ok {
					break gather
				}
				if n.BatchId != r.BatchId {
					next = n
					break gather
				}
				batch = append(batch, n)
			case <-time.After(300 * time.Millisecond):
				break gather
			}
		}
		if err := handle(ctx, c, keys, owner, batch); err != nil {
			owner.Failed(err)
			for _, r := range batch {
				c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Rejected: true})
			}
		}
	}
}

func handle(ctx context.Context, c localv1connect.LocalServiceClient, keys Keys, owner Owner, batch []*localv1.SigningRequest) error {
	first := batch[0]
	switch {
	case first.CreateRootKey:
		pub, err := keys.Create(ctx)
		if err != nil {
			return err
		}
		_, err = c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: first.RequestId, PublicKey: identity.MarshalPublicKey(pub)})
		return err
	case first.DeleteRootKey:
		keys.Delete(first.RootKeyId) // a key it no longer has is deleted too
		_, err := c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: first.RequestId})
		return err
	}
	cards, err := preview.Cards(batch, hints(ctx, c))
	if err != nil {
		return err
	}
	ok, err := owner.Approve(ctx, cards)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRefused
	}
	messages := make([][]byte, len(batch))
	for i, r := range batch {
		messages[i] = r.Message
	}
	sigs, err := keys.Sign(ctx, first.RootKeyId, messages)
	if err != nil {
		return err
	}
	for i, r := range batch {
		if _, err := c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Signature: sigs[i]}); err != nil {
			return err
		}
	}
	return nil
}

// hints are names from the owner role's lists, for the cards.
func hints(ctx context.Context, c localv1connect.LocalServiceClient) preview.Hints {
	h := preview.Hints{MachineNames: map[string]string{}}
	if res, err := c.ListMachines(ctx, &localv1.ListMachinesRequest{}); err == nil {
		for _, m := range res.Machines {
			h.MachineNames[m.MachineId] = m.MachineName
		}
	}
	if res, err := c.ListDevices(ctx, &localv1.ListDevicesRequest{}); err == nil {
		h.Devices = map[string]string{}
		for _, d := range res.Devices {
			h.Devices[d.KeyId] = d.Label
		}
	}
	return h
}
