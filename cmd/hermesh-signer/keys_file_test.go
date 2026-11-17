package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hermeshai/hermesh/pkg/identity"
)

// A root key is made, signs and goes; a key id that is a path names no
// file, so nothing outside the folder is read or removed.
func TestFileKeys(t *testing.T) {
	dir := t.TempDir()
	k := fileKeys{dir: filepath.Join(dir, "keys")}
	pub, err := k.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id := identity.KeyID(pub)
	if !keyIDs.MatchString(id) {
		t.Fatalf("a key id the signer would refuse: %q", id)
	}
	sigs, err := k.Sign(context.Background(), id, [][]byte{[]byte("m")})
	if err != nil || identity.VerifyRaw(pub, []byte("m"), sigs[0]) != nil {
		t.Fatalf("signing: %v", err)
	}
	outside := filepath.Join(dir, "outside.pem")
	os.WriteFile(outside, []byte("x"), 0o600)
	for _, bad := range []string{"../outside", "/etc/passwd", "", "a/b"} {
		if k.Delete(bad) == nil {
			t.Errorf("Delete(%q) did something", bad)
		}
		if _, err := k.Sign(context.Background(), bad, [][]byte{[]byte("m")}); err == nil {
			t.Errorf("Sign(%q) signed", bad)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a file outside the folder went")
	}
	if err := k.Delete(id); err != nil {
		t.Fatal(err)
	}
}
