package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	iv "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	mv "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/acp"
	"github.com/hermeshai/hermesh/internal/shellenv"
	"golang.org/x/mod/semver"
	"google.golang.org/protobuf/proto"
)

// These are release-tested targets, deliberately independent of npm's latest tag.
type providerRecipe struct{ pkg, version string }

var providerRecipes = map[string]providerRecipe{
	"claude":   {"@anthropic-ai/claude-code", "2.1.332"},
	"codex":    {"@openai/codex", "0.172.0"},
	"copilot":  {"@github/copilot", "1.0.117"},
	"opencode": {"@opencode/cli", "2.4.2"},
}

func providerPath(data string) string {
	return filepath.Join(data, "providers", "bin") + string(os.PathListSeparator) + shellenv.Path()
}

type providerState struct {
	Managed map[string]string
	Auto    map[string]bool
	Paths   map[string]string
	Pending map[string]string
	Jobs    []*mv.ProviderOperation
}
type providerManager struct {
	mu        sync.Mutex
	state     providerState
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
	account   string
	writable  bool
	cancelJob map[string]context.CancelFunc
}

func providerPlatform() bool {
	if runtime.GOOS == "darwin" {
		return runtime.GOARCH == "arm64"
	}
	if runtime.GOOS != "linux" {
		return false
	}
	loader := map[string]string{"arm64": "/lib/ld-linux-aarch64.so.1", "amd64": "/lib64/ld-linux-x86-64.so.2"}[runtime.GOARCH]
	_, err := os.Stat(loader)
	return err == nil
}

func providerActive(phase string) bool {
	return phase == "preflight" || phase == "installing" || phase == "verifying" || phase == "waiting"
}

