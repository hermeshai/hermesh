//! Error classification shared by all adapters.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::fmt;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ErrorKind {
    Auth,
    QuotaExceeded,
    RateLimited,
    Overloaded,
    Server,
    Network,
    Timeout,
    ContextOverflow,
    InvalidRequest,
    ContentFiltered,
    Protocol,
    Unknown,
}

impl ErrorKind {
    /// Whether the registry may retry automatically (before any content arrived).
    pub fn is_retryable(self) -> bool {
        matches!(
            self,
            ErrorKind::RateLimited
                | ErrorKind::Overloaded
                | ErrorKind::Server
                | ErrorKind::Network
                | ErrorKind::Timeout
        )
    }

    pub fn as_str(self) -> &'static str {
        match self {
            ErrorKind::Auth => "auth",
            ErrorKind::QuotaExceeded => "quota_exceeded",
            ErrorKind::RateLimited => "rate_limited",
            ErrorKind::Overloaded => "overloaded",
            ErrorKind::Server => "server",
            ErrorKind::Network => "network",
            ErrorKind::Timeout => "timeout",
            ErrorKind::ContextOverflow => "context_overflow",
            ErrorKind::InvalidRequest => "invalid_request",
            ErrorKind::ContentFiltered => "content_filtered",
            ErrorKind::Protocol => "protocol",
            ErrorKind::Unknown => "unknown",
        }
    }
}

impl fmt::Display for ErrorKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, thiserror::Error, JsonSchema)]
#[error("{kind}: {message}")]
pub struct LlmError {
    pub kind: ErrorKind,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub provider_code: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub retry_after_ms: Option<u64>,
}

const DEFAULT_OVERFLOW_PATTERNS: &[&str] = &[
    "context length",
    "context_length",
    "maximum context",
    "context window",
    "prompt is too long",
    "too many tokens",
    "input is too long",
    "reduce the length",
];

const QUOTA_PATTERNS: &[&str] = &[
    "insufficient balance",
    "insufficient_quota",
    "insufficient quota",
    "exceeded your current quota",
    "current_quota",
    "arrears",
    "余额不足",
    "欠费",
];
/// Point to money only in a 429: elsewhere "balance" may be a load balancer's.
const QUOTA_WORDS: &[&str] = &["billing", "balance"];
/// A limit per second or minute, sent under the code an empty account uses
/// (`insufficient_quota`): the wording decides here, not the code. Seen 2026-11-04: right after
/// it, the same key and model answered within seconds.
const PASSING_ALLOCATION: &str = "allocated quota exceeded";
/// The same wording for an allowance that is gone for hours or more, or for good.
const LASTING_ALLOCATIONS: &[&str] = &["free allocated", "hour allocated", "week allocated", "month allocated"];

// Error events inside a stream carry no status, only wording.
const OVERLOADED_PATTERNS: &[&str] = &["overloaded", "overload", "busy", "capacity", "繁忙", "过载"];
const RATE_LIMIT_PATTERNS: &[&str] =
    &["rate_limit", "rate limit", "too many requests", "throttl", "限流", "频率", "请求过多", "并发"];
const SERVER_PATTERNS: &[&str] = &["api_error", "server", "internal", "内部错误", "服务异常"];
const TIMEOUT_PATTERNS: &[&str] = &["timeout", "timed out", "超时"];

impl LlmError {
    pub fn new(kind: ErrorKind, message: impl Into<String>) -> Self {
        Self {
            kind,
            message: message.into(),
            status: None,
            provider_code: None,
            retry_after_ms: None,
        }
    }

    /// Classify a non-2xx HTTP response.
    pub fn from_http(
        status: u16,
        body: &str,
        retry_after_ms: Option<u64>,
        overflow_patterns: &[String],
    ) -> Self {
        let parsed: Option<Value> = serde_json::from_str(body).ok();
        let (message, code) = parsed
            .as_ref()
            .map(extract_error_fields)
            .unwrap_or((None, None));
        let message = message.unwrap_or_else(|| {
            let trimmed = body.trim();
            if trimmed.is_empty() {
                format!("HTTP {status}")
            } else {
                truncate_chars(trimmed, 500)
            }
        });
        let haystack = format!("{} {}", message, code.clone().unwrap_or_default()).to_lowercase();
        let kind = classify(status, &haystack, overflow_patterns);
        Self {
            kind,
            message,
            status: Some(status),
            provider_code: code,
            retry_after_ms,
        }
    }

