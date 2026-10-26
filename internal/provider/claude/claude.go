// Package claude drives Claude Code in headless stream-json mode.
package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

type Provider struct {
	// PathEnv is the PATH used to find binaries ("": the login shell's).
	PathEnv string
	// Binary overrides discovery (tests use a fake CLI).
	Binary string
}

func (p *Provider) Name() string { return "claude" }

func (p *Provider) binary() string {
	if p.Binary != "" {
		return p.Binary
	}
	home, _ := os.UserHomeDir()
	return provider.LookPath("claude", provider.SearchPath(p.PathEnv),
		filepath.Join(home, ".local/bin/claude"), filepath.Join(home, ".claude/local/claude"),
		"/opt/homebrew/bin/claude", "/usr/local/bin/claude", desktopCLI(home))
}

// desktopCLI is the newest Claude Code the Claude desktop app keeps for
// itself (macOS), so its users need no separate install:
// Claude/claude-code/<version>/<build>/claude.app/Contents/MacOS/claude.
func desktopCLI(home string) string {
	dirs, _ := filepath.Glob(filepath.Join(home, "Library/Application Support/Claude/claude-code/*"))
	slices.SortFunc(dirs, func(a, b string) int {
		return slices.Compare(versionKey(filepath.Base(b)), versionKey(filepath.Base(a)))
	})
	for _, d := range dirs {
		bins, _ := filepath.Glob(filepath.Join(d, "*/claude.app/Contents/MacOS/claude"))
		if p := provider.LookPath("claude", "", bins...); p != "" {
			return p
		}
	}
	return ""
}

func versionKey(v string) []int {
	var k []int
	for _, s := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(s)
		k = append(k, n)
	}
	return k
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

func (p *Provider) Discover(ctx context.Context) provider.Info {
	info := provider.Info{Name: "claude"}
	bin := p.binary()
	if !provider.Exists(bin) {
		info.Problem, info.Detail = provider.ProblemNotFound, "claude not found"
		if app := provider.AppWithoutCLI("Claude.app"); app != "" && p.Binary == "" {
			info.Problem, info.Detail = provider.ProblemAppWithoutCLI, app
		}
		return info
	}
	info.Binary = bin
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	env := provider.ProbeEnv(p.PathEnv)
	out, problem, detail := provider.Probe(ctx, env, bin, "--version")
	if problem != provider.ProblemNone {
		info.Problem, info.Detail = problem, detail
		return info
	}
	info.Available = true
	info.Version = versionRE.FindString(string(out))
	if out, problem, _ := provider.Probe(ctx, env, bin, "auth", "status"); problem == provider.ProblemNone {
		var st struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(out, &st) == nil {
			info.LoggedIn = st.LoggedIn
		}
	}
	if !info.LoggedIn && os.Getenv("ANTHROPIC_API_KEY") != "" {
		info.LoggedIn = true
	}
	info.Models = Models
	return info
}

// Models are the aliases Claude Code takes for its latest models; it lists
// none itself.
var Models = []provider.Model{
	{ID: "fable", Name: "Fable", Efforts: efforts},
	{ID: "opus", Name: "Opus", Efforts: efforts},
	{ID: "sonnet", Name: "Sonnet", Efforts: efforts},
	{ID: "haiku", Name: "Haiku", Efforts: efforts},
}

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Args returns the CLI arguments (exported for tests).
func Args(spec provider.StartSpec) []string {
	// --replay-user-messages echoes each message when the agent takes it
	// (isReplay), which tells whether steered input is still waiting.
	// --permission-prompt-tool stdio makes Hermesh the permission host:
	// without one Claude offers no AskUserQuestion in -p mode. Anything
	// else that would prompt is denied, as with --permission-prompts none.
	// --include-partial-messages streams the reply as it is written
	// (stream_event lines), for drafts.
	// crossSessionInbound refuse shuts Claude Code's own inbox: a message
	// posted there by another session, or by anything holding the session's
	// token, would start a turn Hermesh never asked for. --settings outranks
	// the user's and the project's settings.
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--replay-user-messages",
		"--include-partial-messages", "--permission-prompt-tool", "stdio", "--dangerously-skip-permissions",
		"--settings", `{"crossSessionInbound":"refuse"}`}
	for _, d := range spec.ReadDirs {
		args = append(args, "--add-dir", d)
	}
	if spec.ResumeRef != "" {
		args = append(args, "--resume", spec.ResumeRef)
	} else if spec.NewRef != "" {
		args = append(args, "--session-id", spec.NewRef)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	return args
}

