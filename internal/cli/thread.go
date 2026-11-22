package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2/threadv2connect"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
)

func parseRuntimeMode(s string) (identityv1.RuntimeMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "default":
		return identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED, nil
	case "supervised":
		return identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, nil
	case "auto-edits", "auto_edits":
		return identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, nil
	case "auto":
		return identityv1.RuntimeMode_RUNTIME_MODE_AUTO, nil
	case "full-access", "full_access", "full":
		return identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS, nil
	default:
		return 0, fail(exitUsage, "unknown runtime mode %q (supervised, auto-edits, auto, full-access)", s)
	}
}

func parseInteractionMode(s string) (threadv2.InteractionMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return threadv2.InteractionMode_INTERACTION_MODE_UNSPECIFIED, nil
	case "code", "default":
		return threadv2.InteractionMode_INTERACTION_MODE_CODE, nil
	case "plan":
		return threadv2.InteractionMode_INTERACTION_MODE_PLAN, nil
	default:
		return 0, fail(exitUsage, "unknown interaction mode %q (code, plan)", s)
	}
}

func parseOptionsFlags(pairs []string) (map[string]*threadv2.OptionValue, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := map[string]*threadv2.OptionValue{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fail(exitUsage, "invalid --option %q (expected key=value)", p)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch strings.ToLower(v) {
		case "true":
			out[k] = &threadv2.OptionValue{Kind: &threadv2.OptionValue_BoolValue{BoolValue: true}}
		case "false":
			out[k] = &threadv2.OptionValue{Kind: &threadv2.OptionValue_BoolValue{BoolValue: false}}
		default:
			out[k] = &threadv2.OptionValue{Kind: &threadv2.OptionValue_StringValue{StringValue: v}}
		}
	}
	return out, nil
}

// threadClient resolves a ThreadServiceClient either via an Owner local socket
// (--socket / $HERMESH_SOCKET) or via the caller's E2E encrypted Grant tunnel.
func (a *app) withThreadClient(ctx context.Context, sockFlag, machineHint, threadID, projectHint string, fn func(tc threadv2connect.ThreadServiceClient, targetMachineID string) error) error {
	sock := strings.TrimSpace(sockFlag)
	if sock == "" {
		sock = strings.TrimSpace(os.Getenv("HERMESH_SOCKET"))
	}
	if sock != "" {
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return localipc.Dial(ctx, sock)
		}}}
		return fn(threadv2connect.NewThreadServiceClient(hc, "http://local"), machineHint)
	}

	// If no client state.json exists in HERMESH_HOME, fall back to local owner socket if listening.
	if a.mem == nil {
		if _, err := os.Stat(a.statePath()); os.IsNotExist(err) {
			defSock := paths.LocalAPI(paths.DataDir())
			conn, dErr := localipc.Dial(ctx, defSock)
			if dErr == nil {
				_ = conn.Close()
				hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return localipc.Dial(ctx, defSock)
				}}}
				return fn(threadv2connect.NewThreadServiceClient(hc, "http://local"), machineHint)
			}
		}
	}

	// Connect via cryptographic Grant (`peer.Client`) directly to the target machine!
	c, machines, err := a.connect(ctx, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	var target machineRef
	switch {
	case machineHint != "":
		for _, m := range machines {
			if m.ID == machineHint || strings.EqualFold(m.Name, machineHint) {
				target = m
				break
			}
		}
		if target.ID == "" {
			return fail(exitUsage, "unknown machine %q", machineHint)
		}
	case threadID != "":
		for _, m := range machines {
			if kernel.OwnsThread(m.ID, threadID) {
				target = m
				break
			}
		}
		if target.ID == "" && len(machines) == 1 {
			target = machines[0]
		}
		if target.ID == "" {
			return fail(exitUsage, "could not determine machine for thread %q; pass --machine", threadID)
		}
	case projectHint != "" && projectHint != "scratch":
		ref, _, rErr := a.resolveProject(ctx, c, machines, projectHint)
		if rErr != nil {
			return rErr
		}
		target = ref
	default:
		for _, m := range machines {
			if m.Online {
				target = m
				break
			}
		}
		if target.ID == "" && len(machines) > 0 {
			target = machines[0]
		}
		if target.ID == "" {
			return fail(exitUnavailable, "no machines available")
		}
	}

	if !target.Online {
		return fail(exitUnavailable, "machine %s is offline", target.Name)
	}
	pm, err := c.Machine(ctx, target.ID)
	if err != nil {
		return &exitError{code: exitUnavailable, err: err}
	}
	return fn(pm.Thread, target.ID)
}

