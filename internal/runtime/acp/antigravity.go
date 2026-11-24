package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hermeshai/hermesh/internal/runtime"
)

type antigravityMode uint8

const (
	antigravityNone antigravityMode = iota
	antigravityCLI
	antigravityStandalone
)

const antigravityACPDir = "antigravity-acp"

func (p *Provider) antigravityCandidates(ctx context.Context) []launch {
	var standaloneArgs []string
	if p.fallback != nil {
		standaloneArgs = p.fallback.args
	}
	if b := p.effectiveBinary(); b != "" {
		if isAgyBinary(b) {
			return []launch{{bin: b, args: p.Args, mode: antigravityCLI}}
		}
		return []launch{{bin: b, args: standaloneArgs, mode: antigravityStandalone}}
	}
	var agy launch
	for _, bin := range p.lookAll(p.Command, antigravityCLIPaths()...) {
		ok, err := p.agySupportsACP(ctx, bin)
		if ok || (err != nil && agy.bin == "") {
			agy = launch{bin: bin, args: p.Args, mode: antigravityCLI, probeErr: err}
		}
		if ok {
			break
		}
	}
	var out []launch
	if agy.bin != "" {
		out = append(out, agy)
	}
	if p.fallback != nil {
		if bins := p.lookAll(p.fallback.command, p.fallback.paths...); len(bins) > 0 {
			out = append(out, launch{bin: bins[0], args: p.fallback.args, mode: antigravityStandalone})
		}
	}
	return out
}

func antigravityReady(info runtime.Info) bool {
	return info.Available && info.LoggedIn && info.ModelsState != runtime.ModelsError
}

func (p *Provider) discoverAntigravity(ctx context.Context) runtime.Info {
	candidates := p.antigravityCandidates(ctx)
	info, _ := p.selectAntigravity(ctx, candidates, runtime.ProbeEnv(p.PathEnv))
	return info
}

func (p *Provider) selectAntigravity(ctx context.Context, candidates []launch, env []string) (runtime.Info, launch) {
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
		probeEnv = runtime.ProbeEnv(p.PathEnv)
	}
	stamp := antigravityCandidatesStamp(candidates, probeEnv)
	if chosen, ok := p.getChosen(stamp); ok {
		return chosen
	}
	if candidates[0].probeErr == nil && !antigravityStandaloneConfigured(probeEnv) {
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
	if candidates[0].probeErr != nil && len(candidates) > 1 {
		return candidates[1]
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
		if c.probeErr != nil {
			return ""
		}
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

func (p *Provider) agySupportsACP(ctx context.Context, bin string) (bool, error) {
	stamp := binaryStamp(bin)
	if stamp != "" {
		p.acpMu.Lock()
		ok, cached := p.acpProbe[stamp]
		p.acpMu.Unlock()
		if cached {
			return ok, nil
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stdout, stderr, err := runtime.RunCommand(probeCtx, bin, []string{"acp", "--help"}, "", runtime.ProbeEnv(p.PathEnv))
	if err != nil {
		if probeErr := probeCtx.Err(); probeErr != nil {
			if errors.Is(probeErr, context.DeadlineExceeded) {
				return false, fmt.Errorf("agy acp --help timed out: %w", probeErr)
			}
			return false, fmt.Errorf("agy acp --help probe failed: %w", probeErr)
		}
		if errors.Is(err, syscall.EAGAIN) {
			return false, fmt.Errorf("agy acp --help probe failed: %w", err)
		}
		if stamp != "" {
			p.cacheACPProbe(stamp, false)
		}
		return false, nil
	}
	out := strings.ToLower(string(stdout) + "\n" + string(stderr))
	ok := strings.Contains(out, "acp")
	if stamp != "" {
		p.cacheACPProbe(stamp, ok)
	}
	return ok, nil
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
