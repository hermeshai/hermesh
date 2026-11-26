package kernel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

const (
	defaultOwnerShellTimeout = 10 * time.Minute
	maxPromptShellOutput     = 16384
	maxStreamCommandOutput   = 256 << 10
	shellStreamInterval      = 120 * time.Millisecond
	inlineCommandFlushTick   = 50 * time.Millisecond
)

// ParseShellShortcut extracts an owner's "! <command>" direct shell shortcut.
// Only ACTOR_KIND_OWNER messages qualify; Markdown images ("![alt](...)") are ignored.
func ParseShellShortcut(actor *sessionv1.Actor, text string) (string, bool) {
	if actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
		return "", false
	}
	trimmed := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(trimmed, "!") || strings.HasPrefix(trimmed, "![") {
		return "", false
	}
	cmd := strings.TrimSpace(trimmed[1:])
	if cmd == "" {
		return "", false
	}
	return cmd, true
}

func (e *Engine) buildShellEnv(sessionID string) []string {
	base := shellenv.Environ()
	overrides := map[string]string{
		"HERMESH_SESSION_ID":  sessionID,
		"PAGER":               "cat",
		"GIT_PAGER":           "cat",
		"GIT_TERMINAL_PROMPT": "0",
	}
	if e.cfg.DataDir != "" {
		tmp := filepath.Join(e.cfg.DataDir, "sessions", sessionID, "tmp")
		_ = os.MkdirAll(tmp, 0o700)
		overrides["TMPDIR"] = tmp
		overrides["TMP"] = tmp
		overrides["TEMP"] = tmp
	}
	if e.cfg.ExtraEnv != nil {
		for k, v := range e.cfg.ExtraEnv(sessionID) {
			overrides[k] = v
		}
	}
	seen := make(map[string]bool, len(overrides))
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if v, exists := overrides[k]; exists {
			if !seen[k] {
				out = append(out, k+"="+v)
				seen[k] = true
			}
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func inlineShellPreview(raw []byte) (preview string, truncated bool) {
	s := string(raw)
	if len(s) <= maxInlineToolOutput {
		return s, false
	}
	cut := maxInlineToolOutput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n...[output truncated]...", true
}

func formatShellContinuationPrompt(cmd string, exitCode int, timedOut bool, dur time.Duration, raw []byte) string {
	body := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(body) == "" {
		body = "(no output)"
	} else if len(body) > maxPromptShellOutput {
		half := maxPromptShellOutput / 2
		head := half
		for head > 0 && !utf8.RuneStart(body[head]) {
			head--
		}
		tail := len(body) - half
		for tail < len(body) && !utf8.RuneStart(body[tail]) {
			tail++
		}
		body = fmt.Sprintf("%s\n...[truncated %d bytes]...\n%s", body[:head], tail-head, body[tail:])
	}
	status := fmt.Sprintf("%d", exitCode)
	if timedOut {
		status = fmt.Sprintf("timed out after %s", dur.Round(time.Second))
	}
	return fmt.Sprintf(
		"The user ran a shell command in the workspace:\n$ %s\nExit code: %s (%.1fs)\nOutput:\n%s\n\nReview this command result in the context of the task and continue.",
		cmd,
		status,
		dur.Seconds(),
		body,
	)
}

func (e *Engine) execOwnerShellCommand(runCtx context.Context, sess *sessionv1.Session, curRun *sessionv1.Run, workdir, prevTreeOID, cmd string) (string, bool) {
	sessionID := sess.SessionId
	runID := curRun.RunId
	itemID := newID("cmd")
	actor := proto.Clone(curRun.GetAuthor()).(*sessionv1.Actor)

	publishItem := func(status string, exitCode int32, output string, truncated bool, blobSha string, size uint64, clearTool bool) {
		evs, err := e.st.Mutate(func(tx *store.Tx) error {
			it, _ := tx.GetTurnItem(sessionID, itemID)
			if it == nil {
				it = &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sessionID,
					RunId:     runID,
					Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION,
					Actor:     actor,
					ToolName:  "Bash",
					Command:   cmd,
				}
			}
			it.Status = status
			it.ExitCode = exitCode
			it.Output = output
			it.OutputTruncated = truncated
			it.OutputBlobSha256 = blobSha
			it.OutputSize = size
			if err := tx.PutTurnItem(it); err != nil {
				return err
			}
			var sNow *sessionv1.Session
			if s, sErr := tx.GetSession(sessionID); sErr == nil {
				sNow = s
				if sNow.Progress == nil {
					sNow.Progress = &sessionv1.SessionProgress{}
				}
				if clearTool {
					sNow.Progress.CurrentTool = ""
				} else {
					sNow.Progress.CurrentTool = cmd
				}
				_ = putSessionSynced(tx, sNow)
			}
			return tx.AppendEvent(&sessionv1.SessionEvent{
				SessionId: sessionID,
				Type:      "item.updated",
				Session:   sNow,
				Item:      it,
			})
		})
		if err == nil {
			e.publish(evs, nil)
		}
	}

	publishItem("running", 0, "", false, "", 0, false)

	var (
		mu       sync.Mutex
		buf      bytes.Buffer
		dirty    bool
		stopTick = make(chan struct{})
		tickDone = make(chan struct{})
	)
	go func() {
		defer close(tickDone)
		ticker := time.NewTicker(shellStreamInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-ticker.C:
				mu.Lock()
				if !dirty {
					mu.Unlock()
					continue
				}
				dirty = false
				snap := append([]byte(nil), buf.Bytes()...)
				mu.Unlock()
				preview, trunc := inlineShellPreview(snap)
				publishItem("running", 0, preview, trunc, "", uint64(len(snap)), false)
			}
		}
	}()

	start := time.Now()
	cmdCtx, cancel := context.WithTimeout(runCtx, defaultOwnerShellTimeout)
	exitCode, runErr := runtime.RunShellStreaming(cmdCtx, cmd, workdir, e.buildShellEnv(sessionID), func(chunk []byte) {
		mu.Lock()
		buf.Write(chunk)
		dirty = true
		mu.Unlock()
	})
	timedOut := cmdCtx.Err() == context.DeadlineExceeded && runCtx.Err() == nil
	cancel()
	close(stopTick)
	<-tickDone
	dur := time.Since(start)

	mu.Lock()
	raw := buf.Bytes()
	mu.Unlock()

	if timedOut {
		note := fmt.Sprintf("\nCommand timed out after %s and was terminated.", defaultOwnerShellTimeout)
		raw = append(raw, []byte(note)...)
	} else if runErr != nil && len(raw) == 0 {
		raw = []byte(runErr.Error())
	}

	var blobSha string
	if len(raw) > maxInlineToolOutput {
		if ref, bErr := e.st.PutBlob("output.txt", "text/plain; charset=utf-8", raw); bErr == nil {
			blobSha = ref.BlobSha256
		}
	}
	preview, trunc := inlineShellPreview(raw)

	if runCtx.Err() != nil {
		if e.bgCtx.Err() == nil {
			publishItem("failed", -1, preview, trunc, blobSha, uint64(len(raw)), true)
			cp, cumStat := e.captureTurnCheckpoint(sessionID, runID, curRun.Ordinal, workdir, prevTreeOID)
			e.finishRunInterrupted(sessionID, runID, "interrupted", cp, cumStat)
		}
		return "", true
	}

	status := "completed"
	if timedOut || runErr != nil || exitCode != 0 {
		status = "failed"
		if exitCode == 0 {
			exitCode = -1
		}
	}
	publishItem(status, int32(exitCode), preview, trunc, blobSha, uint64(len(raw)), true)
	return formatShellContinuationPrompt(cmd, exitCode, timedOut, dur, raw), false
}

