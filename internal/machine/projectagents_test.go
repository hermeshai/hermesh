package machine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

// A project is for every agent provider (docs/settings-environments.md §5.6):
// the coding agents an older device sends with it are ignored, and a
// session of any provider starts there.
func TestProjectsAreForEveryProvider(t *testing.T) {
	h := newHarness(t, Limits{})
	ctx := context.Background()
	proj, _ := h.n.st.Project("demo")
	if _, err := h.n.ApplySettings(ctx, &identityv1.AdminCommand{OwnerId: h.n.OwnerID(), CommandId: "p1", TargetMachineId: h.n.machineID,
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
		Command:   &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: "demo", Path: proj.Path, Agents: []string{"claude"}}}}); err != nil {
		t.Fatal(err)
	}
	res, err := (&machineService{n: h.n}).ListProjects(context.WithValue(ctx, principalKey{}, h.p), &machinev1.ListProjectsRequest{})
	if err != nil || len(res.Projects) != 1 || len(res.Projects[0].Agents) != 0 {
		t.Fatalf("listed: %v %v", res, err)
	}
	started, err := h.e.StartSession(ctx, h.kp(), &sessionv1.StartSessionRequest{
		ProjectId:      "demo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "fake"},
		InitialPrompt:  "hi",
	})
	if err != nil {
		t.Fatalf("a session of a provider the old device left out: %v", err)
	}
	h.wait(started.Session.SessionId, idle)
}

// Updating clears the coding agents projects were limited to.
func TestProjectAgentsMigrationClearsLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine.db")
	db, err := sqlitedb.Open(path, migrations[:len(migrations)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(project_id,path,default_base_ref,workspace,description,agents,created_at) VALUES('notes','/tmp/notes','HEAD',1,'','codex',0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var agents string
	if err := st.rdb.QueryRow(`SELECT agents FROM projects WHERE project_id='notes'`).Scan(&agents); err != nil || agents != "" {
		t.Fatalf("agents %q, %v", agents, err)
	}
	if p, err := st.Project("notes"); err != nil || p == nil || p.Path != "/tmp/notes" {
		t.Fatalf("project %+v, %v", p, err)
	}
}
