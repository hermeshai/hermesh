// Package machine implements the execution-machine role of hermeshd: it pairs
// with the owner, keeps an outbound connection to the relay, terminates
// end-to-end tunnels and runs coding-agent sessions.
package machine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/flock"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/acp"
	"github.com/hermeshai/hermesh/internal/provider/claude"
	"github.com/hermeshai/hermesh/internal/provider/codex"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// Version is set at build time.
var Version = "0.1.0-dev"

type Config struct {
	DataDir    string
	Logger     *slog.Logger
	Providers  map[string]provider.Provider
	Limits     Limits
	HTTPClient *http.Client
	// WorkspaceMode "none" disables git worktrees (benchmarking only).
	WorkspaceMode string
	// SelfErase: a machine on its own (not a trusted device's) stops with
	// ErrErased when its owner erased everything, to be erased (Erase).
	SelfErase bool
}

func DefaultProviders() map[string]provider.Provider {
	// Each looks on the login shell's PATH as it is when it looks.
	ps := map[string]provider.Provider{
		"claude": &claude.Provider{},
		"codex":  &codex.Provider{},
	}
	for _, p := range acp.Providers("") {
		ps[p.Name()] = p
	}
	return ps
}

// AgentNames lists the agents in display order: Claude Code and Codex,
// then the ACP agents.
func AgentNames() []string {
	names := []string{"claude", "codex"}
	for _, a := range acp.Agents {
		names = append(names, a.Name)
	}
	return names
}

type Machine struct {
	cfg       Config
	log       *slog.Logger
	st        *Store
	key       *identity.SoftwareKey
	machineID string
	audit     *Audit
	sessions  *Sessions

	mu          sync.Mutex
	trust       *identity.Trust
	ownerID     string
	relayURL    string
	machineCert *identityv1.Signed
	machineName string
	revocations *identityv1.RevocationList
	grants      []*identityv1.Grant         // the grants that cover this machine (grants.go)
	health      []*machinev1.MachineProblem // what keeps it from working (health.go)
	healthAt    time.Time                   // when it was read
	paused      bool
	infos       map[string]provider.Info // what the last check of each coding agent found
	checked     time.Time                // when the last check of all of them ended
	checking    chan struct{}            // closed when the check under way ends; nil when none is
	pending     map[string]bool          // the agents the check under way has not finished yet
	defaults    map[string]*identityv1.AgentDefaults
	tunnels     map[*tunnel]struct{}
	connected   bool
	refused     bool       // the relay refused admission when last connecting
	erased      bool       // the owner's erase order came (SelfErase)
	goneSaid    bool       // the log said the owner is gone and how to pair again
	clientsMu   sync.Mutex // rememberClient
	adminSeen   map[string]time.Time
	// grantSeen caches the highest version of each grant recorded in the
	// store, so only first sight and upgrades touch the database.
	grantSeen  map[string]uint64
	statusKick chan struct{}
	lock       *flock.Lock
	// conn is the relay connection while there is one (notices go over it).
	conn    *transport.RelayConn
	notices notices
	// contacts caches the owner's inboxes, for the news it writes (news.go).
	contacts contactBook
	// lettersSeen are the messages delivered to sessions, or being
	// delivered, since it started, and when (letters.go): one the relay
	// hands over again is not delivered twice.
	lettersSeen     map[string]time.Time
	lettersPrunedAt time.Time
	// inboxWake tells the inbox's follower to try the messages it holds
	// again: the machine resumed.
	inboxWake chan struct{}
	// tools is the sessions' own tools socket (sessiontools.go).
	tools sessionTools
}

const (
	kOwnerID     = "owner_id"
	kRelayURL    = "relay_url"
	kRootChain   = "root_chain"
	kMachineCert = "machine_cert"
	kRevocations = "revocations"
	kPaused      = "paused"
	kName        = "name"
)

func KeyPath(dataDir string) string { return filepath.Join(dataDir, "keys", "machine.key") }

