package sqlitedb

import (
	"database/sql"
	"time"
)

// Step is a move of data that a migration cannot say: it reads another
// store or another format and writes this one. It has a name, which is its
// place in the ledger, and is done once.
type Step struct {
	// Name is kept in the ledger: never change it, or the step runs again.
	Name string
	// Run moves the data and says how many things it moved. It must be safe
	// to run again after it failed part way (write by key, skip what is
	// there), and it must not change or remove what it reads: taking the
	// old away is a later step of its own, after a backup.
	Run func() (moved int64, err error)
}

// StepResult is what the ledger holds of a step after RunSteps.
type StepResult struct {
	Name string
	// Ran: this call ran it (it was not done before).
	Ran   bool
	Moved int64
	// Err: it failed, now or before; it is tried again at the next call.
	Err error
}

const stepsTable = `CREATE TABLE IF NOT EXISTS data_steps (
  name TEXT PRIMARY KEY,
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0,
  moved INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
)`

// RunSteps runs, in order, the steps of db's ledger (the table data_steps)
// that are not done: never run, or failed. Each is recorded when it starts
// and when it ends, with what it moved or why it failed. A step that fails
// does not stop the ones after it: they are independent moves, and the
// store opens with what did move. The caller says the failures aloud.
func RunSteps(db *sql.DB, steps []Step) ([]StepResult, error) {
	if _, err := db.Exec(stepsTable); err != nil {
		return nil, err
	}
	out := make([]StepResult, 0, len(steps))
	for _, s := range steps {
		var finished, moved int64
		var failed string
		err := db.QueryRow(`SELECT finished_at, moved, error FROM data_steps WHERE name = ?`, s.Name).Scan(&finished, &moved, &failed)
		if err == nil && finished > 0 && failed == "" {
			out = append(out, StepResult{Name: s.Name, Moved: moved})
			continue
		}
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if _, err := db.Exec(`INSERT INTO data_steps(name, started_at, attempts) VALUES(?, ?, 1)
			ON CONFLICT(name) DO UPDATE SET started_at = excluded.started_at, attempts = attempts + 1`, s.Name, time.Now().UnixMilli()); err != nil {
			return out, err
		}
		n, runErr := s.Run()
		msg := ""
		if runErr != nil {
			msg = runErr.Error()
		}
		if _, err := db.Exec(`UPDATE data_steps SET finished_at = ?, moved = ?, error = ? WHERE name = ?`, time.Now().UnixMilli(), n, msg, s.Name); err != nil {
			return out, err
		}
		out = append(out, StepResult{Name: s.Name, Ran: true, Moved: n, Err: runErr})
	}
	return out, nil
}
