package cli

// The JSON output contract (docs/cli-output.md): every command prints one
// document, {ok, exit_code, error, <data keys>}, with lowercase values,
// numbers as numbers and every field present.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// field is one key of a JSON object, in order.
type field struct {
	key string
	val any
}

// object is a JSON object whose keys keep their order.
type object []field

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.Write(k)
		b.WriteByte(':')
		v, err := encode(f.val)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// encode is json.Marshal without escaping <, > and &.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// errorJSON is the error of a document.
type errorJSON struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
	Retryable bool   `json:"retryable"`
}

// writeDoc prints a command's document: ok, exit_code and error, then the
// data keys in order.
func writeDoc(w io.Writer, err error, data ...field) {
	var ej *errorJSON
	code := exitOK
	if err != nil {
		code, ej = exitCode(err), errorView(err)
	}
	doc := append(object{{"ok", err == nil}, {"exit_code", code}, {"error", ej}}, data...)
	b, _ := encode(doc)
	var out bytes.Buffer
	json.Indent(&out, b, "", "  ")
	fmt.Fprintln(w, out.String())
}

// report prints the document of a JSON command and returns its error
// marked as reported, so Main prints nothing more.
func (a *app) report(w io.Writer, err error, data ...field) error {
	data = append(data, a.extra...)
	if a.inboxWaiting > 0 {
		data = append(data, field{"inbox", object{{"waiting", a.inboxWaiting}}})
	}
	writeDoc(w, err, data...)
	if err == nil {
		return nil
	}
	return reported(err)
}

func reported(err error) error {
	var ee *exitError
	if errors.As(err, &ee) {
		c := *ee
		c.reported = true
		return &c
	}
	return &exitError{code: exitCode(err), kind: errorKind(err), err: err, reported: true}
}

// errorKind is the kind of a failed command (docs/cli-output.md §2).
func errorKind(err error) string {
	var ee *exitError
	if errors.As(err, &ee) && ee.kind != "" {
		return ee.kind
	}
	switch {
	case errors.Is(err, errNotLoggedIn):
		return "not_paired"
	case errors.Is(err, errRevoked), errors.Is(err, transport.ErrAdmission):
		return "revoked"
	case errors.Is(err, context.DeadlineExceeded):
		return "wait_timeout"
	}
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return "denied"
	case connect.CodeUnavailable:
		return "unreachable"
	case connect.CodeInvalidArgument:
		return "usage"
	case connect.CodeNotFound:
		return "not_found"
	case connect.CodeFailedPrecondition:
		return "invalid_state"
	case connect.CodeResourceExhausted:
		return "busy"
	case connect.CodeDeadlineExceeded:
		return "wait_timeout"
	}
	switch exitCode(err) {
	case exitUsage:
		return "usage"
	case exitDenied:
		return "denied"
	case exitUnavailable:
		return "unreachable"
	case exitTimeout:
		return "wait_timeout"
	case exitNeedsInput:
		return "needs_input"
	}
	return "internal"
}

func errorView(err error) *errorJSON {
	e := &errorJSON{Kind: errorKind(err)}
	e.Retryable = exitCode(err) == exitRetryable || e.Kind == "unreachable" // it may be back later
	msg := err
	var ee *exitError
	if errors.As(err, &ee) {
		msg, e.Hint, e.Retryable = ee.err, ee.hint, e.Retryable || ee.retryable
	}
	e.Message = msg.Error()
	if ce, ok := msg.(*connect.Error); ok {
		e.Message = ce.Message() // without the code, which is the kind
	}
	return e
}

// ---------------------------------------------------------------------------
// Values.

// enumName is the contract's spelling of an enum value: "SESSION_STATUS_IDLE" → "idle".
func enumName(v fmt.Stringer, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(v.String(), prefix))
}

// timeJSON is a time in the contract's format, or nil.
func timeJSON(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return &s
}

func tsJSON(ts *timestamppb.Timestamp) *string {
	if ts == nil {
		return nil
	}
	return timeJSON(ts.AsTime())
}

type diffStatJSON struct {
	FilesChanged uint32 `json:"files_changed"`
	Insertions   uint32 `json:"insertions"`
	Deletions    uint32 `json:"deletions"`
}

