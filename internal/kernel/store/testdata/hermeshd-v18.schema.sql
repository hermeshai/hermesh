CREATE TABLE kv (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE projects (
  project_id TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  default_base_ref TEXT NOT NULL,
  created_at INTEGER NOT NULL
, workspace INTEGER NOT NULL DEFAULT 1, description TEXT NOT NULL DEFAULT '', agents TEXT NOT NULL DEFAULT '');
CREATE TABLE sessions (
  session_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  agent TEXT NOT NULL,
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
, workspace INTEGER NOT NULL DEFAULT 1, question_timeout INTEGER NOT NULL DEFAULT -1, result TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '', speed INTEGER NOT NULL DEFAULT 0, conversation TEXT NOT NULL DEFAULT '', agent_model TEXT NOT NULL DEFAULT '', agent_effort TEXT NOT NULL DEFAULT '');
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
CREATE INDEX sessions_by_update ON sessions(updated_at, session_id);
CREATE TABLE deleted_sessions (
  session_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  agent TEXT NOT NULL,
  created_by TEXT NOT NULL,
  grant_id TEXT NOT NULL,
  deleted_at INTEGER NOT NULL
);
CREATE TABLE tool_outputs (
  session_id TEXT NOT NULL,
  call_id TEXT NOT NULL,
  size INTEGER NOT NULL,
  data BLOB NOT NULL,
  PRIMARY KEY (session_id, call_id)
);
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
