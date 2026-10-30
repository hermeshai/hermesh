//go:build unix

// Command bench load-tests the full Hermesh path: agent client → relay →
// E2E tunnel → hermeshd → Codex adapter → coding-agent process (fakecodex).
// It runs relay and hermeshd as separate processes, samples their CPU and
// memory (and the agent processes'), scrapes their /metrics, captures pprof
// profiles and writes a report.
//
//	go run ./bench -sessions 1000 -concurrency 200 -hold 30s
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"github.com/hermeshai/hermesh/internal/owner"
	"github.com/hermeshai/hermesh/internal/peer"
)

var (
	flagSessions    = flag.Int("sessions", 200, "sessions to start")
	flagConcurrency = flag.Int("concurrency", 50, "sessions being started/awaited at once")
	flagAgents      = flag.Int("agents", 0, "agent identities (0 = auto)")
	flagTurns       = flag.Int("turns", 1, "turns per session")
	flagHold        = flag.Duration("hold", 0, "keep all sessions (and their agent processes) alive this long after the load, then archive them")
	flagTurnMS      = flag.Int("turn-ms", 200, "fake agent turn duration")
	flagDeltas      = flag.Int("deltas", 20, "fake agent streamed deltas per turn")
	flagCommands    = flag.Int("commands", 2, "fake agent commands per turn")
	flagOutBytes    = flag.Int("output-bytes", 512, "fake agent output per command")
	flagWrites      = flag.Int("writes", 1, "fake agent files written per turn")
	flagBallast     = flag.Int("ballast-mb", 0, "fake agent resident memory")
	flagProfile     = flag.Duration("profile", 10*time.Second, "CPU profile duration captured mid-load (0 = off)")
	flagMessages    = flag.Int("messages", 0, "agent messages per turn, between the commands")
	flagWatchers    = flag.Int("watchers", 0, "extra event-stream subscribers per session, following each turn (like the app watching progress)")
	flagTrace       = flag.Duration("trace", 0, "execution trace of hermeshd captured mid-load (0 = off; see go tool trace)")
	flagOut         = flag.String("out", "", "report directory (default bench-results/<time>)")
	flagLabel       = flag.String("label", "", "label for this run")
	flagNoWorktree  = flag.Bool("no-worktree", false, "run sessions without git worktrees (measures the daemon itself)")
)

func main() {
	flag.Parse()
	if *flagAgents == 0 {
		*flagAgents = max(2, *flagConcurrency/200+1)
	}
	if *flagOut == "" {
		*flagOut = filepath.Join("bench-results", time.Now().Format("20060102-150405")+suffix(*flagLabel))
	}
	os.MkdirAll(*flagOut, 0o755)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return "-" + s
}

type env struct {
	tmp       string
	bin       string
	relay     *exec.Cmd
	daemon    *exec.Cmd
	relayURL  string
	relayDbg  string
	daemonDbg string
	addr      string
	ownerID   string
	app       localv1connect.LocalServiceClient
	cancelApp context.CancelFunc
	machineID string
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func build(dst string) error {
	for _, c := range []struct{ out, pkg string }{{"hermesh-relay", "./cmd/hermesh-relay"}, {"hermeshd", "./cmd/hermeshd"}, {"fakecodex", "./bench/fakecodex"}} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(dst, c.out), c.pkg).CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %v %s", c.out, err, out)
		}
	}
	return nil
}

func startProc(name string, envv []string, logPath string, bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), envv...)
	f, _ := os.Create(logPath)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(cmd.Start())
	return cmd
}

