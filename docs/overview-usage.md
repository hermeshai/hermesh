# The overview and the statistics: now on one page, the numbers on another

Status: proposed 2026-11-24. iPhone first; the Mac follows (§12, step 6).
The screens are in [overview-usage-iphone-demo.html](overview-usage-iphone-demo.html)
(its scenes: many connections, just started, the first session at work) and
[overview-usage-mac-demo.html](overview-usage-mac-demo.html).
Builds on [work-stats.md](work-stats.md); nothing there is counted again or
counted differently.

## 1. What changes

| Screen | Today | After |
|---|---|---|
| iPhone overview | To Do, a range picker, the work card (bars), the flow, Needs You / In Progress / Recently Finished, the agents' cells | **Only now.** A deck of what needs the owner, the link map, what runs as live tiles, the last three that finished with See All. No counts, no range. |
| iPhone statistics (new: two more views of the overview, by a segmented control at its top under its title, Live \| Usage \| Limits, as the Mac's; the menu gains no place) | — | **Usage**: one row of filter menus (range, coding agent, where, sender), then one card per measure (sessions, tokens, 18 weeks). **Limits**: per coding agent, how much of each rate-limit window is left and when it resets. |
| Tasks (new, from See All) | the drawer's Sessions list | The same sessions as a page, by day. |
| Mac overview | Needs You (a list), the link map with today's counts and lines by state, Working Now (cards, those waiting and failed among them), Usage (a year) | **Only now**, as the iPhone's. Needs You stays the list it is, in the deck's order (§4.1), with the limit row; the map loses its counts and its lines by state; what runs is a list, as is Recently Finished. No cards: a row a thing, its buttons at its end. Usage leaves. |
| Mac statistics (new: two more views of the overview page, by a control in its head, Live \| Usage \| Limits; the sidebar gains no place) | — | The iPhone's numbers, kept the same: the four filters with the same choices and the same counts under them, the same measures, the same limits. Drawn for the Mac as two views, as the iPhone has two: what was used and what is left are not one question, and the limits are a snapshot no filter changes. Usage: its filters in a row at its top (pop-up buttons, an environment's projects a submenu), a measure a row, its number at the leading side and its chart beside it (thin bars in the map's own grey-blue, the last in colour), and 53 weeks (the Mac keeps 400 days). Limits: a row an agent, the one with the least left first; a window says what is left twice, of its allowance (ticks) and of its time until it resets (a line under them, drawn from the reset time the status already carries), so one is read against the other; an agent that reports none says so. Both views' cards are the overview's ordinary cards with a faint tint of the Her panel's field (its lilac and sky only) over them: no glass, as these cards hold data and the Her panel's are buttons. Nothing is side by side that a narrow window must break: what is beside goes under. |
| Account sheet | usage: 18 weeks, active days, streak, share | Usage leaves; it is the Usage segment's last card. |

Three goals decide everything below, in this order: the overview costs
nothing to keep current; a statistics question is one small local answer;
nothing new is counted on machines.

## 2. What each screen reads

Everything but limits is already on the phone.

| On screen | From | New |
|---|---|---|
| Deck: a question | `Session.progress.pending_question` (`TeamState.waiting`) | no |
| Deck: a request to connect | `AppModel.pairings` | no |
| Deck: a session that failed | `TeamState.failed` | no |
| Deck: a grant about to end, notifications off, a second device, Get Started | the rows To Do and Get Started show today | no |
| Deck: a limit nearly used up | §7 | **yes** |
| Link map: senders, environments, their agents | `clients` (not revoked), `machines` and their `status.agents` | no (the Mac draws it; the iPhone does not yet) |
| Link map: a line per session not finished, a dot on those at work | `TeamState.running`, `.waiting`, `.failed` | no |
| Tile: title, sender → agent · project, time at work, the tool in use | `Session.title`, `created_by`, `agent`, `project_id`, `progress.turn_started_at`, `progress.current_tool` | no |
| Recently Finished, Tasks | the held sessions and `loadOlder` (the drawer's list) | no |
| Usage: sessions that worked, done, failed turns, time at work, input and output tokens, per hour or day | `QueryWork` (§6) | recorded since work-stats; failed turns, time at work and the split of tokens were never shown |
| Usage: the same, one period earlier | `QueryWork` with `previous` | a field |
| Usage: only one agent, one environment or one of its projects, one sender | `QueryWork` with `filter` | a field |
| Usage: 18 weeks of tokens, active days, streak | `QueryWork`, 126 days by day | no (moves from the account sheet) |
| Limits | `MachineStatus.agents[].limits` (§7) | **yes** |

## 3. Decisions

1. **The overview asks for no counts.** Every element of it is a function of
   what `Watch` already keeps current on the phone: sessions, grants,
   machines, requests. It needs no `QueryWork`, no timer and no relay call,
   and it is live to the second where today's bars are a minute behind.
   Today the overview asks `QueryWork` again each time the counts move, and
   `workChanged` also recounts 18 weeks whether or not anything shows them;
   both go away.
2. **A statistics screen asks one question per state and gets a small
   answer.** Filtering and the earlier period are done in hermeshd, beside
   the data (§6): the app never receives rows it will throw away, and the
   answer for any filter is tens of rows. The SQL does not change shape;
   there are no new tables, indexes or machine changes.
3. **Limits are a snapshot, not a count.** The agent says what is left; the
   machine keeps the last thing each agent said and tells it with its status
   (§7). No history, no table, no query.
4. **What a screen shows is made by pure functions.** The overview's needs,
   its map and the map's layout are functions in `HermeshSessions` of what
   the model holds, as `TeamState` is today; views draw them and hold only
   what is theirs (the card in front, the node picked). The Mac builds its
   own from the same rules (§12).

## 4. The overview: its state

```swift
// HermeshSessions/Overview.swift
public func needs(team:pairings:expiring:guide:herOff:notificationsOff:secondDevice:) -> [Need]  // the deck, the most pressing first
public func linkMap(senders:environments:team:owner:) -> LinkMap
public struct LinkMapLayout { init(_ map: LinkMap, width: CGFloat) }
public func sessions(through node: String, team: TeamState) -> [Session]
```

with `TeamState` for what runs and what finished. As built these are
functions the page calls, not one stored value: each is pure and tested, and
the page keeps nothing of them. They run again when the page's inputs change
(the held sessions, the grants, the machines, the requests, the guide's
step, the settings the reminders read), on the main actor. Their cost is one
pass over the held sessions (the first page of 50 and every unsettled one,
so hundreds at most) on top of `TeamState`, which already makes that pass:
about a millisecond for 500 held sessions in a debug build
(`theOverviewIsMadeFast`).

### 4.1 What needs the owner

| Kind | Is there when | Order | Done in place |
|---|---|---|---|
| question | a session waits for an answer | 1, the latest first | its options, when one tap can answer (as `QuestionCard` decides today); else opens the session |
| request to connect | `pairings` | 2 | Verify (the pairing sheet) |
| failed | a session's last turn failed | 3 | opens the session |
| limit | a window of an agent has 20% or less left and has not reset (§7.4) | 4 | opens Limits |
| grant ends | within the days To Do warns today | 5 | Renew |
| Get Started | the guide is not done or hidden | 6, alone when nothing else is | Continue, Hide |
| notifications off, second device | as To Do shows them today | 7 | their buttons today |

Someone or something waiting comes first, then what went wrong, then
reminders. Each `Need` has a stable id (the session id, the pairing code,
`limit:<agent>`, …) so the deck keeps its place when the list changes
(`deckFront`). A question or a request to connect that just came goes in
front (`arrived`): it is why the phone rang. Her not being on anywhere is a
need too, after the guide. The deck is the only thing above the map and
takes one card's height whatever its length; with nothing to show it is not
there and the map is first.

The cards page sideways in a paging `ScrollView` (the system's own
physics, and it scrolls with the page's vertical scrolling without a
gesture of ours); the strip of the cards behind is a button to the next.
The menu's swipe from anywhere lets a sideways scroller that is not at its
start have a swipe to the right first, so going back a card does not open
the menu; on the first card it opens it, as anywhere on the page.

### 4.2 The link map

```swift
public struct LinkMap: Equatable {
    public var senders: [Node]        // grants, the busiest first; then "Connect an Agent" when fewer than 3
    public var environments: [Node]   // this device's own first, online before offline; then "Add Environment" when fewer than 3
    public var lines: [Line]          // a session not finished: sender, environment, atWork
}
```

- **Lines are alike.** A session that is running, waiting or failed is one
  line along sender → Hermesh → environment; the ones at work carry a moving
  dot. State is said by the deck, not by the line.
- **The owner is a sender** ("You") while a session the owner started has
  not finished; grants are senders always. A session of a grant not shown
  (revoked, or among "N more") has no line.
- **A node picked** keeps what goes through it and puts every session
  through it (at work, waiting, failed, finished) in one list under the map,
  in place of the tiles and Recently Finished. It does not move the deck.
- **The card in front** dims the rest of the map for a moment when it
  changes, leaving its line or node; at rest the map is whole.
- **Few connections.** A side draws what it has and nothing more: adding a
  grant or an environment is rare, so the map has no dashed place for one
  (it had, while a side had fewer than three; taken off on the Mac and the
  iPhone 2026-11-25). They are added from the menu, New and the Mac's File
  menu. The map's height follows its rows.
- More than six on a side: the first five and "N more" (the Mac's rule).

Layout is a pure function from `LinkMap` and a width to points and paths,
tested without a view.

**Drawing.** Links and lines are one `Canvas` (a handful of paths, drawn
when the state changes, not per frame). The dots are Core Animation layers
moved along their path by `CAKeyframeAnimation`, so the render server moves
them and the app runs no code per frame; they are removed when nothing is at
work, when Reduce Motion is on, in Low Power Mode, and while the page is not
on screen. This is the Mac's choice too (tracks sampled once, moved by the
compositor).

### 4.3 What runs, what finished, Tasks

- A tile's clock is `Text(timerInterval:)` from `progress.turn_started_at`:
  the system ticks it, the model does not. The tool line is
  `progress.current_tool`, which changes only when the session does.
- One session at work: its tile takes the row.
- Recently Finished is the first three of `TeamState.finished`; See All
  opens Tasks: the held sessions by day (Today, Yesterday, Earlier), those
  not finished first within a day, paged with `loadOlder` as the drawer's
  list is. It is a second view of that list, not a second store.

### 4.4 Nothing yet

| Where | Shows |
|---|---|
| Something can be started (no session ever, on the overview and on Tasks) | a dashed card, in the place of the first tile: "+ New Session", and under it that sessions from Her, a connected agent or the owner all show here |
| Nothing can be done (Usage and Limits with nothing to count) | two centered lines: what is missing, and when it will be there; the filter row is not shown |
| Nothing to say (nothing needs the owner, nothing finished) | the section is not there |

## 5. The overview: what it costs

| | Today | After |
|---|---|---|
| Opening it | one `QueryWork` (two aggregations), then again on each move of `work_version` | none |
| A session changes | lists redraw; counts asked again within a second | the needs and the map made again (one pass), only what differs redraws |
| While a session is at work | the "N running" label | a compositor animation and a system clock; no app code per frame |
| A machine's counts move | `QueryWork` again and 18 weeks recounted, shown or not | nothing, unless Statistics is on screen |
| Relay calls | none | none |

## 6. Usage

### 6.1 The questions

| State | Request | Answer |
|---|---|---|
| The page (range and filters) | the range by hour (Today) or day, `filter`, `previous`; `group_by` as the bars are grouped (§6.9) | ≤ 30 rows of sessions and of turns, as many a part, the range's totals, the earlier period's totals |
| The three menus' choices | the range, `WORK_BUCKET_NONE`, `group_by` all four dimensions, no `filter` | one row per (sender, agent, machine, project) that worked: tens of rows, a few hundred at most |
| 18 weeks | 126 days by day, `filter`, `turns_only` | ≤ 126 rows |

The page and the 18 weeks are asked when Usage comes on screen, when the
range or a filter changes, and when `work_version` moves while it is on
screen (at most once a second, as today). The menus' answer is asked with
the page but only when the range or `work_version` changes, not when a
filter does: it is not filtered, and each menu's lines are added up from it
in the app under the other two filters (exact, work-stats §4). A system
menu is built before it opens and has no moment to ask in (§6.3), so its
lines must already be there. Nothing is asked while Statistics is not on
screen.

### 6.2 Where: an environment, or one of its projects

A project's id is its machine's own: the owner names it when adding the
folder, each machine keeps its own list, and the app writes one as
`environment/project` wherever it names it (a session's page; "Agents find
projects as name/project" in an environment's settings). Two machines can
both have a `hermesh`; nothing says they are the same repository, and
nothing recorded could (no remote is kept). So a project is never counted or
chosen under its name alone.

**One filter, not two.** Environment and project are one choice with three
shapes: everywhere; one environment; one project of one environment.

```swift
enum Where: Hashable { case everywhere, environment(String), project(machineID: String, projectID: String) }
```

- **One menu of two levels** (§6.3): Everywhere and the environments; in an
  environment, All Projects and its projects, each with its sessions and
  tokens in the range. The button reads "Environment / Project" when nothing
  is chosen, the environment's name, or `environment/project`.
- **No state that disagrees.** With two menus the owner could hold "Mac mini"
  and a project of the MacBook, and the app would need rules to repair it
  (choosing an environment clears a project of another, a project sets its
  environment). One value has no such state and needs no rule.
- **One button fewer**: the row is four buttons and fits the phone's width.
- **What it lists**: every environment and project with work in the range
  under the other filters, from the answer, not from the lists of machines
  and projects now. So a project since removed, or an environment since
  removed, still appears with its work (work-stats §6.2 keeps their
  history). The one chosen stays listed when it has none.
- **Names.** A machine's name is read at draw time from `machines`, so a
  rename shows at once and the filter, held by `machine_id`, does not break.
  A removed machine has no name left on the device (`work_machines` keeps
  only its id); it shows as `machineName` does today, the id's first 12
  characters. Keeping the last name in `work_machines` would mend that; it
  is not needed for the filter.

Not chosen: **two menus with repair rules** (above); **a project by name
across environments**, which adds up folders that only share a name; **a
project menu alone, each line `environment/project`**, which loses "this
whole environment" unless the environment menu stays, and then is the first
again.

### 6.3 The menus are the system's

(Since §6.9 the range's menu is its own button and the other three are menus
inside one Filters button's; what follows holds for each of them.)

The four filters are SwiftUI `Menu`s, built as the app's own already are
(`SessionsView`'s Group and Sort, `ThreadPage`'s project and environment).
No drawn popover, and no tree: a system menu cannot indent a line.

```swift
Menu {                                            // Environment / Project
    Button { place = .everywhere } label: { Label { Text("Everywhere") } icon: { check(place == .everywhere) } }
    ForEach(environments) { e in
        Menu {                                    // the second level
            Button { place = .environment(e.id) } label: {
                Label { Text("All Projects"); Text(e.counts) } icon: { check(place == .environment(e.id)) }
            }
            Section {
                ForEach(e.projects) { p in
                    Button { place = .project(machineID: e.id, projectID: p.id) } label: {
                        Label { Text(p.id); Text(p.counts) } icon: { check(place == .project(machineID: e.id, projectID: p.id)) }
                    }
                }
            }
        } label: {
            Label { Text(e.name); Text(e.counts) } icon: { check(place.machineID == e.id) }
        }
    }
} label: { chip }
.menuOrder(.fixed)
```

What the system's menus ask of the design:

- **A line that opens a menu cannot also be a choice.** So "this whole
  environment" is the first line inside it (All Projects), not the
  environment's own line; choosing an environment is two taps, a project
  two.
- **One environment only: no first level.** The menu is Everywhere (All
  Projects) and that environment's projects, flat; the button reads
  "Project".
- **The check is the line's icon** (`ThreadPage`'s way), because the lines
  have a second text. A line therefore carries no logo: the agent and sender
  menus are text. An environment's line is checked when the choice is it or
  one of its projects.
- **A second text shows only when it is always there** (the note in
  `ThreadPage`): every line here always has one, the counts or "No
  sessions".
- **Built before it opens.** SwiftUI has no dependable moment when a menu
  opens (an `onAppear` inside one is not a promise), so nothing is asked
  then (§6.1). UIKit's `UIDeferredMenuElement` could
  ask on opening, behind a `UIButton` wrapped for SwiftUI; with one small
  answer already held that is not worth leaving SwiftUI for.
- **While open it does not change.** The lines are those of the moment it
  opened; a count that moves shows the next time.
- **`.menuOrder(.fixed)`**, so the lines keep their order (the busiest
  first) wherever the menu opens.
- Long lists scroll on their own; nothing folds by count.
- To check on a device, since the app has no menu yet that does both: that a
  line opening a menu draws its icon and its second text together.

The range is a `Picker` in its `Menu` (three lines, no second text), as
`SessionsView`'s are.

### 6.4 QueryWork (local.proto)

```proto
message QueryWorkRequest {
  ...
  // Only the work of this client, agent or machine; an empty field: any.
  // project_id needs machine_id (a project is its machine's own): alone it
  // is refused. As group_by: sessions by who started them, turns by who
  // asked.
  WorkGroup filter = 6;
  // Also count the span of the same days just before the range, cut at the
  // same moment before now when the range holds now: previous_* below.
  bool previous = 7;
  // Leave out sessions and range_sessions: for charts of turns and tokens.
  bool turns_only = 8;
}

enum WorkBucket { ...; WORK_BUCKET_NONE = 3; } // the range's totals only

message QueryWorkResponse {
  ...
  repeated SessionCount previous_sessions = 6; // as range_sessions
  repeated TurnCount previous_turns = 7;       // as range_turns
}
```

`WorkGroup` already has the four fields. One value a dimension is what the
screens need; several would be a `repeated` later.

### 6.5 How it is answered

- **Filter.** The key dictionary (`work_keys`, a few dozen rows a machine)
  says which `(machine, key)` pairs match; a project matches only the keys of
  its machine. `workstats.Count` already returns
  rows per `(bucket, machine, key)`; `groupWork` drops the pairs that do not
  match before adding up. The scan is the same range of the same clustered
  keys; there is no new index. Exact, since a session has one client, agent,
  machine and project (work-stats §4).
- **Previous.** One more bound in the query's `VALUES` list (bucket −2), so
  both periods are one statement and one pass. For a range that holds now,
  the earlier bound ends the same time before now ("than yesterday at this
  time"), so a day in progress is compared with as much of the day before.
- **Turns only** skips the presence statement, which is the dearer of the
  two (distinct sessions, a join).
- **None** sends only the whole range's bound.
- `workstats.Count` takes the extra bound and the flag; `machine.CountWork`
  and the owner store's `countWork` pass them on, so this device's own
  machine (read in process) and its copy of the others answer alike.

No table, column or write changes on machines or in `owner.db`. Replication
is untouched.

### 6.6 The numbers

| On screen | From |
|---|---|
| Sessions that worked; done | `range_sessions` |
| Failed turns, time at work | `range_turns.failed_turns`, `.busy_seconds` |
| Tokens; input, output | `range_turns.input_tokens`, `.output_tokens` |
| Bars | `sessions` or `turns` per bucket |
| "Up 12% on the 7 days before" | `previous_sessions`, `previous_turns` |
| A menu's choices and their second lines | the `WORK_BUCKET_NONE` answer by all four dimensions, added up in the app under the other filters |
| 18 weeks, active days, streak | `turns` per day (`usageYear`, as today) |
| "vps-1 counted up to …" | `sources` |

Input tokens include what the agents read from and wrote to their caches
(the drivers add them in); the page says so under the tokens card.

### 6.7 In the app

`StatsModel` (`@Observable`) holds the range, the filter (an agent, a
`Where`, a sender) and the answers by
`(range, filter)`, so going back to a state shows its last answer at once
and the new one replaces it. A request in flight for a state no longer shown
is cancelled. At most 16 answers are kept. `AppModel.work`, `usage`,
`overviewShown`, `loadWork` and `loadUsage` give way to it.

### 6.8 Speed

The statements are work-stats §7's, with one more bound for `previous`.
Measured by `BenchmarkQueryWork` (internal/owner, built 2026-11-24) on an
owner device's copy of five busy machines over 130 days (18,000 measure rows
and as many quarter rows in 30 days: twenty sessions at work at a time, seven
and a half hours a day), on a Mac (M2 Max), counting and grouping:

| Question | Measured | Target (§9, set for an iPhone) |
|---|---|---|
| The page: 30 days by day, with the 30 before, filtered | 129 ms | 40 ms |
| The weeks: 126 days of turns only, filtered | 79 ms | 40 ms |
| The menus: 30 days, totals only, by all four dimensions | 53 ms | 20 ms |

**The targets are not met**, on a Mac, so not on an iPhone either; the OS's
SQLite (what the iPhone uses) measures the same as the bundled one. The time
is SQLite's: about 1 µs a measure row and 1.8 µs a quarter row, because
`GROUP BY` sorts the rows it reads (they come by quarter, the groups are by
machine and key) and the sessions are counted distinct through a join. That
was so before this change (30 days by day without `previous` measured
220 ms with sixty sessions at a time), only never measured. What was done
here: with buckets, the whole range's turns are the buckets' sum, so its rows
are read once (the weeks went from 181 ms to 79 ms).

What it means for the screens: an answer comes off the main thread and a
state shows its last answer until the new one replaces it (§6.7), so the
page does not wait; it is not the instant answer §9 asked for. What would
bring it under the targets is the counts kept by `(m, k, q)` as well (an
index, or the finished days added up once), which is a change of the tables
work-stats.md owns and is not part of this step.

### 6.9 Grouping the bars

A filter says whose work is counted; **分组** (Group) says what the bars are
in parts of. It is a segmented control at the trailing end of the filter row,
not a fourth pop-up beside them, so the two are not read as one kind of thing:
**不分组 | Agent 提供方 | 环境 / 项目** (None | Agent Provider | Environment /
Project). Every way shows the same totals (the number, the bars' heights);
they differ only in whether, and by what, the bars are split, so the first
is named for what it leaves out, not "Total", which all of them are. The page opens on Agent Provider; the choice is kept with the range
and filters while the overview shows its other views. The screens are in
[overview-usage-breakdown-mac-demo.html](overview-usage-breakdown-mac-demo.html).

- **The bars in parts.** Grouped, each bucket's bar in both charts is its
  parts one on another (the busiest at the foot), each in its colour; the bar's
  height is still the whole, so the scale and the left side's number, change
  and figures are as they were. Total is a bar a bucket, as before.
- **The legend** is a line over each chart: a part's colour, name, number and
  share of the whole, the busiest first. Pointed at, the part stands out in
  both charts. Clicked, the part is seen alone in both, on the same scale; the
  page's filters and numbers do not change, and clicking it again, or changing
  the group, shows all. Picking a part as a filter is the filter menus' job.
- **Where** has two levels, as its menu: the environments; with one
  environment chosen in the filter, or only one that worked, its projects (a
  project is its machine's own, §6.2).
- **Colours** are the data-viz palette's first six, told apart in colour
  blindness on the light and dark cards. A part's colour is its place in a
  fixed order (the coding agents' own list, this Mac and then the machines as
  the app lists them, projects by name) while every part drawn falls within
  six, so an agent keeps its colour whatever the range or the filters; else
  each takes its place among those drawn, in that order. More than six parts:
  the five busiest, and the rest added up as one grey part, Other.
- **One question.** The page's own `QueryWork` carries `group_by` (`AGENT`, or
  `MACHINE` and `PROJECT`), and `usageParts` (`packages/core/src/usage.ts`)
  makes the parts from its answer; the totals are the sum of the groups, as a
  session has one agent, machine and project. `groupWork` groups rows hermeshd
  has already read, so this asks nothing more of SQLite (§6.8).
- Every bar's top is rounded 3 px, as the page's other corners, not a capsule.

**The iPhone** groups by the same rules (`usageParts` in
`HermeshSessions/Statistics.swift`, the same cases as the Mac's tests), drawn
for its width. The screens are in
[overview-usage-breakdown-iphone-demo.html](overview-usage-breakdown-iphone-demo.html).

- **Two rows of controls, not three.** Under Live | Usage | Limits, one row
  holds how the page is looked at: first the grouping, the row's widest and
  most used control (a segmented control: 不分组 | 按 Agent | 按环境, None |
  Agents | Machines in English, and By Project in place of By Environment once
  one environment is chosen or only one worked); then what is counted, side
  by side at the trailing end: the range (a menu) and one button the three
  filters are behind. Its menu holds a menu a filter,
  each saying under its name what is chosen. A filter in force tints the
  button, which counts them, and is named in a row under it that scrolls
  sideways; its cross lets it go. So the row is there only when something is
  filtered, and what is filtered is always said. The cost is one more tap to
  choose a filter; a part tapped in a legend answers most of what a filter was
  opened for.
- **Four parts in the bars**, where the Mac draws six: every part when there
  are four or fewer, else the three busiest and the rest as Other. A bar is
  72 pt tall (56 before), in every grouping, so four parts can be told apart.
- **The legend is the card's own**, in two columns between its number and its
  bars (one column at the accessibility type sizes): a part's colour, short
  name, number and share. Other says how many it holds and opens in place to
  every part, those in it grey; both cards open and close together. A part
  tapped is seen alone in both charts (one of Other's in the accent).
- **A bar tapped** says over itself what it stands for, as the Mac's does
  under the pointer: its whole, when, and each part that has any. Tapped
  again it is let go; held, the finger moves along the bars (a day of thirty
  is 8 pt wide). It works in every grouping, the total too.

## 7. Limits

### 7.1 What the agents say

Seen in one real turn of each (step 0, 2026-11-24); the lines are the
drivers' fixtures (`internal/provider/claude/testdata/claude-2.1.330-rate-limit-event.json`,
`internal/provider/codex/testdata/codex-0.171-rate-limits-updated.json`):

| Agent | Where | What |
|---|---|---|
| Claude Code (2.1.330) | a `rate_limit_event` line of its output, once a turn | `rate_limit_info.unifiedWindows`: per window by name (`five_hour`, `seven_day`) a `utilization` from 0 to 1 (0.26 is 26% used) and `resetsAt` in Unix seconds |
| Codex (app-server 0.171) | the notification `account/rateLimits/updated` during a turn (dropped today in `skipNotifications`) | `rateLimits.primary` and `.secondary`, either of them null (the account seen has only a week: 10,080 minutes): `usedPercent` a whole number, `windowDurationMins`, `resetsAt` in Unix seconds; `planType` |
| Antigravity (agy 1.5.16) | nothing in a turn; asked only (below) | |
| Others (ACP) | nothing common | no limits; the page says the agent gives none |

**Asked, without a turn** (2026-11-25; fixtures `claude-2.1.330-get-usage.json`
and `codex-0.171-rate-limits-read.json` beside the others). Both answer the
same windows through their own CLI when asked, so the machine asks them
whenever it checks its coding agents (§7.2):

| Agent | How | What |
|---|---|---|
| Claude Code (2.1.330) | the control request `get_usage` (`skip_behaviors`: only the limits, not the scan of every transcript), on the process that already answers `initialize` for the models | `subscription_type` (the plan); `rate_limits.five_hour` and `.seven_day`: `utilization` from 0 to 100 here, `resets_at` in RFC 3339; `rate_limits_available` false with an API key. Much else (per-model weeks, spend) is left out |
| Codex (app-server 0.171) | the request `account/rateLimits/read` (no params), on the app-server that already answers `model/list` | `rateLimits` as the notification has them, and the account's id, which is not kept |
| Antigravity (agy 1.5.16) | its own `/usage`, run headless: `agy -p /usage --output-format json` (no turn, no tokens, no conversation), once the agy CLI is chosen and signed in | `command.data.groups`: groups of models counted apart (`Gemini Models`, `Claude and GPT models`), each with buckets of `window` `5h` or `weekly`, `remaining_fraction` 0 to 1 and `reset_time` in RFC 3339. With an API key it exits 2: none (fixture `internal/runtime/acp/testdata/agy-1.5.16-usage.json`) |

Antigravity's ACP says its plan (`_meta.antigravity.tier`) and nothing of
its limits, and `/usage` sent as a prompt over ACP goes to the model. Its
`-p` slash commands are agy's own (its changelog: `/usage`, `/quota` and
`/credits` in `-p` runs). The standalone ACP server (no agy) is not asked.
Hermesh does not read agy's sign-in or call Google's unpublished endpoints
itself.

**Groups.** A window may name the models it counts (`LimitWindow.group`):
Antigravity counts groups apart, each with its own five hours and week;
Claude Code and Codex count the account's, with no group. Windows come by
group in the order the agent gave them, the shortest first in each.

**Neither names the account.** Codex's id is only in the answer to the
request `account/rateLimits/read`, which a turn does not make. So a device
cannot tell two accounts of one agent apart, and keeps one card an agent
(§7.4). A Claude window whose name it does not know is left out; the names
are kept in the driver beside their lengths.

### 7.2 From the agent to the machine

```go
// provider
type Limits struct {
    Plan    string
    Windows []LimitWindow // the shortest first
}
type LimitWindow struct {
    Minutes     uint32
    UsedPercent uint32    // 0 to 100
    ResetsAt    time.Time
    Group       string    // the models it counts, when counted apart; "" for the account's
}

type StartSpec struct {
    ...
    // Limits is told what the agent says of its account's limits, when it does.
    Limits func(Limits)
}
```

A callback, not an `Event`: limits belong to the account, not to the
session, and must not enter a session's event log.

Asked, they come back with the agent's check: `Info.Limits`, filled by the
Claude Code, Codex and Antigravity (agy) drivers' `Discover` when the agent is logged in, nil
when it said none. The machine keeps them as it keeps a turn's
(`noteAsked`).

Claude Code says its line once a turn and Codex once or a few times; both
are small, so the drivers parse each one (Codex's is no longer skipped). The
machine keeps one unchanged no more than once a minute (§7.3).

### 7.3 On the machine, and to the devices

The machine keeps the last `Limits` per agent, with when it was said, in
memory and in its store (`limits:<agent>`, one small row an agent;
`internal/machine/limits.go`), so a restart does not forget it. Said again
unchanged it is kept, and told, no more than once a minute; changed, at
once.

```proto
// machine.proto
message AgentStatus {
  ...
  AgentLimits limits = 16; // unset: it never said
}
message AgentLimits {
  repeated LimitWindow windows = 1;
  string plan = 2;
  google.protobuf.Timestamp observed_at = 3;
}
message LimitWindow {
  uint32 minutes = 1;
  uint32 used_percent = 2;
  google.protobuf.Timestamp resets_at = 3;
  string group = 4; // Antigravity: "Gemini Models"; empty for the account's
}
```

It rides `MachineStatus`. A machine's `Watch` compares its status with the
last it sent once a second and sends it when it changed (`proto.Equal`), so
a device has a change within a second of the agent saying it. The status an
agent with a grant reads leaves the limits out, as it leaves out Her and the
screen: they are the owner's accounts.

### 7.4 On the device

- **One card per agent**: of the machines that said something, the one that
  said it last; the card names that machine and how long ago. The agents do
  not name the account (§7.1), so two machines signed in to two accounts of
  one agent show as one card, the latest.
- A window whose `resets_at` has passed is not shown until the agent speaks
  again: what is left then is not known.
- An agent that counts groups of models apart shows each group's name over
  its windows; one that does not, its windows alone.
- A window with 20% or less left, not yet reset, is a `Need` (§4.1); the
  least left of the card's, named with its group when it has one.
- A window not started (the agent gives no reset time; agy says
  1970-01-01 for one not yet used) is whole until first used: shown at
  100% left, its time whole, and "starts counting when first used" for
  when it resets. Before, it was taken for one that had reset and hidden,
  and a group of models all of whose windows were unused vanished.
- On the card, such a window says so itself: its number and its bar in the
  warning colour. The card's corner always shows the account's plan (when
  the agent says one), not a "running low" badge: a card holds several
  windows, often of several groups of models, and one running low is not
  the whole account running low.
- Limits move while that agent runs, and when the agents are checked: the
  machine's first check after it starts, and on the Mac each time the page
  opens (it asks for a check, as the Providers page does). Nothing polls; a
  card from another computer has what that one last said.

### 7.5 What it costs

A machine: one gated parse every five seconds per running session, one small
`meta` write a minute at most, one status a minute at most; asked, one more
request each on the processes a check of Claude Code and Codex already
starts (a second or two of network), and one `agy -p /usage` (a few
seconds) beside Antigravity's; never a turn. A device:
nothing beyond the status it already receives. The center learns nothing:
the status travels end to end as it does today.

## 8. What goes away

- iPhone: the overview's range picker, `WorkCard`, `FlowCard`, the agents'
  rows and cells, `HeatLegend`, `HeatAxis`, To Do and Get Started as sections
  (their rows become `Need`s), `QuestionCard` as a list row (its answering
  stays, on a deck card); the account sheet's `UsageSummary`; `AppModel.work`,
  `usage`, `overviewShown`, `loadWork`, `loadUsage`; `RangeWork`'s flow.
- `TeamLists` stays for Her's team panel, which shares it.
- Nothing in Go or the protocols is removed.
- Docs at the end: work-stats.md §8 (the screens) and §13 (filters and the
  previous period are done), iphone-app.md.

## 9. Performance budget

| | Budget | How it is kept |
|---|---|---|
| Overview, opening | 0 `QueryWork`, 0 relay calls | §3.1 |
| The needs, the map and its layout from 500 held sessions | ≤ 1 ms | one pass, pure; a unit test times it |
| Link map layout, 6 × 6 nodes and 20 lines | ≤ 0.5 ms | pure; a unit test times it |
| Dots, a session at work | no app code per frame | Core Animation; off when hidden, Reduce Motion, Low Power |
| Usage page, 30 days, 5 busy machines, with `previous` | ≤ 40 ms on iPhone, off the main actor | the Go benchmark |
| Usage, 18 weeks, turns only | ≤ 40 ms on iPhone | the Go benchmark |
| The menus' answer (the range, four dimensions, no buckets) | ≤ 20 ms; asked once a range and `work_version`, not per filter | the Go benchmark |
| Answer size | the page and 18 weeks ≤ 150 rows; the menus' a few hundred at most | filter and grouping in hermeshd |
| Limits on a machine | ≤ 1 parse / 5 s a session; ≤ 1 status / min | the gate; a machine test with a fake clock |
| Storage | 0 new on devices; one `meta` row an agent on machines | — |

## 10. Tests

Unit:

- `workstats`: with a filter, the groups kept add up to the unfiltered
  total; two machines with a project of one name are two groups, a filter on
  one leaves the other out, and `project_id` without `machine_id` is
  refused; `previous` equals a second query over the earlier bound; a day in
  progress is cut at the same time the day before, across a DST change;
  `turns_only` and `NONE` return what the full answer holds of them.
- Machine: each driver turns a recorded limits line (step 0's fixtures) into
  `Limits`; the gate; `meta` survives a restart; a status is sent when a
  percent moves and no more than once a minute.
- `HermeshSessions`: the needs of each kind and their order;
  ids stable across a change; `LinkMap` for one sender and one environment
  (no place to add one), for many, for more than six; a node picked.

End to end (as the owner uses it):

- `e2e/work_test.go`: a project of the same name on two machines; the
  Mac's `QueryWork` filtered to one of them counts only its sessions, then with `previous` after a day is made to pass.
- A machine with the fake agent that says its limits: the owner device's
  `Watch` carries them; below 20% they are there at once.
- iPhone UI test (on the simulator the e2e uses): a question answered on the
  deck and the next card comes; the deck dragged; a node picked and the
  sessions through it listed; See All opens Tasks; Statistics filtered by an
  agent, then by one environment's project, changes the totals; with one environment and Her alone, the
  New Session card and no place on the map to add one.

## 11. Success criteria

| | Target | Measured by |
|---|---|---|
| The overview asks for no counts | 0 `QueryWork` while it shows | an owner test counting calls behind the UI test |
| A session's change reaches the overview | within the `Watch` message that carries it | the UI test |
| Filtered totals | equal the sum of the matching groups of the unfiltered answer | the `workstats` test |
| Usage, 30 days with `previous` | ≤ 40 ms on iPhone | the benchmark |
| Limits reach the phone | within one status of the agent saying them; at once below 20% | the e2e |
| A dropped limits line | costs what it costs today | a driver benchmark with and without the gate |

## 12. Steps

Each is one change that can ship alone.

0. **Seeing the limits** (done 2026-11-24, §7.1). Claude's utilization is
   from 0 to 1; neither agent names the account, so `Limits` has no account
   and a device one card an agent.
1. **hermeshd: `QueryWork`** (built 2026-11-24). `filter`, `previous`,
   `turns_only`, `WORK_BUCKET_NONE`; the benchmark, whose targets are not
   met (§6.8); the e2e (`TestWorkOnIPhone`, which does not make a day pass:
   the span before is asked as today before tomorrow's range).
2. **iPhone: the overview** (built 2026-11-24). The needs, the link map
   and its layout in `HermeshSessions/Overview.swift`; the deck, the map,
   the tiles, Recently Finished and Tasks, the empty states; To Do and Get
   Started as sections go. Needs neither step 1 nor limits. Until step 3
   the old counts (range, work card, flow, agents) are moved as they are to
   a Statistics place in the menu (and the rail), so nothing is lost
   meanwhile and only that page asks `QueryWork`. It is a place, not a
   button of the overview's bar: the bar has room for one button beside the
   line that says how the connection stands, which must stay in its middle
   (the end-to-end test measures it).
3. **iPhone: Usage** (built 2026-11-24). `StatsModel`, the filter row and
   its menus, the three cards; the account sheet's usage goes. Usage and
   Limits are views of the overview, picked at its top under its title (the
   bar's middle stays the connection's line), as the Mac's are: the
   Statistics place of step 2 is gone from the menu and the rail.
4. **Machines: limits** (built 2026-11-24). The callback, both drivers
   (tested on the recorded lines), the machine's store, the status, the
   owner's only; the e2e `TestLimitsReachDevices` (the Mac, and an iPhone
   by the machine's watch).
5. **iPhone: Limits**, and the limit `Need`.
6. **Mac** (the Mac demo). The overview and the statistics as §1 says. The
   statistics ask what the iPhone's ask (steps 1 and 4 are theirs too), so
   a filter or a measure added later is added to both; the old `Usage.tsx`
   and the map's counts go. Live and Usage are built (2026-11-24): the
   requests and what the page shows are `packages/core/src/usage.ts`
   (`usagePage`, `usageChoices`), the filters' menus are the system's
   (`popupMenu`, with a second line and a submenu), and Recently Finished
   has See All where the sidebar shows only its rail (every session is the
   sidebar's list; the Mac has no Tasks page). Limits and the limit row of
   Needs You are built too (`packages/core/src/limits.ts`, `overview/Limits.tsx`;
   the spec `limits.spec.ts`, on a second Mac whose fake agent says limits).
   An agent that reports limits and has not yet says so ("shows after it
   runs a task"); one that never will says it gives none.
   Grouping the bars (§6.9) is built (2026-11-25): `usageParts`, the 分组
   control and the legends in `overview/Usage.tsx`, checked in
   `overview.spec.ts`. The iPhone's is built too (2026-11-25): the row of
   controls, the legends and the bars in `StatsPages.swift`, checked in
   `DemoUITests.testReviewOverviewAndSettings` and `HermeshUITests.usage`.
7. **Docs** (§8).

## 13. To settle

- **Asking Codex outright.** `account/rateLimits/read` could refresh a card
  without a turn, but only while a Codex process runs for a session; not
  planned.
- **Words.** The dashed card says "New Session" where lists say "Tasks".
- Get Started on the deck shows the next step only, not the four.

## 14. Not doing

- Limits over time (no history is kept), a forecast of when a window runs
  out, alerts by notification.
- Tokens read from and written to caches as their own measure, and work by
  model: both need a column or a key in the counts and a rebuild on
  machines (work-stats §5.6).
- Cost in money.
- The flow (who asked which agent, over a range) as a picture: the sender
  and agent filters answer it in numbers.
