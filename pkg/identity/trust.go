package identity

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// ClockSkew tolerated when checking validity windows.
const ClockSkew = 5 * time.Minute

// Trust verifies credentials against one owner, anchored on the owner ID
// (the fingerprint of the first trusted device's root key). It keeps the
// longest root chain it has verified (docs/trusted-devices.md 3).
type Trust struct {
	ownerID string

	mu       sync.RWMutex
	chain    []*identityv1.Signed
	root     *identityv1.RootCert
	rootKeys map[string]*ecdsa.PublicKey
}

func NewTrust(ownerID string) *Trust {
	return &Trust{ownerID: ownerID, rootKeys: map[string]*ecdsa.PublicKey{}}
}

func (t *Trust) OwnerID() string { return t.ownerID }

// RootChain returns the chain held, or nil.
func (t *Trust) RootChain() *identityv1.RootChain {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.chain == nil {
		return nil
	}
	return &identityv1.RootChain{Certs: slices.Clone(t.chain)}
}

// Root returns the newest root cert held, or nil.
func (t *Trust) Root() *identityv1.RootCert {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.root
}

func (t *Trust) RootVersion() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.root.GetVersion()
}

// AcceptRootChain verifies c against the anchor and adopts it if it extends
// the chain held. A chain that the held one extends (or equals) is accepted
// without change; one that differs from it at some version is a fork and
// refused. It reports whether the chain changed.
func (t *Trust) AcceptRootChain(c *identityv1.RootChain) (bool, error) {
	certs := c.GetCerts()
	t.mu.RLock()
	held, keys, last := t.chain, t.rootKeys, t.root
	t.mu.RUnlock()
	for i := 0; i < len(held) && i < len(certs); i++ {
		if !bytes.Equal(held[i].GetBody(), certs[i].GetBody()) {
			return false, fmt.Errorf("conflicting root cert v%d", i+1)
		}
	}
	if len(certs) <= len(held) {
		if len(certs) == 0 {
			return false, errors.New("empty root chain")
		}
		return false, nil
	}
	rc, newKeys, err := verifyChain(t.ownerID, last, keys, certs[len(held):])
	if err != nil {
		return false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.chain) != len(held) {
		return false, errors.New("root chain changed concurrently")
	}
	t.chain, t.root, t.rootKeys = slices.Clone(certs), rc, newKeys
	return true, nil
}

// ChainOwner is the owner a root chain names, unverified ("" if none).
func ChainOwner(c *identityv1.RootChain) string {
	rc := &identityv1.RootCert{}
	if len(c.GetCerts()) == 0 || Unverified(c.Certs[0], rc) != nil {
		return ""
	}
	return rc.OwnerId
}

// VerifyRootChain checks a whole chain against an owner ID anchor and
// returns its newest root cert.
func VerifyRootChain(c *identityv1.RootChain, ownerID string) (*identityv1.RootCert, error) {
	if len(c.GetCerts()) == 0 {
		return nil, errors.New("empty root chain")
	}
	rc, _, err := verifyChain(ownerID, nil, nil, c.Certs)
	return rc, err
}

// verifyChain verifies certs following prev (nil: certs starts at version
// 1), whose root keys are prevKeys, and returns the last cert and its keys.
func verifyChain(ownerID string, prev *identityv1.RootCert, prevKeys map[string]*ecdsa.PublicKey, certs []*identityv1.Signed) (*identityv1.RootCert, map[string]*ecdsa.PublicKey, error) {
	for _, s := range certs {
		rc := &identityv1.RootCert{}
		if err := proto.Unmarshal(s.GetBody(), rc); err != nil {
			return nil, nil, err
		}
		if rc.OwnerId != ownerID {
			return nil, nil, errors.New("root cert is for a different owner")
		}
		keys := map[string]*ecdsa.PublicKey{}
		for _, rk := range rc.RootKeys {
			pub, err := ParsePublicKey(rk.PublicKey)
			if err != nil {
				return nil, nil, fmt.Errorf("root key: %w", err)
			}
			keys[KeyID(pub)] = pub
		}
		if len(keys) == 0 || len(keys) != len(rc.RootKeys) {
			return nil, nil, errors.New("root cert needs distinct root keys")
		}
		signers := prevKeys
		if prev == nil {
			// Version 1: one key, which signs it and names the owner.
			if rc.Version != 1 || len(rc.RootKeys) != 1 || OwnerID(rc.RootKeys[0].PublicKey) != ownerID {
				return nil, nil, errors.New("root cert v1 must list the one key the owner id names")
			}
			signers = keys
		} else if rc.Version != prev.Version+1 {
			return nil, nil, fmt.Errorf("root cert v%d does not follow v%d", rc.Version, prev.Version)
		}
		pub := signers[s.GetSignerKeyId()]
		if pub == nil {
			return nil, nil, fmt.Errorf("root cert v%d is not signed by a device of v%d", rc.Version, rc.Version-1)
		}
		if err := VerifyWith(s, pub, rc); err != nil {
			return nil, nil, fmt.Errorf("root cert v%d: %w", rc.Version, err)
		}
		prev, prevKeys = rc, keys
	}
	return prev, prevKeys, nil
}

