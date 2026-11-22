package owner

import (
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
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2/threadv2connect"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/kernel/policy"
	"github.com/hermeshai/hermesh/internal/peer"
)

const threadV2Migrations = `
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

type ownerThreadService struct {
	d *Device
}

var _ threadv2connect.ThreadServiceHandler = (*ownerThreadService)(nil)

func (s *ownerThreadService) o() *Owner { return s.d.owner() }

func (o *Owner) ownerPrincipal() *kernel.Principal {
	o.mu.Lock()
	id := o.ownerID
	o.mu.Unlock()
	if id == "" {
		id = "local_owner"
	}
	return kernel.OwnerPrincipal(id, "Owner")
}

// localEngineFor returns the in-process kernel.Engine if machineID resolves to
// this computer's own execution machine (or when machineID == "" and localMachine is active).
func (o *Owner) localEngineFor(machineID string) (*kernel.Engine, string) {
	o.mu.Lock()
	lm := o.localMachine
	o.mu.Unlock()
	if lm == nil || lm.Threads() == nil {
		return nil, ""
	}
	if machineID == "" || machineID == lm.MachineID() {
		return lm.Threads(), lm.MachineID()
	}
	return nil, ""
}

// resolveMachineForThread finds which machine owns threadID: checks localMachine,
// cached replica rows, and deterministic thread_id prefix matching.
func (o *Owner) resolveMachineForThread(ctx context.Context, threadID string) (localEng *kernel.Engine, machineID string, err error) {
	o.mu.Lock()
	lm := o.localMachine
	o.mu.Unlock()
	if lm != nil && lm.Threads() != nil {
		if kernel.OwnsThread(lm.MachineID(), threadID) {
			return lm.Threads(), lm.MachineID(), nil
		}
		if _, err := lm.Threads().Store().Thread(threadID); err == nil {
			return lm.Threads(), lm.MachineID(), nil
		}
	}
	var cachedMachine string
	if qErr := o.st.db.QueryRow(`SELECT machine_id FROM threads_v2_cache WHERE thread_id = ?`, threadID).Scan(&cachedMachine); qErr == nil && cachedMachine != "" {
		if eng, mid := o.localEngineFor(cachedMachine); eng != nil {
			return eng, mid, nil
		}
		return nil, cachedMachine, nil
	}
	machines, mErr := o.ListMachines(ctx)
	if mErr == nil {
		for _, m := range machines {
			if kernel.OwnsThread(m.MachineId, threadID) {
				if eng, mid := o.localEngineFor(m.MachineId); eng != nil {
					return eng, mid, nil
				}
				return nil, m.MachineId, nil
			}
		}
	}
	return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("thread %s not found on any known machine", threadID))
}

func (o *Owner) dialRemoteThreadMachine(ctx context.Context, machineID string) (*peer.Machine, error) {
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

func (o *Owner) cacheThread(th *threadv2.Thread, proj *threadv2.ThreadProjection) {
	if th == nil || th.ThreadId == "" {
		return
	}
	tb, err := proto.Marshal(th)
	if err != nil {
		return
	}
	var pb []byte
	if proj != nil {
		pb, _ = proto.Marshal(proj)
	}
	archived := 0
	if th.Status == threadv2.ThreadStatus_THREAD_STATUS_ARCHIVED || th.ArchivedAt != nil {
		archived = 1
	}
	updatedMs := time.Now().UnixMilli()
	if th.UpdatedAt != nil {
		updatedMs = th.UpdatedAt.AsTime().UnixMilli()
	}
	if pb != nil {
		_, _ = o.st.db.Exec(
			`INSERT INTO threads_v2_cache(thread_id, machine_id, archived, updated_at, thread_payload, projection_payload)
			 VALUES(?, ?, ?, ?, ?, ?)
			 ON CONFLICT(thread_id) DO UPDATE SET
			   machine_id=excluded.machine_id, archived=excluded.archived, updated_at=excluded.updated_at,
			   thread_payload=excluded.thread_payload, projection_payload=excluded.projection_payload`,
			th.ThreadId, th.MachineId, archived, updatedMs, tb, pb,
		)
	} else {
		_, _ = o.st.db.Exec(
			`INSERT INTO threads_v2_cache(thread_id, machine_id, archived, updated_at, thread_payload)
			 VALUES(?, ?, ?, ?, ?)
			 ON CONFLICT(thread_id) DO UPDATE SET
			   machine_id=excluded.machine_id, archived=excluded.archived, updated_at=excluded.updated_at,
			   thread_payload=excluded.thread_payload`,
			th.ThreadId, th.MachineId, archived, updatedMs, tb,
		)
	}
	o.notifyThreadSubs(&threadv2.SubscribeThreadsResponse{Threads: []*threadv2.Thread{th}})
}

func (o *Owner) removeCachedThread(threadID string) {
	_, _ = o.st.db.Exec(`DELETE FROM threads_v2_cache WHERE thread_id = ?`, threadID)
	o.notifyThreadSubs(&threadv2.SubscribeThreadsResponse{DeletedThreadIds: []string{threadID}})
}

func (o *Owner) cachedThreads(includeArchived bool) []*threadv2.Thread {
	q := `SELECT thread_payload FROM threads_v2_cache`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := o.st.db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*threadv2.Thread
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) == nil {
			t := &threadv2.Thread{}
			if proto.Unmarshal(b, t) == nil {
				out = append(out, t)
			}
		}
	}
	return out
}

func (o *Owner) cachedProjection(threadID string) *threadv2.ThreadProjection {
	var tb, pb []byte
	err := o.st.db.QueryRow(`SELECT thread_payload, projection_payload FROM threads_v2_cache WHERE thread_id = ?`, threadID).Scan(&tb, &pb)
	if err != nil {
		return nil
	}
	if len(pb) > 0 {
		proj := &threadv2.ThreadProjection{}
		if proto.Unmarshal(pb, proj) == nil {
			if len(tb) > 0 {
				th := &threadv2.Thread{}
				if proto.Unmarshal(tb, th) == nil {
					proj.Thread = th
				}
			}
			return proj
		}
	}
	if len(tb) > 0 {
		th := &threadv2.Thread{}
		if proto.Unmarshal(tb, th) == nil {
			return &threadv2.ThreadProjection{Thread: th}
		}
	}
	return nil
}

func (o *Owner) notifyThreadSubs(msg *threadv2.SubscribeThreadsResponse) {
	o.threadMu.Lock()
	defer o.threadMu.Unlock()
	for ch := range o.threadSubs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (o *Owner) wakeThreadOutbox() {
	select {
	case o.threadWake <- struct{}{}:
	default:
	}
}

func (o *Owner) enqueueOutbox(commandID, machineID, threadID, method, summary string, req proto.Message) error {
	if commandID == "" {
		commandID = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	_, err = o.st.db.Exec(
		`INSERT OR IGNORE INTO threads_v2_outbox(command_id, machine_id, thread_id, method, summary, req_payload, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		commandID, machineID, threadID, method, summary, b, time.Now().UnixMilli(),
	)
	if err == nil {
		o.wakeThreadOutbox()
	}
	return err
}

