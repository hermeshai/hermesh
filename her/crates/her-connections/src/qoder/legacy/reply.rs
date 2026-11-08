//! A legacy chat's reply: SSE `data:` lines, each an envelope `{statusCodeValue, body}` whose
//! body is an OpenAI chat chunk or `[DONE]`. Tool calls come natively or, from some models, as
//! `<tool_call>` text, which is lifted out for the tools the request declared.
//! Protocol reference: crates/her-connections/licenses/README.md.
use her_llm::{ErrorKind, LlmError, MessageAccumulator, StopReason, StreamEvent, ToolSpec, Usage};
use serde_json::Value;
use std::collections::BTreeMap;

/// One `data:` line at most: a reply's chunks are small, its errors too.
pub(crate) const LINE_LIMIT: usize = 1024 * 1024;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Open {
    None,
    Text(usize),
    Thinking(usize),
}

/// What one data line comes to.
pub(crate) enum Step {
    Events(Vec<StreamEvent>),
    Done,
    /// Qoder refused the call inside the stream: its status and words, for [`failure`].
    Refused(u16, String),
}

pub(crate) struct Reply {
    tools: Vec<String>,
    blocks: usize,
    open: Open,
    /// Upstream tool-call index -> (block index once named, id, arguments held until then).
    native: BTreeMap<u64, Native>,
    text: TextCalls,
    pub(crate) finish: Option<String>,
    pub(crate) usage: Option<Usage>,
    pub(crate) tool_calls: usize,
}

struct Native {
    index: Option<usize>,
    id: String,
    held: String,
}

impl Reply {
    pub(crate) fn new(tools: &[ToolSpec]) -> Self {
        Self {
            tools: tools.iter().map(|t| t.name.clone()).collect(),
            blocks: 0,
            open: Open::None,
            native: BTreeMap::new(),
            text: TextCalls::default(),
            finish: None,
            usage: None,
            tool_calls: 0,
        }
    }

    /// Reads one SSE line.
    pub(crate) fn line(&mut self, line: &str) -> Step {
        let Some(payload) = line.strip_prefix("data:") else {
            return Step::Events(Vec::new());
        };
        let Ok(envelope) = serde_json::from_str::<Value>(payload.trim()) else {
            return Step::Events(Vec::new());
        };
        let inner = envelope["body"].as_str().unwrap_or_default();
        if let Some(status) = envelope["statusCodeValue"].as_u64().filter(|s| *s != 200) {
            let text = if inner.is_empty() {
                payload.trim()
            } else {
                inner
            };
            return Step::Refused(status as u16, text.into());
        }
        if inner.trim() == "[DONE]" {
            return Step::Done;
        }
        let Ok(chunk) = serde_json::from_str::<Value>(inner.trim()) else {
            return Step::Events(Vec::new());
        };
        // A refusal said inside an OK frame.
        if !chunk["choices"].is_array()
            && (!chunk["error"].is_null()
                || (!chunk["code"].is_null() && !chunk["message"].is_null()))
        {
            return Step::Refused(502, inner.into());
        }
        Step::Events(self.chunk(&chunk))
    }

    fn chunk(&mut self, chunk: &Value) -> Vec<StreamEvent> {
        let mut out = Vec::new();
        let choice = &chunk["choices"][0];
        let delta = &choice["delta"];
        if let Some(text) = delta["reasoning_content"]
            .as_str()
            .filter(|t| !t.is_empty())
        {
            let index = self.thinking_block();
            out.push(StreamEvent::ThinkingDelta {
                index,
                text: text.into(),
            });
        }
        if let Some(text) = delta["content"].as_str().filter(|t| !t.is_empty()) {
            for piece in self.text.feed(text, &self.tools) {
                self.piece(piece, &mut out);
            }
        }
        for call in delta["tool_calls"].as_array().into_iter().flatten() {
            self.native_call(call, &mut out);
        }
        if let Some(reason) = choice["finish_reason"].as_str().filter(|r| !r.is_empty()) {
            self.finish = Some(reason.into());
        }
        if chunk["usage"].is_object() {
            self.usage = Some(usage(&chunk["usage"]));
        }
        out
    }

    /// What is left at the end of the reply: text held back as a possible call.
    pub(crate) fn flush(&mut self) -> Vec<StreamEvent> {
        let mut out = Vec::new();
        for piece in self.text.flush() {
            self.piece(piece, &mut out);
        }
        out
    }

    /// Native calls the model never named: their arguments go nowhere.
    pub(crate) fn nameless(&self) -> usize {
        self.native.values().filter(|c| c.index.is_none()).count()
    }

    pub(crate) fn stop(&self) -> StopReason {
        if self.tool_calls > 0 {
            StopReason::ToolUse
        } else if self.finish.as_deref() == Some("length") {
            StopReason::Length
        } else {
            StopReason::Stop
        }
    }

