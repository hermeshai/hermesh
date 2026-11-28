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
type providerRecipe struct{ pkg, version, bin string }

func (r providerRecipe) command(id string) string {
	if r.bin != "" {
		return r.bin
	}
	return id
}

var providerRecipes = map[string]providerRecipe{
	"claude":    {pkg: "@anthropic-ai/claude-code", version: "2.1.332"},
	"codex":     {pkg: "@openai/codex", version: "0.172.0"},
	"copilot":   {pkg: "@github/copilot", version: "1.0.117"},
	"opencode":  {pkg: "@opencode/cli", version: "2.4.2"},
	"qwen":      {pkg: "@qwen-code/qwen-code", version: "0.28.7"},
	"auggie":    {pkg: "@augmentcode/auggie", version: "1.2.3"},
	"kimi":      {pkg: "@moonshot-ai/kimi-code", version: "2.8.1"},
	"qoder":     {pkg: "@qoder-ai/qodercli", version: "1.1.100", bin: "qodercli"},
	"codebuddy": {pkg: "@tencent-ai/codebuddy-code", version: "2.187.0"},
	"grok":      {pkg: "@xai-official/grok", version: "1.0.92"},
}

func providerPath(data string) string {
	parts := []string{shellenv.Path()}
	if custom := strings.TrimSpace(os.Getenv("NPM_CONFIG_PREFIX")); custom != "" {
		parts = append([]string{filepath.Join(custom, "bin")}, parts...)
	}
	// Preserve legacy Hermesh-isolated binaries at lowest precedence if still present.
	legacy := filepath.Join(data, "providers", "bin")
	if st, err := os.Stat(legacy); err == nil && st.IsDir() {
		parts = append(parts, legacy)
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func sameExecutable(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(sa, sb)
}

// prefixAccessWritable checks without touching the filesystem whether both bin/
// and lib/node_modules/ under prefix live on a local disk writable by this user.
func prefixAccessWritable(prefix string) bool {
	if prefix == "" || !filepath.IsAbs(prefix) {
		return false
	}
	for _, sub := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "lib", "node_modules")} {
		d := sub
		for {
			st, err := os.Stat(d)
			if err == nil {
				if !st.IsDir() || !providerLocalDisk(d) || !dirWritable(d) {
					return false
				}
				break
			}
			parent := filepath.Dir(d)
			if parent == d {
				return false
			}
			d = parent
		}
	}
	return true
}

// prefixProbeWritable actively verifies write permission (catching macOS Seatbelt
// sandboxes, read-only mounts and ACLs) before running npm install.
func prefixProbeWritable(prefix string) bool {
	if !prefixAccessWritable(prefix) {
		return false
	}
	for _, sub := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "lib", "node_modules")} {
		d := sub
		for {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				f, err := os.CreateTemp(d, ".hermesh-probe-*")
				if err != nil {
					return false
				}
				name := f.Name()
				_ = f.Close()
				_ = os.Remove(name)
				break
			}
			parent := filepath.Dir(d)
			if parent == d {
				return false
			}
			d = parent
		}
	}
	return true
}

// npmPackagePrefix returns the writable npm prefix owning binPath when binPath
// resolves into <prefix>/lib/node_modules/<pkg>/..., or "" for Homebrew/native installs.
func npmPackagePrefix(binPath, pkg string) string {
	if binPath == "" || pkg == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		return ""
	}
	marker := "/lib/node_modules/" + pkg + "/"
	slash := filepath.ToSlash(real)
	idx := strings.Index(slash, marker)
	if idx <= 0 {
		return ""
	}
	prefix := filepath.FromSlash(slash[:idx])
	if !prefixAccessWritable(prefix) {
		return ""
	}
	return filepath.Clean(prefix)
}

