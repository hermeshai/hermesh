package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/runtime"
)

// Claude Code says the account's limits on a rate_limit_event line once a
// turn (2.1.330, recorded): each window's use from 0 to 1 and when it
// resets. They read as whole percents, the shortest window first; a window
// it names otherwise is left out, and with none left there is nothing to say.
func TestRateLimitEvent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.330-rate-limit-event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var l wireLine
	if err := json.Unmarshal(b, &l); err != nil || l.Type != "rate_limit_event" || l.RateLimitInfo == nil {
		t.Fatalf("the recorded line: %+v, %v", l, err)
	}
	got, ok := l.RateLimitInfo.limits()
	want := []runtime.LimitWindow{{Minutes: 300, UsedPercent: 26, ResetsAt: time.Unix(1795534800, 0)}, {Minutes: 10080, UsedPercent: 48, ResetsAt: time.Unix(1795939200, 0)}}
	if !ok || got.Plan != "" || !slices.Equal(got.Windows, want) {
		t.Fatalf("limits: %+v", got)
	}
	var other wireLine
	json.Unmarshal([]byte(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day_sonnet":{"utilization":0.1,"resetsAt":1795939200}}}}`), &other)
	if l, ok := other.RateLimitInfo.limits(); ok {
		t.Fatalf("an unknown window: %+v", l)
	}
}

// Asked (get_usage, without a turn: 2.1.330, recorded), Claude Code says the
// same windows, used from 0 to 100, resetting at a time to the microsecond,
// and the plan. They read as the turn's line reads them, to the second; a
// model's own week is left out, as is the turn's. An API key's answer, or
// one that failed, says nothing.
func TestGetUsage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.330-get-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := limitsFrom(b)
	want := []runtime.LimitWindow{{Minutes: 300, UsedPercent: 39, ResetsAt: time.Unix(1795552800, 0)}, {Minutes: 10080, UsedPercent: 64, ResetsAt: time.Unix(1795939200, 0)}}
	if got == nil || got.Plan != "max" || !slices.EqualFunc(got.Windows, want, func(a, b runtime.LimitWindow) bool {
		return a.Minutes == b.Minutes && a.UsedPercent == b.UsedPercent && a.ResetsAt.Equal(b.ResetsAt)
	}) {
		t.Fatalf("limits: %+v", got)
	}
	for _, line := range []string{
		`{"type":"control_response","response":{"subtype":"success","request_id":"hermesh-usage","response":{"subscription_type":null,"rate_limits_available":false,"rate_limits":null}}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"hermesh-usage","error":"unknown subtype"}}`,
	} {
		if l := limitsFrom([]byte(line)); l != nil {
			t.Fatalf("%s: %+v", line, l)
		}
	}
}
