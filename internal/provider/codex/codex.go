// Package codex drives the Codex CLI through `codex app-server` (JSON-RPC
// over stdio, JSONL, without the "jsonrpc" field).
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

type Provider struct {
	PathEnv string
	Binary  string
}

func (p *Provider) Name() string { return "codex" }

func (p *Provider) binary() string {
	if p.Binary != "" {
		return p.Binary
	}
	home, _ := os.UserHomeDir()
	return provider.LookPath("codex", p.PathEnv,
		"/opt/homebrew/bin/codex", "/usr/local/bin/codex", filepath.Join(home, ".local/bin/codex"),
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
		filepath.Join(home, "Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex"),
		filepath.Join(home, "Applications/Codex.app/Contents/Resources/codex"))
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+[-\w.]*`)

func (p *Provider) Discover(ctx context.Context) provider.Info {
	info := provider.Info{Name: "codex"}
	bin := p.binary()
	if bin == "" {
		info.Detail = "codex not found"
		return info
	}
	info.Binary = bin
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		info.Detail = "codex --version failed: " + err.Error()
		return info
	}
	info.Available = true
	info.Version = versionRE.FindString(string(out))
	if out, err := exec.CommandContext(ctx, bin, "login", "status").CombinedOutput(); err == nil && strings.Contains(string(out), "Logged in") {
		info.LoggedIn = true
	}
	return info
}

func (p *Provider) Start(ctx context.Context, spec provider.StartSpec) (provider.Session, error) {
	bin := p.binary()
	if bin == "" {
		return nil, errors.New("codex not found")
	}
	proc, err := provider.StartProc(ctx, bin, []string{"app-server"}, spec.WorkDir, spec.Env)
	if err != nil {
		return nil, err
	}
	s := &session{proc: proc, events: make(chan provider.Event, 256), pending: map[int64]chan rpcResult{}, gate: spec.Gate, ctx: ctx, workDir: spec.WorkDir}
	go s.read()
	fail := func(err error) (provider.Session, error) {
		proc.Stop(time.Second)
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.call(hctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "hermesh", "version": "0.1.0"}}); err != nil {
		return fail(fmt.Errorf("codex initialize: %w", err))
	}
	s.notify("initialized", nil)
	params := map[string]any{"cwd": spec.WorkDir, "sandbox": "danger-full-access", "approvalPolicy": "never"}
	if spec.Model != "" {
		params["model"] = spec.Model
	}
	method := "thread/start"
	if spec.ResumeRef != "" {
		method = "thread/resume"
		params["threadId"] = spec.ResumeRef
	}
	res, err := s.call(hctx, method, params)
	if err != nil {
		if method == "thread/resume" && provider.IsResumeRejected(err.Error()) {
			err = provider.Fail(sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, err.Error())
		}
		return fail(fmt.Errorf("codex %s: %w", method, err))
	}
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	json.Unmarshal(res, &tr)
	if tr.Thread.ID == "" {
		return fail(errors.New("codex did not return a thread id"))
	}
	s.threadID = tr.Thread.ID
	s.emit(provider.Event{Kind: provider.EventRef, Ref: s.threadID})
	if err := s.Send(ctx, provider.Input{Text: spec.Prompt, Images: spec.Images}); err != nil {
		return fail(err)
	}
	return s, nil
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type session struct {
	proc    *provider.Proc
	events  chan provider.Event
	gate    provider.Gate
	ctx     context.Context
	workDir string

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan rpcResult
	threadID string
	turnID   string
	lastMsg  string
	usage    provider.Usage
	closed   bool
	// questions waiting for an answer: asked asynchronously in a message
	// (answered by steering the turn) or blocking (requestUserInput,
	// answered by replying to the request).
	asyncAsks map[string][]string // question id -> titles
	inputAsks map[string]inputAsk
	// progress is when the app-server last reported work (unix nanos):
	// status notifications do not count, nor do the retries it keeps
	// sending while it waits for the network (Codex 0.162 retries forever).
	progress atomic.Int64
}

// notProgress are notifications Codex sends without doing any work.
func notProgress(method string) bool {
	switch method {
	case "error", "warning", "thread/status/changed", "thread/tokenUsage/updated":
		return true
	}
	return strings.HasPrefix(method, "account/") || strings.HasPrefix(method, "mcpServer/") || strings.HasPrefix(method, "remoteControl/")
}

func (s *session) Events() <-chan provider.Event { return s.events }
func (s *session) Done() <-chan struct{}         { return s.proc.Done() }
func (s *session) Err() error                    { return s.proc.Err() }
func (s *session) Pgid() int                     { return s.proc.Pgid() }
func (s *session) LastActivity() time.Time {
	if n := s.progress.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}
func (s *session) Close() error { s.proc.Stop(3 * time.Second); return nil }

func (s *session) emit(e provider.Event) {
	select {
	case s.events <- e:
	case <-s.proc.Done():
	}
}

func (s *session) notify(method string, params any) error {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return s.proc.WriteLine(b)
}

func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan rpcResult, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := s.proc.WriteLine(b); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.proc.Done():
		return nil, fmt.Errorf("codex exited: %v", s.proc.Err())
	}
}

// input is the app-server's UserInput list: text, then local images.
func input(in provider.Input) []any {
	out := []any{map[string]any{"type": "text", "text": in.Text}}
	for _, img := range in.Images {
		out = append(out, map[string]any{"type": "localImage", "path": img.Path})
	}
	return out
}

func (s *session) Send(ctx context.Context, in provider.Input) error {
	s.mu.Lock()
	s.lastMsg = ""
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := s.call(cctx, "turn/start", map[string]any{"threadId": s.threadID, "input": input(in)})
	if err != nil {
		return err
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	json.Unmarshal(res, &tr)
	s.mu.Lock()
	if tr.Turn.ID != "" {
		s.turnID = tr.Turn.ID
	}
	s.mu.Unlock()
	return nil
}

// Steer adds input to the active turn (turn/steer). Codex refuses it once
// the turn has ended ("no active turn to steer") or when the turn cannot
// take input (activeTurnNotSteerable, e.g. a review).
func (s *session) Steer(ctx context.Context, in provider.Input) error {
	s.mu.Lock()
	tid := s.turnID
	s.mu.Unlock()
	if tid == "" {
		return errors.New("no active turn")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := s.call(cctx, "turn/steer", map[string]any{"threadId": s.threadID, "expectedTurnId": tid, "input": input(in)})
	return err
}

func (s *session) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	tid := s.turnID
	s.mu.Unlock()
	if tid == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.call(cctx, "turn/interrupt", map[string]any{"threadId": s.threadID, "turnId": tid})
	return err
}

type message struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result json.RawMessage  `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *session) read() {
	defer close(s.events)
	s.proc.Lines(func(b []byte) {
		// High-volume notifications we do not use: skip without parsing.
		for _, skip := range skipNotifications {
			if bytes.Contains(b, skip) {
				if !bytes.Contains(b, []byte(`"method":"account/`)) && !bytes.Contains(b, []byte(`"method":"mcpServer/`)) {
					s.progress.Store(time.Now().UnixNano()) // reasoning or output deltas
				}
				return
			}
		}
		var m message
		if json.Unmarshal(b, &m) != nil {
			return
		}
		if !notProgress(m.Method) {
			s.progress.Store(time.Now().UnixNano())
		}
		switch {
		case m.ID != nil && m.Method == "": // response
			var id int64
			json.Unmarshal(*m.ID, &id)
			s.mu.Lock()
			ch := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ch != nil {
				r := rpcResult{result: m.Result}
				if m.Error != nil {
					r.err = fmt.Errorf("%s (code %d)", m.Error.Message, m.Error.Code)
				}
				ch <- r
			}
		case m.ID != nil: // server request (approvals etc.)
			s.handleServerRequest(*m.ID, m.Method, m.Params)
		default:
			s.handleNotification(m.Method, m.Params)
		}
	})
}

