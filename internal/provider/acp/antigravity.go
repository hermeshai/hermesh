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

const (
	// Official `agy acp` defaults --app-data-dir to antigravity-cli, while the
	// standalone ACP registry archive uses antigravity-acp.
	antigravityCLIDir = "antigravity-cli"
	antigravityACPDir = "antigravity-acp"
)

func (p *Provider) resolveAntigravity(ctx context.Context) launch {
	var standaloneArgs []string
	if p.fallback != nil {
		standaloneArgs = p.fallback.args
	}
	if p.Binary != "" {
		if isAgyBinary(p.Binary) {
			return launch{bin: p.Binary, args: p.Args, configDir: antigravityCLIDir}
		}
		return launch{bin: p.Binary, args: standaloneArgs, configDir: antigravityACPDir}
	}
	for _, bin := range p.lookAll(p.Command, antigravityCLIPaths()...) {
		if p.agySupportsACP(ctx, bin) {
			return launch{bin: bin, args: p.Args, configDir: antigravityCLIDir}
		}
	}
	if p.fallback != nil {
		if bins := p.lookAll(p.fallback.command, p.fallback.paths...); len(bins) > 0 {
			return launch{bin: bins[0], args: p.fallback.args, configDir: antigravityACPDir}
		}
	}
	return launch{}
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

// The official server requires an explicitly selected auth method, saved by
// authenticate. This only checks configuration in the directory matching the
// selected launch mode; the server validates credentials when a session starts.
// Never read or copy the desktop app's credentials.
func antigravityConfigured(env []string, appDataDir string) bool {
	if appDataDir == "" {
		appDataDir = antigravityACPDir
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gemini")
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GEMINI_HOME="); ok && v != "" {
			dir = v
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, appDataDir, "settings.json"))
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
