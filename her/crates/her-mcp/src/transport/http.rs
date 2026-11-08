//! A remote server over Streamable HTTP: one POST per client message, answered with JSON or an
//! SSE stream; an optional GET stream for messages the server starts.

use async_trait::async_trait;
use eventsource_stream::Eventsource;
use futures::StreamExt;
use parking_lot::Mutex;
use reqwest::StatusCode;
use reqwest::header::ACCEPT;
use reqwest::header::CONTENT_TYPE;
use reqwest::header::HeaderMap;
use reqwest::header::HeaderName;
use reqwest::header::HeaderValue;
use serde_json::Value;
use serde_json::json;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

use super::Inbound;
use super::Transport;
use crate::McpError;
use crate::oauth::BearerSource;

const SESSION_HEADER: &str = "mcp-session-id";
const VERSION_HEADER: &str = "mcp-protocol-version";
/// Largest JSON response body.
const MAX_BODY: usize = 16 << 20;

pub(crate) struct HttpTransport {
    inner: Arc<HttpInner>,
}

struct HttpInner {
    server: String,
    client: reqwest::Client,
    url: String,
    headers: HeaderMap,
    session_id: Mutex<Option<String>>,
    protocol_version: Mutex<Option<String>>,
    inbound: mpsc::UnboundedSender<Inbound>,
    closed: CancellationToken,
    /// OAuth: the access token for each request, refreshed once after a 401.
    auth: Option<Arc<dyn BearerSource>>,
}

/// Fixed request headers, checked once.
pub(crate) fn header_map(headers: &[(String, String)]) -> Result<HeaderMap, String> {
    let mut map = HeaderMap::new();
    for (name, value) in headers {
        let name = HeaderName::from_bytes(name.as_bytes()).map_err(|_| format!("invalid header name '{name}'"))?;
        let value = HeaderValue::from_str(value).map_err(|_| format!("invalid value for header '{name}'"))?;
        map.insert(name, value);
    }
    Ok(map)
}

pub(crate) fn connect(
    server: String,
    client: reqwest::Client,
    url: String,
    headers: &[(String, String)],
    auth: Option<Arc<dyn BearerSource>>,
    inbound: mpsc::UnboundedSender<Inbound>,
) -> Result<HttpTransport, String> {
    if !(url.starts_with("http://") || url.starts_with("https://")) {
        return Err(format!("the URL must start with http:// or https://: {url}"));
    }
    Ok(HttpTransport {
        inner: Arc::new(HttpInner {
            server,
            client,
            url,
            headers: header_map(headers)?,
            session_id: Mutex::new(None),
            protocol_version: Mutex::new(None),
            inbound,
            closed: CancellationToken::new(),
            auth,
        }),
    })
}

impl HttpInner {
    fn request(&self, method: reqwest::Method, bearer: Option<&str>) -> reqwest::RequestBuilder {
        let mut request = self.client.request(method, &self.url).headers(self.headers.clone());
        if let Some(token) = bearer {
            request = request.bearer_auth(token);
        }
        if let Some(id) = self.session_id.lock().clone() {
            request = request.header(SESSION_HEADER, id);
        }
        if let Some(version) = self.protocol_version.lock().clone() {
            request = request.header(VERSION_HEADER, version);
        }
        request
    }

    fn remember_session(&self, response: &reqwest::Response) {
        if let Some(id) = response.headers().get(SESSION_HEADER).and_then(|v| v.to_str().ok()) {
            *self.session_id.lock() = Some(id.to_string());
        }
    }

    fn deliver(&self, value: Value) {
        match value {
            Value::Array(items) => {
                for item in items {
                    let _ = self.inbound.send(Inbound::Message(item));
                }
            }
            other => {
                let _ = self.inbound.send(Inbound::Message(other));
            }
        }
    }

    /// Reads one SSE stream. When it ends before the response to `request_id`, only that
    /// request fails.
    async fn read_events(self: Arc<Self>, response: reqwest::Response, request_id: Option<Value>) {
        let mut events = response.bytes_stream().eventsource();
        let mut answered = request_id.is_none();
        loop {
            let event = tokio::select! {
                _ = self.closed.cancelled() => return,
                event = events.next() => event,
            };
            match event {
                Some(Ok(event)) => {
                    if !(event.event.is_empty() || event.event == "message") || event.data.trim().is_empty() {
                        continue;
                    }
                    match serde_json::from_str::<Value>(&event.data) {
                        Ok(value) => {
                            if let Some(id) = &request_id
                                && value.get("id") == Some(id)
                                && value.get("method").is_none()
                            {
                                answered = true;
                            }
                            self.deliver(value);
                        }
                        Err(_) => tracing::debug!(server = %self.server, "ignored an SSE event that is not JSON"),
                    }
                }
                Some(Err(err)) => {
                    tracing::debug!(server = %self.server, %err, "SSE stream failed");
                    break;
                }
                None => break,
            }
        }
        if !answered && let Some(id) = request_id {
            self.deliver(json!({
                "jsonrpc": "2.0", "id": id,
                "error": { "code": -32000, "message": "the response stream ended before the response" }
            }));
        }
    }

