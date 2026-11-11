package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// Removing a machine that runs Her, and `hermeshd uninstall` and `erase`
// where Her was on (docs/her-server.md §5.6). On the hermeshd that
// newHerEnvironment starts on this computer: her home is looked at here.

// herServer is env's machine as its own user reaches it on this computer:
// hermeshd, its data directory, and its environment for the commands run
// there, her home among it.
func herServer(t *testing.T, env *herEnvironment) (daemon, data string, cli []string) {
	t.Helper()
	her, _ := herBinaries(t)
	return filepath.Join(env.w.bin, "hermeshd"), filepath.Join(env.w.tmp, "her-server"),
		[]string{"HERMESH_HER_BIN=" + her, "HER_HOME=" + env.home, "HER_CREDENTIAL_STORE=file"}
}

func skipHerInDocker(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") == "1" {
		t.Skip("her's home is looked at on this computer")
	}
}

// TestHerRemoveMachineErasesHerData: the remove dialog's "Also erase Her's
// data" (RemoveMachine erase_her): her home on that machine is gone, and
// the machine with it.
func TestHerRemoveMachineErasesHerData(t *testing.T) {
	skipHerInDocker(t)
	env := newHerEnvironment(t)
	if _, err := os.Stat(filepath.Join(env.home, "bots")); err != nil {
		t.Fatalf("her assistants before removing: %v", err)
	}
	if _, err := env.c.RemoveMachine(context.Background(), &localv1.RemoveMachineRequest{MachineId: env.machine, EraseHer: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.home); !os.IsNotExist(err) {
		t.Fatalf("her home after removing the machine with her data: %v", err)
	}
	if m := listedMachine(t, env.c, env.machine); m != nil {
		t.Fatalf("still listed: %v", m)
	}
}

// TestHerRemovedMachineKeepsHerData: removed without erasing her data, the
// machine, still running and now refused by the server, turns Her off by
// itself, as `hermeshd status` there shows, her home staying. `hermeshd
// erase --yes` then keeps it too, and says where it is.
func TestHerRemovedMachineKeepsHerData(t *testing.T) {
	skipHerInDocker(t)
	env := newHerEnvironment(t)
	daemon, data, cli := herServer(t, env)
	if _, err := env.c.RemoveMachine(context.Background(), &localv1.RemoveMachineRequest{MachineId: env.machine}); err != nil {
		t.Fatal(err)
	}
	var status string
	eventually(t, time.Minute, "Her off on the removed machine", func() bool {
		r := run(t, cli, 30*time.Second, daemon, "status", "--data-dir", data)
		status = r.stdout
		for _, l := range strings.Split(r.stdout, "\n") {
			if strings.HasPrefix(l, "her ") {
				return strings.HasSuffix(l, ", off")
			}
		}
		return false
	})
	if _, err := os.Stat(filepath.Join(env.home, "bots")); err != nil {
		t.Fatalf("her assistants on the removed machine: %v\n%s", err, status)
	}

	takeOffline(t, env) // as a person stops `hermeshd run`
	r := run(t, cli, time.Minute, daemon, "erase", "--yes", "--data-dir", data)
	stays := "Stays too: Her's assistants, conversations and settings, in " + env.home + "; delete it yourself to remove them."
	if r.code != 0 || !strings.Contains(r.stdout, stays) || !strings.Contains(r.stdout, "✓ Hermesh on this computer is erased") {
		t.Fatalf("erase --yes: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(env.home, "bots")); err != nil {
		t.Fatalf("her assistants after erase --yes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "machine")); !os.IsNotExist(err) {
		t.Fatalf("the machine kept: %v", err)
	}
}

// TestHerUninstallErasesHerData: `hermeshd uninstall --yes --erase-her` on
// a server where Her runs, stopped first: it says Her's data goes, and her
// home goes with the machine, which its owner's devices stop listing.
func TestHerUninstallErasesHerData(t *testing.T) {
	skipHerInDocker(t)
	env := newHerEnvironment(t)
	daemon, data, cli := herServer(t, env)
	// A copy, in a home of the test's: uninstall removes the hermeshd it
	// runs as, and the CLI's settings in that home.
	home := t.TempDir()
	b, err := os.ReadFile(daemon)
	if err != nil {
		t.Fatal(err)
	}
	daemon = filepath.Join(home, ".local", "bin", "hermeshd")
	os.MkdirAll(filepath.Dir(daemon), 0o755)
	if err := os.WriteFile(daemon, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cli = append(cli, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "HERMESH_HOME="+filepath.Join(home, ".config", "hermesh"))

	takeOffline(t, env) // as a person stops `hermeshd run`
	r := run(t, cli, time.Minute, daemon, "uninstall", "--yes", "--erase-her", "--data-dir", data)
	if r.code != 0 || !strings.Contains(r.stdout, "  • Her's assistants, conversations and settings: "+env.home+"\n") ||
		!strings.Contains(r.stdout, "✓ Hermesh is uninstalled") || strings.Contains(r.stdout, "Stays too") || strings.Contains(r.stderr, "warning") {
		t.Fatalf("uninstall --yes --erase-her: %+v", r)
	}
	for _, p := range []string{env.home, data, daemon} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s after uninstalling: %v", p, err)
		}
	}
	eventually(t, 30*time.Second, "the machine gone from the owner's machines", func() bool { return listedMachine(t, env.c, env.machine) == nil })
}
