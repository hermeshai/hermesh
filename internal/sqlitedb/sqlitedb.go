// Package sqlitedb opens SQLite databases with sane defaults and applies
// embedded, ordered migrations.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the database at path and applies
// migrations whose index is above the stored user_version.
func Open(path string, migrations []string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		db.Close()
		return nil, err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// OpenReader opens a pool of read-only connections to a database that Open
// already created. In WAL mode they read concurrently with the single
// writer connection instead of queueing behind its transactions.
func OpenReader(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=query_only(1)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
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