// Open loads machine state from dataDir (creating the machine key if needed).
func Open(cfg Config) (*Machine, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Providers == nil {
		cfg.Providers = DefaultProviders()
	}
	if cfg.Limits == (Limits{}) {
		cfg.Limits = DefaultLimits()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	lock, err := flock.Acquire(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(filepath.Join(cfg.DataDir, "hermeshd.db"))
	if err != nil {
		lock.Release()
		return nil, err
	}
	key, err := identity.LoadOrCreateKey(KeyPath(cfg.DataDir))
	if err != nil {
		return nil, err
	}
	n := &Machine{cfg: cfg, log: cfg.Logger, st: st, key: key, lock: lock, inboxWake: make(chan struct{}, 1), machineID: identity.MachineID(identity.MarshalPublicKey(key.Public())),
		infos: map[string]provider.Info{}, defaults: map[string]*identityv1.AgentDefaults{}, tunnels: map[*tunnel]struct{}{}, adminSeen: map[string]time.Time{}, grantSeen: map[string]uint64{},
		statusKick: make(chan struct{}, 1), revocations: &identityv1.RevocationList{MinGrantVersions: map[string]uint64{}}}
	if n.audit, err = OpenAudit(filepath.Join(cfg.DataDir, "audit"), key); err != nil {
		return nil, err
	}
	if err := n.loadState(); err != nil {
		return nil, err
	}
	ws := NewWorkspace(filepath.Join(cfg.DataDir, "worktrees"))
	ws.none = cfg.WorkspaceMode == "none"
	n.sessions = &Sessions{st: st, ws: ws, audit: n.audit, log: n.log,
		machineID: n.machineID, dataDir: cfg.DataDir, providers: cfg.Providers, info: n.providerInfo, limits: cfg.Limits,
		paused: n.isPaused, defaults: n.agentDefaults, outOfProcesses: n.outOfProcesses, live: map[string]*live{}}
	n.sessions.w = newWriter(st, n.log, n.sessions.deliver, n.sessions.changed)
	n.sessions.mcp = n.mcpServers
	n.sessions.notice = func(s noticeSession, e *sessionv1.SessionEvent) {
		n.sessionNotice(s, e)
		n.sessionNews(s, e)
	}
	n.sessions.registerMetrics()
	return n, nil
}

func (n *Machine) loadState() error {
	n.loadDefaults()
	owner, _ := n.st.Get(kOwnerID)
	relay, _ := n.st.Get(kRelayURL)
	n.ownerID, n.relayURL = string(owner), string(relay)
	if n.ownerID == "" {
		return nil
	}
	n.trust = identity.NewTrust(n.ownerID)
	rc := &identityv1.RootChain{}
	if ok, err := n.st.GetProto(kRootChain, rc); err != nil || !ok {
		return fmt.Errorf("stored root chain missing: %v", err)
	}
	if _, err := n.trust.AcceptRootChain(rc); err != nil {
		return fmt.Errorf("stored root chain: %w", err)
	}
	nc := &identityv1.Signed{}
	if ok, _ := n.st.GetProto(kMachineCert, nc); ok {
		if _, _, err := n.trust.VerifyMachineCert(nc); err == nil {
			n.machineCert = nc
		}
	}
	if name, _ := n.st.Get(kName); len(name) > 0 {
		n.machineName = string(name)
	}
	rev := &identityv1.RevocationList{}
	if ok, _ := n.st.GetProto(kRevocations, rev); ok {
		if rev.MinGrantVersions == nil {
			rev.MinGrantVersions = map[string]uint64{}
		}
		n.revocations = rev
	}
	p, _ := n.st.Get(kPaused)
	n.paused = string(p) == "1"
	n.loadGrants()
	return nil
}

func (n *Machine) Close() error {
	n.sessions.w.close()
	err := n.st.Close()
	n.audit.Close()
	n.lock.Release()
	return err
}

func (n *Machine) MachineID() string { return n.machineID }

var nameRE = regexp.MustCompile(`[^a-z0-9-]+`)

// SanitizeName turns s into a machine name: lowercase letters, digits and
// '-', at most 63 bytes ("machine" if nothing is left).
func SanitizeName(s string) string {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".local")
	s = strings.Trim(nameRE.ReplaceAllString(s, "-"), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	if s == "" {
		s = "machine"
	}
	return s
}

// SetName sets the machine's name, a setting: the name it reports and
// presents to agents on their end-to-end connections.
func (n *Machine) SetName(name string) error {
	name = SanitizeName(name)
	if err := n.st.Set(kName, []byte(name)); err != nil {
		return err
	}
	n.mu.Lock()
	n.machineName = name
	n.mu.Unlock()
	return nil
}

// OwnerID is the owner this machine is paired with ("" if none).
func (n *Machine) OwnerID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ownerID
}

func (n *Machine) Paired() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.machineCert != nil
}

