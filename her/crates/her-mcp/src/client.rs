//! The client side of one MCP connection: request correlation, timeouts that restart on
//! progress, cancellation, and answers to the requests a server may send.

use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::collections::HashSet;
use std::sync::Arc;
use std::sync::atomic::AtomicI64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio_util::sync::CancellationToken;

use crate::McpError;
use crate::transport::Inbound;
use crate::transport::Transport;
use crate::types::CallToolResult;
use crate::types::InitializeResult;
use crate::types::LATEST_PROTOCOL_VERSION;
use crate::types::SUPPORTED_PROTOCOL_VERSIONS;
use crate::types::ToolInfo;

/// Receives the text of a server's progress notification.
pub type ProgressFn = Arc<dyn Fn(&str) + Send + Sync>;

/// Upper bound on pages of a list, against servers that never stop paginating.
const MAX_PAGES: usize = 1000;

#[derive(Clone, Default)]
pub struct RequestOptions {
    /// Without progress; every progress notification restarts it.
    pub timeout: Duration,
    pub cancel: Option<CancellationToken>,
    pub progress: Option<ProgressFn>,
    /// Tell the server when the request is abandoned. Never for `initialize`.
    pub notify_cancel: bool,
}

/// What the connection reports to its owner.
#[derive(Debug)]
pub(crate) enum ClientEvent {
    ToolsChanged,
    Closed(String),
}

struct Pending {
    tx: oneshot::Sender<Result<Value, McpError>>,
    progress: Option<ProgressFn>,
    last_activity: Arc<Mutex<Instant>>,
}

/// Cheap to clone; all clones share the connection.
#[derive(Clone)]
pub struct McpClient {
    inner: Arc<Inner>,
}

struct Inner {
    server: String,
    transport: Arc<dyn Transport>,
    /// `None` once the connection is closed.
    pending: Mutex<Option<HashMap<i64, Pending>>>,
    next_id: AtomicI64,
    /// `file://` URIs answered to `roots/list`.
    roots: Vec<Value>,
}

impl McpClient {
    /// Takes over `inbound` and starts dispatching what the server sends.
    pub(crate) fn start(
        server: String,
        transport: Arc<dyn Transport>,
        mut inbound: mpsc::UnboundedReceiver<Inbound>,
        roots: Vec<Value>,
        events: mpsc::UnboundedSender<ClientEvent>,
    ) -> McpClient {
        let inner = Arc::new(Inner {
            server,
            transport,
            pending: Mutex::new(Some(HashMap::new())),
            next_id: AtomicI64::new(1),
            roots,
        });
        let weak = Arc::downgrade(&inner);
        tokio::spawn(async move {
            let mut reason = "the connection closed".to_string();
            while let Some(message) = inbound.recv().await {
                let Some(inner) = weak.upgrade() else { return };
                match message {
                    Inbound::Message(value) => inner.dispatch(value, &events),
                    Inbound::Closed(why) => {
                        reason = why;
                        break;
                    }
                }
            }
            if let Some(inner) = weak.upgrade() {
                inner.fail_all(&reason);
            }
            let _ = events.send(ClientEvent::Closed(reason));
        });
        McpClient { inner }
    }

    pub fn server(&self) -> &str {
        &self.inner.server
    }

