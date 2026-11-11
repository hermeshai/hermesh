//! A scriptable model service that speaks the OpenAI chat-completions streaming API, for
//! end-to-end and load tests. It is a minimal HTTP/1.1 server with keep-alive.
//!
//! The reply is chosen by a directive in the most recent user message that contains one:
//! `[[mock:NAME]]` or `[[mock:NAME:ARG]]`. The step within a scenario is the number of
//! assistant messages after that user message, so every task replays its scenario from the
//! start and a whole conversation needs no server-side state.
//!
//! | Scenario | Steps |
//! |---|---|
//! | `text` (no directive) | a short reply in three chunks; in a bot's conversation (send_to_user offered), send_to_user "回复：…" |
//! | `echo` | `echo: <the prompt without the directive>` in small chunks |
//! | `think` | reasoning, then a reply |
//! | `long:N` | N small text chunks |
//! | `big:KB` | about KB kilobytes of text in 1 KB chunks |
//! | `stamp:N[:MS]` | N chunks, each carrying its send time in microseconds (`@<us>;`), MS apart |
//! | `bash:CMD` | a bash call running CMD, then `bash done` |
//! | `files` | write `notes.txt`, read it, edit it, then `files done` |
//! | `parallel` | two writes in one message, then `parallel done` |
//! | `loop:N` | N rounds of a bash call, then `loop done` |
//! | `slow:N` | N chunks 100 ms apart (for cancellation) |
//! | `retry` | HTTP 500 for the first request of the prompt, then a reply |
//! | `fail` | HTTP 400 (a request error that is not retried) |
//! | `cut` | a reply that stops with finish_reason `length` |
//! | `skill:NAME` | follows a skill: read the SKILL.md that the system prompt lists for NAME, then each `references/…` file (read) and `scripts/…` script (`sh`) its text names in backticks, resolved against the skill's directory, then `skill done`; in a bot's conversation, `skill done: <the last result>` sent with `end_turn` |
//! | `invoked` | follows the `<skill>` block that a `/name` put into the prompt, the same way, then `invoked done`; in a bot's conversation, sent with `end_turn`, with the last result after a colon when it read or ran something |
//! | `context` | `skills in context: NAME, …` for the `<skill>` blocks in user messages, or `none` |
//! | `agent:LABEL` | a coding task in seven requests (see [`agent_step`]) |
//! | `fanout:K[:LABEL]` | K `spawn_agent` calls with wait=true, each running `agent`, then a summary |
//! | `read:PATH` | a read of PATH, then `read done` |
//! | `browser:URL` | `browser_navigate` to URL, a click on the first button of the page, then `browser done: <the click's result>` |
//! | `churn:N[:SEED]` | N tool turns in one task, each picked at random: read, write or edit `notes/state.txt`, append to `notes/log.txt`, search with grep, find or rg, read part of a file; then `churn done` (see [`churn_step`]) |
//! | `mcp:SERVER:PROJECT` | `tool_search` for the vault tool, `call_tool` on `mcp__SERVER__vault_reveal_code`, then `mcp done: <its result>`, sent with `end_turn` in a bot's conversation |
//! | `mcp_direct:SERVER:PROJECT` | a direct call of `mcp__SERVER__vault_reveal_code`, then `mcp done: <its result>`, the same way |
//! | `mesh:NAME[:JSON]` | `tool_search` for the `mesh` group, `call_tool` on NAME with the JSON object as its arguments (none: `{}`; the JSON may hold colons, but no `]]`), then `mesh done: <its result>`, or `mesh failed: <its result>` when the call was denied or failed (see [`mesh_failed`]), sent with `end_turn` in a bot's conversation; there, `回复：<the latest message>` sent with `end_turn` for whatever comes next without a directive of its own, news too |
//! | `sheet:PATH` | a workbook session through `call_tool`: `doc_open` PATH, `doc_read` the inputs, `sheet_calc` WACC at 9%, `doc_edit` it, `sheet_trace` what reads it, `doc_save`; then `sheet done` |
//! | `mcp_env:SERVER:VAR` | `call_tool` on `mcp__SERVER__env` for VAR (the server's environment), then `mcp done: <its result>`, the same way |
//! | `send[:TEXT]` | a bot's `send_to_user` of TEXT (else the prompt) with `end_turn` |
//! | `attach:PATH[,PATH…]` | a bot's `send_to_user` of those files with `end_turn` |
//! | `ask:QUESTION\|A\|B…` | a bot's `send_to_user` of that question and its answers |
//! | `relay[:LABEL]` | a bot's `spawn_agent` (described LABEL) with "在查了。" sent and `end_turn`; when the report wakes it, `查到了：<the report>` sent with `end_turn` |
//! | `web_search:QUERY[\|allowed=A,B][\|blocked=C][\|recency=week]` | `tool_search` for the search tool, `call_tool` on `web_search`, then `search done: <its result>` |
//! | `confirm[:CMD]` | a bot's "要执行命令，确认一下。" sent with a bash call running CMD in the same reply, then `做完了：<its result>` sent with `end_turn` |
//! | `relay_slow:SECONDS` | a bot's opening line and a `spawn_agent` (named `writer`) that runs `sleep SECONDS`, a private line that ends the turn, then `回复：<the latest message>` sent with `end_turn` for whatever comes next, its report too |
//! | `relay_told:SECONDS` | a bot's opening line and a `spawn_agent` (named `sales`) that runs `told`, a private line, then `send_to_user` of each thing that wakes it |
//! | `told:SECONDS:TEXT` | a bot's sub-agent: `send_to_user` of TEXT, `sleep SECONDS`, then its report |
//! | `check` | `tool_search`, `call_tool` on `list_agents`, its result sent with `end_turn`, then as `relay_slow` |
//! | `stop_all` | the same with `stop_agents` and `all`, ahead of any message |
//! | `cua:TOOL[:APP]` | `tool_search` for the desktop tools, `call_tool` on `cua_TOOL` (APP as its `app`), then `cua done: <its result>` sent with `end_turn` |
//! | `relay_note:SECONDS` | as `relay_slow`, with a sub-agent that runs `note:SECONDS:3 月的数据整月缺失。` |
//! | `note_session:SECONDS` | the same in a session: `spawn_agent`, `交给子智能体了。`, then `回复：<the latest message>` for whatever comes next |
//! | `note:SECONDS:TEXT` | half a second later, a sub-agent's `send_message` of TEXT to `main`, then a bash call running `sleep SECONDS`, then `写完了。` |
//! | `routine:NAME\|SCHEDULE\|RUN` | a bot's `manage_routine` create, with RUN as the routine's prompt in brackets (`[[mock:RUN]]`); then `转告：<its result>` sent with `end_turn`, and for each report that wakes it, what the run found, sent with `end_turn` |
//! | `connect:NAME[\|URL]` | Her's `call_tool` on `connector_connect` (`name` NAME, `url` URL); then `send_to_user` of the card it prepared (`type` card); when how the card ended wakes her: if it is done, `call_tool` on the connector's `echo` (`mcp__NAME__echo`); then `转告：<its note>`, with what `echo` said on a line of its own, sent with `end_turn` |
//! | `say:TEXT` | TEXT, as it is |
//! | `remember:LINE` | a bot noting LINE in its memory, the `MEMORY.md` its system prompt gives in `<memory path="…">`: a `write` of LINE when nothing is there yet, else an `edit` that puts LINE after the last line; then `记下了。` sent with `end_turn` |
//! | `recall` | `记得：<what the system prompt's <memory> holds>` sent with `end_turn`, or `什么都不记得。` when it holds nothing |
//!
//! [`MockOptions`] adds a gateway's latency: a pause before the first chunk of every reply
//! and between chunks.

pub mod mcp;
pub mod mcp_auth;
pub mod search;
pub mod simulation;

use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::collections::VecDeque;
use std::io;
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio::net::TcpStream;

pub const MODEL_ID: &str = "mock-1";

/// A key the mock's model list refuses (401), for key checks.
pub const REJECTED_KEY: &str = "sk-rejected";

/// What a list under a path with `/bailian/` answers: part of the real plan's list, dated builds,
/// a routing name and models that do not chat included.
const BAILIAN_PLAN_MODELS: &[&str] = &["qwen3.8-max", "qwen3.7-max", "glm-5.3", "deepseek-v4.1-flash-1017", "wan2.7-image", "auto"];

#[derive(Default)]
pub struct Stats {
    pub requests: AtomicU64,
    pub request_bytes: AtomicU64,
    pub peak_request_bytes: AtomicU64,
    pub planning_ns: AtomicU64,
    pub errors_served: AtomicU64,
    pub chunks: AtomicU64,
    per_scenario: Mutex<HashMap<String, u64>>,
    simulation_errors: Mutex<Vec<String>>,
    /// Body sizes of `churn` requests in arrival order, and how many asked for a handoff.
    churn_bodies: Mutex<Vec<u32>>,
    pub churn_handoffs: AtomicU64,
    /// `GET …/models` requests.
    pub model_lists: AtomicU64,
    /// Requests for the public model catalog (`/catalog/api.json`).
    pub catalog_fetches: AtomicU64,
    /// What the latest chat requests asked for, all but their messages and tools.
    chat_controls: Mutex<VecDeque<Value>>,
}

impl Stats {
    pub fn simulation_errors(&self) -> Vec<String> { self.simulation_errors.lock().clone() }

    pub fn scenario_counts(&self) -> HashMap<String, u64> {
        self.per_scenario.lock().clone()
    }

    /// Body sizes of the `churn` requests so far, in arrival order.
    pub fn churn_bodies(&self) -> Vec<u32> {
        self.churn_bodies.lock().clone()
    }

    /// What the latest chat requests asked for, oldest first: their fields, all but the messages
    /// and tools, for tests of what reaches a provider.
    pub fn chat_controls(&self) -> Vec<Value> {
        self.chat_controls.lock().iter().cloned().collect()
    }

    fn record_controls(&self, request: &Value) {
        let Some(fields) = request.as_object() else { return };
        let controls: serde_json::Map<String, Value> =
            fields.iter().filter(|(k, _)| !matches!(k.as_str(), "messages" | "tools")).map(|(k, v)| (k.clone(), v.clone())).collect();
        let mut recorded = self.chat_controls.lock();
        if recorded.len() == CONTROLS_KEPT {
            recorded.pop_front();
        }
        recorded.push_back(Value::Object(controls));
    }
}

/// How many requests' controls are remembered; long simulations should not grow with them.
const CONTROLS_KEPT: usize = 256;

/// Latency of the simulated gateway.
#[derive(Clone, Copy, Debug, Default)]
pub struct MockOptions {
    /// Pause between the response headers and the first chunk.
    pub ttft: Duration,
    /// Pause before every later chunk, unless the scenario sets its own.
    pub chunk_delay: Duration,
}

