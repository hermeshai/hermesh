package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/runtime"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hermesh-fakeacp-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "fakeacp"
	if goruntime.GOOS == "windows" {
		name = "fakeacp.exe"
	}
	fakeBin = filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", fakeBin, "./fakeacp").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fakeacp: %v\n%s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func isolateAntigravityEnv(t *testing.T) (home, gemini, localAppData string) {
	t.Helper()
	prev := systemBinDirs
	systemBinDirs = nil
	t.Cleanup(func() { systemBinDirs = prev })
	home = t.TempDir()
	gemini = t.TempDir()
	localAppData = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEMINI_HOME", gemini)
	t.Setenv("LOCALAPPDATA", localAppData)
	return home, gemini, localAppData
}

func agyExeName() string {
	if goruntime.GOOS == "windows" {
		return "agy.exe"
	}
	return "agy"
}

func standaloneExeName() string {
	if goruntime.GOOS == "windows" {
		return "agy_acp_server.exe"
	}
	return "agy_acp_server.par"
}

func installFakeBinary(t *testing.T, dir, name, mode, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(fakeBin)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		if err := os.WriteFile(filepath.Join(dir, "fakeacp.mode"), []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(dir, "fakeacp.version"), []byte(version), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func writeAntigravitySettings(t *testing.T, gemini, subDir, body string) {
	t.Helper()
	dir := filepath.Join(gemini, subDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readArgsLog(t *testing.T, path string) [][]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatalf("parse args log %q: %v", line, err)
		}
		out = append(out, args)
	}
	return out
}

func setFakeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if content == "" {
		os.Remove(path)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityDiscovery(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		a := antigravityAgent(goos)
		wantFallback := "agy_acp_server.par"
		if goos == "windows" {
			wantFallback = "agy_acp_server.exe"
		}
		if a.Command != "agy" || !slices.Equal(a.Args, []string{"acp"}) || !slices.Equal(a.DesktopApps, []string{"Antigravity.app"}) || len(a.Auth) != 0 {
			t.Fatalf("%s primary: %+v", goos, a)
		}
		if a.fallback == nil || a.fallback.command != wantFallback || !slices.Equal(a.fallback.paths, []string{".local/share/antigravity-acp/" + wantFallback}) || (goos == "linux") != slices.Equal(a.fallback.args, []string{"--uid="}) {
			t.Fatalf("%s fallback: %+v", goos, a.fallback)
		}
	}
	dir := t.TempDir()
	t.Setenv("GEMINI_HOME", dir)
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	if i := p.Discover(context.Background()); !i.Available || i.LoggedIn {
		t.Fatalf("installed without authentication: %+v", i)
	}
	for _, c := range []struct {
		config string
		ready  bool
	}{
		{`{}`, false}, {`invalid`, false}, {`{"auth":{"type":""}}`, false},
		{`{"auth":{"type":"oauth-personal"}}`, true}, {`{"auth":{"type":"oauth-business"}}`, true},
		{"\xef\xbb\xbf" + `{"auth":{"type":"oauth-personal"}}`, true},
	} {
		writeAntigravitySettings(t, dir, antigravityACPDir, c.config)
		if i := p.Discover(context.Background()); !i.Available || i.LoggedIn != c.ready {
			t.Fatalf("%q: %+v", c.config, i)
		}
	}
}

func TestAntigravityAgyCLIDiscoveryAndLaunch(t *testing.T) {
	home, gemini, localAppData := isolateAntigravityEnv(t)

	binDir := t.TempDir()
	agyBin := installFakeBinary(t, binDir, agyExeName(), "acp", "agy 1.5.9")
	setFakeFile(t, binDir, "fakeacp.config", "1")
	setFakeFile(t, binDir, "fakeacp.close", "1")
	setFakeFile(t, binDir, "fakeacp.models", "gemini-4-neon,gemini-3.8-flash")
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)
	t.Setenv("FAKE_ACP_STATE", t.TempDir())

	p := &Provider{Agent: antigravityAgent("linux"), PathEnv: binDir}
	i := p.Discover(context.Background())
	if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || !i.LoggedIn || i.ModelsState != runtime.ModelsKnown || len(i.Models) != 2 {
		t.Fatalf("agy signed in without settings.json: %+v", i)
	}

	for _, cfg := range []string{`{"agentMode":"auto"}`, `{"modelProvider":"gemini"}`} {
		writeAntigravitySettings(t, gemini, "antigravity-cli", cfg)
		pCfg := &Provider{Agent: antigravityAgent("linux"), PathEnv: binDir}
		got := pCfg.Discover(context.Background())
		if !got.Available || !got.LoggedIn || got.ModelsState != runtime.ModelsKnown {
			t.Fatalf("agy with official CLI settings %s: %+v", cfg, got)
		}
	}

	setFakeFile(t, binDir, "fakeacp.auth", "oauth-personal")
	pUnauth := &Provider{Agent: antigravityAgent("linux"), PathEnv: binDir}
	i = pUnauth.Discover(context.Background())
	if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || i.LoggedIn || i.ModelsState != runtime.ModelsAuthRequired {
		t.Fatalf("agy with -32000 auth required: %+v", i)
	}

	setFakeFile(t, binDir, "fakeacp.auth", "")
	setFakeFile(t, binDir, "fakeacp.new_error", "-32603")
	pErr := &Provider{Agent: antigravityAgent("linux"), PathEnv: binDir}
	i = pErr.Discover(context.Background())
	if !i.Available || i.Binary != agyBin || !i.LoggedIn || i.ModelsState != runtime.ModelsError {
		t.Fatalf("agy with non-auth probe failure must distinguish error from auth_required: %+v", i)
	}
	setFakeFile(t, binDir, "fakeacp.new_error", "")

	os.Remove(argsLog)
	evs, err := runRuntimeTurn(t, p, runtime.StartSpec{
		Workdir: t.TempDir(),
		Gate: func(context.Context, runtime.ToolGateRequest) (runtime.ToolGateResult, error) {
			return runtime.ToolGateResult{Allowed: true}, nil
		},
	}, "say hello from agy")
	if err != nil {
		t.Fatal(err)
	}
	var gotMsg string
	for _, ev := range evs {
		if ev.Kind == runtime.EventAssistantMessage {
			gotMsg = ev.Text
		}
	}
	if gotMsg != "hello from agy" {
		t.Fatalf("agy session msg=%q evs=%+v", gotMsg, evs)
	}
	logged := readArgsLog(t, argsLog)
	if !slices.ContainsFunc(logged, func(a []string) bool { return slices.Equal(a, []string{"acp"}) }) {
		t.Fatalf("expected [acp] invocation in %v", logged)
	}

	setFakeFile(t, binDir, "fakeacp.auth", "oauth-personal")
	var out bytes.Buffer
	if err := p.Login(context.Background(), "oauth-personal", &out); err != nil {
		t.Fatalf("agy login: %v", err)
	}
	setFakeFile(t, binDir, "fakeacp.auth", "")

	localBin := installFakeBinary(t, filepath.Join(home, ".local", "bin"), agyExeName(), "acp", "1.5.9")
	pLocal := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: t.TempDir()}
	if got := pLocal.Discover(context.Background()); !got.Available || got.Binary != localBin || !got.LoggedIn {
		t.Fatalf("~/.local/bin discovery: %+v", got)
	}
	os.Remove(localBin)

	winBin := installFakeBinary(t, filepath.Join(localAppData, "agy", "bin"), "agy.exe", "acp", "1.5.9")
	pWin := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: t.TempDir()}
	if got := pWin.Discover(context.Background()); !got.Available || got.Binary != winBin || !got.LoggedIn {
		t.Fatalf("LOCALAPPDATA agy discovery: %+v", got)
	}
}

