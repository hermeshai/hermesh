# The files Her delivered, in a place of their own (2026-11-16)

Status: accepted 2026-11-16 with the owner's decisions (§7), and built the
same day (§6.1 to §6.3).
Prototype: [her-deliverables-demo.html](her-deliverables-demo.html) (it can
be used; the bar at its foot switches between the iPhone and the Mac, where
the entry sits, how the list is grouped, and its states).

Scope: Her's conversation, on the Mac and the iPhone. Coding sessions and
the console are not part of it (§8).

Names: the owner sees **Files** (文件). The code and the protocol say
*deliveries*: `files/*` is already the file panel's (`her-server/files.rs`),
and a row here is a send, not a file on the disk.

## 0. Summary

Her sends files as she works: reports, tables, pictures. Today they can only
be found by scrolling the conversation. This adds a list of them, opened
from Her's page:

1. **A list.** Every file Her sent with `send_to_user`, newest first, each
   with the words she sent it with. It can be searched, filtered by kind,
   and grouped by time or by what the file came from.
2. **Versions.** A file sent again from the same path is the same file, a
   version later. The list shows one row and the versions under it.
3. **A copy.** When Her sends a file, her keeps a copy of it as it was at
   that moment. An earlier version, and the attachment of an old message,
   open as they were sent.
4. **The way back.** From a file: Show in Conversation (the message, marked
   a moment) and Reply (the composer quotes it, so Her goes on with that
   file).

Three places change. The kernel `her` copies the file as `send_to_user`
runs. `her-server` records each delivery in `her.db` and answers two new
requests. `hermeshd` lets a download read the copies and keeps the first
page for offline. The Mac and the iPhone each get the entry, the list and
the two actions.

## 1. Before

| | Today |
|---|---|
| Finding a file Her sent | Scroll the conversation |
| What a device holds | The last 100 messages (her-server.md §7); an older file is reached only by loading older history |
| After Clear Conversation | The files stay on the machine, and nothing in the apps leads to them |
| What a file message points at | A path on the machine (`details.files[].path`), read when opened |
| A file Her changed after sending it | The old message opens the new content |
| A file outside `bots/<id>/files` | The message shows it, and opening it fails: `herhost.Download` refuses what is outside the assistant's folder (`internal/herhost/files.go`, `inside`) |

The last two rows are faults of today, apart from this feature. The copy
(§4.1) fixes both.

## 2. What others do

From using them, not from documentation:

- **Chat apps at work** (Feishu, WeCom, Slack): a conversation has a Files
  view, by time and by kind, with search by name, and each file leads back
  to its message. A file is what was uploaded; it never changes.
- **Assistants that make documents:** what the assistant made opens beside
  the conversation and keeps its versions as it is revised.

Her's case has both: files arrive in a conversation like a chat app's, and
they are revised over several turns like an assistant's documents. So the
list is a chat app's, and a row carries versions.

## 3. The interaction

The prototype is the reference; this section states the rules.

### 3.1 Rules both share

- **What is listed:** files Her sent with `send_to_user` (type
  `attachment`), pictures too. Her's sub-agents and her routines send words
  only, and their files reach the owner when Her passes them on, so one rule
  covers them. Not listed: files in her folder she never sent, and files the
  owner sent her.
- **A row:** two lines. The file's name, with the new mark, and when it was
  last sent (the time under Today and Yesterday, the day before that); then
  the words Her sent it with (its size when she said none), and how many
  versions there are. Its size, where it came from and its versions one by
  one are where it opens (decided 2026-11-16 on the owner's phone: four
  lines were crowded).
- **What it came from:** nothing for a file the owner asked for in the
  conversation; a scheduled task's name, or a coding session's, when Her
  sent it while passing on that task's report or that session's news
  (§4.2).
- **Versions:** sends from the same path are one row. Each different
  content is a version, numbered from 1; the row shows the latest. The same
  content sent again is not a new version: the row moves to the top with
  the new words.
- **New:** a file sent since the owner last closed the list is marked, and
  the entry shows how many files are. Closing the list is having seen them.
  It is apart from the conversation's unread count.
