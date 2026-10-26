// Package provider defines the interface between the session kernel and
// coding-agent CLIs (Claude Code, Codex, ACP agents), plus shared process
// plumbing.
package provider

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// Info describes a provider on this machine.
type Info struct {
	Name      string
	Available bool
	Binary    string
	Version   string
	LoggedIn  bool
	Detail    string
	// Models it offers and its own configuration, as far as it tells
	// (Claude Code and Codex), for picking a machine's defaults.
	Models []Model
	Own    Settings
}

// Model is one model an agent offers.
type Model struct {
	ID, Name string
	Efforts  []string
}

// Settings are what a session runs with; empty ones follow the agent's own
// configuration.
type Settings struct {
	Model, Effort string
	Speed         identityv1.AgentSpeed
}

type ToolKind string

const (
	ToolRead    ToolKind = "read"
	ToolSearch  ToolKind = "search"
	ToolEdit    ToolKind = "edit"
	ToolShell   ToolKind = "shell"
	ToolNetwork ToolKind = "network"
	ToolMCP     ToolKind = "mcp"
	ToolOther   ToolKind = "other"
)

// ToolAction is a normalized tool call the gate decides on.
type ToolAction struct {
	CallID  string
	Kind    ToolKind
	Name    string
	Paths   []string // as given by the agent (absolute or relative)
	Command string
	Summary string
	Raw     json.RawMessage
}

type Decision struct {
	Allow  bool
	Reason string
}

// Gate decides on tool calls. It must be safe for concurrent use.
type Gate func(ctx context.Context, a ToolAction) Decision

type EventKind int

const (
	EventRef EventKind = iota + 1 // provider session reference became known
	EventAgentMessage
	EventToolCall
	EventToolResult
	EventTurnCompleted
	EventError
	EventQuestion // the agent asks the user (Question); answer with Session.Answer
	// EventAgentDraft is reply text the agent is still writing (Text: the
	// next piece). The EventAgentMessage that follows carries all of it.
	EventAgentDraft
)

// Question is a question the agent asks mid-turn.
type Question struct {
	// ID is the provider's handle for Answer.
	ID    string
	Items []QuestionItem
}

type QuestionItem struct {
	ID, Header, Text string
	Options          []Option
	MultiSelect      bool
	AllowOther       bool // free text accepted (always, without options)
	Secret           bool
	Optional         bool // may be left unanswered
}

type Option struct{ Label, Description string }

// Answer answers one QuestionItem.
type Answer struct {
	ItemID   string
	Selected []string // option labels
	Text     string
}

// Reply resolves a question: Answers, or (Declined) none, with Message
// telling the agent why and what to do.
type Reply struct {
	Answers  []Answer
	Declined bool
	Message  string
}

// Value is an answer as one string: the chosen labels and the text,
// comma-separated.
func (a Answer) Value() string {
	v := append([]string{}, a.Selected...)
	if t := strings.TrimSpace(a.Text); t != "" {
		v = append(v, t)
	}
	return strings.Join(v, ", ")
}

type Usage struct {
	InputTokens  uint64
	OutputTokens uint64
	CostUSD      float64
}

// Event is a normalized provider event.
type Event struct {
	Kind EventKind

	Ref string // EventRef

	Text string // AgentMessage, AgentDraft, TurnCompleted (result), Error

	CallID   string // ToolCall, ToolResult
	ToolKind ToolKind
	ToolName string
	Summary  string
	Decision *Decision // ToolCall: set when the gate decided

	IsError   bool
	Truncated bool
	Usage     Usage    // TurnCompleted
	Failure   *Failure // TurnCompleted with IsError, Error: why (nil: classify Text)
	Question  *Question
}

// Image is an attached image file to show the agent.
type Image struct {
	Path      string
	MediaType string
}

// Input is a user message: its text (which lists attached files by path)
// and the attached images.
type Input struct {
	Text   string
	Images []Image
}

// StartSpec describes one provider process.
type StartSpec struct {
	WorkDir string
	TempDir string
	Prompt  string
	Images  []Image // with the first prompt
	// ReadDirs are directories outside WorkDir the agent must be able to
	// read (the session's attached files).
	ReadDirs []string
	Model    string
	// Effort and Speed, like Model, override the agent's own configuration
	// for this session only (Claude Code and Codex; Speed: Codex).
	Effort string
	Speed  identityv1.AgentSpeed
	// ResumeRef resumes a previous provider session (Claude session id or
	// Codex thread id) instead of starting a new one.
	ResumeRef string
	// NewRef is the session id to use for a new Claude session.
	NewRef string
	Env    []string
	Gate   Gate
}

// Session is a running provider process. The first turn starts with
// StartSpec.Prompt; Send starts the next turn once the previous completed.
type Session interface {
	Events() <-chan Event
	Send(ctx context.Context, in Input) error
	// Steer adds a message to the running turn. The turn's EventTurnCompleted
	// then comes only after the agent has handled it. An error means the
	// message was not taken (no turn is running, or the agent cannot take
	// input mid-turn); the caller delivers it as the next turn instead.
	Steer(ctx context.Context, in Input) error
	// Answer resolves a question from EventQuestion; the agent goes on.
	Answer(ctx context.Context, questionID string, r Reply) error
	Interrupt(ctx context.Context) error
	// Close stops the process (gracefully, then by killing its group).
	Close() error
	// Done is closed when the process has exited; Err then reports why.
	Done() <-chan struct{}
	Err() error
	Pgid() int
	// LastActivity is when the agent last produced any output, including
	// output that becomes no event (zero: none yet). The kernel's liveness
	// checks use it.
	LastActivity() time.Time
}

type Provider interface {
	Name() string
	Discover(ctx context.Context) Info
	Start(ctx context.Context, spec StartSpec) (Session, error)
}

// Markdown that CleanTitle drops from a title: a line that is only a code
// fence or a rule, block markers at the start (heading, quote, list item),
// links (keeping their text), ** * ~~ emphasis and code marks. Underscores
// stay (no _emphasis_), so names like __init__.py and snake_case keep them.
var (
	mdSkip   = regexp.MustCompile("^(```|~~~|(-\\s*){3,}$|(\\*\\s*){3,}$|(_\\s*){3,}$)")
	mdBlock  = regexp.MustCompile(`^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+|\[[ xX]\]\s+)+`)
	mdLink   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdStrong = regexp.MustCompile(`(\*\*|~~)(\S(?:.*?\S)?)(\*\*|~~)`)
	mdEm     = regexp.MustCompile(`(^|[^\w*])\*(\S(?:[^*]*?\S)?)\*`)
	mdSpace  = regexp.MustCompile(`\s+`)
)

// CleanTitle makes text one short line for a list: its first line with
// words in it, without Markdown, wrapping quotes or a final period, cut to
// 40 characters.
func CleanTitle(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if mdSkip.MatchString(t) {
			continue
		}
		t = mdBlock.ReplaceAllString(t, "")
		t = mdLink.ReplaceAllString(t, "$1")
		t = mdStrong.ReplaceAllString(t, "$2")
		t = mdEm.ReplaceAllString(t, "$1$2")
		t = strings.ReplaceAll(t, "`", "")
		t = mdSpace.ReplaceAllString(strings.TrimSpace(t), " ")
		t = strings.TrimRight(t, "。.")
		t = strings.TrimSpace(strings.Trim(t, "\"'“”‘’「」"))
		t = strings.TrimSpace(strings.TrimRight(t, "。."))
		if t == "" {
			continue
		}
		if r := []rune(t); len(r) > 40 {
			return strings.TrimSpace(string(r[:40])) + "…"
		}
		return t
	}
	return ""
}
