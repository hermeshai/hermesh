// Command hermeshd is the Hermesh daemon: it runs an execution machine (the machine role)
// and, when bundled with Hermesh.app, the owner role behind the app.
package main

import (
	"context"
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
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/owner"
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

func defaultDataDir() string {
	if v := os.Getenv("HERMESH_DATA_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Hermesh")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "hermesh")
	}
	return filepath.Join(home, ".local", "share", "hermesh")
}

// SocketPath is where the owner role serves LocalService.
func SocketPath(dataDir string) string {
	p := filepath.Join(dataDir, "run", "hermeshd.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), fmt.Sprintf("hermesh-%d", os.Getuid()), "hermeshd.sock")
	}
	return p
}

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
	var dataDir string
	var verbose bool
	root := &cobra.Command{Use: "hermeshd", Short: "Hermesh daemon", SilenceUsage: true}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", defaultDataDir(), "data directory")
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

	var ownerMode bool
	var socket string
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground (used by launchd/systemd and Hermesh.app)",
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
				o, err := owner.Open(owner.Config{DataDir: filepath.Join(dataDir, "owner"), Logger: log, MachineConfig: machineConfig()})
				if err != nil {
					return err
				}
				if socket == "" {
					socket = SocketPath(dataDir)
				}
				go o.Run(ctx)
				log.Info("serving local API", "socket", socket)
				return o.ServeLocal(ctx, socket)
			}
			n, err := openMachine()
			if err != nil {
				return err
			}
			defer n.Close()
			return n.Run(ctx)
		},
	}
	run.Flags().BoolVar(&ownerMode, "owner", false, "run the owner role for Hermesh.app (serves the local API)")
	run.Flags().StringVar(&socket, "socket", "", "local API socket path (owner role)")
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
		Short:   "Pair this machine with your Hermesh owner (shows a pairing code); the official center unless given or $HERMESH_CENTER",
		Args:    cobra.MaximumNArgs(1),
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
			if err := n.Pair(ctx, address, name, os.Stdout, machine.PairOptions{Confirm: machine.TerminalConfirm(os.Stdin, os.Stdout), TrustClaim: yes}); err != nil {
				n.Close()
				return err
			}
			n.Close()
			if noService && !restart {
				return nil
			}
			bin, _ := os.Executable()
			if err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs")); err != nil {
				fmt.Fprintln(os.Stderr, "Could not install the background service:", err)
				fmt.Fprintln(os.Stderr, "Run `hermeshd run` manually or fix the error and run `hermeshd service install`.")
				return nil
			}
			fmt.Println("Background service installed and started.")
			return nil
		},
	}
	pair.Flags().StringVar(&name, "name", "", "machine name shown to the owner (default: hostname)")
	pair.Flags().BoolVar(&noService, "no-service", false, "do not install the background service")
	pair.Flags().BoolVar(&yes, "yes", false, "with a bare center URL: accept the owner the center reports without asking (trusts the center)")

	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine's setup",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The app's hermeshd holds the machine: ask it.
			if text, err := askDiagnostics(cmd.Context(), SocketPath(dataDir)); err == nil {
				fmt.Print(text)
				return nil
			}
			n, err := openMachine()
			if err != nil {
				return err
			}
			defer n.Close()
			ok := true
			fmt.Printf("machine id   %s\n", n.MachineID())
			if n.Paired() {
				fmt.Printf("paired       yes, as %q\n", n.Name())
			} else {
				fmt.Println("paired       NO  → run: hermeshd pair <connection address>")
				ok = false
			}
			fmt.Printf("PATH         %s\n", shellenv.Path())
			infos := n.Discover(cmd.Context())
			for _, name := range []string{"claude", "codex"} {
				ok = report(name, infos[name]) && ok
			}
			for _, a := range acp.Agents { // optional: listed when installed
				if i := infos[a.Name]; i.Available {
					fmt.Printf("%-12s %s at %s, login not checked\n", a.Name, i.Version, i.Binary)
				}
			}
			fmt.Printf("service      installed=%v\n", service.Installed())
			if url := n.RelayURL(); url != "" {
				c := relayv1connect.NewRelayPublicServiceClient(http.DefaultClient, url)
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				info, err := c.GetInfo(ctx, &relayv1.GetInfoRequest{})
				cancel()
				if err != nil {
					fmt.Printf("relay        UNREACHABLE (%s): %v\n", url, err)
					ok = false
				} else {
					fmt.Printf("relay        ok (%s, %s)\n", url, info.RelayId)
				}
			}
			if !ok {
				os.Exit(1)
			}
			return nil
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show pairing and projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := openMachine()
			if err != nil {
				return err
			}
			defer n.Close()
			fmt.Printf("machine id  %s\nname        %s\npaired      %v\nrelay       %s\n", n.MachineID(), n.Name(), n.Paired(), n.RelayURL())
			projects, _ := n.Projects()
			for _, p := range projects {
				fmt.Printf("project     %s → %s (base %s)\n", p.ID, p.Path, p.DefaultBaseRef)
			}
			return nil
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

	svc := &cobra.Command{Use: "service", Short: "Manage the background service"}
	svc.AddCommand(&cobra.Command{Use: "install", RunE: func(cmd *cobra.Command, _ []string) error {
		bin, _ := os.Executable()
		return service.Install(bin, dataDir, filepath.Join(dataDir, "logs"))
	}}, &cobra.Command{Use: "uninstall", RunE: func(cmd *cobra.Command, _ []string) error { return service.Uninstall() }},
		&cobra.Command{Use: "restart", RunE: func(cmd *cobra.Command, _ []string) error { return service.Restart() }})

	helper := &cobra.Command{Use: "spawn-helper", Hidden: true, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return spawn.Serve(os.NewFile(3, "spawn-helper")) }}
	root.AddCommand(run, pair, doctor, status, audit, svc, helper,
		&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) { fmt.Println(machine.Version) }})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// askDiagnostics asks the hermeshd serving sock for its diagnostics.
func askDiagnostics(ctx context.Context, sock string) (string, error) {
	if _, err := os.Stat(sock); err != nil {
		return "", err
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
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

func report(name string, i provider.Info) bool {
	switch {
	case !i.Available:
		fmt.Printf("%-12s NOT FOUND (%s)\n", name, i.Detail)
		return false
	case !i.LoggedIn:
		fmt.Printf("%-12s %s at %s, NOT LOGGED IN → run `%s` once as this user to log in\n", name, i.Version, i.Binary, name)
		return false
	default:
		fmt.Printf("%-12s %s at %s, logged in\n", name, i.Version, i.Binary)
		return true
	}
}
