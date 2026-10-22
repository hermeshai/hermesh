package codex

import (
	"encoding/json"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
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