func TestAntigravityStandaloneFallback(t *testing.T) {
	home, gemini, _ := isolateAntigravityEnv(t)
	standalone := installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)
	t.Setenv("FAKE_ACP_STATE", t.TempDir())

	writeAntigravitySettings(t, gemini, "antigravity-cli", `{"auth":{"type":"oauth-personal"}}`)
	p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: t.TempDir()}
	i := p.Discover(context.Background())
	if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || i.LoggedIn || i.ModelsState != runtime.ModelsAuthRequired {
		t.Fatalf("standalone without antigravity-acp settings: %+v", i)
	}

	writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)
	i = p.Discover(context.Background())
	if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || !i.LoggedIn {
		t.Fatalf("standalone with antigravity-acp settings: %+v", i)
	}

	for _, tc := range []struct {
		goos string
		want []string
	}{
		{"darwin", []string{}},
		{"linux", []string{"--uid="}},
	} {
		os.Remove(argsLog)
		a := antigravityAgent(tc.goos)
		a.fallback.command = standaloneExeName()
		a.fallback.paths = []string{".local/share/antigravity-acp/" + standaloneExeName()}
		lp := &Provider{Agent: a, PathEnv: t.TempDir()}
		if _, err := runRuntimeTurn(t, lp, runtime.StartSpec{Workdir: t.TempDir()}, "say ok"); err != nil {
			t.Fatalf("%s start: %v", tc.goos, err)
		}
		logged := readArgsLog(t, argsLog)
		if len(logged) == 0 || !slices.Equal(logged[len(logged)-1], tc.want) {
			t.Fatalf("%s standalone args = %v, want %v", tc.goos, logged, tc.want)
		}
	}
}

