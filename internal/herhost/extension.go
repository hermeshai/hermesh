package herhost

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

// The owner's own Chrome (her's `her browser extension`, in
// her/crates/her-cli/src/cmd_browser.rs): while Her is on, the Chromium
// browsers installed here start this her as the Hermesh extension's native
// host, for this her's home and socket. Registered again at every Start, so
// the browsers follow her when an update moved it; taken back by Off.

// extensionWait bounds one `her browser extension` command.
const extensionWait = 30 * time.Second

// extension runs `her browser extension <args>` with her's environment, and
// logs what her said it did.
func (h *Host) extension(ctx context.Context, args ...string) {
	if h.cfg.Bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, extensionWait)
	defer cancel()
	home, _ := os.UserHomeDir()
	out, errOut, err := provider.RunCommand(ctx, h.cfg.Bin, append([]string{"browser", "extension"}, args...), home, h.cfg.Env())
	said := strings.TrimSpace(string(out) + string(errOut))
	if err != nil {
		h.log.Info("her browser extension "+args[0]+" failed", "err", err, "output", said)
		return
	}
	h.log.Info("her browser extension "+args[0], "output", said)
}

// ExtensionFolder is where the console's guide has the owner load the
// Hermesh extension from (her/apps/console/src/yours/YourBrowser.tsx): a
// copy of the one the app ships beside her, in Contents/Resources, kept the
// same at every Start, so an update reaches Chrome once it reloads the
// extension. The extension's manifest carries its key, so its ID doesn't
// depend on the folder.
func ExtensionFolder(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Hermesh", "Chrome Extension")
}

// placeExtension copies the app's extension to ExtensionFolder, unless it is
// there already, as it is.
func (h *Host) placeExtension() {
	src := filepath.Join(filepath.Dir(h.cfg.Bin), "..", "Resources", "chrome-extension")
	if _, err := os.Stat(filepath.Join(src, "manifest.json")); err != nil {
		return
	}
	home, err := os.UserHomeDir()
	if err == nil {
		err = syncFolder(src, ExtensionFolder(home))
	}
	if err != nil {
		h.log.Info("placing the Hermesh extension failed", "err", err)
	}
}

// syncFolder makes dst a copy of src, unless it is one already: written
// beside it first, so Chrome never finds half of it.
func syncFolder(src, dst string) error {
	if sameFolder(src, dst) {
		return nil
	}
	tmp := dst + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.CopyFS(tmp, os.DirFS(src)); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// sameFolder: whether a and b hold the same files, with the same contents.
func sameFolder(a, b string) bool {
	files := func(root string) map[string][]byte {
		got := map[string][]byte{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			got[rel], err = os.ReadFile(p)
			return err
		})
		if err != nil {
			return nil
		}
		return got
	}
	x, y := files(a), files(b)
	if x == nil || y == nil || len(x) != len(y) {
		return false
	}
	for name, data := range x {
		if other, ok := y[name]; !ok || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}
