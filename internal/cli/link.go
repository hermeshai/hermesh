package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/flock"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// The link (docs/cli.md). Each command used to make its own connection to
// the relay and its own end-to-end tunnels, and close them when it ended:
// several round trips before its first call, for every command and every
// hook of an agent on this computer. The link is one process per CLI home
// that keeps the relay connection and the tunnels, as the MCP gateway does
// for the agents it serves (Peer). A command reaches the relay's control
// services and its machines through it, over a local endpoint only this
// user can open, and starts it when none runs.
//
// The link only carries calls: a command still checks its grant, lists the
// machines and decides everything itself, with the files in its home. And
// nothing depends on it: whenever the link does not answer, or says it
// cannot vouch for its connection, the command connects by itself, as it
// did before.

const (
	// linkIdle: a link no command used for this long ends.
	linkIdle = 10 * time.Minute
	// linkTrust: the link hands out a relay connection it heard from within
	// this long (the relay pings every 15 s) while the computer's network
	// addresses are those it had; otherwise it pings first, for linkPing at
	// most. A connection that died while the computer slept or changed
	// networks is so never handed out.
	linkTrust = 20 * time.Second
	linkPing  = 1500 * time.Millisecond
	// linkAnswer: how long a command waits for the link's answer about the
	// relay before it connects by itself; linkConnect: how long for its
	// first calls through it (the grant check and the machine list).
	linkAnswer  = 3 * time.Second
	linkConnect = 5 * time.Second
)

// linkRequest is a command's first line to the link: its version (a link
// of another version ends, and the next command starts its own), and the
// machine it wants ("" for the relay's control services), with the name the
// relay last listed it under: a tunnel on which the machine presented
// another (it was renamed since) is made again.
type linkRequest struct {
	Version string `json:"version"`
	Machine string `json:"machine,omitempty"`
	Listed  string `json:"listed,omitempty"`
}

// linkReply is the link's line back. After an ok one the connection
// carries HTTP/2 to the relay or the machine; name is the machine's, as it
// presented it in the end-to-end handshake.
type linkReply struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error,omitempty"`
}

// readLine reads up to a newline, a byte at a time: what follows on the
// connection is not this reader's.
func readLine(r io.Reader) ([]byte, error) {
	var line []byte
	b := make([]byte, 1)
	for len(line) < 4096 {
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			return line, nil
		}
		line = append(line, b[0])
	}
	return nil, errors.New("line too long")
}

// linkDial reaches the relay (machineID "") or a machine through this
// home's link (peer.Dialer.Link). When none answers it starts one, for the
// next command, and fails: this command connects by itself.
func (a *app) linkDial(ctx context.Context, machineID string) (net.Conn, string, error) {
	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	conn, err := localipc.Dial(dctx, paths.Link(a.home))
	cancel()
	if err != nil {
		a.startLink()
		return nil, "", err
	}
	deadline := time.Now().Add(linkAnswer)
	if machineID != "" { // the link may have to reach the machine first, as this command would
		deadline = time.Now().Add(20 * time.Second)
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	req := linkRequest{Version: machine.Version, Machine: machineID}
	a.mu.Lock()
	for _, m := range a.st.Machines {
		if m.ID == machineID {
			req.Listed = m.Name
		}
	}
	a.mu.Unlock()
	line, _ := json.Marshal(req)
	var res linkReply
	if _, err = conn.Write(append(line, '\n')); err == nil {
		if line, err = readLine(conn); err == nil {
			err = json.Unmarshal(line, &res)
		}
	}
	if err == nil && !res.OK {
		err = errors.New("link: " + res.Error)
	}
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	conn.SetDeadline(time.Time{})
	return conn, res.Name, nil
}

// startLink starts this home's link, apart from this command, once.
func (a *app) startLink() {
	a.linkStart.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		cmd := exec.Command(exe, "link")
		cmd.Env = os.Environ()
		detach(cmd)
		if cmd.Start() == nil {
			cmd.Process.Release()
		}
	})
}

func (a *app) linkCmd() *cobra.Command {
	idle := linkIdle
	cmd := &cobra.Command{
		Use:    "link",
		Hidden: true, // started by the commands that find none
		Short:  "Keep this agent's connection to the relay and its machines between commands",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.mem != nil {
				return fail(exitUsage, "a caller that keeps the credentials keeps the connection too")
			}
			return runLink(cmd.Context(), a.home, a.hc, idle)
		},
	}
	cmd.Flags().DurationVar(&idle, "idle", idle, "end after this long without a command")
	return cmd
}

// link is a home's link while it runs.
type link struct {
	home string
	hc   *http.Client
	// client, relay and owner name the pairing it serves: once the home
	// holds another (or none), it ends.
	client, relay, owner string
	stop                 context.CancelFunc

	mu     sync.Mutex
	peer   *Peer                   // the kept connection
	creds  *identityv1.Credentials // what it was made with
	active int                     // commands connected now
	used   time.Time               // when the last one left
	addrs  string                  // the computer's addresses when the relay last answered
}