func (t *Trust) verifyByRoot(s *identityv1.Signed, out proto.Message) error {
	t.mu.RLock()
	pub := t.rootKeys[s.GetSignerKeyId()]
	t.mu.RUnlock()
	if pub == nil {
		return errors.New("not signed by a current owner root key")
	}
	return VerifyWith(s, pub, out)
}

func (t *Trust) VerifyMachineCert(s *identityv1.Signed) (*identityv1.MachineCert, *ecdsa.PublicKey, error) {
	nc := &identityv1.MachineCert{}
	if err := t.verifyByRoot(s, nc); err != nil {
		return nil, nil, fmt.Errorf("machine cert: %w", err)
	}
	if nc.OwnerId != t.ownerID {
		return nil, nil, errors.New("machine cert: wrong owner")
	}
	pub, err := ParsePublicKey(nc.MachinePublicKey)
	if err != nil {
		return nil, nil, err
	}
	if nc.MachineId != MachineID(nc.MachinePublicKey) {
		return nil, nil, errors.New("machine cert: machine id does not match key")
	}
	return nc, pub, nil
}

func (t *Trust) VerifyGrant(s *identityv1.Signed, now time.Time) (*identityv1.Grant, *ecdsa.PublicKey, error) {
	g := &identityv1.Grant{}
	if err := t.verifyByRoot(s, g); err != nil {
		return nil, nil, fmt.Errorf("grant: %w", err)
	}
	if g.OwnerId != t.ownerID {
		return nil, nil, errors.New("grant: wrong owner")
	}
	pub, err := ParsePublicKey(g.ClientPublicKey)
	if err != nil {
		return nil, nil, err
	}
	if g.ClientId != ClientID(g.ClientPublicKey) {
		return nil, nil, errors.New("grant: client id does not match key")
	}
	if nb := g.NotBefore.AsTime(); g.NotBefore != nil && now.Add(ClockSkew).Before(nb) {
		return nil, nil, errors.New("grant: not yet valid")
	}
	if g.ExpiresAt == nil || now.Add(-ClockSkew).After(g.ExpiresAt.AsTime()) {
		return nil, nil, errors.New("grant: expired")
	}
	return g, pub, nil
}

func (t *Trust) VerifyDeviceCert(s *identityv1.Signed) (*identityv1.OwnerDeviceCert, *ecdsa.PublicKey, error) {
	d := &identityv1.OwnerDeviceCert{}
	if err := t.verifyByRoot(s, d); err != nil {
		return nil, nil, fmt.Errorf("device cert: %w", err)
	}
	if d.OwnerId != t.ownerID {
		return nil, nil, errors.New("device cert: wrong owner")
	}
	pub, err := ParsePublicKey(d.DevicePublicKey)
	return d, pub, err
}

func (t *Trust) VerifyAdminCommand(s *identityv1.Signed, machineID string, now time.Time) (*identityv1.AdminCommand, error) {
	c := &identityv1.AdminCommand{}
	if err := t.verifyByRoot(s, c); err != nil {
		return nil, fmt.Errorf("admin command: %w", err)
	}
	if c.OwnerId != t.ownerID || c.TargetMachineId != machineID {
		return nil, errors.New("admin command: wrong owner or target")
	}
	if c.ExpiresAt == nil || now.After(c.ExpiresAt.AsTime()) {
		return nil, errors.New("admin command: expired")
	}
	return c, nil
}

// VerifyEraseOrder checks the owner's order to erase everything
// (docs/erase-everything.md): signed by one of its current root keys.
func (t *Trust) VerifyEraseOrder(s *identityv1.Signed) (*identityv1.EraseOrder, error) {
	o := &identityv1.EraseOrder{}
	if err := t.verifyByRoot(s, o); err != nil {
		return nil, fmt.Errorf("erase order: %w", err)
	}
	if o.OwnerId != t.ownerID || o.EraseId == "" {
		return nil, errors.New("erase order: wrong owner")
	}
	return o, nil
}