struct Shared {
    options: MockOptions,
    stats: Stats,
    next_call: AtomicU64,
    /// Prompts of the `retry` scenario that already got their failure.
    retried: Mutex<HashMap<String, u32>>,
    search: search::SearchState,
}

pub struct MockServer {
    accept: tokio::task::JoinHandle<()>,
    addr: SocketAddr,
    shared: Arc<Shared>,
}

impl MockServer {
    /// Binds 127.0.0.1 on a free port and serves in the background.
    pub async fn start() -> io::Result<MockServer> {
        Self::bind("127.0.0.1:0").await
    }

    pub async fn bind(addr: &str) -> io::Result<MockServer> {
        Self::bind_with(addr, MockOptions::default()).await
    }

    pub async fn bind_with(addr: &str, options: MockOptions) -> io::Result<MockServer> {
        let listener = TcpListener::bind(addr).await?;
        let addr = listener.local_addr()?;
        let shared = Arc::new(Shared {
            options,
            stats: Stats::default(),
            next_call: AtomicU64::new(1),
            retried: Mutex::new(HashMap::new()),
            search: search::SearchState::default(),
        });
        let accept_shared = shared.clone();
        let accept = tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else {
                    continue;
                };
                let _ = stream.set_nodelay(true);
                tokio::spawn(serve_connection(stream, accept_shared.clone()));
            }
        });
        Ok(MockServer { addr, shared, accept })
    }

    pub fn addr(&self) -> SocketAddr {
        self.addr
    }

    pub fn base_url(&self) -> String {
        format!("http://{}/v1", self.addr)
    }

    pub fn stats(&self) -> &Stats {
        &self.shared.stats
    }

    /// `[web_search.endpoints]` pointing every search service but DeepSeek at this server; DeepSeek
    /// is asked at its model account's address, which a test points here too.
    pub fn search_endpoints_toml(&self) -> String {
        search::endpoints_toml(self.addr)
    }

    /// From now on `service` answers every search with this status; 0 ends that.
    pub fn fail_search(&self, service: &str, status: u16) {
        self.shared.search.fail(service, status);
    }

    /// The searches so far, oldest first.
    pub fn search_calls(&self) -> Vec<search::SearchCall> {
        self.shared.search.calls()
    }

    /// A public model catalog in the upstream format.
    pub fn catalog_url(&self) -> String {
        format!("http://{}/catalog/api.json", self.addr)
    }

    /// A `models.toml` with provider `mock` and model `mock/mock-1` pointing at this server.
    pub fn models_toml(&self) -> String {
        format!(
            r#"[providers.mock]
name = "Mock"
api = "openai-chat"
base_url = "{base}"
auth = "none"
currency = "USD"

[[providers.mock.models]]
id = "{MODEL_ID}"
name = "Mock model"
context_window = 200000
max_output_tokens = 32000
reasoning = true
input = ["text", "image"]
cost = {{ input = 1.0, output = 2.0, cache_read = 0.1 }}
"#,
            base = self.base_url()
        )
    }
}

impl Drop for MockServer {
    fn drop(&mut self) { self.accept.abort(); }
}

struct Request {
    method: String,
    path: String,
    body: Vec<u8>,
    close: bool,
    authorization: Option<String>,
    /// `X-Subscription-Token` or `X-Api-Key`.
    token: Option<String>,
}

async fn serve_connection(mut stream: TcpStream, shared: Arc<Shared>) {
    let mut buf = Vec::with_capacity(16 * 1024);
    loop {
        let request = match read_request(&mut stream, &mut buf).await {
            Ok(Some(request)) => request,
            _ => return,
        };
        let ok = if let Some((status, body)) = search::answer(&shared.search, &request) {
            write_json(&mut stream, status, &body).await
        } else if request.path.ends_with("/chat/completions") {
            respond_chat(&mut stream, &request.body, &shared).await
        } else if request.path == "/catalog/api.json" {
            shared.stats.catalog_fetches.fetch_add(1, Ordering::Relaxed);
            write_json(&mut stream, 200, &catalog()).await
        } else if request.path.ends_with("/models") {
            shared.stats.model_lists.fetch_add(1, Ordering::Relaxed);
            if request.authorization.as_deref() == Some(&format!("Bearer {REJECTED_KEY}")) {
                write_json(&mut stream, 401, &json!({"error": {"message": "Authentication Fails, Your api key is invalid"}})).await
            } else {
                let ids: &[&str] = if request.path.contains("/bailian/") { BAILIAN_PLAN_MODELS } else { &[MODEL_ID, "mock-2", "deepseek-flash"] };
                let data: Vec<Value> = ids.iter().map(|id| json!({"id": id, "object": "model"})).collect();
                write_json(&mut stream, 200, &json!({"object": "list", "data": data})).await
            }
        } else {
            write_json(&mut stream, 404, &json!({"error": {"message": "not found"}})).await
        };
        if ok.is_err() || request.close {
            return;
        }
    }
}

async fn read_request(stream: &mut TcpStream, buf: &mut Vec<u8>) -> io::Result<Option<Request>> {
    let header_end = loop {
        if let Some(pos) = find(buf, b"\r\n\r\n") {
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
    let request_line = lines.next().unwrap_or("");
    let method = request_line.split(' ').next().unwrap_or("GET").to_string();
    let path = request_line.split(' ').nth(1).unwrap_or("/").to_string();
    let mut length = 0usize;
    let mut close = false;
    let mut authorization = None;
    let mut token = None;
    for line in lines {
        let Some((name, value)) = line.split_once(':') else { continue };
        let value = value.trim();
        match name.trim().to_ascii_lowercase().as_str() {
            "content-length" => length = value.parse().unwrap_or(0),
            "connection" => close = value.eq_ignore_ascii_case("close"),
            "authorization" => authorization = Some(value.to_string()),
            "x-subscription-token" | "x-api-key" => token = Some(value.to_string()),
            _ => {}
        }
    }
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
    Ok(Some(Request { method, path, body, close, authorization, token }))
}

/// The catalog the mock serves: DeepSeek's flash with a window no real catalog has, so a test
/// can tell it was taken; some other built-in catalog ids empty, the rest missing, which costs
/// only those.
fn catalog() -> Value {
    let mut root = serde_json::Map::new();
    for id in ["alibaba-cn", "anthropic", "moonshotai-cn", "openai", "openrouter", "zhipuai"] {
        root.insert(id.into(), json!({"models": {}}));
    }
    root.insert(
        "deepseek".into(),
        json!({"models": {
            "deepseek-flash": {
                "id": "deepseek-flash", "name": "DeepSeek Flash (mock catalog)",
                "reasoning": true, "tool_call": true,
                "reasoning_options": [{"type": "toggle"}, {"type": "effort", "values": ["low", "high", "max"]}],
                "modalities": {"input": ["text"], "output": ["text"]},
                "limit": {"context": 777777, "output": 32000},
                "release_date": "2026-10-17",
                "cost": {"input": 0.15, "output": 0.6}
            },
            "deepseek-embed": {"id": "deepseek-embed", "type": "embedding", "limit": {"context": 8192}}
        }}),
    );
    Value::Object(root)
}

fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack.windows(needle.len()).position(|w| w == needle)
}

async fn write_json(stream: &mut TcpStream, status: u16, body: &Value) -> io::Result<()> {
    let body = body.to_string();
    let reason = match status {
        200 => "OK",
        400 => "Bad Request",
        401 => "Unauthorized",
        404 => "Not Found",
        _ => "Internal Server Error",
    };
    let head = format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
        body.len()
    );
    stream.write_all(head.as_bytes()).await?;
    stream.write_all(body.as_bytes()).await?;
    stream.flush().await
}

/// One piece of a streamed reply.
enum Part {
    Text(String),
    Thinking(String),
    Call { name: String, args: Value },
}

struct Step {
    parts: Vec<Part>,
    finish: &'static str,
    /// Pause before each chunk.
    delay: Duration,
}

impl Step {
    fn text(text: impl Into<String>) -> Self {
        Self {
            parts: vec![Part::Text(text.into())],
            finish: "stop",
            delay: Duration::ZERO,
        }
    }

    fn calls(calls: Vec<(&str, Value)>) -> Self {
        Self {
            parts: calls
                .into_iter()
                .map(|(name, args)| Part::Call {
                    name: name.to_string(),
                    args,
                })
                .collect(),
            finish: "tool_calls",
            delay: Duration::ZERO,
        }
    }
}

enum Plan {
    Step(Step),
    Status(u16, &'static str),
    Error(u16, String),
}

/// The text of a message's `content`, whether a string or a list of parts.
fn content_text(message: &Value) -> String {
    match message.get("content") {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Array(parts)) => parts
            .iter()
            .filter_map(|p| p.get("text").and_then(Value::as_str))
            .collect::<Vec<_>>()
            .join("\n"),
        _ => String::new(),
    }
}

/// (scenario, argument, prompt without the directive, step)
fn directive(messages: &[Value]) -> (String, String, String, usize) {
    for (i, message) in messages.iter().enumerate().rev() {
        if message.get("role").and_then(Value::as_str) != Some("user") {
            continue;
        }
        let text = content_text(message);
        if let Some(start) = text.find("[[mock:")
            && let Some(len) = text[start..].find("]]")
        {
            let inner = &text[start + 7..start + len];
            let (name, arg) = inner.split_once(':').unwrap_or((inner, ""));
            let prompt = format!("{}{}", &text[..start], &text[start + len + 2..]).trim().to_string();
            let step = messages[i + 1..]
                .iter()
                .filter(|m| m.get("role").and_then(Value::as_str) == Some("assistant"))
                .count();
            return (name.to_string(), arg.to_string(), prompt, step);
        }
    }
    let step = messages
        .iter()
        .rev()
        .take_while(|m| m.get("role").and_then(Value::as_str) != Some("user"))
        .filter(|m| m.get("role").and_then(Value::as_str) == Some("assistant"))
        .count();
    ("text".to_string(), String::new(), String::new(), step)
}

/// What `send` replies to the user messages after the last assistant message: each one's
/// argument, else its prompt.
fn unanswered_sends(messages: &[Value]) -> Vec<String> {
    let from = messages
        .iter()
        .rposition(|m| m.get("role").and_then(Value::as_str) == Some("assistant"))
        .map_or(0, |i| i + 1);
    let texts: Vec<String> = messages[from..]
        .iter()
        .filter(|m| m.get("role").and_then(Value::as_str) == Some("user"))
        .filter_map(|m| {
            let text = content_text(m);
            let start = text.find("[[mock:send")?;
            let len = text[start..].find("]]")?;
            let arg = text[start + 7..start + len].strip_prefix("send").unwrap_or("").trim_start_matches(':');
            let prompt = format!("{}{}", &text[..start], &text[start + len + 2..]).trim().to_string();
            Some(if arg.is_empty() { prompt } else { arg.to_string() })
        })
        .collect();
    if texts.is_empty() { vec!["send done".to_string()] } else { texts }
}

