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
