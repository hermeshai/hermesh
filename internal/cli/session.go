package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/pkg/transport"
)

func (a *app) sessionCmd() *cobra.Command {
	cmd := group(&cobra.Command{Use: "session", Short: "Start and manage coding-agent sessions"})
	cmd.AddCommand(
		a.startCmd(),
		a.getCmd(),
		a.listCmd(),
		a.eventsCmd(),
		a.sendCmd(),
		a.answerCmd(),
		a.interruptCmd(),
		a.archiveCmd(),
		a.unarchiveCmd(),
		a.diffCmd(),
		a.steerCmd(),
		a.queueCmd(),
		a.configCmd(),
		a.respondCmd(),
		a.checkpointsCmd(),
		a.rollbackCmd(),
		a.forkCmd(),
		a.deleteCmd(),
		a.providersCmd(),
		a.outboxCmd(),
	)
	return cmd
}

func localSocketSessionClient(socket string) sessionv1connect.SessionServiceClient {
	if socket == "" {
		socket = os.Getenv("HERMESH_SOCKET")
	}
	if socket == "" {
		return nil
	}
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return localipc.Dial(ctx, socket)
			},
		},
	}
	return sessionv1connect.NewSessionServiceClient(hc, "http://local", transport.ClientNoCompression)
}

func ownerDefaultSocket() string {
	if s := os.Getenv("HERMESH_SOCKET"); s != "" {
		return s
	}
	return paths.LocalAPI(paths.DataDir())
}

// withMachine runs fn on the machine a session belongs to (or over --socket when set).
func (a *app) withMachine(ctx context.Context, sessionID string, fn func(c *peer.Client, n *peer.Machine, ref machineRef) error) error {
	c, machines, err := a.connect(ctx, func(seen []machineRef) []machineRef {
		m, _ := resolveMachine(seen, "", sessionID)
		return []machineRef{m}
	})
	if err != nil {
		return err
	}
	defer c.Close()
	ref, err := resolveMachine(machines, "", sessionID)
	if err != nil {
		return err
	}
	return a.onMachine(ctx, c, ref, fn)
}

func (a *app) onMachine(ctx context.Context, c *peer.Client, ref machineRef, fn func(c *peer.Client, n *peer.Machine, ref machineRef) error) error {
	const hint = "try again once it is back; `hermesh project list` shows the machines offline now"
	if !ref.Online {
		m := offlineMachine{Machine: ref.Name, LastSeen: tsJSON(ref.Entry.GetLastSeen())}
		return &exitError{code: exitUnavailable, kind: "unreachable", err: fmt.Errorf("machine %s is offline (%s)", ref.Name, offlineText(m)), hint: hint}
	}
	n, err := c.Machine(ctx, ref.ID)
	if err != nil {
		return &exitError{code: exitUnavailable, kind: "unreachable", err: fmt.Errorf("machine %s cannot be reached: %v", ref.Name, err), hint: hint}
	}
	if err := checkName(ref, n); err != nil {
		return err
	}
	return fn(c, n, ref)
}

const (
	maxFiles     = 20
	maxFileBytes = 32 << 20
)

type rawAttachment struct {
	name string
	mime string
	data []byte
}

func readRawFiles(filePaths []string) ([]rawAttachment, error) {
	if len(filePaths) > maxFiles {
		return nil, fail(exitUsage, "%d files; at most %d per message", len(filePaths), maxFiles)
	}
	var out []rawAttachment
	total := 0
	for _, p := range filePaths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, fail(exitUsage, "%v", err)
		}
		if fi.IsDir() {
			return nil, fail(exitUsage, "%s is a directory; attach files (or an archive)", p)
		}
		if total += int(fi.Size()); total > maxFileBytes {
			return nil, fail(exitUsage, "files are larger than %d MiB in total", maxFileBytes>>20)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fail(exitUsage, "%v", err)
		}
		mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
		if mt == "" {
			mt = http.DetectContentType(b)
		}
		out = append(out, rawAttachment{name: filepath.Base(p), mime: mt, data: b})
	}
	return out, nil
}

func uploadAttachments(ctx context.Context, sc sessionv1connect.SessionServiceClient, raws []rawAttachment) ([]*sessionv1.AttachmentRef, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]*sessionv1.AttachmentRef, 0, len(raws))
	for _, f := range raws {
		res, err := sc.UploadBlob(ctx, &sessionv1.UploadBlobRequest{
			Name:     f.name,
			MimeType: f.mime,
			Data:     f.data,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res.GetAttachment())
	}
	return out, nil
}

func readPrompt(flag, text, file string, files int) (string, error) {
	switch {
	case text != "":
		return text, nil
	case file == "-":
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	case file != "":
		b, err := os.ReadFile(file)
		return string(b), err
	case files > 0:
		return "", nil
	}
	return "", fail(exitUsage, "give the %s with --%s, or --%s-file (- reads stdin), or attach --file", flag, flag, flag)
}

func buildModelSelection(agent, model, effort string, extraOpts []string) *sessionv1.ModelSelection {
	sel := &sessionv1.ModelSelection{InstanceId: agent, Model: model}
	if effort != "" || len(extraOpts) > 0 {
		sel.Options = map[string]*sessionv1.OptionValue{}
	}
	if effort != "" {
		sel.Options["effort"] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: effort}}
	}
	for _, kv := range extraOpts {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			sel.Options[k] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_BoolValue{BoolValue: true}}
		case "false":
			sel.Options[k] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_BoolValue{BoolValue: false}}
		default:
			sel.Options[k] = &sessionv1.OptionValue{Kind: &sessionv1.OptionValue_StringValue{StringValue: v}}
		}
	}
	return sel
}

func parseRuntimeModeFlag(s string) identityv1.RuntimeMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "supervised":
		return identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED
	case "auto_edits", "auto-edits":
		return identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS
	case "auto":
		return identityv1.RuntimeMode_RUNTIME_MODE_AUTO
	case "full_access", "full":
		return identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS
	default:
		return identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED
	}
}

func parseInteractionModeFlag(s string) sessionv1.InteractionMode {
	if strings.EqualFold(strings.TrimSpace(s), "plan") {
		return sessionv1.InteractionMode_INTERACTION_MODE_PLAN
	}
	if strings.EqualFold(strings.TrimSpace(s), "code") || strings.EqualFold(strings.TrimSpace(s), "default") {
		return sessionv1.InteractionMode_INTERACTION_MODE_CODE
	}
	return sessionv1.InteractionMode_INTERACTION_MODE_UNSPECIFIED
}

// herRelationship is how work sent from a conversation with Her relates to it: "her" when there is one.
func herRelationship(conversation string) string {
	if conversation == "" {
		return ""
	}
	return "her"
}

