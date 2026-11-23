// Package store implements the SQLite WAL persistence layer for Hermesh v2:
// relational state tables, content-addressed blobs, idempotent command
// receipts, and per-thread monotonic event logs committed in single transactions.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

var ErrNotFound = errors.New("not found")

var migrations = []string{`
CREATE TABLE workspaces (
  project_id TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  default_base_ref TEXT NOT NULL,
  workspace_mode INTEGER NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

CREATE TABLE provider_instances (
  instance_id TEXT PRIMARY KEY,
  driver TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  payload BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE threads (
  thread_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  status INTEGER NOT NULL,
  archived INTEGER NOT NULL DEFAULT 0,
  pinned INTEGER NOT NULL DEFAULT 0,
  last_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_threads_updated ON threads(archived, pinned DESC, updated_at DESC);

CREATE TABLE provider_threads (
  provider_thread_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  instance_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_provider_threads_thread ON provider_threads(thread_id, created_at ASC);

CREATE TABLE runs (
  run_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  status INTEGER NOT NULL,
  queue_order REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_runs_thread_ordinal ON runs(thread_id, ordinal ASC);
CREATE INDEX idx_runs_thread_queue ON runs(thread_id, status, queue_order ASC);

CREATE TABLE turn_items (
  item_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  kind INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_turn_items_thread_seq ON turn_items(thread_id, seq ASC);

CREATE TABLE runtime_requests (
  request_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  status INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_runtime_requests_thread ON runtime_requests(thread_id, created_at ASC);

CREATE TABLE checkpoints (
  checkpoint_id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  run_ordinal INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX idx_checkpoints_thread ON checkpoints(thread_id, run_ordinal ASC);

CREATE TABLE thread_allow_rules (
  thread_id TEXT NOT NULL,
  rule_key TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (thread_id, rule_key)
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

CREATE TABLE thread_events (
  thread_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  occurred_at INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (thread_id, seq)
);
`, `
ALTER TABLE runs ADD COLUMN grant_ceiling INTEGER NOT NULL DEFAULT 0;
`, `
ALTER TABLE workspaces ADD COLUMN agents TEXT NOT NULL DEFAULT '';
`, `
ALTER TABLE turn_items RENAME TO turn_items_v3_legacy;
DROP INDEX IF EXISTS idx_turn_items_thread_seq;
CREATE TABLE turn_items (
  thread_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  kind INTEGER NOT NULL,
  payload BLOB NOT NULL,
  PRIMARY KEY (thread_id, item_id)
);
CREATE INDEX idx_turn_items_thread_seq ON turn_items(thread_id, seq ASC);
`}

type WorkspaceRow struct {
	ProjectID      string
	Path           string
	DefaultBaseRef string
	WorkspaceMode  int32
	Description    string
	// Agents are the coding agents the project is set up for (the machine's
	// Project.agents); empty: all.
	Agents []string
}

type Store struct {
	path     string
	blobsDir string
	writer   *sql.DB
	reader   *sql.DB
	ckpt     *sqlitedb.Checkpointer
	writeMu  sync.Mutex
}

func Open(dbPath string) (*Store, error) {
	writer, err := sqlitedb.Open(dbPath, migrations)
	if err != nil {
		return nil, err
	}
	if err := repairLegacyTurnItems(writer); err != nil {
		writer.Close()
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
	blobsDir := filepath.Join(filepath.Dir(dbPath), "blobs_v2")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		ckpt.Close()
		reader.Close()
		writer.Close()
		return nil, err
	}
	return &Store{
		path:     dbPath,
		blobsDir: blobsDir,
		writer:   writer,
		reader:   reader,
		ckpt:     ckpt,
	}, nil
}

func (s *Store) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.ckpt.Close()
	_ = s.reader.Close()
	return s.writer.Close()
}

// Tx wraps an atomic SQLite write transaction plus staged ThreadEvents.
type Tx struct {
	tx     *sql.Tx
	events []*threadv2.ThreadEvent
}

