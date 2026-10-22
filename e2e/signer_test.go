package e2e

import (
	"os"
	"testing"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestSignerSeesCaller: every request the signer gets names the local
// process whose call led to it, so the person can tell when something other
// than Hermesh asks; and the root key itself came from the signer.
func TestSignerSeesCaller(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w := newOwnerMac(t, nil, &localv1.GrantSpec{Agents: []string{"gemini"}})
	exe, _ := os.Executable()
	w.app.mu.Lock()
	callers := append([]*localv1.Caller(nil), w.app.callers...)
	w.app.mu.Unlock()
	// Setup: the key request, then its signatures; then the grant.
	if len(callers) < 3 {
		t.Fatalf("signer saw %d requests", len(callers))
	}
	for i, c := range callers {
		if c == nil || int(c.Pid) != os.Getpid() || c.Executable != exe {
			t.Errorf("request %d: caller %+v, want pid %d %s", i, c, os.Getpid(), exe)
		}
	}
}
