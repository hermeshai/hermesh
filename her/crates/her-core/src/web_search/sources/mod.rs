//! The search services: one file each, translating a request into the service's and its answer
//! into [`RawHit`]s. Everything else about a result is the pipeline's, so a new service cannot
//! change what the model gets (docs/kernel/web-search-design.md §5).

mod bocha;
mod brave;
mod deepseek;
mod exa;
mod parallel;
mod tavily;

use async_trait::async_trait;
use chrono::NaiveDate;
use her_mcp::CallOptions;
use her_mcp::CallToolResult;
use her_mcp::McpError;
use her_mcp::McpServer;
use her_mcp::ServerContext;
use her_mcp::types::Content;
use her_protocol::McpServerConfig;
use her_protocol::web_search::WebSearchErrorKind;
use her_protocol::web_search::WebSearchNeeds;
use parking_lot::Mutex;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::LazyLock;
use tokio_util::sync::CancellationToken;

use super::pipeline::RawHit;

/// The services in their default order (design §3.3).
pub(crate) static ALL: [&dyn Adapter; 6] = [&deepseek::DeepSeek, &exa::Exa, &parallel::Parallel, &tavily::Tavily, &brave::Brave, &bocha::Bocha];

/// One attempt, the longest seen being 11.7 s. A service that takes longer gives way to the next.
pub(crate) const ATTEMPT_SECS: u64 = 20;
/// Far beyond any real answer (57 KB at most, measured): only against a broken service.
const MAX_ANSWER_BYTES: usize = 4 << 20;

pub(crate) struct Spec {
    pub(crate) id: &'static str,
    pub(crate) name: &'static str,
    pub(crate) needs: WebSearchNeeds,
    /// The variable that can hold its key; a model account's key is the account's business.
    pub(crate) env: Option<&'static str>,
    /// Where to get a key.
    pub(crate) key_url: Option<&'static str>,
    /// Empty for a model account: the account's own address.
    pub(crate) endpoint: &'static str,
}

/// How much a query says of the time it wants.
#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum Recency {
    Day,
    Week,
    Month,
    Year,
}

impl Recency {
    pub(crate) fn days(self) -> u64 {
        match self {
            Recency::Day => 1,
            Recency::Week => 7,
            Recency::Month => 30,
            Recency::Year => 365,
        }
    }
}

/// A search as the tool understood it.
#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct SearchRequest {
    pub(crate) query: String,
    pub(crate) allowed: Vec<String>,
    pub(crate) blocked: Vec<String>,
    pub(crate) recency: Option<Recency>,
    pub(crate) since: Option<NaiveDate>,
}

/// What an attempt needs from outside.
pub(crate) struct Net {
    pub(crate) http: reqwest::Client,
    pub(crate) key: Option<String>,
    pub(crate) endpoint: String,
    pub(crate) cancel: CancellationToken,
}

/// A service's answer.
#[derive(Debug, Default)]
pub(crate) struct Found {
    pub(crate) hits: Vec<RawHit>,
    /// The service itself kept to `recency`, so the pipeline's check is only a second look.
    pub(crate) recency_applied: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum SearchError {
    Auth,
    Quota,
    RateLimited,
    /// Why, for the log.
    Unreachable(String),
    BadResponse(String),
    Cancelled,
}

impl SearchError {
    /// `None` when the user stopped it: not the service's failure.
    pub(crate) fn kind(&self) -> Option<WebSearchErrorKind> {
        Some(match self {
            SearchError::Auth => WebSearchErrorKind::AuthFailed,
            SearchError::Quota => WebSearchErrorKind::QuotaExhausted,
            SearchError::RateLimited => WebSearchErrorKind::RateLimited,
            SearchError::Unreachable(_) => WebSearchErrorKind::Unreachable,
            SearchError::BadResponse(_) => WebSearchErrorKind::BadResponse,
            SearchError::Cancelled => return None,
        })
    }
}

#[async_trait]
pub(crate) trait Adapter: Send + Sync {
    fn spec(&self) -> &'static Spec;
    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError>;
}

/// `true` for text that speaks of running out rather than of a refused key.
fn mentions_quota(text: &str) -> bool {
    let text = text.to_lowercase();
    ["quota", "balance", "insufficient", "credit", "money", "余额", "额度"].iter().any(|word| text.contains(word))
}

/// An answer's status as what it means here.
pub(crate) fn status_error(status: u16, body: &str) -> SearchError {
    match status {
        401 => SearchError::Auth,
        402 | 432 | 433 => SearchError::Quota,
        403 if mentions_quota(body) => SearchError::Quota,
        403 => SearchError::Auth,
        429 if mentions_quota(body) => SearchError::Quota,
        429 => SearchError::RateLimited,
        408 | 500..=599 => SearchError::Unreachable(format!("HTTP {status}")),
        _ => SearchError::BadResponse(format!("HTTP {status}")),
    }
}

/// Sends a request and reads its JSON answer. Nothing of a failed answer's body goes further.
pub(crate) async fn json_answer(request: reqwest::RequestBuilder) -> Result<Value, SearchError> {
    let mut response = request.send().await.map_err(|e| SearchError::Unreachable(without_url(e)))?;
    let status = response.status();
    let mut body = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|e| SearchError::Unreachable(without_url(e)))? {
        body.extend_from_slice(&chunk);
        if body.len() > MAX_ANSWER_BYTES {
            return Err(SearchError::BadResponse("the answer is too large".into()));
        }
    }
    if !status.is_success() {
        return Err(status_error(status.as_u16(), &String::from_utf8_lossy(&body)));
    }
    serde_json::from_slice(&body).map_err(|e| SearchError::BadResponse(format!("not JSON: {e}")))
}

