package owner

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// RegisterPush signs this device's push registration (its APNs token,
// notice key and what it wants to hear about) with its device key and
// gives it to the relay, which hands it to the owner's machines. An empty
// token stops the notifications.
func (o *Owner) RegisterPush(ctx context.Context, req *localv1.RegisterPushRequest) error {
	if err := o.requireInit(); err != nil {
		return err
	}
	if req.Token != "" && len(req.NoticePublicKey) != 32 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a notice key is 32 bytes (X25519)"))
	}
	o.mu.Lock()
	device, ownerID := o.device, o.ownerID
	o.mu.Unlock()
	reg := &identityv1.PushRegistration{OwnerId: ownerID, DevicePublicKey: identity.MarshalPublicKey(device.Public()), Token: req.Token,
		Environment: req.Environment, NoticePublicKey: req.NoticePublicKey, Kinds: req.Kinds, IssuedAt: timestamppb.Now()}
	signed, err := identity.Sign(reg, device)
	if err != nil {
		return err
	}
	c, _, err := o.control(ctx)
	if err != nil {
		return err
	}
	_, err = c.RegisterPush(ctx, &relayv1.RegisterPushRequest{Registration: signed})
	return err
}