type inputAsk struct {
	reqID json.RawMessage
	ids   []string
}

// Answer answers a question: a message's questions by steering the turn
// (Codex sleeps until input arrives), requestUserInput by its reply.
func (s *session) Answer(ctx context.Context, id string, r provider.Reply) error {
	s.mu.Lock()
	titles, isAsync := s.asyncAsks[id]
	in, isInput := s.inputAsks[id]
	delete(s.asyncAsks, id)
	delete(s.inputAsks, id)
	s.mu.Unlock()
	values := map[string]string{}
	for _, a := range r.Answers {
		values[a.ItemID] = a.Value()
	}
	switch {
	case isAsync:
		text := r.Message
		if !r.Declined {
			var lines []string
			for i, t := range titles {
				v := values[fmt.Sprint(i+1)]
				if len(titles) == 1 {
					lines = append(lines, v)
				} else {
					lines = append(lines, t+": "+v)
				}
			}
			text = strings.Join(lines, "\n")
		}
		return s.Steer(ctx, provider.Input{Text: text})
	case isInput:
		answers := map[string]any{}
		for _, qid := range in.ids {
			list := []string{r.Message} // never an empty answer
			if !r.Declined {
				list = nil
				for _, a := range r.Answers {
					if a.ItemID == qid {
						list = append(append(list, a.Selected...), nonEmpty(a.Text)...)
					}
				}
			}
			answers[qid] = map[string]any{"answers": list}
		}
		b, _ := json.Marshal(map[string]any{"id": in.reqID, "result": map[string]any{"answers": answers}})
		return s.proc.WriteLine(b)
	}
	return errors.New("no such question")
}

