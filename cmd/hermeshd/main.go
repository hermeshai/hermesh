// Command hermeshd is the Hermesh daemon: it runs an execution machine (the machine role)
// and, when bundled with Hermesh.app, the owner role behind the app.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/owner"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/acp"
	"github.com/hermeshai/hermesh/internal/provider/claude"
	"github.com/hermeshai/hermesh/internal/provider/codex"
	"github.com/hermeshai/hermesh/internal/provider/fake"
	"github.com/hermeshai/hermesh/internal/service"
	"github.com/hermeshai/hermesh/internal/shellenv"
	"github.com/hermeshai/hermesh/internal/spawn"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"github.com/hermeshai/hermesh/pkg/rlimit"
)

var logOut io.Writer = os.Stderr

// spawnHelpers is how many spawn helper processes run; each serves one
// spawn at a time, and four queued at 5000 sessions (bench/README.md).
var spawnHelpers = max(2, runtime.NumCPU()/2)

func logger(verbose bool) *slog.Logger {
	lvl := slog.LevelInfo
	if verbose {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: lvl}))
}

// openLogFile sends logs to <dataDir>/logs/hermeshd.log as well (launchd
// and SMAppService give the daemon no terminal).
func openLogFile(dataDir string) {
	dir := filepath.Join(dataDir, "logs")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "hermeshd.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 20<<20 {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		logOut = io.MultiWriter(os.Stderr, f)
	}
}

