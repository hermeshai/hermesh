package machine

import (
	"bytes"
	"compress/flate"
	"database/sql"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
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
`}

// Store uses one writer connection (db) and a pool of readers (rdb), so
// lookups on the request path don't wait behind batched event writes.
type Store struct {
	db, rdb *sql.DB
	ckpt    *sqlitedb.Checkpointer
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
	if err := st.backfillUsage(); err != nil {
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
	// Agents are the coding agents that may work in it; empty: all.
	Agents []string
}

// InPlace: sessions work directly in the project directory.
func (p *Project) InPlace() bool { return p.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE }

// AllowsAgent: the coding agent may work in the project.
func (p *Project) AllowsAgent(agent string) bool {
	return len(p.Agents) == 0 || slices.Contains(p.Agents, agent)
}

// The agents column: names joined by commas ("" for all).
func joinAgents(a []string) string { return strings.Join(a, ",") }

func splitAgents(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func (s *Store) Projects() ([]Project, error) {
	rows, err := s.rdb.Query(`SELECT project_id, path, default_base_ref, workspace, description, agents FROM projects ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var agents string
		if err := rows.Scan(&p.ID, &p.Path, &p.DefaultBaseRef, &p.Workspace, &p.Description, &agents); err != nil {
			return nil, err
		}
		p.Agents = splitAgents(agents)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(id string) (*Project, error) {
	p := &Project{}
	var agents string
	err := s.rdb.QueryRow(`SELECT project_id, path, default_base_ref, workspace, description, agents FROM projects WHERE project_id=?`, id).Scan(&p.ID, &p.Path, &p.DefaultBaseRef, &p.Workspace, &p.Description, &agents)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	p.Agents = splitAgents(agents)
	return p, err
}

func (s *Store) PutProject(p Project) error {
	_, err := s.db.Exec(`INSERT INTO projects(project_id,path,default_base_ref,workspace,description,agents,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET path=excluded.path, default_base_ref=excluded.default_base_ref, workspace=excluded.workspace, description=excluded.description, agents=excluded.agents`,
		p.ID, p.Path, p.DefaultBaseRef, int(p.Workspace), p.Description, joinAgents(p.Agents), time.Now().Unix())
	return err
}

func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE project_id=?`, id)
	return err
}

// sessionRow is the persisted part of a session.
type sessionRow struct {
	ID, ProjectID, Agent string
	Status               sessionv1.SessionStatus
	CreatedBy, GrantID   string
	Model, ProviderRef   string
	// Effort and Speed are fixed when the session starts, like Model.
	Effort             string
	Speed              identityv1.AgentSpeed
	BaseCommit, Branch string
	// Worktree is the directory the agent works in: its own worktree, or
	// the project directory itself for an in-place session.
	Worktree  string
	Workspace identityv1.Workspace
	// QuestionTimeout is in seconds; -1: the default, 0: nobody answers.
	QuestionTimeout int
	Pgid            int
	Progress        *sessionv1.SessionProgress
	Error           string
	// Result is the agent's full reply in the last turn that finished.
	Result string
	// Title is the first line of the starting prompt, for lists.
	Title                string
	CreatedAt, UpdatedAt time.Time

	// Conversation is the creator's own conversation, where its news and
	// the session's messages to it go (docs/agent-messages.md §7.3).
	Conversation string

	// AgentModel and AgentEffort are what the agent said it runs with when
	// it last started, as far as it says.
	AgentModel, AgentEffort string
}

const sessionCols = `session_id, project_id, agent, status, created_by, grant_id, model, provider_ref, base_commit, branch, worktree, workspace, question_timeout, pgid, progress, error, result, title, created_at, updated_at, effort, speed, conversation, agent_model, agent_effort`

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putSession(db execer, r *sessionRow) error {
	prog, _ := proto.Marshal(r.Progress)
	r.UpdatedAt = time.Now()
	_, err := db.Exec(`INSERT INTO sessions(`+sessionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET status=excluded.status, model=excluded.model, provider_ref=excluded.provider_ref,
		base_commit=excluded.base_commit, branch=excluded.branch, worktree=excluded.worktree, pgid=excluded.pgid,
		progress=excluded.progress, error=excluded.error, result=excluded.result, title=excluded.title, updated_at=excluded.updated_at,
		agent_model=excluded.agent_model, agent_effort=excluded.agent_effort`,
		r.ID, r.ProjectID, r.Agent, int(r.Status), r.CreatedBy, r.GrantID, r.Model, r.ProviderRef,
		r.BaseCommit, r.Branch, r.Worktree, int(r.Workspace), r.QuestionTimeout, r.Pgid, prog, r.Error, r.Result, r.Title, r.CreatedAt.Unix(), r.UpdatedAt.Unix(), r.Effort, int(r.Speed), r.Conversation, r.AgentModel, r.AgentEffort)
	return err
}

