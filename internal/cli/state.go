// Package cli implements the `hermesh` command, which clients (usually AI
// agents) use to run coding agents on the owner's machines.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// state is persisted in $HERMESH_HOME/state.json (0600). It holds only
// public objects; the private key lives next to it in client.key.
type state struct {
	RelayURL  string `json:"relay_url"`
	OwnerID   string `json:"owner_id"`
	RootChain []byte `json:"root_chain,omitempty"`
	Grant     []byte `json:"grant,omitempty"`
	// Machines is what the last machine list showed, so that the next
	// command connects to its machine while the list is on its way.
	Machines []seenMachine `json:"machines,omitempty"`
	// InboxKey is the inbox public key the post office has (message.go).
	InboxKey []byte `json:"inbox_key,omitempty"`
}

type seenMachine struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online,omitempty"`
}

type app struct {
	home   string
	output string
	// as is the caller's name under this agent (--as, docs/agent-names.md).
	as string
	hc *http.Client
	// mem holds the credentials when the caller keeps them (Exec), in
	// place of the files in home.
	mem *ExecOptions

	st    state
	key   *identity.SoftwareKey
	trust *identity.Trust
	mu    sync.Mutex // grant, read by handshakes while it is synced
	grant *identityv1.Signed
	g     *identityv1.Grant
	// inboxWaiting is how many messages wait in this client's inbox, as
	// the relay said while the command connected.
	inboxWaiting uint32
	// extra are data keys a command adds to whatever document it ends with
	// (message send --wait: the message, beside the session).
	extra []field
	// link: commands reach the relay and their machines through this home's
	// link (link.go); linkStart starts it once when none answers.
	link      bool
	linkStart sync.Once
}

func homeDir() string {
	if v := os.Getenv("HERMESH_HOME"); v != "" {
		return v
	}
	cfg, err := os.UserConfigDir()
	if err != nil || cfg == "" {
		h, _ := os.UserHomeDir()
		cfg = filepath.Join(h, ".config")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		cfg = xdg
	} else if h, _ := os.UserHomeDir(); h != "" {
		cfg = filepath.Join(h, ".config")
	}
	return filepath.Join(cfg, "hermesh")
}

func (a *app) keyPath() string   { return filepath.Join(a.home, "client.key") }
func (a *app) statePath() string { return filepath.Join(a.home, "state.json") }

func (a *app) loadKey() error {
	k, err := identity.LoadOrCreateKey(a.keyPath())
	if err != nil {
		return err
	}
	a.key = k
	return nil
}

var errNotLoggedIn error = &exitError{code: exitDenied, kind: "not_paired", err: errors.New("not paired yet"),
	hint: "run `hermesh auth login` (the official center; or give a center URL or connection address) and have the owner approve the request it shows on their Mac"}

// load reads state and verifies the stored grant against the stored root.
func (a *app) load() error {
	if a.mem != nil {
		return a.loadMem()
	}
	b, err := os.ReadFile(a.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return errNotLoggedIn
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &a.st); err != nil {
		return err
	}
	if a.st.Grant == nil {
		return errNotLoggedIn
	}
	if err := a.loadKey(); err != nil {
		return err
	}
	a.trust = identity.NewTrust(a.st.OwnerID)
	rc := &identityv1.RootChain{}
	if err := proto.Unmarshal(a.st.RootChain, rc); err != nil {
		return err
	}
	if _, err := a.trust.AcceptRootChain(rc); err != nil {
		return fmt.Errorf("stored owner root chain: %w", err)
	}
	a.grant = &identityv1.Signed{}
	if err := proto.Unmarshal(a.st.Grant, a.grant); err != nil {
		return err
	}
	a.g = &identityv1.Grant{}
	return identity.Unverified(a.grant, a.g)
}

func (a *app) save() error {
	if a.mem != nil {
		if a.mem.Adopted != nil {
			a.mu.Lock()
			grant := a.grant
			a.mu.Unlock()
			a.mem.Adopted(a.trust.RootChain(), grant)
		}
		return nil
	}
	if err := os.MkdirAll(a.home, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a.st, "", "  ")
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath())
}

// adopt verifies credentials for this key and stores them.
func (a *app) adopt(rootChain *identityv1.RootChain, grant *identityv1.Signed) error {
	if rootChain != nil {
		if _, err := a.trust.AcceptRootChain(rootChain); err != nil {
			return err
		}
	}
	g, pub, err := a.trust.VerifyGrant(grant, time.Now())
	if err != nil {
		return err
	}
	if !pub.Equal(a.key.Public()) {
		return errors.New("grant is for a different key")
	}
	a.mu.Lock()
	a.grant, a.g = grant, g
	a.mu.Unlock()
	a.st.RootChain, _ = proto.Marshal(a.trust.RootChain())
	a.st.Grant, _ = proto.Marshal(grant)
	return a.save()
}

