package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/service"
)

// Erasing (docs/erase-everything.md): `hermeshd erase` erases this
// computer's machine only; `hermeshd device erase-everything`, on a trusted
// device, erases Hermesh on all the owner's devices, as the apps' Erase
// Everything does.

// eraseCommand is `hermeshd erase`.
func eraseCommand(dataDir *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "erase",
		Short: "Erase Hermesh on this computer only: its machine, unpaired, with its projects list, sessions and the worktrees Hermesh made",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := machine.Config{DataDir: filepath.Join(*dataDir, "machine")}
			if _, err := os.Stat(machine.KeyPath(cfg.DataDir)); err != nil {
				if _, err := os.Stat(filepath.Join(*dataDir, "owner")); err == nil {
					return errors.New("this computer is a trusted device, not a machine: to erase Hermesh on all your devices run `hermeshd device erase-everything` (on a Mac: Settings › Account & Devices in the app)")
				}
				fmt.Println("Hermesh keeps no machine here: nothing to erase.")
				return nil
			}
			svc := service.InstalledFor(*dataDir)
			n, err := machine.Open(cfg)
			if err != nil && !svc {
				return fmt.Errorf("can't open this computer's machine (stop `hermeshd run` here first): %w", err)
			}
			what := "This computer's machine"
			if n != nil {
				if n.Paired() {
					what = fmt.Sprintf("This computer's machine “%s” (owner %s)", n.Name(), n.OwnerID())
				}
				n.Close()
			}
			fmt.Printf("%s is unpaired; its projects list, sessions and the worktrees Hermesh made are erased.\n", what)
			fmt.Println("Stays: your project directories and repositories (branches included), and worktrees with uncommitted changes.")
			if !yes {
				t, err := newTerminal()
				if err != nil {
					return fmt.Errorf("%w (or pass --yes)", err)
				}
				if a, err := t.line("Erase? [y/N] "); err != nil || !strings.EqualFold(a, "y") {
					return errors.New("not erased")
				}
			}
			// The service would run it again at once.
			if svc {
				if err := service.Uninstall(); err != nil {
					return fmt.Errorf("stopping the background service: %w", err)
				}
			}
			// Its owner's devices stop listing it, when the server can be reached.
			if n, err := machine.Open(cfg); err == nil {
				if n.Paired() {
					ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
					if err := n.LeaveOwner(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "warning: the server didn't remove it from its owner (%v); remove it there\n", err)
					}
					cancel()
				}
				n.Close()
			}
			kept, err := eraseHere(*dataDir, cfg)
			if err != nil {
				return err
			}
			fmt.Println("✓ Hermesh on this computer is erased. Pair it again with `hermeshd pair <connection address>`.")
			for _, wt := range kept {
				fmt.Printf("  Kept, with uncommitted changes: %s\n", wt)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask")
	return cmd
}

// eraseHere erases the machine in cfg.DataDir (stopped), keeping worktrees
// with uncommitted changes in dataDir/kept-worktrees, and returns them.
func eraseHere(dataDir string, cfg machine.Config) ([]string, error) {
	kept, err := machine.Erase(cfg, filepath.Join(dataDir, "kept-worktrees"))
	if err != nil {
		return kept, fmt.Errorf("erasing this machine: %w", err)
	}
	os.Remove(cfg.DataDir) // empty, unless a worktree could not be moved out
	return kept, nil
}