func (p *Provider) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	bin := p.binary()
	if bin == "" {
		return nil, errors.New("claude not found")
	}
	proc, err := provider.StartProc(ctx, bin, Args(spec), spec.WorkDir, spec.Env)
	if err != nil {
		return nil, err
	}
	s := &session{proc: proc, events: make(chan provider.Event, 256), gate: spec.Gate, ctx: ctx}
	// Read before writing: Claude may fill stdout before it reads stdin.
	go s.read()
	init := map[string]any{"type": "control_request", "request_id": "hermesh-init",
		"request": map[string]any{"subtype": "initialize",
			"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": nil, "hookCallbackIds": []string{"gate"}}}}}}
	if err := s.write(init); err != nil {
		proc.Stop(time.Second)
		return nil, err
	}
	if err := s.Send(ctx, provider.Input{Text: spec.Prompt, Images: spec.Images}); err != nil {
		proc.Stop(time.Second)
		return nil, err
	}
	return s, nil
}

type session struct {
	proc   *provider.Proc
	events chan provider.Event
	gate   provider.Gate
	ctx    context.Context

	reqSeq int
	mu     sync.Mutex
	// The current turn: whether one is running, how many messages were
	// written and echoed back, and the last result held back while the
	// agent still had messages to take (with the usage of all held ones).
	inTurn     bool
	sent, seen int
	held       *provider.Event
	// asks are AskUserQuestion permission requests waiting for an answer,
	// by request id; askCalls are their tool calls (no tool events: the
	// question is the event).
	asks     map[string]askRequest
	askCalls map[string]bool
}

type askRequest struct {
	input     map[string]any
	questions []askQuestion
}

// askQuestion is one question of AskUserQuestion's input.
type askQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
	MultiSelect bool `json:"multiSelect"`
}

// Answer answers an AskUserQuestion: the permission request is allowed
// with the answers (by question text) added to the tool's input, or
// denied with the reason.
func (s *session) Answer(ctx context.Context, id string, r provider.Reply) error {
	s.mu.Lock()
	a, ok := s.asks[id]
	delete(s.asks, id)
	s.mu.Unlock()
	if !ok {
		return errors.New("no such question")
	}
	resp := map[string]any{"behavior": "deny", "message": r.Message}
	if !r.Declined {
		answers := map[string]string{}
		for _, x := range r.Answers {
			var i int
			if _, err := fmt.Sscanf(x.ItemID, "%d", &i); err == nil && i >= 1 && i <= len(a.questions) {
				answers[a.questions[i-1].Question] = x.Value()
			}
		}
		input := map[string]any{}
		for k, v := range a.input {
			input[k] = v
		}
		input["answers"] = answers
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	return s.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
}

func (s *session) Events() <-chan provider.Event { return s.events }
func (s *session) Done() <-chan struct{}         { return s.proc.Done() }
func (s *session) Err() error                    { return s.proc.Err() }
func (s *session) Pgid() int                     { return s.proc.Pgid() }
func (s *session) LastActivity() time.Time       { return s.proc.LastOutput() }
func (s *session) Close() error                  { s.proc.Stop(3 * time.Second); return nil }

func (s *session) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.proc.WriteLine(b)
}

func (s *session) Send(ctx context.Context, in provider.Input) error {
	s.mu.Lock()
	s.inTurn = true
	s.sent++
	s.mu.Unlock()
	return s.writeUser(in)
}

