//! A scriptable MCP server for tests, over stdio (`her-mcp-server`) or Streamable HTTP
//! (in-process with [`serve_http`], or `her-mcp-server --http ADDR`). Over HTTP it can also
//! require a token or OAuth ([`McpTestAuth`], with the authorization server in
//! [`crate::mcp_auth`]), and speak the older HTTP+SSE transport at `/sse` instead.
//!
//! | Tool | Behaviour |
//! |---|---|
//! | `echo {text}` | `echo: <text>`; annotated read-only |
//! | `add {a, b}` | the sum as text and as `structuredContent` |
//! | `image` | a 1×1 PNG |
//! | `fail {message}` | an error result (`isError`) |
//! | `structured` | only `structuredContent` |
//! | `big {kb}` | about `kb` kilobytes of numbered lines |
//! | `slow {seconds}` | progress every 200 ms; stops early when cancelled (logged) |
//! | `env {name}` | the value of an environment variable, or `(unset)` |
//! | `roots` | asks the client for `roots/list` and returns the URIs |
//! | `add_tool` | adds `extra_echo` and sends `tools/list_changed` |
//! | `vault_reveal_code {project}` | a code derived from the project name |
//! | `crash` | exits with status 3 (separate process only) |
//! | `spawn_sleeper` | starts `sleep 300` in the server's process group and returns its pid |
//! | `bad_schema`, `ui_panel` | listed, but her must not offer them |
//!
//! `generated` adds that many plausible tools (`<domain>_<action>`) for tool-search tests;
//! `domain` keeps only one domain's.

use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::io;
use std::io::Write as _;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicI64;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::io::AsyncBufReadExt;
use tokio::io::AsyncRead;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWrite;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio::net::TcpStream;
use tokio::sync::Notify;
use tokio::sync::mpsc;
use tokio::sync::oneshot;

use crate::mcp_auth::McpTestAuth;
use crate::mcp_auth::OAuthState;

/// The logo the test server declares: a 64-pixel tile, teal with a white bar, as an app icon is.
pub const ICON: &str = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAWElEQVR42u3WQQ0AIAwEwWqoYBTguZggTSFzWQPzu8i9ni4AAAAAAAAAAAAAAAAAAAAAAAA+B1TjAAAAAAAAAACmAbxRAAAAAAAAAAAAAAAAAAAAAACAax2wg+zdIlH2xgAAAABJRU5ErkJggg==";

/// A 1×1 transparent PNG.
pub const PNG_1X1: &str =
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

#[derive(Clone, Debug)]
pub struct McpTestOptions {
    pub name: String,
    pub instructions: Option<String>,
    /// Extra generated tools, for tool search.
    pub generated: usize,
    /// Only generate the tools of this domain (`jira`, `crm`...).
    pub domain: Option<String>,
    /// Tools per `tools/list` page; 0 sends them all at once.
    pub page_size: usize,
    /// Appends one line per notable event (calls, cancellations).
    pub log: Option<PathBuf>,
    /// `crash` and `spawn_sleeper` work only when the server is its own process.
    pub own_process: bool,
    /// Over HTTP: who may call.
    pub auth: McpTestAuth,
    /// Over HTTP: the 2024-11-05 HTTP+SSE transport at `/sse` (`POST /sse` answers 405).
    pub legacy_sse: bool,
    /// Over HTTP: the host clients reach it by, in its URL and the addresses its sign-in gives,
    /// as for a client in a container; the address it listens on when `None`.
    pub public_host: Option<String>,
    /// Offer only the evidence-driven scenario tools.
    pub simulation: bool,
    pub simulation_delay_ms: u64,
}

impl Default for McpTestOptions {
    fn default() -> Self {
        Self {
            name: "test".into(),
            instructions: Some("Test server for her. Use echo to repeat text.".into()),
            generated: 0,
            domain: None,
            page_size: 0,
            log: None,
            own_process: false,
            auth: McpTestAuth::None,
            legacy_sse: false,
            public_host: None,
            simulation: false,
            simulation_delay_ms: 0,
        }
    }
}

