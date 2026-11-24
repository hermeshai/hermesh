package owner

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/kernel/policy"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
)

const legacyThreadV2Migrations = `
CREATE TABLE threads_v2_cache (
  thread_id TEXT PRIMARY KEY,
  machine_id TEXT NOT NULL,
  archived INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  thread_payload BLOB NOT NULL,
  projection_payload BLOB
);
CREATE INDEX idx_threads_v2_cache_updated ON threads_v2_cache(archived, updated_at DESC);

CREATE TABLE threads_v2_outbox (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  command_id TEXT NOT NULL UNIQUE,
  machine_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  req_payload BLOB NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_threads_v2_outbox_machine ON threads_v2_outbox(machine_id, seq ASC);

CREATE TABLE threads_v2_blobs (
  sha256 TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

const sessionStoreMigrations = `
DROP TABLE IF EXISTS threads_v2_cache;
DROP TABLE IF EXISTS threads_v2_outbox;
DROP TABLE IF EXISTS threads_v2_blobs;

CREATE TABLE IF NOT EXISTS sessions_cache (
  session_id TEXT PRIMARY KEY,
  machine_id TEXT NOT NULL,
  archived INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  session_payload BLOB NOT NULL,
  projection_payload BLOB
);
CREATE INDEX IF NOT EXISTS idx_sessions_cache_updated ON sessions_cache(archived, updated_at DESC);

