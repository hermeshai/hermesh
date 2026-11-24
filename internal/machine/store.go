package machine

import (
	"database/sql"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
	"github.com/hermeshai/hermesh/pkg/metrics"
)

var migrations = []string{`
CREATE TABLE kv (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE projects (
  project_id TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  default_base_ref TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE sessions (
  session_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  agent TEXT NOT NULL,
  preset INTEGER NOT NULL,
  status INTEGER NOT NULL,
  created_by TEXT NOT NULL,
  grant_id TEXT NOT NULL,
  model TEXT NOT NULL,
  provider_ref TEXT NOT NULL,
  base_commit TEXT NOT NULL,
  branch TEXT NOT NULL,
  worktree TEXT NOT NULL,
  pgid INTEGER NOT NULL,
  progress BLOB,
  error TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE session_events (
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (session_id, seq)
);
CREATE TABLE turns (
  session_id TEXT NOT NULL,
  turn INTEGER NOT NULL,
  commit_sha TEXT NOT NULL,
  PRIMARY KEY (session_id, turn)
);
CREATE TABLE idempotency (
  client_id TEXT NOT NULL,
  key TEXT NOT NULL,
  session_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (client_id, key)
);
CREATE TABLE grants_seen (grant_id TEXT PRIMARY KEY, version INTEGER NOT NULL);
`, `
ALTER TABLE projects ADD COLUMN workspace INTEGER NOT NULL DEFAULT 1;
ALTER TABLE sessions ADD COLUMN workspace INTEGER NOT NULL DEFAULT 1;
`, `
ALTER TABLE sessions ADD COLUMN question_timeout INTEGER NOT NULL DEFAULT -1;
`, `
ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT '';
`, `
ALTER TABLE sessions ADD COLUMN result TEXT NOT NULL DEFAULT '';
`, `
ALTER TABLE sessions DROP COLUMN preset;
`, `
ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT '';
`, `
CREATE INDEX sessions_by_update ON sessions(updated_at, session_id);
`, `
ALTER TABLE projects ADD COLUMN agents TEXT NOT NULL DEFAULT '';
`, `
CREATE TABLE turn_usage (
  session_id TEXT NOT NULL,
  turn INTEGER NOT NULL,
  ended_at INTEGER NOT NULL,
  input_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  PRIMARY KEY (session_id, turn)
);
CREATE INDEX turn_usage_by_end ON turn_usage(ended_at);
`, `
CREATE TABLE deleted_sessions (
  session_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  agent TEXT NOT NULL,
  created_by TEXT NOT NULL,
  grant_id TEXT NOT NULL,
  deleted_at INTEGER NOT NULL
);
`, `
CREATE TABLE tool_outputs (
  session_id TEXT NOT NULL,
  call_id TEXT NOT NULL,
  size INTEGER NOT NULL,
  data BLOB NOT NULL,
  PRIMARY KEY (session_id, call_id)
);
`, `
ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN speed INTEGER NOT NULL DEFAULT 0;
`, `
ALTER TABLE turn_usage ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;
`, `
ALTER TABLE sessions ADD COLUMN conversation TEXT NOT NULL DEFAULT '';
`, `
ALTER TABLE sessions ADD COLUMN agent_model TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN agent_effort TEXT NOT NULL DEFAULT '';
`, `
DROP INDEX turn_usage_by_end;
DROP TABLE turn_usage;
DELETE FROM kv WHERE key = 'turn_usage_backfilled';
CREATE TABLE work_keys (
  k INTEGER PRIMARY KEY,
  client TEXT NOT NULL,
  agent TEXT NOT NULL,
  project_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  UNIQUE (client, agent, project_id)
);
CREATE TABLE work_sessions (
  n INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE,
  k INTEGER NOT NULL,
  status INTEGER NOT NULL,
  failed INTEGER NOT NULL DEFAULT 0,
  turns INTEGER NOT NULL DEFAULT 0,
  turn_start INTEGER NOT NULL DEFAULT 0,
  turn_k INTEGER NOT NULL DEFAULT 0,
  seq INTEGER NOT NULL
);
CREATE INDEX work_sessions_by_seq ON work_sessions(seq);
CREATE TABLE work_quarters (
  q INTEGER NOT NULL,
  k INTEGER NOT NULL,
  n INTEGER NOT NULL,
  PRIMARY KEY (q, k, n)
) WITHOUT ROWID;
CREATE TABLE work_measures (
  q INTEGER NOT NULL,
  k INTEGER NOT NULL,
  turns INTEGER NOT NULL DEFAULT 0,
  failed_turns INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  busy_seconds INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (q, k)
) WITHOUT ROWID;
CREATE TABLE work_hours (
  h INTEGER PRIMARY KEY,
  seq INTEGER NOT NULL
);
CREATE INDEX work_hours_by_seq ON work_hours(seq);
`, `
UPDATE projects SET agents = '';
`}

