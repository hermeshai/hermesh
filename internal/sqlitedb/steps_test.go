package sqlitedb

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStepsRunOnceAndFailuresAreKeptAndRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := Open(path, []Migration{
		{Version: 1, Description: "init", SQL: `CREATE TABLE t (k TEXT PRIMARY KEY)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runs := map[string]int{}
	broken := true
	steps := []Step{
		{Name: "first", Run: func() (int64, error) { runs["first"]++; return 3, nil }},
		{Name: "flaky", Run: func() (int64, error) {
			runs["flaky"]++
			if broken {
				return 1, errors.New("no such column: id")
			}
			return 7, nil
		}},
		{Name: "last", Run: func() (int64, error) { runs["last"]++; return 0, nil }},
	}
	res, err := RunSteps(db, steps)
	if err != nil || len(res) != 3 || !res[0].Ran || res[0].Moved != 3 || res[0].Err != nil || res[1].Err == nil || !res[2].Ran || res[2].Err != nil {
		t.Fatalf("the first start: %+v, %v", res, err)
	}
	// The next start: what is done is not run again; what failed is, and fails again, and is said again.
	res, _ = RunSteps(db, steps)
	if runs["first"] != 1 || runs["last"] != 1 || runs["flaky"] != 2 || res[0].Ran || res[0].Moved != 3 || res[1].Err == nil || !res[1].Ran {
		t.Fatalf("the second start: %+v, runs %v", res, runs)
	}
	var attempts int
	var said string
	db.QueryRow(`SELECT attempts, error FROM data_steps WHERE name = 'flaky'`).Scan(&attempts, &said)
	if attempts != 2 || said != "no such column: id" {
		t.Fatalf("the ledger: %d attempts, %q", attempts, said)
	}
	// Mended: it runs, and then no more.
	broken = false
	if res, _ = RunSteps(db, steps); res[1].Err != nil || res[1].Moved != 7 {
		t.Fatalf("mended: %+v", res[1])
	}
	if res, _ = RunSteps(db, steps); runs["flaky"] != 3 || res[1].Ran || res[1].Moved != 7 {
		t.Fatalf("done: %+v, runs %v", res[1], runs)
	}
}

func TestADatabaseFromANewerHermeshIsNotOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	two := []Migration{
		{Version: 1, Description: "create_a", SQL: `CREATE TABLE a (k TEXT)`},
		{Version: 2, Description: "create_b", SQL: `CREATE TABLE b (k TEXT)`},
	}
	db, err := Open(path, two)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path, two[:1]); !errors.Is(err, ErrNewer) {
		t.Fatalf("an older Hermesh opened it: %v", err)
	}
	if db, err = Open(path, two); err != nil {
		t.Fatalf("the same Hermesh: %v", err)
	}
	db.Close()
}

func TestChecksumValidationAndTamperingMatrix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	orig := []Migration{
		{Version: 1, Description: "create_items", SQL: "CREATE TABLE items (\n  id TEXT PRIMARY KEY,\n  val TEXT NOT NULL\n)"},
		{Version: 2, Description: "seed_item", Run: func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO items(id, val) VALUES ('i1', 'v1')`)
			return err
		}},
	}
	db, err := Open(path, orig)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Surrounding whitespace, CRLF, and lone CR normalization must NOT trigger a mismatch.
	normalized := []Migration{
		{Version: 1, Description: "create_items", SQL: "\r\n\t CREATE TABLE items (\r\n  id TEXT PRIMARY KEY,\r  val TEXT NOT NULL\r\n) \n "},
		orig[1],
	}
	db, err = Open(path, normalized)
	if err != nil {
		t.Fatalf("whitespace/CRLF-normalized SQL should match checksum: %v", err)
	}
	db.Close()

	// 1. Substantive SQL change -> ErrChecksumMismatch.
	sqlTampered := []Migration{
		{Version: 1, Description: "create_items", SQL: `CREATE TABLE items (id TEXT PRIMARY KEY, val TEXT)`},
		orig[1],
	}
	if _, err := Open(path, sqlTampered); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch on tampered SQL, got: %v", err)
	}

	// 2. Kind changed from GO to SQL -> ErrChecksumMismatch.
	kindTampered := []Migration{
		orig[0],
		{Version: 2, Description: "seed_item", SQL: `INSERT INTO items(id, val) VALUES ('i1', 'v1')`},
	}
	if _, err := Open(path, kindTampered); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch on kind change, got: %v", err)
	}

	// 3. user_version out of sync with schema_history -> ErrChecksumMismatch (when <= len(migrations)).
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := Open(path, orig); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch on user_version disagreement, got: %v", err)
	}

	// Restore user_version = 2, then delete row 1 to create a version gap [2].
	raw, _ = OpenRaw(path)
	_, _ = raw.Exec(`PRAGMA user_version = 2; DELETE FROM schema_history WHERE version = 1;`)
	raw.Close()
	if _, err := Open(path, orig); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch on schema_history gap, got: %v", err)
	}

	// Even with a gap at row 1, an older binary knowing only V1 still reports ErrNewer (version 2 > 1).
	if _, err := Open(path, orig[:1]); !errors.Is(err, ErrNewer) {
		t.Fatalf("expected ErrNewer to take precedence for older binary, got: %v", err)
	}

	// Negative user_version -> ErrChecksumMismatch.
	negPath := filepath.Join(t.TempDir(), "neg.db")
	raw, _ = OpenRaw(negPath)
	_, _ = raw.Exec(`PRAGMA user_version = -3`)
	raw.Close()
	if _, err := Open(negPath, orig); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch on negative user_version, got: %v", err)
	}
}

