package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		text string
		want sessionv1.FailureKind
	}{
		// Real Claude Code 2.1.292 / Codex app-server messages.
		{"No conversation found with session ID: 00000000-0000-4000-8000-000000000000", sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED},
		{"no rollout found for thread id 019a0000-0000-7000-8000-000000000000", sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED},
		{"Invalid params: session is not resumable: 019a0000-0000-7000-8000-000000000000 (code -32602)", sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED},
		{"There's an issue with the selected model (claude-no-such-model). It may not exist or you may not have access to it.", sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE},
		// Provider error examples.
		{`API Error: 429 {"type":"error","error":{"type":"rate_limit_error"}}`, sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT},
		{"API Error: 529 Overloaded", sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT},
		{"403 concurrent request limit exceeded; access token", sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT},
		{"API Error: 403 You've reached your monthly usage limit", sessionv1.FailureKind_FAILURE_KIND_QUOTA},
		{"You've hit your limit · resets 5pm", sessionv1.FailureKind_FAILURE_KIND_QUOTA},
		{"Credit balance is too low", sessionv1.FailureKind_FAILURE_KIND_QUOTA},
		{"API Error: 401 Invalid authentication credentials", sessionv1.FailureKind_FAILURE_KIND_AUTH},
		{"Invalid API key · Please run /login", sessionv1.FailureKind_FAILURE_KIND_AUTH},
		{"Not logged in · Please run /login", sessionv1.FailureKind_FAILURE_KIND_AUTH},
		{"prompt is too long: 213456 tokens > 200000 maximum", sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW},
		{"model_context_window_exceeded", sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW},
		{"API Error: 500 Internal server error", sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR},
		{"502 Bad Gateway", sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR},
		{"API Error: Connection closed mid-response. Please retry.", sessionv1.FailureKind_FAILURE_KIND_NETWORK},
		{"stream disconnected before completion: error sending request", sessionv1.FailureKind_FAILURE_KIND_NETWORK},
		{"dial tcp: lookup api.anthropic.com: no such host", sessionv1.FailureKind_FAILURE_KIND_NETWORK},
		{"Request timed out.", sessionv1.FailureKind_FAILURE_KIND_NETWORK},
		{"API Error: 400 invalid_request_error: messages.3: empty content", sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST},
		{`exec: "claude": executable file not found in $PATH`, sessionv1.FailureKind_FAILURE_KIND_START_FAILED},
		// Numbers that merely contain a status code must not match.
		{"processed 402913 tokens in 15290ms", sessionv1.FailureKind_FAILURE_KIND_UNKNOWN},
		{"exit status 4030", sessionv1.FailureKind_FAILURE_KIND_UNKNOWN},
		{"rate of 4291 lines", sessionv1.FailureKind_FAILURE_KIND_UNKNOWN},
		{"", sessionv1.FailureKind_FAILURE_KIND_UNKNOWN},
		{"something odd happened", sessionv1.FailureKind_FAILURE_KIND_UNKNOWN},
	} {
		if got := Classify(c.text); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestFromHTTPStatus(t *testing.T) {
	for _, c := range []struct {
		status int
		text   string
		want   sessionv1.FailureKind
	}{
		{404, "There's an issue with the selected model", sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE},
		{401, "", sessionv1.FailureKind_FAILURE_KIND_AUTH},
		{403, "monthly usage limit", sessionv1.FailureKind_FAILURE_KIND_QUOTA},
		{403, "concurrent request limit", sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT},
		{400, "prompt is too long", sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW},
		{400, "messages: empty", sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST},
		{429, "", sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT},
		{503, "", sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR},
		{0, "", sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED},
	} {
		if got := FromHTTPStatus(c.status, c.text); got != c.want {
			t.Errorf("FromHTTPStatus(%d, %q) = %v, want %v", c.status, c.text, got, c.want)
		}
	}
}

func TestFailureProto(t *testing.T) {
	f := (&Failure{Kind: sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT, Message: "429 with key sk-ant-api03-abcdefghijklmnop", HTTPStatus: 429}).Proto()
	if !f.Retryable || f.Hint == "" || f.HttpStatus != 429 || strings.Contains(f.Message, "abcdefghijklmnop") {
		t.Fatalf("proto: %+v", f)
	}
	if f := (&Failure{Message: strings.Repeat("x", 5000)}).Proto(); f.Kind != sessionv1.FailureKind_FAILURE_KIND_UNKNOWN || len(f.Message) > 2<<10 {
		t.Fatalf("unspecified kind or long message: %v %d", f.Kind, len(f.Message))
	}
	if (*Failure)(nil).Proto() != nil {
		t.Fatal("nil failure")
	}
}

func TestFailureOf(t *testing.T) {
	err := fmt.Errorf("codex thread/resume: %w", Fail(sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED, "no rollout found"))
	if f := FailureOf(err, sessionv1.FailureKind_FAILURE_KIND_START_FAILED); f.Kind != sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED {
		t.Fatalf("wrapped failure: %+v", f)
	}
	if f := FailureOf(errors.New("weird"), sessionv1.FailureKind_FAILURE_KIND_START_FAILED); f.Kind != sessionv1.FailureKind_FAILURE_KIND_START_FAILED {
		t.Fatalf("fallback: %+v", f)
	}
	if f := FailureOf(errors.New("API Error: 401"), sessionv1.FailureKind_FAILURE_KIND_START_FAILED); f.Kind != sessionv1.FailureKind_FAILURE_KIND_AUTH {
		t.Fatalf("classified: %+v", f)
	}
}

func TestRedact(t *testing.T) {
	for _, c := range []struct{ in, gone, kept string }{
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig", "eyJhbGciOiJIUzI1NiJ9", "Authorization"},
		{"ANTHROPIC_API_KEY=sk-ant-api03-ZZZZZZZZZZZZ", "ZZZZZZZZZZZZ", "ANTHROPIC_API_KEY"},
		{`{"api_key": "abcd1234efgh"}`, "abcd1234efgh", "api_key"},
		{"token: ghp_aBcDeFgHiJkLmNoPqRsT1234", "aBcDeFgHiJkLmNoPqRsT", "token"},
		{"fetch https://user:hunter2@example.com/x", "hunter2", "example.com"},
		{"opaque Qm9vbXNoYWthbGFrYTEyMzQ1Njc4OTBBQkNERUZHSElK end", "Qm9vbXNoYWthbGFrYTEyMzQ1Njc4OTBBQkNERUZHSElK", "end"},
		// Ordinary paths and words survive (real Codex 401 text).
		{"Missing bearer or basic authentication in header", "", "basic authentication in header"},
		{"/private/tmp/claude-501/-Users-alice-code-hermesh--worktrees-x/a.txt", "", "-Users-alice-code-hermesh--worktrees-x/a.txt"},
		{"session 019a0000-0000-7000-8000-000000000000 not found", "", "019a0000-0000-7000-8000-000000000000"},
	} {
		out := Redact(c.in)
		if (c.gone != "" && strings.Contains(out, c.gone)) || !strings.Contains(out, c.kept) {
			t.Errorf("Redact(%q) = %q", c.in, out)
		}
	}
}
