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
		cmd, _, _ := extractACPRawInput(json.RawMessage(input))
		if cmd != "printf acp-shell-ok" {
			t.Fatalf("cmd = %q", cmd)
		}
	}
	cmd, _, summary := extractACPRawInput(json.RawMessage(`{"CommandLine":"go test ./...","toolSummary":"Unit test execution"}`))
	if cmd != "go test ./..." || summary != "Unit test execution" {
		t.Fatalf("cmd=%q summary=%q", cmd, summary)
	}
	if got := extractACPRawOutput(json.RawMessage(`{"combinedOutput":"acp-shell-ok","exitCode":0}`)); got != "acp-shell-ok" {
		t.Fatal(got)
	}
	for _, title := range []string{"ask_question", "Run ask_question", "AskUserQuestion"} {
		if ev, _, isMeta := parseACPProgressTool(title, "", nil, "completed"); !isMeta || ev != nil {
			t.Fatalf("parseACPProgressTool(%q, completed) = (%+v, %v), want suppressed meta tool", title, ev, isMeta)
		}
		if _, _, isMeta := parseACPProgressTool(title, "", nil, "failed"); isMeta {
			t.Fatalf("parseACPProgressTool(%q, failed) should remain visible on failure", title)
		}
	}
}

func TestAntigravityStepsFocusAndToolSummary(t *testing.T) {
	t.Setenv("FAKE_ACP_STATE", t.TempDir())
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	prompt := strings.Join([]string{
		"focus Inspecting auth flow",
		"steps Check existing motion=in_progress|Rewrite CSS=pending|Run benchmarks=skipped",
		"run-summary Benchmark CALayer orbit|printf bench-ok",
		"steps Check existing motion=done|Rewrite CSS=done|Run benchmarks=skipped",
		"say All done.",
	}, "\n")
	evs, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir()}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	var focuses []string
	var todoEvents []runtime.Event
	var toolStarts []runtime.Event
	for _, ev := range evs {
		switch ev.Kind {
		case runtime.EventFocusUpdated:
			focuses = append(focuses, ev.Text)
		case runtime.EventTodoUpdated:
			todoEvents = append(todoEvents, ev)
		case runtime.EventToolStarted:
			if strings.Contains(ev.ToolName, "update_steps") || strings.Contains(ev.ToolName, "update_focus") {
				t.Fatalf("meta progress tool leaked as EventToolStarted: %+v", ev)
			}
			toolStarts = append(toolStarts, ev)
		case runtime.EventToolCompleted:
			if strings.Contains(ev.ToolName, "update_steps") || strings.Contains(ev.ToolName, "update_focus") {
				t.Fatalf("meta progress tool leaked as EventToolCompleted: %+v", ev)
			}
		}
	}
	if !slices.Equal(focuses, []string{"Inspecting auth flow"}) {
		t.Fatalf("focuses = %v", focuses)
	}
	if len(todoEvents) != 2 {
		t.Fatalf("expected 2 deduplicated EventTodoUpdated, got %d: %+v", len(todoEvents), todoEvents)
	}
	if todoEvents[0].ItemID != "todo" || todoEvents[0].HasFocus || len(todoEvents[0].TodoSteps) != 3 ||
		todoEvents[0].TodoSteps[0].Status != "in_progress" || todoEvents[0].TodoSteps[2].Status != "skipped" {
		t.Fatalf("unexpected first todo event: %+v", todoEvents[0])
	}
	if todoEvents[1].TodoSteps[0].Status != "completed" || todoEvents[1].TodoSteps[1].Status != "completed" {
		t.Fatalf("expected done normalized to completed, got %+v", todoEvents[1].TodoSteps)
	}
	if len(toolStarts) != 1 || toolStarts[0].Text != "Benchmark CALayer orbit" || toolStarts[0].Command != "printf bench-ok" {
		t.Fatalf("expected run-summary tool start with Text and Command, got %+v", toolStarts)
	}

	// Verify bare, MCP-namespaced, and shell-kind collision cases directly:
	if ev, _, isMeta := parseACPProgressTool("mcp__ccd_turn__update_plan", "other", json.RawMessage(`{"items":[{"content":"Audit ACP","status":"in_progress"}]}`), "completed"); !isMeta || ev == nil || len(ev.TodoSteps) != 1 || ev.TodoSteps[0].Text != "Audit ACP" {
		t.Fatalf("expected MCP update_plan parsed as TodoUpdated, got isMeta=%v ev=%+v", isMeta, ev)
	}
	if _, _, isMeta := parseACPProgressTool("Run update_steps", "execute", json.RawMessage(`{"command":"echo hi"}`), "completed"); isMeta {
		t.Fatal("shell execute tool must never be misclassified as meta progress tool")
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
	callsFile := filepath.Join(t.TempDir(), "new.calls")
	t.Setenv("FAKE_ACP_NEW_CALLS_FILE", callsFile)
	drv := NewDriver("")
	drv.SessionStartRetryDelay = 5 * time.Millisecond
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin, drv: drv}
	if _, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir()}, "say never"); err == nil || !strings.Contains(err.Error(), "hermesh agent login antigravity") {
		t.Fatalf("auth hint: %v", err)
	}
	if calls, _ := os.ReadFile(callsFile); len(calls) != 1 {
		t.Fatalf("auth error must fail immediately without retrying session/new, got %d calls", len(calls))
	}
}

