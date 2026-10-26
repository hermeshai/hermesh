package machine

import (
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// The grants that cover this machine, as the relay sends them whenever one
// changes, each checked against the owner's root keys: who may start
// sessions here, for a machine only to show. A client is still let in only
// by the grant it presents (checkGrant).

// kGrants: the verified grants (relayv1.MachineGrants), so they show while
// the relay can't be reached.
const kGrants = "grants"

func (n *Machine) acceptGrants(in *relayv1.MachineGrants) {
	n.mu.Lock()
	trust := n.trust
	n.mu.Unlock()
	if trust == nil {
		return
	}
	kept := &relayv1.MachineGrants{}
	var gs []*identityv1.Grant
	for _, sg := range in.GetGrants() {
		g, _, err := trust.VerifyGrant(sg, time.Now())
		if err != nil || !identity.Allows(g.MachineIds, n.machineID) {
			continue
		}
		kept.Grants = append(kept.Grants, sg)
		gs = append(gs, g)
	}
	n.mu.Lock()
	n.grants = gs
	n.mu.Unlock()
	n.st.SetProto(kGrants, kept)
}

func (n *Machine) loadGrants() {
	in := &relayv1.MachineGrants{}
	if ok, _ := n.st.GetProto(kGrants, in); ok {
		n.acceptGrants(in)
	}
}

// Grants are the grants that let a client start sessions here now: not
// revoked, superseded or expired; the newest version of each.
func (l *Local) Grants() []*identityv1.Grant {
	n := l.n
	n.mu.Lock()
	defer n.mu.Unlock()
	latest := map[string]*identityv1.Grant{}
	for _, g := range n.grants {
		if n.grantProblemLocked(g) != nil {
			continue
		}
		if cur := latest[g.GrantId]; cur == nil || cur.Version < g.Version {
			latest[g.GrantId] = g
		}
	}
	out := make([]*identityv1.Grant, 0, len(latest))
	for _, g := range latest {
		out = append(out, g)
	}
	return out
}