/// The value `vault_reveal_code` returns for `project`.
pub fn vault_code(project: &str) -> String {
    let mut hash: u32 = 2166136261;
    for byte in project.trim().to_lowercase().bytes() {
        hash ^= u32::from(byte);
        hash = hash.wrapping_mul(16777619);
    }
    format!("FX-{:06X}", hash & 0xFF_FFFF)
}

type Out = mpsc::UnboundedSender<Value>;

struct State {
    options: McpTestOptions,
    simulation: crate::simulation::ToolService,
    extra: Mutex<Vec<Value>>,
    /// In-flight calls by request id, woken by `notifications/cancelled`.
    running: Mutex<HashMap<i64, Arc<Notify>>>,
    /// Requests this server sent to the client, by id.
    asked: Mutex<HashMap<i64, oneshot::Sender<Value>>>,
    next_id: AtomicI64,
    calls: AtomicU64,
    /// `http://host:port`, once bound.
    base: Mutex<String>,
    oauth: OAuthState,
    /// Open `GET /sse` streams by session.
    sse: Mutex<HashMap<u64, Out>>,
}

impl State {
    fn new(options: McpTestOptions) -> Arc<State> {
        Arc::new(State {
            options,
            simulation: crate::simulation::ToolService::default(),
            extra: Mutex::new(Vec::new()),
            running: Mutex::new(HashMap::new()),
            asked: Mutex::new(HashMap::new()),
            next_id: AtomicI64::new(1),
            calls: AtomicU64::new(0),
            base: Mutex::new(String::new()),
            oauth: OAuthState::default(),
            sse: Mutex::new(HashMap::new()),
        })
    }

    fn log(&self, line: &str) {
        if let Some(path) = &self.options.log
            && let Ok(mut file) = std::fs::OpenOptions::new().create(true).append(true).open(path)
        {
            let _ = writeln!(file, "{line}");
        }
    }

    fn tools(&self) -> Vec<Value> {
        if self.options.simulation { return crate::simulation::tool_definitions(); }
        let text = |description: &str| json!({ "type": "string", "description": description });
        let mut tools = vec![
            json!({"name": "echo", "title": "Echo", "description": "Repeat the given text back.",
                "inputSchema": {"type": "object", "properties": {"text": text("Text to repeat")}, "required": ["text"]},
                "annotations": {"readOnlyHint": true}}),
            json!({"name": "add", "description": "Add two numbers.",
                "inputSchema": {"type": "object", "properties": {"a": {"type": "number"}, "b": {"type": "number"}}, "required": ["a", "b"]}}),
            json!({"name": "image", "description": "Return a tiny PNG image.", "inputSchema": {"type": "object"}}),
            json!({"name": "fail", "description": "Return an error result with the given message.",
                "inputSchema": {"type": "object", "properties": {"message": text("Error message")}}}),
            json!({"name": "structured", "description": "Return only structured content.", "inputSchema": {"type": "object", "properties": {}}}),
            json!({"name": "big", "description": "Return about kb kilobytes of numbered lines.",
                "inputSchema": {"type": "object", "properties": {"kb": {"type": "integer"}}}}),
            json!({"name": "slow", "description": "Wait for the given seconds, reporting progress.",
                "inputSchema": {"type": "object", "properties": {"seconds": {"type": "number"}}}}),
            json!({"name": "env", "description": "Read an environment variable of the server process.",
                "inputSchema": {"type": "object", "properties": {"name": text("Variable name")}, "required": ["name"]}}),
            json!({"name": "roots", "description": "List the client's workspace roots.", "inputSchema": {"type": "object"}}),
            json!({"name": "add_tool", "description": "Add a tool named extra_echo to this server.", "inputSchema": {"type": "object"}}),
            json!({"name": "vault_reveal_code", "description": "Reveal the secret release code stored in the vault for a project.",
                "inputSchema": {"type": "object", "properties": {"project": text("Project name")}, "required": ["project"]}}),
            json!({"name": "crash", "description": "Exit the server process.", "inputSchema": {"type": "object"}}),
            json!({"name": "spawn_sleeper", "description": "Start a sleeping child process and return its pid.", "inputSchema": {"type": "object"}}),
            json!({"name": "bad_schema", "description": "A tool whose schema is not an object schema.", "inputSchema": {"type": "array"}}),
            json!({"name": "ui_panel", "description": "Only for user interfaces.", "inputSchema": {"type": "object"},
                "_meta": {"ui": {"visibility": ["app"]}}}),
        ];
        tools.extend(generated_tools(self.options.generated, self.options.domain.as_deref()));
        tools.extend(self.extra.lock().iter().cloned());
        tools
    }

