# Moving data forward

How Hermesh evolves its SQLite stores on machines, owner devices, and relays
without renaming database files or silently losing user data.

## 1. Domain-named databases, versioned inside

Database filenames name a domain, never a schema version (`hermeshd.db`,
`sessions.db`, `owner.db`, `relay.db`, `mcp.db`, `her.db`). Everything about
which version a file is at lives inside the database itself.

| | A store's own schema or rows change | Data is imported from a separate legacy file |
|---|---|---|
| Example | adding a column to `sessions`, or rewriting a Protobuf `BLOB` in place | importing V1 sessions from `hermeshd.db` into `sessions.db` |
| Said as | a `sqlitedb.Migration` (`SQL` or `Run func(*sql.Tx) error`) | a `sqlitedb.Step` in the store's `Steps` |
| Run by | `sqlitedb.Open` | `sqlitedb.RunSteps` |
| Recorded in | `schema_history` (plus `PRAGMA user_version`) | `data_steps` |
| Verified by | SHA-256 checksum on every applied `SQL` migration | idempotent key checks + `testdata/` fixtures |
| Failure mode | atomic rollback; `Open` returns an error (fail-fast) | per-item transaction; logged at ERROR and reported via `MACHINE_PROBLEM_KIND_DATA_MOVE`, retried next start |

## 2. The rules

1. **Migrations are immutable once shipped.** Never edit, reorder, or delete a
   `Migration` that has shipped in a release. `sqlitedb.Open` normalizes line
   endings and surrounding whitespace, hashes each `SQL` migration with
   SHA-256, and compares it against `schema_history`: any modification to an
   applied migration fails immediately with `ErrChecksumMismatch`.
2. **SQL and Go data migrations share one sequential timeline.** Every
   `Migration` has an explicit `Version` (`1, 2, 3, ...`) and `Description`,
   and sets either `SQL` or `Run func(*sql.Tx) error`. When an evolution adds a
   column, decodes/rewrites Protobuf `BLOB` payloads in Go, and then enforces
   constraints or rebuilds a table, write them as consecutive numbered
   `Migration`s. Each runs inside a transaction with `PRAGMA defer_foreign_keys = ON`.
3. **High-risk in-place upgrades snapshot first.** Setting `Backup: true` on a
   `Migration` causes `sqlitedb.Open` to write `<db>.bak-v<current>` via
   `VACUUM INTO` before applying pending migrations on a non-empty store. If a
   backup for that version already exists from an earlier failed attempt, it is
   preserved untouched.
4. **Pre-history stores baseline automatically.** Stores created by `v0.1.9` or
   earlier recorded only `PRAGMA user_version`. When `sqlitedb.Open` opens a
   database with `user_version > 0` and no `schema_history` table, it baselines
   versions `1..user_version` into `schema_history` (with their current SQL
   checksums) before applying newer migrations.
5. **A store from a newer Hermesh is refused.** `sqlitedb.Open` refuses any
   database whose version in `schema_history` or `user_version` exceeds the
   migrations compiled into the binary (`ErrNewer`).
6. **Cross-store steps are named, idempotent, and read-only on the source.**
   - Never rename or remove a `Step` that shipped; its `Name` is its primary key
     in `data_steps`.
   - A `Step` writes by key and skips rows already present so partial failures
     resume cleanly on the next start.
   - The source file is opened via `sqlitedb.OpenReader` and never modified.
   - A failed `Step` is reported in machine health
     (`MACHINE_PROBLEM_KIND_DATA_MOVE`, shown under Needs You on Mac) without
     preventing the store from opening with what did move.
7. **Old protobuf payloads are decoded with their original descriptors.** When
   a stored protobuf message is rewritten or removed, build its descriptors
   from the release before the change (`buf build --exclude-source-info`) and
   decode old bytes via `dynamicpb` by field name rather than assuming field
   numbers stayed compatible.
8. **Every shipped legacy shape has a real schema fixture.** Dump the real
   schema (`sqlite3 old.db .schema`, no rows) into `testdata/` and test against
   rows written in the legacy format.

## 3. Active cross-store steps

In `internal/kernel/store/legacy.go`, for a machine's `sessions.db`:

| Step | From | What moves | What does not |
|---|---|---|---|
| `v1-sessions` | `hermeshd.db` (machine store, schema 18) `sessions`, `session_events` | each V1 session as it stood (in-progress turns become interrupted runs) and its conversation reconstructed from `session_events` | legacy file attachment paths (V1 stored paths rather than content blobs); answered interactive questions |

Owner devices keep a replica cache of machines' sessions (`owner.db`) rather
than authoritative session records, so they require no cross-store step.

## 4. Writing a new migration or cross-store step

### In-place schema or data change (`Migration`)
1. Append a new `sqlitedb.Migration{Version: N, Description: "...", ...}` to
   the store's `migrations` slice.
2. Set `Backup: true` if the migration rebuilds a core table or rewrites
   existing `BLOB` payloads in Go.
3. Add a test that opens a store at version `N-1`, seeds representative rows,
   upgrades to `N`, and verifies both the data and `sqlitedb.History(db)`.

### Cross-store import (`Step`)
1. Dump the source store's real schema into `testdata/` (`sqlite3 old.db .schema`).
2. If protobuf definitions changed, build descriptors from the prior release tag
   (`go tool buf build --exclude-source-info -o legacy.binpb --path ...`) and
   embed them.
3. Add the `Step` to the receiving store and test: full import, idempotency on
   restart, partial failure recorded in `data_steps`, and recovery once mended.
