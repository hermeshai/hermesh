// Package peer connects an agent or the owner device to machines: one relay
// connection, then an end-to-end TLS tunnel per machine.
package peer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1/screenv1connect"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type Dialer struct {
	RelayURL    string
	Role        relayv1.Role
	Key         *identity.SoftwareKey
	Credentials func() *identityv1.Credentials
	Trust       *identity.Trust
	HTTPClient  *http.Client
	// Pipeline: see transport.Dialer (the first connection only).
	Pipeline bool
	// Link, when set, reaches the relay's control services (machineID "")
	// or a machine through a process on this computer that keeps this
	// client's relay connection and its tunnels from one command to the
	// next (the CLI's link). The connection it returns carries HTTP/2 to
	// them; machineName is the name the machine presented in the end-to-end
	// handshake. Once it fails, the client connects by itself, as without.
	Link func(ctx context.Context, machineID string) (conn net.Conn, machineName string, err error)
	// Local, when set, connects to machineID on this computer without the
	// relay (the machine's own endpoint for its user's agents), failing
	// when it is not here. The end-to-end handshake runs on that connection
	// as it does through a tunnel; a machine that is not here, or refuses
	// it, is reached as without.
	Local func(ctx context.Context, machineID string) (net.Conn, error)
	// Direct, when set, returns direct E2E TCP addresses (LAN IPv4 / public
	// IPv6) for machineID to race against the relay.
	Direct func(machineID string) []string
	// OnDirectSuccess, when set, is called with the winning direct address so
	// subsequent dials can try it first.
	OnDirectSuccess func(machineID, addr string)
	// OnUpgrade, when set, is called whenever an existing machine tunnel is
	// upgraded onto a faster direct QUIC/TCP path.
	OnUpgrade func(machineID, path string, rtt time.Duration)
}

// Client is a relay connection plus cached E2E tunnels to machines.
type Client struct {
	d *Dialer
	// conn is read without mu, so that asking whether the client is
	// connected never waits for a dial.
	conn atomic.Pointer[transport.RelayConn]

	mu        sync.Mutex
	closed    bool
	holds     int // Closes that only end a Hold
	machines  map[string]*Machine
	warming   map[string]chan struct{} // closed when the attempt ends
	lastPunch map[string]time.Time     // cooldown for automatic post-relay IPv6 punch upgrades
	gen       int                      // counts Resets

	// linked: the relay and machines are reached through Dialer.Link, with
	// no connection of this client's own (conn is nil until it fails).
	linkMu      sync.Mutex
	linked      bool
	linkControl *transport.H2Client
}

// tunnelConn is implemented by both *transport.H2Client (TCP/Relay) and
// *transport.H3Client (direct UDP QUIC/HTTP3).
type tunnelConn interface {
	Do(*http.Request) (*http.Response, error)
	Alive() bool
	Close() error
	RTT() time.Duration
}

// Machine holds typed clients for one machine over one E2E tunnel.
type Machine struct {
	MachineID string
	// Name is the name the machine itself presented in the end-to-end
	// handshake (the relay cannot change it).
	Name    string
	Session sessionv1connect.SessionServiceClient
	Info    machinev1connect.MachineServiceClient
	Admin   machinev1connect.MachineAdminServiceClient
	Her     herv1connect.HerServiceClient
	Screen  screenv1connect.ScreenServiceClient
	h2      tunnelConn
	httpCli *http.Client
	direct  bool
	path    string
	srtt    atomic.Int64 // smoothed tunnel RTT in nanoseconds
}

// Path returns how this tunnel reaches the machine: "local", "quic_lan",
// "quic_ipv6", "direct_lan", "direct_ipv6", or "relay".
func (n *Machine) Path() string { return n.path }

// RTT returns the live transport smoothed RTT when available (QUIC RFC 9002),
// falling back to the smoothed RPC RTT recorded on this tunnel.
func (n *Machine) RTT() time.Duration {
	if n.h2 != nil {
		if rtt := n.h2.RTT(); rtt > 0 {
			return rtt
		}
	}
	return time.Duration(n.srtt.Load())
}

// RecordRTT folds a new RTT sample into this tunnel's smoothed RTT.
func (n *Machine) RecordRTT(d time.Duration) {
	if d < 0 {
		return
	}
	ns := d.Nanoseconds()
	if ns == 0 {
		// On coarse clocks (e.g. Windows), a fast loopback round-trip can
		// measure 0 ns; clamp to 1 ns so RTT() distinguishes a measured
		// sub-tick sample from an unmeasured tunnel (0).
		ns = 1
	}
	for {
		old := n.srtt.Load()
		next := ns
		if old > 0 {
			next = (old*7 + ns) / 8
		}
		if n.srtt.CompareAndSwap(old, next) {
			return
		}
	}
}