    async fn handle(self: Arc<Self>, message: Value, out: Out) {
        let method = message.get("method").and_then(Value::as_str).unwrap_or_default().to_string();
        let id = message.get("id").cloned();
        let params = message.get("params").cloned().unwrap_or(Value::Null);
        let Some(id) = id else {
            if method == "notifications/cancelled"
                && let Some(request) = params.get("requestId").and_then(Value::as_i64)
            {
                self.log(&format!("cancelled {request}"));
                if let Some(notify) = self.running.lock().get(&request) {
                    notify.notify_one();
                }
            }
            return;
        };
        if method.is_empty() {
            // A response to something this server asked.
            if let Some(key) = id.as_i64()
                && let Some(tx) = self.asked.lock().remove(&key)
            {
                let _ = tx.send(message.get("result").cloned().unwrap_or(Value::Null));
            }
            return;
        }
        let result = match method.as_str() {
            "initialize" => Ok(json!({
                "protocolVersion": params.get("protocolVersion").and_then(Value::as_str).unwrap_or("2025-06-18"),
                "capabilities": { "tools": { "listChanged": true } },
                // A declared logo (MCP 2025-11-25), so clients can show it.
                "serverInfo": { "name": self.options.name, "version": "1.0.0", "icons": [{ "src": ICON, "mimeType": "image/png", "sizes": ["64x64"] }] },
                "instructions": self.options.instructions,
            })),
            "ping" => Ok(json!({})),
            "tools/list" => Ok(self.list(&params)),
            "tools/call" => self.clone().call(&id, &params, &out).await,
            _ => Err((-32601, format!("unknown method {method}"))),
        };
        let response = match result {
            Ok(result) => json!({ "jsonrpc": "2.0", "id": id, "result": result }),
            Err((code, message)) => json!({ "jsonrpc": "2.0", "id": id, "error": { "code": code, "message": message } }),
        };
        let _ = out.send(response);
    }

    fn list(&self, params: &Value) -> Value {
        let tools = self.tools();
        if self.options.page_size == 0 {
            return json!({ "tools": tools });
        }
        let start: usize = params
            .get("cursor")
            .and_then(Value::as_str)
            .and_then(|c| c.strip_prefix("page-"))
            .and_then(|n| n.parse().ok())
            .unwrap_or(0);
        let end = (start + self.options.page_size).min(tools.len());
        let mut page = json!({ "tools": tools[start.min(end)..end] });
        if end < tools.len() {
            page["nextCursor"] = json!(format!("page-{end}"));
        }
        page
    }

