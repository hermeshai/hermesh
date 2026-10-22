package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestNoSessionCap: by default a machine runs every session it is given at
// once; none waits queued for another to finish.
func TestNoSessionCap(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	var ids []string
	for i := range 6 {
		s, r := w.startSession(time.Minute, "--agent", "gemini", "--title", fmt.Sprintf("long %d", i), "--prompt", "shell sleep 600")
		if s.SessionID == "" {
			t.Fatalf("start %d: %+v", i, r)
		}
		ids = append(ids, s.SessionID)
	}
	eventually(t, time.Minute, "all six sessions running at once", func() bool {
		for _, id := range ids {
			if w.get(id).Status != "running" {
				return false
			}
		}
		return true
	})
}