func diffStatView(d *sessionv1.DiffStat) diffStatJSON {
	return diffStatJSON{d.GetFilesChanged(), d.GetInsertions(), d.GetDeletions()}
}

type usageJSON struct {
	InputTokens  uint64  `json:"input_tokens"`
	OutputTokens uint64  `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

func usageView(u *sessionv1.Usage) usageJSON {
	return usageJSON{u.GetInputTokens(), u.GetOutputTokens(), u.GetCostUsd()}
}

type failureJSON struct {
	Kind              string `json:"kind"`
	Message           string `json:"message"`
	Hint              string `json:"hint"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds uint32 `json:"retry_after_seconds"`
}

func failureView(f *sessionv1.Failure) *failureJSON {
	if f == nil {
		return nil
	}
	return &failureJSON{failureName(f.Kind), f.Message, f.Hint, f.Retryable, f.RetryAfterSeconds}
}

type questionJSON struct {
	QuestionID string     `json:"question_id"`
	Turn       uint32     `json:"turn"`
	Items      []itemJSON `json:"items"`
	AskedAt    *string    `json:"asked_at"`
	ExpiresAt  *string    `json:"expires_at"`
}

type itemJSON struct {
	ID          string       `json:"id"`
	Header      string       `json:"header"`
	Text        string       `json:"text"`
	Options     []optionJSON `json:"options"`
	MultiSelect bool         `json:"multi_select"`
	AllowOther  bool         `json:"allow_other"`
	Secret      bool         `json:"secret"`
}

type optionJSON struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func questionView(q *sessionv1.Question) *questionJSON {
	if q == nil {
		return nil
	}
	out := &questionJSON{QuestionID: q.QuestionId, Turn: q.Turn, Items: []itemJSON{}, AskedAt: tsJSON(q.AskedAt), ExpiresAt: tsJSON(q.ExpiresAt)}
	for _, it := range q.Items {
		i := itemJSON{ID: it.Id, Header: it.Header, Text: it.Text, Options: []optionJSON{}, MultiSelect: it.MultiSelect, AllowOther: it.AllowOther, Secret: it.Secret}
		for _, o := range it.Options {
			i.Options = append(i.Options, optionJSON{o.Label, o.Description})
		}
		out.Items = append(out.Items, i)
	}
	return out
}

// sessionJSON is a session as the contract shows it.
type sessionJSON struct {
	SessionID    string        `json:"session_id"`
	Title        string        `json:"title"`
	Project      string        `json:"project"`
	Machine      string        `json:"machine"`
	MachineID    string        `json:"machine_id"`
	ProjectID    string        `json:"project_id"`
	Agent        string        `json:"agent"`
	AgentSession string        `json:"agent_session_id"`
	Workspace    string        `json:"workspace"`
	Branch       string        `json:"branch"`
	Status       string        `json:"status"`
	Turns        uint32        `json:"turns"`
	CurrentTool  string        `json:"current_tool"`
	LastMessage  string        `json:"last_message"`
	Result       string        `json:"result"`
	Question     *questionJSON `json:"question"`
	LastFailure  *failureJSON  `json:"last_failure"`
	DiffStat     diffStatJSON  `json:"diff_stat"`
	Usage        usageJSON     `json:"usage"`
	LastEventSeq uint64        `json:"last_event_seq"`
	CreatedBy    string        `json:"created_by"`
	CreatedAt    *string       `json:"created_at"`
	UpdatedAt    *string       `json:"updated_at"`
}

// sessionView is s, on the machine named machine.
func sessionView(s *sessionv1.Session, machine string) *sessionJSON {
	if s == nil {
		return nil
	}
	p := s.GetProgress()
	last := "" // what the agent says during a turn; once it is over, see Result
	if turnInProgress(s.Status) {
		last = p.GetLastMessageExcerpt()
	}
	return &sessionJSON{SessionID: s.SessionId, Title: s.Title, Project: machine + "/" + s.ProjectId, Machine: machine, MachineID: s.MachineId, ProjectID: s.ProjectId,
		Agent: s.Agent, AgentSession: s.ProviderSessionRef, Workspace: workspaceName(s.Workspace), Branch: s.Branch, Status: statusName(s.Status),
		Turns: p.GetTurnCount(), CurrentTool: p.GetCurrentTool(), LastMessage: last, Result: s.Result,
		Question: questionView(p.GetPendingQuestion()), LastFailure: failureView(p.GetLastFailure()), DiffStat: diffStatView(p.GetDiffStat()),
		Usage: usageView(p.GetUsage()), LastEventSeq: p.GetLastEventSeq(), CreatedBy: s.CreatedBy, CreatedAt: tsJSON(s.CreatedAt), UpdatedAt: tsJSON(s.UpdatedAt)}
}

