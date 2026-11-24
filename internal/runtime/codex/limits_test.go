package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/runtime"
)

// Codex says the account's limits in account/rateLimits/updated during a
// turn (0.171, recorded): here one window of a week, the other null, and the
// plan. The session tells StartSpec.Limits; a notification with no window
// tells nothing.
func TestRateLimitsUpdated(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "codex-0.171-rate-limits-updated.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var said []runtime.Limits
	s := &session{spec: runtime.StartSpec{Limits: func(l runtime.Limits) { said = append(said, l) }}}
	s.handleNotification(m.Method, m.Params)
	s.handleNotification("account/rateLimits/updated", json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1795534800},"secondary":{"usedPercent":40.6,"windowDurationMins":10080,"resetsAt":1795678770}}}`))
	s.handleNotification("account/rateLimits/updated", json.RawMessage(`{"rateLimits":{"primary":null,"secondary":null,"planType":"pro"}}`))
	want := []runtime.Limits{
		{Plan: "pro", Windows: []runtime.LimitWindow{{Minutes: 10080, UsedPercent: 61, ResetsAt: time.Unix(1795678770, 0)}}},
		{Windows: []runtime.LimitWindow{{Minutes: 300, UsedPercent: 12, ResetsAt: time.Unix(1795534800, 0)}, {Minutes: 10080, UsedPercent: 41, ResetsAt: time.Unix(1795678770, 0)}}},
	}
	if len(said) != len(want) {
		t.Fatalf("told %d times: %+v", len(said), said)
	}
	for i := range want {
		if said[i].Plan != want[i].Plan || !slices.Equal(said[i].Windows, want[i].Windows) {
			t.Fatalf("limits %d: %+v, want %+v", i, said[i], want[i])
		}
	}
}
