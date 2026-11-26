package owner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestOfflineOutboxStartDispatchAndCancel(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "owner"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	svc := &ownerSessionService{d: d}

	// 1. Offline StartSession creates an optimistic queued session and outbox entry.
	up, err := svc.UploadBlob(ctx, &sessionv1.UploadBlobRequest{
		MachineId: "m_remote_offline",
		Name:      "photo.jpg",
		MimeType:  "image/jpeg",
		Data:      []byte("jpeg-bytes"),
	})
	if err != nil || up.GetAttachment().GetBlobSha256() == "" {
		t.Fatalf("UploadBlob: %v %+v", err, up)
	}
	started, err := svc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId:     "m_remote_offline",
		ProjectId:     "demo",
		Title:         "Offline task",
		InitialPrompt: "implement auth",
		Attachments:   []*sessionv1.AttachmentRef{up.Attachment},
	})
	if err != nil {
		t.Fatalf("offline StartSession: %v", err)
	}
	if !started.QueuedOffline || !started.Session.OfflineQueued {
		t.Fatalf("expected queued_offline=true, got %+v", started)
	}
	tempID := started.Session.SessionId

	// Local DownloadBlob serves the uploaded attachment immediately even while the target machine is offline.
	dl, err := svc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{
		MachineId:  "m_remote_offline",
		SessionId:  tempID,
		BlobSha256: up.Attachment.BlobSha256,
	})
	if err != nil || string(dl.GetData()) != "jpeg-bytes" {
		t.Fatalf("offline DownloadBlob: %v %+v", err, dl)
	}

	// 2. Follow-up DispatchMessage on offline-queued session appends to outbox and cached projection.
	disp, err := svc.DispatchMessage(ctx, &sessionv1.DispatchMessageRequest{
		SessionId: tempID,
		Text:      "also add tests",
	})
	if err != nil {
		t.Fatalf("offline DispatchMessage: %v", err)
	}
	if !disp.QueuedOffline || disp.Session.QueuedRunCount != 2 {
		t.Fatalf("expected QueuedRunCount=2, got %+v", disp)
	}

	got, err := svc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: tempID})
	if err != nil || len(got.Projection.GetRuns()) != 2 {
		t.Fatalf("GetSession cached projection: %v %+v", err, got)
	}
	// The first message is in it, so its page shows it while it waits.
	if items := got.Projection.GetItems(); len(items) == 0 || items[0].Kind != sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE || items[0].Text != "implement auth" || items[0].RunId != started.InitialRun.RunId {
		t.Fatalf("the waiting message: %+v", items)
	}

	// 3. DeleteSession cancels the offline-queued session and clears outbox + cache.
	if _, err := svc.DeleteSession(ctx, &sessionv1.DeleteSessionRequest{SessionId: tempID}); err != nil {
		t.Fatalf("DeleteSession offline-queued: %v", err)
	}
	if d.owner().cachedProjection(tempID) != nil {
		t.Fatalf("expected cached projection removed after DeleteSession")
	}
	var outboxCount int
	_ = d.owner().st.db.QueryRow(`SELECT COUNT(*) FROM sessions_outbox WHERE session_id = ?`, tempID).Scan(&outboxCount)
	if outboxCount != 0 {
		t.Fatalf("expected outbox cleared after DeleteSession, got %d rows", outboxCount)
	}
}

func TestSessionOnlyCacheFallbackZeroesLastEventSeq(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "owner"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Simulates StartSession succeeding on a remote machine (last_event_seq=3) and
	// caching only the Session row before GetSession fetches the full projection.
	sid := "s_remote_01testseq"
	d.owner().cacheSession(&sessionv1.Session{
		SessionId: sid,
		MachineId: "m_remote_unreachable",
		ProjectId: "hermesh",
		Status:    sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
		Progress:  &sessionv1.SessionProgress{LastEventSeq: 3},
	}, nil)

	svc := &ownerSessionService{d: d}
	res, err := svc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid})
	if err != nil {
		t.Fatalf("GetSession fallback: %v", err)
	}
	if !res.FromCache {
		t.Fatalf("expected FromCache=true")
	}
	if len(res.Projection.GetItems()) != 0 {
		t.Fatalf("expected empty items in session-only fallback, got %d", len(res.Projection.GetItems()))
	}
	if got := res.Projection.GetSession().GetProgress().GetLastEventSeq(); got != 0 {
		t.Fatalf("session-only fallback must report LastEventSeq=0 (not %d) so SubscribeSession does not skip seq 1..3", got)
	}
}

func TestOfflineUpdateSessionArchivesAndPreservesSeq(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "owner"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sid := "s_remote_archive_offline"
	d.owner().cacheSession(&sessionv1.Session{
		SessionId: sid,
		MachineId: "m_remote_unreachable",
		ProjectId: "hermesh",
		Title:     "iOS archive fix",
		Status:    sessionv1.SessionStatus_SESSION_STATUS_IDLE,
		Progress:  &sessionv1.SessionProgress{LastEventSeq: 12},
	}, nil)

	svc := &ownerSessionService{d: d}
	archRes, err := svc.UpdateSession(ctx, &sessionv1.UpdateSessionRequest{
		SessionId:   sid,
		HasArchived: true,
		Archived:    true,
	})
	if err != nil {
		t.Fatalf("offline archive UpdateSession: %v", err)
	}
	if archRes.TransitionApplied != "queued_offline" || archRes.GetSession().GetStatus() != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		t.Fatalf("expected optimistic archived session, got %+v", archRes)
	}
	if got := archRes.GetSession().GetProgress().GetLastEventSeq(); got != 12 {
		t.Fatalf("expected LastEventSeq=12 preserved for mergeSessions, got %d", got)
	}
	if activeOnly := d.owner().cachedSessions(false); len(activeOnly) != 0 {
		t.Fatalf("expected archived session hidden from unarchived cache query, got %+v", activeOnly)
	}
	if all := d.owner().cachedSessions(true); len(all) != 1 || all[0].Status != sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED {
		t.Fatalf("expected archived session in includeArchived cache query, got %+v", all)
	}

	// Rapid offline Undo (Unarchive) -> Rename -> Archive flapping preserves LastEventSeq and exact status.
	unarchRes, err := svc.UpdateSession(ctx, &sessionv1.UpdateSessionRequest{
		SessionId: sid, HasArchived: true, Archived: false,
	})
	if err != nil || unarchRes.GetSession().GetStatus() != sessionv1.SessionStatus_SESSION_STATUS_IDLE || unarchRes.GetSession().GetProgress().GetLastEventSeq() != 12 {
		t.Fatalf("offline unarchive: %v %+v", err, unarchRes)
	}
	renRes, err := svc.UpdateSession(ctx, &sessionv1.UpdateSessionRequest{
		SessionId: sid, Title: "  Renamed Offline  ", HasPinned: true, Pinned: true,
	})
	if err != nil || renRes.GetSession().GetTitle() != "Renamed Offline" || !renRes.GetSession().GetPinned() {
		t.Fatalf("offline rename+pin: %v %+v", err, renRes)
	}

	// Simulate outbox replay failure when GetSession is unavailable: LastEventSeq must NOT be zeroed!
	d.owner().recordOutboxFailure(ctx, nil, outboxRow{sessionID: sid, method: "UpdateSession"}, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot archive while running")))
	afterFail := d.owner().cachedSession(sid)
	if afterFail == nil || afterFail.GetProgress().GetLastEventSeq() != 12 {
		t.Fatalf("recordOutboxFailure clobbered LastEventSeq: %+v", afterFail)
	}
}
