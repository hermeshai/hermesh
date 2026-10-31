# Work stats: one model for the overview, usage and what comes next

Status: accepted 2026-10-31; machines and hermeshd (steps 1 and 2, §11)
built the same day. Replaces `turn_usage`, `GetUsage` and
`GetActivity` on machines and in hermeshd. There are no users yet: no
compatibility with older machines, apps or databases is kept, and a
machine's existing counts are rebuilt from its session events once (§5.6).

## 1. What the screens need

| Screen | Numbers |
|---|---|
| iPhone overview, by range (Today, 7 days, 30 days, picked with a glass segmented control) | sessions that worked and of them those done now; turns and tokens; sessions at work per hour (Today) or per day; who asked which coding agent for how many turns and tokens (the flow); each coding agent's turns and its sessions at work per hour or day; clients that asked for nothing; "N running" stays live |
| iPhone account, Mac overview | tokens per day and coding agent over 18 to 53 weeks; active days, streak, each agent's share; today's tokens on the Mac's link map |
| Later, without new machine releases | any range up to a year; by machine or project; failed turns; hours of agent work; the previous period to compare |

Two goals decide everything below: **storage stays small** on machines
and owner devices, and **every question is answered fast**, including
switching ranges, a year of days, and machines that are offline.

## 2. What is wrong today

- **Storage per turn.** `turn_usage` keeps a row per turn, the session id as
  text in each, plus an index on `ended_at`: about 110 bytes a turn, ~40 MB a
  year for a machine that ends 1,000 turns a day, to answer questions that
  only group by hour, day, client and agent.
- **Reads grow with history.** `GetActivity` reads every turn of the day
  and whole `sessions` rows (`result` and progress included) and walks spans
  hour by hour in Go. `GetUsage` reads every turn of the year and every
  session the machine ever had, on each refresh.
- **Every question crosses the relay.** hermeshd asks every machine each
  time (250–300 ms to the hosted center); a machine that is offline is left
  out of the numbers. Each new chart needs a new machine RPC and a machine
  release.
- **One day at a time.** There is no range question; thirty one-day calls
  would count a session once per day it worked.
- **Who asked is lost.** Agents now write to sessions they did not start
  ([agent-messages.md](agent-messages.md)); each turn's `TurnStarted.asker`
  says who asked for it, but the flow credits every turn to the session's
  creator. Deleted sessions count in usage and not in activity.

## 3. Decision

**Count once, copy the counts, add up on demand.** Work is counted once, on
the machine where it happened, when it happens. Each owner device (an
iPhone, a Mac, a Windows PC) copies those finished counts, a few kilobytes
a day, and adds up the range a screen asks for. No device counts again from
sessions or events, so a second or third device costs one more copy, not one
more count, and every device shows the same numbers (a minute apart at
most). It is the model state-sync already uses for sessions: one replica of
the owner's world per device; counts become part of that world.

1. **Machines count once, when the work happens**, into quarter-hour
   buckets keyed by small integers (§4, §5). The session's event log stays
   the source of truth; the counts are derived from it and can be rebuilt.
2. **Every owner device keeps a replica of every other machine's counts**
   in its own `owner.db`, pulled end to end and kept current from the
   machine's `Watch` feed (§6). Its own machine's counts (a Mac that is one)
   are read in process, never copied, as its sessions are. Machines never
   answer statistical questions.
3. **Screens ask hermeshd one local question**, `QueryWork` (§7): any range,
   by hour or day, grouped by client, agent, machine or project. No relay
   round trip per question; an offline machine counts up to its last sync.
4. **The center learns nothing new.** Counts travel machine → owner device
   end to end, as sessions do ([state-sync.md](state-sync.md) §4.5); the
   relay keeps knowing only presence, names and versions.

## 4. The model

**Time** is the quarter hour: `q = unix seconds / 900`. Every time zone in
use is a whole number of quarters from UTC (+5:30, +5:45 and +12:45
included), so any local hour or day, DST days of 23 or 25 hours too, is an
exact run of quarters. Hours and days are made at query time, in the asking
device's time zone.

