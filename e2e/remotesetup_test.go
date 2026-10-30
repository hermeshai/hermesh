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

// TestRemoteMachineSetup: what a trusted device does to set up a machine it
// is not on (docs/onboarding.md §5.5, §6.7). Check Again has the machine
// look at its coding agents now; Add a Project lists the git repositories
// under the machine's home folder (not those in dependency folders, not
// those deeper than three levels); Test runs one turn of a coding agent
// and shows its answer, keeping no session; and a project added in the
// machine's own shell, while it runs, shows up on the device; one added
// from the device as "~/…" is in the machine's home.
func TestRemoteMachineSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	ctx := context.Background()

	// vps-1, with a home of its own.
	home := t.TempDir()
	app := filepath.Join(home, "app")
	gitRepoAt(t, app, map[string]string{"README.md": "# app\n"})
	gitRepoAt(t, filepath.Join(home, "code", "api"), map[string]string{"README.md": "# api\n"})
	gitRepoAt(t, filepath.Join(home, "code", "node_modules", "dep"), map[string]string{"x": "y"})
	gitRepoAt(t, filepath.Join(home, "a", "b", "c", "deep"), map[string]string{"x": "y"})
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
	if out := pair.output(); !strings.Contains(out, "Coding agents") || !strings.Contains(out, "hermeshd project add") {
		t.Fatalf("pairing ends without the coding agents and the next step:\n%s", out)
	}
	w.logs = append(w.logs, start(t, "vps-1", vpsEnv, daemon, "run", "--data-dir", data))
	var vpsID string
	eventually(t, 30*time.Second, "vps-1 online", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineName == "vps-1" && m.Online {
				vpsID = m.MachineId
			}
		}
		return err == nil && vpsID != ""
	})

	// doctor and status in its shell ask the running machine (it holds its
	// data directory).
	doc := run(t, vpsEnv, 60*time.Second, daemon, "doctor", "--data-dir", data)
	if !strings.Contains(doc.stdout, `paired       yes, as "vps-1"`) || !strings.Contains(doc.stdout, "relay        connected") || !strings.Contains(doc.stdout, "claude") {
		t.Fatalf("doctor while it runs: %+v", doc)
	}
	if st := run(t, vpsEnv, 60*time.Second, daemon, "status", "--data-dir", data); st.code != 0 || !strings.Contains(st.stdout, `paired as "vps-1"`) || !strings.Contains(st.stdout, "connected") {
		t.Fatalf("status while it runs: %+v", st)
	}

	// Check Again reaches the machine and waits for it.
	if _, err := w.app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{MachineId: vpsID}); err != nil {
		t.Fatalf("check again: %v", err)
	}

	// The repositories it finds.
	repos, err := w.app.client.ListMachineRepos(ctx, &localv1.ListMachineReposRequest{MachineId: vpsID})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range repos.Repos {
		paths = append(paths, r.Path)
		if r.Path == "~/app" && (r.Branch != "main" || r.LastCommit == nil) {
			t.Fatalf("repo: %+v", r)
		}
	}
	if strings.Join(paths, " ") != "~/code/api ~/app" && strings.Join(paths, " ") != "~/app ~/code/api" {
		t.Fatalf("repositories: %v", paths)
	}

	// Test: one turn, no session kept.
	res, err := w.app.client.TestMachineAgent(ctx, &localv1.TestMachineAgentRequest{MachineId: vpsID, Agent: "claude"})
	if err != nil || !res.Ok || res.Reply == "" {
		t.Fatalf("test: %+v %v", res, err)
	}
	if s, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{}); err != nil || len(s.Sessions) != 0 {
		t.Fatalf("a test turn left a session: %v %v", s, err)
	}
	if _, err := w.app.client.TestMachineAgent(ctx, &localv1.TestMachineAgentRequest{MachineId: vpsID, Agent: "nope"}); err == nil {
		t.Fatal("a test of a coding agent the machine doesn't have")
	}

	// A project added in the machine's own shell, while it runs.
	add := run(t, vpsEnv, 30*time.Second, daemon, "project", "add", "~/app", "--data-dir", data)
	if add.code != 0 || !strings.Contains(add.stdout, "✓ Added project “app” (separate worktrees) for every coding agent") {
		t.Fatalf("project add: %+v", add)
	}
	real, _ := filepath.EvalSymlinks(app)
	eventually(t, 30*time.Second, "the project on the device", func() bool {
		ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{MachineId: vpsID})
		if err != nil || len(ps.Projects) != 1 {
			return false
		}
		got, _ := filepath.EvalSymlinks(ps.Projects[0].Path)
		return ps.Projects[0].ProjectId == "app" && got == real
	})
	if l := run(t, vpsEnv, 30*time.Second, daemon, "project", "list", "--data-dir", data); !strings.Contains(l.stdout, "app") {
		t.Fatalf("project list: %+v", l)
	}
	if r := run(t, vpsEnv, 30*time.Second, daemon, "project", "add", filepath.Join(home, "missing"), "--data-dir", data); r.code == 0 {
		t.Fatalf("a missing folder added: %+v", r)
	}
	if _, err := os.Stat(app); err != nil {
		t.Fatal(err)
	}

	// A repository found is added from the device as it was shown: ~/code/api.
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "api", Path: "~/code/api", Agents: []string{"claude"}}); err != nil {
		t.Fatalf("a project in the machine's home: %v", err)
	}
	api, _ := filepath.EvalSymlinks(filepath.Join(home, "code", "api"))
	eventually(t, 30*time.Second, "~/code/api in the machine's home", func() bool {
		ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{MachineId: vpsID})
		for _, p := range ps.GetProjects() {
			if got, _ := filepath.EvalSymlinks(p.Path); p.ProjectId == "api" && got == api {
				return true
			}
		}
		return err == nil && false
	})
}
