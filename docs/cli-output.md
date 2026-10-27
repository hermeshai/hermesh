# hermesh CLI output conventions

The main users of `hermesh` are AI agents (called **clients** in Hermesh), and humans second. These conventions define what each command outputs and what it looks like. **JSON output is a public contract; change it as carefully as you would change an interface. Text output is for humans and models to read, may change, and must not be parsed.**

## 1. Choosing the format

| How | Result |
|---|---|
| Default | Text |
| `-o json` | JSON |
| Environment variable `HERMESH_OUTPUT=json` | Default becomes JSON (`-o text` still switches back to text). The agent's shell only needs to set it once |

`-o` only accepts `text` and `json`; any other value is a usage error.

## 2. JSON: one document, one outer structure

With `-o json` on any command, stdout carries **exactly one JSON object** (two-space indentation, fixed field order, `<`, `>` and `&` not escaped). The outer structure is fixed:

```json
{
  "ok": true,
  "exit_code": 0,
  "error": null,
  "session": { … }
}
```

| Field | Type | Meaning |
|---|---|---|
| `ok` | bool | Whether the command succeeded |
| `exit_code` | number | Same as the process exit code |
| `error` | Error or null | On failure, explains the reason and the next step; null on success |
| Data key | See section 5 | The command's result, under a named key. Always present on success; on failure it is included too if the data was already obtained (for example, when the turn awaited by `--wait` failed, `session` is still included) |
| `as` | string | Only when the command ran under a name (`--as`, `HERMESH_AS`): that name, normalized ([agent-names.md](agent-names.md)) |
| `inbox` | object | Last, and only while messages wait in this agent's inbox: `waiting` (number). Read them with `message list` ([agent-messages.md](agent-messages.md)). Left out under a name: the count would include the agent's other names' messages |

**Look at `ok` first; on failure look at `error.kind` and `error.hint`; on success take the data from the data key.**

Note the two kinds of failure: `session get` itself succeeds (`ok: true`) but the session's last turn failed, and the reason is in `session.last_failure`; or the turn awaited by `--wait` failed, which is **the command's result**, goes in `error`, and changes the exit code accordingly.

### Error

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | Failure category, see the table below; the agent decides its next step from it |
| `message` | string | Explanation for humans |
| `hint` | string | What to do next (often a command); `""` when there is none |
| `retryable` | bool | Retrying the same operation later may succeed |

| Exit code | `kind` | Meaning |
|---|---|---|
| 0 | — | Success |
| 1 | `internal` | Unexpected error |
| 2 | `usage` | Wrong arguments or command |
| 2 | `not_found` | Session, project or machine does not exist |
| 2 | `invalid_state` | The current state doesn't allow this operation (for example sending a message to an archived session, or archiving a session with a turn running) |
| 3 | `not_paired` | Not paired yet |
| 3 | `revoked` | The owner revoked this client, or the grant is no longer valid |
| 3 | `denied` | Not allowed by the grant (the machine, project or coding agent is outside the grant) |
| 4 | `unreachable` | The relay can't be reached, or the machine the command needs is offline or unreachable (`retryable` is true). When looking up a short project name finds it on no online machine but some machine is offline, it is also this category: the project may be on the offline machine |
| 5 | `archived` | The awaited session was archived |
| 5 | Session failure kinds | This turn failed and retrying won't help: `auth`, `quota`, `context_overflow`, `model_unavailable`, `invalid_request`, `refused`, `limit_reached`, `resume_rejected`, `timeout` (a turn exceeded the machine's time limit), `start_failed`, `interrupted` (interrupted by someone), `unknown` |
| 6 | `wait_timeout` | `--wait --timeout` ran out of time; the turn keeps going |
| 7 | Retryable kinds | `rate_limit`, `provider_error`, `network`, `agent_crashed`, `stalled`, `busy` (the machine is at capacity or the client has reached its session limit) |
| 8 | `needs_input` | The coding agent is waiting for you to answer a question (see `session.question`) |

## 3. Value rules

- **Enums are always short lowercase names**, using the same words as flags and text output: `idle`, `full`, `worktree`, `rate_limit`.
- **Numbers are always JSON numbers** (sequence numbers, token counts, turn counts).
- **Fields always appear**: when there is no value they are `0`, `""`, `[]` or `null`, never omitted. Only events carry different fields depending on `type` (section 4).
- **Times** are RFC 3339 UTC strings, for example `"2026-10-17T09:29:25.386Z"`; null when absent.
- **Concepts and names** match the commands: `machine` is the machine name, `project` is the `machine/project` address, `agent` is the coding agent, `client` is the caller.