func uploadCLIAttachments(ctx context.Context, tc threadv2connect.ThreadServiceClient, files []string) ([]*threadv2.AttachmentRef, error) {
	var refs []*threadv2.AttachmentRef
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fail(exitUsage, "%v", err)
		}
		res, err := tc.UploadBlob(ctx, &threadv2.UploadBlobRequest{
			Name: filepath.Base(f),
			Data: data,
		})
		if err != nil {
			return nil, err
		}
		refs = append(refs, res.Attachment)
	}
	return refs, nil
}

func waitThreadSettled(ctx context.Context, tc threadv2connect.ThreadServiceClient, threadID string) (*threadv2.ThreadProjection, error) {
	for {
		res, err := tc.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: threadID})
		if err != nil {
			return nil, err
		}
		st := res.Projection.GetThread().GetStatus()
		switch st {
		case threadv2.ThreadStatus_THREAD_STATUS_IDLE,
			threadv2.ThreadStatus_THREAD_STATUS_WAITING_REQUEST,
			threadv2.ThreadStatus_THREAD_STATUS_FAILED,
			threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED:
			return res.Projection, nil
		case threadv2.ThreadStatus_THREAD_STATUS_QUEUED:
			if res.Projection.GetThread().GetQueueHeld() || res.Projection.GetThread().GetOfflineQueued() {
				return res.Projection, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
	}
}

