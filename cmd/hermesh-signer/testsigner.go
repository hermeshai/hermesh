//go:build testsigner

package main

import (
	"context"
	"fmt"

	"github.com/hermeshai/hermesh/internal/preview"
)

// Test builds (-tags testsigner): the signer keeps software root keys in a
// folder of the test's and approves everything, for end-to-end tests on
// machines with neither a TPM nor a person (testsigner_windows.go,
// testsigner_linux.go).

type approveAll struct{}

func (approveAll) Approve(context.Context, []preview.Card) (bool, error) { return true, nil }
func (approveAll) Failed(error)                                          {}
func (approveAll) Show()                                                 {}

// Matched prints the digits the panel would show, for the test to type.
func (approveAll) Matched(m preview.Match) {
	fmt.Printf("HERMESH_SIGNER_MATCH=%s %s\n", m.Digits, m.Label)
}
