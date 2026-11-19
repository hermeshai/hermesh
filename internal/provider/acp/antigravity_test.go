package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

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
	if runtime.GOOS == "windows" {
		return "agy.exe"
	}
	return "agy"
}

func standaloneExeName() string {
	if runtime.GOOS == "windows" {
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

	// 1. agy discovered on PATH uses `agy acp` and antigravity-cli/settings.json.
	binDir := t.TempDir()
	agyBin := installFakeBinary(t, binDir, agyExeName(), "acp", "agy 1.5.9")
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKE_ACP_ARGS_LOG", argsLog)
	t.Setenv("FAKE_ACP_STATE", t.TempDir())

	// Standalone config alone must not mark `agy acp` as signed in.
	writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)
	p := &Provider{Agent: antigravityAgent("linux"), PathEnv: binDir}
	i := p.Discover(context.Background())
	if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || i.LoggedIn || i.ModelsState != provider.ModelsAuthRequired {
		t.Fatalf("agy without antigravity-cli settings: %+v", i)
	}

	// Configuring antigravity-cli/settings.json (with UTF-8 BOM) marks it signed in.
	writeAntigravitySettings(t, gemini, antigravityCLIDir, "\xef\xbb\xbf"+`{"auth":{"type":"oauth-personal"}}`)
	i = p.Discover(context.Background())
	if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || !i.LoggedIn {
		t.Fatalf("agy with antigravity-cli settings: %+v", i)
	}

	// Start must run `agy acp` (never `--uid=`, even for linux Agent config).
	os.Remove(argsLog)
	s, err := p.Start(context.Background(), provider.StartSpec{
		WorkDir: t.TempDir(),
		Prompt:  "say hello from agy",
		Env:     os.Environ(),
		Gate:    func(context.Context, provider.ToolAction) provider.Decision { return provider.Decision{Allow: true} },
	})
	if err != nil {
		t.Fatal(err)
	}
	evs, done := collect(t, s)
	s.Close()
	if done.IsError || len(of(evs, provider.EventAgentMessage)) != 1 || of(evs, provider.EventAgentMessage)[0].Text != "hello from agy" {
		t.Fatalf("agy session: done=%+v evs=%+v", done, evs)
	}
	logged := readArgsLog(t, argsLog)
	if !slices.ContainsFunc(logged, func(a []string) bool { return slices.Equal(a, []string{"acp"}) }) {
		t.Fatalf("expected [acp] invocation in %v", logged)
	}

	// Login must also launch `agy acp`.
	t.Setenv("FAKE_ACP_AUTH", "oauth-personal")
	var out bytes.Buffer
	if err := p.Login(context.Background(), "oauth-personal", &out); err != nil {
		t.Fatalf("agy login: %v", err)
	}
	t.Setenv("FAKE_ACP_AUTH", "")

	// 2. Discovery via ~/.local/bin/agy when PATH is empty.
	localBin := installFakeBinary(t, filepath.Join(home, ".local", "bin"), agyExeName(), "acp", "1.5.9")
	pLocal := &Provider{Agent: antigravityAgent(runtime.GOOS), PathEnv: t.TempDir()}
	if got := pLocal.Discover(context.Background()); !got.Available || got.Binary != localBin || !got.LoggedIn {
		t.Fatalf("~/.local/bin discovery: %+v", got)
	}
	os.Remove(localBin)

	// 3. Discovery via %LOCALAPPDATA%\agy\bin\agy.exe.
	winBin := installFakeBinary(t, filepath.Join(localAppData, "agy", "bin"), "agy.exe", "acp", "1.5.9")
	pWin := &Provider{Agent: antigravityAgent(runtime.GOOS), PathEnv: t.TempDir()}
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

	// antigravity-cli settings alone must not mark standalone server as signed in.
	writeAntigravitySettings(t, gemini, antigravityCLIDir, `{"auth":{"type":"oauth-personal"}}`)
	p := &Provider{Agent: antigravityAgent(runtime.GOOS), PathEnv: t.TempDir()}
	i := p.Discover(context.Background())
	if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || i.LoggedIn || i.ModelsState != provider.ModelsAuthRequired {
		t.Fatalf("standalone without antigravity-acp settings: %+v", i)
	}

	writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)
	i = p.Discover(context.Background())
	if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || !i.LoggedIn {
		t.Fatalf("standalone with antigravity-acp settings: %+v", i)
	}

	// Verify standalone launch arguments for both darwin ([]) and linux ([--uid=]).
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
		s, err := lp.Start(context.Background(), provider.StartSpec{
			WorkDir: t.TempDir(),
			Prompt:  "say ok",
			Env:     os.Environ(),
			Gate:    func(context.Context, provider.ToolAction) provider.Decision { return provider.Decision{Allow: true} },
		})
		if err != nil {
			t.Fatalf("%s start: %v", tc.goos, err)
		}
		_, done := collect(t, s)
		s.Close()
		if done.IsError {
			t.Fatalf("%s done: %+v", tc.goos, done)
		}
		logged := readArgsLog(t, argsLog)
		if len(logged) == 0 || !slices.Equal(logged[len(logged)-1], tc.want) {
			t.Fatalf("%s standalone args = %v, want %v", tc.goos, logged, tc.want)
		}
	}
}