// Store manages machine-level KV, projects, grant replay protection, and work
// counts. Session runtime state and events live in internal/kernel/store.
type Store struct {
	db, rdb *sql.DB
	ckpt    *sqlitedb.Checkpointer

	workMu     sync.Mutex
	workStore  []byte
	workSeq    int64
	workSeqNow atomic.Int64
	workKeys   map[workWho]int64
}

func OpenStore(path string) (*Store, error) {
	db, err := sqlitedb.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	rdb, err := sqlitedb.OpenReader(path)
	if err != nil {
		db.Close()
		return nil, err
	}
	ckpt, err := sqlitedb.StartCheckpointer(path, db, 200*time.Millisecond, mCheckpoint.Observe)
	if err != nil {
		rdb.Close()
		db.Close()
		return nil, err
	}
	walOnce.Do(func() {
		metrics.Default.GaugeFunc("hermesh_machine_wal_bytes", func() int64 {
			st, err := os.Stat(path + "-wal")
			if err != nil {
				return 0
			}
			return st.Size()
		})
	})
	st := &Store{db: db, rdb: rdb, ckpt: ckpt}
	if err := st.openWork(); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

var walOnce sync.Once

var mCheckpoint = metrics.Default.Histogram("hermesh_machine_wal_checkpoint_seconds")

func (s *Store) Close() error {
	s.ckpt.Close()
	s.rdb.Close()
	return s.db.Close()
}

func (s *Store) Get(key string) ([]byte, error) {
	var v []byte
	err := s.rdb.QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func (s *Store) Set(key string, v []byte) error {
	_, err := s.db.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, v)
	return err
}

func (s *Store) GetProto(key string, m proto.Message) (bool, error) {
	b, err := s.Get(key)
	if err != nil || b == nil {
		return false, err
	}
	return true, proto.Unmarshal(b, m)
}

func (s *Store) SetProto(key string, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.Set(key, b)
}

type Project struct {
	ID             string
	Path           string
	DefaultBaseRef string
	Workspace      identityv1.Workspace
	Description    string
}

func (p *Project) InPlace() bool { return p.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE }

func (s *Store) Projects() ([]Project, error) {
	rows, err := s.rdb.Query(`SELECT project_id, path, default_base_ref, workspace, description FROM projects ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Path, &p.DefaultBaseRef, &p.Workspace, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(id string) (*Project, error) {
	p := &Project{}
	err := s.rdb.QueryRow(`SELECT project_id, path, default_base_ref, workspace, description FROM projects WHERE project_id=?`, id).Scan(&p.ID, &p.Path, &p.DefaultBaseRef, &p.Workspace, &p.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (s *Store) PutProject(p Project) error {
	_, err := s.db.Exec(`INSERT INTO projects(project_id,path,default_base_ref,workspace,description,created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET path=excluded.path, default_base_ref=excluded.default_base_ref, workspace=excluded.workspace, description=excluded.description`,
		p.ID, p.Path, p.DefaultBaseRef, int(p.Workspace), p.Description, time.Now().Unix())
	return err
}

func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE project_id=?`, id)
	return err
}

// SeeGrantVersion records v and returns the highest version seen.
func (s *Store) SeeGrantVersion(grantID string, v uint64) (uint64, error) {
	if _, err := s.db.Exec(`INSERT INTO grants_seen(grant_id,version) VALUES(?,?)
		ON CONFLICT(grant_id) DO UPDATE SET version=max(version, excluded.version)`, grantID, v); err != nil {
		return 0, err
	}
	var max uint64
	err := s.db.QueryRow(`SELECT version FROM grants_seen WHERE grant_id=?`, grantID).Scan(&max)
	return max, err
}