// eraseEverythingCommand is `hermeshd device erase-everything`.
func eraseEverythingCommand(dataDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "erase-everything",
		Short: "Erase Hermesh on all your trusted devices, machines and agents, this computer included (signed with this device's key)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			t, err := newTerminal()
			if err != nil {
				return err
			}
			c, err := ownerRole(ctx, *dataDir, true)
			if err != nil {
				return err
			}
			st, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
			if err != nil {
				return err
			}
			preview, err := c.EraseStatus(ctx, &localv1.EraseStatusRequest{})
			if err != nil {
				return err
			}
			host := st.RelayUrl
			if u, err := url.Parse(st.RelayUrl); err == nil && u.Host != "" {
				host = u.Host
			}
			expected, shown := host, host
			if st.Hosted && preview.Account != "" {
				expected = preview.Account
			}
			// The service by its site (hermesh.io), not where devices connect.
			if u, err := url.Parse(st.ConsoleUrl); err == nil && st.Hosted && u.Host != "" {
				shown = u.Host
			}
			printErasePreview(preview.Items, st.Hosted, shown)
			typed, err := t.line(fmt.Sprintf("\nThis can't be undone. Type %s to confirm: ", expected))
			if err != nil {
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(typed), expected) {
				return errors.New("not erased: that isn't " + expected)
			}
			s := startSigner(ctx, c, *dataDir, t)
			defer s.stop()
			res, err := c.EraseEverything(ctx, &localv1.EraseEverythingRequest{})
			if err != nil {
				return err
			}
			// Each one as it reports; those online have their moment, then this computer.
			fmt.Println("\nErasing… those online erase now; reset or pair the others again when you next use them.")
			said := map[int]bool{}
			for start := time.Now(); ; time.Sleep(time.Second) {
				r, err := c.EraseStatus(ctx, &localv1.EraseStatusRequest{EraseId: res.EraseId})
				if err != nil {
					continue
				}
				waiting := false
				for i, it := range r.Items {
					if it.Done && !said[i] {
						said[i] = true
						fmt.Printf("  ✓ %s\n", it.Name)
					}
					waiting = waiting || (it.Online && !it.Done && !it.ThisDevice)
				}
				if !waiting || time.Since(start) > 15*time.Second {
					break
				}
			}
			if _, err := c.FinishErase(ctx, &localv1.FinishEraseRequest{}); err != nil {
				return fmt.Errorf("this computer couldn't erase itself: %w", err)
			}
			fmt.Println("✓ Hermesh on this computer is erased.")
			if after, err := c.GetStatus(ctx, &localv1.GetStatusRequest{}); err == nil && after.Erased != nil {
				if p := after.Erased.Pending; len(p) > 0 {
					fmt.Printf("  Offline then: %s. Reset or pair each again when you next use it.\n", strings.Join(p, ", "))
				}
				for _, wt := range after.Erased.KeptWorktrees {
					fmt.Printf("  Kept, with uncommitted changes: %s\n", wt)
				}
			}
			return nil
		},
	}
}

// printErasePreview says what erasing everything reaches, by name, and
// what stays, as the apps' sheet does.
func printErasePreview(items []*localv1.EraseItem, hosted bool, host string) {
	group := func(kind, title, does, later string) {
		var names []string
		for _, it := range items {
			if it.Kind != kind {
				continue
			}
			switch {
			case it.ThisDevice:
				names = append(names, it.Name+" (this computer)")
			case it.Online:
				names = append(names, it.Name)
			default:
				names = append(names, it.Name+" ("+later+")")
			}
		}
		if len(names) > 0 {
			fmt.Printf("  %s: %s\n    %s\n", title, strings.Join(names, ", "), does)
		}
	}
	fmt.Println("Erase everything in Hermesh? Hermesh on all your devices goes back to how it was when first installed.")
	fmt.Println("\nErases:")
	group("device", "Trusted devices", "Signed out; their owner keys, sessions and settings erased", "offline: offers a reset when next opened")
	group("machine", "Machines", "Unpaired; their projects lists, sessions and the worktrees Hermesh made erased", "offline: to pair again when next online")
	group("agent", "Agents", "Disconnected; their pairing on their own computers erased", "signs in again when next used")
	if hosted {
		fmt.Printf("  Data on %s\n    Messages, new-message alerts and connection approvals; every browser signed out\n", host)
	} else {
		fmt.Printf("  Your server · %s\n    Back to unclaimed, with a new claim code\n", host)
	}
	stays := []string{"Your project directories and repositories, branches included", "Worktrees with uncommitted changes: moved aside and kept, and you're told where"}
	if hosted {
		stays = append(stays, "Your account: sign in with it again for a fresh start")
	}
	stays = append(stays, "What offline devices hold, until each is reset")
	fmt.Println("Stays:")
	for _, s := range stays {
		fmt.Printf("  %s\n", s)
	}
}
