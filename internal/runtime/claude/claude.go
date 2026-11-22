// Package claude implements the long-lived Hermesh v2 runtime.Driver for Claude
// Code over its bidirectional stream-json stdio control protocol.
package claude

import (
	"bytes"
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
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type Driver struct {
	PathEnv string
	Binary  string
}

func NewDriver(pathEnv string) *Driver { return &Driver{PathEnv: pathEnv} }

func (d *Driver) Kind() string { return "claude" }

func (d *Driver) Capabilities(_ string) *threadv2.ProviderCapabilities {
	return &threadv2.ProviderCapabilities{
		InSessionModelSwitch:  true,
		InSessionOptionSwitch: false, // effort flag requires fast --resume cycle
		InSessionModeSwitch:   true,
		MidTurnSteer:          true,
		LiveApproval:          true,
		LiveUserInput:         true,
		NativeResume:          true,
		NativeFork:            true,
		SupportsImages:        true,
		SupportsFiles:         true,
		ReportsContextWindow:  true,
		SupportedRuntimeModes: []identityv1.RuntimeMode{
			identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED,
			identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS,
			identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
			identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		},
	}
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

func (d *Driver) resolveBinary(cfg *threadv2.ProviderInstanceConfig) string {
	if cfg != nil && cfg.BinaryPath != "" {
		return cfg.BinaryPath
	}
	if d.Binary != "" {
		return d.Binary
	}
	home, _ := os.UserHomeDir()
	return provider.LookPath("claude", provider.SearchPath(d.PathEnv),
		filepath.Join(home, ".local/bin/claude"),
		filepath.Join(home, ".claude/local/claude"),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		desktopCLI(home),
	)
}

func desktopCLI(home string) string {
	dirs, _ := filepath.Glob(filepath.Join(home, "Library/Application Support/Claude/claude-code/*"))
	slices.SortFunc(dirs, func(a, b string) int {
		return slices.Compare(versionKey(filepath.Base(b)), versionKey(filepath.Base(a)))
	})
	for _, dir := range dirs {
		bins, _ := filepath.Glob(filepath.Join(dir, "*/claude.app/Contents/MacOS/claude"))
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

func (d *Driver) Probe(ctx context.Context, cfg *threadv2.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	bin := d.resolveBinary(cfg)
	if !provider.Exists(bin) {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "claude CLI not found on PATH",
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv)
	out, prob, detail := provider.Probe(pctx, env, bin, "--version")
	if prob != provider.ProblemNone {
		return runtime.ProbeResult{
			Installed:            true,
			Compatibility:        threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_BROKEN,
			CompatibilityMessage: detail,
		}
	}
	res := runtime.ProbeResult{
		Installed:     true,
		Version:       versionRE.FindString(string(out)),
		Compatibility: threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
	if authOut, authProb, _ := provider.Probe(pctx, env, bin, "auth", "status"); authProb == provider.ProblemNone {
		var st struct {
			LoggedIn bool   `json:"loggedIn"`
			Email    string `json:"email"`
		}
		if json.Unmarshal(authOut, &st) == nil {
			res.Authenticated = st.LoggedIn
			res.AuthLabel = st.Email
		}
	}
	if !res.Authenticated && (os.Getenv("ANTHROPIC_API_KEY") != "" || instEnv["ANTHROPIC_API_KEY"] != "") {
		res.Authenticated = true
		res.AuthLabel = "ANTHROPIC_API_KEY"
	}
	return res
}

func buildArgs(spec runtime.StartSpec) []string {
	permMode := "default"
	if spec.InteractionMode == threadv2.InteractionMode_INTERACTION_MODE_PLAN {
		permMode = "plan"
	}
	var args []string
	if spec.Instance != nil && len(spec.Instance.LaunchArgs) > 0 {
		args = append(args, spec.Instance.LaunchArgs...)
	}
	args = append(args,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--replay-user-messages",
		"--include-partial-messages",
		"--permission-prompt-tool", "stdio",
		"--permission-mode", permMode,
		"--settings", `{"crossSessionInbound":"refuse"}`,
	)
	if spec.ResumeCursor != "" {
		args = append(args, "--resume", spec.ResumeCursor)
	}
	if spec.Selection != nil {
		if m := strings.TrimSpace(spec.Selection.Model); m != "" && m != "default" {
			args = append(args, "--model", m)
		}
		if opt := spec.Selection.Options["reasoningEffort"]; opt != nil {
			if sv := opt.GetStringValue(); sv != "" {
				args = append(args, "--effort", sv)
			}
		}
		if opt := spec.Selection.Options["fastMode"]; opt != nil && opt.GetBoolValue() {
			args = append(args, "--settings", `{"fastMode":true}`)
		}
	}
	return args
}

func (d *Driver) StartSession(ctx context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	bin := d.resolveBinary(spec.Instance)
	if !provider.Exists(bin) {
		return nil, errors.New("claude CLI not found")
	}
	var instEnv map[string]string
	if spec.Instance != nil {
		instEnv = spec.Instance.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv, spec.ExtraEnv)
	proc, err := provider.StartProc(ctx, bin, buildArgs(spec), spec.Workdir, env)
	if err != nil {
		return nil, err
	}
	s := &session{
		proc:      proc,
		spec:      spec,
		selection: proto.Clone(spec.Selection).(*threadv2.ModelSelection),
		events:    make(chan runtime.Event, 256),
		turnCtx:   ctx,
	}
	s.alive.Store(true)
	go s.readLoop()

	initMsg := map[string]any{
		"type":       "control_request",
		"request_id": "hermesh-init",
		"request": map[string]any{
			"subtype": "initialize",
		},
	}
	if err := s.writeJSON(initMsg); err != nil {
		proc.Stop(time.Second)
		return nil, err
	}
	return s, nil
}

type session struct {
	proc      *provider.Proc
	spec      runtime.StartSpec
	events    chan runtime.Event
	alive     atomic.Bool
	reqSeq    atomic.Uint64
	itemSeq   atomic.Uint64
	closeOnce sync.Once

	mu        sync.Mutex
	selection *threadv2.ModelSelection
	turnCtx   context.Context
	inTurn    bool
	sent      int
	seen      int
	msgItemID string
}

func (s *session) Events() <-chan runtime.Event { return s.events }
func (s *session) Alive() bool                  { return s.alive.Load() }

func (s *session) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.proc.WriteLine(b)
}

func (s *session) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	s.mu.Lock()
	s.turnCtx = ctx
	s.inTurn = true
	s.sent++
	s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
	s.mu.Unlock()
	return s.writeUserMessage(in.Prompt, in.Attachments)
}

func (s *session) Steer(_ context.Context, text string, atts []runtime.TurnAttachment) error {
	s.mu.Lock()
	if !s.inTurn {
		s.mu.Unlock()
		return errors.New("no active turn to steer")
	}
	s.sent++
	s.mu.Unlock()
	return s.writeUserMessage(text, atts)
}

func (s *session) writeUserMessage(text string, atts []runtime.TurnAttachment) error {
	var content any = text
	var blocks []any
	if len(atts) > 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
		for _, a := range atts {
			if strings.HasPrefix(a.MimeType, "image/") && len(a.Data) > 0 {
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": a.MimeType,
						"data":       base64.StdEncoding.EncodeToString(a.Data),
					},
				})
			} else if a.Path != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": fmt.Sprintf("\n[Attached file: %s (%s)]", a.Path, a.Name),
				})
			}
		}
		content = blocks
	}
	err := s.writeJSON(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	})
	if err != nil {
		s.mu.Lock()
		s.sent--
		s.mu.Unlock()
	}
	return err
}