func main() {
	if err := rootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCommand() *cobra.Command {
	var dataDir string
	var verbose bool
	root := &cobra.Command{Use: "hermeshd", Short: "Hermesh on a machine: runs coding agent sessions for your agents", SilenceUsage: true,
		Long: `hermeshd runs on each machine where coding agents (Claude Code, Codex) work
for your agents. It runs as the user you code as, in the background.

First time, the install script runs "hermeshd pair" for you. Afterwards:
  hermeshd doctor                   check everything, and what to fix
  hermeshd project add ~/code/app   let agents work in a folder
  hermeshd service status           is it running, and does it start at boot

A computer that runs no coding agents can approve instead: hermeshd device.`}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", paths.DataDir(), "data directory")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "debug logging")

	var debugAddr string
	var maxSessions int
	var idleProcessTTL time.Duration
	var idleTimeout, toolTimeout, turnTimeout time.Duration
	var runFlagSet func(name string) bool // whether a `run` flag was given
	var workspaceMode string
	machineConfig := func() machine.Config {
		lim := machine.DefaultLimits()
		if maxSessions > 0 {
			lim.MaxConcurrent = maxSessions
		}
		if idleProcessTTL > 0 {
			lim.IdleProcessTTL = idleProcessTTL
		}
		for _, o := range []struct {
			flag string
			v    time.Duration
			dst  *time.Duration
		}{{"idle-timeout", idleTimeout, &lim.IdleTimeout}, {"tool-timeout", toolTimeout, &lim.ToolTimeout}, {"turn-timeout", turnTimeout, &lim.TurnTimeout}} {
			if runFlagSet != nil && runFlagSet(o.flag) {
				*o.dst = o.v
			}
		}
		return machine.Config{DataDir: filepath.Join(dataDir, "machine"), Logger: logger(verbose), Providers: providers(), Limits: lim, WorkspaceMode: workspaceMode}
	}
	openMachine := func() (*machine.Machine, error) { return machine.Open(machineConfig()) }

	var ownerMode, noMachine bool
	var socket string
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground (used by launchd/systemd and the Hermesh app)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			openLogFile(dataDir)
			log := logger(verbose)
			log.Info("open file limit", "soft", rlimit.RaiseNoFile())
			if exe, err := os.Executable(); err == nil && spawn.Supported {
				// Spawning from small helpers keeps agent start cost flat
				// as this process accumulates descriptors (see spawn.Helper).
				var hs []*spawn.Helper
				for i := 0; i < spawnHelpers; i++ {
					h, err := spawn.StartHelper(exe, "spawn-helper")
					if err != nil {
						log.Warn("spawn helper unavailable", "err", err)
						break
					}
					hs = append(hs, h)
					defer h.Close()
				}
				provider.UseSpawnHelpers(hs...)
			}
			if debugAddr != "" {
				go func() { log.Warn("debug server stopped", "err", metrics.Serve(debugAddr)) }()
			}
			if ownerMode {
				o, err := owner.Open(owner.Config{DataDir: filepath.Join(dataDir, "owner"), Logger: log, MachineConfig: machineConfig(), NoLocalMachine: noMachine})
				if err != nil {
					return err
				}
				if socket == "" {
					socket = paths.LocalAPI(dataDir)
				}
				go o.Run(ctx)
				log.Info("serving local API", "socket", socket)
				return o.ServeLocal(ctx, socket)
			}
			cfg := machineConfig()
			cfg.SelfErase = true
			n, err := machine.Open(cfg)
			if err != nil {
				return err
			}
			err = n.Run(ctx)
			n.Close()
			if errors.Is(err, machine.ErrErased) {
				return eraseMachine(dataDir, cfg)
			}
			return err
		},
	}
	run.Flags().BoolVar(&ownerMode, "owner", false, "run the owner role for the Hermesh app or a Linux trusted device (serves the local API)")
	run.Flags().StringVar(&socket, "socket", "", "local API socket path (owner role)")
	run.Flags().BoolVar(&noMachine, "no-machine", false, "with --owner: never run a machine here (a Linux trusted device)")
	run.Flags().StringVar(&debugAddr, "debug-addr", os.Getenv("HERMESH_DEBUG_ADDR"), "serve /metrics and /debug/pprof on this loopback address")
	run.Flags().IntVar(&maxSessions, "max-sessions", 0, "maximum concurrently running sessions; more wait queued (default: no limit)")
	run.Flags().StringVar(&workspaceMode, "workspace", "git", "git (worktree per session) or none (benchmarking only)")
	run.Flags().MarkHidden("workspace")
	run.Flags().DurationVar(&idleProcessTTL, "idle-process-ttl", 0, "stop an idle session's agent process after this long (default 10m)")
	run.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "stop a turn whose agent writes nothing for this long while no tool runs (default 15m; 0: never)")
	run.Flags().DurationVar(&toolTimeout, "tool-timeout", 0, "stop a turn whose agent writes nothing for this long while a tool runs (default 2h; 0: never)")
	run.Flags().DurationVar(&turnTimeout, "turn-timeout", 0, "stop a turn that runs longer than this even while it makes progress (default: no limit)")
	runFlagSet = run.Flags().Changed

	var name string
	var noService, yes bool
	pair := &cobra.Command{
		Use:     "pair [center URL | connection address]",
		Aliases: []string{"login"},
		Short:   "Pair this machine with your Hermesh: shows a code to scan with Hermesh on your iPhone or Mac",
		Long: `Pair this machine with your Hermesh. With no argument it uses the official
service (or $HERMESH_CENTER): scan the code it shows with Hermesh on your
iPhone, or enter it in the app, then type here the two digits your device
shows. With a connection address (relay.example.com/o_…) the request goes
to that owner's devices by itself. Then it installs the background service.`,
		Example: `  hermeshd pair
  hermeshd pair connect.hermesh.io/o_abc… --name build-box`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			address := connaddr.Center(arg)
			n, err := openMachine()
			restart := false
			if err != nil && service.Installed() {
				// Pairing again (e.g. after the owner identity was reset):
				// the running service holds the data directory.
				fmt.Println("Stopping the background service to pair this machine…")
				if uerr := service.Uninstall(); uerr == nil {
					restart = true
					n, err = openMachine()
				}
			}
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := n.Pair(ctx, address, name, os.Stdout, machine.PairOptions{Digits: machine.TerminalDigits(os.Stdout), TrustClaim: yes, QR: machine.IsTerminal(os.Stdout)}); err != nil {
				n.Close()
				return err
			}
			infos := n.Discover(ctx)
			n.Close()
			if !noService || restart {
				bin, _ := os.Executable()
				switch err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs")); {
				case errors.Is(err, service.ErrNoSystemd):
					fmt.Println("No background service:", err)
				case err != nil:
					fmt.Fprintln(os.Stderr, "Could not install the background service:", err)
					fmt.Fprintln(os.Stderr, "Run `hermeshd run` manually or fix the error and run `hermeshd service install`.")
				case runtime.GOOS == "linux":
					fmt.Println("✓ Background service on: starts with the server, keeps running after you log out")
				default:
					fmt.Println("✓ Background service on")
				}
			}
			fmt.Println("\nCoding agents")
			ready := false
			for _, name := range []string{"claude", "codex"} {
				ready = report(name, infos[name]) || ready
			}
			for _, a := range acp.Agents {
				if i := infos[a.Name]; i.Available {
					fmt.Printf("  %-10s %s, sign-in not checked\n", a.Name, i.Version)
				}
			}
			if !ready {
				fmt.Println("  Install one as this user and sign in once (run `claude`); `hermeshd doctor` checks again.")
			}
			fmt.Printf("\nNext\n  add a project   hermeshd project add ~/code/app   (or in the Hermesh app)\n  check it all    hermeshd doctor\n  its log         tail -f %s\n  every command   hermeshd --help\n",
				filepath.Join(dataDir, "logs", "hermeshd.log"))
			return nil
		},
	}
	pair.Flags().StringVar(&name, "name", "", "machine name shown to the owner (default: hostname)")
	pair.Flags().BoolVar(&noService, "no-service", false, "do not install the background service")
	pair.Flags().BoolVar(&yes, "yes", false, "with a bare center URL: accept the owner the center reports without asking (trusts the center)")

	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine's setup, and say what to fix",
		Long:  "Check the pairing, the coding agents, the background service and the connection to the relay. It exits 1 when something needs fixing, and says how.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The app's hermeshd holds the machine: ask it.
			if text, err := askDiagnostics(cmd.Context(), paths.LocalAPI(dataDir)); err == nil {
				fmt.Print(text)
				return nil
			}
			c, running, err := machineCheck(cmd.Context(), dataDir, openMachine)
			if err != nil {
				return err
			}
			ok := true
			fmt.Printf("machine id   %s\n", c.MachineID)
			if c.Paired {
				fmt.Printf("paired       yes, as %q\n", c.Name)
			} else {
				fmt.Println("paired       NO  → run: hermeshd pair")
				ok = false
			}
			fmt.Printf("PATH         %s\n", shellenv.Path())
			for _, name := range []string{"claude", "codex"} {
				ok = report(name, c.Agents[name]) && ok
			}
			for _, a := range acp.Agents { // optional: listed when installed
				if i := c.Agents[a.Name]; i.Available {
					fmt.Printf("  %-10s %s at %s, login not checked\n", a.Name, i.Version, i.Binary)
				}
			}
			ok = serviceLine(running) && ok
			switch {
			case running && c.Connected:
				fmt.Printf("relay        connected (%s)\n", c.RelayURL)
			case c.RelayURL != "":
				url := c.RelayURL
				rc := relayv1connect.NewRelayPublicServiceClient(http.DefaultClient, url)
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				info, err := rc.GetInfo(ctx, &relayv1.GetInfoRequest{})
				cancel()
				switch {
				case err != nil:
					fmt.Printf("relay        UNREACHABLE (%s): %v\n", url, err)
					ok = false
				case running:
					fmt.Printf("relay        reachable (%s), not connected yet → its log: tail %s\n", url, filepath.Join(dataDir, "logs", "hermeshd.log"))
				default:
					fmt.Printf("relay        ok (%s, %s)\n", url, info.RelayId)
				}
			}
			if !ok {
				os.Exit(1)
			}
			return nil
		},
	}

	projects := projectCommand(&dataDir, openMachine)
	status := &cobra.Command{
		Use:   "status",
		Short: "Show this machine: its name, whether it is connected, the service and its projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, running, err := machineCheck(cmd.Context(), dataDir, openMachine)
			if err != nil {
				return err
			}
			paired := "not paired → run: hermeshd pair"
			if c.Paired {
				paired = fmt.Sprintf("paired as %q, relay %s", c.Name, c.RelayURL)
				if running && c.Connected {
					paired += ", connected"
				}
			}
			fmt.Printf("machine   %s · %s · Hermesh %s\n", c.MachineID, paired, c.Version)
			serviceLine(running)
			fmt.Println()
			list, _, _ := projects.Find([]string{"list"})
			return list.RunE(cmd, nil)
		},
	}

	audit := &cobra.Command{
		Use:   "audit-verify",
		Short: "Verify the tamper-evident audit log",
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := openMachine()
			if err != nil {
				return err
			}
			defer n.Close()
			count, err := n.VerifyAudit()
			if err != nil {
				return fmt.Errorf("audit log INVALID after %d records: %w", count, err)
			}
			fmt.Printf("audit log ok: %d records\n", count)
			return nil
		},
	}

	svc := &cobra.Command{Use: "service", Short: "The background service: status, install, restart, uninstall",
		Long: `hermeshd runs in the background as a service of this user: a systemd user
service on Linux (with lingering, so it starts at boot with nobody logged
in), a LaunchAgent on macOS. "hermeshd pair" installs it.`}
	svc.AddCommand(&cobra.Command{Use: "status", Short: "Whether the service runs, starts at boot, and where its log is", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !serviceLine(false) {
				os.Exit(1)
			}
			return nil
		}},
		&cobra.Command{Use: "install", Short: "Install and start the service (again)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			bin, _ := os.Executable()
			if err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs")); err != nil {
				return err
			}
			serviceLine(false)
			return nil
		}},
		&cobra.Command{Use: "restart", Short: "Restart the service (after an update)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.Restart(); err != nil {
				return err
			}
			fmt.Println("✓ Restarted")
			return nil
		}},
		&cobra.Command{Use: "uninstall", Short: "Stop the service and stop starting it; pairing and projects stay", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.Uninstall(); err != nil {
				return err
			}
			fmt.Println("✓ The service is stopped and won't start again; `hermeshd service install` brings it back")
			return nil
		}})
	logFile := filepath.Join(dataDir, "logs", "hermeshd.log")
	startsWhen := "at boot"
	if runtime.GOOS == "darwin" {
		startsWhen = "at login"
	}
	serviceLine = func(byHand bool) bool {
		switch {
		case service.Running() && service.AtBoot():
			fmt.Printf("service      running, starts %s · log: tail -f %s\n", startsWhen, logFile)
			return true
		case service.Running():
			fmt.Printf("service      running, but NOT %s → run: hermeshd service install · log: tail -f %s\n", startsWhen, logFile)
			return false
		case service.Installed():
			fmt.Printf("service      installed, NOT RUNNING → run: hermeshd service restart · log: tail %s\n", logFile)
			return false
		case byHand:
			fmt.Printf("service      not installed; hermeshd runs by hand → to start it %s: hermeshd service install\n", startsWhen)
			return false
		default:
			fmt.Println("service      not installed → run: hermeshd service install")
			return false
		}
	}

	helper := &cobra.Command{Use: "spawn-helper", Hidden: true, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return spawn.Serve(os.NewFile(3, "spawn-helper")) }}
	// The tools a session's coding agent starts (docs/agent-messages.md §7.4).
	messageMCP := &cobra.Command{Use: "message-mcp", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return machine.ServeMessageMCP(cmd.Context()) }}
	root.AddCommand(deviceCommands(&dataDir)...)
	root.AddCommand(eraseCommand(&dataDir))
	root.AddCommand(run, pair, doctor, status, audit, svc, helper, messageMCP, projects,
		&cobra.Command{Use: "version", Short: "Print the version", Args: cobra.NoArgs, Run: func(*cobra.Command, []string) { fmt.Println(machine.Version) }})
	return root
}