- **Search** matches the name, Her's words and what it came from, of any
  version. **Three tabs:** All; Documents, what is read (reports, sheets,
  slides, PDF, Markdown, code, and anything that is not media); Media,
  pictures, video and audio, as a grid (`deliveries/list`'s `media`). The
  list is in sections by the day each file was last sent (Today, Yesterday,
  Past 7 Days, Earlier). Six kinds and a grouping by source were too many to
  mean anything (the owner, 2026-11-16).
- **Opening a file** shows it as the conversation does today (§4.5, §4.6).
  Under it: who sent it and when, Her's words, Show in Conversation, Reply.
  A file with versions has a switch between them.
- **Show in Conversation** brings the message into view and marks it a
  moment (built for scheduled tasks and for replies). For a message older
  than what is loaded, the conversation loads back to it first. For a
  message of a conversation that was cleared, the action is not offered:
  the row says "From an earlier conversation".
- **Reply** quotes the delivery as her-reply.md quotes a message: the block
  Her reads names the file's path, then her words. The quote in the sent
  bubble shows the file's name. A file with no words can be replied to
  here, though its message offers no Reply in the conversation: the path is
  the quote.

### 3.2 The Mac

- A button at the top right, between Scheduled Tasks and ⋯, with the count
  of new files while the list is closed.
- It opens a panel beside the conversation, as Her's settings and her
  scheduled tasks open (340 pt). The conversation stays in use.
- Hovering a row shows Show in Conversation, Reply and Download; the
  secondary click has the same, and Open.
- A file opens over the window (`preview/FilePreview`), with the strip of
  §3.1 at its foot.

### 3.3 The iPhone

- A button beside the menu button, at the top left. Progress and Scheduled
  Tasks stay at the right: three buttons there would touch Her's mark on a
  393 pt screen. With the bars on the side it is in the system's bar with
  the other two.
- It pushes a page. Touch and hold on a row: Preview, Show in Conversation,
  Reply, Share.
- A file opens in Quick Look or on `HerFilePage`, as today, with the card
  of §3.1 over its foot.
- Show in Conversation and Reply go back to the conversation.

### 3.4 States

| State | What shows |
|---|---|
| Nothing sent yet | "The files Her sends you are kept here", and what that means |
| No match | "Nothing found", and that names and Her's words are both searched |
| The machine is offline | The list as this device last had it, under a line that says so. Files opened before open from this device; the rest are dimmed, and a tap says they need the machine |
| The machine's Hermesh is older | The entry shows with the "需更新" mark, and its page is the Needs Update view (version-skew.md §6.2) |
| A file with no copy | It opens from its path, as today. If the path is gone, the row says the file is no longer on the machine |

### 3.5 By source (2026-11-16)

Where a file came from is not on its row: most rows would say the same
thing, and the list stays two lines. It is a filter instead, light to reach
and out of the way (prototype: `her-deliveries-filter-demo.html`).

- **The entry**: one glass button at the top right of the files page
  (iPhone) and by Close at the top of the panel (Mac). Filled with the tint
  while a source is chosen.
- **What opens**: on the iPhone a sheet from the bottom, half the screen,
  dragged up for all of it, where a thumb reaches and many sources fit; on
  the Mac a menu under the button, as a mouse reaches anywhere alike.
- **What it lists**: All Sources; Asked in the Conversation; Routines; and
  Coding Sessions, each with how many files the current tab shows. The
  latest first, not by name, so the daily briefs stay on top. With more
  than eight sources, a search. A source with none in the tab can't be
  chosen. A deleted routine stays, marked deleted: its files do. The
  routines that sent no file are not listed, but a line at the end says how
  many there are.
- **Chosen**: the sheet or menu closes; under the tabs a bar says which
  source and how many files, with × to show all again. It works with the
  tabs and the search. One source at a time.
- **A file's source** is its latest send's.

## 4. How it works

```
Her calls send_to_user {type: attachment, files}
  → kernel (bot.rs): check each file, copy it into bots/<id>/sent/, put the copy in the result
  → the conversation's log and events carry the result, as today
  → her-server (hub → deliveries): one row a file in her.db; the assistant's row counts what is new
  → bots/changed reaches the devices: the entry shows the count, an open list reads again

The list:  deliveries/list  → her-server → her.db
A file:    HerDownload {path: the copy}  → hermeshd → bots/<id>/sent/…
```

### 4.1 The copy

**When.** In `SendToUser::run`'s attachment branch
(`her/crates/her-core/src/bot.rs`), in `sent_file`, after the file is found
and before the call answers "Sent.". The copy is what was there when Her
said she sent it. Copying later, on the event or when a device opens the
file, would race with her next edit.

**Where.** `~/.her/bots/<id>/sent/<sha256>/<name>`:

- Beside `files/`, her working folder, not in it: her own listing and
  writing there never meet a copy.
- Under the assistant, so it leaves with the assistant (§4.9).
- The folder is the SHA-256 of the content, so the same content is kept
  once. The file keeps its name, so a download gives it back under that
  name with no change to the protocol.

**How.**

1. Copy to a temporary name in `sent/` (`std::fs::copy`: a clone on APFS,
   which takes no time and no space until the original changes;
   `copy_file_range` on Linux; a plain copy elsewhere).
2. Hash the copy. Hashing the copy, not the original, means the name always
   describes the bytes kept.
3. Make it read-only and rename it into place. If that content is already
   there, drop the temporary file.

It is blocking work, done off the runtime. The tool finds the folder in the
session's tool state, set when a bot's session starts or resumes, as
`RoutineLink` is.

**What the result says.** Each entry of `details.files` gains two fields,
and `size` is the copy's:

```json
{ "path": "/Users/me/.her/bots/her/files/Q3.docx", "size": 1310000,
  "sha256": "3f9a…", "snapshot": "/Users/me/.her/bots/her/sent/3f9a…/Q3.docx" }
```

The model still reads only "Sent.".

**When there is no copy.**

- The file is over 100 MB: not copied. Off APFS the copy is real, and a
  large video would stall the turn and fill the disk.
- The copy fails (the disk is full, the file cannot be read twice): a
  warning in her's log, and the message is sent without one. Delivering
  matters more than keeping.

Either way the entry has no `sha256` and no `snapshot`, and everything
reads the path, as today.

### 4.2 The record

`her-server` gets `deliveries.rs` and the database's second step
(`MIGRATIONS` in `db.rs`):

```sql
CREATE TABLE deliveries (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  bot_id      TEXT NOT NULL,
  session_id  TEXT NOT NULL,      -- the conversation it was sent in
  call_id     TEXT NOT NULL,      -- the send_to_user call: the message
  idx         INTEGER NOT NULL,   -- its place among the call's files
  path        TEXT NOT NULL,      -- where it was: what makes two sends one file
  name        TEXT NOT NULL,
  kind        TEXT NOT NULL,      -- doc, sheet, slide, image, code, other
  size        INTEGER NOT NULL,
  width       INTEGER,            -- a picture's
  height      INTEGER,
  sha256      TEXT,               -- null: no copy
  snapshot    TEXT,
  text        TEXT,               -- Her's words
  sent_at     TEXT NOT NULL,
  source_kind TEXT,               -- routine, session; null: the conversation
  source_id   TEXT,
  source_name TEXT,
  UNIQUE (session_id, call_id, idx)
) STRICT;
CREATE INDEX deliveries_by_bot ON deliveries(bot_id, seq DESC);
CREATE TABLE delivery_marks (
  bot_id     TEXT PRIMARY KEY,
  seen       INTEGER NOT NULL DEFAULT 0,   -- the last seq the owner saw
  backfilled INTEGER NOT NULL DEFAULT 0
) STRICT;
```

**Written** where the hub hands a conversation's events to the registry
(`hub.rs`, beside `bots.on_event` and `routines.on_event`): on
`ToolCompleted` of `send_to_user`, not an error, whose details have files.
The unique key makes writing twice harmless. One task writes the sends in
the order they were made, so `seq` follows that order. The database is the
one the routines open (`Routines`); the first file sent makes it, as the
first routine does. A her that is not the service that stays running
(`her acp`) has none: it records nothing, and its list is empty.

**Why not read the conversation instead.** A device holds 100 messages,
Clear Conversation moves the log to the trash, and search and paging over
JSON lines would mean reading every log each time. `her.db` is where her
keeps records that change and get queried (`db.rs`).

**Kind** is from the name's extension, by one table in `her-protocol` that
both clients' icons use too; a picture is what `sent_file` found to be one.

**What it came from**: §4.11.

A session's news is named by whoever the mesh host names (`mesh/news`'s
`from`), which need not be the session's title. So the record keeps the
session's id with that name, and a client shows the title from its own
list of sessions when it holds the session, as the conversation's session
card does, and the recorded name otherwise.