func waitHTTP(url string) {
	for i := 0; i < 200; i++ {
		if r, err := http.Get(url); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	panic("not reachable: " + url)
}

func setup(ctx context.Context) (*env, error) {
	e := &env{}
	e.tmp, _ = os.MkdirTemp("/tmp", "hmbench")
	e.bin = filepath.Join(e.tmp, "bin")
	if err := build(e.bin); err != nil {
		return nil, err
	}
	relayPort, relayDbg, daemonDbg := freePort(), freePort(), freePort()
	e.relayURL = fmt.Sprintf("http://127.0.0.1:%d", relayPort)
	e.relayDbg = fmt.Sprintf("http://127.0.0.1:%d", relayDbg)
	e.daemonDbg = fmt.Sprintf("http://127.0.0.1:%d", daemonDbg)
	fmt.Printf("debug endpoints: hermeshd %s, relay %s\n", e.daemonDbg, e.relayDbg)
	relayData := filepath.Join(e.tmp, "relay")
	e.relay = startProc("relay", nil, filepath.Join(*flagOut, "relay.log"), filepath.Join(e.bin, "hermesh-relay"), "serve", "--data-dir", relayData,
		"--listen", fmt.Sprintf("127.0.0.1:%d", relayPort), "--debug-addr", fmt.Sprintf("127.0.0.1:%d", relayDbg))
	waitHTTP(e.relayURL + "/healthz")
	out, err := exec.Command(filepath.Join(e.bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).Output()
	if err != nil {
		return nil, err
	}
	claim := strings.TrimSpace(string(out))

	// Owner in-process, driven through its Unix socket like the app.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	o, err := owner.Open(owner.Config{DataDir: filepath.Join(e.tmp, "owner"), Logger: quiet})
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(e.tmp, "o.sock")
	go o.Run(ctx)
	go o.ServeLocal(ctx, sock)
	root, _ := identity.GenerateKey()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	e.app = localv1connect.NewLocalServiceClient(hc, "http://local")
	for i := 0; i < 100; i++ {
		if _, err := e.app.GetStatus(ctx, &localv1.GetStatusRequest{}); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	actx, cancel := context.WithCancel(ctx)
	e.cancelApp = cancel
	go signLoop(actx, e.app, root)
	setupRes, err := e.app.Setup(ctx, &localv1.SetupRequest{RelayUrl: e.relayURL, ClaimCode: claim, RootPublicKey: identity.MarshalPublicKey(root.Public()), RootLabel: "bench"})
	if err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	e.addr, e.ownerID = setupRes.ConnectAddress, setupRes.OwnerId

	// Execution machine: pair, then run with the fake Codex.
	machineData := filepath.Join(e.tmp, "machine")
	pair := exec.Command(filepath.Join(e.bin, "hermeshd"), "pair", e.addr, "--name", "bench", "--no-service", "--data-dir", machineData)
	stdout, _ := pair.StdoutPipe()
	pair.Stderr = os.Stderr
	must(pair.Start())
	code := readCode(stdout)
	res, err := e.app.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "bench"})
	if err != nil {
		return nil, err
	}
	e.machineID = res.MachineId
	io.Copy(io.Discard, stdout)
	if err := pair.Wait(); err != nil {
		return nil, fmt.Errorf("pair: %w", err)
	}
	fake := []string{
		"HERMESH_CODEX_BIN=" + filepath.Join(e.bin, "fakecodex"),
		fmt.Sprintf("FAKE_CODEX_TURN_MS=%d", *flagTurnMS), fmt.Sprintf("FAKE_CODEX_DELTAS=%d", *flagDeltas),
		fmt.Sprintf("FAKE_CODEX_COMMANDS=%d", *flagCommands), fmt.Sprintf("FAKE_CODEX_OUTPUT_BYTES=%d", *flagOutBytes),
		fmt.Sprintf("FAKE_CODEX_WRITE=%d", *flagWrites), fmt.Sprintf("FAKE_CODEX_BALLAST_MB=%d", *flagBallast),
		fmt.Sprintf("FAKE_CODEX_MESSAGES=%d", *flagMessages),
	}
	args := []string{"run", "--data-dir", machineData, "--debug-addr", fmt.Sprintf("127.0.0.1:%d", daemonDbg),
		"--max-sessions", strconv.Itoa(*flagSessions + 10), "--idle-process-ttl", "24h"}
	if *flagNoWorktree {
		args = append(args, "--workspace", "none")
	}
	e.daemon = startProc("hermeshd", fake, filepath.Join(*flagOut, "hermeshd.log"), filepath.Join(e.bin, "hermeshd"), args...)
	waitHTTP(e.daemonDbg + "/metrics")
	for i := 0; i < 200; i++ {
		machines, err := e.app.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err == nil && len(machines.Machines) == 1 && machines.Machines[0].Online && machines.Machines[0].Status != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	repo := filepath.Join(e.tmp, "repo")
	os.MkdirAll(repo, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=b", "GIT_AUTHOR_EMAIL=b@b", "GIT_COMMITTER_NAME=b", "GIT_COMMITTER_EMAIL=b@b")
		if out, err := c.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git: %v %s", err, out)
		}
	}
	if _, err := e.app.AddProject(ctx, &localv1.AddProjectRequest{MachineId: e.machineID, ProjectId: "bench", Path: repo}); err != nil {
		return nil, err
	}
	return e, nil
}

var codeRE = regexp.MustCompile(`Pairing code: ([0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4})`)

func readCode(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if m := codeRE.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	panic("no pairing code")
}

func signLoop(ctx context.Context, app localv1connect.LocalServiceClient, root *identity.SoftwareKey) {
	for ctx.Err() == nil {
		stream, err := app.WatchSigningRequests(ctx, &localv1.WatchSigningRequestsRequest{})
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for stream.Receive() {
			req := stream.Msg()
			sig, _ := root.SignMessage(req.Message)
			app.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Signature: sig})
		}
	}
}

