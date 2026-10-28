package relay

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A device asking to join without naming an owner (a Linux trusted device,
// docs/onboarding.md §9.5) is for a hosted center, where an owner's device
// claims it by its code (hermesh-cloud's TestLinuxTrustedDevice). An own
// server refuses it: it knows its one owner, which the device must name.
func TestDeviceRequestWithoutAnOwner(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	key, _ := identity.GenerateKey()
	signed, _ := identity.Sign(&identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_DEVICE,
		SubjectPublicKey: identity.MarshalPublicKey(key.Public()), Label: "vps-2", CreatedAt: timestamppb.Now()}, key)
	_, err = relayv1connect.NewRelayPublicServiceClient(srv.Client(), srv.URL).SubmitPairing(context.Background(), &relayv1.SubmitPairingRequest{Request: signed})
	if code := connect.CodeOf(err); code != connect.CodeFailedPrecondition && code != connect.CodeInvalidArgument {
		t.Fatalf("an own server took it: %v", err)
	}
}