func TestAntigravitySessionNewAndResumeTransientRecovery(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("FAKE_ACP_STATE", stateDir)
	t.Setenv("FAKE_ACP_RESUME", "resume")

	drv := NewDriver("")
	drv.SessionStartRetryDelay = 5 * time.Millisecond

	t.Run("in_proc_session_new_retry", func(t *testing.T) {
		callsFile := filepath.Join(t.TempDir(), "new.calls")
		argsLog := filepath.Join(t.TempDir(), "args.log")
		t.Setenv("FAKE_ACP_NEW_CALLS_FILE", callsFile)
		t.Setenv("FAKE_ACP_NEW_FAIL_FIRST", "1")
		t.Setenv("FAKE_ACP_NEW_CRASH_FIRST", "0")
		t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)

		p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin, drv: drv}
		evs, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir()}, "say recovered in-proc")
		if err != nil {
			t.Fatalf("expected transient -32603 recovery, got %v", err)
		}
		if calls, _ := os.ReadFile(callsFile); len(calls) != 2 {
			t.Fatalf("expected 2 session/new calls, got %d", len(calls))
		}
		if spawns := readArgsLog(t, argsLog); len(spawns) != 1 {
			t.Fatalf("expected in-proc retry without respawning binary, got %d spawns", len(spawns))
		}
		var got string
		for _, ev := range evs {
			if ev.Kind == runtime.EventAssistantMessage {
				got = ev.Text
			}
		}
		if got != "recovered in-proc" {
			t.Fatalf("unexpected reply %q", got)
		}
	})

	t.Run("wedged_and_crashed_proc_respawn_retry", func(t *testing.T) {
		callsFile := filepath.Join(t.TempDir(), "new.calls")
		argsLog := filepath.Join(t.TempDir(), "args.log")
		t.Setenv("FAKE_ACP_NEW_CALLS_FILE", callsFile)
		t.Setenv("FAKE_ACP_NEW_CRASH_FIRST", "1")
		t.Setenv("FAKE_ACP_NEW_FAIL_FIRST", "3")
		t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)

		p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin, drv: drv}
		evs, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir()}, "write note.txt ok")
		if err != nil {
			t.Fatalf("expected respawn recovery after crash + -32603, got %v", err)
		}
		if spawns := readArgsLog(t, argsLog); len(spawns) < 2 {
			t.Fatalf("expected fresh process respawn, got %d spawns", len(spawns))
		}
		var nativeID string
		for _, ev := range evs {
			if ev.Kind == runtime.EventNativeSession {
				nativeID = ev.NativeID
			}
		}
		if nativeID == "" {
			t.Fatal("missing native session id")
		}

		// Transient -32603 on session/resume must recover the existing native session
		// rather than throwing away native conversation state.
		resCalls := filepath.Join(t.TempDir(), "resume.calls")
		t.Setenv("FAKE_ACP_NEW_FAIL_FIRST", "0")
		t.Setenv("FAKE_ACP_NEW_CRASH_FIRST", "0")
		t.Setenv("FAKE_ACP_RESUME_CALLS_FILE", resCalls)
		t.Setenv("FAKE_ACP_RESUME_FAIL_FIRST", "1")

		evs2, err := runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir(), ResumeCursor: nativeID}, "history")
		if err != nil {
			t.Fatalf("expected transient session/resume recovery, got %v", err)
		}
		var hist string
		for _, ev := range evs2 {
			if ev.Kind == runtime.EventAssistantMessage {
				hist = ev.Text
			}
		}
		if hist != "history: 2 turns" {
			t.Fatalf("expected native history preserved, got %q", hist)
		}

		// Missing session (-32002) must still fail immediately without retrying.
		resMissingCalls := filepath.Join(t.TempDir(), "resume_missing.calls")
		t.Setenv("FAKE_ACP_RESUME_CALLS_FILE", resMissingCalls)
		t.Setenv("FAKE_ACP_RESUME_FAIL_FIRST", "0")
		_, err = runRuntimeTurn(t, p, runtime.StartSpec{Workdir: t.TempDir(), ResumeCursor: "fake-does-not-exist"}, "history")
		if err == nil {
			t.Fatal("expected missing session resume to fail")
		}
		if calls, _ := os.ReadFile(resMissingCalls); len(calls) != 1 {
			t.Fatalf("missing session (-32002) must not retry session/resume, got %d calls", len(calls))
		}
	})
}

