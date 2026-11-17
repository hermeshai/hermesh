package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/hermeshai/hermesh/pkg/identity"
)

// fileKeys are root keys in files of a folder, one per key id: on Linux the
// protected signer's, in a folder only root reads (docs/linux.md §7.3); in
// test builds, a folder of the test's.
type fileKeys struct{ dir string }

// keyIDs are what identity.KeyID makes: a request naming anything else
// (a path, as a program posing as hermeshd could) names no key here.
var keyIDs = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// file is the key keyID's file, or an error for a name that is no key id.
func (k fileKeys) file(keyID string) (string, error) {
	if !keyIDs.MatchString(keyID) {
		return "", errors.New("no such root key")
	}
	return filepath.Join(k.dir, keyID+".pem"), nil
}

func (k fileKeys) Create(context.Context) (*ecdsa.PublicKey, error) {
	key, err := identity.GenerateKey()
	if err != nil {
		return nil, err
	}
	return key.Public(), identity.SaveKey(filepath.Join(k.dir, identity.KeyID(key.Public())+".pem"), key)
}

func (k fileKeys) Delete(keyID string) error {
	f, err := k.file(keyID)
	if err != nil {
		return err
	}
	return os.Remove(f)
}

func (k fileKeys) Sign(_ context.Context, keyID string, messages [][]byte) ([][]byte, error) {
	f, err := k.file(keyID)
	if err != nil {
		return nil, err
	}
	key, err := identity.LoadKey(f)
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