func (n *Machine) openProviderManager() error {
	m := &n.maintenance
	m.ctx, m.stop = context.WithCancel(context.Background())
	m.writable = providerPlatform() && providerLocalDisk(n.cfg.DataDir)
	m.cancelJob = make(map[string]context.CancelFunc)
	if u, err := user.Current(); err == nil {
		m.account = u.Username
		m.writable = m.writable && u.Uid != "0"
	} else {
		m.writable = false
	}
	m.state = providerState{Managed: map[string]string{}, Auto: map[string]bool{}}
	data, err := n.st.Get("provider_maintenance")
	if err != nil {
		return err
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &m.state); err != nil {
			return err
		}
	}
	if m.state.Paths == nil {
		m.state.Paths = map[string]string{}
	}
	if m.state.Pending == nil {
		m.state.Pending = map[string]string{}
	}
	for _, j := range m.state.Jobs {
		if providerActive(j.Phase) {
			j.Phase = "interrupted"
			j.Detail = "Check the installed version before retrying."
			id := j.Request.Provider
			// Activation and SQLite cannot commit together. Only recover a link to
			// the exact staged executable that passed the handshake before shutdown.
			if pending := m.state.Pending[id]; pending != "" {
				target, err := os.Readlink(filepath.Join(n.cfg.DataDir, "providers", "bin", id))
				if err == nil && target == pending {
					m.state.Managed[id] = j.Request.TargetVersion
					m.state.Paths[id] = pending
				}
				delete(m.state.Pending, id)
			}
			j.UpdatedUnixMs = time.Now().UnixMilli()
		}
	}
	if len(data) > 0 {
		if err = n.saveProviders(); err != nil {
			return err
		}
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			n.scheduleProviderUpdates()
			select {
			case <-m.ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (n *Machine) stopProviderManager() {
	m := &n.maintenance
	m.mu.Lock()
	if m.stop != nil {
		m.stop()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// Caller holds the manager mutex after startup.
func (n *Machine) saveProviders() error {
	b, err := json.Marshal(n.maintenance.state)
	if err != nil {
		return err
	}
	return n.st.Set("provider_maintenance", b)
}

func (n *Machine) providerStatus(st *mv.MachineStatus) {
	m := &n.maintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range AgentNames() {
		if n.cfg.Discoverers[id] == nil {
			continue
		}
		item := &mv.ProviderMaintenance{Provider: id, Account: m.account}
		if r, ok := providerRecipes[id]; ok && m.writable && os.Getenv("HERMESH_"+strings.ToUpper(id)+"_BIN") == "" {
			item.Package = r.pkg
			item.TargetVersion = r.version
		}
		item.ManagedVersion = m.state.Managed[id]
		item.AutoUpdate = m.state.Auto[id]
		st.ProviderMaintenance = append(st.ProviderMaintenance, item)
	}
	for _, j := range m.state.Jobs {
		st.ProviderOperations = append(st.ProviderOperations, proto.CloneOf(j))
	}
}

var providerVersion = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?`)
var providerRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)

func (n *Machine) startProviderOperation(op *iv.MaintainProvider) error {
	if op == nil || !providerRequestID.MatchString(op.RequestId) || !slices.Contains(AgentNames(), op.Provider) {
		return cerr(connect.CodeInvalidArgument, "invalid provider or request id")
	}
	m := &n.maintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return errors.New("provider manager unavailable")
	}
	if m.ctx.Err() != nil {
		return m.ctx.Err()
	}
	var archived iv.MaintainProvider
	found, err := n.st.GetProto("provider_receipt/"+op.RequestId, &archived)
	if err != nil {
		return err
	}
	if found {
		if proto.Equal(&archived, op) {
			return nil
		}
		return cerr(connect.CodeAlreadyExists, "request ID already used")
	}
	for _, j := range m.state.Jobs {
		if j.Request.RequestId == op.RequestId {
			if op.Action == "cancel" && op.Provider == j.Request.Provider {
				if j.Phase == "cancelled" {
					return nil
				}
				if j.Phase != "waiting" && j.Phase != "preflight" {
					return cerr(connect.CodeFailedPrecondition, "this stage cannot be cancelled")
				}
				j.Phase = "cancelled"
				j.Detail = ""
				j.UpdatedUnixMs = time.Now().UnixMilli()
				if cancel := m.cancelJob[op.RequestId]; cancel != nil {
					cancel()
				}
				n.kickStatus()
				return n.saveProviders()
			}
			if proto.Equal(op, j.Request) {
				return nil
			}
			return cerr(connect.CodeAlreadyExists, "request ID already used")
		}
		if j.Request.Provider == op.Provider && providerActive(j.Phase) {
			return cerr(connect.CodeFailedPrecondition, "an operation is already running")
		}
	}
	switch op.Action {
	case "verify":
	case "install", "update", "enable_auto", "disable_auto":
		r, ok := providerRecipes[op.Provider]
		if !ok || !m.writable || r.version != op.TargetVersion || os.Getenv("HERMESH_"+strings.ToUpper(op.Provider)+"_BIN") != "" {
			return cerr(connect.CodeFailedPrecondition, "unsupported target or explicit binary override")
		}
		managed := m.state.Managed[op.Provider]
		if op.Action == "update" && !newerProviderVersion(managed, r.version) {
			return cerr(connect.CodeFailedPrecondition, "no approved upgrade on this release line")
		}
		if managed != op.ExpectedVersion || (op.Action == "install") != (managed == "") {
			return cerr(connect.CodeFailedPrecondition, "installation ownership or version has changed")
		}
	default:
		return cerr(connect.CodeInvalidArgument, "unknown provider operation")
	}
	j := &mv.ProviderOperation{Request: proto.CloneOf(op), Phase: "preflight", UpdatedUnixMs: time.Now().UnixMilli()}
	oldJobs := m.state.Jobs
	m.state.Jobs = append(slices.Clone(oldJobs), j)
	// Keep the newest 64 receipts, preserving any active worker.
	if len(m.state.Jobs) > 64 {
		for i, old := range m.state.Jobs {
			if !providerActive(old.Phase) {
				if err := n.st.SetProto("provider_receipt/"+old.Request.RequestId, old.Request); err != nil {
					m.state.Jobs = oldJobs
					return err
				}
				m.state.Jobs = slices.Delete(m.state.Jobs, i, i+1)
				break
			}
		}
	}
	if err := n.saveProviders(); err != nil {
		m.state.Jobs = oldJobs
		return err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelJob[op.RequestId] = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); delete(m.cancelJob, op.RequestId); m.mu.Unlock(); cancel() }()
		err := n.runProviderOperation(ctx, j)
		phase, detail := "succeeded", ""
		if err != nil {
			phase, detail = "failed", err.Error()
		}
		if err := n.providerPhase(j, phase, detail); err != nil && !errors.Is(err, context.Canceled) {
			n.log.Error("persist provider result", "error", err)
		}
	}()
	n.kickStatus()
	return nil
}

func (n *Machine) providerPhase(j *mv.ProviderOperation, phase, detail string) error {
	m := &n.maintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if j.Phase == "cancelled" {
		return context.Canceled
	}
	beforePhase, beforeDetail, beforeTime := j.Phase, j.Detail, j.UpdatedUnixMs
	j.Phase, j.Detail, j.UpdatedUnixMs = phase, detail, time.Now().UnixMilli()
	if err := n.saveProviders(); err != nil {
		j.Phase, j.Detail, j.UpdatedUnixMs = beforePhase, beforeDetail, beforeTime
		return err
	}
	n.kickStatus()
	return nil
}

func newerProviderVersion(current, target string) bool {
	a, b := "v"+current, "v"+target
	return semver.IsValid(a) && semver.IsValid(b) && semver.Major(a) == semver.Major(b) && semver.Prerelease(a) == "" && semver.Prerelease(b) == "" && semver.Compare(b, a) > 0
}

func (n *Machine) scheduleProviderUpdates() {
	m := &n.maintenance
	m.mu.Lock()
	var pending []*iv.MaintainProvider
	for id, on := range m.state.Auto {
		r := providerRecipes[id]
		v := m.state.Managed[id]
		if on && v != "" && r.version != "" && newerProviderVersion(v, r.version) {
			pending = append(pending, &iv.MaintainProvider{RequestId: "auto-" + id + "-" + strings.ReplaceAll(r.version, ".", "_"), Provider: id, Action: "update", ExpectedVersion: v, TargetVersion: r.version})
		}
	}
	m.mu.Unlock()
	for _, op := range pending {
		_ = n.startProviderOperation(op)
	}
}

func (n *Machine) runProviderOperation(parent context.Context, job *mv.ProviderOperation) error {
	op := job.Request
	id := op.Provider
	m := &n.maintenance
	probe := n.cfg.Discoverers[id]
	if probe == nil {
		return errors.New("provider driver unavailable")
	}
	if op.Action == "enable_auto" || op.Action == "disable_auto" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if parent.Err() != nil {
			return parent.Err()
		}
		m.state.Auto[id] = op.Action == "enable_auto"
		return n.saveProviders()
	}
	if op.Action == "update" && n.sessions != nil && n.sessions.SessionSummary().Running > 0 {
		if err := n.providerPhase(job, "waiting", ""); err != nil {
			return err
		}
		for n.sessions.SessionSummary().Running > 0 {
			select {
			case <-parent.Done():
				return parent.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	if n.sessions != nil {
		cfg, _, err := n.sessions.ResolveDriver(id)
		if err != nil {
			return err
		}
		expectedDriver := "acp"
		if id == "claude" || id == "codex" {
			expectedDriver = id
		}
		if cfg.Driver != expectedDriver || cfg.BinaryPath != "" || len(cfg.LaunchArgs) > 0 || cfg.Env["PATH"] != "" || (cfg.AcpFlavor != "" && cfg.AcpFlavor != id) {
			return errors.New("custom provider launch configuration; manage this CLI at its configured source and use the model test to verify the session")
		}
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	info := probe.Discover(ctx)
	if op.Action == "verify" {
		if !info.Available {
			return errors.New("CLI unavailable on this machine")
		}
		if err := n.providerPhase(job, "verifying", ""); err != nil {
			return err
		}
		if err := verifyProvider(ctx, id, info.Binary, providerPath(n.cfg.DataDir), false); err != nil {
			return err
		}
		n.mu.Lock()
		n.infos[id] = info
		n.mu.Unlock()
		n.kickStatus()
		return nil
	}
	root := filepath.Join(n.cfg.DataDir, "providers")
	link := filepath.Join(root, "bin", id)
	checkSource := func(info rt.Info) error {
		if op.Action == "install" {
			if info.Available || info.Binary != "" || (info.Problem != rt.ProblemNotFound && info.Problem != rt.ProblemAppWithoutCLI) {
				return errors.New("existing installation or unresolved discovery error")
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				return errors.New("managed entry already exists; verify installation")
			}
		} else {
			m.mu.Lock()
			prior := m.state.Paths[id]
			m.mu.Unlock()
			dest, readErr := os.Readlink(link)
			if readErr != nil || dest != prior || !info.Available || info.Version != op.ExpectedVersion || info.Binary != link {
				return errors.New("current executable or version differs from the managed installation")
			}
		}
		return nil
	}
	if err := checkSource(info); err != nil {
		return err
	}
	npm := rt.LookPath("npm", shellenv.Path())
	if npm == "" {
		return errors.New("install Node.js and npm on the target machine")
	}
	if free, _, ok := diskSpace(n.cfg.DataDir); ok && free == 0 {
		return errors.New("target disk is full")
	}
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		return err
	}
	for _, dir := range []string{root, filepath.Dir(link)} {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || !providerLocalDisk(dir) {
			return errors.New("managed prefix requires a machine-local directory")
		}
	}
	stage, err := os.MkdirTemp(root, id+"-")
	if err != nil {
		return errors.New("cannot create user installation directory")
	}
	activated := false
	defer func() {
		if !activated {
			os.RemoveAll(stage)
		}
	}()
	r := providerRecipes[id]
	if err := n.providerPhase(job, "installing", ""); err != nil {
		return err
	}
	env := providerInstallEnv()
	_, diagnostic, err := rt.RunCommand(ctx, npm, []string{"install", "--global", "--prefix", stage, "--registry=https://registry.npmjs.org", "--" + strings.SplitN(r.pkg, "/", 2)[0] + ":registry=https://registry.npmjs.org", "--no-audit", "--no-fund", "--engine-strict", r.pkg + "@" + r.version}, stage, env)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, code := range []string{"ENOSPC", "EACCES", "EPERM", "EBADENGINE", "ENOTFOUND", "EAI_AGAIN", "ETIMEDOUT", "ECONNRESET", "EINTEGRITY", "E404"} {
			if strings.Contains(string(diagnostic), code) {
				return fmt.Errorf("npm install failed: %s", code)
			}
		}
		return fmt.Errorf("npm install failed (%v); check Node version, disk space and registry connectivity", err)
	}
	bin := filepath.Join(stage, "bin", id)
	// Keep vendor updaters from changing a version owned by this machine.
	if setting := map[string]string{"copilot": "COPILOT_AUTO_UPDATE=false", "claude": "DISABLE_AUTOUPDATER=1"}[id]; setting != "" {
		launcher := filepath.Join(stage, "launch")
		quoted := "'" + strings.ReplaceAll(bin, "'", "'\"'\"'") + "'"
		if err = os.WriteFile(launcher, []byte("#!/bin/sh\nexport "+setting+"\nexec "+quoted+" \"$@\"\n"), 0700); err != nil {
			return err
		}
		bin = launcher
	}
	b, _, err := rt.RunCommand(ctx, bin, []string{"--version"}, stage, env)
	if err != nil || providerVersion.FindString(string(b)) != r.version {
		return errors.New("installed binary did not report the requested version")
	}
	if err := n.providerPhase(job, "verifying", ""); err != nil {
		return err
	}
	if err := verifyProvider(ctx, id, bin, shellenv.Path(), true); err != nil {
		return err
	}
	// The user may have installed or replaced a CLI while npm was downloading.
	if err := checkSource(probe.Discover(ctx)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The symlink changes atomically. Existing sessions keep their old version directory.
	m.mu.Lock()
	m.state.Pending[id] = bin
	err = n.saveProviders()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if op.Action == "install" {
		// Fail atomically if another process created the entry after preflight.
		err = os.Symlink(bin, link)
	} else {
		next := filepath.Join(stage, "activate")
		if err = os.Symlink(bin, next); err == nil {
			err = os.Rename(next, link)
		}
	}
	if err != nil {
		return err
	}
	activated = true
	m.mu.Lock()
	m.state.Managed[id] = r.version
	m.state.Paths[id] = bin
	delete(m.state.Pending, id)
	err = n.saveProviders()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// A pre-write scan must finish before the post-write result is published.
	n.mu.Lock()
	scan := n.checking
	n.mu.Unlock()
	if scan != nil {
		select {
		case <-scan:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	info = probe.Discover(ctx)
	n.mu.Lock()
	n.infos[id] = info
	n.mu.Unlock()
	n.kickStatus()
	if !info.Available || info.Version != r.version || info.Binary != link {
		return errors.New("activated; target driver did not confirm the expected binary and version")
	}
	return nil
}

func providerInstallEnv() []string {
	env := []string{"PATH=" + shellenv.Path(), "CI=1", "COPILOT_AUTO_UPDATE=false"}
	for _, k := range []string{"HOME", "TMPDIR", "LANG", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func verifyProvider(parent context.Context, id, bin, path string, staged bool) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	args := []string{"app-server"}
	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"hermesh","title":"Hermesh","version":"1"}}}`
	env := rt.ProbeEnv(path)
	if staged {
		env = providerInstallEnv()
	}
	switch id {
	case "claude":
		args = []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
		request = `{"type":"control_request","request_id":"probe","request":{"subtype":"initialize"}}`
	case "codex":
	default:
		agent, _ := acp.AgentByName(id)
		p := &acp.Provider{Agent: agent, PathEnv: path}
		if staged {
			p.Binary = bin
		}
		var extra []string
		bin, args, extra = p.ProtocolCommand(ctx, env)
		env = append(env, extra...)
		request = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"hermesh","version":"1"}}}`
	}
	if bin == "" {
		return errors.New("no protocol executable")
	}
	dir, err := os.MkdirTemp("", "hermesh-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p, err := rt.StartProc(ctx, bin, args, dir, env)
	if err != nil {
		return errors.New("cannot start provider protocol")
	}
	defer p.Stop(100 * time.Millisecond)
	verified := make(chan bool, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		p.Lines(func(line []byte) {
			var msg struct {
				ID     int
				Type   string
				Result struct {
					UserAgent       string
					ProtocolVersion int
				}
				Response struct {
					RequestID string `json:"request_id"`
					Subtype   string
				}
			}
			if json.Unmarshal(line, &msg) != nil {
				return
			}
			relevant := msg.ID == 1
			if id == "claude" {
				relevant = msg.Type == "control_response" && msg.Response.RequestID == "probe"
			}
			if relevant {
				ok := msg.Result.ProtocolVersion == 1
				if id == "codex" {
					ok = msg.Result.UserAgent != ""
				}
				if id == "claude" {
					ok = msg.Response.Subtype == "success"
				}
				select {
				case verified <- ok:
				default:
				}
			}
		})
	}()
	if err = p.WriteLine([]byte(request)); err != nil {
		return errors.New("could not initialize provider")
	}
	select {
	case ok := <-verified:
		if ok {
			return nil
		}
	case <-ended:
		select {
		case ok := <-verified:
			if ok {
				return nil
			}
		default:
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return errors.New("provider protocol initialization failed")
}
