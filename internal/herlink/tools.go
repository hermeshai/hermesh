// Package herlink is Her as one of the owner's agents (docs/her-mesh.md):
// the mesh tools her is offered and what runs them, and her inbox, for the
// machine she runs on. It runs CLI commands for the credentials the machine
// keeps for her, as the MCP gateway does for a connection.
package herlink

import (
	"slices"

	"github.com/hermeshai/hermesh/internal/cli"
	"github.com/hermeshai/hermesh/internal/herhost"
)

// Summary is the group's line in her catalog: always in her context, so it
// says which work goes there. The catalog keeps 250 characters of it.
const Summary = "Coding agents (Claude Code, Codex and others) in the user's projects on their own computers. " +
	"Work in one of their projects goes to a coding session here, not to spawn_agent. " +
	"Also what their sessions are doing and what they changed"

// Guidelines come with the tools' definitions when she first looks for them.
var Guidelines = []string{
	"A coding session is a coding agent at work in one project on one of the user's computers, with full access to that computer. Work in a project of the user's goes to a session. Everything else goes to spawn_agent as before. Never do a project's work yourself with bash, and never hand it to an agent of yours.",
	"To start one: project_list, unless this conversation already gave you the project's address. Name an agent, a model or an effort only when the user did.",
	"The prompt is all the coding agent knows. Give it the goal, what you already know, what counts as done and what to report, in the user's language. Never put a key or a password in it.",
	"After session_start, tell the user in one line that it has started, then end your turn. Never wait for a session or check on it: a <session_news> message wakes you when its turn ends, asks a question or fails.",
	"One job, one session. A follow-up, a correction or a retry of that job goes to the same session with session_send. A separate job gets a session of its own.",
	"When a session asks a question, answer it with session_answer if what the user asked for already settles it. Otherwise ask the user and pass their answer on. The question waits only until the time its news says.",
	"A session's result is the coding agent's own account. Before you tell the user that something changed, look at session_diff with stat. Say where the work is: a session in a worktree project leaves it on its own branch.",
	"A request to stop covers the sessions you started for that work: session_interrupt each, besides stop_agents. Say a session stopped only once the result shows it.",
	"Before you say a session is still working, look at session_get. Archive a session only when the user asks.",
	"A computer that is off is not an error: tell the user which one, and that the work can start when it is back.",
	"Every result is JSON: read `ok` first. On a failure, `error.kind` and `error.hint` say what to do next. A hint that names a command means the tool of that name (`hermesh session get` is session_get; `hermesh message send` is session_send).",
}

// tool is one of her tools: a CLI command under her own name for it.
type tool struct {
	name string
	// cmd is the CLI tool it runs (cli.Tool.Name).
	cmd    string
	desc   string
	writes bool
}

// tools are hers, of the CLI's (cli.Tools). Two names differ from the
// CLI's, because her kernel has send_message and list_agents for her own
// agents.
var tools = []tool{
	{name: "project_list", cmd: "project_list", desc: "List the projects on the user's computers. Each has an address (machine/project), what it is, how sessions work there (worktree: every session gets its own git worktree and branch; in_place: sessions work in the directory itself), the sessions running there, the coding agents it offers, and whether a session can start now (ready, paused, full, no_agent). Computers that are off are under `offline`."},
	{name: "coding_agent_list", cmd: "agent_list", desc: "List the coding agents on the user's computers, one entry per agent per computer: the models it offers, the reasoning efforts each model takes, and what a session runs with when you name none. You need this only when the user names a model or an effort."},
	{name: "session_start", cmd: "session_start", writes: true, desc: "Start a coding agent on one job in one project. Returns at once with the session; keep `session.session_id`. Its news comes to you by itself. In a worktree project the agent works on its own branch, checked out from `base` (default: the project's): to work from another branch, tag or commit, pass it as `base`, and never ask the agent to make a worktree or switch branches. In an in_place project it edits the directory itself."},
	{name: "session_send", cmd: "message_send", writes: true, desc: "Tell a session more: a follow-up, a correction, a retry after a failure. If the turn you asked for is still running, the message joins it, and answers its question if it waits on one. Otherwise it starts a new turn. Returns at once; that turn's news comes by itself. While the session's computer is off, the message waits for it."},
	{name: "session_answer", cmd: "session_answer", writes: true, desc: "Answer the question a session of yours waits on. `choose` picks options by label or number, `text` answers in words, `item` answers one part of a question with several (\"<header or id>=<option or text>\"). `decline` tells the agent that nobody will answer, and it goes on alone."},
	{name: "session_get", cmd: "session_get", desc: "One session as it is now: its status, what it is doing, its pending question, and the full result of its last turn. Use it when news was cut short, when the user asks how a session is doing, or before you say one is still working. Not for waiting."},
	{name: "session_list", cmd: "session_list", desc: "The sessions on the user's computers: yours, and those the user or their other agents started (`created_by` says whose). Filter with `machine`, `status` or `since`. You can stop, archive and answer only your own."},
	{name: "session_events", cmd: "session_events", desc: "What happened in a session, oldest first, a page at a time: messages, tool calls, turns, questions. Without a cursor, the latest `limit` events; `after` and `before` move through them. It is long: read it only when the result and the diff leave the question open."},
	{name: "session_diff", cmd: "session_diff", desc: "What a session changed, in a worktree project: everything, or one `turn`. `stat` gives the summary only: start there. An in_place project has no diff: ask the session with session_send to report its changes."},
	{name: "session_interrupt", cmd: "session_interrupt", desc: "Stop the turn in progress in a session of yours. The session and its changes stay, and session_send goes on in the same conversation."},
	{name: "session_archive", cmd: "session_archive", desc: "Put away a session of yours: it becomes read-only, its worktree is freed, and its branch keeps the work."},
	{name: "session_unarchive", cmd: "session_unarchive", desc: "Bring an archived session of yours back so that it takes messages again."},
}