func (s *session) SetSelection(_ context.Context, sel *threadv2.ModelSelection) error {
	if sel == nil {
		return nil
	}
	s.mu.Lock()
	prev := s.selection
	s.mu.Unlock()
	// Effort/fastMode changes require CLI restart with --resume.
	if prev != nil {
		if prev.Options["reasoningEffort"].GetStringValue() != sel.Options["reasoningEffort"].GetStringValue() ||
			prev.Options["fastMode"].GetBoolValue() != sel.Options["fastMode"].GetBoolValue() {
			return runtime.ErrHotSwitchUnsupported
		}
	}
	id := fmt.Sprintf("hermesh-model-%d", s.reqSeq.Add(1))
	if err := s.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request": map[string]any{
			"subtype": "set_model",
			"model":   sel.Model,
		},
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.selection = proto.Clone(sel).(*threadv2.ModelSelection)
	s.mu.Unlock()
	return nil
}

func (s *session) SetModes(_ context.Context, _ identityv1.RuntimeMode, im threadv2.InteractionMode) error {
	mode := "default"
	if im == threadv2.InteractionMode_INTERACTION_MODE_PLAN {
		mode = "plan"
	}
	id := fmt.Sprintf("hermesh-mode-%d", s.reqSeq.Add(1))
	return s.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request": map[string]any{
			"subtype": "set_permission_mode",
			"mode":    mode,
		},
	})
}

