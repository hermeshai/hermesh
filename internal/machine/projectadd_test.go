package machine

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
)

func addProject(n *Machine, p *identityv1.AddProject) error {
	_, err := n.execAdmin(context.Background(), &identityv1.AdminCommand{Command: &identityv1.AdminCommand_AddProject{AddProject: p}})
	return err
}

// Adding a project again with its own folder changes its settings (the apps
// do this to change its way of working); pointing the name at
// another folder is refused, since every grant naming it would follow.
func TestAddProjectKeepsItsFolder(t *testing.T) {
	n, err := Open(Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{}})
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
	if !p.InPlace() {
		t.Fatalf("settings not changed: %+v", p)
	}
	// The agents an older device sends are ignored: the project is for every provider (docs/settings-environments.md D5).
	if ws, err := n.sessions.Store().Workspace("app"); err != nil || len(ws.Agents) != 0 {
		t.Fatalf("the sessions' workspace is limited to %v (%v)", ws.Agents, err)
	}

	err = addProject(n, &identityv1.AddProject{ProjectId: "app", Path: b})
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("pointing app at another folder: %v", err)
	}
	if p, _ := n.st.Project("app"); !samePath(p.Path, a) {
		t.Fatalf("the project moved to %s", p.Path)
	}
	if ws, err := n.sessions.Store().Workspace("app"); err != nil || !samePath(ws.Path, a) {
		t.Fatalf("the sessions' workspace moved: %+v %v", ws, err)
	}
}

// The sessions' temporary workspace is "scratch": no project may take the
// name, and removing a project of that name leaves the workspace alone.
func TestScratchIsReserved(t *testing.T) {
	n, err := Open(Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	before, err := n.sessions.Store().Workspace(kernel.ScratchProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := addProject(n, &identityv1.AddProject{ProjectId: kernel.ScratchProjectID, Path: testRepo(t)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a project named scratch: %v", err)
	}
	if _, err := n.execAdmin(context.Background(), &identityv1.AdminCommand{Command: &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: kernel.ScratchProjectID}}}); err != nil {
		t.Fatal(err)
	}
	after, err := n.sessions.Store().Workspace(kernel.ScratchProjectID)
	if err != nil || after.Path != before.Path {
		t.Fatalf("scratch workspace after removing a project of its name: %+v %v", after, err)
	}
}

// A project is for every provider in the sessions' kernel too (docs/settings-environments.md D5): the agents an
// older device sends on adding it are not kept, and a limit the kernel kept from before goes when the machine starts.
func TestProjectAgentsReachSessions(t *testing.T) {
	dir := t.TempDir()
	n, err := Open(Config{DataDir: dir, Discoverers: map[string]AgentDiscoverer{}})
	if err != nil {
		t.Fatal(err)
	}
	repo := testRepo(t)
	if err := addProject(n, &identityv1.AddProject{ProjectId: "lim", Path: repo, Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := n.sessions.Store().Workspace("lim"); len(ws.Agents) != 0 {
		t.Fatalf("on adding: %q", ws.Agents)
	}
	if err := n.sessions.SyncWorkspace("lim", repo, "HEAD", identityv1.Workspace_WORKSPACE_WORKTREE, "", []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	n.Close()
	n, err = Open(Config{DataDir: dir, Discoverers: map[string]AgentDiscoverer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if ws, _ := n.sessions.Store().Workspace("lim"); len(ws.Agents) != 0 {
		t.Fatalf("after a restart: %q", ws.Agents)
	}
}
