// Package relay implements hermesh-relay: admission, presence, pairing
// rendezvous, tunnel routing and revocation fan-out. The relay is untrusted:
// it never signs credentials, vouches for requests, serves executable code
// or sees session contents.
//
// The same core serves a self-hosted relay (one owner, claimed with a code)
// and the hosted center (many owners, each isolated in its own tenant).
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type Config struct {
	// DataDir holds the SQLite store of a self-hosted relay (ignored when
	// Backend is set).
	DataDir string
	Logger  *slog.Logger
	// PairingTTL defaults to 10 minutes.
	PairingTTL time.Duration

	// Backend replaces the SQLite store (the hosted center).
	Backend Backend
	// Hosted serves many owners: no claim code, owners come from Backend,
	// and pairing requests need not name an owner (an account claims them).
	Hosted bool
	// AccountHint names, masked, the account an owner belongs to (hosted
	// mode): requesters see it when the owner claims their request.
	AccountHint func(ownerID string) string
	// Claim links a new owner in hosted mode (the code is the account's
	// one-time link code). The root and device certs are already checked
	// for self-consistency; the hook must call RegisterOwner.
	Claim func(ctx context.Context, req *relayv1.ClaimRequest) error
	// ConsoleURL is shown to requesters of a hosted center.
	ConsoleURL string
	// PairingsPerMinute bounds pairing requests per client address
	// (default 10; a hosted center serves many people behind shared NATs).
	PairingsPerMinute int
	// MaxPendingPairings bounds unapproved pairing requests (default 20).
	MaxPendingPairings int
	// MaxOwnerPendingPairings bounds unapproved requests made for one owner
	// with its connection address (default 20): the address is not secret,
	// so a stranger could otherwise flood the owner's signer.
	MaxOwnerPendingPairings int
	// Pusher sends notifications to the owners' iPhones (push.go); nil
	// sends none.
	Pusher Pusher
}

type Server struct {
	cfg     Config
	log     *slog.Logger
	store   Backend
	sqlite  *Store // self-hosted only
	relayID string
	limiter *rateLimiter
	// noticeLimit bounds the notices each machine sends.
	noticeLimit *rateLimiter
	pushMem     memoryPush
	// The post office (messages.go): its store when the Backend has none,
	// its limits, and what hears of each message it takes in.
	messageMem   memoryMessages
	messageLimit *rateLimiter
	pairLimit    *rateLimiter
	messageHook  func(ownerID, inbox string, ring *string)
	ownerHook    func(ownerID string)
	eraseHooks   []func(ownerID string)
	doorbells    Doorbells
	expiredAt    time.Time

	mu      sync.Mutex
	tenants map[string]*tenant
	// single is the claimed owner of a self-hosted relay ("" until claimed).
	single string
}

// tenant is one owner's live state. Peers of different owners never see or
// reach each other.
type tenant struct {
	ownerID     string
	trust       *identity.Trust
	machines    map[string]*peer
	peers       map[*peer]struct{}
	reported    map[string]*relayv1.ReportStatusRequest // name and version
	lastSeen    map[string]time.Time
	watchers    map[string]map[chan *relayv1.MachineUpdate]struct{}
	revocations *identityv1.RevocationList
	// ownerWatchers are woken when what WatchOwner sends may have changed.
	ownerWatchers map[chan struct{}]struct{}
	// inboxWatchers are woken when a message arrives for their inbox.
	inboxWatchers map[string]map[chan struct{}]struct{}
}

type peer struct {
	t       *tenant
	role    relayv1.Role
	subject *identity.Subject
	// signer is the root key that signed an owner device's credential: the
	// device is cut off once that key leaves the chain.
	signer string
	// deviceCert is an owner device's credential (its push registration
	// goes to machines with it).
	deviceCert *identityv1.Signed
	sess       *yamux.Session
	remote     string
}

func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.PairingTTL == 0 {
		cfg.PairingTTL = 10 * time.Minute
	}
	if cfg.MaxPendingPairings == 0 {
		cfg.MaxPendingPairings = 20
	}
	if cfg.MaxOwnerPendingPairings == 0 {
		cfg.MaxOwnerPendingPairings = 20
	}
	if cfg.PairingsPerMinute == 0 {
		cfg.PairingsPerMinute = 10
	}
	s := &Server{cfg: cfg, log: cfg.Logger, store: cfg.Backend, tenants: map[string]*tenant{}, limiter: newRateLimiter(cfg.PairingsPerMinute, time.Minute),
		noticeLimit: newRateLimiter(noticesPerMinute, time.Minute), messageLimit: newRateLimiter(messagesPerMinute, time.Minute),
		pairLimit: newRateLimiter(pairPerHour, time.Hour)}
	if s.store == nil {
		if cfg.Hosted {
			return nil, errors.New("a hosted relay needs a Backend")
		}
		st, err := OpenStore(filepath.Join(cfg.DataDir, "relay.db"))
		if err != nil {
			return nil, err
		}
		s.store, s.sqlite = st, st
	}
	id, err := s.store.RelayID()
	if err != nil {
		return nil, err
	}
	s.relayID = id
	if s.sqlite != nil {
		owner, err := s.sqlite.OwnerID()
		if err != nil {
			return nil, err
		}
		if owner != "" {
			if _, err := s.loadTenant(owner); err != nil {
				return nil, fmt.Errorf("stored owner: %w", err)
			}
			s.single = owner
		}
	}
	// Orders kept past their time by a relay stopped meanwhile.
	if es, ok := s.store.(EraseStore); ok {
		if err := es.PurgeErases(time.Now()); err != nil {
			s.log.Warn("purging erase orders", "err", err)
		}
	}
	s.registerMetrics()
	return s, nil
}

