package e2e

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
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

// TestHerRemoveDeviceErasesHerData: removing a trusted device whose own
// machine runs Her, with "Also erase Her's data" (RemoveDevice erase_her):
// one approval signs both, her home there is gone, and the device with its
// machine.
func TestHerRemoveDeviceErasesHerData(t *testing.T) {
	skipHerInDocker(t)
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	console := herConsole(t)
	w := newOwnerMac(t, herEnv(her, console, herHome(t, mock.models), mock.base), &localv1.GrantSpec{})
	t.Cleanup(w.owner.stop)
	ctx := context.Background()

	// mac2: a second trusted device, running Her on its own machine.
	home := herHome(t, mock.models)
	_, sock := w.otherMac("mac2", herEnv(her, console, home, mock.base))
	root2, _ := identity.GenerateKey()
	app2 := newTestApp(t, sock, root2)
	st, _ := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	join, err := app2.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: st.RelayUrl, OwnerId: st.OwnerId,
		RootPublicKey: rootPub(root2), RootLabel: "mac2", RootPlatform: "macos"})
	if err != nil {
		t.Fatal(err)
	}
	var approval *localv1.ApproveDevicePairingResponse
	eventually(t, 15*time.Second, "mac2's request on the Mac", func() bool {
		approval, err = w.app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
		return err == nil
	})
	eventually(t, time.Minute, "the approval reaches mac2", func() bool {
		r, err := app2.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && r.Done
	})
	if _, err := app2.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: approval.MatchCode}); err != nil {
		t.Fatal(err)
	}
	mac2, err := app2.client.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: "mac2"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Minute, "mac2's machine online on the Mac", func() bool {
		m := listedMachine(t, w.app.client, mac2.MachineId)
		return m != nil && m.Online && m.Status != nil
	})
	turnHerOn(t, w.app.client, mac2.MachineId)
	if _, err := os.Stat(filepath.Join(home, "bots")); err != nil {
		t.Fatalf("her assistants on mac2: %v", err)
	}

	before := len(w.app.signedTypes())
	if _, err := w.app.client.RemoveDevice(ctx, &localv1.RemoveDeviceRequest{KeyId: identity.KeyID(root2.Public()), EraseHer: true}); err != nil {
		t.Fatal(err)
	}
	if got := w.app.signedTypes()[before:]; !slices.Contains(got, "hermesh.identity.v1.AdminCommand") || !slices.Contains(got, "hermesh.identity.v1.RootCert") {
		t.Fatalf("removing signed %v", got)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("her home on the removed device's machine: %v", err)
	}
	devices, err := w.app.client.ListDevices(ctx, &localv1.ListDevicesRequest{})
	if err != nil || len(devices.Devices) != 1 {
		t.Fatalf("devices after removing mac2: %v %v", devices, err)
	}
	eventually(t, 30*time.Second, "mac2's machine no longer listed", func() bool { return listedMachine(t, w.app.client, mac2.MachineId) == nil })
}
