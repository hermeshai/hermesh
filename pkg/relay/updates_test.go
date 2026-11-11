package relay

import (
	"testing"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// A machine that does not read gets, once it does, the newest update of
// each kind and none of those replaced meanwhile, however many were put
// (16 used to fill its queue, and what came after was dropped); the root
// chain comes first.
func TestUpdatesKeepTheNewestOfEachKind(t *testing.T) {
	q := newUpdates()
	revoked := func(v uint64) *relayv1.MachineUpdate {
		return &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Revocations{
			Revocations: &identityv1.RevocationList{MinGrantVersions: map[string]uint64{"g": v}}}}
	}
	q.put(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Grants{Grants: &relayv1.MachineGrants{}}})
	for v := range uint64(100) {
		q.put(revoked(v + 1))
	}
	q.put(&relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_RootChain{RootChain: &identityv1.RootChain{}}})
	q.put(&relayv1.MachineUpdate{}) // says nothing: not kept

	select {
	case <-q.wake:
	default:
		t.Fatal("the stream was not woken")
	}
	got := q.take()
	if len(got) != 3 {
		t.Fatalf("%d updates wait, want the newest of 3 kinds", len(got))
	}
	if got[0].GetRootChain() == nil || got[2].GetGrants() == nil {
		t.Fatalf("order: %v", got)
	}
	if v := got[1].GetRevocations().GetMinGrantVersions()["g"]; v != 100 {
		t.Fatalf("revocations at version %d, want the newest (100)", v)
	}
	if again := q.take(); len(again) != 0 {
		t.Fatalf("%d updates taken twice", len(again))
	}
}