func (s *Server) Close() error { return s.store.Close() }

var (
	mAdmission         = metrics.Default.Histogram("hermesh_relay_admission_seconds")
	mAdmissionsOK      = metrics.Default.Counter(`hermesh_relay_admissions_total{result="ok"}`)
	mAdmissionsRefused = metrics.Default.Counter(`hermesh_relay_admissions_total{result="refused"}`)
	mTunnelOpen        = metrics.Default.Histogram("hermesh_relay_tunnel_open_seconds")
	mTunnelsTotal      = metrics.Default.Counter("hermesh_relay_tunnels_total")
	mTunnelsActive     = metrics.Default.Gauge("hermesh_relay_tunnels_active")
	mTunnelBytes       = metrics.Default.Counter("hermesh_relay_tunnel_bytes_total")
)

func (s *Server) registerMetrics() {
	for _, role := range []relayv1.Role{relayv1.Role_ROLE_MACHINE, relayv1.Role_ROLE_CLIENT, relayv1.Role_ROLE_OWNER} {
		role := role
		name := strings.ToLower(strings.TrimPrefix(role.String(), "ROLE_"))
		metrics.Default.GaugeFunc(`hermesh_relay_peers{role="`+name+`"}`, func() int64 {
			s.mu.Lock()
			defer s.mu.Unlock()
			n := int64(0)
			for _, t := range s.tenants {
				for p := range t.peers {
					if p.role == role {
						n++
					}
				}
			}
			return n
		})
	}
}

// ClaimCode returns the code to claim a self-hosted relay, or "" when it is
// claimed (or hosted).
func (s *Server) ClaimCode() (string, error) {
	if s.sqlite == nil || s.singleOwner() != "" {
		return "", nil
	}
	return s.sqlite.ClaimCode()
}

func (s *Server) RelayID() string { return s.relayID }

// SingleOwner is the owner of a claimed self-hosted relay ("" before it is
// claimed, and for a hosted center).
func (s *Server) SingleOwner() string { return s.singleOwner() }

func (s *Server) singleOwner() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.single
}

