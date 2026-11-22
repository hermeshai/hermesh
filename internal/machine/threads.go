package machine

import (
	"context"
	"os"
	"time"

	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/kernel"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	rtacp "github.com/hermeshai/hermesh/internal/runtime/acp"
	rtclaude "github.com/hermeshai/hermesh/internal/runtime/claude"
	rtcodex "github.com/hermeshai/hermesh/internal/runtime/codex"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
)

func buildRuntimeDrivers(cfg Config) (map[string]rt.Driver, bool) {
	if os.Getenv("HERMESH_FAKE_PROVIDERS") == "1" {
		// Distinct scripted driver instances per family so tests can exercise
		// both native same-provider resume and cross-provider ContextHandoff
		// deterministically without live API keys.
		return map[string]rt.Driver{
			"scripted": scripted.NewDriver(),
			"claude":   scripted.NewDriver(),
			"codex":    scripted.NewDriver(),
			"acp":      &scripted.Driver{DisableSteer: true},
		}, true
	}
	cl := rtclaude.NewDriver("")
	if b := os.Getenv("HERMESH_CLAUDE_BIN"); b != "" {
		cl.Binary = b
	}
	cx := rtcodex.NewDriver("")
	if b := os.Getenv("HERMESH_CODEX_BIN"); b != "" {
		cx.Binary = b
	}
	drivers := map[string]rt.Driver{
		"claude": cl,
		"codex":  cx,
		"acp":    rtacp.NewDriver(""),
	}
	includeScripted := os.Getenv("HERMESH_INCLUDE_SCRIPTED") == "1"
	if includeScripted {
		drivers["scripted"] = scripted.NewDriver()
	}
	_ = cfg
	return drivers, includeScripted
}

func (n *Machine) openThreads() error {
	drivers, includeScripted := buildRuntimeDrivers(n.cfg)
	idleTTL := n.cfg.Limits.IdleProcessTTL
	if idleTTL <= 0 {
		idleTTL = 10 * time.Minute
	}
	eng, err := kernel.Open(kernel.Config{
		DataDir:         n.cfg.DataDir,
		MachineID:       n.machineID,
		Logger:          n.log,
		Drivers:         drivers,
		IncludeScripted: includeScripted,
		IdleTTL:         idleTTL,
		MachineMaxMode:  n.maxRuntimeMode(),
		IsPaused:        n.isPaused,
		OnNotice: func(_ *threadv2.Thread, _ *threadv2.ThreadEvent) {
			n.kickStatus()
		},
	})
	if err != nil {
		return err
	}
	if projects, pErr := n.st.Projects(); pErr == nil {
		for _, pr := range projects {
			_ = eng.SyncWorkspace(pr.ID, pr.Path, pr.DefaultBaseRef, pr.Workspace, pr.Description, nil) // clears a limit kept from before D5
		}
	}
	n.threads = eng
	return nil
}

func (n *Machine) Threads() *kernel.Engine { return n.threads }

func (n *Machine) threadPrincipal(ctx context.Context) *kernel.Principal {
	p := principalFrom(ctx)
	if p == nil {
		return nil
	}
	if p.Kind == PrincipalOwner {
		return kernel.OwnerPrincipal("owner", "Owner")
	}
	if p.Grant != nil {
		return kernel.GrantPrincipal(p.Grant)
	}
	return nil
}

func (l *Local) Threads() *kernel.Engine { return l.n.threads }
