// Package fake is an in-process provider for tests. Prompts are scripts:
// each line is a command:
//
//	say <text>          agent message (\n in text, here and in write and run, is a line break)
//	draft <a>|<b>|...   reply text still being written: pieces a, b, ... 30 ms
//	                    apart (the say that follows is the whole message)
//	warn <text>         an agent error that does not end the turn
//	write <path> <text> edit tool call that writes a file (through the gate)
//	shell <command>     shell tool call (through the gate; not executed)
//	run <command>|<out> shell tool call that printed out (not executed)
//	tokens <in> <out>   the turn's usage (default 10 and 5)
//	sleep <duration>    pause
//	fail <text>         end the turn with an error
//	crash               the process exits mid-turn
//	hang                no output until stopped
//	tool-sleep <d>      a shell tool that runs for d without output
//	output <n>          a shell tool printing n bytes (numbered lines, then END)
//	chatter <d>         output that is not an event (thinking) for d
//	overflow            end the turn with a context-overflow error
//	retry-forever <t>   keep reporting error t without doing anything (as
//	                    Codex does while it waits for the network)
//	refuse-resume       end the first turn of a resumed process with a
//	                    refused-resume error (as Claude Code does), then exit
//	notice              agent message "notice: yes|no" (was the prompt
//	                    prefixed with Hermesh's continuity notice)
//	resumed             agent message "resumed: <ref>" ("" when fresh)
//	env <name>          agent message "<name>=<value>" from the agent's
//	                    environment ("" when unset)
//	images              agent message "images: <file names>" (the images
//	                    this turn got, comma-separated)
//	ask <h>|<q>|<a,b>   ask question q (header h, options a and b) and wait;
//	                    then agent message "answer: <value>" or
//	                    "declined: <message>"
//	ask-secret <h>|<q>  the same, a secret free-text question
//	message <to>|<text> call the session's message_send tool (Hermesh's MCP
//	                    server) to write text to agent to ("" for the
//	                    session's creator); its result is the turn's
//
// Steer appends a script to the running turn, run before it completes.
//
// HERMESH_FAKE_SCRIPTS names a JSON object of prompts and their scripts: a
// prompt ending in one of them runs its script instead, so a demo world's
// conversations read as a person wrote them (e2e TestScreens).
//
// Start itself fails on a resume when the prompt has a line
// "resume-start-reject" (refused).
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hermeshai/hermesh/internal/provider"
)

type Provider struct{}

func (Provider) Name() string { return "fake" }

func (Provider) Discover(context.Context) provider.Info {
	return provider.Info{Name: "fake", Available: true, Version: "1.0.0", LoggedIn: true}
}

func (Provider) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	if spec.ResumeRef != "" {
		for _, line := range strings.Split(spec.Prompt, "\n") {
			if strings.TrimSpace(line) == "resume-start-reject" {
				return nil, provider.Fail(sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, "no rollout found for thread id "+spec.ResumeRef)
			}
		}
	}
	s := &session{spec: spec, events: make(chan provider.Event, 64), done: make(chan struct{}), inbox: make(chan provider.Input, 4), interrupt: make(chan struct{}, 1)}
	ref := spec.ResumeRef
	if ref == "" {
		ref = spec.NewRef
	}
	s.events <- provider.Event{Kind: provider.EventRef, Ref: ref}
	s.inbox <- provider.Input{Text: spec.Prompt, Images: spec.Images}
	go s.run(ctx)
	return s, nil
}

type session struct {
	spec      provider.StartSpec
	events    chan provider.Event
	done      chan struct{}
	inbox     chan provider.Input
	interrupt chan struct{}
	once      sync.Once
	n         int
	turns     int

	mu   sync.Mutex
	last time.Time
	err  error
	// inTurn: a turn is running and takes steered scripts, which run
	// after its own script, before it completes.
	inTurn  bool
	steered []string
	images  []provider.Image // this turn's
	usage   provider.Usage   // this turn's, from tokens
	asked   map[string]chan provider.Reply
}