CREATE TABLE IF NOT EXISTS sessions_outbox (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  command_id TEXT NOT NULL UNIQUE,
  machine_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  req_payload BLOB NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_outbox_machine ON sessions_outbox(machine_id, seq ASC);

CREATE TABLE IF NOT EXISTS sessions_blobs (
  sha256 TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

type ownerSessionService struct {
	d *Device
}

var _ sessionv1connect.SessionServiceHandler = (*ownerSessionService)(nil)

func (s *ownerSessionService) o() *Owner { return s.d.owner() }

func (o *Owner) ownerPrincipal() *kernel.Principal {
	o.mu.Lock()
	id := o.ownerID
	o.mu.Unlock()
	if id == "" {
		id = "owner"
	}
	return kernel.OwnerPrincipal(id, "Owner")
}

// localEngineFor returns the in-process kernel.Engine if machineID resolves to
// this computer's own execution machine (or when machineID == "" and localMachine is active).
func (o *Owner) localEngineFor(machineID string) (*kernel.Engine, string) {
	o.mu.Lock()
	lm := o.localMachine
	o.mu.Unlock()
	if lm == nil || lm.Sessions() == nil {
		return nil, ""
	}
	if machineID == "" || machineID == lm.MachineID() || machineID == lm.Name() {
		return lm.Sessions(), lm.MachineID()
	}
	return nil, ""
}

// resolveMachineForSession finds which machine owns sessionID: checks localMachine,
// cached rows, and deterministic session_id prefix matching.
func (o *Owner) resolveMachineForSession(ctx context.Context, sessionID string) (localEng *kernel.Engine, machineID string, err error) {
	o.mu.Lock()
	lm := o.localMachine
	o.mu.Unlock()
	if lm != nil && lm.Sessions() != nil {
		if kernel.OwnsSession(lm.MachineID(), sessionID) {
			return lm.Sessions(), lm.MachineID(), nil
		}
		if _, err := lm.Sessions().Store().Session(sessionID); err == nil {
			return lm.Sessions(), lm.MachineID(), nil
		}
	}
	var cachedMachine string
	if qErr := o.st.db.QueryRow(`SELECT machine_id FROM sessions_cache WHERE session_id = ?`, sessionID).Scan(&cachedMachine); qErr == nil && cachedMachine != "" {
		if eng, mid := o.localEngineFor(cachedMachine); eng != nil {
			return eng, mid, nil
		}
		return nil, cachedMachine, nil
	}
	machines, mErr := o.ListMachines(ctx)
	if mErr == nil {
		for _, m := range machines {
			if kernel.OwnsSession(m.MachineId, sessionID) {
				if eng, mid := o.localEngineFor(m.MachineId); eng != nil {
					return eng, mid, nil
				}
				return nil, m.MachineId, nil
			}
		}
	}
	return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found on any known machine", sessionID))
}

func (o *Owner) dialRemoteSessionMachine(ctx context.Context, machineID string) (*peer.Machine, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := o.relay(cctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	pm, err := c.Machine(cctx, machineID)
	if err != nil {
		if o.recoverTunnel(c, machineID, err) {
			if c2, err2 := o.relay(cctx); err2 == nil {
				if pm2, err2 := c2.Machine(cctx, machineID); err2 == nil {
					o.noteTunnelOK(machineID)
					return pm2, nil
				}
			}
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	o.noteTunnelOK(machineID)
	return pm, nil
}

func isOfflineErr(err error) bool {
	if err == nil {
		return false
	}
	code := connect.CodeOf(err)
	return code == connect.CodeUnavailable || code == connect.CodeDeadlineExceeded || errors.Is(err, context.DeadlineExceeded)
}

func (o *Owner) cacheSession(sess *sessionv1.Session, proj *sessionv1.SessionProjection) {
	if sess == nil || sess.SessionId == "" {
		return
	}
	sb, err := proto.Marshal(sess)
	if err != nil {
		return
	}
	var pb []byte
	if proj != nil {
		pb, _ = proto.Marshal(proj)
	}
	archived := 0
	if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED || sess.ArchivedAt != nil {
		archived = 1
	}
	updatedMs := time.Now().UnixMilli()
	if sess.UpdatedAt != nil {
		updatedMs = sess.UpdatedAt.AsTime().UnixMilli()
	}
	if pb != nil {
		_, _ = o.st.db.Exec(
			`INSERT INTO sessions_cache(session_id, machine_id, archived, updated_at, session_payload, projection_payload)
			 VALUES(?, ?, ?, ?, ?, ?)
			 ON CONFLICT(session_id) DO UPDATE SET
			   machine_id=excluded.machine_id, archived=excluded.archived, updated_at=excluded.updated_at,
			   session_payload=excluded.session_payload, projection_payload=excluded.projection_payload`,
			sess.SessionId, sess.MachineId, archived, updatedMs, sb, pb,
		)
	} else {
		_, _ = o.st.db.Exec(
			`INSERT INTO sessions_cache(session_id, machine_id, archived, updated_at, session_payload)
			 VALUES(?, ?, ?, ?, ?)
			 ON CONFLICT(session_id) DO UPDATE SET
			   machine_id=excluded.machine_id, archived=excluded.archived, updated_at=excluded.updated_at,
			   session_payload=excluded.session_payload`,
			sess.SessionId, sess.MachineId, archived, updatedMs, sb,
		)
	}
	if o.rep != nil {
		o.rep.sessionWritten(sess.SessionId, sess, false)
		o.rep.broadcastLocalSessionChange([]*sessionv1.Session{machine.BasicSession(sess)}, nil)
	}
	o.notifySessionSubs(&sessionv1.WatchSessionsResponse{Sessions: []*sessionv1.Session{sess}})
}

func (o *Owner) removeCachedSession(sessionID string) {
	_, _ = o.st.db.Exec(`DELETE FROM sessions_cache WHERE session_id = ?`, sessionID)
	if o.rep != nil {
		o.rep.sessionWritten(sessionID, nil, true)
		o.rep.broadcastLocalSessionChange(nil, []string{sessionID})
	}
	o.notifySessionSubs(&sessionv1.WatchSessionsResponse{DeletedSessionIds: []string{sessionID}})
}

func (o *Owner) CancelQueuedSession(sessionID string) error {
	if sessionID == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	_, err := o.st.db.Exec(`DELETE FROM sessions_outbox WHERE session_id = ?`, sessionID)
	o.removeCachedSession(sessionID)
	return err
}

func (o *Owner) cachedSessions(includeArchived bool) []*sessionv1.Session {
	q := `SELECT session_payload FROM sessions_cache`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := o.st.db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*sessionv1.Session
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) == nil {
			s := &sessionv1.Session{}
			if proto.Unmarshal(b, s) == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

func (o *Owner) cachedProjection(sessionID string) *sessionv1.SessionProjection {
	var sb, pb []byte
	err := o.st.db.QueryRow(`SELECT session_payload, projection_payload FROM sessions_cache WHERE session_id = ?`, sessionID).Scan(&sb, &pb)
	if err != nil {
		return nil
	}
	if len(pb) > 0 {
		proj := &sessionv1.SessionProjection{}
		if proto.Unmarshal(pb, proj) == nil {
			if len(sb) > 0 {
				sess := &sessionv1.Session{}
				if proto.Unmarshal(sb, sess) == nil {
					proj.Session = sess
				}
			}
			return proj
		}
	}
	if len(sb) > 0 {
		sess := &sessionv1.Session{}
		if proto.Unmarshal(sb, sess) == nil {
			return &sessionv1.SessionProjection{Session: sess}
		}
	}
	return nil
}

func (o *Owner) notifySessionSubs(msg *sessionv1.WatchSessionsResponse) {
	o.sessionMu.Lock()
	defer o.sessionMu.Unlock()
	for ch := range o.sessionSubs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (o *Owner) wakeSessionOutbox() {
	select {
	case o.sessionWake <- struct{}{}:
	default:
	}
}

func (o *Owner) enqueueOutbox(commandID, machineID, sessionID, method, summary string, req proto.Message) error {
	if commandID == "" {
		commandID = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	_, err = o.st.db.Exec(
		`INSERT OR IGNORE INTO sessions_outbox(command_id, machine_id, session_id, method, summary, req_payload, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		commandID, machineID, sessionID, method, summary, b, time.Now().UnixMilli(),
	)
	if err == nil {
		o.wakeSessionOutbox()
	}
	return err
}

func (o *Owner) syncBlobsToRemote(ctx context.Context, pm *peer.Machine, refs []*sessionv1.AttachmentRef) error {
	for _, r := range refs {
		var name, mime string
		var data []byte
		err := o.st.db.QueryRow(`SELECT name, mime_type, data FROM sessions_blobs WHERE sha256 = ?`, r.BlobSha256).Scan(&name, &mime, &data)
		if err != nil {
			if eng, _ := o.localEngineFor(""); eng != nil {
				data, name, mime, err = eng.Store().GetBlob(r.BlobSha256)
			}
		}
		if err == nil && len(data) > 0 {
			if _, uErr := pm.Session.UploadBlob(ctx, &sessionv1.UploadBlobRequest{
				Name:     name,
				MimeType: mime,
				Data:     data,
			}); uErr != nil {
				return uErr
			}
		}
	}
	return nil
}

// flushSessionOutboxLoop drains durable offline commands in FIFO order per machine
// whenever connectivity returns.
func (o *Owner) flushSessionOutboxLoop(ctx context.Context) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.sessionWake:
		case <-ticker.C:
		}
		o.flushSessionOutboxOnce(ctx)
	}
}

type outboxRow struct {
	seq       int64
	commandID string
	machineID string
	sessionID string
	method    string
	payload   []byte
}

func (o *Owner) flushSessionOutboxOnce(ctx context.Context) {
	rows, err := o.st.db.Query(`SELECT seq, command_id, machine_id, session_id, method, req_payload FROM sessions_outbox ORDER BY seq ASC`)
	if err != nil {
		return
	}
	var list []outboxRow
	for rows.Next() {
		var r outboxRow
		if rows.Scan(&r.seq, &r.commandID, &r.machineID, &r.sessionID, &r.method, &r.payload) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	if len(list) == 0 {
		return
	}

	skipMachine := map[string]bool{}
	for _, item := range list {
		if skipMachine[item.machineID] {
			continue
		}
		pm, err := o.dialRemoteSessionMachine(ctx, item.machineID)
		if err != nil {
			skipMachine[item.machineID] = true
			continue
		}
		if o.st.db.QueryRow(`SELECT session_id, req_payload FROM sessions_outbox WHERE seq = ?`, item.seq).Scan(&item.sessionID, &item.payload) != nil {
			continue
		}
		err = o.replayOutboxItem(ctx, pm, item)
		if err == nil {
			_, _ = o.st.db.Exec(`DELETE FROM sessions_outbox WHERE seq = ?`, item.seq)
			continue
		}
		_, _ = o.st.db.Exec(`UPDATE sessions_outbox SET attempts = attempts + 1, last_error = ? WHERE seq = ?`, err.Error(), item.seq)
		if isOfflineErr(err) {
			skipMachine[item.machineID] = true
		} else {
			o.recordOutboxFailure(item, err)
			_, _ = o.st.db.Exec(`DELETE FROM sessions_outbox WHERE seq = ?`, item.seq)
		}
	}
}

func (o *Owner) recordOutboxFailure(item outboxRow, err error) {
	proj := o.cachedProjection(item.sessionID)
	if proj == nil || proj.Session == nil {
		return
	}
	sess := proj.Session
	if sess.OfflineQueued && sess.Status == sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
		return
	}
	msg := fmt.Sprintf("%s was rejected by the machine: %s", item.method, connectMessage(err))
	sess.Error = msg
	if sess.Progress == nil {
		sess.Progress = &sessionv1.SessionProgress{}
	}
	sess.Progress.LastFailure = &sessionv1.Failure{
		Kind:    sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST,
		Message: msg,
	}
	if sess.OfflineQueued {
		sess.Status = sessionv1.SessionStatus_SESSION_STATUS_CRASHED
		sess.QueuedRunCount = 0
	}
	sess.UpdatedAt = timestamppb.Now()
	o.cacheSession(sess, proj)
}

func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

func (o *Owner) replayOutboxItem(ctx context.Context, pm *peer.Machine, item outboxRow) error {
	switch item.method {
	case "StartSession":
		req := &sessionv1.StartSessionRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		if err := o.syncBlobsToRemote(ctx, pm, req.Attachments); err != nil {
			return err
		}
		res, err := pm.Session.StartSession(ctx, req)
		if err != nil {
			return err
		}
		realID := res.GetSession().GetSessionId()
		if item.sessionID != "" && realID != "" && item.sessionID != realID {
			o.rewriteOutboxSessionID(item.sessionID, realID)
			o.removeCachedSession(item.sessionID)
		}
		if proj, gErr := pm.Session.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: realID}); gErr == nil {
			o.cacheSession(proj.Projection.GetSession(), proj.Projection)
		} else {
			o.cacheSession(res.GetSession(), nil)
		}
		return nil

	case "DispatchMessage":
		req := &sessionv1.DispatchMessageRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		if err := o.syncBlobsToRemote(ctx, pm, req.Attachments); err != nil {
			return err
		}
		res, err := pm.Session.DispatchMessage(ctx, req)
		if err != nil {
			return err
		}
		o.cacheSession(res.GetSession(), nil)
		return nil

	case "ManageQueue":
		req := &sessionv1.ManageQueueRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		res, err := pm.Session.ManageQueue(ctx, req)
		if err != nil {
			return err
		}
		o.cacheSession(res.GetSession(), nil)
		return nil

	case "UpdateSession":
		req := &sessionv1.UpdateSessionRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		res, err := pm.Session.UpdateSession(ctx, req)
		if err != nil {
			return err
		}
		o.cacheSession(res.GetSession(), nil)
		return nil

	case "RespondRuntimeRequest":
		req := &sessionv1.RespondRuntimeRequestRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		for _, ans := range req.Answers {
			if err := o.syncBlobsToRemote(ctx, pm, ans.Attachments); err != nil {
				return err
			}
		}
		res, err := pm.Session.RespondRuntimeRequest(ctx, req)
		if err != nil {
			return err
		}
		o.cacheSession(res.GetSession(), nil)
		return nil
	}
	return nil
}

func (o *Owner) rewriteOutboxSessionID(oldID, newID string) {
	rows, err := o.st.db.Query(`SELECT seq, method, req_payload FROM sessions_outbox WHERE session_id = ?`, oldID)
	if err != nil {
		return
	}
	type patch struct {
		seq     int64
		payload []byte
	}
	var patches []patch
	for rows.Next() {
		var seq int64
		var method string
		var raw []byte
		if rows.Scan(&seq, &method, &raw) != nil {
			continue
		}
		switch method {
		case "DispatchMessage":
			m := &sessionv1.DispatchMessageRequest{}
			if proto.Unmarshal(raw, m) == nil {
				m.SessionId = newID
				b, _ := proto.Marshal(m)
				patches = append(patches, patch{seq, b})
			}
		case "ManageQueue":
			m := &sessionv1.ManageQueueRequest{}
			if proto.Unmarshal(raw, m) == nil {
				m.SessionId = newID
				b, _ := proto.Marshal(m)
				patches = append(patches, patch{seq, b})
			}
		case "UpdateSession":
			m := &sessionv1.UpdateSessionRequest{}
			if proto.Unmarshal(raw, m) == nil {
				m.SessionId = newID
				b, _ := proto.Marshal(m)
				patches = append(patches, patch{seq, b})
			}
		}
	}
	rows.Close()
	for _, p := range patches {
		_, _ = o.st.db.Exec(`UPDATE sessions_outbox SET session_id = ?, req_payload = ? WHERE seq = ?`, newID, p.payload, p.seq)
	}
}

// ---------------------------------------------------------------------------
// Owner SessionServiceHandler Implementation

func (s *ownerSessionService) ListProviders(ctx context.Context, req *sessionv1.ListProvidersRequest) (*sessionv1.ListProvidersResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		return eng.ListProviders(ctx, o.ownerPrincipal(), req.ProjectId, req.Refresh)
	}
	if req.MachineId == "" {
		machines, _ := o.ListMachines(ctx)
		for _, m := range machines {
			if m.Online {
				req.MachineId = m.MachineId
				break
			}
		}
	}
	if req.MachineId == "" {
		return &sessionv1.ListProvidersResponse{MaxRuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS}, nil
	}
	pm, err := o.dialRemoteSessionMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	return pm.Session.ListProviders(ctx, req)
}

func (s *ownerSessionService) UpsertProviderInstance(ctx context.Context, req *sessionv1.UpsertProviderInstanceRequest) (*sessionv1.UpsertProviderInstanceResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		inst, err := eng.UpsertProviderInstance(ctx, o.ownerPrincipal(), req.Config)
		if err != nil {
			return nil, err
		}
		return &sessionv1.UpsertProviderInstanceResponse{Provider: inst}, nil
	}
	pm, err := o.dialRemoteSessionMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	return pm.Session.UpsertProviderInstance(ctx, req)
}

func (s *ownerSessionService) RemoveProviderInstance(ctx context.Context, req *sessionv1.RemoveProviderInstanceRequest) (*sessionv1.RemoveProviderInstanceResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		return &sessionv1.RemoveProviderInstanceResponse{}, eng.RemoveProviderInstance(ctx, o.ownerPrincipal(), req.InstanceId)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	return pm.Session.RemoveProviderInstance(ctx, req)
}

func (s *ownerSessionService) StartSession(ctx context.Context, req *sessionv1.StartSessionRequest) (*sessionv1.StartSessionResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		for _, r := range req.Attachments {
			var name, mime string
			var data []byte
			if o.st.db.QueryRow(`SELECT name, mime_type, data FROM sessions_blobs WHERE sha256 = ?`, r.BlobSha256).Scan(&name, &mime, &data) == nil && len(data) > 0 {
				_, _ = eng.Store().PutBlob(name, mime, data)
			}
		}
		return eng.StartSession(ctx, o.ownerPrincipal(), req)
	}
	targetMachine := req.MachineId
	if targetMachine == "" {
		machines, _ := o.ListMachines(ctx)
		if len(machines) > 0 {
			targetMachine = machines[0].MachineId
		}
	}
	if targetMachine == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no execution machine available"))
	}
	pm, err := o.dialRemoteSessionMachine(ctx, targetMachine)
	if err == nil {
		if bErr := o.syncBlobsToRemote(ctx, pm, req.Attachments); bErr == nil {
			res, lErr := pm.Session.StartSession(ctx, req)
			if lErr == nil {
				o.cacheSession(res.GetSession(), nil)
				return res, nil
			}
			if !isOfflineErr(lErr) {
				return nil, lErr
			}
		}
	} else if !isOfflineErr(err) {
		return nil, err
	}

	tempID := "s_" + kernel.SessionPrefix(targetMachine) + "_" + strings.ToLower(ulid.Make().String())
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.InitialPrompt)
		if i := strings.IndexByte(title, '\n'); i >= 0 {
			title = strings.TrimSpace(title[:i])
		}
		if title == "" {
			title = "New Session"
		}
	}
	now := timestamppb.Now()
	agent := req.GetModelSelection().GetInstanceId()
	if agent == "" {
		agent = "claude"
	}
	wsMode := req.GetWorkspace().GetMode()
	if wsMode == identityv1.Workspace_WORKSPACE_UNSPECIFIED {
		wsMode = identityv1.Workspace_WORKSPACE_WORKTREE
	}
	optSession := &sessionv1.Session{
		SessionId:            tempID,
		MachineId:            targetMachine,
		ProjectId:            cmp.Or(req.ProjectId, kernel.ScratchProjectID),
		Title:                title,
		Status:               sessionv1.SessionStatus_SESSION_STATUS_QUEUED,
		Agent:                agent,
		ModelSelection:       req.ModelSelection,
		RuntimeMode:          policy.NormalizeRequested(req.RuntimeMode),
		EffectiveRuntimeMode: policy.NormalizeRequested(req.RuntimeMode),
		MaxRuntimeMode:       identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InteractionMode:      req.InteractionMode,
		Workspace:            wsMode,
		WorkspaceStrategy:    req.Workspace,
		Actor:                o.ownerPrincipal().Actor,
		CreatedBy:            o.ownerPrincipal().Actor.GetId(),
		OfflineQueued:        true,
		QueuedRunCount:       1,
		Progress:             &sessionv1.SessionProgress{},
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	optRun := &sessionv1.Run{
		RunId:     "run_" + strings.ToLower(ulid.Make().String()),
		SessionId: tempID,
		Ordinal:   1,
		Status:    sessionv1.RunStatus_RUN_STATUS_QUEUED,
		Author:    o.ownerPrincipal().Actor,
		Prompt:    req.InitialPrompt,
		CreatedAt: now,
	}
	proj := &sessionv1.SessionProjection{
		Session: optSession,
		Runs:    []*sessionv1.Run{optRun},
	}
	if err := o.enqueueOutbox(req.CommandId, targetMachine, tempID, "StartSession", title, req); err != nil {
		return nil, err
	}
	o.cacheSession(optSession, proj)
	return &sessionv1.StartSessionResponse{
		Session:       optSession,
		InitialRun:    optRun,
		QueuedOffline: true,
	}, nil
}

func (s *ownerSessionService) GetSession(ctx context.Context, req *sessionv1.GetSessionRequest) (*sessionv1.GetSessionResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		proj, err := eng.GetSession(ctx, o.ownerPrincipal(), req.SessionId)
		if err != nil {
			return nil, err
		}
		return &sessionv1.GetSessionResponse{Projection: proj}, nil
	}
	if pm, dErr := o.dialRemoteSessionMachine(ctx, machineID); dErr == nil {
		if res, gErr := pm.Session.GetSession(ctx, req); gErr == nil {
			o.cacheSession(res.Projection.GetSession(), res.Projection)
			return res, nil
		} else if !isOfflineErr(gErr) {
			if cached := o.cachedProjection(req.SessionId); connect.CodeOf(gErr) == connect.CodeNotFound && cached.GetSession().GetOfflineQueued() {
				return &sessionv1.GetSessionResponse{Projection: cached}, nil
			}
			return nil, gErr
		}
	}
	if cached := o.cachedProjection(req.SessionId); cached != nil {
		return &sessionv1.GetSessionResponse{Projection: cached}, nil
	}
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("machine is offline and session is not cached locally"))
}

func (s *ownerSessionService) ListSessions(ctx context.Context, req *sessionv1.ListSessionsRequest) (*sessionv1.ListSessionsResponse, error) {
	o := s.o()
	incArch := req.IncludeArchived || req.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
	byID := map[string]*sessionv1.Session{}
	for _, cs := range o.cachedSessions(incArch) {
		if (req.MachineId == "" || cs.MachineId == req.MachineId) && (req.ProjectId == "" || cs.ProjectId == req.ProjectId) {
			if req.Status != 0 && cs.Status != req.Status {
				continue
			}
			byID[cs.SessionId] = cs
		}
	}
	if eng, localID := o.localEngineFor(""); eng != nil && (req.MachineId == "" || req.MachineId == localID) {
		if res, err := eng.ListSessions(ctx, o.ownerPrincipal(), &sessionv1.ListSessionsRequest{
			ProjectId:       req.ProjectId,
			Status:          req.Status,
			Since:           req.Since,
			IncludeArchived: incArch,
		}); err == nil {
			for _, sess := range res.Sessions {
				byID[sess.SessionId] = sess
			}
		}
	}
	machines, _ := o.ListMachines(ctx)
	for _, m := range machines {
		if _, isLocal := o.localEngineFor(m.MachineId); isLocal != "" || !m.Online {
			continue
		}
		if req.MachineId != "" && req.MachineId != m.MachineId {
			continue
		}
		if pm, err := o.dialRemoteSessionMachine(ctx, m.MachineId); err == nil {
			if rList, lErr := pm.Session.ListSessions(ctx, req); lErr == nil {
				for _, sess := range rList.Sessions {
					byID[sess.SessionId] = sess
					o.cacheSession(sess, nil)
				}
			}
		}
	}
	out := make([]*sessionv1.Session, 0, len(byID))
	for _, sess := range byID {
		if !incArch && sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
			continue
		}
		out = append(out, sess)
	}
	slices.SortFunc(out, func(a, b *sessionv1.Session) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		return machine.NewestFirst(a, b)
	})
	var next *sessionv1.SessionCursor
	if req.Limit > 0 {
		out, next = onePage(out, int(req.Limit), req.Before, false)
	}
	if req.View == sessionv1.SessionView_SESSION_VIEW_BASIC {
		out = machine.BasicSessions(out)
	}
	return &sessionv1.ListSessionsResponse{Sessions: out, Next: next}, nil
}

func (s *ownerSessionService) WatchSessions(ctx context.Context, req *sessionv1.WatchSessionsRequest, stream *connect.ServerStream[sessionv1.WatchSessionsResponse]) error {
	o := s.o()
	ch := make(chan *sessionv1.WatchSessionsResponse, 64)
	o.sessionMu.Lock()
	o.sessionSubs[ch] = struct{}{}
	o.sessionMu.Unlock()
	defer func() {
		o.sessionMu.Lock()
		delete(o.sessionSubs, ch)
		o.sessionMu.Unlock()
	}()

	if eng, _ := o.localEngineFor(""); eng != nil {
		go func() {
			_ = eng.WatchSessions(ctx, o.ownerPrincipal(), req.IncludeArchived, func(m *sessionv1.WatchSessionsResponse) error {
				if !m.Snapshot {
					select {
					case ch <- m:
					case <-ctx.Done():
					}
				}
				return nil
			})
		}()
	}

	if o.rep != nil {
		repCh := o.rep.add()
		defer o.rep.remove(repCh)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case m, ok := <-repCh:
					if !ok {
						return
					}
					if len(m.Sessions) > 0 || len(m.DeletedSessionIds) > 0 {
						select {
						case ch <- &sessionv1.WatchSessionsResponse{Sessions: m.Sessions, DeletedSessionIds: m.DeletedSessionIds}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}()
	}

	initRes, err := s.ListSessions(ctx, &sessionv1.ListSessionsRequest{IncludeArchived: req.IncludeArchived, View: req.View})
	if err != nil {
		return err
	}
	if err := stream.Send(&sessionv1.WatchSessionsResponse{Snapshot: true, Sessions: initRes.Sessions}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-ch:
			if req.View == sessionv1.SessionView_SESSION_VIEW_BASIC {
				msg = &sessionv1.WatchSessionsResponse{
					Snapshot:          msg.Snapshot,
					Sessions:          machine.BasicSessions(msg.Sessions),
					DeletedSessionIds: msg.DeletedSessionIds,
				}
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (s *ownerSessionService) SubscribeSession(ctx context.Context, req *sessionv1.SubscribeSessionRequest, stream *connect.ServerStream[sessionv1.SessionEvent]) error {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return err
	}
	if eng != nil {
		return eng.SubscribeSession(ctx, o.ownerPrincipal(), req.SessionId, req.AfterSeq, stream.Send)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		return err
	}
	rStream, err := pm.Session.SubscribeSession(ctx, req)
	if err != nil {
		return err
	}
	defer rStream.Close()
	for rStream.Receive() {
		ev := rStream.Msg()
		if ev.Session != nil {
			o.cacheSession(ev.Session, nil)
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return rStream.Err()
}

func (s *ownerSessionService) DispatchMessage(ctx context.Context, req *sessionv1.DispatchMessageRequest) (*sessionv1.DispatchMessageResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		for _, r := range req.Attachments {
			var name, mime string
			var data []byte
			if o.st.db.QueryRow(`SELECT name, mime_type, data FROM sessions_blobs WHERE sha256 = ?`, r.BlobSha256).Scan(&name, &mime, &data) == nil && len(data) > 0 {
				_, _ = eng.Store().PutBlob(name, mime, data)
			}
		}
		return eng.DispatchMessage(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteSessionMachine(ctx, machineID); dErr == nil {
		if bErr := o.syncBlobsToRemote(ctx, pm, req.Attachments); bErr == nil {
			res, mErr := pm.Session.DispatchMessage(ctx, req)
			if mErr == nil {
				o.cacheSession(res.GetSession(), nil)
				return res, nil
			}
			if !isOfflineErr(mErr) {
				return nil, mErr
			}
		}
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.SessionId, "DispatchMessage", req.Text, req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.SessionId)
	var sess *sessionv1.Session
	if proj != nil && proj.Session != nil {
		sess = proj.Session
		sess.OfflineQueued = true
		sess.QueuedRunCount++
		sess.UpdatedAt = timestamppb.Now()
	} else {
		sess = &sessionv1.Session{SessionId: req.SessionId, MachineId: machineID, OfflineQueued: true, QueuedRunCount: 1, UpdatedAt: timestamppb.Now()}
	}
	qRun := &sessionv1.Run{
		RunId:     "run_" + strings.ToLower(ulid.Make().String()),
		SessionId: req.SessionId,
		Status:    sessionv1.RunStatus_RUN_STATUS_QUEUED,
		Author:    o.ownerPrincipal().Actor,
		Prompt:    req.Text,
		CreatedAt: timestamppb.Now(),
	}
	if proj != nil {
		proj.Runs = append(proj.Runs, qRun)
	}
	o.cacheSession(sess, proj)
	return &sessionv1.DispatchMessageResponse{
		Session:       sess,
		Run:           qRun,
		QueuedOffline: true,
	}, nil
}

func (s *ownerSessionService) DeliverMessage(_ context.Context, _ *sessionv1.DeliverMessageRequest) (*sessionv1.DeliverMessageResponse, error) {
	return nil, connect.NewError(connect.CodePermissionDenied, errors.New("DeliverMessage is for signed agent inboxes"))
}

func (s *ownerSessionService) InterruptSession(ctx context.Context, req *sessionv1.InterruptSessionRequest) (*sessionv1.InterruptSessionResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.InterruptSession(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Session.InterruptSession(ctx, req)
	if err == nil {
		o.cacheSession(res.GetSession(), nil)
	}
	return res, err
}

func (s *ownerSessionService) StopBackgroundTask(ctx context.Context, req *sessionv1.StopBackgroundTaskRequest) (*sessionv1.StopBackgroundTaskResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.StopBackgroundTask(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Session.StopBackgroundTask(ctx, req)
	if err == nil {
		o.cacheSession(res.GetSession(), nil)
	}
	return res, err
}

func (s *ownerSessionService) ManageQueue(ctx context.Context, req *sessionv1.ManageQueueRequest) (*sessionv1.ManageQueueResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.ManageQueue(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteSessionMachine(ctx, machineID); dErr == nil {
		res, mErr := pm.Session.ManageQueue(ctx, req)
		if mErr == nil {
			o.cacheSession(res.GetSession(), nil)
			return res, nil
		}
		if !isOfflineErr(mErr) {
			return nil, mErr
		}
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.SessionId, "ManageQueue", req.Operation.String(), req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.SessionId)
	return &sessionv1.ManageQueueResponse{Session: proj.GetSession()}, nil
}

func (s *ownerSessionService) UpdateSession(ctx context.Context, req *sessionv1.UpdateSessionRequest) (*sessionv1.UpdateSessionResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.UpdateSession(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteSessionMachine(ctx, machineID); dErr == nil {
		res, uErr := pm.Session.UpdateSession(ctx, req)
		if uErr == nil {
			o.cacheSession(res.GetSession(), nil)
			return res, nil
		}
		if !isOfflineErr(uErr) {
			return nil, uErr
		}
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.SessionId, "UpdateSession", "session update", req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.SessionId)
	return &sessionv1.UpdateSessionResponse{Session: proj.GetSession(), TransitionApplied: "queued_offline"}, nil
}

func (s *ownerSessionService) RespondRuntimeRequest(ctx context.Context, req *sessionv1.RespondRuntimeRequestRequest) (*sessionv1.RespondRuntimeRequestResponse, error) {
	if req.Decline && len(req.Answers) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("declining a question takes no answer content"))
	}
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.RespondRuntimeRequest(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteSessionMachine(ctx, machineID); dErr == nil {
		for _, ans := range req.Answers {
			_ = o.syncBlobsToRemote(ctx, pm, ans.Attachments)
		}
		res, rErr := pm.Session.RespondRuntimeRequest(ctx, req)
		if rErr == nil {
			o.cacheSession(res.GetSession(), nil)
			return res, nil
		}
		if !isOfflineErr(rErr) {
			return nil, rErr
		}
	}
	if req.SecretValue != "" {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("machine is offline; secret values are never stored in offline outbox"))
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.SessionId, "RespondRuntimeRequest", req.RequestId, req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.SessionId)
	return &sessionv1.RespondRuntimeRequestResponse{
		Session:       proj.GetSession(),
		QueuedOffline: true,
	}, nil
}

func (s *ownerSessionService) ListCheckpoints(ctx context.Context, req *sessionv1.ListCheckpointsRequest) (*sessionv1.ListCheckpointsResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		list, err := eng.ListCheckpoints(ctx, o.ownerPrincipal(), req.SessionId)
		if err != nil {
			return nil, err
		}
		return &sessionv1.ListCheckpointsResponse{Checkpoints: list}, nil
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		if proj := o.cachedProjection(req.SessionId); proj != nil {
			return &sessionv1.ListCheckpointsResponse{Checkpoints: proj.Checkpoints}, nil
		}
		return nil, err
	}
	return pm.Session.ListCheckpoints(ctx, req)
}

func (s *ownerSessionService) RollbackCheckpoint(ctx context.Context, req *sessionv1.RollbackCheckpointRequest) (*sessionv1.RollbackCheckpointResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.RollbackCheckpoint(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Session.RollbackCheckpoint(ctx, req)
	if err == nil {
		o.cacheSession(res.GetSession(), nil)
	}
	return res, err
}

func (s *ownerSessionService) GetDiff(ctx context.Context, req *sessionv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.GetDiff(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return pm.Session.GetDiff(ctx, req)
}

func (s *ownerSessionService) ForkSession(ctx context.Context, req *sessionv1.ForkSessionRequest) (*sessionv1.ForkSessionResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SourceSessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.ForkSession(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Session.ForkSession(ctx, req)
	if err == nil {
		o.cacheSession(res.GetSession(), nil)
	}
	return res, err
}

func (s *ownerSessionService) DeleteSession(ctx context.Context, req *sessionv1.DeleteSessionRequest) (*sessionv1.DeleteSessionResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		if err := eng.DeleteSession(ctx, o.ownerPrincipal(), req.SessionId); err != nil {
			return nil, err
		}
		o.removeCachedSession(req.SessionId)
		return &sessionv1.DeleteSessionResponse{}, nil
	}
	if pm, dErr := o.dialRemoteSessionMachine(ctx, machineID); dErr == nil {
		if _, err := pm.Session.DeleteSession(ctx, req); err != nil {
			return nil, err
		}
	}
	_, _ = o.st.db.Exec(`DELETE FROM sessions_outbox WHERE session_id = ?`, req.SessionId)
	o.removeCachedSession(req.SessionId)
	return &sessionv1.DeleteSessionResponse{}, nil
}

func (s *ownerSessionService) UploadBlob(_ context.Context, req *sessionv1.UploadBlobRequest) (*sessionv1.UploadBlobResponse, error) {
	if len(req.Data) > 64<<20 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("attachment exceeds 64 MiB limit"))
	}
	o := s.o()
	if eng, _ := o.localEngineFor(""); eng != nil {
		ref, err := eng.Store().PutBlob(req.Name, req.MimeType, req.Data)
		if err == nil {
			return &sessionv1.UploadBlobResponse{Attachment: ref}, nil
		}
	}
	sum := sha256.Sum256(req.Data)
	digest := hex.EncodeToString(sum[:])
	name := req.Name
	if name == "" {
		name = "attachment"
	}
	mime := req.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	_, err := o.st.db.Exec(
		`INSERT OR IGNORE INTO sessions_blobs(sha256, name, mime_type, data, created_at) VALUES(?, ?, ?, ?, ?)`,
		digest, name, mime, req.Data, time.Now().UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	return &sessionv1.UploadBlobResponse{
		Attachment: &sessionv1.AttachmentRef{
			BlobSha256: digest,
			Name:       name,
			MimeType:   mime,
			SizeBytes:  uint64(len(req.Data)),
		},
	}, nil
}

func (s *ownerSessionService) DownloadBlob(ctx context.Context, req *sessionv1.DownloadBlobRequest) (*sessionv1.DownloadBlobResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		if data, name, mime, err := eng.Store().GetBlob(req.BlobSha256); err == nil {
			return &sessionv1.DownloadBlobResponse{Data: data, Name: name, MimeType: mime, SizeBytes: uint64(len(data))}, nil
		}
		if req.MachineId != "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("blob not found"))
		}
	} else if req.MachineId != "" {
		pm, dErr := o.dialRemoteSessionMachine(ctx, req.MachineId)
		if dErr != nil {
			return nil, dErr
		}
		return pm.Session.DownloadBlob(ctx, req)
	}
	var name, mime string
	var data []byte
	err := o.st.db.QueryRow(`SELECT name, mime_type, data FROM sessions_blobs WHERE sha256 = ?`, req.BlobSha256).Scan(&name, &mime, &data)
	if err == nil {
		return &sessionv1.DownloadBlobResponse{Data: data, Name: name, MimeType: mime, SizeBytes: uint64(len(data))}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("blob not found"))
	}
	return nil, err
}

func (s *ownerSessionService) ListOutbox(_ context.Context, req *sessionv1.ListOutboxRequest) (*sessionv1.ListOutboxResponse, error) {
	o := s.o()
	q := `SELECT command_id, machine_id, session_id, method, summary, last_error, created_at FROM sessions_outbox`
	var args []any
	if req.MachineId != "" {
		q += ` WHERE machine_id = ?`
		args = append(args, req.MachineId)
	}
	q += ` ORDER BY seq ASC`
	rows, err := o.st.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sessionv1.OutboxEntry
	for rows.Next() {
		var e sessionv1.OutboxEntry
		var createdMs int64
		if rows.Scan(&e.CommandId, &e.MachineId, &e.SessionId, &e.Method, &e.Summary, &e.LastError, &createdMs) == nil {
			e.CreatedAt = timestamppb.New(time.UnixMilli(createdMs))
			out = append(out, &e)
		}
	}
	return &sessionv1.ListOutboxResponse{Entries: out}, nil
}

// broadcastLocalSessionChange notifies WatchSessions and Watch screens when an
// offline-queued session changes locally.
func (r *replica) broadcastLocalSessionChange(sessions []*sessionv1.Session, deleted []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src != nil && (len(sessions) > 0 || len(deleted) > 0) {
		r.broadcastLocked(r.src, &localv1.WatchSessionsResponse{Sessions: sessions, DeletedSessionIds: deleted})
	}
	r.kickLocked()
}