func (o *Owner) syncBlobsToRemote(ctx context.Context, pm *peer.Machine, refs []*threadv2.AttachmentRef) error {
	for _, r := range refs {
		var name, mime string
		var data []byte
		err := o.st.db.QueryRow(`SELECT name, mime_type, data FROM threads_v2_blobs WHERE sha256 = ?`, r.BlobSha256).Scan(&name, &mime, &data)
		if err != nil {
			if eng, _ := o.localEngineFor(""); eng != nil {
				data, name, mime, err = eng.Store().GetBlob(r.BlobSha256)
			}
		}
		if err == nil && len(data) > 0 {
			if _, uErr := pm.Thread.UploadBlob(ctx, &threadv2.UploadBlobRequest{
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

// flushThreadOutboxLoop drains durable offline commands in FIFO order per machine
// whenever connectivity returns.
func (o *Owner) flushThreadOutboxLoop(ctx context.Context) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.threadWake:
		case <-ticker.C:
		}
		o.flushThreadOutboxOnce(ctx)
	}
}

type outboxRow struct {
	seq       int64
	commandID string
	machineID string
	threadID  string
	method    string
	payload   []byte
}

func (o *Owner) flushThreadOutboxOnce(ctx context.Context) {
	rows, err := o.st.db.Query(`SELECT seq, command_id, machine_id, thread_id, method, req_payload FROM threads_v2_outbox ORDER BY seq ASC`)
	if err != nil {
		return
	}
	var list []outboxRow
	for rows.Next() {
		var r outboxRow
		if rows.Scan(&r.seq, &r.commandID, &r.machineID, &r.threadID, &r.method, &r.payload) == nil {
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
		pm, err := o.dialRemoteThreadMachine(ctx, item.machineID)
		if err != nil {
			skipMachine[item.machineID] = true
			continue
		}
		err = o.replayOutboxItem(ctx, pm, item)
		if err == nil {
			_, _ = o.st.db.Exec(`DELETE FROM threads_v2_outbox WHERE seq = ?`, item.seq)
			continue
		}
		_, _ = o.st.db.Exec(`UPDATE threads_v2_outbox SET attempts = attempts + 1, last_error = ? WHERE seq = ?`, err.Error(), item.seq)
		if isOfflineErr(err) {
			skipMachine[item.machineID] = true
		} else {
			// Permanent terminal error (e.g. PermissionDenied / NotFound): drop from outbox after recording error on cached thread.
			_, _ = o.st.db.Exec(`DELETE FROM threads_v2_outbox WHERE seq = ?`, item.seq)
		}
	}
}

func (o *Owner) replayOutboxItem(ctx context.Context, pm *peer.Machine, item outboxRow) error {
	switch item.method {
	case "LaunchThread":
		req := &threadv2.LaunchThreadRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		if err := o.syncBlobsToRemote(ctx, pm, req.Attachments); err != nil {
			return err
		}
		res, err := pm.Thread.LaunchThread(ctx, req)
		if err != nil {
			return err
		}
		realID := res.GetThread().GetThreadId()
		if item.threadID != "" && realID != "" && item.threadID != realID {
			// Rewrite any subsequent queued outbox commands referencing the temporary offline thread ID!
			o.rewriteOutboxThreadID(item.threadID, realID)
			o.removeCachedThread(item.threadID)
		}
		if proj, gErr := pm.Thread.GetThread(ctx, &threadv2.GetThreadRequest{ThreadId: realID}); gErr == nil {
			o.cacheThread(proj.Projection.GetThread(), proj.Projection)
		} else {
			o.cacheThread(res.GetThread(), nil)
		}
		return nil

	case "DispatchMessage":
		req := &threadv2.DispatchMessageRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		if err := o.syncBlobsToRemote(ctx, pm, req.Attachments); err != nil {
			return err
		}
		res, err := pm.Thread.DispatchMessage(ctx, req)
		if err != nil {
			return err
		}
		o.cacheThread(res.GetThread(), nil)
		return nil

	case "ManageQueue":
		req := &threadv2.ManageQueueRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		res, err := pm.Thread.ManageQueue(ctx, req)
		if err != nil {
			return err
		}
		o.cacheThread(res.GetThread(), nil)
		return nil

	case "UpdateThreadConfig":
		req := &threadv2.UpdateThreadConfigRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		res, err := pm.Thread.UpdateThreadConfig(ctx, req)
		if err != nil {
			return err
		}
		o.cacheThread(res.GetThread(), nil)
		return nil

	case "RespondRuntimeRequest":
		req := &threadv2.RespondRuntimeRequestRequest{}
		if err := proto.Unmarshal(item.payload, req); err != nil {
			return err
		}
		for _, ans := range req.Answers {
			if err := o.syncBlobsToRemote(ctx, pm, ans.Attachments); err != nil {
				return err
			}
		}
		res, err := pm.Thread.RespondRuntimeRequest(ctx, req)
		if err != nil {
			return err
		}
		o.cacheThread(res.GetThread(), nil)
		return nil
	}
	return nil
}

func (o *Owner) rewriteOutboxThreadID(oldID, newID string) {
	rows, err := o.st.db.Query(`SELECT seq, method, req_payload FROM threads_v2_outbox WHERE thread_id = ?`, oldID)
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
			m := &threadv2.DispatchMessageRequest{}
			if proto.Unmarshal(raw, m) == nil {
				m.ThreadId = newID
				b, _ := proto.Marshal(m)
				patches = append(patches, patch{seq, b})
			}
		case "ManageQueue":
			m := &threadv2.ManageQueueRequest{}
			if proto.Unmarshal(raw, m) == nil {
				m.ThreadId = newID
				b, _ := proto.Marshal(m)
				patches = append(patches, patch{seq, b})
			}
		case "UpdateThreadConfig":
			m := &threadv2.UpdateThreadConfigRequest{}
			if proto.Unmarshal(raw, m) == nil {
				m.ThreadId = newID
				b, _ := proto.Marshal(m)
				patches = append(patches, patch{seq, b})
			}
		}
	}
	rows.Close()
	for _, p := range patches {
		_, _ = o.st.db.Exec(`UPDATE threads_v2_outbox SET thread_id = ?, req_payload = ? WHERE seq = ?`, newID, p.payload, p.seq)
	}
}

// ---------------------------------------------------------------------------
// Owner ThreadServiceHandler Implementation

func (s *ownerThreadService) ListProviders(ctx context.Context, req *threadv2.ListProvidersRequest) (*threadv2.ListProvidersResponse, error) {
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
		return &threadv2.ListProvidersResponse{MaxRuntimeMode: identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS}, nil
	}
	pm, err := o.dialRemoteThreadMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	return pm.Thread.ListProviders(ctx, req)
}