func classifyDirectPath(addr string) string {
	isQUIC := false
	if stripped, ok := strings.CutPrefix(addr, "quic://"); ok {
		isQUIC = true
		addr = stripped
	}
	host, _, err := net.SplitHostPort(addr)
	isV6 := false
	if err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			isV6 = true
		}
	}
	if isQUIC {
		if isV6 {
			return "quic_ipv6"
		}
		return "quic_lan"
	}
	if isV6 {
		return "direct_ipv6"
	}
	return "direct_lan"
}

// newMachine is the typed clients for machineID over h2, its tunnel.
func newMachine(machineID, name string, h2 *transport.H2Client) *Machine {
	return newMachineWithConn(machineID, name, h2, h2.Client)
}

func newMachineQUIC(machineID, name string, h3 *transport.H3Client) *Machine {
	return newMachineWithConn(machineID, name, h3, h3.Client)
}

func newMachineWithConn(machineID, name string, tc tunnelConn, httpCli *http.Client) *Machine {
	const base = "https://machine"
	return &Machine{MachineID: machineID, Name: name, h2: tc, httpCli: httpCli, path: "relay",
		Session: sessionv1connect.NewSessionServiceClient(tc, base, transport.ClientNoCompression),
		Info:    machinev1connect.NewMachineServiceClient(tc, base, transport.ClientNoCompression),
		Admin:   machinev1connect.NewMachineAdminServiceClient(tc, base, transport.ClientNoCompression),
		Her:     herv1connect.NewHerServiceClient(tc, base, transport.ClientNoCompression),
		Screen:  screenv1connect.NewScreenServiceClient(tc, base, transport.ClientNoCompression)}
}

// HTTP is the tunnel's HTTP client (HTTP/2 or HTTP/3), for the machine's plain
// HTTP routes (Her's console): requests go to "https://machine/…".
func (n *Machine) HTTP() *http.Client { return n.httpCli }

// Alive reports whether the machine's tunnel can accept new requests.
func (n *Machine) Alive() bool { return n.h2.Alive() }

// Close closes the machine's underlying tunnel.
func (n *Machine) Close() error { return n.h2.Close() }

// Detached is a client with no connection to the relay yet: it dials when
// something needs the relay, and reaches the machines of this computer
// (Dialer.Local) without.
func (d *Dialer) Detached() *Client {
	return &Client{d: d, machines: map[string]*Machine{}, warming: map[string]chan struct{}{}}
}

func (d *Dialer) Connect(ctx context.Context) (*Client, error) {
	if d.Link != nil {
		return &Client{d: d, linked: true, machines: map[string]*Machine{}, warming: map[string]chan struct{}{}}, nil
	}
	td := &transport.Dialer{RelayURL: d.RelayURL, Role: d.Role, Key: d.Key, Credentials: d.Credentials, HTTPClient: d.HTTPClient, Pipeline: d.Pipeline}
	conn, err := td.Dial(ctx)
	if err != nil {
		return nil, err
	}
	c := &Client{d: d, machines: map[string]*Machine{}, warming: map[string]chan struct{}{}}
	c.conn.Store(conn)
	return c, nil
}

// Hold keeps the client open through one more Close: of the users sharing
// it, the last to close it closes it.
func (c *Client) Hold() {
	c.mu.Lock()
	c.holds++
	c.mu.Unlock()
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.holds > 0 {
		c.holds--
		return nil
	}
	c.closed = true
	for _, n := range c.machines {
		n.h2.Close()
	}
	c.linkMu.Lock()
	if c.linkControl != nil {
		c.linkControl.Close()
	}
	c.linkMu.Unlock()
	if conn := c.conn.Load(); conn != nil {
		return conn.Close()
	}
	return nil
}

// Done is closed when the current relay connection is lost (never, for a
// client that has none of its own).
func (c *Client) Done() <-chan struct{} {
	if conn := c.conn.Load(); conn != nil {
		return conn.Done()
	}
	return nil
}

// RTT returns the smoothed RTT to the relay (0 when unknown).
func (c *Client) RTT() time.Duration {
	if conn := c.conn.Load(); conn != nil {
		return conn.RTT()
	}
	return 0
}

// MachineTunnel reports the cached tunnel path, smoothed RTT, and liveness for
// machineID without dialing.
func (c *Client) MachineTunnel(machineID string) (path string, rtt time.Duration, alive bool) {
	c.mu.Lock()
	n := c.machines[machineID]
	c.mu.Unlock()
	if n == nil {
		return "", 0, false
	}
	return n.Path(), n.RTT(), n.h2.Alive()
}

// CachedRelayedMachineIDs returns the IDs of cached alive relayed machine
// tunnels on this client.
func (c *Client) CachedRelayedMachineIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for id, n := range c.machines {
		if n != nil && !n.direct && n.h2.Alive() {
			ids = append(ids, id)
		}
	}
	return ids
}

// ProbeTunnel verifies an already-cached relayed tunnel to machineID with a
// lightweight GetInfo RPC and updates its RTT. Returns probed=false when no
// cached relayed tunnel exists.
func (c *Client) ProbeTunnel(ctx context.Context, machineID string) (probed bool, rtt time.Duration, err error) {
	c.mu.Lock()
	n := c.machines[machineID]
	c.mu.Unlock()
	if n == nil || n.direct || !n.h2.Alive() {
		return false, 0, nil
	}
	started := time.Now()
	_, err = n.Info.GetInfo(ctx, &machinev1.GetInfoRequest{})
	elapsed := time.Since(started)
	if err == nil {
		n.RecordRTT(elapsed)
	}
	return true, elapsed, err
}

