# Her and her team: one product from Her and the mesh

Status: stage 1 built on the iPhone 2026-11-13, with acceptance results
updated 2026-11-14 (§13); stage 2 (§6) is not built. The
interaction was agreed on a clickable iPhone prototype on 2026-11-13; this
document says what it takes to build it on the system as it is. It covers
the iPhone app.
The Mac app follows the same model and gets its own section once the
iPhone is built (§12).

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
conversation. It closes by a tap outside, a swipe up, or opening a row.

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
point, and stays current.

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
makes no card. `session_send` and the other tools make none.

One card per session. Sessions that others started are never in Her's
conversation.

### 4.5 The menu

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
  call and a result that is not the document make none; `session_send`
  makes none.
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
- Stage 2 (§6).
- The Mac app (§12).

## 12. Open questions

1. **The Mac app.** Its sidebar lists sessions under Her in the same way.
   The model carries over (the tag, the map, Work); the layout is its own
   design, with a demo first.
2. **The coding agents' cells on Work** (§4.6, item 8). The prototype
   leaves them out; they are a function people have today and nothing else
   shows it. Keep, or move to each agent's page?
3. **Failed sessions on the tag.** "1 failed" stays until the session is
   opened, archived or runs again. Should it count at all, or only show in
   the panel?
4. **B1 first?** With Her as the lead, "answer Codex for me" is the first
   thing people will ask her, and in stage 1 she must say she can't for
   sessions others started. It is a small change on the machine, but it
   widens what a compromised Her Server can do.
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