/// The text of the latest user message, past the word that follows a bot's owner's: a report,
/// after a bot's sub-agent finished.
fn last_user_text(messages: &[Value]) -> String {
    messages
        .iter()
        .rev()
        .filter(|m| m.get("role").and_then(Value::as_str) == Some("user"))
        .map(content_text)
        .find(|text| !text.starts_with("<reminder>"))
        .unwrap_or_default()
}

/// The text of the latest tool result.
fn last_tool_result(messages: &[Value]) -> String {
    messages
        .iter()
        .rev()
        .find(|m| m.get("role").and_then(Value::as_str) == Some("tool"))
        .map(content_text)
        .unwrap_or_default()
}

/// Whether the request lets the model call the tool `name`.
fn offers_tool(request: &Value, name: &str) -> bool {
    request
        .get("tools")
        .and_then(Value::as_array)
        .is_some_and(|tools| tools.iter().any(|t| t.pointer("/function/name").and_then(Value::as_str) == Some(name)))
}

/// A bot's whole answer to the latest message, a report or the owner's.
fn answer_latest(messages: &[Value]) -> Step {
    Step::calls(vec![("send_to_user", json!({ "text": format!("回复：{}", last_user_text(messages)), "end_turn": true }))])
}

/// What a routine's report found: the text inside `<routine_report …>`, without the tags or what
/// the kernel says after them. `None` for anything else.
fn routine_found(text: &str) -> Option<&str> {
    let (_, rest) = text.strip_prefix("<routine_report ")?.split_once('>')?;
    let (found, _) = rest.split_once("</routine_report>")?;
    Some(found.trim())
}

/// How the latest card the model read of ended, and its note: what `<card … outcome="…">` holds.
fn card_end(messages: &[Value]) -> Option<(String, String)> {
    messages.iter().rev().map(content_text).find_map(|text| {
        let (tag, rest) = text.strip_prefix("<card ")?.split_once('>')?;
        let outcome = tag.split_once("outcome=\"")?.1.split('"').next()?.to_string();
        Some((outcome, rest.strip_suffix("</card>")?.to_string()))
    })
}

/// Whether a `mesh` tool's result says the call did not go through. The wire carries no error
/// flag, so the text decides: what the kernel says of a call that was denied, could not be
/// approved, was not found or was stopped; what it says when the mesh host cannot be reached; or
/// a JSON document whose `ok` is false.
fn mesh_failed(result: &str) -> bool {
    const REFUSED: &[&str] = &[
        "The user denied this tool call",
        "Tool call denied",
        "This tool call needs approval",
        "Unknown tool",
        "Invalid arguments",
        "Cancelled by user",
        "The mesh host",
    ];
    REFUSED.iter().any(|start| result.starts_with(start)) || serde_json::from_str::<Value>(result).is_ok_and(|document| document["ok"] == json!(false))
}

/// A scenario's last words: text, or in a bot's conversation, where only `send_to_user` reaches
/// the owner, sent with `end_turn`.
fn last_words(bot: bool, text: String) -> Plan {
    if bot {
        Plan::Step(Step::calls(vec![("send_to_user", json!({ "text": text, "end_turn": true }))]))
    } else {
        Plan::Step(Step::text(text))
    }
}

fn now_us() -> u128 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_micros())
}

