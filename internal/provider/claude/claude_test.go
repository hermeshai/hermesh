package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

func fixture(t *testing.T, name string) line {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestResultFailure(t *testing.T) {
	stderr, _ := os.ReadFile("testdata/claude-2.1.292-unknown-model.stderr.txt")
	for _, c := range []struct {
		name   string
		l      line
		stderr string
		want   sessionv1.FailureKind
		msg    string
	}{
		// Real Claude Code 2.1.292 result lines.
		{"resume not found", fixture(t, "claude-2.1.292-resume-not-found.result.json"), "", sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, "No conversation found"},
		{"unknown model", fixture(t, "claude-2.1.292-unknown-model.result.json"), string(stderr), sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE, "issue with the selected model"},
		// Provider result shapes and the CLI's subtypes.
		{"context exhausted reported as success", line{Subtype: "success", TerminalReason: "prompt_too_long", Result: "Prompt is too long"}, "", sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW, "Prompt is too long"},
		{"max turns", line{Subtype: "error_max_turns", IsError: true}, "", sessionv1.FailureKind_FAILURE_KIND_LIMIT_REACHED, "maximum number of turns"},
		{"rate limited", line{IsError: true, APIErrorStatus: 429, Result: "API Error: Request rejected"}, "", sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT, "Request rejected"},
		{"login only on stderr", line{IsError: true, Subtype: "error_during_execution"}, "warn: x\nInvalid API key · Please run /login\n", sessionv1.FailureKind_FAILURE_KIND_AUTH, "Please run /login"},
		{"unrecognized", line{IsError: true, Subtype: "error_during_execution"}, "", sessionv1.FailureKind_FAILURE_KIND_UNKNOWN, "error_during_execution"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := ResultFailure(c.l, c.stderr)
			if f == nil || f.Kind != c.want || !strings.Contains(f.Message, c.msg) {
				t.Fatalf("got %+v, want %v containing %q", f, c.want, c.msg)
			}
		})
	}
	if f := ResultFailure(line{Subtype: "success", Result: "done"}, ""); f != nil {
		t.Fatalf("success: %+v", f)
	}
}