// Steer writes a message while a turn runs. Claude takes it at the next
// tool boundary of the turn; when the turn has no boundary left it ends
// with a result and runs the message as one more turn. Results are held
// back until every message was taken, so either way it is one turn here.
func (s *session) Steer(ctx context.Context, in provider.Input) error {
	s.mu.Lock()
	if !s.inTurn {
		s.mu.Unlock()
		return errors.New("no turn is running")
	}
	s.sent++ // with the check: a result read meanwhile is held back
	s.mu.Unlock()
	return s.writeUser(in)
}

// writeUser writes a message already counted in sent; images go as
// base64 image blocks after the text.
func (s *session) writeUser(in provider.Input) error {
	var content any = in.Text
	if len(in.Images) > 0 {
		blocks := []any{map[string]any{"type": "text", "text": in.Text}}
		for _, img := range in.Images {
			b, err := os.ReadFile(img.Path)
			if err == nil {
				blocks = append(blocks, map[string]any{"type": "image",
					"source": map[string]any{"type": "base64", "media_type": img.MediaType, "data": base64.StdEncoding.EncodeToString(b)}})
			}
		}
		content = blocks
	}
	err := s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	if err != nil {
		s.mu.Lock()
		s.sent--
		s.mu.Unlock()
	}
	return err
}

func (s *session) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	s.reqSeq++
	id := fmt.Sprintf("hermesh-int-%d", s.reqSeq)
	s.mu.Unlock()
	return s.write(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}})
}

type line struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	IsReplay  bool            `json:"isReplay"`
	// stream_event lines: an API streaming event, and the Task call it is
	// part of (subagents; empty for the main conversation).
	Event        json.RawMessage `json:"event"`
	ParentToolID string          `json:"parent_tool_use_id"`
	// Structured failure details on result lines (Claude Code 2.1.292:
	// errors[] for a refused --resume, api_error_status for API errors,
	// terminal_reason=prompt_too_long even when is_error is false).
	Errors         []string `json:"errors"`
	APIErrorStatus int      `json:"api_error_status"`
	TerminalReason string   `json:"terminal_reason"`
	CostUSD        float64  `json:"total_cost_usd"`
	Usage          struct {
		InputTokens  uint64 `json:"input_tokens"`
		CacheCreate  uint64 `json:"cache_creation_input_tokens"`
		CacheRead    uint64 `json:"cache_read_input_tokens"`
		OutputTokens uint64 `json:"output_tokens"`
	} `json:"usage"`
}

type content struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (s *session) emit(e provider.Event) {
	select {
	case s.events <- e:
	case <-s.proc.Done():
	}
}

func (s *session) read() {
	defer close(s.events)
	defer func() {
		// The process exited with a message it never took (a refused
		// --resume exits before taking the prompt): report the result.
		s.mu.Lock()
		e := s.held
		s.held = nil
		s.mu.Unlock()
		if e != nil {
			select {
			case s.events <- *e:
			default:
			}
		}
	}()
	s.proc.Lines(func(b []byte) {
		var l line
		if json.Unmarshal(b, &l) != nil {
			return
		}
		switch l.Type {
		case "system":
			if l.Subtype == "init" && l.SessionID != "" {
				s.emit(provider.Event{Kind: provider.EventRef, Ref: l.SessionID})
			}
		case "stream_event":
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if l.ParentToolID == "" && json.Unmarshal(l.Event, &ev) == nil && ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				s.emit(provider.Event{Kind: provider.EventAgentDraft, Text: ev.Delta.Text})
			}
		case "assistant":
			var m struct {
				Content []content `json:"content"`
			}
			json.Unmarshal(l.Message, &m)
			for _, c := range m.Content {
				if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
					s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: c.Text})
				}
			}
		case "user":
			if l.IsReplay {
				s.mu.Lock()
				s.seen++
				s.mu.Unlock()
				return
			}
			var m struct {
				Content json.RawMessage `json:"content"`
			}
			json.Unmarshal(l.Message, &m)
			var cs []content
			if json.Unmarshal(m.Content, &cs) == nil {
				for _, c := range cs {
					if c.Type == "tool_result" {
						s.mu.Lock()
						ask := s.askCalls[c.ToolUseID]
						delete(s.askCalls, c.ToolUseID)
						s.mu.Unlock()
						if ask {
							continue
						}
						sum, trunc := provider.KeepOutput(toolResultText(c.Content))
						s.emit(provider.Event{Kind: provider.EventToolResult, CallID: c.ToolUseID, Summary: sum, IsError: c.IsError, Truncated: trunc})
					}
				}
			}
		case "result":
			e := provider.Event{Kind: provider.EventTurnCompleted, Text: l.Result, IsError: l.IsError,
				Usage: provider.Usage{InputTokens: l.Usage.InputTokens + l.Usage.CacheCreate + l.Usage.CacheRead, OutputTokens: l.Usage.OutputTokens, CostUSD: l.CostUSD}}
			if f := ResultFailure(l, s.proc.StderrTail()); f != nil {
				e.IsError, e.Failure = true, f
				if strings.TrimSpace(e.Text) == "" {
					e.Text = f.Message
				}
			}
			s.mu.Lock()
			if s.held != nil {
				e.Usage = addUsage(s.held.Usage, e.Usage)
			}
			if s.seen < s.sent { // steered messages are next: the turn goes on
				s.held = &e
				s.mu.Unlock()
				return
			}
			s.held, s.inTurn = nil, false
			s.mu.Unlock()
			s.emit(e)
		case "control_request":
			s.handleControl(l.RequestID, l.Request)
		}
	})
}

