package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/service"
)

// uninstallCommand is `hermeshd uninstall` (docs/hermeshd.md): it removes
// what the install script put here for this user, after one question.
func uninstallCommand(dataDir *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Hermesh for this user on this computer: its machine, unpaired, the background service, its data and its programs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS == "windows" {
				return errors.New("on Windows, uninstall Hermesh in Settings › Apps; after an install.ps1 install, run `hermeshd erase`, then delete its programs' folder and data folder")
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			if b, err := filepath.EvalSymlinks(bin); err == nil {
				bin = b
			}
			if strings.Contains(bin, ".app/Contents/") {
				return errors.New("this hermeshd is Hermesh.app's: erase Hermesh in the app (Settings › Account & Devices), then move the app to the Trash")
			}
			ask := func(prompt string) (string, error) {
				t, err := newTerminal()
				if err != nil {
					return "", fmt.Errorf("%w (or pass --yes)", err)
				}
				return t.line(prompt)
			}
			if yes {
				ask = nil
			}
			return uninstall(cmd.Context(), os.Stdout, ask, *dataDir, bin)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask")
	return cmd
}

// uninstall removes Hermesh for this user: the machine in dataDir,
// unpaired, the service, dataDir (and where it links to), the CLI's home
// and the programs, hermeshd at bin and hermesh, her and her-console/
// beside it. Her's own data, ~/.her, stays. ask, when not nil, asks first.
func uninstall(ctx context.Context, w io.Writer, ask func(string) (string, error), dataDir, bin string) error {
	home, _ := os.UserHomeDir()
	realHome := home
	if r, err := filepath.EvalSymlinks(home); err == nil {
		realHome = r
	}
	realData := dataDir
	if r, err := filepath.EvalSymlinks(dataDir); err == nil {
		realData = r
	}
	if inside(realHome, realData) || inside(bin, realData) {
		return fmt.Errorf("the data directory %s holds more than Hermesh's data; not removing it", dataDir)
	}
	keptBefore := filepath.Join(dataDir, "kept-worktrees")
	if e, _ := os.ReadDir(keptBefore); len(e) > 0 {
		return fmt.Errorf("%s holds worktrees with uncommitted changes, kept when Hermesh was erased before: move what you want out of it (git worktree move), then uninstall", keptBefore)
	}
	cfg := machine.Config{DataDir: filepath.Join(dataDir, "machine")}
	isMachine := exists(machine.KeyPath(cfg.DataDir))
	if !isMachine && exists(filepath.Join(dataDir, "owner")) {
		if err := checkLeftAsDevice(ctx, dataDir); err != nil {
			return err
		}
	}
	svc := service.InstalledFor(dataDir)
	machineLine := "its machine: unpaired, and gone from your devices"
	if isMachine {
		c, running, err := machineCheck(ctx, dataDir, func() (*machine.Machine, error) { return machine.Open(cfg) })
		if (running || err != nil) && !svc {
			return errors.New("hermeshd runs here outside its service: stop `hermeshd run`, then uninstall")
		}
		if c.Paired {
			machineLine = fmt.Sprintf("its machine “%s” (owner %s): unpaired, and gone from your devices", c.Name, c.OwnerID)
		}
		if c.Busy > 0 {
			machineLine += fmt.Sprintf("; the %d sessions running now stop", c.Busy)
		}
	}
	cliHome := paths.CLIHome()
	programs := []string{bin}
	if p := filepath.Join(filepath.Dir(bin), "hermesh"); exists(p) {
		programs = append([]string{p}, programs...)
	}
	for _, name := range []string{"her", "her-console"} {
		if p := filepath.Join(filepath.Dir(bin), name); exists(p) {
			programs = append(programs, p)
		}
	}
	herHome := filepath.Join(home, ".her")
	keepDir := filepath.Join(home, "hermesh-kept-worktrees")

	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	fmt.Fprintf(w, "This removes Hermesh for %s on this computer:\n", name)
	if isMachine {
		fmt.Fprintf(w, "  • %s\n", machineLine)
	}
	if svc {
		fmt.Fprintln(w, "  • the background service")
	}
	if exists(dataDir) {
		where := dataDir
		if realData != dataDir {
			where += " → " + realData
		}
		fmt.Fprintf(w, "  • its data: %s (sessions, logs, the worktrees Hermesh made)\n", where)
	} else if os.Getuid() == 0 {
		fmt.Fprintln(w, "  (root has no Hermesh data here: installed for another user? Run `su - <user> -c 'hermeshd uninstall'`)")
	}
	if exists(cliHome) {
		fmt.Fprintf(w, "  • the agents' CLI key and settings: %s\n", cliHome)
		if exists(filepath.Join(cliHome, "state.json")) {
			fmt.Fprintln(w, "    (an agent signed in with it stays listed on your devices until you revoke it there)")
		}
	}
	fmt.Fprintf(w, "  • the programs: %s\n", strings.Join(programs, ", "))
	fmt.Fprintln(w, "Stays: your project folders and repositories, branches included.")
	if exists(herHome) {
		fmt.Fprintf(w, "Stays too: Her's assistants, conversations and settings, in %s; delete it yourself to remove them.\n", herHome)
	}
	if isMachine {
		fmt.Fprintf(w, "Worktrees with uncommitted changes move to %s.\n", keepDir)
	}
	if ask != nil {
		if a, err := ask("This can't be undone. Uninstall? [y/N] "); err != nil || !strings.EqualFold(a, "y") {
			if err != nil {
				return err
			}
			return errors.New("not uninstalled")
		}
	}

	// The service would start it again at once.
	if svc {
		if err := service.Uninstall(); err != nil {
			return fmt.Errorf("stopping the background service: %w", err)
		}
	}
	var kept []string
	if isMachine {
		if n, err := openWhenStopped(cfg); err == nil {
			if n.Paired() {
				lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if err := n.LeaveOwner(lctx); err != nil {
					fmt.Fprintf(os.Stderr, "warning: the server didn't remove it from its owner (%v); remove it in the Hermesh app\n", err)
				}
				cancel()
			}
			n.Close()
		}
		var err error
		if kept, err = machine.Erase(cfg, keepDir); err != nil {
			return fmt.Errorf("erasing this machine: %w", err)
		}
	}
	keepData := false
	for _, wt := range kept {
		if !inside(keepDir, wt) {
			keepData = true // it couldn't be moved out
		}
	}
	if !keepData {
		if err := os.RemoveAll(realData); err != nil {
			return err
		}
		os.Remove(dataDir) // the link to it, if it was one
	}
	if err := os.RemoveAll(cliHome); err != nil {
		return err
	}
	for _, p := range programs {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "✓ Hermesh is uninstalled for %s.\n", name)
	for _, wt := range kept {
		fmt.Fprintf(w, "  Kept, with uncommitted changes: %s\n", wt)
	}
	if keepData {
		fmt.Fprintf(w, "  %s stays, for the worktrees above: delete it once they are safe\n", realData)
	}
	if lingering(name) {
		fmt.Fprintf(w, "Lingering stays on for %s, as other services of %s's may need it; to turn it off: sudo loginctl disable-linger %s\n", name, name, name)
	}
	return nil
}

// checkLeftAsDevice refuses while this computer is one of the owner's
// trusted devices: its key may be the one that holds the owner's Hermesh.
func checkLeftAsDevice(ctx context.Context, dataDir string) error {
	c, err := ownerRole(ctx, dataDir, true)
	if err != nil {
		return fmt.Errorf("this computer is set up as a trusted device, and whether it still is one can't be checked: %w", err)
	}
	st, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
	switch {
	case err != nil:
		return err
	case st.Removed:
		return errors.New("this computer was removed from your trusted devices: run `hermeshd device leave`, then uninstall")
	case st.Initialized:
		return errors.New("this computer is one of your trusted devices, holding a key to your Hermesh: remove it from another of your devices and run `hermeshd device leave` here (or erase Hermesh everywhere with `hermeshd device erase-everything`), then uninstall")
	}
	return nil
}

// inside reports whether path is dir or under it.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// lingering reports whether systemd keeps the user's services running
// with nobody logged in; the installer turned it on.
func lingering(name string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	out, err := exec.Command("loginctl", "show-user", name, "--property=Linger").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Linger=yes"
}