    async fn call(self: Arc<Self>, id: &Value, params: &Value, out: &Out) -> Result<Value, (i64, String)> {
        let name = params.get("name").and_then(Value::as_str).unwrap_or_default().to_string();
        let args = params.get("arguments").cloned().unwrap_or_else(|| json!({}));
        let arg = |key: &str| args.get(key).and_then(Value::as_str).unwrap_or_default().to_string();
        self.calls.fetch_add(1, Ordering::Relaxed);
        self.log(&format!("call {name} {args}"));
        if self.options.simulation {
            if self.options.simulation_delay_ms > 0 {
                tokio::time::sleep(Duration::from_millis(self.options.simulation_delay_ms)).await;
            }
            return Ok(self.simulation.call(&name, &args));
        }
        let text = |text: String| json!({ "content": [{ "type": "text", "text": text }] });
        let known = self.tools().iter().any(|t| t.get("name").and_then(Value::as_str) == Some(name.as_str()));
        if !known {
            return Err((-32602, format!("unknown tool {name}")));
        }
        Ok(match name.as_str() {
            "echo" | "extra_echo" => text(format!("echo: {}", arg("text"))),
            "add" => {
                let n = |k: &str| args.get(k).and_then(Value::as_f64).unwrap_or(0.0);
                let sum = n("a") + n("b");
                json!({ "content": [{ "type": "text", "text": format!("{sum}") }], "structuredContent": { "sum": sum } })
            }
            "image" => json!({ "content": [{ "type": "image", "data": PNG_1X1, "mimeType": "image/png" }] }),
            "fail" => json!({ "content": [{ "type": "text", "text": arg("message") }], "isError": true }),
            "structured" => json!({ "structuredContent": { "ok": true, "items": [1, 2, 3] } }),
            "big" => {
                let kb = args.get("kb").and_then(Value::as_u64).unwrap_or(100) as usize;
                let mut body = String::with_capacity(kb * 1024);
                let mut n = 0;
                while body.len() < kb * 1024 {
                    n += 1;
                    body.push_str(&format!("line {n:06} of the big result\n"));
                }
                text(body)
            }
            "slow" => {
                let seconds = args.get("seconds").and_then(Value::as_f64).unwrap_or(1.0);
                let token = params.pointer("/_meta/progressToken").cloned();
                let request = id.as_i64().unwrap_or_default();
                let notify = Arc::new(Notify::new());
                self.running.lock().insert(request, notify.clone());
                let steps = (seconds * 5.0).ceil().max(1.0) as u64;
                let mut cancelled = false;
                for step in 1..=steps {
                    tokio::select! {
                        _ = notify.notified() => { cancelled = true; break; }
                        _ = tokio::time::sleep(Duration::from_millis(200)) => {}
                    }
                    if let Some(token) = &token {
                        let _ = out.send(json!({ "jsonrpc": "2.0", "method": "notifications/progress",
                            "params": { "progressToken": token, "progress": step, "total": steps, "message": format!("step {step} of {steps}") } }));
                    }
                }
                self.running.lock().remove(&request);
                if cancelled {
                    return Err((-32800, "cancelled".into()));
                }
                text(format!("slept {seconds} s"))
            }
            "env" => text(std::env::var(arg("name")).unwrap_or_else(|_| "(unset)".into())),
            "roots" => {
                let ask = self.next_id.fetch_add(1, Ordering::Relaxed) + 10_000;
                let (tx, rx) = oneshot::channel();
                self.asked.lock().insert(ask, tx);
                let _ = out.send(json!({ "jsonrpc": "2.0", "id": ask, "method": "roots/list", "params": {} }));
                match tokio::time::timeout(Duration::from_secs(10), rx).await {
                    Ok(Ok(result)) => {
                        let uris: Vec<String> = result
                            .get("roots")
                            .and_then(Value::as_array)
                            .map(|roots| roots.iter().filter_map(|r| r.get("uri").and_then(Value::as_str)).map(str::to_string).collect())
                            .unwrap_or_default();
                        text(uris.join("\n"))
                    }
                    _ => return Err((-32000, "the client did not answer roots/list".into())),
                }
            }
            "add_tool" => {
                self.extra.lock().push(json!({"name": "extra_echo", "description": "Repeat text (added at runtime).",
                    "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}}}));
                let _ = out.send(json!({ "jsonrpc": "2.0", "method": "notifications/tools/list_changed" }));
                text("added extra_echo".into())
            }
            "vault_reveal_code" => text(format!("Release code for {}: {}", arg("project"), vault_code(&arg("project")))),
            "crash" if self.options.own_process => {
                self.log("crash");
                std::process::exit(3);
            }
            "spawn_sleeper" if self.options.own_process => match std::process::Command::new("sleep").arg("300").spawn() {
                Ok(child) => text(format!("{}", child.id())),
                Err(err) => return Err((-32000, format!("cannot spawn: {err}"))),
            },
            other if other.contains('_') && self.options.generated > 0 => {
                text(format!("{other} done with {args}"))
            }
            other => return Err((-32602, format!("{other} is not available here"))),
        })
    }
}

/// `<domain>_<action>` tools with descriptions and parameters a model can search.
fn generated_tools(count: usize, only: Option<&str>) -> Vec<Value> {
    const DOMAINS: [(&str, &str); 30] = [
        ("weather", "weather forecasts and current conditions"),
        ("calendar", "calendar events and meetings"),
        ("email", "email messages in the mailbox"),
        ("contacts", "people in the address book"),
        ("files", "documents in cloud storage"),
        ("github", "GitHub repositories, issues and pull requests"),
        ("jira", "Jira tickets and sprints"),
        ("slack", "Slack channels and messages"),
        ("crm", "customer accounts and sales leads"),
        ("billing", "invoices and subscriptions"),
        ("inventory", "warehouse stock levels"),
        ("analytics", "website traffic reports"),
        ("maps", "places, routes and directions"),
        ("translate", "text translation between languages"),
        ("stocks", "stock quotes and portfolios"),
        ("news", "news articles and headlines"),
        ("music", "songs and playlists"),
        ("video", "video uploads and channels"),
        ("photos", "photo albums"),
        ("notes", "personal notes"),
        ("tasks", "to-do lists and reminders"),
        ("database", "SQL tables and queries"),
        ("dns", "DNS zones and records"),
        ("kubernetes", "Kubernetes pods and deployments"),
        ("monitoring", "alerts and dashboards"),
        ("payments", "card payments and refunds"),
        ("shipping", "parcel tracking and labels"),
        ("hr", "employee records and time off"),
        ("travel", "flight and hotel bookings"),
        ("recipes", "cooking recipes and ingredients"),
    ];
    const ACTIONS: [(&str, &str); 10] = [
        ("list", "List"),
        ("get", "Get one of the"),
        ("create", "Create new"),
        ("update", "Update existing"),
        ("delete", "Delete"),
        ("search", "Search"),
        ("export", "Export"),
        ("archive", "Archive"),
        ("share", "Share"),
        ("summarize", "Summarize"),
    ];
    let mut out = Vec::with_capacity(count);
    'outer: for (action, verb) in ACTIONS {
        for (domain, what) in DOMAINS.into_iter().filter(|(d, _)| only.is_none_or(|o| o == *d)) {
            if out.len() == count {
                break 'outer;
            }
            out.push(json!({
                "name": format!("{domain}_{action}"),
                "description": format!("{verb} {what}."),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "id": {"type": "string", "description": format!("Identifier in the {domain} service")},
                        "query": {"type": "string", "description": "Free-text filter"},
                        "limit": {"type": "integer", "description": "Maximum number of results"}
                    }
                }
            }));
        }
    }
    out
}

