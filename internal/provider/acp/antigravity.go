package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

type antigravityMode uint8

const (
	antigravityNone antigravityMode = iota
	// Official `agy acp` (1.5.0+): credentials live in the OS keyring or CLI
	// environment (never read by Hermesh); login state comes from ACP protocol.
	antigravityCLI
	// Legacy standalone ACP registry archive (`agy_acp_server.par` / `.exe`):
	// requires an explicit auth.type in $GEMINI_HOME/antigravity-acp/settings.json.
	antigravityStandalone
)

const antigravityACPDir = "antigravity-acp"

func (p *Provider) antigravityCandidates(ctx context.Context) []launch {
	var standaloneArgs []string
	if p.fallback != nil {
		standaloneArgs = p.fallback.args
	}
	if p.Binary != "" {
		if isAgyBinary(p.Binary) {
			return []launch{{bin: p.Binary, args: p.Args, mode: antigravityCLI}}
		}
		return []launch{{bin: p.Binary, args: standaloneArgs, mode: antigravityStandalone}}
	}
	var out []launch
	for _, bin := range p.lookAll(p.Command, antigravityCLIPaths()...) {
		if p.agySupportsACP(ctx, bin) {
			out = append(out, launch{bin: bin, args: p.Args, mode: antigravityCLI})
			break
		}
	}
	if p.fallback != nil {
		if bins := p.lookAll(p.fallback.command, p.fallback.paths...); len(bins) > 0 {
			out = append(out, launch{bin: bins[0], args: p.fallback.args, mode: antigravityStandalone})
		}
	}
	return out
}

// antigravityReady reports whether Discover confirmed this Antigravity entry
// point is runnable and authenticated (neither auth_required nor a probe error).
func antigravityReady(info provider.Info) bool {
	return info.Available && info.LoggedIn && info.ModelsState != provider.ModelsError
}

func (p *Provider) discoverAntigravity(ctx context.Context) provider.Info {
	candidates := p.antigravityCandidates(ctx)
	info, _ := p.selectAntigravity(ctx, candidates, provider.ProbeEnv(p.PathEnv))
	return info
}

// selectAntigravity picks between official `agy acp` and the legacy standalone
// archive. When both are installed, `agy acp` is preferred once confirmed ready,
// but an unauthenticated or broken `agy` must not shadow an already-working
// standalone installation.
func (p *Provider) selectAntigravity(ctx context.Context, candidates []launch, env []string) (provider.Info, launch) {
	if len(candidates) == 0 {
		return p.notFoundInfo(), launch{}
	}
	stamp := antigravityCandidatesStamp(candidates, env)
	if len(candidates) == 1 {
		info := p.discoverLaunch(ctx, candidates[0], env)
		p.setChosen(stamp, candidates[0])
		return info, candidates[0]
	}
	infoAgy := p.discoverLaunch(ctx, candidates[0], env)
	if antigravityReady(infoAgy) {
		p.setChosen(stamp, candidates[0])
		return infoAgy, candidates[0]
	}
	infoStandalone := p.discoverLaunch(ctx, candidates[1], env)
	if antigravityReady(infoStandalone) ||
		(!infoAgy.Available && infoStandalone.Available) ||
		(!infoAgy.LoggedIn && infoStandalone.Available && infoStandalone.LoggedIn) {
		p.setChosen(stamp, candidates[1])
		return infoStandalone, candidates[1]
	}
	p.setChosen(stamp, candidates[0])
	return infoAgy, candidates[0]
}