// agent is one paired identity with one relay connection and one tunnel.
type agent struct {
	client  *peer.Client
	machine *peer.Machine
}

func pairAgents(ctx context.Context, e *env, n int) ([]*agent, error) {
	relayURL, ownerID, _ := connaddr.Parse(e.addr)
	var out []*agent
	for i := 0; i < n; i++ {
		key, _ := identity.GenerateKey()
		pr := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_CLIENT, SubjectPublicKey: identity.MarshalPublicKey(key.Public()),
			Label: fmt.Sprintf("bench-%d", i), OwnerId: ownerID, CreatedAt: timestamppb.Now()}
		pr2, pw := io.Pipe()
		done := make(chan *identityv1.Credentials, 1)
		go func() {
			c, _, err := machine.PairWithRelay(ctx, http.DefaultClient, relayURL, pr, key, pw, machine.PairOptions{})
			if err != nil {
				fmt.Fprintln(os.Stderr, "pair agent:", err)
			}
			pw.Close()
			done <- c
		}()
		code := readCode(pr2)
		go io.Copy(io.Discard, pr2)
		if _, err := e.app.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: code, Spec: &localv1.GrantSpec{Agents: []string{"codex"}}}); err != nil {
			return nil, err
		}
		creds := <-done
		trust := identity.NewTrust(ownerID)
		trust.AcceptRootChain(creds.RootChain)
		d := &peer.Dialer{RelayURL: relayURL, Role: relayv1.Role_ROLE_CLIENT, Key: key, Trust: trust,
			Credentials: func() *identityv1.Credentials { return creds }}
		c, err := d.Connect(ctx)
		if err != nil {
			return nil, err
		}
		nd, err := c.Machine(ctx, e.machineID)
		if err != nil {
			return nil, err
		}
		out = append(out, &agent{client: c, machine: nd})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Load.

type result struct {
	started, idle, failed atomic.Int64
	errMu                 sync.Mutex
	errors                map[string]int
	sessions              []benchSession
	sessMu                sync.Mutex
}

type benchSession struct {
	id string
	a  *agent // only the starting agent may archive it
}

var client = metrics.NewRegistry()

func (r *result) fail(err error) {
	r.failed.Add(1)
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	r.errMu.Lock()
	r.errors[msg]++
	r.errMu.Unlock()
}

// awaitIdle follows events after seq until the session is idle (or ends)
// and records client-side phase latencies relative to t0.
var (
	eventsSeen atomic.Int64
	eventBytes atomic.Int64
	// per second since benchStart: events received and their worst lag (µs)
	benchStart  = time.Now()
	secEvents   [3600]atomic.Int64
	secMaxLagUS [3600]atomic.Int64
)

// observeEvent records how long ev took from the machine to this subscriber
// (same machine, same clock).
func observeEvent(ev *sessionv1.SessionEvent, hist string) {
	eventsSeen.Add(1)
	eventBytes.Add(int64(proto.Size(ev)))
	sec := min(int(time.Since(benchStart).Seconds()), len(secEvents)-1)
	secEvents[sec].Add(1)
	if ev.Ts != nil {
		lag := time.Since(ev.Ts.AsTime())
		client.Histogram(hist).Observe(lag)
		for us := lag.Microseconds(); ; {
			cur := secMaxLagUS[sec].Load()
			if us <= cur || secMaxLagUS[sec].CompareAndSwap(cur, us) {
				break
			}
		}
	}
}

// watch follows a session's events until its turn completes, like a
// second viewer of its progress.
func watch(ctx context.Context, a *agent, id string) {
	stream, err := a.machine.Session.StreamEvents(ctx, &sessionv1.StreamEventsRequest{SessionId: id, Follow: true})
	if err != nil {
		return
	}
	defer stream.Close()
	for stream.Receive() {
		ev := stream.Msg()
		observeEvent(ev, "watcher_event_lag")
		if st := ev.GetStatus(); st != nil && st.Status != sessionv1.SessionStatus_SESSION_STATUS_QUEUED &&
			st.Status != sessionv1.SessionStatus_SESSION_STATUS_PREPARING && st.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
			return
		}
	}
}

