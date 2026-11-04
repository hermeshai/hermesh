package relay

import (
	"net"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/metrics"
)

// A relay on several nodes: the hosted center runs the same relay as equal
// nodes behind one address (hermesh-cloud docs/multi-node-center-design.md).
// Each node holds its own connections; a Cluster tells it where the other
// nodes' are and what they changed, and opens tunnels to the machines they
// hold. Without one the relay is one process, as a self-hosted relay is.
//
// Nothing a Cluster says grants anything. Events carry no state: a node that
// hears one reads the Backend. A node that may have missed events reads it
// again (Resync), and checks every owner it caches against it every
// clusterCheck regardless (OwnerVersions).

// Cluster is what the nodes of a relay share.
type Cluster interface {
	// Start hands the cluster this node, once, before New returns.
	Start(n ClusterNode) error
	// NodeID names this node.
	NodeID() string

	// Connected records a connection this node holds, Reported a machine's
	// name and version on it, and Disconnected its end. The last two touch
	// the record only while it is still that connection's (its epoch), so a
	// late disconnect never removes a newer connection.
	Connected(ownerID string, p Presence) error
	Reported(ownerID string, p Presence) error
	Disconnected(ownerID string, p Presence) error
	// Presence lists the owner's connections on every node, and what is
	// remembered of each of its machines that was online.
	Presence(ownerID string) ([]Presence, map[string]Seen, error)
	// Locate is the connection holding a machine, nil when none does.
	Locate(ownerID, machineID string) (*Presence, error)

	// Publish tells the other nodes caching the owner what changed;
	// Subscribe and Unsubscribe start and stop this node hearing it.
	Publish(ownerID string, e Event) error
	Subscribe(ownerID string) error
	Unsubscribe(ownerID string)

	// OpenTunnel opens a stream to another node for a tunnel to a machine
	// it holds (ClusterNode.Tunnel there).
	OpenTunnel(node string, t *ClusterTunnel) (net.Conn, error)

	// Allow counts a use of limit by key on every node: false once n uses
	// fell in one window. An error leaves the node to count on its own.
	Allow(limit, key string, n int, window time.Duration) (bool, error)
}

// ClusterNode is what a Cluster calls on its node.
type ClusterNode interface {
	// Event is what another node published about an owner.
	Event(ownerID string, e Event)
	// Tunnel serves a stream another node opened to a machine held here.
	Tunnel(t *ClusterTunnel, conn net.Conn)
	// Resync: events may have been lost; every cached owner is read again.
	Resync()
}

// Presence is a connection as the nodes record it.
type Presence struct {
	Role relayv1.Role
	// Subject is the machine's or the client's id; the owner's for a
	// trusted device.
	Subject string
	Node    string
	// Epoch tells this connection from the subject's others.
	Epoch string
	// Name and Version are what a machine reported.
	Name, Version string
	Since         time.Time
}

// Seen is what the nodes remember of a machine that was online: when it
// last was, and what it last reported.
type Seen struct {
	At            time.Time
	Name, Version string
}

// Event says what changed for an owner.
type Event struct {
	Kind EventKind
	// ID is the machine (EventCert, EventPresence) or the inbox (EventInbox)
	// the event is about.
	ID string
	// Epoch is the connection that made a machine's presence change.
	Epoch string
}

type EventKind string

const (
	EventRevocations EventKind = "revocations"
	EventChain       EventKind = "chain"
	EventGrants      EventKind = "grants"
	EventCert        EventKind = "cert"
	EventPush        EventKind = "push"
	EventPresence    EventKind = "presence"
	// EventLists: what WatchOwner sends changed otherwise (pairing
	// requests, agents' cards).
	EventLists     EventKind = "lists"
	EventInbox     EventKind = "inbox"
	EventForgotten EventKind = "forgotten"
)