    /// Sends a request and waits for its result.
    pub async fn request(&self, method: &str, mut params: Value, options: RequestOptions) -> Result<Value, McpError> {
        let inner = &self.inner;
        let id = inner.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, mut rx) = oneshot::channel();
        let last_activity = Arc::new(Mutex::new(Instant::now()));
        if options.progress.is_some() && params.is_object() {
            // The request id doubles as the progress token: unique on this connection even
            // when several sessions share it.
            let meta = params
                .as_object_mut()
                .and_then(|map| map.entry("_meta").or_insert_with(|| json!({})).as_object_mut());
            if let Some(meta) = meta {
                meta.insert("progressToken".into(), json!(id));
            }
        }
        match inner.pending.lock().as_mut() {
            Some(map) => {
                map.insert(
                    id,
                    Pending {
                        tx,
                        progress: options.progress.clone(),
                        last_activity: last_activity.clone(),
                    },
                );
            }
            None => return Err(McpError::Closed("the connection is closed".into())),
        }
        let message = json!({ "jsonrpc": "2.0", "id": id, "method": method, "params": params });
        if let Err(err) = inner.transport.send(message).await {
            inner.take_pending(id);
            return Err(err);
        }
        let cancel = options.cancel.clone().unwrap_or_default();
        loop {
            let deadline = *last_activity.lock() + options.timeout;
            tokio::select! {
                result = &mut rx => {
                    return result.unwrap_or_else(|_| Err(McpError::Closed("the connection closed".into())));
                }
                _ = cancel.cancelled() => {
                    inner.take_pending(id);
                    if options.notify_cancel {
                        self.cancelled(id, "interrupted by the user").await;
                    }
                    return Err(McpError::Cancelled);
                }
                _ = tokio::time::sleep_until(tokio::time::Instant::from_std(deadline)) => {
                    if *last_activity.lock() + options.timeout > Instant::now() {
                        continue; // progress arrived in the meantime
                    }
                    inner.take_pending(id);
                    if options.notify_cancel {
                        self.cancelled(id, "timed out").await;
                    }
                    return Err(McpError::Timeout(options.timeout.as_secs()));
                }
            }
        }
    }

    pub async fn notify(&self, method: &str, params: Value) {
        let message = json!({ "jsonrpc": "2.0", "method": method, "params": params });
        let _ = self.inner.transport.send(message).await;
    }

    async fn cancelled(&self, id: i64, reason: &str) {
        self.notify("notifications/cancelled", json!({ "requestId": id, "reason": reason }))
            .await;
    }

    /// The handshake. Never cancelled (the protocol forbids it); a timeout drops the connection.
    pub async fn initialize(&self, timeout: Duration) -> Result<InitializeResult, McpError> {
        let params = json!({
            "protocolVersion": LATEST_PROTOCOL_VERSION,
            "capabilities": { "roots": { "listChanged": false } },
            "clientInfo": { "name": "her", "title": "her", "version": env!("CARGO_PKG_VERSION") },
        });
        let options = RequestOptions {
            timeout,
            ..Default::default()
        };
        let value = self.request("initialize", params, options).await?;
        let result: InitializeResult =
            serde_json::from_value(value).map_err(|e| McpError::Protocol(format!("invalid initialize result: {e}")))?;
        if !SUPPORTED_PROTOCOL_VERSIONS.contains(&result.protocol_version.as_str()) {
            return Err(McpError::Protocol(format!(
                "the server speaks MCP {}, which her does not support (supported: {})",
                result.protocol_version,
                SUPPORTED_PROTOCOL_VERSIONS.join(", ")
            )));
        }
        self.inner.transport.set_protocol_version(&result.protocol_version);
        self.notify("notifications/initialized", json!({})).await;
        self.inner.transport.initialized();
        Ok(result)
    }

    /// Every page of `tools/list`. Malformed entries are skipped and described in the second
    /// value.
    pub async fn list_tools(&self, timeout: Duration) -> Result<(Vec<ToolInfo>, Vec<String>), McpError> {
        let mut tools = Vec::new();
        let mut problems = Vec::new();
        let mut cursor: Option<String> = None;
        let mut seen = HashSet::new();
        for _ in 0..MAX_PAGES {
            let params = match &cursor {
                Some(cursor) => json!({ "cursor": cursor }),
                None => json!({}),
            };
            let options = RequestOptions {
                timeout,
                notify_cancel: true,
                ..Default::default()
            };
            let page = self.request("tools/list", params, options).await?;
            match page.get("tools") {
                Some(Value::Array(items)) => {
                    for item in items {
                        match ToolInfo::parse(item) {
                            Ok(tool) => tools.push(tool),
                            Err(problem) => problems.push(problem),
                        }
                    }
                }
                None | Some(Value::Null) => {}
                Some(_) => return Err(McpError::Protocol("tools/list returned `tools` that is not a list".into())),
            }
            match page.get("nextCursor").and_then(Value::as_str).filter(|c| !c.is_empty()) {
                None => return Ok((tools, problems)),
                Some(next) => {
                    if !seen.insert(next.to_string()) {
                        return Err(McpError::Protocol("tools/list repeated a cursor".into()));
                    }
                    cursor = Some(next.to_string());
                }
            }
        }
        Err(McpError::Protocol(format!("tools/list returned more than {MAX_PAGES} pages")))
    }

    pub async fn call_tool(&self, name: &str, arguments: Value, options: RequestOptions) -> Result<CallToolResult, McpError> {
        let value = self
            .request("tools/call", json!({ "name": name, "arguments": arguments }), options)
            .await?;
        CallToolResult::parse(value).map_err(|e| McpError::Protocol(format!("invalid tools/call result: {e}")))
    }

    pub async fn close(&self) {
        self.inner.transport.close().await;
        self.inner.fail_all("the connection was closed");
    }
}

