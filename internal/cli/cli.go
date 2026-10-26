package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// Exit codes are part of the agent-facing contract.
const (
	exitOK          = 0
	exitUsage       = 2
	exitDenied      = 3
	exitUnavailable = 4
	exitFailed      = 5
	exitTimeout     = 6
	// The turn failed for a reason that may pass (rate limit, network,
	// provider error, crash, stall); sending the message again may succeed.
	exitRetryable = 7
	// The agent asked a question and waits for an answer
	// (`hermesh session answer`, or a message with `session send`).
	exitNeedsInput = 8
	exitGeneric    = 1
)

type exitError struct {
	code int
	// kind is the error's kind in the output contract ("" derives it).
	kind      string
	err       error
	hint      string
	retryable bool
	// reported: the command already printed the error in its JSON output.
	reported bool
}

func (e *exitError) Error() string {
	if e.hint == "" {
		return e.err.Error()
	}
	return e.err.Error() + " (" + e.hint + ")"
}

func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, a ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, a...)}
}

func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if errors.Is(err, errNotLoggedIn) || errors.Is(err, errRevoked) || errors.Is(err, transport.ErrAdmission) {
		return exitDenied
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exitTimeout
	}
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return exitDenied
	case connect.CodeUnavailable:
		return exitUnavailable
	case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeFailedPrecondition:
		return exitUsage
	case connect.CodeDeadlineExceeded:
		return exitTimeout
	case connect.CodeResourceExhausted:
		return exitRetryable
	}
	return exitGeneric
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	a := &app{hc: http.DefaultClient}
	defaultOutput := "text"
	if os.Getenv("HERMESH_OUTPUT") == "json" {
		defaultOutput = "json"
	}
	root := a.root(defaultOutput)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.execute(ctx, root, stdout, stderr)
}

// root is the command tree, run by a.
func (a *app) root(defaultOutput string) *cobra.Command {
	root := &cobra.Command{Use: "hermesh", Short: "Drive Claude Code, Codex and other coding agents on your owner's machines through Hermesh",
		SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVarP(&a.output, "output", "o", defaultOutput, "output format: text (to read) or json (one document, see docs/cli-output.md); HERMESH_OUTPUT=json makes json the default")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &exitError{code: exitUsage, err: err} })
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		if a.output != "text" && a.output != "json" {
			return &exitError{code: exitUsage, kind: "usage", err: fmt.Errorf("unknown output format %q (text or json)", a.output)}
		}
		if a.mem != nil {
			return nil // credentials held by the caller, no files
		}
		a.home = os.Getenv("HERMESH_HOME")
		if a.home == "" {
			if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
				a.home = x + "/hermesh"
			} else {
				h, _ := os.UserHomeDir()
				a.home = h + "/.config/hermesh"
			}
		}
		return nil
	}
	// `hermesh login` was the command before `auth login`; installs and
	// copied instructions may still use it.
	login := a.loginCmd()
	login.Hidden = true
	root.AddCommand(a.authCmd(), login, a.projectCmd(), a.sessionCmd(), a.messageCmd(), &cobra.Command{Use: "version", Short: "Show this CLI's version", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if a.output == "json" {
				return a.report(c.OutOrStdout(), nil, field{"version", machine.Version})
			}
			fmt.Fprintln(c.OutOrStdout(), machine.Version)
			return nil
		}})
	return root
}

// execute runs root and returns the exit code, printing a failure the
// command did not print itself.
func (a *app) execute(ctx context.Context, root *cobra.Command, stdout, stderr io.Writer) int {
	if err := root.ExecuteContext(ctx); err != nil {
		if strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "accepts") || strings.Contains(err.Error(), "required flag") {
			err = &exitError{code: exitUsage, kind: "usage", err: err}
		}
		var ee *exitError
		switch {
		case errors.As(err, &ee) && ee.reported:
		case a.output == "json":
			writeDoc(stdout, err)
		default:
			fmt.Fprintln(stderr, "error:", err)
		}
		return exitCode(err)
	}
	if a.output == "text" && a.inboxWaiting > 0 {
		fmt.Fprintf(stderr, "%d message(s) wait in your inbox: hermesh message list\n", a.inboxWaiting)
	}
	return exitOK
}

