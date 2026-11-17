//go:build testsigner

package main

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/hermeshai/hermesh/internal/signer"
)

// serve --test <dir> keeps each user's software root keys in dir/<uid>,
// approves everything and runs as any user, with polkit taken as there.
func init() {
	protected := parts
	parts = func(test string) (keysFor, ownerFor, error) {
		if test == "" {
			return protected(test)
		}
		polkitHere = func() bool { return true }
		keys := func(uid int) fileKeys { return fileKeys{dir: filepath.Join(test, strconv.Itoa(uid))} }
		return keys, func(int, int, string) signer.Owner { return approveAll{} }, os.MkdirAll(test, 0o700)
	}
}