func (a *app) startCmd() *cobra.Command {
	var project, agent, base, model, effort, prompt, promptFile, title, locale, idem string
	var socket, runtimeMode, interactionMode, workspaceMode, branch, herConversation string
	var options []string
	var questionTimeout time.Duration
	var files []string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a coding-agent session (Claude Code, Codex, Gemini, Cursor, ...) in a project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			text, err := readPrompt("prompt", prompt, promptFile, len(files))
			if err != nil {
				return err
			}
			raws, err := readRawFiles(files)
			if err != nil {
				return err
			}
			var qt *uint32
			if cmd.Flags().Changed("question-timeout") {
				v := uint32(questionTimeout / time.Second)
				qt = &v
			}
			if idem == "" {
				idem = newIdempotencyKey()
			}
			effLocale := locale
			if effLocale == "" && a.mem == nil {
				effLocale = runtime.DefaultLocaleFromEnv()
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			var ws *sessionv1.WorkspaceStrategy
			if base != "" || branch != "" || workspaceMode != "" {
				ws = &sessionv1.WorkspaceStrategy{BaseRef: base, Branch: branch}
				switch strings.ToLower(workspaceMode) {
				case "in_place", "in-place", "inplace":
					ws.Mode = identityv1.Workspace_WORKSPACE_IN_PLACE
				case "worktree":
					ws.Mode = identityv1.Workspace_WORKSPACE_WORKTREE
				}
			}

			if sc := localSocketSessionClient(socket); sc != nil {
				attRefs, uErr := uploadAttachments(ctx, sc, raws)
				if uErr != nil {
					return uErr
				}
				machineFlag, projID := "", project
				if i := strings.IndexByte(project, '/'); i >= 0 {
					machineFlag, projID = project[:i], project[i+1:]
				}
				res, sErr := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
					CommandId:              idem,
					MachineId:              machineFlag,
					ProjectId:              projID,
					Title:                  title,
					Locale:                 effLocale,
					ModelSelection:         buildModelSelection(agent, model, effort, options),
					Workspace:              ws,
					RuntimeMode:            parseRuntimeModeFlag(runtimeMode),
					InteractionMode:        parseInteractionModeFlag(interactionMode),
					InitialPrompt:          text,
					Attachments:            attRefs,
					QuestionTimeoutSeconds: qt,
					Conversation:           a.as,
					ParentSessionId:        herConversation,
					ParentRelationship:     herRelationship(herConversation),
				})
				if sErr != nil {
					return sErr
				}
				return a.showSession(cmd, res.GetSession(), cmp.Or(machineFlag, "local"), false)
			}

			c, machines, err := a.connect(ctx, warmProject(project))
			if err != nil {
				return err
			}
			defer c.Close()
			ref, projectID, err := a.resolveProject(ctx, c, machines, project)
			if err != nil {
				return err
			}
			return a.onMachine(ctx, c, ref, func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				attRefs, uErr := uploadAttachments(ctx, n.Session, raws)
				if uErr != nil {
					return uErr
				}
				res, err := n.Session.StartSession(ctx, &sessionv1.StartSessionRequest{
					CommandId:              idem,
					ProjectId:              projectID,
					Title:                  title,
					Locale:                 effLocale,
					ModelSelection:         buildModelSelection(agent, model, effort, options),
					Workspace:              ws,
					RuntimeMode:            parseRuntimeModeFlag(runtimeMode),
					InteractionMode:        parseInteractionModeFlag(interactionMode),
					InitialPrompt:          text,
					Attachments:            attRefs,
					QuestionTimeoutSeconds: qt,
					Conversation:           a.as,
					ParentSessionId:        herConversation,
					ParentRelationship:     herRelationship(herConversation),
				})
				if connect.CodeOf(err) == connect.CodeNotFound {
					return &exitError{code: exitUsage, kind: "not_found", err: err, hint: "see `hermesh project list`"}
				}
				if err != nil {
					return err
				}
				s := res.GetSession()
				fmt.Fprintf(cmd.ErrOrStderr(), "session %s in %s/%s\n", s.SessionId, ref.Name, projectID)
				if !wait {
					return a.started(cmd, s, ref.Name)
				}
				return a.waitTurn(ctx, cmd, c, ref, s.SessionId, 0)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "where to work: machine/project as hermesh project list shows it, or the project alone when only one machine has it")
	f.StringVar(&agent, "agent", "claude", "coding agent to run: claude, codex, or an ACP agent (gemini, cursor, copilot, ...); hermesh project list shows those available")
	f.StringVar(&base, "base", "", "base git ref of the session's worktree (worktree projects only; default: the project's); fetched from the project's remote when the machine lacks it")
	f.StringVar(&model, "model", "", "model to run, one hermesh agent list shows for the agent (default: the one the owner set for the agent on that machine, else the agent's own)")
	f.StringVar(&effort, "effort", "", "reasoning effort, one hermesh agent list shows for the model (default: the one the owner set for the agent on that machine, else the agent's own)")
	f.StringVar(&prompt, "prompt", "", "prompt text")
	f.StringVar(&promptFile, "prompt-file", "", "read the prompt from a file (- for stdin)")
	f.StringVar(&title, "title", "", "a short title for session lists (default: generated automatically)")
	f.StringVar(&locale, "locale", "", "BCP 47 language tag for generated session title (e.g. zh-CN, en)")
	f.StringArrayVarP(&files, "file", "f", nil, "attach a file (repeatable; images are shown to the agent, other files are saved on the machine and listed by path)")
	f.DurationVar(&questionTimeout, "question-timeout", 30*time.Minute, "how long the agent's questions wait for an answer before it goes on alone (0: nobody will answer)")
	f.StringVar(&idem, "idempotency-key", "", "retrying with the same key returns the same session")
	f.BoolVar(&wait, "wait", false, waitHelp)
	f.DurationVar(&timeout, "timeout", 0, timeoutHelp)
	// Owner / local-socket flags hidden from embedded MCP/Her tool schemas:
	f.StringVar(&socket, "socket", "", "local owner API socket")
	f.StringVar(&runtimeMode, "runtime-mode", "", "runtime permission mode (plan_readonly, supervised, auto, full_access)")
	f.StringVar(&interactionMode, "mode", "", "interaction mode (default, plan)")
	f.StringVar(&workspaceMode, "workspace", "", "workspace mode (worktree, in_place)")
	f.StringVar(&branch, "branch", "", "custom worktree branch")
	f.StringVar(&herConversation, "her-conversation", "", "the session of the owner's conversation with Her this work is sent from (set by hermeshd for Her)")
	f.StringArrayVar(&options, "option", nil, "provider option key=val")
	for _, hidden := range []string{"socket", "runtime-mode", "mode", "workspace", "branch", "option"} {
		_ = f.MarkHidden(hidden)
	}
	cmd.MarkFlagRequired("project")
	return cmd
}

func (a *app) waitTurn(ctx context.Context, cmd *cobra.Command, c *peer.Client, ref machineRef, id string, afterSeq uint64) error {
	w := cmd.OutOrStdout()
	last := afterSeq
	var failedEvt bool
	var completedEvt bool
	var final sessionv1.SessionStatus
	for attempt := 0; ; attempt++ {
		n, err := c.Machine(ctx, ref.ID)
		if err == nil {
			var stream *connect.ServerStreamForClient[sessionv1.SessionEvent]
			stream, err = n.Session.SubscribeSession(ctx, &sessionv1.SubscribeSessionRequest{SessionId: id, AfterSeq: last})
			if err == nil {
				for stream.Receive() {
					e := stream.Msg()
					if e.Seq > last {
						last = e.Seq
					}
					switch e.Type {
					case "run.completed":
						completedEvt = true
					case "run.failed", "run.interrupted":
						failedEvt = true
					}
					if st := e.GetSession().GetStatus(); st != 0 {
						switch st {
						case sessionv1.SessionStatus_SESSION_STATUS_IDLE,
							sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED,
							sessionv1.SessionStatus_SESSION_STATUS_CRASHED,
							sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
							if e.Type != "session.created" && e.Type != "run.queued" {
								final = st
							}
						}
					}
					if final != 0 {
						break
					}
				}
				err = stream.Err()
				stream.Close()
			}
			if final == 0 && err == nil {
				if cur, gErr := a.getSession(ctx, c, ref, id); gErr == nil && cur != nil {
					switch cur.Status {
					case sessionv1.SessionStatus_SESSION_STATUS_IDLE,
						sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED,
						sessionv1.SessionStatus_SESSION_STATUS_CRASHED,
						sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
						final = cur.Status
					}
				}
			}
		}
		if final != 0 {
			break
		}
		if ctx.Err() != nil {
			var s *sessionv1.Session
			if a.output == "json" {
				fetch, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				s, _ = a.getSession(fetch, c, ref, id)
				cancel()
			}
			return a.outcome(w, ref.Name, s, &exitError{code: exitTimeout, kind: "wait_timeout", err: fmt.Errorf("stopped waiting for session %s; its turn goes on", id),
				hint: fmt.Sprintf("check it with `hermesh session get %s`", id)})
		}
		c.ForgetMachine(n)
		if attempt > 30 {
			return a.outcome(w, ref.Name, nil, &exitError{code: exitUnavailable, kind: "unreachable", err: fmt.Errorf("lost connection to %s: %v", ref.Name, err),
				hint: fmt.Sprintf("check it later with `hermesh session get %s`", id)})
		}
		time.Sleep(2 * time.Second)
	}
	s, err := a.getSession(ctx, c, ref, id)
	if err != nil {
		return a.outcome(w, ref.Name, nil, err)
	}
	if final == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
		return a.outcome(w, ref.Name, s, &exitError{code: exitNeedsInput, kind: "needs_input", err: fmt.Errorf("session %s is waiting for an answer", id),
			hint: fmt.Sprintf("`hermesh session answer %s --choose <option>` or `--text <answer>` (or reply with `hermesh message send --to %s`)", id, id)})
	}
	return a.outcome(w, ref.Name, s, turnError(s, completedEvt, failedEvt, final))
}

