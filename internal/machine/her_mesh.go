package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	inboxv1 "github.com/hermeshai/hermesh/gen/go/hermesh/inbox/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/herhost"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Her on the mesh (docs/her-mesh.md): Her is one of the owner's agents.
// The machine she runs on keeps her client key, her inbox key and the
// grant the owner signed for her, and the news of her sessions until her
// has it in her conversation. What she does with them as an agent (her
// tools, her inbox) is the HerLink's, which lives above this package
// because it runs CLI commands.

const (
	// kHerGrant is Her's signed grant; kHerNews the news not yet in her
	// conversation ([]herNews as JSON).
	kHerGrant = "her_grant"
	kHerNews  = "her_news"
	// herNewsRetry is how often news her could not take is tried again.
	herNewsRetry = 5 * time.Second
)

func herKeyPath(dataDir string) string      { return filepath.Join(dataDir, "keys", "her.key") }
func herInboxKeyPath(dataDir string) string { return filepath.Join(dataDir, "keys", "her-inbox.key") }

// HerCredentials are what Her presents as an agent.
type HerCredentials struct {
	RelayURL, OwnerID string
	Key               *identity.SoftwareKey
	RootChain         *identityv1.RootChain
	Grant             *identityv1.Signed
	// InboxKey is her inbox private key (X25519).
	InboxKey []byte
}

// HerLinkConfig is what a HerLink is made with.
type HerLinkConfig struct {
	DataDir    string
	HTTPClient *http.Client
	Logger     *slog.Logger
	// Credentials are Her's now; nil while she has no grant.
	Credentials func() *HerCredentials
	// Adopted is told of a grant or root chain the relay had newer.
	Adopted func(*identityv1.RootChain, *identityv1.Signed)
	// Revoked is told that the owner revoked her grant.
	Revoked func()
	// News takes the news of one of her sessions; false: try again later.
	News func(*inboxv1.Message) bool
}

// HerLink is Her as an agent: the tools her is offered and what runs them,
// and her inbox.
type HerLink interface {
	herhost.Mesh
	// Render is news as her reads it: the assistant it is for, who it is
	// from, and the text. ok is false for a message that is not the news of
	// a session.
	Render(m *inboxv1.Message) (bot, from, text string, ok bool)
	// Run follows her inbox until ctx ends or her grant is revoked.
	Run(ctx context.Context)
	// Close drops her connections.
	Close()
}

// herNews is one piece of news waiting for her.
type herNews struct {
	ID       string `json:"id"`
	Bot      string `json:"bot"`
	FromID   string `json:"from_id"`
	FromName string `json:"from_name"`
	Text     string `json:"text"`
}

// herMesh is the machine's state of Her as an agent.
type herMesh struct {
	key   *identity.SoftwareKey
	inbox []byte
	link  HerLink
	// grant is her verified grant with its signed form; nil: none.
	grant  *identityv1.Grant
	signed *identityv1.Signed
	stop   context.CancelFunc // the link's Run, while it runs
	kick   chan struct{}      // news waits
}

// openHerMesh makes Her's keys and her link, on a machine that can run her.
func (n *Machine) openHerMesh() {
	n.mesh.kick = make(chan struct{}, 1)
	if n.herUnsupported() != "" {
		return
	}
	key, err := identity.LoadOrCreateKey(herKeyPath(n.cfg.DataDir))
	if err != nil {
		n.log.Warn("Her's client key", "err", err)
		return
	}
	inbox, err := herInboxKey(n.cfg.DataDir)
	if err != nil {
		n.log.Warn("Her's inbox key", "err", err)
		return
	}
	n.mesh.key, n.mesh.inbox = key, inbox
	if n.cfg.NewHerLink != nil {
		n.mesh.link = n.cfg.NewHerLink(HerLinkConfig{DataDir: n.cfg.DataDir, HTTPClient: n.cfg.HTTPClient, Logger: n.log,
			Credentials: n.herCredentials, Adopted: n.herAdopted, Revoked: func() { n.dropHerGrant("the owner revoked it") }, News: n.takeHerNews})
	}
}

func herInboxKey(dataDir string) ([]byte, error) {
	path := herInboxKeyPath(dataDir)
	b, err := os.ReadFile(path)
	if err == nil && len(b) == 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	priv, _, err := identity.NewInboxKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return priv, os.WriteFile(path, priv, 0o600)
}

// herClientKey is Her's public client key (SPKI DER), nil on a machine that
// can't run her.
func (n *Machine) herClientKey() []byte {
	if n.mesh.key == nil {
		return nil
	}
	return identity.MarshalPublicKey(n.mesh.key.Public())
}

