// Package sqlitedb opens SQLite databases with sane defaults and applies
// embedded, versioned migrations verified by checksum (Flyway-style).
//
// How a store's data moves forward (docs/data-migrations.md):
//
//   - What a database's own tables and rows look like changes by the ordered
//     Migrations given to Open: SQL DDL/DML or Go transactions in one
//     monotonic version sequence (1, 2, 3...), each run atomically and
//     recorded in schema_history alongside its SHA-256 checksum and
//     PRAGMA user_version. Once shipped, a migration is never edited or
//     reordered; Open verifies every applied SQL checksum on startup.
//   - Existing databases from earlier Hermesh releases (user_version > 0
//     without schema_history) are baselined automatically into schema_history
//     before running newer migrations.
//   - Data imported from a separate legacy database file moves by Steps
//     (steps.go): named, idempotent, recorded in data_steps, reported on
//     failure without blocking startup, and retried at the next start.
//   - A database written by a newer Hermesh than this one is refused with
//     ErrNewer.
package sqlitedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	// ErrNewer indicates the database was written by a newer Hermesh whose
	// schema version exceeds the migrations known to this binary.
	ErrNewer = errors.New("written by a newer Hermesh")

	// ErrChecksumMismatch indicates an already-applied migration's SQL or kind
	// differs from what is recorded in schema_history.
	ErrChecksumMismatch = errors.New("migration checksum mismatch")

	// ErrInvalidMigration indicates a malformed Migration slice passed to Open.
	ErrInvalidMigration = errors.New("invalid migration definition")
)

const (
	KindSQL = "SQL"
	KindGo  = "GO"
)

const schemaHistoryDDL = `CREATE TABLE IF NOT EXISTS schema_history (
  version      INTEGER PRIMARY KEY,
  description  TEXT NOT NULL,
  kind         TEXT NOT NULL,
  checksum     TEXT NOT NULL,
  applied_at   INTEGER NOT NULL,
  execution_ms INTEGER NOT NULL
)`

var uriPathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

func sqliteURI(path, options string) string {
	return "file:" + uriPathEscaper.Replace(path) + "?" + options
}

// Migration is one ordered, atomic change to a database's schema or data.
// Exactly one of SQL or Run must be provided.
type Migration struct {
	Version     int
	Description string
	SQL         string
	Run         func(tx *sql.Tx) error
	// Backup writes a point-in-time snapshot (<path>.bak-v<current>) via
	// VACUUM INTO before applying pending migrations when upgrading a
	// non-empty database (current > 0).
	Backup bool
}

func (m Migration) kind() string {
	if m.Run != nil {
		return KindGo
	}
	return KindSQL
}

func (m Migration) checksum() string {
	if m.Run != nil {
		return "go"
	}
	return Checksum(m.SQL)
}