func TestAntigravityPrecedenceAndIncompatibleAgy(t *testing.T) {
	t.Run("prefers_ready_agy_over_standalone_without_settings_json", func(t *testing.T) {
		home, gemini, _ := isolateAntigravityEnv(t)
		agyDir := t.TempDir()
		agyBin := installFakeBinary(t, agyDir, agyExeName(), "acp", "1.5.9")
		installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
		writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)

		p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: agyDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || !i.LoggedIn {
			t.Fatalf("expected authenticated agy to win: %+v", i)
		}
	})

	t.Run("upgrade_unauthenticated_agy_preserves_working_standalone", func(t *testing.T) {
		home, gemini, _ := isolateAntigravityEnv(t)
		agyDir := t.TempDir()
		agyBin := installFakeBinary(t, agyDir, agyExeName(), "acp", "1.5.9")
		setFakeFile(t, agyDir, "fakeacp.auth", "oauth-personal")
		standaloneDir := filepath.Join(home, ".local", "share", "antigravity-acp")
		standalone := installFakeBinary(t, standaloneDir, standaloneExeName(), "", "1.4.5")
		setFakeFile(t, standaloneDir, "fakeacp.config", "1")
		setFakeFile(t, standaloneDir, "fakeacp.models", "gemini-3.8-flash")
		writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)
		t.Setenv("FAKE_ACP_STATE", t.TempDir())

		p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: agyDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || !i.LoggedIn || i.ModelsState != runtime.ModelsKnown {
			t.Fatalf("expected working standalone to be preserved: %+v", i)
		}

		evs, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir()}, "say still working")
		if err != nil {
			t.Fatalf("start after discover: %v", err)
		}
		var gotMsg string
		for _, ev := range evs {
			if ev.Kind == runtime.EventAssistantMessage {
				gotMsg = ev.Text
			}
		}
		if gotMsg != "still working" {
			t.Fatalf("session after discover: got=%q evs=%+v", gotMsg, evs)
		}

		cold := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: agyDir}
		if _, err := runRuntimeTurn(t, cold, runtime.StartSpec{Workdir: t.TempDir()}, "say cold start ok"); err != nil {
			t.Fatalf("cold start: %v", err)
		}

		setFakeFile(t, agyDir, "fakeacp.auth", "")
		setFakeFile(t, agyDir, "fakeacp.config", "1")
		setFakeFile(t, agyDir, "fakeacp.models", "gemini-4-neon")
		i = p.Discover(context.Background())
		if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || !i.LoggedIn || i.ModelsState != runtime.ModelsKnown {
			t.Fatalf("expected switch to signed-in agy: %+v", i)
		}
	})

	for _, tc := range []struct {
		name, file, val string
	}{
		{"probe_error", "fakeacp.new_error", "-32603"},
		{"broken_version", "fakeacp.version_fail", "1"},
	} {
		t.Run("upgrade_"+tc.name+"_agy_preserves_working_standalone", func(t *testing.T) {
			home, gemini, _ := isolateAntigravityEnv(t)
			agyDir := t.TempDir()
			installFakeBinary(t, agyDir, agyExeName(), "acp", "1.5.9")
			setFakeFile(t, agyDir, tc.file, tc.val)
			standalone := installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
			writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)

			p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: agyDir}
			i := p.Discover(context.Background())
			if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || !i.LoggedIn {
				t.Fatalf("expected working standalone when agy has %s: %+v", tc.name, i)
			}
		})
	}

	t.Run("upgrade_unauthenticated_agy_preserves_configured_standalone_on_probe_error", func(t *testing.T) {
		home, gemini, _ := isolateAntigravityEnv(t)
		agyDir := t.TempDir()
		installFakeBinary(t, agyDir, agyExeName(), "acp", "1.5.9")
		setFakeFile(t, agyDir, "fakeacp.auth", "oauth-personal")
		standaloneDir := filepath.Join(home, ".local", "share", "antigravity-acp")
		standalone := installFakeBinary(t, standaloneDir, standaloneExeName(), "", "1.4.5")
		setFakeFile(t, standaloneDir, "fakeacp.new_error", "-32603")
		writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)

		p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: agyDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != standalone || !i.LoggedIn || i.ModelsState != runtime.ModelsError {
			t.Fatalf("expected configured standalone with models_state=error, not agy auth_required: %+v", i)
		}
	})

	t.Run("both_unauthenticated_prefers_agy_and_login_uses_agy", func(t *testing.T) {
		home, _, _ := isolateAntigravityEnv(t)
		agyDir := t.TempDir()
		agyBin := installFakeBinary(t, agyDir, agyExeName(), "acp", "1.5.9")
		setFakeFile(t, agyDir, "fakeacp.auth", "oauth-personal")
		installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
		argsLog := filepath.Join(t.TempDir(), "args.log")
		t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)

		p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: agyDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != agyBin || i.LoggedIn || i.ModelsState != runtime.ModelsAuthRequired {
			t.Fatalf("expected agy auth_required when neither is signed in: %+v", i)
		}
		os.Remove(argsLog)
		var out bytes.Buffer
		if err := p.Login(context.Background(), "oauth-personal", &out); err != nil {
			t.Fatalf("login: %v", err)
		}
		if logged := readArgsLog(t, argsLog); !slices.ContainsFunc(logged, func(a []string) bool { return slices.Equal(a, []string{"acp"}) }) {
			t.Fatalf("expected login to invoke agy acp, got %v", logged)
		}
	})

	for _, mode := range []string{"legacy-error", "ide-launcher"} {
		t.Run("incompatible_agy_"+mode+"_falls_back_to_standalone", func(t *testing.T) {
			home, gemini, _ := isolateAntigravityEnv(t)
			badDir := t.TempDir()
			installFakeBinary(t, badDir, agyExeName(), mode, "1.3.0")
			standalone := installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
			writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)

			p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: badDir}
			i := p.Discover(context.Background())
			if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || !i.LoggedIn {
				t.Fatalf("expected fallback to standalone: %+v", i)
			}
		})
	}

	t.Run("skips_incompatible_agy_for_later_valid_agy_on_path", func(t *testing.T) {
		_, _, _ = isolateAntigravityEnv(t)
		badDir := t.TempDir()
		goodDir := t.TempDir()
		installFakeBinary(t, badDir, agyExeName(), "ide-launcher", "1.0.0")
		goodBin := installFakeBinary(t, goodDir, agyExeName(), "acp", "1.5.9")

		p := &Provider{Agent: antigravityAgent(goruntime.GOOS), PathEnv: badDir + string(os.PathListSeparator) + goodDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != goodBin || i.Version != "1.5.9" || !i.LoggedIn {
			t.Fatalf("expected later valid agy on PATH: %+v", i)
		}
	})

	t.Run("incompatible_agy_without_fallback_reports_not_found", func(t *testing.T) {
		_, _, _ = isolateAntigravityEnv(t)
		badDir := t.TempDir()
		installFakeBinary(t, badDir, agyExeName(), "legacy-error", "1.3.0")

		a := antigravityAgent(goruntime.GOOS)
		a.DesktopApps = nil
		p := &Provider{Agent: a, PathEnv: badDir}
		i := p.Discover(context.Background())
		if i.Available || i.Problem != runtime.ProblemNotFound || !strings.Contains(i.Detail, "agy") || !strings.Contains(i.Detail, standaloneExeName()) {
			t.Fatalf("expected not found detail mentioning both entry points: %+v", i)
		}
		var out bytes.Buffer
		if err := p.Login(context.Background(), "oauth-personal", &out); err == nil || !strings.Contains(err.Error(), "agy") || !strings.Contains(err.Error(), standaloneExeName()) {
			t.Fatalf("expected login not-found error mentioning both entry points: %v", err)
		}
	})
}

