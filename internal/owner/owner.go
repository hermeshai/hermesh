// Package owner implements the owner role of the hermeshd bundled with
// Hermesh.app, on each of the owner's trusted devices: onboarding, devices
// joining and leaving, pairing approval, grants, revocation, machine
// administration and cross-machine session views. Anything that adds
// permissions is signed by this device's root key, held by the app.
package owner

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/flock"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type Config struct {
	DataDir    string
	Logger     *slog.Logger
	HTTPClient *http.Client
	// MachineConfig is used when this Mac is enabled as an execution machine.
	MachineConfig machine.Config
	// NoLocalMachine: this device can never be an execution machine (an
	// iPhone), so requests to make it one are refused.
	NoLocalMachine bool
}

// errNoLocalMachine refuses making a device that cannot run agents a machine.
var errNoLocalMachine = connect.NewError(connect.CodeFailedPrecondition, errors.New("this device cannot be an execution machine"))

type Owner struct {
	cfg Config
	log *slog.Logger
	st  *store
	hub *signHub
	// rep is this device's copy of the owner's world, which the List
	// calls answer from (replica.go).
	rep *replica

	mu         sync.Mutex
	ownerID    string
	relayURL   string
	rootPub    *ecdsa.PublicKey
	trust      *identity.Trust
	device     *identity.SoftwareKey
	deviceCert *identityv1.Signed
	client     *peer.Client
	relayInfo  *relayv1.GetInfoResponse
	join       *pendingJoin // this device asking to join an owner
	pendingMu  sync.Mutex   // pending names (pendingNames)
	cardsMu    sync.Mutex   // one card sync at a time (cards.go)
	removed    bool         // the relay no longer counts this device as the owner's
	signedOut  bool         // SignOut: set up, but it neither connects nor signs
	account    string       // the account, as the center named it (display)

	localMachine *machine.Machine
	// lastMachines: the machines as the relay last listed them, shown
	// offline while the relay cannot be reached.
	lastMachines []*localv1.Machine
	localCancel  context.CancelFunc
	localDone    chan struct{} // closed once the local machine stopped and closed
	runCtx       context.Context
}

const (
	kOwnerID      = "owner_id"
	kRelayURL     = "relay_url"
	kRootPub      = "root_public_key"
	kRootChain    = "root_chain"
	kDeviceCert   = "device_cert"
	kLocalMachine = "local_machine"
	kSignedOut    = "signed_out"
	kAccount      = "account"
	// kMachineDir: where this profile's machine is, when not in its own
	// directory (a device's machine from before profiles, profiles.go).
	kMachineDir = "machine_dir"
)

// openProfile opens one profile (profiles.go): cfg.DataDir is its
// directory, and hub the device's signer, shared by its profiles.
func openProfile(cfg Config, hub *signHub) (*Owner, error) {
	if _, err := flock.Acquire(cfg.DataDir); err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(cfg.DataDir, "owner.db"))
	if err != nil {
		return nil, err
	}
	o := &Owner{cfg: cfg, log: cfg.Logger, st: st, hub: hub}
	o.rep = newReplica(o)
	return o, o.load()
}

func (o *Owner) load() error {
	account, _ := o.st.get(kAccount)
	o.account = string(account)
	id, _ := o.st.get(kOwnerID)
	if len(id) == 0 {
		return nil
	}
	signedOut, _ := o.st.get(kSignedOut)
	o.signedOut = string(signedOut) == "1"
	relay, _ := o.st.get(kRelayURL)
	rootPub, _ := o.st.get(kRootPub)
	pub, err := identity.ParsePublicKey(rootPub)
	if err != nil {
		return err
	}
	trust := identity.NewTrust(string(id))
	rc := &identityv1.RootChain{}
	if b, err := o.st.get(kRootChain); err != nil || b == nil || proto.Unmarshal(b, rc) != nil {
		return fmt.Errorf("owner root chain missing: %v", err)
	}
	if _, err := trust.AcceptRootChain(rc); err != nil {
		return err
	}
	dev, err := identity.LoadKey(filepath.Join(o.cfg.DataDir, "keys", "device.key"))
	if err != nil {
		return err
	}
	dc, err := o.st.getSigned(kDeviceCert)
	if err != nil {
		return err
	}
	o.ownerID, o.relayURL, o.rootPub, o.trust, o.device, o.deviceCert = string(id), string(relay), pub, trust, dev, dc
	return nil
}

func (o *Owner) Initialized() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ownerID != ""
}

// Run keeps background work going: the local execution machine (if
// enabled) and the owner relay connection. A profile that is signed out
// does neither.
func (o *Owner) Run(ctx context.Context) {
	o.mu.Lock()
	o.runCtx = ctx
	signedOut := o.signedOut
	o.mu.Unlock()
	if signedOut {
		<-ctx.Done()
		return
	}
	if v, _ := o.st.get(kLocalMachine); (string(v) == "1" && o.Initialized()) || o.machineOnly() {
		if err := o.startLocalMachine(); err != nil {
			o.log.Warn("local machine did not start", "err", err)
		}
	}
	go o.namePending(ctx)
	o.stayConnected(ctx)
	o.mu.Lock()
	if o.client != nil {
		o.client.Close()
		o.client = nil
	}
	o.mu.Unlock()
	o.stopLocalMachine()
}

// stayConnected keeps the relay connection up once set up, so the relay
// sees the owner online and pairing requests reach it without waiting for
// the next call.
func (o *Owner) stayConnected(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		var done <-chan struct{}
		if o.Initialized() {
			if c, err := o.relay(ctx); err == nil {
				done, backoff = c.Done(), time.Second
			} else {
				o.log.Debug("relay unreachable", "err", err, "retry_in", backoff)
			}
		}
		if done != nil {
			select {
			case <-ctx.Done():
			case <-done:
			}
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if o.Initialized() {
			backoff = min(backoff*2, 30*time.Second)
		}
	}
}

func (o *Owner) requireInit() error {
	if !o.Initialized() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("Hermesh is not set up yet"))
	}
	if o.isSignedOut() {
		return errSignedOut
	}
	return nil
}

var errSignedOut = connect.NewError(connect.CodeFailedPrecondition, errors.New("this account is signed out on this device; sign in again"))

func (o *Owner) isSignedOut() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.signedOut
}

func (o *Owner) credentials() *identityv1.Credentials {
	o.mu.Lock()
	defer o.mu.Unlock()
	return &identityv1.Credentials{RootChain: o.trust.RootChain(), Subject: &identityv1.Credentials_DeviceCert{DeviceCert: o.deviceCert}}
}

// relay returns the owner's relay connection, (re)connecting as needed.
func (o *Owner) relay(ctx context.Context) (*peer.Client, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client != nil {
		select {
		case <-o.client.Done():
			o.client = nil
		default:
			return o.client, nil
		}
	}
	d := &peer.Dialer{RelayURL: o.relayURL, Role: relayv1.Role_ROLE_OWNER, Key: o.device, Trust: o.trust, HTTPClient: o.cfg.HTTPClient,
		Credentials: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: o.trust.RootChain(), Subject: &identityv1.Credentials_DeviceCert{DeviceCert: o.deviceCert}}
		}}
	c, err := d.Connect(ctx)
	if err != nil {
		if errors.Is(err, transport.ErrAdmission) {
			go o.checkRemoved()
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay unreachable: %w", err))
	}
	o.client, o.removed = c, false
	go o.fetchRelayInfo(o.relayURL)
	go o.refreshCards()
	return c, nil
}

// fetchRelayInfo learns whether the relay is a hosted center (for Status).
func (o *Owner) fetchRelayInfo(relayURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{})
	if err != nil {
		return
	}
	o.mu.Lock()
	o.relayInfo = info
	o.mu.Unlock()
}

