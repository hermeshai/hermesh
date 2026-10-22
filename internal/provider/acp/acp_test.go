package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeacp")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakeacp")
	if out, err := exec.Command("go", "build", "-o", fakeBin, "./fakeacp").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build fakeacp: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type recorder struct {
	mu    sync.Mutex
	calls []provider.ToolAction
	deny  bool
}

func (r *recorder) gate(_ context.Context, a provider.ToolAction) provider.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, a)
	if r.deny {
		return provider.Decision{Reason: "denied by test"}
	}
	return provider.Decision{Allow: true}
}

type fixture struct {
	t     *testing.T
	dir   string
	state string
	env   []string
	gate  *recorder
	p     *Provider
}

func newFixture(t *testing.T, env ...string) *fixture {
	f := &fixture{t: t, dir: t.TempDir(), state: t.TempDir(), gate: &recorder{}}
	f.dir, _ = filepath.EvalSymlinks(f.dir)
	f.env = append(append(os.Environ(), "FAKE_ACP_STATE="+f.state), env...)
	f.p = &Provider{Agent: Agent{Name: "fake", Command: "fakeacp", Args: []string{"acp"}}, Binary: fakeBin}
	return f
}

func (f *fixture) start(prompt, resume, model string) (provider.Session, error) {
	return f.p.Start(context.Background(), provider.StartSpec{WorkDir: f.dir, TempDir: f.t.TempDir(),
		Prompt: prompt, ResumeRef: resume, Model: model, Env: f.env, Gate: f.gate.gate})
}

func collect(t *testing.T, s provider.Session) (events []provider.Event, done provider.Event) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatalf("events closed: %v", s.Err())
			}
			events = append(events, e)
			if e.Kind == provider.EventTurnCompleted {
				return events, e
			}
		case <-deadline:
			t.Fatalf("timed out; events so far: %+v", events)
		}
	}
}

