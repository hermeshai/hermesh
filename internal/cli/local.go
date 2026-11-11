package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/peer"
)

// An agent on a machine's own computer (Claude Code using this CLI on the
// Mac that also runs hermeshd) reaches that machine over its endpoint for
// this user's agents (paths.Local, machine.serveLocal), not through the
// relay: the shortest way, and one that works while the relay cannot be
// reached. The machine serves it as it serves a tunnel: the same handshake
// and the same grant, as the machine knows it.

// localDial connects to machineID on this computer (peer.Dialer.Local).
func localDial(ctx context.Context, machineID string) (net.Conn, error) {
	path := paths.Local(machineID)
	if path == "" {
		return nil, errors.New("machines of this computer are reached through the relay")
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return localipc.Dial(ctx, path)
}

// withoutRelay is what a command works with while the relay cannot be
// reached: the machines as the relay last listed them, those of this
// computer online and the others not. Nil when none is here.
func (a *app) withoutRelay() (*peer.Client, []machineRef) {
	var machines []machineRef
	here := false
	for _, m := range a.st.Machines {
		ref := machineRef{ID: m.ID, Name: m.Name}
		if path := paths.Local(m.ID); path != "" {
			if _, err := os.Stat(path); err == nil {
				ref.Online, here = true, true
			}
		}
		machines = append(machines, ref)
	}
	if !here {
		return nil, nil
	}
	return a.dialer(true).Detached(), machines
}
