// Package herhost runs her, the kernel of Her (docs/her-server.md), for a
// machine while Her is on, and carries her's JSON-RPC for the owner's
// devices: HerService, the console's HTTP and Her's status.
package herhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

type Config struct {
	// Bin is the her program; ConsoleDir its console's files.
	Bin, ConsoleDir string
	// Socket is where her listens (paths.Her).
	Socket string
	// Env is her's environment: a new terminal's (shellenv.Environ), which
	// carries hermeshd's own HER_* variables.
	Env func() []string
	// Version is hermeshd's, for her's client_info.
	Version string
	Logger  *slog.Logger
	// ExtensionHost: while Her is on, the browsers here start this her as
	// the Hermesh extension's host, for Socket (extension.go).
	ExtensionHost bool
	// Notice is told of each newest the owner's devices tell of (snapshot's
	// changed): her reply, a question, or what waits for the owner.
	Notice func(*machinev1.HerLatest)
}

// Host keeps one her running while Her is on.
type Host struct {
	cfg Config
	log *slog.Logger
	op  sync.Mutex // Start and Stop, one at a time

	mu    sync.Mutex
	stop  context.CancelFunc // set while Her is on
	done  chan struct{}      // closed when the supervisor has stopped her
	home  string             // her's home, once her was started
	snap  *snapshot          // while her answers
	err   string             // why her is not running although Her is on
	conns map[string]*conn   // the connections Open streams and console event streams hold
}

const (
	// readyWait is how long her may take to answer once started.
	readyWait = 30 * time.Second
	// dialWait bounds connecting to her and initializing the connection.
	dialWait = 5 * time.Second
)

var errOff = connect.NewError(connect.CodeFailedPrecondition, errors.New("Her is off on this machine"))

func New(cfg Config) *Host {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Host{cfg: cfg, log: cfg.Logger, conns: map[string]*conn{}}
}

// Start runs her until Stop, starting it again whenever it stops.
func (h *Host) Start() {
	h.op.Lock()
	defer h.op.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop, h.done = cancel, make(chan struct{})
	go h.supervise(ctx, h.done)
}

// Stop stops her: SIGTERM, then its whole process group is killed. It
// returns once her is gone.
func (h *Host) Stop() {
	h.op.Lock()
	defer h.op.Unlock()
	h.halt()
}

// Off stops her for Her turned off: the browsers no longer start her for
// the extension either. (hermeshd stopping only Stops.)
func (h *Host) Off() {
	h.op.Lock()
	defer h.op.Unlock()
	h.halt()
	if h.cfg.ExtensionHost {
		h.extension(context.Background(), "uninstall")
	}
}

func (h *Host) halt() {
	h.mu.Lock()
	stop, done := h.stop, h.done
	h.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	<-done
	h.mu.Lock()
	h.stop, h.home, h.snap, h.err = nil, "", nil, ""
	h.mu.Unlock()
}

// Status is Her's status as last seen; it never asks her.
func (h *Host) Status() *machinev1.HerStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := &machinev1.HerStatus{Enabled: h.stop != nil, Error: h.err}
	if s := h.snap; s != nil {
		st.Running, st.Version, st.ModelReady, st.Unread = true, s.version, s.modelReady, s.unread()
		if s.latest != nil {
			st.Latest = proto.Clone(s.latest).(*machinev1.HerLatest)
		}
	}
	return st
}

// Busy counts her's assistants at work (a turn, sub-agents, a routine's
// run), as last seen: restarting her would stop their work.
func (h *Host) Busy() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.snap == nil {
		return 0
	}
	return h.snap.busy()
}

func (h *Host) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)
	if h.cfg.ExtensionHost {
		h.placeExtension()
		h.extension(ctx, "install", "--socket", h.cfg.Socket)
	}
	wait := time.Second
	for {
		start := time.Now()
		err := h.serve(ctx)
		h.mu.Lock()
		h.snap = nil
		if ctx.Err() == nil {
			h.err = err.Error()
		}
		h.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		h.log.Warn("her stopped; starting it again", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Minute)
	}
}

