//go:build testsigner

package main

import (
	"os"

	"github.com/hermeshai/hermesh/internal/signer"
)

// run --test <dir> keeps software root keys in dir and approves everything.
func init() {
	tpm := parts
	parts = func(sid, test string) (signer.Keys, signer.Owner, error) {
		if test == "" {
			return tpm(sid, test)
		}
		return fileKeys{dir: test}, approveAll{}, os.MkdirAll(test, 0o700)
	}
}
