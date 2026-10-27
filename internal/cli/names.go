package cli

// Names under an agent (docs/agent-names.md §4): several bots or chats that
// share one agent, or the conversations of one computer, each say which they
// are with --as, and messages to and from them carry the name.

// asHelp is --as's help, and the `as` parameter's in every tool.
const asHelp = "who you are under this agent, when several bots or chats share it: your own name (or your conversation's key); " +
	"replies and news come back to that name. Default: $HERMESH_AS, which `message hook` sets in Claude Code"