// ClusterTunnel is a tunnel a node forwards to the node holding its
// machine. The forwarding node checked the principal as for a tunnel of its
// own; the holding node checks again that the machine is still on that
// connection and that the grant still stands.
type ClusterTunnel struct {
	OwnerID, MachineID string
	// Epoch is the machine's connection the forwarding node found.
	Epoch        string
	Role         relayv1.Role
	SubjectID    string
	GrantID      string
	GrantVersion uint64
}

// OwnerVersions is a Backend that numbers each owner's changes of root
// chain and revocations (the hosted center): a node of several checks the
// owners it caches against it.
type OwnerVersions interface {
	// OwnerVersions has no entry for an owner the Backend no longer knows.
	OwnerVersions(ownerIDs []string) (map[string]uint64, error)
}

// RevocationsUpdater is a Backend that changes an owner's revocations in
// one transaction, so that two nodes revoking at once both count.
type RevocationsUpdater interface {
	UpdateRevocations(ownerID string, change func(cur *identityv1.RevocationList)) (*identityv1.RevocationList, error)
}

const (
	// clusterCheck: a node of several compares the owners it caches with the
	// Backend this often, and drops those unused here for tenantIdle.
	clusterCheck = 30 * time.Second
	tenantIdle   = 5 * time.Minute
)

var mTunnelsForwarded = metrics.Default.Counter("hermesh_relay_tunnels_forwarded_total")

type clusterNode struct{ s *Server }

func (n clusterNode) Event(ownerID string, e Event)          { n.s.onEvent(ownerID, e) }
func (n clusterNode) Tunnel(t *ClusterTunnel, conn net.Conn) { n.s.serveForwarded(t, conn) }
func (n clusterNode) Resync()                                { go n.s.reloadAll() }

// publish tells the other nodes what changed for an owner.
func (s *Server) publish(ownerID string, e Event) {
	if s.cluster == nil || ownerID == "" {
		return
	}
	if err := s.cluster.Publish(ownerID, e); err != nil {
		s.log.Warn("publishing to the other nodes", "owner", ownerID, "event", e.Kind, "err", err)
	}
}

// onEvent acts on what another node changed, as that node did itself.
func (s *Server) onEvent(ownerID string, e Event) {
	s.mu.Lock()
	t := s.tenants[ownerID]
	s.mu.Unlock()
	if t == nil {
		return // not cached here: read afresh once it is
	}
	switch e.Kind {
	case EventRevocations, EventChain:
		s.reload(t)
	case EventGrants:
		s.pushGrants(t)
	case EventCert:
		s.sendCert(t, e.ID)
	case EventPush:
		s.broadcastPushTargets(t)
	case EventPresence:
		s.superseded(t, e.ID, e.Epoch)
	case EventInbox:
		s.wakeInbox(t, e.ID)
	case EventForgotten:
		s.forget(ownerID)
		s.cluster.Unsubscribe(ownerID)
		return
	}
	s.wakeOwner(t)
}

// superseded closes this node's connection of a machine that connected
// again elsewhere: the newest connection wins, as on one node.
func (s *Server) superseded(t *tenant, machineID, epoch string) {
	s.mu.Lock()
	p := t.machines[machineID]
	s.mu.Unlock()
	if p == nil || epoch == "" || p.epoch == epoch {
		return
	}
	// Events come in any order: the record says which connection is newest.
	cur, err := s.cluster.Locate(t.ownerID, machineID)
	if err != nil || cur == nil || cur.Epoch == p.epoch {
		return
	}
	s.log.Info("machine connected to another node", "owner", t.ownerID, "machine", machineID, "node", cur.Node)
	p.sess.Close()
}