func turnError(s *sessionv1.Session, completedEvt, failedEvt bool, final sessionv1.SessionStatus) error {
	id := s.SessionId
	var f *sessionv1.Failure
	switch {
	case final == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED:
		return &exitError{code: exitFailed, kind: "archived", err: fmt.Errorf("session %s was archived", id),
			hint: fmt.Sprintf("`hermesh session unarchive %s` to go on with it", id)}
	case final == sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
		f = s.GetProgress().GetLastFailure()
		if f == nil {
			f = &sessionv1.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED, Retryable: true,
				Message: "the turn did not finish", Hint: "send the message again to resume the session"}
		}
	case failedEvt || (!completedEvt && s.GetProgress().GetLastFailure() != nil):
		f = s.GetProgress().GetLastFailure()
	}
	if f == nil {
		return nil
	}
	code := exitFailed
	if f.GetRetryable() {
		code = exitRetryable
	}
	kind := firstNonEmpty(failureName(f.GetKind()), "unknown")
	msg := firstNonEmpty(f.GetMessage(), s.Error, s.Result, "turn failed")
	return &exitError{code: code, kind: kind, err: fmt.Errorf("session %s: %s: %s", id, kind, msg), hint: f.GetHint(), retryable: f.GetRetryable()}
}

func (a *app) outcome(w io.Writer, machine string, s *sessionv1.Session, err error) error {
	if a.output != "json" {
		if s != nil {
			writeSession(w, s, machine, true)
		}
		return err
	}
	if s == nil {
		return a.report(w, err)
	}
	return a.report(w, err, field{"session", sessionView(s, machine)})
}

func (a *app) started(cmd *cobra.Command, s *sessionv1.Session, machine string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "→ it runs on; check it with `hermesh session get %s` (status, then the full result once idle)\n", s.SessionId)
	return a.showSession(cmd, s, machine, false)
}

func (a *app) showSession(cmd *cobra.Command, s *sessionv1.Session, machine string, full bool) error {
	if a.output == "json" {
		return a.report(cmd.OutOrStdout(), nil, field{"session", sessionView(s, machine)})
	}
	writeSession(cmd.OutOrStdout(), s, machine, full)
	return nil
}

const (
	waitHelp    = "block until the turn finishes, then print the session with the agent's full result (exit code: how the turn went); without it the command returns at once and session get shows progress and, once idle, the result"
	timeoutHelp = "with --wait: stop waiting after this long (exit code 6; the turn goes on, see session get). Pass one below the time limit of the tool running this command"
)

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a *app) getSession(ctx context.Context, c *peer.Client, ref machineRef, id string) (*sessionv1.Session, error) {
	n, err := c.Machine(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	res, err := n.Session.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: id})
	if err != nil {
		return nil, err
	}
	return res.GetProjection().GetSession(), nil
}

func failureName(k sessionv1.FailureKind) string {
	if k == sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED {
		return ""
	}
	if _, ok := sessionv1.FailureKind_name[int32(k)]; !ok {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(k.String(), "FAILURE_KIND_"))
}

func statusName(s sessionv1.SessionStatus) string {
	if _, ok := sessionv1.SessionStatus_name[int32(s)]; !ok {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(s.String(), "SESSION_STATUS_"))
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func writeSession(w io.Writer, s *sessionv1.Session, machine string, full bool) {
	v := sessionView(s, machine)
	fmt.Fprintf(w, "session %s · %s · %s · %s · %s\n", v.SessionID, v.Status, v.Project, v.Agent, plural(int(v.Turns), "turn"))
	active := turnInProgress(s.Status)
	switch {
	case v.CurrentTool != "":
		fmt.Fprintf(w, "now: %s\n", oneLine(v.CurrentTool, 120))
	case active && v.LastMessage != "":
		fmt.Fprintf(w, "last: %s\n", oneLine(v.LastMessage, 120))
	}
	if f := v.LastFailure; f != nil {
		fmt.Fprintf(w, "failure: %s: %s%s\n", f.Kind, oneLine(f.Message, 200), suffixParen(f.Hint))
	}
	if d := v.DiffStat; d.FilesChanged > 0 {
		fmt.Fprintf(w, "changes: %s changed, +%d -%d (`hermesh session diff %s`)\n", plural(int(d.FilesChanged), "file"), d.Insertions, d.Deletions, v.SessionID)
	}
	if !full {
		return
	}
	if q := s.GetProgress().GetPendingQuestion(); q != nil {
		fmt.Fprintln(w)
		writeQuestion(w, q)
	} else if !active && v.Result != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(v.Result))
	}
}

func turnInProgress(st sessionv1.SessionStatus) bool {
	switch st {
	case sessionv1.SessionStatus_SESSION_STATUS_QUEUED, sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
		sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
		return true
	}
	return false
}