// Asked (agy 1.5.16 `-p /usage`, recorded), Antigravity says its quota a
// group of models at a time, what is left of each window: it reads as how
// much is used, each group's windows the shortest first, in the order agy
// gives the groups. An answer that is not /usage's, or has no window, says
// nothing.
func TestAgyUsage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "agy-1.5.16-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	want := []runtime.LimitWindow{
		{Minutes: 300, UsedPercent: 3, ResetsAt: at("2026-11-24T22:18:29Z"), Group: "Gemini Models"},
		{Minutes: 10080, UsedPercent: 17, ResetsAt: at("2026-11-30T20:03:19Z"), Group: "Gemini Models"},
		{Minutes: 300, UsedPercent: 0, ResetsAt: at("2026-11-24T22:37:18Z"), Group: "Claude and GPT models"},
		{Minutes: 10080, UsedPercent: 88, ResetsAt: at("2026-11-25T12:22:09Z"), Group: "Claude and GPT models"},
	}
	if got := usageFrom(b); got == nil || got.Plan != "" || !slices.Equal(got.Windows, want) {
		t.Fatalf("limits: %+v", got)
	}
	for _, out := range []string{
		`{"status":"SUCCESS","response":"ok"}`,
		`{"command":{"name":"credits","data":{"groups":[{"name":"x","buckets":[{"window":"5h","remaining_fraction":1,"reset_time":"2026-11-24T22:37:18Z"}]}]}}}`,
		`{"command":{"name":"usage","data":{"groups":[{"name":"x","buckets":[{"window":"monthly","remaining_fraction":1,"reset_time":"2026-11-24T22:37:18Z"}]}]}}}`,
		`not json`,
	} {
		if l := usageFrom([]byte(out)); l != nil {
			t.Fatalf("%s: %+v", out, l)
		}
	}
}

