package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestSessionSummaryCacheAndInvalidation(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "machine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Empty store.
	sum, err := st.SessionSummary()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Running != 0 || len(sum.Counts) != 0 || len(sum.ActiveByProject) != 0 {
		t.Fatalf("unexpected initial summary: %+v", sum)
	}

	bigNotes := string(bytes.Repeat([]byte("x"), 64*1024))
	put := func(id, proj string, status sessionv1.SessionStatus, archived bool) {
		t.Helper()
		s := &sessionv1.Session{
			SessionId: id,
			ProjectId: proj,
			Status:    status,
			Title:     bigNotes,
		}
		if archived {
			s.Status = sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
		}
		if _, err := st.Mutate(func(tx *Tx) error {
			return tx.PutSession(s)
		}); err != nil {
			t.Fatal(err)
		}
	}

	put("s_q", "p1", sessionv1.SessionStatus_SESSION_STATUS_QUEUED, false)
	put("s_p", "p1", sessionv1.SessionStatus_SESSION_STATUS_PREPARING, false)
	put("s_r", "p1", sessionv1.SessionStatus_SESSION_STATUS_RUNNING, false)
	put("s_w", "p2", sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT, false)
	put("s_i", "p2", sessionv1.SessionStatus_SESSION_STATUS_IDLE, false)
	put("s_c", "p2", sessionv1.SessionStatus_SESSION_STATUS_CRASHED, false)
	put("s_a", "p1", sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED, true)

	sum, err = st.SessionSummary()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Running != 3 {
		t.Fatalf("Running = %d, want 3", sum.Running)
	}
	if sum.Counts["queued"] != 1 || sum.Counts["preparing"] != 1 || sum.Counts["running"] != 1 ||
		sum.Counts["waiting_input"] != 1 || sum.Counts["idle"] != 1 || sum.Counts["crashed"] != 1 {
		t.Fatalf("unexpected Counts: %+v", sum.Counts)
	}
	if sum.ActiveByProject["p1"] != 3 || sum.ActiveByProject["p2"] != 1 {
		t.Fatalf("unexpected ActiveByProject: %+v", sum.ActiveByProject)
	}

	// Mutating returned maps must not corrupt the cached summary.
	sum.Counts["running"] = 999
	sum.ActiveByProject["p1"] = 999
	sum2, err := st.SessionSummary()
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Counts["running"] != 1 || sum2.ActiveByProject["p1"] != 3 {
		t.Fatalf("cache was mutated by caller: %+v", sum2)
	}

	// Cached hits must not unmarshal payloads (tiny fixed map-clone allocations only).
	for i := 0; i < 40; i++ {
		put(fmt.Sprintf("s_idle_%d", i), "p3", sessionv1.SessionStatus_SESSION_STATUS_IDLE, false)
	}
	if _, err := st.SessionSummary(); err != nil {
		t.Fatal(err)
	}
	cachedAllocs := testing.AllocsPerRun(50, func() {
		_, _ = st.SessionSummary()
	})
	fullScanAllocs := testing.AllocsPerRun(10, func() {
		_, _ = st.ListSessions("", false, 0)
	})
	if cachedAllocs > 6 || cachedAllocs*20 >= fullScanAllocs {
		t.Fatalf("expected cached SessionSummary allocs (%.1f) << full ListSessions allocs (%.1f)", cachedAllocs, fullScanAllocs)
	}

	// Status transition, archive, and delete invalidate the summary immediately.
	put("s_r", "p1", sessionv1.SessionStatus_SESSION_STATUS_IDLE, false)
	put("s_w", "p2", sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED, true)
	if _, err := st.Mutate(func(tx *Tx) error {
		return tx.DeleteSession("s_p")
	}); err != nil {
		t.Fatal(err)
	}
	sum3, err := st.SessionSummary()
	if err != nil {
		t.Fatal(err)
	}
	if sum3.Running != 0 || sum3.ActiveByProject["p1"] != 1 || sum3.ActiveByProject["p2"] != 0 {
		t.Fatalf("unexpected summary after transitions: %+v", sum3)
	}
}

