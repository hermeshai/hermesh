# Moving data forward

How what a Hermesh keeps (its SQLite stores on machines and owner devices)
goes from one version's shape to the next. Written 2026-11-24, after the
kernel's store took over from two older ones and the move into it failed
without a word: 235 sessions were listed and none opened.

## 1. Two kinds of change

| | A store's own tables change | Data comes from another store or format |
|---|---|---|
| Example | a column added to `sessions` | the V1 sessions of `hermeshd.db` into `sessions_v2.db` |
| Said as | a migration: SQL in the store's `migrations` list | a step: Go code in the store's `Steps` |
| Run by | `sqlitedb.Open` | `sqlitedb.RunSteps` |
| Counted in | `PRAGMA user_version` | the table `data_steps` (the ledger) |
| Atomic | each in a transaction | each thing moved in a transaction (one session whole, or not yet) |

Migrations were there before and are unchanged. Steps are new.

## 2. The rules

1. **Migrations are only added to.** Never edit or reorder one that shipped:
   a store that ran it will not run it again.
2. **A step has a name, and the name is its place in the ledger.** Never
   rename one; never remove one (a store that never ran it then never
   would). The ledger keeps, per step: when it started and ended, how many
   things it moved, how often it was tried, and its error.
3. **A step is done once.** One that succeeded is not run again. One that
   failed is run again at every start until it succeeds.
4. **A step can be run again after failing part way.** It writes by key and
   skips what is already there, so a second run moves only what the first
   did not.
5. **A step does not change what it reads.** The old store is opened to
   read. Taking the old data away is a later step of its own, some releases
   on, and that step makes a copy of the file first (`VACUUM INTO`). No such
   step exists yet; when the first is written, the copy is part of it.
6. **A step that fails says so.** `RunSteps` returns each step's result; the
   store keeps them (`Store.Steps`); the machine logs a failed one at ERROR
   and reports it with its health (`MACHINE_PROBLEM_KIND_DATA_MOVE`, the
   step's name and error as its detail), which the Mac shows under Needs
   You. It does not keep the store from opening, nor the steps after it from
   running: the app works with what did move.
7. **Old messages are read as what they were.** When a protobuf message
   whose bytes are in a store is rewritten or removed, its descriptors as
   they were are kept beside the step (`internal/kernel/store/legacy.binpb`)
   and old payloads are decoded with them, then carried over by their
   fields' names. Field numbers of a new message are never assumed to be
   the old one's.
8. **A store from a newer Hermesh is not opened.** `sqlitedb.Open` refuses a
   database whose `user_version` is above the migrations it was given
   (`ErrNewer`): running on tables it was not written for is worse than not
   starting. Going back a version means putting the newer Hermesh back, or
   the store's backup.
9. **Each shape that shipped has a fixture.** A store's schema as it was on
   a real machine (no rows) is kept in `testdata/`, and a test writes rows
   into it as the old Hermesh did and opens it with the current code. A
   step is not written against the tables one remembers.

## 3. What it would have caught

The first import of V1 sessions selected `id`, `machine_id`, `created_as`
and `turn_count`; the table has `session_id` and none of the other three.
The query failed, the function returned, nothing was logged. Under the rules
above: rule 9's test fails before it ships (the fixture is the real table);
had it shipped, rule 6 puts "v1-sessions: no such column: id" in the log and
under Needs You, and rule 3 runs it again when a fixed version is installed.

## 4. The steps there are

In `internal/kernel/store/legacy.go`, for a machine's `sessions_v2.db`:

| Step | From | What moves | What does not |
|---|---|---|---|
| `v1-sessions` | `hermeshd.db` (machine store, schema 18) `sessions`, `session_events` | each session as it stood (nothing at work: what ran is idle, a turn in progress is an interrupted run); its conversation from its log: a turn is a run, and the messages, the messages between agents, the tools with their results and the errors are items | files attached to messages (the V1 store kept their paths, not the files); questions (they are answered) |
| `v2-threads` | `threads_v2.db` (schema 4) `threads`, `runs`, `turn_items`, `provider_threads`, `checkpoints`, `blobs` | each thread as a session with its runs, items, provider threads and checkpoints; the blobs' rows (their files are moved by `migrateLegacyDirs`) | the threads' log of events (a session's page is made of what it holds; its log goes on from where the thread's was); requests that waited; receipts; allow rules |

On the machine the bug was found on (a copy of its stores): 235 sessions and
3 threads, 472 runs and 27,702 items, in 1.5 seconds.

Owner devices keep a copy of machines' sessions, not sessions of their own,
so they have no step: a device has what its machines say.

## 5. Writing the next one

1. Dump the old store's schema from a real one into `testdata/`
   (`sqlite3 old.db .schema`): the schema only.
2. If its rows hold protobuf messages that are changing, build their
   descriptors from the commit before the change
   (`buf build --exclude-source-info -o legacy.binpb --path …`) and embed
   them.
3. Write the step in the store that receives the data and add it to the end
   of its list.
4. Write the test: rows as the old code wrote them; what is here after;
   that a second start moves nothing; that a broken row is said, kept in the
   ledger and moved once mended.
5. Run it on a copy of a real store (`sqlite3 real.db ".backup copy.db"`)
   and compare the counts, before it reaches anyone's machine.

## 6. Not done

- A copy before a step that removes data: no step removes any (rule 5).
- Showing a failed move on the iPhone: it shows no machine health at all
  today; the Mac does.
- The owner devices' older copies of threads: `owner.db`'s migration
  `sessionStoreMigrations` drops `threads_v2_cache`, `threads_v2_outbox` and
  `threads_v2_blobs` and starts the session ones empty. The cache fills
  again from the machines; a command queued offline in the old outbox, not
  yet sent, is lost with it. That was so before this document and is not
  changed here.