func (s *session) Events() <-chan provider.Event { return s.events }
func (s *session) Done() <-chan struct{}         { return s.done }
func (s *session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *session) Pgid() int { return 0 }
func (s *session) LastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *session) Send(_ context.Context, in provider.Input) error {
	select {
	case s.inbox <- in:
		return nil
	case <-s.done:
		return errors.New("closed")
	}
}

func (s *session) Steer(_ context.Context, in provider.Input) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inTurn {
		return errors.New("no turn is running")
	}
	s.steered = append(s.steered, in.Text)
	s.images = append(s.images, in.Images...)
	return nil
}

func (s *session) Answer(_ context.Context, id string, r provider.Reply) error {
	s.mu.Lock()
	ch := s.asked[id]
	delete(s.asked, id)
	s.mu.Unlock()
	if ch == nil {
		return errors.New("no such question")
	}
	ch <- r
	return nil
}

func (s *session) Interrupt(context.Context) error {
	select {
	case s.interrupt <- struct{}{}:
	default:
	}
	return nil
}

func (s *session) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *session) run(ctx context.Context) {
	defer close(s.events)
	for {
		select {
		case <-s.done:
			return
		case in := <-s.inbox:
			s.turn(ctx, in)
		}
	}
}

func (s *session) emit(e provider.Event) bool {
	if e.Kind != provider.EventError { // a retry is not progress
		s.touch()
	}
	select {
	case s.events <- e:
		return true
	case <-s.done:
		return false
	}
}

func (s *session) touch() {
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
}

// exit ends the process: no more events, Done closed.
func (s *session) exit(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.Close()
}

func (s *session) turn(ctx context.Context, in provider.Input) {
	s.turns++
	prompt := in.Text
	s.mu.Lock()
	s.inTurn, s.images, s.usage = true, in.Images, provider.Usage{InputTokens: 10, OutputTokens: 5}
	s.mu.Unlock()
	if b, err := os.ReadFile(os.Getenv("HERMESH_FAKE_SCRIPTS")); err == nil {
		var scripts map[string]string
		json.Unmarshal(b, &scripts)
		for p, script := range scripts {
			if strings.HasSuffix(strings.TrimSpace(prompt), p) {
				prompt = script
			}
		}
	}
	defer func() {
		s.mu.Lock()
		s.inTurn, s.steered = false, nil
		s.mu.Unlock()
	}()
	var last string
	for {
		l, ended := s.script(ctx, prompt)
		if ended {
			return
		}
		if l != "" {
			last = l
		}
		s.mu.Lock()
		if len(s.steered) == 0 {
			s.inTurn = false
			s.mu.Unlock()
			break
		}
		prompt = strings.Join(s.steered, "\n")
		s.steered = nil
		s.mu.Unlock()
	}
	s.mu.Lock()
	usage := s.usage
	s.mu.Unlock()
	s.emit(provider.Event{Kind: provider.EventTurnCompleted, Text: last, Usage: usage})
}