func (s *ownerThreadService) UpsertProviderInstance(ctx context.Context, req *threadv2.UpsertProviderInstanceRequest) (*threadv2.UpsertProviderInstanceResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		inst, err := eng.UpsertProviderInstance(ctx, o.ownerPrincipal(), req.Config)
		if err != nil {
			return nil, err
		}
		return &threadv2.UpsertProviderInstanceResponse{Provider: inst}, nil
	}
	pm, err := o.dialRemoteThreadMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	return pm.Thread.UpsertProviderInstance(ctx, req)
}

func (s *ownerThreadService) RemoveProviderInstance(ctx context.Context, req *threadv2.RemoveProviderInstanceRequest) (*threadv2.RemoveProviderInstanceResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		return &threadv2.RemoveProviderInstanceResponse{}, eng.RemoveProviderInstance(ctx, o.ownerPrincipal(), req.InstanceId)
	}
	pm, err := o.dialRemoteThreadMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	return pm.Thread.RemoveProviderInstance(ctx, req)
}

func (s *ownerThreadService) LaunchThread(ctx context.Context, req *threadv2.LaunchThreadRequest) (*threadv2.LaunchThreadResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	if eng, _ := o.localEngineFor(req.MachineId); eng != nil {
		// Copy any owner-uploaded blobs into local engine store first.
		for _, r := range req.Attachments {
			var name, mime string
			var data []byte
			if o.st.db.QueryRow(`SELECT name, mime_type, data FROM threads_v2_blobs WHERE sha256 = ?`, r.BlobSha256).Scan(&name, &mime, &data) == nil && len(data) > 0 {
				_, _ = eng.Store().PutBlob(name, mime, data)
			}
		}
		return eng.LaunchThread(ctx, o.ownerPrincipal(), req)
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
	pm, err := o.dialRemoteThreadMachine(ctx, targetMachine)
	if err == nil {
		if bErr := o.syncBlobsToRemote(ctx, pm, req.Attachments); bErr == nil {
			res, lErr := pm.Thread.LaunchThread(ctx, req)
			if lErr == nil {
				o.cacheThread(res.GetThread(), nil)
				return res, nil
			}
			if !isOfflineErr(lErr) {
				return nil, lErr
			}
		}
	} else if !isOfflineErr(err) {
		return nil, err
	}

	// Machine is offline -> persist in durable Outbox and create optimistic local thread!
	tempID := "th_" + kernel.ThreadPrefix(targetMachine) + "_" + strings.ToLower(ulid.Make().String())
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.InitialPrompt)
		if title == "" {
			title = "New Thread"
		}
	}
	now := timestamppb.Now()
	optThread := &threadv2.Thread{
		ThreadId:             tempID,
		MachineId:            targetMachine,
		ProjectId:            req.ProjectId,
		Title:                title,
		Status:               threadv2.ThreadStatus_THREAD_STATUS_QUEUED,
		ModelSelection:       req.ModelSelection,
		RuntimeMode:          policy.NormalizeRequested(req.RuntimeMode),
		EffectiveRuntimeMode: policy.NormalizeRequested(req.RuntimeMode),
		MaxRuntimeMode:       identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		InteractionMode:      req.InteractionMode,
		Workspace:            req.Workspace,
		CreatedBy:            o.ownerPrincipal().Actor,
		OfflineQueued:        true,
		QueuedRunCount:       1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	optRun := &threadv2.Run{
		RunId:     "run_" + strings.ToLower(ulid.Make().String()),
		ThreadId:  tempID,
		Status:    threadv2.RunStatus_RUN_STATUS_QUEUED,
		Author:    o.ownerPrincipal().Actor,
		Prompt:    req.InitialPrompt,
		CreatedAt: now,
	}
	proj := &threadv2.ThreadProjection{
		Thread: optThread,
		Runs:   []*threadv2.Run{optRun},
	}
	if err := o.enqueueOutbox(req.CommandId, targetMachine, tempID, "LaunchThread", title, req); err != nil {
		return nil, err
	}
	o.cacheThread(optThread, proj)
	return &threadv2.LaunchThreadResponse{
		Thread:        optThread,
		InitialRun:    optRun,
		QueuedOffline: true,
	}, nil
}