// reload reads an owner's root chain and revocations again (another node
// changed them, or events may have been lost) and acts on what changed.
func (s *Server) reload(t *tenant) {
	// The version first: a change after it is read again next time.
	version := s.ownerVersion(t.ownerID)
	rc, err := s.store.RootChain(t.ownerID)
	if err != nil {
		s.log.Warn("reloading an owner", "owner", t.ownerID, "err", err)
		return
	}
	if rc == nil {
		s.forget(t.ownerID)
		s.cluster.Unsubscribe(t.ownerID)
		return
	}
	rev, err := s.store.Revocations(t.ownerID)
	if err != nil {
		s.log.Warn("reloading an owner's revocations", "owner", t.ownerID, "err", err)
		return
	}
	s.mu.Lock()
	t.version = version
	s.mu.Unlock()
	if changed, err := t.trust.AcceptRootChain(rc); err != nil {
		s.log.Warn("reloading an owner's root chain", "owner", t.ownerID, "err", err)
	} else if changed {
		s.chainChanged(t)
	}
	s.revocationsChanged(t, rev)
}

// reloadAll reloads every owner this node caches.
func (s *Server) reloadAll() {
	s.mu.Lock()
	ts := make([]*tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	for _, t := range ts {
		s.reload(t)
	}
}

// ownerVersion is the Backend's count of an owner's changes (0 without one).
func (s *Server) ownerVersion(ownerID string) uint64 {
	vs, ok := s.store.(OwnerVersions)
	if !ok {
		return 0
	}
	m, err := vs.OwnerVersions([]string{ownerID})
	if err != nil {
		return 0
	}
	return m[ownerID]
}

// revocationsChanged installs revocations another node stored, closes the
// connections they revoke here and sends them to the machines here.
func (s *Server) revocationsChanged(t *tenant, cur *identityv1.RevocationList) {
	if cur.MinGrantVersions == nil {
		cur.MinGrantVersions = map[string]uint64{}
	}
	s.mu.Lock()
	same := proto.Equal(t.revocations, cur)
	t.revocations = cur
	var kick []*peer
	if !same {
		for p := range t.peers {
			if p.role != relayv1.Role_ROLE_OWNER && isRevoked(cur, p.subject) != nil {
				kick = append(kick, p)
			}
		}
	}
	s.mu.Unlock()
	if same {
		return
	}
	for _, p := range kick {
		s.log.Info("disconnecting revoked peer", "owner", t.ownerID, "subject", p.subject.ID)
		p.sess.Close()
	}
	s.broadcastMachineUpdate(t, &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Revocations{Revocations: cur}})
	s.pushGrants(t)
}

// sendCert gives a machine connected here the cert another node stored for
// it.
func (s *Server) sendCert(t *tenant, machineID string) {
	s.mu.Lock()
	watched := len(t.watchers[machineID]) > 0
	s.mu.Unlock()
	if !watched {
		return
	}
	certs, err := s.store.MachineCerts(t.ownerID)
	if err != nil || certs[machineID] == nil {
		return
	}
	u := &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_MachineCert{MachineCert: certs[machineID]}}
	s.mu.Lock()
	for ch := range t.watchers[machineID] {
		select {
		case ch <- u:
		default:
		}
	}
	s.mu.Unlock()
}

// clusterLoop keeps the owners a node caches true to the Backend (events
// may be lost) and drops those no longer used here.
func (s *Server) clusterLoop() {
	tk := time.NewTicker(clusterCheck)
	defer tk.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-tk.C:
			s.checkTenants(now)
		}
	}
}