func resolveInstallPrefix(ctx context.Context, npm string, env []string) (string, error) {
	if out, _, err := rt.RunCommand(ctx, npm, []string{"prefix", "-g"}, os.TempDir(), env); err == nil {
		if def := strings.TrimSpace(string(out)); prefixProbeWritable(def) {
			return filepath.Clean(def), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot determine user home directory for CLI installation")
	}
	fallback := filepath.Join(home, ".local")
	for _, sub := range []string{filepath.Join(fallback, "bin"), filepath.Join(fallback, "lib", "node_modules")} {
		if err := os.MkdirAll(sub, 0755); err != nil {
			return "", errors.New("default npm global directory is not writable and ~/.local could not be created")
		}
	}
	if !prefixProbeWritable(fallback) {
		return "", errors.New("neither default npm global directory nor ~/.local is writable")
	}
	return fallback, nil
}

type providerState struct {
	Managed map[string]string
	Auto    map[string]bool
	Paths   map[string]string
	Pending map[string]string
	Jobs    []*mv.ProviderOperation
}
type cachedPrefix struct {
	binary  string
	version string
	prior   string
	prefix  string
	at      time.Time
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
	prefixes  map[string]cachedPrefix
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
	m.prefixes = make(map[string]cachedPrefix)
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
			if pending := m.state.Pending[id]; pending != "" {
				pctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
				out, _, runErr := rt.RunCommand(pctx, pending, []string{"--version"}, os.TempDir(), providerInstallEnv())
				cancel()
				if runErr == nil && providerVersion.FindString(string(out)) == j.Request.TargetVersion {
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

const providerPrefixCacheTTL = 30 * time.Second

// managedInstall resolves the active npm-managed version and prefix for id,
// recognizing both Hermesh-installed packages and user-writable npm global installs.
// Caller must hold m.mu.
func (n *Machine) managedInstall(id string, infos map[string]rt.Info) (version, prefix string) {
	r, ok := providerRecipes[id]
	if !ok {
		return "", ""
	}
	m := &n.maintenance
	info, hasInfo := infos[id]
	if hasInfo && info.Available && info.Version != "" {
		prior := m.state.Paths[id]
		now := time.Now()
		if c, ok := m.prefixes[id]; ok &&
			c.binary == info.Binary &&
			c.version == info.Version &&
			c.prior == prior &&
			now.Sub(c.at) < providerPrefixCacheTTL {
			if c.prefix == "" {
				return "", ""
			}
			return info.Version, c.prefix
		}
		resolved := npmPackagePrefix(info.Binary, r.pkg)
		if resolved == "" && prior != "" && sameExecutable(info.Binary, prior) {
			p := filepath.Dir(filepath.Dir(prior))
			if prefixAccessWritable(p) {
				resolved = p
			}
		}
		if m.prefixes != nil {
			m.prefixes[id] = cachedPrefix{
				binary:  info.Binary,
				version: info.Version,
				prior:   prior,
				prefix:  resolved,
				at:      now,
			}
		}
		if resolved != "" {
			return info.Version, resolved
		}
		return "", ""
	}
	if !hasInfo {
		if v := m.state.Managed[id]; v != "" {
			var p string
			if prior := m.state.Paths[id]; prior != "" {
				p = filepath.Dir(filepath.Dir(prior))
			}
			return v, p
		}
	}
	return "", ""
}

func (n *Machine) snapshotInfos() map[string]rt.Info {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]rt.Info, len(n.infos))
	for k, v := range n.infos {
		out[k] = v
	}
	return out
}

func (n *Machine) providerStatus(st *mv.MachineStatus) {
	infos := n.snapshotInfos()
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
			item.ManagedVersion, _ = n.managedInstall(id, infos)
		}
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
	infos := n.snapshotInfos()
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
		managed, _ := n.managedInstall(op.Provider, infos)
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
	infos := n.snapshotInfos()
	m := &n.maintenance
	m.mu.Lock()
	var pending []*iv.MaintainProvider
	for id, on := range m.state.Auto {
		r := providerRecipes[id]
		v, _ := n.managedInstall(id, infos)
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
	npm := rt.LookPath("npm", shellenv.Path())
	if npm == "" {
		return errors.New("install Node.js and npm on the target machine")
	}
	if free, _, ok := diskSpace(n.cfg.DataDir); ok && free == 0 {
		return errors.New("target disk is full")
	}
	env := providerInstallEnv()
	r := providerRecipes[id]
	cmdName := r.command(id)
	var prefix string
	if op.Action == "install" {
		if info.Available || info.Binary != "" || (info.Problem != rt.ProblemNotFound && info.Problem != rt.ProblemAppWithoutCLI) {
			return errors.New("existing installation or unresolved discovery error")
		}
		var err error
		prefix, err = resolveInstallPrefix(ctx, npm, env)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(prefix, "bin", cmdName)); !os.IsNotExist(err) {
			return errors.New("executable already exists in target prefix; recheck installation")
		}
	} else {
		if !info.Available || info.Version != op.ExpectedVersion {
			return errors.New("current executable or version differs from the expected installation")
		}
		prefix = npmPackagePrefix(info.Binary, r.pkg)
		if prefix == "" {
			m.mu.Lock()
			prior := m.state.Paths[id]
			m.mu.Unlock()
			if prior != "" && sameExecutable(info.Binary, prior) {
				prefix = filepath.Dir(filepath.Dir(prior))
			}
		}
		if prefix == "" || !prefixProbeWritable(prefix) {
			return errors.New("current executable is not in a writable npm prefix")
		}
	}
	bin := filepath.Join(prefix, "bin", cmdName)
	if err := n.providerPhase(job, "installing", ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.state.Pending[id] = bin
	err := n.saveProviders()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	_, diagnostic, err := rt.RunCommand(ctx, npm, []string{"install", "--global", "--prefix", prefix, "--registry=https://registry.npmjs.org", "--" + strings.SplitN(r.pkg, "/", 2)[0] + ":registry=https://registry.npmjs.org", "--no-audit", "--no-fund", "--engine-strict", r.pkg + "@" + r.version}, os.TempDir(), env)
	if err != nil {
		m.mu.Lock()
		delete(m.state.Pending, id)
		_ = n.saveProviders()
		m.mu.Unlock()
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
	b, _, err := rt.RunCommand(ctx, bin, []string{"--version"}, os.TempDir(), env)
	if err != nil || providerVersion.FindString(string(b)) != r.version {
		return errors.New("installed binary did not report the requested version")
	}
	if err := n.providerPhase(job, "verifying", ""); err != nil {
		return err
	}
	if err := verifyProvider(ctx, id, bin, providerPath(n.cfg.DataDir), true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.state.Managed[id] = r.version
	m.state.Paths[id] = bin
	delete(m.state.Pending, id)
	clear(m.prefixes)
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
	if !info.Available || info.Version != r.version || !sameExecutable(info.Binary, bin) {
		return errors.New("activated; target driver did not confirm the expected binary and version")
	}
	return nil
}

func providerInstallEnv() []string {
	env := []string{"PATH=" + shellenv.Path(), "CI=1", "COPILOT_AUTO_UPDATE=false"}
	for _, k := range []string{"HOME", "TMPDIR", "LANG", "NPM_CONFIG_PREFIX", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
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
