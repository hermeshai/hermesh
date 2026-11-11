package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hermeshai/hermesh/internal/machine"
)

// screenCommand is Computer Control on this machine (docs/computer-control.md,
// docs/computer-control-linux.md): whether the owner's devices can see and
// operate its screen, and the switches a person at the machine has.
func screenCommand(dataDir *string, open func() (*machine.Machine, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "screen", Short: "Computer Control: whether your devices can see and operate this screen",
		Long: `Your trusted devices can see and operate this machine's screen (Computer
Control in the iPhone app's Her settings). On Linux it works on X11, GNOME,
Plasma and wlroots desktops (Sway, Hyprland and their kind).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := machineCheck(cmd.Context(), *dataDir, open)
			if err != nil {
				return err
			}
			if c.Screen == nil {
				fmt.Println("Computer Control is not installed on this machine")
				return nil
			}
			fmt.Println(screenLine(c.Screen))
			return nil
		}}
	cmd.AddCommand(
		&cobra.Command{Use: "stop", Short: "End the session under way", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, ok, err := machine.ControlRequest(cmd.Context(), filepath.Join(*dataDir, "machine"), "POST", "/screen/stop", nil)
				if !ok {
					return errors.New("hermeshd is not running")
				}
				return err
			}},
		&cobra.Command{Use: "off", Short: "Refuse every device's request to see this screen, until `hermeshd screen on`", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				f := machine.ScreenOffPath(filepath.Join(*dataDir, "machine"))
				if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(f, nil, 0o600); err != nil {
					return err
				}
				machine.ControlRequest(cmd.Context(), filepath.Join(*dataDir, "machine"), "POST", "/screen/stop", nil)
				fmt.Println("Computer Control is off on this machine")
				return nil
			}},
		&cobra.Command{Use: "on", Short: "Let your devices see and operate this screen again", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := os.Remove(machine.ScreenOffPath(filepath.Join(*dataDir, "machine"))); err != nil && !os.IsNotExist(err) {
					return err
				}
				fmt.Println("Computer Control is on on this machine")
				return nil
			}},
		&cobra.Command{Use: "allow", Short: "On Plasma: let Hermesh show this screen without asking each time", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				bin := screenBin()
				if bin == "" {
					return errors.New("Computer Control is not installed on this machine")
				}
				c := exec.CommandContext(cmd.Context(), bin, "--allow")
				c.Stdout, c.Stderr = os.Stdout, os.Stderr
				return c.Run()
			}},
	)
	return cmd
}

// screenLine says how Computer Control is on this machine, in a line.
func screenLine(s *machine.ScreenCheck) string {
	switch {
	case s.Off:
		return "off (hermeshd screen on turns it on)"
	case s.Viewer != "":
		return s.Viewer + " is in control (hermeshd screen stop ends it)"
	case s.Desktop.Error == "locked":
		return "the screen is locked: unlock it at the computer"
	case s.Desktop.Available:
		way := map[string]string{"x11": "X11", "gnome": "GNOME", "portal": "the desktop portal", "wlroots": "wlroots", "test": "a test picture", "darwin": "macOS"}[s.Desktop.Way]
		if way == "" {
			way = "the system"
		}
		return "ready, through " + way
	case s.Desktop.Detail != "":
		return "not available: " + s.Desktop.Detail
	}
	return "not available"
}
