package identity

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hpke"
	"crypto/rand"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// Messages between the owner's inboxes (docs/agent-messages.md §4) are
// signed by their sender, then HPKE-sealed to the recipient inbox's key with
// the suite notices use. The inbox and the message id are part of the HPKE
// info, so a sealed message the relay moves to another inbox or id doesn't
// open.

func messageInfo(inbox, id string) []byte {
	return []byte("hermesh-message-v1\x00" + inbox + "\x00" + id)
}

// NewInboxKey makes an inbox's X25519 key pair.
func NewInboxKey() (private, public []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// InboxPublicKey is the public half of an inbox's private key.
func InboxPublicKey(private []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

// SealMessage encrypts m to an inbox key, for that inbox and message id.
func SealMessage(inboxKey []byte, inbox, id string, m proto.Message) ([]byte, error) {
	kem, kdf, aead := noticeSuite()
	if len(inboxKey) != 32 {
		return nil, errors.New("an inbox key is 32 bytes (X25519)")
	}
	pk, err := kem.NewPublicKey(inboxKey)
	if err != nil {
		return nil, err
	}
	body, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(pk, kdf, aead, messageInfo(inbox, id), body)
}

// OpenMessage decrypts what SealMessage made, with the inbox's private key.
func OpenMessage(private []byte, inbox, id string, sealed []byte, out proto.Message) error {
	kem, kdf, aead := noticeSuite()
	k, err := kem.NewPrivateKey(private)
	if err != nil {
		return err
	}
	body, err := hpke.Open(k, kdf, aead, messageInfo(inbox, id), sealed)
	if err != nil {
		return err
	}
	return proto.Unmarshal(body, out)
}

// ContactInfo is a contact that verified against the owner's trust.
type ContactInfo struct {
	// Inbox is the client id (c_…) or machine id (m_…).
	Inbox string
	// Name is the agent's as the owner approved it; "" for a machine.
	Name string
	// Kind is "mcp" or "cli" for an agent, "machine" for a machine.
	Kind string
	// Key is the identity key that signs the inbox's registration and its
	// messages.
	Key *ecdsa.PublicKey
	// InboxKey is the registered X25519 key, nil until the inbox has one.
	InboxKey []byte
	// TakesMessages is how the inbox said it takes them.
	TakesMessages string
}

// VerifyCard checks an agent's contact card: signed by the device key of a
// current trusted device, for this owner, its client id that of its key.
func (t *Trust) VerifyCard(card, deviceCert *identityv1.Signed) (*identityv1.ContactCard, *ecdsa.PublicKey, error) {
	_, dev, err := t.VerifyDeviceCert(deviceCert)
	if err != nil {
		return nil, nil, fmt.Errorf("card: %w", err)
	}
	c := &identityv1.ContactCard{}
	if err := VerifyWith(card, dev, c); err != nil {
		return nil, nil, fmt.Errorf("card: %w", err)
	}
	if c.OwnerId != t.ownerID {
		return nil, nil, errors.New("card: wrong owner")
	}
	if c.ClientId != ClientID(c.ClientPublicKey) {
		return nil, nil, errors.New("card: client id does not match key")
	}
	pub, err := ParsePublicKey(c.ClientPublicKey)
	if err != nil {
		return nil, nil, err
	}
	return c, pub, nil
}

// VerifyContact checks a contact: who the inbox is, and its registration if
// it has one.
func (t *Trust) VerifyContact(c *identityv1.Contact) (*ContactInfo, error) {
	var info ContactInfo
	switch {
	case c.GetCard() != nil:
		card, pub, err := t.VerifyCard(c.Card, c.DeviceCert)
		if err != nil {
			return nil, err
		}
		info = ContactInfo{Inbox: card.ClientId, Name: card.Name, Kind: card.Kind, Key: pub}
	case c.GetMachineCert() != nil:
		mc, pub, err := t.VerifyMachineCert(c.MachineCert)
		if err != nil {
			return nil, err
		}
		info = ContactInfo{Inbox: mc.MachineId, Kind: "machine", Key: pub}
	default:
		return nil, errors.New("contact: neither a card nor a machine cert")
	}
	if c.Registration != nil {
		reg := &identityv1.InboxRegistration{}
		if err := VerifyWith(c.Registration, info.Key, reg); err != nil {
			return nil, fmt.Errorf("inbox registration: %w", err)
		}
		if reg.OwnerId != t.ownerID || reg.Inbox != info.Inbox || len(reg.InboxPublicKey) != 32 {
			return nil, errors.New("inbox registration: not this inbox's")
		}
		info.InboxKey, info.TakesMessages = reg.InboxPublicKey, reg.TakesMessages
	}
	return &info, nil
}

// SignRegistration signs an inbox's registration with its identity key.
func SignRegistration(key Signer, ownerID, inbox string, inboxKey []byte, takes string) (*identityv1.Signed, error) {
	return Sign(&identityv1.InboxRegistration{OwnerId: ownerID, Inbox: inbox, InboxPublicKey: inboxKey, TakesMessages: takes,
		IssuedAt: timestamppb.Now()}, key)
}

// SameCard reports whether two cards say the same (issued_at aside).
func SameCard(a, b *identityv1.ContactCard) bool {
	return a.OwnerId == b.OwnerId && a.ClientId == b.ClientId && bytes.Equal(a.ClientPublicKey, b.ClientPublicKey) && a.Name == b.Name && a.Kind == b.Kind
}