// serviceLine prints the background service's state (byHand: a hermeshd
// runs here outside it), and whether it is fine; set in rootCommand, where
// the log's place is known.
var serviceLine func(byHand bool) bool

// machineCheck asks the running machine how it is (it holds the data
// directory), or opens it when it isn't running.
func machineCheck(ctx context.Context, dataDir string, open func() (*machine.Machine, error)) (machine.Check, bool, error) {
	var c machine.Check
	if out, ok, err := machine.ControlRequest(ctx, filepath.Join(dataDir, "machine"), "GET", "/check", nil); ok {
		if err == nil {
			err = json.Unmarshal(out, &c)
		}
		return c, true, err
	}
	n, err := open()
	if err != nil {
		return c, false, err
	}
	defer n.Close()
	return n.Check(ctx), false, nil
}

// askDiagnostics asks the hermeshd serving sock for its diagnostics.
func askDiagnostics(ctx context.Context, sock string) (string, error) {
	if _, err := os.Stat(sock); err != nil {
		return "", err
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := localv1connect.NewLocalServiceClient(hc, "http://local").GetDiagnostics(ctx, &localv1.GetDiagnosticsRequest{})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// providers returns the coding agents; HERMESH_FAKE_PROVIDERS=1 replaces
// them with a scripted fake (tests only; it can only be set locally).
func providers() map[string]provider.Provider {
	if os.Getenv("HERMESH_FAKE_PROVIDERS") == "1" {
		return map[string]provider.Provider{"claude": fake.Provider{}, "codex": fake.Provider{}}
	}
	ps := machine.DefaultProviders()
	if b := os.Getenv("HERMESH_CODEX_BIN"); b != "" {
		ps["codex"] = &codex.Provider{PathEnv: shellenv.Path(), Binary: b}
	}
	if b := os.Getenv("HERMESH_CLAUDE_BIN"); b != "" {
		ps["claude"] = &claude.Provider{PathEnv: shellenv.Path(), Binary: b}
	}
	for _, a := range acp.Agents {
		if b := os.Getenv("HERMESH_" + strings.ToUpper(a.Name) + "_BIN"); b != "" {
			ps[a.Name] = &acp.Provider{Agent: a, PathEnv: shellenv.Path(), Binary: b}
		}
	}
	return ps
}

// installPages are where the coding agents Hermesh checks are installed from.
var installPages = map[string]string{
	"claude": "https://github.com/anthropics/claude-code",
	"codex":  "https://github.com/openai/codex",
}

func report(name string, i provider.Info) bool {
	switch {
	case !i.Available:
		fmt.Printf("  %-10s not installed: %s\n", name, installPages[name])
		return false
	case !i.LoggedIn:
		fmt.Printf("  %-10s %s, NOT SIGNED IN → run `%s` once as this user (on a server it prints a link to open on any device)\n", name, i.Version, name)
		return false
	default:
		fmt.Printf("  %-10s %s, signed in\n", name, i.Version)
		return true
	}
}

// eraseMachine erases a machine whose owner erased everything
// (docs/erase-everything.md), and takes its service away, so that it waits
// to be paired as a newly installed one does. The rest of dataDir stays: on
// a Mac it is also the app's (its owners' profiles).
func eraseMachine(dataDir string, cfg machine.Config) error {
	kept, err := eraseHere(dataDir, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "The owner erased everything: Hermesh's data on this machine is erased. Pair it again with `hermeshd pair <connection address>`.")
	for _, wt := range kept {
		fmt.Fprintf(os.Stderr, "Kept, with uncommitted changes: %s\n", wt)
	}
	if service.InstalledFor(dataDir) {
		return service.Uninstall()
	}
	return nil
}