func (a *app) credentials() *identityv1.Credentials {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &identityv1.Credentials{RootChain: a.trust.RootChain(), Subject: &identityv1.Credentials_Grant{Grant: a.grant}}
}

// connect opens the relay connection, picks up a renewed or re-issued grant
// if the owner published one, and lists the machines this client can reach.
// Admission, the grant check and the list go out together, with E2E
// handshakes to the machines warm picks from the last list: the ones the
// command will likely use.
func (a *app) connect(ctx context.Context, warm func([]machineRef) []machineRef) (*peer.Client, []machineRef, error) {
	if err := a.load(); err != nil {
		return nil, nil, err
	}
	warn := func(warnings []string) {
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}
	if a.link && a.kept() == nil {
		// Through this home's link, when it answers (link.go): the first
		// calls only read, so when it does not carry them in time the
		// command starts over by itself, as it would without a link.
		d := a.dialer(true)
		d.Link = a.linkDial
		if c, err := d.Connect(ctx); err == nil {
			_, err := c.Control()
			switch {
			case c.Linked():
				lctx, cancel := context.WithTimeout(ctx, linkConnect)
				machines, warnings, err := a.settle(lctx, c, warm)
				cancel()
				if ee := (*exitError)(nil); err == nil || errors.As(err, &ee) {
					warn(warnings)
					return orNil(c, err), machines, err
				}
			case err != nil: // no link answered, and the relay could not be reached, or refused
				c.Close()
				if errors.Is(err, transport.ErrAdmission) {
					return nil, nil, a.refused(err)
				}
				return nil, nil, err
			default: // no link answered: c connected by itself
				machines, warnings, err := a.settle(ctx, c, warm)
				warn(warnings)
				return orNil(c, err), machines, err
			}
		}
	}
	var c *peer.Client
	var err error
	if p := a.kept(); p != nil {
		c, err = p.open(ctx, a)
	} else {
		c, err = a.dial(ctx, true)
	}
	if err != nil {
		return nil, nil, err
	}
	machines, warnings, err := a.settle(ctx, c, warm)
	warn(warnings)
	return orNil(c, err), machines, err
}

// orNil is c unless err.
func orNil(c *peer.Client, err error) *peer.Client {
	if err != nil {
		return nil
	}
	return c
}

// settle makes the first calls on c: the grant check and the machine list,
// with the handshakes warm picks. It closes c when it fails; warnings are
// what to tell the caller besides.
func (a *app) settle(ctx context.Context, c *peer.Client, warm func([]machineRef) []machineRef) (machines []machineRef, warnings []string, err error) {
	before := a.credentials()
	synced := make(chan error, 1)
	var inbox error
	go func() {
		_, err := a.syncGrant(ctx, c)
		if err == nil {
			inbox = a.registerInbox(ctx, c)
		}
		synced <- err
	}()
	if warm != nil {
		var seen []machineRef
		for _, m := range a.st.Machines {
			seen = append(seen, machineRef{ID: m.ID, Name: m.Name, Online: m.Online})
		}
		c.Warm(ids(warm(seen)))
	}
	listed, err := listMachines(ctx, c)
	serr := <-synced
	if inbox != nil {
		warnings = append(warnings, fmt.Sprint("registering this agent's inbox: ", inbox))
	}
	switch {
	case errors.Is(serr, errRevoked), errors.Is(serr, transport.ErrAdmission):
		c.Close()
		return nil, warnings, a.refused(serr)
	case errors.Is(serr, errBadUpdate):
		warnings = append(warnings, serr.Error())
	}
	if p := a.kept(); p != nil {
		p.adopt(a)
	} else if !proto.Equal(before, a.credentials()) {
		c.Reset() // warmed with the old grant
	}
	if err != nil {
		c.Close()
		if errors.Is(err, transport.ErrAdmission) {
			return nil, warnings, a.refused(err)
		}
		return nil, warnings, err
	}
	machines = a.machineRefs(listed) // with the root the sync may have renewed
	a.remember(machines)
	if c.Linked() {
		// The link's tunnels outlive a command: one made before a machine
		// was renamed still says its old name. It is made again (linkDial
		// names the machine as listed), and checkName judges what the
		// machine presents then, as for a command that connects by itself.
		names := map[string]string{}
		for _, m := range machines {
			names[m.ID] = m.Name
		}
		c.ForgetRenamed(names)
	}
	return machines, warnings, nil
}