impl Inner {
    fn take_pending(&self, id: i64) -> Option<Pending> {
        self.pending.lock().as_mut().and_then(|map| map.remove(&id))
    }

    fn fail_all(&self, reason: &str) {
        let pending = self.pending.lock().take().unwrap_or_default();
        for (_, waiter) in pending {
            let _ = waiter.tx.send(Err(McpError::Closed(reason.to_string())));
        }
    }

    fn dispatch(self: &Arc<Self>, value: Value, events: &mpsc::UnboundedSender<ClientEvent>) {
        let method = value.get("method").and_then(Value::as_str).map(str::to_string);
        let id = value.get("id").filter(|id| !id.is_null()).cloned();
        match (method, id) {
            (Some(method), Some(id)) => self.answer(method, id),
            (Some(method), None) => self.notification(&method, value.get("params"), events),
            (None, Some(id)) => {
                let Some(id) = id.as_i64() else {
                    tracing::debug!(server = %self.server, "ignored a response with an id her never sent");
                    return;
                };
                let Some(waiter) = self.take_pending(id) else {
                    return; // abandoned (timed out or cancelled) or unknown
                };
                let result = match value.get("error").filter(|e| !e.is_null()) {
                    Some(error) => Err(McpError::Rpc {
                        code: error.get("code").and_then(Value::as_i64).unwrap_or(-32603),
                        message: error
                            .get("message")
                            .and_then(Value::as_str)
                            .unwrap_or("unknown error")
                            .to_string(),
                    }),
                    None => Ok(value.get("result").cloned().unwrap_or(Value::Null)),
                };
                let _ = waiter.tx.send(result);
            }
            (None, None) => {}
        }
    }

    /// Requests from the server: `ping` and `roots/list`; everything else is not offered.
    fn answer(self: &Arc<Self>, method: String, id: Value) {
        let response = match method.as_str() {
            "ping" => json!({ "jsonrpc": "2.0", "id": id, "result": {} }),
            "roots/list" => json!({ "jsonrpc": "2.0", "id": id, "result": { "roots": self.roots } }),
            _ => json!({
                "jsonrpc": "2.0", "id": id,
                "error": { "code": -32601, "message": format!("her does not support {method}") }
            }),
        };
        let inner = self.clone();
        tokio::spawn(async move {
            let _ = inner.transport.send(response).await;
        });
    }

    fn notification(&self, method: &str, params: Option<&Value>, events: &mpsc::UnboundedSender<ClientEvent>) {
        match method {
            "notifications/progress" => {
                let Some(params) = params else { return };
                let Some(token) = params.get("progressToken").and_then(Value::as_i64) else {
                    return;
                };
                let (progress, activity) = match self.pending.lock().as_ref().and_then(|map| map.get(&token)) {
                    Some(pending) => (pending.progress.clone(), pending.last_activity.clone()),
                    None => return,
                };
                *activity.lock() = Instant::now();
                if let Some(progress) = progress {
                    let text = match params.get("message").and_then(Value::as_str) {
                        Some(message) => message.to_string(),
                        None => {
                            let done = params.get("progress").map(number).unwrap_or_default();
                            match params.get("total") {
                                Some(total) => format!("{done}/{}", number(total)),
                                None => done,
                            }
                        }
                    };
                    progress(&format!("{text}\n"));
                }
            }
            "notifications/tools/list_changed" => {
                let _ = events.send(ClientEvent::ToolsChanged);
            }
            "notifications/message" => {
                let level = params.and_then(|p| p.get("level")).and_then(Value::as_str).unwrap_or("info");
                let data = params.and_then(|p| p.get("data")).map(|d| d.to_string()).unwrap_or_default();
                tracing::info!(target: "her_mcp::log", server = %self.server, level, "{data}");
            }
            _ => {}
        }
    }
}

fn number(value: &Value) -> String {
    match value.as_f64() {
        Some(n) if n.fract() == 0.0 => format!("{}", n as i64),
        Some(n) => format!("{n}"),
        None => value.to_string(),
    }
}