func (s *session) Interrupt(context.Context) error {
	id := fmt.Sprintf("hermesh-int-%d", s.reqSeq.Add(1))
	return s.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]any{"subtype": "interrupt"},
	})
}

func (s *session) ForceStop() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() { s.proc.Stop(0) })
	return nil
}

func (s *session) Close() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() { s.proc.Stop(2 * time.Second) })
	return nil
}

func (s *session) emit(ev runtime.Event) {
	select {
	case s.events <- ev:
	case <-s.proc.Done():
	}
}

type wireLine struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	Message        json.RawMessage `json:"message"`
	RequestID      string          `json:"request_id"`
	Request        json.RawMessage `json:"request"`
	Result         string          `json:"result"`
	IsError        bool            `json:"is_error"`
	IsReplay       bool            `json:"isReplay"`
	Event          json.RawMessage `json:"event"`
	ParentToolID   string          `json:"parent_tool_use_id"`
	Errors         []string        `json:"errors"`
	TerminalReason string          `json:"terminal_reason"`
	CostUSD        float64         `json:"total_cost_usd"`
	Usage          struct {
		InputTokens  uint64 `json:"input_tokens"`
		CacheCreate  uint64 `json:"cache_creation_input_tokens"`
		CacheRead    uint64 `json:"cache_read_input_tokens"`
		OutputTokens uint64 `json:"output_tokens"`
	} `json:"usage"`
}

