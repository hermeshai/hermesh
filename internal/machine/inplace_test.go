package machine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func (h *harness) inPlaceProject(id, dir string) {
	h.t.Helper()
	if err := h.n.st.PutProject(Project{ID: id, Path: dir, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.e.SyncWorkspace(id, dir, "", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) startIn(project, prompt string) string {
	h.t.Helper()
	res, err := h.e.StartSession(context.Background(), h.kp(), &sessionv1.StartSessionRequest{
		ProjectId:      project,
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "fake"},
		InitialPrompt:  prompt,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Session.SessionId
}

// An in-place session works in the directory itself, like the agent run
// there by hand: no worktree, no snapshot, no diff of its own when not a git repo.
func TestInPlaceSessionWorksInTheDirectory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, DefaultLimits())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("mine\n"), 0o644)
	h.inPlaceProject("notes", dir)

	if _, err := h.e.StartSession(ctx, h.kp(), &sessionv1.StartSessionRequest{
		ProjectId:      "notes",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "fake"},
		InitialPrompt:  "say x",
		Workspace:      &sessionv1.WorkspaceStrategy{BaseRef: "HEAD"},
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a base ref for an in-place project: %v", err)
	}
	id := h.startIn("notes", "write todo.txt buy milk\nsay done")
	sess := h.wait(id, idle)
	if b, _ := os.ReadFile(filepath.Join(dir, "todo.txt")); string(b) != "buy milk\n" {
		t.Fatalf("todo.txt in the directory: %q", b)
	}
	if sess.Workspace != identityv1.Workspace_WORKSPACE_IN_PLACE || sess.Branch != "" || h.worktreeExists(id) {
		t.Fatalf("session: %v (worktree exists: %v)", sess, h.worktreeExists(id))
	}
	if h.rec.spec(-1).WorkDir != dir {
		t.Fatalf("agent ran in %s", h.rec.spec(-1).WorkDir)
	}
	if ckpts, _ := h.e.ListCheckpoints(ctx, h.kp(), id); len(ckpts) != 0 || sess.GetProgress().GetDiffStat() != nil {
		t.Fatalf("an in-place plain-directory turn has no snapshot: ckpts=%v stat=%v", ckpts, sess.GetProgress().GetDiffStat())
	}
	if _, err := h.e.GetDiff(ctx, h.kp(), &sessionv1.GetDiffRequest{SessionId: id}); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "works in place") {
		t.Fatalf("diff: %v", err)
	}

	// Archiving frees a worktree but never touches an in-place directory
	// (a worktree session archived alongside shows the cleanup ran).
	wt := h.start("say w")
	h.wait(wt, idle)
	for _, s := range []string{id, wt} {
		if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: s, HasArchived: true, Archived: true}); err != nil {
			t.Fatal(err)
		}
	}
	if h.worktreeExists(wt) {
		t.Fatal("the worktree session was not cleaned up")
	}
	for _, f := range []string{"readme.md", "todo.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("archiving removed %s: %v", f, err)
		}
	}
	// Unarchived, it works in the same directory again.
	if _, err := h.e.UpdateSession(ctx, h.kp(), &sessionv1.UpdateSessionRequest{SessionId: id, HasArchived: true, Archived: false}); err != nil {
		t.Fatal(err)
	}
	h.send(id, "write later.txt after\nsay done")
	h.wait(id, idle)
	if b, _ := os.ReadFile(filepath.Join(dir, "later.txt")); string(b) != "after\n" {
		t.Fatalf("after unarchive: %q", b)
	}
}

// In a git repository worked in place Hermesh makes no branch and no
// branch commit: the changes are left in the owner's working tree.
func TestInPlaceGitRepoGetsNoHermeshCommits(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	repo := testRepo(t)
	before := gitOut(t, repo, "rev-parse", "HEAD")
	h.inPlaceProject("app", repo)
	id := h.startIn("app", "write feature.txt new\nsay done")
	h.wait(id, idle)
	if after := gitOut(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved: %s -> %s", before, after)
	}
	if b := gitOut(t, repo, "branch", "--list", "hermesh/*"); strings.TrimSpace(b) != "" {
		t.Fatalf("branches: %q", b)
	}
	if st := gitOut(t, repo, "status", "--porcelain"); !strings.Contains(st, "?? feature.txt") {
		t.Fatalf("status: %q", st)
	}
}

// Adding a project picks its mode: a worktree for a git repository root,
// in place for any other directory; a worktree needs git.
func TestProjectWorkspaceMode(t *testing.T) {
	ctx := context.Background()
	repo := testRepo(t)
	sub := filepath.Join(repo, "docs")
	os.Mkdir(sub, 0o755)
	plain := t.TempDir()
	for _, c := range []struct {
		mode identityv1.Workspace
		path string
		want identityv1.Workspace
		err  string
	}{
		{identityv1.Workspace_WORKSPACE_UNSPECIFIED, repo, identityv1.Workspace_WORKSPACE_WORKTREE, ""},
		{identityv1.Workspace_WORKSPACE_UNSPECIFIED, plain, identityv1.Workspace_WORKSPACE_IN_PLACE, ""},
		{identityv1.Workspace_WORKSPACE_UNSPECIFIED, sub, identityv1.Workspace_WORKSPACE_IN_PLACE, ""},
		{identityv1.Workspace_WORKSPACE_IN_PLACE, repo, identityv1.Workspace_WORKSPACE_IN_PLACE, ""},
		{identityv1.Workspace_WORKSPACE_WORKTREE, plain, 0, "not a git repository"},
		{identityv1.Workspace_WORKSPACE_UNSPECIFIED, filepath.Join(plain, "missing"), 0, "does not exist"},
	} {
		got, _, err := projectWorkspace(ctx, c.mode, c.path)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("%v %s: got %v, want error %q", c.mode, c.path, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%v %s: got %v %v, want %v", c.mode, c.path, got, err, c.want)
		}
	}
}
