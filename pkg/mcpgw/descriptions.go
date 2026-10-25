package mcpgw

import "github.com/hermeshai/hermesh/internal/cli"

// MCP descriptions use tool and parameter names, independently of CLI help.
var toolDescriptions = map[string]string{
	"auth_status":       `Check this connection's identity, grant, relay connectivity and whether the grant is current. Use this for access diagnostics after approval; use hermesh_status for setup, approval progress and the user's next step. The auth object includes checks and the grant's scope and visibility.`,
	"project_list":      `List accessible projects on the user's reachable machines. Each projects entry has a project address (machine/project), description, workspace (worktree or in_place), active_sessions, platform, available agents and state (ready, paused, full or no_agent). Pass its project and an agent's name to session_start. A project name alone also works if unique across machines. Unreachable machines appear in offline; an empty projects list can still have ok=true. If a note is present, relay its setup or approval instructions to the user.`,
	"session_start":     `Start a coding-agent session in project on the user's selected machine. Choose project and agent from project_list. A non-empty prompt is required: omitting it or passing an empty string fails without creating a session. Returns the session object; save session.session_id and pass it as session_id to subsequent tools. By default, returns without waiting for the turn to finish. In in_place mode the agent directly edits the project's existing working directory; in worktree mode it edits a separate session worktree. Follow with session_get for progress and results, session_events for messages and tool calls, and session_answer for a pending question.`,
	"session_get":       `Get a visible session by session_id: status, progress, pending question and the agent's full result once a turn ends. Poll this after session_start, session_send or session_answer, including after a wait_timeout. Use session_events for event history and session_answer when the agent is waiting for input.`,
	"session_list":      `List sessions visible to this connection on reachable machines. The grant's visibility controls which sessions appear: own shows this client's sessions; all also shows sessions started by other clients in permitted projects. Visibility grants read access, not control: this connection can send, answer, interrupt, archive or unarchive only sessions it started. Use auth_status to inspect the grant. Archived sessions are excluded by default; archived=true lists archived sessions instead.`,
	"session_events":    `Read a visible session's events (messages, tool calls, turns and questions), oldest first within each page. Without a cursor, returns the latest limit events. Set after to the last seq seen (or session.last_event_seq from session_get) for newer events, or before for older events; do not set both. The page object contains first_seq, last_seq, has_more_before and has_more_after. Use page.last_seq as after or page.first_seq as before for the next page. With after and no new events, events is empty and page.last_seq keeps that cursor.`,
	"session_send":      `Send a non-empty message to a session started by this connection. An idle session starts a new turn. During a running turn, Claude Code and Codex take the message at the next tool boundary; agents accepting input only between turns (ACP) run it as the next turn. A message also answers a pending question, though session_answer supports structured answers. With wait=true, waits until the agent has handled every message sent so far.`,
	"session_answer":    `Answer a pending question in a session started by this connection. Read the question with session_get first. Use choose as an array of option labels or numbers, text for a free-text answer, or item as an array of "<header or id>=<option or text>" strings for a multi-part question. question defaults to the pending question's id. Set decline=true if nobody will answer, so the agent continues on its own.`,
	"session_interrupt": `Stop the turn in progress in a session started by this connection. The session and its existing changes remain; session_send continues the same conversation.`,
	"session_archive":   `Archive a session started by this connection when you are done with it. It becomes read-only; for worktree projects, its worktree is freed and its branch keeps the work.`,
	"session_unarchive": `Restore an archived session started by this connection so it accepts messages again. For worktree projects, its worktree is restored.`,
	"session_diff":      `Show a visible session's changes in a worktree project: cumulative by default, or for one turn using turn; stat=true returns only the summary. This tool does not support in_place projects. For an in_place session you started, use session_send with message asking the agent to inspect and report changes (for a Git repository, run git diff, git diff --cached and git status), then read its result with session_get. These are working-directory changes and may include work from other sessions or the user.`,
}

const waitDescription = `With wait=true, timeout limits the tool call, not the agent's execution. In MCP, an omitted timeout, "0s", or a value above 50s is capped at 50s; "0s" does not mean unlimited waiting. Hosted clients should prefer wait=false and poll session_get, or use timeout="30s" (at most 50s). On error.kind="wait_timeout", the turn continues: use session.session_id to poll session_get instead of repeating the action.`

func toolDescription(t cli.Tool) string {
	desc := toolDescriptions[t.Name]
	if desc == "" {
		desc = t.Description
	}
	if t.Name == "session_start" || t.Name == "session_send" || t.Name == "session_answer" {
		desc += "\n\n" + waitDescription
	}
	return desc
}

func parameterDescription(t cli.Tool, p cli.ToolParam) string {
	switch p.Name {
	case "project":
		return "Where to work: a machine/project address from project_list, or a project name if unique across machines"
	case "agent":
		return "Coding agent name from the project's agents in project_list (for example claude or codex)"
	case "prompt":
		return "Required non-empty prompt text; omitting it or passing an empty string does not create a session"
	case "message":
		return "Required non-empty message text"
	case "choose":
		return "Array of option labels or numbers to pick (multiple entries for multi-select)"
	case "item":
		return "Array of answers for a multi-part question, each formatted as <header or id>=<option or text>"
	case "question_timeout":
		return "How long the agent waits for answers to its questions before continuing alone; 0s means nobody will answer, not unlimited waiting"
	case "timeout":
		return waitDescription + " With wait=false, a positive timeout still limits the call; omitted or 0s adds no deadline."
	case "wait":
		if t.Name == "session_send" {
			return "Wait until the agent has handled every message sent so far; otherwise return immediately and poll session_get"
		}
		return "Wait until the turn finishes and return its result; otherwise return immediately and poll session_get"
	}
	return p.Help
}
