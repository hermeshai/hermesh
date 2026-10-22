package cli

import (
	"testing"

	"github.com/hermeshai/hermesh/internal/peer"
)

// TestCheckName: an agent uses a machine picked by name only if the
// machine presents that name on the end-to-end connection, so a relay that
// swaps the names of an owner's machines cannot misdirect work.
func TestCheckName(t *testing.T) {
	listed := machineRef{ID: "m_a", Name: "prod"}
	if err := checkName(listed, &peer.Machine{MachineID: "m_a", Name: "prod"}); err != nil {
		t.Fatalf("same name: %v", err)
	}
	if err := checkName(listed, &peer.Machine{MachineID: "m_a", Name: "staging"}); err == nil {
		t.Fatal("a machine calling itself staging was used as prod")
	}
	// Listed without a name (by id): nothing to hold it to.
	if err := checkName(machineRef{ID: "m_a", Name: "m_a"}, &peer.Machine{MachineID: "m_a", Name: "staging"}); err != nil {
		t.Fatalf("by id: %v", err)
	}
}
