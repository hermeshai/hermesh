// Package codextest keeps tests that run the real Codex out of the user's
// own Codex state: their threads and trusted projects would otherwise show
// up in the user's Codex apps.
package codextest

import (
	"os"
	"path/filepath"
)

// IsolateHome points CODEX_HOME at a fresh directory that links the user's
// login (auth.json) and has an empty config. Codex rewrites auth.json in
// place, so a refreshed login still reaches the user's file. The returned
// function removes the directory.
func IsolateHome() (func(), error) {
	user := os.Getenv("CODEX_HOME")
	if user == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		user = filepath.Join(h, ".codex")
	}
	dir, err := os.MkdirTemp("", "hermesh-codex-home-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	if err := os.Symlink(filepath.Join(user, "auth.json"), filepath.Join(dir, "auth.json")); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), nil, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Setenv("CODEX_HOME", dir); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