func awaitIdle(ctx context.Context, a *agent, id string, after uint64, t0 time.Time, phase string) (uint64, error) {
	stream, err := a.machine.Session.StreamEvents(ctx, &sessionv1.StreamEventsRequest{SessionId: id, AfterSeq: after, Follow: true})
	if err != nil {
		return after, err
	}
	defer stream.Close()
	sawRunning, sawFirst := false, false
	for stream.Receive() {
		ev := stream.Msg()
		after = ev.Seq
		observeEvent(ev, "event_lag")
		if st := ev.GetStatus(); st != nil {
			switch st.Status {
			case sessionv1.SessionStatus_SESSION_STATUS_RUNNING:
				if !sawRunning {
					sawRunning = true
					client.Histogram(phase + "_to_running").Since(t0)
				}
			case sessionv1.SessionStatus_SESSION_STATUS_IDLE:
				client.Histogram(phase + "_turn_total").Since(t0)
				return after, nil
			case sessionv1.SessionStatus_SESSION_STATUS_CRASHED, sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED:
				return after, fmt.Errorf("session %s: %s", st.Status, st.Reason)
			}
		}
		if !sawFirst && (ev.GetToolCall() != nil || ev.GetAgentMessage() != nil) {
			sawFirst = true
			client.Histogram(phase + "_first_event").Since(t0)
		}
	}
	if err := stream.Err(); err != nil {
		return after, err
	}
	return after, fmt.Errorf("stream ended early")
}

func load(ctx context.Context, agents []*agent, r *result, onHalf func()) time.Duration {
	jobs := make(chan int)
	var wg sync.WaitGroup
	var halfOnce sync.Once
	start := time.Now()
	for w := 0; w < *flagConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				a := agents[i%len(agents)]
				t0 := time.Now()
				res, err := a.machine.Session.StartSession(ctx, &sessionv1.StartSessionRequest{ProjectId: "bench", Agent: "codex",
					Prompt: fmt.Sprintf("bench task %d", i), IdempotencyKey: fmt.Sprintf("bench-%d", i)})
				client.Histogram("start_rpc").Since(t0)
				if err != nil {
					r.fail(err)
					continue
				}
				r.started.Add(1)
				id := res.Session.SessionId
				r.sessMu.Lock()
				r.sessions = append(r.sessions, benchSession{id, a})
				r.sessMu.Unlock()
				for w := 0; w < *flagWatchers; w++ {
					go watch(ctx, a, id)
				}
				seq, err := awaitIdle(ctx, a, id, 0, t0, "turn1")
				if err != nil {
					r.fail(err)
					continue
				}
				ok := true
				for t := 2; t <= *flagTurns; t++ {
					t1 := time.Now()
					if _, err := a.machine.Session.SendMessage(ctx, &sessionv1.SendMessageRequest{SessionId: id, Text: fmt.Sprintf("follow-up %d", t)}); err != nil {
						r.fail(err)
						ok = false
						break
					}
					client.Histogram("send_rpc").Since(t1)
					if seq, err = awaitIdle(ctx, a, id, seq, t1, "turnN"); err != nil {
						r.fail(err)
						ok = false
						break
					}
				}
				if ok {
					r.idle.Add(1)
				}
				if n := r.idle.Load(); n == int64(*flagSessions/2) {
					halfOnce.Do(func() { go onHalf() })
				}
				if *flagHold == 0 {
					t2 := time.Now()
					a.machine.Session.ArchiveSession(ctx, &sessionv1.ArchiveSessionRequest{SessionId: id})
					client.Histogram("archive_rpc").Since(t2)
				}
			}
		}()
	}
	for i := 0; i < *flagSessions; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return time.Since(start)
}

// ---------------------------------------------------------------------------
// Resource sampling.

type sample struct {
	T                                          float64
	Wall                                       time.Time `json:"-"`
	RelayCPU, DaemonCPU, ChildCPU              float64   // percent of one core
	HelperCPU, MachineCPU                      float64   // spawn helpers; every process on the machine
	RelayRSS, DaemonRSS, ChildRSS              int64     // KiB
	HelperRSS                                  int64
	Children                                   int
	DaemonGoroutines, RelayGoroutines          int64
	DaemonFDs, DaemonSessionsLive, DaemonProcs int64
	DaemonWAL                                  int64 // bytes
}

type procStat struct {
	ppid int
	rss  int64
	cpu  float64 // seconds
	comm string
}

// reclaim: from the first archive until no agent process is left (-1 if
// some were still alive after the timeout).
var reclaim time.Duration