/// Without its URL: a URL can carry the query.
fn without_url(error: reqwest::Error) -> String {
    error.without_url().to_string()
}

pub(crate) fn text_of(value: &Value, key: &str) -> String {
    value.get(key).and_then(Value::as_str).unwrap_or_default().to_string()
}

pub(crate) fn opt_text(value: &Value, key: &str) -> Option<String> {
    value.get(key).and_then(Value::as_str).filter(|s| !s.trim().is_empty()).map(String::from)
}

pub(crate) fn texts_of(value: &Value, key: &str) -> Vec<String> {
    value.get(key).and_then(Value::as_array).map(|items| items.iter().filter_map(Value::as_str).map(String::from).collect()).unwrap_or_default()
}

/// A connection, and what it was opened with: its address and headers.
type Connection = (String, Arc<McpServer>);

/// The MCP connections to the services that are reached that way, for the whole process.
static CONNECTIONS: LazyLock<Mutex<HashMap<&'static str, Connection>>> = LazyLock::new(|| Mutex::new(HashMap::new()));

fn connection(id: &'static str, url: &str, headers: &BTreeMap<String, String>, tool: &str, net: &Net) -> Arc<McpServer> {
    let fingerprint = format!("{url}\n{headers:?}");
    let mut connections = CONNECTIONS.lock();
    if let Some((known, server)) = connections.get(id)
        && *known == fingerprint
    {
        return server.clone();
    }
    let config = McpServerConfig {
        name: format!("web-search-{id}"),
        url: Some(url.to_string()),
        headers: headers.clone(),
        enabled_tools: Some(vec![tool.to_string()]),
        startup_timeout_secs: Some(ATTEMPT_SECS / 2),
        tool_timeout_secs: Some(ATTEMPT_SECS),
        ..McpServerConfig::default()
    };
    let context = ServerContext {
        http: Some(net.http.clone()),
        roots: Vec::new(),
        cwd: std::env::temp_dir(),
        project_root: None,
        on_status: None,
        oauth: None,
    };
    let server = McpServer::new(config, context);
    server.connect();
    if let Some((_, old)) = connections.insert(id, (fingerprint, server.clone())) {
        tokio::spawn(async move { old.shutdown().await });
    }
    server
}

/// Calls a service's tool over MCP. A connection that failed is forgotten, so the next search
/// starts a fresh one.
pub(crate) async fn call_mcp(id: &'static str, url: &str, headers: BTreeMap<String, String>, tool: &str, arguments: Value, net: &Net) -> Result<CallToolResult, SearchError> {
    let server = connection(id, url, &headers, tool, net);
    let options = CallOptions { cancel: net.cancel.clone(), progress: None };
    match server.call_tool(tool, arguments, options).await {
        Ok(result) if result.is_error => {
            let text = result.content.iter().filter_map(|c| if let Content::Text(text) = c { Some(text.as_str()) } else { None }).collect::<Vec<_>>().join(" ");
            Err(tool_error(&text, net.key.is_some()))
        }
        Ok(result) => Ok(result),
        Err(McpError::Cancelled) => Err(SearchError::Cancelled),
        Err(error) => {
            let mut connections = CONNECTIONS.lock();
            if connections.get(id).is_some_and(|(_, known)| Arc::ptr_eq(known, &server)) {
                connections.remove(id);
            }
            drop(connections);
            tokio::spawn(async move { server.shutdown().await });
            Err(connection_error(&error, net.key.is_some()))
        }
    }
}

