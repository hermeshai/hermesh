package kernel

import (
	"context"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestParseShellShortcut(t *testing.T) {
	owner := &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_OWNER, Id: "owner"}
	ext := &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: "cl_1"}

	if cmd, ok := ParseShellShortcut(owner, "! git status -sb"); !ok || cmd != "git status -sb" {
		t.Fatalf("expected git status -sb, got ok=%v cmd=%q", ok, cmd)
	}
	if cmd, ok := ParseShellShortcut(owner, "  !echo hi  "); !ok || cmd != "echo hi" {
		t.Fatalf("expected echo hi, got ok=%v cmd=%q", ok, cmd)
	}
	if _, ok := ParseShellShortcut(owner, "![diagram](foo.png)"); ok {
		t.Fatalf("markdown image must not match shell shortcut")
	}
	if _, ok := ParseShellShortcut(owner, "!   "); ok {
		t.Fatalf("empty bang must not match")
	}
	if _, ok := ParseShellShortcut(ext, "! rm -rf /"); ok {
		t.Fatalf("non-owner actor must never trigger shell shortcut")
	}
}

func TestOwnerShellShortcutRunsInWorktreeStreamsBlobAndWakesAgent(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("uses POSIX sh syntax in test command")
	}
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	if err := eng.SyncWorkspace("shproj", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "Shell repo", nil); err != nil {
		t.Fatal(err)
	}
	owner := OwnerPrincipal("dev_owner", "Owner")

	// Start a worktree session directly with a `! <cmd>` shortcut that writes a file and prints output.
	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "shproj",
		InitialPrompt: "! printf 'wt_marker_ok\\n' > from_bang.txt && pwd && cat from_bang.txt",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sid := launched.Session.SessionId

	proj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	wtPath := proj.Session.GetWorkspaceStrategy().GetWorktreePath()
	if wtPath == "" || wtPath == repo {
		t.Fatalf("expected isolated worktree path, got %q (repo=%q)", wtPath, repo)
	}
	// Verify file was written in the session's worktree, NOT in the base repo.
	if _, err := os.Stat(filepath.Join(wtPath, "from_bang.txt")); err != nil {
		t.Fatalf("expected from_bang.txt in worktree %s: %v", wtPath, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "from_bang.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected base repo %s to stay untouched", repo)
	}

	var cmdItem, replyItem *sessionv1.TurnItem
	for _, it := range proj.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION {
			cmdItem = it
		}
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			replyItem = it
		}
	}
	if cmdItem == nil || cmdItem.Status != "completed" || cmdItem.ExitCode != 0 || !strings.Contains(cmdItem.Output, "wt_marker_ok") {
		t.Fatalf("expected completed COMMAND_EXECUTION item with wt_marker_ok, got %+v", cmdItem)
	}
	if replyItem == nil || !strings.Contains(replyItem.Text, "Reviewed") || !strings.Contains(replyItem.Text, "Exit code: 0") {
		t.Fatalf("expected coding agent to review command output automatically, got %+v", replyItem)
	}
	// Verify checkpoint captured the file created by `! <cmd>`.
	if len(proj.Checkpoints) < 2 {
		t.Fatalf("expected baseline + turn checkpoint, got %d", len(proj.Checkpoints))
	}

	// Dispatch a command with >16KB output and non-zero exit code; verify blob spill & failed status.
	_, err = eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "! awk 'BEGIN{for(i=0;i<1200;i++) printf \"line-%04d-0123456789abcdef\\n\", i; exit 7}'",
	})
	if err != nil {
		t.Fatalf("DispatchMessage large failed command: %v", err)
	}
	proj2 := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	var bigCmd *sessionv1.TurnItem
	for _, it := range proj2.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION && strings.HasPrefix(it.Command, "awk ") {
			bigCmd = it
		}
	}
	if bigCmd == nil || bigCmd.Status != "failed" || bigCmd.ExitCode != 7 || !bigCmd.OutputTruncated || bigCmd.OutputBlobSha256 == "" {
		t.Fatalf("expected failed truncated command with blob sha, got %+v", bigCmd)
	}
	blobData, _, _, err := eng.Store().GetBlob(bigCmd.OutputBlobSha256)
	if err != nil || !strings.Contains(string(blobData), "line-1199-") {
		t.Fatalf("expected full output in blob store, err=%v len=%d", err, len(blobData))
	}
}