// eventView is an event as the contract shows it: common fields, then
// the fields of its type.
func eventView(e *sessionv1.SessionEvent) object {
	turn, _ := strconv.Atoi(e.TurnId)
	o := object{{"seq", e.Seq}, {"ts", tsJSON(e.Ts)}, {"turn", turn}}
	typed := func(t string, fs ...field) object { return append(append(o, field{"type", t}), fs...) }
	switch k := e.Kind.(type) {
	case *sessionv1.SessionEvent_Status:
		return typed("status", field{"status", statusName(k.Status.Status)}, field{"reason", k.Status.Reason})
	case *sessionv1.SessionEvent_TurnStarted:
		return typed("turn_started", field{"asker", k.TurnStarted.Asker})
	case *sessionv1.SessionEvent_MessageReceived:
		m := k.MessageReceived
		return typed("message_received", field{"message_id", m.MessageId}, field{"from", m.From}, field{"from_name", m.FromName}, field{"text", m.Text},
			field{"in_reply_to", m.InReplyTo}, field{"delivered", m.Delivered})
	case *sessionv1.SessionEvent_MessageSent:
		m := k.MessageSent
		return typed("message_sent", field{"message_id", m.MessageId}, field{"to", m.To}, field{"to_name", m.ToName}, field{"text", m.Text}, field{"in_reply_to", m.InReplyTo})
	case *sessionv1.SessionEvent_UserMessage:
		atts := []object{}
		for _, f := range k.UserMessage.Attachments {
			atts = append(atts, object{{"name", f.Name}, {"media_type", f.MediaType}, {"size", f.Size}, {"path", f.Path}, {"sha256", f.Sha256}, {"image", f.Image}})
		}
		return typed("user_message", field{"text", k.UserMessage.Text}, field{"attachments", atts})
	case *sessionv1.SessionEvent_AgentMessage:
		return typed("agent_message", field{"text", k.AgentMessage.Text})
	case *sessionv1.SessionEvent_ToolCall:
		c := k.ToolCall
		return typed("tool_call", field{"call_id", c.CallId}, field{"kind", c.Kind}, field{"name", c.Name}, field{"summary", c.Summary},
			field{"decision", c.Decision}, field{"reason", c.Reason})
	case *sessionv1.SessionEvent_ToolResult:
		r := k.ToolResult
		return typed("tool_result", field{"call_id", r.CallId}, field{"summary", r.Summary}, field{"is_error", r.IsError}, field{"truncated", r.Truncated})
	case *sessionv1.SessionEvent_TurnCompleted:
		tc := k.TurnCompleted
		return typed("turn_completed", field{"result", tc.Result}, field{"is_error", tc.IsError}, field{"failure", failureView(tc.Failure)},
			field{"diff_stat", diffStatView(tc.DiffStat)}, field{"usage", usageView(tc.Usage)}, field{"commit", tc.Commit})
	case *sessionv1.SessionEvent_Error:
		return typed("error", field{"message", k.Error.Message}, field{"failure", failureView(k.Error.Failure)})
	case *sessionv1.SessionEvent_QuestionAsked:
		return typed("question_asked", field{"question", questionView(k.QuestionAsked.Question)})
	case *sessionv1.SessionEvent_QuestionResolved:
		r := k.QuestionResolved
		answers := []object{}
		for _, x := range r.Answers {
			sel := x.Selected
			if sel == nil {
				sel = []string{}
			}
			answers = append(answers, object{{"item_id", x.ItemId}, {"selected", sel}, {"text", x.Text}})
		}
		return typed("question_resolved", field{"question_id", r.QuestionId}, field{"resolution", enumName(r.Resolution, "QUESTION_RESOLUTION_")},
			field{"by", r.By}, field{"answers", answers})
	}
	return typed("unknown")
}
