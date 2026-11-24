package mobile

import (
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestInProcessBinaryProtobufFFIUnaryAndStreamAcrossPause(t *testing.T) {
	c, err := Start(t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.o.Close) // after Pause, before TempDir is removed
	t.Cleanup(c.Pause)

	// Pause closes the 127.0.0.1 loopback TCP listener (TN2277 mitigation),
	// yet in-process binary Protobuf FFI must continue serving LocalService
	// with zero socket errors!
	c.Pause()

	reqBytes, err := proto.Marshal(&localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	resBytes, errCode, errMsg := c.InvokeUnary("GetStatus", reqBytes, 5*time.Second)
	if errCode != "" {
		t.Fatalf("InvokeUnary GetStatus while paused failed: code=%q msg=%q", errCode, errMsg)
	}
	var status localv1.GetStatusResponse
	if err := proto.Unmarshal(resBytes, &status); err != nil {
		t.Fatalf("unmarshal GetStatusResponse: %v", err)
	}
	if status.GetInitialized() {
		t.Fatalf("expected uninitialized fresh core, got %+v", &status)
	}

	// Error mapping check: empty machine_id returns invalid_argument.
	badReq, _ := proto.Marshal(&localv1.GetHerConversationRequest{})
	_, errCode, _ = c.InvokeUnary("GetHerConversation", badReq, 5*time.Second)
	if errCode != "invalid_argument" {
		t.Fatalf("expected invalid_argument for empty machine_id, got %q", errCode)
	}

	// Server-streaming binary Protobuf FFI check: open Watch, receive initial snapshot, then close cleanly.
	watchReq, _ := proto.Marshal(&localv1.WatchRequest{})
	sid := c.OpenStream("Watch", watchReq)
	frame, done, errCode, errMsg := c.NextStreamFrame(sid)
	if done || errCode != "" {
		t.Fatalf("Watch first frame: done=%v code=%q msg=%q", done, errCode, errMsg)
	}
	var w localv1.WatchResponse
	if err := proto.Unmarshal(frame, &w); err != nil {
		t.Fatalf("unmarshal WatchResponse: %v", err)
	}
	if w.GetStatus() == nil {
		t.Fatalf("expected initial WatchResponse with Status, got %+v", &w)
	}
	c.CloseStream(sid)

	// Verify SessionService unary & streaming procedures work over FFI too!
	lsReq, _ := proto.Marshal(&sessionv1.ListSessionsRequest{})
	lsResBytes, errCode, errMsg := c.InvokeUnary("/hermesh.session.v1.SessionService/ListSessions", lsReq, 5*time.Second)
	if errCode != "" {
		t.Fatalf("SessionService/ListSessions FFI failed: %s %s", errCode, errMsg)
	}
	var lsRes sessionv1.ListSessionsResponse
	if err := proto.Unmarshal(lsResBytes, &lsRes); err != nil {
		t.Fatalf("unmarshal ListSessionsResponse: %v", err)
	}

	subReq, _ := proto.Marshal(&sessionv1.WatchSessionsRequest{})
	ssid := c.OpenStream("/hermesh.session.v1.SessionService/WatchSessions", subReq)
	sframe, sdone, errCode, errMsg := c.NextStreamFrame(ssid)
	if sdone || errCode != "" {
		t.Fatalf("WatchSessions first frame: done=%v code=%q msg=%q", sdone, errCode, errMsg)
	}
	var subSnap sessionv1.WatchSessionsResponse
	if err := proto.Unmarshal(sframe, &subSnap); err != nil || !subSnap.Snapshot {
		t.Fatalf("expected WatchSessions initial snapshot, got %+v (err=%v)", &subSnap, err)
	}
	c.CloseStream(ssid)
}