func nonEmpty(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{strings.TrimSpace(s)}
}

func (s *session) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method == "item/tool/requestUserInput" && s.askInput(id, params) {
		return // answered by Answer
	}
	// approvalPolicy is "never", so these should not arrive; decline any
	// that do.
	var result any = map[string]any{"decision": "decline"}
	if strings.Contains(method, "requestUserInput") || strings.Contains(method, "elicitation") {
		result = map[string]any{"action": "decline"}
	}
	b, _ := json.Marshal(map[string]any{"id": json.RawMessage(id), "result": result})
	s.proc.WriteLine(b)
}

// askInput turns a requestUserInput request into a question.
func (s *session) askInput(id json.RawMessage, params json.RawMessage) bool {
	var p struct {
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsOther  bool   `json:"isOther"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Questions) == 0 {
		return false
	}
	q := &provider.Question{ID: "input:" + string(id)}
	var ids []string
	for _, x := range p.Questions {
		it := provider.QuestionItem{ID: x.ID, Header: x.Header, Text: x.Question, AllowOther: x.IsOther || len(x.Options) == 0, Secret: x.IsSecret}
		for _, o := range x.Options {
			it.Options = append(it.Options, provider.Option{Label: o.Label, Description: o.Description})
		}
		q.Items = append(q.Items, it)
		ids = append(ids, x.ID)
	}
	s.mu.Lock()
	if s.inputAsks == nil {
		s.inputAsks = map[string]inputAsk{}
	}
	s.inputAsks[q.ID] = inputAsk{reqID: id, ids: ids}
	s.mu.Unlock()
	s.emit(provider.Event{Kind: provider.EventQuestion, Question: q})
	return true
}

type item struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Command          string `json:"command"`
	Status           string `json:"status"`
	ExitCode         *int   `json:"exitCode"`
	AggregatedOutput string `json:"aggregatedOutput"`
	Changes          []struct {
		Path string `json:"path"`
		Kind any    `json:"kind"`
	} `json:"changes"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Query  string `json:"query"`
	Phase  string `json:"phase"`
	// An agent message that asks: Codex then waits (sleeping) for input.
	Delivery  string `json:"delivery"`
	Questions []struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
	} `json:"questions"`
}