// loadTenant returns the owner's tenant, loading it from the backend; nil
// with no error for an owner the backend does not know.
func (s *Server) loadTenant(ownerID string) (*tenant, error) {
	s.mu.Lock()
	t := s.tenants[ownerID]
	s.mu.Unlock()
	if t != nil {
		return t, nil
	}
	if s.sqlite != nil && s.singleOwner() != "" && ownerID != s.singleOwner() {
		return nil, nil
	}
	rc, err := s.store.RootChain(ownerID)
	if err != nil || rc == nil {
		return nil, err
	}
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(rc); err != nil {
		return nil, fmt.Errorf("stored root chain of %s: %w", ownerID, err)
	}
	rev, err := s.store.Revocations(ownerID)
	if err != nil {
		return nil, err
	}
	t = &tenant{ownerID: ownerID, trust: trust, machines: map[string]*peer{}, peers: map[*peer]struct{}{},
		reported: map[string]*relayv1.ReportStatusRequest{}, lastSeen: map[string]time.Time{},
		watchers: map[string]map[chan *relayv1.MachineUpdate]struct{}{}, revocations: rev, ownerWatchers: map[chan struct{}]struct{}{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.tenants[ownerID]; cur != nil {
		return cur, nil
	}
	s.tenants[ownerID] = t
	return t, nil
}

// tenantFor returns the tenant of an owner known to this relay.
func (s *Server) tenantFor(ownerID string) (*tenant, error) {
	t, err := s.loadTenant(ownerID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("unknown owner %q", ownerID)
	}
	return t, nil
}

// RegisterOwner makes a new owner known to a hosted relay (the center calls
// it once the owner's first device linked an account). The root chain must
// be self consistent for the owner it names; it vouches for nothing else.
func (s *Server) RegisterOwner(rootChain *identityv1.RootChain) (string, error) {
	ownerID := identity.ChainOwner(rootChain)
	if _, err := identity.NewTrust(ownerID).AcceptRootChain(rootChain); err != nil {
		return "", err
	}
	if cur, err := s.store.RootChain(ownerID); err != nil {
		return "", err
	} else if cur == nil {
		if err := s.store.SetRootChain(ownerID, rootChain); err != nil {
			return "", err
		}
	}
	t, err := s.tenantFor(ownerID)
	if err != nil {
		return "", err
	}
	if err := s.acceptRootChain(t, rootChain); err != nil {
		return "", err
	}
	return ownerID, nil
}

// acceptRootChain installs a longer root chain for t and tells its machines.
func (s *Server) acceptRootChain(t *tenant, c *identityv1.RootChain) error {
	defer s.wakeOwner(t)
	changed, err := t.trust.AcceptRootChain(c)
	if err != nil || !changed {
		return err
	}
	if err := s.store.SetRootChain(t.ownerID, t.trust.RootChain()); err != nil {
		return err
	}
	s.broadcastMachineUpdate(t, &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_RootChain{RootChain: t.trust.RootChain()}})
	// A removed device gets no more notices.
	s.broadcastPushTargets(t)
	// A device removed from the chain is disconnected now, not when it
	// next connects.
	current := map[string]bool{}
	for _, rk := range t.trust.Root().GetRootKeys() {
		if pub, err := identity.ParsePublicKey(rk.PublicKey); err == nil {
			current[identity.KeyID(pub)] = true
		}
	}
	s.mu.Lock()
	var kick []*peer
	for p := range t.peers {
		if p.role == relayv1.Role_ROLE_OWNER && !current[p.signer] {
			kick = append(kick, p)
		}
	}
	s.mu.Unlock()
	for _, p := range kick {
		s.log.Info("disconnecting a removed device", "owner", t.ownerID)
		p.sess.Close()
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Mount(mux)
	return mux
}

// Mount adds the relay endpoints to mux.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/v1/connect", s.handleConnect)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	mux.Handle(relayv1connect.NewRelayPublicServiceHandler(&publicService{s: s}))
}

// ---------------------------------------------------------------------------
// Admission.

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
	if err != nil {
		return
	}
	conn := transport.NetConnFromWS(ws)
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	nonce := make([]byte, 32)
	rand.Read(nonce)
	if err := transport.WriteFrame(conn, &relayv1.Challenge{Nonce: nonce, RelayId: s.relayID, ServerTime: timestamppb.Now()}); err != nil {
		return
	}
	var hello relayv1.Hello
	if err := transport.ReadFrame(conn, &hello); err != nil {
		return
	}
	admitStart := time.Now()
	p, err := s.admit(&hello, nonce)
	mAdmission.Since(admitStart)
	if err != nil {
		mAdmissionsRefused.Inc()
		s.log.Info("admission refused", "remote", r.RemoteAddr, "role", hello.Role, "err", err)
		transport.WriteFrame(conn, &relayv1.HelloResult{Ok: false, Error: err.Error()})
		return
	}
	if err := transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true}); err != nil {
		return
	}
	mAdmissionsOK.Inc()
	conn.SetDeadline(time.Time{})
	cfg := transport.YamuxConfig()
	if p.role == relayv1.Role_ROLE_OWNER {
		cfg.EnableKeepAlive = false // keepAlive, more patient
	}
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		return
	}
	if p.role == relayv1.Role_ROLE_OWNER {
		go keepAlive(sess, cfg.KeepAliveInterval, ownerQuiet)
	}
	p.sess, p.remote = sess, r.RemoteAddr
	s.register(p)
	defer s.unregister(p)
	if p.role == relayv1.Role_ROLE_OWNER {
		s.mu.Lock()
		hook := s.ownerHook
		s.mu.Unlock()
		if hook != nil {
			go hook(p.t.ownerID)
		}
	}
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go s.handleStream(p, st)
	}
}

// ownerQuiet is how long an owner device may leave the relay's pings
// unanswered before its connection is closed. The iPhone app answers none
// while it is suspended in the background, and with its connection kept
// for a few minutes it comes back in a ping rather than a dial
// (internal/owner connectOrKeep). Machines and agents are closed after
// 30–45 s (yamux's keepalive), as whether a machine is online is shown.
const ownerQuiet = 5 * time.Minute

// keepAlive pings sess every interval, and closes it once quiet went by
// since a ping was last answered.
func keepAlive(sess *yamux.Session, every, quiet time.Duration) {
	answered := time.Now()
	for {
		select {
		case <-sess.CloseChan():
			return
		case <-time.After(every):
		}
		if _, err := sess.Ping(); err == nil {
			answered = time.Now()
		} else if time.Since(answered) > quiet {
			sess.Close()
			return
		}
	}
}

