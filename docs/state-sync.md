# Hermesh state sync: one replica per device, change feeds on every hop

Status: accepted, 2026-10-25 (decisions in §10). Built 2026-10-26, steps 1
to 5 (§7); what the building changed is in §11. The hosted center gets
`WatchOwner` with its own release (§10, 5); until then hermeshd polls it
once per device.

The Mac app and the iPhone app show the owner's world: machines,
agents (clients), devices, projects, pairing requests, sessions and their
conversations. This document replaces how that world reaches them. Today
each screen pulls it, on timers, through the relay. After this change each
owner device keeps one replica of it in hermeshd, every source pushes its
changes into that replica, and every screen subscribes to the replica.

## 1. Why

Measured on 2026-10-25 against the real hermeshd of an owner whose 30
sessions are all on this Mac, with the hosted center about 250–300 ms away:

| Call | Time | What it waits on |
|---|---|---|
| `GetStatus` | 0.4–0.8 ms | nothing |
| `StreamSessionEvents`, local session | 1–14 ms (458 KB) | SQLite |
| `ListSessions` (limit 50) | 285–830 ms | `ListMachines` at the relay, before reading anything |
| `ListMachines`, `ListProjects`, `ListDevices`, `GetUsage` | 280–460 ms | the relay |
| `ListClients`, `ListPairingRequests` | 240–830 ms | the relay |

Three causes, one root:

1. **Every read goes to the relay first.** `onlineMachines`, `sessionMachine`,
   `ListDevices`, `ListProjects`, `GetUsage` and `findMachine` each call the
   relay's `ListMachines` before doing anything, so a list whose data is all
   on this Mac still waits one relay round trip
   ([owner.go](../internal/owner/owner.go), `machines`, `onlineMachines`).
2. **Every screen polls on its own timer.** Relay calls per minute today
   (N: other machines online):

   | Running | Relay control calls / min | End-to-end calls / min |
   |---|---|---|
   | Menu bar only ([notify.ts](../apps/desktop/src/main/notify.ts): status, clients, machines every 3 s; pairing requests every 2 s; the session watch lists machines every 5 s) | ≈ 82 | 20 N (`GetInfo`) |
   | + main window ([store.tsx](../apps/desktop/src/renderer/src/store.tsx): pairing requests every second; machines, clients, projects, devices every 3 s, three of which list machines) | ≈ 222 | 60 N (`GetInfo` ×2, `ListProjects`) |
   | + Settings window (its own store, the same polls) | ≈ 362 | 100 N |
   | iPhone ([AppModel.swift](../apps/ios/Hermesh/Model/AppModel.swift): pairing requests every 2 s; machines, clients, devices, status every 4 s) | ≈ 90 | — |

   The cost grows with time × screens × machines, not with what changes.
   Idle, nothing changes, and the app still makes thousands of relay calls
   an hour.