func (e *Engine) resolveCommandWorkdir(sess *sessionv1.Session) (string, error) {
	var base string
	if sess.ProjectId == ScratchProjectID {
		base = e.scratchDir
	} else {
		ws, err := e.st.Workspace(sess.ProjectId)
		if err != nil {
			return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("the session's project is gone"))
		}
		base = filepath.Clean(ws.Path)
	}
	if sess.GetWorkspaceStrategy().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE {
		wt := sess.GetWorkspaceStrategy().GetWorktreePath()
		if wt == "" {
			wt = filepath.Join(e.vcs.WorktreesRoot(), sess.SessionId)
		}
		wt = filepath.Clean(wt)
		if st, err := os.Stat(wt); err == nil && st.IsDir() {
			return wt, nil
		}
	}
	if st, err := os.Stat(base); err == nil && st.IsDir() {
		return base, nil
	}
	return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("the session's workspace folder is unavailable"))
}

// takeCompleteUTF8 splits b into a valid UTF-8 prefix and any trailing incomplete rune bytes (<= 3 bytes).
func takeCompleteUTF8(b []byte, final bool) (emit string, rest []byte) {
	if len(b) == 0 {
		return "", nil
	}
	if final {
		return strings.ToValidUTF8(string(b), "\uFFFD"), nil
	}
	cut := len(b)
	for i := 1; i <= 3 && i <= len(b); i++ {
		idx := len(b) - i
		if utf8.RuneStart(b[idx]) {
			if !utf8.FullRune(b[idx:]) {
				cut = idx
			}
			break
		}
	}
	if cut == 0 {
		return "", b
	}
	return strings.ToValidUTF8(string(b[:cut]), "\uFFFD"), append([]byte(nil), b[cut:]...)
}

