//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInstall lays out what the install script leaves for a user, with
// the data directory linked to another disk, as on a Synology volume. It
// returns the data entry, where it links, the CLI home and hermeshd.
func fakeInstall(t *testing.T) (data, real, cli, bin string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HERMESH_HOME", "")
	real = filepath.Join(t.TempDir(), "@hermesh-1000")
	data = filepath.Join(home, ".local", "share", "hermesh")
	cli = filepath.Join(home, ".config", "hermesh")
	bin = filepath.Join(home, ".local", "bin", "hermeshd")
	for _, d := range []string{filepath.Join(real, "logs"), filepath.Dir(data), cli, filepath.Dir(bin)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.Symlink(real, data)
	real, _ = filepath.EvalSymlinks(real) // macOS: /var is /private/var
	for _, f := range []string{filepath.Join(real, "logs", "hermeshd.log"), filepath.Join(cli, "client.key"), bin, filepath.Join(filepath.Dir(bin), "hermesh")} {
		os.WriteFile(f, []byte("x"), 0o600)
	}
	return data, real, cli, bin
}

// her and its console go with the programs; Her's data stays, and it says
// so.
func TestUninstallRemovesHerAndKeepsHerData(t *testing.T) {
	data, _, _, bin := fakeInstall(t)
	her, console, herHome := filepath.Join(filepath.Dir(bin), "her"), filepath.Join(filepath.Dir(bin), "her-console"), filepath.Join(os.Getenv("HOME"), ".her")
	os.WriteFile(her, []byte("x"), 0o755)
	os.MkdirAll(filepath.Join(console, "assets"), 0o755)
	os.WriteFile(filepath.Join(console, "index.html"), []byte("x"), 0o644)
	os.MkdirAll(herHome, 0o700)
	os.WriteFile(filepath.Join(herHome, "config.toml"), []byte("x"), 0o600)
	var out bytes.Buffer
	if err := uninstall(context.Background(), &out, nil, data, bin, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{bin + ", " + her + ", " + console, "Her's assistants, conversations and settings, in " + herHome} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if exists(her) || exists(console) {
		t.Error("her or its console is still there")
	}
	if !exists(filepath.Join(herHome, "config.toml")) {
		t.Error("Her's data removed")
	}
}

func TestUninstallRemovesEverythingAfterAsking(t *testing.T) {
	data, real, cli, bin := fakeInstall(t)
	var asked string
	var out bytes.Buffer
	err := uninstall(context.Background(), &out, func(p string) (string, error) { asked = p; return "y", nil }, data, bin, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "Uninstall? [y/N]") {
		t.Errorf("asked %q", asked)
	}
	for _, want := range []string{"its data: " + data + " → " + real, "CLI key and settings: " + cli, filepath.Join(filepath.Dir(bin), "hermesh") + ", " + bin, "✓ Hermesh is uninstalled"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	for _, p := range []string{data, real, cli, bin, filepath.Join(filepath.Dir(bin), "hermesh")} {
		if exists(p) {
			t.Errorf("%s is still there", p)
		}
	}
}

func TestUninstallRemovesNothingUnlessYes(t *testing.T) {
	data, real, cli, bin := fakeInstall(t)
	err := uninstall(context.Background(), &bytes.Buffer{}, func(string) (string, error) { return "", nil }, data, bin, false)
	if err == nil || err.Error() != "not uninstalled" {
		t.Fatalf("Enter: %v", err)
	}
	for _, p := range []string{filepath.Join(real, "logs", "hermeshd.log"), cli, bin} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
}

func TestUninstallRefuses(t *testing.T) {
	t.Run("data directory holding the home folder", func(t *testing.T) {
		never := func(string) (string, error) { t.Fatal("asked"); return "", nil }
		_, _, _, bin := fakeInstall(t)
		home, _ := os.UserHomeDir()
		if err := uninstall(context.Background(), &bytes.Buffer{}, never, home, bin, false); err == nil || !strings.Contains(err.Error(), "not removing it") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("worktrees kept by an earlier erase", func(t *testing.T) {
		never := func(string) (string, error) { t.Fatal("asked"); return "", nil }
		data, real, _, bin := fakeInstall(t)
		os.MkdirAll(filepath.Join(real, "kept-worktrees", "s_1"), 0o700)
		if err := uninstall(context.Background(), &bytes.Buffer{}, never, data, bin, false); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
			t.Fatalf("got %v", err)
		}
		if !exists(filepath.Join(real, "kept-worktrees", "s_1")) {
			t.Error("the kept worktree was removed")
		}
	})
}
