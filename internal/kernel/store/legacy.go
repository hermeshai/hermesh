package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
)

// What came before this store (docs/data-migrations.md): a machine's
// sessions were in its hermeshd.db (the V1 sessions, their events a log of
// protobuf messages), and its threads in threads_v2.db (the V2 threads, the
// tables this store's grew out of). Both are moved here by steps of the
// store's ledger, each once. The old files are read, never written: they
// stay as they are, so a move that went wrong can be mended and run again.
//
// The old messages are not this code's any more: session.proto was rewritten
// and thread.proto is gone. legacy.binpb is their descriptors as they were
// (built from the tree at 2ceb85be: `buf build --exclude-source-info`, the
// paths proto/hermesh/session/v1 and proto/hermesh/thread/v2), so old
// payloads are read as what they were and carried over by their fields'
// names.

//go:embed legacy.binpb
var legacyDescriptors []byte

var legacyFiles = sync.OnceValues(func() (*protoregistry.Files, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(legacyDescriptors, &set); err != nil {
		return nil, err
	}
	return protodesc.NewFiles(&set)
})

// legacyJSON reads payload as the old message name, as JSON by its fields'
// names.
func legacyJSON(name string, payload []byte) (map[string]any, error) {
	files, err := legacyFiles()
	if err != nil {
		return nil, err
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return nil, err
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a message", name)
	}
	msg := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(payload, msg); err != nil {
		return nil, err
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(b, &out)
}

// fromLegacy fills into from an old message's JSON; a field the new message
// has no more is left out.
func fromLegacy(j map[string]any, into proto.Message) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, into)
}

// threadKeys are the fields of the V2 messages whose names said "thread"
// where the session messages say "session".
var threadKeys = map[string]string{"thread_id": "session_id", "provider_thread_id": "provider_session_id", "active_provider_thread_id": "active_provider_session_id", "thread": "session"}

// renameThread renames those fields, at any depth.
func renameThread(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if to, ok := threadKeys[k]; ok {
				k = to
			}
			out[k] = renameThread(val)
		}
		return out
	case []any:
		for i := range x {
			x[i] = renameThread(x[i])
		}
	}
	return v
}

// threadStatus is what a V2 thread's status is as a session's. Nothing of an
// old store is at work when it is moved, so what was at work then is idle
// now; what failed is idle with its error (as a session whose turn failed).
var threadStatus = map[string]sessionv1.SessionStatus{
	"THREAD_STATUS_ARCHIVED": sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED,
}

// legacySteps are the moves into this store, in the order they came to be.
// A step's name is its place in the ledger: it is never changed, and a step
// is never taken out (a store that never ran it would then never run it).
func (s *Store) legacySteps(dataDir string) []sqlitedb.Step {
	return []sqlitedb.Step{
		{Name: "v1-sessions", Run: func() (int64, error) { return s.importV1Sessions(filepath.Join(dataDir, "hermeshd.db")) }},
		{Name: "v2-threads", Run: func() (int64, error) { return s.importV2Threads(filepath.Join(dataDir, "threads_v2.db")) }},
	}
}

// openLegacy opens an old store to read, if it is there and has the table.
func openLegacy(path, table string) (*sql.DB, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	db, err := sqlitedb.OpenReader(path)
	if err != nil {
		return nil, err
	}
	var one int
	if err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, nil
	} else if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// importV1Sessions moves the V1 sessions of a machine's hermeshd.db (its
