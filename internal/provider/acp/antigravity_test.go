package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

func TestAntigravityDiscovery(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		a := antigravityAgent(goos)
		want := "agy_acp_server.par"
		if goos == "windows" {
			want = "agy_acp_server.exe"
		}
		if a.Command != want || a.Paths[0] != ".local/share/antigravity-acp/"+want || (goos == "linux") != slices.Equal(a.Args, []string{"--uid="}) || len(a.Auth) != 0 {
			t.Fatalf("%s: %+v", goos, a)
		}
	}
	dir := t.TempDir()
	t.Setenv("GEMINI_HOME", dir)
	p := &Provider{Agent: antigravityAgent("darwin"), Binary: fakeBin}
	if i := p.Discover(context.Background()); !i.Available || i.LoggedIn {
		t.Fatalf("installed without authentication: %+v", i)
	}
	os.Mkdir(filepath.Join(dir, "antigravity-acp"), 0o700)
	file := filepath.Join(dir, "antigravity-acp", "settings.json")
	for _, c := range []struct {
		config string
		ready  bool
	}{
		{`{}`, false}, {`invalid`, false}, {`{"auth":{"type":""}}`, false},
		{`{"auth":{"type":"oauth-personal"}}`, true}, {`{"auth":{"type":"oauth-business"}}`, true},
	} {
		os.WriteFile(file, []byte(c.config), 0o600)
		if i := p.Discover(context.Background()); !i.Available || i.LoggedIn != c.ready {
			t.Fatalf("%s: %+v", c.config, i)
		}
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
