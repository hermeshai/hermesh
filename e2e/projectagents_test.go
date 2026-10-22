package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestProjectAgents: the owner sets a project up for some coding agents.
// The agent CLI lists only those for it, and the machine refuses a session
// of any other there; a project set up for none takes all.
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
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: ms.Machines[0].MachineId, ProjectId: "bad", Path: repo, Agents: []string{"vim"}}); err == nil {
		t.Fatal("a project for an unknown agent was added")
	}

	// The owner sees what each project is for, and where it is.
	ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps.Projects {
		if p.ProjectId == "site" && (!slices.Equal(p.Agents, []string{"gemini"}) || p.Path == "") {
			t.Fatalf("owner's view of site: %+v", p)
		}
	}

	// The agent CLI: demo is for all (gemini and cursor here), site for gemini.
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
	}
	slices.Sort(agents["owner-mac/demo"])
	if !slices.Equal(agents["owner-mac/demo"], []string{"cursor", "gemini"}) || !slices.Equal(agents["owner-mac/site"], []string{"gemini"}) {
		t.Fatalf("agents by project: %v", agents)
	}

	// Cursor is refused in site, and takes demo; Gemini takes site.
	if r := w.cli(time.Minute, "session", "start", "--project", "site", "--agent", "cursor", "--prompt", "say x"); r.code == 0 || !strings.Contains(r.stderr+r.stdout, "not set up for agent") {
		t.Fatalf("cursor in site: %+v", r)
	}
	if s, r := w.startSession(time.Minute, "--agent", "cursor", "--wait", "--prompt", "say from cursor"); r.code != 0 || s.Status != "idle" {
		t.Fatalf("cursor in demo: %+v", r)
	}
	if r := w.cli(time.Minute, "session", "start", "--project", "site", "--agent", "gemini", "--wait", "--prompt", "say from gemini"); r.code != 0 {
		t.Fatalf("gemini in site: %+v", r)
	}
}
