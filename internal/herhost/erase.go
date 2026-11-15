package herhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// mark is the file her leaves in a home she makes (her-core paths.rs, MARK).
const mark = ".her-home"

// Erase deletes her data (docs/her-server.md §5.6): her home, and on a Mac
// her Keychain items, unless HER_CREDENTIAL_STORE kept her off the Keychain.
// Her must be off. A home is deleted whole, so only one that is hers alone:
// ~/.her, or one her made; else nothing is erased.
func (h *Host) Erase() error {
	h.op.Lock()
	defer h.op.Unlock()
	h.mu.Lock()
	on := h.stop != nil
	h.mu.Unlock()
	if on {
		return errors.New("her data can't be erased while Her runs")
	}
	env := h.cfg.Env()
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home := herHome(env, userHome)
	if !hersAlone(home, userHome) {
		return fmt.Errorf("her home %s was not made by her and may hold other files: not erased; delete her files there yourself", home)
	}
	if runtime.GOOS == "darwin" && !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "HER_CREDENTIAL_STORE=") }) {
		// her-llm's credentials: model keys, ChatGPT's tokens, connector secrets.
		if err := deleteKeychainItems("her"); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("erasing her home: %w", err)
	}
	h.log.Info("Her's data erased", "home", home)
	return nil
}

// hersAlone reports whether erasing may delete all of home: ~/.her, a home
// her made, or none at all.
func hersAlone(home, userHome string) bool {
	if filepath.Clean(home) == filepath.Join(userHome, ".her") {
		return true
	}
	if _, err := os.Lstat(home); errors.Is(err, os.ErrNotExist) {
		return true
	}
	_, err := os.Lstat(filepath.Join(home, mark))
	return err == nil
}

// Home is her home, as her environment names it (HER_HOME, else ~/.her).
func (h *Host) Home() string {
	userHome, _ := os.UserHomeDir()
	return herHome(h.cfg.Env(), userHome)
}

// deleteKeychainItems deletes every generic password of service; a variable
// for tests, which must never touch the Keychain.
var deleteKeychainItems = func(service string) error {
	// One item per call; security fails once none is left (exit 44).
	for range 10000 {
		err := exec.Command("/usr/bin/security", "delete-generic-password", "-s", service).Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("erasing her Keychain items (%s): %w", service, err)
		}
	}
	return fmt.Errorf("erasing her Keychain items (%s): too many", service)
}