**Dimensions**, each stored once in a key dictionary:

| Dimension | Of a session | Of a turn |
|---|---|---|
| Machine | the machine it runs on | its session's |
| Project | `project_id` | its session's |
| Coding agent | `agent` | its session's |
| Client | `created_by`: who started it | `TurnStarted.asker`: who asked for the turn (the creator when empty) |

**Facts**:

- **Presence**: session *n* was at work (a turn in progress) at some instant
  of quarter *q*. Distinct sessions per bucket come from presence.
- **Measures** per quarter and turn key: turns that ended, of them failed
  (an error that was not an interruption), input and output tokens, and busy
  seconds (turn time inside the quarter). All additive.
- **Session state**: its status and whether its last turn failed, kept
  current; "done" is idle without a failure, or archived (as today).

**Exactness.** A session has one machine, project, coding agent and
creator, so session counts summed over any grouping of those dimensions are
exact; measures are sums. No sketch or approximation is needed.

Failed turns and busy seconds are not on a screen yet. They are recorded
now because they cost almost nothing (SQLite stores 0 and 1 in no bytes)
and cannot be recovered for sessions deleted later.

## 5. Machines

### 5.1 Tables

```sql
CREATE TABLE work_keys (
  k INTEGER PRIMARY KEY,
  client TEXT NOT NULL,
  agent TEXT NOT NULL,
  project_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  UNIQUE (client, agent, project_id)
);

CREATE TABLE work_sessions (
  n INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE,
  k INTEGER NOT NULL,                 -- creator key
  status INTEGER NOT NULL,            -- SessionStatus
  failed INTEGER NOT NULL DEFAULT 0,  -- last turn failed (not interrupted)
  turns INTEGER NOT NULL DEFAULT 0,   -- last turn counted (idempotence)
  turn_start INTEGER NOT NULL DEFAULT 0, -- unix seconds of the turn in progress, 0: none
  turn_k INTEGER NOT NULL DEFAULT 0,  -- that turn's key (its asker)
  seq INTEGER NOT NULL
);
CREATE INDEX work_sessions_by_seq ON work_sessions(seq);

CREATE TABLE work_quarters (           -- presence
  q INTEGER NOT NULL,
  k INTEGER NOT NULL,                  -- the session's creator key
  n INTEGER NOT NULL,
  PRIMARY KEY (q, k, n)
) WITHOUT ROWID;

CREATE TABLE work_measures (
  q INTEGER NOT NULL,
  k INTEGER NOT NULL,                  -- the turn's key
  turns INTEGER NOT NULL DEFAULT 0,
  failed_turns INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  busy_seconds INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (q, k)
) WITHOUT ROWID;

CREATE TABLE work_hours (              -- which hours changed, for replication
  h INTEGER PRIMARY KEY,               -- q / 4
  seq INTEGER NOT NULL
);
CREATE INDEX work_hours_by_seq ON work_hours(seq);
```

`meta` holds `work_store` (16 random bytes, made with the database) and
`work_seq` (a counter, one step per write transaction).

`WITHOUT ROWID` keeps each row inside its primary key's B-tree: no second
index, and a range of quarters is one contiguous scan. Numbers are varints:
a quarter takes 4 bytes, a key or session number 1–3, a zero none.

### 5.2 Size

A busy machine: 30 sessions a day, 1,000 turns, each session at work about
2 hours of the day, three keys.

| | Rows a day | Bytes a row | A day | A year |
|---|---|---|---|---|
| `turn_usage` today (with its index) | 1,000 | ~110 | ~110 KB | ~40 MB |
| `work_quarters` | 30 × 8 = 240 | ~14 | ~3.4 KB | ~1.2 MB |
| `work_measures` | ~40 working quarters × 3 keys = 120 | ~22 | ~2.6 KB | ~1 MB |
| `work_sessions` | 30 | ~50 | ~1.5 KB | ~0.5 MB |
| `work_hours` | ≤ 24 | ~20 | ~0.4 KB | ~0.15 MB |
| **Together** | | | **~8 KB** | **~3 MB** |