/// `bot`: a bot's conversation, which offers `send_to_user`.
fn plan(shared: &Shared, messages: &[Value], name: &str, arg: &str, prompt: &str, step: usize, bot: bool) -> Plan {
    let n = |default: usize| arg.parse().unwrap_or(default);
    let done = |label: &str| Plan::Step(Step::text(format!("{label} done")));
    match name {
        "sim" => simulation::plan(messages, arg),
        "skill" => skill_step(messages, arg, step, bot),
        "invoked" => invoked_step(messages, prompt, step, bot),
        "context" => Plan::Step(Step::text(skills_in_context(messages))),
        "echo" => Plan::Step(Step::text(format!("echo: {prompt}"))),
        "remember" => remember_step(messages, arg, step, bot),
        "recall" => match memory(messages) {
            Some((_, text)) if !text.is_empty() => last_words(bot, format!("记得：{text}")),
            _ => last_words(bot, "什么都不记得。".into()),
        },
        "think" => Plan::Step(Step {
            parts: vec![
                Part::Thinking("Let me think about this.".into()),
                Part::Text("Thought it through.".into()),
            ],
            finish: "stop",
            delay: Duration::ZERO,
        }),
        "long" => Plan::Step(Step {
            parts: (0..n(200)).map(|i| Part::Text(format!("t{i} "))).collect(),
            finish: "stop",
            delay: Duration::ZERO,
        }),
        "big" => {
            let line: String = "0123456789abcdef".repeat(64);
            Plan::Step(Step {
                parts: (0..n(64)).map(|_| Part::Text(line.clone())).collect(),
                finish: "stop",
                delay: Duration::ZERO,
            })
        }
        "stamp" => {
            let (count, pause) = arg.split_once(':').unwrap_or((arg, "0"));
            Plan::Step(Step {
                // Filled in at send time.
                parts: (0..count.parse().unwrap_or(50)).map(|_| Part::Text(String::new())).collect(),
                finish: "stop",
                delay: Duration::from_millis(pause.parse().unwrap_or(0)),
            })
        }
        "slow" => Plan::Step(Step {
            parts: (0..n(50)).map(|i| Part::Text(format!("s{i} "))).collect(),
            finish: "stop",
            delay: Duration::from_millis(100),
        }),
        "bash" => match step {
            0 => Plan::Step(Step::calls(vec![("bash", json!({ "command": if arg.is_empty() { "echo hi" } else { arg } }))])),
            _ => done("bash"),
        },
        "files" => match step {
            0 => Plan::Step(Step::calls(vec![("write", json!({"path": "notes.txt", "content": "alpha\nbeta\n"}))])),
            1 => Plan::Step(Step::calls(vec![("read", json!({"path": "notes.txt"}))])),
            2 => Plan::Step(Step::calls(vec![(
                "edit",
                json!({"path": "notes.txt", "edits": [{"old_text": "alpha", "new_text": "gamma"}]}),
            )])),
            _ => done("files"),
        },
        "parallel" => match step {
            0 => Plan::Step(Step::calls(vec![
                ("write", json!({"path": "p1.txt", "content": "one\n"})),
                ("write", json!({"path": "p2.txt", "content": "two\n"})),
            ])),
            _ => done("parallel"),
        },
        "loop" => {
            if step < n(3) {
                Plan::Step(Step::calls(vec![("bash", json!({ "command": format!("echo round {step}") }))]))
            } else {
                done("loop")
            }
        }
        "retry" => {
            let mut retried = shared.retried.lock();
            let count = retried.entry(prompt.to_string()).or_insert(0);
            *count += 1;
            if *count == 1 {
                Plan::Status(500, "mock failure, try again")
            } else {
                Plan::Step(Step::text("recovered after a retry"))
            }
        }
        "mcp" | "mcp_direct" => {
            let (server, project) = arg.split_once(':').unwrap_or((arg, "apollo"));
            let tool = format!("mcp__{server}__vault_reveal_code");
            let arguments = json!({ "project": project });
            match (name, step) {
                ("mcp", 0) => Plan::Step(Step::calls(vec![("tool_search", json!({ "query": "vault release code" }))])),
                ("mcp", 1) => Plan::Step(Step::calls(vec![("call_tool", json!({ "name": tool, "arguments": arguments }))])),
                ("mcp_direct", 0) => Plan::Step(Step::calls(vec![(tool.as_str(), arguments)])),
                _ => last_words(bot, format!("mcp done: {}", last_tool_result(messages))),
            }
        }
        "mesh" => {
            let (tool, arguments) = arg.split_once(':').unwrap_or((arg, ""));
            match step {
                0 => Plan::Step(Step::calls(vec![("tool_search", json!({ "server": "mesh" }))])),
                1 => {
                    let arguments = serde_json::from_str::<Value>(arguments).ok().filter(Value::is_object).unwrap_or_else(|| json!({}));
                    Plan::Step(Step::calls(vec![("call_tool", json!({ "name": tool, "arguments": arguments }))]))
                }
                // What wakes a bot after its reply, such as news of the work it started.
                3.. if bot => Plan::Step(answer_latest(messages)),
                _ => {
                    let result = last_tool_result(messages);
                    let how = if mesh_failed(&result) { "failed" } else { "done" };
                    last_words(bot, format!("mesh {how}: {result}"))
                }
            }
        }
        "sheet" => {
            let call = |name: &str, arguments: Value| Plan::Step(Step::calls(vec![("call_tool", json!({ "name": name, "arguments": arguments }))]));
            let wacc = json!([{ "op": "set_value", "sheet": "假设", "cell": "B6", "value": 0.09 }]);
            match step {
                0 => call("doc_open", json!({ "path": arg })),
                1 => call("doc_read", json!({ "doc": arg, "range": "假设!A1:C9" })),
                2 => call("sheet_calc", json!({ "doc": arg, "ops": wacc })),
                3 => call("doc_edit", json!({ "doc": arg, "ops": wacc, "note": "WACC 上调到 9%" })),
                4 => call("sheet_trace", json!({ "doc": arg, "cell": "假设!B6", "direction": "down", "depth": 2 })),
                5 => call("doc_save", json!({ "doc": arg })),
                _ => done("sheet"),
            }
        }
        "mcp_env" => {
            let (server, var) = arg.split_once(':').unwrap_or((arg, "PATH"));
            match step {
                0 => Plan::Step(Step::calls(vec![("call_tool", json!({ "name": format!("mcp__{server}__env"), "arguments": { "name": var } }))])),
                _ => last_words(bot, format!("mcp done: {}", last_tool_result(messages))),
            }
        }
        "web_search" => match step {
            0 => Plan::Step(Step::calls(vec![("tool_search", json!({ "query": "web search" }))])),
            1 => Plan::Step(Step::calls(vec![("call_tool", json!({ "name": "web_search", "arguments": search::call_arguments(arg) }))])),
            _ => Plan::Step(Step::text(format!("search done: {}", last_tool_result(messages)))),
        },
        "fail" => Plan::Status(400, "mock rejects this request"),
        "send" => match step {
            0 => {
                // Every message since the bot last replied gets its own, as a bot answers a
                // burst; the last ends the turn.
                let texts = unanswered_sends(messages);
                let last = texts.len() - 1;
                Plan::Step(Step::calls(
                    texts
                        .into_iter()
                        .enumerate()
                        .map(|(i, text)| ("send_to_user", json!({ "text": text, "end_turn": i == last })))
                        .collect(),
                ))
            }
            _ => done("send"),
        },
        "attach" => match step {
            0 => Plan::Step(Step::calls(vec![(
                "send_to_user",
                json!({
                    "type": "attachment",
                    "files": arg.split(',').map(|path| json!({ "path": path })).collect::<Vec<_>>(),
                    "end_turn": true,
                }),
            )])),
            _ => done("attach"),
        },
        "ask" => match step {
            0 => {
                let mut parts = arg.split('|');
                let question = parts.next().unwrap_or_default();
                let options: Vec<_> = parts.map(|label| json!({ "label": label })).collect();
                Plan::Step(Step::calls(vec![("send_to_user", json!({ "type": "question", "text": question, "options": options }))]))
            }
            _ => done("ask"),
        },
        // Commands for sub-agents, one each, which the user may have to approve.
        "delegate" => match step {
            0 => Plan::Step(Step::calls(delegated(arg))),
            1 => Plan::Step(Step::text("交给子智能体了。".to_string())),
            _ => done("delegate"),
        },
        // The same for a bot.
        "relay_run" => match step {
            0 => {
                let mut calls = delegated(arg);
                calls.push(("send_to_user", json!({ "text": "交给子智能体了。", "end_turn": true })));
                Plan::Step(Step::calls(calls))
            }
            _ => Plan::Step(Step::calls(vec![(
                "send_to_user",
                json!({ "text": format!("做完了：{}", last_user_text(messages)), "end_turn": true }),
            )])),
        },
        "relay" => match step {
            0 => Plan::Step(Step::calls(vec![
                (
                    "spawn_agent",
                    json!({ "description": if arg.is_empty() { "查资料" } else { arg }, "task": "Look it up and report." }),
                ),
                ("send_to_user", json!({ "text": "在查了。", "end_turn": true })),
            ])),
            _ => Plan::Step(Step::calls(vec![(
                "send_to_user",
                json!({ "text": format!("查到了：{}", last_user_text(messages)), "end_turn": true }),
            )])),
        },
        // Long work handed over, and the bot goes on talking (bot-turn-design §3).
        "relay_slow" => match step {
            0 => Plan::Step(Step::calls(vec![
                ("send_to_user", json!({ "text": "好，这个要一会儿，写好发你。" })),
                (
                    "spawn_agent",
                    json!({ "description": "写长文", "task": format!("[[mock:bash:sleep {arg}]] Write it and report."), "name": "writer" }),
                ),
            ])),
            1 => Plan::Step(Step::text("It is at work; its report will wake me.".to_string())),
            _ => Plan::Step(answer_latest(messages)),
        },
        // The same with a sub-agent that speaks up on the way (agent-messaging-design §3.1).
        "relay_note" => match step {
            0 => Plan::Step(Step::calls(vec![
                ("send_to_user", json!({ "text": "好，我先把数据汇总出来。" })),
                (
                    "spawn_agent",
                    json!({ "description": "汇总销售数据", "task": format!("[[mock:note:{arg}:3 月的数据整月缺失。]] Sum it and report."), "name": "sales" }),
                ),
            ])),
            1 => Plan::Step(Step::text("It is at work; its report will wake me.".to_string())),
            _ => Plan::Step(answer_latest(messages)),
        },
        // A sub-agent that tells the owner itself, with the bot asleep meanwhile (§17).
        "relay_told" => match step {
            0 => Plan::Step(Step::calls(vec![
                ("send_to_user", json!({ "text": "好，我先把数据汇总出来。" })),
                (
                    "spawn_agent",
                    json!({ "description": "汇总销售数据", "task": format!("[[mock:told:{arg}:3 月的数据整月缺失，我先按其余 11 个月做。]] Sum it and report."), "name": "sales" }),
                ),
            ])),
            1 => Plan::Step(Step::text("It is at work; its report will wake me.".to_string())),
            _ => Plan::Step(answer_latest(messages)),
        },
        "note_session" => match step {
            0 => Plan::Step(Step::calls(vec![(
                "spawn_agent",
                json!({ "description": "汇总销售数据", "task": format!("[[mock:note:{arg}:3 月的数据整月缺失。]] Sum it and report."), "name": "sales" }),
            )])),
            1 => Plan::Step(Step::text("交给子智能体了。".to_string())),
            _ => Plan::Step(Step::text(format!("回复：{}", last_user_text(messages)))),
        },
        "note" => {
            let (seconds, text) = arg.split_once(':').unwrap_or((arg, "something came up"));
            match step {
                // By then the bot has ended its turn.
                0 => Plan::Step(Step {
                    delay: Duration::from_millis(500),
                    ..Step::calls(vec![("send_message", json!({ "to": "main", "message": text }))])
                }),
                1 => Plan::Step(Step::calls(vec![("bash", json!({ "command": format!("sleep {seconds}") }))])),
                _ => Plan::Step(Step::text("写完了。".to_string())),
            }
        }
        "told" => {
            let (seconds, text) = arg.split_once(':').unwrap_or((arg, "有新进展。"));
            match step {
                // By then the bot has ended its turn.
                0 => Plan::Step(Step {
                    delay: Duration::from_millis(500),
                    ..Step::calls(vec![("send_to_user", json!({ "text": text }))])
                }),
                1 => Plan::Step(Step::calls(vec![("bash", json!({ "command": format!("sleep {seconds}") }))])),
                _ => Plan::Step(Step::text("写完了。".to_string())),
            }
        }
        // Computer use: its tools are deferred too.
        "cua" => {
            let (tool, arguments) = match arg.split_once(':') {
                Some((tool, app)) => (tool, json!({ "app": app })),
                None => (arg, json!({})),
            };
            match step {
                0 => Plan::Step(Step::calls(vec![("tool_search", json!({ "query": "cua desktop apps" }))])),
                1 => Plan::Step(Step::calls(vec![("call_tool", json!({ "name": format!("cua_{tool}"), "arguments": arguments }))])),
                2 => Plan::Step(Step::calls(vec![("send_to_user", json!({ "text": format!("cua done: {}", last_tool_result(messages)), "end_turn": true }))])),
                _ => done("cua"),
            }
        }
        // Asked how the agents stand, or to stop them: the tools are deferred (bot-turn-design §5.3).
        "check" | "stop_all" => {
            let (tool, arguments) = if name == "check" { ("list_agents", json!({})) } else { ("stop_agents", json!({ "all": true })) };
            match step {
                0 => Plan::Step(Step::calls(vec![("tool_search", json!({ "query": tool }))])),
                1 => Plan::Step(Step::calls(vec![("call_tool", json!({ "name": tool, "arguments": arguments }))])),
                2 => Plan::Step(Step::calls(vec![("send_to_user", json!({ "text": last_tool_result(messages), "end_turn": true }))])),
                _ => Plan::Step(answer_latest(messages)),
            }
        }
        // A bot that answers in plain text, which the user never sees, and sends once reminded
        // (bot-design §3.11). It takes long enough for a next message to come meanwhile.
        "quiet" => {
            let answer = if arg.is_empty() { prompt } else { arg };
            match step {
                0 => Plan::Step(Step {
                    delay: Duration::from_millis(200),
                    ..Step::text(answer.to_string())
                }),
                _ if last_user_text(messages).contains("<reply_reminder>") => {
                    Plan::Step(Step::calls(vec![("send_to_user", json!({ "text": answer, "end_turn": true }))]))
                }
                _ => done("quiet"),
            }
        }
        "say" => Plan::Step(Step::text(arg.to_string())),
        // A bot that makes a routine, then passes on what comes back (routine-design §2.3).
        "routine" => match step {
            0 => {
                let mut parts = arg.splitn(3, '|');
                let (name, schedule, run) = (parts.next().unwrap_or(""), parts.next().unwrap_or(""), parts.next().unwrap_or(""));
                Plan::Step(Step::calls(vec![(
                    "manage_routine",
                    json!({ "action": "create", "name": name, "prompt": format!("[[mock:{run}]]"), "schedule": schedule, "when": "说好的时间" }),
                )]))
            }
            _ => {
                // What came last, but for what the kernel adds to keep the model up to date. Of a
                // report, only what the run found, as a model passes it on.
                let text = messages.iter().rev().map(content_text).find(|text| !text.starts_with("<environment_update>")).unwrap_or_default();
                let text = routine_found(&text).map_or_else(|| format!("转告：{text}"), str::to_string);
                Plan::Step(Step::calls(vec![("send_to_user", json!({ "text": text, "end_turn": true }))]))
            }
        },
        // Her asks the owner for a connector on a secure card (docs/her-secure-cards.md §6).
        "connect" => match step {
            0 => {
                let (name, url) = arg.split_once('|').unwrap_or((arg, ""));
                let mut arguments = json!({ "name": name });
                if !url.is_empty() {
                    arguments["url"] = json!(url);
                }
                Plan::Step(Step::calls(vec![("call_tool", json!({ "name": "connector_connect", "arguments": arguments }))]))
            }
            1 => {
                let result = last_tool_result(messages);
                match result.split("Card ").nth(1).and_then(|rest| rest.split_whitespace().next()) {
                    Some(card) => Plan::Step(Step::calls(vec![("send_to_user", json!({ "type": "card", "card": card, "text": "这件事要用到它，请在卡片上确认。" }))])),
                    None => last_words(bot, format!("接不上：{result}")),
                }
            }
            _ => {
                let (outcome, note) = card_end(messages).unwrap_or_default();
                let tried = messages.last().and_then(|m| m.get("role")).and_then(Value::as_str) == Some("tool");
                let name = arg.split('|').next().unwrap_or(arg);
                if outcome == "done" && !tried {
                    // The connector works in the turn its card's end woke.
                    Plan::Step(Step::calls(vec![("call_tool", json!({ "name": format!("mcp__{name}__echo"), "arguments": { "text": "接上了" } }))]))
                } else if tried {
                    last_words(bot, format!("转告：{note}\n{}", last_tool_result(messages)))
                } else {
                    last_words(bot, format!("转告：{note}"))
                }
            }
        },
        // A bot that never sends, reminded or not (bot-design §3.11).
        "mute" => Plan::Step(Step::text("写了字，没有发。".to_string())),
        "confirm" => match step {
            0 => Plan::Step(Step::calls(vec![
                ("send_to_user", json!({ "text": "要执行命令，确认一下。" })),
                ("bash", json!({ "command": if arg.is_empty() { "touch confirmed.txt" } else { arg } })),
            ])),
            _ => Plan::Step(Step::calls(vec![(
                "send_to_user",
                json!({ "text": format!("做完了：{}", last_tool_result(messages)), "end_turn": true }),
            )])),
        },
        "agent" => agent_step(arg, step),
        "fanout" => {
            let (count, label) = arg.split_once(':').unwrap_or((arg, "fan"));
            match step {
                0 => Plan::Step(Step::calls(
                    (0..count.parse().unwrap_or(4))
                        .map(|i| {
                            let task = format!("[[mock:agent:{label}-a{i}]] 请检查子模块 {i} 并写一份报告");
                            ("spawn_agent", json!({"description": format!("检查子模块 {i}"), "task": task, "name": format!("a{i}"), "wait": true}))
                        })
                        .collect(),
                )),
                _ => Plan::Step(Step::text(prose(label, "汇总", 1000))),
            }
        }
        "read" => match step {
            0 => Plan::Step(Step::calls(vec![("read", json!({"path": arg}))])),
            _ => done("read"),
        },
        "browser" => match step {
            0 => Plan::Step(Step::calls(vec![("browser_navigate", json!({"url": arg}))])),
            1 => {
                // The first button's ref in the page the navigation reported.
                let page = last_tool_result(messages);
                let button = page
                    .lines()
                    .find(|line| line.contains("button"))
                    .and_then(|line| line.split("[ref=").nth(1))
                    .and_then(|rest| rest.split(']').next())
                    .unwrap_or("e1")
                    .to_string();
                Plan::Step(Step::calls(vec![("browser_act", json!({"ref": button, "action": "click"}))]))
            }
            _ => Plan::Step(Step::text(format!("browser done: {}", last_tool_result(messages)))),
        },
        // Two pages, the first in the built-in browser and the second in the user's own, then
        // the tabs of both (connectors-entry-design §9.2). The URLs are separated by a space.
        "browsers" => {
            let mut urls = arg.split_whitespace();
            let (builtin, yours) = (urls.next().unwrap_or_default(), urls.next().unwrap_or_default());
            match step {
                0 => Plan::Step(Step::calls(vec![("browser_navigate", json!({"url": builtin}))])),
                1 => Plan::Step(Step::calls(vec![("browser_navigate", json!({"url": yours, "browser": "yours"}))])),
                2 => Plan::Step(Step::calls(vec![("browser_tabs", json!({}))])),
                _ => Plan::Step(Step::text(format!("browsers done: {}", last_tool_result(messages)))),
            }
        }
        // The user's own browser: a page opened there, its tabs, what a tab shows, and handing
        // the page to the user (browser-design §8.4.7).
        "yours" => match step {
            0 => Plan::Step(Step::calls(vec![("browser_navigate", json!({"url": arg.trim(), "browser": "yours"}))])),
            _ => Plan::Step(Step::text(format!("yours opened: {}", last_tool_result(messages)))),
        },
        "yours_tabs" => match step {
            0 => Plan::Step(Step::calls(vec![("browser_tabs", json!({}))])),
            _ => Plan::Step(Step::text(format!("yours tabs: {}", last_tool_result(messages)))),
        },
        "yours_read" => match step {
            0 => Plan::Step(Step::calls(vec![("browser_snapshot", json!({"tab": arg.trim()}))])),
            _ => Plan::Step(Step::text(format!("yours read: {}", last_tool_result(messages)))),
        },
        // A sub-agent opens a page there, and reports.
        "yours_agent" => match step {
            0 => {
                let task = format!("[[mock:yours:{}]] open it", arg.trim());
                Plan::Step(Step::calls(vec![("spawn_agent", json!({"description": "打开页面", "task": task, "name": "opener", "wait": true}))]))
            }
            _ => Plan::Step(Step::text(format!("agent done: {}", last_tool_result(messages)))),
        },
        // Reads the first tab there, then keeps it in use for this many milliseconds.
        "yours_hold" => match step {
            0 => Plan::Step(Step::calls(vec![("browser_snapshot", json!({"tab": "y1"}))])),
            _ => Plan::Step(Step {
                parts: vec![Part::Text("held".into())],
                finish: "stop",
                delay: Duration::from_millis(arg.trim().parse().unwrap_or(1000)),
            }),
        },
        "handoff" => match step {
            0 => Plan::Step(Step::calls(vec![("call_tool", json!({"name": "browser_wait", "arguments": {"user": arg.trim()}}))])),
            _ => Plan::Step(Step::text(format!("handoff done: {}", last_tool_result(messages)))),
        },
        // A page opened without naming a browser: where the user's default sends it.
        "default_browser" => match step {
            0 => Plan::Step(Step::calls(vec![("browser_navigate", json!({"url": arg.trim()}))])),
            _ => Plan::Step(Step::text(format!("default opened: {}", last_tool_result(messages)))),
        },
        "cut" => Plan::Step(Step {
            parts: vec![Part::Text("this reply is cut".into())],
            finish: "length",
            delay: Duration::ZERO,
        }),
        _ => Plan::Step(Step {
            parts: vec![Part::Text("Hello ".into()), Part::Text("from ".into()), Part::Text("mock.".into())],
            finish: "stop",
            delay: Duration::ZERO,
        }),
    }
}

