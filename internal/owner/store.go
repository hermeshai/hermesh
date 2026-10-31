package owner

import (
	"database/sql"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
	"github.com/hermeshai/hermesh/pkg/identity"
)

var migrations = []string{`
CREATE TABLE kv (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE grants (
  grant_id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  signed BLOB NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
`, workMigrations}

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	return &store{db: db}, nil
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