func (p *Provider) resolveAntigravityStart(ctx context.Context, env []string) launch {
	candidates := p.antigravityCandidates(ctx)
	if len(candidates) == 0 {
		return launch{}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	probeEnv := env
	if probeEnv == nil {
		probeEnv = provider.ProbeEnv(p.PathEnv)
	}
	stamp := antigravityCandidatesStamp(candidates, probeEnv)
	if chosen, ok := p.getChosen(stamp); ok {
		return chosen
	}
	if !antigravityStandaloneConfigured(probeEnv) {
		return candidates[0]
	}
	_, chosen := p.selectAntigravity(ctx, candidates, probeEnv)
	return chosen
}

func (p *Provider) resolveAntigravityLogin(ctx context.Context) launch {
	candidates := p.antigravityCandidates(ctx)
	if len(candidates) == 0 {
		return launch{}
	}
	return candidates[0]
}

func (p *Provider) getChosen(stamp string) (launch, bool) {
	p.acpMu.Lock()
	defer p.acpMu.Unlock()
	if stamp != "" && p.chosenStamp == stamp && p.chosenLaunch.bin != "" {
		return p.chosenLaunch, true
	}
	return launch{}, false
}

func (p *Provider) setChosen(stamp string, l launch) {
	if stamp == "" || l.bin == "" {
		return
	}
	p.acpMu.Lock()
	defer p.acpMu.Unlock()
	p.chosenStamp = stamp
	p.chosenLaunch = l
}

func (p *Provider) clearChosen() {
	p.acpMu.Lock()
	defer p.acpMu.Unlock()
	p.chosenStamp = ""
	p.chosenLaunch = launch{}
}

func antigravityCandidatesStamp(candidates []launch, env []string) string {
	var parts []string
	for _, c := range candidates {
		parts = append(parts, binaryStamp(c.bin), modelContext(c.bin, env))
	}
	return strings.Join(parts, "\x1f")
}

func antigravityCLIPaths() []string {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return []string{filepath.Join(local, "agy", "bin", "agy.exe")}
	}
	return nil
}

func isAgyBinary(bin string) bool {
	base := filepath.Base(bin)
	if i := strings.LastIndexByte(base, '\\'); i >= 0 {
		base = base[i+1:]
	}
	ext := strings.ToLower(filepath.Ext(base))
	if ext == ".exe" || ext == ".cmd" || ext == ".bat" {
		base = base[:len(base)-len(ext)]
	}
	return strings.EqualFold(base, "agy")
}

func binaryStamp(bin string) string {
	target, err := filepath.EvalSymlinks(bin)
	if err != nil {
		target = bin
	}
	st, err := os.Stat(target)
	if err != nil {
		return ""
	}
	return bin + "\x00" + target + "\x00" + strconv.FormatInt(st.Size(), 10) + "\x00" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

func (p *Provider) agySupportsACP(ctx context.Context, bin string) bool {
	stamp := binaryStamp(bin)
	if stamp != "" {
		p.acpMu.Lock()
		ok, cached := p.acpProbe[stamp]
		p.acpMu.Unlock()
		if cached {
			return ok
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stdout, stderr, err := provider.RunCommand(probeCtx, bin, []string{"acp", "--help"}, "", provider.ProbeEnv(p.PathEnv))
	if err != nil {
		transient := errors.Is(probeCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, syscall.EAGAIN)
		if !transient && stamp != "" {
			p.cacheACPProbe(stamp, false)
		}
		return false
	}
	out := strings.ToLower(string(stdout) + "\n" + string(stderr))
	ok := strings.Contains(out, "acp")
	if stamp != "" {
		p.cacheACPProbe(stamp, ok)
	}
	return ok
}

func (p *Provider) cacheACPProbe(stamp string, ok bool) {
	p.acpMu.Lock()
	defer p.acpMu.Unlock()
	if p.acpProbe == nil {
		p.acpProbe = map[string]bool{}
	}
	p.acpProbe[stamp] = ok
}

func geminiHome(env []string) string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gemini")
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GEMINI_HOME="); ok && v != "" {
			dir = v
		}
	}
	return dir
}

// The legacy standalone ACP archive requires an explicitly selected auth.type
// in $GEMINI_HOME/antigravity-acp/settings.json, saved by authenticate. Official
// `agy acp` manages credentials via the OS keyring or CLI environment and is
// checked over ACP protocol instead. Never read OS keychains or copy desktop
// credentials.
func antigravityStandaloneConfigured(env []string) bool {
	b, err := os.ReadFile(filepath.Join(geminiHome(env), antigravityACPDir, "settings.json"))
	if err != nil {
		return false
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	var settings struct {
		Auth struct {
			Type string `json:"type"`
		} `json:"auth"`
	}
	return json.Unmarshal(b, &settings) == nil && settings.Auth.Type != ""
}
