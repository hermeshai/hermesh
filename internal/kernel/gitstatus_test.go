package kernel

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestEngineGitStatusTurnSettleAndRefreshPreservesOrder(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	owner := OwnerPrincipal("dev_owner", "Owner")

	repo := initGitProject(t)
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(repo, "init", "--bare", "-b", "main", origin)
	git(repo, "remote", "add", "origin", origin)
	git(repo, "push", "-u", "origin", "main")

	if err := eng.SyncWorkspace("proj", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil); err != nil {
		t.Fatal(err)
	}

	waitIdle := func(id string) *sessionv1.Session {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			proj, err := eng.GetSession(ctx, owner, id)
			if err != nil {
				t.Fatal(err)
			}
			if proj.Session.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
				return proj.Session
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s did not settle: status=%v", id, proj.Session.Status)
			}
			time.Sleep(15 * time.Millisecond)
		}
	}

	// 1. Pure Q&A session on worktree -> CLEAN.
	qaStart, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "proj",
		InitialPrompt: "say pure answer",
	})
	if err != nil {
		t.Fatal(err)
	}
	qaSess := waitIdle(qaStart.Session.SessionId)
	if qaSess.GetGitStatus().GetState() != sessionv1.GitState_GIT_STATE_CLEAN {
		t.Fatalf("Q&A session git_status = %+v, want CLEAN", qaSess.GetGitStatus())
	}

	// 2. Session that writes a file -> DIRTY on turn settle; external commit & push -> UNPUSHED then PUSHED via RefreshGitStatus without mutating UpdatedAt.
	codeStart, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "proj",
		InitialPrompt: "write feature.txt hello-git-status\nsay wrote it",
	})
	if err != nil {
		t.Fatal(err)
	}
	codeSess := waitIdle(codeStart.Session.SessionId)
	if codeSess.GetGitStatus().GetState() != sessionv1.GitState_GIT_STATE_DIRTY || codeSess.GetGitStatus().GetDirtyFiles() != 1 {
		t.Fatalf("dirty session git_status = %+v, want DIRTY (1 file)", codeSess.GetGitStatus())
	}
	beforeUpdated := codeSess.UpdatedAt.AsTime()
	wt := codeSess.GetWorkspaceStrategy().GetWorktreePath()
	branch := codeSess.GetWorkspaceStrategy().GetBranch()

	git(wt, "-c", "user.name=T", "-c", "user.email=t@h.local", "add", "feature.txt")
	git(wt, "-c", "user.name=T", "-c", "user.email=t@h.local", "commit", "-m", "feat: ship feature")

	time.Sleep(20 * time.Millisecond)
	ref1, err := eng.RefreshGitStatus(ctx, owner, []string{codeSess.SessionId})
	if err != nil || len(ref1.Sessions) != 1 {
		t.Fatalf("RefreshGitStatus unpushed: %v, %+v", err, ref1)
	}
	if ref1.Sessions[0].GetGitStatus().GetState() != sessionv1.GitState_GIT_STATE_UNPUSHED || ref1.Sessions[0].GetGitStatus().GetCommitsAhead() != 1 {
		t.Fatalf("after commit git_status = %+v, want UNPUSHED (1 ahead)", ref1.Sessions[0].GetGitStatus())
	}
	if !ref1.Sessions[0].UpdatedAt.AsTime().Equal(beforeUpdated) {
		t.Fatalf("RefreshGitStatus mutated UpdatedAt: got %v, want %v", ref1.Sessions[0].UpdatedAt.AsTime(), beforeUpdated)
	}

	git(wt, "push", "origin", branch)
	ref2, err := eng.RefreshGitStatus(ctx, owner, []string{codeSess.SessionId})
	if err != nil || len(ref2.Sessions) != 1 {
		t.Fatalf("RefreshGitStatus pushed: %v, %+v", err, ref2)
	}
	if ref2.Sessions[0].GetGitStatus().GetState() != sessionv1.GitState_GIT_STATE_PUSHED || ref2.Sessions[0].GetGitStatus().GetHeadSubject() != "feat: ship feature" {
		t.Fatalf("after push git_status = %+v, want PUSHED", ref2.Sessions[0].GetGitStatus())
	}
	if !ref2.Sessions[0].UpdatedAt.AsTime().Equal(beforeUpdated) {
		t.Fatalf("RefreshGitStatus mutated UpdatedAt on push: got %v, want %v", ref2.Sessions[0].UpdatedAt.AsTime(), beforeUpdated)
	}

	// 3. In-place Q&A session stays CLEAN even if a later commit is pushed in the shared repo folder.
	inPlaceQAStart, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "proj",
		Workspace:     &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace_WORKSPACE_IN_PLACE},
		InitialPrompt: "say just looking",
	})
	if err != nil {
		t.Fatal(err)
	}
	inPlaceQASess := waitIdle(inPlaceQAStart.Session.SessionId)
	if inPlaceQASess.GetGitStatus().GetState() != sessionv1.GitState_GIT_STATE_CLEAN {
		t.Fatalf("in-place Q&A initial git_status = %+v, want CLEAN", inPlaceQASess.GetGitStatus())
	}
	git(repo, "-c", "user.name=T", "-c", "user.email=t@h.local", "commit", "--allow-empty", "-m", "unrelated main commit")
	git(repo, "push", "origin", "main")
	ref3, err := eng.RefreshGitStatus(ctx, owner, []string{inPlaceQASess.SessionId})
	if err != nil || len(ref3.Sessions) != 1 {
		t.Fatalf("RefreshGitStatus in-place Q&A: %v, %+v", err, ref3)
	}
	if ref3.Sessions[0].GetGitStatus().GetState() != sessionv1.GitState_GIT_STATE_CLEAN {
		t.Fatalf("in-place Q&A after external push got %+v, want CLEAN", ref3.Sessions[0].GetGitStatus())
	}
}