func of(events []provider.Event, k provider.EventKind) []provider.Event {
	var out []provider.Event
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func TestTurnsToolsAndLoad(t *testing.T) {
	f := newFixture(t)
	s, err := f.start("say hello world\nwrite sub/a.txt hi\nshell echo from-shell\nread sub/a.txt", "", "")
	if err != nil {
		t.Fatal(err)
	}
	evs, done := collect(t, s)
	refs := of(evs, provider.EventRef)
	if len(refs) != 1 || !strings.HasPrefix(refs[0].Ref, "fake-") {
		t.Fatalf("refs: %+v", refs)
	}
	ref := refs[0].Ref
	if msgs := of(evs, provider.EventAgentMessage); len(msgs) != 1 || msgs[0].Text != "hello world" {
		t.Fatalf("chunks must merge into one message: %+v", msgs)
	}
	calls := of(evs, provider.EventToolCall)
	if len(calls) != 3 || calls[0].ToolKind != provider.ToolEdit || calls[1].ToolKind != provider.ToolShell || calls[2].ToolKind != provider.ToolRead ||
		calls[0].Decision == nil || !calls[0].Decision.Allow || calls[0].Summary != "Write sub/a.txt" {
		t.Fatalf("tool calls: %+v", calls)
	}
	// One gate decision per tool call, made once its input is known
	// (each is announced empty, then filled in, then asks permission).
	g := f.gate.calls
	if len(g) != 3 || g[0].Paths[0] != filepath.Join(f.dir, "sub/a.txt") || g[1].Command != "echo from-shell" || g[2].Paths[0] != filepath.Join(f.dir, "sub/a.txt") {
		t.Fatalf("gate: %+v", g)
	}
	results := of(evs, provider.EventToolResult)
	if len(results) != 3 || results[0].IsError || !strings.Contains(results[1].Summary, "from-shell") || results[2].Summary != "hi\n" {
		t.Fatalf("results: %+v", results)
	}
	if done.IsError || done.Text != "hello world" || done.Usage.InputTokens != 105 || done.Usage.OutputTokens != 20 {
		t.Fatalf("turn: %+v", done)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "sub/a.txt")); string(b) != "hi\n" {
		t.Fatalf("file: %q", b)
	}

	if err := s.Send(context.Background(), provider.Input{Text: "history"}); err != nil {
		t.Fatal(err)
	}
	if _, done := collect(t, s); done.Text != "history: 2 turns" {
		t.Fatalf("turn 2: %+v", done)
	}
	s.Close()
	<-s.Done()

	// A new process loads the session; the replayed history is not
	// reported again.
	s, err = f.start("history", ref, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evs, done = collect(t, s)
	if done.Text != "history: 3 turns" || len(of(evs, provider.EventToolCall)) != 0 || len(of(evs, provider.EventAgentMessage)) != 1 {
		t.Fatalf("after load: %+v", evs)
	}
	if r := of(evs, provider.EventRef); len(r) != 1 || r[0].Ref != ref {
		t.Fatalf("ref after load: %+v", r)
	}
}

func TestResumeModes(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_RESUME=resume")
	s, err := f.start("say one", "", "")
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := collect(t, s)
	ref := of(evs, provider.EventRef)[0].Ref
	s.Close()
	s, err = f.start("history", ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, done := collect(t, s); done.Text != "history: 2 turns" {
		t.Fatalf("resume: %+v", done)
	}
	s.Close()
	if _, err := f.start("history", "fake-missing", ""); err == nil || !strings.Contains(err.Error(), "session/resume") || kind(err) != sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED {
		t.Fatalf("unknown session must be a refused resume, so the kernel starts fresh: %v", err)
	}

	f2 := newFixture(t, "FAKE_ACP_RESUME=none")
	f2.state = f.state
	if _, err := f2.start("history", ref, ""); err == nil || !strings.Contains(err.Error(), "cannot resume") || kind(err) != sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED {
		t.Fatalf("agent without resume: %v", err)
	}
}

func TestGateDenies(t *testing.T) {
	f := newFixture(t)
	f.gate.deny = true
	s, err := f.start("write a.txt nope\nsay after", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evs, done := collect(t, s)
	calls := of(evs, provider.EventToolCall)
	if len(calls) != 1 || calls[0].Decision.Allow || calls[0].Decision.Reason != "denied by test" {
		t.Fatalf("calls: %+v", calls)
	}
	if r := of(evs, provider.EventToolResult); len(r) != 1 || !r[0].IsError {
		t.Fatalf("results: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "a.txt")); err == nil {
		t.Fatal("denied write happened")
	}
	if done.IsError || done.Text != "after" {
		t.Fatalf("turn: %+v", done)
	}
}

// A grant the agent would remember beyond this session is never chosen.
func TestNeverAllowAlways(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_ALLOW_ALWAYS_ONLY=1")
	s, err := f.start("write a.txt x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	collect(t, s)
	if _, err := os.Stat(filepath.Join(f.dir, "a.txt")); err == nil {
		t.Fatal("write went through an allow_always option")
	}
}

func TestAuthAndModel(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_AUTH=cursor_login", "FAKE_ACP_MODELS=m1")
	if _, err := f.start("say x", "", ""); err == nil || !strings.Contains(err.Error(), "Authentication required") || kind(err) != sessionv1.FailureKind_FAILURE_KIND_AUTH {
		t.Fatalf("without authenticate: %v", err)
	}
	f.p.Auth = []string{"cursor_login"}
	s, err := f.start("say x", "", "m1")
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	s.Close()
	if _, err := f.start("say x", "", "m2"); err == nil || !strings.Contains(err.Error(), `model "m2"`) || kind(err) != sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE {
		t.Fatalf("unknown model must fail the start: %v", err)
	}
}

// Agents that list models as a session config option (the current ACP way)
// get session/set_config_option; unknown models fail before the prompt.
func TestModelConfigOption(t *testing.T) {
	f := newFixture(t, "FAKE_ACP_CONFIG=1", "FAKE_ACP_MODELS=small,large")
	s, err := f.start("say x", "", "large")
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	s.Close()
	if _, err := f.start("say x", "", "huge"); err == nil || !strings.Contains(err.Error(), "available: small, large") {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestInterruptAndFailure(t *testing.T) {
	f := newFixture(t)
	s, err := f.start("say waiting\nsleep 30s\nsay never", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(300 * time.Millisecond)
	if err := s.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, done := collect(t, s); !done.IsError || done.Text != "interrupted" {
		t.Fatalf("interrupted turn: %+v", done)
	}
	s.Send(context.Background(), provider.Input{Text: "fail model overloaded"})
	if _, done := collect(t, s); !done.IsError || !strings.Contains(done.Text, "model overloaded") || done.Failure == nil || done.Failure.Kind != sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT {
		t.Fatalf("failed turn: %+v", done)
	}
	s.Send(context.Background(), provider.Input{Text: "say still alive"})
	if _, done := collect(t, s); done.IsError || done.Text != "still alive" {
		t.Fatalf("turn after failure: %+v", done)
	}
}

// A JSON-RPC auth_required error on a prompt is AUTH whatever its text.
func TestPromptAuthRequired(t *testing.T) {
	f := newFixture(t)
	s, err := f.start("fail-code -32000 please sign in again", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, done := collect(t, s); done.Failure == nil || done.Failure.Kind != sessionv1.FailureKind_FAILURE_KIND_AUTH {
		t.Fatalf("turn: %+v", done)
	}
}

func kind(err error) sessionv1.FailureKind {
	return provider.FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_UNKNOWN).Kind
}

func TestDiscover(t *testing.T) {
	info := (&Provider{Agent: Agent{Name: "fake", Command: "fakeacp"}, Binary: fakeBin}).Discover(context.Background())
	if !info.Available || info.Version != "1.2.3" {
		t.Fatalf("info: %+v", info)
	}
	info = (&Provider{Agent: Agent{Name: "nope", Command: "hermesh-no-such-agent"}, PathEnv: t.TempDir()}).Discover(context.Background())
	if info.Available || !strings.Contains(info.Detail, "not found") {
		t.Fatalf("missing agent: %+v", info)
	}
}

// Images go as image blocks only to agents that declare they take them;
// the text lists the files either way.
func TestImagesOnlyWhenTheAgentTakesThem(t *testing.T) {
	img := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o600)
	for _, c := range []struct {
		env  string
		want string
	}{{"FAKE_ACP_IMAGES=1", "images: image/png"}, {"FAKE_ACP_IMAGES=0", "images: "}} {
		f := newFixture(t, c.env)
		s, err := f.p.Start(context.Background(), provider.StartSpec{WorkDir: f.dir, TempDir: t.TempDir(),
			Prompt: "images", Images: []provider.Image{{Path: img, MediaType: "image/png"}}, Env: f.env, Gate: f.gate.gate})
		if err != nil {
			t.Fatal(err)
		}
		if _, done := collect(t, s); done.Text != c.want {
			t.Fatalf("%s: %+v", c.env, done)
		}
		s.Close()
	}
}

// A form elicitation is a question: options for an enum field, free text
// for a string, fields the schema does not require optional; the answer
// goes back in the schema's values.
func TestElicitationForm(t *testing.T) {
	for _, c := range []struct {
		reply provider.Reply
		want  string
	}{
		{provider.Reply{Answers: []provider.Answer{{ItemID: "choice", Selected: []string{"Blue"}}, {ItemID: "note", Text: "fast"}}}, "answer: v-blue fast"},
		// The note is not required: it can be left out.
		{provider.Reply{Answers: []provider.Answer{{ItemID: "choice", Selected: []string{"Red"}}}}, "answer: v-red <nil>"},
		{provider.Reply{Declined: true, Message: "nobody"}, "declined: decline"},
	} {
		f := newFixture(t)
		s, err := f.start("ask Pick a color|Red,Blue", "", "")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.After(30 * time.Second)
		for done := false; !done; {
			select {
			case e := <-s.Events():
				switch e.Kind {
				case provider.EventQuestion:
					q := e.Question
					if len(q.Items) != 2 || q.Items[0].ID != "choice" || len(q.Items[0].Options) != 2 || q.Items[0].Options[1].Label != "Blue" ||
						!strings.HasPrefix(q.Items[0].Text, "Pick a color") || q.Items[1].ID != "note" || !q.Items[1].AllowOther ||
						q.Items[0].Optional || !q.Items[1].Optional {
						t.Fatalf("question: %+v", q)
					}
					if err := s.Answer(context.Background(), q.ID, c.reply); err != nil {
						t.Fatal(err)
					}
				case provider.EventTurnCompleted:
					if e.Text != c.want {
						t.Fatalf("turn: %+v, want %q", e, c.want)
					}
					done = true
				}
			case <-deadline:
				t.Fatal("timed out")
			}
		}
		s.Close()
	}
}

// fakeApp makes a desktop app under $HOME/Applications whose executable,
// run as Node on its CLI script, is fakeacp (anything else fails).
func fakeApp(t *testing.T, a App) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app := filepath.Join(home, "Applications", a.Bundle)
	script := filepath.Join(app, a.Script)
	os.MkdirAll(filepath.Dir(script), 0o755)
	os.WriteFile(script, nil, 0o644)
	exe := filepath.Join(app, a.Exec)
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte(fmt.Sprintf("#!/bin/sh\n[ \"$ELECTRON_RUN_AS_NODE\" = 1 ] && [ \"$1\" = %q ] || exit 3\nshift\nexec %q \"$@\"\n", script, fakeBin)), 0o755)
}

// Without the CLI on PATH, the copy inside the agent's desktop app is used.
func TestDesktopAppCLI(t *testing.T) {
	a := App{Bundle: "Fake Desktop.app", Exec: "Contents/MacOS/Fake Desktop", Script: "Contents/Resources/lib/cli.js"}
	fakeApp(t, a)
	f := newFixture(t)
	f.p = &Provider{Agent: Agent{Name: "fake", Command: "hermesh-no-such-agent", Args: []string{"acp"}, Apps: []App{a}}, PathEnv: t.TempDir()}
	if info := f.p.Discover(context.Background()); !info.Available || info.Version != "1.2.3" {
		t.Fatalf("info: %+v", info)
	}
	s, err := f.start("say hello from the app", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if evs, _ := collect(t, s); len(of(evs, provider.EventAgentMessage)) != 1 || of(evs, provider.EventAgentMessage)[0].Text != "hello from the app" {
		t.Fatalf("events: %+v", evs)
	}
}
