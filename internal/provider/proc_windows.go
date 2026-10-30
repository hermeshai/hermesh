package provider

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/hermeshai/hermesh/internal/spawn"
)

// Each agent gets a job object of its own in phase W1 (docs/windows.md
// §4.3); until then agents cannot start on Windows.

func setGroup(cmd *exec.Cmd) {}

func joinGroup(cmd *exec.Cmd) error {
	return fmt.Errorf("starting %s: %w on Windows yet", cmd.Path, errors.ErrUnsupported)
}

func killGroup(pgid int) error { return errors.ErrUnsupported }

// KillGroup ends a process tree this daemon started.
func KillGroup(pgid int) {}

// There are no spawn helpers on Windows.

func startSpawn(bin string, args []string, dir string, env []string) (*Proc, error) {
	return nil, errors.ErrUnsupported
}

func runSpawned(ctx context.Context, hs []*spawn.Helper, bin string, args []string, dir string, env []string) (stdout, stderr []byte, err error) {
	return nil, nil, errors.ErrUnsupported
}
