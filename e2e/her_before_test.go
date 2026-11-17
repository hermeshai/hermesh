package e2e

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestHerOnAMachineFromBeforeHer: the owner's device is newer than one of
// its machines, whose hermeshd is from before Her (HERMESH_TEST_BEFORE_HER,
// acting as 0.1.4; docs/version-skew.md §6.7). Once it answers, its status
// has no Her, which the apps show as needing an update. Turning Her on there
// asks it first and says it needs an update (unimplemented), with no Face
// ID; a signed Her command it gets all the same says so too, never "unknown
// admin command". Updated (started again without the switch), its status has
// Her, and Her goes on there.
func TestHerOnAMachineFromBeforeHer(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, nil, &localv1.GrantSpec{})
	c := w.app.client
	ctx := context.Background()
	home, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	box, sock := w.otherMac("old-box", []string{"HERMESH_TEST_BEFORE_HER=1"})
	daemon := w.logs[len(w.logs)-1] // the one otherMac started
	id := w.joinTyped(box, home, "old-box").LocalMachineId

	var m *localv1.Machine
	eventually(t, time.Minute, "old-box answers", func() bool {
		m = listedMachine(t, c, id)
		return m.GetStatus() != nil
	})
	if m.Status.Her != nil || m.Her != nil {
		t.Fatalf("Her on a machine from before her: %v %v", m.Status.Her, m.Her)
	}

	signed := len(w.app.signedTypes())
	_, err = c.SetHer(ctx, &localv1.SetHerRequest{MachineId: id, Enabled: true})
	if connect.CodeOf(err) != connect.CodeUnimplemented || strings.Contains(err.Error(), "unknown admin command") {
		t.Fatalf("Her on, from before her: %v", err)
	}
	if got := w.app.signedTypes(); len(got) != signed {
		t.Fatalf("Face ID asked for a machine from before her: %v", got[signed:])
	}
	// Signed and sent all the same (a command the device can't check first):
	// 0.1.4's answer, made "update Hermesh there".
	_, err = c.SetHerCommandConnector(ctx, &localv1.SetHerCommandConnectorRequest{MachineId: id,
		Connector: &identityv1.SetHerCommandConnector{Name: "notes", Command: "/bin/cat"}})
	if connect.CodeOf(err) != connect.CodeUnimplemented || strings.Contains(err.Error(), "unknown admin command") {
		t.Fatalf("a connector, from before her: %v", err)
	}
	if got := w.app.signedTypes(); len(got) != signed+1 {
		t.Fatalf("signed %v", got[signed:])
	}

	// Updated: started again, without the switch.
	daemon.stop()
	w.logs = append(w.logs, start(t, "owner old-box updated", nil, filepath.Join(w.bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(w.tmp, "old-box"), "--socket", sock))
	eventually(t, time.Minute, "old-box answers with Her", func() bool {
		return listedMachine(t, c, id).GetStatus().GetHer() != nil
	})
	if _, err := c.SetHer(ctx, &localv1.SetHerRequest{MachineId: id, Enabled: true}); err != nil {
		t.Fatalf("Her on, updated: %v", err)
	}
	if got := w.app.signedTypes(); !slices.Contains(got[signed+1:], "hermesh.identity.v1.AdminCommand") {
		t.Fatalf("Her on, updated, signed %v", got[signed+1:])
	}
	eventually(t, time.Minute, "Her on for old-box", func() bool { return listedMachine(t, c, id).GetHer().GetEnabled() })
}
