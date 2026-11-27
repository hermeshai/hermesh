package owner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

func TestOwnerStoreUpgradesAcrossAllHistoricalAndDevStates(t *testing.T) {
	for _, startVer := range []int{1, 3, 6} {
		t.Run(fmt.Sprintf("v0.1.x user_version %d baselines and upgrades to V7", startVer), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.db")
			raw, err := sqlitedb.OpenRaw(path)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < startVer; i++ {
				if _, err := raw.Exec(migrations[i].SQL); err != nil {
					t.Fatalf("v%d: %v", i+1, err)
				}
			}
			if _, err := raw.Exec(fmt.Sprintf(`INSERT INTO kv(key, value) VALUES ('k1', x'01'); PRAGMA user_version = %d;`, startVer)); err != nil {
				t.Fatal(err)
			}
			raw.Close()

			st, err := openStore(path)
			if err != nil {
				t.Fatalf("openStore from v%d: %v", startVer, err)
			}
			defer st.db.Close()

			v, err := st.get("k1")
			if err != nil || len(v) != 1 || v[0] != 1 {
				t.Fatalf("expected kv row preserved, got %v (%v)", v, err)
			}
			hist, err := sqlitedb.History(st.db)
			if err != nil || len(hist) != 7 {
				t.Fatalf("expected 7 schema_history rows, got %+v (%v)", hist, err)
			}
		})
	}

	t.Run("pre-release dev user_version 7 (only threads_v2 tables) collapses to V7", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.db")
		raw, err := sqlitedb.OpenRaw(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			if _, err := raw.Exec(migrations[i].SQL); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := raw.Exec(`
CREATE TABLE threads_v2_cache (thread_id TEXT PRIMARY KEY);
CREATE TABLE threads_v2_outbox (seq INTEGER PRIMARY KEY AUTOINCREMENT);
CREATE TABLE threads_v2_blobs (sha256 TEXT PRIMARY KEY);
PRAGMA user_version = 7;
`); err != nil {
			t.Fatal(err)
		}
		raw.Close()

		st, err := openStore(path)
		if err != nil {
			t.Fatalf("openStore from dev v7: %v", err)
		}
		defer st.db.Close()

		for _, dropped := range []string{"threads_v2_cache", "threads_v2_outbox", "threads_v2_blobs"} {
			var n int
			_ = st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, dropped).Scan(&n)
			if n != 0 {
				t.Fatalf("expected %s dropped", dropped)
			}
		}
		for _, created := range []string{"sessions_cache", "sessions_outbox", "sessions_blobs", "session_files_kept"} {
			var n int
			_ = st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, created).Scan(&n)
			if n != 1 {
				t.Fatalf("expected %s created", created)
			}
		}
	})

	t.Run("pre-release dev user_version 8 (sessions_cache without session_files_kept) collapses to V7", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.db")
		raw, err := sqlitedb.OpenRaw(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			if _, err := raw.Exec(migrations[i].SQL); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := raw.Exec(sessionStoreMigrations + `; INSERT INTO sessions_cache(session_id, machine_id, archived, updated_at, session_payload) VALUES ('s8', 'm1', 0, 10, x'02'); PRAGMA user_version = 8;`); err != nil {
			t.Fatal(err)
		}
		raw.Close()

		st, err := openStore(path)
		if err != nil {
			t.Fatalf("openStore from dev v8: %v", err)
		}
		defer st.db.Close()

		var n int
		if err := st.db.QueryRow(`SELECT count(*) FROM sessions_cache WHERE session_id = 's8'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("expected sessions_cache row preserved, got %d (%v)", n, err)
		}
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_files_kept'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("expected session_files_kept created, got %d (%v)", n, err)
		}
	})

	t.Run("pre-release dev user_version 9 collapses to squashed V7", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.db")
		raw, err := sqlitedb.OpenRaw(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range migrations {
			if _, err := raw.Exec(m.SQL); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := raw.Exec(`
CREATE TABLE threads_v2_cache (thread_id TEXT PRIMARY KEY);
INSERT INTO sessions_cache(session_id, machine_id, archived, updated_at, session_payload) VALUES ('s1', 'm1', 0, 10, x'02');
INSERT INTO session_files_kept(key, session_id, payload, size, used_at) VALUES ('f1', 's1', x'03', 1, 20);
PRAGMA user_version = 9;
`); err != nil {
			t.Fatal(err)
		}
		raw.Close()

		st, err := openStore(path)
		if err != nil {
			t.Fatalf("openStore from dev v9: %v", err)
		}
		defer st.db.Close()

		var n int
		if err := st.db.QueryRow(`SELECT count(*) FROM sessions_cache WHERE session_id = 's1'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("expected sessions_cache preserved, got %d (%v)", n, err)
		}
		if err := st.db.QueryRow(`SELECT count(*) FROM session_files_kept WHERE key = 'f1'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("expected session_files_kept preserved, got %d (%v)", n, err)
		}
		hist, err := sqlitedb.History(st.db)
		if err != nil || len(hist) != 7 {
			t.Fatalf("expected 7 schema_history rows after dev v9 collapse, got %+v (%v)", hist, err)
		}
	})

	t.Run("0-byte empty owner.db initializes cleanly and corrupt owner.db errors", func(t *testing.T) {
		emptyPath := filepath.Join(t.TempDir(), "owner.db")
		_ = os.WriteFile(emptyPath, nil, 0o600)
		st, err := openStore(emptyPath)
		if err != nil {
			t.Fatalf("openStore on 0-byte owner.db: %v", err)
		}
		st.db.Close()

		corruptPath := filepath.Join(t.TempDir(), "owner.db")
		_ = os.WriteFile(corruptPath, []byte("corrupt bytes"), 0o600)
		if _, err := openStore(corruptPath); err == nil {
			t.Fatal("expected error opening corrupt owner.db")
		}
	})

	t.Run("genuine newer schema_history store and pre-history uv 10 return ErrNewer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.db")
		future := append([]sqlitedb.Migration(nil), migrations...)
		future = append(future, sqlitedb.Migration{Version: 8, Description: "future_v8", SQL: `CREATE TABLE future_tbl (id TEXT PRIMARY KEY)`})
		db, err := sqlitedb.Open(path, future)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := openStore(path); !errors.Is(err, sqlitedb.ErrNewer) {
			t.Fatalf("expected ErrNewer for future schema_history v8, got %v", err)
		}

		uv10Path := filepath.Join(t.TempDir(), "owner_uv10.db")
		raw, _ := sqlitedb.OpenRaw(uv10Path)
		_, _ = raw.Exec(`PRAGMA user_version = 10`)
		raw.Close()
		if _, err := openStore(uv10Path); !errors.Is(err, sqlitedb.ErrNewer) {
			t.Fatalf("expected ErrNewer for uv=10 owner.db, got %v", err)
		}
	})
}