// Ping waits for the relay to answer a ping on the current connection.
func (c *Client) Ping(ctx context.Context) error {
	conn := c.conn.Load()
	if conn == nil {
		return net.ErrClosed
	}
	return conn.Ping(ctx)
}

// Heard is when the relay was last heard from on the current connection.
func (c *Client) Heard() time.Time {
	if conn := c.conn.Load(); conn != nil {
		return conn.Heard()
	}
	return time.Time{}
}

// Drop closes the relay connection, which stopped answering; the next use
// dials again.
func (c *Client) Drop() {
	if conn := c.conn.Load(); conn != nil {
		conn.Close()
	}
}

// tunnelTimeout identifies the relay connection on which a new machine
// tunnel failed to complete its TLS handshake. Ordinary RPC deadlines and
// authentication failures are not evidence for replacing that connection.
type tunnelTimeout struct {
	err  error
	conn *transport.RelayConn
}

func (e *tunnelTimeout) Error() string { return e.err.Error() }
func (e *tunnelTimeout) Unwrap() error { return e.err }

// RecoverTunnel drops the still-current relay connection after a new tunnel's
// transport timeout. Callers must bound this recovery: a sleeping machine can
// cause the same timeout, and replacing the relay also ends its other tunnels.
// It does not retry the call or change its deadline.
func (c *Client) RecoverTunnel(err error) bool {
	var failed *tunnelTimeout
	if !errors.As(err, &failed) || c.conn.Load() != failed.conn || failed.conn.IsClosed() {
		return false
	}
	failed.conn.Close()
	return true
}

// Admitted waits for the relay's answer to this connection's Hello: nil
// once it admitted it.
func (c *Client) Admitted(ctx context.Context) error {
	conn := c.conn.Load()
	if conn == nil {
		return nil
	}
	return conn.Admitted(ctx)
}

// Refused is why the relay refused the current connection
// (transport.ErrAdmission), once it is closed; nil otherwise.
func (c *Client) Refused() error {
	conn := c.conn.Load()
	if conn == nil || !conn.IsClosed() {
		return nil
	}
	if err := conn.Explain(nil); errors.Is(err, transport.ErrAdmission) {
		return err
	}
	return nil
}

// relay returns the relay connection, dialing it again when it was lost
// (a relay restart or a network change): long waits and follows must
// outlive the connection they started on. Tunnels belong to the old one.
func (c *Client) relay(ctx context.Context) (*transport.RelayConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, net.ErrClosed
	}
	old := c.conn.Load()
	if old != nil && !old.IsClosed() {
		return old, nil
	}
	if err := c.Refused(); err != nil {
		return nil, err // refused: dialing again would be too
	}
	td := &transport.Dialer{RelayURL: c.d.RelayURL, Role: c.d.Role, Key: c.d.Key, Credentials: c.d.Credentials, HTTPClient: c.d.HTTPClient}
	if old == nil {
		// The first connection of a client whose link failed: as Connect
		// makes it. The machines it reached through the link stay.
		td.Pipeline = c.d.Pipeline
	} else {
		for _, n := range c.machines {
			n.h2.Close()
		}
		c.machines = map[string]*Machine{}
	}
	conn, err := td.Dial(ctx)
	if err != nil {
		return nil, err
	}
	c.conn.Store(conn)
	return conn, nil
}