func suffixParen(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func writeQuestion(w io.Writer, q *sessionv1.Question) {
	if q == nil {
		return
	}
	for _, it := range q.Items {
		h := ""
		if it.Header != "" {
			h = "[" + it.Header + "] "
		}
		fmt.Fprintf(w, "? %s%s\n", h, strings.TrimSpace(it.Text))
		for i, o := range it.Options {
			d := ""
			if o.Description != "" {
				d = " — " + o.Description
			}
			fmt.Fprintf(w, "  %d. %s%s\n", i+1, o.Label, d)
		}
		var notes []string
		if it.MultiSelect {
			notes = append(notes, "choose one or more")
		}
		if it.AllowOther && len(it.Options) > 0 {
			notes = append(notes, "or answer in your own words")
		}
		if it.Secret {
			notes = append(notes, "secret: passed to the agent, not recorded")
		}
		if len(q.Items) > 1 {
			notes = append(notes, "item "+it.Id)
		}
		if len(notes) > 0 {
			fmt.Fprintf(w, "  (%s)\n", strings.Join(notes, "; "))
		}
	}
}

func (a *app) answerCmd() *cobra.Command {
	var questionID, text, socket string
	var choose, items []string
	var decline, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "answer <session id>",
		Short: "Answer the question the agent asked (it waits for it)",
		Long: `Answer the agent's pending question: pick options with --choose (label or
number, repeatable for multi-select), give free text with --text, or answer
each item of a multi-part question with --item <header or id>=<value>.
--decline tells the agent nobody will answer, so it goes on on its own.
A message sent with ` + "`hermesh message send`" + ` while the agent waits also answers it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			answerOn := func(sc sessionv1connect.SessionServiceClient) (*sessionv1.Session, uint64, error) {
				projRes, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: args[0]})
				if err != nil {
					return nil, 0, err
				}
				before := projRes.GetProjection().GetSession()
				q := before.GetProgress().GetPendingQuestion()
				if q == nil {
					return nil, 0, &exitError{code: exitUsage, kind: "invalid_state", err: fmt.Errorf("session %s is not waiting for an answer (it is %s)", args[0], statusName(before.GetStatus()))}
				}
				reqID := firstNonEmpty(questionID, before.GetPendingRequestId(), q.GetQuestionId())
				rreq := &sessionv1.RespondRuntimeRequestRequest{
					SessionId: args[0],
					RequestId: reqID,
					Decline:   decline,
				}
				if !decline {
					ansMap, err := buildAnswersMap(q, choose, text, items)
					if err != nil {
						return nil, 0, err
					}
					rreq.Answers = ansMap
				}
				res, err := sc.RespondRuntimeRequest(ctx, rreq)
				if err != nil {
					return nil, 0, err
				}
				return res.GetSession(), before.GetProgress().GetLastEventSeq(), nil
			}
			if sc := localSocketSessionClient(socket); sc != nil {
				sess, _, err := answerOn(sc)
				if err != nil {
					return err
				}
				return a.showSession(cmd, sess, "local", false)
			}
			return a.withMachine(ctx, args[0], func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				sess, lastSeq, err := answerOn(n.Session)
				if err != nil {
					return err
				}
				if !wait {
					return a.started(cmd, sess, ref.Name)
				}
				return a.waitTurn(ctx, cmd, c, ref, args[0], lastSeq)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&questionID, "question", "", "question id (default: the pending one)")
	f.StringArrayVarP(&choose, "choose", "c", nil, "option to pick, by label or number (repeatable)")
	f.StringVar(&text, "text", "", "free-text answer")
	f.StringArrayVar(&items, "item", nil, "answer one item of a multi-part question: <header or id>=<option or text> (repeatable)")
	f.BoolVar(&decline, "decline", false, "no answer: the agent goes on on its own")
	f.BoolVar(&wait, "wait", false, waitHelp)
	f.DurationVar(&timeout, "timeout", 0, timeoutHelp)
	f.StringVar(&socket, "socket", "", "local owner API socket")
	_ = f.MarkHidden("socket")
	return cmd
}

func buildAnswersMap(q *sessionv1.Question, choose []string, text string, items []string) (map[string]*sessionv1.QuestionAnswer, error) {
	pick := func(it *sessionv1.QuestionItem, v string) (string, bool) {
		v = strings.TrimSpace(v)
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(it.Options) {
			return it.Options[n-1].Label, true
		}
		for _, o := range it.Options {
			if strings.EqualFold(o.Label, v) {
				return o.Label, true
			}
		}
		return "", false
	}
	out := map[string]*sessionv1.QuestionAnswer{}
	if len(items) > 0 {
		if len(choose) > 0 || text != "" {
			return nil, fail(exitUsage, "use either --item or --choose/--text")
		}
		for _, kv := range items {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fail(exitUsage, "--item takes <header or id>=<value>, got %q", kv)
			}
			var it *sessionv1.QuestionItem
			for _, x := range q.Items {
				if x.Id == k || strings.EqualFold(x.Header, k) {
					it = x
				}
			}
			if it == nil {
				return nil, fail(exitUsage, "the question has no item %q", k)
			}
			qa := &sessionv1.QuestionAnswer{}
			if l, ok := pick(it, v); ok {
				qa.SelectedOptions = []string{l}
			} else {
				qa.CustomText = v
			}
			out[it.Id] = qa
		}
		return out, nil
	}
	if len(q.Items) != 1 {
		return nil, fail(exitUsage, "the question has %d items: answer each with --item <header or id>=<value>", len(q.Items))
	}
	if len(choose) == 0 && strings.TrimSpace(text) == "" {
		return nil, fail(exitUsage, "give an answer with --choose or --text (or --decline)")
	}
	it := q.Items[0]
	qa := &sessionv1.QuestionAnswer{CustomText: text}
	for _, c := range choose {
		l, ok := pick(it, c)
		if !ok {
			return nil, fail(exitUsage, "no option %q (options: %s)", c, labels(it))
		}
		qa.SelectedOptions = append(qa.SelectedOptions, l)
	}
	out[it.Id] = qa
	return out, nil
}

func labels(it *sessionv1.QuestionItem) string {
	var l []string
	for i, o := range it.Options {
		l = append(l, fmt.Sprintf("%d. %s", i+1, o.Label))
	}
	return strings.Join(l, ", ")
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
}

func (a *app) getCmd() *cobra.Command {
	var socket string
	var fullProjection bool
	cmd := &cobra.Command{
		Use:   "get <session id>",
		Short: "Show a session: its status and progress; its question when it asks one; the agent's full result once a turn is over",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sc := localSocketSessionClient(socket); sc != nil {
				res, err := sc.GetSession(cmd.Context(), &sessionv1.GetSessionRequest{SessionId: args[0]})
				if err != nil {
					return err
				}
				if fullProjection && a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"projection", res.GetProjection()}, field{"session", sessionView(res.GetProjection().GetSession(), "local")})
				}
				return a.showSession(cmd, res.GetProjection().GetSession(), "local", true)
			}
			return a.withMachine(cmd.Context(), args[0], func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				res, err := n.Session.GetSession(cmd.Context(), &sessionv1.GetSessionRequest{SessionId: args[0]})
				if err != nil {
					return err
				}
				if fullProjection && a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"projection", res.GetProjection()}, field{"session", sessionView(res.GetProjection().GetSession(), ref.Name)})
				}
				return a.showSession(cmd, res.GetProjection().GetSession(), ref.Name, true)
			})
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	cmd.Flags().BoolVar(&fullProjection, "projection", false, "include full runs/items/requests projection in JSON")
	_ = cmd.Flags().MarkHidden("socket")
	_ = cmd.Flags().MarkHidden("projection")
	return cmd
}

func (a *app) listCmd() *cobra.Command {
	var machineFlag, status, socket string
	var since time.Duration
	var archived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List this client's sessions on all reachable machines (not the archived ones unless --archived)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var st sessionv1.SessionStatus
			if archived {
				status = "archived"
			}
			if status != "" {
				v, ok := sessionv1.SessionStatus_value["SESSION_STATUS_"+strings.ToUpper(status)]
				if !ok {
					return fail(exitUsage, "unknown status %q", status)
				}
				st = sessionv1.SessionStatus(v)
			}
			var sinceTS *time.Time
			if since > 0 {
				t := time.Now().Add(-since)
				sinceTS = &t
			}
			if sc := localSocketSessionClient(socket); sc != nil {
				req := &sessionv1.ListSessionsRequest{
					MachineId:       machineFlag,
					Status:          st,
					IncludeArchived: archived || st == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED,
				}
				if sinceTS != nil {
					req.Since = timestampOf(*sinceTS)
				}
				res, err := sc.ListSessions(cmd.Context(), req)
				if err != nil {
					return err
				}
				views := []*sessionJSON{}
				for _, s := range res.GetSessions() {
					views = append(views, sessionView(s, s.MachineId))
				}
				if a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"sessions", views}, field{"offline", []offlineMachine{}})
				}
				for _, v := range views {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", v.SessionID, v.Status, v.Agent, v.Title)
				}
				return nil
			}
			return a.listSessions(cmd, machineFlag, st, sinceTS)
		},
	}
	cmd.Flags().StringVar(&machineFlag, "machine", "", "machine name or id")
	cmd.Flags().BoolVar(&archived, "archived", false, "list the archived sessions instead")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (queued, running, waiting_input, idle, crashed, archived)")
	cmd.Flags().DurationVar(&since, "since", 0, "only sessions updated within this duration")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	_ = cmd.Flags().MarkHidden("socket")
	return cmd
}

func (a *app) listSessions(cmd *cobra.Command, machineFlag string, status sessionv1.SessionStatus, since *time.Time) error {
	ctx := cmd.Context()
	c, machines, err := a.connect(ctx, warmTargets(machineFlag))
	if err != nil {
		return err
	}
	defer c.Close()
	machines, err = targetMachines(machines, machineFlag)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, n := range machines {
		names[n.ID] = n.Name
	}
	got := map[string][]*sessionv1.Session{}
	var mu sync.Mutex
	errs := c.FanOut(ctx, ids(machines), func(ctx context.Context, n *peer.Machine) error {
		req := &sessionv1.ListSessionsRequest{
			Status:          status,
			IncludeArchived: status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED,
		}
		if since != nil {
			req.Since = timestampOf(*since)
		}
		r, err := n.Session.ListSessions(ctx, req)
		if err == nil {
			mu.Lock()
			got[n.MachineID] = r.Sessions
			names[n.MachineID] = n.Name
			mu.Unlock()
		}
		return err
	})
	out := &sessionv1.ListSessionsResponse{}
	for _, n := range machines {
		for _, s := range got[n.ID] {
			if status == 0 && s.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
				continue
			}
			out.Sessions = append(out.Sessions, s)
		}
	}
	w := cmd.OutOrStdout()
	offline := offlineOf(machines, errs)
	if a.output == "json" {
		views := []*sessionJSON{}
		for _, s := range out.Sessions {
			views = append(views, sessionView(s, names[s.MachineId]))
		}
		return a.report(w, nil, field{"sessions", views}, field{"offline", offline})
	}
	if len(out.Sessions) == 0 {
		fmt.Fprintln(w, "no sessions")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
		fmt.Fprintln(tw, "SESSION\tTITLE\tSTATUS\tPROJECT\tAGENT\tTURNS\tNOW / LAST")
		for _, s := range out.Sessions {
			v := sessionView(s, names[s.MachineId])
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", v.SessionID, oneLine(v.Title, 40), v.Status, v.Project, v.Agent, v.Turns, oneLine(firstNonEmpty(v.CurrentTool, v.LastMessage, v.Result), 60))
		}
		tw.Flush()
	}
	for _, m := range offline {
		fmt.Fprintf(w, "offline: %s (%s); its sessions are not listed\n", m.Machine, offlineText(m))
	}
	return nil
}

type synthEvent struct {
	seq uint64
	obj object
	txt string
}

func projectionEvents(proj *sessionv1.SessionProjection) []synthEvent {
	if proj == nil {
		return nil
	}
	runOrd := map[string]int{}
	for _, r := range proj.Runs {
		runOrd[r.RunId] = int(r.Ordinal)
	}
	reqByID := map[string]*sessionv1.RuntimeRequest{}
	for _, rq := range proj.Requests {
		reqByID[rq.RequestId] = rq
	}
	type rawEv struct {
		at  time.Time
		ord int
		obj func(seq uint64) object
		txt string
	}
	var all []rawEv
	for _, r := range proj.Runs {
		if r.StartedAt != nil {
			r := r
			all = append(all, rawEv{
				at:  r.StartedAt.AsTime(),
				ord: 0,
				obj: func(seq uint64) object {
					return object{
						{"seq", seq}, {"ts", tsJSON(r.StartedAt)}, {"turn", int(r.Ordinal)},
						{"type", "turn_started"}, {"asker", r.GetAuthor().GetId()},
					}
				},
				txt: fmt.Sprintf("── turn %d ──\n", r.Ordinal),
			})
		}
		if r.CompletedAt != nil && (r.Status == sessionv1.RunStatus_RUN_STATUS_COMPLETED || r.Status == sessionv1.RunStatus_RUN_STATUS_FAILED || r.Status == sessionv1.RunStatus_RUN_STATUS_INTERRUPTED || r.Status == sessionv1.RunStatus_RUN_STATUS_CANCELLED) {
			r := r
			isErr := r.Status == sessionv1.RunStatus_RUN_STATUS_FAILED || r.Status == sessionv1.RunStatus_RUN_STATUS_INTERRUPTED
			var fail *sessionv1.Failure
			if isErr {
				fail = cmp.Or(r.Failure, proj.GetSession().GetProgress().GetLastFailure())
			}
			resText := ""
			if r.Ordinal == proj.GetSession().GetProgress().GetTurnCount() {
				resText = proj.GetSession().GetResult()
			}
			u := &sessionv1.Usage{
				InputTokens:  r.GetUsage().GetInputTokens(),
				OutputTokens: r.GetUsage().GetOutputTokens(),
			}
			all = append(all, rawEv{
				at:  r.CompletedAt.AsTime(),
				ord: 3,
				obj: func(seq uint64) object {
					return object{
						{"seq", seq}, {"ts", tsJSON(r.CompletedAt)}, {"turn", int(r.Ordinal)},
						{"type", "turn_completed"}, {"result", resText}, {"is_error", isErr},
						{"failure", failureView(fail)}, {"diff_stat", diffStatView(proj.GetSession().GetProgress().GetDiffStat())},
						{"usage", usageView(u)}, {"commit", ""},
					}
				},
				txt: func() string {
					if isErr {
						return fmt.Sprintf("✗ turn %d failed: %s\n", r.Ordinal, r.ErrorMessage)
					}
					return fmt.Sprintf("✓ turn %d done\n", r.Ordinal)
				}(),
			})
		}
	}
	for _, it := range proj.Items {
		it := it
		t := time.Time{}
		if it.CreatedAt != nil {
			t = it.CreatedAt.AsTime()
		}
		turn := runOrd[it.RunId]
		switch it.Kind {
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE:
			atts := []object{}
			for _, f := range it.Attachments {
				atts = append(atts, object{{"name", f.Name}, {"media_type", f.MimeType}, {"size", f.SizeBytes}, {"sha256", f.BlobSha256}})
			}
			txt := it.Text
			var attLines strings.Builder
			for _, f := range it.Attachments {
				fmt.Fprintf(&attLines, "  attached: %s (%s, %d bytes)\n", f.Name, f.MimeType, f.SizeBytes)
			}
			all = append(all, rawEv{
				at: t, ord: 1,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "user_message"}, {"text", txt}, {"attachments", atts}}
				},
				txt: fmt.Sprintf("> %s\n%s", oneLine(txt, 200), attLines.String()),
			})
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE:
			txt := it.Text
			all = append(all, rawEv{
				at: t, ord: 2,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "agent_message"}, {"text", txt}}
				},
				txt: strings.TrimSpace(txt) + "\n",
			})
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_SYSTEM_NOTICE, sessionv1.TurnItemKind_TURN_ITEM_KIND_HANDOFF:
			txt := it.Text
			all = append(all, rawEv{
				at: t, ord: 2,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "notice"}, {"text", txt}}
				},
				txt: fmt.Sprintf("ℹ %s\n", strings.TrimSpace(txt)),
			})
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE:
			summary := "Write " + it.FilePath
			all = append(all, rawEv{
				at: t, ord: 2,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "tool_call"},
						{"call_id", it.ItemId}, {"kind", "edit"}, {"name", "Write"}, {"summary", summary}, {"decision", "allow"}, {"reason", ""}}
				},
				txt: fmt.Sprintf("• %s\n", summary),
			})
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION, sessionv1.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL:
			summary := firstNonEmpty(it.Command, it.FilePath, it.Text)
			if it.ToolName != "" && it.FilePath != "" && it.Command == "" {
				summary = it.ToolName + " " + it.FilePath
			}
			all = append(all, rawEv{
				at: t, ord: 2,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "tool_call"},
						{"call_id", it.ItemId}, {"kind", "command"}, {"name", cmp.Or(it.ToolName, "bash")}, {"summary", summary}, {"decision", "allow"}, {"reason", ""}}
				},
				txt: fmt.Sprintf("• %s: %s\n", cmp.Or(it.ToolName, "bash"), oneLine(summary, 120)),
			})
			if it.Status != "running" && it.Output != "" {
				outTxt := it.Output
				all = append(all, rawEv{
					at: t, ord: 2,
					obj: func(seq uint64) object {
						return object{{"seq", seq}, {"ts", tsJSON(cmp.Or(it.UpdatedAt, it.CreatedAt))}, {"turn", turn}, {"type", "tool_result"},
							{"call_id", it.ItemId}, {"summary", outTxt}, {"is_error", it.Status == "failed" || it.ExitCode != 0}, {"truncated", it.OutputTruncated}}
					},
					txt: fmt.Sprintf("  %s\n", oneLine(outTxt, 200)),
				})
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST:
			if rq := reqByID[it.RequestId]; rq != nil && rq.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
				qText := ""
				if qs := rq.GetUserInput().GetQuestions(); len(qs) > 0 {
					qText = qs[0].GetText()
				}
				all = append(all, rawEv{
					at: t, ord: 2,
					obj: func(seq uint64) object {
						return object{{"seq", seq}, {"ts", tsJSON(rq.CreatedAt)}, {"turn", turn}, {"type", "question_asked"}, {"question_id", rq.RequestId}, {"text", qText}}
					},
					txt: fmt.Sprintf("? %s\n", oneLine(qText, 200)),
				})
				if rq.Status != sessionv1.RuntimeRequestStatus_RUNTIME_REQUEST_STATUS_PENDING {
					by := rq.GetResolvedBy().GetId()
					if rq.GetResolvedBy().GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER {
						by = "owner"
					}
					var vals []string
					for _, a := range rq.ResolvedAnswers {
						vals = append(vals, a.SelectedOptions...)
						if a.CustomText != "" {
							vals = append(vals, a.CustomText)
						}
					}
					line := "↳ question settled\n"
					switch rq.QuestionResolution {
					case sessionv1.QuestionResolution_QUESTION_RESOLUTION_ANSWERED:
						line = fmt.Sprintf("↳ answered by %s: %s\n", by, strings.Join(vals, ", "))
					case sessionv1.QuestionResolution_QUESTION_RESOLUTION_DECLINED, sessionv1.QuestionResolution_QUESTION_RESOLUTION_TIMED_OUT:
						line = "↳ no answer: the agent continues on its own\n"
					}
					rt := t
					if rq.ResolvedAt != nil {
						rt = rq.ResolvedAt.AsTime()
					}
					all = append(all, rawEv{
						at: rt, ord: 2,
						obj: func(seq uint64) object {
							return object{{"seq", seq}, {"ts", tsJSON(cmp.Or(rq.ResolvedAt, rq.CreatedAt))}, {"turn", turn}, {"type", "question_resolved"},
								{"question_id", rq.RequestId}, {"resolution", strings.ToLower(strings.TrimPrefix(rq.QuestionResolution.String(), "QUESTION_RESOLUTION_"))}, {"by", by}}
						},
						txt: line,
					})
				}
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED:
			all = append(all, rawEv{
				at: t, ord: 1,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "message_received"},
						{"message_id", it.MailMessageId}, {"from", it.MailAddress}, {"from_name", it.MailName}, {"text", it.Text},
						{"in_reply_to", it.MailInReplyTo}, {"delivered", it.MailDelivered}}
				},
				txt: fmt.Sprintf("✉ from %s (%s): %s\n", firstNonEmpty(it.MailName, it.MailAddress), it.MailMessageId, oneLine(it.Text, 200)),
			})
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_SENT:
			all = append(all, rawEv{
				at: t, ord: 2,
				obj: func(seq uint64) object {
					return object{{"seq", seq}, {"ts", tsJSON(it.CreatedAt)}, {"turn", turn}, {"type", "message_sent"},
						{"message_id", it.MailMessageId}, {"to", it.MailAddress}, {"to_name", it.MailName}, {"text", it.Text}, {"in_reply_to", it.MailInReplyTo}}
				},
				txt: fmt.Sprintf("✉ to %s (%s): %s\n", firstNonEmpty(it.MailName, it.MailAddress), it.MailMessageId, oneLine(it.Text, 200)),
			})
		}
	}
	slices.SortStableFunc(all, func(a, b rawEv) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return cmp.Compare(a.ord, b.ord)
	})
	out := make([]synthEvent, len(all))
	for i, r := range all {
		seq := uint64(i + 1)
		out[i] = synthEvent{seq: seq, obj: r.obj(seq), txt: r.txt}
	}
	return out
}

func (a *app) eventsCmd() *cobra.Command {
	var after, before uint64
	var limit uint32
	cmd := &cobra.Command{
		Use:   "events <session id>",
		Short: "Show a session's events (messages, tool calls, turns, questions), a page at a time: the latest by default",
		Long: `Show a session's events, oldest first, a page at a time. Each event has a
seq; without a cursor the page is the latest --limit events.

  --after SEQ   the events right after SEQ: what happened since you last
                looked (pass the last seq you saw, or the session's
                last_event_seq from session get)
  --before SEQ  the events right before SEQ: older ones

The JSON page object has first_seq and last_seq (cursors for --before and
--after) and has_more_before / has_more_after. With --after and nothing
new, events is empty and last_seq is still SEQ.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if after > 0 && before > 0 {
				return fail(exitUsage, "give --after or --before, not both")
			}
			return a.withMachine(cmd.Context(), args[0], func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				res, err := n.Session.GetSession(cmd.Context(), &sessionv1.GetSessionRequest{SessionId: args[0]})
				if err != nil {
					return err
				}
				all := projectionEvents(res.GetProjection())
				lim := int(limit)
				if lim <= 0 {
					lim = 50
				}
				if lim > 1000 {
					lim = 1000
				}
				var page []synthEvent
				var moreBefore, moreAfter bool
				switch {
				case after > 0:
					startIdx := len(all)
					for i, ev := range all {
						if ev.seq > after {
							startIdx = i
							break
						}
					}
					moreBefore = startIdx > 0 && len(all) > 0
					rest := all[startIdx:]
					if len(rest) > lim {
						moreAfter = true
						rest = rest[:lim]
					}
					page = rest
				case before > 0:
					endIdx := 0
					for i, ev := range all {
						if ev.seq < before {
							endIdx = i + 1
						}
					}
					moreAfter = endIdx < len(all)
					sub := all[:endIdx]
					if len(sub) > lim {
						moreBefore = true
						sub = sub[len(sub)-lim:]
					}
					page = sub
				default:
					if len(all) > lim {
						moreBefore = true
						page = all[len(all)-lim:]
					} else {
						page = all
					}
				}
				first, last := before, after
				if len(page) > 0 {
					first, last = page[0].seq, page[len(page)-1].seq
				}
				w := cmd.OutOrStdout()
				if a.output == "json" {
					evs := []object{}
					for _, e := range page {
						evs = append(evs, e.obj)
					}
					return a.report(w, nil, field{"events", evs}, field{"page", object{{"first_seq", first}, {"last_seq", last},
						{"has_more_before", moreBefore}, {"has_more_after", moreAfter}}})
				}
				for _, e := range page {
					fmt.Fprint(w, e.txt)
				}
				note := fmt.Sprintf("events %d–%d", first, last)
				if len(page) == 0 {
					note = "no events here"
				}
				if moreBefore {
					note += fmt.Sprintf("; earlier: --before %d", first)
				}
				if moreAfter {
					note += fmt.Sprintf("; later: --after %d", last)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), note)
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.Uint32Var(&limit, "limit", 50, "events per page (at most 1000)")
	f.Uint64Var(&after, "after", 0, "the events right after this seq")
	f.Uint64Var(&before, "before", 0, "the events right before this seq")
	return cmd
}

func (a *app) sendCmd() *cobra.Command {
	var message, file string
	var files []string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:    "send <session id>",
		Hidden: true,
		Short:  "Send a message to a session; while it works, the message joins the running turn",
		Long: `Send a message to a session.

Attach files with -f (repeatable). An idle session starts a new turn. While a turn runs, Claude Code and Codex
take the message into that turn (at the next tool boundary); agents that
take input only between turns (ACP) run it as the next turn, right after.
--wait returns once the agent has handled every message sent so far.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: `hermesh session send` is now `hermesh message send --to %s`\n", args[0])
			text, err := readPrompt("message", message, file, len(files))
			if err != nil {
				return err
			}
			raws, err := readRawFiles(files)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			return a.withMachine(ctx, args[0], func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				projRes, err := n.Session.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: args[0]})
				if err != nil {
					return err
				}
				before := projRes.GetProjection().GetSession()
				if before.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
					return &exitError{code: exitUsage, kind: "invalid_state", err: fmt.Errorf("session %s is archived (read-only)", args[0]),
						hint: fmt.Sprintf("`hermesh session unarchive %s` to go on with it", args[0])}
				}
				attRefs, uErr := uploadAttachments(ctx, n.Session, raws)
				if uErr != nil {
					return uErr
				}
				res, err := n.Session.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
					SessionId:    args[0],
					Text:         text,
					Attachments:  attRefs,
					Conversation: a.as,
				})
				if err != nil {
					return err
				}
				if !wait {
					return a.started(cmd, res.GetSession(), ref.Name)
				}
				return a.waitTurn(ctx, cmd, c, ref, args[0], before.GetProgress().GetLastEventSeq())
			})
		},
	}
	cmd.Flags().StringVar(&message, "message", "", "message text")
	cmd.Flags().StringArrayVarP(&files, "file", "f", nil, "attach a file (repeatable)")
	cmd.Flags().StringVar(&file, "message-file", "", "read the message from a file (- for stdin)")
	cmd.Flags().BoolVar(&wait, "wait", false, waitHelp)
	cmd.Flags().DurationVar(&timeout, "timeout", 0, timeoutHelp)
	return cmd
}

func (a *app) interruptCmd() *cobra.Command {
	return a.sessionVerb("interrupt", "Stop the turn in progress; the session stays, and the next message goes on in the same conversation",
		func(ctx context.Context, sc sessionv1connect.SessionServiceClient, id string) (*sessionv1.Session, error) {
			res, err := sc.InterruptSession(ctx, &sessionv1.InterruptSessionRequest{SessionId: id})
			if err != nil {
				return nil, err
			}
			return res.GetSession(), nil
		})
}

func (a *app) archiveCmd() *cobra.Command {
	return a.sessionVerb("archive", "Archive a session you are done with: it becomes read-only and its worktree is freed (its branch keeps the work)",
		func(ctx context.Context, sc sessionv1connect.SessionServiceClient, id string) (*sessionv1.Session, error) {
			res, err := sc.UpdateSession(ctx, &sessionv1.UpdateSessionRequest{SessionId: id, HasArchived: true, Archived: true})
			if err != nil {
				return nil, err
			}
			return res.GetSession(), nil
		})
}

func (a *app) unarchiveCmd() *cobra.Command {
	return a.sessionVerb("unarchive", "Bring an archived session back: its worktree is restored and it takes messages again",
		func(ctx context.Context, sc sessionv1connect.SessionServiceClient, id string) (*sessionv1.Session, error) {
			res, err := sc.UpdateSession(ctx, &sessionv1.UpdateSessionRequest{SessionId: id, HasArchived: true, Archived: false})
			if err != nil {
				return nil, err
			}
			return res.GetSession(), nil
		})
}

func (a *app) sessionVerb(verb, short string, do func(ctx context.Context, sc sessionv1connect.SessionServiceClient, id string) (*sessionv1.Session, error)) *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   verb + " <session id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sc := localSocketSessionClient(socket); sc != nil {
				s, err := do(cmd.Context(), sc, args[0])
				if err != nil {
					return err
				}
				return a.showSession(cmd, s, "local", false)
			}
			return a.withMachine(cmd.Context(), args[0], func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				s, err := do(cmd.Context(), n.Session, args[0])
				if err != nil {
					return err
				}
				return a.showSession(cmd, s, ref.Name, false)
			})
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	_ = cmd.Flags().MarkHidden("socket")
	return cmd
}

func (a *app) diffCmd() *cobra.Command {
	var turn uint32
	var checkpointID, socket string
	var stat bool
	cmd := &cobra.Command{
		Use:   "diff <session id>",
		Short: "Show the session's changes (cumulative, or one --turn; worktree projects only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runDiff := func(sc sessionv1connect.SessionServiceClient) error {
				w := cmd.OutOrStdout()
				var offset uint64
				var patch strings.Builder
				for {
					d, err := sc.GetDiff(cmd.Context(), &sessionv1.GetDiffRequest{
						SessionId:    args[0],
						Turn:         turn,
						CheckpointId: checkpointID,
						StatOnly:     stat,
						Offset:       offset,
					})
					if err != nil {
						return err
					}
					if a.output != "json" && stat {
						s := d.GetStat()
						fmt.Fprintf(w, "%s changed, +%d -%d (%s..%s)\n", plural(int(s.GetFilesChanged()), "file"), s.GetInsertions(), s.GetDeletions(), short(d.BaseCommit), short(d.HeadCommit))
						return nil
					}
					if a.output != "json" {
						fmt.Fprint(w, d.Diff)
					} else {
						patch.WriteString(d.Diff)
					}
					if !d.Truncated {
						if a.output != "json" {
							return nil
						}
						return a.report(w, nil, field{"diff", object{{"base_commit", d.BaseCommit}, {"head_commit", d.HeadCommit},
							{"stat", diffStatView(d.Stat)}, {"patch", patch.String()}}})
					}
					offset = d.NextOffset
				}
			}
			if sc := localSocketSessionClient(socket); sc != nil {
				return runDiff(sc)
			}
			return a.withMachine(cmd.Context(), args[0], func(c *peer.Client, n *peer.Machine, ref machineRef) error {
				return runDiff(n.Session)
			})
		},
	}
	cmd.Flags().Uint32Var(&turn, "turn", 0, "only this turn's changes")
	cmd.Flags().BoolVar(&stat, "stat", false, "summary only")
	cmd.Flags().StringVar(&checkpointID, "checkpoint", "", "checkpoint id")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	_ = cmd.Flags().MarkHidden("checkpoint")
	_ = cmd.Flags().MarkHidden("socket")
	return cmd
}

func short(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	return c
}

// ---------------------------------------------------------------------------
// Extended interactive session subcommands (owner & CLI parity).

func (a *app) withSessionClient(ctx context.Context, socket, sessionID string, fn func(sc sessionv1connect.SessionServiceClient, machineName string) error) error {
	if sc := localSocketSessionClient(cmp.Or(socket, os.Getenv("HERMESH_SOCKET"))); sc != nil {
		return fn(sc, "local")
	}
	if sessionID != "" {
		return a.withMachine(ctx, sessionID, func(_ *peer.Client, n *peer.Machine, ref machineRef) error {
			return fn(n.Session, ref.Name)
		})
	}
	sc := localSocketSessionClient(ownerDefaultSocket())
	return fn(sc, "local")
}

func (a *app) steerCmd() *cobra.Command {
	var socket string
	var files []string
	cmd := &cobra.Command{
		Use:   "steer <session id> <text>",
		Short: "Steer the active turn in real time (or enqueue next if provider lacks mid-turn steer)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			raws, err := readRawFiles(files)
			if err != nil {
				return err
			}
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, machineName string) error {
				atts, err := uploadAttachments(cmd.Context(), sc, raws)
				if err != nil {
					return err
				}
				res, err := sc.DispatchMessage(cmd.Context(), &sessionv1.DispatchMessageRequest{
					SessionId:   args[0],
					Text:        args[1],
					Attachments: atts,
					Intent:      sessionv1.DispatchIntent_DISPATCH_INTENT_STEER,
				})
				if err != nil {
					return err
				}
				return a.showSession(cmd, res.GetSession(), machineName, false)
			})
		},
	}
	cmd.Flags().StringArrayVarP(&files, "file", "f", nil, "attach a file")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) queueCmd() *cobra.Command {
	var socket, op, runID, beforeRunID, text string
	cmd := &cobra.Command{
		Use:   "queue <session id>",
		Short: "Manage queued prompts on a session (edit, cancel, reorder, promote_steer, resume)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var qop sessionv1.QueueOperation
			switch strings.ToLower(op) {
			case "edit":
				qop = sessionv1.QueueOperation_QUEUE_OPERATION_EDIT
			case "delete", "remove", "cancel":
				qop = sessionv1.QueueOperation_QUEUE_OPERATION_CANCEL
			case "reorder":
				qop = sessionv1.QueueOperation_QUEUE_OPERATION_REORDER
			case "promote_steer", "promote":
				qop = sessionv1.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER
			case "resume", "resume_queue":
				qop = sessionv1.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE
			default:
				return fail(exitUsage, "unknown --op %q", op)
			}
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, machineName string) error {
				res, err := sc.ManageQueue(cmd.Context(), &sessionv1.ManageQueueRequest{
					SessionId:   args[0],
					Operation:   qop,
					RunId:       runID,
					BeforeRunId: beforeRunID,
					Text:        text,
				})
				if err != nil {
					return err
				}
				return a.showSession(cmd, res.GetSession(), machineName, false)
			})
		},
	}
	cmd.Flags().StringVar(&op, "op", "", "queue operation (edit, cancel, reorder, promote_steer, resume)")
	cmd.Flags().StringVar(&runID, "run", "", "queued run id")
	cmd.Flags().StringVar(&beforeRunID, "before-run", "", "target run id to place before")
	cmd.Flags().StringVar(&text, "text", "", "updated prompt text")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) configCmd() *cobra.Command {
	var socket, title, locale, agent, model, effort, runtimeMode, interactionMode string
	var options []string
	var pin, unpin, regenerateTitle bool
	cmd := &cobra.Command{
		Use:   "config <session id>",
		Short: "Update a session's title, model, runtime mode, interaction mode, or pin state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effLocale := locale
			if effLocale == "" && regenerateTitle && a.mem == nil {
				effLocale = runtime.DefaultLocaleFromEnv()
			}
			req := &sessionv1.UpdateSessionRequest{
				SessionId:       args[0],
				Title:           title,
				RegenerateTitle: regenerateTitle,
				Locale:          effLocale,
				RuntimeMode:     parseRuntimeModeFlag(runtimeMode),
				InteractionMode: parseInteractionModeFlag(interactionMode),
			}
			if agent != "" || model != "" || effort != "" || len(options) > 0 {
				req.ModelSelection = buildModelSelection(agent, model, effort, options)
			}
			if pin || unpin {
				req.HasPinned = true
				req.Pinned = pin && !unpin
			}
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, machineName string) error {
				res, err := sc.UpdateSession(cmd.Context(), req)
				if err != nil {
					return err
				}
				return a.showSession(cmd, res.GetSession(), machineName, false)
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().BoolVar(&regenerateTitle, "regenerate-title", false, "regenerate the session title with AI")
	cmd.Flags().StringVar(&locale, "locale", "", "BCP 47 language tag for generated title (e.g. zh-CN, en)")
	cmd.Flags().StringVar(&agent, "agent", "", "provider instance id")
	cmd.Flags().StringVar(&model, "model", "", "model id")
	cmd.Flags().StringVar(&effort, "effort", "", "reasoning effort")
	cmd.Flags().StringArrayVar(&options, "option", nil, "provider option key=val")
	cmd.Flags().StringVar(&runtimeMode, "runtime-mode", "", "runtime permission mode")
	cmd.Flags().StringVar(&interactionMode, "mode", "", "interaction mode (default, plan)")
	cmd.Flags().BoolVar(&pin, "pin", false, "pin session")
	cmd.Flags().BoolVar(&unpin, "unpin", false, "unpin session")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) respondCmd() *cobra.Command {
	var socket, decision, feedback, secretValue string
	var approvePlan, executePlan bool
	cmd := &cobra.Command{
		Use:   "respond <session id> <request id>",
		Short: "Respond to a tool approval, plan review, or secret request on a session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dec sessionv1.ApprovalDecision
			switch strings.ToLower(decision) {
			case "once", "approve_once", "accept_once":
				dec = sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
			case "session", "approve_session", "accept_for_session":
				dec = sessionv1.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_SESSION
			case "deny", "decline":
				dec = sessionv1.ApprovalDecision_APPROVAL_DECISION_DECLINE
			case "cancel":
				dec = sessionv1.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN
			}
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, machineName string) error {
				res, err := sc.RespondRuntimeRequest(cmd.Context(), &sessionv1.RespondRuntimeRequestRequest{
					SessionId:   args[0],
					RequestId:   args[1],
					Decision:    dec,
					ApprovePlan: approvePlan || executePlan,
					Feedback:    feedback,
					SecretValue: secretValue,
				})
				if err != nil {
					return err
				}
				return a.showSession(cmd, res.GetSession(), machineName, false)
			})
		},
	}
	cmd.Flags().StringVar(&decision, "decision", "", "approval decision (once, session, deny, cancel)")
	cmd.Flags().BoolVar(&approvePlan, "approve-plan", false, "approve plan review")
	cmd.Flags().BoolVar(&executePlan, "execute-plan", false, "approve plan and switch to default execution mode")
	cmd.Flags().StringVar(&feedback, "feedback", "", "feedback text")
	cmd.Flags().StringVar(&secretValue, "secret", "", "ephemeral secret value")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) checkpointsCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "checkpoints <session id>",
		Short: "List Git workspace checkpoints for a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, _ string) error {
				res, err := sc.ListCheckpoints(cmd.Context(), &sessionv1.ListCheckpointsRequest{SessionId: args[0]})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"checkpoints", res.GetCheckpoints()})
				}
				for _, cp := range res.GetCheckpoints() {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  turn=%d  files=%d\n",
						cp.CheckpointId, cp.RunOrdinal, len(cp.Files))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) rollbackCmd() *cobra.Command {
	var socket string
	var restoreFiles bool
	cmd := &cobra.Command{
		Use:   "rollback <session id> <checkpoint id>",
		Short: "Roll back a session's workspace to an earlier checkpoint",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, machineName string) error {
				res, err := sc.RollbackCheckpoint(cmd.Context(), &sessionv1.RollbackCheckpointRequest{
					SessionId:    args[0],
					CheckpointId: args[1],
					RestoreFiles: restoreFiles,
				})
				if err != nil {
					return err
				}
				return a.showSession(cmd, res.GetSession(), machineName, false)
			})
		},
	}
	cmd.Flags().BoolVar(&restoreFiles, "restore-files", true, "restore workspace files to checkpoint tree")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) forkCmd() *cobra.Command {
	var socket, title, locale, agent, model, effort string
	var upToTurn uint32
	cmd := &cobra.Command{
		Use:   "fork <session id>",
		Short: "Fork a session (and its Git checkpoint tree) into a new independent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effLocale := locale
			if effLocale == "" && a.mem == nil {
				effLocale = runtime.DefaultLocaleFromEnv()
			}
			req := &sessionv1.ForkSessionRequest{
				SourceSessionId: args[0],
				UpToRunOrdinal:  upToTurn,
				Title:           title,
				Locale:          effLocale,
			}
			if agent != "" || model != "" || effort != "" {
				req.ModelSelection = buildModelSelection(agent, model, effort, nil)
			}
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, machineName string) error {
				res, err := sc.ForkSession(cmd.Context(), req)
				if err != nil {
					return err
				}
				return a.showSession(cmd, res.GetSession(), machineName, false)
			})
		},
	}
	cmd.Flags().Uint32Var(&upToTurn, "turn", 0, "fork up to this run ordinal (0: latest)")
	cmd.Flags().StringVar(&title, "title", "", "title for forked session")
	cmd.Flags().StringVar(&locale, "locale", "", "BCP 47 language tag for forked session title (e.g. zh-CN, en)")
	cmd.Flags().StringVar(&agent, "agent", "", "provider instance id")
	cmd.Flags().StringVar(&model, "model", "", "model id")
	cmd.Flags().StringVar(&effort, "effort", "", "reasoning effort")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) deleteCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "delete <session id>",
		Short: "Permanently delete a session and its worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withSessionClient(cmd.Context(), socket, args[0], func(sc sessionv1connect.SessionServiceClient, _ string) error {
				_, err := sc.DeleteSession(cmd.Context(), &sessionv1.DeleteSessionRequest{SessionId: args[0]})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"deleted", args[0]})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) providersCmd() *cobra.Command {
	var socket, machineID, projectID string
	var refresh bool
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List provider instances and capabilities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withSessionClient(cmd.Context(), cmp.Or(socket, ownerDefaultSocket()), "", func(sc sessionv1connect.SessionServiceClient, _ string) error {
				res, err := sc.ListProviders(cmd.Context(), &sessionv1.ListProvidersRequest{
					MachineId: machineID,
					ProjectId: projectID,
					Refresh:   refresh,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"providers", res.GetProviders()}, field{"max_runtime_mode", res.GetMaxRuntimeMode().String()})
				}
				for _, p := range res.GetProviders() {
					fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) installed=%v auth=%v\n",
						p.InstanceId, p.Driver, p.Installed, p.Authenticated)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&machineID, "machine", "", "machine id")
	cmd.Flags().StringVar(&projectID, "project", "", "project id")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "force fresh probe")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}

func (a *app) outboxCmd() *cobra.Command {
	var socket, machineID string
	cmd := &cobra.Command{
		Use:   "outbox",
		Short: "List pending offline outbox commands",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withSessionClient(cmd.Context(), cmp.Or(socket, ownerDefaultSocket()), "", func(sc sessionv1connect.SessionServiceClient, _ string) error {
				res, err := sc.ListOutbox(cmd.Context(), &sessionv1.ListOutboxRequest{MachineId: machineID})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(cmd.OutOrStdout(), nil, field{"entries", res.GetEntries()})
				}
				for _, e := range res.GetEntries() {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", e.CommandId, e.MachineId, e.SessionId, e.Method)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&machineID, "machine", "", "machine id")
	cmd.Flags().StringVar(&socket, "socket", "", "local owner API socket")
	return cmd
}
