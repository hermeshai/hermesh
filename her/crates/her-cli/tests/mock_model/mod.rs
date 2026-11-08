//! A simulated model service for end-to-end tests: an OpenAI-compatible chat completions
//! endpoint backed by a small scripted agent instead of a model.
//!
//! - Tokens are counted as 4 bytes of the request JSON each.
//! - A prefix cache is simulated: the longest run of leading messages shared with an earlier
//!   request (with the same model, tools and system prompt) is reported as cached.
//! - With a window set, a request whose prompt and `max_tokens` exceed it fails with the
//!   usual "maximum context length" error.
//! - The agent works through numbered steps; each runs one command with a long output and
//!   leaves a `STEP-DONE-n` marker in its result. It only knows which steps are done from the
//!   markers it can see, and its handoff summary lists them, so the task can only finish if
//!   summaries carry the progress across compactions.

#![allow(dead_code)]

use serde_json::Value;
use serde_json::json;
use std::collections::BTreeSet;
use std::io::BufRead;
use std::io::BufReader;
use std::io::Read;
use std::io::Write;
use std::net::SocketAddr;
use std::net::TcpListener;
use std::net::TcpStream;
use std::sync::Arc;
use std::sync::Mutex;

pub const HANDOFF_TAG: &str = "<handoff_request>";
pub const STEP_MARKER: &str = "STEP-DONE-";
pub const SUMMARY_TITLE: &str = "HANDOFF SUMMARY #";
pub const FINISHED: &str = "ALL STEPS DONE";

#[derive(Clone, Debug)]
pub struct Behavior {
    /// Steps of the scripted task.
    pub steps: u32,
    /// Lines each step command prints after its marker.
    pub lines_per_step: u32,
    /// After this step, the agent reads back the part of a cut output that it did not see.
    pub read_back_after: Option<u32>,
    /// Requests whose prompt plus `max_tokens` exceed this many tokens fail as too long.
    pub window: Option<u64>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    /// A normal turn.
    Turn,
    /// A request for a handoff summary.
    Handoff,
    /// Rejected as too long.
    Rejected,
}

#[derive(Clone, Debug)]
pub struct Recorded {
    pub body: Value,
    pub kind: Kind,
    pub prompt_tokens: u64,
    pub cached_tokens: u64,
    pub completion_tokens: u64,
}

impl Recorded {
    pub fn messages(&self) -> &[Value] {
        self.body["messages"].as_array().map(Vec::as_slice).unwrap_or(&[])
    }
}

#[derive(Clone, Debug)]
pub struct ReadBack {
    pub path: String,
    /// The omitted lines, as the note gave them.
    pub first: u64,
    pub last: u64,
    /// read calls made.
    pub pages: u32,
    /// Lines read back so far.
    lines: Vec<String>,
    /// What the cut result showed around the note.
    head: String,
    tail: String,
    /// What the command printed.
    expected: String,
    pub done: bool,
    /// Head, the lines read back and tail put together are exactly what the command printed.
    pub verified: bool,
}

/// Lines per read call when reading a cut output back.
const READ_PAGE: u64 = 300;

struct State {
    behavior: Behavior,
    requests: Vec<Recorded>,
    /// Earlier requests as cache units: the first holds the model, tools and system prompt,
    /// then one per message.
    seen: Vec<Vec<String>>,
    handoffs: u32,
    read_back: Option<ReadBack>,
}

pub struct MockModel {
    addr: SocketAddr,
    state: Arc<Mutex<State>>,
}

impl MockModel {
    pub fn start(behavior: Behavior) -> MockModel {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let addr = listener.local_addr().expect("address");
        let state = Arc::new(Mutex::new(State {
            behavior,
            requests: Vec::new(),
            seen: Vec::new(),
            handoffs: 0,
            read_back: None,
        }));
        let shared = state.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(stream) = stream else { continue };
                let state = shared.clone();
                std::thread::spawn(move || {
                    let _ = serve(stream, &state);
                });
            }
        });
        MockModel { addr, state }
    }

    /// For `base_url` in models.toml.
    pub fn base_url(&self) -> String {
        format!("http://{}/v1", self.addr)
    }

    pub fn requests(&self) -> Vec<Recorded> {
        self.state.lock().unwrap().requests.clone()
    }

    pub fn read_back(&self) -> Option<ReadBack> {
        self.state.lock().unwrap().read_back.clone()
    }
}