    /// Classify an error object that arrived inside a stream.
    pub fn from_stream_error(error: &Value, overflow_patterns: &[String]) -> Self {
        let (message, code) = extract_error_fields(&serde_json::json!({ "error": error }));
        let message = message.unwrap_or_else(|| error.to_string());
        let haystack = format!("{} {}", message, code.clone().unwrap_or_default()).to_lowercase();
        let has = |patterns: &[&str]| patterns.iter().any(|p| haystack.contains(p));
        // Errors that will not pass come first, so that a broad word below cannot make them
        // retried.
        let kind = if haystack.contains("connection_login_required")
            || haystack.contains("local_auth_invalid")
        {
            ErrorKind::Auth
        } else if matches_any(&haystack, DEFAULT_OVERFLOW_PATTERNS, overflow_patterns) {
            ErrorKind::ContextOverflow
        } else if is_passing_allocation(&haystack) {
            ErrorKind::RateLimited
        } else if has(QUOTA_PATTERNS) {
            ErrorKind::QuotaExceeded
        } else if has(OVERLOADED_PATTERNS) {
            ErrorKind::Overloaded
        } else if has(RATE_LIMIT_PATTERNS) {
            ErrorKind::RateLimited
        } else if haystack.contains("authentication") || haystack.contains("permission") {
            ErrorKind::Auth
        } else if haystack.contains("invalid_request") {
            ErrorKind::InvalidRequest
        } else if has(SERVER_PATTERNS) {
            ErrorKind::Server
        } else if has(TIMEOUT_PATTERNS) {
            ErrorKind::Timeout
        } else {
            ErrorKind::Unknown
        };
        Self {
            kind,
            message,
            status: None,
            provider_code: code,
            retry_after_ms: None,
        }
    }

    /// The provider says the requested model does not exist (it may have been retired).
    pub fn is_missing_model(&self) -> bool {
        if !matches!(self.kind, ErrorKind::InvalidRequest | ErrorKind::Unknown) {
            return false;
        }
        let code = self.provider_code.as_deref().unwrap_or_default().to_lowercase();
        let message = self.message.to_lowercase();
        code.contains("model_not_found")
            || (message.contains("model")
                && ["not exist", "does not exist", "not found", "no such model", "unknown model"]
                    .iter()
                    .any(|p| message.contains(p)))
    }

    pub fn from_reqwest(err: &reqwest::Error) -> Self {
        let kind = if err.is_timeout() {
            ErrorKind::Timeout
        } else {
            ErrorKind::Network
        };
        let mut message = err.to_string();
        let mut source = std::error::Error::source(err);
        while let Some(inner) = source {
            message.push_str(": ");
            message.push_str(&inner.to_string());
            source = inner.source();
        }
        Self::new(kind, message)
    }
}

fn classify(status: u16, haystack: &str, overflow_patterns: &[String]) -> ErrorKind {
    if haystack.contains("connection_login_required")
        || haystack.contains("connection_permission_required")
    {
        return ErrorKind::Auth;
    }
    match status {
        401 | 403 => ErrorKind::Auth,
        402 => ErrorKind::QuotaExceeded,
        408 => ErrorKind::Timeout,
        429 if is_passing_allocation(haystack) => ErrorKind::RateLimited,
        429 if QUOTA_PATTERNS.iter().chain(QUOTA_WORDS).any(|p| haystack.contains(p)) => ErrorKind::QuotaExceeded,
        429 => ErrorKind::RateLimited,
        503 | 529 => ErrorKind::Overloaded,
        500..=599 if haystack.contains("overloaded") => ErrorKind::Overloaded,
        500..=599 => ErrorKind::Server,
        400..=499 if matches_any(haystack, DEFAULT_OVERFLOW_PATTERNS, overflow_patterns) => {
            ErrorKind::ContextOverflow
        }
        400..=499 if haystack.contains("content_filter") || haystack.contains("content filter") => {
            ErrorKind::ContentFiltered
        }
        400..=499 => ErrorKind::InvalidRequest,
        _ => ErrorKind::Unknown,
    }
}