func TestInProgressAsyncLaunchPollsTaskLogImmediately(t *testing.T) {
	tmp := t.TempDir()
	tasksDir := filepath.Join(tmp, ".system_generated", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(tasksDir, "task-42.log")
	if err := os.WriteFile(logFile, []byte("Compiling crate A...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := NewDriver("")
	drv.TaskLogPollInterval = 15 * time.Millisecond
	s := &session{
		drv:           drv,
		events:        make(chan runtime.Event, 32),
		tools:         map[string]*acpToolState{},
		bgTasks:       map[string]*acpBgTask{},
		terminals:     map[string]*managedTerm{},
		agentTerms:    map[string]*acpAgentTerm{},
		sessionCtx:    ctx,
		sessionCancel: cancel,
		turnCtx:       ctx,
		inPrompt:      true,
	}
	s.alive.Store(true)

	logURI := "file://" + filepath.ToSlash(logFile)
	ackText := "Tool is running as a background task with task id: conv-1/task-42\nTask Description: cargo build\nTask logs are available at: " + logURI
	rawUpdate, _ := json.Marshal(map[string]any{
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call_bg_inprog",
			"title":         "run_command",
			"kind":          "execute",
			"status":        "in_progress",
			"rawInput":      map[string]any{"CommandLine": "cargo build", "toolSummary": "Build workspace"},
			"rawOutput":     ackText,
		},
	})
	s.handleSessionUpdate(rawUpdate)

	if err := os.WriteFile(logFile, []byte("Compiling crate A...\nCompiling crate B...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-s.events:
			if ev.Kind == runtime.EventBackgroundTask && strings.Contains(ev.Output, "Compiling crate B...") {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for live task log poll while tool status is in_progress")
		}
	}
}

func TestAntigravityReadLimitsOnDemandAndCooldown(t *testing.T) {
	_, _, _ = isolateAntigravityEnv(t)
	usage, err := os.ReadFile(filepath.Join("testdata", "agy-1.5.16-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	installFakeBinary(t, binDir, agyExeName(), "acp", "agy 1.5.16")
	setFakeFile(t, binDir, "fakeacp.usage", string(usage))
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)
	t.Setenv("FAKE_ACP_STATE", t.TempDir())

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	p := &Provider{
		Agent:   antigravityAgent(goruntime.GOOS),
		PathEnv: binDir,
		now:     func() time.Time { return now },
	}
	countUsageCalls := func() int {
		t.Helper()
		var n int
		for _, args := range readArgsLog(t, argsLog) {
			if len(args) >= 2 && args[0] == "-p" && args[1] == "/usage" {
				n++
			}
		}
		return n
	}

	// Routine Discover must NOT run `agy -p /usage`.
	info := p.Discover(context.Background())
	if !info.Available || !info.LoggedIn || info.Limits != nil {
		t.Fatalf("Discover should not populate Limits: %+v", info)
	}
	if n := countUsageCalls(); n != 0 {
		t.Fatalf("Discover invoked `agy -p /usage` %d times, want 0", n)
	}

	// Explicit ReadLimits runs `agy -p /usage` once and parses all 4 windows.
	lim := p.ReadLimits(context.Background())
	if lim == nil || len(lim.Windows) != 4 {
		t.Fatalf("first ReadLimits: %+v", lim)
	}
	if n := countUsageCalls(); n != 1 {
		t.Fatalf("after first ReadLimits, usage calls = %d, want 1", n)
	}

	// Within the 60s cooldown, ReadLimits does not re-run `agy`.
	now = now.Add(30 * time.Second)
	if again := p.ReadLimits(context.Background()); again != nil {
		t.Fatalf("within cooldown, expected nil, got %+v", again)
	}
	if n := countUsageCalls(); n != 1 {
		t.Fatalf("within cooldown, usage calls = %d, want 1", n)
	}

	// Past the 60s cooldown, ReadLimits runs `agy -p /usage` again.
	now = now.Add(31 * time.Second)
	if fresh := p.ReadLimits(context.Background()); fresh == nil || len(fresh.Windows) != 4 {
		t.Fatalf("after cooldown, ReadLimits = %+v", fresh)
	}
	if n := countUsageCalls(); n != 2 {
		t.Fatalf("after cooldown, usage calls = %d, want 2", n)
	}

	// Non-antigravity and standalone ACP servers never invoke `-p /usage`.
	other := &Provider{Agent: Agent{Name: "gemini", Command: "gemini", Args: []string{"--acp"}}, PathEnv: binDir}
	if l := other.ReadLimits(context.Background()); l != nil {
		t.Fatalf("non-antigravity ReadLimits: %+v", l)
	}
	standalone := installFakeBinary(t, t.TempDir(), standaloneExeName(), "", "1.4.5")
	pStandalone := &Provider{Agent: antigravityAgent(goruntime.GOOS), Binary: standalone}
	if l := pStandalone.ReadLimits(context.Background()); l != nil {
		t.Fatalf("standalone ReadLimits: %+v", l)
	}
}