func TestOwnerShellShortcutInterruptKillsProcessAndSkipsAgent(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	if err := eng.SyncWorkspace("intproj", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "Interrupt repo", nil); err != nil {
		t.Fatal(err)
	}
	owner := OwnerPrincipal("dev_owner", "Owner")

	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "intproj",
		InitialPrompt: "! echo started_sleeping && sleep 30",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sid := launched.Session.SessionId

	// Wait until streaming output shows `started_sleeping` while command is still running.
	deadline := time.Now().Add(5 * time.Second)
	var sawRunning bool
	for time.Now().Before(deadline) {
		p, _ := eng.GetSession(ctx, owner, sid)
		for _, it := range p.GetItems() {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION && it.Status == "running" && strings.Contains(it.Output, "started_sleeping") {
				sawRunning = true
				break
			}
		}
		if sawRunning {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !sawRunning {
		t.Fatalf("expected streaming output while shell command was running")
	}

	if _, err := eng.InterruptSession(ctx, owner, &sessionv1.InterruptSessionRequest{SessionId: sid}); err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}
	proj := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	for _, it := range proj.Items {
		if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
			t.Fatalf("interrupted shell command must not wake coding agent, got assistant message %q", it.Text)
		}
	}
}

func TestRunCommandStreamsMultilineScriptSilentlyAndCancelsCleanly(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("uses POSIX sh script")
	}
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()

	repo := initGitProject(t)
	if err := eng.SyncWorkspace("rcproj", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "RunCommand repo", nil); err != nil {
		t.Fatal(err)
	}
	owner := OwnerPrincipal("dev_owner", "Owner")

	launched, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:     "rcproj",
		InitialPrompt: "say ready",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sid := launched.Session.SessionId
	before := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	runsBefore := len(before.Runs)
	itemsBefore := len(before.Items)

	// 1. Multi-line script with shebang runs in worktree, streams output, and adds zero Runs or TurnItems!
	script := "#!/usr/bin/env bash\ndir=\"${1:-.}\"\nif [ ! -d \"$dir\" ]; then exit 1; fi\nprintf 'inline_multiline_ok\\n'"
	var out strings.Builder
	var doneEv *sessionv1.RunCommandEvent
	err = eng.RunCommand(ctx, owner, &sessionv1.RunCommandRequest{
		SessionId: sid,
		Command:   script,
	}, func(ev *sessionv1.RunCommandEvent) error {
		out.WriteString(ev.OutputDelta)
		if ev.Done {
			doneEv = ev
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunCommand failed: %v", err)
	}
	if doneEv == nil || !doneEv.Done || doneEv.ExitCode != 0 || !strings.Contains(out.String(), "inline_multiline_ok") {
		t.Fatalf("unexpected RunCommand result: done=%+v out=%q", doneEv, out.String())
	}

	after, _ := eng.GetSession(ctx, owner, sid)
	if len(after.Runs) != runsBefore || len(after.Items) != itemsBefore {
		t.Fatalf("RunCommand must not create Runs or TurnItems! runs %d->%d, items %d->%d", runsBefore, len(after.Runs), itemsBefore, len(after.Items))
	}

	// 2. Non-owner grant is denied.
	grant := &Principal{Actor: &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: "ext"}}
	if err := eng.RunCommand(ctx, grant, &sessionv1.RunCommandRequest{SessionId: sid, Command: "echo nope"}, func(*sessionv1.RunCommandEvent) error { return nil }); err == nil {
		t.Fatalf("expected non-owner RunCommand to be denied")
	}

	// 3. Cancelling stream context kills long-running child process promptly.
	cCtx, cancel := context.WithCancel(ctx)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- eng.RunCommand(cCtx, owner, &sessionv1.RunCommandRequest{
			SessionId: sid,
			Command:   "echo started_stream && sleep 30",
		}, func(ev *sessionv1.RunCommandEvent) error {
			if strings.Contains(ev.OutputDelta, "started_stream") {
				select {
				case <-started:
				default:
					close(started)
				}
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for first chunk")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("RunCommand did not terminate promptly on stream cancel")
	}
}