/// A connection's failure. Without a key to blame, a refusal says this way in is closed (Exa
/// refuses free use over IPv6 or proxied paths), so the next service gets its turn all the same.
fn connection_error(error: &McpError, has_key: bool) -> SearchError {
    let text = error.to_string();
    let status = text.split("HTTP ").nth(1).and_then(|rest| rest.get(..3)).and_then(|code| code.parse::<u16>().ok());
    let status = match error {
        McpError::AuthRequired | McpError::NeedsLogin(_) => Some(401),
        _ => status,
    };
    match status.map(|status| status_error(status, &text)) {
        Some(SearchError::Auth) if !has_key => SearchError::Unreachable(text),
        Some(known) => known,
        None => SearchError::Unreachable(text),
    }
}

/// A service's tool reported a failure in words.
fn tool_error(text: &str, has_key: bool) -> SearchError {
    let lower = text.to_lowercase();
    if lower.contains("429") || lower.contains("rate limit") || lower.contains("too many") {
        return SearchError::RateLimited;
    }
    if lower.contains("402") || mentions_quota(&lower) {
        return SearchError::Quota;
    }
    let refused = lower.contains("401") || lower.contains("403") || lower.contains("unauthorized") || lower.contains("forbidden") || lower.contains("api key");
    if refused && has_key {
        return SearchError::Auth;
    }
    SearchError::Unreachable(text.chars().take(200).collect())
}

/// The JSON a service's tool answered: its structured result, or the JSON in its text.
pub(crate) fn tool_json(result: &CallToolResult) -> Result<Value, SearchError> {
    if let Some(value) = &result.structured_content {
        return Ok(value.clone());
    }
    let text = result.content.iter().find_map(|c| if let Content::Text(text) = c { Some(text.as_str()) } else { None });
    let text = text.ok_or_else(|| SearchError::BadResponse("no text in the answer".into()))?;
    serde_json::from_str(text).map_err(|e| SearchError::BadResponse(format!("not JSON: {e}")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn statuses_mean_the_same_from_every_service() {
        assert_eq!(status_error(401, ""), SearchError::Auth);
        assert_eq!(status_error(403, "forbidden"), SearchError::Auth);
        assert_eq!(status_error(403, r#"{"message":"You do not have enough money"}"#), SearchError::Quota);
        assert_eq!(status_error(402, ""), SearchError::Quota);
        assert_eq!(status_error(432, ""), SearchError::Quota);
        assert_eq!(status_error(429, "slow down"), SearchError::RateLimited);
        assert_eq!(status_error(429, "monthly quota exceeded"), SearchError::Quota);
        assert_eq!(status_error(503, ""), SearchError::Unreachable("HTTP 503".into()));
        assert_eq!(status_error(400, ""), SearchError::BadResponse("HTTP 400".into()));
    }

    #[test]
    fn a_closed_door_without_a_key_is_not_a_bad_key() {
        let forbidden = McpError::Transport("HTTP 403 Forbidden: use a key".into());
        assert!(matches!(connection_error(&forbidden, false), SearchError::Unreachable(_)));
        assert_eq!(connection_error(&forbidden, true), SearchError::Auth);
        assert_eq!(connection_error(&McpError::Transport("HTTP 429 Too Many Requests: wait".into()), false), SearchError::RateLimited);
        assert!(matches!(connection_error(&McpError::Timeout(10), false), SearchError::Unreachable(_)));
        assert!(matches!(connection_error(&McpError::AuthRequired, false), SearchError::Unreachable(_)));
        assert_eq!(connection_error(&McpError::AuthRequired, true), SearchError::Auth);
    }

    #[test]
    fn a_tools_words_are_understood() {
        assert_eq!(tool_error("Error: 429 rate limited", false), SearchError::RateLimited);
        assert_eq!(tool_error("insufficient credits", true), SearchError::Quota);
        assert_eq!(tool_error("invalid API key", true), SearchError::Auth);
        assert!(matches!(tool_error("invalid API key", false), SearchError::Unreachable(_)));
        assert!(matches!(tool_error("something broke", true), SearchError::Unreachable(_)));
    }
}