// loadHerGrant takes up the grant kept from before, if it still holds.
func (n *Machine) loadHerGrant() {
	b, _ := n.st.Get(kHerGrant)
	if len(b) == 0 {
		return
	}
	signed := &identityv1.Signed{}
	err := proto.Unmarshal(b, signed)
	if err == nil {
		err = n.keepHerGrant(signed, false)
	}
	if err != nil {
		n.log.Info("Her's grant no longer holds", "err", err)
		n.st.Set(kHerGrant, []byte{})
	}
}

// keepHerGrant checks a grant for this machine's Her and keeps it: signed
// by the owner's root key, for her key here, naming this machine, and not
// revoked.
func (n *Machine) keepHerGrant(signed *identityv1.Signed, store bool) error {
	if n.mesh.key == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this machine can't run Her"))
	}
	n.mu.Lock()
	trust := n.trust
	n.mu.Unlock()
	if trust == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this machine has no owner"))
	}
	g, _, err := trust.VerifyGrant(signed, time.Now())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	switch {
	case g.HerMachineId != n.machineID:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the grant is not for Her on this machine"))
	case !bytes.Equal(g.ClientPublicKey, n.herClientKey()):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the grant is not for this machine's Her key"))
	}
	n.mu.Lock()
	problem := n.grantProblemLocked(g)
	if cur := n.mesh.grant; problem == nil && cur != nil && cur.GrantId == g.GrantId && g.Version < cur.Version {
		problem = errors.New("grant superseded by a newer version")
	}
	if problem == nil {
		n.mesh.grant, n.mesh.signed = g, signed
	}
	n.mu.Unlock()
	if problem != nil {
		return connect.NewError(connect.CodeInvalidArgument, problem)
	}
	if store {
		if err := n.st.SetProto(kHerGrant, signed); err != nil {
			return err
		}
	}
	return nil
}

// SetHerGrant gives this machine's Her her grant and puts her on the mesh.
func (n *Machine) SetHerGrant(signed *identityv1.Signed, chain *identityv1.RootChain) error {
	if chain != nil {
		n.acceptRootChain(chain)
	}
	if err := n.keepHerGrant(signed, true); err != nil {
		return err
	}
	n.log.Info("Her has her grant")
	n.startHerLink()
	n.kickStatus()
	return nil
}

// dropHerGrant takes Her off the mesh: her grant is gone here, and her is
// no longer offered the tools.
func (n *Machine) dropHerGrant(why string) {
	n.mu.Lock()
	had := n.mesh.grant != nil
	n.mesh.grant, n.mesh.signed = nil, nil
	stop := n.mesh.stop
	n.mesh.stop = nil
	n.mu.Unlock()
	if !had {
		return
	}
	n.log.Info("Her is off the mesh", "why", why)
	n.st.Set(kHerGrant, []byte{})
	n.st.Set(kHerNews, []byte{})
	if stop != nil {
		stop()
	}
	if n.mesh.link != nil {
		n.mesh.link.Close()
	}
	n.her.MeshChanged()
	n.kickStatus()
}

// checkHerGrant drops Her's grant once a revocation covers it.
func (n *Machine) checkHerGrant() {
	n.mu.Lock()
	g := n.mesh.grant
	var problem error
	if g != nil {
		problem = n.grantProblemLocked(g)
	}
	n.mu.Unlock()
	if problem != nil {
		n.dropHerGrant(problem.Error())
	}
}

// herAdopted keeps a newer version of her grant that the relay had.
func (n *Machine) herAdopted(chain *identityv1.RootChain, signed *identityv1.Signed) {
	if chain != nil {
		n.acceptRootChain(chain)
	}
	if signed != nil {
		if err := n.keepHerGrant(signed, true); err != nil {
			n.log.Debug("a newer grant for Her that doesn't hold", "err", err)
		}
	}
}

func (n *Machine) herCredentials() *HerCredentials {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.mesh.signed == nil || n.trust == nil {
		return nil
	}
	return &HerCredentials{RelayURL: n.relayURL, OwnerID: n.ownerID, Key: n.mesh.key, RootChain: n.trust.RootChain(), Grant: n.mesh.signed, InboxKey: n.mesh.inbox}
}

// herMeshNow is what serves her's mesh tools: her link, while Her is on
// and holds a grant.
func (n *Machine) herMeshNow() herhost.Mesh {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.mesh.link == nil || n.mesh.grant == nil || !n.herOn {
		return nil
	}
	return n.mesh.link
}