// Mutate runs fn in a serialized SQLite transaction and returns all staged
// ThreadEvents once committed so callers can fan them out to live subscribers.
func (s *Store) Mutate(fn func(tx *Tx) error) ([]*threadv2.ThreadEvent, error) {
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

func (tx *Tx) PutProviderInstance(cfg *threadv2.ProviderInstanceConfig) error {
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

func (s *Store) ProviderInstances() ([]*threadv2.ProviderInstanceConfig, error) {
	rows, err := s.reader.Query(`SELECT payload FROM provider_instances ORDER BY instance_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.ProviderInstanceConfig
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		cfg := &threadv2.ProviderInstanceConfig{}
		if err := proto.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// NextSeq increments and returns the thread's monotonic sequence counter inside tx.
func (tx *Tx) NextSeq(threadID string) (uint64, error) {
	var seq uint64
	err := tx.tx.QueryRow(`SELECT last_seq FROM threads WHERE thread_id = ?`, threadID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	seq++
	if _, err := tx.tx.Exec(`UPDATE threads SET last_seq = ? WHERE thread_id = ?`, seq, threadID); err != nil {
		return 0, err
	}
	return seq, nil
}

func (tx *Tx) AppendEvent(ev *threadv2.ThreadEvent) error {
	if ev.OccurredAt == nil {
		ev.OccurredAt = timestamppb.Now()
	}
	if ev.Seq == 0 {
		seq, err := tx.NextSeq(ev.ThreadId)
		if err != nil {
			return err
		}
		ev.Seq = seq
	}
	if ev.Thread != nil && ev.Thread.LastSeq < ev.Seq {
		ev.Thread.LastSeq = ev.Seq
	}
	b, err := proto.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO thread_events(thread_id, seq, occurred_at, payload) VALUES(?, ?, ?, ?)`,
		ev.ThreadId, ev.Seq, ev.OccurredAt.AsTime().UnixMilli(), b,
	)
	if err != nil {
		return err
	}
	tx.events = append(tx.events, proto.Clone(ev).(*threadv2.ThreadEvent))
	return nil
}

func (tx *Tx) PutThread(t *threadv2.Thread) error {
	if t.CreatedAt == nil {
		t.CreatedAt = timestamppb.Now()
	}
	t.UpdatedAt = timestamppb.Now()
	var currentSeq uint64
	_ = tx.tx.QueryRow(`SELECT last_seq FROM threads WHERE thread_id = ?`, t.ThreadId).Scan(&currentSeq)
	if currentSeq > t.LastSeq {
		t.LastSeq = currentSeq
	}
	b, err := proto.Marshal(t)
	if err != nil {
		return err
	}
	archived := 0
	if t.Status == threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED || t.ArchivedAt != nil {
		archived = 1
	}
	pinned := 0
	if t.Pinned {
		pinned = 1
	}
	_, err = tx.tx.Exec(
		`INSERT INTO threads(thread_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(thread_id) DO UPDATE SET
		   project_id=excluded.project_id, status=excluded.status, archived=excluded.archived,
		   pinned=excluded.pinned, last_seq=MAX(threads.last_seq, excluded.last_seq),
		   updated_at=excluded.updated_at, payload=excluded.payload`,
		t.ThreadId, t.ProjectId, int32(t.Status), archived, pinned, t.LastSeq,
		t.CreatedAt.AsTime().UnixMilli(), t.UpdatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) GetThread(threadID string) (*threadv2.Thread, error) {
	var b []byte
	var lastSeq uint64
	err := tx.tx.QueryRow(`SELECT payload, last_seq FROM threads WHERE thread_id = ?`, threadID).Scan(&b, &lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t := &threadv2.Thread{}
	if err := proto.Unmarshal(b, t); err != nil {
		return nil, err
	}
	t.LastSeq = lastSeq
	return t, nil
}

func (tx *Tx) DeleteThread(threadID string) error {
	for _, tbl := range []string{"thread_events", "thread_allow_rules", "checkpoints", "runtime_requests", "turn_items", "runs", "provider_threads", "threads"} {
		if _, err := tx.tx.Exec(`DELETE FROM `+tbl+` WHERE thread_id = ?`, threadID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Thread(threadID string) (*threadv2.Thread, error) {
	var b []byte
	var lastSeq uint64
	err := s.reader.QueryRow(`SELECT payload, last_seq FROM threads WHERE thread_id = ?`, threadID).Scan(&b, &lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t := &threadv2.Thread{}
	if err := proto.Unmarshal(b, t); err != nil {
		return nil, err
	}
	t.LastSeq = lastSeq
	return t, nil
}

func (s *Store) ListThreads(projectID string, includeArchived bool, limit uint32) ([]*threadv2.Thread, error) {
	q := `SELECT payload, last_seq FROM threads WHERE 1=1`
	var args []any
	if projectID != "" {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if !includeArchived {
		q += ` AND archived = 0`
	}
	q += ` ORDER BY pinned DESC, updated_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := s.reader.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.Thread
	for rows.Next() {
		var b []byte
		var seq uint64
		if err := rows.Scan(&b, &seq); err != nil {
			return nil, err
		}
		t := &threadv2.Thread{}
		if err := proto.Unmarshal(b, t); err != nil {
			return nil, err
		}
		t.LastSeq = seq
		out = append(out, t)
	}
	return out, rows.Err()
}

func (tx *Tx) PutProviderThread(pt *threadv2.ProviderThread) error {
	if pt.CreatedAt == nil {
		pt.CreatedAt = timestamppb.Now()
	}
	pt.UpdatedAt = timestamppb.Now()
	b, err := proto.Marshal(pt)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO provider_threads(provider_thread_id, thread_id, instance_id, created_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider_thread_id) DO UPDATE SET instance_id=excluded.instance_id, updated_at=excluded.updated_at, payload=excluded.payload`,
		pt.ProviderThreadId, pt.ThreadId, pt.InstanceId, pt.CreatedAt.AsTime().UnixMilli(), pt.UpdatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) GetProviderThread(ptID string) (*threadv2.ProviderThread, error) {
	var b []byte
	err := tx.tx.QueryRow(`SELECT payload FROM provider_threads WHERE provider_thread_id = ?`, ptID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	pt := &threadv2.ProviderThread{}
	return pt, proto.Unmarshal(b, pt)
}

func (s *Store) ProviderThreads(threadID string) ([]*threadv2.ProviderThread, error) {
	rows, err := s.reader.Query(`SELECT payload FROM provider_threads WHERE thread_id = ? ORDER BY created_at ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.ProviderThread
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		pt := &threadv2.ProviderThread{}
		if err := proto.Unmarshal(b, pt); err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, rows.Err()
}

func (tx *Tx) PutRun(r *threadv2.Run) error {
	if r.CreatedAt == nil {
		r.CreatedAt = timestamppb.Now()
	}
	b, err := proto.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO runs(run_id, thread_id, ordinal, status, queue_order, created_at, updated_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
		   ordinal=excluded.ordinal, status=excluded.status, queue_order=excluded.queue_order,
		   updated_at=excluded.updated_at, payload=excluded.payload`,
		r.RunId, r.ThreadId, r.Ordinal, int32(r.Status), r.QueueOrder,
		r.CreatedAt.AsTime().UnixMilli(), time.Now().UnixMilli(), b,
	)
	return err
}

// SetRunGrantCeiling records the cryptographic Grant ceiling of the principal
// that authored runID; the policy gate enforces it for that run only.
func (tx *Tx) SetRunGrantCeiling(runID string, ceiling identityv1.RuntimeMode) error {
	_, err := tx.tx.Exec(`UPDATE runs SET grant_ceiling = ? WHERE run_id = ?`, int32(ceiling), runID)
	return err
}

// Run loads one run by id.
func (s *Store) Run(runID string) (*threadv2.Run, error) {
	var b []byte
	err := s.reader.QueryRow(`SELECT payload FROM runs WHERE run_id = ?`, runID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &threadv2.Run{}
	return r, proto.Unmarshal(b, r)
}

// RunGrantCeiling returns the Grant ceiling recorded for runID (UNSPECIFIED for owner runs).
func (s *Store) RunGrantCeiling(runID string) identityv1.RuntimeMode {
	var v int32
	if err := s.reader.QueryRow(`SELECT grant_ceiling FROM runs WHERE run_id = ?`, runID).Scan(&v); err != nil {
		return identityv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED
	}
	return identityv1.RuntimeMode(v)
}

func (tx *Tx) GetRun(runID string) (*threadv2.Run, error) {
	var b []byte
	err := tx.tx.QueryRow(`SELECT payload FROM runs WHERE run_id = ?`, runID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &threadv2.Run{}
	return r, proto.Unmarshal(b, r)
}

func (tx *Tx) QueuedRuns(threadID string) ([]*threadv2.Run, error) {
	rows, err := tx.tx.Query(
		`SELECT payload FROM runs WHERE thread_id = ? AND status = ? ORDER BY queue_order ASC, created_at ASC`,
		threadID, int32(threadv2.RunStatus_RUN_STATUS_QUEUED),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.Run
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		r := &threadv2.Run{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Runs(threadID string) ([]*threadv2.Run, error) {
	rows, err := s.reader.Query(`SELECT payload FROM runs WHERE thread_id = ? ORDER BY ordinal ASC, queue_order ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.Run
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		r := &threadv2.Run{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *Tx) GetTurnItem(threadID, itemID string) (*threadv2.TurnItem, error) {
	var b []byte
	err := tx.tx.QueryRow(
		`SELECT payload FROM turn_items WHERE thread_id = ? AND item_id = ?`,
		threadID, itemID,
	).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	it := &threadv2.TurnItem{}
	return it, proto.Unmarshal(b, it)
}

func (tx *Tx) PutTurnItem(it *threadv2.TurnItem) error {
	var existingSeq uint64
	var existingPayload []byte
	if err := tx.tx.QueryRow(
		`SELECT seq, payload FROM turn_items WHERE thread_id = ? AND item_id = ?`,
		it.ThreadId, it.ItemId,
	).Scan(&existingSeq, &existingPayload); err == nil {
		if it.Seq == 0 && existingSeq > 0 {
			it.Seq = existingSeq
		}
		if it.CreatedAt == nil && len(existingPayload) > 0 {
			prev := &threadv2.TurnItem{}
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
		seq, err := tx.NextSeq(it.ThreadId)
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
		`INSERT INTO turn_items(thread_id, item_id, run_id, seq, kind, payload)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(thread_id, item_id) DO UPDATE SET
		   run_id=excluded.run_id,
		   seq=excluded.seq,
		   kind=excluded.kind,
		   payload=excluded.payload`,
		it.ThreadId, it.ItemId, it.RunId, it.Seq, int32(it.Kind), b,
	)
	return err
}

// SettleRunItems closes any streaming or running TurnItems left behind when a
// run enters a terminal state (completed, interrupted, or failed), returning
// the updated items so callers can broadcast item.updated events.
func (tx *Tx) SettleRunItems(threadID, runID, terminalStatus string) ([]*threadv2.TurnItem, error) {
	rows, err := tx.tx.Query(
		`SELECT payload FROM turn_items WHERE thread_id = ? AND run_id = ? ORDER BY seq ASC`,
		threadID, runID,
	)
	if err != nil {
		return nil, err
	}
	var pending []*threadv2.TurnItem
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		it := &threadv2.TurnItem{}
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

	var settled []*threadv2.TurnItem
	for _, it := range pending {
		it.Streaming = false
		if it.Status == "running" {
			if it.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE && terminalStatus == "failed" {
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

func isKernelUniqueItemID(id string) bool {
	for _, p := range []string{"itm_", "ckpt_", "err_", "reqitm_", "rb_", "sw_"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// repairLegacyTurnItems runs once when migrating from schema <= 3 (where
// turn_items had a global PRIMARY KEY(item_id) that caused driver-local IDs
// such as msg_1 / rsn_msg_1 to collide across threads and runs). It rebuilds
// turn_items per thread from the intact thread_events log inside a single
// transaction and drops turn_items_v3_legacy on commit.
func repairLegacyTurnItems(db *sql.DB) error {
	var exists int
	err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='turn_items_v3_legacy'`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	threadSet := map[string]bool{}
	for _, q := range []string{
		`SELECT thread_id FROM threads`,
		`SELECT DISTINCT thread_id FROM thread_events`,
		`SELECT DISTINCT thread_id FROM turn_items_v3_legacy`,
	} {
		rows, qErr := tx.Query(q)
		if qErr != nil {
			return qErr
		}
		for rows.Next() {
			var tid string
			if rows.Scan(&tid) == nil && tid != "" {
				threadSet[tid] = true
			}
		}
		rows.Close()
	}

	for threadID := range threadSet {
		runsByID := map[string]*threadv2.Run{}
		if rRows, rErr := tx.Query(`SELECT payload FROM runs WHERE thread_id = ?`, threadID); rErr == nil {
			for rRows.Next() {
				var b []byte
				if rRows.Scan(&b) == nil {
					r := &threadv2.Run{}
					if proto.Unmarshal(b, r) == nil && r.RunId != "" {
						runsByID[r.RunId] = r
					}
				}
			}
			rRows.Close()
		}

		var legacyItems []*threadv2.TurnItem
		if lRows, lErr := tx.Query(`SELECT payload FROM turn_items_v3_legacy WHERE thread_id = ? ORDER BY seq ASC`, threadID); lErr == nil {
			for lRows.Next() {
				var b []byte
				if lRows.Scan(&b) == nil {
					it := &threadv2.TurnItem{}
					if proto.Unmarshal(b, it) == nil && it.ItemId != "" {
						it.ThreadId = threadID
						legacyItems = append(legacyItems, it)
					}
				}
			}
			lRows.Close()
		}

		type rawEv struct {
			seq     uint64
			payload []byte
		}
		var evRows []rawEv
		if eRows, eErr := tx.Query(`SELECT seq, payload FROM thread_events WHERE thread_id = ? ORDER BY seq ASC`, threadID); eErr == nil {
			for eRows.Next() {
				var e rawEv
				if eRows.Scan(&e.seq, &e.payload) == nil {
					evRows = append(evRows, e)
				}
			}
			eRows.Close()
		}

		itemsByID := map[string]*threadv2.TurnItem{}
		if len(evRows) == 0 {
			for _, it := range legacyItems {
				itemsByID[it.ItemId] = it
			}
		} else {
			scopedIDs := map[string]string{}
			usedSeqs := map[uint64]bool{}
			for _, it := range legacyItems {
				if isKernelUniqueItemID(it.ItemId) {
					itemsByID[it.ItemId] = it
					if it.RunId != "" {
						scopedIDs[it.RunId+"\x00"+it.ItemId] = it.ItemId
					}
					if it.Seq > 0 {
						usedSeqs[it.Seq] = true
					}
				}
			}
			for _, raw := range evRows {
				ev := &threadv2.ThreadEvent{}
				if proto.Unmarshal(raw.payload, ev) != nil {
					continue
				}
				if ev.Item != nil && ev.Item.ItemId != "" {
					inc := proto.Clone(ev.Item).(*threadv2.TurnItem)
					inc.ThreadId = threadID
					rawID := inc.ItemId
					key := inc.RunId + "\x00" + rawID
					effID, seen := scopedIDs[key]
					if !seen {
						if prev, exists := itemsByID[rawID]; exists && prev.RunId != inc.RunId {
							effID = inc.RunId + ":" + rawID
						} else {
							effID = rawID
						}
						scopedIDs[key] = effID
					}
					inc.ItemId = effID
					if prev, exists := itemsByID[effID]; exists {
						inc.Seq = prev.Seq
						if prev.CreatedAt != nil {
							inc.CreatedAt = prev.CreatedAt
						}
						if inc.ToolName == "" {
							inc.ToolName = prev.ToolName
						}
						if inc.Command == "" {
							inc.Command = prev.Command
						}
						if inc.FilePath == "" {
							inc.FilePath = prev.FilePath
						}
						if prev.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION &&
							inc.Kind == threadv2.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL {
							inc.Kind = threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION
						}
					} else {
						if inc.Seq == 0 || inc.Seq >= raw.seq || usedSeqs[inc.Seq] {
							inc.Seq = raw.seq
						}
						usedSeqs[inc.Seq] = true
					}
					itemsByID[effID] = inc
				} else if ev.Type == "item.delta" && ev.TargetItemId != "" && ev.TextDelta != "" {
					if cur := itemsByID[ev.TargetItemId]; cur != nil {
						cur.Text += ev.TextDelta
					}
				}
				if ev.Type == "request.resolved" && ev.Request != nil {
					if reqIt := itemsByID["reqitm_"+ev.Request.RequestId]; reqIt != nil {
						reqIt.Status = "completed"
					}
				}
				if ev.Type == "thread.rolled_back" && ev.Checkpoint != nil {
					maxOrd := ev.Checkpoint.RunOrdinal
					for id, it := range itemsByID {
						if r := runsByID[it.RunId]; r != nil && r.Ordinal > maxOrd && r.Status != threadv2.RunStatus_RUN_STATUS_QUEUED {
							delete(itemsByID, id)
						}
					}
				}
			}
		}

		for id, it := range itemsByID {
			if r := runsByID[it.RunId]; r != nil {
				if r.Status == threadv2.RunStatus_RUN_STATUS_ROLLED_BACK {
					delete(itemsByID, id)
					continue
				}
				switch r.Status {
				case threadv2.RunStatus_RUN_STATUS_COMPLETED:
					if it.Streaming || it.Status == "running" {
						it.Streaming = false
						it.Status = "completed"
					}
				case threadv2.RunStatus_RUN_STATUS_INTERRUPTED, threadv2.RunStatus_RUN_STATUS_CANCELLED:
					if it.Streaming || it.Status == "running" {
						it.Streaming = false
						it.Status = "interrupted"
					}
				case threadv2.RunStatus_RUN_STATUS_FAILED:
					if it.Streaming {
						it.Streaming = false
						if it.Status == "running" {
							it.Status = "completed"
						}
					} else if it.Status == "running" {
						it.Status = "interrupted"
					}
				}
			}
			b, err := proto.Marshal(it)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO turn_items(thread_id, item_id, run_id, seq, kind, payload)
				 VALUES(?, ?, ?, ?, ?, ?)
				 ON CONFLICT(thread_id, item_id) DO UPDATE SET
				   run_id=excluded.run_id, seq=excluded.seq, kind=excluded.kind, payload=excluded.payload`,
				threadID, it.ItemId, it.RunId, it.Seq, int32(it.Kind), b,
			); err != nil {
				return err
			}
		}
	}

	if _, err := tx.Exec(`DROP TABLE turn_items_v3_legacy`); err != nil {
		return err
	}
	return tx.Commit()
}

func (tx *Tx) DeleteTurnItemsAfterRunIds(threadID string, removedRunIDs map[string]bool) error {
	for runID := range removedRunIDs {
		if _, err := tx.tx.Exec(`DELETE FROM turn_items WHERE thread_id = ? AND run_id = ?`, threadID, runID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TurnItems(threadID string) ([]*threadv2.TurnItem, error) {
	rows, err := s.reader.Query(`SELECT payload FROM turn_items WHERE thread_id = ? ORDER BY seq ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.TurnItem
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		it := &threadv2.TurnItem{}
		if err := proto.Unmarshal(b, it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (tx *Tx) PutRuntimeRequest(req *threadv2.RuntimeRequest) error {
	if req.CreatedAt == nil {
		req.CreatedAt = timestamppb.Now()
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO runtime_requests(request_id, thread_id, run_id, status, created_at, payload)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(request_id) DO UPDATE SET status=excluded.status, payload=excluded.payload`,
		req.RequestId, req.ThreadId, req.RunId, int32(req.Status), req.CreatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (tx *Tx) GetRuntimeRequest(requestID string) (*threadv2.RuntimeRequest, error) {
	var b []byte
	err := tx.tx.QueryRow(`SELECT payload FROM runtime_requests WHERE request_id = ?`, requestID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &threadv2.RuntimeRequest{}
	return r, proto.Unmarshal(b, r)
}

func (s *Store) RuntimeRequests(threadID string) ([]*threadv2.RuntimeRequest, error) {
	rows, err := s.reader.Query(`SELECT payload FROM runtime_requests WHERE thread_id = ? ORDER BY created_at ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.RuntimeRequest
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		r := &threadv2.RuntimeRequest{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *Tx) PutCheckpoint(cp *threadv2.Checkpoint) error {
	if cp.CreatedAt == nil {
		cp.CreatedAt = timestamppb.Now()
	}
	b, err := proto.Marshal(cp)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(
		`INSERT INTO checkpoints(checkpoint_id, thread_id, run_ordinal, created_at, payload)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(checkpoint_id) DO UPDATE SET payload=excluded.payload`,
		cp.CheckpointId, cp.ThreadId, cp.RunOrdinal, cp.CreatedAt.AsTime().UnixMilli(), b,
	)
	return err
}

func (s *Store) Checkpoints(threadID string) ([]*threadv2.Checkpoint, error) {
	rows, err := s.reader.Query(`SELECT payload FROM checkpoints WHERE thread_id = ? ORDER BY run_ordinal ASC, created_at ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.Checkpoint
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		cp := &threadv2.Checkpoint{}
		if err := proto.Unmarshal(b, cp); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

func (tx *Tx) AddAllowRule(threadID, ruleKey string) error {
	if ruleKey == "" {
		return nil
	}
	_, err := tx.tx.Exec(
		`INSERT OR IGNORE INTO thread_allow_rules(thread_id, rule_key, created_at) VALUES(?, ?, ?)`,
		threadID, ruleKey, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) AllowRules(threadID string) (map[string]bool, error) {
	rows, err := s.reader.Query(`SELECT rule_key FROM thread_allow_rules WHERE thread_id = ?`, threadID)
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

func (s *Store) EventsAfter(threadID string, afterSeq uint64) ([]*threadv2.ThreadEvent, error) {
	rows, err := s.reader.Query(
		`SELECT payload FROM thread_events WHERE thread_id = ? AND seq > ? ORDER BY seq ASC`,
		threadID, afterSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*threadv2.ThreadEvent
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		ev := &threadv2.ThreadEvent{}
		if err := proto.Unmarshal(b, ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) Projection(threadID string) (*threadv2.ThreadProjection, error) {
	t, err := s.Thread(threadID)
	if err != nil {
		return nil, err
	}
	runs, err := s.Runs(threadID)
	if err != nil {
		return nil, err
	}
	items, err := s.TurnItems(threadID)
	if err != nil {
		return nil, err
	}
	reqs, err := s.RuntimeRequests(threadID)
	if err != nil {
		return nil, err
	}
	ckpts, err := s.Checkpoints(threadID)
	if err != nil {
		return nil, err
	}
	pts, err := s.ProviderThreads(threadID)
	if err != nil {
		return nil, err
	}
	return &threadv2.ThreadProjection{
		Thread:          t,
		Runs:            runs,
		Items:           items,
		Requests:        reqs,
		Checkpoints:     ckpts,
		ProviderThreads: pts,
	}, nil
}

// GetReceipt checks whether an idempotent command_id already executed.
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

// PutBlob stores content-addressed attachment bytes on disk and metadata in SQLite.
func (s *Store) PutBlob(name, mimeType string, data []byte) (*threadv2.AttachmentRef, error) {
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
	return &threadv2.AttachmentRef{
		BlobSha256: digest,
		Name:       name,
		MimeType:   mimeType,
		SizeBytes:  uint64(len(data)),
	}, nil
}

func (s *Store) GetBlob(digest string) (data []byte, name, mimeType string, err error) {
	// Reject path traversal in digest.
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