func TestMixedSQLAndGoMigrationsAndForeignKeyEnforcement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	migs := []Migration{
		{
			Version:     1,
			Description: "create_users",
			SQL:         `CREATE TABLE users (id TEXT PRIMARY KEY, raw_name TEXT NOT NULL)`,
		},
		{
			Version:     2,
			Description: "seed_and_add_upper",
			SQL: `
INSERT INTO users(id, raw_name) VALUES ('u1', 'alice'), ('u2', 'bob');
ALTER TABLE users ADD COLUMN upper_name TEXT NOT NULL DEFAULT '';
`,
		},
		{
			Version:     3,
			Description: "backfill_upper_in_go",
			Run: func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE users SET upper_name = upper(raw_name)`)
				return err
			},
		},
		{
			Version:     4,
			Description: "rebuild_table_preserving_fk",
			SQL: `
CREATE TABLE posts (post_id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id));
INSERT INTO posts(post_id, user_id) VALUES ('p1', 'u1');
CREATE TABLE users_new (id TEXT PRIMARY KEY, display_name TEXT NOT NULL);
INSERT INTO users_new(id, display_name) SELECT id, upper_name FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
`,
		},
	}

	db, err := Open(path, migs)
	if err != nil {
		t.Fatalf("Open with mixed SQL + Go + FK table rebuild: %v", err)
	}
	defer db.Close()

	// Verify foreign_keys = ON was restored on the writer connection after migrations!
	var fkEnabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fkEnabled); err != nil || fkEnabled != 1 {
		t.Fatalf("expected PRAGMA foreign_keys = 1 after Open, got %d (%v)", fkEnabled, err)
	}
	if _, err := db.Exec(`INSERT INTO posts(post_id, user_id) VALUES ('p_bad', 'missing_user')`); err == nil {
		t.Fatal("expected runtime foreign key constraint enforcement after Open, but invalid INSERT succeeded")
	}

	// Now attempt a V5 rebuild that orphans 'p1' -> checkForeignKeys must reject and roll back!
	badV5 := append([]Migration(nil), migs...)
	badV5 = append(badV5, Migration{
		Version:     5,
		Description: "orphan_fk_violation",
		SQL: `
CREATE TABLE users_broken (id TEXT PRIMARY KEY, display_name TEXT NOT NULL);
INSERT INTO users_broken(id, display_name) VALUES ('u2', 'BOB');
DROP TABLE users;
ALTER TABLE users_broken RENAME TO users;
`,
	})
	db.Close()

	if _, err := Open(path, badV5); err == nil || !strings.Contains(err.Error(), "foreign key violation") {
		t.Fatalf("expected foreign key violation error from V5, got: %v", err)
	}

	// Re-open at V4: 'u1' and 'p1' must still be intact (V5 rolled back atomically).
	db, err = Open(path, migs)
	if err != nil {
		t.Fatalf("re-open at V4 after rolled-back V5: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT display_name FROM users WHERE id = 'u1'`).Scan(&name); err != nil || name != "ALICE" {
		t.Fatalf("expected u1 intact after rollback, got %q (%v)", name, err)
	}
}

