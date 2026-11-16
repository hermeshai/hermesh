package herhost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

// stopStale has nothing to stop: her is in a job of this daemon's
// (docs/windows.md §4.3), so no her outlives the hermeshd that started it.
func stopStale(lock string) {}

// end stops her and what it started. Windows has no signal that asks: her
// is asked on its endpoint, where it closes its sessions and saves their
// logs, and its job ends what is left a moment later.
func (h *Host) end(p *provider.Proc) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if c, err := dial(ctx, h.cfg.Socket, h.cfg.Version, false); err == nil {
		c.call(ctx, "server/shutdown", json.RawMessage("{}"))
		c.Close()
	}
	cancel()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
	}
	provider.KillGroup(p.Pgid())
}