/// Serves one client over a pair of streams (stdin and stdout of a process, or a duplex in a
/// test) until the input ends.
pub async fn serve_stdio<R, W>(reader: R, writer: W, options: McpTestOptions)
where
    R: AsyncRead + Unpin + Send + 'static,
    W: AsyncWrite + Unpin + Send + 'static,
{
    let state = State::new(options);
    let (out_tx, mut out_rx) = mpsc::unbounded_channel::<Value>();
    let writer_task = tokio::spawn(async move {
        let mut writer = writer;
        while let Some(message) = out_rx.recv().await {
            let mut line = message.to_string();
            line.push('\n');
            if writer.write_all(line.as_bytes()).await.is_err() || writer.flush().await.is_err() {
                break;
            }
        }
    });
    let mut lines = tokio::io::BufReader::new(reader).lines();
    while let Ok(Some(line)) = lines.next_line().await {
        let Ok(message) = serde_json::from_str::<Value>(&line) else {
            continue;
        };
        tokio::spawn(state.clone().handle(message, out_tx.clone()));
    }
    drop(out_tx);
    let _ = writer_task.await;
}

/// An MCP server on HTTP, serving in the background.
pub struct McpHttpServer {
    accept: tokio::task::JoinHandle<()>,
    state: Arc<State>,
}

impl Drop for McpHttpServer {
    fn drop(&mut self) { self.accept.abort(); }
}