// waitAgentsGone polls until hermeshd has no fakecodex children and
// returns that moment.
func waitAgentsGone(e *env, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(agentPids(psAll(), e.daemon.Process.Pid)) == 0 {
			return time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return time.Time{}
}

// agentPids lists fake agents started by hermeshd (np), directly or through
// its spawn helper.
func agentPids(ps map[int]procStat, np int) []int {
	parents := map[int]bool{np: true}
	for pid, st := range ps {
		if st.ppid == np && strings.Contains(st.comm, "hermeshd") {
			parents[pid] = true
		}
	}
	var out []int
	for pid, st := range ps {
		if parents[st.ppid] && strings.Contains(st.comm, "fakecodex") {
			out = append(out, pid)
		}
	}
	return out
}

func psAll() map[int]procStat {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,rss=,time=,comm=").Output()
	if err != nil {
		return nil
	}
	m := map[int]procStat{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		rss, _ := strconv.ParseInt(f[2], 10, 64)
		m[pid] = procStat{ppid: ppid, rss: rss, cpu: parseCPUTime(f[3]), comm: f[4]}
	}
	return m
}

func parseCPUTime(s string) float64 {
	// [[dd-]hh:]mm:ss.ss
	var total float64
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, _ := strconv.ParseFloat(s[:i], 64)
		total += d * 86400
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	mult := 1.0
	for i := len(parts) - 1; i >= 0; i-- {
		v, _ := strconv.ParseFloat(parts[i], 64)
		total += v * mult
		mult *= 60
	}
	return total
}

func scrape(url string) map[string]float64 {
	r, err := http.Get(url + "/metrics")
	if err != nil {
		return nil
	}
	defer r.Body.Close()
	m := map[string]float64{}
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		v, _ := strconv.ParseFloat(line[i+1:], 64)
		m[line[:i]] = v
	}
	return m
}

func sampler(ctx context.Context, e *env, start time.Time, out *[]sample, mu *sync.Mutex) {
	prev := map[int]procStat{}
	prevT := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		ps := psAll()
		dt := now.Sub(prevT).Seconds()
		cpu := func(pid int) float64 {
			c, p := ps[pid], prev[pid]
			if p.cpu == 0 || c.cpu < p.cpu {
				return 0
			}
			return (c.cpu - p.cpu) / dt * 100
		}
		s := sample{T: now.Sub(start).Seconds(), Wall: now}
		rp, np := e.relay.Process.Pid, e.daemon.Process.Pid
		s.RelayCPU, s.DaemonCPU = cpu(rp), cpu(np)
		s.RelayRSS, s.DaemonRSS = ps[rp].rss, ps[np].rss
		for pid, st := range ps {
			s.MachineCPU += cpu(pid)
			if st.ppid == np && strings.Contains(st.comm, "hermeshd") {
				s.HelperCPU += cpu(pid)
				s.HelperRSS += st.rss
			}
		}
		for _, pid := range agentPids(ps, np) {
			s.Children++
			s.ChildRSS += ps[pid].rss
			s.ChildCPU += cpu(pid)
		}
		if m := scrape(e.daemonDbg); m != nil {
			s.DaemonGoroutines = int64(m["go_sched_goroutines_goroutines"])
			s.DaemonFDs = int64(m["process_open_fds"])
			s.DaemonWAL = int64(m["hermesh_machine_wal_bytes"])
			s.DaemonProcs = int64(m["hermesh_machine_provider_processes"])
			for k, v := range m {
				if strings.HasPrefix(k, "hermesh_machine_sessions{") {
					s.DaemonSessionsLive += int64(v)
				}
			}
		}
		if m := scrape(e.relayDbg); m != nil {
			s.RelayGoroutines = int64(m["go_sched_goroutines_goroutines"])
		}
		mu.Lock()
		*out = append(*out, s)
		mu.Unlock()
		prev, prevT = ps, now
	}
}

func fetch(url, path string) error {
	r, err := http.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r.Body)
	return err
}