About 14 times smaller, and it grows with time at work, not with turns: two
hundred short turns in an hour are four quarter rows per key.

### 5.3 Writing

All in the transaction that already stores the event (where `putUsage` is
today), plus a timer:

| When | Writes |
|---|---|
| `TurnStarted` | the session's creator key and the turn's key (`INSERT OR IGNORE`); its `work_sessions` row the first time; `turn_start`, `turn_k`; presence for the start quarter |
| Each quarter boundary, while a turn is in progress | presence for the new quarter, for each such session (one timer per machine, idle when nothing runs) |
| `TurnCompleted` | if `turns` is already this turn's number, nothing (a replayed event). Else presence for every quarter from `turn_start` to the end (`INSERT OR IGNORE`, filling any gap, e.g. a sleep); busy seconds added to each of those quarters' measures; in the end quarter turns, failed turns and tokens added (`ON CONFLICT DO UPDATE`); then `turns`, `turn_start = 0` |
| Status change | `status` and `failed` on its `work_sessions` row, when they changed |
| Session deleted | nothing: the work stays counted, with its last state |
| Crash recovery | `turn_start = 0` (presence was written up to the last quarter boundary; a turn that never completed adds no measures) |

Every transaction that touches `work_quarters` or `work_measures` sets its
hours' `work_hours.seq` to the new `work_seq`; one that touches keys or
sessions sets theirs. A turn writes about five to ten small rows, once.

### 5.4 Retention

Two years on machines (a new owner device can take a year of history),
pruned once a day by a primary-key range delete on each table
(`WHERE q < :cutoff`, `h < :cutoff / 4`), and sessions with no presence left.

### 5.5 Who may pull

`PullWork` filters by key with the rule `canRead` uses: the owner reads
everything; a client reads the keys whose client is itself, or whose
project its grant allows when it sees every task. Only owner devices pull
today; the rule keeps the door open for agents.

### 5.6 Rebuilding

The counts are a function of the session events. On the schema step that
introduces them, and whenever their rules change, the machine replays the
`TurnStarted` and `TurnCompleted` events of its stored sessions into empty
tables, then makes a new `work_store` so owner devices take everything
again. Deleted sessions' work is lost by a rebuild; it is the one case, and
a rare one.

The schema step drops `turn_usage` and its index and `VACUUM`s once.

## 6. Replication to owner devices

### 6.1 Machine protocol (machine.proto)

```proto
service MachineService {
  // The machine's work counts changed since after, for the caller's
  // keys (§5.5): whole hours, keys and sessions. Owner devices keep a
  // replica of them (docs/work-stats.md).
  rpc PullWork(PullWorkRequest) returns (PullWorkResponse) {
    option (hermesh.options.v1.access) = ACCESS_READ;
  }
}

message PullWorkRequest {
  bytes store = 1;     // the store the caller holds; empty: none
  uint64 after = 2;    // the seq it holds
  // With after 0 (a first pull): only hours from this one on, for a
  // device that keeps less history than the machine (§6.3).
  int64 from_hour = 3;
}

message PullWorkResponse {
  // Not the request's: drop what you hold of this machine, then apply.
  bytes store = 1;
  // This answer brings the caller up to seq; more: ask again from it.
  uint64 seq = 2;
  bool more = 3;
  repeated WorkKey keys = 4;
  repeated WorkSession sessions = 5;
  repeated WorkHour hours = 6;  // each replaces the caller's rows of that hour
}

message WorkKey { uint32 key = 1; string client = 2; string agent = 3; string project_id = 4; }

message WorkSession {
  uint32 session = 1;
  uint32 key = 2;
  hermesh.session.v1.SessionStatus status = 3;
  bool failed = 4;
}

message WorkHour {
  int64 hour = 1;                      // unix seconds / 3600
  repeated WorkPresence presence = 2;
  repeated WorkMeasure measures = 3;
}

message WorkPresence { uint32 quarter = 1; uint32 key = 2; uint32 session = 3; } // quarter: 0-3 of the hour

message WorkMeasure {
  uint32 quarter = 1;
  uint32 key = 2;
  uint32 turns = 3;
  uint32 failed_turns = 4;
  uint64 input_tokens = 5;
  uint64 output_tokens = 6;
  uint32 busy_seconds = 7;
}

message WatchResponse {
  ...
  // Every message: the machine's work_seq. Behind it, call PullWork.
  uint64 work_seq = 10;
}
```

