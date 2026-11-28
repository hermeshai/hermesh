package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	iv "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	lv "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	mv "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/machine"
)

// Run in a disposable environment. The previous daemon is built with the
// preceding release's package pins. All writes run on a separately paired
// machine. Other providers are supplied by the fixture's official installers.
func TestProviderMaintenanceReal(t *testing.T) {
	if os.Getenv("HERMESH_PROVIDER_ACCEPTANCE") != "1" {
		t.Skip("requires an isolated provider acceptance environment")
	}
	if prebuilt := os.Getenv("HERMESH_PROVIDER_FIXTURE_BIN"); prebuilt != "" {
		buildOnce.Do(func() { binDir = prebuilt })
	}
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &lv.GrantSpec{Agents: []string{"codex"}})
	ctx := context.Background()
	home, err := w.app.client.GetStatus(ctx, &lv.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp(shortTemp, "hm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "remote.sock")
	args := []string{"run", "--owner", "--data-dir", filepath.Join(w.tmp, "provider-target"), "--socket", sock}
	var env []string
	remote := start(t, "previous target", env, os.Getenv("HERMESH_PROVIDER_PREVIOUS"), args...)
	target := w.joinTyped(localClient(t, sock), home, "provider-target").LocalMachineId
	status := func() *mv.MachineStatus {
		res, err := w.app.client.ListMachines(ctx, &lv.ListMachinesRequest{})
		if err != nil {
			return nil
		}
		for _, m := range res.Machines {
			if m.MachineId == target && m.Online {
				return m.Status
			}
		}
		return nil
	}
	capability := func(id string) *mv.ProviderMaintenance {
		for _, c := range status().GetProviderMaintenance() {
			if c.Provider == id {
				return c
			}
		}
		return nil
	}
	run := func(t *testing.T, id, action, from, to string) {
		t.Helper()
		op := &iv.MaintainProvider{Provider: id, Action: action, RequestId: fmt.Sprintf("%s-%s-%d", id, action, time.Now().UnixNano()), ExpectedVersion: from, TargetVersion: to}
		req := &lv.MaintainProviderRequest{MachineId: target, Operation: op}
		if _, err := w.app.client.MaintainProvider(ctx, req); err != nil {
			t.Fatal(err)
		}
		if id == "claude" && action == "install" {
			// Lose the observation channel after acknowledgement. The target keeps running.
			w.relay.stop()
			w.startRelay()
		}
		last := ""
		limit := time.Now().Add(11 * time.Minute)
		for time.Now().Before(limit) {
			for _, j := range status().GetProviderOperations() {
				if j.Request.RequestId != op.RequestId {
					continue
				}
				if j.Phase != last {
					t.Logf("%s %s %s → %s: %s %s", id, action, from, to, j.Phase, j.Detail)
					last = j.Phase
				}
				switch j.Phase {
				case "succeeded":
					if _, err := w.app.client.MaintainProvider(ctx, req); err != nil {
						t.Fatal("replay", err)
					}
					for _, a := range status().GetAgents() {
						if a.Name == id {
							t.Logf("driver %s version %s signedIn=%t", id, a.Version, a.LoggedIn)
						}
					}
					return
				case "failed", "interrupted":
					t.Fatalf("target reported %s: %s", j.Phase, j.Detail)
				}
			}
			time.Sleep(400 * time.Millisecond)
		}
		t.Fatal("no terminal receipt; last phase", last)
	}
	olds := map[string]string{}
	managed := []string{"claude", "codex", "copilot", "opencode"}
	eventually(t, 30*time.Second, "paired target capabilities", func() bool { return capability("codex").GetTargetVersion() != "" })
	for _, id := range managed {
		olds[id] = capability(id).TargetVersion
	}
	// Optional interactive release check: Electron owns every action on a
	// freshly paired target. The test harness performs only the daemon upgrade.
	if fixture := os.Getenv("HERMESH_PROVIDER_UI_LIFECYCLE"); fixture != "" {
		step := func(stage string) {
			file := fixture + "." + stage
			body, _ := json.Marshal(map[string]string{"socket": w.sock, "target": target})
			if err := os.WriteFile(file, body, 0600); err != nil {
				t.Fatal(err)
			}
			eventually(t, 12*time.Minute, "desktop "+stage, func() bool { _, err := os.Stat(file + ".done"); return err == nil })
			for _, id := range managed {
				c := capability(id)
				if c.GetManagedVersion() != c.GetTargetVersion() {
					t.Fatalf("%s %s: %v", stage, id, c)
				}
			}
		}
		step("install")
		remote.stop()
		start(t, "new target", env, filepath.Join(w.bin, "hermeshd"), args...)
		eventually(t, time.Minute, "catalog updated", func() bool { v := capability("codex").GetTargetVersion(); return v != "" && v != olds["codex"] })
		step("update")
		return
	}
	for _, id := range managed {
		t.Run("install-"+id, func(t *testing.T) { run(t, id, "install", "", olds[id]) })
	}
	if t.Failed() {
		return
	}
	t.Run("enable-auto", func(t *testing.T) { run(t, "codex", "enable_auto", olds["codex"], olds["codex"]) })
	remote.stop()
	start(t, "new target", env, filepath.Join(w.bin, "hermeshd"), args...)
	eventually(t, time.Minute, "next release on same machine", func() bool { v := capability("codex").GetTargetVersion(); return v != "" && v != olds["codex"] })
	for _, id := range managed {
		to := capability(id).TargetVersion
		t.Run("update-"+id, func(t *testing.T) {
			if id != "codex" {
				run(t, id, "update", olds[id], to)
			} else {
				eventually(t, 8*time.Minute, "automatic codex upgrade", func() bool {
					for _, j := range status().GetProviderOperations() {
						if j.Request.Action == "update" && j.Request.Provider == id {
							if j.Phase == "failed" {
								t.Fatal(j.Detail)
							}
							if j.Phase == "succeeded" {
								return true
							}
						}
					}
					return false
				})
			}
			if v := capability(id).GetManagedVersion(); v != to {
				t.Fatalf("managed version %s want %s", v, to)
			}
		})
	}
	for _, c := range status().GetProviderMaintenance() {
		id := c.Provider
		t.Run("verify-"+id, func(t *testing.T) {
			run(t, id, "verify", "", "")
		})
	}
	if fixture := os.Getenv("HERMESH_PROVIDER_UI_FIXTURE"); fixture != "" {
		body, err := json.Marshal(map[string]string{"socket": w.sock, "target": target})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(fixture, body, 0600); err != nil {
			t.Fatal(err)
		}
		eventually(t, 12*time.Minute, "Electron acceptance finished", func() bool { _, err := os.Stat(fixture + ".done"); return err == nil })
	}
	accounts := os.Getenv("HERMESH_PROVIDER_ACCOUNT_TESTS")
	if accounts != "" {
		t.Run("model", func(t *testing.T) {
			for _, id := range machine.AgentNames() {
				if accounts != "all" && !strings.Contains(","+accounts+",", ","+id+",") {
					continue
				}
				t.Run(id, func(t *testing.T) {
					t.Parallel()
					res, err := w.app.client.TestMachineAgent(ctx, &lv.TestMachineAgentRequest{MachineId: target, Agent: id})
					if err != nil {
						t.Fatal(err)
					}
					if !res.Ok || !strings.EqualFold(strings.Trim(strings.TrimSpace(res.Reply), ".!"), "ready") {
						t.Fatalf("model did not reply ready: %.220s", res.Reply)
					}
					t.Logf("%s answered ready in %d ms", id, res.Millis)
				})
			}
		})
	}
}

// Re-run model acceptance independently on an explicitly selected, signed-in
// machine. The installed clients keep their existing credentials there.
func TestProviderAccountTurns(t *testing.T) {
	sock, target, agents := os.Getenv("HERMESH_PROVIDER_ACCOUNT_SOCKET"), os.Getenv("HERMESH_PROVIDER_ACCOUNT_MACHINE"), os.Getenv("HERMESH_PROVIDER_ACCOUNT_TESTS")
	if sock == "" || target == "" || agents == "" {
		t.Skip("requires an explicit owner socket, machine ID and providers")
	}
	client := localClient(t, sock)
	ids := strings.Split(agents, ",")
	if agents == "all" {
		ids = machine.AgentNames()
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			res, err := client.TestMachineAgent(ctx, &lv.TestMachineAgentRequest{MachineId: target, Agent: id})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Ok || !strings.EqualFold(strings.Trim(strings.TrimSpace(res.Reply), ".!"), "ready") {
				t.Fatalf("model test failed on %s; inspect the provider's result in machine settings", target)
			}
			t.Logf("ready in %d ms", res.Millis)
		})
	}
}