/// A bot's memory as its system prompt gives it (docs/her-memory.md §4): where `MEMORY.md` is,
/// and what it holds, which is nothing before the first note.
fn memory(messages: &[Value]) -> Option<(String, String)> {
    let system = messages
        .first()
        .filter(|m| matches!(m.get("role").and_then(Value::as_str), Some("system" | "developer")))
        .map(content_text)?;
    let (_, rest) = system.rsplit_once("<memory path=\"")?;
    let (path, rest) = rest.split_once("\">\n")?;
    let (text, _) = rest.split_once("\n</memory>")?;
    Some((path.to_string(), if text == "(empty)" { String::new() } else { text.to_string() }))
}

/// A bot noting `line` in its memory, with the file tools it has anyway.
fn remember_step(messages: &[Value], line: &str, step: usize, bot: bool) -> Plan {
    let Some((path, text)) = memory(messages) else {
        return last_words(bot, "no memory".into());
    };
    if step > 0 {
        return last_words(bot, "记下了。".into());
    }
    match text.lines().last() {
        None => Plan::Step(Step::calls(vec![("write", json!({ "path": path, "content": format!("{line}\n") }))])),
        Some(last) => Plan::Step(Step::calls(vec![("edit", json!({ "path": path, "edits": [{ "old_text": last, "new_text": format!("{last}\n{line}") }] }))])),
    }
}

/// A model following the skill `name`: first the SKILL.md that the catalog in the system
/// prompt lists, then what its text names in backticks, as the catalog's instructions say.
fn skill_step(messages: &[Value], name: &str, step: usize, bot: bool) -> Plan {
    let system = messages
        .first()
        .filter(|m| matches!(m.get("role").and_then(Value::as_str), Some("system" | "developer")))
        .map(content_text)
        .unwrap_or_default();
    let marker = format!("<skill name=\"{name}\" path=\"");
    let Some(path) = system
        .find(&marker)
        .and_then(|at| system[at + marker.len()..].split('"').next())
        .map(|path| path.replace("&quot;", "\"").replace("&lt;", "<").replace("&gt;", ">").replace("&amp;", "&"))
    else {
        return last_words(bot, format!("no skill named {name}"));
    };
    if step == 0 {
        return Plan::Step(Step::calls(vec![("read", json!({ "path": path }))]));
    }
    // The first tool result after the prompt holds the SKILL.md.
    let is_prompt =
        |m: &Value| m.get("role").and_then(Value::as_str) == Some("user") && content_text(m).contains("[[mock:skill");
    let start = messages.iter().rposition(is_prompt).unwrap_or(0);
    let body = messages[start..]
        .iter()
        .find(|m| m.get("role").and_then(Value::as_str) == Some("tool"))
        .map(content_text)
        .unwrap_or_default();
    let dir = std::path::Path::new(&path).parent().map(|d| d.display().to_string()).unwrap_or_default();
    match skill_actions(&body, &dir).into_iter().nth(step - 1) {
        Some(action) => Plan::Step(Step::calls(vec![action])),
        // A bot's owner sees what the skill gave: the last file read or script run.
        None if bot => last_words(bot, format!("skill done: {}", last_tool_result(messages))),
        None => Plan::Step(Step::text("skill done")),
    }
}

/// A model following a skill that the user invoked with `/name`: the instructions are already
/// in the prompt, inside a `<skill>` block that says which directory relative paths start from.
fn invoked_step(messages: &[Value], prompt: &str, step: usize, bot: bool) -> Plan {
    let Some(start) = prompt.find("<skill name=\"") else {
        return last_words(bot, "no skill in the message".into());
    };
    let block = &prompt[start..];
    let block = &block[..block.find("</skill>").unwrap_or(block.len())];
    let marker = "Relative paths in this skill are relative to ";
    let dir = block
        .find(marker)
        .and_then(|at| block[at + marker.len()..].split('\n').next())
        .map(|line| line.trim_end_matches('.').to_string())
        .unwrap_or_default();
    match skill_actions(block, &dir).into_iter().nth(step) {
        Some(action) => Plan::Step(Step::calls(vec![action])),
        // A bot's owner sees what the skill gave, when it named something to read or run.
        None if bot && step > 0 => last_words(bot, format!("invoked done: {}", last_tool_result(messages))),
        None => last_words(bot, "invoked done".into()),
    }
}

/// The skills whose instructions the conversation carries: `<skill>` blocks in user messages,
/// as a `/name` or a compaction puts them there.
fn skills_in_context(messages: &[Value]) -> String {
    const MARKER: &str = "<skill name=\"";
    let mut names = Vec::new();
    for message in messages.iter().filter(|m| m.get("role").and_then(Value::as_str) == Some("user")) {
        let text = content_text(message);
        let mut rest = text.as_str();
        while let Some(at) = rest.find(MARKER) {
            rest = &rest[at + MARKER.len()..];
            names.extend(rest.split('"').next().map(str::to_string));
        }
    }
    if names.is_empty() {
        "skills in context: none".into()
    } else {
        format!("skills in context: {}", names.join(", "))
    }
}

/// What a skill's text names in backticks: `references/…` to read, `scripts/…` to run with sh.
fn skill_actions(body: &str, dir: &str) -> Vec<(&'static str, Value)> {
    body.split('`')
        .skip(1)
        .step_by(2)
        .filter_map(|quoted| {
            if quoted.starts_with("references/") {
                Some(("read", json!({ "path": format!("{dir}/{quoted}") })))
            } else if quoted.starts_with("scripts/") {
                Some(("bash", json!({ "command": format!("sh '{dir}/{quoted}'") })))
            } else {
                None
            }
        })
        .collect()
}

