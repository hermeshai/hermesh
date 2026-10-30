package signer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// daemon plays hermeshd's side: it hands the signer requests and keeps what
// it completes.
type daemon struct {
	localv1connect.UnimplementedLocalServiceHandler
	reqs      chan *localv1.SigningRequest
	completed chan *localv1.CompleteSigningRequestRequest
	asksShow  atomic.Bool
}

func (d *daemon) WatchSigningRequests(ctx context.Context, req *localv1.WatchSigningRequestsRequest, stream *connect.ServerStream[localv1.SigningRequest]) error {
	d.asksShow.Store(req.Show)
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-d.reqs:
			if err := stream.Send(r); err != nil {
				return err
			}
		}
	}
}

func (d *daemon) CompleteSigningRequest(_ context.Context, req *localv1.CompleteSigningRequestRequest) (*localv1.CompleteSigningRequestResponse, error) {
	d.completed <- req
	return &localv1.CompleteSigningRequestResponse{}, nil
}

func (d *daemon) ListMachines(context.Context, *localv1.ListMachinesRequest) (*localv1.ListMachinesResponse, error) {
	return &localv1.ListMachinesResponse{}, nil
}

func (d *daemon) ListDevices(context.Context, *localv1.ListDevicesRequest) (*localv1.ListDevicesResponse, error) {
	return &localv1.ListDevicesResponse{}, nil
}

// softKeys keep root keys in memory.
type softKeys struct {
	keys    map[string]*identity.SoftwareKey
	deleted chan string
}

func (k *softKeys) Create(context.Context) (*ecdsa.PublicKey, error) {
	key, err := identity.GenerateKey()
	if err != nil {
		return nil, err
	}
	k.keys[identity.KeyID(key.Public())] = key
	return key.Public(), nil
}

func (k *softKeys) Delete(keyID string) error {
	delete(k.keys, keyID)
	k.deleted <- keyID
	return nil
}

func (k *softKeys) Sign(_ context.Context, keyID string, messages [][]byte) ([][]byte, error) {
	key := k.keys[keyID]
	if key == nil {
		return nil, errors.New("no such key")
	}
	var sigs [][]byte
	for _, m := range messages {
		sig, err := key.SignMessage(m)
		if err != nil {
			return nil, err
		}
		sigs = append(sigs, sig)
	}
	return sigs, nil
}

// owner approves while approve is set, and tells what it was asked.
type owner struct {
	approve  atomic.Bool
	asked    chan []preview.Card
	failures chan error
	shown    chan struct{}
}

func (o *owner) Approve(_ context.Context, cards []preview.Card) (bool, error) {
	o.asked <- cards
	return o.approve.Load(), nil
}
func (o *owner) Failed(err error) { o.failures <- err }
func (o *owner) Show()            { o.shown <- struct{}{} }

func grantRequest(t *testing.T, label, keyID, batch string) *localv1.SigningRequest {
	t.Helper()
	client, _ := identity.GenerateKey()
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(&identityv1.Grant{OwnerId: "o_x", ClientLabel: label, ClientPublicKey: identity.MarshalPublicKey(client.Public()), ExpiresAt: timestamppb.Now(), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	typ := identity.MessageType(&identityv1.Grant{})
	return &localv1.SigningRequest{RequestId: identity.RandomCode(16), Type: typ, Body: body, Message: identity.SigningInput(typ, body), BatchId: batch, RootKeyId: keyID}
}

// The signer creates a root key, shows a batch once and signs all of it,
// refuses what the owner declines, brings its approval forward when asked,
// and deletes a key.
func TestRun(t *testing.T) {
	d := &daemon{reqs: make(chan *localv1.SigningRequest, 8), completed: make(chan *localv1.CompleteSigningRequestRequest, 8)}
	mux := http.NewServeMux()
	mux.Handle(localv1connect.NewLocalServiceHandler(d))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	keys := &softKeys{keys: map[string]*identity.SoftwareKey{}, deleted: make(chan string, 1)}
	o := &owner{asked: make(chan []preview.Card, 4), failures: make(chan error, 4), shown: make(chan struct{}, 1)}
	o.approve.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, localv1connect.NewLocalServiceClient(srv.Client(), srv.URL), keys, o)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	next := func() *localv1.CompleteSigningRequestRequest {
		t.Helper()
		select {
		case c := <-d.completed:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("nothing completed")
			return nil
		}
	}

	d.reqs <- &localv1.SigningRequest{RequestId: "new", BatchId: "b0", CreateRootKey: true}
	created := next()
	pub, err := identity.ParsePublicKey(created.PublicKey)
	if err != nil || created.RequestId != "new" {
		t.Fatalf("created %v: %v", created, err)
	}
	keyID := identity.KeyID(pub)
	if !d.asksShow.Load() {
		t.Fatal("its watch didn't ask for show requests")
	}

	one, two := grantRequest(t, "bot", keyID, "b1"), grantRequest(t, "other bot", keyID, "b1")
	d.reqs <- one
	d.reqs <- two
	for _, r := range []*localv1.SigningRequest{one, two} {
		c := next()
		if c.RequestId != r.RequestId || identity.VerifyRaw(pub, r.Message, c.Signature) != nil {
			t.Fatalf("%s: %v", r.RequestId, c)
		}
	}
	if cards := <-o.asked; len(cards) != 2 {
		t.Fatalf("asked once about %d cards", len(cards))
	}

	o.approve.Store(false)
	declined := grantRequest(t, "bot", keyID, "b2")
	d.reqs <- declined
	if c := next(); c.RequestId != declined.RequestId || !c.Rejected {
		t.Fatalf("declined: %v", c)
	}
	<-o.asked
	if err := <-o.failures; !errors.Is(err, ErrRefused) {
		t.Fatalf("failure: %v", err)
	}

	d.reqs <- &localv1.SigningRequest{Show: true}
	select {
	case <-o.shown:
	case <-time.After(10 * time.Second):
		t.Fatal("not shown")
	}

	d.reqs <- &localv1.SigningRequest{RequestId: "gone", DeleteRootKey: true, RootKeyId: keyID}
	if c := next(); c.RequestId != "gone" || c.Rejected {
		t.Fatalf("delete: %v", c)
	}
	if got := <-keys.deleted; got != keyID {
		t.Fatalf("deleted %s", got)
	}
}