// hidden are the CLI's parameters she doesn't see: hermeshd sets `as` and
// the idempotency key, she never waits, and the rest are for messages to
// agents.
var hidden = []string{"as", "idempotency_key", "wait", "timeout", "subject", "reply_to"}

// params are the descriptions that differ from the CLI's help.
var params = map[string]string{
	"project":          "Where to work: a machine/project address from project_list, or a project's name when only one computer has it",
	"prompt":           "Required: the job, in words. It is all the coding agent knows",
	"agent":            "A coding agent the project offers in project_list (for example claude or codex)",
	"model":            "A model the agent offers in coding_agent_list (default: the agent's default there)",
	"effort":           "One of the model's reasoning efforts in coding_agent_list (default: the agent's default there)",
	"base":             "Branch, tag or commit the session's worktree is checked out from (worktree projects only; default: the project's base in project_list)",
	"question_timeout": "How long the agent's questions wait for an answer before it goes on alone; 0s: nobody will answer",
	"choose":           "Option labels or numbers to pick (several for a multi-select)",
	"item":             "Answers to a question with several parts, each \"<header or id>=<option or text>\"",
}

func commands() map[string]cli.Tool {
	out := map[string]cli.Tool{}
	for _, t := range cli.Tools() {
		out[t.Name] = t
	}
	return out
}

func find(name string) (tool, bool) {
	i := slices.IndexFunc(tools, func(t tool) bool { return t.name == name })
	if i < 0 {
		return tool{}, false
	}
	return tools[i], true
}

// group is what her is offered.
func group() *herhost.MeshGroup {
	cmds := commands()
	g := &herhost.MeshGroup{Summary: Summary, Guidelines: Guidelines}
	for _, t := range tools {
		g.Tools = append(g.Tools, herhost.MeshTool{Name: t.name, Description: t.desc, Parameters: schema(t, cmds[t.cmd]), Writes: t.writes})
	}
	return g
}

// schema is a tool's parameters as JSON Schema.
func schema(t tool, c cli.Tool) map[string]any {
	if t.name == "session_send" {
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"session_id", "message"}, "properties": map[string]any{
			"session_id": map[string]any{"type": "string", "description": "The session to write to"},
			"message":    map[string]any{"type": "string", "description": "Required: what to tell it"},
		}}
	}
	props := map[string]any{}
	required := []string{}
	param := func(p cli.ToolParam) map[string]any {
		desc := params[p.Name]
		if desc == "" {
			desc = p.Help
		}
		if p.Default != "" && p.Default != "false" && p.Default != "0" && p.Default != "0s" && p.Default != "[]" {
			desc += " (default " + p.Default + ")"
		}
		switch p.Type {
		case "duration":
			return map[string]any{"type": "string", "description": desc + "; a duration such as 30m or 2h"}
		case "array":
			return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
		}
		return map[string]any{"type": p.Type, "description": desc}
	}
	if c.Arg != nil {
		props[c.Arg.Name] = param(*c.Arg)
		required = append(required, c.Arg.Name)
	}
	for _, p := range c.Params {
		if slices.Contains(hidden, p.Name) {
			continue
		}
		props[p.Name] = param(p)
		if p.Required || (t.name == "session_start" && p.Name == "prompt") {
			required = append(required, p.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
