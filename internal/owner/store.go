package owner

import (
	"database/sql"
	"errors"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
	"github.com/hermeshai/hermesh/pkg/identity"
)

var migrations = []sqlitedb.Migration{
	{Version: 1, Description: "create_kv_and_grants", SQL: `
CREATE TABLE kv (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE grants (
  grant_id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  signed BLOB NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
`},
	{Version: 2, Description: "create_work_tables", SQL: workMigrations},
	{Version: 3, Description: "create_her_tables", SQL: herMigrations},
	{Version: 4, Description: "create_her_kept_conversations", SQL: herKeptMigrations},
	{Version: 5, Description: "create_her_deliveries", SQL: herDeliveriesMigrations},
	{Version: 6, Description: "create_delta_cursors", SQL: deltaMigrations},
	{Version: 7, Description: "create_session_caches_and_files", SQL: sessionStoreMigrations + "\n" + sessionFilesMigrations},
}

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	if err := migrateDevOwnerStore(path); err != nil {
		return nil, err
	}
	db, err := sqlitedb.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	return &store{db: db}, nil
}

// migrateDevOwnerStore collapses pre-release owner.db schemas (7..9, before
// schema_history existed) onto squashed version 7.
func migrateDevOwnerStore(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	raw, err := sqlitedb.OpenRaw(path)
	if err != nil {
		return err
	}
	defer raw.Close()
	var one int
	if err := raw.QueryRow(`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_history'`).Scan(&one); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var uv int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv < 7 || uv > 9 {
		return err
	}
	tx, err := raw.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS threads_v2_cache`,
		`DROP TABLE IF EXISTS threads_v2_outbox`,
		`DROP TABLE IF EXISTS threads_v2_blobs`,
		sessionStoreMigrations,
		sessionFilesMigrations,
		`PRAGMA user_version = 7`,
	} {
		if _, err := tx.Exec(q); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *store) get(key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func (s *store) set(key string, v []byte) error {
	_, err := s.db.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, v)
	return err
}

func (s *store) getSigned(key string) (*identityv1.Signed, error) {
	b, err := s.get(key)
	if err != nil || b == nil {
		return nil, err
	}
	out := &identityv1.Signed{}
	return out, proto.Unmarshal(b, out)
}

func (s *store) setSigned(key string, v *identityv1.Signed) error {
	b, _ := proto.Marshal(v)
	return s.set(key, b)
}

type grantRow struct {
	grant   *identityv1.Grant
	signed  *identityv1.Signed
	revoked bool
}

func (s *store) putGrant(signed *identityv1.Signed, revoked bool) error {
	g := &identityv1.Grant{}
	if err := identity.Unverified(signed, g); err != nil {
		return err
	}
	b, _ := proto.Marshal(signed)
	rv := 0
	if revoked {
		rv = 1
	}
	_, err := s.db.Exec(`INSERT INTO grants(grant_id,client_id,version,signed,revoked,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(grant_id) DO UPDATE SET version=excluded.version, signed=excluded.signed, revoked=excluded.revoked, updated_at=excluded.updated_at
		WHERE excluded.version >= grants.version`, g.GrantId, g.ClientId, g.Version, b, rv, time.Now().Unix())
	return err
}

// clearGrants forgets the grants of an owner this device left.
func (s *store) clearGrants() error {
	_, err := s.db.Exec(`DELETE FROM grants`)
	return err
}

// clearRemoteAccountData forgets remote caches, outboxes, work counts, delta
// cursors, and remote Her caches when leaving an owner, while preserving and
// re-keying any kept local Her state across local machine key rotation.
func (s *store) clearRemoteAccountData(oldLocalID, newLocalID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM grants`,
		`DELETE FROM sessions_cache`,
		`DELETE FROM sessions_outbox`,
		`DELETE FROM sessions_blobs`,
		`DELETE FROM work_machines`,
		`DELETE FROM work_keys`,
		`DELETE FROM work_sessions`,
		`DELETE FROM work_quarters`,
		`DELETE FROM work_measures`,
		`DELETE FROM delta_cursors`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for _, tbl := range []string{"her_waiting", "her_status", "her_bots", "her_conversations", "her_deliveries"} {
		if oldLocalID != "" && newLocalID != "" {
			if _, err := tx.Exec(`DELETE FROM `+tbl+` WHERE machine_id <> ? AND machine_id <> ?`, oldLocalID, newLocalID); err != nil {
				return err
			}
			if oldLocalID != newLocalID {
				if _, err := tx.Exec(`UPDATE `+tbl+` SET machine_id = ? WHERE machine_id = ?`, newLocalID, oldLocalID); err != nil {
					return err
				}
			}
		} else {
			if _, err := tx.Exec(`DELETE FROM ` + tbl); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *store) setRevoked(grantID string) error {
	_, err := s.db.Exec(`UPDATE grants SET revoked=1, updated_at=? WHERE grant_id=?`, time.Now().Unix(), grantID)
	return err
}

func (s *store) grants() ([]*grantRow, error) {
	rows, err := s.db.Query(`SELECT signed, revoked FROM grants ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*grantRow
	for rows.Next() {
		var b []byte
		var rv int
		if err := rows.Scan(&b, &rv); err != nil {
			return nil, err
		}
		r := &grantRow{signed: &identityv1.Signed{}, grant: &identityv1.Grant{}, revoked: rv == 1}
		if proto.Unmarshal(b, r.signed) != nil || identity.Unverified(r.signed, r.grant) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) grant(id string) (*grantRow, error) {
	all, err := s.grants()
	if err != nil {
		return nil, err
	}
	for _, g := range all {
		if g.grant.GrantId == id {
			return g, nil
		}
	}
	return nil, nil
}
