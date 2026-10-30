package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestAccountsOnOneDevice: one Mac belongs to two owners, each on its own
// relay, with a profile and a root key for each (docs/trusted-devices.md
// 4.8). It switches between them, each with its own machine; it signs out
// and back in without another device; and, signed out, it is removed by
// another device of that owner, learns so when it signs in, and leaves.
func TestAccountsOnOneDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	relayBin, daemonBin := filepath.Join(bin, "hermesh-relay"), filepath.Join(bin, "hermeshd")
	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	ctx := context.Background()
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})

	startRelay := func(name string) (url, claim string) {
		port := freePort(t)
		data := filepath.Join(tmp, name)
		p := start(t, name, nil, relayBin, "serve", "--data-dir", data, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
		logs = append(logs, p)
		eventually(t, 20*time.Second, name+" up", func() bool { return strings.Contains(p.output(), "relay listening") })
		return fmt.Sprintf("http://127.0.0.1:%d", port), strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", data).stdout)
	}
	relayA, claimA := startRelay("relay-a")
	relayB, claimB := startRelay("relay-b")

	macData, sock := filepath.Join(tmp, "mac"), filepath.Join(sockDir, "m.sock")
	startMac := func() *proc {
		p := start(t, "mac", env, daemonBin, "run", "--owner", "--data-dir", macData, "--socket", sock)
		logs = append(logs, p)
		return p
	}
	macProc := startMac()
	rootA, _ := identity.GenerateKey()
	rootB, _ := identity.GenerateKey()
	app := newTestApp(t, sock, rootA)
	app.addKey(rootB)
	status := func(a *testApp) *localv1.GetStatusResponse {
		st, err := a.client.GetStatus(ctx, &localv1.GetStatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	profiles := func() []*localv1.Profile {
		res, err := app.client.ListProfiles(ctx, &localv1.ListProfilesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Profiles
	}
	machineOnline := func(a *testApp, name string) bool {
		res, err := a.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineName == name && m.Online })
	}

	// Account A, with this Mac as its machine.
	setupA, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayA, ClaimCode: claimA, RootPublicKey: rootPub(rootA), RootLabel: "mac",
		RootPlatform: "macos", LocalMachineName: "mac-a", Account: "a@example.com"})
	if err != nil {
		t.Fatalf("setup A: %v\n%s", err, macProc.output())
	}
	eventually(t, 30*time.Second, "mac-a online", func() bool { return machineOnline(app, "mac-a") })

	// Account B, added: a new profile with its own key and its own machine.
	added, err := app.client.AddProfile(ctx, &localv1.AddProfileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if st := status(app); st.Initialized || st.LocalMachineEnabled {
		t.Fatalf("an added profile is not empty: %+v", st)
	}
	setupB, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayB, ClaimCode: claimB, RootPublicKey: rootPub(rootB), RootLabel: "mac",
		RootPlatform: "macos", LocalMachineName: "mac-b", Account: "b@example.com"})
	if err != nil {
		t.Fatalf("setup B: %v\n%s", err, macProc.output())
	}
	if st := status(app); st.OwnerId != setupB.OwnerId || st.DeviceKeyId != identity.KeyID(rootB.Public()) || st.Account != "b@example.com" {
		t.Fatalf("status of B: %+v", st)
	}
	eventually(t, 30*time.Second, "mac-b online", func() bool { return machineOnline(app, "mac-b") })
	if ms, _ := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); len(ms.GetMachines()) != 1 {
		t.Fatalf("B sees machines that are not its own: %v", ms)
	}
	ps := profiles()
	if len(ps) != 2 || ps[0].OwnerId != setupA.OwnerId || ps[0].Current || ps[0].Account != "a@example.com" ||
		ps[1].Id != added.Id || ps[1].OwnerId != setupB.OwnerId || !ps[1].Current {
		t.Fatalf("profiles: %v", ps)
	}
	idA, idB := ps[0].Id, ps[1].Id

	// Joining an owner this Mac already has is refused; the empty profile
	// that asked is not listed once another is current.
	if _, err := app.client.AddProfile(ctx, &localv1.AddProfileRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: relayA, OwnerId: setupA.OwnerId, RootLabel: "mac"}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("joining A again: %v", err)
	}

	// Switching: A is back with its own machine; B's stops.
	if _, err := app.client.SwitchProfile(ctx, &localv1.SwitchProfileRequest{Id: idA}); err != nil {
		t.Fatal(err)
	}
	if len(profiles()) != 2 {
		t.Fatalf("the empty profile is listed: %v", profiles())
	}
	if st := status(app); st.OwnerId != setupA.OwnerId {
		t.Fatalf("status after switching to A: %+v", st)
	}
	eventually(t, 30*time.Second, "mac-a online again", func() bool { return machineOnline(app, "mac-a") && status(app).LocalMachineName == "mac-a" })

	// A second Mac joins A.
	sock2 := filepath.Join(sockDir, "m2.sock")
	mac2 := start(t, "mac2", env, daemonBin, "run", "--owner", "--data-dir", filepath.Join(tmp, "mac2"), "--socket", sock2)
	logs = append(logs, mac2)
	root2, _ := identity.GenerateKey()
	app2 := newTestApp(t, sock2, root2)
	join, err := app2.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: relayA, OwnerId: setupA.OwnerId,
		RootPublicKey: rootPub(root2), RootLabel: "mac2", RootPlatform: "macos"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "join request on the Mac", func() bool {
		res, err := app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		return err == nil && slices.ContainsFunc(res.Pairings, func(p *localv1.PairingDetails) bool { return p.Kind == identityv1.PairingKind_PAIRING_KIND_DEVICE })
	})
	approval, err := app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "approval reaches mac2", func() bool {
		r, err := app2.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && r.Done
	})
	if _, err := app2.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: approval.MatchCode}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "mac2 sees mac-a online", func() bool { return machineOnline(app2, "mac-a") })

	// Signing out keeps the keys but stops everything: no relay connection,
	// no machine, no calls that need the owner.
	if _, err := app.client.SignOut(ctx, &localv1.SignOutRequest{}); err != nil {
		t.Fatal(err)
	}
	if st := status(app); !st.SignedOut || !st.Initialized || st.LocalMachineEnabled || st.RelayConnected {
		t.Fatalf("status after signing out: %+v", st)
	}
	if _, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("listing machines signed out: %v", err)
	}
	eventually(t, 30*time.Second, "mac2 sees mac-a offline", func() bool { return !machineOnline(app2, "mac-a") })
	// Signed out survives a restart.
	macProc.stop()
	macProc = startMac()
	app.cancel()
	app = newTestApp(t, sock, rootA)
	app.addKey(rootB)
	if st := status(app); !st.SignedOut || st.OwnerId != setupA.OwnerId {
		t.Fatalf("status after a restart: %+v", st)
	}
	// Signing in again needs no other device (a self-hosted relay: no
	// browser sign-in either).
	if _, err := app.client.SignIn(ctx, &localv1.SignInRequest{}); err != nil {
		t.Fatal(err)
	}
	if st := status(app); st.SignedOut || st.Removed {
		t.Fatalf("status after signing in: %+v", st)
	}
	eventually(t, 30*time.Second, "mac2 sees mac-a online after signing in", func() bool { return machineOnline(app2, "mac-a") })

	// Signed out, the Mac is removed from mac2: signing in, it learns so,
	// and leaving deletes A's root key. B is untouched.
	if _, err := app.client.SignOut(ctx, &localv1.SignOutRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := app2.client.RemoveDevice(ctx, &localv1.RemoveDeviceRequest{KeyId: identity.KeyID(rootA.Public())}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.client.SignIn(ctx, &localv1.SignInRequest{}); err != nil {
		t.Fatal(err)
	}
	if st := status(app); !st.Removed {
		t.Fatalf("a removed Mac signed in as trusted: %+v", st)
	}
	if _, err := app.client.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := app.deletedKeys(); !slices.Equal(got, []string{identity.KeyID(rootA.Public())}) {
		t.Fatalf("deleted root keys %v", got)
	}
	if ps := profiles(); len(ps) != 2 || ps[0].Id != idA || ps[0].OwnerId != "" || !ps[0].Current || ps[1].Id != idB {
		t.Fatalf("profiles after leaving A: %v", ps)
	}
	if _, err := app.client.SwitchProfile(ctx, &localv1.SwitchProfileRequest{Id: idB}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "mac-b online after switching back", func() bool { return machineOnline(app, "mac-b") })

	// The current profile survives a restart.
	macProc.stop()
	startMac()
	app.cancel()
	app = newTestApp(t, sock, rootB)
	if st := status(app); st.OwnerId != setupB.OwnerId || st.SignedOut {
		t.Fatalf("status after a restart: %+v", st)
	}
	eventually(t, 30*time.Second, "mac-b online after a restart", func() bool { return machineOnline(app, "mac-b") })
}

// TestOwnerFromBeforeProfiles: a Mac set up before profiles (owner.db and
// keys at the top of the owner directory, its machine beside it) keeps its
// owner and its machine: hermeshd moves the owner into a profile, and the
// machine, whose worktrees name its path, stays where it is.
func TestOwnerFromBeforeProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	relayBin, daemonBin := filepath.Join(bin, "hermesh-relay"), filepath.Join(bin, "hermeshd")
	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	ctx := context.Background()
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, relayBin, "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relay)
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)

	data, sock := filepath.Join(tmp, "mac"), filepath.Join(sockDir, "m.sock")
	startMac := func() *proc {
		p := start(t, "mac", []string{"HERMESH_FAKE_PROVIDERS=1"}, daemonBin, "run", "--owner", "--data-dir", data, "--socket", sock)
		logs = append(logs, p)
		return p
	}
	mac := startMac()
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayURL, ClaimCode: claim, RootPublicKey: rootPub(root), RootLabel: "mac", LocalMachineName: "mac"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || before.LocalMachineId == "" {
		t.Fatalf("status: %v %v", before, err)
	}
	mac.stop()
	app.cancel()

	// The layout from before profiles.
	ownerDir := filepath.Join(data, "owner")
	cur, _ := os.ReadFile(filepath.Join(ownerDir, "current"))
	profile := filepath.Join(ownerDir, "profiles", string(cur))
	for from, to := range map[string]string{"owner.db": "owner/owner.db", "owner.db-wal": "owner/owner.db-wal", "owner.db-shm": "owner/owner.db-shm",
		"keys": "owner/keys", "machine": "machine"} {
		if err := os.Rename(filepath.Join(profile, from), filepath.Join(data, to)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	os.RemoveAll(filepath.Join(ownerDir, "profiles"))
	os.Remove(filepath.Join(ownerDir, "current"))

	startMac()
	app = newTestApp(t, sock, root)
	st, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || st.OwnerId != setup.OwnerId || st.DeviceKeyId != before.DeviceKeyId || st.SignedOut {
		t.Fatalf("status after moving: %v %v", st, err)
	}
	eventually(t, 30*time.Second, "its machine online, under the same id", func() bool {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineId == before.LocalMachineId && m.Online })
	})
	if _, err := os.Stat(filepath.Join(ownerDir, "owner.db")); !os.IsNotExist(err) {
		t.Fatalf("owner.db left at the top: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "machine", "hermeshd.db")); err != nil {
		t.Fatalf("the machine moved: %v", err)
	}
	res, err := app.client.ListProfiles(ctx, &localv1.ListProfilesRequest{})
	if err != nil || len(res.Profiles) != 1 || res.Profiles[0].OwnerId != setup.OwnerId || !res.Profiles[0].Current {
		t.Fatalf("profiles: %v %v", res, err)
	}
}