func TestAntigravityAgyCapabilityProbeTimeout(t *testing.T) {
	_, _, _ = isolateAntigravityEnv(t)
	dir := t.TempDir()
	bin := installFakeBinary(t, dir, agyExeName(), "help-hangs", "1.5.9")
	a := antigravityAgent(goruntime.GOOS)
	a.DesktopApps = nil
	p := &Provider{Agent: a, PathEnv: dir}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got := p.Discover(ctx)
	if got.Available || got.Problem != runtime.ProblemTimedOut || got.Binary != bin || !strings.Contains(got.Detail, "agy acp --help") {
		t.Fatalf("expected an inconclusive agy capability probe: %+v", got)
	}

	setFakeFile(t, dir, "fakeacp.mode", "acp")
	got = p.Discover(context.Background())
	if !got.Available || got.Binary != bin || got.Version != "1.5.9" {
		t.Fatalf("expected agy to recover without replacing the executable: %+v", got)
	}
}

func TestAntigravityBinaryOverride(t *testing.T) {
	for _, tc := range []struct {
		bin  string
		want bool
	}{
		{"agy", true},
		{"/usr/local/bin/agy", true},
		{`C:\Users\me\AppData\Local\agy\bin\agy.exe`, true},
		{`C:\bin\AGY.CMD`, true},
		{"agy_acp_server.par", false},
		{`C:\tools\agy_acp_server.exe`, false},
		{"fakeacp", false},
	} {
		if got := isAgyBinary(tc.bin); got != tc.want {
			t.Fatalf("isAgyBinary(%q) = %v, want %v", tc.bin, got, tc.want)
		}
	}

	_, _, _ = isolateAntigravityEnv(t)
	agyBin := installFakeBinary(t, t.TempDir(), agyExeName(), "acp", "1.5.9")
	p := &Provider{Agent: antigravityAgent("linux"), Binary: agyBin}
	cmd := p.resolve(context.Background())
	if cmd.bin != agyBin || !slices.Equal(cmd.args, []string{"acp"}) || cmd.mode != antigravityCLI {
		t.Fatalf("agy override resolve: %+v", cmd)
	}
	if i := p.Discover(context.Background()); !i.Available || !i.LoggedIn || i.Version != "1.5.9" {
		t.Fatalf("agy override discover: %+v", i)
	}

	standalone := installFakeBinary(t, t.TempDir(), standaloneExeName(), "", "1.4.5")
	pStandalone := &Provider{Agent: antigravityAgent("linux"), Binary: standalone}
	cmd = pStandalone.resolve(context.Background())
	if cmd.bin != standalone || !slices.Equal(cmd.args, []string{"--uid="}) || cmd.mode != antigravityStandalone {
		t.Fatalf("standalone override resolve: %+v", cmd)
	}
}

