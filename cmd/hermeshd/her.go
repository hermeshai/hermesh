package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/hermeshai/hermesh/internal/herhost"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/paths"
)

// herCommand is Her's conversation in this terminal (docs/linux.md §8): on
// a server with no app, the person talks to Her where she runs.
func herCommand(dataDir *string) *cobra.Command {
	return &cobra.Command{Use: "her", Short: "Talk to Her here, in this terminal (Her on for this machine)", Args: cobra.NoArgs,
		Long: `Talk to Her in this terminal: the Her this machine runs once Her is on for it
in the Hermesh app, with her conversation, memory and routines, the same one
your other devices talk to. What she sends shows as it comes; when she asks
before doing something, answer y or n. Ctrl+D ends.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			sock := herSocket(*dataDir)
			if sock == "" {
				return fmt.Errorf("Her is not running here: turn Her on for this machine in the Hermesh app (hermeshd status says whether she is on)")
			}
			return herhost.Talk(ctx, sock, machine.Version, os.Stdin, os.Stdout)
		}}
}

// herSocket is where the her this user's hermeshd runs answers: a machine's
// (the install command's), or the app's own machine in its current profile;
// "" when none answers.
func herSocket(dataDir string) string {
	candidates := []string{paths.Her(filepath.Join(dataDir, "machine"))}
	profiles, _ := filepath.Glob(filepath.Join(dataDir, "owner", "profiles", "*", "machine"))
	for _, p := range profiles {
		candidates = append(candidates, paths.Her(p))
	}
	for _, sock := range candidates {
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "unix", sock)
		if err == nil {
			c.Close()
			return sock
		}
	}
	return ""
}
