package sqlitedb

import (
	"errors"
	"path/filepath"
	"testing"
)

// A step runs once, its result kept in the ledger; one that failed says so
// and is tried again at the next start, without stopping the ones after
// it; a database from a newer Hermesh is not opened.
func TestStepsRunOnceAndFailuresAreKeptAndRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := Open(path, []string{`CREATE TABLE t (k TEXT PRIMARY KEY)`})
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
	two := []string{`CREATE TABLE a (k TEXT)`, `CREATE TABLE b (k TEXT)`}
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