func TestAntigravityPrecedenceAndIncompatibleAgy(t *testing.T) {
	t.Run("prefers_agy_over_standalone", func(t *testing.T) {
		home, gemini, _ := isolateAntigravityEnv(t)
		agyDir := t.TempDir()
		agyBin := installFakeBinary(t, agyDir, agyExeName(), "acp", "1.5.9")
		installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
		writeAntigravitySettings(t, gemini, antigravityCLIDir, `{"auth":{"type":"oauth-personal"}}`)

		p := &Provider{Agent: antigravityAgent(runtime.GOOS), PathEnv: agyDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != agyBin || i.Version != "1.5.9" || !i.LoggedIn {
			t.Fatalf("expected agy to win: %+v", i)
		}
	})

	for _, mode := range []string{"legacy-error", "ide-launcher"} {
		t.Run("incompatible_agy_"+mode+"_falls_back_to_standalone", func(t *testing.T) {
			home, gemini, _ := isolateAntigravityEnv(t)
			badDir := t.TempDir()
			installFakeBinary(t, badDir, agyExeName(), mode, "1.3.0")
			standalone := installFakeBinary(t, filepath.Join(home, ".local", "share", "antigravity-acp"), standaloneExeName(), "", "1.4.5")
			writeAntigravitySettings(t, gemini, antigravityACPDir, `{"auth":{"type":"oauth-personal"}}`)

			p := &Provider{Agent: antigravityAgent(runtime.GOOS), PathEnv: badDir}
			i := p.Discover(context.Background())
			if !i.Available || i.Binary != standalone || i.Version != "1.4.5" || !i.LoggedIn {
				t.Fatalf("expected fallback to standalone: %+v", i)
			}
		})
	}

	t.Run("skips_incompatible_agy_for_later_valid_agy_on_path", func(t *testing.T) {
		_, gemini, _ := isolateAntigravityEnv(t)
		badDir := t.TempDir()
		goodDir := t.TempDir()
		installFakeBinary(t, badDir, agyExeName(), "ide-launcher", "1.0.0")
		goodBin := installFakeBinary(t, goodDir, agyExeName(), "acp", "1.5.9")
		writeAntigravitySettings(t, gemini, antigravityCLIDir, `{"auth":{"type":"oauth-personal"}}`)

		p := &Provider{Agent: antigravityAgent(runtime.GOOS), PathEnv: badDir + string(os.PathListSeparator) + goodDir}
		i := p.Discover(context.Background())
		if !i.Available || i.Binary != goodBin || i.Version != "1.5.9" || !i.LoggedIn {
			t.Fatalf("expected later valid agy on PATH: %+v", i)
		}
	})

	t.Run("incompatible_agy_without_fallback_reports_not_found", func(t *testing.T) {
		_, _, _ = isolateAntigravityEnv(t)
		badDir := t.TempDir()
		installFakeBinary(t, badDir, agyExeName(), "legacy-error", "1.3.0")

		a := antigravityAgent(runtime.GOOS)
		a.DesktopApps = nil // isolate from any host Antigravity.app
		p := &Provider{Agent: a, PathEnv: badDir}
		i := p.Discover(context.Background())
		if i.Available || i.Problem != provider.ProblemNotFound || !strings.Contains(i.Detail, "agy") || !strings.Contains(i.Detail, standaloneExeName()) {
			t.Fatalf("expected not found detail mentioning both entry points: %+v", i)
		}
		var out bytes.Buffer
		if err := p.Login(context.Background(), "oauth-personal", &out); err == nil || !strings.Contains(err.Error(), "agy") || !strings.Contains(err.Error(), standaloneExeName()) {
			t.Fatalf("expected login not-found error mentioning both entry points: %v", err)
		}
	})
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

	_, gemini, _ := isolateAntigravityEnv(t)
	agyBin := installFakeBinary(t, t.TempDir(), agyExeName(), "acp", "1.5.9")
	writeAntigravitySettings(t, gemini, antigravityCLIDir, `{"auth":{"type":"oauth-personal"}}`)
	p := &Provider{Agent: antigravityAgent("linux"), Binary: agyBin}
	cmd := p.resolve(context.Background())
	if cmd.bin != agyBin || !slices.Equal(cmd.args, []string{"acp"}) || cmd.configDir != antigravityCLIDir {
		t.Fatalf("agy override resolve: %+v", cmd)
	}
	if i := p.Discover(context.Background()); !i.Available || !i.LoggedIn || i.Version != "1.5.9" {
		t.Fatalf("agy override discover: %+v", i)
	}

	standalone := installFakeBinary(t, t.TempDir(), standaloneExeName(), "", "1.4.5")
	pStandalone := &Provider{Agent: antigravityAgent("linux"), Binary: standalone}
	cmd = pStandalone.resolve(context.Background())
	if cmd.bin != standalone || !slices.Equal(cmd.args, []string{"--uid="}) || cmd.configDir != antigravityACPDir {
		t.Fatalf("standalone override resolve: %+v", cmd)
	}
}

func TestAntigravityPermissionModeAndResume(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_REQUIRE_MODE=1", "FAKE_ACP_RESUME=resume")
	f.p.Agent = antigravityAgent("darwin")
	s, err := f.start("write hello.txt yes", "", "")
	if err != nil {
		t.Fatal(err)
	}
	evs, done := collect(t, s)
	if done.IsError {
		t.Fatal(done)
	}
	ref := of(evs, provider.EventRef)[0].Ref
	s.Close()
	s, err = f.start("history", ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, done := collect(t, s); done.IsError || done.Text != "history: 2 turns" {
		t.Fatal(done)
	}
}

func TestAntigravityToolFields(t *testing.T) {
	for _, input := range []string{`{"CommandLine":"printf acp-shell-ok"}`, `{"command_line":"printf acp-shell-ok"}`} {
		a := Classify(toolCall{Kind: "execute", Title: "Run command?", RawInput: json.RawMessage(input)})
		if a.Kind != provider.ToolShell || a.Command != "printf acp-shell-ok" {
			t.Fatalf("%+v", a)
		}
	}
	if got := rawText(json.RawMessage(`{"combinedOutput":"acp-shell-ok","exitCode":0}`)); got != "acp-shell-ok" {
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
	f := newFixture(t, "FAKE_ACP_AUTH=oauth-personal")
	f.p.Agent = antigravityAgent("darwin")
	if _, err := f.start("say never", "", ""); err == nil || !strings.Contains(err.Error(), "hermesh agent login antigravity") {
		t.Fatalf("auth hint: %v", err)
	}
}