// RunCommand executes an owner-initiated shell command/script in the session's workspace
// and streams output chunks directly to the caller without mutating conversation turns.
func (e *Engine) RunCommand(ctx context.Context, p *Principal, req *sessionv1.RunCommandRequest, send func(*sessionv1.RunCommandEvent) error) error {
	if p == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	if p.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
		return connect.NewError(connect.CodePermissionDenied, errors.New("only the owner may run direct workspace commands"))
	}
	cmd := strings.TrimSpace(req.GetCommand())
	if cmd == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("command is empty"))
	}
	sess, err := e.st.Session(req.GetSessionId())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return err
	}
	if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("archived session is read-only"))
	}
	if e.cfg.IsPaused != nil && e.cfg.IsPaused() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("machine is paused"))
	}
	workdir, err := e.resolveCommandWorkdir(sess)
	if err != nil {
		return err
	}

	cmdCtx, cancel := context.WithTimeout(ctx, defaultOwnerShellTimeout)
	defer cancel()

	var (
		mu        sync.Mutex
		pending   []byte
		totalSeen int
		truncated bool
		stopFlush = make(chan struct{})
		flushDone = make(chan struct{})
		sendErr   error
	)

	flush := func(final bool) bool {
		mu.Lock()
		emit, rem := takeCompleteUTF8(pending, final)
		pending = rem
		mu.Unlock()
		if emit == "" {
			return true
		}
		if err := send(&sessionv1.RunCommandEvent{OutputDelta: emit}); err != nil {
			mu.Lock()
			if sendErr == nil {
				sendErr = err
			}
			mu.Unlock()
			cancel()
			return false
		}
		return true
	}

	go func() {
		defer close(flushDone)
		ticker := time.NewTicker(inlineCommandFlushTick)
		defer ticker.Stop()
		for {
			select {
			case <-stopFlush:
				return
			case <-ticker.C:
				if !flush(false) {
					return
				}
			}
		}
	}()

	start := time.Now()
	exitCode, runErr := runtime.RunShellStreaming(cmdCtx, cmd, workdir, e.buildShellEnv(sess.SessionId), func(chunk []byte) {
		mu.Lock()
		defer mu.Unlock()
		if totalSeen >= maxStreamCommandOutput {
			truncated = true
			return
		}
		room := maxStreamCommandOutput - totalSeen
		if len(chunk) > room {
			chunk = chunk[:room]
			truncated = true
		}
		totalSeen += len(chunk)
		pending = append(pending, chunk...)
	})
	timedOut := cmdCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
	close(stopFlush)
	<-flushDone
	dur := time.Since(start)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	mu.Lock()
	sErr := sendErr
	mu.Unlock()
	if sErr != nil {
		return sErr
	}

	if runErr != nil && totalSeen == 0 && !timedOut {
		mu.Lock()
		pending = append(pending, []byte(runErr.Error())...)
		mu.Unlock()
	}
	if !flush(true) {
		mu.Lock()
		defer mu.Unlock()
		return sendErr
	}
	if (timedOut || runErr != nil) && exitCode == 0 {
		exitCode = -1
	}
	return send(&sessionv1.RunCommandEvent{
		Done:       true,
		ExitCode:   int32(exitCode),
		DurationMs: uint64(dur.Milliseconds()),
		TimedOut:   timedOut,
		Truncated:  truncated,
	})
}