// Control returns an HTTP client for the relay control services.
func (c *Client) Control() (*transport.H2Client, error) {
	if hc := c.controlByLink(); hc != nil {
		return hc, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := c.relay(ctx)
	if err != nil {
		return nil, err
	}
	return conn.Control()
}

// Linked says whether the client reaches the relay through Dialer.Link
// (false once the link failed, or without one).
func (c *Client) Linked() bool {
	c.linkMu.Lock()
	defer c.linkMu.Unlock()
	return c.linked
}

// controlByLink is the control client through the link; nil when the
// client connects by itself (it has no link, or the link failed).
func (c *Client) controlByLink() *transport.H2Client {
	c.linkMu.Lock()
	defer c.linkMu.Unlock()
	if !c.linked {
		return nil
	}
	if c.linkControl != nil && c.linkControl.Alive() {
		return c.linkControl
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := c.d.Link(ctx, "")
	if err != nil {
		c.linked = false
		return nil
	}
	hc, err := transport.NewH2Client(conn)
	if err != nil {
		conn.Close()
		c.linked = false
		return nil
	}
	c.linkControl = hc
	return hc
}

// machineHere is machineID on this computer, reached without the relay;
// nil when it is not here or refused the handshake (the relay's way then
// says why).
func (c *Client) machineHere(ctx context.Context, machineID string) *Machine {
	if c.d.Local == nil {
		return nil
	}
	raw, err := c.d.Local(ctx, machineID)
	if err != nil {
		return nil
	}
	tc, sub, err := transport.ClientHandshake(ctx, raw, c.d.Key.CryptoSigner(), c.d.Credentials, c.d.Trust, machineID)
	if err != nil {
		raw.Close()
		return nil
	}
	h2, err := transport.NewH2Client(tc)
	if err != nil {
		tc.Close()
		return nil
	}
	n := newMachine(machineID, sub.MachineName, h2)
	n.path = "local"
	return n
}

// machineByLink is machineID through the link; nil when the client
// connects by itself.
func (c *Client) machineByLink(ctx context.Context, machineID string) *Machine {
	c.linkMu.Lock()
	linked := c.linked
	c.linkMu.Unlock()
	if !linked {
		return nil
	}
	conn, name, err := c.d.Link(ctx, machineID)
	if err == nil {
		var h2 *transport.H2Client
		if h2, err = transport.NewH2Client(conn); err == nil {
			return newMachine(machineID, name, h2)
		}
		conn.Close()
	}
	c.linkMu.Lock()
	c.linked = false
	c.linkMu.Unlock()
	return nil
}

// Machine returns (and caches) an E2E connection to machineID. It joins a
// Warm or concurrent Machine dial in progress, and retries if that attempt fails.
func (c *Client) Machine(ctx context.Context, machineID string) (*Machine, error) {
	n, _, err := c.machineWithOrigin(ctx, machineID)
	return n, err
}

func (c *Client) machineWithOrigin(ctx context.Context, machineID string) (n *Machine, cached bool, err error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, false, errors.New("peer client closed")
		}
		if m := c.machines[machineID]; m != nil && m.h2.Alive() {
			c.mu.Unlock()
			return m, true, nil
		}
		w := c.warming[machineID]
		if w == nil {
			w = make(chan struct{})
			c.warming[machineID] = w
			c.mu.Unlock()
			m, err := c.dialAndFinishWarm(ctx, machineID, w)
			return m, false, err
		}
		c.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

func (c *Client) dialAndFinishWarm(ctx context.Context, machineID string, w chan struct{}) (*Machine, error) {
	defer func() {
		c.mu.Lock()
		if c.warming[machineID] == w {
			delete(c.warming, machineID)
		}
		c.mu.Unlock()
		close(w)
	}()
	return c.dialMachine(ctx, machineID)
}

// RelayMachine returns (and caches) an E2E connection to machineID through
// the relay without running direct-path discovery.
func (c *Client) RelayMachine(ctx context.Context, machineID string) (*Machine, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("peer client closed")
	}
	if n := c.machines[machineID]; n != nil && n.h2.Alive() {
		c.mu.Unlock()
		return n, nil
	}
	gen := c.gen
	c.mu.Unlock()
	rn, conn, err := c.openRelayTunnel(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return c.storeMachine(machineID, gen, conn, rn)
}

// Warm connects to machines in the background, ahead of the calls likely to
// need them. onError can recover the relay that a background dial timed out on.
func (c *Client) Warm(machineIDs []string, onError func(string, error)) {
	for _, id := range machineIDs {
		c.mu.Lock()
		if c.warming[id] != nil {
			c.mu.Unlock()
			continue
		}
		if n := c.machines[id]; n != nil {
			if n.h2.Alive() {
				c.mu.Unlock()
				continue
			}
			n.h2.Close()
			delete(c.machines, id)
		}
		done := make(chan struct{})
		c.warming[id] = done
		c.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := c.dialMachine(ctx, id)
			cancel()
			if err != nil && onError != nil {
				onError(id, err)
			}
			c.mu.Lock()
			delete(c.warming, id)
			c.mu.Unlock()
			close(done)
		}()
	}
}

const (
	directStagger       = 25 * time.Millisecond
	directQUICHeadStart = 80 * time.Millisecond
	directHeadLAN       = 80 * time.Millisecond
	directHeadWAN       = 280 * time.Millisecond
	directTimeout       = 2 * time.Second
)

func directHeadFor(addrs []string) time.Duration {
	for _, hp := range addrs {
		hp = strings.TrimPrefix(hp, "quic://")
		host, _, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			return directHeadWAN
		}
	}
	return directHeadLAN
}