/// A coding task with realistic content, one request per step: reasoning and mixed Chinese
/// and English text, a read of `src/lib.rs`, a command and a read in parallel, a 4 KB file
/// (`notes/LABEL.md`, so that tasks sharing a workspace do not collide) written, read back and
/// edited, a command with about 9 KB of output, and a 3000-character
/// summary. The workspace needs `src/lib.rs` and `README.md`.
/// `spawn_agent` calls for the `|`-separated commands: each runs one and reports.
fn delegated(commands: &str) -> Vec<(&'static str, Value)> {
    commands
        .split('|')
        .enumerate()
        .map(|(i, command)| {
            let task = format!("[[mock:bash:{command}]] Run it and report.");
            ("spawn_agent", json!({ "description": format!("运行 {command}"), "task": task, "name": format!("runner-{i}") }))
        })
        .collect()
}

fn agent_step(label: &str, step: usize) -> Plan {
    let call = |name: &str, args: Value| Part::Call {
        name: name.to_string(),
        args,
    };
    let parts = match step {
        0 => vec![
            Part::Thinking(prose(label, "思路", 600)),
            Part::Text(format!("我先看一下 {label} 的代码结构，再决定怎么改。")),
            call("read", json!({"path": "src/lib.rs"})),
        ],
        1 => vec![
            Part::Text("代码读完了，再看看目录和说明文档。".into()),
            call("bash", json!({"command": "ls -la src && wc -l src/lib.rs"})),
            call("read", json!({"path": "README.md"})),
        ],
        2 => vec![
            Part::Thinking(prose(label, "方案", 300)),
            call("write", json!({"path": format!("notes/{label}.md"), "content": report(label)})),
        ],
        3 => vec![call("read", json!({"path": format!("notes/{label}.md")}))],
        4 => vec![call(
            "edit",
            json!({"path": format!("notes/{label}.md"), "edits": [{"old_text": "状态：TODO", "new_text": "状态：DONE"}]}),
        )],
        5 => vec![
            Part::Text("最后跑一下检查。".into()),
            call("bash", json!({"command": "seq 1 2000"})),
        ],
        _ => return Plan::Step(Step::text(prose(label, "总结", 3000))),
    };
    Plan::Step(Step {
        parts,
        finish: "tool_calls",
        delay: Duration::ZERO,
    })
}

/// About `chars` characters of mixed Chinese and English prose about `label`.
fn prose(label: &str, topic: &str, chars: usize) -> String {
    let mut out = String::new();
    let mut i = 0;
    while out.chars().count() < chars {
        let sentence = match i % 4 {
            0 => format!("{topic} {i}：模块 {label} 的第 {i} 项检查通过，耗时稳定在 {} 毫秒。", 12 + i % 7),
            1 => "The parser handles `edge cases` such as empty input, CRLF line endings and very long lines. ".to_string(),
            2 => "需要注意：并发写入时先拿锁，再更新索引，最后刷新缓存。\n".to_string(),
            _ => format!("Next, compare run {i} with the baseline and record the numbers in the table below.\n"),
        };
        out.push_str(&sentence);
        i += 1;
    }
    out
}

/// A 4 KB markdown report with one `状态：TODO` line, for the write and edit steps.
fn report(label: &str) -> String {
    let mut out = format!("# 检查报告：{label}\n\n状态：TODO\n\n| 项目 | 结果 | 说明 |\n|---|---|---|\n");
    let mut row = 0;
    while out.len() < 3600 {
        out.push_str(&format!("| check-{row} | 通过 | 第 {row} 项：输入为空、超长行和 \"引号\" 都已覆盖 |\n"));
        row += 1;
    }
    out.push_str("\n```rust\nfn main() {\n    println!(\"报告完成\");\n}\n```\n");
    out
}

/// Splits text into chunks of about `size` characters.
fn chunks(text: &str, size: usize) -> Vec<String> {
    let chars: Vec<char> = text.chars().collect();
    chars.chunks(size.max(1)).map(|c| c.iter().collect()).collect()
}

/// The marker each `churn` reply carries, and the handoff summary too: the step count of the
/// task survives compactions, which drop the replies that counted it.
const CHURN_MARK: &str = "[churn-step ";

/// Plans a `churn` request from its raw body, or `None` for other scenarios. The history of
/// a churn task grows to hundreds of kilobytes; parsing all of it for each of thousands of
/// requests would make the mock, not the kernel, the slow part.
fn churn_plan(body: &[u8], stats: &Stats) -> Option<Plan> {
    let text = std::str::from_utf8(body).ok()?;
    // The prompt stays in the context through compactions (user messages are kept).
    let start = text.find("[[mock:churn:")? + "[[mock:churn:".len();
    let end = start + text[start..].find("]]")?;
    let (count, seed) = text[start..end].split_once(':').unwrap_or((&text[start..end], "1"));
    let count: usize = count.parse().ok()?;
    let seed: u64 = seed.parse().unwrap_or(1);
    stats.churn_bodies.lock().push(body.len() as u32);
    // The step the latest reply (or summary) reached; the first request has none.
    let done = text.rfind(CHURN_MARK).and_then(|at| {
        let rest = &text[at + CHURN_MARK.len()..];
        rest[..rest.find(']')?].parse::<usize>().ok()
    });
    if text.contains("<handoff_request>") {
        stats.churn_handoffs.fetch_add(1, Ordering::Relaxed);
        let reached = done.unwrap_or(0);
        return Some(Plan::Step(Step::text(format!(
            "Goal: run the churn task to {count} steps. Progress: {CHURN_MARK}{reached}] steps are done; \
             notes/state.txt and notes/log.txt hold the latest state. Next: continue with the next step."
        ))));
    }
    let step = done.map_or(0, |d| d + 1);
    if step >= count {
        return Some(Plan::Step(Step::text(format!("churn done after {count} steps"))));
    }
    Some(Plan::Step(churn_step(seed, step)))
}

/// What step `k` of a churn task does, from a hash of the seed and the step: a write of
/// `notes/state.txt` (1–3 KB with a `TODO-k` line), often followed by an edit of that line;
/// reads of it; a line appended to `notes/log.txt`; a search (grep, find, rg); a read of part
/// of `src/lib.rs` or of the log; or a small note file written.
fn churn_step(seed: u64, k: usize) -> Step {
    let roll = |k: usize| {
        let mut x = seed ^ (k as u64).wrapping_mul(0x9E37_79B9_7F4A_7C15);
        x ^= x >> 33;
        x = x.wrapping_mul(0xFF51_AFD7_ED55_8CCD);
        x ^= x >> 33;
        x % 100
    };
    // A step that rolls a write is an edit instead when the step before it wrote: so in a
    // run of such steps, writes and edits alternate.
    // Step 0 is always a read.
    let is_write = |k: usize| {
        let mut run = 0;
        while run < k && (25..45).contains(&roll(k - run)) {
            run += 1;
        }
        run % 2 == 1
    };
    let r = roll(k);
    let (what, name, args) = if k == 0 {
        // The write tool overwrites only files read in the session.
        ("read", "read", json!({"path": "notes/state.txt"}))
    } else if is_write(k - 1) && r < 60 {
        let prev = k - 1;
        (
            "edit",
            "edit",
            json!({"path": "notes/state.txt", "edits": [{"old_text": format!("TODO-{prev}"), "new_text": format!("DONE-{prev}")}]}),
        )
    } else {
        match r {
            0..25 => ("read", "read", json!({"path": "notes/state.txt"})),
            25..45 => {
                let mut content = format!("# state at step {k}\nTODO-{k}\n");
                let lines = 20 + (r as usize + k) % 40;
                for i in 0..lines {
                    content.push_str(&format!("item {i:>3}: value {} 状态正常 ok\n", (k * 31 + i * 7) % 1000));
                }
                ("write", "write", json!({"path": "notes/state.txt", "content": content}))
            }
            45..60 => (
                "append",
                "bash",
                json!({"command": format!("printf 'entry %d at step %d\\n' {} {k} >> notes/log.txt && tail -n 3 notes/log.txt", k % 97)}),
            ),
            60..72 => {
                let command = match k % 4 {
                    0 => format!("grep -n 'value_{}(' src/lib.rs | head -5", k % 50),
                    1 => format!("grep -c 'entry {}' notes/log.txt", k % 97),
                    2 => "find . -name '*.txt' -not -path './.git/*' | sort | head -20".to_string(),
                    _ => "rg -n 'TODO|DONE' notes | head -10 || grep -rn 'TODO\\|DONE' notes | head -10".to_string(),
                };
                ("search", "bash", json!({ "command": command }))
            }
            // About one step in seven appends a line; this stays inside the log.
            72..80 => ("read log", "read", json!({"path": "notes/log.txt", "offset": 1 + k / 16, "limit": 40})),
            80..88 => ("read source", "read", json!({"path": "src/lib.rs", "offset": 1 + (k * 13) % 600, "limit": 60})),
            _ => (
                "note",
                "write",
                json!({"path": format!("notes/n{}.md", k % 10), "content": format!("note {k}\n{}", "- point\n".repeat(1 + k % 8))}),
            ),
        }
    };
    Step {
        parts: vec![
            Part::Text(format!("{CHURN_MARK}{k}] {what}")),
            Part::Call { name: name.to_string(), args },
        ],
        finish: "tool_calls",
        delay: Duration::ZERO,
    }
}

/// `her models check` offers a tool of its own and can put no directive in its words.
fn check_plan(request: &Value, messages: &[Value]) -> Option<Plan> {
    let tools = request.get("tools")?.as_array()?;
    tools.iter().find(|tool| tool["function"]["name"] == "check_echo")?;
    if messages.last().is_some_and(|m| m.get("role").and_then(Value::as_str) == Some("tool")) {
        return Some(Plan::Step(Step::text("ping")));
    }
    let text = messages.last().map(content_text).unwrap_or_default();
    let echo = |text: &str| ("check_echo", json!({"text": text}));
    let calls = if text.contains("twice") { vec![echo("a"), echo("b")] } else { vec![echo("ping")] };
    Some(Plan::Step(Step::calls(calls)))
}