A page carries up to 31 days of changed hours, read in one SQLite read
transaction, in `seq` order. An hour that changes again is sent again,
whole; while sessions run, that is one or two hours of a few dozen rows.

### 6.2 hermeshd

- **When.** Only while the app runs in the foreground (on the iPhone the
  process is frozen otherwise; the notification extension never pulls):
  1. **Catching up.** When a machine's feed starts or resumes (the app
     opens, comes back, or the machine comes online) and its `work_seq` is
     beyond what is held: one pull, at once.
  2. **Following.** After that, a machine whose `work_seq` moved is pulled
     at most once a minute, whatever screen is open, so an offline-to-be
     machine is never far behind. A machine that did nothing is never
     pulled. The overview refreshes no more often than today's
     once a minute (its "N running" label is live from the sessions anyway).
  3. **This device's own machine** is never pulled: `QueryWork` reads its
     tables in process, and its screens hear of its changes within a second.

  One pull in flight per machine; it repeats while `more`.
- **Applying.** One `owner.db` transaction per page: a different `store`
  first deletes the machine's rows; then keys and sessions are upserted,
  each hour's rows replaced, and the machine's `seq` saved. A failed page
  changes nothing and is asked again.
- **Cost.** §6.4.
- **Offline machines** keep counting up to their last sync; `QueryWork`
  says until when (§7).
- **Removed machines** keep their history (work that was done); erasing
  everything deletes `owner.db` as today.

### 6.3 Owner tables (owner.db)

```sql
CREATE TABLE work_machines (
  m INTEGER PRIMARY KEY,
  machine_id TEXT NOT NULL UNIQUE,
  store BLOB NOT NULL,
  seq INTEGER NOT NULL,
  synced_at INTEGER NOT NULL,          -- this device's clock, last page applied
  history INTEGER NOT NULL DEFAULT 0   -- 1 once the first full pull ended
);
CREATE TABLE work_keys (m INTEGER, k INTEGER, client TEXT, agent TEXT, project_id TEXT, PRIMARY KEY (m, k)) WITHOUT ROWID;
CREATE TABLE work_sessions (m INTEGER, n INTEGER, k INTEGER, status INTEGER, failed INTEGER, PRIMARY KEY (m, n)) WITHOUT ROWID;
CREATE TABLE work_quarters (q INTEGER, m INTEGER, k INTEGER, n INTEGER, PRIMARY KEY (q, m, k, n)) WITHOUT ROWID;
CREATE TABLE work_measures (q INTEGER, m INTEGER, k INTEGER, turns INTEGER, failed_turns INTEGER,
  input_tokens INTEGER, output_tokens INTEGER, busy_seconds INTEGER, PRIMARY KEY (q, m, k)) WITHOUT ROWID;
```

Quarters lead every key, so one range scan covers every machine. A device
keeps what its screens show, pruned daily like machines: an iPhone 130 days
(the account's 18 weeks and a margin), a Mac 400 days (53 weeks and a
margin). Its first pull of a machine asks only from there (`from_hour`).

### 6.4 What it costs a device