func (s *Server) admit(h *relayv1.Hello, nonce []byte) (*peer, error) {
	ownerID := identity.ChainOwner(h.GetCredentials().GetRootChain())
	if ownerID == "" {
		return nil, errors.New("missing root chain")
	}
	// An owner that erased everything moments ago: its devices ask for its
	// order (docs/erase-everything.md). Only for an owner not served here.
	errErased := errors.New("this relay no longer serves this device's owner (the owner erased everything)")
	erased := func() bool { order, _, _ := s.eraseOrder(ownerID); return order != nil }
	if s.sqlite != nil && s.singleOwner() != ownerID && erased() {
		return nil, errErased
	}
	if s.sqlite != nil && s.singleOwner() == "" {
		return nil, errors.New("relay is not claimed yet")
	}
	t, err := s.loadTenant(ownerID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		if s.sqlite == nil {
			if erased() {
				return nil, errErased
			}
			return nil, errors.New("this center no longer serves this device's owner (the owner identity was reset); pair this device again")
		}
		return nil, errors.New("this relay does not serve that owner")
	}
	before := t.trust.RootVersion()
	sub, err := t.trust.VerifyCredentials(h.Credentials, time.Now())
	superseded := false
	if err != nil {
		// A machine or agent whose credential was replaced (owner recovery or a
		// lowered grant) is admitted with the newer stored credential for the
		// same key; it then fetches that credential.
		var ferr error
		if sub, ferr = s.supersedingCredential(t, h); ferr != nil {
			return nil, err
		}
		superseded = true
	}
	if t.trust.RootVersion() != before {
		s.store.SetRootChain(t.ownerID, t.trust.RootChain())
		s.broadcastMachineUpdate(t, &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_RootChain{RootChain: t.trust.RootChain()}})
	}
	want := map[relayv1.Role]identity.SubjectKind{
		relayv1.Role_ROLE_MACHINE: identity.SubjectMachine,
		relayv1.Role_ROLE_CLIENT:  identity.SubjectClient,
		relayv1.Role_ROLE_OWNER:   identity.SubjectOwnerDevice,
	}
	if k, ok := want[h.Role]; !ok || k != sub.Kind {
		return nil, errors.New("credentials do not match role")
	}
	if err := identity.VerifyRaw(sub.PublicKey, transport.AdmitMessage(s.relayID, nonce, h.Role), h.Proof); err != nil {
		return nil, errors.New("bad admission proof")
	}
	if err := s.checkNotRevoked(t, sub); err != nil {
		// A lowered grant: admit with the newer version for the same key.
		alt, ferr := s.supersedingCredential(t, h)
		if superseded || ferr != nil || s.checkNotRevoked(t, alt) != nil {
			return nil, err
		}
		sub, superseded = alt, true
	}
	if sub.Kind == identity.SubjectMachine {
		known, err := s.store.MachineCerts(t.ownerID)
		if err != nil {
			return nil, err
		}
		if _, ok := known[sub.ID]; !ok {
			// Machines become known through pairing (or the owner publishing
			// a cert); a removed machine's cert is gone. (A machine reads this
			// text, and the other refusals that mean it is no longer its
			// owner's: internal/machine gone.)
			return nil, errors.New("unknown or removed machine")
		}
		if !superseded {
			s.store.PutMachineCert(t.ownerID, sub.ID, h.Credentials.GetMachineCert())
		}
	}
	if sub.Kind == identity.SubjectClient && !superseded {
		s.store.PutGrant(t.ownerID, sub.Grant, h.Credentials.GetGrant())
	}
	return &peer{t: t, role: h.Role, subject: sub, signer: h.Credentials.GetDeviceCert().GetSignerKeyId(), deviceCert: h.Credentials.GetDeviceCert()}, nil
}

func (s *Server) supersedingCredential(t *tenant, h *relayv1.Hello) (*identity.Subject, error) {
	switch c := h.GetCredentials().GetSubject().(type) {
	case *identityv1.Credentials_MachineCert:
		old := &identityv1.MachineCert{}
		if err := identity.Unverified(c.MachineCert, old); err != nil {
			return nil, err
		}
		certs, err := s.store.MachineCerts(t.ownerID)
		if err != nil {
			return nil, err
		}
		stored := certs[identity.MachineID(old.MachinePublicKey)]
		if stored == nil {
			return nil, errors.New("no stored machine cert")
		}
		nc, pub, err := t.trust.VerifyMachineCert(stored)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(nc.MachinePublicKey, old.MachinePublicKey) {
			return nil, errors.New("key mismatch")
		}
		return &identity.Subject{Kind: identity.SubjectMachine, ID: nc.MachineId, PublicKey: pub, Machine: nc}, nil
	case *identityv1.Credentials_Grant:
		old := &identityv1.Grant{}
		if err := identity.Unverified(c.Grant, old); err != nil {
			return nil, err
		}
		stored, err := s.store.GrantForClient(t.ownerID, identity.ClientID(old.ClientPublicKey))
		if err != nil || stored == nil {
			return nil, errors.New("no stored grant")
		}
		g, pub, err := t.trust.VerifyGrant(stored, time.Now())
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(g.ClientPublicKey, old.ClientPublicKey) {
			return nil, errors.New("key mismatch")
		}
		return &identity.Subject{Kind: identity.SubjectClient, ID: g.ClientId, PublicKey: pub, Grant: g}, nil
	}
	return nil, errors.New("no superseding credential")
}

func (s *Server) checkNotRevoked(t *tenant, sub *identity.Subject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return isRevoked(t.revocations, sub)
}

