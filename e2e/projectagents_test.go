package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestProjectAgents: a project is for every agent provider
// (docs/settings-environments.md §5.6). Coding agents an older app still
// sends with it are ignored; which providers an agent may use is its
// access, and the CLI lists those for every project.
func TestProjectAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "HERMESH_CURSOR_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini", "cursor"}})
	ctx := context.Background()
	ms, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t, map[string]string{"README.md": "# site\n"})
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: ms.Machines[0].MachineId, ProjectId: "site", Path: repo, Agents: []string{"gemini"}}); err != nil {
		t.Fatal(err)
	}

	// The owner sees no agents on it, and where it is.
	ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps.Projects {
		if p.ProjectId == "site" && (len(p.Agents) != 0 || p.Path == "") {
			t.Fatalf("owner's view of site: %+v", p)
		}
	}

	// The agent CLI: both projects take every provider its access allows.
	r := w.cli(30*time.Second, "project", "list", "-o", "json")
	var doc struct {
		Projects []struct {
			Project string
			Agents  []struct{ Name string }
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("project list: %v\n%+v", err, r)
	}
	agents := map[string][]string{}
	for _, p := range doc.Projects {
		for _, a := range p.Agents {
			agents[p.Project] = append(agents[p.Project], a.Name)
		}
		slices.Sort(agents[p.Project])
	}
	if !slices.Equal(agents["owner-mac/demo"], []string{"cursor", "gemini"}) || !slices.Equal(agents["owner-mac/site"], []string{"cursor", "gemini"}) {
		t.Fatalf("agents by project: %v", agents)
	}

	// Cursor, left out by the old app, works in site.
	if r := w.cli(time.Minute, "session", "start", "--project", "site", "--agent", "cursor", "--wait", "--prompt", "say from cursor"); r.code != 0 {
		t.Fatalf("cursor in site: %+v", r)
	}
	if r := w.cli(time.Minute, "session", "start", "--project", "site", "--agent", "gemini", "--wait", "--prompt", "say from gemini"); r.code != 0 {
		t.Fatalf("gemini in site: %+v", r)
	}
	// Its access still limits it: Claude is not in it.
	if r := w.cli(time.Minute, "session", "start", "--project", "site", "--agent", "claude", "--prompt", "say x"); r.code == 0 {
		t.Fatalf("claude outside the access: %+v", r)
	}
}