func (s *ownerThreadService) GetThread(ctx context.Context, req *threadv2.GetThreadRequest) (*threadv2.GetThreadResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		proj, err := eng.GetThread(ctx, o.ownerPrincipal(), req.ThreadId)
		if err != nil {
			return nil, err
		}
		return &threadv2.GetThreadResponse{Projection: proj}, nil
	}
	if pm, dErr := o.dialRemoteThreadMachine(ctx, machineID); dErr == nil {
		if res, gErr := pm.Thread.GetThread(ctx, req); gErr == nil {
			o.cacheThread(res.Projection.GetThread(), res.Projection)
			return res, nil
		} else if !isOfflineErr(gErr) {
			return nil, gErr
		}
	}
	if cached := o.cachedProjection(req.ThreadId); cached != nil {
		return &threadv2.GetThreadResponse{Projection: cached}, nil
	}
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("machine is offline and thread is not cached locally"))
}

func (s *ownerThreadService) ListThreads(ctx context.Context, req *threadv2.ListThreadsRequest) (*threadv2.ListThreadsResponse, error) {
	o := s.o()
	byID := map[string]*threadv2.Thread{}
	for _, ct := range o.cachedThreads(req.IncludeArchived) {
		if (req.MachineId == "" || ct.MachineId == req.MachineId) && (req.ProjectId == "" || ct.ProjectId == req.ProjectId) {
			byID[ct.ThreadId] = ct
		}
	}
	if eng, localID := o.localEngineFor(""); eng != nil && (req.MachineId == "" || req.MachineId == localID) {
		if list, err := eng.ListThreads(ctx, o.ownerPrincipal(), req.ProjectId, req.IncludeArchived, req.Limit); err == nil {
			for _, t := range list {
				byID[t.ThreadId] = t
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
		if pm, err := o.dialRemoteThreadMachine(ctx, m.MachineId); err == nil {
			if rList, lErr := pm.Thread.ListThreads(ctx, req); lErr == nil {
				for _, t := range rList.Threads {
					byID[t.ThreadId] = t
					o.cacheThread(t, nil)
				}
			}
		}
	}
	out := make([]*threadv2.Thread, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b *threadv2.Thread) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		return b.GetUpdatedAt().AsTime().Compare(a.GetUpdatedAt().AsTime())
	})
	if req.Limit > 0 && len(out) > int(req.Limit) {
		out = out[:req.Limit]
	}
	return &threadv2.ListThreadsResponse{Threads: out}, nil
}