func addUsage(a, b provider.Usage) provider.Usage {
	return provider.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens, CostUSD: a.CostUSD + b.CostUSD}
}

// ResultFailure classifies a result line (nil: the turn succeeded). The
// stderr tail is consulted only when the result itself says nothing
// recognizable (exported for tests).
func ResultFailure(l line, stderr string) *provider.Failure {
	msg := strings.TrimSpace(strings.Join(l.Errors, "; "))
	if msg == "" {
		msg = strings.TrimSpace(l.Result)
	}
	switch {
	case l.TerminalReason == "prompt_too_long":
		if msg == "" {
			msg = "the conversation exceeds the model's context window"
		}
		return &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW, Message: msg, HTTPStatus: l.APIErrorStatus}
	case !l.IsError:
		return nil
	case l.Subtype == "error_max_turns":
		return &provider.Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_LIMIT_REACHED, Message: firstNonEmpty(msg, "maximum number of turns reached")}
	}
	f := &provider.Failure{Message: msg, HTTPStatus: l.APIErrorStatus}
	if provider.IsResumeRejected(msg) {
		f.Kind = sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED
	} else if l.APIErrorStatus != 0 {
		f.Kind = provider.FromHTTPStatus(l.APIErrorStatus, msg)
	} else {
		f.Kind = provider.Classify(msg)
	}
	if f.Kind == sessionv1.FailureKind_FAILURE_KIND_UNKNOWN && stderr != "" {
		tail := lastLine(stderr)
		if k := provider.Classify(tail); k != sessionv1.FailureKind_FAILURE_KIND_UNKNOWN {
			f.Kind = k
			f.Message = firstNonEmpty(msg, tail)
		}
	}
	if f.Message == "" {
		f.Message = "claude reported an error (" + firstNonEmpty(l.Subtype, l.TerminalReason, "no details") + ")"
	}
	return f
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []content
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