    fn piece(&mut self, piece: Piece, out: &mut Vec<StreamEvent>) {
        match piece {
            Piece::Text(text) if !text.is_empty() => {
                let index = self.text_block();
                out.push(StreamEvent::TextDelta { index, text });
            }
            Piece::Text(_) => {}
            Piece::Call { name, arguments } => {
                let index = self.new_block();
                self.tool_calls += 1;
                out.push(StreamEvent::ToolCallStart {
                    index,
                    id: format!("call_{}", uuid::Uuid::new_v4().simple()),
                    name,
                });
                out.push(StreamEvent::ToolCallArgs {
                    index,
                    json: arguments,
                });
            }
        }
    }

    fn native_call(&mut self, call: &Value, out: &mut Vec<StreamEvent>) {
        let at = call["index"].as_u64().unwrap_or(0);
        let name = call["function"]["name"]
            .as_str()
            .map(str::trim)
            .unwrap_or_default()
            .to_owned();
        let arguments = call["function"]["arguments"]
            .as_str()
            .unwrap_or_default()
            .to_owned();
        let id = call["id"].as_str().unwrap_or_default().to_owned();
        let entry = self.native.entry(at).or_insert(Native {
            index: None,
            id: String::new(),
            held: String::new(),
        });
        if !id.is_empty() {
            entry.id = id;
        }
        if let Some(index) = entry.index {
            if !arguments.is_empty() {
                out.push(StreamEvent::ToolCallArgs {
                    index,
                    json: arguments,
                });
            }
            return;
        }
        // An id or arguments can come before the name; a call is given once it is named.
        entry.held.push_str(&arguments);
        if name.is_empty() {
            return;
        }
        let held = std::mem::take(&mut entry.held);
        let id = if entry.id.is_empty() {
            format!("call_{}", uuid::Uuid::new_v4().simple())
        } else {
            entry.id.clone()
        };
        let mut pending = Vec::new();
        for piece in self.text.flush() {
            self.piece(piece, &mut pending);
        }
        out.extend(pending);
        let index = self.new_block();
        self.tool_calls += 1;
        if let Some(entry) = self.native.get_mut(&at) {
            entry.index = Some(index);
        }
        out.push(StreamEvent::ToolCallStart { index, id, name });
        if !held.is_empty() {
            out.push(StreamEvent::ToolCallArgs { index, json: held });
        }
    }

    fn new_block(&mut self) -> usize {
        self.open = Open::None;
        self.blocks += 1;
        self.blocks - 1
    }

    fn text_block(&mut self) -> usize {
        match self.open {
            Open::Text(index) => index,
            _ => {
                let index = self.new_block();
                self.open = Open::Text(index);
                index
            }
        }
    }

    fn thinking_block(&mut self) -> usize {
        match self.open {
            Open::Thinking(index) => index,
            _ => {
                let index = self.new_block();
                self.open = Open::Thinking(index);
                index
            }
        }
    }
}

fn usage(u: &Value) -> Usage {
    let prompt = u["prompt_tokens"].as_u64().unwrap_or(0);
    let cached = u["prompt_tokens_details"]["cached_tokens"]
        .as_u64()
        .unwrap_or(0);
    Usage {
        input: prompt.saturating_sub(cached),
        output: u["completion_tokens"].as_u64().unwrap_or(0),
        cache_read: cached,
        reasoning: u["completion_tokens_details"]["reasoning_tokens"].as_u64(),
        ..Default::default()
    }
}

/// Applies a finished reply's usage to the message being built.
pub(crate) fn apply_usage(acc: &mut MessageAccumulator, reply: &Reply) {
    if let Some(usage) = &reply.usage {
        acc.usage = usage.clone();
    }
}

enum Piece {
    Text(String),
    Call { name: String, arguments: String },
}

const OPEN: &str = "<tool_call>";
const CLOSE: &str = "</tool_call>";

/// Holds text from a `<tool_call>` opener until its block is whole, then gives it as a call
/// when it names a declared tool; anything else stays text. Openers inside a code fence or an
/// inline code span are prose.
#[derive(Default)]
struct TextCalls {
    held: String,
    /// Inside a code fence, an inline code span, and the backticks given last, not yet read.
    fence: bool,
    inline: bool,
    ticks: usize,
}