func (s *ownerThreadService) SubscribeThreads(ctx context.Context, req *threadv2.SubscribeThreadsRequest, stream *connect.ServerStream[threadv2.SubscribeThreadsResponse]) error {
	o := s.o()
	ch := make(chan *threadv2.SubscribeThreadsResponse, 64)
	o.threadMu.Lock()
	o.threadSubs[ch] = struct{}{}
	o.threadMu.Unlock()
	defer func() {
		o.threadMu.Lock()
		delete(o.threadSubs, ch)
		o.threadMu.Unlock()
	}()

	// Also bridge localEngine live updates into ch!
	if eng, _ := o.localEngineFor(""); eng != nil {
		go func() {
			_ = eng.SubscribeThreads(ctx, o.ownerPrincipal(), req.IncludeArchived, func(m *threadv2.SubscribeThreadsResponse) error {
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

	initRes, err := s.ListThreads(ctx, &threadv2.ListThreadsRequest{IncludeArchived: req.IncludeArchived})
	if err != nil {
		return err
	}
	if err := stream.Send(&threadv2.SubscribeThreadsResponse{Snapshot: true, Threads: initRes.Threads}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-ch:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (s *ownerThreadService) SubscribeThread(ctx context.Context, req *threadv2.SubscribeThreadRequest, stream *connect.ServerStream[threadv2.ThreadEvent]) error {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return err
	}
	if eng != nil {
		return eng.SubscribeThread(ctx, o.ownerPrincipal(), req.ThreadId, req.AfterSeq, stream.Send)
	}
	pm, err := o.dialRemoteThreadMachine(ctx, machineID)
	if err != nil {
		return err
	}
	rStream, err := pm.Thread.SubscribeThread(ctx, req)
	if err != nil {
		return err
	}
	defer rStream.Close()
	for rStream.Receive() {
		ev := rStream.Msg()
		if ev.Thread != nil {
			o.cacheThread(ev.Thread, nil)
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return rStream.Err()
}

func (s *ownerThreadService) DispatchMessage(ctx context.Context, req *threadv2.DispatchMessageRequest) (*threadv2.DispatchMessageResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		for _, r := range req.Attachments {
			var name, mime string
			var data []byte
			if o.st.db.QueryRow(`SELECT name, mime_type, data FROM threads_v2_blobs WHERE sha256 = ?`, r.BlobSha256).Scan(&name, &mime, &data) == nil && len(data) > 0 {
				_, _ = eng.Store().PutBlob(name, mime, data)
			}
		}
		return eng.DispatchMessage(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteThreadMachine(ctx, machineID); dErr == nil {
		if bErr := o.syncBlobsToRemote(ctx, pm, req.Attachments); bErr == nil {
			res, mErr := pm.Thread.DispatchMessage(ctx, req)
			if mErr == nil {
				o.cacheThread(res.GetThread(), nil)
				return res, nil
			}
			if !isOfflineErr(mErr) {
				return nil, mErr
			}
		}
	}
	// Offline queue into durable Outbox!
	if err := o.enqueueOutbox(req.CommandId, machineID, req.ThreadId, "DispatchMessage", req.Text, req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.ThreadId)
	var th *threadv2.Thread
	if proj != nil && proj.Thread != nil {
		th = proj.Thread
		th.OfflineQueued = true
		th.QueuedRunCount++
		th.UpdatedAt = timestamppb.Now()
	} else {
		th = &threadv2.Thread{ThreadId: req.ThreadId, MachineId: machineID, OfflineQueued: true, QueuedRunCount: 1, UpdatedAt: timestamppb.Now()}
	}
	qRun := &threadv2.Run{
		RunId:     "run_" + strings.ToLower(ulid.Make().String()),
		ThreadId:  req.ThreadId,
		Status:    threadv2.RunStatus_RUN_STATUS_QUEUED,
		Author:    o.ownerPrincipal().Actor,
		Prompt:    req.Text,
		CreatedAt: timestamppb.Now(),
	}
	if proj != nil {
		proj.Runs = append(proj.Runs, qRun)
	}
	o.cacheThread(th, proj)
	return &threadv2.DispatchMessageResponse{
		Thread:        th,
		Run:           qRun,
		QueuedOffline: true,
	}, nil
}

func (s *ownerThreadService) InterruptRun(ctx context.Context, req *threadv2.InterruptRunRequest) (*threadv2.InterruptRunResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.InterruptRun(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteThreadMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Thread.InterruptRun(ctx, req)
	if err == nil {
		o.cacheThread(res.GetThread(), nil)
	}
	return res, err
}

func (s *ownerThreadService) ManageQueue(ctx context.Context, req *threadv2.ManageQueueRequest) (*threadv2.ManageQueueResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.ManageQueue(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteThreadMachine(ctx, machineID); dErr == nil {
		res, mErr := pm.Thread.ManageQueue(ctx, req)
		if mErr == nil {
			o.cacheThread(res.GetThread(), nil)
			return res, nil
		}
		if !isOfflineErr(mErr) {
			return nil, mErr
		}
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.ThreadId, "ManageQueue", req.Operation.String(), req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.ThreadId)
	return &threadv2.ManageQueueResponse{Thread: proj.GetThread()}, nil
}

func (s *ownerThreadService) UpdateThreadConfig(ctx context.Context, req *threadv2.UpdateThreadConfigRequest) (*threadv2.UpdateThreadConfigResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.UpdateThreadConfig(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteThreadMachine(ctx, machineID); dErr == nil {
		res, uErr := pm.Thread.UpdateThreadConfig(ctx, req)
		if uErr == nil {
			o.cacheThread(res.GetThread(), nil)
			return res, nil
		}
		if !isOfflineErr(uErr) {
			return nil, uErr
		}
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.ThreadId, "UpdateThreadConfig", "config update", req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.ThreadId)
	return &threadv2.UpdateThreadConfigResponse{Thread: proj.GetThread(), TransitionApplied: "queued_offline"}, nil
}

func (s *ownerThreadService) RespondRuntimeRequest(ctx context.Context, req *threadv2.RespondRuntimeRequestRequest) (*threadv2.RespondRuntimeRequestResponse, error) {
	o := s.o()
	if req.CommandId == "" {
		req.CommandId = "cmd_" + strings.ToLower(ulid.Make().String())
	}
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.RespondRuntimeRequest(ctx, o.ownerPrincipal(), req)
	}
	if pm, dErr := o.dialRemoteThreadMachine(ctx, machineID); dErr == nil {
		for _, ans := range req.Answers {
			_ = o.syncBlobsToRemote(ctx, pm, ans.Attachments)
		}
		res, rErr := pm.Thread.RespondRuntimeRequest(ctx, req)
		if rErr == nil {
			o.cacheThread(res.GetThread(), nil)
			return res, nil
		}
		if !isOfflineErr(rErr) {
			return nil, rErr
		}
	}
	// Never persist SecretValue into SQLite outbox!
	if req.SecretValue != "" {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("machine is offline; secret values are never stored in offline outbox"))
	}
	if err := o.enqueueOutbox(req.CommandId, machineID, req.ThreadId, "RespondRuntimeRequest", req.RequestId, req); err != nil {
		return nil, err
	}
	proj := o.cachedProjection(req.ThreadId)
	return &threadv2.RespondRuntimeRequestResponse{
		Thread:        proj.GetThread(),
		QueuedOffline: true,
	}, nil
}

func (s *ownerThreadService) ListCheckpoints(ctx context.Context, req *threadv2.ListCheckpointsRequest) (*threadv2.ListCheckpointsResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		list, err := eng.ListCheckpoints(ctx, o.ownerPrincipal(), req.ThreadId)
		if err != nil {
			return nil, err
		}
		return &threadv2.ListCheckpointsResponse{Checkpoints: list}, nil
	}
	pm, err := o.dialRemoteThreadMachine(ctx, machineID)
	if err != nil {
		if proj := o.cachedProjection(req.ThreadId); proj != nil {
			return &threadv2.ListCheckpointsResponse{Checkpoints: proj.Checkpoints}, nil
		}
		return nil, err
	}
	return pm.Thread.ListCheckpoints(ctx, req)
}

func (s *ownerThreadService) RollbackCheckpoint(ctx context.Context, req *threadv2.RollbackCheckpointRequest) (*threadv2.RollbackCheckpointResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.RollbackCheckpoint(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteThreadMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Thread.RollbackCheckpoint(ctx, req)
	if err == nil {
		o.cacheThread(res.GetThread(), nil)
	}
	return res, err
}

func (s *ownerThreadService) GetThreadDiff(ctx context.Context, req *threadv2.GetThreadDiffRequest) (*threadv2.GetThreadDiffResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.GetThreadDiff(ctx, o.ownerPrincipal(), req.ThreadId, req.CheckpointId)
	}
	pm, err := o.dialRemoteThreadMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return pm.Thread.GetThreadDiff(ctx, req)
}

func (s *ownerThreadService) ForkThread(ctx context.Context, req *threadv2.ForkThreadRequest) (*threadv2.ForkThreadResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.SourceThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		return eng.ForkThread(ctx, o.ownerPrincipal(), req)
	}
	pm, err := o.dialRemoteThreadMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := pm.Thread.ForkThread(ctx, req)
	if err == nil {
		o.cacheThread(res.GetThread(), nil)
	}
	return res, err
}

func (s *ownerThreadService) DeleteThread(ctx context.Context, req *threadv2.DeleteThreadRequest) (*threadv2.DeleteThreadResponse, error) {
	o := s.o()
	eng, machineID, err := o.resolveMachineForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if eng != nil {
		if err := eng.DeleteThread(ctx, o.ownerPrincipal(), req.ThreadId); err != nil {
			return nil, err
		}
		o.removeCachedThread(req.ThreadId)
		return &threadv2.DeleteThreadResponse{}, nil
	}
	if pm, dErr := o.dialRemoteThreadMachine(ctx, machineID); dErr == nil {
		if _, err := pm.Thread.DeleteThread(ctx, req); err != nil {
			return nil, err
		}
	}
	_, _ = o.st.db.Exec(`DELETE FROM threads_v2_outbox WHERE thread_id = ?`, req.ThreadId)
	o.removeCachedThread(req.ThreadId)
	return &threadv2.DeleteThreadResponse{}, nil
}

func (s *ownerThreadService) UploadBlob(_ context.Context, req *threadv2.UploadBlobRequest) (*threadv2.UploadBlobResponse, error) {
	if len(req.Data) > 64<<20 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("attachment exceeds 64 MiB limit"))
	}
	o := s.o()
	if eng, _ := o.localEngineFor(""); eng != nil {
		ref, err := eng.Store().PutBlob(req.Name, req.MimeType, req.Data)
		if err == nil {
			return &threadv2.UploadBlobResponse{Attachment: ref}, nil
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
		`INSERT OR IGNORE INTO threads_v2_blobs(sha256, name, mime_type, data, created_at) VALUES(?, ?, ?, ?, ?)`,
		digest, name, mime, req.Data, time.Now().UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	return &threadv2.UploadBlobResponse{
		Attachment: &threadv2.AttachmentRef{
			BlobSha256: digest,
			Name:       name,
			MimeType:   mime,
			SizeBytes:  uint64(len(req.Data)),
		},
	}, nil
}

func (s *ownerThreadService) DownloadBlob(ctx context.Context, req *threadv2.DownloadBlobRequest) (*threadv2.DownloadBlobResponse, error) {
	o := s.o()
	if eng, _ := o.localEngineFor(""); eng != nil {
		if data, name, mime, err := eng.Store().GetBlob(req.BlobSha256); err == nil {
			return &threadv2.DownloadBlobResponse{Data: data, Name: name, MimeType: mime}, nil
		}
	}
	var name, mime string
	var data []byte
	err := o.st.db.QueryRow(`SELECT name, mime_type, data FROM threads_v2_blobs WHERE sha256 = ?`, req.BlobSha256).Scan(&name, &mime, &data)
	if err == nil {
		return &threadv2.DownloadBlobResponse{Data: data, Name: name, MimeType: mime}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("blob not found"))
	}
	_ = ctx
	return nil, err
}

func (s *ownerThreadService) ListOutbox(_ context.Context, req *threadv2.ListOutboxRequest) (*threadv2.ListOutboxResponse, error) {
	o := s.o()
	q := `SELECT command_id, machine_id, thread_id, method, summary, last_error, created_at FROM threads_v2_outbox`
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
	var out []*threadv2.OutboxEntry
	for rows.Next() {
		var e threadv2.OutboxEntry
		var createdMs int64
		if rows.Scan(&e.CommandId, &e.MachineId, &e.ThreadId, &e.Method, &e.Summary, &e.LastError, &createdMs) == nil {
			e.CreatedAt = timestamppb.New(time.UnixMilli(createdMs))
			out = append(out, &e)
		}
	}
	return &threadv2.ListOutboxResponse{Entries: out}, nil
}