async fn respond_chat(stream: &mut TcpStream, body: &[u8], shared: &Shared) -> io::Result<()> {
    shared.stats.requests.fetch_add(1, Ordering::Relaxed);
    shared.stats.request_bytes.fetch_add(body.len() as u64, Ordering::Relaxed);
    shared.stats.peak_request_bytes.fetch_max(body.len() as u64, Ordering::Relaxed);
    let planning = std::time::Instant::now();
    let (name, plan) = match churn_plan(body, &shared.stats) {
        Some(plan) => ("churn".to_string(), plan),
        None => {
            let request: Value = match serde_json::from_slice(body) {
                Ok(v) => v,
                Err(_) => return write_json(stream, 400, &json!({"error": {"message": "invalid JSON"}})).await,
            };
            shared.stats.record_controls(&request);
            let messages = request.get("messages").and_then(Value::as_array).map(Vec::as_slice).unwrap_or_default();
            let (name, arg, prompt, step) = directive(messages);
            let bot = offers_tool(&request, "send_to_user");
            let plan = match check_plan(&request, messages) {
                Some(plan) => plan,
                // An assistant's conversation with no directive: it answers the latest message,
                // as a model does, instead of writing text nobody sees.
                None if name == "text" && step == 0 && bot => Plan::Step(answer_latest(messages)),
                None => plan(shared, messages, &name, &arg, &prompt, step, bot),
            };
            (name, plan)
        }
    };
    shared.stats.planning_ns.fetch_add(planning.elapsed().as_nanos() as u64, Ordering::Relaxed);
    *shared.stats.per_scenario.lock().entry(name.clone()).or_insert(0) += 1;
    let step = match plan {
        Plan::Step(step) => step,
        Plan::Status(status, message) => {
            shared.stats.errors_served.fetch_add(1, Ordering::Relaxed);
            let kind = if status >= 500 { "server_error" } else { "invalid_request_error" };
            return write_json(stream, status, &json!({"error": {"message": message, "type": kind}})).await;
        }
        Plan::Error(status, message) => {
            if name == "sim" {
                let mut errors = shared.stats.simulation_errors.lock();
                if errors.len() < 16 { errors.push(message.clone()); }
            }
            shared.stats.errors_served.fetch_add(1, Ordering::Relaxed);
            let kind = if status >= 500 { "server_error" } else { "invalid_request_error" };
            return write_json(stream, status, &json!({"error": {"message": message, "type": kind}})).await;
        }
    };
    let head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\nTransfer-Encoding: chunked\r\n\r\n";
    stream.write_all(head.as_bytes()).await?;
    stream.flush().await?;
    let options = shared.options;
    if !options.ttft.is_zero() {
        tokio::time::sleep(options.ttft).await;
    }
    let delay = if step.delay.is_zero() { options.chunk_delay } else { step.delay };
    let mut first = true;
    let stamp = name == "stamp";
    let mut output_chars = 0usize;
    let mut tool_index = 0u64;
    for part in step.parts {
        let deltas: Vec<Value> = match part {
            Part::Text(text) if stamp => {
                let _ = text;
                vec![json!({ "content": format!("@{};", now_us()) })]
            }
            Part::Text(text) => {
                output_chars += text.len();
                chunks(&text, 8).into_iter().map(|c| json!({ "content": c })).collect()
            }
            Part::Thinking(text) => {
                output_chars += text.len();
                chunks(&text, 8).into_iter().map(|c| json!({ "reasoning_content": c })).collect()
            }
            Part::Call { name, args } => {
                let id = format!("call_{}", shared.next_call.fetch_add(1, Ordering::Relaxed));
                let index = tool_index;
                tool_index += 1;
                let args = args.to_string();
                output_chars += args.len();
                let mut out = vec![json!({"tool_calls": [{"index": index, "id": id, "type": "function",
                    "function": {"name": name, "arguments": ""}}]})];
                out.extend(
                    chunks(&args, 16)
                        .into_iter()
                        .map(|c| json!({"tool_calls": [{"index": index, "function": {"arguments": c}}]})),
                );
                out
            }
        };
        for mut delta in deltas {
            // The first chunk already waited for the time to first token.
            let pause = if first && !options.ttft.is_zero() { Duration::ZERO } else { delay };
            first = false;
            if !pause.is_zero() {
                tokio::time::sleep(pause).await;
            }
            if stamp {
                // Stamped right before the write, after any pause.
                delta = json!({ "content": format!("@{};", now_us()) });
            }
            let chunk = json!({"id": "mock", "object": "chat.completion.chunk", "model": MODEL_ID,
                "choices": [{"index": 0, "delta": delta, "finish_reason": null}]});
            write_event(stream, &chunk.to_string()).await?;
            shared.stats.chunks.fetch_add(1, Ordering::Relaxed);
        }
    }
    let prompt_tokens = body.len() / 4;
    let completion_tokens = output_chars / 4 + 1;
    let last = json!({"id": "mock", "object": "chat.completion.chunk", "model": MODEL_ID,
        "choices": [{"index": 0, "delta": {}, "finish_reason": step.finish}],
        "usage": {"prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens,
            "total_tokens": prompt_tokens + completion_tokens}});
    write_event(stream, &last.to_string()).await?;
    write_event(stream, "[DONE]").await?;
    stream.write_all(b"0\r\n\r\n").await?;
    stream.flush().await
}

