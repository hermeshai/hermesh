// Package identity implements Hermesh keys, signed envelopes, credential
// chain verification and pairing codes.
package identity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Signer signs domain-separated messages with an ECDSA P-256 key. The
// signature is ASN.1 DER over SHA-256(message). Root keys held in the
// Secure Enclave implement this through signing delegation.
type Signer interface {
	Public() *ecdsa.PublicKey
	SignMessage(message []byte) ([]byte, error)
}

// SoftwareKey is a P-256 private key held in memory.
type SoftwareKey struct {
	Key *ecdsa.PrivateKey
}

func GenerateKey() (*SoftwareKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &SoftwareKey{Key: k}, nil
}

func (k *SoftwareKey) Public() *ecdsa.PublicKey { return &k.Key.PublicKey }

func (k *SoftwareKey) SignMessage(message []byte) ([]byte, error) {
	h := sha256.Sum256(message)
	return ecdsa.SignASN1(rand.Reader, k.Key, h[:])
}

// CryptoSigner exposes the key for crypto/tls.
func (k *SoftwareKey) CryptoSigner() crypto.Signer { return k.Key }

// SaveKey writes the key as PKCS#8 PEM with mode 0600.
func SaveKey(path string, k *SoftwareKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(k.Key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadKey(path string) (*SoftwareKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%s: not a P-256 key", path)
	}
	return &SoftwareKey{Key: ek}, nil
}

// LoadOrCreateKey loads the key at path, creating it if missing.
func LoadOrCreateKey(path string) (*SoftwareKey, error) {
	k, err := LoadKey(path)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k, err = GenerateKey()
	if err != nil {
		return nil, err
	}
	return k, SaveKey(path, k)
}

// MarshalPublicKey returns the PKIX SubjectPublicKeyInfo DER encoding.
func MarshalPublicKey(pub *ecdsa.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err) // P-256 keys always marshal
	}
	return der
}

func ParsePublicKey(spki []byte) (*ecdsa.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 public key")
	}
	return ek, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Fingerprint is lowercase base32 of SHA-256(SPKI).
func Fingerprint(spki []byte) string {
	h := sha256.Sum256(spki)
	return strings.ToLower(b32.EncodeToString(h[:]))
}

func KeyID(pub *ecdsa.PublicKey) string { return Fingerprint(MarshalPublicKey(pub)) }

func OwnerID(firstRootKey []byte) string { return "o_" + Fingerprint(firstRootKey)[:24] }
func MachineID(spki []byte) string       { return "m_" + Fingerprint(spki)[:24] }
func ClientID(spki []byte) string        { return "c_" + Fingerprint(spki)[:24] }

// ShortFingerprint is for humans: "abcd efgh".
func ShortFingerprint(spki []byte) string {
	fp := Fingerprint(spki)
	return fp[:4] + " " + fp[4:8]
}
