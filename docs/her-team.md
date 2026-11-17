# Her and her team: one product from Her and the mesh

Status: stage 1 built on the iPhone 2026-11-13, with acceptance results
updated 2026-11-14 (§13); stage 2 (§6) decided 2026-11-14 (all three, B1,
B3 then B2), designed in §6.1 to §6.5 and built 2026-11-15 (§6.6). The
interaction was agreed on a clickable iPhone prototype on 2026-11-13; this
document says what it takes to build it on the system as it is. It covers
the iPhone app.
The Mac app follows the same model; its design is §14 (decided and built
2026-11-15, §14.8).

It builds on [her-server.md](her-server.md) (what Her is),
[her-mesh.md](her-mesh.md) (Her as an agent that starts coding sessions),
[work-stats.md](work-stats.md) (the counts behind the overview) and
[iphone-app.md](iphone-app.md).

## 0. Summary

Today the app is two products side by side: Her, and the mesh (overview,
grants, machines, sessions). The menu lists sessions that other agents
started right under Her's entry, and they read as Her's conversations.

The design makes them one: **the mesh is Her's team.** Machines are where
the team lives, coding agents are its members, connected agents (Grok,
ChatGPT) are others who send it work, and the overview is the team's
report.

- **Her's page is the start page.** Her name tag shows what the team is
  doing and opens a team panel, where a waiting question is answered in
  place.
- **Work Her starts is a card in her conversation.** Work that others
  start is not in her conversation.
- **The menu is a map of the team**, not a history: Her, Work, the
  machines with their coding agents, the connected agents. No session
  titles.
- **Work** is the overview plus what needs the owner, what runs and what
  just finished. All Sessions is reached from Work and is the page that
  exists today.
- **Without Her** the same app works: Work is the start page, and Her's
  entry asks where she should run.

**What it costs.** Stage 1, which is the whole agreed interaction, changes
**the iPhone app and the shared Swift package only**: every piece of data
it shows is already on the phone (§5). No change to hermeshd, the Her
kernel, the relay, the center or any protocol file. Three things that go
further need the backend and are stage 2 (§6): Her answering a session she
did not start, Her's numbers as a card, and a sturdier link from Her's tool
call to its session.

## 1. The problem today

| What | Where | Why it confuses |
|---|---|---|
| The menu lists Needs You, In Progress and Recent sessions under Her | `DrawerView.lists` | A session Grok started on Codex sits in the list under "Her" and reads as a chat with Her |
| The app opens on the overview | `AppModel.root = .overview` | Her, the core, is one of five places |
| Her starts sessions, but her conversation shows nothing of them | `herChat` (only her words and files become rows) | The owner must leave Her and find the session in the menu |
| What needs the owner is in the menu, in the overview and in each session | three places | No single place to act |

## 2. Decisions

Made by the owner on 2026-11-13, on the prototype.

1. The direction is "Her and her team": the name tag as the team's state,
   the menu as the team's map, Work as overview plus sessions.
2. An entry to all sessions stays.
3. That entry is inside Work only. The menu gets no "All Sessions" row; the
   menu as designed stays as it is.
4. All Sessions is not a new page. The build reuses `SessionsView`, and
   `ScopedSessionsView` for one machine, one coding agent or one connected
   agent.
5. This document comes before the build, and says for each part whether it
   is app-only or needs the backend.

## 3. What exists today

Read from the code on 2026-11-13.

| Need | What there is | Where |
|---|---|---|
| Every session with its state, on the phone | `model.sessions`, kept current by the Go core's replica (`SESSION_VIEW_BASIC`) | `AppModel`, `internal/owner/replica.go` |
| Who started a session | `Session.created_by` (a client id, or `owner`) and `created_as` (the name under it) | `session.proto` |
| That client's name and logo | `model.clients`, `clientLabel`, `GrantAvatar` | `AppModel`, `Components.swift` |
| Which client is Her | `Grant.her_machine_id` is set on Her's grant only | `identity.proto`, used in `GrantsView` |
| A waiting question, in lists | `progress.pending_question` is part of the basic view | `Session.pendingQuestion` |
| Answering from the phone | `model.answer(_:answers:decline:)`, `QuestionPanel` | `AppModel`, `QuestionPanel.swift` |
| What a running session does now | `progress.current_tool`, `phase`, `turn_started_at` (basic view; `last_message_excerpt` is not) | `session.proto` |
| A machine's coding agents | `Machine.status.agents` | `machine.proto` |
| Counts by range, by client and agent | `QueryWork` with `group_by` client and agent; `WorkCard`, `FlowCard` | `local.proto`, `OverviewView.swift` |
| Her's tool calls and their results | `HerEntry.assistant(calls:)` and `.result(text:)` reach the phone; rows are made only for `send_to_user` | `HermeshHer/Session.swift`, `Chat.swift` |
| The result of Her's `session_start` | The CLI's JSON document, unchanged, with `session.session_id` | [her-mesh.md](her-mesh.md) §4.5, [cli-output.md](cli-output.md) |
| All sessions, grouped, with archive | `SessionsView` | `SessionsView.swift` |
| One machine's or one grant's sessions | `ScopedSessionsView` with `SessionFilter` (grant, project, agent, machine) | `SessionsView.swift`, `Sessions.swift` |
| Her's first run | `HerFirstRun`, `HerChoices` | `HerSettings.swift` |

What the phone can't do today, and still can't after this: start a
session itself, or write a message into a session. It reads, answers
questions, interrupts, renames, archives and deletes. The prototype's
"New session" item and the message field on a session's page are **not**
part of this design (§11).

## 4. The design

### 4.1 Start page

| Her | The app opens on |
|---|---|
| Runs on at least one machine | Her |
| Runs nowhere, there is a machine, and the owner has not said "Not Now" on this iPhone | Her's first run (§4.9) |
| Runs nowhere, and the owner said "Not Now" | Work |
| There is no machine yet | Work: she has nowhere to run, and Work has Get Started, the way to a first machine |
| The demo | Work: the demo has no Her |

"Not Now" is kept on the iPhone only (`UserDefaults`). It is forgotten as
soon as a machine runs Her.

### 4.2 Her's name tag

The capsule under Her's avatar carries her name and, beside it, the
team's state. It is drawn from `model.sessions` (not archived) and
`model.pairings`, so it works while Her's machine is offline.

| State, first that holds | The tag shows |
|---|---|
| One session waits for an answer | name · orange dot · "Codex is waiting" |
| Several things wait (questions, requests to connect) | name · orange dot · "3 need you" |
| A session's last turn failed, nothing waits | name · red dot · "1 failed" |
| Sessions run | name · the agents' logos · "2 working" |
| Nothing | the name alone |

When something waits and sessions also run, the running count follows the
words as a small ring and a number.

**Taps.** Today the avatar and the name are one button that opens Her's
settings. They become two: the avatar opens Her's settings (as today), the
tag opens the team panel.

**Width.** The tag grows and shrinks with its content, animated; with
Reduce Motion it changes at once.

### 4.3 The team panel

A glass panel that grows out of the tag, under the avatar, over a dimmed
conversation. It closes by a tap outside, a tap on the avatar, a swipe up,
or opening a row. However much it lists, it stops short of the composer's
row (it scrolls inside), so the dim to tap stays in sight on a small iPhone.

| Section | Rows | From |
|---|---|---|
| Head | "Team", and "Today · 11 sessions ›" which opens Work | `QueryWork`, today |
| Needs You | Each waiting question; each request to connect; each failed session | as `DrawerView.lists` today |
| In Progress | Title, "sender → agent · project" (the shared row in §4.7), what it does now, how long | `current_tool`, `turn_started_at` |
| Just Finished | The two sessions that ended last | `updated_at` |

