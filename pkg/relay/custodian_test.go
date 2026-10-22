package relay

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A pairing request that names a custodian (the server holds the client's
// key: an MCP connection) comes only from the server's own gateway: over
// the network it is refused, so nobody can borrow that path or its badge.
func TestCustodialPairingsComeOnlyFromTheGateway(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	key, _ := identity.GenerateKey()
	request := func(kind identityv1.PairingKind, owner, custodian string) *identityv1.Signed {
		signed, err := identity.Sign(&identityv1.PairingRequest{Kind: kind, SubjectPublicKey: identity.MarshalPublicKey(key.Public()),
			Label: "Claude (claude.ai)", OwnerId: owner, CreatedAt: timestamppb.Now(), Custodian: custodian}, key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	c := relayv1connect.NewRelayPublicServiceClient(srv.Client(), srv.URL)
	_, err = c.SubmitPairing(context.Background(), &relayv1.SubmitPairingRequest{Request: request(identityv1.PairingKind_PAIRING_KIND_CLIENT, "o_x", "https://evil.example")})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "custodian") {
		t.Fatalf("a custodial request over the network: %v", err)
	}
	for name, r := range map[string]*identityv1.Signed{
		"no custodian":  request(identityv1.PairingKind_PAIRING_KIND_CLIENT, "o_x", ""),
		"a machine":     request(identityv1.PairingKind_PAIRING_KIND_MACHINE, "o_x", "https://relay.example"),
		"no owner":      request(identityv1.PairingKind_PAIRING_KIND_CLIENT, "", "https://relay.example"),
		"another owner": request(identityv1.PairingKind_PAIRING_KIND_CLIENT, "o_x", "https://relay.example"),
	} {
		if _, _, err := s.SubmitCustodialPairing(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