// A refused --resume starts the process normally; the reason only appears
// in the result line, which must reach the kernel as a classified failure.
func TestStartReportsResumeRejection(t *testing.T) {
	scripted(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nread a\nread b\ncat " + filepath.Join(mustAbs(t, "testdata"), "claude-2.1.292-resume-not-found.result.json") +
		"\necho 'No conversation found with session ID: x' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := (&Provider{Binary: bin}).Start(context.Background(), provider.StartSpec{WorkDir: dir,
		Prompt: "hi", ResumeRef: "00000000-0000-4000-8000-000000000000", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed without a turn result")
			}
			if e.Kind != provider.EventTurnCompleted {
				continue
			}
			if !e.IsError || e.Failure == nil || e.Failure.Kind != sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED || !strings.Contains(e.Text, "No conversation found") {
				t.Fatalf("turn: %+v failure %+v", e, e.Failure)
			}
			return
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A message steered after Claude finished the turn's model calls is run as
// one more Claude turn (two results, as Claude Code 2.1.292 does): Hermesh
// reports one turn, ending with the last result.
func TestSteerAfterLastToolIsOneTurn(t *testing.T) {
	scripted(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	replay := func(text string) string {
		return `echo '{"type":"user","message":{"role":"user","content":"` + text + `"},"isReplay":true}'` + "\n"
	}
	say := func(text string) string {
		return `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}'` + "\n"
	}
	result := func(text string) string {
		return `echo '{"type":"result","subtype":"success","result":"` + text + `","usage":{"input_tokens":3,"output_tokens":2}}'` + "\n"
	}
	script := "#!/bin/sh\nread init\nread prompt\n" + replay("hi") + say("first") +
		"read steer\n" + result("first") + replay("more") + say("second") + result("second") + "sleep 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := (&Provider{Binary: bin}).Start(context.Background(), provider.StartSpec{WorkDir: dir, Prompt: "hi", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	next := func() provider.Event {
		t.Helper()
		select {
		case e := <-s.Events():
			return e
		case <-time.After(10 * time.Second):
			t.Fatal("timed out")
		}
		return provider.Event{}
	}
	for e := next(); e.Kind != provider.EventAgentMessage; e = next() {
	}
	if err := s.Steer(context.Background(), provider.Input{Text: "more"}); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for {
		e := next()
		if e.Kind == provider.EventAgentMessage {
			msgs = append(msgs, e.Text)
		}
		if e.Kind == provider.EventTurnCompleted {
			if e.Text != "second" || e.IsError || e.Usage.InputTokens != 6 || len(msgs) != 1 {
				t.Fatalf("turn: %+v, messages %q", e, msgs)
			}
			break
		}
	}
	select {
	case e := <-s.Events():
		t.Fatalf("event after the turn: %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
	if err := s.Steer(context.Background(), provider.Input{Text: "late"}); err == nil {
		t.Fatal("steer between turns must fail")
	}
}

// AskUserQuestion arrives as a permission request (with Hermesh as the
// permission host); the answer goes back as the tool's input with
// answers keyed by question text, as Claude Code 2.1.292 takes it.
func TestAskUserQuestion(t *testing.T) {
	scripted(t)
	if args := strings.Join(Args(provider.StartSpec{}), " "); !strings.Contains(args, "--permission-prompt-tool stdio") || strings.Contains(args, "--permission-prompts") {
		t.Fatal(args)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	ask := `{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tu1",` +
		`"input":{"questions":[{"question":"Which color?","header":"Color","multiSelect":false,"options":[{"label":"Red","description":"warm"},{"label":"Blue","description":"cool"}]}]}}}`
	script := "#!/bin/sh\nread init\nread prompt\n" +
		`echo '{"type":"user","message":{"role":"user","content":"hi"},"isReplay":true}'` + "\n" +
		"echo '" + ask + "'\nread resp\n" +
		`case "$resp" in *'"behavior":"allow"'*'"answers":{"Which color?":"Blue, deep"}'*) r=ok;; *) r=bad;; esac` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"'$r'"}'` + "\nsleep 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := (&Provider{Binary: bin}).Start(context.Background(), provider.StartSpec{WorkDir: dir, Prompt: "hi", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		select {
		case e := <-s.Events():
			switch e.Kind {
			case provider.EventQuestion:
				q := e.Question
				if len(q.Items) != 1 || q.Items[0].Header != "Color" || q.Items[0].Text != "Which color?" || len(q.Items[0].Options) != 2 || !q.Items[0].AllowOther {
					t.Fatalf("question: %+v", q)
				}
				if err := s.Answer(context.Background(), q.ID, provider.Reply{Answers: []provider.Answer{{ItemID: "1", Selected: []string{"Blue"}, Text: "deep"}}}); err != nil {
					t.Fatal(err)
				}
			case provider.EventTurnCompleted:
				if e.Text != "ok" {
					t.Fatalf("the CLI got the wrong answer (%s)", e.Text)
				}
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out")
		}
	}
}

// Without a CLI install, the newest Claude Code the desktop app keeps is used.
func TestDesktopCLI(t *testing.T) {
	scripted(t)
	home := t.TempDir()
	base := filepath.Join(home, "Library/Application Support/Claude/claude-code")
	mk := func(version, build string) string {
		dir := filepath.Join(base, version, build, "claude.app/Contents/MacOS")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "claude")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '"+version+" (Claude Code)'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	mk("2.1.99", "aaa")
	newest := mk("2.1.299", "bbb")
	os.MkdirAll(filepath.Join(base, "2.1.300", "ccc"), 0o755) // a download not finished: no binary yet

	if got := desktopCLI(home); got != newest {
		t.Fatalf("got %q, want %q", got, newest)
	}
	// The binary is the Claude Code it says (checked as a found one is).
	if info := (&Provider{Binary: newest}).Discover(context.Background()); !info.Available || info.Version != "2.1.299" {
		t.Fatalf("got %+v", info)
	}
}

// With --include-partial-messages the reply streams as drafts (stream_event
// text deltas, as Claude Code sends them) that add up to the assistant
// message; a subagent's deltas (parent_tool_use_id) are not the reply.
func TestDraftsAddUpToMessage(t *testing.T) {
	scripted(t)
	if !strings.Contains(strings.Join(Args(provider.StartSpec{}), " "), "--include-partial-messages") {
		t.Fatal("drafts need --include-partial-messages")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	delta := func(text, parent string) string {
		return `echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}},"parent_tool_use_id":` + parent + `}'` + "\n"
	}
	script := "#!/bin/sh\nread init\nread prompt\n" +
		`echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}},"parent_tool_use_id":null}'` + "\n" +
		delta("Hel", "null") + delta("sub", `"toolu_1"`) + delta("lo", "null") +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"Hello","usage":{"input_tokens":3,"output_tokens":2}}'` + "\nsleep 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := (&Provider{Binary: bin}).Start(context.Background(), provider.StartSpec{WorkDir: dir, Prompt: "hi", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var drafts, msg string
	for msg == "" {
		select {
		case e := <-s.Events():
			switch e.Kind {
			case provider.EventAgentDraft:
				drafts += e.Text
			case provider.EventAgentMessage:
				msg = e.Text
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out")
		}
	}
	if drafts != "Hello" || msg != "Hello" {
		t.Fatalf("drafts %q, message %q", drafts, msg)
	}
}

// The model and effort a session runs with go on the command line, for it
// alone; unset, Claude Code's own settings apply.
func TestModelArgs(t *testing.T) {
	args := strings.Join(Args(provider.StartSpec{Model: "opus", Effort: "low"}), " ")
	if !strings.Contains(args, "--model opus") || !strings.Contains(args, "--effort low") {
		t.Fatal(args)
	}
	if args := strings.Join(Args(provider.StartSpec{}), " "); !strings.Contains(args, `--settings {"crossSessionInbound":"refuse"}`) {
		t.Fatalf("Claude Code's own inbox must stay shut: %s", args)
	}
	if args := strings.Join(Args(provider.StartSpec{}), " "); strings.Contains(args, "--model") || strings.Contains(args, "--effort") {
		t.Fatal(args)
	}
}

// scripted skips a test whose fake Claude Code is a shell script.
func scripted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a shell script")
	}
}