func (o *Owner) control(ctx context.Context) (relayv1connect.OwnerControlServiceClient, *peer.Client, error) {
	c, err := o.relay(ctx)
	if err != nil {
		return nil, nil, err
	}
	hc, err := c.Control()
	if err != nil {
		return nil, nil, err
	}
	return relayv1connect.NewOwnerControlServiceClient(hc, "http://relay", transport.ClientNoCompression), c, nil
}

// rootKey parses the root public key given with Setup or RequestJoin, or,
// when none was given, has the signer create one.
func (o *Owner) rootKey(ctx context.Context, der []byte) (*ecdsa.PublicKey, error) {
	if len(der) == 0 {
		pub, _, err := o.hub.createKey(ctx)
		return pub, err
	}
	pub, err := identity.ParsePublicKey(der)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("root public key: %w", err))
	}
	return pub, nil
}

func (o *Owner) signRoot(ctx context.Context, msgs ...proto.Message) ([]*identityv1.Signed, error) {
	o.mu.Lock()
	pub := o.rootPub
	o.mu.Unlock()
	return o.hub.signBatch(ctx, pub, msgs)
}

// ---------------------------------------------------------------------------
// Onboarding.

func (o *Owner) Status() *localv1.GetStatusResponse {
	st := o.ownerStatus()
	if !st.Initialized && o.machineOnly() {
		o.machineOnlyStatus(st)
	}
	return st
}

func (o *Owner) ownerStatus() *localv1.GetStatusResponse {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := &localv1.GetStatusResponse{Initialized: o.ownerID != "", OwnerId: o.ownerID, RelayUrl: o.relayURL, Version: machine.Version}
	if o.ownerID != "" {
		st.ConnectAddress = connaddr.Format(o.relayURL, o.ownerID)
	}
	if o.client != nil {
		select {
		case <-o.client.Done():
		default:
			st.RelayConnected = true
		}
	}
	if o.localMachine != nil {
		st.LocalMachineEnabled, st.LocalMachineId, st.LocalMachineName = true, o.localMachine.MachineID(), o.localMachine.Name()
	}
	if o.relayInfo != nil {
		st.Hosted, st.ConsoleUrl = o.relayInfo.Hosted, o.relayInfo.ConsoleUrl
	}
	if o.rootPub != nil && o.ownerID != "" {
		st.DeviceKeyId = identity.KeyID(o.rootPub)
	}
	st.Removed, st.SignedOut, st.Account = o.removed, o.signedOut, o.account
	return st
}

// checkRelay makes sure this network reaches the relay, by HTTPS and by
// WebSocket, before anything asks for Touch ID: a company network or proxy
// that blocks it fails here, with nothing created or signed.
func (o *Owner) checkRelay(ctx context.Context, relayURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{})
	if err == nil {
		err = transport.Probe(ctx, relayURL, o.cfg.HTTPClient)
	}
	if err == nil {
		return nil
	}
	host := strings.TrimPrefix(strings.TrimPrefix(relayURL, "https://"), "http://")
	var ce *connect.Error
	if errors.As(err, &ce) {
		err = errors.New(ce.Message()) // "403 Forbidden", from whatever answered instead of the relay
	}
	// The apps recognise this form and say it in the person's language.
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay unreachable (%s): %w", host, err))
}

func (o *Owner) publicClient(relayURL string) relayv1connect.RelayPublicServiceClient {
	return relayv1connect.NewRelayPublicServiceClient(o.cfg.HTTPClient, strings.TrimSuffix(relayURL, "/"))
}

// Setup makes this the owner's first trusted device: root cert v1 lists
// its root key and names the owner, and the relay is claimed (or, on a
// hosted center, the account linked) with it.
func (o *Owner) Setup(ctx context.Context, req *localv1.SetupRequest) (*localv1.SetupResponse, error) {
	if o.Initialized() {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("already set up"))
	}
	if o.cfg.NoLocalMachine && req.LocalMachineName != "" {
		return nil, errNoLocalMachine
	}
	relayURL := normalizeRelayURL(req.RelayUrl)
	if err := o.checkRelay(ctx, relayURL); err != nil {
		return nil, err
	}
	rootPub, err := o.rootKey(ctx, req.RootPublicKey)
	if err != nil {
		return nil, err
	}
	req.RootPublicKey = identity.MarshalPublicKey(rootPub)
	ownerID := identity.OwnerID(req.RootPublicKey)
	device, err := identity.GenerateKey()
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.rootPub = rootPub
	o.mu.Unlock()
	msgs := []proto.Message{
		&identityv1.RootCert{OwnerId: ownerID, Version: 1, IssuedAt: timestamppb.Now(),
			RootKeys: []*identityv1.RootKey{{PublicKey: req.RootPublicKey, Label: req.RootLabel, Platform: req.RootPlatform}}},
		deviceCert(ownerID, device, req.RootLabel)}
	if req.LocalMachineName != "" {
		nc, err := o.localMachineCert(ownerID, rootPub)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, nc)
	}
	signed, err := o.signRoot(ctx, msgs...) // one Touch ID for all of it
	if err != nil {
		return nil, err
	}
	chain := &identityv1.RootChain{Certs: signed[:1]}
	if _, err := o.publicClient(relayURL).Claim(ctx, &relayv1.ClaimRequest{ClaimCode: req.ClaimCode, ClaimVerifier: req.ClaimVerifier, RootChain: chain, DeviceCert: signed[1]}); err != nil {
		return nil, fmt.Errorf("claim relay: %w", err)
	}
	if err := o.saveIdentity(ownerID, relayURL, req.RootPublicKey, chain, device, signed[1], req.Account); err != nil {
		return nil, err
	}
	if len(signed) > 2 {
		if err := o.installLocalMachine(ctx, signed[2], req.LocalMachineName); err != nil {
			return nil, fmt.Errorf("this Mac as a machine: %w", err)
		}
	}
	return &localv1.SetupResponse{OwnerId: ownerID, ConnectAddress: connaddr.Format(relayURL, ownerID)}, nil
}

// deviceCert certifies this device's device key, for its root key to sign.
func deviceCert(ownerID string, device *identity.SoftwareKey, label string) *identityv1.OwnerDeviceCert {
	return &identityv1.OwnerDeviceCert{OwnerId: ownerID, DevicePublicKey: identity.MarshalPublicKey(device.Public()), Label: label, IssuedAt: timestamppb.Now()}
}

func (o *Owner) saveIdentity(ownerID, relayURL string, rootPub []byte, chain *identityv1.RootChain, device *identity.SoftwareKey, deviceCert *identityv1.Signed, account string) error {
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(chain); err != nil {
		return err
	}
	if _, _, err := trust.VerifyDeviceCert(deviceCert); err != nil {
		return err
	}
	if err := identity.SaveKey(filepath.Join(o.cfg.DataDir, "keys", "device.key"), device); err != nil {
		return err
	}
	b, _ := proto.Marshal(chain)
	for k, v := range map[string][]byte{kOwnerID: []byte(ownerID), kRelayURL: []byte(relayURL), kRootPub: rootPub, kRootChain: b, kAccount: []byte(account), kSignedOut: {}} {
		if err := o.st.set(k, v); err != nil {
			return err
		}
	}
	o.st.setSigned(kDeviceCert, deviceCert)
	pub, _ := identity.ParsePublicKey(rootPub)
	o.mu.Lock()
	if o.client != nil {
		o.client.Close()
		o.client = nil
	}
	o.ownerID, o.relayURL, o.rootPub, o.trust, o.device, o.deviceCert = ownerID, relayURL, pub, trust, device, deviceCert
	o.account, o.signedOut = account, false
	o.mu.Unlock()
	return nil
}