func (n *Machine) Name() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.machineName
}

func (n *Machine) isPaused() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.paused
}

func (n *Machine) Connected() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.connected
}

// providerInfo is what the last check found of a coding agent; one it found
// missing, or never checked, is checked again now, so a session with an
// agent installed since is not refused.
func (n *Machine) providerInfo(name string) provider.Info {
	n.mu.Lock()
	i, ok := n.infos[name]
	p := n.cfg.Providers[name]
	n.mu.Unlock()
	if (ok && i.Available) || p == nil {
		return i
	}
	i = p.Discover(context.Background())
	n.mu.Lock()
	n.infos[name] = i
	n.mu.Unlock()
	return i
}

func (n *Machine) kickStatus() {
	select {
	case n.statusKick <- struct{}{}:
	default:
	}
}

func (n *Machine) credentials() *identityv1.Credentials {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &identityv1.Credentials{RootChain: n.trust.RootChain(), MachineName: n.machineName, Subject: &identityv1.Credentials_MachineCert{MachineCert: n.machineCert}}
}

// The coding agents are checked (installed, version, logged in: each CLI is
// run) only when something needs to know: the app showing them, an agent
// asking where it can work, a session about to start. Nothing checks them
// in the background.

// AgentsFresh is how long a check stands for agents asking where they can
// work; past it, they wait for a new one.
const AgentsFresh = 5 * time.Minute

// AgentsOnce, as CheckAgents' maxAge: check only if no check has run yet.
const AgentsOnce = time.Duration(math.MaxInt64)

// CheckAgents starts a check of every coding agent unless one is under way,
// or (!force) the last one ended within maxAge; the channel closes when the
// coding agents are checked. Each agent shows in Status as soon as its own
// check ends.
func (n *Machine) CheckAgents(force bool, maxAge time.Duration) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.checking != nil {
		return n.checking
	}
	if !force && !n.checked.IsZero() && time.Since(n.checked) < maxAge {
		done := make(chan struct{})
		close(done)
		return done
	}
	done := make(chan struct{})
	n.checking = done
	n.pending = map[string]bool{}
	for name := range n.cfg.Providers {
		n.pending[name] = true
	}
	go func() {
		var wg sync.WaitGroup
		for name, p := range n.cfg.Providers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				info := p.Discover(context.Background())
				n.mu.Lock()
				n.infos[name] = info
				delete(n.pending, name)
				n.mu.Unlock()
			}()
		}
		wg.Wait()
		n.mu.Lock()
		short := false // an agent could not start for want of processes: say so now
		for _, i := range n.infos {
			short = short || i.Problem == provider.ProblemNoProcesses
		}
		n.checked = time.Now()
		n.checking = nil
		n.mu.Unlock()
		close(done)
		if short {
			n.refreshHealth()
		}
	}()
	return done
}

// Discover checks every coding agent now and returns what it found.
func (n *Machine) Discover(ctx context.Context) map[string]provider.Info {
	select {
	case <-n.CheckAgents(true, 0):
	case <-ctx.Done():
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.infos)
}

func (n *Machine) Status() *machinev1.MachineStatus {
	n.freshHealth()
	n.mu.Lock()
	st := &machinev1.MachineStatus{Name: n.machineName, ProtocolVersion: transport.ProtocolVersion, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		MaxSessions: uint32(n.cfg.Limits.MaxConcurrent), Paused: n.paused, Version: Version}
	for _, name := range append(AgentNames(), "fake") {
		// Claude Code and Codex are always listed; other agents when found
		// (working or not), when their app lacks the CLI, or while their
		// first check is under way.
		i, ok := n.infos[name]
		if (ok && (i.Available || i.Problem != provider.ProblemNotFound || name == "claude" || name == "codex")) || (!ok && n.pending[name]) {
			a := &machinev1.AgentStatus{Name: name, Available: i.Available, Version: i.Version,
				LoggedIn: i.LoggedIn, Detail: i.Detail, Checking: n.pending[name], Problem: machinev1.AgentProblem(i.Problem), Binary: i.Binary}
			n.agentModels(a, i)
			st.Agents = append(st.Agents, a)
		}
	}
	st.Problems = n.health
	n.mu.Unlock()
	st.RunningSessions = uint32(n.sessions.Running())
	st.Sessions = n.sessions.Counts()
	return st
}

