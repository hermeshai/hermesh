package owner

import (
	"context"
	"path/filepath"
	"testing"

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
	started, err := svc.StartSession(ctx, &sessionv1.StartSessionRequest{
		MachineId:     "m_remote_offline",
		ProjectId:     "demo",
		Title:         "Offline task",
		InitialPrompt: "implement auth",
	})
	if err != nil {
		t.Fatalf("offline StartSession: %v", err)
	}
	if !started.QueuedOffline || !started.Session.OfflineQueued {
		t.Fatalf("expected queued_offline=true, got %+v", started)
	}
	tempID := started.Session.SessionId

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