func normalizeRelayURL(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "/")
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return s
}

// ---------------------------------------------------------------------------
// Local execution machine.

func (o *Owner) SetLocalMachine(ctx context.Context, enabled bool, name string) (string, error) {
	if err := o.requireInit(); err != nil {
		return "", err
	}
	if enabled && o.cfg.NoLocalMachine {
		return "", errNoLocalMachine
	}
	if !enabled {
		o.mu.Lock()
		if o.localCancel != nil {
			o.localCancel()
			o.localCancel, o.localMachine = nil, nil
		}
		o.mu.Unlock()
		return "", o.st.set(kLocalMachine, []byte("0"))
	}
	o.mu.Lock()
	running := o.localMachine
	o.mu.Unlock()
	if running != nil {
		return running.MachineID(), o.st.set(kLocalMachine, []byte("1"))
	}
	n, err := o.openLocalMachine()
	if err != nil {
		return "", err
	}
	paired := n.Paired() && n.OwnerID() == o.ownerID
	n.Close()
	if paired {
		if err := o.st.set(kLocalMachine, []byte("1")); err != nil {
			return "", err
		}
		if err := o.startLocalMachine(); err != nil {
			return "", err
		}
		return o.Status().LocalMachineId, nil
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	nc, err := o.localMachineCert(o.ownerID, o.rootPub)
	if err != nil {
		return "", err
	}
	signed, err := o.signRoot(ctx, nc)
	if err != nil {
		return "", err
	}
	if err := o.installLocalMachine(ctx, signed[0], name); err != nil {
		return "", err
	}
	return o.Status().LocalMachineId, nil
}

// localMachineCert certifies this device's own machine, for this device's
// root key to sign: removing the device removes the machine too.
func (o *Owner) localMachineCert(ownerID string, rootPub *ecdsa.PublicKey) (*identityv1.MachineCert, error) {
	n, err := o.openLocalMachine()
	if err != nil {
		return nil, err
	}
	defer n.Close()
	return &identityv1.MachineCert{OwnerId: ownerID, MachineId: n.MachineID(), MachinePublicKey: n.PublicKeySPKI(),
		IssuedAt: timestamppb.Now(), DeviceKeyId: identity.KeyID(rootPub)}, nil
}

// installLocalMachine publishes this device's signed machine cert, installs
// it, names the machine and starts it.
func (o *Owner) installLocalMachine(ctx context.Context, cert *identityv1.Signed, name string) error {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return err
	}
	if _, err := ctl.PublishMachineCert(ctx, &relayv1.PublishMachineCertRequest{MachineCert: cert}); err != nil {
		return err
	}
	n, err := o.openLocalMachine()
	if err != nil {
		return err
	}
	err = n.Install(o.ownerID, o.relayURL, o.trust.RootChain(), cert)
	if err == nil && name != "" {
		err = n.SetName(name)
	}
	n.Close()
	if err != nil {
		return err
	}
	if err := o.st.set(kLocalMachine, []byte("1")); err != nil {
		return err
	}
	return o.startLocalMachine()
}

func (o *Owner) openLocalMachine() (*machine.Machine, error) {
	return machine.Open(o.localMachineConfig())
}

func (o *Owner) localMachineConfig() machine.Config {
	cfg := o.cfg.MachineConfig
	cfg.DataDir = filepath.Join(o.cfg.DataDir, "machine")
	if dir, _ := o.st.get(kMachineDir); len(dir) > 0 {
		cfg.DataDir = string(dir)
	}
	if cfg.Logger == nil {
		cfg.Logger = o.log
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = o.cfg.HTTPClient
	}
	return cfg
}

func (o *Owner) startLocalMachine() error {
	o.mu.Lock()
	if o.localMachine != nil {
		o.mu.Unlock()
		return nil
	}
	parent := o.runCtx
	o.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	n, err := o.openLocalMachine()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	o.mu.Lock()
	o.localMachine, o.localCancel, o.localDone = n, cancel, done
	o.mu.Unlock()
	go func() {
		if err := n.Run(ctx); err != nil {
			o.log.Warn("local machine stopped", "err", err)
		}
		n.Close()
		close(done)
	}()
	return nil
}

// stopLocalMachine stops the local machine and waits until it is closed.
func (o *Owner) stopLocalMachine() {
	o.mu.Lock()
	cancel, done := o.localCancel, o.localDone
	o.localCancel, o.localMachine, o.localDone = nil, nil, nil
	o.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		o.log.Warn("local machine did not stop in time")
	}
}

// ---------------------------------------------------------------------------
// Pairing.

// SanitizeMachineName turns s into a machine name (see machine.SanitizeName).
func SanitizeMachineName(s string) string { return machine.SanitizeName(s) }

// kPendingNames: machine id → a name chosen at approval, to set on the
// machine once it connects (JSON).
const kPendingNames = "pending_names"

func (o *Owner) pendingNames() map[string]string {
	m := map[string]string{}
	if b, _ := o.st.get(kPendingNames); len(b) > 0 {
		json.Unmarshal(b, &m)
	}
	return m
}

func (o *Owner) setPendingName(machineID, name string) {
	o.pendingMu.Lock()
	defer o.pendingMu.Unlock()
	m := o.pendingNames()
	if name == "" {
		delete(m, machineID)
	} else {
		m[machineID] = name
	}
	b, _ := json.Marshal(m)
	o.st.set(kPendingNames, b)
}

// namePending names the machines approved under another name than the one
// they asked with, once they are online (run in the background).
func (o *Owner) namePending(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		pending := o.pendingNames()
		if len(pending) == 0 || !o.Initialized() {
			continue
		}
		machines, err := o.ListMachines(ctx)
		if err != nil {
			continue
		}
		for _, m := range machines {
			name, ok := pending[m.MachineId]
			if !ok || !m.Online {
				continue
			}
			if _, err := o.admin(ctx, m.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: name}}
			}); err == nil {
				o.setPendingName(m.MachineId, "")
			}
		}
	}
}

func (o *Owner) lookupPairing(ctx context.Context, code string) (*identityv1.PairingRequest, *localv1.PairingDetails, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, nil, err
	}
	norm := identity.NormalizePairingCode(code)
	// Claims a request made without an owner; the owner typed its code here.
	res, err := ctl.ClaimPairingRequest(ctx, &relayv1.ClaimPairingRequestRequest{Code: norm})
	if err != nil {
		return nil, nil, err
	}
	return o.pairingDetails(norm, res.Request, res.ExpiresAt)
}

// clientConfirmed: a client request that reached this owner because the
// person said, signed in to their account, that it is theirs: a hosted
// center's account claimed it (through a pairing link, or a code typed in
// the app), or the center's MCP gateway made it after its consent page. Its
// requester shows no verification code; there is nothing to compare.
func clientConfirmed(pr *identityv1.PairingRequest) bool {
	return pr.Kind == identityv1.PairingKind_PAIRING_KIND_CLIENT && (pr.OwnerId == "" || pr.Custodian != "")
}