impl McpHttpServer {
    /// The MCP endpoint: `/mcp`, or `/sse` for the older transport.
    pub fn url(&self) -> String {
        let path = if self.state.options.legacy_sse { "sse" } else { "mcp" };
        format!("{}/{path}", self.state.base.lock())
    }

    pub fn simulation(&self) -> &crate::simulation::ToolService { &self.state.simulation }

    pub fn calls(&self) -> u64 {
        self.state.calls.load(Ordering::Relaxed)
    }

    /// Dynamic client registrations, authorization-code grants, refreshes and revocations.
    pub fn oauth_counts(&self) -> (u64, u64, u64, u64) {
        let o = &self.state.oauth;
        (
            o.registrations.load(Ordering::Relaxed),
            o.grants.load(Ordering::Relaxed),
            o.refreshes.load(Ordering::Relaxed),
            o.revocations.load(Ordering::Relaxed),
        )
    }

    /// The `client_id` of the last authorization request.
    pub fn last_client(&self) -> Option<String> {
        self.state.oauth.last_client.lock().clone()
    }
}

/// Binds `addr` (for example `127.0.0.1:0`) and serves Streamable HTTP at `/mcp`.
pub async fn serve_http(addr: &str, options: McpTestOptions) -> io::Result<McpHttpServer> {
    let listener = TcpListener::bind(addr).await?;
    let addr = listener.local_addr()?;
    let state = State::new(options);
    *state.base.lock() = match &state.options.public_host {
        Some(host) => format!("http://{host}:{}", addr.port()),
        None => format!("http://{addr}"),
    };
    let sessions = Arc::new(AtomicU64::new(0));
    let accept_state = state.clone();
    let accept = tokio::spawn(async move {
        loop {
            let Ok((stream, _)) = listener.accept().await else { continue };
            let _ = stream.set_nodelay(true);
            tokio::spawn(serve_http_connection(stream, accept_state.clone(), sessions.clone()));
        }
    });
    Ok(McpHttpServer { state, accept })
}

pub(crate) struct HttpRequest {
    pub(crate) method: String,
    pub(crate) path: String,
    pub(crate) headers: HashMap<String, String>,
    pub(crate) body: Vec<u8>,
}

async fn read_http(stream: &mut TcpStream, buf: &mut Vec<u8>) -> io::Result<Option<HttpRequest>> {
    let header_end = loop {
        if let Some(pos) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
            break pos;
        }
        let mut chunk = [0u8; 8192];
        let n = stream.read(&mut chunk).await?;
        if n == 0 {
            return Ok(None);
        }
        buf.extend_from_slice(&chunk[..n]);
    };
    let head = String::from_utf8_lossy(&buf[..header_end]).into_owned();
    let mut lines = head.split("\r\n");
    let mut first = lines.next().unwrap_or("").split(' ');
    let method = first.next().unwrap_or("GET").to_string();
    let path = first.next().unwrap_or("/").to_string();
    let headers: HashMap<String, String> = lines
        .filter_map(|line| line.split_once(':'))
        .map(|(name, value)| (name.trim().to_ascii_lowercase(), value.trim().to_string()))
        .collect();
    let length: usize = headers.get("content-length").and_then(|v| v.parse().ok()).unwrap_or(0);
    let body_start = header_end + 4;
    while buf.len() < body_start + length {
        let mut chunk = vec![0u8; (body_start + length - buf.len()).min(64 * 1024)];
        let n = stream.read(&mut chunk).await?;
        if n == 0 {
            return Ok(None);
        }
        buf.extend_from_slice(&chunk[..n]);
    }
    let body = buf[body_start..body_start + length].to_vec();
    buf.drain(..body_start + length);
    Ok(Some(HttpRequest { method, path, headers, body }))
}

pub(crate) async fn write_response(stream: &mut TcpStream, status: &str, extra: &str, body: &[u8]) -> io::Result<()> {
    let head = format!("HTTP/1.1 {status}\r\n{extra}Content-Length: {}\r\n\r\n", body.len());
    stream.write_all(head.as_bytes()).await?;
    stream.write_all(body).await?;
    stream.flush().await
}

