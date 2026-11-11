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
// Warm attempt in progress, and makes a failed one again.
func (c *Client) Machine(ctx context.Context, machineID string) (*Machine, error) {
	for {
		c.mu.Lock()
		if n := c.machines[machineID]; n != nil && n.h2.Alive() {
			c.mu.Unlock()
			return n, nil
		}
		w := c.warming[machineID]
		c.mu.Unlock()
		if w == nil {
			return c.dialMachine(ctx, machineID)
		}
		select {
		case <-w:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Warm connects to machines in the background, ahead of the calls likely to
// need them.
func (c *Client) Warm(machineIDs []string) {
	for _, id := range machineIDs {
		c.mu.Lock()
		if c.machines[id] != nil || c.warming[id] != nil {
			c.mu.Unlock()
			continue
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

func (c *Client) dialMachine(ctx context.Context, machineID string) (*Machine, error) {
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()
	n := c.machineHere(ctx, machineID)
	if n == nil {
		n = c.machineByLink(ctx, machineID)
	}
	if n != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.gen != gen {
			n.h2.Close()
			return nil, fmt.Errorf("connect to %s: credentials changed", machineID)
		}
		c.machines[machineID] = n
		return n, nil
	}
	conn, err := c.relay(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := conn.OpenTunnel(machineID)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tc, sub, err := transport.ClientHandshake(hctx, raw, c.d.Key.CryptoSigner(), c.d.Credentials, c.d.Trust, machineID)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("connect to %s: %w", machineID, conn.Explain(err))
	}
	h2, err := transport.NewTunnelClient(tc)
	if err != nil {
		tc.Close()
		return nil, err
	}
	n = newMachine(machineID, sub.MachineName, h2)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		h2.Close()
		return nil, fmt.Errorf("connect to %s: credentials changed", machineID)
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

// Forget drops a cached tunnel (after an error).
func (c *Client) Forget(machineID string) {
	c.mu.Lock()
	if n := c.machines[machineID]; n != nil {
		n.h2.Close()
		delete(c.machines, machineID)
	}
	c.mu.Unlock()
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
				c.Forget(id)
				mu.Lock()
				errs[id] = err
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	return errs
}