func scanSession(sc interface{ Scan(...any) error }) (*sessionRow, error) {
	r := &sessionRow{Progress: &sessionv1.SessionProgress{}}
	var status, workspace, speed int
	var prog []byte
	var c, u int64
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.Agent, &status, &r.CreatedBy, &r.GrantID, &r.Model, &r.ProviderRef,
		&r.BaseCommit, &r.Branch, &r.Worktree, &workspace, &r.QuestionTimeout, &r.Pgid, &prog, &r.Error, &r.Result, &r.Title, &c, &u, &r.Effort, &speed, &r.Conversation, &r.AgentModel, &r.AgentEffort); err != nil {
		return nil, err
	}
	r.Status, r.Workspace, r.Speed = sessionv1.SessionStatus(status), identityv1.Workspace(workspace), identityv1.AgentSpeed(speed)
	r.CreatedAt, r.UpdatedAt = time.Unix(c, 0), time.Unix(u, 0)
	proto.Unmarshal(prog, r.Progress)
	return r, nil
}

func (s *Store) Session(id string) (*sessionRow, error) {
	r, err := scanSession(s.rdb.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE session_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *Store) Sessions(where string, args ...any) ([]*sessionRow, error) {
	q := `SELECT ` + sessionCols + ` FROM sessions`
	if where != "" {
		q += ` WHERE ` + where
	}
	rows, err := s.rdb.Query(q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteSession removes a session's record, events, turns and idempotency
// keys, and keeps what Usage needs to count its turns (who started it,
// where, with which agent) in deleted_sessions.
func (s *Store) DeleteSession(r *sessionRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO deleted_sessions(session_id, project_id, agent, created_by, grant_id, deleted_at) VALUES(?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.Agent, r.CreatedBy, r.GrantID, time.Now().Unix()); err != nil {
		return err
	}
	for _, table := range []string{"sessions", "session_events", "turns", "idempotency", "tool_outputs"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE session_id=?`, r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeletedSessionsSince is what is kept of the sessions deleted at or after t
// (to the second).
func (s *Store) DeletedSessionsSince(t time.Time) ([]*sessionRow, error) {
	rows, err := s.rdb.Query(`SELECT session_id, project_id, agent, created_by, grant_id FROM deleted_sessions WHERE deleted_at >= ?`, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionRow
	for rows.Next() {
		r := &sessionRow{}
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Agent, &r.CreatedBy, &r.GrantID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeletedSessions returns what is kept of the deleted sessions (see DeleteSession).
func (s *Store) DeletedSessions() ([]*sessionRow, error) {
	rows, err := s.rdb.Query(`SELECT session_id, project_id, agent, created_by, grant_id FROM deleted_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionRow
	for rows.Next() {
		r := &sessionRow{}
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Agent, &r.CreatedBy, &r.GrantID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EachSession calls f with each stored session that matches where (which
// may end with ORDER BY), until f returns false.
func (s *Store) EachSession(where string, args []any, f func(*sessionRow) bool) error {
	rows, err := s.rdb.Query(`SELECT `+sessionCols+` FROM sessions WHERE `+where, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return err
		}
		if !f(r) {
			break
		}
	}
	return rows.Err()
}

// WriteBatch persists events and the latest version of each session row in
// one transaction.
func (s *Store) WriteBatch(ops []writeOp) error {
	t0 := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	mBatchBegin.Since(t0)
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT OR REPLACE INTO session_events(session_id, seq, payload) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	rows := map[string]*sessionRow{}
	now := time.Now().Unix()
	for _, op := range ops {
		if op.turn != nil {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO turns(session_id, turn, commit_sha) VALUES(?,?,?)`,
				op.turn.sessionID, op.turn.turn, op.turn.commit); err != nil {
				return err
			}
		}
		if op.idem != nil {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO idempotency(client_id,key,session_id,created_at) VALUES(?,?,?,?)`,
				op.idem.clientID, op.idem.key, op.idem.sessionID, now); err != nil {
				return err
			}
		}
		if op.ev != nil {
			b, err := proto.Marshal(op.ev)
			if err != nil {
				return err
			}
			if _, err := ins.Exec(op.l.id, op.ev.Seq, b); err != nil {
				return err
			}
			if err := putUsage(tx, op.l.id, op.ev); err != nil {
				return err
			}
			if op.output != "" {
				if err := putToolOutput(tx, op.l.id, op.ev.GetToolResult().GetCallId(), op.output); err != nil {
					return err
				}
			}
		}
		if op.row != nil {
			rows[op.row.ID] = op.row
		}
	}
	for _, r := range rows {
		if err := putSession(tx, r); err != nil {
			return err
		}
	}
	t1 := time.Now()
	err = tx.Commit()
	mBatchCommit.Since(t1)
	return err
}

func (s *Store) AppendEvent(sessionID string, e *sessionv1.SessionEvent) error {
	b, err := proto.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO session_events(session_id, seq, payload) VALUES(?,?,?)`, sessionID, e.Seq, b)
	return err
}

func (s *Store) Events(sessionID string, afterSeq uint64, limit int) ([]*sessionv1.SessionEvent, error) {
	rows, err := s.rdb.Query(`SELECT payload FROM session_events WHERE session_id=? AND seq>? ORDER BY seq LIMIT ?`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.SessionEvent
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		e := &sessionv1.SessionEvent{}
		if err := proto.Unmarshal(b, e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventPage returns up to limit events, oldest first: those right after
// afterSeq when it is set, else those right before beforeSeq (0: the
// latest). It also says whether there are more on either side.
func (s *Store) EventPage(sessionID string, afterSeq, beforeSeq uint64, limit int) (evs []*sessionv1.SessionEvent, moreBefore, moreAfter bool, err error) {
	exists := func(cond string, seq uint64) (bool, error) {
		var one int
		err := s.rdb.QueryRow(`SELECT 1 FROM session_events WHERE session_id=? AND `+cond+` LIMIT 1`, sessionID, seq).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	if afterSeq > 0 {
		if evs, err = s.Events(sessionID, afterSeq, limit+1); err != nil {
			return nil, false, false, err
		}
		if moreAfter = len(evs) > limit; moreAfter {
			evs = evs[:limit]
		}
		moreBefore, err = exists("seq<=?", afterSeq)
		return evs, moreBefore, moreAfter, err
	}
	upper := beforeSeq
	if upper == 0 {
		upper = math.MaxInt64
	}
	rows, err := s.rdb.Query(`SELECT payload FROM session_events WHERE session_id=? AND seq<? ORDER BY seq DESC LIMIT ?`, sessionID, upper, limit+1)
	if err != nil {
		return nil, false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, false, false, err
		}
		e := &sessionv1.SessionEvent{}
		if err := proto.Unmarshal(b, e); err != nil {
			return nil, false, false, err
		}
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, false, err
	}
	if moreBefore = len(evs) > limit; moreBefore {
		evs = evs[:limit]
	}
	slices.Reverse(evs)
	if beforeSeq > 0 {
		moreAfter, err = exists("seq>=?", beforeSeq)
	}
	return evs, moreBefore, moreAfter, err
}

func (s *Store) MaxSeq(sessionID string) (uint64, error) {
	var n sql.NullInt64
	err := s.rdb.QueryRow(`SELECT max(seq) FROM session_events WHERE session_id=?`, sessionID).Scan(&n)
	return uint64(n.Int64), err
}

func (s *Store) TurnCommit(sessionID string, turn uint32) (string, error) {
	var c string
	err := s.rdb.QueryRow(`SELECT commit_sha FROM turns WHERE session_id=? AND turn=?`, sessionID, turn).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return c, err
}

func (s *Store) Idempotent(clientID, key string) (string, error) {
	var id string
	err := s.rdb.QueryRow(`SELECT session_id FROM idempotency WHERE client_id=? AND key=? AND created_at>?`,
		clientID, key, time.Now().Add(-24*time.Hour).Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
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

// putToolOutput keeps a tool's whole output, compressed, beside its event
// (docs/state-sync.md §3.5).
func putToolOutput(tx *sql.Tx, sessionID, callID, output string) error {
	var b bytes.Buffer
	z, _ := flate.NewWriter(&b, flate.DefaultCompression)
	z.Write([]byte(output))
	z.Close()
	_, err := tx.Exec(`INSERT OR REPLACE INTO tool_outputs(session_id, call_id, size, data) VALUES(?,?,?,?)`, sessionID, callID, len(output), b.Bytes())
	return err
}

// ToolOutput is a tool's whole output as kept beside its event, and its
// size, decompressed as it is read; ok is false when none is (its event
// holds it whole).
func (s *Store) ToolOutput(sessionID, callID string) (size uint64, output io.Reader, ok bool, err error) {
	var data []byte
	switch err := s.rdb.QueryRow(`SELECT size, data FROM tool_outputs WHERE session_id=? AND call_id=?`, sessionID, callID).Scan(&size, &data); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil, false, nil
	case err != nil:
		return 0, nil, false, err
	}
	return size, flate.NewReader(bytes.NewReader(data)), true, nil
}
