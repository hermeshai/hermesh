package machine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/provider"
)

// A folder made for a project: under the home folder only, one plain name,
// never over what is there; with git, one a session can take a worktree of.
func TestCreateFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx := context.Background()
	os.Mkdir(filepath.Join(home, "code"), 0o755)

	path, err := CreateFolder(ctx, &identityv1.CreateFolder{Parent: "~/code", Name: "app", GitInit: true})
	if err != nil || path != "~/code/app" {
		t.Fatalf("made %q, %v", path, err)
	}
	if _, err := ValidateRepo(ctx, filepath.Join(home, "code", "app")); err != nil {
		t.Fatalf("not a repository: %v", err)
	}
	if _, err := git(ctx, filepath.Join(home, "code", "app"), "rev-parse", "--verify", "HEAD"); err != nil {
		t.Fatalf("no first commit to take worktrees from: %v", err)
	}
	if path, err := CreateFolder(ctx, &identityv1.CreateFolder{Name: "notes"}); err != nil || path != "~/notes" || isRepoTop(filepath.Join(home, "notes")) {
		t.Fatalf("a plain folder in the home folder: %q %v", path, err)
	}
	for _, c := range []struct {
		f    *identityv1.CreateFolder
		code connect.Code
	}{
		{&identityv1.CreateFolder{Parent: "~/code", Name: "app"}, connect.CodeAlreadyExists},
		{&identityv1.CreateFolder{Parent: "~/code", Name: "a/b"}, connect.CodeInvalidArgument},
		{&identityv1.CreateFolder{Parent: "~/code", Name: ".hidden"}, connect.CodeInvalidArgument},
		{&identityv1.CreateFolder{Parent: "~/code", Name: ".."}, connect.CodeInvalidArgument},
		{&identityv1.CreateFolder{Parent: "~/missing", Name: "x"}, connect.CodeNotFound},
		{&identityv1.CreateFolder{Parent: "code", Name: "x"}, connect.CodeInvalidArgument},
		{&identityv1.CreateFolder{Parent: t.TempDir(), Name: "x"}, connect.CodePermissionDenied},
	} {
		if _, err := CreateFolder(ctx, c.f); connect.CodeOf(err) != c.code {
			t.Errorf("%v: %v, want %v", c.f, err, c.code)
		}
	}
	// A link under the home folder to elsewhere doesn't take a folder out of it.
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(home, "out"))
	if _, err := CreateFolder(ctx, &identityv1.CreateFolder{Parent: "~/out", Name: "x"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("through a link out of the home folder: %v", err)
	}
}

// The machine's own permission-mode limit: in its status, on its threads at
// once (launches clamp to it), and kept across a restart. Its status says it
// serves threads, and each project whether its folder is a git repository.
func TestMachineLimitAndStatus(t *testing.T) {
	dir := t.TempDir()
	n, err := Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if st := n.Status(); !st.Threads || st.MaxRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		t.Fatalf("status before: threads %v, limit %v", st.Threads, st.MaxRuntimeMode)
	}
	set := func(m identityv1.RuntimeMode) error {
		_, err := n.execAdmin(ctx, &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetMaxRuntimeMode{SetMaxRuntimeMode: &identityv1.SetMaxRuntimeMode{Mode: m}}})
		return err
	}
	if err := set(identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS); err != nil {
		t.Fatal(err)
	}
	if err := set(identityv1.RuntimeMode(99)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an unknown mode: %v", err)
	}
	if st := n.Status(); st.MaxRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("status after: %v", st.MaxRuntimeMode)
	}
	owner := kernel.OwnerPrincipal("o", "O")
	l, err := n.threads.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{Title: "x", RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS})
	if err != nil || l.Thread.EffectiveRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS || l.Thread.MaxRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("a launch under the limit: %+v %v", l.GetThread(), err)
	}

	repo, plain := testRepo(t), t.TempDir()
	if err := addProject(n, &identityv1.AddProject{ProjectId: "inplace", Path: repo, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE}); err != nil {
		t.Fatal(err)
	}
	if err := addProject(n, &identityv1.AddProject{ProjectId: "plain", Path: plain}); err != nil {
		t.Fatal(err)
	}
	res, err := (&machineService{n: n}).projects(ctx, &Principal{Kind: PrincipalOwner}, false)
	if err != nil {
		t.Fatal(err)
	}
	repoOf := map[string]bool{}
	for _, p := range res.Projects {
		repoOf[p.ProjectId] = p.Repo
	}
	if !repoOf["inplace"] || repoOf["plain"] {
		t.Fatalf("repo flags %v", repoOf)
	}
	n.Close()

	n, err = Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	l, err = n.threads.LaunchThread(ctx, owner, &threadv2.LaunchThreadRequest{Title: "y", RuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS})
	if err != nil || l.Thread.EffectiveRuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS {
		t.Fatalf("after a restart: %v %v", l.GetThread().GetEffectiveRuntimeMode(), err)
	}
}