// SignEraseAck says, with key s, that subject (the id the owner knows it
// by) got the erase order eraseID and is erasing. A trusted device passes
// its device cert, which ties s to its root key, the subject.
func SignEraseAck(eraseID, subject string, deviceCert *identityv1.Signed, s Signer) (*identityv1.Signed, error) {
	return Sign(&identityv1.EraseAck{EraseId: eraseID, PublicKey: MarshalPublicKey(s.Public()), Subject: subject, DeviceCert: deviceCert}, s)
}

// VerifyEraseAck checks an ack against the key it carries. Its subject must
// be named by that key (a machine's or an agent's id) or, for a trusted
// device, by its device cert, which trust (the erased owner's chain; nil:
// none) verifies: the cert names the key, signed by the subject root key.
func VerifyEraseAck(s *identityv1.Signed, trust *Trust) (*identityv1.EraseAck, error) {
	a := &identityv1.EraseAck{}
	if _, err := SelfSigned(s, a, func(a *identityv1.EraseAck) []byte { return a.PublicKey }); err != nil {
		return nil, fmt.Errorf("erase ack: %w", err)
	}
	if a.DeviceCert == nil {
		if a.Subject != MachineID(a.PublicKey) && a.Subject != ClientID(a.PublicKey) {
			return nil, errors.New("erase ack: its subject is not named by its key")
		}
		return a, nil
	}
	if trust == nil {
		return nil, errors.New("erase ack: no owner to verify its device cert against")
	}
	if _, pub, err := trust.VerifyDeviceCert(a.DeviceCert); err != nil || !bytes.Equal(MarshalPublicKey(pub), a.PublicKey) {
		return nil, fmt.Errorf("erase ack: its device cert does not name its key (%v)", err)
	}
	if a.DeviceCert.SignerKeyId != a.Subject {
		return nil, errors.New("erase ack: its device cert is not signed by its subject")
	}
	return a, nil
}

// SubjectKind identifies who presented credentials.
type SubjectKind int

const (
	SubjectMachine SubjectKind = iota + 1
	SubjectClient
	SubjectOwnerDevice
)

// Subject is a verified credential holder.
type Subject struct {
	Kind      SubjectKind
	ID        string // machine_id, client_id or owner_id
	PublicKey *ecdsa.PublicKey
	Machine   *identityv1.MachineCert
	// MachineName is the name a machine presented with its credentials:
	// authenticated only where the credentials came in a handshake the
	// machine's key made (an end-to-end connection).
	MachineName string
	Grant       *identityv1.Grant
	Device      *identityv1.OwnerDeviceCert
}

// VerifyCredentials adopts a longer root chain carried in c and then
// verifies the subject credential against the current root keys (an older
// chain that the held one extends is fine).
func (t *Trust) VerifyCredentials(c *identityv1.Credentials, now time.Time) (*Subject, error) {
	if c == nil {
		return nil, errors.New("missing credentials")
	}
	if rc := c.RootChain; rc != nil {
		if _, err := t.AcceptRootChain(rc); err != nil {
			return nil, err
		}
	}
	switch s := c.Subject.(type) {
	case *identityv1.Credentials_MachineCert:
		nc, pub, err := t.VerifyMachineCert(s.MachineCert)
		if err != nil {
			return nil, err
		}
		return &Subject{Kind: SubjectMachine, ID: nc.MachineId, PublicKey: pub, Machine: nc, MachineName: c.MachineName}, nil
	case *identityv1.Credentials_Grant:
		g, pub, err := t.VerifyGrant(s.Grant, now)
		if err != nil {
			return nil, err
		}
		return &Subject{Kind: SubjectClient, ID: g.ClientId, PublicKey: pub, Grant: g}, nil
	case *identityv1.Credentials_DeviceCert:
		d, pub, err := t.VerifyDeviceCert(s.DeviceCert)
		if err != nil {
			return nil, err
		}
		return &Subject{Kind: SubjectOwnerDevice, ID: d.OwnerId, PublicKey: pub, Device: d}, nil
	}
	return nil, errors.New("credentials carry no subject")
}

// Allows reports whether a grant scope list ("*" = all) contains v.
func Allows(scope []string, v string) bool {
	return slices.Contains(scope, "*") || slices.Contains(scope, v)
}
