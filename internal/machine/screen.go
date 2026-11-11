package machine

import (
	"context"
	"os"
	"path/filepath"
	"time"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	screenhost "github.com/hermeshai/hermesh/internal/screen"
)

// ScreenOffPath is the file that turns Computer Control off on the machine
// whose data directory is dataDir (`hermeshd screen off`).
func ScreenOffPath(dataDir string) string { return filepath.Join(dataDir, "screen-off") }

// ScreenCheck is Computer Control on this machine, for `hermeshd status`.
type ScreenCheck struct {
	Off     bool
	Desktop screenhost.Desktop
	Viewer  string
}

func (n *Machine) screenCheck() *ScreenCheck {
	if n.cfg.ScreenBin == "" {
		return nil
	}
	_, err := os.Stat(ScreenOffPath(n.cfg.DataDir))
	return &ScreenCheck{Off: err == nil, Desktop: n.screen.Desktop(), Viewer: n.screen.Viewer()}
}

// Computer Control on this machine (docs/computer-control.md): ScreenService
// on the owner's tunnels, answered by internal/screen.

type screenService struct{ n *Machine }

func (s *screenService) Connect(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
	res, err := s.n.screen.Connect(ctx, req)
	if req.SessionId == "" { // a new session, not a live one finding another path
		rec := AuditRecord{Kind: "screen", Subject: principalFrom(ctx).ID(), Action: "connect", Decision: "allow", Detail: req.Viewer}
		if err != nil {
			rec.Decision, rec.Reason = "error", err.Error()
		}
		s.n.audit.Log(rec)
	}
	return res, err
}

// screenServers are the relay's STUN and TURN servers for a session that
// starts; none when it runs none, is an older relay, or does not answer in
// time: the two ends then find what paths they can by themselves.
func (n *Machine) screenServers(ctx context.Context) []*relayv1.IceServer {
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		return nil
	}
	c, err := n.control(conn)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := c.GetScreenServers(ctx, &relayv1.GetScreenServersRequest{})
	if err != nil {
		return nil
	}
	return res.Servers
}

// screenStatus is nil on a machine that can't show its screen.
func (n *Machine) screenStatus() *machinev1.ScreenStatus {
	if !n.screen.Available() {
		return nil
	}
	return &machinev1.ScreenStatus{Available: true, Viewer: n.screen.Viewer()}
}