impl TextCalls {
    fn feed(&mut self, text: &str, tools: &[String]) -> Vec<Piece> {
        self.held.push_str(text);
        let mut out = Vec::new();
        loop {
            if self.quoted() || tools.is_empty() {
                let rest = std::mem::take(&mut self.held);
                self.give(&rest, &mut out);
                return out;
            }
            match self.held.find(OPEN) {
                Some(start) => {
                    let before = self.held[..start].to_owned();
                    self.give(&before, &mut out);
                    if self.quoted() {
                        continue;
                    }
                    let after = &self.held[start + OPEN.len()..];
                    let Some(end) = after.find(CLOSE) else {
                        self.held.replace_range(..start, "");
                        return out;
                    };
                    let block = after[..end].to_owned();
                    let rest = after[end + CLOSE.len()..].to_owned();
                    match parse_call(&block, tools) {
                        Some((name, arguments)) => out.push(Piece::Call { name, arguments }),
                        None => {
                            let raw = format!("{OPEN}{block}{CLOSE}");
                            self.give(&raw, &mut out);
                        }
                    }
                    self.held = rest;
                }
                None => {
                    // Keep a tail that may still become the opener.
                    let keep = (1..OPEN.len())
                        .rev()
                        .find(|n| self.held.ends_with(&OPEN[..*n]))
                        .unwrap_or(0);
                    let cut = self.held.len() - keep;
                    let given = self.held[..cut].to_owned();
                    self.held.replace_range(..cut, "");
                    self.give(&given, &mut out);
                    return out;
                }
            }
        }
    }

    fn flush(&mut self) -> Vec<Piece> {
        let rest = std::mem::take(&mut self.held);
        let mut out = Vec::new();
        self.give(&rest, &mut out);
        out
    }

    fn give(&mut self, text: &str, out: &mut Vec<Piece>) {
        if text.is_empty() {
            return;
        }
        for c in text.chars() {
            if c == '`' {
                self.ticks += 1;
                continue;
            }
            (self.fence, self.inline) = self.after_ticks();
            self.ticks = 0;
            if c == '\n' {
                self.inline = false;
            }
        }
        out.push(Piece::Text(text.to_owned()));
    }

    /// The code state once the backticks given last are read: three or more open or close a
    /// fence, fewer an inline span outside one.
    fn after_ticks(&self) -> (bool, bool) {
        match self.ticks {
            0 => (self.fence, self.inline),
            n if n >= 3 => (!self.fence, false),
            _ if self.fence => (true, false),
            _ => (false, !self.inline),
        }
    }

    /// The text given so far ends inside code: an opener there is prose about a call.
    fn quoted(&self) -> bool {
        let (fence, inline) = self.after_ticks();
        fence || inline
    }
}

/// A `<tool_call>` block: `{"name", "arguments"}` JSON, or `<function=NAME>` with
/// `<parameter=KEY>VALUE</parameter>` lines. Only declared tools count.
fn parse_call(block: &str, tools: &[String]) -> Option<(String, String)> {
    if let Ok(value) = serde_json::from_str::<Value>(block.trim()) {
        let name = value["name"].as_str()?.trim().to_owned();
        let arguments = match &value["arguments"] {
            Value::Object(_) => value["arguments"].to_string(),
            Value::String(text)
                if serde_json::from_str::<Value>(text).is_ok_and(|v| v.is_object()) =>
            {
                text.clone()
            }
            _ => return None,
        };
        return tools.contains(&name).then_some((name, arguments));
    }
    let start = block.find("<function=")? + "<function=".len();
    let name_end = block[start..].find('>')? + start;
    let name = block[start..name_end].trim().to_owned();
    if !tools.contains(&name) {
        return None;
    }
    let body = &block[name_end + 1..];
    let body = body.split("</function>").next().unwrap_or(body);
    let mut arguments = serde_json::Map::new();
    for part in body.split("<parameter=").skip(1) {
        let (key, rest) = part.split_once('>')?;
        let value = rest.split("</parameter>").next().unwrap_or(rest);
        let value = value.strip_prefix('\n').unwrap_or(value);
        let value = value.strip_suffix('\n').unwrap_or(value);
        let typed = serde_json::from_str::<Value>(value.trim())
            .ok()
            .filter(|v| !v.is_string())
            .unwrap_or_else(|| Value::String(value.to_owned()));
        arguments.insert(key.trim().to_owned(), typed);
    }
    Some((name, Value::Object(arguments).to_string()))
}

const QUEUED: &str = "10605";
/// Codes Qoder's App reads as the plan's usage or credits used up.
const QUOTA: &[&str] = &[
    "110", "112", "113", "114", "115", "116", "117", "118", "119", "122",
];
/// Refusals of this request, not of the account.
const REFUSED: &[&str] = &[
    "406", "416", "430", "80411", "80412", "100400", "100401", "100403",
];
/// How Qoder's App recognises an input over the model's limit.
const TOO_LONG: &[&str] = &[
    "prompt is too long",
    "context_length_exceeded",
    "context exceeds the limit",
    "input is too long",
    "exceeds the limit",
    "token limit",
    "maximum context length",
];