func TestMidSequenceFailureAndPanicRollbackCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "midfail.db")
	failV3 := true
	panicV3 := true

	makeMigs := func() []Migration {
		return []Migration{
			{Version: 1, Description: "v1", SQL: `CREATE TABLE items (id TEXT PRIMARY KEY)`},
			{Version: 2, Description: "v2", SQL: `INSERT INTO items(id) VALUES ('from_v2')`},
			{Version: 3, Description: "v3_go", Run: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`INSERT INTO items(id) VALUES ('partial_v3')`); err != nil {
					return err
				}
				if panicV3 {
					panic("boom inside migration")
				}
				if failV3 {
					return errors.New("v3 transient error")
				}
				return nil
			}},
			{Version: 4, Description: "v4", SQL: `INSERT INTO items(id) VALUES ('from_v4')`},
		}
	}

	// 1. Panic inside V3 rolls back V3's partial insert while keeping V1 & V2 committed.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic to propagate")
			}
		}()
		_, _ = Open(path, makeMigs())
	}()

	// 2. Error inside V3 also rolls back V3 without running V4.
	panicV3 = false
	if _, err := Open(path, makeMigs()); err == nil || !strings.Contains(err.Error(), "v3 transient error") {
		t.Fatalf("expected v3 error, got %v", err)
	}

	// Verify DB is cleanly at V2 with zero partial V3 rows.
	dbAtV2, err := Open(path, makeMigs()[:2])
	if err != nil {
		t.Fatalf("Open at V2 after failed V3: %v", err)
	}
	var count int
	dbAtV2.QueryRow(`SELECT count(*) FROM items WHERE id = 'partial_v3'`).Scan(&count)
	dbAtV2.Close()
	if count != 0 {
		t.Fatalf("partial_v3 row leaked despite rollback")
	}

	// 3. Once V3 succeeds, V3 and V4 apply cleanly.
	failV3 = false
	db, err := Open(path, makeMigs())
	if err != nil {
		t.Fatalf("Open after fixing V3: %v", err)
	}
	defer db.Close()
	db.QueryRow(`SELECT count(*) FROM items`).Scan(&count)
	if count != 3 {
		t.Fatalf("expected 3 items (from_v2, partial_v3, from_v4), got %d", count)
	}
}

func TestBaselineBackupAtomicityAndEmptyStoreNoBackup(t *testing.T) {
	dir := t.TempDir()

	// Brand-new empty store (uv == 0) must NOT write a useless .bak-v0 file.
	freshPath := filepath.Join(dir, "fresh.db")
	freshDB, err := Open(freshPath, []Migration{
		{Version: 1, Description: "init", Backup: true, SQL: `CREATE TABLE t (k TEXT PRIMARY KEY)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	freshDB.Close()
	if _, err := os.Stat(freshPath + ".bak-v0"); !os.IsNotExist(err) {
		t.Fatalf("expected no .bak-v0 for brand-new empty DB, got %v", err)
	}

	// Simulate a v0.1.9 store (uv = 1, no schema_history) plus a leftover 0-byte .bak-v1 and stale .bak-v1.tmp.
	path := filepath.Join(dir, "legacy_uv.db")
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT NOT NULL); INSERT INTO kv VALUES ('a', 'orig'); PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	bakPath := path + ".bak-v1"
	if err := os.WriteFile(bakPath, nil, 0o600); err != nil { // 0-byte corrupt placeholder from earlier crash
		t.Fatal(err)
	}
	if err := os.WriteFile(bakPath+".tmp", []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	failV2 := true
	migs := []Migration{
		{Version: 1, Description: "create_kv", SQL: `CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT NOT NULL)`},
		{Version: 2, Description: "rewrite_kv", Backup: true, Run: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE kv SET v = 'mutated'`); err != nil {
				return err
			}
			if failV2 {
				return errors.New("v2 failed")
			}
			return nil
		}},
	}

	// Attempt 1 fails in V2, AFTER replacing the 0-byte .bak-v1 with a valid untouched v1 snapshot.
	if _, err := Open(path, migs); err == nil {
		t.Fatal("expected V2 failure")
	}
	bakReader, err := OpenReader(bakPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	var bakUV int
	var hasHistInBak int
	bakReader.QueryRow(`PRAGMA user_version`).Scan(&bakUV)
	bakReader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_history'`).Scan(&hasHistInBak)
	bakReader.Close()
	if bakUV != 1 || hasHistInBak != 0 {
		t.Fatalf("backup must be taken BEFORE baselineHistory touches the DB: uv=%d hasHist=%d", bakUV, hasHistInBak)
	}
	bakBytesAttempt1, _ := os.ReadFile(bakPath)

	// Attempt 2 succeeds and preserves the original .bak-v1 untouched.
	failV2 = false
	db, err := Open(path, migs)
	if err != nil {
		t.Fatalf("Attempt 2 Open: %v", err)
	}
	db.Close()
	bakBytesAttempt2, _ := os.ReadFile(bakPath)
	if string(bakBytesAttempt1) != string(bakBytesAttempt2) {
		t.Fatal("Attempt 2 overwrote the pristine .bak-v1 from Attempt 1")
	}
}