func (s *session) readLoop() {
	defer func() {
		s.alive.Store(false)
		close(s.events)
	}()
	_ = s.proc.Lines(func(raw []byte) {
		var l wireLine
		if json.Unmarshal(bytes.TrimSpace(raw), &l) != nil {
			return
		}
		switch l.Type {
		case "system":
			if l.Subtype == "init" && l.SessionID != "" {
				s.emit(runtime.Event{
					Kind:         runtime.EventNativeSession,
					NativeID:     l.SessionID,
					ResumeCursor: l.SessionID,
				})
			}

		case "stream_event":
			if l.ParentToolID != "" {
				return
			}
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			}
			if json.Unmarshal(l.Event, &ev) == nil && ev.Type == "content_block_delta" {
				s.mu.Lock()
				msgID := s.msgItemID
				s.mu.Unlock()
				if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
					s.emit(runtime.Event{Kind: runtime.EventAssistantDelta, ItemID: msgID, Text: ev.Delta.Text})
				} else if ev.Delta.Type == "thinking_delta" && ev.Delta.Thinking != "" {
					s.emit(runtime.Event{Kind: runtime.EventReasoningDelta, ItemID: "rsn_" + msgID, Text: ev.Delta.Thinking})
				}
			}

		case "assistant":
			var m struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			}
			if json.Unmarshal(l.Message, &m) == nil {
				s.mu.Lock()
				msgID := s.msgItemID
				s.mu.Unlock()
				for _, c := range m.Content {
					switch c.Type {
					case "text":
						if strings.TrimSpace(c.Text) != "" {
							s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: msgID, Text: c.Text})
						}
					case "tool_use":
						if c.Name != "AskUserQuestion" && c.Name != "ExitPlanMode" {
							_, cmd, _ := classifyTool(c.Name, c.Input)
							s.emit(runtime.Event{
								Kind:     runtime.EventToolStarted,
								ItemID:   c.ID,
								ToolName: c.Name,
								Command:  cmd,
							})
						}
					}
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
				Content []struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
					IsError   bool            `json:"is_error"`
				} `json:"content"`
			}
			if json.Unmarshal(l.Message, &m) == nil {
				for _, c := range m.Content {
					if c.Type == "tool_result" && c.ToolUseID != "" {
						var text string
						if json.Unmarshal(c.Content, &text) != nil {
							text = string(c.Content)
						}
						kept, _ := provider.KeepOutput(text)
						exit := int32(0)
						if c.IsError {
							exit = 1
						}
						s.emit(runtime.Event{
							Kind:     runtime.EventToolCompleted,
							ItemID:   c.ToolUseID,
							ExitCode: exit,
							Output:   kept,
						})
					}
				}
			}

		case "result":
			inTok := l.Usage.InputTokens + l.Usage.CacheCreate + l.Usage.CacheRead
			s.emit(runtime.Event{
				Kind: runtime.EventUsage,
				Usage: &threadv2.ContextWindowUsage{
					InputTokens:        inTok,
					OutputTokens:       l.Usage.OutputTokens,
					CacheReadTokens:    l.Usage.CacheRead,
					ContextUsedTokens:  inTok + l.Usage.OutputTokens,
					ContextLimitTokens: 200000,
				},
			})
			s.mu.Lock()
			if s.seen < s.sent && !l.IsError {
				s.mu.Unlock()
				return
			}
			s.inTurn = false
			// Advance msgItemID for future turns.
			s.msgItemID = fmt.Sprintf("msg_%d", s.itemSeq.Add(1))
			s.mu.Unlock()

			if l.Subtype == "interrupted" || l.TerminalReason == "interrupted" {
				s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
				return
			}
			if l.IsError {
				msg := strings.TrimSpace(strings.Join(l.Errors, "; "))
				if msg == "" {
					msg = strings.TrimSpace(l.Result)
				}
				if msg == "" {
					msg = "claude reported an error"
				}
				s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: l.Subtype, ErrorMessage: msg})
				return
			}
			s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})

		case "control_request":
			go s.handleControlRequest(l.RequestID, l.Request)
		}
	})
}