// isRevoked reports why sub is no longer allowed, if it is not.
func isRevoked(r *identityv1.RevocationList, sub *identity.Subject) error {
	switch sub.Kind {
	case identity.SubjectMachine:
		for _, id := range r.RevokedMachineIds {
			if id == sub.ID {
				return errors.New("machine removed")
			}
		}
	case identity.SubjectClient:
		for _, id := range r.RevokedGrantIds {
			if id == sub.Grant.GrantId {
				return errors.New("grant revoked")
			}
		}
		if min := r.MinGrantVersions[sub.Grant.GrantId]; sub.Grant.Version < min {
			return errors.New("grant superseded")
		}
	}
	return nil
}

func (s *Server) register(p *peer) {
	t := p.t
	s.mu.Lock()
	t.peers[p] = struct{}{}
	var old *peer
	if p.role == relayv1.Role_ROLE_MACHINE {
		old = t.machines[p.subject.ID]
		t.machines[p.subject.ID] = p
		t.lastSeen[p.subject.ID] = time.Now()
	}
	s.wakeOwnerLocked(t)
	s.mu.Unlock()
	if old != nil {
		old.sess.Close()
	}
	s.store.LogConnection(t.ownerID, int(p.role), p.subject.ID, p.remote, "connect")
	s.log.Info("peer connected", "owner", t.ownerID, "role", p.role, "subject", p.subject.ID, "remote", p.remote)
}

func (s *Server) unregister(p *peer) {
	t := p.t
	s.mu.Lock()
	delete(t.peers, p)
	if p.role == relayv1.Role_ROLE_MACHINE && t.machines[p.subject.ID] == p {
		delete(t.machines, p.subject.ID)
		t.lastSeen[p.subject.ID] = time.Now()
	}
	s.wakeOwnerLocked(t)
	s.mu.Unlock()
	p.sess.Close()
	s.store.LogConnection(t.ownerID, int(p.role), p.subject.ID, p.remote, "disconnect")
	s.log.Info("peer disconnected", "owner", t.ownerID, "role", p.role, "subject", p.subject.ID)
}

// ---------------------------------------------------------------------------
// Streams.

type peerKey struct{}

func peerFrom(ctx context.Context) *peer { p, _ := ctx.Value(peerKey{}).(*peer); return p }

func (s *Server) handleStream(p *peer, st net.Conn) {
	st.SetReadDeadline(time.Now().Add(10 * time.Second))
	var h relayv1.StreamHeader
	if err := transport.ReadFrame(st, &h); err != nil {
		st.Close()
		return
	}
	st.SetReadDeadline(time.Time{})
	switch h.Kind {
	case relayv1.StreamKind_STREAM_KIND_CONTROL:
		defer st.Close()
		ctx := context.WithValue(context.Background(), peerKey{}, p)
		transport.ServeH2(ctx, st, s.controlMux(p.role))
	case relayv1.StreamKind_STREAM_KIND_TUNNEL:
		s.handleTunnel(p, st, h.MachineId)
	default:
		st.Close()
	}
}

func (s *Server) controlMux(role relayv1.Role) http.Handler {
	mux := http.NewServeMux()
	switch role {
	case relayv1.Role_ROLE_MACHINE:
		mux.Handle(relayv1connect.NewMachineControlServiceHandler(&machineControl{s: s}, transport.NoCompression))
		mux.Handle(relayv1connect.NewMessageServiceHandler(&messageService{s: s}, transport.NoCompression))
	case relayv1.Role_ROLE_CLIENT:
		mux.Handle(relayv1connect.NewClientControlServiceHandler(&clientControl{s: s}, transport.NoCompression))
		mux.Handle(relayv1connect.NewMessageServiceHandler(&messageService{s: s}, transport.NoCompression))
	case relayv1.Role_ROLE_OWNER:
		path, h := relayv1connect.NewOwnerControlServiceHandler(&ownerControl{s: s}, transport.NoCompression)
		mux.Handle(path, countOwnerRPCs(h))
	}
	return mux
}

// mOwnerRPCs counts the owner devices' control calls by method: what an
// owner's app costs the relay (docs/state-sync.md §8). Nothing per owner.
var mOwnerRPCs = func() map[string]*metrics.Counter {
	m := map[string]*metrics.Counter{}
	methods := relayv1.File_hermesh_relay_v1_relay_proto.Services().ByName("OwnerControlService").Methods()
	for i := range methods.Len() {
		name := string(methods.Get(i).Name())
		m["/"+relayv1connect.OwnerControlServiceName+"/"+name] = metrics.Default.Counter(`hermesh_relay_owner_rpcs_total{method="` + name + `"}`)
	}
	return m
}()