fn serve(mut stream: TcpStream, state: &Mutex<State>) -> std::io::Result<()> {
    let mut reader = BufReader::new(stream.try_clone()?);
    let mut line = String::new();
    reader.read_line(&mut line)?; // request line
    let mut length = 0usize;
    loop {
        line.clear();
        reader.read_line(&mut line)?;
        let header = line.trim_end();
        if header.is_empty() {
            break;
        }
        if let Some((name, value)) = header.split_once(':')
            && name.eq_ignore_ascii_case("content-length")
        {
            length = value.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0u8; length];
    reader.read_exact(&mut body)?;
    let body: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
    let response = state.lock().unwrap().respond(body);
    stream.write_all(response.as_bytes())?;
    stream.flush()
}

/// 4 bytes per token, rounded up.
pub fn tokens(text: &str) -> u64 {
    (text.len() as u64).div_ceil(4)
}

/// The text of a message's content (a string or a list of parts).
pub fn content_text(message: &Value) -> String {
    match &message["content"] {
        Value::String(s) => s.clone(),
        Value::Array(parts) => parts
            .iter()
            .filter_map(|p| p["text"].as_str())
            .collect::<Vec<_>>()
            .join("\n"),
        _ => String::new(),
    }
}

/// Step numbers named by markers in tool results and user messages (summaries included).
pub fn markers(messages: &[Value]) -> BTreeSet<u32> {
    let mut done = BTreeSet::new();
    for message in messages {
        if !matches!(message["role"].as_str(), Some("tool") | Some("user")) {
            continue;
        }
        let text = content_text(message);
        let mut rest = text.as_str();
        while let Some(at) = rest.find(STEP_MARKER) {
            rest = &rest[at + STEP_MARKER.len()..];
            let digits: String = rest.chars().take_while(char::is_ascii_digit).collect();
            if let Ok(n) = digits.parse() {
                done.insert(n);
            }
        }
    }
    done
}

/// Path and offset from a cut tool result's note.
pub fn saved_output(text: &str) -> Option<(String, u64)> {
    let start = text.find("Full output: ")? + "Full output: ".len();
    let rest = &text[start..];
    let end = rest.find(" (read it with offset=")?;
    let path = rest[..end].to_string();
    let offset = rest[end + " (read it with offset=".len()..]
        .chars()
        .take_while(char::is_ascii_digit)
        .collect::<String>()
        .parse()
        .ok()?;
    Some((path, offset))
}

enum Reply {
    Text(String),
    Tool { name: &'static str, args: Value },
}

impl State {
    fn respond(&mut self, body: Value) -> String {
        let messages = body["messages"].as_array().cloned().unwrap_or_default();
        let units = units(&body, &messages);
        let prompt_tokens: u64 = units.iter().map(|u| tokens(u)).sum();
        let max_tokens = body["max_tokens"]
            .as_u64()
            .or_else(|| body["max_completion_tokens"].as_u64())
            .unwrap_or(0);
        if let Some(window) = self.behavior.window
            && prompt_tokens + max_tokens > window
        {
            self.requests.push(Recorded {
                body,
                kind: Kind::Rejected,
                prompt_tokens,
                cached_tokens: 0,
                completion_tokens: 0,
            });
            let message = format!(
                "This model's maximum context length is {window} tokens. However, you requested {} tokens \
                 ({prompt_tokens} in the messages, {max_tokens} in the completion). Please reduce the length \
                 of the messages.",
                prompt_tokens + max_tokens
            );
            let error = json!({"error": {"message": message, "type": "invalid_request_error", "code": "context_length_exceeded"}});
            return http(400, "application/json", &error.to_string());
        }
        let cached_tokens = self
            .seen
            .iter()
            .map(|earlier| {
                earlier
                    .iter()
                    .zip(&units)
                    .take_while(|(a, b)| a == b)
                    .map(|(a, _)| tokens(a))
                    .sum::<u64>()
            })
            .max()
            .unwrap_or(0);
        self.seen.push(units);
        let handoff = messages
            .last()
            .is_some_and(|m| content_text(m).contains(HANDOFF_TAG));
        let (kind, reply) = if handoff {
            self.handoffs += 1;
            (Kind::Handoff, Reply::Text(self.summary(&messages)))
        } else {
            (Kind::Turn, self.next_action(&messages))
        };
        let id = format!("mock-{}", self.requests.len() + 1);
        let (stream, completion_tokens) = sse(&id, &reply, prompt_tokens, cached_tokens);
        self.requests.push(Recorded {
            body,
            kind,
            prompt_tokens,
            cached_tokens,
            completion_tokens,
        });
        http(200, "text/event-stream", &stream)
    }

    fn summary(&self, messages: &[Value]) -> String {
        let done = markers(messages);
        let list: Vec<String> = done.iter().map(|n| format!("{STEP_MARKER}{n}")).collect();
        let next = (1..).find(|n| !done.contains(n)).unwrap_or(1);
        let read_back = match &self.read_back {
            Some(r) if r.verified => format!("verified ({} lines {}-{})", r.path, r.first, r.last),
            Some(_) => "pending".to_string(),
            None => "not done yet".to_string(),
        };
        format!(
            "{SUMMARY_TITLE}{}\nGoal: run every step of the job, one command per step.\nCompleted: {}\n\
             Read-back: {read_back}\nNext: step {next}.",
            self.handoffs,
            if list.is_empty() { "none".to_string() } else { list.join(", ") }
        )
    }

    fn next_action(&mut self, messages: &[Value]) -> Reply {
        let last = messages.last().cloned().unwrap_or(Value::Null);
        let last_is_tool = last["role"] == "tool";
        let last_text = content_text(&last);
        // Reading a cut output back, page by page, following read's own hints.
        if let Some(read_back) = &mut self.read_back
            && !read_back.done
            && last_is_tool
        {
            let page = last_text.split("\n\n[Showing lines").next().unwrap_or_default();
            read_back.lines.extend(page.lines().map(str::to_string));
            let next = last_text
                .split("Use offset=")
                .nth(1)
                .and_then(|rest| rest.chars().take_while(char::is_ascii_digit).collect::<String>().parse::<u64>().ok());
            if let Some(next) = next.filter(|n| *n <= read_back.last) {
                read_back.pages += 1;
                return Reply::Tool {
                    name: "read",
                    args: json!({"path": read_back.path, "offset": next, "limit": READ_PAGE}),
                };
            }
            let wanted = (read_back.last - read_back.first + 1) as usize;
            let middle: String = read_back.lines.iter().take(wanted).map(|l| format!("{l}\n")).collect();
            read_back.verified = read_back.lines.len() >= wanted
                && format!("{}{middle}{}", read_back.head, read_back.tail) == read_back.expected;
            read_back.done = true;
        }
        let done = markers(messages);
        if let Some(after) = self.behavior.read_back_after
            && self.read_back.is_none()
            && done.contains(&after)
            && last_is_tool
            && let Some((path, first)) = saved_output(&last_text)
            && let Some(read_back) = self.start_read_back(&last_text, path, first)
        {
            let args = json!({"path": read_back.path, "offset": first, "limit": READ_PAGE});
            self.read_back = Some(read_back);
            return Reply::Tool { name: "read", args };
        }
        let next = (1..).find(|n| !done.contains(n)).unwrap_or(1);
        if next > self.behavior.steps {
            return Reply::Text(format!("{FINISHED}: {}", self.behavior.steps));
        }
        Reply::Tool {
            name: "bash",
            args: json!({"command": format!("echo {STEP_MARKER}{next} && seq 1 {}", self.behavior.lines_per_step)}),
        }
    }

    /// Notes what a cut step output showed, to check the read-back against it later.
    fn start_read_back(&self, result: &str, path: String, first: u64) -> Option<ReadBack> {
        let body = &result[result.find("Output:\n")? + "Output:\n".len()..];
        let note_at = body.find("[Lines ")?;
        let note_end = note_at + body[note_at..].find("]\n")? + 2;
        let range = &body[note_at + "[Lines ".len()..];
        let last = range
            .split('-')
            .nth(1)?
            .chars()
            .take_while(char::is_ascii_digit)
            .collect::<String>()
            .parse()
            .ok()?;
        let step: u32 = body
            .strip_prefix(STEP_MARKER)?
            .chars()
            .take_while(char::is_ascii_digit)
            .collect::<String>()
            .parse()
            .ok()?;
        let numbers: String = (1..=self.behavior.lines_per_step).map(|n| format!("{n}\n")).collect();
        Some(ReadBack {
            path,
            first,
            last,
            pages: 1,
            lines: Vec::new(),
            head: body[..note_at].to_string(),
            tail: body[note_end..].to_string(),
            expected: format!("{STEP_MARKER}{step}\n{numbers}"),
            done: false,
            verified: false,
        })
    }
}

/// The request as cache units: model, tools and system prompt first, then each message.
fn units(body: &Value, messages: &[Value]) -> Vec<String> {
    let (system, rest) = match messages.first() {
        Some(first) if first["role"] == "system" => (first.clone(), &messages[1..]),
        _ => (Value::Null, messages),
    };
    let mut units = vec![json!({"model": body["model"], "tools": body["tools"], "system": system}).to_string()];
    units.extend(rest.iter().map(Value::to_string));
    units
}

/// The reply as an OpenAI-style event stream, and its completion tokens.
fn sse(id: &str, reply: &Reply, prompt_tokens: u64, cached_tokens: u64) -> (String, u64) {
    let mut events = Vec::new();
    let (completion_tokens, finish) = match reply {
        Reply::Text(text) => {
            events.push(json!({"id": id, "choices": [{"index": 0, "delta": {"role": "assistant", "content": text}, "finish_reason": null}]}));
            (tokens(text) + 1, "stop")
        }
        Reply::Tool { name, args } => {
            let arguments = args.to_string();
            events.push(json!({"id": id, "choices": [{"index": 0, "delta": {"role": "assistant", "tool_calls": [
                {"index": 0, "id": format!("call_{id}"), "type": "function", "function": {"name": name, "arguments": arguments}}
            ]}, "finish_reason": null}]}));
            (tokens(&arguments) + 8, "tool_calls")
        }
    };
    events.push(json!({"id": id, "choices": [{"index": 0, "delta": {}, "finish_reason": finish}], "usage": {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": prompt_tokens + completion_tokens,
        "prompt_tokens_details": {"cached_tokens": cached_tokens}
    }}));
    let mut body = String::new();
    for event in events {
        body.push_str(&format!("data: {event}\n\n"));
    }
    body.push_str("data: [DONE]\n\n");
    (body, completion_tokens)
}

fn http(status: u16, content_type: &str, body: &str) -> String {
    let reason = if status == 200 { "OK" } else { "Bad Request" };
    format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
}