func (s *Server) checkTenants(now time.Time) {
	s.mu.Lock()
	var ids, idle []string
	for id, t := range s.tenants {
		if len(t.peers) == 0 && now.Sub(t.used) > tenantIdle {
			delete(s.tenants, id)
			idle = append(idle, id)
			continue
		}
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range idle {
		s.cluster.Unsubscribe(id)
	}
	for _, id := range ids {
		// A subscription lost on the way (a failed load) comes back.
		if err := s.cluster.Subscribe(id); err != nil {
			s.log.Warn("hearing an owner's events", "owner", id, "err", err)
		}
	}
	vs, ok := s.store.(OwnerVersions)
	if !ok {
		return
	}
	for len(ids) > 0 {
		batch := ids[:min(len(ids), 1000)]
		ids = ids[len(batch):]
		versions, err := vs.OwnerVersions(batch)
		if err != nil {
			s.log.Warn("checking cached owners", "err", err)
			return
		}
		for _, id := range batch {
			s.mu.Lock()
			t := s.tenants[id]
			stale := t != nil && t.version != versions[id]
			s.mu.Unlock()
			if _, known := versions[id]; t != nil && (stale || !known) {
				s.reload(t)
			}
		}
	}
}

// presenceOf is how the nodes record p.
func (s *Server) presenceOf(p *peer) Presence {
	return Presence{Role: p.role, Subject: p.subject.ID, Node: s.cluster.NodeID(), Epoch: p.epoch, Since: p.since}
}

// livePresence is an owner's connections on every node.
type livePresence struct {
	machines map[string]Presence
	clients  map[string]bool
	owner    bool
	seen     map[string]Seen
}

// clusterPresence is the owner's connections on every node; false when
// this node has no cluster, or it can't say (the node then says what it
// holds).
func (s *Server) clusterPresence(ownerID string) (*livePresence, bool) {
	if s.cluster == nil {
		return nil, false
	}
	all, seen, err := s.cluster.Presence(ownerID)
	if err != nil {
		s.log.Warn("reading presence", "owner", ownerID, "err", err)
		return nil, false
	}
	lp := &livePresence{machines: map[string]Presence{}, clients: map[string]bool{}, seen: seen}
	for _, p := range all {
		switch p.Role {
		case relayv1.Role_ROLE_MACHINE:
			lp.machines[p.Subject] = p
		case relayv1.Role_ROLE_CLIENT:
			lp.clients[p.Subject] = true
		case relayv1.Role_ROLE_OWNER:
			lp.owner = true
		}
	}
	return lp, true
}

// forwardTunnel takes a tunnel to a machine another node holds there.
func (s *Server) forwardTunnel(p *peer, st net.Conn, machineID string) {
	at, err := s.cluster.Locate(p.t.ownerID, machineID)
	if err != nil || at == nil || at.Node == s.cluster.NodeID() {
		return
	}
	ct := &ClusterTunnel{OwnerID: p.t.ownerID, MachineID: machineID, Epoch: at.Epoch, Role: p.role, SubjectID: p.subject.ID}
	if g := p.subject.Grant; g != nil {
		ct.GrantID, ct.GrantVersion = g.GrantId, g.Version
	}
	fs, err := s.cluster.OpenTunnel(at.Node, ct)
	if err != nil {
		s.log.Warn("forwarding a tunnel", "owner", p.t.ownerID, "machine", machineID, "node", at.Node, "err", err)
		return
	}
	defer fs.Close()
	mTunnelsForwarded.Inc()
	splice(st, fs)
}

// serveForwarded serves a tunnel another node forwarded to a machine held
// here, after checking again what can have changed since that node did.
func (s *Server) serveForwarded(ct *ClusterTunnel, conn net.Conn) {
	defer conn.Close()
	s.mu.Lock()
	var machine *peer
	var rev *identityv1.RevocationList
	if t := s.tenants[ct.OwnerID]; t != nil {
		machine, rev = t.machines[ct.MachineID], t.revocations
	}
	s.mu.Unlock()
	if machine == nil || machine.epoch != ct.Epoch {
		return
	}
	switch ct.Role {
	case relayv1.Role_ROLE_CLIENT:
		g := &identityv1.Grant{GrantId: ct.GrantID, Version: ct.GrantVersion}
		if isRevoked(rev, &identity.Subject{Kind: identity.SubjectClient, ID: ct.SubjectID, Grant: g}) != nil {
			return
		}
	case relayv1.Role_ROLE_OWNER:
	default:
		return
	}
	s.spliceToMachine(conn, machine, ct.MachineID, ct.Role, ct.SubjectID)
}
