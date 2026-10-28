package e2e

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestEraseEverything: from one trusted device, with one approval, the
// owner erases Hermesh everywhere (docs/erase-everything.md). The other
// trusted device, a Mac that is only a machine, a machine on a server and
// the agent each erase themselves when the relay refuses them; a machine
// that was off does so when it next starts. A worktree with uncommitted
// changes is kept, a clean one is not. The relay can be claimed again, and
// the Mac set up from scratch.
func TestEraseEverything(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	ctx := context.Background()
	daemon := filepath.Join(w.bin, "hermeshd")

	// Two sessions on the Mac's machine: one leaves its worktree with
	// uncommitted changes, the other leaves it clean.
	dirty, r := w.startSession(time.Minute, "--wait", "--prompt", "say working")
	if dirty.Status != "idle" {
		t.Fatalf("dirty session: %+v", r)
	}
	dirtyWT := w.worktree(dirty.SessionID)
	if err := os.WriteFile(filepath.Join(dirtyWT, "unsaved.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, r := w.startSession(time.Minute, "--wait", "--prompt", "say done")
	if clean.Status != "idle" {
		t.Fatalf("clean session: %+v", r)
	}
	cleanWT := w.worktree(clean.SessionID)
	if _, err := os.Stat(cleanWT); err != nil {
		t.Fatal(err)
	}

	// Two machines on servers: vps-on runs, vps-off is off when the owner
	// erases everything.
	pairVPS := func(name string) (data string) {
		data = filepath.Join(w.tmp, name)
		pair := start(t, "pair "+name, env, daemon, "pair", w.addr, "--name", name, "--no-service", "--data-dir", data)
		if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: name}); err != nil {
			t.Fatal(err)
		}
		if c := pair.wait(t, 30*time.Second); c != 0 {
			t.Fatalf("pair %s: %s", name, pair.output())
		}
		return data
	}
	runVPS := func(name, data string) *proc {
		p := start(t, name, env, daemon, "run", "--data-dir", data)
		w.logs = append(w.logs, p)
		return p
	}
	online := func(name string) bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineName == name && m.Online })
	}
	onData, offData := pairVPS("vps-on"), pairVPS("vps-off")
	vpsOn, vpsOff := runVPS("vps-on", onData), runVPS("vps-off", offData)
	eventually(t, 30*time.Second, "both servers online", func() bool { return online("vps-on") && online("vps-off") })
	vpsOff.stop()

	// A Mac that is only a machine, and a second trusted device.
	mac3, _ := w.otherMac("mac3", env)
	home, _ := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	w.joinTyped(mac3, home, "mac3")
	_, mac2Sock := w.otherMac("mac2", env)
	root2, _ := identity.GenerateKey()
	app2 := newTestApp(t, mac2Sock, root2)
	join, err := app2.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: home.RelayUrl, OwnerId: home.OwnerId,
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
	eventually(t, 30*time.Second, "mac2 sees the machines", func() bool {
		res, err := app2.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && len(res.Machines) == 4
	})

	// Erase everything, from the Mac: one approval.
	before := len(w.app.signedTypes())
	erase, err := w.app.client.EraseEverything(ctx, &localv1.EraseEverythingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.app.signedTypes()[before:]; !slices.Equal(got, []string{"hermesh.identity.v1.EraseOrder"}) {
		t.Fatalf("erasing signed %v", got)
	}

	// The agent learns it at its next command, and forgets its pairing.
	r = w.cli(30*time.Second, "project", "list")
	if r.code != 3 || !strings.Contains(r.stderr, "the owner erased everything") {
		t.Fatalf("the agent after the erase: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(w.tmp, "agent", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("the agent kept its pairing: %v", err)
	}

	// The Mac shows each one erasing; vps-off stays to do.
	status := func() map[string]*localv1.EraseItem {
		res, err := w.app.client.EraseStatus(ctx, &localv1.EraseStatusRequest{EraseId: erase.EraseId})
		if err != nil {
			t.Fatal(err)
		}
		items := map[string]*localv1.EraseItem{}
		for _, it := range res.Items {
			items[it.Kind+"/"+it.Name] = it
		}
		return items
	}
	eventually(t, time.Minute, "the others erasing", func() bool {
		items := status()
		for _, k := range []string{"device/mac2", "machine/vps-on", "machine/mac3", "agent/bot"} {
			if items[k] == nil || !items[k].Done {
				return false
			}
		}
		return true
	})
	items := status()
	if len(items) != 6 || !items["device/test-mac"].ThisDevice || items["device/test-mac"].Done || items["machine/vps-off"].Done {
		t.Fatalf("erase items: %v", items)
	}
	if c := vpsOn.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("vps-on exited %d: %s", c, vpsOn.output())
	}
	if entries, _ := os.ReadDir(onData); len(entries) != 0 {
		t.Fatalf("vps-on kept %v", entries)
	}

	// The Mac erases itself last, and opens empty, saying what was done.
	if _, err := w.app.client.FinishErase(ctx, &localv1.FinishEraseRequest{}); err != nil {
		t.Fatal(err)
	}
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || st.Initialized || st.Erased == nil || st.Erased.EraseId != erase.EraseId || !slices.Equal(st.Erased.Pending, []string{"vps-off"}) ||
		len(st.Erased.KeptWorktrees) != 1 {
		t.Fatalf("the Mac after erasing: %v %v", st, err)
	}
	if got := w.app.deletedKeys(); !slices.Equal(got, []string{identity.KeyID(w.root.Public())}) {
		t.Fatalf("deleted root keys %v", got)
	}
	kept := st.Erased.KeptWorktrees[0]
	if b, err := os.ReadFile(filepath.Join(kept, "unsaved.txt")); err != nil || string(b) != "work in progress\n" {
		t.Fatalf("the kept worktree %s: %q %v", kept, b, err)
	}
	if _, err := os.Stat(cleanWT); !os.IsNotExist(err) {
		t.Fatalf("the clean worktree is left: %v", err)
	}
	if _, err := os.Stat(dirtyWT); !os.IsNotExist(err) {
		t.Fatalf("the dirty worktree was not moved out: %v", err)
	}
	if out := gitOut(t, kept, "status", "--porcelain"); !strings.Contains(out, "unsaved.txt") {
		t.Fatalf("the kept worktree is no longer a worktree: %q", out)
	}
	if _, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("listing machines after erasing: %v", err)
	}

	// The other trusted device and the machine-only Mac erased themselves.
	eventually(t, 30*time.Second, "mac2 erased", func() bool {
		st, err := app2.client.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && !st.Initialized && st.Erased != nil && st.Erased.EraseId == erase.EraseId
	})
	if got := app2.deletedKeys(); !slices.Equal(got, []string{identity.KeyID(root2.Public())}) {
		t.Fatalf("mac2 deleted root keys %v", got)
	}
	eventually(t, 30*time.Second, "mac3 erased", func() bool {
		st, err := mac3.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && !st.MachineOnly && !st.Initialized && st.Erased != nil
	})

	// vps-off, started again, learns it then.
	vpsOff = runVPS("vps-off", offData)
	if c := vpsOff.wait(t, time.Minute); c != 0 {
		t.Fatalf("vps-off exited %d: %s", c, vpsOff.output())
	}
	if !strings.Contains(vpsOff.output(), "The owner erased everything") {
		t.Fatalf("vps-off said: %s", vpsOff.output())
	}
	if entries, _ := os.ReadDir(offData); len(entries) != 0 {
		t.Fatalf("vps-off kept %v", entries)
	}

	// A cold start: the relay can be claimed again, and the Mac set up anew.
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(w.bin, "hermesh-relay"), "claim-code", "--data-dir", filepath.Join(w.tmp, "relay")).stdout)
	setup, err := w.app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: home.RelayUrl, ClaimCode: claim, RootLabel: "test-mac"})
	if err != nil {
		t.Fatalf("setting up again: %v", err)
	}
	if setup.OwnerId == home.OwnerId {
		t.Fatal("set up again as the erased owner")
	}
	if st, _ := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{}); !st.Initialized || st.Erased != nil {
		t.Fatalf("status once set up again: %v", st)
	}
}