**Answering in place.** A question is answered in the panel when it has
one required item, with options, one choice, not secret; any other items
are optional and left blank. A single optional choice also keeps its
buttons. The options are buttons; one tap answers that item by its id
(`model.answer`), regardless of its position in the form. Any other
question (several required items, free text, a secret) is a row that opens
the session, where `QuestionPanel` handles it as today. This includes the
owner’s 2026-11-14 decision to allow a choice with optional notes.

Empty, the panel says "Nothing is running now." and still shows Just
Finished.

### 4.4 Session cards in Her's conversation

When Her starts a session, a card appears in her conversation at that
point, and stays current. When she later asks more of it, or tells the
owner what its news said, the card moves down to that point: a job
picked up again days later shows where the owner reads now, not where it
began.

| The session | The card shows |
|---|---|
| Queued, preparing, running | Agent logo, title, "agent · machine", what it does now, a timer |
| Waits for an answer | "Waiting for an answer" and an orange dot |
| Idle after a good turn | A check, and "2 files · +18 −6" (`diff_stat`), or "Done" without changes |
| Last turn failed, or crashed | A red dot and the failure in words |
| Not on the phone (deleted, or older than what the phone holds) | Title, agent and machine from the call's result, no state |

A tap opens the session's page. The card never answers a question: news of
her own sessions goes to Her first ([her-mesh.md](her-mesh.md) §6.5), and
two answers would race.

**How the app knows.** No new data. `herChat` already walks every reply's
tool calls. It adds a row kind for a call whose tool is `session_start`
(itself, or inside `call_tool`, which `herTool` already unwraps), whose
result is not an error, and whose result text parses as the CLI's document
with `session.session_id`. The card looks the session up in
`model.sessions` by that id. A call that was denied, failed or cancelled
makes no card.

**Where it stands.** One card per session, at the last of:

- a `session_start` that started it;
- a `session_send` or `session_answer` to it (its `session_id` argument)
  whose result is not an error;
- each message Her sends the owner with `send_to_user` in a task that news
  of the session ([her-mesh.md](her-mesh.md) §6.2) woke, under that message.
  The news's mail names the session (`from.id`). News she tells the owner
  nothing of moves nothing; the owner's next message ends it.

The card's row key is `session:<id>`, the same wherever it stands, so the
conversation moves it and draws nothing again. It is named by what named
the session first in the history read: the start's result, a send's
result (`session` on an online machine), or the news's tag (`title`,
`agent`, and the machine from `project`). History is read a page at a
time, so the start may not be among it. A session nothing names makes no
card. Each result and news is read only for a session not named yet, so
the conversation costs no more to lay out as a job goes on.

Sessions that others started have a card only once Her asks more of them.
The other tools make none.

### 4.5 The menu

Superseded by §16 (2026-11-17): the menu is Her, the overview and every
session; machines and connected agents are reached from the account.

| Row | Opens | Marks |
|---|---|---|
| Search (top) | The search sheet, as today | |
| Her | Her | Unread count, as today; "Off" while she runs nowhere |
| Work | Work | Orange dot when something needs the owner; ring and count of running sessions |
| **Machines** (heading) | The Machines page, as today's place | |
| Each machine | Its sessions (`ScopedSessionsView`, machine) | Green dot online; "Her is here"; "Offline · 2 hours ago" |
| Each installed coding agent, under its machine | Its sessions there (machine and agent) | Orange dot, ring |
| **Connected Agents** (heading) | The Grants page, as today's place | |
| Each connected agent | Its sessions (`ScopedSessionsView`, grant) | A request to connect that waits is listed here with an orange dot and opens the pairing sheet |
| Account (bottom) | The account sheet, as today | |

Gone from the menu: the Overview, Grants and Machines place rows (the
headings open the same pages), the three session lists, and the All
Sessions link.

Her's own grant is not listed under Connected Agents; she is the first
row. Revoked grants are on the Grants page only.

A scoped list opened from the menu is pushed on Work, so Back returns to
Work. Its bar gets one button to the page it belongs to: the machine's
page, the coding agent's page (defaults and projects), or the grant's
page.

### 4.6 Work

Today's overview, renamed, with the lists that leave the menu.

1. What is there today above the work: requests to connect, grants that
   end, the notifications and second-device rows, Get Started, unreachable
   machines. Unchanged.
2. The range (Today, 7 Days, 30 Days) and `WorkCard`. Its glass button for
   what runs now scrolls to In Progress; today it opens the menu.
3. `FlowCard`: who sent work to which coding agent. Her's bands are in the
   accent.
4. **Needs You**: as in the panel, answering in place.
5. **In Progress**.
6. **Recently Finished**: three.
7. **All Sessions ›**: pushes `SessionsView`.
8. The coding agents' cells (`BucketCells`), as today.

The machines section at the end of today's overview goes: the menu lists
them.

Without Her and after "Not Now", a banner at the top says "Her isn't on"
and opens her first run.

### 4.7 All Sessions and scoped lists

`SessionsView` as it is: groups, the view menu, archived sessions, swipe
and long-press. It stops being a root page and is pushed from Work.

`ScopedSessionsView` as it is. `SessionFilter` already takes a machine and
an agent together; only the route is new.

**One change to the row**, everywhere `SessionRow` is used: the second
line reads "Grok → Codex · hermesh" where it reads "Grok · Codex · hermesh"
today. The arrow says who sent the work to whom, which is the point of the
whole design. Her's sessions read "Her → Claude Code · hermesh".

### 4.8 A session's page

The page already names the grant on what was sent. Two changes:

- Her's messages carry her avatar and "Her", not "Her on MacBook".
- Each turn is named by who asked for it (`turn_started.asker`), so a
  session Grok started and Her later wrote to shows both.

### 4.9 Without Her

`HerFirstRun` stays the place that asks where Her should run, and takes
the prototype's look: Her asleep, the machines as cards, one button. It
gains **Not Now**, which makes Work the start page (§4.1).

Once a machine runs her, Her wakes and writes first, as today.

### 4.10 Her's machine offline

Her's page shows what it shows today. The name tag, the panel, the menu
and Work keep working: they read the other machines' sessions directly and
ask Her nothing. This is why the team's state is drawn by the app and not
told by Her.

## 5. Where each piece gets its data

Every row is stage 1.

| Piece | Data | Has it today | Change |
|---|---|---|---|
| Start page | Whether a machine runs Her; "Not Now" | yes; new local flag | app |
| Name tag | Sessions' states, requests to connect | yes | app |
| Panel: lists | The same | yes | app |
| Panel: today's count | `QueryWork` | yes | app |
| Panel: answer in place | `pending_question`, `AnswerQuestion` | yes | app |
| Session card: which session | Her's `session_start` call and its result text | yes | shared Swift package (`herChat`) |
| Session card: state | `model.sessions` | yes | app |
| Menu: machines and agents | `machines`, `status.agents` | yes | app |
| Menu: connected agents | `clients`, `her_machine_id`, `pairings` | yes | app |
| Menu: marks | Sessions by machine and agent | yes | app |
| Work: card and flow | `QueryWork` | yes | app |
| Work: lists | Sessions | yes | app |
| Scoped list by machine and agent | `SessionFilter` | yes | app (one route) |
| Row "sender → agent" | `created_by`, `created_as`, clients | yes | app |
| Session page: who asked each turn | `turn_started.asker` | yes | shared Swift package (`Transcript`) |

**Not touched in stage 1:** `proto/`, `internal/`, `cmd/`, `her/`, the
relay, the center (hermesh-cloud), the hosted MCP, the console, push
notifications.

Because nothing on a machine changes, the design works with machines on
today's released version, and no deployment has to come first.

## 6. What needs the backend (stage 2)

None of this blocks stage 1. Each is its own decision.