// pairingDetails checks a pairing request the relay returned for code and
// describes it for the owner.
func (o *Owner) pairingDetails(norm string, request *identityv1.Signed, expiresAt *timestamppb.Timestamp) (*identityv1.PairingRequest, *localv1.PairingDetails, error) {
	pr := &identityv1.PairingRequest{}
	if _, err := identity.SelfSigned(request, pr, func(m *identityv1.PairingRequest) []byte { return m.SubjectPublicKey }); err != nil {
		return nil, nil, connect.NewError(connect.CodeDataLoss, fmt.Errorf("pairing request signature: %w", err))
	}
	// The code is derived from the requester's key. If the relay swapped
	// the key, it no longer matches what the user typed.
	if identity.PairingCode(pr.Kind, pr.SubjectPublicKey) != norm {
		return nil, nil, connect.NewError(connect.CodeDataLoss, errors.New("the request returned for this code does not match it; the relay may be tampering"))
	}
	// An empty owner: a hosted center's account claimed the request, and
	// only this owner's tenant can see it. The requester checks the binding
	// with the verification code the app shows.
	if pr.OwnerId != o.ownerID && pr.OwnerId != "" {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pairing request is for a different owner"))
	}
	d := &localv1.PairingDetails{Kind: pr.Kind, Label: pr.Label, Hostname: pr.Hostname, Platform: pr.Platform,
		Fingerprint: identity.ShortFingerprint(pr.SubjectPublicKey), ExpiresAt: expiresAt, Code: norm,
		VerificationCode: identity.VerificationCode(pr.Kind, pr.SubjectPublicKey, o.ownerID), Custodian: pr.Custodian,
		Confirmed: clientConfirmed(pr)}
	switch pr.Kind {
	case identityv1.PairingKind_PAIRING_KIND_MACHINE:
		d.SubjectId = identity.MachineID(pr.SubjectPublicKey)
	case identityv1.PairingKind_PAIRING_KIND_CLIENT:
		d.SubjectId = identity.ClientID(pr.SubjectPublicKey)
	case identityv1.PairingKind_PAIRING_KIND_DEVICE:
		d.SubjectId = identity.Fingerprint(pr.SubjectPublicKey)
	}
	return pr, d, nil
}

func (o *Owner) LookupPairing(ctx context.Context, code string) (*localv1.PairingDetails, error) {
	_, d, err := o.lookupPairing(ctx, code)
	return d, err
}

// ListPairingRequests returns the requests waiting for this owner, as the
// replica last polled them, without those expired since.
func (o *Owner) ListPairingRequests(ctx context.Context) ([]*localv1.PairingDetails, error) {
	s, err := o.rep.use(ctx)
	if err != nil {
		return nil, err
	}
	o.rep.mu.Lock()
	list, err := listed(&s.pairings)
	o.rep.mu.Unlock()
	return slices.DeleteFunc(list, func(d *localv1.PairingDetails) bool {
		return d.ExpiresAt != nil && d.ExpiresAt.AsTime().Before(time.Now())
	}), err
}

// listPairingRequests asks the relay for the requests waiting for this
// owner. One the relay tampered with is left out (and logged), not shown.
func (o *Owner) listPairingRequests(ctx context.Context) ([]*localv1.PairingDetails, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListPairingRequests(ctx, &relayv1.ListPairingRequestsRequest{})
	if err != nil {
		return nil, err
	}
	return o.pairingsFrom(res), nil
}

// pairingsFrom is the pairing requests the relay listed (ListPairingRequests,
// or WatchOwner). One the relay tampered with is left out (and logged).
func (o *Owner) pairingsFrom(res *relayv1.ListPairingRequestsResponse) []*localv1.PairingDetails {
	var out []*localv1.PairingDetails
	for _, p := range res.Pairings {
		_, d, err := o.pairingDetails(identity.NormalizePairingCode(p.Code), p.Request, p.ExpiresAt)
		if err != nil {
			o.log.Warn("pending pairing request left out", "code", p.Code, "err", err)
			continue
		}
		out = append(out, d)
	}
	return out
}

// ApproveMachinePairing certifies a machine that asked to join. It returns
// the machine's id and, when the machine asked for them (a Mac joining as a
// machine only, whose owner id came from the center), the two digits the
// signer showed for the person to type there.
func (o *Owner) ApproveMachinePairing(ctx context.Context, code, name string) (string, string, error) {
	pr, _, err := o.lookupPairing(ctx, code)
	if err != nil {
		return "", "", err
	}
	if pr.Kind != identityv1.PairingKind_PAIRING_KIND_MACHINE {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("this code belongs to an agent; use Add agent"))
	}
	// The machine names itself with the label it asked with; a different
	// name chosen here is set on it once it connects (a setting).
	if name = SanitizeMachineName(name); name == "machine" && pr.Label != "" {
		name = SanitizeMachineName(pr.Label)
	}
	machines, err := o.ListMachines(ctx)
	if err != nil {
		return "", "", err
	}
	machineID := identity.MachineID(pr.SubjectPublicKey)
	taken := map[string]bool{}
	for _, n := range machines {
		if n.MachineId != machineID {
			taken[n.MachineName] = true
		}
	}
	if taken[name] {
		// The name it asked with is taken (two Macs both called "MacBook
		// Pro"): the next free one. A name chosen here must be free.
		if name != SanitizeMachineName(pr.Label) {
			return "", "", connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a machine named %q already exists", name))
		}
		name = freeName(name, taken)
	}
	if name != SanitizeMachineName(pr.Label) {
		o.setPendingName(machineID, name)
	}
	if pr.ConfirmMatch {
		ctx = withShowMatch(ctx)
	}
	signed, err := o.signRoot(ctx, &identityv1.MachineCert{OwnerId: o.ownerID, MachineId: machineID, MachinePublicKey: pr.SubjectPublicKey, IssuedAt: timestamppb.Now()})
	if err != nil {
		return "", "", err
	}
	ctl, _, err := o.control(ctx)
	if err != nil {
		return "", "", err
	}
	if _, err := ctl.CompletePairing(ctx, &relayv1.CompletePairingRequest{Code: identity.NormalizePairingCode(code), Credential: signed[0]}); err != nil {
		return "", "", err
	}
	match := ""
	if pr.ConfirmMatch {
		match = identity.JoinMatchCode(signed[0].Body)
	}
	return machineID, match, nil
}

// freeName is name with the first suffix ("-2", "-3", …) not taken, within
// a machine name's 63 bytes.
func freeName(name string, taken map[string]bool) string {
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		base := name
		if len(base)+len(suffix) > 63 {
			base = strings.TrimRight(base[:63-len(suffix)], "-")
		}
		if !taken[base+suffix] {
			return base + suffix
		}
	}
}

func defaultSpec(spec *localv1.GrantSpec) *localv1.GrantSpec {
	s := proto.Clone(spec).(*localv1.GrantSpec)
	if len(s.MachineIds) == 0 {
		s.MachineIds = []string{"*"}
	}
	if len(s.ProjectIds) == 0 {
		s.ProjectIds = []string{"*"}
	}
	if len(s.Agents) == 0 {
		s.Agents = []string{"claude", "codex"}
	}
	if s.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_UNSPECIFIED {
		s.TaskVisibility = identityv1.TaskVisibility_TASK_VISIBILITY_OWN
	}
	if s.TtlDays == 0 {
		s.TtlDays = 30
	}
	if s.TtlDays > 90 {
		s.TtlDays = 90
	}
	return s
}

func grantFromSpec(base *identityv1.Grant, spec *localv1.GrantSpec) *identityv1.Grant {
	s := defaultSpec(spec)
	g := proto.Clone(base).(*identityv1.Grant)
	g.ClientLabel = s.Label
	g.MachineIds, g.ProjectIds, g.Agents = s.MachineIds, s.ProjectIds, s.Agents
	g.TaskVisibility, g.MaxConcurrentSessions = s.TaskVisibility, s.MaxConcurrentSessions
	g.NotBefore = timestamppb.New(time.Now().Add(-time.Minute))
	g.ExpiresAt = timestamppb.New(time.Now().Add(time.Duration(s.TtlDays) * 24 * time.Hour))
	return g
}

