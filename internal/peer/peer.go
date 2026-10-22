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
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
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
}

// Client is a relay connection plus cached E2E tunnels to machines.
type Client struct {
	d    *Dialer
	conn *transport.RelayConn

	mu       sync.Mutex
	closed   bool
	machines map[string]*Machine
	warming  map[string]chan struct{} // closed when the attempt ends
	gen      int                      // counts Resets
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
	h2      *transport.H2Client
}

func (d *Dialer) Connect(ctx context.Context) (*Client, error) {
	td := &transport.Dialer{RelayURL: d.RelayURL, Role: d.Role, Key: d.Key, Credentials: d.Credentials, HTTPClient: d.HTTPClient, Pipeline: d.Pipeline}
	conn, err := td.Dial(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{d: d, conn: conn, machines: map[string]*Machine{}, warming: map[string]chan struct{}{}}, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, n := range c.machines {
		n.h2.Close()
	}
	return c.conn.Close()
}

// Done is closed when the current relay connection is lost.
func (c *Client) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Done()
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
	if !c.conn.IsClosed() {
		return c.conn, nil
	}
	if err := c.conn.Explain(nil); errors.Is(err, transport.ErrAdmission) {
		return nil, err // refused: dialing again would be too
	}
	for _, n := range c.machines {
		n.h2.Close()
	}
	c.machines = map[string]*Machine{}
	td := &transport.Dialer{RelayURL: c.d.RelayURL, Role: c.d.Role, Key: c.d.Key, Credentials: c.d.Credentials, HTTPClient: c.d.HTTPClient}
	conn, err := td.Dial(ctx)
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return conn, nil
}

// Control returns an HTTP client for the relay control services.
func (c *Client) Control() (*transport.H2Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := c.relay(ctx)
	if err != nil {
		return nil, err
	}
	return conn.Control()
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
	h2, err := transport.NewH2Client(tc)
	if err != nil {
		tc.Close()
		return nil, err
	}
	const base = "https://machine"
	n := &Machine{MachineID: machineID, Name: sub.MachineName, h2: h2,
		Session: sessionv1connect.NewSessionServiceClient(h2, base, transport.ClientNoCompression),
		Info:    machinev1connect.NewMachineServiceClient(h2, base, transport.ClientNoCompression),
		Admin:   machinev1connect.NewMachineAdminServiceClient(h2, base, transport.ClientNoCompression)}
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