func (s *session) handleControlRequest(reqID string, raw json.RawMessage) {
	var cr struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &cr) != nil || cr.Subtype != "can_use_tool" {
		_ = s.writeJSON(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": reqID,
				"response":   map[string]any{"behavior": "allow"},
			},
		})
		return
	}

	var inputMap map[string]any
	_ = json.Unmarshal(cr.Input, &inputMap)
	if inputMap == nil {
		inputMap = map[string]any{}
	}

	s.mu.Lock()
	tctx := s.turnCtx
	s.mu.Unlock()

	reply := func(resp map[string]any) {
		_ = s.writeJSON(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": reqID,
				"response":   resp,
			},
		})
	}

	if s.spec.Gate == nil {
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})
		return
	}

	switch cr.ToolName {
	case "ExitPlanMode":
		planMD, _ := inputMap["plan"].(string)
		planID := fmt.Sprintf("plan_%d", s.itemSeq.Add(1))
		s.emit(runtime.Event{Kind: runtime.EventPlanProposed, ItemID: planID, PlanTitle: "Implementation Plan", Text: planMD})
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
			Plan: &threadv2.PlanReviewPayload{
				PlanId:   planID,
				Title:    "Implementation Plan",
				Markdown: planMD,
			},
		})
		if err != nil || !res.PlanApproved {
			msg := res.Feedback
			if msg == "" {
				msg = "Please revise the plan before exiting Plan mode."
			}
			reply(map[string]any{"behavior": "deny", "message": msg})
			return
		}
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})

	case "AskUserQuestion":
		var qPayload struct {
			Questions []struct {
				Question    string `json:"question"`
				Header      string `json:"header"`
				MultiSelect bool   `json:"multiSelect"`
				Options     []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(cr.Input, &qPayload) != nil || len(qPayload.Questions) == 0 {
			reply(map[string]any{"behavior": "deny", "message": "Malformed AskUserQuestion payload"})
			return
		}
		var qItems []*threadv2.QuestionItem
		for i, q := range qPayload.Questions {
			qid := fmt.Sprintf("q_%d", i+1)
			var opts []*threadv2.QuestionOption
			for _, o := range q.Options {
				opts = append(opts, &threadv2.QuestionOption{Id: o.Label, Label: o.Label, Description: o.Description})
			}
			qItems = append(qItems, &threadv2.QuestionItem{
				Id:          qid,
				Header:      q.Header,
				Question:    q.Question,
				Options:     opts,
				MultiSelect: q.MultiSelect,
				AllowCustom: true,
				Required:    true,
			})
		}
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind:      threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT,
			UserInput: &threadv2.UserInputPayload{Questions: qItems},
		})
		if err != nil || !res.Allowed {
			reply(map[string]any{"behavior": "deny", "message": "User declined question"})
			return
		}
		answersOut := map[string]string{}
		for i, q := range qPayload.Questions {
			qid := fmt.Sprintf("q_%d", i+1)
			if ans := res.Answers[qid]; ans != nil {
				vals := slices.Clone(ans.SelectedOptions)
				if ans.CustomText != "" {
					vals = append(vals, ans.CustomText)
				}
				answersOut[q.Question] = strings.Join(vals, ", ")
			}
		}
		inputMap["answers"] = answersOut
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})

	default:
		cat, cmd, targetPath := classifyTool(cr.ToolName, cr.Input)
		res, err := s.spec.Gate(tctx, runtime.ToolGateRequest{
			Kind: threadv2.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
			Approval: &threadv2.ToolApprovalPayload{
				ToolCategory: cat,
				ToolName:     cr.ToolName,
				Command:      cmd,
				TargetPath:   targetPath,
			},
		})
		if err != nil || !res.Allowed {
			msg := res.Feedback
			if msg == "" {
				msg = "Tool execution declined by operator"
			}
			reply(map[string]any{"behavior": "deny", "message": msg})
			if res.CancelTurn {
				_ = s.Interrupt(context.Background())
			}
			return
		}
		if cat == "file_write" && targetPath != "" {
			s.emit(runtime.Event{
				Kind:     runtime.EventFileChanged,
				ItemID:   fmt.Sprintf("file_%d", s.itemSeq.Add(1)),
				FilePath: targetPath,
			})
		}
		reply(map[string]any{"behavior": "allow", "updatedInput": inputMap})
	}
}

func classifyTool(name string, raw json.RawMessage) (category, cmd, path string) {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Command      string `json:"command"`
	}
	_ = json.Unmarshal(raw, &in)
	p := in.FilePath
	if p == "" {
		p = in.NotebookPath
	}
	if p == "" {
		p = in.Path
	}
	switch name {
	case "Read", "Glob", "Grep", "LS":
		return "read", "", p
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return "file_write", "", p
	case "Bash", "BashOutput", "KillShell":
		return "command", in.Command, ""
	case "WebFetch", "WebSearch":
		return "network", "", ""
	default:
		if strings.HasPrefix(name, "mcp__") {
			return "mcp", "", ""
		}
		return "mcp", "", p
	}
}
