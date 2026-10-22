package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// Crockford base32 avoids I, L, O, U.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// PairingCode derives the 12-character (60-bit) code from the subject key,
// so a relay that swaps the key produces a different code.
func PairingCode(kind identityv1.PairingKind, spki []byte) string {
	h := sha256.New()
	h.Write([]byte("hermesh-pair-v1\x00"))
	h.Write([]byte{byte(kind)})
	h.Write(spki)
	c := crockford.EncodeToString(h.Sum(nil))[:12]
	return c[:4] + "-" + c[4:8] + "-" + c[8:12]
}

// NormalizePairingCode accepts lowercase input, spaces and missing dashes,
// and maps commonly confused letters per Crockford.
func NormalizePairingCode(s string) string {
	r := strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1")
	c := r.Replace(strings.ToUpper(strings.TrimSpace(s)))
	if len(c) != 12 {
		return c
	}
	return c[:4] + "-" + c[4:8] + "-" + c[8:12]
}

// RandomCode returns n random Crockford base32 characters.
func RandomCode(n int) string {
	b := make([]byte, (n*5+7)/8)
	rand.Read(b)
	return crockford.EncodeToString(b)[:n]
}

// JoinMatchCode is the two digits a trusted device shows once it signed
// the root cert adding a new device, for the person to type on that device.
// The new device computes them from the root cert it received: they match
// only if the cert came from the person's own device, so a center that
// routed the request to another owner is caught (1 in 100 to guess).
func JoinMatchCode(rootCertBody []byte) string {
	h := sha256.Sum256(append([]byte("hermesh-join-match-v1\x00"), rootCertBody...))
	return fmt.Sprintf("%02d", (uint16(h[0])<<8|uint16(h[1]))%100)
}

// VerificationCode binds a requester's key to the owner it was told about
// when a hosted center claims its pairing request. The requester computes
// it from its own key and the owner id it received; the owner's signer from
// the key it received and its own owner id. The two match only if nobody
// swapped either, so the center stays out of the trust anchor.
func VerificationCode(kind identityv1.PairingKind, spki []byte, ownerID string) string {
	h := sha256.New()
	h.Write([]byte("hermesh-enroll-v1\x00"))
	h.Write([]byte{byte(kind)})
	h.Write(spki)
	h.Write([]byte(ownerID))
	c := crockford.EncodeToString(h.Sum(nil))[:12]
	return c[:4] + "-" + c[4:8] + "-" + c[8:12]
}