// Run serves until ctx is done.
func (n *Machine) Run(ctx context.Context) error {
	if !n.Paired() {
		return errors.New("this machine is not paired yet; run `hermeshd pair <connection address>`")
	}
	if err := n.sessions.Recover(); err != nil {
		return err
	}
	go n.sessions.Janitor(ctx)
	go n.sessions.CountWork(ctx)
	go n.serveSessionTools(ctx)
	go n.serveControl(ctx)
	n.log.Info("hermeshd machine running", "machine", n.machineID, "name", n.Name(), "relay", n.relayURL)
	n.runLink(ctx)
	n.mu.Lock()
	erased := n.erased
	n.mu.Unlock()
	if erased {
		return ErrErased
	}
	return nil
}

// applyRevocations merges r (which can only reduce permissions), closes
// affected tunnels and cancels sessions of revoked agents.
func (n *Machine) applyRevocations(r *identityv1.RevocationList) {
	if r == nil {
		return
	}
	n.mu.Lock()
	cur := proto.Clone(n.revocations).(*identityv1.RevocationList)
	before := proto.Clone(cur).(*identityv1.RevocationList)
	mergeRevocations(cur, r)
	if proto.Equal(cur, before) {
		n.mu.Unlock()
		return
	}
	n.revocations = cur
	var close []*tunnel
	for t := range n.tunnels {
		if t.principal.Kind == PrincipalClient && n.grantProblemLocked(t.principal.Grant) != nil {
			close = append(close, t)
		}
	}
	n.mu.Unlock()
	n.st.SetProto(kRevocations, cur)
	for _, t := range close {
		t.conn.Close()
	}
	for _, id := range r.RevokedGrantIds {
		for _, c := range n.clientsForGrant(id) {
			n.sessions.InterruptByClient(c, "agent access revoked")
		}
	}
	n.audit.Log(AuditRecord{Kind: "revocations", Detail: fmt.Sprintf("grants=%v machines=%v min=%v", cur.RevokedGrantIds, cur.RevokedMachineIds, cur.MinGrantVersions)})
}

func (n *Machine) clientsForGrant(grantID string) []string {
	rows, err := n.st.Sessions(`grant_id = ?`, grantID)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.CreatedBy] {
			seen[r.CreatedBy] = true
			out = append(out, r.CreatedBy)
		}
	}
	return out
}

func mergeRevocations(cur, in *identityv1.RevocationList) {
	add := func(dst *[]string, ids []string) {
		for _, id := range ids {
			found := false
			for _, x := range *dst {
				if x == id {
					found = true
					break
				}
			}
			if !found {
				*dst = append(*dst, id)
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
}

// grantProblemLocked reports why a grant may not be used now.
func (n *Machine) grantProblemLocked(g *identityv1.Grant) error {
	for _, id := range n.revocations.RevokedGrantIds {
		if id == g.GrantId {
			return errors.New("grant revoked")
		}
	}
	if g.Version < n.revocations.MinGrantVersions[g.GrantId] {
		return errors.New("grant superseded by a newer version")
	}
	if g.ExpiresAt != nil && time.Now().After(g.ExpiresAt.AsTime().Add(identity.ClockSkew)) {
		return errors.New("grant expired")
	}
	if !identity.Allows(g.MachineIds, n.machineID) {
		return errors.New("grant does not cover this machine")
	}
	return nil
}

func (n *Machine) checkGrant(g *identityv1.Grant) error {
	n.mu.Lock()
	maxSeen, ok := n.grantSeen[g.GrantId]
	n.mu.Unlock()
	if !ok || g.Version > maxSeen {
		stored, err := n.st.SeeGrantVersion(g.GrantId, g.Version)
		if err != nil {
			return err
		}
		n.mu.Lock()
		if cur, ok := n.grantSeen[g.GrantId]; !ok || stored > cur {
			n.grantSeen[g.GrantId] = stored
		}
		maxSeen = n.grantSeen[g.GrantId]
		n.mu.Unlock()
	}
	if g.Version < maxSeen {
		return errors.New("a newer version of this grant exists")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.grantProblemLocked(g)
}

func (n *Machine) RelayURL() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.relayURL
}

func (n *Machine) Projects() ([]Project, error) { return n.st.Projects() }

func (n *Machine) VerifyAudit() (int, error) {
	return VerifyAudit(filepath.Join(n.cfg.DataDir, "audit"), append([]identity.Signer{n.key}, retiredKeys(n.cfg.DataDir)...)...)
}
