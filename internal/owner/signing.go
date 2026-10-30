package owner

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Root key signatures come from the signer (Hermesh Signer.app: Secure
// Enclave + Touch ID). hermeshd posts SigningRequests; the signer parses the
// body itself, shows it, signs and completes the request. It also creates
// the root key when hermeshd asks for one.

var ErrSigningRejected = errors.New("the owner declined to sign")

type signHub struct {
	mu sync.Mutex
	// Each signer's requests, and whether it takes show requests (ShowSigner).
	watchers map[chan *localv1.SigningRequest]bool
	pending  map[string]*pendingSign
}

type pendingSign struct {
	req  *localv1.SigningRequest
	done chan []byte // the signature or the new public key; closed = rejected
}

func newSignHub() *signHub {
	return &signHub{watchers: map[chan *localv1.SigningRequest]bool{}, pending: map[string]*pendingSign{}}
}

func (h *signHub) watch(show bool) (chan *localv1.SigningRequest, func()) {
	ch := make(chan *localv1.SigningRequest, 32)
	h.mu.Lock()
	h.watchers[ch] = show
	for _, p := range h.pending {
		ch <- p.req
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.watchers, ch)
		h.mu.Unlock()
	}
}

func (h *signHub) complete(req *localv1.CompleteSigningRequestRequest) error {
	h.mu.Lock()
	p := h.pending[req.RequestId]
	delete(h.pending, req.RequestId)
	h.mu.Unlock()
	if p == nil {
		return errors.New("no such signing request")
	}
	switch {
	case req.Rejected:
		close(p.done)
	case p.req.CreateRootKey:
		p.done <- req.PublicKey
	default:
		p.done <- req.Signature
	}
	return nil
}

// state is whether a signer watches, and whether a request waits for the
// owner's approval there.
func (h *signHub) state() *localv1.SignerState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := &localv1.SignerState{Connected: len(h.watchers) > 0}
	for _, p := range h.pending {
		if p.req.Type != "" {
			st.Busy = st.Connected
		}
	}
	return st
}

// show has the signers that asked for it bring their open approval forward.
func (h *signHub) show() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, show := range h.watchers {
		if show {
			select {
			case ch <- &localv1.SigningRequest{Show: true}:
			default:
			}
		}
	}
}

// post registers req and hands it to every watcher. Callers hold h.mu.
func (h *signHub) post(req *localv1.SigningRequest) *pendingSign {
	p := &pendingSign{req: req, done: make(chan []byte, 1)}
	h.pending[req.RequestId] = p
	for ch := range h.watchers {
		select {
		case ch <- req:
		default:
		}
	}
	return p
}

// createKey asks the signer for a new root key and returns its public key.
func (h *signHub) createKey(ctx context.Context) (*ecdsa.PublicKey, []byte, error) {
	req := &localv1.SigningRequest{RequestId: identity.RandomCode(16), BatchId: identity.RandomCode(10), CreateRootKey: true, Caller: callerFrom(ctx)}
	h.mu.Lock()
	p := h.post(req)
	h.mu.Unlock()
	select {
	case der, ok := <-p.done:
		if !ok {
			return nil, nil, ErrSigningRejected
		}
		pub, err := identity.ParsePublicKey(der)
		if err != nil {
			return nil, nil, fmt.Errorf("the signer returned an invalid root key: %w", err)
		}
		return pub, der, nil
	case <-time.After(3 * time.Minute):
		h.drop(req.BatchId)
		return nil, nil, errors.New("timed out waiting for the signer to create a root key")
	case <-ctx.Done():
		h.drop(req.BatchId)
		return nil, nil, ctx.Err()
	}
}

// deleteKey has the signer delete a root key this device no longer uses.
// It waits a little for the signer, which may not be running; a key it
// misses only stays unused.
func (h *signHub) deleteKey(ctx context.Context, keyID string) error {
	req := &localv1.SigningRequest{RequestId: identity.RandomCode(16), BatchId: identity.RandomCode(10), DeleteRootKey: true, RootKeyId: keyID, Caller: callerFrom(ctx)}
	h.mu.Lock()
	p := h.post(req)
	h.mu.Unlock()
	select {
	case _, ok := <-p.done:
		if !ok {
			return errors.New("the signer did not delete the root key")
		}
		return nil
	case <-time.After(10 * time.Second):
		h.drop(req.BatchId)
		return errors.New("timed out waiting for the signer to delete the root key")
	case <-ctx.Done():
		h.drop(req.BatchId)
		return ctx.Err()
	}
}

// signBatch asks the signer to sign msgs with the root key in one
// confirmation and returns the signed envelopes.
func (h *signHub) signBatch(ctx context.Context, root *ecdsa.PublicKey, msgs []proto.Message) ([]*identityv1.Signed, error) {
	batch := identity.RandomCode(10)
	type item struct {
		p     *pendingSign
		typ   string
		body  []byte
		input []byte
	}
	items := make([]item, len(msgs))
	h.mu.Lock()
	for i, m := range msgs {
		body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		typ := identity.MessageType(m)
		input := identity.SigningInput(typ, body)
		req := &localv1.SigningRequest{RequestId: identity.RandomCode(16), Type: typ, Body: body, Message: input, BatchId: batch, Caller: callerFrom(ctx),
			ShowMatch: showMatch(ctx), Confirmed: confirmed(ctx), RootKeyId: identity.KeyID(root)}
		items[i] = item{p: h.post(req), typ: typ, body: body, input: input}
	}
	h.mu.Unlock()
	// Requests stay pending until the signer connects (watch replays them) or
	// the timeout passes.
	out := make([]*identityv1.Signed, len(items))
	timeout := time.After(3 * time.Minute)
	for i, it := range items {
		select {
		case sig, ok := <-it.p.done:
			if !ok {
				h.drop(batch)
				return nil, ErrSigningRejected
			}
			if identity.VerifyRaw(root, it.input, sig) != nil {
				h.drop(batch)
				return nil, errors.New("the signer returned an invalid signature")
			}
			out[i] = &identityv1.Signed{Type: it.typ, Body: it.body, SignerKeyId: identity.KeyID(root),
				Alg: identityv1.SignatureAlgorithm_SIGNATURE_ALGORITHM_ECDSA_P256_SHA256, Signature: sig}
		case <-timeout:
			h.drop(batch)
			return nil, errors.New("timed out waiting for the owner to confirm")
		case <-ctx.Done():
			h.drop(batch)
			return nil, ctx.Err()
		}
	}
	return out, nil
}

type showMatchKey struct{}

// withShowMatch has the signer show the two digits of what it signs in ctx
// (a machine that asked for them: PairingRequest.confirm_match).
func withShowMatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, showMatchKey{}, true)
}

func showMatch(ctx context.Context) bool {
	v, _ := ctx.Value(showMatchKey{}).(bool)
	return v
}

type confirmedKey struct{}

// withConfirmed tells the signer that the client pairing it signs for in
// ctx was already confirmed as the person's own (PairingDetails.confirmed):
// it shows no verification code to compare.
func withConfirmed(ctx context.Context) context.Context {
	return context.WithValue(ctx, confirmedKey{}, true)
}

func confirmed(ctx context.Context) bool {
	v, _ := ctx.Value(confirmedKey{}).(bool)
	return v
}

func (h *signHub) drop(batch string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, p := range h.pending {
		if p.req.BatchId == batch {
			delete(h.pending, id)
		}
	}
}