Measured on this Mac's own machine database (75 sessions, 90 turns in two
weeks, its rows turned into this design's) and estimated for a busy
machine (1,000 turns and 30 sessions a day, ten sessions at work at once).
Bytes are the replicated rows as protobuf; every call also carries the
transport's own envelope, as any call does today.

| | This Mac (real) | Busy machine |
|---|---|---|
| A day of work, replicated | 0.1–0.6 KB | ~8 KB |
| One following pull (the hour in progress) | ~0.1 KB | ~0.6 KB |
| Following pulls while the app is open | only after a change, ≤ 1 a minute | ≤ 60 an hour, ~40 KB an hour |
| Catching up after a day away | 1 call, < 1 KB | 1 call, ~8 KB |
| First pull on a new iPhone (130 days) | ~20 KB | ~1 MB, in pages |
| Kept on the iPhone | ~20 KB | ~1 MB |
| Ingest | one SQLite transaction per pull, under a millisecond | the same |

Compared with today: the overview asks every machine once a minute while
sessions change, whether or not that machine did anything, and each machine
reads its turns and session rows to answer; account usage asks every
machine again. Here only machines that changed are asked, they read a few
rows by primary key, and every question after that is local.

**Side effects.**

- **Counts are kept on the device's disk.** Today the iPhone holds sessions
  only in memory; this adds per-quarter counts with client ids, agent names
  and project ids (never prompts, replies or code) to `owner.db`, protected
  by iOS data protection, deleted by signing out and by erasing everything.
- **Freshness.** Numbers are at most a minute behind while the app is open,
  as today; on opening, the last numbers show at once and the catch-up
  follows within about a second. An offline machine shows its last synced
  work, labelled.
- **First run on a device** takes one to a few seconds for a busy machine's
  history; the overview shows what has arrived and says it is syncing.
- **Each owner device pulls on its own** (iPhone and Mac both): a machine
  answers each from a few rows; nothing is shared through the center.
- **A second replicated store** to keep right: a different `store` resets a
  machine's rows, hours are replaced whole (idempotent), and a rebuild on the
  machine makes every device start over cleanly.

## 7. Asking: QueryWork (local.proto)

```proto
service LocalService {
  // Work counted over a range, from this device's replica: no machine is
  // asked. By hour or day in time_zone, grouped as asked.
  rpc QueryWork(QueryWorkRequest) returns (QueryWorkResponse);
}

message QueryWorkRequest {
  string time_zone = 1;               // IANA
  string first_day = 2;               // YYYY-MM-DD in time_zone
  uint32 days = 3;                    // 1 to 400
  WorkBucket bucket = 4;              // HOUR or DAY
  repeated WorkDimension group_by = 5;
}

enum WorkBucket { WORK_BUCKET_UNSPECIFIED = 0; WORK_BUCKET_HOUR = 1; WORK_BUCKET_DAY = 2; }
enum WorkDimension {
  WORK_DIMENSION_UNSPECIFIED = 0;
  WORK_DIMENSION_CLIENT = 1;   // sessions: who started them; turns: who asked
  WORK_DIMENSION_AGENT = 2;
  WORK_DIMENSION_MACHINE = 3;
  WORK_DIMENSION_PROJECT = 4;
}

message WorkGroup { string client = 1; string agent = 2; string machine_id = 3; string project_id = 4; } // the dimensions asked

message SessionCount {
  uint32 bucket = 1;                  // from 0, the first hour or day
  WorkGroup group = 2;
  uint32 sessions = 3;                // at work in the bucket, each once
  uint32 done = 4;                    // of them, done now
}

message TurnCount {
  uint32 bucket = 1;
  WorkGroup group = 2;
  uint32 turns = 3;
  uint32 failed_turns = 4;
  uint64 input_tokens = 5;
  uint64 output_tokens = 6;
  uint64 busy_seconds = 7;
}

message WorkSource {
  string machine_id = 1;
  google.protobuf.Timestamp synced_at = 2; // counts include its work until then
  bool live = 3;                           // caught up, following now
  bool syncing = 4;                        // still taking its history
}

message QueryWorkResponse {
  repeated SessionCount sessions = 1;       // per bucket
  repeated SessionCount range_sessions = 2; // the whole range: each session once
  repeated TurnCount turns = 3;
  repeated TurnCount range_turns = 4;
  repeated WorkSource sources = 5;
}
```