func (s *session) handleControl(id string, raw json.RawMessage) {
	var req struct {
		Subtype string `json:"subtype"`
		// hook_callback: the hook input; can_use_tool: the tool input
		RawInput json.RawMessage `json:"input"`
		Input    struct {
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"-"`
		ToolName string `json:"tool_name"` // can_use_tool
	}
	json.Unmarshal(raw, &req)
	if req.Subtype == "hook_callback" {
		json.Unmarshal(req.RawInput, &req.Input)
	}
	var resp map[string]any
	switch {
	case req.Subtype == "hook_callback" && req.Input.ToolName == "AskUserQuestion":
		// Asking is no action the preset limits; the question follows as
		// a permission request.
		s.mu.Lock()
		if s.askCalls == nil {
			s.askCalls = map[string]bool{}
		}
		s.askCalls[req.Input.ToolUseID] = true
		s.mu.Unlock()
		resp = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "allow"}}
	case req.Subtype == "can_use_tool" && req.ToolName == "AskUserQuestion":
		if s.ask(id, req.RawInput) {
			return // answered by Answer
		}
		resp = map[string]any{"behavior": "deny", "message": "Hermesh could not read this question; ask in your reply instead."}
	case req.Subtype == "can_use_tool" && req.ToolName == "ExitPlanMode":
		resp = map[string]any{"behavior": "deny", "message": "This session does not use plan mode: go ahead with the plan."}
	case req.Subtype == "hook_callback":
		a := Classify(req.Input.ToolName, req.Input.ToolInput)
		a.CallID = req.Input.ToolUseID
		d := provider.Decision{Allow: true}
		if s.gate != nil {
			d = s.gate(s.ctx, a)
		}
		s.emit(provider.Event{Kind: provider.EventToolCall, CallID: a.CallID, ToolKind: a.Kind, ToolName: a.Name, Summary: a.Summary, Decision: &d})
		decision := "allow"
		if !d.Allow {
			decision = "deny"
		}
		resp = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse",
			"permissionDecision": decision, "permissionDecisionReason": d.Reason}}
	default:
		// Other permission prompts: deny, as without a permission host.
		// The hook already decided on the tool.
		resp = map[string]any{"behavior": "deny", "message": "not permitted by Hermesh"}
	}
	s.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
}

// ask turns an AskUserQuestion permission request into a question.
func (s *session) ask(id string, raw json.RawMessage) bool {
	var input map[string]any
	var in struct {
		Questions []askQuestion `json:"questions"`
	}
	if json.Unmarshal(raw, &input) != nil || json.Unmarshal(raw, &in) != nil || len(in.Questions) == 0 {
		return false
	}
	q := &provider.Question{ID: id}
	for i, x := range in.Questions {
		it := provider.QuestionItem{ID: fmt.Sprint(i + 1), Header: x.Header, Text: x.Question, MultiSelect: x.MultiSelect, AllowOther: true}
		for _, o := range x.Options {
			it.Options = append(it.Options, provider.Option{Label: o.Label, Description: o.Description})
		}
		q.Items = append(q.Items, it)
	}
	s.mu.Lock()
	if s.asks == nil {
		s.asks = map[string]askRequest{}
	}
	s.asks[id] = askRequest{input: input, questions: in.Questions}
	s.mu.Unlock()
	s.emit(provider.Event{Kind: provider.EventQuestion, Question: q})
	return true
}

// Classify maps a Claude tool call to a normalized action.
func Classify(name string, input json.RawMessage) provider.ToolAction {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
		Command      string `json:"command"`
		URL          string `json:"url"`
		Query        string `json:"query"`
		Description  string `json:"description"`
	}
	json.Unmarshal(input, &in)
	a := provider.ToolAction{Name: name, Raw: input}
	switch name {
	case "Read":
		a.Kind, a.Paths, a.Summary = provider.ToolRead, []string{in.FilePath}, in.FilePath
	case "Glob", "Grep", "LS":
		a.Kind, a.Summary = provider.ToolSearch, strings.TrimSpace(in.Pattern+" "+in.Path)
		if in.Path != "" {
			a.Paths = []string{in.Path}
		}
	case "Edit", "MultiEdit", "Write":
		a.Kind, a.Paths, a.Summary = provider.ToolEdit, []string{in.FilePath}, in.FilePath
	case "NotebookEdit":
		a.Kind, a.Paths, a.Summary = provider.ToolEdit, []string{in.NotebookPath}, in.NotebookPath
	case "Bash", "BashOutput", "KillShell":
		a.Kind, a.Command, a.Summary = provider.ToolShell, in.Command, in.Command
	case "WebFetch", "WebSearch":
		a.Kind, a.Summary = provider.ToolNetwork, in.URL+in.Query
	default:
		a.Kind = provider.ToolOther
		if strings.HasPrefix(name, "mcp__") {
			a.Kind = provider.ToolMCP
		}
		a.Summary = in.Description
	}
	a.Summary, _ = provider.Truncate(a.Summary, 500)
	return a
}