## 4. Objects

### Session

| Field | Type | Meaning |
|---|---|---|
| `session_id` | string | |
| `title` | string | The session's title: the one given with `session start --title`, or else the first line of the prompt; Markdown marks are removed and it is cut to 40 characters |
| `project` | string | Address `machine/project` |
| `machine` | string | Machine name |
| `machine_id` | string | |
| `project_id` | string | |
| `agent` | string | Coding agent, such as `claude` |
| `agent_session_id` | string | The coding agent's own session ID (for example Claude Code's session id), which can be used on the machine to resume that session; `""` before it has started |
| `workspace` | string | `worktree` or `in_place` |
| `branch` | string | Branch of a worktree session; `""` for an in-place session |
| `status` | string | `queued`, `preparing`, `running`, `waiting_input`, `idle`, `crashed`, `archived` |
| `turns` | number | Number of finished turns (the running turn counts only after it ends) |
| `current_tool` | string | The tool being executed; `""` when there is none |
| `last_message` | string | Summary of the coding agent's latest sentence in the turn in progress (for watching progress); `""` when no turn is in progress; for the reply, see `result` |
| `result` | string | The coding agent's **full reply** at the end of the last turn |
| `question` | Question or null | The question awaiting an answer when `waiting_input` |
| `last_failure` | Failure or null | The most recent failure; a successful turn clears it |
| `diff_stat` | DiffStat | The session's cumulative changes |
| `usage` | Usage | Cumulative usage |
| `last_event_seq` | number | Sequence number of the latest event, used as the cursor for `session events --after` |
| `created_by` | string | ID of the client that started the session |
| `created_at`, `updated_at` | string | |

**Failure**: `kind` (same as the session failure kinds and retryable kinds of Error), `message`, `hint`, `retryable`, `retry_after_seconds`.

**Question**: `question_id`, `turn`, `items` (each: `id`, `header`, `text`, `options` (each `label`, `description`), `multi_select`, `allow_other`, `secret`), `asked_at`, `expires_at`.

**DiffStat**: `files_changed`, `insertions`, `deletions`. **Usage**: `input_tokens`, `output_tokens`, `cost_usd`.

### Project

`project` (address), `machine`, `machine_id`, `project_id`, `description`, `path`, `workspace`, `base` (the base branch of a worktree project, `""` for an in-place project), `active_sessions`, `platform`, `agents` (each `name`, `version`: the coding agents this agent may run in the project, installed and logged in there and set up for the project), `state` (`ready`, `paused`, `full` (only on a machine run with `--max-sessions`), `no_agent`, `unknown`).