// group makes cmd a group of subcommands: alone it prints its help, and
// an unknown subcommand is a usage error (cobra would print the help and
// exit 0, which reads as success).
func group(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	return cmd
}

// ---------------------------------------------------------------------------

// workspaceName is the CLI spelling of a project's workspace mode.
func workspaceName(w identityv1.Workspace) string {
	if w == identityv1.Workspace_WORKSPACE_IN_PLACE {
		return "in_place"
	}
	return "worktree"
}

type machineRef struct {
	ID, Name string
	Online   bool
	Entry    *relayv1.MachineEntry
}

func listMachines(ctx context.Context, c *peer.Client) ([]*relayv1.MachineEntry, error) {
	hc, err := c.Control()
	if err != nil {
		return nil, err
	}
	res, err := relayv1connect.NewClientControlServiceClient(hc, "http://relay", transport.ClientNoCompression).ListMachines(ctx, &relayv1.ListMachinesRequest{})
	if err != nil {
		return nil, err
	}
	return res.Machines, nil
}

// machineRefs keeps the listed machines whose certs the owner root signed.
// Their names are the relay's word: checkName holds each machine to the
// name it presents on the end-to-end connection.
func (a *app) machineRefs(entries []*relayv1.MachineEntry) []machineRef {
	var out []machineRef
	for _, e := range entries {
		nc, _, err := a.trust.VerifyMachineCert(e.MachineCert)
		if err != nil {
			continue
		}
		name := e.Name
		if name == "" {
			name = nc.MachineId
		}
		out = append(out, machineRef{ID: nc.MachineId, Name: name, Online: e.Online, Entry: e})
	}
	slices.SortFunc(out, func(x, y machineRef) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// checkName refuses a machine that calls itself something else, over the
// end-to-end connection, than the name the relay listed it under: a relay
// relabelling the owner's machines would send work to the wrong one.
func checkName(ref machineRef, n *peer.Machine) error {
	if ref.Name == ref.ID || n.Name == ref.Name {
		return nil
	}
	return &exitError{code: exitUnavailable, kind: "mismatch", retryable: true,
		err:  fmt.Errorf("machine %s calls itself %q, but the list said %q: not using it", ref.ID, n.Name, ref.Name),
		hint: "it was probably just renamed: run `hermesh project list` again; if this keeps happening, the relay may be tampering"}
}

// findMachine returns the machine with that name or id.
func findMachine(machines []machineRef, nameOrID string) (machineRef, bool) {
	for _, m := range machines {
		if m.ID == nameOrID || m.Name == nameOrID {
			return m, true
		}
	}
	return machineRef{}, false
}

// resolveMachine picks the machine named by flag, or else the one the
// session id belongs to (its id starts with the machine id's prefix).
func resolveMachine(machines []machineRef, flag, sessionID string) (machineRef, error) {
	if flag != "" {
		if m, ok := findMachine(machines, flag); ok {
			return m, nil
		}
		return machineRef{}, &exitError{code: exitUsage, kind: "not_found", err: fmt.Errorf("no machine %q", flag), hint: "see `hermesh project list`"}
	}
	if parts := strings.SplitN(sessionID, "_", 3); len(parts) == 3 {
		for _, m := range machines {
			if strings.HasPrefix(strings.TrimPrefix(m.ID, "m_"), parts[1]) {
				return m, nil
			}
		}
	}
	return machineRef{}, &exitError{code: exitUsage, kind: "not_found", err: fmt.Errorf("no session %q on the machines this client can reach", sessionID),
		hint: "see `hermesh session list`"}
}

// targetMachines returns the machine named by flag, or all of them.
func targetMachines(machines []machineRef, flag string) ([]machineRef, error) {
	if flag != "" {
		n, err := resolveMachine(machines, flag, "")
		if err != nil {
			return nil, err
		}
		return []machineRef{n}, nil
	}
	return machines, nil
}

// warmTargets warms what targetMachines will pick.
func warmTargets(flag string) func([]machineRef) []machineRef {
	return func(seen []machineRef) []machineRef {
		ms, _ := targetMachines(seen, flag)
		return ms
	}
}

func ids(machines []machineRef) []string {
	var out []string
	for _, n := range machines {
		if n.Online {
			out = append(out, n.ID)
		}
	}
	return out
}

func newIdempotencyKey() string { return strings.ToLower(ulid.Make().String()) }

func timestampOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }
