package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

// Values from the app-server schema (codex app-server generate-json-schema,
// TurnError.codexErrorInfo).
func TestTurnErrorFailure(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want sessionv1.FailureKind
		http int
	}{
		{`{"message":"Your input exceeds the context window","codexErrorInfo":"contextWindowExceeded"}`, sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW, 0},
		{`{"message":"You've hit your usage limit","codexErrorInfo":"usageLimitExceeded"}`, sessionv1.FailureKind_FAILURE_KIND_QUOTA, 0},
		{`{"message":"rate limited","codexErrorInfo":"rateLimitExceeded"}`, sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT, 0},
		{`{"message":"x","codexErrorInfo":"unauthorized"}`, sessionv1.FailureKind_FAILURE_KIND_AUTH, 0},
		{`{"message":"stream disconnected","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":null}}}`, sessionv1.FailureKind_FAILURE_KIND_NETWORK, 0},
		{`{"message":"failed","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":401}}}`, sessionv1.FailureKind_FAILURE_KIND_AUTH, 401},
		{`{"message":"failed","codexErrorInfo":{"responseTooManyFailedAttempts":{"httpStatusCode":503}}}`, sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR, 503},
		// No code: the message decides.
		{`{"message":"unexpected status 429 Too Many Requests","codexErrorInfo":null}`, sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT, 0},
		{`{"message":"something","codexErrorInfo":"other"}`, sessionv1.FailureKind_FAILURE_KIND_UNKNOWN, 0},
	} {
		var te turnError
		if err := json.Unmarshal([]byte(c.raw), &te); err != nil {
			t.Fatal(err)
		}
		if f := te.failure(); f.Kind != c.want || f.HTTPStatus != c.http || f.Message == "" {
			t.Errorf("%s: %+v, want %v/%d", c.raw, f, c.want, c.http)
		}
	}
}

// What Codex 0.162 sends while it cannot reach the provider (it retries
// forever) is not progress; deltas and items are.
func TestNotProgress(t *testing.T) {
	for m, want := range map[string]bool{
		"error": true, "warning": true, "account/updated": true, "account/rateLimits/updated": true,
		"mcpServer/startupStatus/updated": true, "remoteControl/status/changed": true, "thread/status/changed": true,
		"item/started": false, "item/completed": false, "turn/completed": false, "item/agentMessage/delta": false, "": false,
	} {
		if notProgress(m) != want {
			t.Errorf("notProgress(%q) = %v", m, !want)
		}
	}
}

func TestCallDropsPendingRequestWhenWriteFails(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proc, err := provider.StartProc(t.Context(), bin, []string{"-test.run=^$"}, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(time.Second) })
	go proc.Lines(func([]byte) {})
	select {
	case <-proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("test process did not exit")
	}
	s := &session{proc: proc, pending: map[int64]chan rpcResult{}}
	if _, err := s.call(context.Background(), "turn/steer", nil); err == nil {
		t.Fatal("write to the exited process succeeded")
	}
	if len(s.pending) != 0 {
		t.Fatalf("failed write left %d pending requests", len(s.pending))
	}
}

// Codex says the account's limits in account/rateLimits/updated during a
// turn (0.171, recorded): here one window of a week, the other null, and
// the plan. The driver tells StartSpec.Limits; a notification with no
// window tells nothing.
func TestRateLimitsUpdatedTellsLimits(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "codex-0.171-rate-limits-updated.json"))
	if err != nil {
		t.Fatal(err)
	}
	var said []provider.Limits
	s := &session{events: make(chan provider.Event, 8), limits: func(l provider.Limits) { said = append(said, l) }}
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	s.handleNotification(m.Method, m.Params)
	s.handleNotification("account/rateLimits/updated", json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1795534800},"secondary":{"usedPercent":40.6,"windowDurationMins":10080,"resetsAt":1795678770}}}`))
	s.handleNotification("account/rateLimits/updated", json.RawMessage(`{"rateLimits":{"primary":null,"secondary":null,"planType":"pro"}}`))
	want := []provider.Limits{
		{Plan: "pro", Windows: []provider.LimitWindow{{Minutes: 10080, UsedPercent: 61, ResetsAt: time.Unix(1795678770, 0)}}},
		{Windows: []provider.LimitWindow{{Minutes: 300, UsedPercent: 12, ResetsAt: time.Unix(1795534800, 0)}, {Minutes: 10080, UsedPercent: 41, ResetsAt: time.Unix(1795678770, 0)}}},
	}
	if len(said) != len(want) {
		t.Fatalf("told %d times: %+v", len(said), said)
	}
	for i := range want {
		if said[i].Plan != want[i].Plan || !slices.Equal(said[i].Windows, want[i].Windows) {
			t.Fatalf("limits %d: %+v, want %+v", i, said[i], want[i])
		}
	}
	if len(s.events) != 0 {
		t.Fatalf("an event of the session's: %d", len(s.events))
	}
}