// machine store at schema 18, which the machine brings it to before this
// store opens): each session as it stood, and its conversation from its log
// of events (its turns as runs; what the owner or an agent wrote, what the
// coding agent answered, the tools it used and what went wrong as items). A
// session already here is left as it is.
func (s *Store) importV1Sessions(path string) (int64, error) {
	old, err := openLegacy(path, "sessions")
	if old == nil || err != nil {
		return 0, err
	}
	defer old.Close()
	rows, err := old.Query(`SELECT session_id, project_id, agent, status, created_by, model, provider_ref, branch, progress, error, created_at, updated_at,
		workspace, result, title, effort, conversation, agent_model, agent_effort FROM sessions ORDER BY created_at`)
	if err != nil {
		return 0, err
	}
	type v1 struct {
		sess     *sessionv1.Session
		progress []byte
	}
	var all []v1
	for rows.Next() {
		var id, project, agent, createdBy, model, ref, branch, errText, result, title, effort, conversation, agentModel, agentEffort string
		var status, workspace int32
		var created, updated int64
		var progress []byte
		if err := rows.Scan(&id, &project, &agent, &status, &createdBy, &model, &ref, &branch, &progress, &errText, &created, &updated,
			&workspace, &result, &title, &effort, &conversation, &agentModel, &agentEffort); err != nil {
			rows.Close()
			return 0, err
		}
		// The V1 statuses are the session's own still (their numbers did not change); nothing of an old store is at
		// work now.
		st := sessionv1.SessionStatus(status)
		switch st {
		case sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED, sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
		default:
			st = sessionv1.SessionStatus_SESSION_STATUS_IDLE
		}
		// What ran it: the model it was given, else the one its agent chose itself.
		model, effort = firstOf(model, agentModel), firstOf(effort, agentEffort)
		actor := &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: createdBy}
		if createdBy == "owner" || createdBy == "" {
			actor = &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_OWNER, Id: "owner"}
			createdBy = "owner"
		}
		all = append(all, v1{progress: progress, sess: &sessionv1.Session{
			SessionId: id, ProjectId: project, Agent: agent, Status: st, CreatedBy: createdBy, CreatedAs: conversation, Title: title,
			Branch: branch, Workspace: identityv1.Workspace(workspace), Model: model, Effort: effort, ProviderSessionRef: ref, Result: result, Error: errText,
			ModelSelection:    &sessionv1.ModelSelection{InstanceId: agent, Model: model},
			WorkspaceStrategy: &sessionv1.WorkspaceStrategy{Mode: identityv1.Workspace(workspace), Branch: branch},
			Actor:             actor,
			Lineage:           &sessionv1.SessionLineage{Relationship: "root", Controllable: true},
			CreatedAt:         timestamppb.New(time.Unix(created, 0)), UpdatedAt: timestamppb.New(time.Unix(updated, 0)),
		}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var moved int64
	for _, x := range all {
		if _, err := s.Session(x.sess.SessionId); err == nil {
			continue // here already
		} else if !errors.Is(err, ErrNotFound) {
			return moved, err
		}
		// Its progress is the message it still is (SessionProgress did not change); what it was doing then is over.
		p := &sessionv1.SessionProgress{}
		if err := proto.Unmarshal(x.progress, p); err != nil {
			return moved, fmt.Errorf("session %s: its progress: %w", x.sess.SessionId, err)
		}
		p.Phase, p.CurrentTool, p.PendingQuestion, p.TurnStartedAt = "", "", nil, nil
		x.sess.Progress = p
		runs, items, err := v1Conversation(old, x.sess)
		if err != nil {
			return moved, fmt.Errorf("session %s: its conversation: %w", x.sess.SessionId, err)
		}
		// One session a transaction: it is here whole, or not yet.
		if _, err := s.Mutate(func(tx *Tx) error {
			for _, it := range items {
				p.LastEventSeq = max(p.LastEventSeq, it.Seq)
			}
			if err := tx.PutSessionPreserveUpdatedAt(x.sess); err != nil {
				return err
			}
			for _, r := range runs {
				if err := tx.PutRun(r); err != nil {
					return err
				}
			}
			for _, it := range items {
				if err := tx.putLegacyItem(it); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return moved, fmt.Errorf("session %s: %w", x.sess.SessionId, err)
		}
		moved++
	}
	return moved, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// putLegacyItem writes an item as it was: its own times, not now's.
func (tx *Tx) putLegacyItem(it *sessionv1.TurnItem) error {
	b, err := proto.Marshal(it)
	if err != nil {
		return err
	}
	_, err = tx.tx.Exec(`INSERT OR IGNORE INTO turn_items(session_id, item_id, run_id, seq, kind, payload) VALUES(?, ?, ?, ?, ?, ?)`,
		it.SessionId, it.ItemId, it.RunId, it.Seq, int32(it.Kind), b)
	return err
}

// v1Conversation makes a V1 session's runs and items from its log: a turn is
// a run (who asked, what they wrote, how it ended), and within it each
// message, tool and error is an item, at its event's place in the log. What
// the log has before a turn's start is that turn's (a message from another
// agent comes, then the turn it starts); what it has after the last turn's
// end is a run of its own. Not carried over: the files attached to a
// message (the V1 store kept their paths, not the files) and the questions
// asked, which are answered.
func v1Conversation(old *sql.DB, sess *sessionv1.Session) ([]*sessionv1.Run, []*sessionv1.TurnItem, error) {
	rows, err := old.Query(`SELECT seq, payload FROM session_events WHERE session_id = ? ORDER BY seq`, sess.SessionId)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	id := sess.SessionId
	var runs []*sessionv1.Run
	var items []*sessionv1.TurnItem
	var run *sessionv1.Run
	// waiting are the items the log has before their turn's start.
	var waiting []*sessionv1.TurnItem
	tools := map[string]*sessionv1.TurnItem{}
	text := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	begin := func(at *timestamppb.Timestamp, asker string) {
		author := sess.Actor
		if asker != "" && asker != sess.CreatedBy {
			author = &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: asker}
			if asker == "owner" {
				author = &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_OWNER, Id: "owner"}
			}
		}
		run = &sessionv1.Run{RunId: fmt.Sprintf("run_v1_%s_%d", id, len(runs)+1), SessionId: id, Ordinal: uint32(len(runs) + 1), Status: sessionv1.RunStatus_RUN_STATUS_INTERRUPTED,
			Author: author, Intent: sessionv1.DispatchIntent_DISPATCH_INTENT_START, ModelSelection: sess.ModelSelection, CreatedAt: at, StartedAt: at}
		runs = append(runs, run)
		for _, it := range waiting {
			it.RunId = run.RunId
			if run.Prompt == "" && (it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE || it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED) {
				run.Prompt = it.Text
			}
		}
		waiting = nil
	}
	for rows.Next() {
		var seq uint64
		var payload []byte
		if err := rows.Scan(&seq, &payload); err != nil {
			return nil, nil, err
		}
		ev, err := legacyJSON("hermesh.session.v1.SessionEvent", payload)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", seq, err)
		}
		at := sess.CreatedAt // an event without its time: the session's
		if ts := text(ev, "ts"); ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				at = timestamppb.New(t)
			}
		}
		item := func(kind sessionv1.TurnItemKind) *sessionv1.TurnItem {
			it := &sessionv1.TurnItem{ItemId: fmt.Sprintf("itm_v1_%s_%d", id, seq), SessionId: id, Seq: seq, Kind: kind, Status: "completed", CreatedAt: at, UpdatedAt: at}
			if run != nil {
				it.RunId = run.RunId
			} else {
				waiting = append(waiting, it)
			}
			items = append(items, it)
			return it
		}
		if m, ok := ev["turn_started"].(map[string]any); ok {
			begin(at, text(m, "asker"))
		} else if m, ok := ev["user_message"].(map[string]any); ok {
			it := item(sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE)
			it.Text = text(m, "text")
			if run != nil {
				it.Actor = run.Author
				if run.Prompt == "" {
					run.Prompt = it.Text
				}
			}
		} else if m, ok := ev["message_received"].(map[string]any); ok {
			it := item(sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED)
			it.Text, it.MailMessageId, it.MailAddress, it.MailName = text(m, "text"), text(m, "message_id"), text(m, "from"), text(m, "from_name")
			it.MailDelivered, it.MailInReplyTo = text(m, "delivered"), text(m, "in_reply_to")
			if run != nil && run.Prompt == "" {
				run.Prompt = it.Text
			}
		} else if m, ok := ev["message_sent"].(map[string]any); ok {
			it := item(sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_SENT)
			it.Text, it.MailMessageId, it.MailAddress, it.MailName, it.MailInReplyTo = text(m, "text"), text(m, "message_id"), text(m, "to"), text(m, "to_name"), text(m, "in_reply_to")
		} else if m, ok := ev["agent_message"].(map[string]any); ok {
			item(sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE).Text = text(m, "text")
		} else if m, ok := ev["tool_call"].(map[string]any); ok {
			it := item(sessionv1.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL)
			it.ToolName, it.Command = text(m, "name"), text(m, "summary")
			if text(m, "decision") == "deny" {
				it.Status, it.Output = "failed", text(m, "reason")
			}
			tools[text(m, "call_id")] = it
		} else if m, ok := ev["tool_result"].(map[string]any); ok {
			if it := tools[text(m, "call_id")]; it != nil {
				it.Output = text(m, "summary")
				it.OutputTruncated, _ = m["truncated"].(bool)
				if failed, _ := m["is_error"].(bool); failed {
					it.Status = "failed"
				}
				it.UpdatedAt = at
			}
		} else if m, ok := ev["error"].(map[string]any); ok {
			item(sessionv1.TurnItemKind_TURN_ITEM_KIND_ERROR).Text = text(m, "message")
		} else if m, ok := ev["turn_completed"].(map[string]any); ok && run != nil {
			run.Status, run.CompletedAt = sessionv1.RunStatus_RUN_STATUS_COMPLETED, at
			if failed, _ := m["is_error"].(bool); failed {
				run.Status, run.ErrorMessage = sessionv1.RunStatus_RUN_STATUS_FAILED, text(m, "result")
				if f, ok := m["failure"].(map[string]any); ok {
					run.Failure = &sessionv1.Failure{}
					if err := fromLegacy(f, run.Failure); err != nil {
						return nil, nil, fmt.Errorf("event %d: its failure: %w", seq, err)
					}
				}
			}
			run, tools = nil, map[string]*sessionv1.TurnItem{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(waiting) > 0 { // after the last turn's end
		begin(waiting[0].CreatedAt, "")
	}
	return runs, items, nil
}

// importV2Threads moves the V2 threads of a machine's threads_v2.db (its
// schema 4): each thread as a session, with its runs, items, provider
// threads and checkpoints, read as the messages they were and carried over
// by their fields' names. Their log of events is not moved: a session's
// page is made of what it holds, and its log goes on from where the thread's
// was. A session already here is left as it is.
func (s *Store) importV2Threads(path string) (int64, error) {
	old, err := openLegacy(path, "threads")
	if old == nil || err != nil {
		return 0, err
	}
	defer old.Close()
	rows, err := old.Query(`SELECT thread_id, last_seq, payload FROM threads ORDER BY created_at`)
	if err != nil {
		return 0, err
	}
	type thread struct {
		id      string
		lastSeq uint64
		payload []byte
	}
	var all []thread
	for rows.Next() {
		var t thread
		if err := rows.Scan(&t.id, &t.lastSeq, &t.payload); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// payloads reads a table's messages of one thread as new ones.
	payloads := func(query, name, id string, each func(map[string]any) error) error {
		rows, err := old.Query(query, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				return err
			}
			j, err := legacyJSON(name, b)
			if err != nil {
				return err
			}
			if err := each(renameThread(j).(map[string]any)); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	var moved int64
	for _, t := range all {
		if _, err := s.Session(t.id); err == nil {
			continue // here already
		} else if !errors.Is(err, ErrNotFound) {
			return moved, err
		}
		sess, err := v2Session(t.payload, t.lastSeq)
		if err != nil {
			return moved, fmt.Errorf("thread %s: %w", t.id, err)
		}
		var runs []*sessionv1.Run
		var items []*sessionv1.TurnItem
		var providers []*sessionv1.ProviderSession
		var checkpoints []*sessionv1.Checkpoint
		err = errors.Join(
			payloads(`SELECT payload FROM runs WHERE thread_id = ? ORDER BY ordinal`, "hermesh.thread.v2.Run", t.id, func(j map[string]any) error {
				r := &sessionv1.Run{}
				runs = append(runs, r)
				return fromLegacy(j, r)
			}),
			payloads(`SELECT payload FROM turn_items WHERE thread_id = ? ORDER BY seq`, "hermesh.thread.v2.TurnItem", t.id, func(j map[string]any) error {
				it := &sessionv1.TurnItem{}
				items = append(items, it)
				return fromLegacy(j, it)
			}),
			payloads(`SELECT payload FROM provider_threads WHERE thread_id = ? ORDER BY created_at`, "hermesh.thread.v2.ProviderThread", t.id, func(j map[string]any) error {
				p := &sessionv1.ProviderSession{}
				providers = append(providers, p)
				return fromLegacy(j, p)
			}),
			payloads(`SELECT payload FROM checkpoints WHERE thread_id = ? ORDER BY run_ordinal`, "hermesh.thread.v2.Checkpoint", t.id, func(j map[string]any) error {
				c := &sessionv1.Checkpoint{}
				checkpoints = append(checkpoints, c)
				return fromLegacy(j, c)
			}),
		)
		if err != nil {
			return moved, fmt.Errorf("thread %s: %w", t.id, err)
		}
		// What ran or waited when the old store was last written is over.
		for _, r := range runs {
			switch r.Status {
			case sessionv1.RunStatus_RUN_STATUS_PREPARING, sessionv1.RunStatus_RUN_STATUS_QUEUED, sessionv1.RunStatus_RUN_STATUS_RUNNING, sessionv1.RunStatus_RUN_STATUS_WAITING_REQUEST:
				r.Status = sessionv1.RunStatus_RUN_STATUS_INTERRUPTED
			}
		}
		for _, it := range items {
			it.Streaming = false
			if it.Status == "running" || it.Status == "waiting" {
				it.Status = "interrupted"
			}
		}
		if _, err := s.Mutate(func(tx *Tx) error {
			if err := tx.PutSessionPreserveUpdatedAt(sess); err != nil {
				return err
			}
			for _, r := range runs {
				if err := tx.PutRun(r); err != nil {
					return err
				}
			}
			for _, it := range items {
				if err := tx.putLegacyItem(it); err != nil {
					return err
				}
			}
			for _, p := range providers {
				if err := tx.PutProviderSession(p); err != nil {
					return err
				}
			}
			for _, c := range checkpoints {
				if err := tx.PutCheckpoint(c); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return moved, fmt.Errorf("thread %s: %w", t.id, err)
		}
		moved++
	}
	// The files its messages name are in the blobs folder already (migrateLegacyDirs); what they are is here.
	blobs, err := old.Query(`SELECT sha256, name, mime_type, size_bytes, created_at FROM blobs`)
	if err != nil {
		return moved, err
	}
	defer blobs.Close()
	for blobs.Next() {
		var sha, name, mime string
		var size, created int64
		if err := blobs.Scan(&sha, &name, &mime, &size, &created); err != nil {
			return moved, err
		}
		s.writeMu.Lock()
		_, err := s.writer.Exec(`INSERT OR IGNORE INTO blobs(sha256, name, mime_type, size_bytes, created_at) VALUES(?, ?, ?, ?, ?)`, sha, name, mime, size, created)
		s.writeMu.Unlock()
		if err != nil {
			return moved, err
		}
	}
	return moved, blobs.Err()
}

// v2Session is a V2 thread as the session it is now: its fields by their
// names, and those the session says otherwise (its strategy for the work
// tree, who made it, its use of the context window, its last error, how many
// runs it had and where its log was).
func v2Session(payload []byte, lastSeq uint64) (*sessionv1.Session, error) {
	j, err := legacyJSON("hermesh.thread.v2.Thread", payload)
	if err != nil {
		return nil, err
	}
	status, _ := j["status"].(string)
	turns, _ := j["run_count"].(float64)
	failed, _ := j["last_error"].(string)
	strategy, actor, usage := j["workspace"], j["created_by"], j["usage"]
	for _, k := range []string{"status", "run_count", "last_error", "last_seq", "workspace", "created_by", "usage"} {
		delete(j, k)
	}
	j = renameThread(j).(map[string]any)
	if strategy != nil {
		j["workspace_strategy"] = strategy
	}
	if actor != nil {
		j["actor"] = actor
	}
	if usage != nil {
		j["context_usage"] = usage
	}
	s := &sessionv1.Session{}
	if err := fromLegacy(j, s); err != nil {
		return nil, err
	}
	s.Status = sessionv1.SessionStatus_SESSION_STATUS_IDLE
	if st, ok := threadStatus[status]; ok {
		s.Status = st
	}
	s.Error = failed
	s.Agent, s.Model = s.GetModelSelection().GetInstanceId(), s.GetModelSelection().GetModel()
	s.Workspace, s.Branch = s.GetWorkspaceStrategy().GetMode(), s.GetWorkspaceStrategy().GetBranch()
	s.CreatedBy = s.GetActor().GetId()
	if s.GetActor().GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER || s.CreatedBy == "" {
		s.CreatedBy = "owner"
	}
	s.ActiveRunId, s.PendingRequestId, s.QueuedRunCount, s.ActiveBackgroundCount, s.ActiveDaemonCount = "", "", 0, 0, 0
	s.Progress = &sessionv1.SessionProgress{TurnCount: uint32(turns), LastEventSeq: lastSeq}
	if strings.TrimSpace(failed) != "" {
		s.Progress.LastFailure = &sessionv1.Failure{Message: failed}
	}
	return s, nil
}
