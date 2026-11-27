package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// What came before this store (docs/data-migrations.md): a machine's V1
// sessions lived in hermeshd.db (their events a log of protobuf messages).
// They move into sessions.db once via the store's data_steps ledger. The old
// file is opened read-only and never modified, so a move that went wrong can
// be mended and retried.
//
// Because session.proto was rewritten after v0.1.9, legacy.binpb embeds the
// v0.1.9 descriptors (built from v0.1.9: `buf build --exclude-source-info
// --path proto/hermesh/session/v1 -o legacy.binpb`) so old payloads are
// decoded as they were written and carried over by field name.

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

// legacySteps are the cross-store moves into sessions.db, in the order they
// came to be. A step's name is its place in data_steps: never rename or remove
// one that shipped.
func (s *Store) legacySteps(dataDir string) []sqlitedb.Step {
	return []sqlitedb.Step{
		{Name: "v1-sessions", Run: func() (int64, error) { return s.importV1Sessions(filepath.Join(dataDir, "hermeshd.db")) }},
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
