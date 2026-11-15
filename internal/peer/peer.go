// Package peer connects an agent or the owner device to machines: one relay
// connection, then an end-to-end TLS tunnel per machine.
package peer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
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
}

// Client is a relay connection plus cached E2E tunnels to machines.
type Client struct {
	d *Dialer
	// conn is read without mu, so that asking whether the client is
	// connected never waits for a dial.
	conn atomic.Pointer[transport.RelayConn]

	mu       sync.Mutex
	closed   bool
	holds    int // Closes that only end a Hold
	machines map[string]*Machine
	warming  map[string]chan struct{} // closed when the attempt ends
	gen      int                      // counts Resets

	// linked: the relay and machines are reached through Dialer.Link, with
	// no connection of this client's own (conn is nil until it fails).
	linkMu      sync.Mutex
	linked      bool
	linkControl *transport.H2Client
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
	h2      *transport.H2Client
	direct  bool
}

// newMachine is the typed clients for machineID over h2, its tunnel.
func newMachine(machineID, name string, h2 *transport.H2Client) *Machine {
	const base = "https://machine"
	return &Machine{MachineID: machineID, Name: name, h2: h2,
		Session: sessionv1connect.NewSessionServiceClient(h2, base, transport.ClientNoCompression),
		Info:    machinev1connect.NewMachineServiceClient(h2, base, transport.ClientNoCompression),
		Admin:   machinev1connect.NewMachineAdminServiceClient(h2, base, transport.ClientNoCompression),
		Her:     herv1connect.NewHerServiceClient(h2, base, transport.ClientNoCompression),
		Screen:  screenv1connect.NewScreenServiceClient(h2, base, transport.ClientNoCompression)}
}

// HTTP is the tunnel's HTTP/2 client, for the machine's plain HTTP routes
// (Her's console): requests go to "https://machine/…".
func (n *Machine) HTTP() *http.Client { return n.h2.Client }

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
	return newMachine(machineID, sub.MachineName, h2)
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
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, errors.New("peer client closed")
		}
		if n := c.machines[machineID]; n != nil && n.h2.Alive() {
			c.mu.Unlock()
			return n, nil
		}
		w := c.warming[machineID]
		if w == nil {
			w = make(chan struct{})
			c.warming[machineID] = w
			c.mu.Unlock()
			n, err := c.dialAndFinishWarm(ctx, machineID, w)
			return n, err
		}
		c.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			return nil, ctx.Err()
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
// need them.
func (c *Client) Warm(machineIDs []string) {
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
			c.dialMachine(ctx, id)
			cancel()
			c.mu.Lock()
			delete(c.warming, id)
			c.mu.Unlock()
			close(done)
		}()
	}
}

const (
	directStagger = 25 * time.Millisecond
	directHeadLAN = 80 * time.Millisecond
	directHeadWAN = 280 * time.Millisecond
	directTimeout = 2 * time.Second
)

func directHeadFor(addrs []string) time.Duration {
	for _, hp := range addrs {
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
	ch := make(chan res, len(addrs))
	for i, addr := range addrs {
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
			ch <- res{n: newMachine(machineID, sub.MachineName, h2), addr: addr}
		}(time.Duration(i)*directStagger, addr)
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
	return newMachine(machineID, sub.MachineName, h2), conn, nil
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
	return n, nil
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
// returns per-machine errors.
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
			n, err := c.Machine(nctx, id)
			if err == nil {
				err = fn(nctx, n)
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