| # | Capability | Why stage 1 lacks it | What changes | Where |
|---|---|---|---|---|
| B1 | Her answers, writes to, or stops a session she did not start ("tell Codex: pnpm") | A grant controls only its own sessions ([her-mesh.md](her-mesh.md) §15, question 4) | The machine's rule gains an exception for the grant that carries `her_machine_id`. No protocol change. Machines on an older version still refuse | `internal/machine` |
| B2 | "What did the team do today?" answered with a card of numbers | Her has `session_list` and can answer in words today. She has no counts, and a result can't carry a card | A `work` command in the CLI and a `work_summary` tool in the `mesh` group; machines serve `PullWork` to a client (the read rule is written for it, only owner devices call it today); B3 for the card | `internal/cli`, `internal/herhost`, `internal/machine` |
| B3 | A mesh tool's result carries data for the apps | `MeshCallResult` is text and an error flag. The app parses the text for the session id, which holds as long as the CLI's document does | `MeshCallResult` gains `details`; the kernel passes it to the tool result's `details`, which the apps already read for files and cards; hermeshd fills it for `session_start` | `her/crates/her-protocol`, `her/crates/her-core/src/mesh.rs`, `internal/herhost/mesh.go` |

B3 is worth doing only if B2 is, or if parsing proves fragile. A test in
stage 1 holds the contract meanwhile (§9).

The relay and the center change in neither stage. No new enum value
crosses the hosted MCP.

**Decided 2026-11-14:** all three, in the order B1, B3, B2. The owner
accepted B1's wider reach (§6.1, Security).

### 6.1 B1: Her acts on every session she can read

**The rule.** A machine lets a client change a session (`canWrite`) when
the client started it, **or** the client's grant carries `her_machine_id`
and the client may read the session (`canRead`: Her's grant sees every
task). `canWrite` decides `session send`, answering a question, interrupt,
archive and unarchive. Rename and delete stay the owner's.

What she already had: `message send --to <session>` (her `session_send`)
is delivered under the read rule and the grant's projects and agents, not
under `canWrite` (`Sessions.Deliver`). So B1 adds answering, stopping,
archiving and unarchiving; writing worked before.

**Security.** A compromised Her Server could already write to every session
she reads, which is running any command where it runs. B1 adds stopping a
turn (it loses that turn), archiving (undone by unarchive; the branch keeps
the work) and answering a question. The exception holds only for a grant
signed by the owner's root key with `her_machine_id`, which only the
owner's app makes, for one Her key. The audit log names Her as the subject
of each change, as for every client.

**News.** Her's `session_send` makes her the one who asked for that turn,
so its news comes to her (machines already write news to the turn's asker,
[her-mesh.md](her-mesh.md) §6.1). An answer does not: the turn's news
still goes to whoever asked for it, and to the session's creator.

**Approval.** Unchanged. A write or an answer to a session she did not
start has no "don't ask again" rule and is asked every time
([her-mesh.md](her-mesh.md) §5). Interrupt and archive run unasked, as for
her own.

**Words.** The tools stop saying "of yours" (`session_answer`,
`session_interrupt`, `session_archive`, `session_unarchive`,
`session_list`), and one guideline is added:

> A session the user or another agent started is theirs. Answer, write
> to, stop or archive it only when the user asks for that session, and say
> whose it is.