func (o *Owner) ApproveClientPairing(ctx context.Context, code string, spec *localv1.GrantSpec) (string, string, error) {
	pr, _, err := o.lookupPairing(ctx, code)
	if err != nil {
		return "", "", err
	}
	if pr.Kind != identityv1.PairingKind_PAIRING_KIND_CLIENT {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("this code belongs to a machine; use Add machine"))
	}
	if spec == nil {
		spec = &localv1.GrantSpec{}
	}
	if spec.Label == "" {
		spec.Label = pr.Label
	}
	// An MCP connection works for its agent product the way the owner's app
	// does: it sees every session in its projects (and steers its own).
	if pr.Custodian != "" && spec.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_UNSPECIFIED {
		spec.TaskVisibility = identityv1.TaskVisibility_TASK_VISIBILITY_ALL
	}
	base := &identityv1.Grant{OwnerId: o.ownerID, GrantId: "g_" + strings.ToLower(ulid.Make().String()), Version: 1,
		ClientId: identity.ClientID(pr.SubjectPublicKey), ClientPublicKey: pr.SubjectPublicKey, Custodian: pr.Custodian}
	g := grantFromSpec(base, spec)
	if clientConfirmed(pr) {
		ctx = withConfirmed(ctx)
	}
	signed, err := o.signRoot(ctx, g)
	if err != nil {
		return "", "", err
	}
	ctl, _, err := o.control(ctx)
	if err != nil {
		return "", "", err
	}
	if _, err := ctl.CompletePairing(ctx, &relayv1.CompletePairingRequest{Code: identity.NormalizePairingCode(code), Credential: signed[0]}); err != nil {
		return "", "", err
	}
	o.st.putGrant(signed[0], false)
	go o.refreshCards()
	return g.ClientId, g.GrantId, nil
}

// ---------------------------------------------------------------------------
// Machines.

// ListMachines lists the owner's machines as the relay knows them (see
// machines for the list with this device's own).
func (o *Owner) ListMachines(ctx context.Context) ([]*localv1.Machine, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListMachines(ctx, &relayv1.ListMachinesRequest{})
	if err != nil {
		return nil, err
	}
	return o.machinesFrom(res), nil
}

// machinesFrom is the machines the relay listed (ListMachines, or
// WatchOwner) that are trustworthy, as this device shows them.
func (o *Owner) machinesFrom(res *relayv1.ListMachinesResponse) []*localv1.Machine {
	o.adoptChain(res.RootChain) // another device may have joined or left
	local := o.Status().LocalMachineId
	var out []*localv1.Machine
	for _, e := range res.Machines {
		nc, _, err := o.trust.VerifyMachineCert(e.MachineCert)
		if err != nil {
			continue // not trustworthy; never shown
		}
		name := e.Name // as the machine reports it
		if pending, ok := o.pendingNames()[nc.MachineId]; ok {
			name = pending // set on it shortly (namePending)
		}
		if name == "" {
			name = nc.MachineId
		}
		out = append(out, &localv1.Machine{MachineId: nc.MachineId, MachineName: name, Online: e.Online, LastSeen: e.LastSeen, Version: e.Version,
			Local: nc.MachineId == local, DeviceKeyId: nc.DeviceKeyId})
	}
	slices.SortFunc(out, func(a, b *localv1.Machine) int { return strings.Compare(a.MachineName, b.MachineName) })
	return out
}

// local is this device's own machine, in process, when it runs one: what
// concerns this Mac never goes through the relay.
func (o *Owner) local() *machine.Local {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.localMachine == nil {
		return nil
	}
	return o.localMachine.Local()
}

// machines lists the owner's machines: this device's own from in process,
// with its state (Online: whether the relay can reach it), and the others
// as the replica last polled them from the relay (see listedMachines).
func (o *Owner) machines(ctx context.Context) ([]*localv1.Machine, error) {
	s, err := o.rep.use(ctx)
	if err != nil {
		return nil, err
	}
	o.rep.mu.Lock()
	listed, err := listed(&s.machines)
	o.rep.mu.Unlock()
	return o.withLocal(listed, err)
}

// listedMachines lists the owner's machines as the relay knows them. Without
// the relay, they are as it last listed them, offline, with its error.
func (o *Owner) listedMachines(ctx context.Context) ([]*localv1.Machine, error) {
	return o.keepListed(o.ListMachines(ctx))
}

// keepListed keeps what the relay listed, offline, for when it cannot be
// reached; then (err) it is what it last listed.
func (o *Owner) keepListed(listed []*localv1.Machine, err error) ([]*localv1.Machine, error) {
	o.mu.Lock()
	if err == nil {
		o.lastMachines = nil
		for _, m := range listed {
			off := proto.CloneOf(m)
			off.Online = false
			o.lastMachines = append(o.lastMachines, off)
		}
	} else {
		for _, m := range o.lastMachines {
			listed = append(listed, proto.CloneOf(m))
		}
	}
	o.mu.Unlock()
	return listed, err
}

// withLocal adds this device's own machine to the machines the relay
// listed; with it, the relay not answering is no error.
func (o *Owner) withLocal(listed []*localv1.Machine, err error) ([]*localv1.Machine, error) {
	l := o.local()
	if l == nil {
		return listed, err
	}
	info := l.Info()
	me := &localv1.Machine{MachineId: info.MachineId, MachineName: info.MachineName, Online: l.Connected(), Version: machine.Version, Local: true, Status: info.Status}
	out := []*localv1.Machine{me}
	for _, m := range listed {
		if m.MachineId == me.MachineId {
			me.LastSeen, me.DeviceKeyId = m.LastSeen, m.DeviceKeyId
		} else {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *localv1.Machine) int { return strings.Compare(a.MachineName, b.MachineName) })
	return out, nil
}

// machineStatus gives each other online machine its state (coding agents,
// sessions, pausing), which the relay does not know, as the replica follows
// it end to end. One not followed (yet) is left without one.
func (o *Owner) machineStatus(machines []*localv1.Machine) {
	o.rep.feeds(func(feeds map[string]*feed) {
		for _, n := range machines {
			if f := feeds[n.MachineId]; n.Online && !n.Local && f != nil && f.ready { // this device's own has its state already
				n.Status = f.status
			}
		}
	})
}

func (o *Owner) findMachine(ctx context.Context, idOrName string) (*localv1.Machine, error) {
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range machines {
		if n.MachineId == idOrName || n.MachineName == idOrName {
			return n, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no machine %q", idOrName))
}

// admin sends a machine an admin command. Settings (projects, pausing, name,
// agent defaults) grant nothing and go without a root signature, over this trusted
// device's own connection; anything else is signed (Touch ID).
func (o *Owner) admin(ctx context.Context, machineID string, build func(*identityv1.AdminCommand)) (string, error) {
	cmd := &identityv1.AdminCommand{OwnerId: o.ownerID, CommandId: identity.RandomCode(16), TargetMachineId: machineID,
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute))}
	build(cmd)
	req := &machinev1.ApplyAdminCommandRequest{}
	switch cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject, *identityv1.AdminCommand_RemoveProject, *identityv1.AdminCommand_SetPaused, *identityv1.AdminCommand_SetName,
		*identityv1.AdminCommand_SetAgentDefaults:
		if l := o.local(); l != nil && l.MachineID() == machineID {
			return l.ApplySettings(ctx, build) // this device's own machine, in process
		}
		req.Settings = cmd
	default:
		signed, err := o.signRoot(ctx, cmd)
		if err != nil {
			return "", err
		}
		req.Command = signed[0]
	}
	c, err := o.relay(ctx)
	if err != nil {
		return "", err
	}
	n, err := c.Machine(ctx, machineID)
	if err != nil {
		return "", connect.NewError(connect.CodeUnavailable, err)
	}
	res, err := n.Admin.ApplyAdminCommand(ctx, req)
	if err != nil {
		c.Forget(machineID)
		return "", err
	}
	return res.Message, nil
}