func TestSpecialCharactersInDatabasePathsAndCorruptFile(t *testing.T) {
	// Spaces, single quotes, Unicode, '#', and '%' are valid directory chars across platforms ('?' on Unix).
	sub := "Yi's 中文 #1 100% space"
	if runtime.GOOS != "windows" {
		sub += " q?mark"
	}
	dir := filepath.Join(t.TempDir(), sub)
	path := filepath.Join(dir, "sessions#test%1.db")

	migs := []Migration{
		{Version: 1, Description: "v1", SQL: `CREATE TABLE t (k TEXT PRIMARY KEY)`},
	}
	db, err := Open(path, migs)
	if err != nil {
		t.Fatalf("Open with special URI characters in path (%q): %v", path, err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES ('ok')`); err != nil {
		t.Fatal(err)
	}
	ckpt, err := StartCheckpointer(path, db, 20*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("StartCheckpointer with special path: %v", err)
	}
	reader, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader with special path: %v", err)
	}
	var got string
	if err := reader.QueryRow(`SELECT k FROM t`).Scan(&got); err != nil || got != "ok" {
		t.Fatalf("OpenReader read mismatch: %q (%v)", got, err)
	}
	reader.Close()
	ckpt.Close()
	db.Close()

	// Confirm the file was actually created at the exact path (not truncated at '#' or '?') and Backup works with quotes!
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected DB file at exact path %q: %v", path, err)
	}
	migsV2 := append(migs, Migration{Version: 2, Description: "v2_with_backup", Backup: true, SQL: `INSERT INTO t VALUES ('v2')`})
	db2, err := Open(path, migsV2)
	if err != nil {
		t.Fatalf("Open V2 with Backup on path containing single quote: %v", err)
	}
	db2.Close()
	if _, err := os.Stat(path + ".bak-v1"); err != nil {
		t.Fatalf("expected backup file at %q: %v", path+".bak-v1", err)
	}

	// Corrupt non-SQLite file on disk returns error cleanly.
	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	_ = os.WriteFile(corruptPath, []byte("not a valid sqlite3 header at all!"), 0o600)
	if _, err := Open(corruptPath, migs); err == nil {
		t.Fatal("expected error opening corrupt non-SQLite file")
	}
}

func TestConcurrentReadersWriterAndCheckpointer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conc.db")
	db, err := Open(path, []Migration{
		{Version: 1, Description: "init", SQL: `CREATE TABLE counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	ckpt, err := StartCheckpointer(path, db, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	rdb, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if _, err := db.Exec(`INSERT INTO counter(id, n) VALUES (?, ?)`, i+1, i); err != nil {
				t.Errorf("writer: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			var c int
			if err := rdb.QueryRow(`SELECT count(*) FROM counter`).Scan(&c); err != nil {
				t.Errorf("reader: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	rdb.Close()
	ckpt.Close()
	db.Close()
}

func TestInvalidMigrationDefinitionsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	cases := [][]Migration{
		{{Version: 2, Description: "gap", SQL: `CREATE TABLE a(x INT)`}},
		{{Version: 1, Description: "   ", SQL: `CREATE TABLE a(x INT)`}},
		{{Version: 1, Description: "neither"}},
		{{Version: 1, Description: "both", SQL: `SELECT 1`, Run: func(*sql.Tx) error { return nil }}},
	}
	for i, tc := range cases {
		if _, err := Open(path, tc); !errors.Is(err, ErrInvalidMigration) {
			t.Fatalf("case %d: expected ErrInvalidMigration, got %v", i, err)
		}
	}
}

func TestCheckpointerIdlesWithoutWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idle.db")
	db, err := Open(path, []Migration{
		{Version: 1, Description: "init", SQL: `CREATE TABLE t (k TEXT PRIMARY KEY)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var runs atomic.Int64
	ckpt, err := StartCheckpointer(path, db, 40*time.Millisecond, func(time.Duration) {
		runs.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ckpt.Close()

	// After migrations settled, an idle DB must not keep executing checkpoints on every tick.
	before := runs.Load()
	time.Sleep(350 * time.Millisecond)
	if got := runs.Load() - before; got > 1 {
		t.Fatalf("idle database ran %d checkpoints in 350ms; want <= 1", got)
	}

	// A write wakes checkpointing again.
	if _, err := db.Exec(`INSERT INTO t(k) VALUES('a')`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runs.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("checkpointer did not checkpoint after write")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