**What is new.** After each write, and after `deliveries/seen`, the
assistant's row is counted again: files (distinct paths) with a `seq` above
`seen`. `BotSummary` gains `new_deliveries` and `deliveries_latest` (the
highest `seq`). `bots/changed` already carries the row to every device, so
the count is the same on the Mac and the iPhone, and no new notification is
needed: a device with the list open reads it again when `deliveries_latest`
moves.

### 4.3 The protocol

Two requests in `her-protocol`, listed in `initialize`'s `methods` like the
rest:

`deliveries/list`

```
params  { bot_id, before?: seq, limit?: 50 (at most 200), query?: string, kind?: string }
result  { files: [ { path, name, kind,
                     versions: [ { seq, session_id, call_id, size, width?, height?,
                                   sha256?, snapshot?, text?, sent_at, source? } ] } ],
          seen: seq, next?: seq }
```

- A page is files, not sends: the files whose latest `seq` is below
  `before`, newest first. `next` is the `before` of the following page.
- `versions` is newest first and already folded: sends of the same content
  in a row are one entry, the latest; a content changed back to is a
  version of its own. A send that kept no copy is left out, and a file
  whose latest send has no copy has that one entry only, since its earlier
  content cannot be shown.
- `query` matches `name`, `text` and `source_name` of any version, without
  regard to case. `LIKE` is enough for a few thousand rows.