fn is_passing_allocation(haystack: &str) -> bool {
    haystack.contains(PASSING_ALLOCATION) && !LASTING_ALLOCATIONS.iter().any(|p| haystack.contains(p))
}

fn matches_any(haystack: &str, defaults: &[&str], extra: &[String]) -> bool {
    defaults.iter().any(|p| haystack.contains(p))
        || extra.iter().any(|p| haystack.contains(&p.to_lowercase()))
}

/// Pulls `(message, code)` out of the common error body shapes.
fn extract_error_fields(value: &Value) -> (Option<String>, Option<String>) {
    let err = value.get("error").unwrap_or(value);
    let message = err
        .get("message")
        .and_then(Value::as_str)
        .or_else(|| err.as_str())
        .or_else(|| value.get("message").and_then(Value::as_str))
        .map(str::to_string);
    let code = err
        .get("code")
        .and_then(|c| match c {
            Value::String(s) => Some(s.clone()),
            Value::Number(n) => Some(n.to_string()),
            _ => None,
        })
        .or_else(|| err.get("type").and_then(Value::as_str).map(str::to_string));
    (message, code)
}

pub(crate) fn truncate_chars(text: &str, max: usize) -> String {
    if text.chars().count() <= max {
        return text.to_string();
    }
    let mut out: String = text.chars().take(max).collect();
    out.push('…');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classifies_common_statuses() {
        let none: Vec<String> = Vec::new();
        assert_eq!(
            LlmError::from_http(401, "{}", None, &none).kind,
            ErrorKind::Auth
        );
        assert_eq!(
            LlmError::from_http(
                402,
                r#"{"error":{"message":"Insufficient Balance"}}"#,
                None,
                &none
            )
            .kind,
            ErrorKind::QuotaExceeded
        );
        assert_eq!(
            LlmError::from_http(429, "slow down", None, &none).kind,
            ErrorKind::RateLimited
        );
        assert_eq!(
            LlmError::from_http(529, "", None, &none).kind,
            ErrorKind::Overloaded
        );
        assert_eq!(
            LlmError::from_http(500, "", None, &none).kind,
            ErrorKind::Server
        );
        let overflow = LlmError::from_http(
            400,
            r#"{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}"#,
            None,
            &none,
        );
        assert_eq!(overflow.kind, ErrorKind::ContextOverflow);
        assert_eq!(
            overflow.provider_code.as_deref(),
            Some("context_length_exceeded")
        );
        assert_eq!(
            LlmError::from_http(400, "bad", None, &none).kind,
            ErrorKind::InvalidRequest
        );
    }

    #[test]
    fn missing_models_are_recognized() {
        let gone = LlmError::from_http(400, r#"{"error":{"message":"Model Not Exist","code":"invalid_request_error"}}"#, None, &[]);
        assert!(gone.is_missing_model());
        let openai = LlmError::from_http(404, r#"{"error":{"message":"The model `x` does not exist","code":"model_not_found"}}"#, None, &[]);
        assert!(openai.is_missing_model());
        assert!(!LlmError::from_http(400, r#"{"error":{"message":"bad tool schema"}}"#, None, &[]).is_missing_model());
        assert!(!LlmError::from_http(401, r#"{"error":{"message":"model not found"}}"#, None, &[]).is_missing_model());
    }

    #[test]
    fn passing_trouble_is_told_from_lasting_trouble() {
        let none: Vec<String> = Vec::new();
        assert_eq!(LlmError::from_http(408, "", None, &none).kind, ErrorKind::Timeout);
        let quota = |body: &str| LlmError::from_http(429, body, None, &none).kind;
        assert_eq!(quota(r#"{"error":{"message":"您的账户已欠费，请充值后重试","code":"1113"}}"#), ErrorKind::QuotaExceeded);
        assert_eq!(quota(r#"{"error":{"type":"exceeded_current_quota_error","message":"suspended"}}"#), ErrorKind::QuotaExceeded);
        assert_eq!(quota(r#"{"error":{"message":"Requests rate limit exceeded"}}"#), ErrorKind::RateLimited);
        let stream = |error: serde_json::Value| LlmError::from_stream_error(&error, &none).kind;
        assert_eq!(stream(serde_json::json!({"code": "Throttling.RateQuota", "message": "Requests rate limit exceeded"})), ErrorKind::RateLimited);
        assert_eq!(stream(serde_json::json!({"message": "系统繁忙，请稍后再试"})), ErrorKind::Overloaded);
        assert_eq!(stream(serde_json::json!({"message": "请求频率过高"})), ErrorKind::RateLimited);
        assert_eq!(stream(serde_json::json!({"type": "overloaded_error", "message": "Overloaded"})), ErrorKind::Overloaded);
        assert_eq!(stream(serde_json::json!({"message": "upstream timed out"})), ErrorKind::Timeout);
        assert_eq!(stream(serde_json::json!({"message": "Internal error"})), ErrorKind::Server);
        // Broad words do not make lasting errors retried.
        assert_eq!(stream(serde_json::json!({"message": "input exceeds the maximum context length of the model's capacity"})), ErrorKind::ContextOverflow);
        assert_eq!(stream(serde_json::json!({"code": "insufficient_quota", "message": "server says no"})), ErrorKind::QuotaExceeded);
        assert_eq!(stream(serde_json::json!({"message": "tool schema is malformed"})), ErrorKind::Unknown);
        assert_eq!(stream(serde_json::json!({"message": "load balancer: server unavailable"})), ErrorKind::Server);
    }

    #[test]
    fn a_limit_per_minute_is_told_from_an_empty_account_under_the_same_code() {
        let none: Vec<String> = Vec::new();
        let body = |message: &str| serde_json::json!({"error": {"message": message, "code": "insufficient_quota"}}).to_string();
        let http = |message: &str| LlmError::from_http(429, &body(message), None, &none).kind;
        let stream = |message: &str| {
            let error = serde_json::json!({"message": message, "code": "insufficient_quota"});
            LlmError::from_stream_error(&error, &none).kind
        };

        // Passes by itself: the body as it came back, and the forms that say to try again soon.
        for passing in [
            "Allocated quota exceeded, please increase your quota limit. For details, see: https://www.alibabacloud.com/help/en/model-studio/error-code#token-limit",
            "concurrency allocated quota exceeded",
            "usage allocated quota exceeded. please try again later.",
        ] {
            assert_eq!(http(passing), ErrorKind::RateLimited, "{passing}");
            assert_eq!(stream(passing), ErrorKind::RateLimited, "{passing}");
            assert!(http(passing).is_retryable());
        }

        // Gone for hours or longer, or for good: reported at once.
        for lasting in [
            "Free allocated quota exceeded.",
            "hour allocated quota exceeded",
            "week allocated quota exceeded",
            "month allocated quota exceeded",
            "Your token-plan quota has been exhausted.",
            "You exceeded your current quota, please check your plan and billing details.",
            "账户已欠费",
        ] {
            assert_eq!(http(lasting), ErrorKind::QuotaExceeded, "{lasting}");
            assert_eq!(stream(lasting), ErrorKind::QuotaExceeded, "{lasting}");
            assert!(!http(lasting).is_retryable());
        }

        // Only a 429 speaks of the moment; the code alone still means an empty account.
        let allocated = body("Allocated quota exceeded");
        assert_eq!(LlmError::from_http(402, &allocated, None, &none).kind, ErrorKind::QuotaExceeded);
        assert_eq!(http("server says no"), ErrorKind::QuotaExceeded);
        assert_eq!(stream("server says no"), ErrorKind::QuotaExceeded);
    }

    #[test]
    fn custom_overflow_pattern() {
        let patterns = vec!["too much input".to_string()];
        let err = LlmError::from_http(400, "Too much input for this model", None, &patterns);
        assert_eq!(err.kind, ErrorKind::ContextOverflow);
    }
}