**Older machines** refuse as today ("only the client that started the
session may change it"), and Her says so. Nothing changes on the phone.

### 6.2 B3: a mesh tool's result carries details

- `MeshCallResult` gains `details`, any JSON, absent by default. The kernel
  hands it to the tool result (`ToolOutput::with_details`); `call_tool`
  already passes a deferred tool's details through. A host that sends
  none changes nothing.
- hermeshd's `Mesh.Call` returns details beside the text. `herlink` fills
  them for `session_start`, `{"session": {session_id, title, agent,
  machine, machine_id, project}}`, and for `work_summary`, `{"work": …}`
  (§6.3).
- The app takes Her's session card from `details.session` first and keeps
  reading the text when there are none: a Her Server on an older hermeshd.
  Details are not cut, as a long text result is.

### 6.3 B2: what the team did, as numbers and a card

**The CLI.** `hermesh work [--days N] [--machine M] [--time-zone TZ]`.
It pulls each reachable machine's counts with `PullWork` (from the range's
first hour, page by page) and adds them up as `QueryWork` does on an
owner device ([work-stats.md](work-stats.md) §7). Machines already serve
`PullWork` to clients and filter it by the read rule, so no machine
changes. A day runs from midnight in `--time-zone` (an IANA name; default
the computer's); `--days` is 1 (today) to 31.

```json
{"ok": true, "exit_code": 0, "error": null,
 "work": {
   "from": "2026-11-14T00:00:00+08:00", "to": "2026-11-15T00:00:00+08:00", "days": 1,
   "sessions": 11, "done": 8, "turns": 40, "failed_turns": 2,
   "input_tokens": 912000, "output_tokens": 41000, "busy_seconds": 5400,
   "buckets": [0, 0, 1, 3, …],
   "by_agent":  [{"agent": "claude", "sessions": 7, "done": 5, "turns": 30, "failed_turns": 1, "busy_seconds": 4000}],
   "by_client": [{"client": "c_…", "name": "Grok", "you": false, "sessions": 4, "done": 3, "turns": 9, "failed_turns": 0, "busy_seconds": 900}]
 },
 "offline": []}
```

- `sessions` are sessions at work in the range, each once; `done`, those
  idle after a good turn or archived now; `buckets`, sessions at work in
  each hour (one day) or each day.
- `by_client`: sessions by who started them, turns by who asked for them,
  as on Work. `client` is `owner` for the user; `you` marks the client
  that runs the command. Names come from the owner's contacts (`message
  contacts`); a client without one has its id.
- A machine that is off, or answers `PullWork` with "unimplemented" (a
  version before work counts), is listed under `offline`, and its work is
  not counted.
- Not added to `cli.Tools()`: the MCP gateway's agents don't get it in
  this step, so the hosted MCP doesn't change.

**The tool.** `work_summary` in the `mesh` group, read-only, runs `work`
with `days` and `machine`. Its description:

> What the coding agents on the user's computers did over the last days:
> sessions at work, how many are done, turns, failed turns, tokens and
> time, by coding agent and by who sent the work (the user, you, or
> another agent). Use it when the user asks what the team or their agents
> did. The app shows the numbers as a card above your reply: say in words
> what stands out, not every number.

**The card.** `herChat` makes a work row for a `work_summary` call that
succeeded and has `details.work`, as it makes a session row. It is drawn
like Work's card, smaller: the number of sessions, "Done 8 · 40 turns ·
953K tokens", a bar for each hour or day, and the agents and senders with
their logos. A tap opens Work.

### 6.4 Changes by module (stage 2)

| Module | Change |
|---|---|
| `internal/machine/sessions.go` | `canWrite`: Her's exception (B1) |
| `her/crates/her-protocol` | `MeshCallResult.details`; schema |
| `her/crates/her-core/src/mesh.rs` | Passes the details to the tool result |
| `internal/herhost/mesh.go` | `Mesh.Call` returns details; `mesh/call` sends them |
| `internal/cli` | `hermesh work` |
| `internal/herlink` | Details for `session_start` and `work_summary`; the new tool; B1's words |
| `packages/HermeshKit/Sources/HermeshHer/Chat.swift` | Session row from details; the work row |
| `apps/ios/Hermesh/Views/Her/HerConversation.swift` | The work card |
| `docs/her-mesh.md`, `docs/cli.md`, `docs/cli-output.md` | The rule, the tool, the command |

### 6.5 Tests (stage 2)

- **B1, unit** (`internal/machine`): Her's grant answers, interrupts,
  archives and unarchives another client's session; an ordinary grant that
  sees every task still can't; rename and delete stay the owner's.
- **B3, unit**: the kernel puts a host's details on the tool result
  (`her-core/tests/mesh.rs`); `herhost` sends them; `herChat` prefers
  `details.session` and still reads the text without it.
- **B2, unit** (`internal/cli`): adding up pages from two machines over a
  range, by agent and client, with a session in both halves of the range
  counted once.
- **End to end** (`TestHerMesh`, real hermeshd and Her, scripted coding
  agents): `session_start`'s result carries `details.session`; another
  agent (the test's CLI) starts a session that asks, and Her answers it
  with `session_answer`, then stops another of its sessions with
  `session_interrupt`; `work_summary` counts the sessions of both, with
  her own marked `you`.
- **iPhone** (`TestIPhone`, `testHerTeam`): Her's work card appears and
  opens Work.

### 6.6 Acceptance, 2026-11-15

Passed:

- `go test ./internal/machine ./internal/cli ./internal/herlink
  ./internal/herhost ./pkg/mcpgw ./internal/workstats`, including
  `TestHerActsOnEverySession` (observed failing before the rule changed),
  `TestCountWork` and `TestDetails`.
- `cargo test -p her-protocol` (the schema regenerated),
  `-p her-core --test mesh`, `-p her-server mesh_host`; the TypeScript
  types regenerated from the schema.
- `swift test --package-path packages/HermeshKit --filter ChatTests`: the
  session card from details and from text, and the work card.
- `HERMESH_FAKE_PROVIDERS=1 HERMESH_HER_BIN=… go test ./e2e -run
  '^TestHerMesh$'`: real hermeshd and Her with scripted coding agents;
  `details.session` on `session_start`; Her answers the test CLI's
  session (asked first) and stops another of its sessions (unasked);
  `work_summary` counts both, Her's marked `you`.
- `TestIPhone` with `HERMESH_IPHONE_GREP=testHerTeam`, iOS 27.2: the
  stage 1 team flow, then Her's work card, which opens Work. The card's
  screenshot was inspected: Chinese words, counts, agents and senders fit
  the phone.

Two earlier attempts at the iPhone run did not reach the UI tests: the
first stopped at a switch in `HerConversation.swift` that lacked the work
row (fixed), the second could not fetch Swift packages while github.com
was unreachable from the machine.

## 7. Changes by module (stage 1)

| Module | Change |
|---|---|
| `packages/HermeshKit/Sources/HermeshHer/Chat.swift` | `HerItem` gains a session row; `herChat` makes it from `session_start` calls |
| `packages/HermeshKit/Sources/HermeshSessions` | The team's state as one pure function (what waits, what runs, what failed, which words); who asked each turn in `Transcript` |
| `Model/AppModel.swift` | `Root`: `overview` becomes `work`, `grants`, `machines` and `sessions` stay reachable as pushed pages; `Route` gains all sessions and machine-and-agent sessions; the start page rule; "Not Now" |
| `Views/Main/DrawerView.swift` | The new menu (§4.5); the session lists go |
| `Views/Main/OverviewView.swift` | Work (§4.6); `SessionRow`'s second line |
| `Views/Her/HerView.swift` | The name tag and its two taps; the team panel |
| `Views/Her/HerConversation.swift` | The session card row |
| `Views/Her/HerSettings.swift` | `HerFirstRun`: the look, Not Now |
| `Views/Session/SessionsView.swift` | Pushed, not a root; the scoped list's bar button |
| `Views/Session/SessionView.swift` | Her's name and avatar; per-turn sender |
| `Model/DemoService.swift` | Her with a conversation that started a session; sessions from two connected agents; one waiting question with options |
| `Localizable` | The words of §8, English and Chinese |

Shared components to reuse, not rewrite: `SessionRow`, `QuestionPanel`'s
answer path, `GrantAvatar`, `AgentLogo`, `WorkCard`, `FlowCard`,
`BucketCells`, `HerAvatar`, `HerChoices`.

## 8. Words

A first draft; the prototype has them in Chinese.

| English | Chinese |
|---|---|
| Work | 工作 |
| Team | 团队 |
| Connected Agents | 连接的 Agent |
| Machines | 机器 |
| Needs You | 需要你处理 |
| In Progress | 进行中 |
| Just Finished / Recently Finished | 刚完成 / 最近完成 |
| All Sessions | 全部会话 |
| %@ is waiting | %@ 在等你 |
| %lld need you | %lld 个在等你 |
| %lld working | %lld 个在忙 |
| %lld failed | %lld 个失败 |
| Nothing is running now. | 现在没有在跑的活。 |
| Her is here | Her 在这里 |
| Waiting for an answer | 在等回答 |
| Her isn't on | Her 还没启用 |
| Not Now | 以后再说 (the app's word for it already) |
| Not on (Her's row in the menu) | 未启用 |
| Sent by %@ | %@ 派的活 |

## 9. Tests

Unit (Swift package):

- `herChat`: a `session_start` call with a good result makes one session
  row with the id; inside `call_tool` too; an error, a denial, a cancelled
  call and a result that is not the document make none.
- `herChat`: a `session_send` or `session_answer` days later moves the one
  card down to it, named as before; one that failed moves nothing. What Her
  says after news of a session has its card under it, named from the news;
  news she says nothing of moves nothing.
- The team's state: each row of §4.2's table, and which wins.
- Who asked each turn.

Contract (Go, test only): the `mesh` group's `session_start` result is a
document with `session.session_id`, as the app reads it. It fails when
that document changes shape.

UI tests (demo data): the menu lists no session title; the tag's words for
each state; answering in the panel takes the question away in the panel,
on Work and on the session's page; the card in Her's conversation opens
its session; Work → All Sessions → a session → Back, Back; a machine row
and an agent row open the right list; Not Now makes Work the start page
and the banner brings the first run back. Tests that find sessions by
`drawer-session-…`, and those that open Her's settings by `her-name`, are
moved to the new places.

End to end (`TestIPhone` with `HERMESH_IPHONE_GREP=testHer`, real hermeshd
and Her with scripted model and coding agents): ask Her to start a
session; the card appears and reaches Done; a second client starts a
session that asks a question; the tag says it waits; answer in the panel;
the session goes on.

Then every control is walked by hand once, in light and dark, on a phone
and on iPhone Duo's side-bar layout.

## 10. Steps

Each ends with its tests passing and is usable alone.

1. The shared package: the session row, the team's state, the per-turn
   sender. → unit tests and the contract test.
2. The menu, Work, the routes, All Sessions under Work, the row's arrow.
   → UI tests for navigation.
3. The name tag and the team panel, with answering in place. → UI tests.
4. Session cards in Her's conversation. → UI tests, end to end.
5. The session page's sender names; the first run's look, Not Now and the
   start page. → UI tests.
6. Words in both languages; walk every control; end to end.

After step 2 the confusion this started from is gone. Steps 3 to 5 make
Her the centre.

## 11. Not doing now

- Starting a session, or writing into one, from the phone itself. Her does
  it.
- Other agents' sessions as cards in Her's conversation.
- A session's steps live inside Her's conversation
  ([her-mesh.md](her-mesh.md) §13).
- Search and a state filter on All Sessions (the prototype's stand-in page
  had them; the search sheet exists).
- The suggestions above Her's message field. The prototype used them to
  drive the demo.
- `work` for the MCP gateway's agents (§6.3): only Her has it.

## 12. Open questions

1. **The Mac app.** Decided 2026-11-15, designed in §14.
2. **The coding agents' cells on Work** (§4.6, item 8). The prototype
   leaves them out; they are a function people have today and nothing else
   shows it. Keep, or move to each agent's page?
3. **Failed sessions on the tag.** "1 failed" stays until the session is
   opened, archived or runs again. Should it count at all, or only show in
   the panel?
4. **B1 first?** Settled 2026-11-14: built, with the rest of stage 2
   (§6.1).
5. **After compaction (checked 2026-11-14).** Compaction appends a
   checkpoint to the history (`her-core/src/compaction.rs`); it does not
   delete the tool calls or results. `Kernel::transcript` walks the whole
   branch, and `transcript_of` keeps those entries, so a loaded card
   survives compaction. Older history still uses the existing pagination;
   a card appears when its call and result have been loaded.
6. **The first-device guide.** The guide for a first device ends in the
   app; where Her's first run sits in it is not decided here.

## 13. Acceptance work, 2026-11-14

The remaining stage 1 work adds the Chinese words, moves the existing UI
tests to Work and the team menu, and covers the panel and Her's session
card against the real local services. The one-choice rule in §4.3 now
allows optional extra fields, and uses the choice's item id even when an
optional field comes first. The panel's heading can be swiped up to close
it; the name tag honours Reduce Motion.
Its busy ring uses the same timeline drawing approach as Her's avatar,
and stays still with Reduce Motion or while inactive, without installing
a repeating UIKit animation.
The start page is chosen only from an initialized owner's snapshot. The
iOS 26 run exposed a pre-join status-only snapshot choosing Work too
early, even when the owner joined afterward already had a machine.

Existing UI checks needed their timing made independent of the new
navigation: streaming starts after the session opens, and Duo's resize
check compares the resulting width and its persistence rather than
assuming every point of the gesture resizes the menu. The running-count
link is exercised while work runs, before it is interrupted; the empty
In Progress section is not shown. The panel's swipe test drags out of its
heading, beyond the dismissal threshold. The Chinese typing
fixture and settings-return focus fix come from main's `d796093b`; the
new avatar entry is covered by sending a message after returning from
settings. No production backend or protocol changed.
Search checks are scoped to the search list: Work remains underneath its
sheet and can contain a session with the same accessibility identifier.
The text-search check submits the query before reading the result list,
so search suggestions are dismissed. `testSearchAndConnectivity` provides
a focused run of search, loading, offline recovery and notifications.
Foreground recovery waits for the sync line to disappear, since it can
first appear just after the app becomes foreground; the machine/agent
continuity checks remain in place.
The offline-loader check positions the session row before disconnecting
the relay, then opens it immediately: navigating through the menu again
could consume the ten-second loading state before the offline message.
The real-service question fixture already has a required choice and an
optional note, so the quick-answer rule is covered through the UI too.
The card check scrolls back from the end and requires the card to be
hittable before opening it; the scripted model echoes long tool results
and session news after the card, pushing it outside the viewport.

The iPhone harness limits Xcode to four build jobs after initial parallel
runs caused resource contention and a service setup timeout. A separate
retry also hit an occupied test TURN port before launching the UI.
Her's full iOS 27.2 run repeatedly waited 60 seconds for animation
quiescence, including later in the existing file flow after replacing the
tag's spinner. That run was stopped; the full flow passed on the installed
iOS 26 runtime, and the focused team flow passed on iOS 27.2. The spinner
change alone is not claimed to resolve every animation wait.

Passed:

- `swift test --package-path packages/HermeshKit`: 81 tests, including the
  quick-choice regression (observed failing before the fix).
- `HERMESH_FAKE_PROVIDERS=1 HERMESH_HER_BIN=… go test ./e2e -run
  '^TestHerMesh$' -count=1 -v -timeout 15m`: the real Her/mesh contract with
  scripted coding agents.
- `DuoUITests/testWalk`, with `TEST_RUNNER_HERMESH_DUO_SHOTS` and
  `TEST_RUNNER_HERMESH_DUO_POSE=open`: upright and landscape, including
  sidebar resizing and hide/show. Result: `/tmp/hermesh-team-duo-3.xcresult`.
- `HERMESH_IPHONE_E2E=1 go test ./e2e -run '^TestIPhoneFirstRun$'
  -count=1 -v -timeout 45m`: claim a server, no-machine Work start page,
  defer/resume the guide, pair a machine and an agent, run the first task,
  reconnect and erase the isolated test owner. Result:
  `/tmp/hermesh-team-first-run.xcresult`.
- `TestIPhone` with `HERMESH_IPHONE_GREP=testHerTeam`, iOS 27.2:
  settings-return sending, both panel dismissal gestures, quick answering,
  the completed session card and its Her sender, enabled-Her restart and
  the dark panel. Result: `/tmp/hermesh-team-her-focused-2.xcresult`.
  The light/dark panel and completed-card screenshots were inspected;
  Chinese text renders correctly and the controls fit the phone.
- `TestIPhone` with `HERMESH_IPHONE_GREP=testHer`, iOS 26: the complete
  Her flow, including onboarding, Chinese input, settings, memory, files,
  approval, team/card/restart, offline recovery, history and data erasure.
  Result: `/tmp/hermesh-team-her-ios26-3.xcresult`.
- `TestIPhone` with `HERMESH_IPHONE_GREP=testSearchAndConnectivity`,
  iOS 27.2: search, the centered loader, machine and relay offline recovery,
  foreground continuity and notification answering. Result:
  `/tmp/hermesh-team-connectivity-2.xcresult`.
- Default `TestIPhone`, iOS 27.2: the complete mesh app flow, from joining,
  Work and session operations through grants, machines, appearance,
  search, offline recovery and notification answering. Result:
  `/tmp/hermesh-team-iphone-8.xcresult`.

Manual Simulator checks covered the team menu, machine/agent scoping,
quick answering, Her's Not Now entry, account appearance, and a session
in dark appearance, with Duo open and closed. These are Simulator checks,
not physical-device or frame-performance measurements. Duo's automated
PNG captures were black in this runtime, so they are not visual evidence;
the interactive Device Hub checks and UI assertions are recorded separately.
The installed iOS 26 runtime renders Chinese as missing-glyph boxes,
including in the system keyboard. Its UI assertions can check behaviour,
but its screenshots are not evidence of Chinese typography or layout.

For Her's full flow use `HERMESH_IPHONE_GREP=testHer`; `testHerTeam` is the
focused team/settings-return/start-page/dark-appearance regression. Both
need `HERMESH_IPHONE_E2E=1` and a built `HERMESH_HER_BIN`. The default
`TestIPhone` runs the mesh app flow, not either Her test.

## 14. The Mac app

Decided by the owner on 2026-11-15. The model is the iPhone's (§4): Her
leads, the mesh is her team, Work is the overview. The Mac keeps what a
desktop needs that the phone does not: the session list one click away.

### 14.1 Decisions

1. **The sidebar is navigation only** (revised 2026-11-15, after trying
   the first build, which had the session list under the map: three kinds
   of things in one column read as a muddle). Her, Work, Search and All
   Sessions; then the team's map, which folds. No session title.
2. **Sessions in three columns, as Mail** (chosen 2026-11-15 on the demo,
   over a full-page list with Back): All Sessions or a row of the map opens
   the session list as a column of its own; the session chosen in it shows
   beside it, so switching sessions is one click.
3. **The sidebar has a rail** (2026-11-15, on the demo): expanded, or a
   74 px column of icons; it is never hidden, and which of the two is the
   owner's choice, kept from page to page (§14.9).
4. **New windows open 1240 × 800**, as before. For a day they opened
   1440 × 860, to fit the info card beside the expanded sidebar and the
   list. At 1240 the card gives way beside the expanded sidebar and fits
   beside the rail.
5. **The start page** is the iPhone's rule (§4.1): Her when she runs on a
   machine; otherwise her first run, or Work.

### 14.2 The sidebar

Superseded by §16 (2026-11-17): no team map and no list column; the
sidebar lists the sessions itself.

| Part | Rows | Opens | Marks |
|---|---|---|---|
| Top | Her | Her | Unread count, as today; "Off" while she runs nowhere |
| | Work (today's Overview, ⌘1) | Work | Orange dot when something needs the owner; ring and count of running sessions |
| | Search (⌘K) | The search, as today | |
| **Team** (heading, folds; open by default) | All Sessions, first, staying when the team is folded (in the top part when there is no team) | The list's column, all sessions | How many (archived aside) |
| | Each machine, this Mac first | The list's column, its sessions | Green dot online; "Her is here"; "Offline · 2 hours ago" |
| | Each installed coding agent, under its machine, flush with it | Its sessions there | Orange dot, ring |
| | Connected agents, on one line each | Its sessions | A request to connect that waits has an orange dot and opens the pairing sheet |
| Foot | The account menu, as today | | |

- **Grouping.** `sessionGroupings` in `@hermesh/core` gains `sender`, and
  it is the default view for a list that has never been set. A section is
  "Sent by Her", "Sent by Grok", "Sent by you"; the order is Her, then the
  agents by their latest session, then the owner. The other groupings stay
  in the view menu. A view the owner already chose is kept.
- **The list's column** (300 px) is the list as before (view menu,
  archived, rename, context menu), headed by what it shows ("All
  Sessions", "Codex on This Mac", "Sent by Grok") and how many. The row of
  the sidebar it came from is marked current. Its session shows beside it;
  with none chosen yet, "Choose a session". A session opened from
  elsewhere (Her's card, Work, a notification) shows with the list beside
  it, all sessions when the list's filter leaves it out. Work and Her have
  no list. This is the Mac's form of the iPhone's scoped lists (§4.7).
- **Narrow windows** give way in order: the info card, then the sidebar
  shrinks to its rail, then the list beside a session (the list alone
  stays: it is the page).
- **Rows** read "Grok → Codex · hermesh" (§4.7).
- **Her's own grant** is not among the connected agents; she is the first
  row.

### 14.3 Her's page

- **The name tag.** The capsule under the avatar carries the team's state
  (§4.2, same words and order). Today the avatar and the name both open
  her settings; they become two: the avatar opens the settings panel, the
  capsule opens the team panel. The environment menu in the capsule, when
  Her runs on several machines, moves to the ⋯ menu.
- **The team panel** is a popover under the capsule (§4.3: Needs You with
  answering in place, In Progress, Just Finished, "Today · 11 sessions ›"
  to Work). Esc or a click outside closes it.
- **Cards in her conversation.** The session card (§4.4) and the work card
  (§6.3), drawn as on the iPhone at the conversation's width. Built in
  `packages/her-client/src/botChat.ts`, the Mac's `herChat`, with the same
  rules and tests as `Chat.swift`. A click opens the session, or Work.

### 14.4 Work

Today's Overview, renamed, with the lists the sidebar no longer stands for:

1. Unreachable machines and Attention, as today.
2. The link map, as today (it is the Mac's richer form of `FlowCard`).
3. **Needs You**, answering in place (§4.3's rule).
4. Working, as today (In Progress).
5. **Recently Finished**: three.
6. Usage, as today.

There is no All Sessions link: the sidebar's list is all sessions.

### 14.5 Without Her

The first run takes Not Now (§4.9), kept in the app's settings on this
Mac; Work then opens, with the "Her isn't on" banner.

### 14.6 Shared code

The team's state (§4.2) is a pure function in Swift today
(`HermeshSessions/Team.swift`). It is written again in TypeScript in
`@hermesh/core`, with the same table of tests, so the tag, the panel and
the sidebar's marks say the same on both apps.

### 14.7 Steps

1. `@hermesh/core`: the team's state and the `sender` grouping; `botChat`:
   session and work rows. → unit tests.
2. The sidebar (§14.2) and the start page. → Playwright.
3. The tag and the team panel. → Playwright.
4. The cards in Her's conversation. → Playwright.
5. Work's lists; Not Now. → Playwright.
6. Words in English and Chinese; end to end with real hermeshd and Her
   (the desktop's e2e, as `TestHerMesh` drives the iPhone's); every
   control walked in light and dark.

No backend change: everything the Mac shows is already in its store, as it
was for the iPhone (§5).

### 14.8 As built, 2026-11-15

A clickable demo came first and was agreed: [Hermesh Mac · Her and her
team](https://claude.ai/artifact/HBcQMpfA1H6YH53bCJ2TBx). The owner asked to
keep Work's link map ("链路") as it is. Built as §14.1 to §14.7, with these
choices:

- **The list's default.** The window keeps whether the owner picked the
  view in its menu (`viewChosen`). Until then the list is by who sent the
  work, also for a window that saved a view before this version. A view
  picked in the menu is kept, as before.
- **The environments** moved from the capsule to Her's ⋯ menu, with Add
  Environment and the console: the capsule is the team's button.
- **The panel's "Today · N sessions"** counts the sessions the window holds
  (`todayStats`), not `QueryWork`; Work's own counts are unchanged.
- **Needs You** on Work is the existing attention card, which now also
  lists the sessions that ask (one click answers a simple question,
  `quickChoice`) and those whose last turn failed. Working Now stays below
  it, picked by the map as before; a waiting session shows in both.
- **A restart keeps an open session.** Decided by the owner on
  2026-11-15: a window that quit on a session opens on it again, with its
  unfinished answer; the start page rule holds otherwise.
- **Tests start on Work.** The e2e launcher opens Work for the fixture's
  owner window, as most specs expect; `team.spec.ts` checks the start page
  itself (`start: true`). Specs reach the list through `sessionList()`
  (`e2e/sessions.ts`), which opens All Sessions when its column is not
  showing.

Shared code: `teamState` and `quickChoice` in `@hermesh/core` (the same
table as `Team.swift`), the `sender` grouping, and the session and work
rows in `@hermesh/her-client`'s `botChat` (the same rules as `Chat.swift`).

Passed, 2026-11-15:

- Unit: `@hermesh/core` (80, with `team.test.ts`), `@hermesh/her-client`
  (146, with `team.test.ts`), the desktop app (124, with `start.test.ts`
  and the list column's cases in `layout.test.ts`);
  the desktop typecheck, its e2e specs included.
- `go test ./e2e -run 'TestDesktop$'`: every spec, with `team.spec.ts`
  (the start page and Not Now; the sidebar as navigation; All Sessions and
  the map's rows opening the list's column, the chosen session beside it;
  the list by sender; Work's quick answer and Recently Finished) and the
  specs updated for the new words, the default and the three columns
  (`frame.spec.ts` checks the narrow-window order at 1440, 1300, 1000 and
  900 px, and the rail kept from page to page; `team.spec.ts` the rail's
  places, marks and hover words).
- `HERMESH_HER_BIN=… go test ./e2e -run '^TestDesktopHer$'`: real
  hermeshd and her with her-mock and scripted coding agents on this Mac:
  the capsule opens the team panel and Esc closes it; Her starts a session
  after the owner's approval; its card in her conversation reaches Done,
  the panel lists it under Just Finished as "Her → Claude · demo", and the
  card opens the session; environments are switched from ⋯.

Not done: walking every control by hand in light and dark.

### 14.9 The rail

What it holds and the narrow-window order are superseded by §16
(2026-11-17); its two states, kept as the owner left them, stand.

Decided by the owner on 2026-11-15, on the demo's board A2.

- **Two states, never hidden.** Expanded (272 px) or the rail (74 px:
  the traffic lights span x 12 to 72 on macOS 27, measured, and keep 2 px
  clear of the rail's edge). View › Expand Sidebar / Collapse Sidebar
  (⌃⌘S) switches them, kept for the window.
- **The owner's choice, not the page's** (decided 2026-11-15). For a
  while the sidebar folded to its rail whenever sessions showed and came
  back on Her and Work; trying it, the owner found the column jumping at
  each click. Now no click changes it: it stays as the owner left it,
  from page to page, as in Mail and Finder.
- **A window too narrow** for the expanded sidebar (beside the list,
  when sessions show) shows the rail; asked to expand there, the sidebar
  shows over the content until it is put away or a page is chosen.
- **What it holds**, top to bottom: Her (her cat; unread count, or a ring
  while she runs nowhere), Work (an orange dot when something needs the
  owner; a ring with how many run), Search, All Sessions; then the
  machines as tiles with a dot for online, each with its coding agents'
  logos under it (an orange dot when one waits, a ring while one runs);
  then the connected agents' avatars, and requests to connect with a dot;
  at the foot, Expand and the account, whose menu opens beside the rail.
- **Where you are.** The current place has the sidebar's fill and an
  accent bar on its left. Each icon says on hover or focus what it is and
  how it stands ("Codex on This Mac · 1 waiting for you · 1 working"); the
  list's column, beside it, names what it shows.
- **The narrow-window order**: beside a session the card needs 1402 px
  with the expanded sidebar (1204 with the rail), the expanded sidebar
  1132 px, and the list gives way below 934 px (rail, list and
  conversation).

## 15. Progress

Decided by the owner on 2026-11-15, on a clickable demo ([Hermesh ·
进展](https://claude.ai/artifact/QHG99cUAwDCFf3fbpsggBT)), and built the same
day on the Mac and the iPhone. Her hands work out to sub-agents of her own
(`spawn_agent`). Her conversation says little of them, and should: Progress
is where the owner follows that work, as they would ask a secretary how
things stand.

Changed by the owner on 2026-11-16: Progress is Her's own work, her
sub-agents, and no coding session. It first held the coding sessions she
started on the mesh (`session_start`) too, but those are the team's
sessions: All Sessions holds them, by coding agent, and they read as
somebody else's work among Her's. In her conversation each stays a card
that opens the session (§4.4); its question is All Sessions' to ask.

### 15.1 The page

- **Where.** A page of Her's, not a place of its own (decided 2026-11-15:
  on its own it was too heavy and too thin): a button at the top right of
  her page, on the iPhone a glass circle across from the menu button (where
  the system stands the bars on the side, iPhone Duo, in that bar with the
  menu button, as a page that draws its own top leaves its buttons there),
  on the Mac before ⋯. It stays there, not a card of her page beside her
  files and routines: it is looked at often (decided 2026-11-17). It opens Progress over her conversation (pushed on the iPhone, in
  her page's place on the Mac, whose sidebar keeps Her current), with a
  way back to her. Its mark is an orange dot while a sub-agent of hers
  waits for the owner, else a blue one while one is at work (2026-11-16),
  counted while a page of hers follows her conversation.
- **Groups.** One for each thing that set work off: the owner's message (its
  words head the group, with what Her last told the owner since), or a
  routine's run. A message that sent nothing out is no group. Agents whose
  `spawn_agent` calls are on a page of the history not read yet are a group
  of their own, "Asked earlier", until that page is read (Earlier Work).
- **Order.** Groups with something waiting, then those at work, the latest
  first; then the ended ones under "Ended". A group that had ended when the
  page came up is folded; one that ends while it shows stays open.
- **A card** says who does the work (a sub-agent, or the coding agent and
  its machine), how it stands (starting, in progress, needs your approval,
  done, failed, stopped), what the work is (what Her called it, else its
  task's first line), one line of what it does now or how it ended, and how
  long it ran with how many tokens it used. Not when it started.

That was the first version. Using it, the owner found that most messages
send one piece of work out, so the page was a list of groups of one; on both
apps it became a feed instead, decided on 2026-11-15 on a second demo
([进展 · iPhone 信息流](https://claude.ai/artifact/KPaxCyu9xG8oQBsoEAhiZL)).
The groups stay as the data (`progressGroups`, `herProgress`); the page
shows each task of theirs as a post:

- **One post a piece of work**, in three parts: Needs you (it waits for the
  owner, or failed), Now (at work), Earlier (ended); the latest first in
  each. No summary line, no filter.
- **A post** is its title alone, a few lines of how it goes (what it asks,
  why it failed, the start of its report, else its latest words or
  thought; up to four), one piece that shows where it stands, and a foot:
  one time on the left (how long it has worked, in the accent, or how long
  ago it ended) with the tokens it used, and ⓘ on the right. Only a post
  that needs the owner has a mark: an orange dot when it waits, a red one
  when it failed. No picture, no state word: the part it is under says
  that. (Decided 2026-11-15 after trying it on the phone: what set it off,
  shown on every post, made the feed busy.)
- **The piece**: while a sub-agent works, a bar for each call it made (the
  last beating) and the call it is on; while it waits, Review, which opens
  its record, where the approval is answered (an approval shows what it
  would run, too much for a post); done, the files it wrote. A coding
  session: the tool it runs, or the lines it changed. Results with more to
  show (numbers, tables) need sub-agents to report them in a shape the
  apps can read, which they do not yet.
- **What set it off** shows from the post's ⓘ in a sheet (iPhone, Liquid
  Glass at half height) or a panel over the page (Mac): what the owner said
  and when, what Her said back, everything it sent out, each as it stands
  (a tap opens one), and a way to the conversation. Opened in the post
  itself, it was too much for the feed. A record's page has the same
  behind the capsule at its top.

### 15.2 A card's page

A sub-agent's card opens its record, laid out as a coding session's page is
and drawn with the same pieces (the Mac's `ChatFrame` and its rows in
`session/Chat.tsx`; the iPhone's `PromptBubble`, `ToolFold`, `ToolLine`,
`LiveBar`, `FailedCard` and `followsEnd` beside `SessionView`): what Her asked
of it on the right, as what was sent to a session, and what she told it later
the same way; its words across the page; what it thought and called in
between as one grey line ("Read 2 files, ran 3 commands"), folded until
opened, each call then a line that opens to its output; and how it ended: a
line that says its last words went back to Her, the failure card, or a note
that it was stopped. It opens at its end and follows it as it grows. While it
works, the bar at the bottom says so, for how long, with Stop
(`agent_interrupt`); what it asks the owner is answered there, above it, with
the approval the conversation shows. How it stands, how long it ran, its
tokens and steps, who sent it out and the owner's words that set it off (a tap
shows them whole, with the other cards of the same group) are its info: the
Mac's info card beside it, the iPhone's info sheet. Its data is its own (Her's
conversation, §15.3), shown in the session's shapes.

It reads as a session does, and is not one: a sub-agent is not among All
Sessions, where it would read as work somebody sent to the team.

### 15.3 Where the data comes from

Nothing new is asked of her. The conversation the Her page follows
(`bots/open`, then `session/event`) already carries every sub-agent's own
session: the snapshot's `agents`, then each `agent_event`. The groups, the
cards and the record are pure functions of that state: `progress.ts` in
`@hermesh/her-client` and `Progress.swift` in `HermeshHer`, with the same
tests. A view's steps are worked out once (it is replaced, never changed).

- **One connection.** On the Mac, Progress is the Her page's own component:
  going from the conversation to Progress to a record opens nothing. On the
  iPhone the pages hold Her's connections one after another (`HerChat.hold`),
  which close a moment after the last page let go; before, each page of
  hers opened them again.
- **Rows as they show.** The Mac's record draws only the rows near what is
  in sight, as a session's conversation does (`ChatFrame`); the iPhone's
  is a plain stack, as a session's is.
- **Offline.** What hermeshd keeps of Her's conversation has no agents: the
  page then says the work shows once the machine is back.

### 15.4 A long record

A snapshot carries each agent's last 100 items, as it does the
conversation's. The agent's page now has a cursor that names the agent
(`<agent id>/i<n>`), and `session/history` on the main session, given such
a cursor, answers with that agent's earlier items (`her-server`'s mirror).
The apps read on with it when asked (Earlier Steps), one page at a time. A
her from before this gives the agent's page a cursor of the main session's
kind; the apps tell by the `/` and do not read on with it.

### 15.5 Tests

- `cargo test -p her-server --lib mirror`: an agent's history by its cursor.
- `@hermesh/her-client` `progress.test.ts`, and `ProgressTests.swift` in
  `HermeshHerTests`: groups, card states and posts (what they say, the call
  under way, the files written), tokens and times, the
  record's steps, agents of earlier pages and their cursor, a routine's run.
- `go test ./e2e -run '^TestDesktopHerProgress$'`: real hermeshd and her
  with her-mock, the Mac app: the entry appears with Her; an empty page; a
  sub-agent's post under Needs you, what set it off from its ⓘ, Review to
  its record read as a session (Her's words in a bubble, the info card with
  its state and what set it off), the approval answered in the bar at the
  bottom, its call as a folded line that opens, the line that its last words
  went back to Her, time and tokens, the info card hidden and shown; the
  post under Earlier once it ends; another at work, Progress's "1 at
  work", its post under Now, and the busy bubble naming it and opening its
  record; two at work, the bubble's menu of them; Her answering, "Typing…"
  and never her call (§15.6); a coding session she starts, a card in her
  conversation and no post.
- `HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerProgress go test ./e2e
  -run '^TestIPhone$'`: the same story on the iPhone's feed: the post under
  Needs you, its ⓘ's sheet with the owner's words and Her's, Review to
  the record (what Her asked as a bubble, the approval at the bottom, its
  call a folded line that opens, the line that its last words went back to
  Her, its info sheet), and the ended post under Earlier; another at work, Progress
  marked at work, its post under Now, the busy bubble naming it and
  opening its record; two at work, the system's menu of them; Her
  answering, "Typing…" and never her call. `testHerApprovalEnd`: under a conversation longer than
  the page, a sub-agent's question in the composer's place leaves her last
  words in view.

### 15.6 The busy bubble

Decided by the owner on 2026-11-17 (docs/her-busy-demo.html), in place of
what was decided on 2026-11-16 (the dots gone once she answered, a sub-agent
at work being Progress's alone): a conversation gone quiet while her work
went on misled. The last bubble of her conversation says who of hers is at
work, in one line, and never what they are doing:

- While her task runs and waits for no approval: Her, "Typing…".
- Her sub-agents at work that wait for nobody, by name (as an approval
  names them: the description, else the name), the one started last first,
  after Her when she answers too.
- No bubble when nothing is at work, nor while what is waits for the owner:
  the question in the composer's place says so.

The dots, still, and the words are swept left to right by one light, at the
shimmer's pace (2.4 s): a mask, so they only fade where it passes, whatever
is under the bubble (a band of the bubble's colour laid over them showed as
a box on the iPhone's translucent bubble). With less motion asked for, no
light and no turns.

One sub-agent: the line opens its record in Progress (›), whose way back is
her conversation. Several take turns in one cell as wide as the widest name,
the next each time the light has swept the line (not while the pointer is on
it or its menu is open); the line ends with their number and a chevron
pointing where their menu opens, up (turned over while it is open, on the
Mac). It is a pull-down, not a pop-up: it picks no value, so not ⌃⌄. Its
menu (the system's on the iPhone, the app's on the Mac) lists them, each
opening its record, then "See All in Progress".

It is read from the task's live state and its sub-agents' alone
(her-client's `busyBubble`, HermeshHer's `herBusyBubble`): who, not their
calls; nothing more is asked of her, nor kept.

## 16. The sidebar, again

Decided by the owner on 2026-11-17, on `docs/sidebar-b-demo.html` (plan B
of `docs/sidebar-demo.html`). The product is two things: Her, who drives
the work, and the mesh, which the owner watches. The sidebar says where to
go and how things stand; it manages nothing. It looks the same with one
agent on one machine as with twenty.

**Words** (decided 2026-11-17): what the list holds is a **task**
(任务), everywhere the owner reads it, on the Mac and the iPhone, in
Chinese and English; "session" stays the name in code, the protocol and
the CLI. A task is what Her (or another agent) sent a coding agent; its
back-and-forth is its conversation (对话). Work (工作) is now **Overview**
(概览). On the iPhone the catalog's English keys still say "session" and
carry the English "Task" text as their `en` value. Below, "session" is the
code's word.

### 16.1 What it holds

| Part | Rows | Marks |
|---|---|---|
| Top band (Mac) | The sidebar toggle after the traffic lights; Search (⌘K) at its end | |
| Places | Her; Overview (⌘1, the page Work was) | Her: a blue dot while something is unread, "Off" while she runs nowhere. Overview: an orange dot when something needs the owner, a ring while anything runs |
| Sessions | The heading "Sessions" with the view menu at its end; every session, as the view lays them out | Per row, below |
| Foot | The account (Mac: "Connected", no count of machines) | A dot only when not connected |

**No numbers** anywhere in it: no unread count, no count of sessions, of
groups or of machines. Counts are on the overview.

**A row** is the coding agent's logo, the title (one line, cut), and one
mark: orange, it waits for the owner; red, it failed; a ring, it runs;
nothing, it is done. The state in words is for screen readers. With the
view's metadata on, a second line says who sent the work to which agent,
the project and the time. A click (tap) opens the session; hovering shows
⋯ in the mark's place; right-click (Mac) or long-press (iPhone) for
rename, archive and delete. The iPhone's rows have no swipe actions: in
the menu a swipe left is the menu's own, to close it.

**The view menu** is the list's, unchanged: group (Mac: by who sent the
work, status, date, project, agent, machine; iPhone: the same but who sent
it), sort, show. Until the owner picks a view the list is **by status,
titles alone**. Group headings collapse, as before, and carry no count.

**Empty.** With no session at all the list says "No sessions yet" and
"Give Her something to do. The sessions she sends to agents show up here."
(a machine alone, which has no Her: "When your agents connect, their
sessions show up here."). By status, **In Progress stays** while nothing
runs, with "None right now" under it (`alwaysActive` in
`sessionSections`); Needs You shows only when something does. Archived
sessions on request, older ones loaded at the end, as before.

Gone: the team map (machines, their coding agents, connected agents,
requests to connect), All Sessions, the list's column beside a session
(the window is two columns again), the scoped lists, and the overview's
All Sessions link on the iPhone. A request to connect, a grant that ends
and a machine offline are on the overview, as before.

### 16.2 Machines and agents

Their settings are where settings are: on the Mac, Settings › Machines and
Grants, from the account menu; on the iPhone, Account and Devices has
Machines and Connected Agents, which open those pages. Their pages show a
machine's or an agent's session count as a value, no longer a link.

### 16.3 The rail

Two states, as §14.9: expanded or the rail, the owner's choice. The rail
holds Her and the overview, then one logo for each coding agent with
sessions at hand (waiting, failed, at work; `liveByAgent`), what waits
first: the rail says who is at work, the sidebar on what. One session:
the logo opens it. More: a menu beside it names the agent and lists each
session's title and mark; one click opens it (the keyboard as in a native
menu). The logo's tooltip says how its sessions stand ("Codex · 1 waiting
for you · 1 working"). Search and Expand are at its foot (the iPhone's
rail keeps Expand at its top and Search among the places).

### 16.4 The narrow window (Mac)

The card gives way first, then the sidebar shrinks to its rail; asked for
there, the sidebar shows over the conversation until it is put away, a
page or a session is chosen, or the selection changes. With the card:
1102 px (the expanded sidebar, the conversation's 560 and the card's 270);
the expanded sidebar alone 832 px.

### 16.5 Tests

- Units: `liveByAgent` and `alwaysActive` in `@hermesh/core` and
  HermeshSessions; the layouts (`layout.test.ts`, `LayoutTests`).
- Mac (`apps/desktop/e2e`): `frame.spec.ts` (groups, rhythm, rows and
  marks, view menu, collapse, row menu, narrow window), `team.spec.ts`
  (places, marks, no map, the rail and its agent menu),
  `sessionlist.spec.ts`, `onboarding.spec.ts` (both empty states).
- iPhone (`HermeshUITests`): `menu()` and `viewMenu()` in
  `testTheIPhoneApp`, `DemoUITests.testReviewHerAndHerTeam`,
  `WideUITests.testTheMenuBesideThePage`.

