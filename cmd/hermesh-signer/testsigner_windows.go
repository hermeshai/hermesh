//go:build testsigner

package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"os"
	"path/filepath"

	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Test builds (-tags testsigner): run --test <dir> keeps software root
// keys in dir and approves everything, for end-to-end tests on machines
// with neither a TPM nor a person.
func init() {
	tpm := parts
	parts = func(sid, test string) (signer.Keys, signer.Owner, error) {
		if test == "" {
			return tpm(sid, test)
		}
		return softKeys{dir: test}, approveAll{}, os.MkdirAll(test, 0o700)
	}
}

type softKeys struct{ dir string }

func (k softKeys) Create(context.Context) (*ecdsa.PublicKey, error) {
	key, err := identity.GenerateKey()
	if err != nil {
		return nil, err
	}
	return key.Public(), identity.SaveKey(filepath.Join(k.dir, identity.KeyID(key.Public())+".pem"), key)
}

func (k softKeys) Delete(keyID string) error { return os.Remove(filepath.Join(k.dir, keyID+".pem")) }

func (k softKeys) Sign(_ context.Context, keyID string, messages [][]byte) ([][]byte, error) {
	key, err := identity.LoadKey(filepath.Join(k.dir, keyID+".pem"))
	if err != nil {
		return nil, errors.New("no such root key")
	}
	sigs := make([][]byte, len(messages))
	for i, m := range messages {
		if sigs[i], err = key.SignMessage(m); err != nil {
			return nil, err
		}
	}
	return sigs, nil
}

type approveAll struct{}

func (approveAll) Approve(context.Context, []preview.Card) (bool, error) { return true, nil }
func (approveAll) Failed(error)                                          {}
func (approveAll) Show()                                                 {}