    /// The stream for messages the server starts, such as `tools/list_changed`. Reconnects
    /// with backoff; a server without one answers 405.
    async fn listen(self: Arc<Self>) {
        let mut failures = 0u32;
        let mut retry = Duration::from_secs(1);
        while !self.closed.is_cancelled() && failures < 5 {
            let bearer = match &self.auth {
                Some(auth) => auth.token().await.ok(),
                None => None,
            };
            let request = self.request(reqwest::Method::GET, bearer.as_deref()).header(ACCEPT, "text/event-stream");
            let response = tokio::select! {
                _ = self.closed.cancelled() => return,
                response = request.send() => response,
            };
            match response {
                Ok(response) if response.status() == StatusCode::METHOD_NOT_ALLOWED => return,
                Ok(response) if response.status().is_success() => {
                    failures = 0;
                    retry = Duration::from_secs(1);
                    self.clone().read_events(response, None).await;
                }
                Ok(_) | Err(_) => failures += 1,
            }
            tokio::select! {
                _ = self.closed.cancelled() => return,
                _ = tokio::time::sleep(retry) => {}
            }
            retry = (retry * 2).min(Duration::from_secs(30));
        }
    }
}

#[async_trait]
impl Transport for HttpTransport {
    async fn send(&self, message: Value) -> Result<(), McpError> {
        let inner = &self.inner;
        let is_request = message.get("method").is_some() && message.get("id").is_some();
        let request_id = is_request.then(|| message.get("id").cloned()).flatten();
        let had_session = inner.session_id.lock().is_some();
        let mut bearer = match &inner.auth {
            Some(auth) => Some(auth.token().await?),
            None => None,
        };
        let mut refreshed = false;
        let response = loop {
            let request = inner
                .request(reqwest::Method::POST, bearer.as_deref())
                .header(CONTENT_TYPE, "application/json")
                .header(ACCEPT, "application/json, text/event-stream")
                .json(&message);
            let response = tokio::select! {
                _ = inner.closed.cancelled() => return Err(McpError::Closed("the connection was closed".into())),
                response = request.send() => response.map_err(|e| McpError::Transport(format!("request failed: {e}")))?,
            };
            // An expired or revoked token: refresh once and send again.
            if response.status() == StatusCode::UNAUTHORIZED
                && !refreshed
                && let (Some(auth), Some(stale)) = (&inner.auth, &bearer)
            {
                bearer = Some(auth.refresh(stale).await?);
                refreshed = true;
                continue;
            }
            break response;
        };
        inner.remember_session(&response);
        let status = response.status();
        if status == StatusCode::ACCEPTED || status == StatusCode::NO_CONTENT {
            return if is_request {
                Err(McpError::Protocol("the server accepted a request without answering it".into()))
            } else {
                Ok(())
            };
        }
        if status == StatusCode::UNAUTHORIZED {
            return Err(McpError::AuthRequired);
        }
        if status == StatusCode::NOT_FOUND && had_session {
            return Err(McpError::SessionExpired);
        }
        if !status.is_success() {
            let body = response.text().await.unwrap_or_default();
            let excerpt: String = body.chars().take(300).collect();
            return Err(McpError::Transport(format!("HTTP {status}: {}", excerpt.trim())));
        }
        if !is_request {
            return Ok(());
        }
        let event_stream = response
            .headers()
            .get(CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .is_some_and(|v| v.starts_with("text/event-stream"));
        if event_stream {
            tokio::spawn(inner.clone().read_events(response, request_id));
            return Ok(());
        }
        let bytes = response
            .bytes()
            .await
            .map_err(|e| McpError::Transport(format!("reading the response failed: {e}")))?;
        if bytes.len() > MAX_BODY {
            return Err(McpError::Protocol("the response is larger than 16 MB".into()));
        }
        let value: Value =
            serde_json::from_slice(&bytes).map_err(|e| McpError::Protocol(format!("the response is not JSON: {e}")))?;
        inner.deliver(value);
        Ok(())
    }

    fn set_protocol_version(&self, version: &str) {
        *self.inner.protocol_version.lock() = Some(version.to_string());
    }

    fn initialized(&self) {
        tokio::spawn(self.inner.clone().listen());
    }

    async fn close(&self) {
        let inner = &self.inner;
        inner.closed.cancel();
        let session = inner.session_id.lock().clone();
        if session.is_some() {
            let request = inner.request(reqwest::Method::DELETE, None).send();
            let _ = tokio::time::timeout(Duration::from_secs(1), request).await;
        }
        let _ = inner.inbound.send(Inbound::Closed("the connection was closed".into()));
    }
}