/// A failed legacy call as the kernel's error.
pub(crate) fn failure(status: u16, text: &str) -> LlmError {
    tracing::debug!(status, body = %crate::adapter::scrub(text), "qoder_legacy_failure");
    let status = if (400..=599).contains(&status) {
        status
    } else {
        502
    };
    let mut value = serde_json::from_str::<Value>(text.trim()).unwrap_or(Value::Null);
    // A wrapper can hold the queue's own body as its message.
    if matches!(code_of(&value).as_deref(), None | Some("" | "401" | "403"))
        && let Some(inner) = value["message"]
            .as_str()
            .and_then(|m| serde_json::from_str::<Value>(m.trim()).ok())
        && code_of(&inner).as_deref() == Some(QUEUED)
    {
        value = inner;
    }
    let code = code_of(&value).unwrap_or_default();
    let mut message = value["message"].as_str().unwrap_or(text).trim().to_owned();
    if let Some(detail) = value["details"]
        .as_str()
        .and_then(|d| serde_json::from_str::<Value>(d).ok())
        && let Some(more) = detail["error"]["message"].as_str()
    {
        message = format!("{message}: {more}");
    }
    let queue = queue_of(&value);
    let lower = message.to_lowercase();
    let make = |kind: ErrorKind, code: &str, text: String| {
        let mut error = LlmError::new(kind, text);
        error.status = Some(status);
        error.provider_code = Some(code.into());
        error
    };
    if code == QUEUED
        || queue
            .as_ref()
            .is_some_and(|q| q["isQueued"] == true || q["serviceAvailable"] == false)
    {
        let wait = queue
            .as_ref()
            .and_then(|q| {
                q["retryAfterSeconds"]
                    .as_f64()
                    .or_else(|| q["retryAfterSeconds"].as_str()?.parse().ok())
            })
            .filter(|s| *s > 0.0)
            .unwrap_or(30.0);
        let mut error = make(
            ErrorKind::Overloaded,
            "qoder_queued",
            "Qoder 正在为该模型排队，稍后自动重试".into(),
        );
        error.retry_after_ms = Some((wait * 1000.0).ceil() as u64);
        return error;
    }
    if status == 413
        || lower.contains("request body size exceeds")
        || lower.contains("request_too_large")
    {
        return make(
            ErrorKind::ContextOverflow,
            "request_too_large",
            format!("Qoder 请求体超出上限：{message}"),
        );
    }
    if TOO_LONG.iter().any(|p| lower.contains(p)) {
        return make(
            ErrorKind::ContextOverflow,
            "context_length_exceeded",
            message,
        );
    }
    if QUOTA.contains(&code.as_str())
        || lower.contains("quota exhausted")
        || lower.contains("billing daily count exceeded")
    {
        return make(
            ErrorKind::QuotaExceeded,
            &format!("qoder_{code}"),
            format!("Qoder 额度已用尽：{message}"),
        );
    }
    if lower.contains("session blocked") {
        return make(
            ErrorKind::ContentFiltered,
            "qoder_sensitive_content",
            message,
        );
    }
    if REFUSED.contains(&code.as_str()) {
        return make(
            ErrorKind::InvalidRequest,
            &format!("qoder_{code}"),
            format!("Qoder 拒绝了该请求（{code}）：{message}"),
        );
    }
    if lower.contains("error in upstream response") {
        return make(ErrorKind::Server, "qoder_upstream_failed", message);
    }
    if status == 403 && code == "101" {
        // The envelope's signature: a protocol fault of this adapter, not a lapsed sign-in.
        return make(ErrorKind::Protocol, "qoder_signature_invalid", message);
    }
    if matches!(status, 401 | 403) {
        let mut error = make(
            ErrorKind::Auth,
            if code.is_empty() {
                "auth_rejected"
            } else {
                &code
            },
            message,
        );
        error.status = Some(401);
        return error;
    }
    let mut error = LlmError::from_http(status, text, None, &[]);
    if !code.is_empty() {
        error.provider_code = Some(format!("qoder_{code}"));
    }
    error
}

fn code_of(value: &Value) -> Option<String> {
    match &value["code"] {
        Value::String(code) => Some(code.clone()),
        Value::Number(code) => Some(code.to_string()),
        _ => None,
    }
}

/// The queue state of an error body: under `queue`, or its message as JSON.
fn queue_of(value: &Value) -> Option<Value> {
    if value["queue"].is_object() {
        return Some(value["queue"].clone());
    }
    value["message"]
        .as_str()
        .and_then(|m| serde_json::from_str::<Value>(m.trim()).ok())
        .filter(|q| q.get("isQueued").is_some() || q.get("serviceAvailable").is_some())
}
