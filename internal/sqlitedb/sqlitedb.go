// Package sqlitedb opens SQLite databases with sane defaults and applies
// embedded, ordered migrations.
//
// How a store's data moves forward (docs/data-migrations.md):
//
//   - What a database's own tables look like changes by the migrations given
//     to Open: ordered, each once and in a transaction, counted in
//     user_version. They are only added to, never edited.
//   - Data that comes from another store or another format (an old database
//     file, old messages) moves by Steps (steps.go): named, each once, kept
//     in a ledger with what it did, said aloud when it fails and tried again
//     at the next start.
//   - A database written by a newer Hermesh than this one is not opened.
package sqlitedb

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ErrNewer: the database was written by a newer Hermesh, whose tables this
// one does not know. Opening it would run on a shape it was not written
// for, so it is refused: update Hermesh (or put the newer one back).
var ErrNewer = errors.New("written by a newer Hermesh")

// Open opens (creating if needed) the database at path and applies
// migrations whose index is above the stored user_version.
func Open(path string, migrations []string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, "file:"+path+"?"+writerOptions)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		db.Close()
		return nil, err
	}
	if v > len(migrations) {
		db.Close()
		return nil, fmt.Errorf("%w: %s is at schema %d, this Hermesh knows %d", ErrNewer, filepath.Base(path), v, len(migrations))
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
	db, err := sql.Open(driverName, "file:"+path+"?"+readerOptions)
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
	db, err := sql.Open(driverName, "file:"+path+"?"+checkpointOptions)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Checkpointer{db: db, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		idleInterval := max(interval, 2*time.Second)
		curInterval := interval
		t := time.NewTicker(curInterval)
		defer t.Stop()
		var settled bool
		var settledSize int64
		var settledMod time.Time
		idleTicks := 0
		setInterval := func(d time.Duration) {
			if curInterval != d {
				curInterval = d
				t.Reset(d)
			}
		}
		for {
			select {
			case <-c.stop:
				db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
				return
			case <-t.C:
			}
			st, err := os.Stat(path + "-wal")
			if err != nil || st.Size() == 0 || (settled && st.Size() == settledSize && st.ModTime().Equal(settledMod)) {
				idleTicks++
				if idleTicks >= 5 {
					setInterval(idleInterval)
				}
				continue
			}
			idleTicks = 0
			setInterval(interval)
			mode := "PASSIVE"
			if st.Size() > walLimit {
				mode = "RESTART"
			}
			start := time.Now()
			var busy, logFrames, ckptFrames int
			qerr := db.QueryRow("PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &logFrames, &ckptFrames)
			if observe != nil {
				observe(time.Since(start))
			}
			if qerr == nil && busy == 0 && logFrames == ckptFrames {
				if st2, err2 := os.Stat(path + "-wal"); err2 == nil {
					settled, settledSize, settledMod = true, st2.Size(), st2.ModTime()
				} else {
					settled, settledSize, settledMod = true, 0, time.Time{}
				}
			} else {
				settled = false
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