- `seen` lets a client mark rows without a second request.

`deliveries/seen`

```
params  { bot_id, seq }      -- the highest seq the list showed
```

It never lowers the mark, nor moves it past the latest send: a send made
later is new whatever a client passed.

### 4.4 hermeshd

| File | What changes |
|---|---|
| `internal/herhost/allow.go` | The gate lets `deliveries/list` and `deliveries/seen` through. `HerStatus.methods` then tells the devices the machine has them |
| `internal/herhost/files.go` | `Download` serves what is inside `bots/<id>/files` or `bots/<id>/sent`. Nothing else changes: the path is the copy's, and the name it sends is the file's |
| `internal/owner/her_conversation.go`, `local.proto` | The owner's core keeps the first page of `deliveries/list` (no `before`, `query` or `kind`) as `HerCall` returns it, with what it already keeps of the machine's Her. `GetHerConversationResponse` gains `deliveries` (the result's JSON). It goes when the conversation's copy goes |

`HerUpload`, `HerSend` and `machine.proto` do not change.

### 4.5 The Mac

| File | What changes |
|---|---|
| `packages/her-client/src/deliveries.ts` (new) | Grouping by time and by source, which files are new, what to read of a version, the quote for a reply, pages joined; the cases in `packages/her-client/testdata/deliveries.json` |
| `packages/her-client/src/botChat.ts` | A file of a message carries `snapshot`; `readSent`, the one rule of §4.8 |
| `apps/desktop/.../her/Deliveries.tsx` (new) | The button and its count; the panel (search, kinds, grouping, rows, versions, the grid of pictures, the states of §3.4, the row's menu); a version over the window, with the switch between versions and Her's words, Show in Conversation and Reply at its foot |
| `her/TopBar.tsx`, `her/HerPage.tsx`, `her/context.tsx`, `her/kept.ts` | The button; `Panel` gains `files`; the first page this Mac keeps |
| `her/Conversation.tsx` | `showMessage` (reads earlier history until the message is there) and `replyInComposer`, for the panel |
| `her/Messages.tsx`, `her/preview/FilePreview.tsx` | Open a message's file by `snapshot`, falling back to `path` |
| `src/main/her.ts`, `ipc.ts`, `preload`, `shared/bridge.ts` | A file Her sent is kept by its `sha256` under the app's data folder (`Her Files/<machine>/<sha256>/<name>`, at most 50 MB each), read and saved from there first; `herFilesKept` says which are kept |
| `icons.tsx`, `locales/*/her.json`, `her/her.css` | Two icons (`tray`, `bubble`), the words, the styles |

### 4.6 The iPhone

| File | What changes |
|---|---|
| `HermeshHer/Deliveries.swift` (new) | The same as `deliveries.ts`, with the same cases in its tests (`DeliveriesTests.swift`) |
| `HermeshHer/Chat.swift`, `Environment.swift`, `Kept.swift` | `HerFile.snapshot`; the row's `newDeliveries` and `deliveriesLatest`; the first page kept |
| `Views/Her/HerFilesPage.swift` (new) | The page, its rows and versions, the grid of pictures, the menu of a row, and a version on a page of its own with Her's words, Show in Conversation and Reply under it |
| `Views/Her/HerView.swift`, `Model/AppModel.swift`, `Views/Main/MainView.swift` | The button beside the menu button (in the system's bar when the bars are on the side); the route `.herFiles` |
| `Views/Her/HerChat.swift` | `download` reads the copy, and keeps a file Her sent by its `sha256` in Caches (`Her Files/…`); `reveal(message:)` reads earlier history until the conversation holds the message |
| `Views/Her/HerConversation.swift`, `HerFilePage.swift` | The conversation's own files read by their copy; a message asked to show is shown when the page comes back too |
| `Localizable.xcstrings` | The new words |

The page holds the environment's connection itself, as the memory page
does (version-skew.md §6.6): a pushed page's tasks outlive the page under
it only that way.

### 4.7 Files sent before this

Once for each assistant, `her-server` reads the logs in `sessions/` whose
header names it, and writes a row for each file of each `send_to_user`
result, with the result's time and words, and the source by §4.2's rule.
Then it sets `backfilled` and moves `seen` to the end, so nothing old shows
as new. The writer does it, in its order: for every assistant when the
database is opened, and for an assistant before its next send is written,
when the first send makes the database. It reads only what was sent before
that moment: what came after is on its way through the events, and taking
it from the log would write it early and mark it seen.

- These rows have no copy. They open from their path, and several sends of
  one path are one row with no versions (§4.3).
- Conversations in the trash are not read: the owner cleared them before
  anything said their files would stay.

### 4.8 Different versions

| This app | The machine | What happens |
|---|---|---|
| New | Old | `herAnswers(machine.her, "deliveries/list")` is false: the entry shows with "需更新", and the page is the Needs Update view. Messages have no `snapshot`, and open by `path` |
| Old | New | The app has no entry. Messages carry two fields it does not read, and open by `path`, as before |
| New | New, a file sent before the update | No `snapshot`: by `path` |

One rule for opening, in both clients: the copy when the entry has one;
if the machine answers not found or refused, the path.

`her` and its `hermeshd` ship in one package, so a machine that makes
copies also serves them.

### 4.9 Storage, and when things go

| Event | The rows | The copies |
|---|---|---|
| Clear Conversation | Stay. Show in Conversation is not offered for them | Stay |
| Delete the assistant (`bots/delete`) | Deleted with its routines | Go to the trash with `bots/<id>/` |
| Erase her data (her-server.md §5.6) | Gone with `her.db` | Gone with `~/.her` |
| Turn Her off on the machine, or remove the machine | Stay on the machine | Stay. The owner's devices drop what they kept |

Nothing removes old versions in this version. The console's Storage page
gets a part of its own for `bots/*/sent` (`StorageKind::Sent`), so the
owner sees what the copies take. It adds up file sizes, so on APFS it
overstates: a copy of an unchanged file takes no space of its own.

### 4.10 Safety

- **The model is told nothing new.** The copy's path is in the result's
  details, which never go to the model.
- **Her changing a copy.** `~/.her` is a protected folder
  (`protected.rs`): a write there asks the owner in every mode but full
  access. `sent/` is outside her working folder, so that applies, and the
  copies are read-only. Under full access she could still change one; that
  mode is the owner's choice.
- **What a download can reach** grows by one folder, `bots/<id>/sent`,
  checked the same way (`inside`, symlinks resolved). The copies are files
  Her already chose to send the owner.
- **A file from outside her folder** becomes readable from the owner's
  devices through its copy. That is the point of sending it; the approval
  that let her read the file stands as before.
- **Search** runs as bound parameters, never as SQL text.

### 4.11 Where a file came from (2026-11-16)

The first rule followed what the clients' conversation does: the latest
routine report or session news since the owner's last message and the last
task's end. It missed two things that happen: a routine's report wakes Her,
she gives the work to a sub-agent, and sends its file in a later task woken
by that agent's report; and a routine's file sent later, because the owner
asked for it again. The record now goes by three rules, the first that
applies:

1. **Who wrote the content.** When a routine's run ends, her looks in the
   bot's folder for the files changed since the run started whose names the
   run's own log mentions (the folder is shared with the conversation,
   which may be writing there too), and keeps their SHA-256 (`run_files`:
   the run, the routine and its name, the path, the hash). A file sent with
   one of those contents is the routine's, whenever it is sent. Sends made
   before the run's end was saved, since the report reaches Her first, are
   claimed then, if made after the run started.
2. **What woke the send.** The latest of what Her was told since the last
   task's end: a routine's report is that routine's (and its run); news of
   a coding session is that session's; the owner's message is the
   conversation's. A sub-agent's report goes back to the call that last
   gave that agent work (`spawn_agent` or `send_message`, by `agent_id` in
   its result) and is whatever Her was doing then.
3. **Otherwise** the conversation's.

The record keeps the run (`source_run`) too. A run that cannot write, as in
the strict mode where nothing is changed without asking and nobody is there
to ask, makes no files; what its report woke is still its by rule 2.

`deliveries/sources` counts files by their latest send's source: a routine
by its id and its name now, sessions by their name (each session is new, its
name stays), and how many of them are pictures, video or audio, so a tab
shows its own count. `deliveries/list` takes `source` (`conversation`,
`routine` with `id`, `session` with `name`). hermeshd keeps only the page
that has no filter at all, `media` and `source` included.

Records written before this keep the source the first rule gave them.

## 5. Tests

| Layer | What |
|---|---|
| The kernel (`her-core/tests/bot.rs`) | A sent file has a copy with its content and name, read-only, and the result names it. The file changed and sent again: two copies, each as sent. The same content twice: one copy. Over the limit, or `sent/` not writable: sent without a copy. A file outside her folder is copied |
| her-server (`deliveries.rs`) | A `ToolCompleted` writes rows once, and twice changes nothing. Paging by file. Folding versions. `query` and `kind`. The source after a routine's report, cleared by the owner's message. The count of new files, and `seen`. `bots/delete` removes the rows. The backfill from a log, run twice |
| her-server (`db.rs`) | The second step applies to a database of the first, with its routines intact |
| hermeshd (`herhost/files_test.go`, `allow_test.go`) | `Download` serves `sent/`, still refuses what is outside both folders, and a symlink out of `sent/`. The gate lets the two requests through |
| The core (`internal/owner`) | The first page is kept and served by `GetHerConversation`; a page with `before` or `query` is not kept; it goes with the conversation's copy |
| The Mac's reading (`her-client`) | Shared cases with the iPhone: grouping by time and source, new rows, kinds, a file with and without a copy |
| The iPhone's reading (`HermeshHerTests`) | The same cases |
| End to end, the core (`e2e`, `TestHerDeliveries`) | The real `hermeshd` and `her`, her-mock as the model: she sends a file, changes it, sends it again; `deliveries/list` has one file with two versions; each copy downloads with its own content; the status lists the methods |
| End to end, the Mac (`apps/desktop/e2e/her-deliveries.spec.ts`) | The button's count; the panel; search and a filter; versions; opening an earlier version shows the earlier content; Show in Conversation marks the message; Reply quotes the file and Her's answer is a new version, counted at the button |
| End to end, the iPhone (`TestIPhone`, `testHerDeliveries`) | The same path in the Simulator: the button, the page, a version, Show in Conversation, Reply; the list while the machine is offline |

her-mock already sends files (`[[mock:attach <paths>]]`,
`her-mock/src/lib.rs`). The end-to-end tests change the file between two
sends from outside, through the machine's disk.

## 6. Building it, in order

```
1. The kernel: the copy, the result's fields                       → her-core tests
2. hermeshd: Download serves sent/; clients open by the copy       → herhost tests; old messages open as sent
3. her-server: the tables, writing, the two requests, the count    → deliveries.rs tests, TestHerDeliveries
4. Reading: her-client, HermeshHer, the shared cases               → both run the same cases
5. The Mac: the button, the panel, opening, the two actions        → her-deliveries.spec.ts
6. The iPhone: the button, the page, opening, the two actions      → testHerDeliveries
7. Offline: the kept first page, files kept by sha256              → the core's tests, the iPhone's offline case
8. The backfill, the Storage part                                  → deliveries.rs tests
```

Steps 1 and 2 are worth shipping alone: they fix the two faults of §1.

### 6.1 What is built (2026-11-16): steps 1 and 2

| File | What changed |
|---|---|
| `her/crates/her-core/src/bot.rs` | `keep_copy` and `SentDir`; `sent_file` keeps the copy and names it in the entry (`sha256`, `snapshot`, the copy's `size`); over `MAX_COPY` (100 MB), or when the copy fails, the entry is as before |
| `her/crates/her-core/src/kernel.rs`, `paths.rs` | `link_sent` where a bot's session starts and resumes; `HerHome::bot_sent_dir` |
| `internal/herhost/files.go` | `Download` serves `bots/<id>/sent` too (`sentCopy`), itself inside the assistant's folder |
| `packages/her-client/src/botChat.ts` | `BotFile.snapshot`; `readSent`, the one rule of §4.8 |
| `apps/desktop/.../her/Messages.tsx`, `preview/FilePreview.tsx` | Pictures, previews and Save read through `readSent` |
| `HermeshHer/Chat.swift` | `HerFile.snapshot` and `source` |
| `apps/ios/.../Her/HerChat.swift`, `HerConversation.swift` | `loadPicture`, `download` and `openHerFile` read the copy, then the path; pictures are kept by what they read |
| `docs/her-server.md` §7 | `sent/` in what `~/.her` holds |

Tests: the kernel's (`a_sent_file_is_kept_as_it_was_when_sent`,
`a_file_outside_the_folder_is_kept_too`,
`a_file_that_cannot_be_copied_is_sent_without_a_copy`);
`TestDownloadSentCopy`; her-client's and HermeshHer's reading of
`snapshot`; and `TestHerSentCopies` (`e2e/her_sent_test.go`), the real
`hermeshd` and `her` with her-mock: a file sent, changed and sent again
comes down from each message's copy as it was sent, and a file outside the
assistant's folder comes down by its copy.

The apps, against the real `her`, which now names a copy in every file it
sends: `TestDesktopHer` passed (a PDF Her sent opens over the window and is
saved). On the iPhone, `testHer` in the Simulator passed its step "files Her
sent" (a Markdown file and a Go file open on their page) and failed three
steps later, in `herEarlier` ("the first ones, read at the top"), which
loads earlier messages and has nothing to do with files. The working tree
held other unfinished changes to the conversation and to that test at the
time, and the cause was not found: to run again once those are settled.

### 6.2 What is built (2026-11-16): step 3

| File | What changed |
|---|---|
| `her/crates/her-protocol/src/bot.rs`, `api.rs`, `schema.rs` | The two requests and their types; `DeliveryKind::of`, the one table of kinds; `BotSummary.new_deliveries` and `deliveries_latest`. `schema.json`, `packages/her-protocol` and the protocol reference are generated from them |
| `her/crates/her-server/src/deliveries.rs` (new) | The tables (`SCHEMA_V2`), `sent_of`, `source_of`, `record`, `list` with `fold`, `mark_seen`, `counts`; `Deliveries`, which takes the hub's events; the two handlers |
| `her-server`: `db.rs`, `hub.rs`, `server.rs`, `native.rs`, `bots.rs`, `routines.rs` | The second step of the database; the hub's call; the dispatch; the row's two fields and `set_deliveries`; `bots/delete` removes the rows; the database handed to what else records in it, and counted when it is opened |
| `internal/herhost/allow.go` | The gate lets the two requests through |

Tests: `deliveries.rs` (what a result says was sent, the source, one file a
path with its versions, `fold`, pages with search and filters, what is new
and seen, a deleted bot); `db.rs` (a database of routines alone takes the
new tables); the gate's `TestOpenTo`; her's own end to end
(`the_files_a_bot_sent_are_listed_with_their_versions_and_counted_until_seen`
in `her-cli/tests/e2e_bots.rs`: `her serve` and the mock model, the row's
count over `bots/changed`, the list still there after `bots/clear`); and
`TestHerDeliveries` (`e2e/her_sent_test.go`): the real `hermeshd` and
`her`, the status lists the two requests, a file sent, changed and sent
again is one file with two versions that come down as sent, and the row
counts until `deliveries/seen`.

Not in step 3, as planned: the backfill and the Storage part (step 8), the
first page kept for offline (step 7). Those came next (§6.3).

### 6.3 What is built (2026-11-16): steps 4 to 8

All of §4, as §4.5 and §4.6 list it for the apps, and:

| Step | Where | What |
|---|---|---|
| 7 | `internal/owner/her.go`, `her_conversation.go`, `her_local.go`, `store.go`; `local.proto` | `keepDeliveries` keeps what `deliveries/list` answered for Her's first page, and moves its `seen` after `deliveries/seen`; `GetHerConversationResponse.deliveries`; a table of its own, forgotten with the rest |
| 8 | `her-server/src/deliveries.rs`; `her-core` `JsonlStore::logs`, `routine::log_transcript` | The backfill (§4.7) |
| 8 | `her-server/src/storage.rs`, `StorageKind::Sent`, the console's words | The Storage page counts the copies apart |

Tests, beyond §6.1 and §6.2:

| Layer | What |
|---|---|
| `deliveries.rs` | What was sent before is written once, seen, older than what is sent now, for the assistants asked |
| `storage.rs` | The copies are a part of their own |
| `her-cli/tests/e2e_bots.rs` | `files_sent_before_there_was_a_record_are_read_from_the_logs`: `her serve` without a database; the next send reads the log; a cleared conversation's file is not read |
| `internal/owner` | `TestHerDeliveriesKept`: the first page kept, not a later page, a search, a filter or another assistant's; `seen` moved, never back; kept over a restart; forgotten with Her |
| her-client, HermeshHer | The shared cases (`testdata/deliveries.json`): grouping by time in local time and by source, what is new, what to read, the quote, pages joined; the row's two fields |
| `TestHerDeliveries` | Now also: this Mac's copy of the first page, with `seen` moved. It caught a race first: a send's backfill read the log after the next send, wrote that one early and marked it seen; the backfill now reads only what came before (§4.7) |
| `TestDesktopHerDeliveries` (`apps/desktop/e2e/her-deliveries.spec.ts`) | The real `hermeshd` and `her` with her-mock: the button counts one new file over two sends of it; the panel lists it once, new, with two versions; each opens as it was sent; Show in Conversation marks its message; Reply quotes it and Her is told the file; search, kinds and grouping; closed, the list is seen and the next file alone is new |
| `TestIPhone`, `testHerDeliveries` | The same path in the Simulator; leaving the page is having seen it (the count clears); and the Mac taken offline: the list as last seen, and a version opened before opens from the iPhone. It found two faults of the page's "seen": what the list showed was read only when it changed, so a page that opened on the copy the iPhone kept read nothing (now read from its first showing); and `deliveries/seen` went on the environment's connection, which the page holds and lets go of as it leaves (now on a connection of its own, `HerChat.seeDeliveries`) |

With a real model (2026-11-16): this repository's `her serve` on a her home
of its own, signed in to ChatGPT there, with GPT-6.1-Sol, a temporary
assistant asked to write a one-line report and send it, to change it and
send it again, and, in a message that replied to the second send as the
apps quote a file, to add a line. Her handed each piece to a sub-agent and
sent the file when its report came back, in a later task. All fourteen
checks held: each send named its own copy, holding what was sent; the
reply was understood as about that file; the list had the report once with
three versions, newest first, each as sent, the latest the reply's answer;
the row counted it new until `deliveries/seen`. The script sends what a
client sends, with no app between.

Differences from §4.1 as first written: none. One thing learned: a path
that does not exist answers not found before it is judged outside the
folder, so a copy that was removed is not found, and the client's rule
(§4.8) falls back to the path either way.

### 6.4 What is built (2026-11-16): where a file came from, and the filter

§4.11 in her-server (`deliveries.rs`: `SCHEMA_V3`, `source_of` following sub-agents back, `run_files` and
`run_ended`, `sources`, `list`'s `source`), her-core (`bot::written_since`), her-protocol
(`DeliverySourceFilter`, `deliveries/sources`), hermeshd (allowed; the page kept only without any filter), the
shared cases (`testdata/deliveries.json`), the Mac's menu and the iPhone's sheet (§3.5), and her-mock's
`write` and `routine_file`.

Tests: `deliveries.rs` (a sub-agent's work traced back, a run's content claimed whenever sent, the filter, the
sources); her-cli `e2e_routines` (a routine's run writes a file, sent on and asked for again: the routine's
both times); `TestHerDeliveries` through hermeshd; the Mac's `her-deliveries.spec.ts` and the iPhone's
`testHerDeliveries` (the filter, chosen and cleared). With GPT-6.1-Sol, in a kept test home with full access
(`~/.her-test`): the owner's file is the conversation's; a one-off routine's brief is the routine's with its run,
when its report woke Her and when the owner asked for it again; the sources and one source's files are right.
With files changed without asking, the run could not run `date` and wrote nothing, which its report said.

## 7. The owner's decisions

| Question | Decision |
|---|---|
| The name | Files (文件) |
| What is listed | Only what Her sent with `send_to_user` |
| The interaction | As the prototype has it |
| Keeping a copy | Yes: when `send_to_user` runs, in `bots/<id>/sent/`, by the content's hash |
| A file over 100 MB | No copy |
| Removing old versions | Not in this version: keep all, and show the size on the Storage page |
| Conversations in the trash | Left out of the backfill (§4.7) |
| A session's name as a source | The session's id is recorded; the client names it (§4.2) |
| Where a file came from | Not on its row; a light filter by source instead (§3.5): the iPhone's from the bottom |
| Choosing sources | One at a time |
| Routines that sent no file | Not listed; a line says how many |

The owner settled the name, what is listed, the interaction and the copy,
and left the rest to this design's recommendations.

## 8. Not in this version

- Files the owner sent Her, and files in her folder she did not send.
- Removing a file from the list, and clearing old versions.
- Small pictures made by the machine for the grid: a picture's copy is read
  whole (at most 10 MB, `MAX_IMAGE`) and kept on the device.
- Comparing two versions.
- The console, and coding sessions' files.

## 9. What was not chosen

- **Reading the list from the conversation.** No new storage, but it ends at
  100 messages, at Clear Conversation, and cannot be searched (§4.2).
- **Copying when the event arrives, in her-server.** The kernel would not
  change, but the copy would be taken after "Sent.", the result in the log
  would not name it, and old messages would need the record to find theirs.
- **No copy, versions by time only.** Every version would open the same,
  latest content: a list of versions that cannot be told apart.
- **Hard links instead of copies.** A tool that writes a file in place
  would change the copy with it.
- **A request to download by hash.** A new field in `machine.proto` and the
  local protocol, for what a path under `sent/` already does.
- **The count of new files kept by each device.** The Mac and the iPhone
  would disagree about what is new.

## 10. Open questions

None. If the owner later wants the files of cleared conversations back in
the list, the backfill reads the trash too; nothing else changes.