// The iPhone operates a separately paired machine with a disposable account.
// Only the release change is driven outside the app.
func iphoneProviderRelease(t *testing.T, w *ownerMac, mux *http.ServeMux) {
	t.Helper()
	old := t.TempDir()
	if r := run(t, nil, 4*time.Minute, "python3", "scripts/build-provider-fixture.py", old, "--os", "darwin"); r.code != 0 {
		t.Fatalf("previous release: %s", r.stderr)
	}
	sock := filepath.Join(filepath.Dir(w.sock), "provider.sock")
	args := []string{"run", "--owner", "--data-dir", t.TempDir(), "--socket", sock}
	homeDir := t.TempDir()
	env := []string{"HOME=" + homeDir, "NPM_CONFIG_PREFIX=" + filepath.Join(homeDir, ".local"), "SHELL=/bin/sh", "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	previous := start(t, "provider target", env, filepath.Join(old, "previous"), args...)
	home, err := w.app.client.GetStatus(context.Background(), &lv.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	w.joinTyped(localClient(t, sock), home, "provider-mac")
	mux.HandleFunc("POST /provider-release", func(rw http.ResponseWriter, r *http.Request) {
		previous.stop()
		start(t, "updated provider target", env, filepath.Join(w.bin, "hermeshd"), args...)
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprint(rw, "{}")
	})
}