func countOwnerRPCs(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := mOwnerRPCs[r.URL.Path]; c != nil {
			c.Inc()
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleTunnel(p *peer, st net.Conn, machineID string) {
	defer st.Close()
	if p.role != relayv1.Role_ROLE_CLIENT && p.role != relayv1.Role_ROLE_OWNER {
		return
	}
	if p.role == relayv1.Role_ROLE_CLIENT {
		if !identity.Allows(p.subject.Grant.MachineIds, machineID) || s.checkNotRevoked(p.t, p.subject) != nil {
			return
		}
	}
	s.mu.Lock()
	machine := p.t.machines[machineID] // only this owner's machines
	s.mu.Unlock()
	if machine == nil {
		return
	}
	openStart := time.Now()
	ns, err := machine.sess.OpenStream()
	if err != nil {
		return
	}
	mTunnelOpen.Since(openStart)
	mTunnelsTotal.Inc()
	mTunnelsActive.Add(1)
	defer mTunnelsActive.Add(-1)
	defer ns.Close()
	tid := identity.RandomCode(12)
	hdr := &relayv1.StreamHeader{Kind: relayv1.StreamKind_STREAM_KIND_TUNNEL, MachineId: machineID,
		PrincipalRole: p.role, SubjectId: p.subject.ID, TunnelId: tid, TraceId: tid}
	if err := transport.WriteFrame(ns, hdr); err != nil {
		return
	}
	splice(st, ns)
}

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		n, _ := io.Copy(dst, src)
		mTunnelBytes.Add(n)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

// ---------------------------------------------------------------------------
// Revocation fan-out.

// ApplyRevocations merges in into the owner's revocations, disconnects
// whatever they revoke and pushes them to the owner's machines. Revocations only
// ever reduce permissions, so the embedding server may call this directly.
func (s *Server) ApplyRevocations(ownerID string, in *identityv1.RevocationList) error {
	t, err := s.tenantFor(ownerID)
	if err != nil {
		return err
	}
	return s.applyRevocations(t, in)
}

func (s *Server) applyRevocations(t *tenant, in *identityv1.RevocationList) error {
	defer s.wakeOwner(t)
	s.mu.Lock()
	cur := proto.Clone(t.revocations).(*identityv1.RevocationList)
	s.mu.Unlock()
	in = proto.Clone(in).(*identityv1.RevocationList)
	in.OwnerId = t.ownerID
	MergeRevocations(cur, in)
	grants, err := s.store.Grants(t.ownerID)
	if err != nil {
		return err
	}
	// An agent whose machines are all removed can reach nothing: revoke it
	// too. Revoking only lowers permissions, so it needs no root signature.
	removed := setOf(cur.RevokedMachineIds)
	var stranded []string
	for _, sg := range grants {
		g := &identityv1.Grant{}
		if identity.Unverified(sg, g) == nil && len(g.MachineIds) > 0 && len(liveMachines(g.MachineIds, removed)) == 0 {
			stranded = append(stranded, g.GrantId)
		}
	}
	MergeRevocations(cur, &identityv1.RevocationList{RevokedGrantIds: stranded})
	if err := s.store.SetRevocations(t.ownerID, cur); err != nil {
		return err
	}
	for _, id := range in.RevokedMachineIds {
		s.store.DeleteMachineCert(t.ownerID, id)
	}
	// Revoked agents leave the list. What refuses them is the revocation
	// list, kept apart and never pruned, here and on every machine.
	revoked := setOf(cur.RevokedGrantIds)
	var gone []string
	for _, sg := range grants {
		g := &identityv1.Grant{}
		if identity.Unverified(sg, g) == nil && revoked[g.GrantId] {
			if err := s.store.DeleteGrant(t.ownerID, g.GrantId); err != nil {
				s.log.Warn("forgetting a revoked grant", "owner", t.ownerID, "grant", g.GrantId, "err", err)
			}
			gone = append(gone, g.ClientId)
		}
	}
	s.dropInboxes(t, gone, in.RevokedMachineIds)
	s.mu.Lock()
	t.revocations = cur
	var kick []*peer
	for p := range t.peers {
		if p.role != relayv1.Role_ROLE_OWNER {
			if err := isRevoked(cur, p.subject); err != nil {
				kick = append(kick, p)
			}
		}
	}
	s.mu.Unlock()
	for _, p := range kick {
		s.log.Info("disconnecting revoked peer", "owner", t.ownerID, "subject", p.subject.ID)
		p.sess.Close()
	}
	s.broadcastMachineUpdate(t, &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Revocations{Revocations: cur}})
	s.pushGrants(t)
	return nil
}

func setOf(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// liveMachines is scope without removed machines ("*" stays).
func liveMachines(scope []string, removed map[string]bool) []string {
	out := []string{}
	for _, id := range scope {
		if !removed[id] {
			out = append(out, id)
		}
	}
	return out
}

// MergeRevocations folds in into cur (union of ids, max of versions).
func MergeRevocations(cur, in *identityv1.RevocationList) {
	add := func(dst *[]string, ids []string) {
		seen := map[string]bool{}
		for _, id := range *dst {
			seen[id] = true
		}
		for _, id := range ids {
			if !seen[id] {
				*dst = append(*dst, id)
				seen[id] = true
			}
		}
	}
	add(&cur.RevokedGrantIds, in.RevokedGrantIds)
	add(&cur.RevokedMachineIds, in.RevokedMachineIds)
	if cur.MinGrantVersions == nil {
		cur.MinGrantVersions = map[string]uint64{}
	}
	for k, v := range in.MinGrantVersions {
		if v > cur.MinGrantVersions[k] {
			cur.MinGrantVersions[k] = v
		}
	}
	if cur.OwnerId == "" {
		cur.OwnerId = in.OwnerId
	}
}

func (s *Server) broadcastMachineUpdate(t *tenant, u *relayv1.MachineUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, set := range t.watchers {
		for ch := range set {
			select {
			case ch <- u:
			default:
			}
		}
	}
}

// MachineEntries lists an owner's machines with presence, name and version.
func (s *Server) MachineEntries(ownerID string) ([]*relayv1.MachineEntry, error) {
	t, err := s.tenantFor(ownerID)
	if err != nil {
		return nil, err
	}
	return s.machineEntries(t, nil, false)
}

func (s *Server) machineEntries(t *tenant, filter func(string) bool, onlineOnly bool) ([]*relayv1.MachineEntry, error) {
	certs, err := s.store.MachineCerts(t.ownerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*relayv1.MachineEntry
	for id, c := range certs {
		if filter != nil && !filter(id) {
			continue
		}
		_, online := t.machines[id]
		if onlineOnly && !online {
			continue
		}
		r := t.reported[id]
		e := &relayv1.MachineEntry{MachineCert: c, Online: online, Name: r.GetName(), Version: r.GetVersion()}
		if ls, ok := t.lastSeen[id]; ok {
			e.LastSeen = timestamppb.New(ls)
		}
		out = append(out, e)
	}
	return out, nil
}

// ClientEntries lists an owner's agents that are not revoked (latest grant,
// presence, machines that still exist).
func (s *Server) ClientEntries(ownerID string) ([]*relayv1.ClientEntry, error) {
	t, err := s.tenantFor(ownerID)
	if err != nil {
		return nil, err
	}
	return s.clientEntries(t)
}

func (s *Server) clientEntries(t *tenant) ([]*relayv1.ClientEntry, error) {
	grants, err := s.store.Grants(t.ownerID)
	if err != nil {
		return nil, err
	}
	online := map[string]bool{}
	s.mu.Lock()
	for p := range t.peers {
		if p.role == relayv1.Role_ROLE_CLIENT {
			online[p.subject.ID] = true
		}
	}
	rev := t.revocations
	s.mu.Unlock()
	removed := setOf(rev.RevokedMachineIds)
	cards, _ := s.messageStore().Cards(t.ownerID)
	var out []*relayv1.ClientEntry
	for _, sg := range grants {
		g := &identityv1.Grant{}
		if err := identity.Unverified(sg, g); err != nil {
			continue
		}
		// Rows revoked before revocations forgot them are not listed either.
		if isRevoked(rev, &identity.Subject{Kind: identity.SubjectClient, ID: g.ClientId, Grant: g}) != nil {
			continue
		}
		out = append(out, &relayv1.ClientEntry{Grant: sg, Online: online[g.ClientId], MachineIds: liveMachines(g.MachineIds, removed),
			Card: cards[g.ClientId].Card, CardDeviceCert: cards[g.ClientId].DeviceCert})
	}
	return out, nil
}

// machineGrants is the grants that cover machineID, unrevoked, for that
// machine to show who may start sessions on it; nil when they can't be read.
func (s *Server) machineGrants(t *tenant, machineID string) *relayv1.MachineGrants {
	grants, err := s.store.Grants(t.ownerID)
	if err != nil {
		s.log.Warn("listing grants for a machine", "owner", t.ownerID, "machine", machineID, "err", err)
		return nil
	}
	s.mu.Lock()
	rev := t.revocations
	s.mu.Unlock()
	out := &relayv1.MachineGrants{}
	for _, sg := range grants {
		g := &identityv1.Grant{}
		if identity.Unverified(sg, g) != nil || !identity.Allows(g.MachineIds, machineID) {
			continue
		}
		if isRevoked(rev, &identity.Subject{Kind: identity.SubjectClient, ID: g.ClientId, Grant: g}) != nil {
			continue
		}
		out.Grants = append(out.Grants, sg)
	}
	return out
}

// pushGrants sends each of the owner's connected machines the grants that
// cover it, after one was published or revoked.
func (s *Server) pushGrants(t *tenant) {
	s.mu.Lock()
	var ids []string
	for id, set := range t.watchers {
		if len(set) > 0 {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		g := s.machineGrants(t, id)
		if g == nil {
			continue
		}
		u := &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_Grants{Grants: g}}
		s.mu.Lock()
		for ch := range t.watchers[id] {
			select {
			case ch <- u:
			default:
			}
		}
		s.mu.Unlock()
	}
}

// eraseOrder is an erased owner's order, root chain and acks, while the
// relay keeps them.
func (s *Server) eraseOrder(ownerID string) (*identityv1.Signed, *identityv1.RootChain, []string) {
	es, ok := s.store.(EraseStore)
	if !ok {
		return nil, nil, nil
	}
	order, chain, acks, err := es.EraseOrder(ownerID, time.Now())
	if err != nil {
		s.log.Warn("reading an erase order", "owner", ownerID, "err", err)
		return nil, nil, nil
	}
	return order, chain, acks
}

// AddEraseHook tells f each owner that erased everything, once the relay
// forgot it: the MCP gateway ends its connections, a center its account's.
func (s *Server) AddEraseHook(f func(ownerID string)) {
	s.mu.Lock()
	s.eraseHooks = append(s.eraseHooks, f)
	s.mu.Unlock()
}

// erased forgets an owner that erased everything. Its connections close a
// moment later, so that the device that asked hears the answer first; a
// self-hosted relay is then unclaimed again. Its order goes after
// EraseRetention.
func (s *Server) erased(ownerID string) {
	s.mu.Lock()
	if s.single == ownerID {
		s.single = ""
	}
	hooks := slices.Clone(s.eraseHooks)
	s.mu.Unlock()
	s.log.Info("owner erased everything", "owner", ownerID)
	time.AfterFunc(500*time.Millisecond, func() {
		s.ForgetOwner(ownerID)
		for _, f := range hooks {
			f(ownerID)
		}
		if code, err := s.ClaimCode(); err == nil && code != "" {
			fmt.Fprintf(os.Stderr, "\n  This relay is not claimed any more: its owner erased everything.\n  Claim code: %s\n\n", code)
		}
	})
	// The order goes once the erase had its time: devices that connect later
	// are refused, as for an owner that is gone.
	if es, ok := s.store.(EraseStore); ok {
		time.AfterFunc(EraseRetention+time.Second, func() {
			if err := es.PurgeErases(time.Now()); err != nil {
				s.log.Warn("purging erase orders", "err", err)
			}
		})
	}
}

// SetOwnerHook tells f the owner each time one of its trusted devices
// connects: the MCP gateway sends it the approvals still waiting.
func (s *Server) SetOwnerHook(f func(ownerID string)) {
	s.mu.Lock()
	s.ownerHook = f
	s.mu.Unlock()
}

// OwnerReachable reports whether a request to connect would reach the owner
// now: one of its devices is connected, or one is registered for push.
func (s *Server) OwnerReachable(ownerID string) bool {
	if s.OwnerOnline(ownerID) {
		return true
	}
	if s.cfg.Pusher == nil {
		return false
	}
	t, err := s.loadTenant(ownerID)
	return err == nil && t != nil && len(s.pushTargets(t)) > 0
}

// OwnerOnline reports whether one of the owner's devices is connected.
func (s *Server) OwnerOnline(ownerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tenants[ownerID]
	if t == nil {
		return false
	}
	for p := range t.peers {
		if p.role == relayv1.Role_ROLE_OWNER {
			return true
		}
	}
	return false
}

// ForgetOwner drops a hosted owner the embedding server has deleted from
// its backend: its devices are disconnected, and admitted no more.
func (s *Server) ForgetOwner(ownerID string) {
	s.mu.Lock()
	t := s.tenants[ownerID]
	delete(s.tenants, ownerID)
	var peers []*peer
	if t != nil {
		for p := range t.peers {
			peers = append(peers, p)
		}
	}
	s.mu.Unlock()
	for _, p := range peers {
		p.sess.Close()
	}
	if t != nil {
		s.log.Info("owner forgotten", "owner", ownerID, "disconnected", len(peers))
	}
}

// ---------------------------------------------------------------------------

type rateLimiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(n int, window time.Duration) *rateLimiter {
	return &rateLimiter{n: n, window: window, hits: map[string][]time.Time{}}
}

func (r *rateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	h := r.hits[key][:0]
	for _, t := range r.hits[key] {
		if now.Sub(t) < r.window {
			h = append(h, t)
		}
	}
	if len(h) >= r.n {
		r.hits[key] = h
		return false
	}
	r.hits[key] = append(h, now)
	return true
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RawTunnel opens a tunnel stream to a connected machine with an arbitrary
// header, bypassing every relay check. It exists so red-team tests can act
// as a compromised relay; a real compromised relay can do the same.
func (s *Server) RawTunnel(machineID string, hdr *relayv1.StreamHeader) (net.Conn, error) {
	s.mu.Lock()
	var n *peer
	for _, t := range s.tenants {
		if p := t.machines[machineID]; p != nil {
			n = p
		}
	}
	s.mu.Unlock()
	if n == nil {
		return nil, errors.New("machine not connected")
	}
	st, err := n.sess.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := transport.WriteFrame(st, hdr); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// Store exposes the relay database of a self-hosted relay (red-team tests
// tamper with it the way an attacker who owns the relay host could).
func (s *Server) Store() *Store { return s.sqlite }

// Backend exposes the relay's storage.
func (s *Server) Backend() Backend { return s.store }