func (a *app) threadCmd() *cobra.Command {
	var sock string
	cmd := group(&cobra.Command{
		Use:   "thread",
		Short: "Launch, steer, queue, approve, configure, and rewind v2 coding threads",
	})
	cmd.PersistentFlags().StringVar(&sock, "socket", "", "Owner local API socket path (defaults to $HERMESH_SOCKET or Grant credentials)")

	// 1. providers
	var provMachine, provProject string
	var provRefresh bool
	providersCmd := &cobra.Command{
		Use:   "providers",
		Short: "List available provider instances, models, options, and runtime mode ceiling",
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withThreadClient(c.Context(), sock, provMachine, "", provProject, func(tc threadv2connect.ThreadServiceClient, mid string) error {
				res, err := tc.ListProviders(c.Context(), &threadv2.ListProvidersRequest{
					MachineId: mid, ProjectId: provProject, Refresh: provRefresh,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"providers", res.Providers}, field{"max_runtime_mode", res.MaxRuntimeMode.String()})
				}
				tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "INSTANCE\tDRIVER\tINSTALLED\tAUTH\tMODELS")
				for _, p := range res.Providers {
					var slugs []string
					for _, m := range p.Models {
						slugs = append(slugs, m.Slug)
					}
					fmt.Fprintf(tw, "%s\t%s\t%v\t%v\t%s\n", p.InstanceId, p.Driver, p.Installed, p.Authenticated, strings.Join(slugs, ","))
				}
				return tw.Flush()
			})
		},
	}
	providersCmd.Flags().StringVar(&provMachine, "machine", "", "Target machine ID or name")
	providersCmd.Flags().StringVar(&provProject, "project", "", "Project ID for workspace commands/skills")
	providersCmd.Flags().BoolVar(&provRefresh, "refresh", false, "Force live provider probe")

	// 2. start
	var stMachine, stProject, stProvider, stModel, stRuntime, stInteraction, stWorkspace, stBase, stBranch, stPrompt, stTitle, stCmdID string
	var stOpts, stFiles []string
	var stWait bool
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Launch a new v2 thread (optionally with an initial prompt)",
		RunE: func(c *cobra.Command, _ []string) error {
			rm, err := parseRuntimeMode(stRuntime)
			if err != nil {
				return err
			}
			im, err := parseInteractionMode(stInteraction)
			if err != nil {
				return err
			}
			opts, err := parseOptionsFlags(stOpts)
			if err != nil {
				return err
			}
			wsMode := identityv1.Workspace_WORKSPACE_UNSPECIFIED
			switch strings.ToLower(stWorkspace) {
			case "in-place", "inplace":
				wsMode = identityv1.Workspace_WORKSPACE_IN_PLACE
			case "worktree":
				wsMode = identityv1.Workspace_WORKSPACE_WORKTREE
			}
			projID := stProject
			if _, pName, ok := strings.Cut(stProject, "/"); ok {
				projID = pName
			}
			return a.withThreadClient(c.Context(), sock, stMachine, "", stProject, func(tc threadv2connect.ThreadServiceClient, mid string) error {
				atts, err := uploadCLIAttachments(c.Context(), tc, stFiles)
				if err != nil {
					return err
				}
				var sel *threadv2.ModelSelection
				if stProvider != "" || stModel != "" || len(opts) > 0 {
					sel = &threadv2.ModelSelection{InstanceId: stProvider, Model: stModel, Options: opts}
				}
				res, err := tc.LaunchThread(c.Context(), &threadv2.LaunchThreadRequest{
					CommandId:       stCmdID,
					MachineId:       mid,
					ProjectId:       projID,
					Title:           stTitle,
					ModelSelection:  sel,
					RuntimeMode:     rm,
					InteractionMode: im,
					Workspace:       &threadv2.WorkspaceStrategy{Mode: wsMode, BaseRef: stBase, Branch: stBranch},
					InitialPrompt:   stPrompt,
					Attachments:     atts,
				})
				if err != nil {
					return err
				}
				th := res.Thread
				var proj *threadv2.ThreadProjection
				if stWait && stPrompt != "" && !res.QueuedOffline {
					proj, err = waitThreadSettled(c.Context(), tc, th.ThreadId)
					if err != nil {
						return err
					}
					th = proj.Thread
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil,
						field{"thread", th},
						field{"queued_offline", res.QueuedOffline},
						field{"projection", proj},
					)
				}
				fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\n", th.ThreadId, th.Status.String(), th.Title)
				return nil
			})
		},
	}
	startCmd.Flags().StringVar(&stMachine, "machine", "", "Machine ID or name")
	startCmd.Flags().StringVar(&stProject, "project", "", "Project ID")
	startCmd.Flags().StringVar(&stProvider, "provider", "", "Provider instance ID (claude, codex, gemini, ...)")
	startCmd.Flags().StringVar(&stModel, "model", "", "Model slug")
	startCmd.Flags().StringSliceVar(&stOpts, "option", nil, "Model option key=value")
	startCmd.Flags().StringVar(&stRuntime, "runtime-mode", "", "Permission mode: supervised, auto-edits, auto, full-access")
	startCmd.Flags().StringVar(&stInteraction, "mode", "", "Interaction mode: code, plan")
	startCmd.Flags().StringVar(&stWorkspace, "workspace", "", "Workspace strategy: in-place, worktree")
	startCmd.Flags().StringVar(&stBase, "base", "", "Base git ref for worktree")
	startCmd.Flags().StringVar(&stBranch, "branch", "", "Worktree branch name")
	startCmd.Flags().StringVar(&stPrompt, "prompt", "", "Initial prompt")
	startCmd.Flags().StringVar(&stTitle, "title", "", "Thread title")
	startCmd.Flags().StringVar(&stCmdID, "idempotency-key", "", "Idempotent command ID")
	startCmd.Flags().StringSliceVar(&stFiles, "file", nil, "Attach file(s)")
	startCmd.Flags().BoolVar(&stWait, "wait", false, "Wait until initial run settles")

	// 3. list
	var lsMachine, lsProject string
	var lsArchived bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List threads",
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withThreadClient(c.Context(), sock, lsMachine, "", lsProject, func(tc threadv2connect.ThreadServiceClient, mid string) error {
				res, err := tc.ListThreads(c.Context(), &threadv2.ListThreadsRequest{
					MachineId: mid, ProjectId: lsProject, IncludeArchived: lsArchived,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"threads", res.Threads})
				}
				for _, t := range res.Threads {
					fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\t%s\n", t.ThreadId, t.Status.String(), t.ProjectId, t.Title)
				}
				return nil
			})
		},
	}
	listCmd.Flags().StringVar(&lsMachine, "machine", "", "Filter by machine")
	listCmd.Flags().StringVar(&lsProject, "project", "", "Filter by project")
	listCmd.Flags().BoolVar(&lsArchived, "archived", false, "Include archived threads")

	// 4. get
	var getMachine string
	getCmd := &cobra.Command{
		Use:   "get <thread_id>",
		Short: "Inspect full thread projection (runs, items, requests, checkpoints)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tid := args[0]
			return a.withThreadClient(c.Context(), sock, getMachine, tid, "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.GetThread(c.Context(), &threadv2.GetThreadRequest{ThreadId: tid})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"projection", res.Projection})
				}
				th := res.Projection.GetThread()
				fmt.Fprintf(c.OutOrStdout(), "%s (%s) runs=%d queued=%d\n", th.GetTitle(), th.GetStatus().String(), len(res.Projection.GetRuns()), th.GetQueuedRunCount())
				for _, it := range res.Projection.GetItems() {
					if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE {
						fmt.Fprintln(c.OutOrStdout(), it.Text)
					}
				}
				return nil
			})
		},
	}
	getCmd.Flags().StringVar(&getMachine, "machine", "", "Target machine")

	// 5. send & steer
	makeDispatch := func(use, short string, defaultIntent threadv2.DispatchIntent) *cobra.Command {
		var dmMachine, dmText, dmIntent, dmProvider, dmModel, dmRuntime, dmInteraction, dmCmdID string
		var dmOpts, dmFiles []string
		var dmWait bool
		dc := &cobra.Command{
			Use:   use + " <thread_id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				tid := args[0]
				intent := defaultIntent
				switch strings.ToLower(dmIntent) {
				case "start":
					intent = threadv2.DispatchIntent_DISPATCH_INTENT_START
				case "steer":
					intent = threadv2.DispatchIntent_DISPATCH_INTENT_STEER
				case "queue":
					intent = threadv2.DispatchIntent_DISPATCH_INTENT_QUEUE
				case "fresh":
					intent = threadv2.DispatchIntent_DISPATCH_INTENT_FRESH_CONTEXT
				}
				rm, err := parseRuntimeMode(dmRuntime)
				if err != nil {
					return err
				}
				im, err := parseInteractionMode(dmInteraction)
				if err != nil {
					return err
				}
				opts, err := parseOptionsFlags(dmOpts)
				if err != nil {
					return err
				}
				return a.withThreadClient(c.Context(), sock, dmMachine, tid, "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
					atts, err := uploadCLIAttachments(c.Context(), tc, dmFiles)
					if err != nil {
						return err
					}
					var sel *threadv2.ModelSelection
					if dmProvider != "" || dmModel != "" || len(opts) > 0 {
						sel = &threadv2.ModelSelection{InstanceId: dmProvider, Model: dmModel, Options: opts}
					}
					res, err := tc.DispatchMessage(c.Context(), &threadv2.DispatchMessageRequest{
						CommandId:       dmCmdID,
						ThreadId:        tid,
						Text:            dmText,
						Attachments:     atts,
						Intent:          intent,
						ModelSelection:  sel,
						RuntimeMode:     rm,
						InteractionMode: im,
					})
					if err != nil {
						return err
					}
					var proj *threadv2.ThreadProjection
					if dmWait && !res.QueuedOffline && res.GetRun().GetStatus() != threadv2.RunStatus_RUN_STATUS_QUEUED {
						proj, err = waitThreadSettled(c.Context(), tc, tid)
						if err != nil {
							return err
						}
					}
					if a.output == "json" {
						return a.report(c.OutOrStdout(), nil,
							field{"thread", res.Thread},
							field{"run", res.Run},
							field{"steered_active_run", res.SteeredActiveRun},
							field{"queued_offline", res.QueuedOffline},
							field{"projection", proj},
						)
					}
					fmt.Fprintf(c.OutOrStdout(), "%s\t%s\n", res.GetThread().GetThreadId(), res.GetThread().GetStatus().String())
					return nil
				})
			},
		}
		dc.Flags().StringVar(&dmMachine, "machine", "", "Target machine")
		dc.Flags().StringVar(&dmText, "prompt", "", "Message text")
		dc.Flags().StringVar(&dmIntent, "intent", "", "Dispatch intent: auto, start, steer, queue, fresh")
		dc.Flags().StringVar(&dmProvider, "provider", "", "Override provider instance")
		dc.Flags().StringVar(&dmModel, "model", "", "Override model slug")
		dc.Flags().StringSliceVar(&dmOpts, "option", nil, "Override model option key=value")
		dc.Flags().StringVar(&dmRuntime, "runtime-mode", "", "Override runtime mode")
		dc.Flags().StringVar(&dmInteraction, "mode", "", "Override interaction mode (code, plan)")
		dc.Flags().StringVar(&dmCmdID, "idempotency-key", "", "Idempotent command ID")
		dc.Flags().StringSliceVar(&dmFiles, "file", nil, "Attach file(s)")
		dc.Flags().BoolVar(&dmWait, "wait", false, "Wait until thread settles")
		return dc
	}

	// 6. interrupt
	var intMachine string
	var intForce, intHold bool
	interruptCmd := &cobra.Command{
		Use:   "interrupt <thread_id>",
		Short: "Interrupt or force-stop the active run on a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tid := args[0]
			return a.withThreadClient(c.Context(), sock, intMachine, tid, "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.InterruptRun(c.Context(), &threadv2.InterruptRunRequest{
					ThreadId: tid, Force: intForce, HoldQueue: intHold,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"thread", res.Thread})
				}
				fmt.Fprintln(c.OutOrStdout(), res.Thread.Status.String())
				return nil
			})
		},
	}
	interruptCmd.Flags().StringVar(&intMachine, "machine", "", "Target machine")
	interruptCmd.Flags().BoolVar(&intForce, "force", false, "Force kill unresponsive process group")
	interruptCmd.Flags().BoolVar(&intHold, "hold-queue", false, "Pause queued follow-ups")

	// 7. queue
	var qMachine, qRunID, qBeforeID, qText, qOp string
	queueCmd := &cobra.Command{
		Use:   "queue <thread_id>",
		Short: "Manage queued follow-up messages (edit, cancel, reorder, promote, resume)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tid := args[0]
			var op threadv2.QueueOperation
			switch strings.ToLower(qOp) {
			case "edit":
				op = threadv2.QueueOperation_QUEUE_OPERATION_EDIT
			case "cancel":
				op = threadv2.QueueOperation_QUEUE_OPERATION_CANCEL
			case "reorder":
				op = threadv2.QueueOperation_QUEUE_OPERATION_REORDER
			case "promote":
				op = threadv2.QueueOperation_QUEUE_OPERATION_PROMOTE_TO_STEER
			case "resume":
				op = threadv2.QueueOperation_QUEUE_OPERATION_RESUME_QUEUE
			default:
				return fail(exitUsage, "--op must be edit, cancel, reorder, promote, or resume")
			}
			return a.withThreadClient(c.Context(), sock, qMachine, tid, "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.ManageQueue(c.Context(), &threadv2.ManageQueueRequest{
					ThreadId: tid, Operation: op, RunId: qRunID, BeforeRunId: qBeforeID, Text: qText,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"thread", res.Thread}, field{"queued_runs", res.QueuedRuns})
				}
				fmt.Fprintf(c.OutOrStdout(), "queued=%d\n", len(res.QueuedRuns))
				return nil
			})
		},
	}
	queueCmd.Flags().StringVar(&qMachine, "machine", "", "Target machine")
	queueCmd.Flags().StringVar(&qOp, "op", "", "Operation: edit, cancel, reorder, promote, resume")
	queueCmd.Flags().StringVar(&qRunID, "run", "", "Target queued run ID")
	queueCmd.Flags().StringVar(&qBeforeID, "before", "", "Insert before run ID (reorder)")
	queueCmd.Flags().StringVar(&qText, "text", "", "Updated text (edit)")

	// 8. approve (for TOOL_APPROVAL, PLAN_REVIEW, USER_INPUT, SECRET_INPUT)
	var apMachine, apReqID, apDecision, apFeedback, apSecret string
	var apAnswers []string
	var apPlan bool
	approveCmd := &cobra.Command{
		Use:   "respond <thread_id>",
		Short: "Resolve a pending RuntimeRequest (approval, question, plan review, or secret)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tid := args[0]
			dec := threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
			switch strings.ToLower(apDecision) {
			case "", "once", "accept":
				dec = threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_ONCE
			case "thread", "always":
				dec = threadv2.ApprovalDecision_APPROVAL_DECISION_ACCEPT_FOR_THREAD
			case "decline", "deny":
				dec = threadv2.ApprovalDecision_APPROVAL_DECISION_DECLINE
			case "cancel":
				dec = threadv2.ApprovalDecision_APPROVAL_DECISION_CANCEL_TURN
			default:
				return fail(exitUsage, "unknown --decision %q (once, thread, decline, cancel)", apDecision)
			}
			ansMap := map[string]*threadv2.QuestionAnswer{}
			for _, pair := range apAnswers {
				qid, val, ok := strings.Cut(pair, "=")
				if ok {
					ansMap[qid] = &threadv2.QuestionAnswer{SelectedOptions: strings.Split(val, ",")}
				}
			}
			return a.withThreadClient(c.Context(), sock, apMachine, tid, "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				reqID := apReqID
				if reqID == "" {
					gt, err := tc.GetThread(c.Context(), &threadv2.GetThreadRequest{ThreadId: tid})
					if err != nil {
						return err
					}
					reqID = gt.Projection.GetThread().GetPendingRequestId()
				}
				res, err := tc.RespondRuntimeRequest(c.Context(), &threadv2.RespondRuntimeRequestRequest{
					ThreadId:    tid,
					RequestId:   reqID,
					Decision:    dec,
					Feedback:    apFeedback,
					Answers:     ansMap,
					ApprovePlan: apPlan,
					SecretValue: apSecret,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"thread", res.Thread}, field{"request", res.Request})
				}
				fmt.Fprintln(c.OutOrStdout(), "resolved", reqID)
				return nil
			})
		},
	}
	approveCmd.Flags().StringVar(&apMachine, "machine", "", "Target machine")
	approveCmd.Flags().StringVar(&apReqID, "request", "", "Request ID (defaults to active pending request)")
	approveCmd.Flags().StringVar(&apDecision, "decision", "once", "Decision: once, thread, decline, cancel")
	approveCmd.Flags().StringVar(&apFeedback, "feedback", "", "Feedback on decline or plan revision")
	approveCmd.Flags().StringSliceVar(&apAnswers, "answer", nil, "Question answer qid=opt1,opt2")
	approveCmd.Flags().BoolVar(&apPlan, "approve-plan", false, "Approve proposed plan and switch to Code mode")
	approveCmd.Flags().StringVar(&apSecret, "secret", "", "Ephemeral secret value (never persisted)")

	// 9. config
	var cfMachine, cfProvider, cfModel, cfRuntime, cfInteraction, cfTitle string
	var cfOpts []string
	var cfArchive, cfUnarchive, cfPin bool
	configCmd := &cobra.Command{
		Use:   "config <thread_id>",
		Short: "Update thread model, options, permission mode, title, pin, or archive state",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tid := args[0]
			rm, err := parseRuntimeMode(cfRuntime)
			if err != nil {
				return err
			}
			im, err := parseInteractionMode(cfInteraction)
			if err != nil {
				return err
			}
			opts, err := parseOptionsFlags(cfOpts)
			if err != nil {
				return err
			}
			return a.withThreadClient(c.Context(), sock, cfMachine, tid, "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				var sel *threadv2.ModelSelection
				if cfProvider != "" || cfModel != "" || len(opts) > 0 {
					sel = &threadv2.ModelSelection{InstanceId: cfProvider, Model: cfModel, Options: opts}
				}
				req := &threadv2.UpdateThreadConfigRequest{
					ThreadId:        tid,
					ModelSelection:  sel,
					RuntimeMode:     rm,
					InteractionMode: im,
					Title:           cfTitle,
				}
				if c.Flags().Changed("pin") {
					req.HasPinned = true
					req.Pinned = cfPin
				}
				if cfArchive {
					req.HasArchived = true
					req.Archived = true
				} else if cfUnarchive {
					req.HasArchived = true
					req.Archived = false
				}
				res, err := tc.UpdateThreadConfig(c.Context(), req)
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"thread", res.Thread}, field{"transition_applied", res.TransitionApplied})
				}
				fmt.Fprintln(c.OutOrStdout(), res.TransitionApplied)
				return nil
			})
		},
	}
	configCmd.Flags().StringVar(&cfMachine, "machine", "", "Target machine")
	configCmd.Flags().StringVar(&cfProvider, "provider", "", "Provider instance")
	configCmd.Flags().StringVar(&cfModel, "model", "", "Model slug")
	configCmd.Flags().StringSliceVar(&cfOpts, "option", nil, "Option key=value")
	configCmd.Flags().StringVar(&cfRuntime, "runtime-mode", "", "Runtime mode")
	configCmd.Flags().StringVar(&cfInteraction, "mode", "", "Interaction mode")
	configCmd.Flags().StringVar(&cfTitle, "title", "", "New thread title")
	configCmd.Flags().BoolVar(&cfPin, "pin", false, "Pin thread")
	configCmd.Flags().BoolVar(&cfArchive, "archive", false, "Archive thread")
	configCmd.Flags().BoolVar(&cfUnarchive, "unarchive", false, "Unarchive thread")

	// 10. checkpoints, rollback, diff, fork, delete, outbox
	var cpMachine string
	ckptsCmd := &cobra.Command{
		Use:   "checkpoints <thread_id>",
		Short: "List Git checkpoints captured for a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.withThreadClient(c.Context(), sock, cpMachine, args[0], "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.ListCheckpoints(c.Context(), &threadv2.ListCheckpointsRequest{ThreadId: args[0]})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"checkpoints", res.Checkpoints})
				}
				for _, cp := range res.Checkpoints {
					fmt.Fprintf(c.OutOrStdout(), "#%d\t%s\t%s\tfiles=%d\n", cp.RunOrdinal, cp.CheckpointId, cp.GitTreeOid, len(cp.Files))
				}
				return nil
			})
		},
	}
	ckptsCmd.Flags().StringVar(&cpMachine, "machine", "", "Target machine")

	var rbMachine, rbCkpt string
	var rbFiles bool
	rollbackCmd := &cobra.Command{
		Use:   "rollback <thread_id>",
		Short: "Rewind thread conversation (and optionally working tree files) to a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.withThreadClient(c.Context(), sock, rbMachine, args[0], "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.RollbackCheckpoint(c.Context(), &threadv2.RollbackCheckpointRequest{
					ThreadId: args[0], CheckpointId: rbCkpt, RestoreFiles: rbFiles,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"thread", res.Thread}, field{"checkpoint", res.Checkpoint})
				}
				fmt.Fprintln(c.OutOrStdout(), "rolled back to", res.Checkpoint.CheckpointId)
				return nil
			})
		},
	}
	rollbackCmd.Flags().StringVar(&rbMachine, "machine", "", "Target machine")
	rollbackCmd.Flags().StringVar(&rbCkpt, "checkpoint", "", "Target checkpoint ID")
	rollbackCmd.Flags().BoolVar(&rbFiles, "restore-files", false, "Also restore working tree files")

	var dfMachine, dfCkpt string
	diffCmd := &cobra.Command{
		Use:   "diff <thread_id>",
		Short: "Show unified Git diff of a thread's workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.withThreadClient(c.Context(), sock, dfMachine, args[0], "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.GetThreadDiff(c.Context(), &threadv2.GetThreadDiffRequest{ThreadId: args[0], CheckpointId: dfCkpt})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"unified_diff", res.UnifiedDiff}, field{"files", res.Files})
				}
				fmt.Fprint(c.OutOrStdout(), res.UnifiedDiff)
				return nil
			})
		},
	}
	diffCmd.Flags().StringVar(&dfMachine, "machine", "", "Target machine")
	diffCmd.Flags().StringVar(&dfCkpt, "checkpoint", "", "Base checkpoint ID")

	var fkMachine, fkTitle string
	var fkOrdinal uint32
	forkCmd := &cobra.Command{
		Use:   "fork <thread_id>",
		Short: "Branch a new thread from an existing thread's history",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.withThreadClient(c.Context(), sock, fkMachine, args[0], "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.ForkThread(c.Context(), &threadv2.ForkThreadRequest{
					SourceThreadId: args[0], UpToRunOrdinal: fkOrdinal, Title: fkTitle,
				})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"thread", res.Thread})
				}
				fmt.Fprintln(c.OutOrStdout(), res.Thread.ThreadId)
				return nil
			})
		},
	}
	forkCmd.Flags().StringVar(&fkMachine, "machine", "", "Target machine")
	forkCmd.Flags().Uint32Var(&fkOrdinal, "up-to", 0, "Fork up to run ordinal")
	forkCmd.Flags().StringVar(&fkTitle, "title", "", "Forked thread title")

	var delMachine string
	delCmd := &cobra.Command{
		Use:   "delete <thread_id>",
		Short: "Delete a thread and clean up its worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.withThreadClient(c.Context(), sock, delMachine, args[0], "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				if _, err := tc.DeleteThread(c.Context(), &threadv2.DeleteThreadRequest{ThreadId: args[0]}); err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"deleted", args[0]})
				}
				fmt.Fprintln(c.OutOrStdout(), "deleted", args[0])
				return nil
			})
		},
	}
	delCmd.Flags().StringVar(&delMachine, "machine", "", "Target machine")

	outboxCmd := &cobra.Command{
		Use:   "outbox",
		Short: "List pending offline commands in the Owner Outbox",
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withThreadClient(c.Context(), sock, "", "", "", func(tc threadv2connect.ThreadServiceClient, _ string) error {
				res, err := tc.ListOutbox(c.Context(), &threadv2.ListOutboxRequest{})
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"entries", res.Entries})
				}
				for _, e := range res.Entries {
					fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\t%s\n", e.CommandId, e.MachineId, e.Method, e.Summary)
				}
				return nil
			})
		},
	}

	cmd.AddCommand(
		providersCmd,
		startCmd,
		listCmd,
		getCmd,
		makeDispatch("send", "Send or queue a follow-up message on a thread", threadv2.DispatchIntent_DISPATCH_INTENT_AUTO),
		makeDispatch("steer", "Steer an active run mid-turn", threadv2.DispatchIntent_DISPATCH_INTENT_STEER),
		interruptCmd,
		queueCmd,
		approveCmd,
		configCmd,
		ckptsCmd,
		rollbackCmd,
		diffCmd,
		forkCmd,
		delCmd,
		outboxCmd,
	)
	return cmd
}
