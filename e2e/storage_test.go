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
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// TestStorage: the owner's device sees another machine's sessions'
// checkouts over its connection, as on its own machine, releases the idle
// ones that hold nothing their branch lacks, keeps the others saying why,
// and turns auto-release on there (docs/settings-environments.md §5.7).
func TestStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	ctx := context.Background()

	home := t.TempDir()
	app := filepath.Join(home, "app")
	gitRepoAt(t, app, map[string]string{"README.md": "# app\n"})
	if err := os.WriteFile(filepath.Join(app, ".git", "info", "exclude"), []byte("node_modules/\n.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vpsEnv := append(homeEnv(home), env...)
	daemon := filepath.Join(w.bin, "hermeshd")
	data := filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", vpsEnv, daemon, "pair", w.addr, "--name", "vps-1", "--no-service", "--data-dir", data)
	if _, err := w.app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	w.logs = append(w.logs, start(t, "vps-1", vpsEnv, daemon, "run", "--data-dir", data))
	var vps *localv1.Machine
	eventually(t, 30*time.Second, "vps-1 online", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineName == "vps-1" && m.Online && m.Status != nil {
				vps = m
			}
		}
		return err == nil && vps != nil
	})
	// It says it answers storage: the apps show the page rather than "needs update".
	if !slices.Contains(vps.Status.Methods, "ListWorkspaces") || !slices.Contains(vps.Status.Methods, "ReleaseWorkspaces") {
		t.Fatalf("methods: %v", vps.Status.Methods)
	}
	if add := run(t, vpsEnv, 30*time.Second, daemon, "project", "add", "~/app", "--data-dir", data); add.code != 0 {
		t.Fatalf("project add: %+v", add)
	}
	eventually(t, 30*time.Second, "the project on the device", func() bool {
		ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{MachineId: vps.MachineId})
		return err == nil && len(ps.Projects) == 1
	})

	// Two sessions there: one leaves only node_modules, the other an .env.
	var ids []string
	for range 2 {
		s, r := w.startSession(time.Minute, "--project", "vps-1/app", "--agent", "claude", "--wait", "--prompt", "say ready")
		if r.code != 0 || s.Status != "idle" {
			t.Fatalf("session: %+v", r)
		}
		ids = append(ids, s.SessionID)
	}
	list := func(cached bool) *machinev1.ListWorkspacesResponse {
		t.Helper()
		res, err := w.app.client.ListWorkspaces(ctx, &localv1.ListWorkspacesRequest{MachineId: vps.MachineId, Cached: cached})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	rows := map[string]*machinev1.Workspace{}
	for _, r := range list(false).Workspaces {
		rows[r.SessionId] = r
	}
	if len(rows) != 2 || rows[ids[0]] == nil || rows[ids[1]] == nil {
		t.Fatalf("listed: %v", rows)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(rows[ids[0]].Path, "node_modules", "dep", "index.js"), strings.Repeat("x", 1<<20))
	write(filepath.Join(rows[ids[1]].Path, ".env"), "TOKEN=keep")

	after := list(false)
	for _, r := range after.Workspaces {
		switch r.SessionId {
		case ids[0]:
			if r.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_UNSPECIFIED || r.SizeBytes < 1<<20 || r.LastActive == nil || r.Title == "" || r.ProjectId != "app" {
				t.Fatalf("releasable: %v", r)
			}
		case ids[1]:
			if r.Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES || r.BlockPath != ".env" {
				t.Fatalf("kept: %v", r)
			}
		}
	}
	if after.Workspaces[0].SessionId != ids[0] {
		t.Fatal("not largest first")
	}

	res, err := w.app.client.ReleaseWorkspaces(ctx, &localv1.ReleaseWorkspacesRequest{MachineId: vps.MachineId, SessionIds: ids})
	if err != nil || len(res.Results) != 2 {
		t.Fatalf("release: %v %v", res, err)
	}
	if !res.Results[0].Released || res.Results[1].Released || res.Results[1].Block != machinev1.WorkspaceBlock_WORKSPACE_BLOCK_LOCAL_FILES {
		t.Fatalf("results: %v", res.Results)
	}
	if _, err := os.Stat(rows[ids[0]].Path); !os.IsNotExist(err) {
		t.Fatalf("released checkout still there: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(rows[ids[1]].Path, ".env")); err != nil || string(b) != "TOKEN=keep" {
		t.Fatalf(".env: %q %v", b, err)
	}
	if got := list(true).Workspaces; len(got) != 1 || got[0].SessionId != ids[1] {
		t.Fatalf("cached after release: %v", got)
	}

	// Auto-release, a setting of that machine.
	if _, err := w.app.client.SetWorkspacePolicy(ctx, &localv1.SetWorkspacePolicyRequest{MachineId: vps.MachineId, AutoRelease: true}); err != nil {
		t.Fatal(err)
	}
	if !list(true).AutoRelease {
		t.Fatal("auto-release not on")
	}

	// The released session goes on: its next message checks it out again.
	if r := w.cli(time.Minute, "message", "send", "--to", ids[0], "--wait", "--message", "say again"); r.code != 0 {
		t.Fatalf("send: %+v", r)
	}
	if _, err := os.Stat(rows[ids[0]].Path); err != nil {
		t.Fatalf("not checked out again: %v", err)
	}

	// This Mac's own machine answers the same way, in process.
	if s, r := w.startSession(time.Minute, "--agent", "claude", "--wait", "--prompt", "say here"); r.code != 0 || s.Status != "idle" {
		t.Fatalf("local session: %+v", r)
	}
	ms, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms.Machines {
		if m.MachineName != "owner-mac" {
			continue
		}
		local, err := w.app.client.ListWorkspaces(ctx, &localv1.ListWorkspacesRequest{MachineId: m.MachineId})
		if err != nil || len(local.Workspaces) != 1 || local.Workspaces[0].ProjectId != "demo" {
			t.Fatalf("this Mac: %v %v", local, err)
		}
	}
}