// remember stores the machine list for the next command's warm.
func (a *app) remember(machines []machineRef) {
	var seen []seenMachine
	for _, m := range machines {
		seen = append(seen, seenMachine{ID: m.ID, Name: m.Name, Online: m.Online})
	}
	if slices.Equal(seen, a.st.Machines) {
		return
	}
	a.mu.Lock() // read by the link's dials (linkDial) while a command warms
	a.st.Machines = seen
	a.mu.Unlock()
	if a.mem == nil {
		a.save()
	} else if p := a.mem.Peer; p != nil {
		p.mu.Lock()
		p.machines = seen
		p.mu.Unlock()
	}
}

// kept is the relay connection the caller keeps between commands, if any.
func (a *app) kept() *Peer {
	if a.mem == nil {
		return nil
	}
	return a.mem.Peer
}

// dial opens the relay connection with the stored credentials; pipelined,
// a refusal surfaces from the first call.
func (a *app) dial(ctx context.Context, pipeline bool) (*peer.Client, error) {
	return a.dialer(pipeline).Connect(ctx)
}

// dialer connects with the stored credentials.
func (a *app) dialer(pipeline bool) *peer.Dialer {
	return &peer.Dialer{RelayURL: a.st.RelayURL, Role: relayv1.Role_ROLE_CLIENT, Key: a.key, Credentials: a.credentials, Trust: a.trust, HTTPClient: a.hc,
		Pipeline: pipeline}
}

// syncGrant adopts a renewed or re-issued grant if the owner published one,
// returning the version it replaced (0: unchanged). It fails with
// errRevoked once the owner revoked this agent.
func (a *app) syncGrant(ctx context.Context, c *peer.Client) (uint64, error) {
	hc, err := c.Control()
	if err != nil {
		return 0, err
	}
	res, err := relayv1connect.NewClientControlServiceClient(hc, "http://relay", transport.ClientNoCompression).GetLatestGrant(ctx, &relayv1.GetLatestGrantRequest{})
	if err != nil {
		return 0, err
	}
	if res.Revoked {
		return 0, errRevoked
	}
	a.inboxWaiting = res.InboxWaiting
	if res.Grant != nil && !proto.Equal(res.Grant, a.grant) {
		ng := &identityv1.Grant{}
		if identity.Unverified(res.Grant, ng) == nil && ng.GrantId == a.g.GrantId && ng.Version >= a.g.Version {
			old := a.g.Version
			if err := a.adopt(res.RootChain, res.Grant); err != nil {
				return 0, fmt.Errorf("%w: %v", errBadUpdate, err)
			}
			return old, nil
		}
	} else if res.RootChain != nil {
		if changed, _ := a.trust.AcceptRootChain(res.RootChain); changed {
			a.st.RootChain, _ = proto.Marshal(a.trust.RootChain())
			a.save()
		}
	}
	return 0, nil
}

// refused is the error of a client the owner no longer admits. When the
// owner erased everything (docs/erase-everything.md), the agent says it is
// erasing and forgets its pairing.
func (a *app) refused(err error) error {
	if order := machine.FetchEraseOrder(context.Background(), a.hc, a.st.RelayURL, a.st.OwnerID, a.trust); order != nil {
		if err := machine.AckErase(context.Background(), a.hc, a.st.RelayURL, order.EraseId, a.g.ClientId, nil, a.key); err != nil {
			fmt.Fprintln(os.Stderr, "warning: telling the owner this agent is erasing:", err)
		}
		if _, ferr := a.forget(); ferr != nil {
			return ferr
		}
		return &exitError{code: exitDenied, kind: "revoked", err: fmt.Errorf("%w: the owner erased everything; this computer's pairing is erased", err),
			hint: "run `hermesh auth login <address>` to pair with an owner again"}
	}
	return &exitError{code: exitDenied, kind: "revoked", err: err, hint: fmt.Sprintf("run `hermesh auth login %s` to ask the owner again", a.address())}
}

// address is the connection address this client is paired through.
func (a *app) address() string { return connaddr.Format(a.st.RelayURL, a.st.OwnerID) }

var (
	errRevoked   = errors.New("this client's access has been revoked by the owner")
	errBadUpdate = errors.New("ignoring updated grant")
)
