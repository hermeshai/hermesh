//! A remote server over the 2024-11-05 HTTP+SSE transport: a GET event stream whose first
//! `endpoint` event names the address to POST messages to; every answer comes back on the
//! stream (mcp-design §13.7).

use async_trait::async_trait;
use eventsource_stream::Eventsource;
use futures::StreamExt;
use reqwest::StatusCode;
use reqwest::Url;
use reqwest::header::ACCEPT;
use reqwest::header::CONTENT_TYPE;
use reqwest::header::HeaderMap;
use serde_json::Value;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio_util::sync::CancellationToken;

use super::Inbound;
use super::Transport;
use super::http::header_map;
use crate::McpError;
use crate::oauth::BearerSource;

pub(crate) struct SseTransport {
    inner: Arc<SseInner>,
}

struct SseInner {
    client: reqwest::Client,
    headers: HeaderMap,
    auth: Option<Arc<dyn BearerSource>>,
    endpoint: Url,
    inbound: mpsc::UnboundedSender<Inbound>,
    closed: CancellationToken,
}

/// Opens the event stream and waits up to `wait` for the `endpoint` event.
pub(crate) async fn connect(
    server: String,
    client: reqwest::Client,
    url: String,
    headers: &[(String, String)],
    auth: Option<Arc<dyn BearerSource>>,
    inbound: mpsc::UnboundedSender<Inbound>,
    wait: Duration,
) -> Result<SseTransport, McpError> {
    let base = Url::parse(&url).map_err(|e| McpError::Transport(format!("invalid URL {url}: {e}")))?;
    if !matches!(base.scheme(), "http" | "https") {
        return Err(McpError::Transport(format!("the URL must start with http:// or https://: {url}")));
    }
    let headers = header_map(headers).map_err(McpError::Transport)?;
    let mut bearer = match &auth {
        Some(auth) => Some(auth.token().await?),
        None => None,
    };
    let mut refreshed = false;
    let response = loop {
        let mut request = client.get(base.clone()).headers(headers.clone()).header(ACCEPT, "text/event-stream");
        if let Some(token) = &bearer {
            request = request.bearer_auth(token);
        }
        let response = request.send().await.map_err(|e| McpError::Transport(format!("request failed: {e}")))?;
        if response.status() == StatusCode::UNAUTHORIZED
            && !refreshed
            && let (Some(auth), Some(stale)) = (&auth, &bearer)
        {
            bearer = Some(auth.refresh(stale).await?);
            refreshed = true;
            continue;
        }
        break response;
    };
    match response.status() {
        StatusCode::UNAUTHORIZED => return Err(McpError::AuthRequired),
        status if !status.is_success() => return Err(McpError::Transport(format!("HTTP {status} for the event stream"))),
        _ => {}
    }
    let closed = CancellationToken::new();
    let (endpoint_tx, endpoint_rx) = oneshot::channel();
    tokio::spawn(read_stream(server, response, base.clone(), inbound.clone(), closed.clone(), endpoint_tx));
    let endpoint = match tokio::time::timeout(wait, endpoint_rx).await {
        Ok(Ok(Ok(endpoint))) => endpoint,
        Ok(Ok(Err(why))) => {
            closed.cancel();
            return Err(McpError::Protocol(why));
        }
        Ok(Err(_)) => return Err(McpError::Transport("the event stream ended before naming its endpoint".into())),
        Err(_) => {
            closed.cancel();
            return Err(McpError::Protocol(format!("no endpoint event within {} s", wait.as_secs())));
        }
    };
    Ok(SseTransport {
        inner: Arc::new(SseInner {
            client,
            headers,
            auth,
            endpoint,
            inbound,
            closed,
        }),
    })
}

/// The endpoint, resolved against the stream's URL; another host is refused so that requests
/// and tokens cannot be sent elsewhere.
fn resolve_endpoint(base: &Url, data: &str) -> Result<Url, String> {
    let endpoint = base.join(data.trim()).map_err(|e| format!("the endpoint '{}' is not a URL: {e}", data.trim()))?;
    let same = endpoint.scheme() == base.scheme() && endpoint.host_str() == base.host_str() && endpoint.port_or_known_default() == base.port_or_known_default();
    if !same {
        return Err(format!("the endpoint {endpoint} is on another host than {base}"));
    }
    Ok(endpoint)
}

