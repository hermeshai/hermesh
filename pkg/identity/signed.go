package identity

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// MessageType returns the fully-qualified protobuf name used as the signing
// domain, e.g. "hermesh.identity.v1.Grant".
func MessageType(m proto.Message) string {
	return string(m.ProtoReflect().Descriptor().FullName())
}

// SigningInput is the exact byte string a signer signs.
func SigningInput(typ string, body []byte) []byte {
	out := make([]byte, 0, len("hermesh-sig-v1")+len(typ)+len(body)+2)
	out = append(out, "hermesh-sig-v1\x00"...)
	out = append(out, typ...)
	out = append(out, 0)
	return append(out, body...)
}

// Sign serializes m and signs it.
func Sign(m proto.Message, s Signer) (*identityv1.Signed, error) {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	typ := MessageType(m)
	sig, err := s.SignMessage(SigningInput(typ, body))
	if err != nil {
		return nil, err
	}
	return &identityv1.Signed{
		Type:        typ,
		Body:        body,
		SignerKeyId: KeyID(s.Public()),
		Alg:         identityv1.SignatureAlgorithm_SIGNATURE_ALGORITHM_ECDSA_P256_SHA256,
		Signature:   sig,
	}, nil
}

var ErrBadSignature = errors.New("bad signature")

// VerifyWith checks s against pub and parses the body into out, which must
// be of the declared type.
func VerifyWith(s *identityv1.Signed, pub *ecdsa.PublicKey, out proto.Message) error {
	if s == nil {
		return errors.New("missing signed object")
	}
	if want := MessageType(out); s.Type != want {
		return fmt.Errorf("signed type %q, want %q", s.Type, want)
	}
	if s.Alg != identityv1.SignatureAlgorithm_SIGNATURE_ALGORITHM_ECDSA_P256_SHA256 {
		return fmt.Errorf("unsupported signature algorithm %v", s.Alg)
	}
	if s.SignerKeyId != KeyID(pub) {
		return fmt.Errorf("%w: signer key mismatch", ErrBadSignature)
	}
	h := sha256.Sum256(SigningInput(s.Type, s.Body))
	if !ecdsa.VerifyASN1(pub, h[:], s.Signature) {
		return ErrBadSignature
	}
	return proto.Unmarshal(s.Body, out)
}

// SelfSigned verifies a message signed by the key it carries (pairing
// requests). pubOf extracts that key from the parsed message.
func SelfSigned[T proto.Message](s *identityv1.Signed, out T, pubOf func(T) []byte) (*ecdsa.PublicKey, error) {
	if err := proto.Unmarshal(s.GetBody(), out); err != nil {
		return nil, err
	}
	pub, err := ParsePublicKey(pubOf(out))
	if err != nil {
		return nil, err
	}
	if err := VerifyWith(s, pub, out); err != nil {
		return nil, err
	}
	return pub, nil
}

// VerifyRaw checks a signature over an arbitrary domain-separated message.
func VerifyRaw(pub *ecdsa.PublicKey, message, sig []byte) error {
	h := sha256.Sum256(message)
	if !ecdsa.VerifyASN1(pub, h[:], sig) {
		return ErrBadSignature
	}
	return nil
}

// Unverified parses a signed body without checking the signature. Only use
// it to read fields needed to find the verification key, or for display of
// objects that were verified elsewhere.
func Unverified(s *identityv1.Signed, out proto.Message) error {
	if s == nil {
		return errors.New("missing signed object")
	}
	if want := MessageType(out); s.Type != want {
		return fmt.Errorf("signed type %q, want %q", s.Type, want)
	}
	return proto.Unmarshal(s.Body, out)
}