// Checksum returns the SHA-256 hex digest of normalized SQL text (CRLF/CR
// normalized to LF and surrounding whitespace trimmed).
func Checksum(sqlText string) string {
	norm := strings.ReplaceAll(sqlText, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	norm = strings.TrimSpace(norm)
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// HistoryEntry represents one applied migration recorded in schema_history.
type HistoryEntry struct {
	Version     int
	Description string
	Kind        string
	Checksum    string
	AppliedAt   int64
	ExecutionMs int64
}

func validateMigrations(migrations []Migration) error {
	for i, m := range migrations {
		if m.Version != i+1 {
			return fmt.Errorf("%w: index %d has version %d, want %d", ErrInvalidMigration, i, m.Version, i+1)
		}
		if strings.TrimSpace(m.Description) == "" {
			return fmt.Errorf("%w: migration %d has empty description", ErrInvalidMigration, m.Version)
		}
		hasSQL := strings.TrimSpace(m.SQL) != ""
		hasRun := m.Run != nil
		if hasSQL == hasRun {
			return fmt.Errorf("%w: migration %d (%s) must set exactly one of SQL or Run", ErrInvalidMigration, m.Version, m.Description)
		}
	}
	return nil
}

// OpenRaw opens a single-writer SQLite connection with standard writer pragmas
// without inspecting or modifying schema migrations.
func OpenRaw(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, sqliteURI(path, writerOptions))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Open opens (creating if needed) the database at path, validates applied
// migration checksums in schema_history (baselining pre-history stores from
// PRAGMA user_version), and applies pending migrations in order.
func Open(path string, migrations []Migration) (*sql.DB, error) {
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	db, err := OpenRaw(path)
	if err != nil {
		return nil, err
	}
	if err := migrate(context.Background(), db, path, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB, path string, migrations []Migration) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var uv int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		return err
	}
	if uv < 0 {
		return fmt.Errorf("%w: negative user_version %d in %s", ErrChecksumMismatch, uv, filepath.Base(path))
	}

	hasHistory, err := connTableExists(ctx, conn, "schema_history")
	if err != nil {
		return err
	}

	var current int
	if !hasHistory {
		if uv > len(migrations) {
			return fmt.Errorf("%w: %s is at schema %d, this Hermesh knows %d", ErrNewer, filepath.Base(path), uv, len(migrations))
		}
		current = uv
	} else {
		entries, err := connHistory(ctx, conn)
		if err != nil {
			return err
		}
		maxHistVer := len(entries)
		if len(entries) > 0 && entries[len(entries)-1].Version > maxHistVer {
			maxHistVer = entries[len(entries)-1].Version
		}
		maxVer := max(maxHistVer, uv)
		if maxVer > len(migrations) {
			return fmt.Errorf("%w: %s is at schema %d, this Hermesh knows %d", ErrNewer, filepath.Base(path), maxVer, len(migrations))
		}
		for i, e := range entries {
			if e.Version != i+1 {
				return fmt.Errorf("%w: %s schema_history gap at row %d (got version %d)", ErrChecksumMismatch, filepath.Base(path), i+1, e.Version)
			}
		}
		if uv != len(entries) {
			return fmt.Errorf("%w: %s user_version (%d) disagrees with schema_history (%d)", ErrChecksumMismatch, filepath.Base(path), uv, len(entries))
		}
		for i, e := range entries {
			m := migrations[i]
			if e.Kind != m.kind() {
				return fmt.Errorf("%w: %s migration %d (%s): recorded kind %s, current %s", ErrChecksumMismatch, filepath.Base(path), m.Version, m.Description, e.Kind, m.kind())
			}
			if e.Kind == KindSQL && e.Checksum != m.checksum() {
				return fmt.Errorf("%w: %s migration %d (%s): SQL was modified after being applied", ErrChecksumMismatch, filepath.Base(path), m.Version, m.Description)
			}
		}
		current = len(entries)
	}

	// Snapshot BEFORE baselineHistory or pending migrations touch any page of the database.
	if current > 0 && current < len(migrations) && needsBackup(migrations[current:]) {
		if err := backupDatabase(ctx, conn, path, current); err != nil {
			return err
		}
	}

	if !hasHistory {
		if err := baselineHistory(ctx, conn, migrations[:current]); err != nil {
			return err
		}
	}

	if current < len(migrations) {
		// Follow SQLite's official 12-step schema change procedure on the
		// pinned writer connection: disable connection-level foreign_keys
		// outside transactions so table rebuilds (CREATE new -> INSERT ->
		// DROP old -> RENAME) succeed, verify PRAGMA foreign_key_check inside
		// each transaction before COMMIT, and always re-enable foreign_keys.
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		defer func() {
			_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
		}()
		for _, m := range migrations[current:] {
			if err := applyMigration(ctx, conn, m); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, m Migration) error {
	start := time.Now()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if m.Run != nil {
		err = m.Run(tx)
	} else {
		_, err = tx.ExecContext(ctx, m.SQL)
	}
	if err != nil {
		return fmt.Errorf("migration %d (%s): %w", m.Version, m.Description, err)
	}
	if err := checkForeignKeys(tx); err != nil {
		return fmt.Errorf("migration %d (%s): %w", m.Version, m.Description, err)
	}
	execMs := time.Since(start).Milliseconds()
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO schema_history(version, description, kind, checksum, applied_at, execution_ms) VALUES(?, ?, ?, ?, ?, ?)`,
		m.Version, m.Description, m.kind(), m.checksum(), time.Now().UnixMilli(), execMs,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
		return err
	}
	return tx.Commit()
}

func checkForeignKeys(tx *sql.Tx) error {
	rows, err := tx.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var tbl, parent string
		var rowid sql.NullInt64
		var fkid int
		_ = rows.Scan(&tbl, &rowid, &parent, &fkid)
		return fmt.Errorf("foreign key violation on table %s referencing %s", tbl, parent)
	}
	return rows.Err()
}

func connTableExists(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var one int
	err := conn.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func baselineHistory(ctx context.Context, conn *sql.Conn, applied []Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaHistoryDDL); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, m := range applied {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO schema_history(version, description, kind, checksum, applied_at, execution_ms) VALUES(?, ?, ?, ?, ?, 0)`,
			m.Version, m.Description, m.kind(), m.checksum(), now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func needsBackup(pending []Migration) bool {
	for _, m := range pending {
		if m.Backup {
			return true
		}
	}
	return false
}

func backupDatabase(ctx context.Context, conn *sql.Conn, path string, currentVersion int) error {
	bakPath := fmt.Sprintf("%s.bak-v%d", path, currentVersion)
	if fi, err := os.Stat(bakPath); err == nil && fi.Size() > 0 {
		return nil // keep the pristine pre-upgrade snapshot from the first attempt
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmpPath := bakPath + ".tmp"
	_ = os.Remove(tmpPath)
	escaped := strings.ReplaceAll(tmpPath, "'", "''")
	if _, err := conn.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("backup %s before migration: %w", filepath.Base(path), err)
	}
	_ = os.Chmod(tmpPath, 0o600)
	if err := os.Rename(tmpPath, bakPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish backup %s: %w", filepath.Base(bakPath), err)
	}
	return nil
}

func connHistory(ctx context.Context, conn *sql.Conn) ([]HistoryEntry, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version, description, kind, checksum, applied_at, execution_ms FROM schema_history ORDER BY version ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Version, &e.Description, &e.Kind, &e.Checksum, &e.AppliedAt, &e.ExecutionMs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// History returns all rows in schema_history ordered by version ascending.
func History(db *sql.DB) ([]HistoryEntry, error) {
	rows, err := db.Query(`SELECT version, description, kind, checksum, applied_at, execution_ms FROM schema_history ORDER BY version ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Version, &e.Description, &e.Kind, &e.Checksum, &e.AppliedAt, &e.ExecutionMs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OpenReader opens a pool of read-only connections to a database that Open
// already created. In WAL mode they read concurrently with the single
// writer connection instead of queueing behind its transactions.
func OpenReader(path string) (*sql.DB, error) {
	db, err := sql.Open(driverName, sqliteURI(path, readerOptions))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(runtime.NumCPU())
	db.SetMaxIdleConns(runtime.NumCPU())
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Checkpointer moves WAL content into the database file from its own
// connection, so that the writer never does it inside a commit (SQLite's
// automatic checkpoint runs in the committing connection).
//
// PASSIVE checkpoints never block anyone, but while readers keep coming
// they never let the WAL wrap around, so under a steady event stream it
// grew to 3.7 GB and I/O on it stalled the writer for over a minute. Past
// walLimit the checkpointer therefore runs RESTART, which waits briefly
// for readers and makes the next commit start the WAL from the
// beginning; journal_size_limit then trims the file.
type Checkpointer struct {
	db   *sql.DB
	stop chan struct{}
	done chan struct{}
}

const walLimit = 64 << 20

// StartCheckpointer turns off automatic checkpoints on writer and
// checkpoints path every interval; observe, if set, gets each duration.
func StartCheckpointer(path string, writer *sql.DB, interval time.Duration, observe func(time.Duration)) (*Checkpointer, error) {
	if _, err := writer.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		return nil, err
	}
	if _, err := writer.Exec(fmt.Sprintf("PRAGMA journal_size_limit=%d", walLimit)); err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, sqliteURI(path, checkpointOptions))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Checkpointer{db: db, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-c.stop:
				db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
				return
			case <-t.C:
			}
			mode := "PASSIVE"
			if st, err := os.Stat(path + "-wal"); err == nil && st.Size() > walLimit {
				mode = "RESTART"
			}
			start := time.Now()
			db.Exec("PRAGMA wal_checkpoint(" + mode + ")")
			if observe != nil {
				observe(time.Since(start))
			}
		}
	}()
	return c, nil
}

// Close runs a final checkpoint (call it before closing the writer).
func (c *Checkpointer) Close() error {
	close(c.stop)
	<-c.done
	return c.db.Close()
}
