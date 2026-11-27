package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

// The store sessions.db took over from on v0.1.9 machines: hermeshd.db at
// schema 18 (testdata/hermeshd-v18.schema.sql, no rows), with rows written as
// the old Hermesh wrote them in the old protobuf messages.

// oldMessage is a message of the old definitions, from its JSON.
func oldMessage(t *testing.T, name, json string) []byte {
	t.Helper()
	files, err := legacyFiles()
	if err != nil {
		t.Fatal(err)
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		t.Fatal(err)
	}
	m := dynamicpb.NewMessage(d.(protoreflect.MessageDescriptor))
	if err := protojson.Unmarshal([]byte(json), m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// oldStore makes an old store in dir from its schema in testdata.
func oldStore(t *testing.T, dir, file, schema string, version int) *sql.DB {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", schema))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.OpenRaw(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b) + "; PRAGMA user_version = " + string(rune('0'+version/10)) + string(rune('0'+version%10))); err != nil {
		t.Fatalf("%s: %v", schema, err)
	}
	return db
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// v1Machine is a machine's hermeshd.db with three V1 sessions: one Her sent
// work to by a message, with a tool and its result, and finished; one of the
// owner's whose turn failed and that is archived; one cut off at work.
func v1Machine(t *testing.T, dir string) {
	t.Helper()
	db := oldStore(t, dir, "hermeshd.db", "hermeshd-v18.schema.sql", 18)
	defer db.Close()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	session := func(id, agent string, status int, by, title, result, errText string, progress string) {
		exec(t, db, `INSERT INTO sessions(session_id, project_id, agent, status, created_by, grant_id, model, provider_ref, base_commit, branch, worktree, pgid, progress, error,
			created_at, updated_at, workspace, question_timeout, result, title, effort, speed, conversation, agent_model, agent_effort)
			VALUES(?, 'app', ?, ?, ?, 'g1', '', 'native-1', 'abc', ?, '/w', 0, ?, ?, ?, ?, 1, -1, ?, ?, '', 0, 'inbox', 'opus', 'high')`,
			id, agent, status, by, "hermesh/"+id, oldMessage(t, "hermesh.session.v1.SessionProgress", progress), errText, at.Unix(), at.Add(time.Hour).Unix(), result, title)
	}
	n := map[string]int{}
	event := func(id, json string) {
		n[id]++
		ts := at.Add(time.Duration(n[id]) * time.Minute).Format(time.RFC3339)
		json = `{"seq":"` + string(rune('0'+n[id])) + `","ts":"` + ts + `",` + json[1:]
		exec(t, db, `INSERT INTO session_events(session_id, seq, payload) VALUES(?, ?, ?)`, id, n[id], oldMessage(t, "hermesh.session.v1.SessionEvent", json))
	}
	session("s_done", "claude", 4, "c_her", "Fix the login form", "Fixed it.", "", `{"turn_count":1,"last_message_excerpt":"Fixed it.","phase":"thinking","current_tool":"Bash","usage":{"input_tokens":"900","output_tokens":"100"}}`)
	// The message comes, then the turn it starts.
	event("s_done", `{"message_received":{"message_id":"m1","from":"her@x","from_name":"Her","text":"Fix the login form","delivered":"inbox"}}`)
	event("s_done", `{"turn_started":{"turn":1,"asker":"c_her"}}`)
	event("s_done", `{"tool_call":{"call_id":"t1","kind":"execute","name":"Bash","summary":"go test ./..."}}`)
	event("s_done", `{"tool_result":{"call_id":"t1","summary":"ok","truncated":true}}`)
	event("s_done", `{"agent_message":{"text":"Fixed it."}}`)
	event("s_done", `{"message_sent":{"message_id":"m2","to":"her@x","to_name":"Her","text":"Done","in_reply_to":"m1"}}`)
	event("s_done", `{"turn_completed":{"turn":1,"result":"Fixed it."}}`)

	session("s_failed", "codex", 5, "owner", "Upgrade Astro", "", "rate limited", `{"turn_count":2}`)
	event("s_failed", `{"turn_started":{"turn":1}}`)
	event("s_failed", `{"user_message":{"text":"Upgrade Astro to 5"}}`)
	event("s_failed", `{"tool_call":{"call_id":"t1","name":"Edit","summary":"package.json","decision":"deny","reason":"outside the project"}}`)
	event("s_failed", `{"error":{"message":"rate limited"}}`)
	event("s_failed", `{"turn_completed":{"turn":1,"result":"rate limited","is_error":true,"failure":{"kind":"FAILURE_KIND_RATE_LIMIT","message":"rate limited","retryable":true}}}`)
	event("s_failed", `{"turn_started":{"turn":2,"asker":"c_bot"}}`)
	event("s_failed", `{"user_message":{"text":"again"}}`)
	event("s_failed", `{"turn_completed":{"turn":2,"result":"done"}}`)

	session("s_cut", "claude", 3, "c_her", "Cut off at work", "", "", `{"turn_count":1}`)
	event("s_cut", `{"turn_started":{"turn":1,"asker":"c_her"}}`)
	event("s_cut", `{"user_message":{"text":"go"}}`)
	event("s_cut", `{"agent_message":{"text":"On it"}}`)
}

func items(t *testing.T, st *Store, id string) []*sessionv1.TurnItem {
	t.Helper()
	rows, err := st.reader.Query(`SELECT payload FROM turn_items WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []*sessionv1.TurnItem
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		it := &sessionv1.TurnItem{}
		if err := proto.Unmarshal(b, it); err != nil {
			t.Fatal(err)
		}
		out = append(out, it)
	}
	return out
}

func runsOf(t *testing.T, st *Store, id string) []*sessionv1.Run {
	t.Helper()
	rows, err := st.reader.Query(`SELECT payload FROM runs WHERE session_id = ? ORDER BY ordinal`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []*sessionv1.Run
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		r := &sessionv1.Run{}
		if err := proto.Unmarshal(b, r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func step(t *testing.T, st *Store, name string) sqlitedb.StepResult {
	t.Helper()
	for _, r := range st.Steps() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no step %s in %+v", name, st.Steps())
	return sqlitedb.StepResult{}
}

// A machine that ran the V1 sessions opens this store: each session is here
// as it stood, nothing of it at work, with its conversation from its log.
// The old store is as it was, and the next start moves nothing again.
func TestV1SessionsMoveInWithTheirConversations(t *testing.T) {
	dir := t.TempDir()
	v1Machine(t, dir)
	before, _ := os.ReadFile(filepath.Join(dir, "hermeshd.db"))
	st, err := Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	if r := step(t, st, "v1-sessions"); !r.Ran || r.Moved != 3 || r.Err != nil {
		t.Fatalf("the move: %+v", r)
	}

	done, err := st.Session("s_done")
	if err != nil {
		t.Fatal(err)
	}
	if done.Title != "Fix the login form" || done.Agent != "claude" || done.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || done.CreatedBy != "c_her" ||
		done.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT || done.Result != "Fixed it." || done.Branch != "hermesh/s_done" ||
		done.Workspace != identityv1.Workspace(1) || done.ProviderSessionRef != "native-1" || done.CreatedAs != "inbox" ||
		done.Model != "opus" || done.Effort != "high" || done.ModelSelection.GetInstanceId() != "claude" || done.ModelSelection.GetModel() != "opus" {
		t.Fatalf("the session: %v", done)
	}
	if !done.CreatedAt.AsTime().Equal(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)) || !done.UpdatedAt.AsTime().Equal(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("its times (the old store's are seconds): %v, %v", done.CreatedAt.AsTime(), done.UpdatedAt.AsTime())
	}
	if p := done.Progress; p.TurnCount != 1 || p.LastMessageExcerpt != "Fixed it." || p.Usage.GetInputTokens() != 900 || p.Phase != "" || p.CurrentTool != "" || p.LastEventSeq != 6 {
		t.Fatalf("its progress: %v", p)
	}
	runs := runsOf(t, st, "s_done")
	if len(runs) != 1 || runs[0].Ordinal != 1 || runs[0].Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED || runs[0].Prompt != "Fix the login form" ||
		runs[0].Author.GetId() != "c_her" || runs[0].CompletedAt == nil {
		t.Fatalf("its runs: %v", runs)
	}
	its := items(t, st, "s_done")
	var kinds []string
	for _, it := range its {
		kinds = append(kinds, strings.TrimPrefix(it.Kind.String(), "TURN_ITEM_KIND_"))
		if it.RunId != runs[0].RunId || it.Status != "completed" || it.CreatedAt == nil {
			t.Fatalf("an item: %v", it)
		}
	}
	if strings.Join(kinds, " ") != "MESSAGE_RECEIVED TOOL_CALL ASSISTANT_MESSAGE MESSAGE_SENT" {
		t.Fatalf("its items: %v", kinds)
	}
	if m := its[0]; m.Text != "Fix the login form" || m.MailName != "Her" || m.MailAddress != "her@x" || m.MailMessageId != "m1" || m.MailDelivered != "inbox" {
		t.Fatalf("the message that came: %v", m)
	}
	if tool := its[1]; tool.ToolName != "Bash" || tool.Command != "go test ./..." || tool.Output != "ok" || !tool.OutputTruncated {
		t.Fatalf("the tool and its result, one item: %v", tool)
	}
	if m := its[3]; m.Text != "Done" || m.MailInReplyTo != "m1" || m.MailName != "Her" {
		t.Fatalf("the message sent: %v", m)
	}

	failed, _ := st.Session("s_failed")
	if failed.Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED || failed.Error != "rate limited" || failed.CreatedBy != "owner" || failed.Actor.GetKind() != sessionv1.ActorKind_ACTOR_KIND_OWNER {
		t.Fatalf("the archived one: %v", failed)
	}
	runs = runsOf(t, st, "s_failed")
	if len(runs) != 2 || runs[0].Status != sessionv1.RunStatus_RUN_STATUS_FAILED || runs[0].Failure.GetKind() != sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT ||
		!runs[0].Failure.GetRetryable() || runs[0].Prompt != "Upgrade Astro to 5" || runs[0].Author.GetId() != "owner" ||
		runs[1].Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED || runs[1].Author.GetId() != "c_bot" || runs[1].Prompt != "again" {
		t.Fatalf("its runs: %v", runs)
	}
	its = items(t, st, "s_failed")
	if len(its) != 4 || its[1].Status != "failed" || its[1].Output != "outside the project" || its[2].Kind != sessionv1.TurnItemKind_TURN_ITEM_KIND_ERROR || its[2].Text != "rate limited" ||
		its[3].RunId != runs[1].RunId {
		t.Fatalf("its items: %v", its)
	}

	cut, _ := st.Session("s_cut")
	if runs = runsOf(t, st, "s_cut"); cut.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || len(runs) != 1 || runs[0].Status != sessionv1.RunStatus_RUN_STATUS_INTERRUPTED {
		t.Fatalf("the one cut off: %v, %v", cut, runs)
	}
	st.Close()

	// The old store is not touched, and the next start runs nothing again: what changed here since stays.
	if after, _ := os.ReadFile(filepath.Join(dir, "hermeshd.db")); string(after) != string(before) {
		t.Fatal("the old store was written")
	}
	st, err = Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r := step(t, st, "v1-sessions"); r.Ran || r.Moved != 3 || r.Err != nil {
		t.Fatalf("the next start: %+v", r)
	}
	if n := len(items(t, st, "s_done")); n != 4 {
		t.Fatalf("moved twice: %d items", n)
	}
}

func TestAMoveThatFailsIsSaidKeptAndTriedAgain(t *testing.T) {
	dir := t.TempDir()
	v1Machine(t, dir)
	old, _ := sqlitedb.OpenRaw(filepath.Join(dir, "hermeshd.db"))
	exec(t, old, `UPDATE session_events SET payload = x'ffffffff' WHERE session_id = 's_failed' AND seq = 3`)
	st, err := Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("the store did not open: %v", err)
	}
	r := step(t, st, "v1-sessions")
	if r.Err == nil || !strings.Contains(r.Err.Error(), "s_failed") || !strings.Contains(r.Err.Error(), "event 3") || r.Moved != 1 {
		t.Fatalf("the move that failed says which session and event, and what it moved before: %+v", r)
	}
	if _, err := st.Session("s_done"); err != nil {
		t.Fatalf("what moved before the failure is here: %v", err)
	}
	if _, err := st.Session("s_failed"); err == nil || len(runsOf(t, st, "s_failed")) != 0 {
		t.Fatal("a session is here in part")
	}
	var said string
	var attempts int
	st.reader.QueryRow(`SELECT error, attempts FROM data_steps WHERE name = 'v1-sessions'`).Scan(&said, &attempts)
	if !strings.Contains(said, "s_failed") || attempts != 1 {
		t.Fatalf("the ledger: %q, %d attempts", said, attempts)
	}
	st.Close()

	// Mended: the next start moves the two that were left, and the one that was here is as it was.
	exec(t, old, `DELETE FROM session_events WHERE session_id = 's_failed' AND seq = 3`)
	old.Close()
	st, err = Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r := step(t, st, "v1-sessions"); !r.Ran || r.Err != nil || r.Moved != 2 {
		t.Fatalf("mended: %+v", r)
	}
	if n := len(items(t, st, "s_done")); n != 4 {
		t.Fatalf("moved twice: %d items", n)
	}
	if _, err := st.Session("s_cut"); err != nil {
		t.Fatal(err)
	}
}

func TestNoOldStoreIsNothingToMove(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, r := range st.Steps() {
		if !r.Ran || r.Moved != 0 || r.Err != nil {
			t.Fatalf("%+v", r)
		}
	}
}

func TestDevSessionsV2StoreTransitionsToSessionsDB(t *testing.T) {
	dir := t.TempDir()
	// Create a pre-release sessions_v2.db at schema 4 (with active WAL and dead threads_v2.db).
	if err := os.WriteFile(filepath.Join(dir, "threads_v2.db"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "threads_v2.db-wal"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := sqlitedb.OpenRaw(filepath.Join(dir, "sessions_v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	exec(t, raw, migrations[0].SQL)
	exec(t, raw, `CREATE TABLE data_steps (name TEXT PRIMARY KEY, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0, moved INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '')`)
	exec(t, raw, `INSERT INTO data_steps VALUES ('v1-sessions', 1, 2, 235, 1, ''), ('v2-threads', 2, 3, 3, 1, '')`)
	sessBytes, err := proto.Marshal(&sessionv1.Session{SessionId: "s_keep_39", ProjectId: "app", Title: "Recent session"})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, raw, `INSERT INTO sessions(session_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload) VALUES ('s_keep_39', 'app', 1, 0, 0, 0, 100, 200, ?)`, sessBytes)
	exec(t, raw, `PRAGMA user_version = 4`)
	raw.Close()

	st, err := Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("Open(sessions.db) after dev v2 transition: %v", err)
	}
	defer st.Close()

	got, err := st.Session("s_keep_39")
	if err != nil || got.Title != "Recent session" {
		t.Fatalf("expected s_keep_39 preserved across rename, got %+v (%v)", got, err)
	}
	for _, gone := range []string{"sessions_v2.db", "sessions_v2.db-wal", "threads_v2.db", "threads_v2.db-wal"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", gone, err)
		}
	}
	hist, err := sqlitedb.History(st.writer)
	if err != nil || len(hist) != 1 || hist[0].Version != 1 || hist[0].Description != "initial_sessions_schema" {
		t.Fatalf("unexpected schema_history after dev transition: %+v (%v)", hist, err)
	}
	if r := step(t, st, "v1-sessions"); r.Ran || r.Moved != 235 {
		t.Fatalf("expected existing v1-sessions ledger entry preserved without re-running, got %+v", r)
	}
}

func TestDevSessionsV2EdgeCases(t *testing.T) {
	t.Run("empty 0-byte sessions_v2.db is cleaned up and sessions.db initializes full V1", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "sessions_v2.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Open(filepath.Join(dir, "sessions.db"))
		if err != nil {
			t.Fatalf("Open with 0-byte sessions_v2.db: %v", err)
		}
		defer st.Close()
		if _, err := st.Mutate(func(tx *Tx) error {
			return tx.PutSession(&sessionv1.Session{SessionId: "s_new", ProjectId: "app"})
		}); err != nil {
			t.Fatalf("expected full V1 schema after cleaning 0-byte sessions_v2.db: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "sessions_v2.db")); !os.IsNotExist(err) {
			t.Fatalf("expected 0-byte sessions_v2.db removed, got %v", err)
		}
	})

	t.Run("0-byte placeholder sessions.db does not orphan populated sessions_v2.db", func(t *testing.T) {
		dir := t.TempDir()
		raw, err := sqlitedb.OpenRaw(filepath.Join(dir, "sessions_v2.db"))
		if err != nil {
			t.Fatal(err)
		}
		exec(t, raw, migrations[0].SQL)
		sessBytes, _ := proto.Marshal(&sessionv1.Session{SessionId: "s_saved", ProjectId: "app", Title: "Saved"})
		exec(t, raw, `INSERT INTO sessions(session_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload) VALUES ('s_saved', 'app', 1, 0, 0, 0, 1, 2, ?)`, sessBytes)
		exec(t, raw, `PRAGMA user_version = 4`)
		raw.Close()

		// Create an empty 0-byte sessions.db alongside populated sessions_v2.db.
		if err := os.WriteFile(filepath.Join(dir, "sessions.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Open(filepath.Join(dir, "sessions.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer st.Close()
		if got, err := st.Session("s_saved"); err != nil || got.Title != "Saved" {
			t.Fatalf("expected s_saved recovered from sessions_v2.db, got %+v (%v)", got, err)
		}
	})

	t.Run("populated sessions.db wins if stray sessions_v2.db also exists", func(t *testing.T) {
		dir := t.TempDir()
		st1, err := Open(filepath.Join(dir, "sessions.db"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = st1.Mutate(func(tx *Tx) error {
			return tx.PutSession(&sessionv1.Session{SessionId: "s_primary", ProjectId: "app"})
		})
		st1.Close()

		_ = os.WriteFile(filepath.Join(dir, "sessions_v2.db"), []byte("stray"), 0o600)
		st2, err := Open(filepath.Join(dir, "sessions.db"))
		if err != nil {
			t.Fatalf("re-open sessions.db with stray sessions_v2.db: %v", err)
		}
		defer st2.Close()
		if _, err := st2.Session("s_primary"); err != nil {
			t.Fatalf("expected s_primary intact: %v", err)
		}
	})

	t.Run("early schema-1 sessions_v2.db backfills v2..v4 tables and uncheckpointed WAL frames", func(t *testing.T) {
		dir := t.TempDir()
		oldPath := filepath.Join(dir, "sessions_v2.db")
		raw, err := sqlitedb.OpenRaw(oldPath)
		if err != nil {
			t.Fatal(err)
		}
		// Old schema 1: deleted_sessions had NO project_id/created_by, and background_tasks / turn_image_blobs did not exist.
		exec(t, raw, `
CREATE TABLE workspaces (project_id TEXT PRIMARY KEY, path TEXT NOT NULL, default_base_ref TEXT NOT NULL, workspace_mode INTEGER NOT NULL, description TEXT NOT NULL DEFAULT '', agents TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL);
CREATE TABLE provider_instances (instance_id TEXT PRIMARY KEY, driver TEXT NOT NULL, enabled INTEGER NOT NULL, payload BLOB NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE sessions (session_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, status INTEGER NOT NULL, archived INTEGER NOT NULL DEFAULT 0, pinned INTEGER NOT NULL DEFAULT 0, last_seq INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE deleted_sessions (session_id TEXT PRIMARY KEY, deleted_at INTEGER NOT NULL);
CREATE TABLE provider_sessions (provider_session_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, instance_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE runs (run_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, ordinal INTEGER NOT NULL, status INTEGER NOT NULL, queue_order REAL NOT NULL DEFAULT 0, grant_ceiling INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE turn_items (session_id TEXT NOT NULL, item_id TEXT NOT NULL, run_id TEXT NOT NULL, seq INTEGER NOT NULL, kind INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY (session_id, item_id));
CREATE TABLE runtime_requests (request_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, run_id TEXT NOT NULL, status INTEGER NOT NULL, created_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE checkpoints (checkpoint_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, run_ordinal INTEGER NOT NULL, created_at INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE session_allow_rules (session_id TEXT NOT NULL, rule_key TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (session_id, rule_key));
CREATE TABLE blobs (sha256 TEXT PRIMARY KEY, name TEXT NOT NULL, mime_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE command_receipts (command_id TEXT PRIMARY KEY, method TEXT NOT NULL, payload BLOB NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE session_events (session_id TEXT NOT NULL, seq INTEGER NOT NULL, occurred_at INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY (session_id, seq));
PRAGMA wal_autocheckpoint = 0;
PRAGMA user_version = 1;
`)
		sessBytes, _ := proto.Marshal(&sessionv1.Session{SessionId: "s_wal_only", ProjectId: "app", CreatedBy: "owner"})
		exec(t, raw, `INSERT INTO sessions(session_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload) VALUES ('s_wal_only', 'app', 1, 0, 0, 0, 10, 20, ?)`, sessBytes)
		raw.Close()

		st, err := Open(filepath.Join(dir, "sessions.db"))
		if err != nil {
			t.Fatalf("Open from schema-1 sessions_v2.db: %v", err)
		}
		defer st.Close()
		if _, err := st.Session("s_wal_only"); err != nil {
			t.Fatalf("expected WAL row s_wal_only preserved: %v", err)
		}
		// Verify backfilled deleted_sessions columns and turn_image_blobs table work!
		if _, err := st.Mutate(func(tx *Tx) error { return tx.DeleteSession("s_wal_only") }); err != nil {
			t.Fatalf("DeleteSession failed on backfilled deleted_sessions columns: %v", err)
		}
		if err := st.PutTurnImageBlob("s_other", "r1", "/tmp/a.png", "abc", 10, 1); err != nil {
			t.Fatalf("PutTurnImageBlob failed on backfilled table: %v", err)
		}
	})

	t.Run("busy external reader on sessions_v2.db aborts rename without deleting WAL", func(t *testing.T) {
		dir := t.TempDir()
		oldPath := filepath.Join(dir, "sessions_v2.db")
		raw, err := sqlitedb.OpenRaw(oldPath)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, raw, migrations[0].SQL)
		exec(t, raw, `PRAGMA wal_autocheckpoint = 0; PRAGMA user_version = 4;`)
		sess1, _ := proto.Marshal(&sessionv1.Session{SessionId: "s_before_lock", ProjectId: "app"})
		exec(t, raw, `INSERT INTO sessions(session_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload) VALUES ('s_before_lock', 'app', 1, 0, 0, 0, 1, 2, ?)`, sess1)

		// Hold an active read snapshot on an older WAL point while another commit lands in WAL.
		extReader, err := sqlitedb.OpenReader(oldPath)
		if err != nil {
			t.Fatal(err)
		}
		readTx, err := extReader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		var cnt int
		_ = readTx.QueryRow(`SELECT count(*) FROM sessions`).Scan(&cnt)

		sess2, _ := proto.Marshal(&sessionv1.Session{SessionId: "s_in_wal", ProjectId: "app"})
		exec(t, raw, `INSERT INTO sessions(session_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload) VALUES ('s_in_wal', 'app', 1, 0, 0, 0, 3, 4, ?)`, sess2)
		raw.Close()

		// Because readTx holds a snapshot before s_in_wal, wal_checkpoint(TRUNCATE) reports busy != 0 and Open MUST abort safely!
		if _, err := Open(filepath.Join(dir, "sessions.db")); err == nil || !strings.Contains(err.Error(), "busy") {
			readTx.Rollback()
			extReader.Close()
			t.Fatalf("expected busy checkpoint error while external reader holds snapshot, got: %v", err)
		}
		if _, err := os.Stat(oldPath); err != nil {
			t.Fatalf("sessions_v2.db must remain untouched when busy: %v", err)
		}

		// Release external reader -> retry succeeds with BOTH s_before_lock and s_in_wal preserved!
		_ = readTx.Rollback()
		_ = extReader.Close()

		st, err := Open(filepath.Join(dir, "sessions.db"))
		if err != nil {
			t.Fatalf("Open after releasing external reader: %v", err)
		}
		defer st.Close()
		for _, id := range []string{"s_before_lock", "s_in_wal"} {
			if _, err := st.Session(id); err != nil {
				t.Fatalf("expected %s preserved after WAL recovery, got %v", id, err)
			}
		}
	})

	t.Run("sessions_v2.db with unexpected future schema > 4 returns ErrNewer", func(t *testing.T) {
		dir := t.TempDir()
		raw, err := sqlitedb.OpenRaw(filepath.Join(dir, "sessions_v2.db"))
		if err != nil {
			t.Fatal(err)
		}
		exec(t, raw, migrations[0].SQL)
		exec(t, raw, `PRAGMA user_version = 5`)
		raw.Close()

		if _, err := Open(filepath.Join(dir, "sessions.db")); err == nil || !strings.Contains(err.Error(), "written by a newer Hermesh") {
			t.Fatalf("expected ErrNewer for schema 5 sessions_v2.db, got %v", err)
		}
	})
}
