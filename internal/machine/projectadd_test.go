package machine

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/provider"
)

func addProject(n *Machine, p *identityv1.AddProject) error {
	_, err := n.execAdmin(context.Background(), &identityv1.AdminCommand{Command: &identityv1.AdminCommand_AddProject{AddProject: p}})
	return err
}

// Adding a project again with its own folder changes its settings (the apps
// do this to change its agents or way of working); pointing the name at
// another folder is refused, since every grant naming it would follow.
func TestAddProjectKeepsItsFolder(t *testing.T) {
	n, err := Open(Config{DataDir: t.TempDir(), Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	a, b := testRepo(t), testRepo(t)

	if err := addProject(n, &identityv1.AddProject{ProjectId: "app", Path: a}); err != nil {
		t.Fatal(err)
	}
	if err := addProject(n, &identityv1.AddProject{ProjectId: "app", Path: a, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE, Agents: []string{"claude"}}); err != nil {
		t.Fatalf("changing the project's settings: %v", err)
	}
	p, _ := n.st.Project("app")
	if !p.InPlace() || !slices.Equal(p.Agents, []string{"claude"}) {
		t.Fatalf("settings not changed: %+v", p)
	}

	err = addProject(n, &identityv1.AddProject{ProjectId: "app", Path: b})
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("pointing app at another folder: %v", err)
	}
	if p, _ := n.st.Project("app"); !samePath(p.Path, a) {
		t.Fatalf("the project moved to %s", p.Path)
	}
	if ws, err := n.threads.Store().Workspace("app"); err != nil || !samePath(ws.Path, a) {
		t.Fatalf("the threads' workspace moved: %+v %v", ws, err)
	}
}

// The threads' temporary workspace is "scratch": no project may take the
// name, and removing a project of that name leaves the workspace alone.
func TestScratchIsReserved(t *testing.T) {
	n, err := Open(Config{DataDir: t.TempDir(), Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	before, err := n.threads.Store().Workspace(kernel.ScratchProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := addProject(n, &identityv1.AddProject{ProjectId: kernel.ScratchProjectID, Path: testRepo(t)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a project named scratch: %v", err)
	}
	if _, err := n.execAdmin(context.Background(), &identityv1.AdminCommand{Command: &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: kernel.ScratchProjectID}}}); err != nil {
		t.Fatal(err)
	}
	after, err := n.threads.Store().Workspace(kernel.ScratchProjectID)
	if err != nil || after.Path != before.Path {
		t.Fatalf("scratch workspace after removing a project of its name: %+v %v", after, err)
	}
}

// The agents a project is set up for reach the threads' kernel, on adding it
// and when the machine starts.
func TestProjectAgentsReachThreads(t *testing.T) {
	dir := t.TempDir()
	n, err := Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := addProject(n, &identityv1.AddProject{ProjectId: "lim", Path: testRepo(t), Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := n.threads.Store().Workspace("lim"); !slices.Equal(ws.Agents, []string{"claude"}) {
		t.Fatalf("on adding: %q", ws.Agents)
	}
	n.Close()
	n, err = Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if ws, _ := n.threads.Store().Workspace("lim"); !slices.Equal(ws.Agents, []string{"claude"}) {
		t.Fatalf("after a restart: %q", ws.Agents)
	}
}