func (s *session) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(params, &p) == nil && p.Delta != "" {
			s.emit(provider.Event{Kind: provider.EventAgentDraft, Text: p.Delta})
		}
	case "item/started", "item/completed":
		var p struct {
			Item item `json:"item"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.handleItem(method == "item/completed", p.Item)
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens  uint64 `json:"inputTokens"`
					OutputTokens uint64 `json:"outputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &p) == nil {
			s.mu.Lock()
			s.usage = provider.Usage{InputTokens: p.TokenUsage.Total.InputTokens, OutputTokens: p.TokenUsage.Total.OutputTokens}
			s.mu.Unlock()
		}
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &p) == nil && p.Turn.ID != "" {
			s.mu.Lock()
			s.turnID = p.Turn.ID
			s.mu.Unlock()
		}
	case "turn/completed":
		var p struct {
			Turn struct {
				Status string     `json:"status"`
				Error  *turnError `json:"error"`
			} `json:"turn"`
		}
		json.Unmarshal(params, &p)
		s.mu.Lock()
		last, usage := s.lastMsg, s.usage
		s.turnID = "" // nothing left to interrupt
		s.mu.Unlock()
		e := provider.Event{Kind: provider.EventTurnCompleted, Text: last, Usage: usage}
		if p.Turn.Status != "completed" {
			e.IsError = true
			if p.Turn.Error != nil {
				e.Text = p.Turn.Error.Message
				e.Failure = p.Turn.Error.failure()
			} else if e.Text == "" {
				e.Text = "turn " + p.Turn.Status
			}
		}
		s.emit(e)
	case "error":
		var p struct {
			Error     turnError `json:"error"`
			WillRetry bool      `json:"willRetry"`
		}
		if json.Unmarshal(params, &p) == nil && p.Error.Message != "" {
			// Also the retries ("Reconnecting... waiting for network"):
			// the caller sees what the agent is stuck on.
			f := p.Error.failure()
			text := p.Error.Message
			if p.WillRetry {
				text = strings.TrimSpace(text + " " + p.Error.AdditionalDetails)
				f.Message = text
			}
			s.emit(provider.Event{Kind: provider.EventError, Text: text, Failure: f})
		}
	}
}

func (s *session) handleItem(completed bool, it item) {
	switch it.Type {
	case "agentMessage":
		if completed && strings.TrimSpace(it.Text) != "" {
			s.mu.Lock()
			s.lastMsg = it.Text
			s.mu.Unlock()
			s.emit(provider.Event{Kind: provider.EventAgentMessage, Text: it.Text})
		}
		if completed && it.Delivery == "async" && len(it.Questions) > 0 {
			q := &provider.Question{ID: "async:" + it.ID}
			var titles []string
			for i, x := range it.Questions {
				qi := provider.QuestionItem{ID: fmt.Sprint(i + 1), Text: x.Title, AllowOther: true}
				for _, o := range x.Options {
					qi.Options = append(qi.Options, provider.Option{Label: o})
				}
				q.Items = append(q.Items, qi)
				titles = append(titles, x.Title)
			}
			s.mu.Lock()
			if s.asyncAsks == nil {
				s.asyncAsks = map[string][]string{}
			}
			s.asyncAsks[q.ID] = titles
			s.mu.Unlock()
			s.emit(provider.Event{Kind: provider.EventQuestion, Question: q})
		}
	case "commandExecution":
		if !completed {
			s.toolCall(it.ID, provider.ToolShell, "shell", it.Command, nil)
			return
		}
		out, trunc := provider.Truncate(it.AggregatedOutput, 16<<10)
		isErr := it.Status != "completed" || (it.ExitCode != nil && *it.ExitCode != 0)
		s.emit(provider.Event{Kind: provider.EventToolResult, CallID: it.ID, Summary: out, IsError: isErr, Truncated: trunc})
	case "fileChange":
		var paths []string
		for _, c := range it.Changes {
			paths = append(paths, c.Path)
		}
		if !completed {
			s.toolCall(it.ID, provider.ToolEdit, "edit", strings.Join(paths, ", "), paths)
			return
		}
		s.emit(provider.Event{Kind: provider.EventToolResult, CallID: it.ID, Summary: it.Status, IsError: it.Status != "completed"})
	case "mcpToolCall":
		if !completed {
			s.toolCall(it.ID, provider.ToolMCP, it.Server+"/"+it.Tool, it.Server+"/"+it.Tool, nil)
			return
		}
		s.emit(provider.Event{Kind: provider.EventToolResult, CallID: it.ID, Summary: it.Status, IsError: it.Status != "completed"})
	case "webSearch":
		if !completed {
			s.toolCall(it.ID, provider.ToolNetwork, "web_search", it.Query, nil)
			return
		}
		s.emit(provider.Event{Kind: provider.EventToolResult, CallID: it.ID, Summary: "done"})
	}
}

