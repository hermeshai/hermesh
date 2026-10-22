package mcpgw

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Vault seals the client private keys the gateway holds. A sealed key opens
// only with the same aad, so a key copied to another connection's row
// doesn't.
type Vault interface {
	Seal(plaintext, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// aesVault is AES-256-GCM under one master key. The sealed form is a
// version byte, the nonce, then the ciphertext.
type aesVault struct{ aead cipher.AEAD }

const vaultVersion = 1

// NewAESVault returns a Vault under a 32-byte master key.
func NewAESVault(key []byte) (Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("vault key is %d bytes, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &aesVault{aead: aead}, nil
}

// KeyFileVault returns a Vault under the master key in path, creating the
// file (mode 0600, 32 random bytes) when create is set and it is missing.
func KeyFileVault(path string, create bool) (Vault, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return NewAESVault(key)
}

func (v *aesVault) Seal(plaintext, aad []byte) ([]byte, error) {
	out := make([]byte, 1+v.aead.NonceSize(), 1+v.aead.NonceSize()+len(plaintext)+v.aead.Overhead())
	out[0] = vaultVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return v.aead.Seal(out, out[1:], plaintext, aad), nil
}

func (v *aesVault) Open(sealed, aad []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(sealed) < 1+n || sealed[0] != vaultVersion {
		return nil, errors.New("vault: not a sealed key")
	}
	return v.aead.Open(nil, sealed[1:1+n], sealed[1+n:], aad)
}