// serve runs her once: until it stops, or ctx ends and it is stopped.
func (h *Host) serve(ctx context.Context) error {
	env := h.cfg.Env()
	if runtime.GOOS == "darwin" && !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "HER_DESKTOP=") }) {
		// Computer use is on unless the owner's configuration turns it off:
		// macOS asks the owner for its permissions first (docs/her-server.md §4.4).
		env = append(env, "HER_DESKTOP=on")
	}
	home, _ := os.UserHomeDir()
	h.mu.Lock()
	h.home = herHome(env, home)
	h.mu.Unlock()
	stopStale(filepath.Join(filepath.Dir(h.cfg.Socket), "her.lock"))
	p, err := provider.StartProc(ctx, h.cfg.Bin, []string{"serve", "--listen", "unix://" + h.cfg.Socket}, home, env)
	if err != nil {
		return fmt.Errorf("her could not start: %w", err)
	}
	go p.Lines(func([]byte) {})
	defer func() {
		select {
		case <-p.Done():
		default:
			provider.KillGroup(p.Pgid())
		}
		<-p.Done()
	}()
	mon, err := h.ready(ctx, p)
	if err != nil {
		return err
	}
	defer mon.Close()
	return h.watch(ctx, mon, p)
}

// ready waits until her answers; the connection that answered is the
// monitor's.
func (h *Host) ready(ctx context.Context, p *provider.Proc) (*conn, error) {
	deadline := time.Now().Add(readyWait)
	for {
		dctx, cancel := context.WithTimeout(ctx, dialWait)
		c, err := dial(dctx, h.cfg.Socket, h.cfg.Version, true)
		cancel()
		if err == nil {
			return c, nil
		}
		select {
		case <-p.Done():
			return nil, stopped(p)
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("her did not answer within %v: %v", readyWait, err)
		}
	}
}

// watch keeps the snapshot current from the monitor's connection until her
// stops, the connection fails or ctx ends.
func (h *Host) watch(ctx context.Context, mon *conn, p *provider.Proc) error {
	res, err := mon.call(ctx, "bots/subscribe", nil)
	if err == nil && res.Error != nil {
		err = fmt.Errorf("bots/subscribe: %s", res.Error.Message)
	}
	if err != nil {
		return err
	}
	var list struct {
		Bots []bot `json:"bots"`
	}
	json.Unmarshal(res.Result, &list)
	snap := newSnapshot(mon.version, list.Bots)
	h.mu.Lock()
	h.snap, h.err = snap, ""
	h.mu.Unlock()
	h.log.Info("her running", "version", mon.version, "socket", h.cfg.Socket)
	models := make(chan struct{}, 1)
	models <- struct{}{}
	go h.followModels(ctx, mon, snap, models)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.Done():
			return stopped(p)
		case <-mon.done:
			// her exiting closes the connection first: say why it exited.
			select {
			case <-p.Done():
				return stopped(p)
			case <-time.After(time.Second):
			}
			return fmt.Errorf("lost her: %w", mon.err)
		case n := <-mon.notes:
			switch n.method {
			case "bots/changed":
				var c struct {
					Bots    []bot    `json:"bots"`
					Deleted []string `json:"deleted"`
				}
				if json.Unmarshal(n.params, &c) == nil {
					h.mu.Lock()
					news := snap.changed(c.Bots, c.Deleted, time.Now())
					h.mu.Unlock()
					if news != nil && h.cfg.Notice != nil {
						h.cfg.Notice(news)
					}
				}
			case "models/changed":
				select {
				case models <- struct{}{}:
				default:
				}
			}
		}
	}
}

// followModels lists the models again whenever kicked, one listing at a
// time and outside the loop reading the monitor's notifications (the
// answer comes on the same connection).
func (h *Host) followModels(ctx context.Context, mon *conn, snap *snapshot, kick <-chan struct{}) {
	for {
		select {
		case <-mon.done:
			return
		case <-kick:
		}
		res, err := mon.call(ctx, "models/list", nil)
		if err != nil || res.Error != nil {
			continue
		}
		ready := modelReady(res.Result)
		h.mu.Lock()
		snap.modelReady = ready
		h.mu.Unlock()
	}
}

func stopped(p *provider.Proc) error {
	if err := p.Err(); err != nil {
		return fmt.Errorf("her stopped: %w", err)
	}
	return errors.New("her stopped")
}

// herHome is the home her uses with env (her-core HerHome::from_env):
// HER_HOME, else ~/.her; dir is her's working directory.
func herHome(env []string, dir string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HER_HOME="); ok {
			if strings.TrimSpace(v) == "" {
				break
			}
			if !filepath.IsAbs(v) {
				v = filepath.Join(dir, v)
			}
			return v
		}
	}
	return filepath.Join(dir, ".her")
}