// ---------------------------------------------------------------------------

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fmt.Printf("setting up (out: %s)…\n", *flagOut)
	e, err := setup(ctx)
	if err != nil {
		return err
	}
	defer func() {
		syscall.Kill(-e.daemon.Process.Pid, syscall.SIGKILL)
		syscall.Kill(-e.relay.Process.Pid, syscall.SIGKILL)
		e.daemon.Wait()
		e.relay.Wait()
		os.RemoveAll(e.tmp)
	}()
	agents, err := pairAgents(ctx, e, *flagAgents)
	if err != nil {
		return err
	}
	fmt.Printf("ready: %d agents; starting %d sessions, concurrency %d, %d turn(s)\n", len(agents), *flagSessions, *flagConcurrency, *flagTurns)

	var samples []sample
	var smu sync.Mutex
	sctx, stopSampler := context.WithCancel(ctx)
	t0 := time.Now()
	go sampler(sctx, e, t0, &samples, &smu)
	time.Sleep(1500 * time.Millisecond) // baseline samples

	var profWG sync.WaitGroup
	onHalf := func() {
		// where goroutines wait under load (aggregated stacks)
		go fetch(e.daemonDbg+"/debug/pprof/goroutine?debug=1", filepath.Join(*flagOut, "hermeshd.goroutine-load.txt"))
		if *flagTrace > 0 {
			profWG.Add(1)
			go func() {
				defer profWG.Done()
				fetch(fmt.Sprintf("%s/debug/pprof/trace?seconds=%g", e.daemonDbg, flagTrace.Seconds()), filepath.Join(*flagOut, "hermeshd.trace"))
			}()
		}
		if *flagProfile <= 0 {
			return
		}
		profWG.Add(2)
		sec := int(flagProfile.Seconds())
		go func() {
			defer profWG.Done()
			fetch(fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", e.daemonDbg, sec), filepath.Join(*flagOut, "hermeshd.cpu.pprof"))
		}()
		go func() {
			defer profWG.Done()
			fetch(fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", e.relayDbg, sec), filepath.Join(*flagOut, "relay.cpu.pprof"))
		}()
	}
	r := &result{errors: map[string]int{}}
	elapsed := load(ctx, agents, r, onHalf)
	fmt.Printf("load done in %s: %d started, %d idle, %d failed\n", elapsed.Round(time.Millisecond), r.started.Load(), r.idle.Load(), r.failed.Load())

	if *flagHold > 0 {
		fmt.Printf("holding %d sessions for %s…\n", r.idle.Load(), *flagHold)
		time.Sleep(*flagHold)
		fetch(e.daemonDbg+"/debug/pprof/heap", filepath.Join(*flagOut, "hermeshd.heap.pprof"))
		fetch(e.daemonDbg+"/debug/pprof/goroutine", filepath.Join(*flagOut, "hermeshd.goroutine.pprof"))
		holdEnd := time.Now()
		var cwg sync.WaitGroup
		sem := make(chan struct{}, 64)
		for _, bs := range r.sessions {
			cwg.Add(1)
			sem <- struct{}{}
			go func(bs benchSession) {
				defer cwg.Done()
				defer func() { <-sem }()
				t := time.Now()
				if _, err := bs.a.machine.Session.ArchiveSession(ctx, &sessionv1.ArchiveSessionRequest{SessionId: bs.id}); err != nil {
					r.fail(fmt.Errorf("archive: %w", err))
					return
				}
				client.Histogram("archive_rpc").Since(t)
			}(bs)
		}
		cwg.Wait()
		fmt.Printf("archived all in %s\n", time.Since(holdEnd).Round(time.Millisecond))
		reclaim = -1
		if t := waitAgentsGone(e, 30*time.Second); !t.IsZero() {
			reclaim = t.Sub(holdEnd)
		}
		fmt.Printf("agent processes gone %s after the first archive\n", reclaim.Round(time.Millisecond))
		time.Sleep(time.Second)
	} else {
		time.Sleep(2 * time.Second)
	}
	profWG.Wait()
	daemonMetrics := scrapeRaw(e.daemonDbg)
	relayMetrics := scrapeRaw(e.relayDbg)
	stopSampler()
	smu.Lock()
	defer smu.Unlock()
	return report(e, r, elapsed, samples, daemonMetrics, relayMetrics)
}

func scrapeRaw(url string) string {
	r, err := http.Get(url + "/metrics")
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// ---------------------------------------------------------------------------
// Report.

type promHist struct {
	bounds []float64
	counts []float64
	sum    float64
	n      float64
}

var bucketRE = regexp.MustCompile(`^([a-z_]+)_bucket\{(.*?),?le="([^"]+)"\} (\d+)$`)

func parseHists(text string) map[string]*promHist {
	out := map[string]*promHist{}
	for _, line := range strings.Split(text, "\n") {
		m := bucketRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if m[2] != "" {
			name += "{" + strings.TrimSuffix(m[2], ",") + "}"
		}
		h := out[name]
		if h == nil {
			h = &promHist{}
			out[name] = h
		}
		b := 1e18
		if m[3] != "+Inf" {
			b, _ = strconv.ParseFloat(m[3], 64)
		}
		c, _ := strconv.ParseFloat(m[4], 64)
		h.bounds = append(h.bounds, b)
		h.counts = append(h.counts, c)
		h.n = c
	}
	return out
}

func (h *promHist) q(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	target := q * h.n
	prevC, prevB := 0.0, 0.0
	for i, c := range h.counts {
		if c >= target && c > prevC {
			if h.bounds[i] > 1e17 {
				return time.Duration(prevB * 1e9)
			}
			v := prevB + (h.bounds[i]-prevB)*(target-prevC)/(c-prevC)
			return time.Duration(v * 1e9)
		}
		prevC, prevB = c, h.bounds[i]
	}
	return 0
}

func fmtD(d time.Duration) string {
	switch {
	case d == 0:
		return "—"
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d)/1e3)
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d)/1e6)
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func report(e *env, r *result, elapsed time.Duration, samples []sample, daemonM, relayM string) error {
	var b bytes.Buffer
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("# Hermesh bench %s\n\n", *flagLabel)
	w("sessions=%d concurrency=%d agents=%d turns=%d hold=%s no-worktree=%v | fake agent: turn=%dms deltas=%d commands=%d messages=%d output=%dB writes=%d ballast=%dMB watchers=%d\n\n",
		*flagSessions, *flagConcurrency, *flagAgents, *flagTurns, *flagHold, *flagNoWorktree, *flagTurnMS, *flagDeltas, *flagCommands, *flagMessages, *flagOutBytes, *flagWrites, *flagBallast, *flagWatchers)
	w("## Outcome\n\n- elapsed %s, **%.1f sessions/s**, started %d, completed %d, failed %d\n", elapsed.Round(time.Millisecond),
		float64(r.idle.Load())/elapsed.Seconds(), r.started.Load(), r.idle.Load(), r.failed.Load())
	w("- events delivered to subscribers: %d (%.0f/s over the load, %.1f MiB)\n", eventsSeen.Load(),
		float64(eventsSeen.Load())/elapsed.Seconds(), float64(eventBytes.Load())/(1<<20))
	if *flagHold > 0 {
		if reclaim < 0 {
			w("- agent processes still alive 30s after archiving\n")
		} else {
			w("- all agent processes gone %s after the first archive\n", reclaim.Round(time.Millisecond))
		}
	}
	var errs []string
	for k, v := range r.errors {
		errs = append(errs, fmt.Sprintf("  - %d× %s", v, k))
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		w("- errors:\n%s\n", strings.Join(errs, "\n"))
	}
	w("\n## Client-side latency (agent's view)\n\n| phase | n | p50 | p90 | p99 | max |\n|---|---|---|---|---|---|\n")
	hs := client.Histograms()
	var names []string
	for k := range hs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		h := hs[n]
		w("| %s | %d | %s | %s | %s | %s |\n", n, h.Count(), fmtD(h.Quantile(.5)), fmtD(h.Quantile(.9)), fmtD(h.Quantile(.99)), fmtD(h.Quantile(1)))
	}
	for _, sec := range []struct{ title, text string }{{"hermeshd", daemonM}, {"relay", relayM}} {
		w("\n## Server-side latency: %s\n\n| metric | n | p50 | p90 | p99 |\n|---|---|---|---|---|\n", sec.title)
		ph := parseHists(sec.text)
		var keys []string
		for k, h := range ph {
			if h.n > 0 {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			h := ph[k]
			w("| %s | %.0f | %s | %s | %s |\n", strings.TrimPrefix(strings.TrimPrefix(k, "hermesh_machine_"), "hermesh_relay_"), h.n, fmtD(h.q(.5)), fmtD(h.q(.9)), fmtD(h.q(.99)))
		}
	}
	// Resources.
	if len(samples) > 0 {
		base := samples[0]
		var peak sample
		var sumDaemon, sumRelay, sumChild, sumHelper, sumMachine float64
		for _, s := range samples {
			sumHelper += s.HelperCPU
			sumMachine += s.MachineCPU
			peak.HelperCPU = max(peak.HelperCPU, s.HelperCPU)
			peak.HelperRSS = max(peak.HelperRSS, s.HelperRSS)
			peak.MachineCPU = max(peak.MachineCPU, s.MachineCPU)
			sumDaemon += s.DaemonCPU
			sumRelay += s.RelayCPU
			sumChild += s.ChildCPU
			if s.DaemonCPU > peak.DaemonCPU {
				peak.DaemonCPU = s.DaemonCPU
			}
			if s.RelayCPU > peak.RelayCPU {
				peak.RelayCPU = s.RelayCPU
			}
			if s.ChildCPU > peak.ChildCPU {
				peak.ChildCPU = s.ChildCPU
			}
			peak.DaemonRSS = max(peak.DaemonRSS, s.DaemonRSS)
			peak.RelayRSS = max(peak.RelayRSS, s.RelayRSS)
			peak.ChildRSS = max(peak.ChildRSS, s.ChildRSS)
			peak.Children = max(peak.Children, s.Children)
			peak.DaemonGoroutines = max(peak.DaemonGoroutines, s.DaemonGoroutines)
			peak.RelayGoroutines = max(peak.RelayGoroutines, s.RelayGoroutines)
			peak.DaemonFDs = max(peak.DaemonFDs, s.DaemonFDs)
			peak.DaemonWAL = max(peak.DaemonWAL, s.DaemonWAL)
		}
		n := float64(len(samples))
		w("\n## Resources (sampled every 1s)\n\n| process | avg CPU | peak CPU | baseline RSS | peak RSS | peak goroutines |\n|---|---|---|---|---|---|\n")
		w("| relay | %.0f%% | %.0f%% | %s | %s | %d |\n", sumRelay/n, peak.RelayCPU, mib(base.RelayRSS), mib(peak.RelayRSS), peak.RelayGoroutines)
		w("| hermeshd | %.0f%% | %.0f%% | %s | %s | %d |\n", sumDaemon/n, peak.DaemonCPU, mib(base.DaemonRSS), mib(peak.DaemonRSS), peak.DaemonGoroutines)
		w("| spawn helpers | %.0f%% | %.0f%% | — | %s | — |\n", sumHelper/n, peak.HelperCPU, mib(peak.HelperRSS))
		w("| agent processes (all) | %.0f%% | %.0f%% | — | %s (%d procs) | — |\n", sumChild/n, peak.ChildCPU, mib(peak.ChildRSS), peak.Children)
		w("| whole machine (%d cores) | %.0f%% | %.0f%% | — | — | — |\n", runtime.NumCPU(), sumMachine/n, peak.MachineCPU)
		live := max(int64(peak.Children), 1)
		w("\n- hermeshd marginal memory per live session: **%.1f KiB** ((peak − baseline) / %d live processes)\n", float64(peak.DaemonRSS-base.DaemonRSS)/float64(live), live)
		w("- agent process RSS each: %.1f MiB\n- hermeshd peak open fds: %d\n- peak SQLite WAL: %.1f MiB\n", float64(peak.ChildRSS)/float64(live)/1024, peak.DaemonFDs, float64(peak.DaemonWAL)/(1<<20))
		w("\n<details><summary>timeline</summary>\n\n| t | relay cpu | machine cpu | helpers cpu | agents cpu | machine cpu | machine rss | agent procs | machine goroutines | machine fds | events/s recv | max event lag |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, s := range samples {
			sec := min(max(int(s.Wall.Sub(benchStart).Seconds())-1, 0), len(secEvents)-1)
			w("| %.0f | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %s | %d | %d | %d | %d | %s |\n", s.T, s.RelayCPU, s.DaemonCPU, s.HelperCPU, s.ChildCPU, s.MachineCPU, mib(s.DaemonRSS), s.Children, s.DaemonGoroutines, s.DaemonFDs,
				secEvents[sec].Load(), fmtD(time.Duration(secMaxLagUS[sec].Load())*time.Microsecond))
		}
		w("\n</details>\n")
	}
	if _, err := os.Stat(filepath.Join(*flagOut, "hermeshd.cpu.pprof")); err == nil {
		for _, p := range []string{"hermeshd", "relay"} {
			out, _ := exec.Command("go", "tool", "pprof", "-top", "-machinecount=25", filepath.Join(*flagOut, p+".cpu.pprof")).CombinedOutput()
			w("\n## CPU profile: %s (top 25)\n\n```\n%s```\n", p, out)
		}
	}
	os.WriteFile(filepath.Join(*flagOut, "report.md"), b.Bytes(), 0o644)
	os.WriteFile(filepath.Join(*flagOut, "hermeshd.metrics.txt"), []byte(daemonM), 0o644)
	os.WriteFile(filepath.Join(*flagOut, "relay.metrics.txt"), []byte(relayM), 0o644)
	js, _ := json.MarshalIndent(samples, "", " ")
	os.WriteFile(filepath.Join(*flagOut, "samples.json"), js, 0o644)
	fmt.Println(b.String())
	return nil
}

func mib(kib int64) string { return fmt.Sprintf("%.1fMiB", float64(kib)/1024) }

var _ = connect.CodeOf