async fn write_event(stream: &mut TcpStream, data: &str) -> io::Result<()> {
    let event = format!("data: {data}\n\n");
    let chunk = format!("{:x}\r\n{event}\r\n", event.len());
    stream.write_all(chunk.as_bytes()).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn directives_pick_the_scenario_and_step() {
        let messages = vec![
            json!({"role": "system", "content": "sys"}),
            json!({"role": "user", "content": "do it [[mock:files]] please"}),
            json!({"role": "assistant", "content": null, "tool_calls": []}),
            json!({"role": "tool", "content": "ok"}),
        ];
        let (name, arg, prompt, step) = directive(&messages);
        assert_eq!((name.as_str(), arg.as_str(), prompt.as_str(), step), ("files", "", "do it  please", 1));
        let messages = vec![json!({"role": "user", "content": [{"type": "text", "text": "[[mock:long:7]]"}]})];
        let (name, arg, _, step) = directive(&messages);
        assert_eq!((name.as_str(), arg.as_str(), step), ("long", "7", 0));
    }

    #[test]
    fn a_bot_notes_in_the_memory_its_prompt_gives_and_recalls_from_it() {
        let system = |memory: &str| json!({"role": "system", "content": format!("You are Her.\n`<memory>` at the end.\n\n<memory path=\"/h/bots/b1/memory/MEMORY.md\">\n{memory}\n</memory>\n")});
        let user = json!({"role": "user", "content": "[[mock:remember:- 新的一条。]] 记住"});
        let sent = |text: &str| ("send_to_user", json!({ "text": text, "end_turn": true }));
        assert_eq!(
            call_of(remember_step(&[system("(empty)"), user.clone()], "- 新的一条。", 0, true)),
            ("write", json!({ "path": "/h/bots/b1/memory/MEMORY.md", "content": "- 新的一条。\n" }))
        );
        assert_eq!(
            call_of(remember_step(&[system("## A\n- 旧的一条。"), user.clone()], "- 新的一条。", 0, true)),
            ("edit", json!({ "path": "/h/bots/b1/memory/MEMORY.md", "edits": [{ "old_text": "- 旧的一条。", "new_text": "- 旧的一条。\n- 新的一条。" }] }))
        );
        assert_eq!(call_of(remember_step(&[system("(empty)"), user.clone()], "- 新的一条。", 1, true)), sent("记下了。"));
        assert_eq!(call_of(remember_step(&[json!({"role": "system", "content": "no memory here"}), user], "- x", 0, true)), sent("no memory"));
        assert_eq!(memory(&[system("## A\n- 旧的一条。")]), Some(("/h/bots/b1/memory/MEMORY.md".into(), "## A\n- 旧的一条。".into())));
        assert_eq!(memory(&[system("(empty)")]).unwrap().1, "");
    }

    #[test]
    fn a_bot_without_a_directive_answers_with_send_to_user() {
        let messages = vec![json!({"role": "user", "content": "hi"})];
        let (name, _, _, step) = directive(&messages);
        assert_eq!((name.as_str(), step), ("text", 0));
        let bot = json!({"tools": [{"type": "function", "function": {"name": "send_to_user"}}]});
        assert!(offers_tool(&bot, "send_to_user"));
        assert!(!offers_tool(&json!({"tools": [{"type": "function", "function": {"name": "read"}}]}), "send_to_user"));
        assert!(!offers_tool(&json!({}), "send_to_user"));
        assert_eq!(call_of(Plan::Step(answer_latest(&messages))), ("send_to_user", json!({"text": "回复：hi", "end_turn": true})));
    }

    fn shared() -> Shared {
        Shared {
            options: MockOptions::default(),
            stats: Stats::default(),
            next_call: AtomicU64::new(1),
            retried: Mutex::new(HashMap::new()),
            search: search::SearchState::default(),
        }
    }

    #[test]
    fn in_a_bot_an_mcp_call_ends_with_its_result_sent() {
        let messages = [
            json!({"role": "user", "content": "[[mock:mcp:vault:Apollo]] go"}),
            json!({"role": "assistant", "content": null}),
            json!({"role": "tool", "content": "mcp__vault__vault_reveal_code"}),
            json!({"role": "assistant", "content": null}),
            json!({"role": "tool", "content": "Release code for Apollo: FX-1"}),
        ];
        let last = |bot| call_of(plan(&shared(), &messages, "mcp", "vault:Apollo", "go", 2, bot));
        assert_eq!(last(false), ("text", json!("mcp done: Release code for Apollo: FX-1")));
        assert_eq!(last(true), ("send_to_user", json!({"text": "mcp done: Release code for Apollo: FX-1", "end_turn": true})));
    }

    #[test]
    fn the_mesh_scenario_calls_the_tool_with_its_json_and_says_how_it_went() {
        let arg = r#"session_start:{"project":"mac/app","prompt":"fix: the test"}"#;
        let mut messages = vec![json!({"role": "user", "content": format!("[[mock:mesh:{arg}]]")})];
        let (name, parsed, _, _) = directive(&messages);
        assert_eq!((name.as_str(), parsed.as_str()), ("mesh", arg));
        let step = |messages: &[Value], step, bot| call_of(plan(&shared(), messages, "mesh", arg, "", step, bot));
        assert_eq!(step(&messages, 0, true), ("tool_search", json!({"server": "mesh"})));
        messages.extend([json!({"role": "assistant", "content": null}), json!({"role": "tool", "content": "Group mesh: …"})]);
        assert_eq!(step(&messages, 1, true), ("call_tool", json!({"name": "session_start", "arguments": {"project": "mac/app", "prompt": "fix: the test"}})));
        // No JSON: no arguments.
        assert_eq!(call_of(plan(&shared(), &messages, "mesh", "project_list", "", 1, true)), ("call_tool", json!({"name": "project_list", "arguments": {}})));
        messages.extend([json!({"role": "assistant", "content": null}), json!({"role": "tool", "content": r#"{"ok":true,"session":"s1"}"#})]);
        assert_eq!(step(&messages, 2, false), ("text", json!(r#"mesh done: {"ok":true,"session":"s1"}"#)));
        assert_eq!(step(&messages, 2, true), ("send_to_user", json!({"text": r#"mesh done: {"ok":true,"session":"s1"}"#, "end_turn": true})));
        for failed in ["The user denied this tool call.", "Tool call denied: The tool is blocked by the rule `mesh`", "The mesh host is not connected, so these tools cannot run now.", r#"{"ok":false,"error":{"kind":"no_grant"}}"#] {
            *messages.last_mut().unwrap() = json!({"role": "tool", "content": failed});
            assert_eq!(step(&messages, 2, true), ("send_to_user", json!({"text": format!("mesh failed: {failed}"), "end_turn": true})));
        }
        // News that wakes the bot afterwards is answered as a message without a directive is.
        messages.extend([json!({"role": "assistant", "content": null}), json!({"role": "user", "content": "<news>built</news>"})]);
        assert_eq!(step(&messages, 3, true), ("send_to_user", json!({"text": "回复：<news>built</news>", "end_turn": true})));
    }

    #[test]
    fn a_bot_passes_on_what_its_routine_found() {
        let arg = "Stretch|in 1m|say:Time to stretch.";
        let mut messages = vec![
            json!({"role": "user", "content": format!("[[mock:routine:{arg}]] remind me")}),
            json!({"role": "assistant", "content": null}),
            json!({"role": "tool", "content": "Saved routine \"Stretch\" (id r_1)."}),
        ];
        let told = |messages: &[Value], step| call_of(plan(&shared(), messages, "routine", arg, "remind me", step, true));
        assert_eq!(told(&messages, 1), ("send_to_user", json!({"text": "转告：Saved routine \"Stretch\" (id r_1).", "end_turn": true})));
        // The report wakes it: the tags and what the kernel says after them stay out.
        messages.push(json!({"role": "assistant", "content": null}));
        messages.push(json!({"role": "user", "content": "<routine_report routine=\"Stretch\" id=\"r_1\" scheduled=\"Sun 2026-11-08 20:00\" \
            ran=\"Sun 2026-11-08 20:00\">\nTime to stretch.\n</routine_report>\nThis routine is the user's own request: pass on what it found."}));
        messages.push(json!({"role": "user", "content": "<environment_update>\nTime: Sun 2026-11-08 20:00\n</environment_update>"}));
        assert_eq!(told(&messages, 2), ("send_to_user", json!({"text": "Time to stretch.", "end_turn": true})));
    }

    #[test]
    fn her_asks_for_a_connector_on_a_card_and_passes_on_how_it_ended() {
        let arg = "tracker|http://127.0.0.1:9/mcp";
        let mut messages = vec![json!({"role": "user", "content": format!("[[mock:connect:{arg}]] connect it")})];
        let step = |messages: &[Value], step| call_of(plan(&shared(), messages, "connect", arg, "connect it", step, true));
        assert_eq!(step(&messages, 0), ("call_tool", json!({"name": "connector_connect", "arguments": {"name": "tracker", "url": "http://127.0.0.1:9/mcp"}})));
        messages.push(json!({"role": "assistant", "content": null}));
        messages.push(json!({"role": "tool", "content": "Nothing changed yet. Card c_k3j9x2m4qa asks the owner to connect tracker (http://127.0.0.1:9/mcp) and sign in."}));
        let (name, args) = step(&messages, 1);
        assert_eq!((name, args["type"].as_str(), args["card"].as_str()), ("send_to_user", Some("card"), Some("c_k3j9x2m4qa")));
        messages.push(json!({"role": "assistant", "content": null}));
        messages.push(json!({"role": "tool", "content": "Shown to the owner."}));
        messages.push(json!({"role": "user", "content": "<card id=\"c_k3j9x2m4qa\" kind=\"connect\" outcome=\"done\">Tracker connected: 3 tools (echo, env, sleep).</card>"}));
        // Done: it tries the connector first, then passes the note on with what it said.
        assert_eq!(step(&messages, 2), ("call_tool", json!({"name": "mcp__tracker__echo", "arguments": {"text": "接上了"}})));
        messages.push(json!({"role": "assistant", "content": null}));
        messages.push(json!({"role": "tool", "content": "接上了"}));
        assert_eq!(step(&messages, 3), ("send_to_user", json!({"text": "转告：Tracker connected: 3 tools (echo, env, sleep).\n接上了", "end_turn": true})));
        // Declined: the note alone.
        messages.push(json!({"role": "assistant", "content": null}));
        messages.push(json!({"role": "user", "content": "<card id=\"c_2\" kind=\"connect\" outcome=\"declined\">The owner declined.</card>"}));
        assert_eq!(step(&messages, 4), ("send_to_user", json!({"text": "转告：The owner declined.", "end_turn": true})));
    }

    fn call_of(plan: Plan) -> (&'static str, Value) {
        match plan {
            Plan::Step(mut step) => match step.parts.pop() {
                // Leaked: a test helper, so the expectations stay string literals.
                Some(Part::Call { name, args }) => (String::leak(name), args),
                Some(Part::Text(text)) => ("text", json!(text)),
                _ => panic!("unexpected part"),
            },
            Plan::Status(..) | Plan::Error(..) => panic!("unexpected status"),
        }
    }

    #[test]
    fn the_skill_scenario_follows_the_catalog_and_the_skill() {
        let system = json!({"role": "system", "content": "...\n<available_skills>\nintro\n\
            <skill name=\"report\" path=\"/u/.agents/skills/report/SKILL.md\">Reports.</skill>\n</available_skills>"});
        let user = json!({"role": "user", "content": "[[mock:skill:report]] go"});
        let mut messages = vec![system.clone(), user.clone()];
        assert_eq!(
            call_of(skill_step(&messages, "report", 0, false)),
            ("read", json!({"path": "/u/.agents/skills/report/SKILL.md"}))
        );
        messages.push(json!({"role": "assistant", "content": null}));
        messages.push(json!({"role": "tool", "content": "Read `references/a.md`, then run `scripts/b.sh`."}));
        assert_eq!(
            call_of(skill_step(&messages, "report", 1, false)),
            ("read", json!({"path": "/u/.agents/skills/report/references/a.md"}))
        );
        assert_eq!(
            call_of(skill_step(&messages, "report", 2, false)),
            ("bash", json!({"command": "sh '/u/.agents/skills/report/scripts/b.sh'"}))
        );
        assert_eq!(call_of(skill_step(&messages, "report", 3, false)), ("text", json!("skill done")));
        assert_eq!(call_of(skill_step(&[system.clone(), user.clone()], "other", 0, false)), ("text", json!("no skill named other")));

        // In a bot's conversation the owner is sent what the skill gave: its last result.
        for result in ["a's notes", "b's report"] {
            messages.push(json!({"role": "assistant", "content": null}));
            messages.push(json!({"role": "tool", "content": result}));
        }
        let sent = |text: &str| ("send_to_user", json!({"text": text, "end_turn": true}));
        assert_eq!(call_of(skill_step(&messages, "report", 3, true)), sent("skill done: b's report"));
        assert_eq!(call_of(skill_step(&[system.clone(), user], "other", 0, true)), sent("no skill named other"));

        let prompt = "<skill name=\"report\">\nRelative paths in this skill are relative to /u/s/report.\n\n\
                      Run `scripts/b.sh`.\n</skill>\n\nthis week";
        assert_eq!(call_of(invoked_step(&[], prompt, 0, false)), ("bash", json!({"command": "sh '/u/s/report/scripts/b.sh'"})));
        assert_eq!(call_of(invoked_step(&[], prompt, 1, false)), ("text", json!("invoked done")));
        let ran = [json!({"role": "user", "content": prompt}), json!({"role": "assistant", "content": null}), json!({"role": "tool", "content": "b's report"})];
        assert_eq!(call_of(invoked_step(&ran, prompt, 1, true)), sent("invoked done: b's report"));

        // Only user messages count: the catalog in the system prompt does not.
        let kept = json!({"role": "user", "content": "kept\n\n<skill name=\"a\">x</skill>\n\n<skill name=\"b\">y</skill>"});
        assert_eq!(skills_in_context(&[system.clone(), kept]), "skills in context: a, b");
        assert_eq!(skills_in_context(&[system]), "skills in context: none");
    }

    #[test]
    fn churn_counts_steps_across_handoffs() {
        let stats = Stats::default();
        let body = |extra: &str| {
            json!({"messages": [{"role": "user", "content": "[[mock:churn:5:7]] go"}, {"role": "assistant", "content": extra}]})
                .to_string()
        };
        let step_of = |plan: Option<Plan>| match plan {
            Some(Plan::Step(step)) => match &step.parts[0] {
                Part::Text(text) => text.clone(),
                _ => String::new(),
            },
            _ => panic!("no plan"),
        };
        assert!(step_of(churn_plan(body("").as_bytes(), &stats)).starts_with("[churn-step 0]"));
        assert!(step_of(churn_plan(body("[churn-step 2] read").as_bytes(), &stats)).starts_with("[churn-step 3]"));
        let handoff = body("[churn-step 3] edit <handoff_request>");
        assert!(step_of(churn_plan(handoff.as_bytes(), &stats)).contains("[churn-step 3]"));
        assert_eq!(stats.churn_handoffs.load(Ordering::Relaxed), 1);
        assert_eq!(step_of(churn_plan(body("[churn-step 4] read").as_bytes(), &stats)), "churn done after 5 steps");
        assert!(churn_plan(br#"{"messages":[{"role":"user","content":"[[mock:echo]] hi"}]}"#, &stats).is_none());
        // An edit only ever follows a write, and targets the line that write added.
        for k in 1..2000 {
            let step = churn_step(7, k);
            if let Part::Call { name, args } = &step.parts[1]
                && name == "edit"
            {
                let previous = churn_step(7, k - 1);
                let Part::Call { name, args: written } = &previous.parts[1] else {
                    panic!("edit at {k} follows no call");
                };
                assert_eq!(name, "write", "edit at {k} follows no write");
                let target = args["edits"][0]["old_text"].as_str().unwrap();
                assert!(written["content"].as_str().unwrap().contains(target));
            }
        }
    }

    #[test]
    fn agent_task_has_seven_requests_and_one_edit_target() {
        let calls = |step| match agent_step("s1", step) {
            Plan::Step(step) => step.parts.iter().filter(|p| matches!(p, Part::Call { .. })).count(),
            Plan::Status(..) | Plan::Error(..) => panic!(),
        };
        assert_eq!((0..7).map(calls).collect::<Vec<_>>(), vec![1, 2, 1, 1, 1, 1, 0]);
        let report = report("s1");
        assert_eq!(report.matches("状态：TODO").count(), 1);
        assert!((3600..4200).contains(&report.len()), "{}", report.len());
        assert!(prose("s1", "总结", 3000).chars().count() >= 3000);
    }
}