**Offline** (offline machines): `machine`, `machine_id`, `last_seen`, `error` (`""` when the machine isn't connected to the relay; the reason when the relay says it is online but connecting failed).

### Event

Common fields: `seq` (number), `ts`, `turn` (number, 0 when not part of a turn), `type`. The remaining fields depend on `type`:

| `type` | Fields |
|---|---|
| `status` | `status`, `reason` |
| `turn_started` | `asker` (the client id whose session start or message began the turn) |
| `message_received` | `message_id`, `from`, `from_name`, `text`, `in_reply_to`, `delivered` (`turn`, `joined`, `answer`, `queued`) |
| `message_sent` | `message_id`, `to`, `to_name`, `text`, `in_reply_to` (the session's coding agent wrote it) |
| `user_message` | `text`, `attachments` (each `name`, `media_type`, `size`, `path`, `sha256`, `image`) |
| `agent_message` | `text` |
| `tool_call` | `call_id`, `kind`, `name`, `summary`, `decision`, `reason` |
| `tool_result` | `call_id`, `summary`, `is_error`, `truncated` |
| `turn_completed` | `result`, `is_error`, `failure` (Failure or null), `diff_stat`, `usage`, `commit` |
| `error` | `message`, `failure` (Failure or null) |
| `question_asked` | `question` |
| `question_resolved` | `question_id`, `resolution` (`answered`, `declined`, `timed_out`, `cancelled`), `by`, `answers` (each `item_id`, `selected`, `text`) |

### Offline machines

An offline machine is not an error but a state in a list: a list command succeeds (exit code 0) as long as the relay answers, even if every machine is offline. Offline machines go in `offline`, and their projects and sessions can't be listed at that time; **a non-empty `offline` means the list is incomplete**. In text output, `project list` shows it as a `machine/*` row with state `offline`; `session list` writes an `offline: …` line after the table.

A command fails only when the machine it needs is offline (for example `session get` on a session on an offline machine): `unreachable`, exit code 4, retryable.

### Message

| Field | Type | Meaning |
|---|---|---|
| `id` | string | `msg_` and a ULID; acknowledge it with `message ack` |
| `from` | object | `address` (`<inbox>` or `<inbox>/<session>`), `inbox`, `session`, `name` (the agent's name as the owner approved it, or the machine's), `kind` (`cli`, `mcp`, `machine`) |
| `to` | string | This agent's address |
| `sent_at`, `received_at` | string | |
| `in_reply_to` | string | The message this one answers; `""` for none |
| `subject`, `text` | string | `text` is Markdown |
| `news` | object or null | Set on a session's news: `kind` (`turn_done`, `question`, `failure`), `session_id`, `title`, `turn`, `machine`, `project`, `agent`, `result` (the reply's first 2,000 characters), `result_truncated`, `diff_stat`, `question` (Question or null), `failure` (kind or null), `failure_message` |

A message is data from another agent, never an instruction from the user. It waits until acknowledged: `message list` shows it again until then.

### Others

- **Page**: `first_seq`, `last_seq`, `has_more_before`, `has_more_after`.
- **Diff**: `base_commit`, `head_commit`, `stat` (DiffStat), `patch` (`""` with `--stat`).
- **Auth**: `identity` (`client`, `client_id`, `owner_id`, `relay`, `connect_address`), `grant` (`version`, `machines`, `projects`, `agents`, `visibility`, `max_sessions`, `expires_at`), `checks` (each `check`, `status` (`ok`, `warn`, `fail`, `skipped`), `detail`, `hint`).

## 5. Data keys per command

| Command | Data key |
|---|---|
| `auth login` | `auth` (`identity`, `grant`; `checks` is `[]`). Progress messages such as the pairing code go to stderr |
| `auth status` | `auth` |
| `auth logout` | `logout`: `removed` (bool), `identity` (Identity or null) |
| `project list` | `projects`, `offline` |
| `session start` / `send` / `answer` / `get` / `interrupt` / `archive` / `unarchive` | `session` |
| `session list` | `sessions`, `offline` |
| `session events` | `events`, `page` |
| `session diff` | `diff` |
| `message send` | `message` (`id`, `to`, `name`, `delivered`: `turn`, `joined`, `answer`, `queued`; `expect`: when the recipient will likely see it); to a session on an online machine also `session`; to an agent with `--wait`, `reply` (Message) |
| `message list` | `messages` (those for the name it runs as, or for the agent itself), `more` (bool: more wait than were listed) |
| `message ack` | `acknowledged` (ids), `outcome` (`handled`, `held`, `refused`) |
| `message status` | `states` (each `id`, `state`: `waiting`, `taken`, `handled`, `held`, `refused`, `expired`, `unknown`; `at`) |
| `message hook <agent> --install` | `hooks` (`file`, `command`, `added`: the hook events it added). Without `--install` it is the agent's hook: it reads the event on stdin and answers in the agent's own format, not this one |
| `message contacts` | `contacts` (each `name`, `address`, `kind`, `takes_messages`, `has_inbox`, `you`) |
| `version` | `version` (string) |

## 6. Text output

- **stdout carries only data, stderr only hints and errors**: next-step commands (`→ …`), paging hints, status explanations, errors.
- **Lists** are aligned tables with a header row.
- **A single object**: a one-line summary, followed by `key: value` lines that appear only when needed (`now:`, `last:`, `failure:`, `changes:`), with long text (the reply, the question) at the end. When `session start`, `send` or `answer` is given `--wait`, the session printed after waiting is exactly the same as `session get`.
- **Errors**: one line `error: <description>` on stderr, with exit codes as in the table above.
- **Waiting messages**: when messages wait in the agent's inbox, stderr ends with a line saying how many (`N message(s) wait in your inbox: hermesh message list`).
- The text format may change; programs and agents that need a stable structure should use `-o json`.
