# The `hermesh` CLI, for agents

Agents that run on a computer (Claude Code, Codex, Cursor, your own) use
Hermesh through the `hermesh` command line. Install and sign in:

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
hermesh auth login --name "Codex on MacBook"   # or: hermesh auth login <connection address>
```

`hermesh auth status` checks pairing step by step (the local identity, the grant, the relay connection, whether the grant is current) and gives the next command when something is wrong. If you were revoked or the grant expired, run `hermesh auth login` again: it applies afresh, no arguments needed. To switch owners, run `hermesh auth logout` first.

Commands are "noun + action": `auth`, `project`, `session` and `message`, each with its actions (`hermesh <noun> --help`). The agent calling the CLI is the **client**; the agent doing the work on the machine is the **coding agent** (`--agent claude`). Over MCP, the same commands are tools (`project_list`, `session_start`, …; [docs/mcp-gateway.md](mcp-gateway.md) §7).

```bash
hermesh project list                # where you can work: machine/project, the owner's notes, branch, platform, usable coding agents, whether a session can start now
hermesh session start --project vps-1/demo --agent claude --wait --prompt "Fix the bug on the login page"   # demo alone is enough if only one machine has it
hermesh session start --project demo --title "Login page bug" --prompt "..."   # a title for the app and session list; default: the prompt's first line
hermesh message send --to s_xxx --wait --message "Add one more test"   # the one way to tell a session anything; while it runs, the message joins your turn
hermesh message send --to s_xxx --wait -f screenshot.png -f spec.pdf --message "Change it to match these"   # attach files
hermesh session answer s_xxx --wait --choose 2   # answer the agent's question (also --text, --decline, or just write to the session)
hermesh session diff s_xxx
hermesh session list                # your sessions (--archived for archived ones)
hermesh session interrupt s_xxx     # stop the running turn; the session stays, and a later message continues it
hermesh session archive s_xxx       # when done: read-only, frees the worktree (the branch keeps the changes); unarchive restores it
hermesh session events s_xxx --after 812   # events page by page: the latest 50 by default; --after newer, --before older
hermesh message list --wait         # your inbox: news of your sessions, messages from the owner's other agents
hermesh --as "Tech Demos" session start --project web --prompt "…"   # several bots share this agent? each passes its own name: its news and replies come back to it alone
hermesh --as "Tech Demos" message wake   # started by a webhook (a Grok Bot routine)? a one-time link for your owner, who pastes its URL and key: then Hermesh wakes you when messages arrive
hermesh message ack msg_xxx         # handled: it leaves the inbox
hermesh message send --to "Grok Bot" --message "PR #143 is ready"   # to another of the owner's agents (message contacts lists them)
```

**Messages to a session.** `message send --to <session>` goes straight to its machine when it's online. If you asked for the turn running now, it joins it (Claude Code and Codex take it at the next tool boundary) and answers its question; otherwise it starts a turn of its own, or waits for one, so one agent's words never go into another's turn. Any agent whose grant covers the session and that may see it can write to it; only the agent that started it can interrupt or archive it. While the machine is offline the message waits for it (files need it online). `session send` still works for now, as an alias.

**Your inbox.** When a turn of a session you started ends, asks a question or fails while you aren't waiting on it (no `--wait` running), its news comes to your inbox as a message, sealed end to end, and waits there, even while you're offline, until you acknowledge it (30 days at most). Every command's JSON says `inbox.waiting` while messages wait. `message status` tells what became of a message, `message contacts` lists the owner's agents and machines. See [docs/agent-messages.md](agent-messages.md).

**Claude Code and Codex on your computer.** `hermesh message hook claude --install` (or `codex`) adds Hermesh's hooks to the agent's user settings. Each conversation then gets the messages addressed to it, such as news of the sessions it started and replies to what it wrote: at the start of its turns and when a turn would end, and in Claude Code also between tool calls and while idle (a new turn). Messages addressed to no conversation stay in the inbox; the hooks only say how many wait. In Claude Code the conversation's commands know its key (`HERMESH_AS`); from Codex, add `--as <key>` to the commands.

**Sessions never end on their own.** However long one has been idle, another message continues the same coding agent session.

**Questions mid-turn.** Claude Code (AskUserQuestion), Codex and ACP agents with form questions can ask in the middle of a turn. The session goes to `waiting_input`; `--wait` returns at once with exit code 8 and prints the question and options. Answer with `session answer` (`--choose` a label or number, `--text`, or `--decline`), with `session send` (the message is the answer), or on the session page in the app. Unanswered, a question expires after 30 minutes by default and the agent is told to use its own judgment and state its assumptions; `session start --question-timeout 0` means nobody will answer in this session. Answers to secret questions go only to the agent and aren't recorded in events.

**Projects** are added by the owner in the app. Any directory can be one, with a worktree switch: **separate worktree** (the default for a git repository root: each session on its own branch `hermesh/<session>` from `--base`, a snapshot every turn, changes shown by `session diff`; ignored local files named in the repository's `.worktreeinclude` are copied in) or **in place** (the only option outside git: the agent reads and writes the directory directly, as if you ran it there yourself; no branch, no commits, no diff, and concurrent sessions coordinate among themselves).

**Files.** `-f/--file` can be repeated, up to 20 files and 32 MiB per message. They're saved in the session's attachments directory on the machine (outside the worktree, so never in the diff), and the message lists their paths; PNG, JPEG, GIF and WebP images up to 3.5 MiB also go to the agent as images.

**Waiting.** `session start`, `send` and `answer` return once the message is delivered (stderr suggests `hermesh session get <id>`; after the turn, the session's `result` is the agent's full reply). With `--wait` they wait for the turn to end and the exit code says how it went; from a tool with a time limit, add a shorter `--timeout` (exit code 6; the turn keeps going, follow up with `get`). The session ID is always the first line on stderr, so it's known even if the process is killed midway. The message comes from `--prompt` / `--message` or a file (`--prompt-file`, `--message-file`; `-` for stdin); stdin is never read unless asked for.

**Output.** Text for people by default; with `-o json` (or `HERMESH_OUTPUT=json`) stdout is one JSON document with the same outer shape for every command: `ok`, `exit_code`, `error` (`{kind, message, hint, retryable}` on failure, null on success), and data under named keys (`session`, `sessions`, `projects`, `events`). An offline machine isn't an error: lists come back with it under `offline`; a command fails with exit code 4 only when the machine it needs is offline. See [docs/cli-output.md](cli-output.md).

**Exit codes.** 0 success; 1 internal error; 2 bad arguments, not found, or not allowed now (like sending to an archived session); 3 not paired, revoked or not authorized; 4 relay or the needed machine offline or unreachable (retryable); 5 session failed; 6 timeout; 7 transient failure (rate limit, network, server error, agent crashed or stuck: send again); 8 the agent is waiting for an answer. A session's latest failure is in `session.last_failure` (`kind`, whether it's retryable, a `hint`); a session that crashed, got stuck or failed to start is `crashed`, and the next message continues with its context.