func TestAntigravityPermissionModeAndResume(t *testing.T) {
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	t.Setenv("FAKE_ACP_REQUIRE_MODE", "1")
	t.Setenv("FAKE_ACP_RESUME", "resume")
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	dir := t.TempDir()
	evs, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: dir}, "write hello.txt yes")
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, ev := range evs {
		if ev.Kind == runtime.EventNativeSession {
			ref = ev.NativeID
		}
	}
	evs2, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: dir, ResumeCursor: ref}, "history")
	if err != nil {
		t.Fatal(err)
	}
	var msg string
	for _, ev := range evs2 {
		if ev.Kind == runtime.EventAssistantMessage {
			msg = ev.Text
		}
	}
	if msg != "history: 2 turns" {
		t.Fatalf("got %q, evs=%+v", msg, evs2)
	}
}

func TestAntigravityToolFields(t *testing.T) {
	for _, input := range []string{`{"CommandLine":"printf acp-shell-ok"}`, `{"command_line":"printf acp-shell-ok"}`} {
		cmd, _ := extractACPRawInput(json.RawMessage(input))
		if cmd != "printf acp-shell-ok" {
			t.Fatalf("cmd = %q", cmd)
		}
	}
	if got := extractACPRawOutput(json.RawMessage(`{"combinedOutput":"acp-shell-ok","exitCode":0}`)); got != "acp-shell-ok" {
		t.Fatal(got)
	}
}

func TestACPLogin(t *testing.T) {
	t.Setenv("FAKE_ACP_AUTH", "oauth-personal")
	state := t.TempDir()
	t.Setenv("FAKE_ACP_STATE", state)
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	var out bytes.Buffer
	if err := p.Login(context.Background(), "oauth-personal", &out); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(state)
	if len(files) != 0 {
		t.Fatal("login must not create a model session")
	}
	if err := p.Login(context.Background(), "not-offered", &out); err == nil || !strings.Contains(err.Error(), "oauth-personal") {
		t.Fatalf("method validation: %v", err)
	}
	t.Setenv("FAKE_ACP_AUTH_WAIT", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out.Reset()
	if err := p.Login(ctx, "oauth-personal", &out); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	if !strings.Contains(out.String(), "https://example.test/login") {
		t.Fatalf("login link was not forwarded: %s", out.String())
	}
}

func TestAntigravityAuthHint(t *testing.T) {
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	t.Setenv("FAKE_ACP_AUTH", "oauth-personal")
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	if _, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir()}, "say never"); err == nil || !strings.Contains(err.Error(), "hermesh agent login antigravity") {
		t.Fatalf("auth hint: %v", err)
	}
}