// turnError is the app-server's TurnError.
type turnError struct {
	Message           string          `json:"message"`
	AdditionalDetails string          `json:"additionalDetails"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
}

// failure maps codexErrorInfo (a string code, or an object keyed by the
// code with an optional httpStatusCode) to a failure kind; the message
// text decides when the code says nothing.
func (te *turnError) failure() *provider.Failure {
	msg := strings.TrimSpace(te.Message + " " + te.AdditionalDetails)
	f := &provider.Failure{Message: msg, Kind: CodexFailureKind(te.CodexErrorInfo)}
	var withStatus map[string]struct {
		HTTPStatusCode int `json:"httpStatusCode"`
	}
	if json.Unmarshal(te.CodexErrorInfo, &withStatus) == nil {
		for _, v := range withStatus {
			f.HTTPStatus = v.HTTPStatusCode
		}
	}
	if f.Kind == sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED {
		f.Kind = provider.Classify(msg)
	}
	return f
}

// CodexFailureKind maps a codexErrorInfo value (exported for tests).
func CodexFailureKind(info json.RawMessage) sessionv1.FailureKind {
	var code string
	if json.Unmarshal(info, &code) == nil {
		switch code {
		case "contextWindowExceeded":
			return sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW
		case "sessionBudgetExceeded":
			return sessionv1.FailureKind_FAILURE_KIND_LIMIT_REACHED
		case "usageLimitExceeded":
			return sessionv1.FailureKind_FAILURE_KIND_QUOTA
		case "rateLimitExceeded", "serverOverloaded", "flexUnavailable":
			return sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT
		case "cyberPolicy", "misalignmentPolicyViolation", "tooManyDenials":
			return sessionv1.FailureKind_FAILURE_KIND_REFUSED
		case "internalServerError":
			return sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR
		case "unauthorized":
			return sessionv1.FailureKind_FAILURE_KIND_AUTH
		case "badRequest":
			return sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST
		}
		return sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED
	}
	var obj map[string]struct {
		HTTPStatusCode int `json:"httpStatusCode"`
	}
	if json.Unmarshal(info, &obj) != nil {
		return sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED
	}
	for k, v := range obj {
		switch k {
		case "httpConnectionFailed", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts":
			if kind := provider.FromHTTPStatus(v.HTTPStatusCode, ""); kind != sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED {
				return kind
			}
			return sessionv1.FailureKind_FAILURE_KIND_NETWORK
		case "activeTurnNotSteerable":
			return sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST
		}
	}
	return sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED
}

// toolCall records an action; the gate here only observes (approvalPolicy
// is "never").
func (s *session) toolCall(id string, kind provider.ToolKind, name, summary string, paths []string) {
	a := provider.ToolAction{CallID: id, Kind: kind, Name: name, Summary: summary, Paths: paths}
	if kind == provider.ToolShell {
		a.Command = summary
	}
	d := provider.Decision{Allow: true}
	if s.gate != nil {
		d = s.gate(s.ctx, a)
	}
	sum, _ := provider.Truncate(summary, 500)
	s.emit(provider.Event{Kind: provider.EventToolCall, CallID: id, ToolKind: kind, ToolName: name, Summary: sum, Decision: &d})
}

var skipNotifications = [][]byte{
	[]byte(`"method":"item/commandExecution/outputDelta"`),
	[]byte(`"method":"item/reasoning/`),
	[]byte(`"method":"account/rateLimits/updated"`),
	[]byte(`"method":"mcpServer/startupStatus/updated"`),
}
