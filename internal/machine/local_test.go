package machine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hermeshai/hermesh/pkg/transport"
)

// TestGone: only the relay saying the machine is no longer its owner's
// counts as removal; a reset relay or a passing failure does not.
func TestGone(t *testing.T) {
	refused := func(msg string) error { return fmt.Errorf("%w: %s", transport.ErrAdmission, msg) }
	for _, c := range []struct {
		err  error
		want bool
	}{
		{refused("machine removed"), true},
		{refused("unknown or removed machine"), true},
		{refused("this center no longer serves this device's owner (the owner identity was reset); pair this device again"), true},
		{refused("this relay does not serve that owner"), true},
		{refused("relay is not claimed yet"), false},
		{refused("database is locked"), false},
		{errors.New("dial relay: connection refused"), false},
		{nil, false},
	} {
		if got := gone(c.err); got != c.want {
			t.Errorf("gone(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
