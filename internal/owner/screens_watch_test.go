package owner

import (
	"testing"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestDrainSessionChangesFastPathAndBurstCoalesce(t *testing.T) {
	ch := make(chan *localv1.WatchSessionsResponse, 8)
	first := &localv1.WatchSessionsResponse{
		Sessions: []*sessionv1.Session{{SessionId: "s1", Title: "v1"}},
	}
	got, ok := drainSessionChanges(first, ch)
	if !ok || got != first {
		t.Fatalf("expected zero-copy fast path when channel is empty, got ok=%v same=%v", ok, got == first)
	}

	// Queue multiple updates: s1 updated twice, s2 updated then deleted, s3 deleted then recreated.
	ch <- &localv1.WatchSessionsResponse{
		Sessions: []*sessionv1.Session{
			{SessionId: "s2", Title: "temp"},
			{SessionId: "s1", Title: "v2"},
		},
		DeletedSessionIds: []string{"s3"},
	}
	ch <- &localv1.WatchSessionsResponse{
		MachinesChanged: true,
		Sessions: []*sessionv1.Session{
			{SessionId: "s1", Title: "v3"},
			{SessionId: "s3", Title: "reborn"},
		},
		DeletedSessionIds: []string{"s2"},
	}

	merged, ok := drainSessionChanges(first, ch)
	if !ok {
		t.Fatal("expected open channel")
	}
	if !merged.MachinesChanged {
		t.Fatal("expected MachinesChanged to propagate")
	}
	if len(merged.Sessions) != 2 || merged.Sessions[0].SessionId != "s1" || merged.Sessions[0].Title != "v3" ||
		merged.Sessions[1].SessionId != "s3" || merged.Sessions[1].Title != "reborn" {
		t.Fatalf("unexpected merged sessions: %+v", merged.Sessions)
	}
	if len(merged.DeletedSessionIds) != 1 || merged.DeletedSessionIds[0] != "s2" {
		t.Fatalf("unexpected deleted ids: %+v", merged.DeletedSessionIds)
	}
}

func TestWatchResponseEmpty(t *testing.T) {
	if !watchResponseEmpty(nil) || !watchResponseEmpty(&localv1.WatchResponse{}) {
		t.Fatal("expected empty WatchResponse to be detected")
	}
	if watchResponseEmpty(&localv1.WatchResponse{WorkVersion: 1}) {
		t.Fatal("expected WorkVersion=1 to be non-empty")
	}
	if watchResponseEmpty(&localv1.WatchResponse{HerKeptVersion: 2}) {
		t.Fatal("expected HerKeptVersion=2 to be non-empty")
	}
	if watchResponseEmpty(&localv1.WatchResponse{Sessions: []*sessionv1.Session{{SessionId: "s1"}}}) {
		t.Fatal("expected Sessions to be non-empty")
	}
}
