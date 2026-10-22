package mcpgw

import (
	"context"
	"net/http"

	"github.com/hermeshai/hermesh/pkg/relay"
)

// SingleOwner is the Accounts of a self-hosted relay: one owner (none until
// the relay is claimed) and no sign-in. Anyone who reaches the relay can
// start a connection, as anyone can ask to pair today; only the owner's
// Touch ID lets it reach a machine.
type SingleOwner struct{ Relay *relay.Server }

func (s SingleOwner) Browser(*http.Request) (Account, bool) {
	return Account{ID: "owner", OwnerID: s.Relay.SingleOwner()}, true
}

func (s SingleOwner) SignInURL(next string) string { return next }

func (s SingleOwner) Owner(context.Context, string) (string, error) {
	return s.Relay.SingleOwner(), nil
}