// DialDirect tries addrs in parallel (staggered slightly so preferred
// addresses go first) and returns the first established E2E tunnel to
// machineID, cancelling the remaining attempts.
func (d *Dialer) DialDirect(ctx context.Context, machineID string, addrs []string) (*Machine, error) {
	if len(addrs) == 0 {
		return nil, errors.New("no direct addresses")
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type res struct {
		n    *Machine
		addr string
		err  error
	}
	hasQUIC := false
	for _, a := range addrs {
		if strings.HasPrefix(a, "quic://") {
			hasQUIC = true
			break
		}
	}
	ch := make(chan res, len(addrs))
	quicIdx, tcpIdx := 0, 0
	for _, addr := range addrs {
		var delay time.Duration
		if strings.HasPrefix(addr, "quic://") {
			delay = time.Duration(quicIdx) * directStagger
			quicIdx++
		} else {
			delay = time.Duration(tcpIdx) * directStagger
			if hasQUIC {
				// Give QUIC an 80 ms head start so local VPN/proxy Fake-TCP
				// SYN-ACK responders cannot hijack LAN dials ahead of QUIC.
				delay += directQUICHeadStart
			}
			tcpIdx++
		}
		go func(delay time.Duration, addr string) {
			if delay > 0 {
				t := time.NewTimer(delay)
				select {
				case <-t.C:
				case <-rctx.Done():
					t.Stop()
					ch <- res{err: rctx.Err()}
					return
				}
			}
			dctx, dcancel := context.WithTimeout(rctx, directTimeout)
			defer dcancel()
			started := time.Now()
			if quicAddr, isQUIC := strings.CutPrefix(addr, "quic://"); isQUIC {
				h3, sub, err := transport.DialQUIC(dctx, quicAddr, d.Key.CryptoSigner(), d.Credentials, d.Trust, machineID)
				if err != nil {
					ch <- res{err: err}
					return
				}
				m := newMachineQUIC(machineID, sub.MachineName, h3)
				m.direct = true
				m.path = classifyDirectPath(addr)
				m.RecordRTT(time.Since(started))
				ch <- res{n: m, addr: addr}
				return
			}
			nd := net.Dialer{Timeout: directTimeout, KeepAlive: 15 * time.Second}
			raw, err := nd.DialContext(dctx, "tcp", addr)
			if err != nil {
				ch <- res{err: err}
				return
			}
			tc, sub, err := transport.ClientHandshake(dctx, raw, d.Key.CryptoSigner(), d.Credentials, d.Trust, machineID)
			if err != nil {
				raw.Close()
				ch <- res{err: err}
				return
			}
			h2, err := transport.NewTunnelClient(tc)
			if err != nil {
				tc.Close()
				ch <- res{err: err}
				return
			}
			m := newMachine(machineID, sub.MachineName, h2)
			m.direct = true
			m.path = classifyDirectPath(addr)
			m.RecordRTT(time.Since(started))
			ch <- res{n: m, addr: addr}
		}(delay, addr)
	}

	var firstErr error
	var winner *Machine
	var winAddr string
	for range addrs {
		r := <-ch
		if r.n != nil {
			if winner == nil {
				winner, winAddr = r.n, r.addr
				cancel()
			} else {
				r.n.Close()
			}
		} else if firstErr == nil {
			firstErr = r.err
		}
	}
	if winner != nil {
		if d.OnDirectSuccess != nil && winAddr != "" {
			d.OnDirectSuccess(machineID, winAddr)
		}
		return winner, nil
	}
	if firstErr == nil {
		return nil, errors.New("direct dial failed")
	}
	return nil, firstErr
}

// RaceDirect tries addrs via DialDirect and races that against slow after a
// brief direct head-start (waking slow immediately if all direct dials finish
// failing first). Whichever succeeds first cancels the other and wins; at
// most one of direct or viaSlow is non-nil when err is nil.
func (d *Dialer) RaceDirect(ctx context.Context, machineID string, addrs []string, slow func(context.Context) (*Machine, error)) (direct, viaSlow *Machine, err error) {
	if len(addrs) == 0 {
		sn, err := slow(ctx)
		return nil, sn, err
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type attempt struct {
		n   *Machine
		err error
	}
	directCh := make(chan attempt, 1)
	go func() {
		dn, err := d.DialDirect(rctx, machineID, addrs)
		directCh <- attempt{n: dn, err: err}
	}()

	slowCh := make(chan attempt, 1)
	slowRunning := false
	startSlow := func() {
		slowRunning = true
		go func() {
			sn, err := slow(rctx)
			slowCh <- attempt{n: sn, err: err}
		}()
	}

	head := time.NewTimer(directHeadFor(addrs))
	defer head.Stop()
	var directErr error
	directDone := false
	select {
	case a := <-directCh:
		directDone = true
		head.Stop()
		if a.n != nil {
			return a.n, nil, nil
		}
		directErr = a.err
		startSlow()
	case <-head.C:
		startSlow()
	case <-ctx.Done():
		cancel()
		if a := <-directCh; a.n != nil {
			a.n.Close()
		}
		return nil, nil, ctx.Err()
	}

	var slowErr error
	for !directDone || slowRunning {
		select {
		case a := <-directCh:
			directDone = true
			if a.n != nil {
				cancel()
				if slowRunning {
					go func() {
						if s := <-slowCh; s.n != nil {
							s.n.Close()
						}
					}()
				}
				return a.n, nil, nil
			}
			directErr = a.err
		case s := <-slowCh:
			slowRunning = false
			if s.n != nil {
				cancel()
				if !directDone {
					go func() {
						if a := <-directCh; a.n != nil {
							a.n.Close()
						}
					}()
				}
				return nil, s.n, nil
			}
			slowErr = s.err
		}
	}
	if slowErr != nil {
		return nil, nil, slowErr
	}
	return nil, nil, directErr
}

func (c *Client) openRelayTunnel(ctx context.Context, machineID string) (*Machine, *transport.RelayConn, error) {
	conn, err := c.relay(ctx)
	if err != nil {
		return nil, nil, err
	}
	started := time.Now()
	raw, err := conn.OpenTunnel(machineID)
	if err != nil {
		return nil, nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tc, sub, err := transport.ClientHandshake(hctx, raw, c.d.Key.CryptoSigner(), c.d.Credentials, c.d.Trust, machineID)
	if err != nil {
		raw.Close()
		err = fmt.Errorf("connect to %s: %w", machineID, conn.Explain(err))
		var timeout net.Error
		if !errors.Is(err, context.Canceled) && !errors.Is(err, transport.ErrAdmission) && errors.As(err, &timeout) && timeout.Timeout() {
			return nil, nil, &tunnelTimeout{err: err, conn: conn}
		}
		return nil, nil, err
	}
	h2, err := transport.NewTunnelClient(tc)
	if err != nil {
		tc.Close()
		return nil, nil, err
	}
	m := newMachine(machineID, sub.MachineName, h2)
	m.path = "relay"
	m.RecordRTT(time.Since(started))
	return m, conn, nil
}

func (c *Client) dialMachine(ctx context.Context, machineID string) (*Machine, error) {
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()
	n := c.machineHere(ctx, machineID)
	if n == nil {
		n = c.machineByLink(ctx, machineID)
	}
	if n != nil {
		return c.storeMachine(machineID, gen, nil, n)
	}
	var addrs []string
	if c.d.Direct != nil {
		addrs = c.d.Direct(machineID)
	}
	var slowConn *transport.RelayConn
	dn, sn, err := c.d.RaceDirect(ctx, machineID, addrs, func(rctx context.Context) (*Machine, error) {
		rn, conn, err := c.openRelayTunnel(rctx, machineID)
		if err == nil {
			slowConn = conn
		}
		return rn, err
	})
	if err != nil {
		return nil, err
	}
	if dn != nil {
		dn.direct = true
		return c.storeMachine(machineID, gen, nil, dn)
	}
	return c.storeMachine(machineID, gen, slowConn, sn)
}

func (c *Client) storeMachine(machineID string, gen int, conn *transport.RelayConn, n *Machine) (*Machine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.gen != gen || (conn != nil && !c.linked && c.conn.Load() != conn) {
		n.h2.Close()
		if c.gen != gen {
			return nil, fmt.Errorf("connect to %s: credentials changed", machineID)
		}
		return nil, fmt.Errorf("connect to %s: relay changed", machineID)
	}
	if existing := c.machines[machineID]; existing != nil {
		if existing.h2.Alive() {
			n.h2.Close()
			return existing, nil
		}
		existing.h2.Close()
	}
	c.machines[machineID] = n
	shouldPunch := !n.direct && c.d.Role == relayv1.Role_ROLE_OWNER && c.canAutoPunchLocked(machineID)
	if shouldPunch {
		if c.lastPunch == nil {
			c.lastPunch = map[string]time.Time{}
		}
		c.lastPunch[machineID] = time.Now()
	}
	if shouldPunch {
		go func(id string, cur *Machine) {
			uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if dn := c.dialPunchedQUIC(uctx, id, cur); dn != nil {
				c.replaceMachine(id, cur, dn)
			}
		}(machineID, n)
	}
	return n, nil
}

func (c *Client) canAutoPunchLocked(machineID string) bool {
	if c.lastPunch == nil {
		return true
	}
	return time.Since(c.lastPunch[machineID]) >= 10*time.Second
}

// ResetPunchCooldown clears automatic IPv6 punch cooldowns after a network change.
func (c *Client) ResetPunchCooldown() {
	c.mu.Lock()
	c.lastPunch = nil
	c.mu.Unlock()
}

func (c *Client) replaceMachine(machineID string, expected, replacement *Machine) bool {
	if replacement == nil {
		return false
	}
	replacement.direct = true
	c.mu.Lock()
	if c.closed || !replacement.h2.Alive() || c.machines[machineID] != expected {
		c.mu.Unlock()
		replacement.h2.Close()
		return false
	}
	c.machines[machineID] = replacement
	c.mu.Unlock()
	if expected != nil {
		expected.h2.Close()
	}
	if c.d.OnUpgrade != nil {
		c.d.OnUpgrade(machineID, replacement.Path(), replacement.RTT())
	}
	return true
}

// UpgradeToDirect attempts to upgrade cached relayed tunnels (and, when
// preferLAN is true, non-LAN tunnels after returning to Wi-Fi) onto a faster
// direct QUIC/TCP tunnel. Failed attempts leave existing tunnels untouched.
func (c *Client) UpgradeToDirect(ctx context.Context, preferLAN bool) []string {
	type cand struct {
		id  string
		cur *Machine
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	var cands []cand
	for id, n := range c.machines {
		if n == nil || !n.h2.Alive() {
			continue
		}
		if !n.direct || (preferLAN && n.path != "quic_lan" && n.path != "direct_lan" && n.path != "local") {
			cands = append(cands, cand{id: id, cur: n})
		}
	}
	c.mu.Unlock()
	if len(cands) == 0 {
		return nil
	}
	var (
		mu       sync.Mutex
		upgraded []string
		wg       sync.WaitGroup
	)
	for _, cd := range cands {
		wg.Add(1)
		go func(cd cand) {
			defer wg.Done()
			var addrs []string
			if c.d.Direct != nil {
				addrs = c.d.Direct(cd.id)
			}
			if cd.cur.direct && preferLAN {
				addrs = filterLANAddrs(addrs)
			}
			if len(addrs) > 0 {
				dctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
				dn, err := c.d.DialDirect(dctx, cd.id, addrs)
				cancel()
				if err == nil && dn != nil {
					if c.replaceMachine(cd.id, cd.cur, dn) {
						mu.Lock()
						upgraded = append(upgraded, cd.id)
						mu.Unlock()
					}
					return
				}
			}
			if !cd.cur.direct && c.d.Role == relayv1.Role_ROLE_OWNER {
				pctx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
				dn := c.dialPunchedQUIC(pctx, cd.id, cd.cur)
				cancel()
				if dn != nil && c.replaceMachine(cd.id, cd.cur, dn) {
					mu.Lock()
					upgraded = append(upgraded, cd.id)
					mu.Unlock()
				}
			}
		}(cd)
	}
	wg.Wait()
	return upgraded
}

func filterLANAddrs(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		hp := strings.TrimPrefix(a, "quic://")
		host, _, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil && (v4[0] == 10 || (v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) || (v4[0] == 192 && v4[1] == 168)) {
			out = append(out, a)
		}
	}
	return out
}

func localGlobalIPv6s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				continue
			}
			s := ip.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
			if len(out) >= 4 {
				return out
			}
		}
	}
	return out
}

