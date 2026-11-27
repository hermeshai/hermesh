// Package store implements the unified SQLite WAL persistence layer for Hermesh sessions:
// relational state tables, content-addressed blobs, idempotent command receipts,
// deletion tombstones for Watch sync, and per-session monotonic event logs.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

var ErrNotFound = errors.New("not found")

var migrations = []sqlitedb.Migration{{
	Version:     1,
	Description: "initial_sessions_schema",
	SQL: `
CREATE TABLE workspaces (
  project_id TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  default_base_ref TEXT NOT NULL,
  workspace_mode INTEGER NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  agents TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

CREATE TABLE provider_instances (
  instance_id TEXT PRIMARY KEY,
  driver TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  payload BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE sessions (
  session_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  status INTEGER NOT NULL,
  archived INTEGER NOT NULL DEFAULT 0,
  pinned INTEGER NOT NULL DEFAULT 0,
  last_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_sessions_updated ON sessions(archived, pinned DESC, updated_at DESC);

CREATE TABLE deleted_sessions (
  session_id TEXT PRIMARY KEY,
  deleted_at INTEGER NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_deleted_sessions_at ON deleted_sessions(deleted_at ASC);

CREATE TABLE provider_sessions (
  provider_session_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  instance_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_provider_sessions_session ON provider_sessions(session_id, created_at ASC);

CREATE TABLE runs (
  run_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  status INTEGER NOT NULL,
  queue_order REAL NOT NULL DEFAULT 0,
  grant_ceiling INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_runs_session_ordinal ON runs(session_id, ordinal ASC);
CREATE INDEX idx_runs_session_queue ON runs(session_id, status, queue_order ASC);

CREATE TABLE turn_items (
  session_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  kind INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (session_id, item_id)
);
CREATE INDEX idx_turn_items_session_seq ON turn_items(session_id, seq ASC);

CREATE TABLE runtime_requests (
  request_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  status INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_runtime_requests_session ON runtime_requests(session_id, created_at ASC);

CREATE TABLE checkpoints (
  checkpoint_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  run_ordinal INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_checkpoints_session ON checkpoints(session_id, run_ordinal ASC);

CREATE TABLE session_allow_rules (
  session_id TEXT NOT NULL,
  rule_key TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, rule_key)
);

CREATE TABLE blobs (
  sha256 TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE command_receipts (
  command_id TEXT PRIMARY KEY,
  method TEXT NOT NULL,
  payload BLOB NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE session_events (
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  occurred_at INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (session_id, seq)
);

CREATE TABLE background_tasks (
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  status INTEGER NOT NULL,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (session_id, task_id)
);
CREATE INDEX idx_background_tasks_session ON background_tasks(session_id, started_at ASC);

CREATE TABLE turn_image_blobs (
  session_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  asked_path TEXT NOT NULL,
  blob_sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  turn INTEGER NOT NULL,
  PRIMARY KEY (session_id, run_id, asked_path)
);
CREATE INDEX idx_turn_image_blobs_session ON turn_image_blobs(session_id);
`,
}}

type TurnImageBlob struct {
	AskedPath  string
	BlobSHA256 string
	Size       int64
	Turn       uint32
}

type DeletedSessionRecord struct {
	SessionID string
	ProjectID string
	CreatedBy string
}

type WorkspaceRow struct {
	ProjectID      string
	Path           string
	DefaultBaseRef string
	WorkspaceMode  int32
	Description    string
	Agents         []string
}

type SessionFilter struct {
	ProjectID       string
	Status          sessionv1.SessionStatus
	Since           time.Time
	IncludeArchived bool
	Before          *sessionv1.SessionCursor
	Limit           uint32
}

type SessionSummary struct {
	Running         int
	Counts          map[string]uint32
	ActiveByProject map[string]uint32
}

type Store struct {
	path     string
	blobsDir string
	writer   *sql.DB
	reader   *sql.DB
	ckpt     *sqlitedb.Checkpointer
	writeMu  sync.Mutex
	// steps: what the moves into this store did when it opened (legacy.go).
	steps []sqlitedb.StepResult

	summaryGen   atomic.Uint64
	summaryMu    sync.Mutex
	summaryAtGen uint64
	summaryCache SessionSummary

	turnItemsMu    sync.Mutex
	turnItemsGen   uint64
	turnItemsCache map[string][]*sessionv1.TurnItem
	turnItemsOrder []string
}

// Steps is what the moves into this store did when it opened: those done
// before, those that ran now and what they moved, and those that failed
// (which are tried again at the next start). The machine says the failed
// ones to its owner.
func (s *Store) Steps() []sqlitedb.StepResult { return s.steps }

func Open(dbPath string) (*Store, error) {
	dataDir := filepath.Dir(dbPath)
	if err := migrateDevSessionsV2(dbPath, dataDir); err != nil {
		return nil, err
	}
	writer, err := sqlitedb.Open(dbPath, migrations)
	if err != nil {
		return nil, err
	}
	reader, err := sqlitedb.OpenReader(dbPath)
	if err != nil {
		writer.Close()
		return nil, err
	}
	ckpt, err := sqlitedb.StartCheckpointer(dbPath, writer, 30*time.Second, nil)
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	blobsDir := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		ckpt.Close()
		reader.Close()
		writer.Close()
		return nil, err
	}
	st := &Store{
		path:     dbPath,
		blobsDir: blobsDir,
		writer:   writer,
		reader:   reader,
		ckpt:     ckpt,
	}
	// What came before this store moves in (legacy.go), each move once. One that fails is kept in the ledger, said
	// by Steps, and tried again at the next start: the store opens with what did move.
	if st.steps, err = sqlitedb.RunSteps(writer, st.legacySteps(dataDir)); err != nil {
		st.Close()
		return nil, err
	}
	st.summaryGen.Store(1)
	st.turnItemsGen = 1
	st.turnItemsCache = make(map[string][]*sessionv1.TurnItem, maxCachedTurnItemSessions)
	return st, nil
}

