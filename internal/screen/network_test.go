package screen

import (
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
)

// A restart must not reuse an interface snapshot from the old network.
// Poison only the test's cached flags; the OS interfaces stay untouched.
func TestNetworkRefreshesInterfacesOnRestart(t *testing.T) {
	n, err := newSystemNet()
	if err != nil {
		t.Fatal(err)
	}
	a, err := ice.NewAgentWithOptions(ice.WithNet(n), ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4}),
		ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost}), ice.WithIncludeLoopback())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for run := 0; run < 2; run++ {
		cached, err := n.Net.Interfaces()
		if err != nil {
			t.Fatal(err)
		}
		for _, iface := range cached {
			iface.Flags &^= net.FlagUp
		}
		if run > 0 {
			if err := a.Restart("", ""); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan struct{})
		if err := a.OnCandidate(func(c ice.Candidate) {
			if c == nil {
				close(done)
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.GatherCandidates(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("gather did not finish")
		}
		candidates, err := a.GetLocalCandidates()
		if err != nil || len(candidates) == 0 {
			t.Fatalf("gather %d reused stale interfaces: candidates=%v, err=%v", run, candidates, err)
		}
	}
}
