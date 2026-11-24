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

// The stores this one took over from, as they were on a machine: their
// tables are testdata's (the schemas of a real hermeshd.db at version 18 and
// a real threads_v2.db at version 4, no rows), and the rows are written here
// as the old Hermesh wrote them, in the old messages.

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
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, file))
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

// v2Machine is a machine's threads_v2.db with one V2 thread at work when it
// was last written: a run that ended, one that ran, their items, its
// provider thread and a checkpoint.
func v2Machine(t *testing.T, dir string) {
	t.Helper()
	db := oldStore(t, dir, "threads_v2.db", "threads_v2-v4.schema.sql", 4)
	defer db.Close()
	thread := oldMessage(t, "hermesh.thread.v2.Thread", `{"thread_id":"t_1","machine_id":"m1","project_id":"app","title":"Ship the overview","title_renamed":true,
		"status":"THREAD_STATUS_RUNNING","model_selection":{"instance_id":"codex","model":"gpt-6"},"runtime_mode":"RUNTIME_MODE_AUTO",
		"workspace":{"mode":"WORKSPACE_WORKTREE","branch":"hermesh/t_1","worktree_path":"/w/t_1"},"active_provider_thread_id":"p_1",
		"created_by":{"kind":"ACTOR_KIND_OWNER","id":"owner","label":"Yi"},"lineage":{"relationship":"root","controllable":true},
		"active_run_id":"r_2","last_seq":"41","run_count":2,"usage":{"context_used_tokens":"1200"},"last_error":"","pinned":true,
		"created_at":"2026-11-21T09:00:00Z","updated_at":"2026-11-21T10:00:00Z"}`)
	exec(t, db, `INSERT INTO threads(thread_id, project_id, status, archived, pinned, last_seq, created_at, updated_at, payload) VALUES('t_1', 'app', 4, 0, 1, 41, 1, 2, ?)`, thread)
	run := func(id string, ordinal int, status string) {
		exec(t, db, `INSERT INTO runs(run_id, thread_id, ordinal, status, queue_order, created_at, updated_at, payload) VALUES(?, 't_1', ?, 0, 0, 1, 2, ?)`, id, ordinal,
			oldMessage(t, "hermesh.thread.v2.Run", `{"run_id":"`+id+`","thread_id":"t_1","ordinal":`+string(rune('0'+ordinal))+`,"status":"`+status+`","prompt":"do it",
				"provider_thread_id":"p_1","model_selection":{"instance_id":"codex","model":"gpt-6"},"created_at":"2026-11-21T09:00:00Z"}`))
	}
	run("r_1", 1, "RUN_STATUS_COMPLETED")
	run("r_2", 2, "RUN_STATUS_RUNNING")
	item := func(id, runID string, seq int, json string) {
		exec(t, db, `INSERT INTO turn_items(thread_id, item_id, run_id, seq, kind, payload) VALUES('t_1', ?, ?, ?, 0, ?)`, id, runID, seq,
			oldMessage(t, "hermesh.thread.v2.TurnItem", `{"item_id":"`+id+`","thread_id":"t_1","run_id":"`+runID+`","seq":"`+string(rune('0'+seq))+`",`+json[1:]))
	}
	item("i_1", "r_1", 1, `{"kind":"TURN_ITEM_KIND_USER_MESSAGE","status":"completed","text":"do it"}`)
	item("i_2", "r_1", 2, `{"kind":"TURN_ITEM_KIND_FILE_CHANGE","status":"completed","file_path":"a.go","diff":"+x","additions":3,"deletions":1}`)
	item("i_3", "r_2", 3, `{"kind":"TURN_ITEM_KIND_ASSISTANT_MESSAGE","status":"running","text":"work","streaming":true}`)
	exec(t, db, `INSERT INTO provider_threads(provider_thread_id, thread_id, instance_id, created_at, updated_at, payload) VALUES('p_1', 't_1', 'codex', 1, 2, ?)`,
		oldMessage(t, "hermesh.thread.v2.ProviderThread", `{"provider_thread_id":"p_1","thread_id":"t_1","instance_id":"codex"}`))
	exec(t, db, `INSERT INTO checkpoints(checkpoint_id, thread_id, run_ordinal, created_at, payload) VALUES('c_1', 't_1', 1, 1, ?)`,
		oldMessage(t, "hermesh.thread.v2.Checkpoint", `{"checkpoint_id":"c_1","thread_id":"t_1","run_ordinal":1}`))
	exec(t, db, `INSERT INTO blobs(sha256, name, mime_type, size_bytes, created_at) VALUES('abc', 'a.png', 'image/png', 12, 1)`)
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
	st, err := Open(filepath.Join(dir, "sessions_v2.db"))
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
	// Its progress as it was, less what it was doing then; its log goes on after its last item.
	if p := done.Progress; p.TurnCount != 1 || p.LastMessageExcerpt != "Fixed it." || p.Usage.GetInputTokens() != 900 || p.Phase != "" || p.CurrentTool != "" || p.LastEventSeq != 6 {
		t.Fatalf("its progress: %v", p)
	}
	// One turn, started by Her's message, which the log has before the turn's start: the run's prompt and first item.
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

	// Archived stays archived; a failed turn is a failed run with why, a tool refused a failed item; the next turn
	// another run, by who asked for it.
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

	// At work when the old store was last written: idle now, its turn interrupted.
	cut, _ := st.Session("s_cut")
	if runs = runsOf(t, st, "s_cut"); cut.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || len(runs) != 1 || runs[0].Status != sessionv1.RunStatus_RUN_STATUS_INTERRUPTED {
		t.Fatalf("the one cut off: %v, %v", cut, runs)
	}
	st.Close()

	// The old store is not touched, and the next start runs nothing again: what changed here since stays.
	if after, _ := os.ReadFile(filepath.Join(dir, "hermeshd.db")); string(after) != string(before) {
		t.Fatal("the old store was written")
	}
	st, err = Open(filepath.Join(dir, "sessions_v2.db"))
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

// A machine that ran the V2 threads: each thread is a session, read as the
// message it was and carried over by its fields' names, with its runs, items,
// provider thread and checkpoint; what was at work is over.
func TestV2ThreadsMoveInAsSessions(t *testing.T) {
	dir := t.TempDir()
	v2Machine(t, dir)
	st, err := Open(filepath.Join(dir, "sessions_v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r := step(t, st, "v2-threads"); !r.Ran || r.Moved != 1 || r.Err != nil {
		t.Fatalf("the move: %+v", r)
	}
	if r := step(t, st, "v1-sessions"); r.Err != nil || r.Moved != 0 {
		t.Fatalf("no V1 store here: %+v", r)
	}
	s, err := st.Session("t_1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Ship the overview" || !s.TitleRenamed || s.MachineId != "m1" || s.ProjectId != "app" || !s.Pinned ||
		s.Status != sessionv1.SessionStatus_SESSION_STATUS_IDLE || s.ActiveRunId != "" ||
		s.Agent != "codex" || s.Model != "gpt-6" || s.ModelSelection.GetModel() != "gpt-6" || s.RuntimeMode != identityv1.RuntimeMode_RUNTIME_MODE_AUTO ||
		s.WorkspaceStrategy.GetBranch() != "hermesh/t_1" || s.WorkspaceStrategy.GetWorktreePath() != "/w/t_1" || s.Branch != "hermesh/t_1" || s.Workspace != identityv1.Workspace_WORKSPACE_WORKTREE ||
		s.ActiveProviderSessionId != "p_1" || s.Actor.GetLabel() != "Yi" || s.CreatedBy != "owner" || s.Lineage.GetRelationship() != "root" ||
		s.ContextUsage.GetContextUsedTokens() != 1200 || s.Progress.TurnCount != 2 || s.Progress.LastEventSeq != 41 {
		t.Fatalf("the session: %v", s)
	}
	if !s.CreatedAt.AsTime().Equal(time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)) || !s.UpdatedAt.AsTime().Equal(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("its times: %v, %v", s.CreatedAt.AsTime(), s.UpdatedAt.AsTime())
	}
	runs := runsOf(t, st, "t_1")
	if len(runs) != 2 || runs[0].Status != sessionv1.RunStatus_RUN_STATUS_COMPLETED || runs[1].Status != sessionv1.RunStatus_RUN_STATUS_INTERRUPTED ||
		runs[0].Prompt != "do it" || runs[0].ProviderSessionId != "p_1" || runs[0].SessionId != "t_1" || runs[1].Ordinal != 2 {
		t.Fatalf("its runs: %v", runs)
	}
	its := items(t, st, "t_1")
	if len(its) != 3 || its[0].Kind != sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE || its[0].Text != "do it" || its[0].SessionId != "t_1" ||
		its[1].Kind != sessionv1.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE || its[1].FilePath != "a.go" || its[1].Diff != "+x" || its[1].Additions != 3 || its[1].Deletions != 1 ||
		its[2].Status != "interrupted" || its[2].Streaming || its[2].Seq != 3 {
		t.Fatalf("its items (a file change's fields moved numbers between the messages): %v", its)
	}
	var n int
	for q, want := range map[string]int{
		`SELECT count(*) FROM provider_sessions WHERE session_id = 't_1' AND provider_session_id = 'p_1'`: 1,
		`SELECT count(*) FROM checkpoints WHERE session_id = 't_1' AND checkpoint_id = 'c_1'`:             1,
		`SELECT count(*) FROM blobs WHERE sha256 = 'abc' AND name = 'a.png'`:                              1,
	} {
		if st.reader.QueryRow(q).Scan(&n); n != want {
			t.Fatalf("%s: %d", q, n)
		}
	}
}

// A move that fails says so, is kept in the ledger and does not keep the
// store from opening or the other move from running; once what was wrong is
// mended, the next start moves the rest, and what had moved is not moved
// twice.
func TestAMoveThatFailsIsSaidKeptAndTriedAgain(t *testing.T) {
	dir := t.TempDir()
	v1Machine(t, dir)
	v2Machine(t, dir)
	// The second session's log has an event that is no message of the old kind.
	old, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "hermeshd.db"))
	exec(t, old, `UPDATE session_events SET payload = x'ffffffff' WHERE session_id = 's_failed' AND seq = 3`)
	st, err := Open(filepath.Join(dir, "sessions_v2.db"))
	if err != nil {
		t.Fatalf("the store did not open: %v", err)
	}
	r := step(t, st, "v1-sessions")
	if r.Err == nil || !strings.Contains(r.Err.Error(), "s_failed") || !strings.Contains(r.Err.Error(), "event 3") || r.Moved != 1 {
		t.Fatalf("the move that failed says which session and event, and what it moved before: %+v", r)
	}
	if r := step(t, st, "v2-threads"); r.Err != nil || r.Moved != 1 {
		t.Fatalf("the other move: %+v", r)
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
	st, err = Open(filepath.Join(dir, "sessions_v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r := step(t, st, "v1-sessions"); !r.Ran || r.Err != nil || r.Moved != 2 {
		t.Fatalf("mended: %+v", r)
	}
	if r := step(t, st, "v2-threads"); r.Ran || r.Moved != 1 {
		t.Fatalf("the move that was done ran again: %+v", r)
	}
	if n := len(items(t, st, "s_done")); n != 4 {
		t.Fatalf("moved twice: %d items", n)
	}
	if _, err := st.Session("s_cut"); err != nil {
		t.Fatal(err)
	}
}

// A machine with no old store: nothing to move, and that is done too.
func TestNoOldStoreIsNothingToMove(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions_v2.db"))
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
