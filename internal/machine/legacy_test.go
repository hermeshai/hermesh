package machine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

// oldMachine is a machine's data folder as an earlier Hermesh left it: its
// machine store (this package's own migrations, so the tables are the ones
// the move reads) with one V1 session, and no kernel store yet.
func oldMachine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlitedb.Open(filepath.Join(dir, "hermeshd.db"), migrations)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Unix()
	if _, err := db.Exec(`INSERT INTO sessions(session_id, project_id, agent, status, created_by, grant_id, model, provider_ref, base_commit, branch, worktree, pgid, progress, error,
		created_at, updated_at, workspace, result, title) VALUES('s_old', 'app', 'claude', 4, 'c_her', 'g1', 'opus', '', '', 'hermesh/s_old', '', 0, x'', '', ?, ?, 1, 'Fixed it.', 'Fix the login form')`, at, at); err != nil {
		t.Fatalf("the machine store's sessions are not what the move reads: %v", err)
	}
	return dir
}

// A machine updated from before the kernel's store: the sessions it had are
// listed and open (a session that could not be read sent its page back to
// the overview). Nothing is wrong, so the machine says no problem.
func TestAnUpdatedMachineOpensItsEarlierSessions(t *testing.T) {
	dir := oldMachine(t)
	n, err := Open(Config{DataDir: dir, Discoverers: map[string]AgentDiscoverer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	s, err := n.GetSessionFor(context.Background(), &Principal{Kind: PrincipalOwner}, "s_old")
	if err != nil {
		t.Fatalf("the earlier session does not open: %v", err)
	}
	if s.Title != "Fix the login form" || s.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || s.Agent != "claude" || s.Result != "Fixed it." {
		t.Fatalf("the session: %v", s)
	}
	all, err := n.sessions.ListAllSessions(false)
	if err != nil || len(all) != 1 || all[0].SessionId != "s_old" {
		t.Fatalf("listed: %v, %v", all, err)
	}
	for _, p := range n.checkHealth() {
		if p.Kind == machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_DATA_MOVE {
			t.Fatalf("a move said to have failed: %v", p)
		}
	}
}

// A move that fails does not keep the machine from starting: it says so with
// its health, naming the move and why, until a start when it goes through.
func TestAMoveThatFailsIsTheMachinesProblem(t *testing.T) {
	dir := oldMachine(t)
	db, _ := sqlitedb.Open(filepath.Join(dir, "hermeshd.db"), migrations)
	if _, err := db.Exec(`UPDATE sessions SET progress = x'ffffffff'`); err != nil { // no message of the progress's kind
		t.Fatal(err)
	}
	db.Close()
	n, err := Open(Config{DataDir: dir, Discoverers: map[string]AgentDiscoverer{}})
	if err != nil {
		t.Fatalf("the machine did not start: %v", err)
	}
	var said *machinev1.MachineProblem
	for _, p := range n.checkHealth() {
		if p.Kind == machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_DATA_MOVE {
			said = p
		}
	}
	if said == nil || !strings.Contains(said.Detail, "v1-sessions") || !strings.Contains(said.Detail, "s_old") {
		t.Fatalf("the machine's problems: %v", n.checkHealth())
	}
	n.Close()

	// Mended, the next start moves it and the problem is gone.
	db, _ = sqlitedb.Open(filepath.Join(dir, "hermeshd.db"), migrations)
	db.Exec(`UPDATE sessions SET progress = x''`)
	db.Close()
	n, err = Open(Config{DataDir: dir, Discoverers: map[string]AgentDiscoverer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if _, err := n.GetSessionFor(context.Background(), &Principal{Kind: PrincipalOwner}, "s_old"); err != nil {
		t.Fatalf("after the mend: %v", err)
	}
	for _, p := range n.checkHealth() {
		if p.Kind == machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_DATA_MOVE {
			t.Fatalf("still said: %v", p)
		}
	}
}
