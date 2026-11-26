package owner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/internal/peer"
)

// A session's files for the app beside this owner (docs/session-files.md §6.3): the
// same code on the Mac's hermeshd and in the iPhone. A request goes to the session's
// machine, as GetDiff does; what came back is kept, so what a device opened once opens
// with the machine away. A turn's bytes never change, so those are served from here.

const sessionFilesMigrations = `
CREATE TABLE session_files_kept (
  key TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  payload BLOB NOT NULL,
  size INTEGER NOT NULL,
  used_at INTEGER NOT NULL
);
CREATE INDEX idx_session_files_kept_used ON session_files_kept(used_at);
CREATE INDEX idx_session_files_kept_session ON session_files_kept(session_id);
`

// What this device keeps of sessions' files at most; the least recently opened go first.
var sessionFilesKeptMax int64 = 200 << 20

func filesKey(method, machineID, sessionID, runID string, parts ...string) string {
	return strings.Join(append([]string{method, machineID, sessionID, runID}, parts...), "\x00")
}

func (o *Owner) keptFiles(key string, into proto.Message) bool {
	var b []byte
	if o.st.db.QueryRow(`SELECT payload FROM session_files_kept WHERE key = ?`, key).Scan(&b) != nil {
		return false
	}
	if proto.Unmarshal(b, into) != nil {
		return false
	}
	_, _ = o.st.db.Exec(`UPDATE session_files_kept SET used_at = ? WHERE key = ?`, time.Now().UnixMilli(), key)
	return true
}