// RenameMachine gives a machine another name: a setting, sent over this
// device's own connection (no Touch ID).
func (o *Owner) RenameMachine(ctx context.Context, machineID, name string) error {
	n, err := o.findMachine(ctx, machineID)
	if err != nil {
		return err
	}
	name = SanitizeMachineName(name)
	machines, err := o.machines(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if m.MachineName == name && m.MachineId != n.MachineId {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a machine named %q already exists", name))
		}
	}
	_, err = o.admin(ctx, n.MachineId, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: name}}
	})
	return err
}

func (o *Owner) SetMachinePaused(ctx context.Context, machineID string, paused bool) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetPaused{SetPaused: &identityv1.SetPaused{Paused: paused}}
	})
	return err
}

// SetAgentDefaults sets what a coding agent's sessions on a machine run
// with when the client names no model: a setting (no Touch ID).
func (o *Owner) SetAgentDefaults(ctx context.Context, machineID string, d *identityv1.AgentDefaults) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: d}
	})
	return err
}

func (o *Owner) RemoveMachine(ctx context.Context, machineID string) error {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return err
	}
	if _, err := ctl.Revoke(ctx, &relayv1.RevokeRequest{Revocations: &identityv1.RevocationList{OwnerId: o.ownerID, RevokedMachineIds: []string{machineID}}}); err != nil {
		return err
	}
	if o.Status().LocalMachineId == machineID {
		o.SetLocalMachine(ctx, false, "")
	}
	return nil
}

func (o *Owner) AddProject(ctx context.Context, req *localv1.AddProjectRequest) error {
	_, err := o.admin(ctx, req.MachineId, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: req.ProjectId, Path: req.Path, DefaultBaseRef: req.DefaultBaseRef, Workspace: req.Workspace,
			Description: req.Description, Agents: req.Agents}}
	})
	return err
}

func (o *Owner) RemoveProject(ctx context.Context, machineID, projectID string) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: projectID}}
	})
	return err
}

// onlineMachines returns the machines that can be reached (optionally just
// one): this device's own always, the others when online.
func (o *Owner) onlineMachines(ctx context.Context, only string) ([]*localv1.Machine, []*localv1.Unreachable, error) {
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, nil, err
	}
	var on []*localv1.Machine
	var off []*localv1.Unreachable
	for _, n := range machines {
		if only != "" && n.MachineId != only && n.MachineName != only {
			continue
		}
		if n.Online || n.Local {
			on = append(on, n)
		} else {
			off = append(off, &localv1.Unreachable{MachineId: n.MachineId, MachineName: n.MachineName, Error: "offline"})
		}
	}
	return on, off, nil
}

// fanOut runs remote on each machine over the relay, and local on this
// device's own in process (skipped when nil); the machines it fails on come
// back unreachable.
func (o *Owner) fanOut(ctx context.Context, machines []*localv1.Machine, local func(l *machine.Local, meta *localv1.Machine) error,
	remote func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error) ([]*localv1.Unreachable, error) {
	byID := map[string]*localv1.Machine{}
	var ids []string
	var bad []*localv1.Unreachable
	for _, n := range machines {
		if !n.Local {
			byID[n.MachineId] = n
			ids = append(ids, n.MachineId)
			continue
		}
		if l := o.local(); local != nil && l != nil {
			if err := local(l, n); err != nil {
				bad = append(bad, &localv1.Unreachable{MachineId: n.MachineId, MachineName: n.MachineName, Error: err.Error()})
			}
		}
	}
	if len(ids) == 0 {
		return bad, nil
	}
	c, err := o.relay(ctx)
	if err != nil {
		return bad, err
	}
	errs := c.FanOut(ctx, ids, func(ctx context.Context, pn *peer.Machine) error { return remote(ctx, pn, byID[pn.MachineID]) })
	for id, err := range errs {
		bad = append(bad, &localv1.Unreachable{MachineId: id, MachineName: byID[id].MachineName, Error: err.Error()})
	}
	return bad, nil
}

func (o *Owner) ListProjects(ctx context.Context, machineID string) (*localv1.ListProjectsResponse, error) {
	on, off, err := o.onlineMachines(ctx, machineID)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	out := &localv1.ListProjectsResponse{Unreachable: off}
	// The followed machines' projects are in the replica; the others are asked.
	var ask []*localv1.Machine
	o.rep.feeds(func(feeds map[string]*feed) {
		for _, n := range on {
			if f := feeds[n.MachineId]; !n.Local && f != nil && f.ready {
				out.Projects = append(out.Projects, f.projects...)
			} else {
				ask = append(ask, n)
			}
		}
	})
	add := func(res *machinev1.ListProjectsResponse, meta *localv1.Machine) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range res.Projects {
			out.Projects = append(out.Projects, &localv1.Project{MachineId: meta.MachineId, ProjectId: p.ProjectId, DefaultBaseRef: p.DefaultBaseRef, Workspace: p.Workspace,
				Description: p.Description, Agents: p.Agents, Path: p.Path})
		}
	}
	bad, err := o.fanOut(ctx, ask, func(l *machine.Local, meta *localv1.Machine) error {
		res, err := l.ListProjects(ctx)
		if err == nil {
			add(res, meta)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		res, err := n.Info.ListProjects(ctx, &machinev1.ListProjectsRequest{})
		if err == nil {
			add(res, meta)
		}
		return err
	})
	out.Unreachable = append(out.Unreachable, bad...)
	return out, err
}

// ---------------------------------------------------------------------------
// Agents.

func specFromGrant(g *identityv1.Grant) *localv1.GrantSpec {
	ttl := uint32(0)
	if g.ExpiresAt != nil && g.NotBefore != nil {
		ttl = uint32(g.ExpiresAt.AsTime().Sub(g.NotBefore.AsTime()).Hours()/24 + 0.5)
	}
	return &localv1.GrantSpec{Label: g.ClientLabel, MachineIds: g.MachineIds, ProjectIds: g.ProjectIds, Agents: g.Agents,
		TaskVisibility: g.TaskVisibility, TtlDays: ttl, MaxConcurrentSessions: g.MaxConcurrentSessions}
}

// ListClients lists the owner's agents, as the replica last polled them.
func (o *Owner) ListClients(ctx context.Context) ([]*localv1.Client, error) {
	s, err := o.rep.use(ctx)
	if err != nil {
		return nil, err
	}
	o.rep.mu.Lock()
	defer o.rep.mu.Unlock()
	return listed(&s.clients)
}

// listClients asks the relay for the owner's agents.
func (o *Owner) listClients(ctx context.Context) ([]*localv1.Client, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil, err
	}
	return o.clientsFrom(res), nil
}

// clientsFrom is the agents the relay listed (ListClients, or WatchOwner)
// whose grants this owner signed.
func (o *Owner) clientsFrom(res *relayv1.ListClientsResponse) []*localv1.Client {
	byGrant := map[string]*localv1.Client{}
	var out []*localv1.Client
	for _, c := range res.Clients {
		g, _, err := o.trust.VerifyGrant(c.Grant, time.Now())
		expired := false
		if err != nil {
			// Expired grants still show up so they can be renewed.
			g = &identityv1.Grant{}
			if identity.Unverified(c.Grant, g) != nil || g.OwnerId != o.ownerID {
				continue
			}
			expired = true
		}
		a := &localv1.Client{ClientId: g.ClientId, GrantId: g.GrantId, Label: g.ClientLabel, Fingerprint: identity.ShortFingerprint(g.ClientPublicKey),
			Spec: specFromGrant(g), GrantVersion: g.Version, ExpiresAt: g.ExpiresAt, Online: c.Online && !expired, LastSeen: c.LastSeen, Revoked: c.Revoked,
			Custodian: g.Custodian}
		if len(c.MachineIds) > 0 { // without removed machines; an update signs them away
			a.Spec.MachineIds = c.MachineIds
		}
		if prev := byGrant[g.GrantId]; prev == nil || prev.GrantVersion < a.GrantVersion {
			byGrant[g.GrantId] = a
		}
	}
	for _, a := range byGrant {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b *localv1.Client) int { return strings.Compare(a.Label, b.Label) })
	return out
}