// migrateDevSessionsV2 transitions unreleased pre-release local stores
// (sessions_v2.db at schema 1..4 and leftover threads_v2.db) to sessions.db at
// squashed schema 1.
func migrateDevSessionsV2(dbPath, dataDir string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dataDir, "threads_v2.db"+suffix))
	}
	if filepath.Base(dbPath) != "sessions.db" {
		return nil
	}
	oldPath := filepath.Join(dataDir, "sessions_v2.db")
	oldFi, oldErr := os.Stat(oldPath)
	if errors.Is(oldErr, os.ErrNotExist) {
		return nil
	} else if oldErr != nil {
		return oldErr
	}

	if fi, err := os.Stat(dbPath); err == nil {
		walFi, walErr := os.Stat(dbPath + "-wal")
		if fi.Size() > 0 || (walErr == nil && walFi.Size() > 0) {
			return nil
		}
		// Remove empty 0-byte placeholder sessions.db if a populated sessions_v2.db is waiting.
		if oldFi.Size() > 0 {
			_ = os.Remove(dbPath)
			_ = os.Remove(dbPath + "-wal")
			_ = os.Remove(dbPath + "-shm")
		} else {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	raw, err := sqlitedb.OpenRaw(oldPath)
	if err != nil {
		return err
	}
	var uv int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		raw.Close()
		return err
	}
	var one int
	hasSessions := raw.QueryRow(`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&one) == nil
	if uv == 0 && !hasSessions {
		_ = raw.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(oldPath + suffix)
		}
		return nil
	}
	if uv > 4 {
		raw.Close()
		return fmt.Errorf("%w: sessions_v2.db is at schema %d", sqlitedb.ErrNewer, uv)
	}
	if uv < 1 || !hasSessions {
		raw.Close()
		return fmt.Errorf("%w: sessions_v2.db at schema %d is missing sessions table", sqlitedb.ErrChecksumMismatch, uv)
	}

	_, _ = raw.Exec(`ALTER TABLE deleted_sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`)
	_, _ = raw.Exec(`ALTER TABLE deleted_sessions ADD COLUMN created_by TEXT NOT NULL DEFAULT ''`)
	if _, err := raw.Exec(`
CREATE TABLE IF NOT EXISTS background_tasks (
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  status INTEGER NOT NULL,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (session_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_background_tasks_session ON background_tasks(session_id, started_at ASC);
CREATE TABLE IF NOT EXISTS turn_image_blobs (
  session_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  asked_path TEXT NOT NULL,
  blob_sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  turn INTEGER NOT NULL,
  PRIMARY KEY (session_id, run_id, asked_path)
);
CREATE INDEX IF NOT EXISTS idx_turn_image_blobs_session ON turn_image_blobs(session_id);
`); err != nil {
		raw.Close()
		return err
	}
	_, _ = raw.Exec(`DELETE FROM data_steps WHERE name = 'v2-threads'`)
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		raw.Close()
		return err
	}
	var busy, logFrames, ckptFrames int
	if err := raw.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &ckptFrames); err != nil {
		raw.Close()
		return err
	}
	if busy != 0 {
		raw.Close()
		return fmt.Errorf("sessions_v2.db is busy (wal_checkpoint busy=%d, log=%d, checkpointed=%d); close other readers before upgrading", busy, logFrames, ckptFrames)
	}
	if err := raw.Close(); err != nil {
		return err
	}
	if err := os.Rename(oldPath, dbPath); err != nil {
		return err
	}
	_ = os.Remove(oldPath + "-wal")
	_ = os.Remove(oldPath + "-shm")
	return nil
}

func (s *Store) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.ckpt.Close()
	_ = s.reader.Close()
	return s.writer.Close()
}

// Tx wraps an atomic SQLite write transaction plus staged SessionEvents.
type Tx struct {
	tx             *sql.Tx
	events         []*sessionv1.SessionEvent
	summaryDirty   bool
	turnItemsDirty map[string]struct{}
}

func (tx *Tx) markTurnItemsDirty(sessionID string) {
	if sessionID == "" {
		return
	}
	if tx.turnItemsDirty == nil {
		tx.turnItemsDirty = map[string]struct{}{}
	}
	tx.turnItemsDirty[sessionID] = struct{}{}
}

func (s *Store) Mutate(fn func(tx *Tx) error) ([]*sessionv1.SessionEvent, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	sqlTx, err := s.writer.Begin()
	if err != nil {
		return nil, err
	}
	tx := &Tx{tx: sqlTx}
	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		return nil, err
	}
	if err := sqlTx.Commit(); err != nil {
		return nil, err
	}
	if tx.summaryDirty {
		s.summaryGen.Add(1)
	}
	if len(tx.turnItemsDirty) > 0 {
		s.invalidateTurnItems(tx.turnItemsDirty)
	}
	return tx.events, nil
}

func (tx *Tx) PutWorkspace(w WorkspaceRow) error {
	_, err := tx.tx.Exec(
		`INSERT INTO workspaces(project_id, path, default_base_ref, workspace_mode, description, agents, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   path=excluded.path, default_base_ref=excluded.default_base_ref,
		   workspace_mode=excluded.workspace_mode, description=excluded.description, agents=excluded.agents, updated_at=excluded.updated_at`,
		w.ProjectID, w.Path, w.DefaultBaseRef, w.WorkspaceMode, w.Description, strings.Join(w.Agents, ","), time.Now().UnixMilli(),
	)
	return err
}

func (tx *Tx) RemoveWorkspace(projectID string) error {
	_, err := tx.tx.Exec(`DELETE FROM workspaces WHERE project_id = ?`, projectID)
	return err
}

func splitAgents(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func (s *Store) Workspace(projectID string) (WorkspaceRow, error) {
	var w WorkspaceRow
	var agents string
	err := s.reader.QueryRow(
		`SELECT project_id, path, default_base_ref, workspace_mode, description, agents FROM workspaces WHERE project_id = ?`,
		projectID,
	).Scan(&w.ProjectID, &w.Path, &w.DefaultBaseRef, &w.WorkspaceMode, &w.Description, &agents)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceRow{}, ErrNotFound
	}
	w.Agents = splitAgents(agents)
	return w, err
}

func (s *Store) Workspaces() ([]WorkspaceRow, error) {
	rows, err := s.reader.Query(`SELECT project_id, path, default_base_ref, workspace_mode, description, agents FROM workspaces ORDER BY project_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkspaceRow
	for rows.Next() {
		var w WorkspaceRow
		var agents string
		if err := rows.Scan(&w.ProjectID, &w.Path, &w.DefaultBaseRef, &w.WorkspaceMode, &w.Description, &agents); err != nil {
			return nil, err
		}
		w.Agents = splitAgents(agents)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (tx *Tx) PutProviderInstance(cfg *sessionv1.ProviderInstanceConfig) error {
	b, err := proto.Marshal(cfg)
	if err != nil {
		return err
	}
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	_, err = tx.tx.Exec(
		`INSERT INTO provider_instances(instance_id, driver, enabled, payload, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(instance_id) DO UPDATE SET driver=excluded.driver, enabled=excluded.enabled, payload=excluded.payload, updated_at=excluded.updated_at`,
		cfg.InstanceId, cfg.Driver, enabled, b, time.Now().UnixMilli(),
	)
	return err
}

func (tx *Tx) DeleteProviderInstance(instanceID string) error {
	_, err := tx.tx.Exec(`DELETE FROM provider_instances WHERE instance_id = ?`, instanceID)
	return err
}

func (s *Store) ProviderInstances() ([]*sessionv1.ProviderInstanceConfig, error) {
	rows, err := s.reader.Query(`SELECT payload FROM provider_instances ORDER BY instance_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.ProviderInstanceConfig
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		cfg := &sessionv1.ProviderInstanceConfig{}
		if err := proto.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func ensureProgressSeq(s *sessionv1.Session, seq uint64) {
	if s == nil {
		return
	}
	if s.Progress == nil {
		s.Progress = &sessionv1.SessionProgress{}
	}
	if seq > s.Progress.LastEventSeq {
		s.Progress.LastEventSeq = seq
	}
}

func (tx *Tx) NextSeq(sessionID string) (uint64, error) {
	var seq uint64
	err := tx.tx.QueryRow(`SELECT last_seq FROM sessions WHERE session_id = ?`, sessionID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	seq++
	if _, err := tx.tx.Exec(`UPDATE sessions SET last_seq = ? WHERE session_id = ?`, seq, sessionID); err != nil {
		return 0, err
	}
	return seq, nil
}

func (tx *Tx) AppendEvent(ev *sessionv1.SessionEvent) error {
	if ev.OccurredAt == nil {
		ev.OccurredAt = timestamppb.Now()
	}
	if ev.Seq == 0 {
		seq, err := tx.NextSeq(ev.SessionId)
		if err != nil {
			return err
		}
		ev.Seq = seq
	}
	if ev.Session != nil {
		ensureProgressSeq(ev.Session, ev.Seq)
	}
	b, err := proto.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO session_events(session_id, seq, occurred_at, payload) VALUES(?, ?, ?, ?)`,
		ev.SessionId, ev.Seq, ev.OccurredAt.AsTime().UnixMilli(), b,
	)
	if err != nil {
		return err
	}
	tx.events = append(tx.events, proto.Clone(ev).(*sessionv1.SessionEvent))
	return nil
}

func (tx *Tx) PutSession(s *sessionv1.Session) error {
	if s.CreatedAt == nil {
		s.CreatedAt = timestamppb.Now()
	}
	s.UpdatedAt = timestamppb.Now()
	return tx.putSessionInternal(s)
}

func (tx *Tx) PutSessionPreserveUpdatedAt(s *sessionv1.Session) error {
	if s.CreatedAt == nil {
		s.CreatedAt = timestamppb.Now()
	}
	if s.UpdatedAt == nil {
		s.UpdatedAt = s.CreatedAt
	}
	return tx.putSessionInternal(s)
}

func (tx *Tx) putSessionInternal(s *sessionv1.Session) error {
	var currentSeq uint64
	_ = tx.tx.QueryRow(`SELECT last_seq FROM sessions WHERE session_id = ?`, s.SessionId).Scan(&currentSeq)
	ensureProgressSeq(s, currentSeq)

	b, err := proto.Marshal(s)
	if err != nil {
		return err
	}
	archived := 0
	if s.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED || s.ArchivedAt != nil {
		archived = 1
	}
	pinned := 0
	if s.Pinned {
		pinned = 1
	}
	_, err = tx.tx.Exec(
		`INSERT INTO sessions(session_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   project_id=excluded.project_id, status=excluded.status, archived=excluded.archived,
		   pinned=excluded.pinned, last_seq=MAX(sessions.last_seq, excluded.last_seq),
		   updated_at=excluded.updated_at, payload=excluded.payload`,
		s.SessionId, s.ProjectId, int32(s.Status), archived, pinned, s.Progress.LastEventSeq,
		s.CreatedAt.AsTime().UnixMilli(), s.UpdatedAt.AsTime().UnixMilli(), b,
	)
	if err == nil {
		tx.summaryDirty = true
	}
	return err
}

func (tx *Tx) GetSession(sessionID string) (*sessionv1.Session, error) {
	var b []byte
	var lastSeq uint64
	err := tx.tx.QueryRow(`SELECT payload, last_seq FROM sessions WHERE session_id = ?`, sessionID).Scan(&b, &lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s := &sessionv1.Session{}
	if err := proto.Unmarshal(b, s); err != nil {
		return nil, err
	}
	ensureProgressSeq(s, lastSeq)
	return s, nil
}

func (tx *Tx) DeleteSession(sessionID string) error {
	var projectID, createdBy string
	var b []byte
	if err := tx.tx.QueryRow(`SELECT project_id, payload FROM sessions WHERE session_id = ?`, sessionID).Scan(&projectID, &b); err == nil && len(b) > 0 {
		s := &sessionv1.Session{}
		if proto.Unmarshal(b, s) == nil {
			createdBy = s.CreatedBy
			if createdBy == "" {
				createdBy = s.GetActor().GetId()
			}
		}
	}
	for _, tbl := range []string{"session_events", "session_allow_rules", "checkpoints", "turn_image_blobs", "background_tasks", "runtime_requests", "turn_items", "runs", "provider_sessions", "sessions"} {
		if _, err := tx.tx.Exec(`DELETE FROM `+tbl+` WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
	}
	_, err := tx.tx.Exec(
		`INSERT OR REPLACE INTO deleted_sessions(session_id, project_id, created_by, deleted_at) VALUES(?, ?, ?, ?)`,
		sessionID, projectID, createdBy, time.Now().UnixMilli(),
	)
	if err == nil {
		tx.summaryDirty = true
		tx.markTurnItemsDirty(sessionID)
	}
	return err
}

func (s *Store) DeletedSessionsSince(since time.Time) ([]string, error) {
	recs, err := s.DeletedSessionRecordsSince(since)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.SessionID
	}
	return out, nil
}

func (s *Store) DeletedSessionRecordsSince(since time.Time) ([]DeletedSessionRecord, error) {
	rows, err := s.reader.Query(`SELECT session_id, project_id, created_by FROM deleted_sessions WHERE deleted_at >= ? ORDER BY deleted_at ASC`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeletedSessionRecord
	for rows.Next() {
		var r DeletedSessionRecord
		if err := rows.Scan(&r.SessionID, &r.ProjectID, &r.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeletedSession(sessionID string) (*DeletedSessionRecord, error) {
	var r DeletedSessionRecord
	err := s.reader.QueryRow(`SELECT session_id, project_id, created_by FROM deleted_sessions WHERE session_id = ?`, sessionID).Scan(&r.SessionID, &r.ProjectID, &r.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) Session(sessionID string) (*sessionv1.Session, error) {
	var b []byte
	var lastSeq uint64
	err := s.reader.QueryRow(`SELECT payload, last_seq FROM sessions WHERE session_id = ?`, sessionID).Scan(&b, &lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess := &sessionv1.Session{}
	if err := proto.Unmarshal(b, sess); err != nil {
		return nil, err
	}
	ensureProgressSeq(sess, lastSeq)
	return sess, nil
}

func (s *Store) ListSessions(projectID string, includeArchived bool, limit uint32) ([]*sessionv1.Session, error) {
	return s.QuerySessions(SessionFilter{
		ProjectID:       projectID,
		IncludeArchived: includeArchived,
		Limit:           limit,
	})
}

func (s *Store) SessionSummary() (SessionSummary, error) {
	gen := s.summaryGen.Load()
	s.summaryMu.Lock()
	if s.summaryAtGen == gen && gen != 0 {
		cached := cloneSessionSummary(s.summaryCache)
		s.summaryMu.Unlock()
		return cached, nil
	}
	s.summaryMu.Unlock()

	rows, err := s.reader.Query(`SELECT project_id, status, COUNT(*) FROM sessions WHERE archived = 0 GROUP BY project_id, status`)
	if err != nil {
		return SessionSummary{Counts: map[string]uint32{}, ActiveByProject: map[string]uint32{}}, err
	}
	defer rows.Close()

	sum := SessionSummary{
		Counts:          map[string]uint32{},
		ActiveByProject: map[string]uint32{},
	}
	for rows.Next() {
		var projectID string
		var rawStatus int32
		var n uint32
		if err := rows.Scan(&projectID, &rawStatus, &n); err != nil {
			return SessionSummary{Counts: map[string]uint32{}, ActiveByProject: map[string]uint32{}}, err
		}
		switch sessionv1.SessionStatus(rawStatus) {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED:
			sum.Counts["queued"] += n
			sum.ActiveByProject[projectID] += n
		case sessionv1.SessionStatus_SESSION_STATUS_PREPARING:
			sum.Running += int(n)
			sum.Counts["preparing"] += n
			sum.ActiveByProject[projectID] += n
		case sessionv1.SessionStatus_SESSION_STATUS_RUNNING:
			sum.Running += int(n)
			sum.Counts["running"] += n
			sum.ActiveByProject[projectID] += n
		case sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			sum.Running += int(n)
			sum.Counts["waiting_input"] += n
			sum.ActiveByProject[projectID] += n
		case sessionv1.SessionStatus_SESSION_STATUS_IDLE:
			sum.Counts["idle"] += n
		case sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
			sum.Counts["crashed"] += n
		}
	}
	if err := rows.Err(); err != nil {
		return SessionSummary{Counts: map[string]uint32{}, ActiveByProject: map[string]uint32{}}, err
	}

	s.summaryMu.Lock()
	if s.summaryGen.Load() == gen {
		s.summaryAtGen = gen
		s.summaryCache = sum
	}
	s.summaryMu.Unlock()
	return cloneSessionSummary(sum), nil
}

func cloneSessionSummary(in SessionSummary) SessionSummary {
	return SessionSummary{
		Running:         in.Running,
		Counts:          maps.Clone(in.Counts),
		ActiveByProject: maps.Clone(in.ActiveByProject),
	}
}

func (s *Store) QuerySessions(f SessionFilter) ([]*sessionv1.Session, error) {
	q := `SELECT payload, last_seq FROM sessions WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.Status != sessionv1.SessionStatus_SESSION_STATUS_UNSPECIFIED {
		q += ` AND status = ?`
		args = append(args, int32(f.Status))
	} else if !f.IncludeArchived {
		q += ` AND archived = 0`
	}
	if !f.Since.IsZero() {
		q += ` AND updated_at > ?`
		args = append(args, f.Since.UnixMilli())
	}
	if f.Before != nil && f.Before.UpdatedAt != nil {
		bm := f.Before.UpdatedAt.AsTime().UnixMilli()
		q += ` AND (updated_at < ? OR (updated_at = ? AND session_id < ?))`
		args = append(args, bm, bm, f.Before.SessionId)
	}
	q += ` ORDER BY pinned DESC, updated_at DESC, session_id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.reader.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.Session
	for rows.Next() {
		var b []byte
		var seq uint64
		if err := rows.Scan(&b, &seq); err != nil {
			return nil, err
		}
		sess := &sessionv1.Session{}
		if err := proto.Unmarshal(b, sess); err != nil {
			return nil, err
		}
		ensureProgressSeq(sess, seq)
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (tx *Tx) PutProviderSession(ps *sessionv1.ProviderSession) error {
	if ps.CreatedAt == nil {
		ps.CreatedAt = timestamppb.Now()
	}
	ps.UpdatedAt = timestamppb.Now()
	b, err := proto.Marshal(ps)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO provider_sessions(provider_session_id, session_id, instance_id, created_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider_session_id) DO UPDATE SET instance_id=excluded.instance_id, updated_at=excluded.updated_at, payload=excluded.payload`,
		ps.ProviderSessionId, ps.SessionId, ps.InstanceId, ps.CreatedAt.AsTime().UnixMilli(), ps.UpdatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) GetProviderSession(psID string) (*sessionv1.ProviderSession, error) {
	var b []byte
	err := tx.tx.QueryRow(`SELECT payload FROM provider_sessions WHERE provider_session_id = ?`, psID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ps := &sessionv1.ProviderSession{}
	return ps, proto.Unmarshal(b, ps)
}

func (s *Store) ProviderSessions(sessionID string) ([]*sessionv1.ProviderSession, error) {
	rows, err := s.reader.Query(`SELECT payload FROM provider_sessions WHERE session_id = ? ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.ProviderSession
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		ps := &sessionv1.ProviderSession{}
		if err := proto.Unmarshal(b, ps); err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

func (tx *Tx) PutRun(r *sessionv1.Run) error {
	if r.CreatedAt == nil {
		r.CreatedAt = timestamppb.Now()
	}
	b, err := proto.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO runs(run_id, session_id, ordinal, status, queue_order, created_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
		   ordinal=excluded.ordinal, status=excluded.status, queue_order=excluded.queue_order,
		   updated_at=excluded.updated_at, payload=excluded.payload`,
		r.RunId, r.SessionId, r.Ordinal, int32(r.Status), r.QueueOrder,
		r.CreatedAt.AsTime().UnixMilli(), time.Now().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) SetRunGrantCeiling(runID string, ceiling identityv1.RuntimeMode) error {
	_, err := tx.tx.Exec(`UPDATE runs SET grant_ceiling = ? WHERE run_id = ?`, int32(ceiling), runID)
	return err
}

func (s *Store) Run(runID string) (*sessionv1.Run, error) {
	var b []byte
	err := s.reader.QueryRow(`SELECT payload FROM runs WHERE run_id = ?`, runID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &sessionv1.Run{}
	return r, proto.Unmarshal(b, r)
}

func (s *Store) RunGrantCeiling(runID string) identityv1.RuntimeMode {
	var v int32
	if err := s.reader.QueryRow(`SELECT grant_ceiling FROM runs WHERE run_id = ?`, runID).Scan(&v); err != nil {
		return identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED
	}
	return identityv1.RuntimeMode(v)
}

func (tx *Tx) GetRun(runID string) (*sessionv1.Run, error) {
	var b []byte
	err := tx.tx.QueryRow(`SELECT payload FROM runs WHERE run_id = ?`, runID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &sessionv1.Run{}
	return r, proto.Unmarshal(b, r)
}

func (tx *Tx) QueuedRuns(sessionID string) ([]*sessionv1.Run, error) {
	rows, err := tx.tx.Query(
		`SELECT payload FROM runs WHERE session_id = ? AND status = ? ORDER BY queue_order ASC, created_at ASC`,
		sessionID, int32(sessionv1.RunStatus_RUN_STATUS_QUEUED),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.Run
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		r := &sessionv1.Run{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Runs(sessionID string) ([]*sessionv1.Run, error) {
	rows, err := s.reader.Query(`SELECT payload FROM runs WHERE session_id = ? ORDER BY ordinal ASC, queue_order ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.Run
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		r := &sessionv1.Run{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *Tx) GetTurnItem(sessionID, itemID string) (*sessionv1.TurnItem, error) {
	var b []byte
	err := tx.tx.QueryRow(
		`SELECT payload FROM turn_items WHERE session_id = ? AND item_id = ?`,
		sessionID, itemID,
	).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	it := &sessionv1.TurnItem{}
	return it, proto.Unmarshal(b, it)
}

func (tx *Tx) PutTurnItem(it *sessionv1.TurnItem) error {
	var existingSeq uint64
	var existingPayload []byte
	if err := tx.tx.QueryRow(
		`SELECT seq, payload FROM turn_items WHERE session_id = ? AND item_id = ?`,
		it.SessionId, it.ItemId,
	).Scan(&existingSeq, &existingPayload); err == nil {
		if it.Seq == 0 && existingSeq > 0 {
			it.Seq = existingSeq
		}
		if it.CreatedAt == nil && len(existingPayload) > 0 {
			prev := &sessionv1.TurnItem{}
			if proto.Unmarshal(existingPayload, prev) == nil && prev.CreatedAt != nil {
				it.CreatedAt = prev.CreatedAt
			}
		}
	}
	if it.CreatedAt == nil {
		it.CreatedAt = timestamppb.Now()
	}
	it.UpdatedAt = timestamppb.Now()
	if it.Seq == 0 {
		seq, err := tx.NextSeq(it.SessionId)
		if err != nil {
			return err
		}
		it.Seq = seq
	}
	b, err := proto.Marshal(it)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO turn_items(session_id, item_id, run_id, seq, kind, payload)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, item_id) DO UPDATE SET
		   run_id=excluded.run_id,
		   seq=excluded.seq,
		   kind=excluded.kind,
		   payload=excluded.payload`,
		it.SessionId, it.ItemId, it.RunId, it.Seq, int32(it.Kind), b,
	)
	if err == nil {
		tx.markTurnItemsDirty(it.SessionId)
	}
	return err
}

// SettleRunItems closes any streaming or running TurnItems left behind when a
// run enters a terminal state (completed, interrupted, or failed), returning
// the updated items so callers can broadcast item.updated events. When a run
// completes normally, items backed by a still-running BackgroundTask stay in
// "running" status until that background task finishes.
func (tx *Tx) SettleRunItems(sessionID, runID, terminalStatus string) ([]*sessionv1.TurnItem, error) {
	activeBgItems := map[string]bool{}
	if terminalStatus == "completed" {
		if bRows, bErr := tx.tx.Query(
			`SELECT payload FROM background_tasks WHERE session_id = ? AND status = ?`,
			sessionID, int32(sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING),
		); bErr == nil {
			for bRows.Next() {
				var raw []byte
				if bRows.Scan(&raw) == nil {
					bt := &sessionv1.BackgroundTask{}
					if proto.Unmarshal(raw, bt) == nil && bt.ItemId != "" {
						activeBgItems[bt.ItemId] = true
					}
				}
			}
			bRows.Close()
		}
	}

	rows, err := tx.tx.Query(
		`SELECT payload FROM turn_items WHERE session_id = ? AND run_id = ? ORDER BY seq ASC`,
		sessionID, runID,
	)
	if err != nil {
		return nil, err
	}
	var pending []*sessionv1.TurnItem
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		it := &sessionv1.TurnItem{}
		if err := proto.Unmarshal(b, it); err != nil {
			rows.Close()
			return nil, err
		}
		if it.Streaming || it.Status == "running" {
			pending = append(pending, it)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var settled []*sessionv1.TurnItem
	for _, it := range pending {
		if terminalStatus == "completed" && activeBgItems[it.ItemId] {
			if !it.Streaming {
				continue
			}
			it.Streaming = false
			if err := tx.PutTurnItem(it); err != nil {
				return nil, err
			}
			settled = append(settled, it)
			continue
		}
		it.Streaming = false
		if it.Status == "running" {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && terminalStatus == "failed" {
				it.Status = "completed"
			} else {
				it.Status = terminalStatus
			}
		}
		if err := tx.PutTurnItem(it); err != nil {
			return nil, err
		}
		settled = append(settled, it)
	}
	return settled, nil
}

func (tx *Tx) RunTurnItems(sessionID, runID string) ([]*sessionv1.TurnItem, error) {
	if sessionID == "" || runID == "" {
		return nil, nil
	}
	rows, err := tx.tx.Query(
		`SELECT payload FROM turn_items WHERE session_id = ? AND run_id = ? ORDER BY seq ASC`,
		sessionID, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.TurnItem
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		it := &sessionv1.TurnItem{}
		if err := proto.Unmarshal(b, it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (tx *Tx) DeleteTurnItemsAfterRunIds(sessionID string, removedRunIDs map[string]bool) error {
	for runID := range removedRunIDs {
		if _, err := tx.tx.Exec(`DELETE FROM turn_items WHERE session_id = ? AND run_id = ?`, sessionID, runID); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`DELETE FROM background_tasks WHERE session_id = ? AND run_id = ?`, sessionID, runID); err != nil {
			return err
		}
	}
	if len(removedRunIDs) > 0 {
		tx.markTurnItemsDirty(sessionID)
	}
	return nil
}

func (tx *Tx) GetBackgroundTask(sessionID, taskID string) (*sessionv1.BackgroundTask, error) {
	var b []byte
	err := tx.tx.QueryRow(
		`SELECT payload FROM background_tasks WHERE session_id = ? AND task_id = ?`,
		sessionID, taskID,
	).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	bt := &sessionv1.BackgroundTask{}
	return bt, proto.Unmarshal(b, bt)
}

func (tx *Tx) PutBackgroundTask(bt *sessionv1.BackgroundTask) error {
	now := timestamppb.Now()
	if prev, err := tx.GetBackgroundTask(bt.SessionId, bt.TaskId); err == nil {
		if bt.StartedAt == nil && prev.StartedAt != nil {
			bt.StartedAt = prev.StartedAt
		}
		if prev.RunId != "" {
			bt.RunId = prev.RunId
		}
		if prev.ItemId != "" {
			bt.ItemId = prev.ItemId
		}
		if bt.Kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_UNSPECIFIED {
			bt.Kind = prev.Kind
		}
		if bt.Title == "" {
			bt.Title = prev.Title
		}
		if bt.Command == "" {
			bt.Command = prev.Command
		}
		if bt.OutputTail == "" && prev.OutputTail != "" {
			bt.OutputTail = prev.OutputTail
		}
		if bt.StopScope == sessionv1.BackgroundTaskStopScope_BACKGROUND_TASK_STOP_SCOPE_UNSPECIFIED {
			bt.StopScope = prev.StopScope
		}
	}
	if bt.StartedAt == nil {
		bt.StartedAt = now
	}
	bt.UpdatedAt = now
	if bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING &&
		bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED &&
		bt.CompletedAt == nil {
		bt.CompletedAt = now
	}
	b, err := proto.Marshal(bt)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO background_tasks(session_id, task_id, run_id, status, started_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, task_id) DO UPDATE SET
		   run_id=excluded.run_id, status=excluded.status, updated_at=excluded.updated_at, payload=excluded.payload`,
		bt.SessionId, bt.TaskId, bt.RunId, int32(bt.Status),
		bt.StartedAt.AsTime().UnixMilli(), bt.UpdatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) BackgroundTasks(sessionID string) ([]*sessionv1.BackgroundTask, error) {
	rows, err := tx.tx.Query(
		`SELECT payload FROM background_tasks WHERE session_id = ? ORDER BY started_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.BackgroundTask
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		bt := &sessionv1.BackgroundTask{}
		if err := proto.Unmarshal(b, bt); err != nil {
			return nil, err
		}
		out = append(out, bt)
	}
	return out, rows.Err()
}

// RefreshSessionBackgroundCounts recomputes Session.ActiveBackgroundCount and
// Session.ActiveDaemonCount from currently RUNNING background_tasks rows.
func (tx *Tx) RefreshSessionBackgroundCounts(sess *sessionv1.Session) (awaited uint32, daemon uint32, err error) {
	tasks, err := tx.BackgroundTasks(sess.SessionId)
	if err != nil {
		return 0, 0, err
	}
	for _, bt := range tasks {
		if bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
			continue
		}
		if bt.Kind == sessionv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_DAEMON || !bt.Awaited {
			daemon++
		} else {
			awaited++
		}
	}
	sess.ActiveBackgroundCount = awaited
	sess.ActiveDaemonCount = daemon
	return awaited, daemon, nil
}

// StopRunningBackgroundTasks transitions matching RUNNING background tasks on
// sessionID (or all when taskID == "") to STOPPED and settles their linked TurnItems.
func (tx *Tx) StopRunningBackgroundTasks(sessionID, taskID, summary string) ([]*sessionv1.BackgroundTask, []*sessionv1.TurnItem, error) {
	tasks, err := tx.BackgroundTasks(sessionID)
	if err != nil {
		return nil, nil, err
	}
	var stopped []*sessionv1.BackgroundTask
	var items []*sessionv1.TurnItem
	now := timestamppb.Now()
	for _, bt := range tasks {
		if bt.Status != sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING {
			continue
		}
		if taskID != "" && bt.TaskId != taskID {
			continue
		}
		bt.Status = sessionv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
		if summary != "" {
			bt.Summary = summary
		}
		bt.CompletedAt = now
		if err := tx.PutBackgroundTask(bt); err != nil {
			return nil, nil, err
		}
		stopped = append(stopped, bt)
		if bt.ItemId != "" {
			if it, iErr := tx.GetTurnItem(sessionID, bt.ItemId); iErr == nil && it.Status == "running" {
				it.Streaming = false
				it.Status = "interrupted"
				if err := tx.PutTurnItem(it); err != nil {
					return nil, nil, err
				}
				items = append(items, it)
			}
		}
	}
	return stopped, items, nil
}

func (s *Store) BackgroundTasks(sessionID string) ([]*sessionv1.BackgroundTask, error) {
	rows, err := s.reader.Query(
		`SELECT payload FROM background_tasks WHERE session_id = ? ORDER BY started_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.BackgroundTask
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		bt := &sessionv1.BackgroundTask{}
		if err := proto.Unmarshal(b, bt); err != nil {
			return nil, err
		}
		out = append(out, bt)
	}
	return out, rows.Err()
}

const maxCachedTurnItemSessions = 24

func (s *Store) invalidateTurnItems(dirty map[string]struct{}) {
	s.turnItemsMu.Lock()
	defer s.turnItemsMu.Unlock()
	s.turnItemsGen++
	for id := range dirty {
		if _, ok := s.turnItemsCache[id]; !ok {
			continue
		}
		delete(s.turnItemsCache, id)
		if idx := slices.Index(s.turnItemsOrder, id); idx >= 0 {
			s.turnItemsOrder = slices.Delete(s.turnItemsOrder, idx, idx+1)
		}
	}
}

func (s *Store) touchTurnItemsOrderLocked(sessionID string) {
	if idx := slices.Index(s.turnItemsOrder, sessionID); idx >= 0 {
		if idx == len(s.turnItemsOrder)-1 {
			return
		}
		s.turnItemsOrder = slices.Delete(s.turnItemsOrder, idx, idx+1)
	}
	s.turnItemsOrder = append(s.turnItemsOrder, sessionID)
}

func (s *Store) TurnItems(sessionID string) ([]*sessionv1.TurnItem, error) {
	s.turnItemsMu.Lock()
	if cached, ok := s.turnItemsCache[sessionID]; ok {
		s.touchTurnItemsOrderLocked(sessionID)
		out := slices.Clone(cached)
		s.turnItemsMu.Unlock()
		return out, nil
	}
	gen := s.turnItemsGen
	s.turnItemsMu.Unlock()

	rows, err := s.reader.Query(`SELECT payload FROM turn_items WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.TurnItem
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		it := &sessionv1.TurnItem{}
		if err := proto.Unmarshal(b, it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.turnItemsMu.Lock()
	if s.turnItemsGen == gen && s.turnItemsCache != nil {
		if _, exists := s.turnItemsCache[sessionID]; !exists && len(s.turnItemsOrder) >= maxCachedTurnItemSessions {
			evict := s.turnItemsOrder[0]
			s.turnItemsOrder = s.turnItemsOrder[1:]
			delete(s.turnItemsCache, evict)
		}
		s.turnItemsCache[sessionID] = out
		s.touchTurnItemsOrderLocked(sessionID)
		ret := slices.Clone(out)
		s.turnItemsMu.Unlock()
		return ret, nil
	}
	s.turnItemsMu.Unlock()
	return out, nil
}

func (tx *Tx) PutRuntimeRequest(req *sessionv1.RuntimeRequest) error {
	if req.CreatedAt == nil {
		req.CreatedAt = timestamppb.Now()
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO runtime_requests(request_id, session_id, run_id, status, created_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(request_id) DO UPDATE SET status=excluded.status, payload=excluded.payload`,
		req.RequestId, req.SessionId, req.RunId, int32(req.Status), req.CreatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) GetRuntimeRequest(requestID string) (*sessionv1.RuntimeRequest, error) {
	var b []byte
	err := tx.tx.QueryRow(`SELECT payload FROM runtime_requests WHERE request_id = ?`, requestID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &sessionv1.RuntimeRequest{}
	return r, proto.Unmarshal(b, r)
}

func (s *Store) RuntimeRequests(sessionID string) ([]*sessionv1.RuntimeRequest, error) {
	rows, err := s.reader.Query(`SELECT payload FROM runtime_requests WHERE session_id = ? ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.RuntimeRequest
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		r := &sessionv1.RuntimeRequest{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *Tx) PutCheckpoint(cp *sessionv1.Checkpoint) error {
	if cp.CreatedAt == nil {
		cp.CreatedAt = timestamppb.Now()
	}
	b, err := proto.Marshal(cp)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO checkpoints(checkpoint_id, session_id, run_ordinal, created_at, payload)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(checkpoint_id) DO UPDATE SET payload=excluded.payload`,
		cp.CheckpointId, cp.SessionId, cp.RunOrdinal, cp.CreatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (s *Store) Checkpoints(sessionID string) ([]*sessionv1.Checkpoint, error) {
	rows, err := s.reader.Query(`SELECT payload FROM checkpoints WHERE session_id = ? ORDER BY run_ordinal ASC, created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.Checkpoint
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		cp := &sessionv1.Checkpoint{}
		if err := proto.Unmarshal(b, cp); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

func (tx *Tx) AddAllowRule(sessionID, ruleKey string) error {
	if ruleKey == "" {
		return nil
	}
	_, err := tx.tx.Exec(
		`INSERT OR IGNORE INTO session_allow_rules(session_id, rule_key, created_at) VALUES(?, ?, ?)`,
		sessionID, ruleKey, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) AllowRules(sessionID string) (map[string]bool, error) {
	rows, err := s.reader.Query(`SELECT rule_key FROM session_allow_rules WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func (s *Store) EventsAfter(sessionID string, afterSeq uint64) ([]*sessionv1.SessionEvent, error) {
	rows, err := s.reader.Query(
		`SELECT payload FROM session_events WHERE session_id = ? AND seq > ? ORDER BY seq ASC`,
		sessionID, afterSeq,
	)
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
		ev := &sessionv1.SessionEvent{}
		if err := proto.Unmarshal(b, ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) Projection(sessionID string) (*sessionv1.SessionProjection, error) {
	sess, err := s.Session(sessionID)
	if err != nil {
		return nil, err
	}
	runs, err := s.Runs(sessionID)
	if err != nil {
		return nil, err
	}
	items, err := s.TurnItems(sessionID)
	if err != nil {
		return nil, err
	}
	reqs, err := s.RuntimeRequests(sessionID)
	if err != nil {
		return nil, err
	}
	ckpts, err := s.Checkpoints(sessionID)
	if err != nil {
		return nil, err
	}
	ps, err := s.ProviderSessions(sessionID)
	if err != nil {
		return nil, err
	}
	bts, err := s.BackgroundTasks(sessionID)
	if err != nil {
		return nil, err
	}
	return &sessionv1.SessionProjection{
		Session:          sess,
		Runs:             runs,
		Items:            items,
		Requests:         reqs,
		Checkpoints:      ckpts,
		ProviderSessions: ps,
		BackgroundTasks:  bts,
	}, nil
}

func (s *Store) GetReceipt(commandID, method string, dst proto.Message) (bool, error) {
	if commandID == "" {
		return false, nil
	}
	var gotMethod string
	var b []byte
	err := s.reader.QueryRow(`SELECT method, payload FROM command_receipts WHERE command_id = ?`, commandID).Scan(&gotMethod, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if gotMethod != method {
		return false, fmt.Errorf("command_id %q already used for %s", commandID, gotMethod)
	}
	return true, proto.Unmarshal(b, dst)
}

func (tx *Tx) PutReceipt(commandID, method string, res proto.Message) error {
	if commandID == "" {
		return nil
	}
	b, err := proto.Marshal(res)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT OR REPLACE INTO command_receipts(command_id, method, payload, created_at) VALUES(?, ?, ?, ?)`,
		commandID, method, b, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) PutBlob(name, mimeType string, data []byte) (*sessionv1.AttachmentRef, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	blobFile := filepath.Join(s.blobsDir, digest)
	if _, err := os.Stat(blobFile); errors.Is(err, os.ErrNotExist) {
		tmp := blobFile + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, blobFile); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
	}
	if name == "" {
		name = "attachment"
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	_, err := s.Mutate(func(tx *Tx) error {
		_, err := tx.tx.Exec(
			`INSERT OR IGNORE INTO blobs(sha256, name, mime_type, size_bytes, created_at) VALUES(?, ?, ?, ?, ?)`,
			digest, name, mimeType, len(data), time.Now().UnixMilli(),
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sessionv1.AttachmentRef{
		BlobSha256: digest,
		Name:       name,
		MimeType:   mimeType,
		SizeBytes:  uint64(len(data)),
	}, nil
}

func (s *Store) GetBlob(digest string) (data []byte, name, mimeType string, err error) {
	if len(digest) != 64 || slices.ContainsFunc([]rune(digest), func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) {
		return nil, "", "", ErrNotFound
	}
	err = s.reader.QueryRow(`SELECT name, mime_type FROM blobs WHERE sha256 = ?`, digest).Scan(&name, &mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", ErrNotFound
	}
	if err != nil {
		return nil, "", "", err
	}
	data, err = os.ReadFile(filepath.Join(s.blobsDir, digest))
	if os.IsNotExist(err) {
		return nil, "", "", ErrNotFound
	}
	return data, name, mimeType, err
}

func (s *Store) BlobPath(digest string) string {
	return filepath.Join(s.blobsDir, digest)
}

func (s *Store) PutTurnImageBlob(sessionID, runID, askedPath, digest string, size int64, turn uint32) error {
	if sessionID == "" || runID == "" || askedPath == "" || digest == "" {
		return nil
	}
	_, err := s.Mutate(func(tx *Tx) error {
		_, err := tx.tx.Exec(
			`INSERT INTO turn_image_blobs(session_id, run_id, asked_path, blob_sha256, size, turn) VALUES(?, ?, ?, ?, ?, ?)
			 ON CONFLICT(session_id, run_id, asked_path) DO UPDATE SET blob_sha256 = excluded.blob_sha256, size = excluded.size, turn = excluded.turn`,
			sessionID, runID, askedPath, digest, size, turn,
		)
		return err
	})
	return err
}

func (s *Store) TurnImageBlob(sessionID, runID, askedPath string) (TurnImageBlob, bool) {
	if sessionID == "" || askedPath == "" {
		return TurnImageBlob{}, false
	}
	var out TurnImageBlob
	out.AskedPath = askedPath
	var err error
	if runID != "" {
		err = s.reader.QueryRow(
			`SELECT blob_sha256, size, turn FROM turn_image_blobs WHERE session_id = ? AND run_id = ? AND asked_path = ?`,
			sessionID, runID, askedPath,
		).Scan(&out.BlobSHA256, &out.Size, &out.Turn)
	} else {
		err = s.reader.QueryRow(
			`SELECT blob_sha256, size, turn FROM turn_image_blobs WHERE session_id = ? AND asked_path = ? ORDER BY turn DESC LIMIT 1`,
			sessionID, askedPath,
		).Scan(&out.BlobSHA256, &out.Size, &out.Turn)
	}
	return out, err == nil && out.BlobSHA256 != ""
}

func (s *Store) SessionTurnImageDigests(sessionID string) []string {
	rows, err := s.reader.Query(`SELECT DISTINCT blob_sha256 FROM turn_image_blobs WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil && d != "" {
			out = append(out, d)
		}
	}
	return out
}

// PruneBlobs deletes candidate blob digests that are no longer referenced by
// any remaining turn_items, runs, runtime_requests, or turn_image_blobs.
func (s *Store) PruneBlobs(candidates []string) {
	if len(candidates) == 0 {
		return
	}
	seen := map[string]bool{}
	var unique []string
	for _, d := range candidates {
		if len(d) == 64 && !seen[d] {
			seen[d] = true
			unique = append(unique, d)
		}
	}
	if len(unique) == 0 {
		return
	}
	var removed []string
	_, _ = s.Mutate(func(tx *Tx) error {
		for _, d := range unique {
			raw := []byte(d)
			var inUse int
			err := tx.tx.QueryRow(`
				SELECT EXISTS(
					SELECT 1 FROM turn_items WHERE instr(payload, ?) > 0
					UNION ALL
					SELECT 1 FROM runs WHERE instr(payload, ?) > 0
					UNION ALL
					SELECT 1 FROM runtime_requests WHERE instr(payload, ?) > 0
					UNION ALL
					SELECT 1 FROM turn_image_blobs WHERE blob_sha256 = ?
				)`, raw, raw, raw, d).Scan(&inUse)
			if err == nil && inUse == 0 {
				if _, dErr := tx.tx.Exec(`DELETE FROM blobs WHERE sha256 = ?`, d); dErr == nil {
					removed = append(removed, d)
				}
			}
		}
		return nil
	})
	for _, d := range removed {
		_ = os.Remove(filepath.Join(s.blobsDir, d))
	}
}