// dialPunchedQUIC upgrades a relayed owner tunnel to direct QUIC over global
// IPv6 by asking the machine (over the existing E2E relay tunnel) to send UDP
// hole-punch packets from its explicitly bound IPv6 QUIC socket toward our
// bound UDP socket, opening stateful IPv6 firewalls on both ends.
func (c *Client) dialPunchedQUIC(ctx context.Context, machineID string, cur *Machine) *Machine {
	if cur == nil || !cur.h2.Alive() {
		return nil
	}
	v6s := localGlobalIPv6s()
	if len(v6s) == 0 {
		return nil
	}
	pc, err := net.ListenPacket("udp6", net.JoinHostPort(v6s[0], "0"))
	if err != nil {
		pc, err = net.ListenPacket("udp6", "[::]:0")
		if err != nil {
			return nil
		}
	}
	ua, ok := pc.LocalAddr().(*net.UDPAddr)
	if !ok || ua.Port <= 0 {
		_ = pc.Close()
		return nil
	}
	pstr := strconv.Itoa(ua.Port)
	targets := make([]string, 0, len(v6s))
	for _, ip := range v6s {
		targets = append(targets, net.JoinHostPort(ip, pstr))
	}
	qtr := &quic.Transport{Conn: pc}
	cleanup := func() {
		_ = qtr.Close()
		_ = pc.Close()
	}

	rpcCtx, rpcCancel := context.WithTimeout(ctx, 2*time.Second)
	res, err := cur.Info.PunchDirect(rpcCtx, &machinev1.PunchDirectRequest{Targets: targets})
	rpcCancel()
	if err != nil || len(res.GetDirectQuicAddrs()) == 0 {
		cleanup()
		return nil
	}

	var remoteV6 []string
	for _, raw := range res.GetDirectQuicAddrs() {
		hp := strings.TrimPrefix(strings.TrimSpace(raw), "quic://")
		host, _, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			remoteV6 = append(remoteV6, hp)
			if dst, rerr := net.ResolveUDPAddr("udp6", hp); rerr == nil {
				_, _ = qtr.WriteTo([]byte("HMP1"), dst)
			}
		}
	}
	if len(remoteV6) == 0 {
		cleanup()
		return nil
	}

	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	type dialRes struct {
		h3   *transport.H3Client
		name string
		addr string
		rtt  time.Duration
	}
	ch := make(chan dialRes, len(remoteV6))
	for _, addr := range remoteV6 {
		go func(addr string) {
			started := time.Now()
			h3, sub, err := transport.DialQUICOnTransport(rctx, qtr, addr, c.d.Key.CryptoSigner(), c.d.Credentials, c.d.Trust, machineID)
			if err != nil {
				ch <- dialRes{}
				return
			}
			ch <- dialRes{h3: h3, name: sub.MachineName, addr: addr, rtt: time.Since(started)}
		}(addr)
	}
	var winner dialRes
	for range remoteV6 {
		r := <-ch
		if r.h3 != nil {
			if winner.h3 == nil {
				winner = r
				cancel()
			} else {
				_ = r.h3.Close()
			}
		}
	}
	if winner.h3 == nil {
		cleanup()
		return nil
	}
	winner.h3.AttachPacketTransport(qtr, pc)
	m := newMachineQUIC(machineID, winner.name, winner.h3)
	m.direct = true
	m.path = "quic_ipv6"
	m.RecordRTT(winner.rtt)
	if c.d.OnDirectSuccess != nil {
		c.d.OnDirectSuccess(machineID, "quic://"+winner.addr)
	}
	return m
}

