package codex

import (
	"encoding/json"
	"errors"
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

// Asked (account/rateLimits/read, without a turn: 0.171, recorded), Codex
// answers the limits as a turn tells them. An answer with no window, or a
// refusal (not logged in), says nothing.
func TestRateLimitsRead(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "codex-0.171-rate-limits-read.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	answer := func(result json.RawMessage, fail error) func(string, any, any) error {
		return func(method string, params, v any) error {
			if method != "account/rateLimits/read" || params != nil {
				t.Fatalf("asked %s %v", method, params)
			}
			if fail != nil {
				return fail
			}
			return json.Unmarshal(result, v)
		}
	}
	got := limitsFrom(answer(m.Result, nil))
	want := []runtime.LimitWindow{{Minutes: 10080, UsedPercent: 61, ResetsAt: time.Unix(1795678770, 0)}}
	if got == nil || got.Plan != "pro" || !slices.Equal(got.Windows, want) {
		t.Fatalf("limits: %+v", got)
	}
	if l := limitsFrom(answer(json.RawMessage(`{"rateLimits":{"primary":null,"secondary":null,"planType":"pro"}}`), nil)); l != nil {
		t.Fatalf("no window: %+v", l)
	}
	if l := limitsFrom(answer(nil, errors.New("not logged in"))); l != nil {
		t.Fatalf("refused: %+v", l)
	}
}