3. **Payloads carry what no screen shows.**
   - A `Session` in a list carries `result` (the agent's full last reply),
     `progress.last_message_excerpt` and `provider_session_ref`. No screen of
     the Mac or the iPhone reads them. They are 107 KB of a 113 KB first
     page (binary); without them a session is about 220 bytes.
   - A conversation carries every tool's full output. The Mac shows it
     only when a row is opened ([Chat.tsx](../apps/desktop/src/renderer/src/session/Chat.tsx),
     `ToolRow`), and the iPhone shows two lines. Of the largest session's
     421 KB, 346 KB is tool output.

The root is the same each time: the owner device **pulls state it should
subscribe to**. Machines coming and going, agents connecting, pairing
requests and session changes are events, but they are read as tables to be
polled. The fixes one could make in place (caches with expiry, coalescing,
pausing hidden windows, sharing polls over IPC, relisting one machine) each
patch a symptom. Each also needs its own expiry and invalidation rules, and
the next feature needs them all again.

## 2. Decision

One pattern, applied on every hop:

```
sources (unchanged)                      each owner device            screens
relay / center ──(A) snapshot + changes──┐
                                         ├─► replica in hermeshd ──(C) one local Watch──► Electron main,
machine (each) ──(B) snapshot + changes──┘   (in memory)                                  windows;
                 resumable, summaries only                                                the iPhone in process

details on demand: events by seq (BASIC view), full items by seq, older pages by cursor
```

Four rules:

1. **One replica per device, in hermeshd.** Electron main, every window and
   the iPhone's in-process owner ([internal/mobile](../internal/mobile/mobile.go))
   read the same replica. Every `List*` answers from it and never waits on
   the relay. Writes go to their source as today; the change comes back
   through the feed. Data flows one way, so there is nothing to invalidate.
2. **Every hop is a snapshot, then changes, resumable where it pays.**
   - **(A)** The relay pushes machine presence, names and versions, agents'
     presence and grants, pairing requests and the root chain.
   - **(B)** Each machine pushes its sessions, projects and state, and
     resumes from a token after a reconnect.
   - **(C)** hermeshd pushes the replica to each screen.
3. **Summaries in feeds, details on demand.** A `view` (as
   [AIP-157](https://google.aip.dev/157)) on session and event calls: feeds
   and lists use BASIC; whoever needs more asks for FULL. There is still one
   `Session` message, not a second "summary" message that would drift from
   it.
4. **Large items by reference.** In a BASIC event a long tool output is cut
   to a preview, with its size. The whole output, which the machine now keeps
   instead of cutting it at 16 KiB, is read when the row is opened (§3.5).
   Diffs, images and attachments later use the same mechanism.

## 3. Protocols

Field numbers continue each message's current numbering. Every addition is
optional for older peers (§5).

### 3.1 Views (session.proto)

```proto
// How much of a session a call returns (AIP-157).
enum SessionView {
  // FULL: what callers that predate views get.
  SESSION_VIEW_UNSPECIFIED = 0;
  // What lists show: all but result, progress.last_message_excerpt and
  // provider_session_ref.
  SESSION_VIEW_BASIC = 1;
  SESSION_VIEW_FULL = 2;
}

// How much of each event a call returns.
enum EventView {
  // FULL: what callers that predate views get.
  EVENT_VIEW_UNSPECIFIED = 0;
  // A tool result's summary longer than 3 lines or 512 bytes comes as its
  // first 3 lines, at most 512 bytes (cut at a UTF-8 boundary), with
  // preview set.
  EVENT_VIEW_BASIC = 1;
  // As today: a tool result's summary up to 16 KiB.
  EVENT_VIEW_FULL = 2;
}

message ListSessionsRequest  { ...; SessionView view = 5; }
message WatchSessionsRequest { SessionView view = 1; }
message StreamEventsRequest  { ...; EventView view = 4; }
message ListEventsRequest    { ...; EventView view = 5; }

message ToolResult {
  ...
  // summary is a preview (BASIC): ReadToolOutput returns the whole output.
  bool preview = 5;
  // The whole output's length in bytes, when it is longer than summary.
  uint64 size = 6;
  // (truncated keeps its meaning: the output was cut when recorded; with
  // §3.5 that happens only past 1 MiB, keeping its head and tail.)
}

service SessionService {
  ...
  // ReadToolOutput streams a tool's whole output, as recorded, in chunks
  // of at most 64 KiB.
  rpc ReadToolOutput(ReadToolOutputRequest) returns (stream ToolOutputChunk) {
    option (hermesh.options.v1.access) = ACCESS_READ;
  }
}

message ReadToolOutputRequest {
  string session_id = 1;
  string call_id = 2;
}

message ToolOutputChunk {
  bytes data = 1;
  // On the first chunk: the whole length, so a screen can show progress.
  uint64 size = 2;
}
```

UNSPECIFIED means FULL, so the CLI (`hermesh session list` shows `result`
and the excerpt, [session.go](../internal/cli/session.go)) and MCP see no
change.

`ListEvents` already pages by seq in both directions, which gives
tail-first loading of long conversations later (§9).

### 3.2 (A) Relay → owner (relay.proto)

```proto
service OwnerControlService {
  ...
  // WatchOwner sends what ListMachines, ListClients and ListPairingRequests
  // return, then each change, until the connection ends.
  rpc WatchOwner(WatchOwnerRequest) returns (stream OwnerUpdate);
}

message WatchOwnerRequest {}

// Each list whole, in the first message and whenever it changed (unset: as
// before). Pairing requests expire by their expires_at.
message OwnerUpdate {
  ListMachinesResponse machines = 1;          // with the root chain
  ListClientsResponse clients = 2;
  ListPairingRequestsResponse pairings = 3;
}
```

- **Where it lives.** It is built the way `MachineControlService.WatchUpdates`
  is ([services.go](../pkg/relay/services.go)): per-tenant watchers, fed
  where presence and pairing already change (admission, disconnect,
  `ReportStatus`, `PutPairing`/`ClaimPairing`, `CompletePairing`,
  `PublishGrant`, `Revoke`, `PublishRootChain`).
- **Pacing.** At most one message per 200 ms per watcher: where presence,
  names, grants and pairing requests change, the tenant's watchers are
  woken, build the three lists as the List calls do and send those that
  changed. A watcher also looks every 30 s, for a change made where none is
  woken.
- **Whole lists.** A handful of machines and agents: sending a list whole
  when it changed lets the owner verify it with the very code that verifies
  the List calls, with nothing new to drift.
- **No resume token.** The snapshot is small: a handful of machines and
  agents. Each new relay connection starts with one.
- **Nothing new for the center.** It sends exactly what the three List
  calls send today, stores nothing new, and costs one more stream on the
  owner's existing connection. Pushes are per tenant, in the place that
  already holds presence and tunnels, so they scale the way those do.

### 3.3 (B) Machine → owner (machine.proto)

```proto
service MachineService {
  ...
  // Watch sends the caller's view of this machine (the sessions it may
  // read, its projects, the machine's state), then each change, until ctx
  // ends. The first message comes at once, empty if nothing changed since
  // resume_token, so the caller knows the watch is in place.
  rpc Watch(WatchRequest) returns (stream WatchResponse) {
    option (hermesh.options.v1.access) = ACCESS_READ;
  }
}

message WatchRequest {
  // From the last message applied; empty: start fresh.
  bytes resume_token = 1;
  // A fresh start sends what ListSessions' first page holds: the newest
  // limit sessions plus every unsettled one.
  uint32 limit = 2;
  hermesh.session.v1.SessionView view = 3;
  // The sent_at of the last message applied, as the machine sent it; unset
  // on a fresh start. If resume_token is refused, the reset also carries the
  // sessions deleted since then.
  google.protobuf.Timestamp held_at = 4;
}

message WatchResponse {
  // Set on the first message of a fresh start, or when resume_token was
  // refused: replace everything held for this machine (§4.3 for deletions).
  bool reset = 1;
  repeated hermesh.session.v1.Session sessions = 2;    // added or changed
  repeated string deleted_session_ids = 3;
  hermesh.session.v1.SessionCursor next = 4;           // with reset: where older sessions start
  repeated Project projects = 5;                       // added or changed (active_sessions included)
  repeated string removed_project_ids = 6;
  MachineStatus status = 7;                            // with reset, and when it changed
  // Every message: resume from here.
  bytes resume_token = 8;
  // Every message: the machine's clock when it sent it (for held_at).
  google.protobuf.Timestamp sent_at = 9;
}
```

- **Resume tokens.** Opaque to the caller; inside, a start id (random per
  hermeshd start) and a change counter. The machine keeps, in memory, the
  counter value of each session's and project's last change since it
  started (a few bytes each), so any token of this start resumes; one from
  another start is refused, and a reset sent. Its state is compared each
  second, in process, and sent when it changed. The counter is used, not
  `updated_at`, because the machine's clock can go back.
- **Pacing.** Changes are batched at 100 ms, the dirty set and tick that
  `Sessions.Watch` uses today ([sessionlist.go](../internal/machine/sessionlist.go)).
  Status changes ride the same tick. Agent scans still happen only on
  demand (`CheckAgents`); when one ends, the new status is pushed.
- **Usage is not pushed.** The replica refreshes a machine's usage for
  today when one of its sessions' `turn_count` grows (§4.4).
- **Who may watch.** The same principal filtering as `ListSessions` and
  `ListProjects`, so an agent with a grant could use the feed later too.
- **The owner's own machine.** It is fed by the same function in process,
  never through the relay.

### 3.4 (C) hermeshd → screens (local.proto)

```proto
service LocalService {
  ...
  // Watch sends the replica, then each change, until the caller leaves.
  rpc Watch(WatchRequest) returns (stream WatchResponse);
  // A session's events by seq, as SessionService.ListEvents.
  rpc ListSessionEvents(ListSessionEventsRequest) returns (ListSessionEventsResponse);
}

message WatchRequest {}

message WatchResponse {
  // Set on the first message, which carries everything. Replace what you
  // hold, except sessions: merge those (a later state wins), so older pages
  // already loaded stay.
  bool reset = 1;
  GetStatusResponse status = 2;                        // with reset, and when it changed
  repeated Machine machines = 3;                       // added or changed
  repeated string removed_machine_ids = 4;
  repeated Client clients = 5;
  repeated string removed_grant_ids = 6;
  repeated Device devices = 7;
  repeated string removed_device_key_ids = 8;
  repeated Project projects = 9;
  repeated ProjectRef removed_projects = 10;
  repeated PairingDetails pairings = 11;
  repeated string removed_pairing_codes = 12;
  repeated hermesh.session.v1.Session sessions = 13;   // BASIC
  repeated string deleted_session_ids = 14;
  hermesh.session.v1.SessionCursor next = 15;          // with reset: where older sessions start
}

message ProjectRef {
  string machine_id = 1;
  string project_id = 2;
}

message Machine {
  ...
  // Why this machine's sessions and projects cannot be read now ("offline",
  // or the error); empty while they can.
  string unreachable = 9;
}

message ListSessionsRequest        { ...; hermesh.session.v1.SessionView view = 4; }
message StreamSessionEventsRequest { ...; hermesh.session.v1.EventView view = 4; }

message ListSessionEventsRequest {
  string session_id = 1;
  uint64 after_seq = 2;
  uint64 before_seq = 3;
  uint32 limit = 4;
  hermesh.session.v1.EventView view = 5;
}

message ListSessionEventsResponse {
  repeated hermesh.session.v1.SessionEvent events = 1;
}
```

- **One stream, one field pair per kind.** The fields follow the existing
  List responses and `WatchSessionsResponse` (`sessions` plus
  `deleted_session_ids`). A new kind of data is one more pair of fields,
  with no new RPC, poll or cache; older screens ignore it.
- **Unreachable machines.** `Machine.unreachable` replaces the separate
  unreachable lists in Watch: the screens' "can't read machine X" notices
  derive from it.
- **No resume token.** A local reconnect re-sends the snapshot, which costs
  under a millisecond.
- **Existing calls stay.** Every existing LocalService call is kept and
  answered from the replica, for tests and any caller that does not watch. Writes are
  unchanged: their response still carries the new state for an immediate
  update, and the same change arrives on Watch.
- **Older pages.** `ListSessions` with `before` still asks the machines,
  since older pages are not in the replica, and only when a list scrolls
  there.

### 3.5 Whole tool outputs (machine)

Today every provider keeps only the first 16 KiB of a tool's output when it
records it (`provider.Truncate(…, 16<<10)` in [claude.go](../internal/provider/claude/claude.go),
codex.go and acp.go); the rest is gone. That bound kept events small
because every list and stream carried them whole. With previews in feeds
and streams it can go:

- **Recording.** An output up to 16 KiB stays inline in its event, as
  today. A longer one is kept whole in a table of its own,
  `tool_outputs(session_id, call_id, size, data)`, compressed. Its event
  holds its first 16 KiB, `size` and `truncated` (what FULL gives, as
  before), and BASIC cuts that to the preview. So `session_events` stays
  as small as today and replaying or paging a conversation never reads the
  large outputs.
- **Bound.** At most 1 MiB is kept: past that, the first and last 512 KiB,
  with a line in between saying how many bytes were left out, and
  `truncated` set. Build and test logs keep their end, where the error
  usually is; today only their start survives.
- **Reading.** `ReadToolOutput` streams it in 64 KiB chunks, whether it is
  inline or in `tool_outputs`, and the screen shows the text as it
  arrives. The owner forwards the stream as it does `StreamSessionEvents`
  (in process for its own machine). LocalService gets the same
  `ReadToolOutput`.
- **Lifetime.** The rows go with their session when it is deleted.
- **FULL view.** It gives what it gives today, the first 16 KiB, so the CLI
  and MCP see no change.

What the coding agent hands over is the limit: an agent that shortens a
long output itself is shown as it shortened it.

## 4. The replica

### 4.1 What it holds

| Kind | Source | Kept |
|---|---|---|
| Status | this hermeshd | always; `relay_connected` from the connection, so `GetStatus` never dials the relay (it does today when disconnected) |
| Machines | (A), plus this device's own machine | all; `status` and `unreachable` from (B). This device's own machine is read in process at each call (milliseconds, no relay), never copied |
| Agents (clients), pairing requests | (A) | all, verified as today (`VerifyGrant`, `pairingDetails`); pairing requests dropped at `expires_at` |
| Devices | root chain from (A) + machines | derived, as `ListDevices` does today |
| Projects | (B) per machine | all |
| Sessions | (B) per machine, BASIC | per machine: the newest 50 plus every unsettled one; trimmed after each change, since a session that changes becomes the newest and comes back. A page past them (`before`, or more than 50 of a machine that has more) is asked of the machine |
| Usage | `GetUsage` per machine | days before today: once per time zone; today: refreshed (§4.4) |
| Today's activity | `GetActivity` per machine, not kept | the sessions that worked each hour of a day and by who started them, counted by the machine from its turns' start and end (`turn_usage`) and the turn in progress; the iPhone asks when its overview opens and, while sessions change, once a minute at most |
| Feed state | per machine | following / error / offline, and its resume token |

Memory: a few hundred BASIC sessions of about 220 bytes each, plus the
rest: well under a megabyte.

### 4.2 Lifetime

Only the owner's screens call LocalService: Electron main and the windows
on the Mac, the app itself on the iPhone (and tests). The `hermesh` CLI is
not one of them: it is an agent, and reaches machines over the relay with
its grant ([state.go](../internal/cli/state.go)), never through the replica.

Sources run while anything needs them: a `Watch` subscriber, or a `List*`
call in the last 60 s. The first `List*` with nothing running starts them
and waits for the first snapshot, up to the follow timeout of 15 s used
today. The last subscriber leaving stops them 60 s later, not at once.

| Case | What runs |
|---|---|
| Mac app running | Electron main always watches: sources run |
| Mac app quit (hermeshd stays, as a LaunchAgent, for its machine role) | nothing calls: sources stop 60 s after main went away; no relay polling, stream or tunnel for the owner role all day |
| A window reloads, or main's watch reconnects after an error | the gap is under 60 s: the replica stays warm, no new snapshot from every machine |
| iPhone in the foreground / suspended | the app watches / the process is frozen; back in the foreground it watches again and the owner resumes (B) from its tokens |
| A test or script calling `List*` without watching | the first call waits for the snapshot; calls within 60 s are answered at once |

### 4.3 Consistency

- **Ordering.** Each source applies under the replica's lock and
  broadcasts in that order. A new subscriber gets its snapshot and its
  place in the broadcast atomically.
- **Merging.** A later session state wins (by `progress.last_event_seq`,
  then `updated_at`), as `isLaterState` already decides in the UI
  ([sessions.ts](../packages/core/src/sessions.ts)).
- **Machine reset.** On a reset from (B), that machine's sessions are
  replaced. A reset after a refused resume also carries the ids that
  `deleted_sessions` (kept for good, [store.go](../internal/machine/store.go))
  recorded since the request's `held_at`. So a deletion made while
  disconnected is never missed, without a fixed window, and both times are
  the machine's own clock. A fresh start holds nothing, so it needs none.
  Today's relist misses such deletions.
- **Offline machines.** When a machine goes offline, its sessions are kept,
  last known, and its `unreachable` is set; a reset from (B) replaces them
  when it is back. Today a window that stays open keeps them as they were
  (still "running", its clock ticking) and one that reloads drops them; the
  replica makes both the same, and the screens say what is known (§6).
- **Slow subscribers.** One that falls behind (256 buffered messages, as
  today) is closed with `ResourceExhausted`, reconnects and gets a
  snapshot.

### 4.4 Usage

`GetUsage` keeps each other machine's answer while the turns of its
sessions in the replica (their `turn_count`s added up) and the day in the
caller's time zone stay the same; this device's own is read in process. The
overview drops its 60 s timer and asks again as turns end.

### 4.5 Trust

The replica is in hermeshd's memory, which already sees everything today.
Nothing changes in who verifies what:
- machine certificates and grants pushed by (A) are verified exactly as the
  List calls verify them;
- the relay sees nothing new;
- (B) runs end to end, as today.

A relay could withhold updates; it can already lie about presence today.

## 5. Older peers

Fallbacks live in hermeshd's source adapters only. Screens always see the
same Watch.

| Peer | Detected by | Fallback |
|---|---|---|
| Relay without `WatchOwner` (the hosted center until its release) | `Unimplemented` on the first call | Poll `ListMachines` and `ListClients` every 5 s and `ListPairingRequests` every 2 s, once per device, only while sources run; poll once right after this device's own writes (also with `WatchOwner`, so the next list shows them) |
| Machine without `Watch` | `Unimplemented` | Today's `ListSessions` first page + `WatchSessions`; `GetInfo` and `ListProjects` on connect, `GetInfo` again 1 s after its sessions change status (its counts), both every 30 s (changes other devices made) and at once after this device's own; no resume (relist on reconnect, as today) |
| Machine that ignores `view` | a FULL session or event arrives | The owner cuts it to BASIC before the replica, so screens get small payloads anyway; only the tunnel carries the full bytes |
| Machine without `ReadToolOutput` | `Unimplemented` | The owner serves `ReadToolOutput` from `ListEvents(after_seq: seq-1, limit: 1, view: FULL)`: the first 16 KiB, as recorded there |
| Callers of the old LocalService calls (tests, the stand-in daemon's clients) | — | Answered from the replica |

The desktop and iPhone apps ship with their own hermeshd or owner, so a
screen never meets an older replica. There are no iPhone installs to keep
working yet: the iPhone app moves to `Watch` in step 2 with no transition.

The `hermesh` CLI is untouched by this design: it reaches machines as an
agent with FULL views, as today (§3.1).

## 6. Screens

- **Mac renderer.** [store.tsx](../apps/desktop/src/renderer/src/store.tsx)
  and [useSessions.ts](../apps/desktop/src/renderer/src/useSessions.ts)
  follow `Watch`, with no timers left. `loadOlder` keeps `ListSessions`
  with `before`.
- **Mac main.** [notify.ts](../apps/desktop/src/main/notify.ts) follows
  `Watch` for its notices. Each window and main keep their own stream:
  local and cheap, no IPC layer to add.
- **Conversation.** It opens with `StreamSessionEvents(view: BASIC)`. Opening
  a row with `preview` set reads `ReadToolOutput` and shows the text as it
  arrives (rows of failed tools, which open by themselves, read at once). The last 10 opened
  conversations are kept in memory and resume from their last seq, so
  going back to one shows at once.
- **Sessions of an unreachable machine** stay in the lists, marked as on a
  machine that cannot be reached. A turn in progress or a question shows as
  "state unknown" with no clock running; they count neither as in progress
  nor as needing the owner (lists, tray, notices); interrupting, answering
  and archiving are disabled with the reason. Opening one shows what the
  conversation cache holds, or says the machine cannot be reached now.
- **iPhone.** [AppModel.swift](../apps/ios/Hermesh/Model/AppModel.swift) does
  the same, and its two-line preview reads `preview`. Going back to the
  foreground reconnects (C) at once; the owner resumes (B) from its tokens.

## 7. Migration

Each step ships alone with unit tests and an end-to-end test (AGENTS.md
§4). After step 2, later steps only swap a source adapter inside hermeshd;
screens are not touched again.

| Step | Change | What it buys | Needs |
|---|---|---|---|
| 1 | The replica in hermeshd, fed by today's calls (the fallback adapters of §5); `sessionWatch` becomes the machine sources; every `List*` from the replica; `GetStatus` stops dialing. **Built 2026-10-26** ([replica.go](../internal/owner/replica.go)): every list p50 60–160 µs with the relay 300 ms away, 54 relay control calls a minute with three windows polling ([replica_test.go](../e2e/replica_test.go)); `GetUsage` is still asked of the machines, its cache (§4.4) comes with step 2 | Local reads in ms; relay polls once per device (≈ 54 / min) whatever the screens | hermeshd only |
| 2 | `LocalService.Watch`; Mac renderer, Mac main and iPhone follow it | No polls from any screen | step 1 |
| 3 | Views (§3.1), whole tool outputs (§3.5), `ReadToolOutput`, `ListSessionEvents`, previews in the conversation, the conversation cache; the owner cuts payloads itself for machines that ignore views | List and conversation bytes down (≈ 220 B a session; the 421 KB session ≈ 100 KB); an opened tool output is whole up to 1 MiB | step 2; full gains on the tunnel once machines update |
| 4 | `OwnerControlService.WatchOwner` | Relay control calls ≈ 0 while nothing changes | step 1; a center release (bump the submodule in hermesh-cloud, redeploy) |
| 5 | `MachineService.Watch` with resume tokens | No `GetInfo`/`ListProjects` polls; reconnects without relists; deletions never missed | step 1 |

Steps 3, 4 and 5 are independent of each other. All five were built on
2026-10-26; the tests named in §8 check them.

## 8. Success criteria

| Criterion | Target | Measured by |
|---|---|---|
| A list call (any `List*`, first page) | p50 ≤ 5 ms with the relay 300 ms away | Go e2e behind `delayProxy` ([latency_test.go](../e2e/latency_test.go)) |
| Relay control calls, owner idle | step 1: ≤ 60 / min per device, the same with 1 or 3 windows; step 4: 0 besides the stream | a new relay counter `hermesh_relay_owner_rpcs_total{method}` (a process metric, nothing per owner stored); Go e2e |
| Calls from screens to hermeshd, idle | 0 after step 2 | the gateway counts forwarded calls ([gateway.ts](../apps/desktop/src/main/gateway.ts)); desktop e2e |
| One more window | +0 relay calls | the same counter |
| A BASIC session | p50 ≤ 400 B | unit test on generated sessions; checked once on real data (≈ 220 B) |
| Opening the largest real session (421 KB today) | ≤ 110 KB over the socket | e2e with a recorded conversation of the same shape |
| An opened tool output | whole up to 1 MiB; head and tail past that; first bytes on screen ≤ 50 ms for a local session | Go e2e with a fake agent printing 64 KiB and 3 MiB; desktop e2e opening the row |
| Reconnect to a machine with nothing changed | one empty message, no relist | Go e2e: drop and restore the tunnel |
| A deletion while disconnected | gone after reconnect | Go e2e |
| Desktop smoothness | `start.ms`, `switch.session.ms`, `open.long.ms` no worse | `pnpm --filter @hermesh/desktop perf`, with the stand-in daemon serving `Watch` and a relay delay added to it |

## 9. Not doing (now)

- **Persisting the replica.** Writing it to disk would give an instant cold
  start and last-known state of offline machines. The replica has room for
  it; it waits for a need.
- **Tail-first loading of long conversations.** `ListSessionEvents(before_seq)`
  makes it possible. Rows must not split a tool call from its result, so it
  waits until BASIC events prove not enough.
- **A generic sync engine, CRDTs or field masks.** Three streams of one
  shape are enough.
- **Direct LAN connections between devices and machines.** That is about
  the transport, under `peer`, and does not touch this design.

## 10. Decisions from review

1. ~~Sources stop 60 s after the last subscriber or `List*` call (§4.2): is
   60 s right?~~ Decided 2026-10-25: 60 s.
2. ~~Keep an offline machine's last-known sessions in the replica, marked
   unreachable (§4.3)?~~ Decided 2026-10-25: keep them, marked (§4.3, §6).
3. ~~A 512-byte preview, or a few lines?~~ Decided 2026-10-25: at most 3
   lines and 512 bytes; outputs kept whole (§3.5).
4. ~~Tombstones on a reset: the last 7 days enough?~~ Decided 2026-10-25:
   no window; the deletions since the caller's `held_at` (§3.3, §4.3).
5. ~~Step 4 needs a center release. Do it next to the pending center
   redeploy, or on its own?~~ Decided 2026-10-25: its own release, once
   step 4 is done and tested; until then hermeshd polls once per device.
6. ~~Should `hermesh session list` move to BASIC?~~ Decided 2026-10-25: no.
   Its table and `-o json` show `result`; the CLI stays out of this work.

## 11. What building it changed

- **`OwnerUpdate` carries whole lists** (§3.2), not changes of each item:
  the lists are small, and the owner verifies them with the List calls'
  own code.
- **A long output's event keeps its first 16 KiB** (§3.5), not only the
  preview, so FULL readers (the CLI, MCP) see what they did; `truncated`
  keeps saying the summary is not the whole output, `size` how long the
  whole is.
- **`ReadToolOutputRequest` in LocalService has the result's `seq`**, so the
  owner can answer for a machine without `ReadToolOutput` from that event.
- **A FULL `ListSessions` asks the machines**: the replica holds BASIC
  sessions only; the screens ask for BASIC.
- **Resume tokens hold for a machine's whole run** (§3.3), not the last
  10,000 changes.
- **A machine without `Watch` has its state and projects read again every
  30 s** besides after its sessions change (§5), for changes other devices
  make.
- **On a phone** (2026-10-27, `TestMobileNetwork`: the iPhone's core in a
  process of its own, the relay 150 ms away and the Mac as far behind it,
  the app suspended with SIGSTOP while its network goes away):
  - hermeshd dials the relay without `o.mu`, so the status and the
    replica's lists never wait on a dial (the app's status at launch waited
    two round trips, and when the dial stalled past 10 s the app showed its
    onboarding to an owner already set up); admission is pipelined and the
    end-to-end handshakes with the machines last seen online go out with it.
  - Pausing (the app leaves the front) stops the sources, keeping what they
    hold; so does the 60 s idle stop. The next start begins from it: lists
    answer at once, last known, and each machine's Watch resumes from its
    token. Before, the pollers dialed again right after the pause, and after
    a while away a start from nothing re-read every machine and lost the
    deletions made meanwhile. Each machine's `held_at` is kept on disk too,
    so a launch asks for what was deleted since the app last watched.
  - A screen's Watch reads only what the replica holds (no waiting for it to
    settle at each tick, no machine asked through the relay), and its first
    message waits for the relay's lists and at most 2 s for the machines;
    one still being read is `Machine.syncing`. A relay that does not answer
    no longer ends an iPhone's Watch (it has no machine of its own whose
    presence made the error moot).
  - A follow that fails is tried again (2 s, doubling to 30 s): a relay that
    pushes its lists says nothing while they stay the same.
  - Without the relay, whether a machine is online is not known: a list the
    relay did not answer keeps the machines as it last listed them, a follow
    that could not reach the relay leaves its machine `syncing`, and a poll
    cut short (the sources stopped, its caller left) is not kept. Before,
    each made the screens show the Mac offline or not responding for a
    moment, after the app came back or went back and forth.
  - The core stops listening on its loopback port while paused: iOS may take
    a suspended app's listening socket back without a word (TN2277), and
    then the app stayed on "Connecting to Hermesh…" until it was quit.
  - The relay connection's warm handshakes are under way before the calls
    that waited for the dial wake, so they join them rather than make their
    own.
  - Round trips, before → after: the status at launch 2 → 0; the world
    complete 7.9 → 6.2 (the floor: TCP and the upgrade, then the end-to-end
    handshake and one call to the Mac, two each); after a minute away with
    dead connections, more than 90 s → 5; a session deleted meanwhile never
    → gone with it.
- **Measured** (Go e2e, the relay 300 ms away): every list p50 60–170 µs;
  0 relay control calls while three windows ask as before; a pairing
  request on a screen 0.4 s after it was made; a BASIC session 221–236
  bytes; a whole output's first bytes 0.3–3 ms; a reconnect resumed with
  nothing listed again.