// AdoptDirect installs a pre-dialed direct tunnel n for machineID if c is still
// active and has no live tunnel for that machine, closing n otherwise.
func (c *Client) AdoptDirect(machineID string, n *Machine) bool {
	if n == nil {
		return false
	}
	n.direct = true
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !n.h2.Alive() {
		n.h2.Close()
		return false
	}
	if existing := c.machines[machineID]; existing != nil {
		if existing.h2.Alive() {
			n.h2.Close()
			return false
		}
		existing.h2.Close()
	}
	c.machines[machineID] = n
	return true
}

// Reset drops the machine connections, made or being made: the credentials
// they presented changed. (A machine refuses credentials after the TLS 1.3
// handshake has completed on this side.)
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for _, n := range c.machines {
		n.h2.Close()
	}
	c.machines = map[string]*Machine{}
}

// ForgetRenamed drops the connections to the machines that presented
// another name than names (by machine id) has for them: made before a
// machine was renamed, they are made again, and say what it presents now.
func (c *Client) ForgetRenamed(names map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, n := range c.machines {
		if want, ok := names[id]; ok && n.Name != want {
			n.h2.Close()
			delete(c.machines, id)
		}
	}
}

// Forget drops the current cached tunnel deliberately.
func (c *Client) Forget(machineID string) {
	c.mu.Lock()
	if n := c.machines[machineID]; n != nil {
		n.h2.Close()
		delete(c.machines, machineID)
	}
	c.mu.Unlock()
}