func (o *Owner) latestGrant(ctx context.Context, grantID string) (*identityv1.Grant, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil, err
	}
	var best *identityv1.Grant
	for _, c := range res.Clients {
		g := &identityv1.Grant{}
		if identity.Unverified(c.Grant, g) != nil || g.GrantId != grantID {
			continue
		}
		if best == nil || g.Version > best.Version {
			best = g
		}
	}
	if best == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no agent with grant %s", grantID))
	}
	return best, nil
}

// UpdateClient re-issues the grant with a new spec. Older versions are
// invalidated immediately (they may have been more permissive).
func (o *Owner) UpdateClient(ctx context.Context, grantID string, spec *localv1.GrantSpec) (uint64, []*localv1.MachineAck, error) {
	cur, err := o.latestGrant(ctx, grantID)
	if err != nil {
		return 0, nil, err
	}
	g := grantFromSpec(cur, spec)
	g.Version = cur.Version + 1
	signed, err := o.signRoot(ctx, g)
	if err != nil {
		return 0, nil, err
	}
	ctl, _, err := o.control(ctx)
	if err != nil {
		return 0, nil, err
	}
	if _, err := ctl.PublishGrant(ctx, &relayv1.PublishGrantRequest{Grant: signed[0]}); err != nil {
		return 0, nil, err
	}
	o.st.putGrant(signed[0], false)
	go o.refreshCards()
	acks, err := o.revoke(ctx, &identityv1.RevocationList{OwnerId: o.ownerID, MinGrantVersions: map[string]uint64{grantID: g.Version}})
	return g.Version, acks, err
}

func (o *Owner) RevokeClient(ctx context.Context, grantID string) ([]*localv1.MachineAck, error) {
	acks, err := o.revoke(ctx, &identityv1.RevocationList{OwnerId: o.ownerID, RevokedGrantIds: []string{grantID}})
	if err == nil {
		o.st.setRevoked(grantID)
	}
	return acks, err
}

// revoke tells the relay (which disconnects affected peers at once) and
// then pushes the list to every online machine over E2E, so a relay that
// swallows revocations is noticed.
func (o *Owner) revoke(ctx context.Context, r *identityv1.RevocationList) ([]*localv1.MachineAck, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := ctl.Revoke(ctx, &relayv1.RevokeRequest{Revocations: r}); err != nil {
		return nil, err
	}
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	var on []*localv1.Machine
	var acks []*localv1.MachineAck
	for _, n := range machines {
		if n.Online || n.Local {
			on = append(on, n)
		} else {
			acks = append(acks, &localv1.MachineAck{MachineId: n.MachineId, MachineName: n.MachineName, Error: "offline; it will apply the change when it reconnects"})
		}
	}
	var mu sync.Mutex
	applied := func(meta *localv1.Machine) {
		mu.Lock()
		acks = append(acks, &localv1.MachineAck{MachineId: meta.MachineId, MachineName: meta.MachineName, Ok: true})
		mu.Unlock()
	}
	bad, err := o.fanOut(ctx, on, func(l *machine.Local, meta *localv1.Machine) error {
		l.PushRevocations(r, o.trust.RootChain())
		applied(meta)
		return nil
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		_, err := n.Admin.PushRevocations(ctx, &machinev1.PushRevocationsRequest{Revocations: r, RootChain: o.trust.RootChain()})
		if err == nil {
			applied(meta)
		}
		return err
	})
	for _, b := range bad {
		acks = append(acks, &localv1.MachineAck{MachineId: b.MachineId, MachineName: b.MachineName, Error: b.Error})
	}
	return acks, err
}

// ---------------------------------------------------------------------------
// Sessions.

// ListSessions takes the sessions of every reachable machine (or one), or
// its page of them, and merges the pages into one. In the BASIC view a
// followed machine's first page is in the replica (its newest
// keepSessions, all of them when it has no more); the others are asked.
func (o *Owner) ListSessions(ctx context.Context, req *localv1.ListSessionsRequest) (*localv1.ListSessionsResponse, error) {
	on, off, err := o.onlineMachines(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	more := false
	out := &localv1.ListSessionsResponse{Unreachable: off}
	basic := req.View == sessionv1.SessionView_SESSION_VIEW_BASIC
	var ask []*localv1.Machine
	o.rep.feeds(func(feeds map[string]*feed) {
		for _, n := range on {
			f := feeds[n.MachineId]
			if n.Local || f == nil || !f.ready || !basic || req.Before != nil || f.more && (req.Limit == 0 || req.Limit > keepSessions) {
				ask = append(ask, n)
				continue
			}
			for _, s := range f.sessions {
				out.Sessions = append(out.Sessions, s)
			}
			more = more || f.more
		}
	})
	page := &sessionv1.ListSessionsRequest{Limit: req.Limit, Before: req.Before, View: req.View}
	add := func(res *sessionv1.ListSessionsResponse) {
		if basic {
			res.Sessions = machine.BasicSessions(res.Sessions) // a machine from before views sends them whole
		}
		mu.Lock()
		out.Sessions = append(out.Sessions, res.Sessions...)
		more = more || res.Next != nil
		mu.Unlock()
	}
	bad, err := o.fanOut(ctx, ask, func(l *machine.Local, _ *localv1.Machine) error {
		res, err := l.ListSessions(page)
		if err == nil {
			add(res)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, _ *localv1.Machine) error {
		res, err := n.Session.ListSessions(ctx, page)
		if err == nil {
			add(res)
		}
		return err
	})
	out.Unreachable = append(out.Unreachable, bad...)
	if req.Limit > 0 {
		out.Sessions, out.Next = onePage(out.Sessions, int(req.Limit), req.Before, more)
	} else {
		slices.SortFunc(out.Sessions, func(a, b *sessionv1.Session) int { return b.UpdatedAt.AsTime().Compare(a.UpdatedAt.AsTime()) })
	}
	return out, err
}

// GetUsage asks every reachable machine for its tokens by day and coding
// agent and adds them up. Another machine's answer is kept while its turns
// (as the replica follows them) and the day stay the same.
func (o *Owner) GetUsage(ctx context.Context, req *localv1.GetUsageRequest) (*localv1.GetUsageResponse, error) {
	on, off, err := o.onlineMachines(ctx, "")
	if err != nil {
		return nil, err
	}
	day := usageDay(req.TimeZone)
	var held []*sessionv1.GetUsageResponse
	var ask []*localv1.Machine
	for _, n := range on {
		if res := o.rep.heldUsage(n.MachineId, req, day); !n.Local && res != nil {
			held = append(held, res)
		} else {
			ask = append(ask, n)
		}
	}
	var mu sync.Mutex
	type key struct{ date, agent string }
	days := map[key]*sessionv1.UsageDay{}
	question := &sessionv1.GetUsageRequest{Since: req.Since, TimeZone: req.TimeZone}
	add := func(res *sessionv1.GetUsageResponse) {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range res.Days {
			k := key{d.Date, d.Agent}
			if t := days[k]; t != nil {
				t.InputTokens += d.InputTokens
				t.OutputTokens += d.OutputTokens
				t.Sessions += d.Sessions
				t.Turns += d.Turns
			} else {
				days[k] = proto.CloneOf(d) // a kept answer stays as it was
			}
		}
	}
	bad, err := o.fanOut(ctx, ask, func(l *machine.Local, _ *localv1.Machine) error {
		res, err := l.Usage(question)
		if err == nil {
			add(res)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, _ *localv1.Machine) error {
		turns, known := o.rep.turns(n.MachineID)
		res, err := n.Session.GetUsage(ctx, question)
		if err == nil {
			add(res)
			if known {
				o.rep.keepUsage(n.MachineID, req, day, turns, res)
			}
		}
		return err
	})
	for _, res := range held {
		add(res)
	}
	out := &localv1.GetUsageResponse{Unreachable: append(off, bad...)}
	for _, d := range days {
		out.Days = append(out.Days, d)
	}
	machine.SortUsage(out.Days)
	return out, err
}

// onePage cuts the machines' pages (each machine's newest past before, a
// first page with its unsettled sessions too) to the newest limit across
// them, plus on a first page the unsettled ones, and says where the next
// page starts: just past the last one kept, when some machine has more or
// some were cut. A machine too old to page answers with all its sessions.
func onePage(sessions []*sessionv1.Session, limit int, before *sessionv1.SessionCursor, more bool) ([]*sessionv1.Session, *sessionv1.SessionCursor) {
	slices.SortFunc(sessions, machine.NewestFirst)
	var page, unsettled []*sessionv1.Session
	for _, s := range sessions {
		if !machine.Past(s, before) {
			continue
		}
		if len(page) < limit {
			page = append(page, s)
			continue
		}
		more = true // the next page brings it
		if before == nil && machine.Unsettled(s) {
			unsettled = append(unsettled, s)
		}
	}
	var next *sessionv1.SessionCursor
	if more && len(page) > 0 {
		next = machine.CursorAt(page[len(page)-1])
	}
	return append(page, unsettled...), next
}

// localSession is this device's own machine when it owns the session (by
// the session id's prefix), else nil.
func (o *Owner) localSession(sessionID string) *machine.Local {
	l := o.local()
	parts := strings.SplitN(sessionID, "_", 3)
	if l == nil || len(parts) != 3 || parts[0] != "s" || !strings.HasPrefix(strings.TrimPrefix(l.MachineID(), "m_"), parts[1]) {
		return nil
	}
	return l
}

// sessionMachine finds the machine that owns a session from its id prefix.
func (o *Owner) sessionMachine(ctx context.Context, sessionID string) (*peer.Machine, error) {
	if parts := strings.SplitN(sessionID, "_", 3); len(parts) != 3 || parts[0] != "s" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad session id"))
	}
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range machines {
		if ownsSession(n.MachineId, sessionID) {
			c, err := o.relay(ctx)
			if err != nil {
				return nil, err
			}
			pn, err := c.Machine(ctx, n.MachineId)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s is unreachable: %w", n.MachineName, err))
			}
			return pn, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("no machine owns this session"))
}

func (o *Owner) StreamSessionEvents(ctx context.Context, req *localv1.StreamSessionEventsRequest, send func(*sessionv1.SessionEvent) error) error {
	ask := &sessionv1.StreamEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, Follow: req.Follow, Partial: req.Partial, View: req.View}
	if l := o.localSession(req.SessionId); l != nil {
		return l.StreamEvents(ctx, ask, send)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return err
	}
	if req.View == sessionv1.EventView_EVENT_VIEW_BASIC { // a machine from before views sends events whole
		full := send
		send = func(e *sessionv1.SessionEvent) error { return full(machine.BasicEvent(e)) }
	}
	stream, err := n.Session.StreamEvents(ctx, ask)
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(stream.Msg()); err != nil {
			return err
		}
	}
	return stream.Err()
}