async fn read_stream(
    server: String,
    response: reqwest::Response,
    base: Url,
    inbound: mpsc::UnboundedSender<Inbound>,
    closed: CancellationToken,
    endpoint_tx: oneshot::Sender<Result<Url, String>>,
) {
    let mut events = response.bytes_stream().eventsource();
    let mut endpoint_tx = Some(endpoint_tx);
    let reason = loop {
        let event = tokio::select! {
            _ = closed.cancelled() => return,
            event = events.next() => event,
        };
        match event {
            Some(Ok(event)) if event.event == "endpoint" => {
                if let Some(tx) = endpoint_tx.take() {
                    let _ = tx.send(resolve_endpoint(&base, &event.data));
                }
            }
            Some(Ok(event)) => {
                if !(event.event.is_empty() || event.event == "message") || event.data.trim().is_empty() {
                    continue;
                }
                match serde_json::from_str::<Value>(&event.data) {
                    Ok(Value::Array(items)) => {
                        for item in items {
                            let _ = inbound.send(Inbound::Message(item));
                        }
                    }
                    Ok(value) => {
                        let _ = inbound.send(Inbound::Message(value));
                    }
                    Err(_) => tracing::debug!(%server, "ignored an SSE event that is not JSON"),
                }
            }
            Some(Err(err)) => break format!("the event stream failed: {err}"),
            None => break "the server closed the event stream".to_string(),
        }
    };
    let _ = inbound.send(Inbound::Closed(reason));
}

#[async_trait]
impl Transport for SseTransport {
    async fn send(&self, message: Value) -> Result<(), McpError> {
        let inner = &self.inner;
        let mut bearer = match &inner.auth {
            Some(auth) => Some(auth.token().await?),
            None => None,
        };
        let mut refreshed = false;
        loop {
            let mut request = inner
                .client
                .post(inner.endpoint.clone())
                .headers(inner.headers.clone())
                .header(CONTENT_TYPE, "application/json")
                .json(&message);
            if let Some(token) = &bearer {
                request = request.bearer_auth(token);
            }
            let response = tokio::select! {
                _ = inner.closed.cancelled() => return Err(McpError::Closed("the connection was closed".into())),
                response = request.send() => response.map_err(|e| McpError::Transport(format!("request failed: {e}")))?,
            };
            let status = response.status();
            if status == StatusCode::UNAUTHORIZED {
                if !refreshed && let (Some(auth), Some(stale)) = (&inner.auth, &bearer) {
                    bearer = Some(auth.refresh(stale).await?);
                    refreshed = true;
                    continue;
                }
                return Err(McpError::AuthRequired);
            }
            if !status.is_success() {
                let body = response.text().await.unwrap_or_default();
                let excerpt: String = body.chars().take(300).collect();
                return Err(McpError::Transport(format!("HTTP {status}: {}", excerpt.trim())));
            }
            // The answer, if any, arrives on the event stream.
            return Ok(());
        }
    }

    async fn close(&self) {
        self.inner.closed.cancel();
        let _ = self.inner.inbound.send(Inbound::Closed("the connection was closed".into()));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn endpoints_stay_on_the_streams_host() {
        let base = Url::parse("https://mcp.example.com/v1/sse").unwrap();
        assert_eq!(resolve_endpoint(&base, "/messages?session=1").unwrap().as_str(), "https://mcp.example.com/messages?session=1");
        assert_eq!(resolve_endpoint(&base, "messages").unwrap().as_str(), "https://mcp.example.com/v1/messages");
        assert!(resolve_endpoint(&base, "https://evil.example.net/messages").is_err());
        assert!(resolve_endpoint(&base, "http://mcp.example.com/messages").is_err(), "another scheme");
    }
}
