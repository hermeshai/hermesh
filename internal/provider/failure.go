package provider

import (
	"errors"
	"regexp"
	"strings"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Failure explains why a turn or a session start failed.
type Failure struct {
	Kind       sessionv1.FailureKind
	Message    string
	HTTPStatus int
	RetryAfter time.Duration
}

// GetKind returns the kind (UNSPECIFIED for nil).
func (f *Failure) GetKind() sessionv1.FailureKind {
	if f == nil {
		return sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED
	}
	return f.Kind
}

// FailureError is an error carrying a classified failure (Start returns it
// when it knows why it failed).
type FailureError struct{ Failure }

func (e *FailureError) Error() string { return e.Message }

// Fail returns an error of the given kind.
func Fail(kind sessionv1.FailureKind, msg string) error {
	return &FailureError{Failure{Kind: kind, Message: msg}}
}

// FailureOf classifies an error: its own failure when it carries one,
// otherwise by its text, with fallback for unrecognized errors.
func FailureOf(err error, fallback sessionv1.FailureKind) *Failure {
	var fe *FailureError
	if errors.As(err, &fe) {
		f := fe.Failure
		return &f
	}
	f := &Failure{Kind: Classify(err.Error()), Message: err.Error()}
	if f.Kind == sessionv1.FailureKind_FAILURE_KIND_UNKNOWN {
		f.Kind = fallback
	}
	return f
}

type kindInfo struct {
	retryable bool
	hint      string
}

var kinds = map[sessionv1.FailureKind]kindInfo{
	sessionv1.FailureKind_FAILURE_KIND_AUTH:                 {false, "the agent is not logged in on this machine; ask the owner to run the agent's CLI once as the same user and log in"},
	sessionv1.FailureKind_FAILURE_KIND_QUOTA:                {false, "the agent's usage limit is reached; retry after it resets, or use another agent or model"},
	sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT:           {true, "the provider is rate limiting or over capacity; send the message again later"},
	sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR:       {true, "the provider returned a server error; send the message again later"},
	sessionv1.FailureKind_FAILURE_KIND_NETWORK:              {true, "the connection to the provider failed; send the message again"},
	sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW:     {false, "the conversation no longer fits the model's context; the next message continues in a fresh context (work in the worktree is kept)"},
	sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE:    {false, "the model does not exist or this account cannot use it; start a session with another model"},
	sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST:      {false, "the provider rejected the request; change the message"},
	sessionv1.FailureKind_FAILURE_KIND_REFUSED:              {false, "the model refused the task; rephrase it"},
	sessionv1.FailureKind_FAILURE_KIND_LIMIT_REACHED:        {false, "the turn hit the agent's step or token limit; send \"continue\" to go on"},
	sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED:      {false, "the provider session could not be resumed; the next message continues in a fresh context (work in the worktree is kept)"},
	sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED:        {true, "the agent process exited unexpectedly; send the message again to resume the session"},
	sessionv1.FailureKind_FAILURE_KIND_STALLED:              {true, "the agent made no progress for too long and was stopped; send the message again to resume the session"},
	sessionv1.FailureKind_FAILURE_KIND_TIMEOUT:              {false, "the turn exceeded the machine's time limit; send the message again to continue"},
	sessionv1.FailureKind_FAILURE_KIND_START_FAILED:         {false, "the agent could not be started; run `hermeshd doctor` on the machine"},
	sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED:          {false, "the turn was stopped on purpose; send a message to go on in this session"},
	sessionv1.FailureKind_FAILURE_KIND_BASE_REF_UNAVAILABLE: {false, "check the base ref name or sync the corresponding branch/tag into the project's local repository, then send another message in the same session to retry the original task"},
	sessionv1.FailureKind_FAILURE_KIND_UNKNOWN:              {false, ""},
}

// Retryable reports whether retrying the same request later may succeed.
func Retryable(k sessionv1.FailureKind) bool { return kinds[k].retryable }

// Proto converts f for the API, redacting and truncating the message.
func (f *Failure) Proto() *sessionv1.Failure {
	if f == nil {
		return nil
	}
	k := f.Kind
	if k == sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED {
		k = sessionv1.FailureKind_FAILURE_KIND_UNKNOWN
	}
	msg, _ := Truncate(Redact(strings.TrimSpace(f.Message)), 2<<10)
	return &sessionv1.Failure{Kind: k, Message: msg, Retryable: kinds[k].retryable, Hint: kinds[k].hint,
		RetryAfterSeconds: uint32(f.RetryAfter / time.Second), HttpStatus: uint32(f.HTTPStatus)}
}

// FromHTTPStatus classifies a provider HTTP status; text refines the
// ambiguous ones (403 can be a usage limit, 400 a context overflow).
func FromHTTPStatus(status int, text string) sessionv1.FailureKind {
	lower := strings.ToLower(text)
	switch {
	case status == 0:
		return sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED
	case strings.Contains(lower, concurrentLimit):
		return sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT
	case status == 401 || status == 403:
		if strings.Contains(lower, "usage limit") {
			return sessionv1.FailureKind_FAILURE_KIND_QUOTA
		}
		return sessionv1.FailureKind_FAILURE_KIND_AUTH
	case status == 402:
		return sessionv1.FailureKind_FAILURE_KIND_QUOTA
	case status == 404:
		return sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE
	case status == 408:
		return sessionv1.FailureKind_FAILURE_KIND_NETWORK
	case status == 413 || (status == 400 && isContextOverflow(lower)):
		return sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW
	case status == 429 || status == 529:
		return sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT
	case status >= 500:
		return sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR
	case status >= 400:
		return sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST
	}
	return sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED
}

const concurrentLimit = "concurrent request limit"

// HTTP status codes only when not part of a longer number ("402913 tokens").
var (
	codeAuth     = regexp.MustCompile(`(^|[^0-9])(401|403)([^0-9]|$)`)
	codeForbid   = regexp.MustCompile(`(^|[^0-9])403([^0-9]|$)`)
	codeQuota    = regexp.MustCompile(`(^|[^0-9])402([^0-9]|$)`)
	codeCapacity = regexp.MustCompile(`(^|[^0-9])(429|529)([^0-9]|$)`)
	code5xx      = regexp.MustCompile(`(^|[^0-9])5[0-9]{2}([^0-9]|$)`)
	code400      = regexp.MustCompile(`(^|[^0-9])400([^0-9]|$)`)
)

func isContextOverflow(lower string) bool {
	return containsAny(lower, "context length", "context_length_exceeded", "maximum context", "prompt is too long", "prompt_too_long",
		"context window", "context_window", "contextwindowexceeded", "context size has been exceeded", "too many tokens") ||
		(strings.Contains(lower, "token") && strings.Contains(lower, "limit") && !strings.Contains(lower, "rate"))
}

// resumeRejected are provider messages that positively identify a refused
// resume (a missing transcript), as opposed to any failure that happened
// during a resumed run. Keep it tight: a false positive throws away a
// recoverable conversation.
var resumeRejected = []string{
	"no conversation found",      // Claude Code: --resume with a missing transcript
	"no rollout found",           // Codex: thread/resume of an unknown thread
	"no saved session found",     // Qwen Code
	"session not found",          // ACP agents
	"thread not found",           //
	"conversation not found",     //
	"unknown session",            //
	"session does not exist",     //
	"invalid session id",         // Codex: malformed thread id
	"invalid session identifier", // Qoder
	"session is not resumable",   // DeepSeek Harness: no saved session
}

// IsResumeRejected reports whether text says a resumed session is gone.
func IsResumeRejected(text string) bool {
	return containsAny(strings.ToLower(text), resumeRejected...)
}

// Classify maps a provider or CLI error text to a failure kind. Rules are
// ordered from specific to generic:
// context overflow before quota (so "token limit" is not a quota), auth
// before quota, provider errors before network.
func Classify(text string) sessionv1.FailureKind {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch {
	case lower == "":
		return sessionv1.FailureKind_FAILURE_KIND_UNKNOWN
	case strings.Contains(lower, concurrentLimit):
		return sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT
	case codeForbid.MatchString(lower) && strings.Contains(lower, "usage limit"):
		return sessionv1.FailureKind_FAILURE_KIND_QUOTA
	case IsResumeRejected(lower):
		return sessionv1.FailureKind_FAILURE_KIND_RESUME_REJECTED
	case isContextOverflow(lower):
		return sessionv1.FailureKind_FAILURE_KIND_CONTEXT_OVERFLOW
	case codeAuth.MatchString(lower) || containsAny(lower, "unauthorized", "authentication required", "authentication_error",
		"login required", "not logged in", "please log in", "please login", "please run /login", "invalid api key", "invalid x-api-key",
		"invalid_api_key", "api key not valid", "refresh token", "token expired", "access token", "auth_required", "does not have access"):
		return sessionv1.FailureKind_FAILURE_KIND_AUTH
	case codeQuota.MatchString(lower) || containsAny(lower, "usage limit", "usagelimitexceeded", "insufficient_quota", "insufficient balance",
		"insufficient_balance", "credit balance", "out of credits", "quota", "billing", "you've hit your limit", "you’ve hit your limit"):
		return sessionv1.FailureKind_FAILURE_KIND_QUOTA
	case codeCapacity.MatchString(lower) || containsAny(lower, "rate limit", "rate_limit", "ratelimitexceeded", "too many requests",
		"overloaded", "serveroverloaded", "no capacity", "capacity exceeded", "resource_exhausted", "resource exhausted"):
		return sessionv1.FailureKind_FAILURE_KIND_RATE_LIMIT
	case code5xx.MatchString(lower) || containsAny(lower, "internal server error", "internalservererror", "server had an error",
		"service unavailable", "bad gateway", "gateway timeout"):
		return sessionv1.FailureKind_FAILURE_KIND_PROVIDER_ERROR
	case containsAny(lower, "stream disconnected", "connection closed", "connection reset", "connection refused", "connection error",
		"mid-response", "error sending request", "unable to connect", "dial tcp", "no such host", "dns", "i/o timeout",
		"network error", "network is unreachable", "econnreset", "econnrefused", "etimedout", "socket hang up", "fetch failed", "request timed out", "tls handshake"):
		return sessionv1.FailureKind_FAILURE_KIND_NETWORK
	case strings.Contains(lower, "model") && containsAny(lower, "not found", "not exist", "unknown", "unrecognized", "not available",
		"not supported", "issue with the selected model", "may not exist", "no access"):
		return sessionv1.FailureKind_FAILURE_KIND_MODEL_UNAVAILABLE
	case code400.MatchString(lower) || containsAny(lower, "invalid_request_error", "bad request", "badrequest"):
		return sessionv1.FailureKind_FAILURE_KIND_INVALID_REQUEST
	case containsAny(lower, "executable file not found", "exec format error"):
		return sessionv1.FailureKind_FAILURE_KIND_START_FAILED
	}
	return sessionv1.FailureKind_FAILURE_KIND_UNKNOWN
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	// A credential after the scheme has a digit ("basic authentication" is prose).
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z._~+/=-]*[0-9][a-z0-9._~+/=-]{7,}`), "$1 [redacted]"},
	{regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password|passwd|authorization)["']?\s*[:=]\s*["']?)[^\s"',;&]{4,}`), "${1}[redacted]"},
	{regexp.MustCompile(`\b(sk|pk|rk|sess|ghp|gho|ghs|github_pat|xox[abpr]|glpat|AKIA)[-_][A-Za-z0-9_-]{8,}`), "[redacted]"},
	{regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`), "${1}[redacted]@"},
}

var opaqueRun = regexp.MustCompile(`[A-Za-z0-9+/_=]{32,}`)

// Redact removes credentials and long opaque tokens from text that leaves
// the machine.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	// Long runs mixing upper, lower and digits look like keys, not words
	// or paths.
	return opaqueRun.ReplaceAllStringFunc(s, func(run string) string {
		var up, low, dig bool
		for _, c := range run {
			switch {
			case c >= 'A' && c <= 'Z':
				up = true
			case c >= 'a' && c <= 'z':
				low = true
			case c >= '0' && c <= '9':
				dig = true
			}
		}
		if up && low && dig {
			return "[redacted]"
		}
		return run
	})
}