// script runs one prompt's commands; ended: the turn already ended (an
// error, a crash, the process stopping).
func (s *session) script(ctx context.Context, prompt string) (last string, ended bool) {
	for _, line := range strings.Split(prompt, "\n") {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		s.n++
		id := "call-" + string(rune('a'+s.n%26))
		switch cmd {
		case "Reply": // Test's fixed prompt (machine.TestAgent): answered as a model would
			last = "ready"
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: last})
		case "say":
			arg = strings.ReplaceAll(arg, `\n`, "\n")
			last = arg
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: arg})
		case "draft":
			for _, piece := range strings.Split(arg, "|") {
				s.emit(provider.Event{Kind: provider.EventAgentDraft, Text: piece})
				select {
				case <-time.After(30 * time.Millisecond):
				case <-s.done:
					return "", true
				}
			}
		case "warn":
			s.emit(provider.Event{Kind: provider.EventError, Text: arg})
		case "message": // message <to>|<text>: through the session's message_send tool
			to, text, _ := strings.Cut(arg, "|")
			s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: provider.ToolOther, ToolName: "message_send", Summary: to})
			res, isErr := s.callTool(ctx, "message_send", map[string]any{"to": to, "message": text})
			last = res
			s.emit(provider.Event{Kind: provider.EventToolResult, CallID: id, Summary: res, IsError: isErr})
		case "write":
			path, text, _ := strings.Cut(arg, " ")
			text = strings.ReplaceAll(text, `\n`, "\n")
			a := provider.ToolAction{CallID: id, Kind: provider.ToolEdit, Name: "Write", Paths: []string{path}, Summary: path}
			d := s.decide(ctx, a)
			s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: a.Kind, ToolName: a.Name, Summary: a.Summary, Decision: &d})
			res := "ok"
			if d.Allow {
				p := path
				if !filepath.IsAbs(p) {
					p = filepath.Join(s.spec.WorkDir, p)
				}
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(text+"\n"), 0o644); err != nil {
					res = err.Error()
				}
			} else {
				res = "denied: " + d.Reason
			}
			s.emit(provider.Event{Kind: provider.EventToolResult, CallID: id, Summary: res, IsError: !d.Allow})
		case "shell":
			a := provider.ToolAction{CallID: id, Kind: provider.ToolShell, Name: "Bash", Command: arg, Summary: arg}
			d := s.decide(ctx, a)
			s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: a.Kind, ToolName: a.Name, Summary: a.Summary, Decision: &d})
			s.emit(provider.Event{Kind: provider.EventToolResult, CallID: id, Summary: "simulated", IsError: !d.Allow})
		case "run":
			command, out, _ := strings.Cut(arg, "|")
			s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: provider.ToolShell, ToolName: "Bash", Summary: command, Decision: &provider.Decision{Allow: true}})
			s.emit(provider.Event{Kind: provider.EventToolResult, CallID: id, Summary: strings.ReplaceAll(out, `\n`, "\n")})
		case "tokens":
			in, out, _ := strings.Cut(arg, " ")
			i, _ := strconv.ParseUint(in, 10, 64)
			o, _ := strconv.ParseUint(out, 10, 64)
			s.mu.Lock()
			s.usage = provider.Usage{InputTokens: i, OutputTokens: o}
			s.mu.Unlock()
		case "sleep":
			d, _ := time.ParseDuration(arg)
			select {
			case <-time.After(d):
			case <-s.interrupt:
				s.emit(provider.Event{Kind: provider.EventTurnCompleted, Text: "interrupted", IsError: true})
				return "", true
			case <-s.done:
				return "", true
			}
		case "fail":
			s.emit(provider.Event{Kind: provider.EventTurnCompleted, Text: arg, IsError: true})
			return "", true
		case "crash":
			s.exit(errors.New("exit status 1: fake crash"))
			return "", true
		case "hang":
			<-s.done
			return "", true
		case "output": // a shell command printing arg bytes, numbered lines ending "END"
			n, _ := strconv.Atoi(arg)
			var b strings.Builder
			for i := 1; b.Len() < n-4; i++ {
				fmt.Fprintf(&b, "line %07d of the output\n", i)
			}
			out := b.String()[:max(n-4, 0)] + "END\n"
			s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: provider.ToolShell, ToolName: "Bash", Summary: "print " + arg, Decision: &provider.Decision{Allow: true}})
			kept, cut := provider.KeepOutput(out)
			s.emit(provider.Event{Kind: provider.EventToolResult, CallID: id, Summary: kept, Truncated: cut})
		case "tool-sleep":
			d, _ := time.ParseDuration(arg)
			s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: provider.ToolShell, ToolName: "Bash", Summary: "sleep " + arg, Decision: &provider.Decision{Allow: true}})
			select {
			case <-time.After(d):
			case <-s.done:
				return "", true
			}
			s.emit(provider.Event{Kind: provider.EventToolResult, CallID: id, Summary: "slept"})
		case "chatter":
			d, _ := time.ParseDuration(arg)
			for end := time.Now().Add(d); time.Now().Before(end); {
				s.touch()
				select {
				case <-time.After(20 * time.Millisecond):
				case <-s.done:
					return "", true
				}
			}
		case "retry-forever":
			for {
				s.emit(provider.Event{Kind: provider.EventError, Text: arg, Failure: &provider.Failure{Kind: provider.Classify(arg), Message: arg}})
				select {
				case <-time.After(50 * time.Millisecond):
				case <-s.done:
					return "", true
				}
			}
		case "overflow":
			s.emit(provider.Event{Kind: provider.EventTurnCompleted, Text: "Prompt is too long", IsError: true,
				Failure: &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW, Message: "Prompt is too long"}})
			return "", true
		case "refuse-resume":
			if s.spec.ResumeRef != "" && s.turns == 1 {
				msg := "No conversation found with session ID: " + s.spec.ResumeRef
				s.emit(provider.Event{Kind: provider.EventTurnCompleted, Text: msg, IsError: true,
					Failure: &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, Message: msg}})
				s.exit(errors.New("exit status 1"))
				return "", true
			}
		case "notice":
			last = "notice: no"
			if strings.Contains(prompt, "[Hermesh] The earlier conversation") {
				last = "notice: yes"
			}
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: last})
		case "ask", "ask-secret":
			parts := strings.SplitN(arg, "|", 3)
			for len(parts) < 3 {
				parts = append(parts, "")
			}
			item := provider.QuestionItem{ID: "q1", Header: parts[0], Text: parts[1], AllowOther: true, Secret: cmd == "ask-secret"}
			for _, o := range strings.Split(parts[2], ",") {
				if o != "" {
					item.Options = append(item.Options, provider.Option{Label: o})
				}
			}
			ch := make(chan provider.Reply, 1)
			s.mu.Lock()
			if s.asked == nil {
				s.asked = map[string]chan provider.Reply{}
			}
			qid := "fq-" + id
			s.asked[qid] = ch
			s.mu.Unlock()
			if !s.emit(provider.Event{Kind: provider.EventQuestion, Question: &provider.Question{ID: qid, Items: []provider.QuestionItem{item}}}) {
				return "", true
			}
			select {
			case r := <-ch:
				if r.Declined {
					last = "declined: " + r.Message
				} else {
					for _, a := range r.Answers {
						last = "answer: " + a.Value()
					}
				}
				s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: last})
			case <-s.interrupt:
				s.emit(provider.Event{Kind: provider.EventTurnCompleted, Text: "interrupted", IsError: true})
				return "", true
			case <-s.done:
				return "", true
			}
		case "images":
			s.mu.Lock()
			var names []string
			for _, img := range s.images {
				names = append(names, filepath.Base(img.Path))
			}
			s.mu.Unlock()
			last = "images: " + strings.Join(names, ",")
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: last})
		case "resumed":
			last = "resumed: " + s.spec.ResumeRef
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: last})
		case "env":
			v := ""
			for _, kv := range s.spec.Env {
				if name, val, ok := strings.Cut(kv, "="); ok && name == arg {
					v = val
				}
			}
			last = arg + "=" + v
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: last})
		}
	}
	return last, false
}

func (s *session) decide(ctx context.Context, a provider.ToolAction) provider.Decision {
	if s.spec.Gate == nil {
		return provider.Decision{Allow: true}
	}
	return s.spec.Gate(ctx, a)
}

// callTool calls one of the tools Hermesh gave the session, the way an
// agent does: it starts the MCP server and calls the tool.
func (s *session) callTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	if len(s.spec.MCPServers) == 0 {
		return "no Hermesh tools in this session", true
	}
	m := s.spec.MCPServers[0]
	cmd := exec.CommandContext(ctx, m.Command, m.Args...)
	cmd.Env = os.Environ()
	for k, v := range m.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "fake", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return "starting the tools: " + err.Error(), true
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	var out []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			out = append(out, t.Text)
		}
	}
	return strings.Join(out, "\n"), res.IsError
}
