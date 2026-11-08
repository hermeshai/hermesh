package herhost

import (
	"context"
	"os"
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
