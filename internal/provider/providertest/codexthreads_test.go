package providertest

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/codex"
)

// threadName reads a thread's name from Codex's own app server, as the
// Codex apps do.
func threadName(t *testing.T, bin, ref string) string {
	t.Helper()
	cmd := exec.Command(bin, "app-server")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer in.Close()
	for i, m := range []string{"initialize", "thread/read"} {
		params := map[string]any{"clientInfo": map[string]any{"name": "test", "version": "0"}}
		if m == "thread/read" {
			params = map[string]any{"threadId": ref}
		}
		b, _ := json.Marshal(map[string]any{"id": i, "method": m, "params": params})
		in.Write(append(b, '\n'))
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var r struct {
			ID     *int
			Result struct{ Thread struct{ Name *string } }
			Error  *struct{ Message string }
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == nil || *r.ID != 1 {
			continue
		}
		if r.Error != nil {
			t.Fatalf("thread/read: %s", r.Error.Message)
		}
		if r.Result.Thread.Name == nil {
			return ""
		}
		return *r.Result.Thread.Name
	}
	t.Fatal("codex app-server ended before answering thread/read")
	return ""
}

// TestCodexThreadTitleAndArchive: the real Codex names a session's thread
// after its title and renames it while it runs; the thread archives and
// unarchives (twice is no error), and an archived thread still resumes
// with its conversation.
func TestCodexThreadTitleAndArchive(t *testing.T) {
	if os.Getenv("HERMESH_REAL_CLI") != "1" {
		t.Skip("real Codex: HERMESH_REAL_CLI=1")
	}
	p := &codex.Provider{PathEnv: os.Getenv("PATH")}
	info := p.Discover(context.Background())
	if !info.Available || !info.LoggedIn {
		t.Skipf("codex not available: %+v", info)
	}
	ctx := context.Background()
	dir := gitRepo(t)
	allow := func(context.Context, provider.ToolAction) provider.Decision { return provider.Decision{Allow: true} }
	s, err := p.Start(ctx, provider.StartSpec{WorkDir: dir, TempDir: t.TempDir(), Env: os.Environ(), Gate: allow,
		Title: "PR 1251 review: Feishu QR sign-in", Prompt: "Remember the word kumquat. Reply with just: ok"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := collect(t, s, 3*time.Minute)
	if tr.result.IsError || tr.ref == "" {
		t.Fatalf("turn: %+v", tr.result)
	}
	if got := threadName(t, info.Binary, tr.ref); got != "PR 1251 review: Feishu QR sign-in" {
		t.Fatalf("thread named %q", got)
	}
	if err := s.(provider.Titler).SetTitle(ctx, "Feishu QR review"); err != nil {
		t.Fatal(err)
	}
	if got := threadName(t, info.Binary, tr.ref); got != "Feishu QR review" {
		t.Fatalf("renamed thread named %q", got)
	}
	s.Close()
	<-s.Done()

	for _, archived := range []bool{true, true, false, false, true} {
		if err := p.Archive(ctx, tr.ref, archived); err != nil {
			t.Fatalf("archive %v: %v", archived, err)
		}
	}
	s, err = p.Start(ctx, provider.StartSpec{WorkDir: dir, TempDir: t.TempDir(), Env: os.Environ(), Gate: allow, ResumeRef: tr.ref,
		Title: "Feishu QR review", Prompt: "Which word did I ask you to remember? Reply with just the word."})
	if err != nil {
		t.Fatalf("resume of an archived thread: %v", err)
	}
	defer s.Close()
	if tr2 := collect(t, s, 3*time.Minute); tr2.result.IsError || tr2.ref != tr.ref || !strings.Contains(strings.ToLower(tr2.result.Text), "kumquat") {
		t.Fatalf("resumed: ref %s, %q", tr2.ref, tr2.result.Text)
	}
}
