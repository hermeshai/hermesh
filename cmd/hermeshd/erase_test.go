//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/provider"
)

// herWasOn makes the machine in dataDir one where Her was on and is off
// now, her data kept in a home of the test's: never the user's ~/.her, nor
// the Keychain.
func herWasOn(t *testing.T, dataDir string) (home string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "her")
	t.Setenv("HER_HOME", home)
	t.Setenv("HER_CREDENTIAL_STORE", "file")
	if err := os.MkdirAll(filepath.Join(home, "bots", "b1"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := machine.Config{DataDir: filepath.Join(dataDir, "machine"), Providers: map[string]provider.Provider{}}
	n, err := machine.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.Close()
	st, err := machine.OpenStore(filepath.Join(cfg.DataDir, "hermeshd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Set("her", []byte("0")); err != nil { // as SetHer off leaves it
		t.Fatal(err)
	}
	return home
}

// `hermeshd uninstall` and `hermeshd erase`, where Her was on and her data
// is kept, ask whether it goes too, after their own question; --erase-her
// deletes it without asking; --yes alone keeps it. Kept, they say where.
func TestUninstallAndEraseAskAboutHerData(t *testing.T) {
	for _, name := range []string{"uninstall", "erase"} {
		for _, tc := range []struct {
			flags    string
			answer   string // to the question about Her's data; "-": not asked
			eraseHer bool
			yes      bool
			deleted  bool
		}{
			{flags: "", answer: "y", deleted: true},
			{flags: "", answer: ""},
			{flags: "", answer: "n"},
			{flags: "--erase-her", answer: "-", eraseHer: true, deleted: true},
			{flags: "--yes --erase-her", answer: "-", eraseHer: true, yes: true, deleted: true},
			{flags: "--yes", answer: "-", yes: true},
		} {
			t.Run(fmt.Sprintf("%s %s answering %q", name, tc.flags, tc.answer), func(t *testing.T) {
				data, real, _, bin := fakeInstall(t)
				home := herWasOn(t, data)
				var out bytes.Buffer
				var asked []string
				ask := func(p string) (string, error) {
					asked = append(asked, p)
					fmt.Fprint(&out, p)
					if strings.HasPrefix(p, "Also delete Her's data") {
						return tc.answer, nil
					}
					return "y", nil
				}
				if tc.yes {
					ask = nil
				}
				run := func() error {
					if name == "uninstall" {
						return uninstall(context.Background(), &out, ask, data, bin, tc.eraseHer)
					}
					return erase(context.Background(), &out, ask, data, tc.eraseHer)
				}
				if err := run(); err != nil {
					t.Fatalf("%v\n%s", err, out.String())
				}
				herQuestion := "Also delete Her's data in " + home + "? [y/N] "
				if got := slices.Contains(asked, herQuestion); got != (tc.answer != "-") {
					t.Errorf("asked %q", asked)
				}
				if len(asked) == 2 && asked[1] != herQuestion {
					t.Errorf("asked about Her's data before the command's own question: %q", asked)
				}
				_, err := os.Stat(home)
				if deleted := os.IsNotExist(err); deleted != tc.deleted {
					t.Errorf("her home deleted %v (%v), want %v\n%s", deleted, err, tc.deleted, out.String())
				}
				stays := "Stays too: Her's assistants, conversations and settings, in " + home + "; delete it yourself to remove them."
				if said := strings.Contains(out.String(), stays); said == tc.deleted {
					t.Errorf("says it stays: %v\n%s", said, out.String())
				}
				if exists(filepath.Join(real, "machine", "hermeshd.db")) {
					t.Error("the machine is still there")
				}
			})
		}
	}
}