// ListSessionEvents pages through a session's events on its machine.
func (o *Owner) ListSessionEvents(ctx context.Context, req *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
	if l := o.localSession(req.SessionId); l != nil {
		return l.ListEvents(req)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	res, err := n.Session.ListEvents(ctx, req)
	if err == nil && req.View == sessionv1.EventView_EVENT_VIEW_BASIC {
		for i, e := range res.Events {
			res.Events[i] = machine.BasicEvent(e)
		}
	}
	return res, err
}

// ReadToolOutput streams a tool's whole output from its session's machine.
// One from before ReadToolOutput gives it as the event at req.Seq recorded
// it (its first 16 KiB).
func (o *Owner) ReadToolOutput(ctx context.Context, req *localv1.ReadToolOutputRequest, send func(*sessionv1.ToolOutputChunk) error) error {
	ask := &sessionv1.ReadToolOutputRequest{SessionId: req.SessionId, CallId: req.CallId}
	if l := o.localSession(req.SessionId); l != nil {
		return l.ReadToolOutput(ask, send)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return err
	}
	stream, err := n.Session.ReadToolOutput(ctx, ask)
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(stream.Msg()); err != nil {
			return err
		}
	}
	if connect.CodeOf(stream.Err()) != connect.CodeUnimplemented || req.Seq == 0 {
		return stream.Err()
	}
	res, err := n.Session.ListEvents(ctx, &sessionv1.ListEventsRequest{SessionId: req.SessionId, AfterSeq: req.Seq - 1, Limit: 1})
	if err != nil {
		return err
	}
	for _, e := range res.Events {
		if r := e.GetToolResult(); r != nil && r.CallId == req.CallId {
			return send(&sessionv1.ToolOutputChunk{Data: []byte(r.Summary), Size: uint64(len(r.Summary))})
		}
	}
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no output of %s at %d", req.CallId, req.Seq))
}

func (o *Owner) InterruptSession(ctx context.Context, id string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Interrupt(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.InterruptSession(ctx, &sessionv1.InterruptSessionRequest{SessionId: id}))
}

func (o *Owner) ArchiveSession(ctx context.Context, id string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Archive(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.ArchiveSession(ctx, &sessionv1.ArchiveSessionRequest{SessionId: id}))
}

func (o *Owner) UnarchiveSession(ctx context.Context, id string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Unarchive(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.UnarchiveSession(ctx, &sessionv1.UnarchiveSessionRequest{SessionId: id}))
}

// DeleteSession deletes a session for good on its machine.
func (o *Owner) DeleteSession(ctx context.Context, id string) error {
	if l := o.localSession(id); l != nil {
		return l.Delete(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return err
	}
	if _, err = n.Session.DeleteSession(ctx, &sessionv1.DeleteSessionRequest{SessionId: id}); err == nil {
		o.rep.sessionWritten(id, nil, true)
	}
	return err
}

// written keeps in the replica what a change to another machine's session
// returned, and passes it on.
func (o *Owner) written(id string) func(*sessionv1.Session, error) (*sessionv1.Session, error) {
	return func(s *sessionv1.Session, err error) (*sessionv1.Session, error) {
		if err == nil {
			o.rep.sessionWritten(id, s, false)
		}
		return s, err
	}
}

// RenameSession changes a session's title on its machine.
func (o *Owner) RenameSession(ctx context.Context, id, title string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Rename(ctx, id, title)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.RenameSession(ctx, &sessionv1.RenameSessionRequest{SessionId: id, Title: title}))
}

// AnswerQuestion answers a session's question as the owner.
func (o *Owner) AnswerQuestion(ctx context.Context, req *sessionv1.AnswerQuestionRequest) (*sessionv1.Session, error) {
	if l := o.localSession(req.SessionId); l != nil {
		return l.AnswerQuestion(ctx, req)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	return o.written(req.SessionId)(n.Session.AnswerQuestion(ctx, req))
}

func (o *Owner) GetDiff(ctx context.Context, id string, turn uint32) (*sessionv1.GetDiffResponse, error) {
	if l := o.localSession(id); l != nil {
		return l.Diff(ctx, &sessionv1.GetDiffRequest{SessionId: id, Turn: turn})
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return n.Session.GetDiff(ctx, &sessionv1.GetDiffRequest{SessionId: id, Turn: turn})
}