**How it is answered.** hermeshd turns the range into bucket bounds in
quarters (Go's time zone database, so DST days come out right), then runs
two aggregations in SQLite, each a nested range scan of a clustered key, on
its copy of the other machines and, the same way, on its own machine's
tables in process ([workstats](../internal/workstats/workstats.go)):

```sql
WITH b(i, q0, q1) AS (VALUES (0, ?, ?), (1, ?, ?), ...)
SELECT b.i, w.m, w.k, sum(w.turns), sum(w.failed_turns), sum(w.input_tokens),
       sum(w.output_tokens), sum(w.busy_seconds)
FROM b JOIN work_measures w ON w.q >= b.q0 AND w.q < b.q1
GROUP BY b.i, w.m, w.k;

WITH b(i, q0, q1) AS (VALUES ...)
SELECT b.i, p.m, p.k, count(DISTINCT p.n),
       count(DISTINCT CASE WHEN s.status = :archived OR (s.status = :idle AND s.failed = 0) THEN p.n END)
FROM b JOIN work_quarters p ON p.q >= b.q0 AND p.q < b.q1
JOIN work_sessions s ON s.m = p.m AND s.n = p.n
GROUP BY b.i, p.m, p.k;
```

and the same with one bound for the range totals. Rows come back per
(bucket, machine, key); Go maps keys to the asked dimensions through the
dictionary and adds rows that fall into the same group (exact, §4).

**Speed.** Thirty days of five busy machines are ~36,000 presence and
~18,000 measure rows, aggregated inside SQLite: a target of 20 ms on an
iPhone. A year of days by agent (~220,000 measure rows) has a target of
100 ms; if it is slower, hermeshd keeps the finished days of the asked time
zone in a cache table that ingest invalidates by hour. Nothing crosses the
network, so switching Today, 7 days and 30 days is that fast too.

**Live.** The local `Watch` (C) gains `uint64 work_version = 16`, bumped
when a page is applied; screens ask `QueryWork` again when it moves (at
most once a second). The overview needs no timer. "N running" still comes
from the sessions in the replica.

## 8. Screens

| Number | From |
|---|---|
| Overview: sessions that worked, done | `range_sessions`, no grouping |
| Turns, tokens (card and flow caption) | `range_turns`, no grouping |
| Bars by hour or day | `sessions` per bucket, no grouping |
| Flow: who asked which agent, turns and tokens | `range_turns` by client, agent |
| An agent's turns; its cells by hour or day | `range_turns` and `sessions` by agent |
| Clients that asked for nothing | the grants, less the clients in `range_turns` |
| A machine offline or syncing | `sources`: "vps-1 is offline; counted up to Oct 29" |
| Account and Mac usage: tokens per day and agent, active days, streak, share | `turns` by day and agent over 18 or 53 weeks |
| Mac link map: today's tokens | `range_turns` for today |

One call per screen state: the overview asks once with
`group_by = [CLIENT, AGENT]` and derives everything above, since sessions
partition by those dimensions.

## 9. What goes away

- Machine: `turn_usage`, `putUsage`, `backfillUsage`, `Store.TurnUsage`,
  `Store.TurnSpans`, `Sessions.Usage`, `Sessions.Activity`.
- session.proto: `GetUsage`, `GetActivity` and their messages.
- local.proto and hermeshd: `GetUsage`, `GetActivity`, their fan-out, and
  the replica's kept usage answers (`heldUsage`, `keepUsage`).
- iPhone: `DayActivity` and the usage and activity loaders; Mac:
  `useUsage`. Both read `QueryWork`.
- Docs: state-sync.md §3.3, §4.1 and §4.4 (usage and activity are no
  longer asked of machines), iphone-app.md, technical-design.md (where they
  name `turn_usage`, `GetUsage` or `GetActivity`).

## 10. Success criteria

| | Target | Measured by |
|---|---|---|
| Machine storage | ≤ 10 KB per busy machine-day | a store test writing a synthetic busy year, read with `dbstat` |
| Machine write | ≤ 10 rows and one transaction per turn | the store test |
| Replica freshness | a turn's end reaches the owner device's counts within 1 s plus one relay round trip | an owner test |
| Catch-up after a day away | one `PullWork` per machine, ≤ 50 KB | an owner test counting calls and bytes |
| Following | no pull for a machine that did nothing; ≤ 1 a minute per machine otherwise; none in the background | an owner test with a fake clock |
| `QueryWork`, 30 days, 5 busy machines | ≤ 20 ms on iPhone (≤ 10 ms on a Mac) | a Go benchmark on a synthetic replica |
| `QueryWork`, 53 weeks by day and agent | ≤ 100 ms on iPhone | the same benchmark |
| Switching ranges | no network | an owner test: no relay calls |
| Offline machine | counted up to its last sync, and said so | an owner test |
| Who asked | a turn asked by another agent counts for that agent | a machine test, and the iPhone e2e |
| Rebuild | rebuilt counts equal those recorded live | a machine test over the e2e's sessions |

## 11. Steps

1. **Machine.** Tables, writes (§5.3), the quarter timer, retention,
   rebuild, `PullWork`, `work_seq` on `Watch`; drop `turn_usage`,
   `GetUsage`, `GetActivity`. Store tests and benchmarks.
2. **hermeshd.** `owner.db` tables, the pull loop (relay and in process),
   `QueryWork`, `work_version`; drop `GetUsage`, `GetActivity` and their
   caches. Owner tests and the query benchmark.
3. **iPhone.** The overview by range (segmented control, live label, bars,
   flow, agents, sources), the account's usage; UI test.
4. **Mac.** The link map's today and the usage grid from `QueryWork`; the
   desktop e2e's usage check.
5. **Docs** (§9).

## 12. Considered and not chosen

- **Machines answer each question, devices keep no copy.** Machines would
  serve `QueryWork` on their own tables and devices would add up the
  answers, keeping them in memory until a machine's `work_seq` moves. Nothing
  is stored on devices and there is no replication protocol. But each range a
  device has not asked yet waits a relay round trip per machine (~0.3 s), the
  answers are lost when the app restarts, and **an offline machine drops out of
  the numbers**: a MacBook closed for the evening would vanish from "7 days".
  The device copy exists for exactly those three things.
- **One aggregator for all devices.** Either the center, which then keeps
  the owner's work history (against keeping it to presence, names and
  versions), or one of the owner's machines, which every device would depend
  on and which may be the one that is offline.
- **The center keeps sealed copies** (counts sealed to the owner's devices,
  as agent messages are sealed in inboxes), so devices could read an offline
  machine's counts the machine never sent them. It adds storage and key
  management at the center for a gap the device copy already mostly closes.
- **Replicating per-turn rows.** Most flexible, about 14 times the bytes on
  every device, and every query would bucket turns itself.
- **Hour buckets.** A quarter of the rows, but wrong by half an hour in
  India, Nepal and other :30 and :45 zones.
- **Day rollups.** Days depend on the asking device's time zone, which moves
  with the owner.
- **Approximate distinct counts (HyperLogLog).** Exact sets are small at
  this scale.
- **Counting at the center.** Against keeping the center to presence, names
  and versions.

## 13. Not doing (now)

- Ranges on the Mac overview's own cards (it shows usage and today).
- Custom ranges, stepping back period by period, comparing with the previous
  period: `QueryWork` answers them when a screen needs them.
- Cost in money: some agents report `cost_usd`, not all; add a measure when a
  screen shows it.