// runLink serves home's link until no command used it for idle, the home's
// pairing went or changed, or a command of another version asked. It
// returns at once when the home is not paired or its link runs already.
func runLink(ctx context.Context, home string, hc *http.Client, idle time.Duration) error {
	a := &app{home: home, hc: hc}
	if err := a.load(); err != nil {
		return nil
	}
	lock, err := flock.Acquire(filepath.Join(home, "link"))
	if err != nil {
		return nil
	}
	defer lock.Release()
	ln, err := localipc.Listen(paths.Link(home))
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	k := &link{home: home, hc: hc, peer: &Peer{}, client: a.g.ClientId, relay: a.st.RelayURL, owner: a.st.OwnerID,
		stop: stop, used: time.Now(), addrs: localAddrs()}
	defer func() {
		k.mu.Lock()
		k.peer.Close()
		k.mu.Unlock()
	}()
	defer os.Remove(paths.Link(home))
	defer ln.Close()
	go k.warm(ctx, a)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go k.serve(ctx, conn)
		}
	}()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		k.mu.Lock()
		unused := k.active == 0 && time.Since(k.used) > idle
		k.mu.Unlock()
		if _, err := os.Stat(a.statePath()); unused || err != nil {
			return nil
		}
	}
}

// peerFor is the kept connection for a's credentials. When the home holds
// others than it was made with (a renewed, changed or re-issued grant, a
// longer root chain), it is made anew: the relay admitted the old one with
// the old grant and goes by that, and the machines were shown it. The
// commands still using the old one keep it until they end.
func (k *link) peerFor(a *app) *Peer {
	creds := a.credentials()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.creds != nil && !proto.Equal(creds, k.creds) {
		k.peer.Close()
		k.peer = &Peer{}
	}
	k.creds = creds
	return k.peer
}

// warm connects to the relay ahead of the next command.
func (k *link) warm(ctx context.Context, a *app) {
	if c, err := k.peerFor(a).open(ctx, a); err == nil {
		c.Control()
		c.Close()
	}
}

// serve answers one command's connection, then carries its calls until it
// closes.
func (k *link) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	k.mu.Lock()
	k.active++
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		k.active--
		k.used = time.Now()
		k.mu.Unlock()
	}()
	refuse := func(why string) {
		b, _ := json.Marshal(linkReply{Error: why})
		conn.Write(append(b, '\n'))
	}
	// failed refuses with err; a relay that no longer admits this agent
	// (revoked) ends the link: the commands say so themselves.
	failed := func(c *peer.Client, err error) {
		refuse(err.Error())
		if errors.Is(err, transport.ErrAdmission) || (c != nil && c.Refused() != nil) {
			k.stop()
		}
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	line, err := readLine(conn)
	var req linkRequest
	if err != nil || json.Unmarshal(line, &req) != nil {
		return
	}
	if req.Version != machine.Version {
		refuse("this link is of version " + machine.Version)
		k.stop()
		return
	}
	// The home as it is now: with the grant or the root chain a command
	// adopted since (peerFor).
	a := &app{home: k.home, hc: k.hc}
	if err := a.load(); err != nil || a.g.ClientId != k.client || a.st.RelayURL != k.relay || a.st.OwnerID != k.owner {
		refuse("this link served another pairing")
		k.stop()
		return
	}
	if exp := a.g.GetExpiresAt(); exp != nil && time.Now().After(exp.AsTime()) {
		// The relay judges a grant when it admits a connection: a command
		// that connects by itself is told its grant expired.
		refuse("the grant expired")
		k.stop()
		return
	}
	c, err := k.peerFor(a).open(ctx, a)
	if err != nil {
		failed(nil, err)
		return
	}
	defer c.Close()
	if !k.answers(ctx, a, c) {
		refuse("the relay connection did not answer")
		return
	}
	var carry http.Handler
	reply := linkReply{OK: true}
	if req.Machine == "" {
		hc, err := c.Control()
		if err == nil {
			err = c.Admitted(ctx)
		}
		if err != nil {
			failed(c, err)
			return
		}
		carry = carryTo(hc.Client, "http://relay", func() {})
	} else {
		n, err := c.Machine(ctx, req.Machine)
		if err == nil && req.Listed != "" && n.Name != req.Listed {
			c.Forget(req.Machine) // renamed since this tunnel was made?
			n, err = c.Machine(ctx, req.Machine)
		}
		if err != nil {
			failed(c, err)
			return
		}
		reply.Name = n.Name
		carry = carryTo(n.HTTP(), "https://machine", func() { c.Forget(req.Machine) })
	}
	b, _ := json.Marshal(reply)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	transport.ServeH2(ctx, conn, carry)
}

// answers says whether the relay connection can be handed to a command: it
// was heard from a moment ago on the network the computer still has, or it
// answers a ping now. One that does not is closed, and dialed again for
// the next command.
func (k *link) answers(ctx context.Context, a *app, c *peer.Client) bool {
	addrs := localAddrs()
	k.mu.Lock()
	same := addrs == k.addrs
	k.mu.Unlock()
	if heard := c.Heard(); same && time.Since(heard) < linkTrust {
		return true
	}
	pctx, cancel := context.WithTimeout(ctx, linkPing)
	err := c.Ping(pctx)
	cancel()
	if err != nil {
		c.Drop()
		go k.warm(ctx, a)
		return false
	}
	k.mu.Lock()
	k.addrs = addrs
	k.mu.Unlock()
	return true
}

// localAddrs is the computer's network addresses now, in one string.
func localAddrs() string {
	addrs, _ := net.InterfaceAddrs()
	var out []string
	for _, a := range addrs {
		out = append(out, a.String())
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

// carryTo passes a command's calls on to base over hc, each answer as it
// comes; failed is told when one could not be carried.
func carryTo(hc *http.Client, base string, failed func()) http.Handler {
	u, _ := url.Parse(base)
	return &httputil.ReverseProxy{
		Rewrite:       func(r *httputil.ProxyRequest) { r.SetURL(u) },
		Transport:     hc.Transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			failed()
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}
