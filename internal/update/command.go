package update

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/service"
)

// Run is `hermeshd update` and `hermesh update`: it updates the programs
// in the running one's folder and restarts the service, asking first when
// that stops a session (yes: it does not ask). check only says whether a
// newer version exists.
func Run(ctx context.Context, in io.Reader, out io.Writer, dataDir string, check, yes bool) error {
	current := machine.Version
	dir, err := Dir()
	if err != nil {
		return err
	}
	r, err := Latest(ctx)
	if err != nil {
		return fmt.Errorf("could not look for an update: %w", err)
	}
	if !r.Newer(current) {
		fmt.Fprintf(out, "Hermesh %s is the latest.\n", current)
		return nil
	}
	if check {
		fmt.Fprintf(out, "Hermesh %s is available (this computer has %s).\n", r.Version, current)
		return nil
	}
	fmt.Fprintf(out, "Hermesh %s → %s\n", current, r.Version)
	c, machineUp := machineCheck(ctx, dataDir)
	if c.Busy > 0 && !yes {
		if c.Busy == 1 {
			fmt.Fprintln(out, "1 session is running here; restarting the service ends its turn.")
		} else {
			fmt.Fprintf(out, "%d sessions are running here; restarting the service ends their turns.\n", c.Busy)
		}
		fmt.Fprint(out, "Update now? [y/N] ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			if answer == "" {
				fmt.Fprintln(out)
			}
			fmt.Fprintln(out, "Nothing was updated. `hermeshd update --yes` updates without asking.")
			return nil
		}
	}
	names, err := Install(ctx, dataDir, dir, r)
	if err != nil {
		return err
	}
	Edit(dataDir, func(s *State) { s.From, s.To, s.Updated, s.Available = current, r.Version, time.Now(), "" })
	fmt.Fprintf(out, "✓ verified %s\n", strings.Join(names, " and "))
	fmt.Fprintf(out, "✓ installed in %s\n", tilde(dir))
	switch {
	case service.Running():
		if err := service.Restart(); err != nil {
			return err
		}
		// A machine says its version when it is up again; a trusted
		// device has no control socket.
		deadline := time.Now().Add(time.Minute)
		for time.Now().Before(deadline) {
			if c, _ := machineCheck(ctx, dataDir); (machineUp && c.Version == r.Version) || (!machineUp && service.Running()) {
				fmt.Fprintf(out, "✓ the service restarted on %s\n", r.Version)
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return errors.New("the service did not come back on the new version; see `hermeshd service status` and its log")
	case machineUp:
		fmt.Fprintln(out, "hermeshd runs by hand here: restart it to use the new version.")
	}
	return nil
}

// Auto is `hermeshd update auto [on|off]`.
func Auto(out io.Writer, dataDir, arg string) error {
	switch arg {
	case "on", "off":
		if err := Edit(dataDir, func(s *State) { s.Off = arg == "off" }); err != nil {
			return err
		}
	case "":
	default:
		return fmt.Errorf("%q: on or off", arg)
	}
	if Load(dataDir).Off {
		fmt.Fprintln(out, "Automatic updates are off. `hermeshd update` still updates.")
	} else {
		fmt.Fprintln(out, "Automatic updates are on: Hermesh updates itself when no session is running.")
	}
	return nil
}

// Line is how updates are, for `hermeshd status`.
func Line(dataDir string) string {
	s := Load(dataDir)
	line := "automatic"
	if s.Off {
		line = "off"
	}
	if s.Available != "" {
		line += " · " + s.Available + " is available"
	} else if !s.Checked.IsZero() {
		line += " · checked " + ago(time.Since(s.Checked))
	}
	return line
}

func ago(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// machineCheck asks a running machine how it is; ok is false when none
// answers.
func machineCheck(ctx context.Context, dataDir string) (c machine.Check, ok bool) {
	b, ok, err := machine.ControlRequest(ctx, filepath.Join(dataDir, "machine"), "GET", "/check", nil)
	if ok && err == nil {
		json.Unmarshal(b, &c)
	}
	return c, ok && err == nil
}

func tilde(dir string) string {
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(dir, home+"/") {
		return "~" + dir[len(home):]
	}
	return dir
}
