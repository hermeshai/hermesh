package relay

import (
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// Backend persists what the relay knows, per owner. Everything in it is
// public or signed: nothing stored can make a machine execute anything.
//
// A self-hosted relay serves one owner and keeps this in SQLite (Store);
// the hosted center serves many owners from Postgres.
type Backend interface {
	RelayID() (string, error)

	// RootChain is nil for an owner this backend does not know.
	RootChain(ownerID string) (*identityv1.RootChain, error)
	SetRootChain(ownerID string, c *identityv1.RootChain) error
	Revocations(ownerID string) (*identityv1.RevocationList, error)
	SetRevocations(ownerID string, r *identityv1.RevocationList) error

	PutMachineCert(ownerID, machineID string, cert *identityv1.Signed) error
	DeleteMachineCert(ownerID, machineID string) error
	MachineCerts(ownerID string) (map[string]*identityv1.Signed, error)

	// PutGrant stores g only if its version is newer than the stored one.
	PutGrant(ownerID string, g *identityv1.Grant, signed *identityv1.Signed) error
	// DeleteGrant forgets a revoked grant (the revocation list still refuses it).
	DeleteGrant(ownerID, grantID string) error
	Grants(ownerID string) ([]*identityv1.Signed, error)
	GrantForClient(ownerID, clientID string) (*identityv1.Signed, error)

	// Pairing rendezvous, keyed by pairing code (derived from the subject
	// key, so codes are unique across owners).
	PutPairing(p *Pairing) error
	GetPairing(code string) (*Pairing, error)
	// ClaimPairing binds an unclaimed pairing to an owner (hosted mode).
	ClaimPairing(code, ownerID, accountHint string) error
	CompletePairing(code string, result *identityv1.Signed) error
	PendingPairings(now time.Time) (int, error)
	// OwnerPendingPairings lists the codes of unanswered, unexpired requests
	// bound to ownerID (and, in a single-owner store, unbound old rows),
	// newest first.
	OwnerPendingPairings(ownerID string, now time.Time) ([]string, error)
	PurgePairings(now time.Time) error

	LogConnection(ownerID string, role int, subjectID, remote, event string)
	Close() error
}

// Pairing is a pending (or just completed) pairing request.
type Pairing struct {
	Code string
	Kind identityv1.PairingKind
	// Request is the requester's self-signed PairingRequest.
	Request *identityv1.Signed
	// OwnerID is the owner the request is bound to: named by the requester
	// (self-hosted relay) or set when an account claims it (hosted center).
	// A machine never trusts this binding: it checks the verification code.
	// A client claimed through a pairing link accepts it (machine.PairWithRelay).
	OwnerID string
	// AccountHint is a masked account name shown to the requester after a
	// claim ("y***@gmail.com"); informational only.
	AccountHint string
	ExpiresAt   time.Time
	// Result is the signed MachineCert or Grant once the owner approved.
	Result *identityv1.Signed
}