// ForgetDirect drops cached direct tunnels while leaving relayed tunnels intact.
func (c *Client) ForgetDirect() {
	c.mu.Lock()
	for id, n := range c.machines {
		if !n.direct {
			continue
		}
		n.h2.Close()
		delete(c.machines, id)
	}
	c.mu.Unlock()
}

// ProbeOrForgetTunnels checks each cached machine with GetInfo after a
// network change. A healthy relay can still carry an unresponsive tunnel.
func (c *Client) ProbeOrForgetTunnels(ctx context.Context) {
	c.mu.Lock()
	tunnels := make([]*Machine, 0, len(c.machines))
	for _, n := range c.machines {
		tunnels = append(tunnels, n)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range tunnels {
		wg.Go(func() {
			timeout := 1500 * time.Millisecond
			if !n.direct {
				timeout = max(timeout, 3*n.RTT())
			}
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			started := time.Now()
			_, err := n.Info.GetInfo(pctx, &machinev1.GetInfoRequest{})
			if err != nil {
				c.ForgetMachine(n)
				return
			}
			n.RecordRTT(time.Since(started))
		})
	}
	wg.Wait()
}

// HasReplacement reports whether machineID now has a different live cached
// tunnel than old (e.g. after UpgradeToDirect or auto-punch replaced a relayed
// tunnel with a direct QUIC tunnel).
func (c *Client) HasReplacement(machineID string, old *Machine) bool {
	c.mu.Lock()
	cur := c.machines[machineID]
	c.mu.Unlock()
	return cur != nil && cur != old && cur.h2.Alive()
}

// ForgetMachine drops the tunnel used by a failed call. A later failure of
// an old tunnel must not close the replacement another caller has made.
func (c *Client) ForgetMachine(n *Machine) {
	if n == nil {
		return
	}
	c.mu.Lock()
	if c.machines[n.MachineID] != n {
		c.mu.Unlock()
		return
	}
	delete(c.machines, n.MachineID)
	c.mu.Unlock()
	n.h2.Close()
}

// FanOut runs fn against each machine concurrently (at most 8 at a time) and
// returns per-machine errors. If a call fails on a cached tunnel that died or
// was replaced by an upgrade, it drops that tunnel and retries once on a fresh
// connection.
func (c *Client) FanOut(ctx context.Context, machineIDs []string, fn func(ctx context.Context, n *Machine) error) map[string]error {
	errs := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, id := range machineIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			nctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			n, cached, err := c.machineWithOrigin(nctx, id)
			if err == nil {
				err = fn(nctx, n)
				if err != nil && cached && nctx.Err() == nil && (!n.Alive() || c.HasReplacement(id, n)) {
					c.ForgetMachine(n)
					n, err = c.Machine(nctx, id)
					if err == nil {
						err = fn(nctx, n)
					}
				}
			}
			if err != nil {
				c.ForgetMachine(n)
				mu.Lock()
				errs[id] = err
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	return errs
}