async fn serve_http_connection(mut stream: TcpStream, state: Arc<State>, sessions: Arc<AtomicU64>) {
    let mut buf = Vec::new();
    while let Ok(Some(request)) = read_http(&mut stream, &mut buf).await {
        let (path, query) = request.path.split_once('?').map(|(p, q)| (p.to_string(), q.to_string())).unwrap_or((request.path.clone(), String::new()));
        let base = state.base.lock().clone();
        let endpoint = if state.options.legacy_sse { "/sse" } else { "/mcp" };
        let site = crate::mcp_auth::Site { auth: &state.options.auth, oauth: &state.oauth, base: &base, resource_path: endpoint };
        if let Some(result) = crate::mcp_auth::route(&mut stream, &request, &path, &query, &site).await {
            if result.is_err() {
                return;
            }
            continue;
        }
        if state.options.legacy_sse {
            // `/stream` is the same stream at an address that does not end in `/sse`.
            let stream_path = matches!(path.as_str(), "/sse" | "/stream");
            if !(stream_path || path == "/messages") || (request.method == "POST" && stream_path) {
                let status = if stream_path { "405 Method Not Allowed" } else { "404 Not Found" };
                if write_response(&mut stream, status, "", b"").await.is_err() {
                    return;
                }
                continue;
            }
            match crate::mcp_auth::admit(&mut stream, &request, &state.options.auth, &state.oauth, &base, if path == "/stream" { "/stream" } else { "/sse" }).await {
                Ok(true) => {}
                Ok(false) => continue,
                Err(_) => return,
            }
            if request.method == "GET" {
                serve_sse_stream(stream, state).await;
                return;
            }
            let session: u64 = crate::mcp_auth::form(&query).get("session").and_then(|s| s.parse().ok()).unwrap_or(0);
            let Some(out) = state.sse.lock().get(&session).cloned() else {
                let _ = write_response(&mut stream, "404 Not Found", "", b"unknown session").await;
                return;
            };
            let Ok(message) = serde_json::from_slice::<Value>(&request.body) else {
                let _ = write_response(&mut stream, "400 Bad Request", "", b"not JSON").await;
                return;
            };
            tokio::spawn(state.clone().handle(message, out));
            if write_response(&mut stream, "202 Accepted", "", b"").await.is_err() {
                return;
            }
            continue;
        }
        if request.path.split('?').next() == Some("/mcp") {
            match crate::mcp_auth::admit(&mut stream, &request, &state.options.auth, &state.oauth, &base, "/mcp").await {
                Ok(true) => {}
                Ok(false) => continue,
                Err(_) => return,
            }
        }
        if path != "/mcp" {
            if write_response(&mut stream, "404 Not Found", "", b"").await.is_err() {
                return;
            }
            continue;
        }
        match request.method.as_str() {
            // No stream for server-started messages: notifications come with responses.
            "GET" => {
                if write_response(&mut stream, "405 Method Not Allowed", "", b"").await.is_err() {
                    return;
                }
            }
            "DELETE" => {
                state.log("session deleted");
                if write_response(&mut stream, "200 OK", "", b"").await.is_err() {
                    return;
                }
            }
            "POST" => {
                let Ok(message) = serde_json::from_slice::<Value>(&request.body) else {
                    let _ = write_response(&mut stream, "400 Bad Request", "", b"not JSON").await;
                    return;
                };
                let is_request = message.get("method").is_some() && message.get("id").is_some();
                if !is_request {
                    // Notifications and responses: handled, answered with 202.
                    let (tx, _rx) = mpsc::unbounded_channel();
                    state.clone().handle(message, tx).await;
                    if write_response(&mut stream, "202 Accepted", "", b"").await.is_err() {
                        return;
                    }
                    continue;
                }
                let mut session_header = String::new();
                if message.get("method").and_then(Value::as_str) == Some("initialize") {
                    let n = sessions.fetch_add(1, Ordering::Relaxed) + 1;
                    session_header = format!("Mcp-Session-Id: session-{n}\r\n");
                } else if !request.headers.contains_key("mcp-session-id") {
                    let _ = write_response(&mut stream, "400 Bad Request", "", b"missing Mcp-Session-Id").await;
                    return;
                }
                let tool = message.pointer("/params/name").and_then(Value::as_str).unwrap_or_default();
                let streaming = matches!(tool, "slow" | "add_tool" | "roots");
                let id = message.get("id").cloned();
                let (tx, mut rx) = mpsc::unbounded_channel();
                tokio::spawn(state.clone().handle(message, tx));
                if streaming {
                    let head = format!("HTTP/1.1 200 OK\r\n{session_header}Content-Type: text/event-stream\r\nConnection: close\r\n\r\n");
                    if stream.write_all(head.as_bytes()).await.is_err() {
                        return;
                    }
                    while let Some(out) = rx.recv().await {
                        let done = out.get("id") == id.as_ref() && out.get("method").is_none();
                        let event = format!("event: message\ndata: {out}\n\n");
                        if stream.write_all(event.as_bytes()).await.is_err() || stream.flush().await.is_err() {
                            return;
                        }
                        if done {
                            break;
                        }
                    }
                    return;
                }
                let response = loop {
                    match rx.recv().await {
                        Some(out) if out.get("id") == id.as_ref() && out.get("method").is_none() => break out,
                        Some(_) => continue,
                        None => return,
                    }
                };
                let body = response.to_string();
                let extra = format!("{session_header}Content-Type: application/json\r\n");
                if write_response(&mut stream, "200 OK", &extra, body.as_bytes()).await.is_err() {
                    return;
                }
            }
            _ => {
                if write_response(&mut stream, "405 Method Not Allowed", "", b"").await.is_err() {
                    return;
                }
            }
        }
    }
}