// startHerLink follows Her's inbox and offers her the tools, while Her is
// on and holds a grant.
func (n *Machine) startHerLink() {
	n.mu.Lock()
	run := n.mesh.link != nil && n.mesh.grant != nil && n.herOn && n.mesh.stop == nil
	var ctx context.Context
	if run {
		ctx, n.mesh.stop = context.WithCancel(context.Background())
	}
	n.mu.Unlock()
	if run {
		go n.mesh.link.Run(ctx)
		go n.deliverHerNews(ctx)
	}
	n.her.MeshChanged()
}

// stopHerLink stops following her inbox (Her turned off, or hermeshd
// stopping); her grant stays.
func (n *Machine) stopHerLink() {
	n.mu.Lock()
	stop := n.mesh.stop
	n.mesh.stop = nil
	n.mu.Unlock()
	if stop != nil {
		stop()
		n.mesh.link.Close()
	}
}

// herTakes hands m to this machine's own Her when it is addressed to her,
// without the post office.
func (n *Machine) herTakes(m *inboxv1.Message) bool {
	n.mu.Lock()
	mine := n.mesh.grant != nil && n.mesh.link != nil && m.GetTo().GetInbox() == n.mesh.grant.ClientId
	n.mu.Unlock()
	return mine && n.takeHerNews(m)
}

// takeHerNews queues the news of one of Her's sessions for her
// conversation. false: it could not be kept, and waits where it is.
func (n *Machine) takeHerNews(m *inboxv1.Message) bool {
	bot, from, text, ok := n.mesh.link.Render(m)
	if !ok {
		return true // nothing she reads
	}
	if bots := n.her.Bots(); len(bots) == 1 {
		bot = bots[0]
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	queue := n.herNewsLocked()
	for _, q := range queue {
		if q.ID == m.MessageId {
			return true
		}
	}
	queue = append(queue, herNews{ID: m.MessageId, Bot: bot, FromID: m.GetFrom().GetSession(), FromName: from, Text: text})
	if err := n.setHerNewsLocked(queue); err != nil {
		n.log.Warn("keeping news for Her", "err", err)
		return false
	}
	select {
	case n.mesh.kick <- struct{}{}:
	default:
	}
	return true
}

func (n *Machine) herNewsLocked() []herNews {
	var queue []herNews
	if b, _ := n.st.Get(kHerNews); len(b) > 0 {
		json.Unmarshal(b, &queue)
	}
	return queue
}

func (n *Machine) setHerNewsLocked(queue []herNews) error {
	if len(queue) == 0 {
		return n.st.Set(kHerNews, []byte{}) // the store keeps no NULL
	}
	b, err := json.Marshal(queue)
	if err != nil {
		return err
	}
	return n.st.Set(kHerNews, b)
}

// deliverHerNews hands the queue to her, oldest first, until ctx ends: an
// entry leaves once her has it in the conversation.
func (n *Machine) deliverHerNews(ctx context.Context) {
	for ctx.Err() == nil {
		n.mu.Lock()
		queue := n.herNewsLocked()
		n.mu.Unlock()
		if len(queue) == 0 {
			select {
			case <-ctx.Done():
			case <-n.mesh.kick:
			}
			continue
		}
		q := queue[0]
		err := n.her.News(ctx, q.Bot, q.ID, q.FromID, q.FromName, q.Text)
		if err != nil && !errors.Is(err, herhost.ErrRefused) {
			if ctx.Err() == nil {
				n.log.Info("her did not take news yet", "news", q.ID, "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(herNewsRetry):
			}
			continue
		}
		if err != nil {
			n.log.Warn("her would not take news", "news", q.ID, "err", err)
		} else {
			n.log.Info("news in Her's conversation", "news", q.ID, "from", q.FromID)
		}
		n.mu.Lock()
		queue = n.herNewsLocked()
		for i, x := range queue {
			if x.ID == q.ID {
				queue = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		err = n.setHerNewsLocked(queue)
		n.mu.Unlock()
		if err != nil {
			// Not removed: it would be delivered again without end.
			n.log.Warn("news for Her could not be taken off the queue", "err", err)
			return
		}
	}
}

// setHerGrant is MachineAdminService.SetHerGrant.
func (s *adminService) SetHerGrant(ctx context.Context, req *machinev1.SetHerGrantRequest) (*machinev1.SetHerGrantResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	if req.Grant == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing grant"))
	}
	if err := s.n.SetHerGrant(req.Grant, req.RootChain); err != nil {
		return nil, err
	}
	return &machinev1.SetHerGrantResponse{}, nil
}