func (o *Owner) keepFiles(key, sessionID string, m proto.Message) {
	b, err := proto.Marshal(m)
	if err != nil || int64(len(b)) > sessionFilesKeptMax/4 {
		return
	}
	_, _ = o.st.db.Exec(`INSERT INTO session_files_kept(key, session_id, payload, size, used_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET payload = excluded.payload, size = excluded.size, used_at = excluded.used_at`,
		key, sessionID, b, len(b), time.Now().UnixMilli())
	var total int64
	_ = o.st.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM session_files_kept`).Scan(&total)
	for total > sessionFilesKeptMax {
		var oldest string
		var size int64
		if o.st.db.QueryRow(`SELECT key, size FROM session_files_kept ORDER BY used_at ASC LIMIT 1`).Scan(&oldest, &size) != nil {
			return
		}
		_, _ = o.st.db.Exec(`DELETE FROM session_files_kept WHERE key = ?`, oldest)
		total -= size
	}
}

func (o *Owner) forgetFiles(sessionID string) {
	_, _ = o.st.db.Exec(`DELETE FROM session_files_kept WHERE session_id = ?`, sessionID)
}

// filesCall asks the session's machine: this computer's own engine directly, another
// through its connection. Answers are kept; with the machine away, the kept answer.
func filesCall[Res proto.Message](ctx context.Context, o *Owner, sessionID, machineHint string, key func(machineID string) string,
	local func(*kernel.Engine) (Res, error), remote func(*peer.Machine) (Res, error), newRes func() Res,
) (Res, error) {
	var zero Res
	eng, machineID, err := o.resolveMachineForSession(ctx, sessionID)
	if err != nil {
		if machineHint == "" {
			return zero, err
		}
		machineID = machineHint
	}
	if eng != nil {
		return local(eng)
	}
	pm, err := o.dialRemoteSessionMachine(ctx, machineID)
	var res Res
	if err == nil {
		res, err = remote(pm)
	}
	if err == nil {
		o.keepFiles(key(machineID), sessionID, res)
		return res, nil
	}
	if isOfflineErr(err) {
		if kept := newRes(); o.keptFiles(key(machineID), kept) {
			return kept, nil
		}
	}
	return zero, err
}

func resolvedAtTurn(res *sessionv1.ResolveFilesResponse) bool {
	for _, f := range res.GetFiles() {
		if f.GetTurn() > 0 {
			return true
		}
	}
	return false
}

func (o *Owner) runHasCheckpoint(sessionID, runID string) bool {
	if runID == "" {
		return false
	}
	proj := o.cachedProjectionOnly(sessionID)
	if proj == nil {
		return false
	}
	for _, cp := range proj.GetCheckpoints() {
		if cp.GetRunId() == runID && cp.GetGitTreeOid() != "" {
			return true
		}
	}
	for _, r := range proj.GetRuns() {
		if r.GetRunId() == runID {
			switch r.GetStatus() {
			case sessionv1.RunStatus_RUN_STATUS_COMPLETED, sessionv1.RunStatus_RUN_STATUS_FAILED, sessionv1.RunStatus_RUN_STATUS_INTERRUPTED, sessionv1.RunStatus_RUN_STATUS_CANCELLED:
				return true
			}
		}
	}
	return false
}

// sessionTurnStamp changes when a session advances to a new turn or checkpoint so
// cached ReadFile responses refresh their `Now` comparison only after new work runs.
func (o *Owner) sessionTurnStamp(sessionID string) string {
	proj := o.cachedProjectionOnly(sessionID)
	if proj == nil {
		return ""
	}
	lastTree := ""
	if cps := proj.GetCheckpoints(); len(cps) > 0 {
		lastTree = cps[len(cps)-1].GetGitTreeOid()
	}
	return fmt.Sprintf("%s:%d", lastTree, len(proj.GetRuns()))
}

func (s *ownerSessionService) ResolveFiles(ctx context.Context, req *sessionv1.ResolveFilesRequest) (*sessionv1.ResolveFilesResponse, error) {
	o := s.o()
	joined := strings.Join(req.Paths, "\x01")
	key := func(m string) string { return filesKey("resolve", m, req.SessionId, req.RunId, joined) }
	doneKey := func(m string) string { return filesKey("resolve.turn", m, req.SessionId, req.RunId, joined) }
	// A finished turn's ResolveFiles answer is fixed once its checkpoint exists: serve locally without asking.
	if req.RunId != "" {
		if eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId); err == nil && eng == nil {
			kept := &sessionv1.ResolveFilesResponse{}
			if o.keptFiles(doneKey(machineID), kept) || (o.keptFiles(key(machineID), kept) && resolvedAtTurn(kept)) {
				return kept, nil
			}
		}
	}
	settled := req.RunId != "" && o.runHasCheckpoint(req.SessionId, req.RunId)
	res, err := filesCall(ctx, o, req.SessionId, req.MachineId, key,
		func(eng *kernel.Engine) (*sessionv1.ResolveFilesResponse, error) {
			return eng.ResolveFiles(ctx, o.ownerPrincipal(), req)
		},
		func(pm *peer.Machine) (*sessionv1.ResolveFilesResponse, error) {
			return pm.Session.ResolveFiles(ctx, req)
		},
		func() *sessionv1.ResolveFilesResponse { return &sessionv1.ResolveFilesResponse{} })
	if err == nil && req.RunId != "" && (settled || resolvedAtTurn(res)) {
		if eng, machineID, mErr := o.resolveMachineForSession(ctx, req.SessionId); mErr == nil && eng == nil {
			o.keepFiles(doneKey(machineID), req.SessionId, res)
		}
	}
	return res, err
}

func (s *ownerSessionService) ListFiles(ctx context.Context, req *sessionv1.ListFilesRequest) (*sessionv1.ListFilesResponse, error) {
	o := s.o()
	return filesCall(ctx, o, req.SessionId, req.MachineId,
		func(m string) string { return filesKey("list", m, req.SessionId, req.RunId, req.Dir) },
		func(eng *kernel.Engine) (*sessionv1.ListFilesResponse, error) {
			return eng.ListFiles(ctx, o.ownerPrincipal(), req)
		},
		func(pm *peer.Machine) (*sessionv1.ListFilesResponse, error) { return pm.Session.ListFiles(ctx, req) },
		func() *sessionv1.ListFilesResponse { return &sessionv1.ListFilesResponse{} })
}

func (s *ownerSessionService) ReadFile(ctx context.Context, req *sessionv1.ReadFileRequest) (*sessionv1.ReadFileResponse, error) {
	o := s.o()
	key := func(m string) string { return filesKey("read", m, req.SessionId, req.RunId, req.Path) }
	stamp := o.sessionTurnStamp(req.SessionId)
	doneKey := func(m string) string { return filesKey("read.turn", m, req.SessionId, req.RunId, req.Path, stamp) }
	// A finished turn's file is fixed once kept: serve locally without asking its machine again.
	if req.RunId != "" {
		if eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId); err == nil && eng == nil {
			kept := &sessionv1.ReadFileResponse{}
			if o.keptFiles(doneKey(machineID), kept) || (stamp == "" && o.keptFiles(key(machineID), kept) && kept.GetTurn() > 0) {
				return kept, nil
			}
		}
	}
	settled := req.RunId != "" && o.runHasCheckpoint(req.SessionId, req.RunId)
	res, err := filesCall(ctx, o, req.SessionId, req.MachineId, key,
		func(eng *kernel.Engine) (*sessionv1.ReadFileResponse, error) {
			return eng.ReadFile(ctx, o.ownerPrincipal(), req)
		},
		func(pm *peer.Machine) (*sessionv1.ReadFileResponse, error) { return pm.Session.ReadFile(ctx, req) },
		func() *sessionv1.ReadFileResponse { return &sessionv1.ReadFileResponse{} })
	if err == nil && req.RunId != "" && (settled || res.GetTurn() > 0) {
		if eng, machineID, mErr := o.resolveMachineForSession(ctx, req.SessionId); mErr == nil && eng == nil {
			o.keepFiles(doneKey(machineID), req.SessionId, res)
		}
	}
	return res, err
}

func (s *ownerSessionService) ReadFileBytes(ctx context.Context, req *sessionv1.ReadFileBytesRequest) (*sessionv1.ReadFileBytesResponse, error) {
	o := s.o()
	key := func(m string) string {
		return filesKey("bytes", m, req.SessionId, req.RunId, req.Path, fmt.Sprintf("%d+%d", req.Offset, req.Length))
	}
	// A turn's part kept is that part for good: no need to ask.
	if req.RunId != "" {
		if eng, machineID, err := o.resolveMachineForSession(ctx, req.SessionId); err == nil && eng == nil {
			if kept := (&sessionv1.ReadFileBytesResponse{}); o.keptFiles(key(machineID), kept) && kept.Turn > 0 {
				return kept, nil
			}
		}
	}
	return filesCall(ctx, o, req.SessionId, req.MachineId, key,
		func(eng *kernel.Engine) (*sessionv1.ReadFileBytesResponse, error) {
			return eng.ReadFileBytes(ctx, o.ownerPrincipal(), req)
		},
		func(pm *peer.Machine) (*sessionv1.ReadFileBytesResponse, error) {
			return pm.Session.ReadFileBytes(ctx, req)
		},
		func() *sessionv1.ReadFileBytesResponse { return &sessionv1.ReadFileBytesResponse{} })
}