/// `GET /sse`: names the address for messages, then carries every message the server sends.
async fn serve_sse_stream(mut stream: TcpStream, state: Arc<State>) {
    static SESSIONS: AtomicU64 = AtomicU64::new(0);
    let session = SESSIONS.fetch_add(1, Ordering::Relaxed) + 1;
    let (tx, mut rx) = mpsc::unbounded_channel();
    state.sse.lock().insert(session, tx);
    let head = format!("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\n\r\nevent: endpoint\ndata: /messages?session={session}\n\n");
    if stream.write_all(head.as_bytes()).await.is_ok() && stream.flush().await.is_ok() {
        while let Some(out) = rx.recv().await {
            let event = format!("event: message\ndata: {out}\n\n");
            if stream.write_all(event.as_bytes()).await.is_err() || stream.flush().await.is_err() {
                break;
            }
        }
    }
    state.sse.lock().remove(&session);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generated_tools_are_unique() {
        let tools = generated_tools(300, None);
        assert_eq!(tools.len(), 300);
        let names: std::collections::HashSet<&str> = tools.iter().filter_map(|t| t["name"].as_str()).collect();
        assert_eq!(names.len(), 300);
        assert_eq!(vault_code("Apollo"), vault_code(" apollo "));
    }

    #[tokio::test]
    async fn a_public_host_names_the_server_in_its_url_and_its_sign_in() {
        let options = McpTestOptions { public_host: Some("host.docker.internal".into()), auth: McpTestAuth::OAuth(Default::default()), ..Default::default() };
        let server = serve_http("127.0.0.1:0", options).await.unwrap();
        let url = server.url();
        let port = url.strip_prefix("http://host.docker.internal:").and_then(|rest| rest.strip_suffix("/mcp")).unwrap_or_else(|| panic!("{url}"));
        let (status, body) = crate::mcp_auth::browse(&format!("http://127.0.0.1:{port}/.well-known/oauth-protected-resource/mcp")).await.unwrap();
        assert!(status.contains(" 200 "), "{status}");
        let metadata: Value = serde_json::from_str(&body).unwrap();
        assert_eq!(metadata["resource"], url.as_str());
        assert_eq!(metadata["authorization_servers"], json!([format!("http://host.docker.internal:{port}")]));
    }
}
