CREATE TABLE workspaces (
  project_id TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  default_base_ref TEXT NOT NULL,
  workspace_mode INTEGER NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
, agents TEXT NOT NULL DEFAULT '');
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
, grant_ceiling INTEGER NOT NULL DEFAULT 0);
CREATE INDEX idx_runs_thread_ordinal ON runs(thread_id, ordinal ASC);
CREATE INDEX idx_runs_thread_queue ON runs(thread_id, status, queue_order ASC);
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