func TestTurnItemsCacheInvalidationAndEviction(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "machine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	bigText := string(bytes.Repeat([]byte("a"), 32*1024))
	putSession := func(id string) {
		t.Helper()
		if _, err := st.Mutate(func(tx *Tx) error {
			return tx.PutSession(&sessionv1.Session{
				SessionId: id,
				ProjectId: "p1",
				Status:    sessionv1.SessionStatus_SESSION_STATUS_RUNNING,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	putItem := func(sessionID, itemID, runID, status, text string) {
		t.Helper()
		if _, err := st.Mutate(func(tx *Tx) error {
			return tx.PutTurnItem(&sessionv1.TurnItem{
				SessionId: sessionID,
				ItemId:    itemID,
				RunId:     runID,
				Kind:      sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE,
				Status:    status,
				Text:      text,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}

	putSession("s1")
	for i := 0; i < 20; i++ {
		putItem("s1", fmt.Sprintf("it_%d", i), "r1", "completed", bigText)
	}

	// Prime cache and verify contents.
	items, err := st.TurnItems("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 20 || items[0].Text != bigText {
		t.Fatalf("unexpected items len=%d", len(items))
	}

	// Mutating returned slice header must not corrupt cached slice.
	items[0] = nil
	items2, err := st.TurnItems("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items2) != 20 || items2[0] == nil || items2[0].ItemId != "it_0" {
		t.Fatalf("cached slice corrupted: %+v", items2[0])
	}

	// Cached hits must avoid unmarshaling 640 KB of protobuf payloads (1 slice clone alloc).
	cachedAllocs := testing.AllocsPerRun(50, func() {
		got, err := st.TurnItems("s1")
		if err != nil || len(got) != 20 {
			t.Fatalf("TurnItems: %v (%d)", err, len(got))
		}
	})
	if cachedAllocs > 2 {
		t.Fatalf("expected <=2 allocs on cached TurnItems hit, got %.1f", cachedAllocs)
	}

	// Updating an item invalidates s1 immediately.
	putItem("s1", "it_20", "r2", "running", "stream")
	items3, err := st.TurnItems("s1")
	if err != nil || len(items3) != 21 || items3[20].Status != "running" {
		t.Fatalf("expected 21 items after PutTurnItem, got %d (%v)", len(items3), err)
	}

	// SettleRunItems invalidates s1 (via PutTurnItem).
	if _, err := st.Mutate(func(tx *Tx) error {
		_, err := tx.SettleRunItems("s1", "r2", "completed")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	items4, err := st.TurnItems("s1")
	if err != nil || items4[20].Status != "completed" {
		t.Fatalf("expected settled status completed, got %+v (%v)", items4[20], err)
	}

	// DeleteTurnItemsAfterRunIds invalidates s1.
	if _, err := st.Mutate(func(tx *Tx) error {
		return tx.DeleteTurnItemsAfterRunIds("s1", map[string]bool{"r2": true})
	}); err != nil {
		t.Fatal(err)
	}
	items5, err := st.TurnItems("s1")
	if err != nil || len(items5) != 20 {
		t.Fatalf("expected 20 items after DeleteTurnItemsAfterRunIds, got %d (%v)", len(items5), err)
	}

	// Bound cache size across > maxCachedTurnItemSessions sessions.
	for i := 0; i < maxCachedTurnItemSessions+8; i++ {
		sid := fmt.Sprintf("extra_%d", i)
		putSession(sid)
		putItem(sid, "it_0", "r1", "completed", "x")
		if _, err := st.TurnItems(sid); err != nil {
			t.Fatal(err)
		}
	}
	st.turnItemsMu.Lock()
	cacheLen := len(st.turnItemsCache)
	orderLen := len(st.turnItemsOrder)
	st.turnItemsMu.Unlock()
	if cacheLen > maxCachedTurnItemSessions || orderLen != cacheLen {
		t.Fatalf("expected bounded cache <= %d, got map=%d order=%d", maxCachedTurnItemSessions, cacheLen, orderLen)
	}

	// DeleteSession invalidates deleted session.
	if _, err := st.Mutate(func(tx *Tx) error {
		return tx.DeleteSession("s1")
	}); err != nil {
		t.Fatal(err)
	}
	items6, err := st.TurnItems("s1")
	if err != nil || len(items6) != 0 {
		t.Fatalf("expected 0 items after DeleteSession, got %d (%v)", len(items6), err)
	}
}
