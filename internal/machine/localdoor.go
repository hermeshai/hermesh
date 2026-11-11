package machine

import (
	"context"
	"os"

	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
)

// serveLocal takes the connections of this user's own agents on this
// computer (paths.Local), until ctx ends. Each is served as a tunnel the
// relay spliced is: the same end-to-end handshake, the same grant and the
// same checks, without the relay. An agent on the machine's own computer
// so reaches it by the shortest way, and also while the relay cannot be
// reached. Only this user can connect; what an agent may do is still what
// its grant says, as the machine knows it.
func (n *Machine) serveLocal(ctx context.Context) {
	path := paths.Local(n.machineID)
	if path == "" {
		return
	}
	ln, err := localipc.Listen(path)
	if err != nil {
		n.log.Warn("local endpoint", "err", err)
		return
	}
	go func() {
		<-ctx.Done()
		ln.Close()
		os.Remove(path)
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go n.serveTunnel(ctx, conn, true)
	}
}
