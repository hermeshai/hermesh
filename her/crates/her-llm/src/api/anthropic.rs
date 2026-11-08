//! `anthropic-messages`: Anthropic Messages and compatible endpoints.

use serde::Serialize;
use serde::Serializer;
use serde::ser::SerializeMap;
use serde_json::Map;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;

use crate::api::WireApi;
use crate::api::WireRequest;
use crate::api::http;
use crate::api::http::JsonBody;
use crate::api::http::SerializeIter;
use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::model::AuthHeader;
use crate::model::Model;
use crate::model::REQUIRED_MAX_OUTPUT_TOKENS;
use crate::model::ThinkingControl;
use crate::stream::LlmStream;
use crate::stream::MessageAccumulator;
use crate::stream::StreamEvent;
use crate::types::AssistantContent;
use crate::types::AssistantMessage;
use crate::types::Context;
use crate::types::Message;
use crate::types::RequestOptions;
use crate::types::StopReason;
use crate::types::ThinkingLevel;
use crate::types::ToolCall;
use crate::types::ToolChoice;
use crate::types::ToolResultMessage;
use crate::types::UserContent;

pub const API_ID: &str = "anthropic-messages";
const API_VERSION: &str = "2023-06-01";

pub struct AnthropicApi;

impl WireApi for AnthropicApi {
    fn id(&self) -> &'static str {
        API_ID
    }

    fn stream(&self, req: WireRequest) -> LlmStream {
        Box::pin(async_stream::stream! {
            let model = req.model.clone();
            let mut acc = MessageAccumulator::for_model(&model);
            let overflow = model.compat.overflow_patterns();
            let body = build_body(&model, &req.context, &req.options);
            let url = format!("{}/v1/messages", model.base_url.trim_end_matches('/'));
            let mut builder = req
                .http
                .post(&url)
                .header("anthropic-version", API_VERSION)
                .header(reqwest::header::ACCEPT, "text/event-stream")
                .header(reqwest::header::CONTENT_TYPE, "application/json")
                .body(body);
            if let Some(key) = &req.credentials.api_key {
                builder = match model.compat.auth_header.unwrap_or(AuthHeader::XApiKey) {
                    AuthHeader::XApiKey => builder.header("x-api-key", key),
                    AuthHeader::Bearer => builder.bearer_auth(key),
                };
            }
            for (name, value) in &req.credentials.headers {
                builder = builder.header(name.as_str(), value.as_str());
            }
            for (name, value) in &model.headers {
                builder = builder.header(name.as_str(), value.as_str());
            }
            let response = match http::send(builder, &req.timeouts, &overflow).await {
                Ok(response) => response,
                Err(err) => {
                    yield StreamEvent::Failed(acc.fail(err));
                    return;
                }
            };
            let mut events = http::sse_events(response, req.timeouts.idle);
            let mut state = AnthropicStreamState::default();
            loop {
                let event = match events.next().await {
                    Ok(Some(event)) => event,
                    Ok(None) => break,
                    Err(err) => {
                        yield StreamEvent::Failed(acc.fail(err));
                        return;
                    }
                };
                let data: Value = match serde_json::from_str(event.data.trim()) {
                    Ok(data) => data,
                    Err(err) => {
                        tracing::debug!(%err, "skipping unparsable event");
                        continue;
                    }
                };
                match state.on_event(&event.event, &data, &mut acc, &overflow) {
                    Step::Events(list) => {
                        for ev in list {
                            acc.apply(&ev);
                            yield ev;
                        }
                    }
                    Step::Stop => break,
                    Step::Fail(err) => {
                        yield StreamEvent::Failed(acc.fail(err));
                        return;
                    }
                }
            }
            if !state.stopped {
                yield StreamEvent::Failed(acc.fail(LlmError::new(
                    ErrorKind::Protocol,
                    "the stream ended before message_stop",
                )));
                return;
            }
            match state.stop_reason.as_deref() {
                Some("refusal") => {
                    yield StreamEvent::Failed(acc.fail(LlmError::new(
                        ErrorKind::ContentFiltered,
                        "the model refused to answer",
                    )));
                }
                Some("max_tokens") => yield StreamEvent::Done(acc.finish(StopReason::Length)),
                Some("tool_use") => yield StreamEvent::Done(acc.finish(StopReason::ToolUse)),
                _ => yield StreamEvent::Done(acc.finish(StopReason::Stop)),
            }
        })
    }
}

pub(crate) enum Step {
    Events(Vec<StreamEvent>),
    Stop,
    Fail(LlmError),
}

#[derive(Default)]
pub(crate) struct AnthropicStreamState {
    blocks: HashMap<u64, usize>,
    next_index: usize,
    pub(crate) stop_reason: Option<String>,
    pub(crate) stopped: bool,
}

impl AnthropicStreamState {
    fn index_for(&mut self, provider_index: u64) -> usize {
        if let Some(index) = self.blocks.get(&provider_index) {
            return *index;
        }
        let index = self.next_index;
        self.next_index += 1;
        self.blocks.insert(provider_index, index);
        index
    }

    pub(crate) fn on_event(
        &mut self,
        event_name: &str,
        data: &Value,
        acc: &mut MessageAccumulator,
        overflow: &[String],
    ) -> Step {
        let kind = data.get("type").and_then(Value::as_str).unwrap_or(event_name);
        let mut out = Vec::new();
        match kind {
            "message_start" => {
                let message = data.get("message").unwrap_or(&Value::Null);
                if let Some(id) = message.get("id").and_then(Value::as_str) {
                    acc.response_id = Some(id.to_string());
                }
                if let Some(usage) = message.get("usage") {
                    merge_usage(acc, usage);
                }
            }
            "content_block_start" => {
                let index = self.index_for(data.get("index").and_then(Value::as_u64).unwrap_or(0));
                let block = data.get("content_block").unwrap_or(&Value::Null);
                match block.get("type").and_then(Value::as_str).unwrap_or("") {
                    "text" => {
                        let text = block.get("text").and_then(Value::as_str).unwrap_or("");
                        if !text.is_empty() {
                            out.push(StreamEvent::TextDelta {
                                index,
                                text: text.to_string(),
                            });
                        }
                    }
                    "thinking" => {
                        acc.ensure_thinking(index);
                        let text = block.get("thinking").and_then(Value::as_str).unwrap_or("");
                        if !text.is_empty() {
                            out.push(StreamEvent::ThinkingDelta {
                                index,
                                text: text.to_string(),
                            });
                        }
                    }
                    "redacted_thinking" => {
                        let data = block.get("data").and_then(Value::as_str).unwrap_or("").to_string();
                        acc.set_redacted_thinking(index, data);
                    }
                    "tool_use" => {
                        let id = block.get("id").and_then(Value::as_str).unwrap_or("").to_string();
                        let name = block.get("name").and_then(Value::as_str).unwrap_or("").to_string();
                        out.push(StreamEvent::ToolCallStart { index, id, name });
                        if let Some(input) = block.get("input").filter(|i| i.as_object().is_some_and(|o| !o.is_empty())) {
                            out.push(StreamEvent::ToolCallArgs {
                                index,
                                json: input.to_string(),
                            });
                        }
                    }
                    _ => {}
                }
            }
            "content_block_delta" => {
                let index = self.index_for(data.get("index").and_then(Value::as_u64).unwrap_or(0));
                let delta = data.get("delta").unwrap_or(&Value::Null);
                let text_of = |key: &str| delta.get(key).and_then(Value::as_str).unwrap_or("").to_string();
                match delta.get("type").and_then(Value::as_str).unwrap_or("") {
                    "text_delta" => out.push(StreamEvent::TextDelta {
                        index,
                        text: text_of("text"),
                    }),
                    "thinking_delta" => out.push(StreamEvent::ThinkingDelta {
                        index,
                        text: text_of("thinking"),
                    }),
                    "signature_delta" => acc.append_thinking_signature(index, &text_of("signature")),
                    "input_json_delta" => {
                        let json = text_of("partial_json");
                        if !json.is_empty() {
                            out.push(StreamEvent::ToolCallArgs { index, json });
                        }
                    }
                    _ => {}
                }
            }
            "message_delta" => {
                if let Some(reason) = data.get("delta").and_then(|d| d.get("stop_reason")).and_then(Value::as_str) {
                    self.stop_reason = Some(reason.to_string());
                }
                if let Some(usage) = data.get("usage") {
                    merge_usage(acc, usage);
                }
            }
            "message_stop" => {
                self.stopped = true;
                return Step::Stop;
            }
            "error" => {
                let error = data.get("error").unwrap_or(data);
                return Step::Fail(LlmError::from_stream_error(error, overflow));
            }
            _ => {}
        }
        Step::Events(out)
    }
}

fn merge_usage(acc: &mut MessageAccumulator, usage: &Value) {
    let get = |key: &str| usage.get(key).and_then(Value::as_u64);
    if let Some(v) = get("input_tokens") {
        acc.usage.input = v;
    }
    if let Some(v) = get("output_tokens") {
        acc.usage.output = v;
    }
    if let Some(v) = get("cache_read_input_tokens") {
        acc.usage.cache_read = v;
    }
    if let Some(v) = get("cache_creation_input_tokens") {
        acc.usage.cache_write = v;
    }
}

fn ephemeral() -> Value {
    json!({ "type": "ephemeral" })
}

/// The request body; messages are serialized straight from the context (see [`JsonBody`]).
pub fn build_body(model: &Model, ctx: &Context, opts: &RequestOptions) -> Vec<u8> {
    let cache = model.compat.cache_control.unwrap_or(true);
    // The last two user messages get a cache breakpoint. Find them up front, so that each
    // message can be written as soon as it is reached.
    let breakpoints = if cache { last_user_turns(&ctx.messages, 2) } else { Vec::new() };
    let mut messages = JsonBody::new(ctx);
    // Consecutive tool results become one user message.
    let mut run: Vec<&ToolResultMessage> = Vec::new();
    let mut run_start = 0;
    for (index, message) in ctx.messages.iter().enumerate() {
        if !matches!(message, Message::ToolResult(_)) && !run.is_empty() {
            messages.message(&UserTurn::Results(&run, breakpoints.contains(&run_start)));
            run.clear();
        }
        match message {
            Message::ToolResult(result) => {
                if run.is_empty() {
                    run_start = index;
                }
                run.push(result);
            }
            Message::User(user) => messages.message(&UserTurn::User(&user.content, breakpoints.contains(&index))),
            Message::Assistant(assistant) => {
                if assistant.content.iter().any(|c| assistant_block(c).is_some()) {
                    messages.message(&AssistantTurn(assistant));
                }
            }
        }
    }
    if !run.is_empty() {
        messages.message(&UserTurn::Results(&run, breakpoints.contains(&run_start)));
    }

    let mut body = Map::new();
    body.insert("model".into(), json!(model.id));
    if !ctx.system.is_empty() {
        let mut system = json!({ "type": "text", "text": ctx.system });
        if cache {
            system["cache_control"] = ephemeral();
        }
        body.insert("system".into(), json!([system]));
    }
    if !ctx.tools.is_empty() {
        let mut tools: Vec<Value> = ctx
            .tools
            .iter()
            .map(|t| json!({ "name": t.name, "description": t.description, "input_schema": t.parameters }))
            .collect();
        if cache && let Some(last) = tools.last_mut() {
            last["cache_control"] = ephemeral();
        }
        body.insert("tools".into(), Value::Array(tools));
        if opts.tool_choice == ToolChoice::None {
            body.insert("tool_choice".into(), json!({ "type": "none" }));
        }
    }
    apply_thinking(&mut body, model, opts);
    body.insert("stream".into(), json!(true));
    for (key, value) in &opts.extra_body {
        body.insert(key.clone(), value.clone());
    }
    messages.finish(&body)
}

/// The output limit and the thinking parameters. A level goes out the way the model takes it: a
/// budget where its row lists one (newer models reject that), else an effort, which needs adaptive
/// thinking to think at all. `Default` sends nothing, and each model does what it does by default.
fn apply_thinking(body: &mut Map<String, Value>, model: &Model, opts: &RequestOptions) {
    let level = if model.reasoning { model.resolve_thinking(opts.thinking) } else { ThinkingLevel::Default };
    let thinks = !matches!(level, ThinkingLevel::Default | ThinkingLevel::Off);
    let effort = level.is_effort().then(|| json!({ "effort": model.thinking_value(level) }));
    if thinks && model.thinking_control != ThinkingControl::Effort {
        let budget = model.thinking_budget(level);
        body.insert("max_tokens".into(), json!(model.budget_max_tokens(budget)));
        body.insert("thinking".into(), json!({ "type": "enabled", "budget_tokens": budget }));
        if let (ThinkingControl::EffortOrBudget, Some(effort)) = (model.thinking_control, effort) {
            body.insert("output_config".into(), effort);
        }
        return;
    }
    let max_tokens = opts.max_output_tokens.or_else(|| model.default_max_output()).unwrap_or(REQUIRED_MAX_OUTPUT_TOKENS);
    body.insert("max_tokens".into(), json!(max_tokens));
    if thinks {
        // Without `display` the reasoning of newer models comes back empty.
        body.insert("thinking".into(), json!({ "type": "adaptive", "display": "summarized" }));
        if let Some(effort) = effort {
            body.insert("output_config".into(), effort);
        }
        return;
    }
    if level == ThinkingLevel::Off {
        body.insert("thinking".into(), json!({ "type": "disabled" }));
    }
    if let Some(temperature) = opts.temperature {
        body.insert("temperature".into(), json!(temperature));
    }
}

fn is_image(content: &UserContent) -> bool {
    matches!(content, UserContent::Image { .. })
}

// The messages below serialize straight from the context, without copying it. The bool is the
// cache breakpoint, set on the last block of the message.

enum UserTurn<'a> {
    User(&'a [UserContent], bool),
    /// Tool results, then the images of the failed ones, which cannot stay in their results.
    Results(&'a [&'a ToolResultMessage], bool),
}

impl Serialize for UserTurn<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(2))?;
        map.serialize_entry("role", "user")?;
        match *self {
            UserTurn::User(content, breakpoint) => {
                let blocks = content.iter().map(Block::Content);
                map.serialize_entry("content", &marked(blocks, content.len(), breakpoint))?;
            }
            UserTurn::Results(run, breakpoint) => {
                let images = run
                    .iter()
                    .filter(|r| r.is_error)
                    .flat_map(|r| r.content.iter().filter(|c| is_image(c)));
                let count = run.len() + images.clone().count();
                let blocks = run.iter().map(|r| Block::ToolResult(r)).chain(images.map(Block::Content));
                map.serialize_entry("content", &marked(blocks, count, breakpoint))?;
            }
        }
        map.end()
    }
}

fn marked<'a>(
    blocks: impl Iterator<Item = Block<'a>>,
    count: usize,
    breakpoint: bool,
) -> SerializeIter<impl Iterator<Item = Marked<'a>>> {
    SerializeIter::new(blocks.enumerate().map(move |(i, block)| Marked {
        block,
        cache: breakpoint && i + 1 == count,
    }))
}

enum Block<'a> {
    Content(&'a UserContent),
    ToolResult(&'a ToolResultMessage),
}

struct Marked<'a> {
    block: Block<'a>,
    cache: bool,
}

impl Serialize for Marked<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(None)?;
        match self.block {
            Block::Content(UserContent::Text { text }) => {
                map.serialize_entry("type", "text")?;
                map.serialize_entry("text", text)?;
            }
            Block::Content(UserContent::Image { mime_type, data }) => {
                map.serialize_entry("type", "image")?;
                map.serialize_entry("source", &ImageSource { mime_type, data })?;
            }
            Block::ToolResult(result) => {
                map.serialize_entry("type", "tool_result")?;
                map.serialize_entry("tool_use_id", &result.call_id)?;
                map.serialize_entry("content", &ResultContent(result))?;
                if result.is_error {
                    map.serialize_entry("is_error", &true)?;
                }
            }
        }
        if self.cache {
            map.serialize_entry("cache_control", &TypeOnly { kind: "ephemeral" })?;
        }
        map.end()
    }
}

struct ImageSource<'a> {
    mime_type: &'a str,
    data: &'a str,
}

impl Serialize for ImageSource<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(3))?;
        map.serialize_entry("type", "base64")?;
        map.serialize_entry("media_type", self.mime_type)?;
        map.serialize_entry("data", self.data)?;
        map.end()
    }
}

#[derive(Serialize)]
struct TypeOnly {
    #[serde(rename = "type")]
    kind: &'static str,
}

#[derive(Serialize)]
struct TextBlock<'a> {
    #[serde(rename = "type")]
    kind: &'static str,
    text: &'a str,
}

/// A tool result's own blocks: failed results lose their images to the enclosing message.
struct ResultContent<'a>(&'a ToolResultMessage);

impl Serialize for ResultContent<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let result = self.0;
        let kept = |c: &&UserContent| !(result.is_error && is_image(c));
        if result.content.iter().any(|c| kept(&c)) {
            let blocks = result.content.iter().filter(kept).map(|c| Marked {
                block: Block::Content(c),
                cache: false,
            });
            serializer.collect_seq(blocks)
        } else {
            serializer.collect_seq([TextBlock {
                kind: "text",
                text: "(no output)",
            }])
        }
    }
}

struct AssistantTurn<'a>(&'a AssistantMessage);

impl Serialize for AssistantTurn<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(2))?;
        map.serialize_entry("role", "assistant")?;
        map.serialize_entry("content", &SerializeIter::new(self.0.content.iter().filter_map(assistant_block)))?;
        map.end()
    }
}

enum AssistantBlock<'a> {
    Thinking { text: &'a str, signature: &'a str },
    RedactedThinking(&'a str),
    Text(&'a str),
    ToolUse(&'a ToolCall),
}

fn assistant_block(content: &AssistantContent) -> Option<AssistantBlock<'_>> {
    Some(match content {
        AssistantContent::Thinking {
            text,
            signature,
            redacted,
        } => {
            // Thinking without a signature cannot be replayed; it is dropped.
            let signature = signature.as_deref()?;
            if *redacted {
                AssistantBlock::RedactedThinking(signature)
            } else {
                AssistantBlock::Thinking { text, signature }
            }
        }
        AssistantContent::Text { text } if text.is_empty() => return None,
        AssistantContent::Text { text } => AssistantBlock::Text(text),
        AssistantContent::ToolCall(call) => AssistantBlock::ToolUse(call),
    })
}

impl Serialize for AssistantBlock<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(None)?;
        match self {
            AssistantBlock::Thinking { text, signature } => {
                map.serialize_entry("type", "thinking")?;
                map.serialize_entry("thinking", text)?;
                map.serialize_entry("signature", signature)?;
            }
            AssistantBlock::RedactedThinking(data) => {
                map.serialize_entry("type", "redacted_thinking")?;
                map.serialize_entry("data", data)?;
            }
            AssistantBlock::Text(text) => {
                map.serialize_entry("type", "text")?;
                map.serialize_entry("text", text)?;
            }
            AssistantBlock::ToolUse(call) => {
                map.serialize_entry("type", "tool_use")?;
                map.serialize_entry("id", &call.id)?;
                map.serialize_entry("name", &call.name)?;
                map.serialize_entry("input", &call.arguments)?;
            }
        }
        map.end()
    }
}

/// Where the last `n` user messages of the request begin: at a user message, or at the first
/// of a run of tool results (which are sent together as one user message).
fn last_user_turns(messages: &[Message], n: usize) -> Vec<usize> {
    let mut found = Vec::new();
    let mut index = messages.len();
    while index > 0 && found.len() < n {
        index -= 1;
        match &messages[index] {
            Message::User(_) => found.push(index),
            Message::ToolResult(_) => {
                while index > 0 && matches!(messages[index - 1], Message::ToolResult(_)) {
                    index -= 1;
                }
                found.push(index);
            }
            Message::Assistant(_) => {}
        }
    }
    found
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::test_model;
    use crate::types::ToolCall;
    use crate::types::ToolResultMessage;
    use crate::types::Usage;
    use crate::types::UserMessage;

    /// Events recorded from DeepSeek's Anthropic-compatible endpoint.
    const RECORDED: &[(&str, &str)] = &[
        ("message_start", r#"{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"deepseek-flash","content":[],"usage":{"input_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}"#),
        ("content_block_start", r#"{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}"#),
        ("content_block_delta", r#"{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Need the tool."}}"#),
        ("content_block_delta", r#"{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}"#),
        ("content_block_stop", r#"{"type":"content_block_stop","index":0}"#),
        ("content_block_start", r#"{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}"#),
        ("content_block_delta", r#"{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking."}}"#),
        ("content_block_start", r#"{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_00_x","name":"get_time","input":{}}}"#),
        ("content_block_delta", r#"{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}"#),
        ("content_block_delta", r#"{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}"#),
        ("ping", r#"{"type":"ping"}"#),
        ("message_delta", r#"{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":300,"output_tokens":45}}"#),
        ("message_stop", r#"{"type":"message_stop"}"#),
    ];

    #[test]
    fn parses_recorded_stream() {
        let model = test_model(API_ID);
        let mut acc = MessageAccumulator::for_model(&model);
        let mut state = AnthropicStreamState::default();
        for (name, raw) in RECORDED {
            let data: Value = serde_json::from_str(raw).unwrap();
            match state.on_event(name, &data, &mut acc, &[]) {
                Step::Events(events) => events.iter().for_each(|ev| acc.apply(ev)),
                Step::Stop => break,
                Step::Fail(err) => panic!("unexpected failure: {err}"),
            }
        }
        assert!(state.stopped);
        assert_eq!(state.stop_reason.as_deref(), Some("tool_use"));
        let msg = acc.finish(StopReason::ToolUse);
        assert_eq!(msg.usage.input, 300);
        assert_eq!(msg.usage.output, 45);
        assert_eq!(msg.content.len(), 3);
        match &msg.content[0] {
            AssistantContent::Thinking { text, signature, .. } => {
                assert_eq!(text, "Need the tool.");
                assert_eq!(signature.as_deref(), Some("sig-1"));
            }
            other => panic!("unexpected {other:?}"),
        }
        assert_eq!(msg.text(), "Checking.");
        assert_eq!(msg.tool_calls()[0].arguments.to_value()["city"], "Paris");
    }

    #[test]
    fn stream_error_is_classified() {
        let model = test_model(API_ID);
        let mut acc = MessageAccumulator::for_model(&model);
        let mut state = AnthropicStreamState::default();
        let data = json!({"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}});
        match state.on_event("error", &data, &mut acc, &[]) {
            Step::Fail(err) => assert_eq!(err.kind, ErrorKind::Overloaded),
            _ => panic!("expected failure"),
        }
    }

    #[test]
    fn builds_body_with_grouped_tool_results() {
        let mut model = test_model(API_ID);
        model.max_output_tokens = Some(64_000);
        model.thinking_control = ThinkingControl::Budget;
        let call = |id: &str| ToolCall {
            id: id.into(),
            name: "read".into(),
            arguments: json!({"path": id}).into(),
            arguments_error: None,
            provider_data: None,
        };
        let result = |id: &str, is_error: bool| {
            Message::from(ToolResultMessage {
                call_id: id.into(),
                tool_name: "read".into(),
                content: vec![UserContent::Text { text: format!("content of {id}") }],
                is_error,
                details: None,
                ts: 0,
                duration_ms: None,
            })
        };
        let ctx = Context {
            system: "sys".into(),
            messages: vec![
                Message::from(UserMessage::text("read both")),
                Message::from(AssistantMessage {
                    content: vec![
                        AssistantContent::Thinking {
                            text: "plan".into(),
                            signature: Some("sig".into()),
                            redacted: false,
                        },
                        AssistantContent::ToolCall(call("a")),
                        AssistantContent::ToolCall(call("b")),
                    ],
                    provider: "test".into(),
                    api: API_ID.into(),
                    model: "test-model".into(),
                    response_id: None,
                    usage: Usage::default(),
                    stop_reason: StopReason::ToolUse,
                    error: None,
                    ts: 0,
                    duration_ms: None,
                }),
                result("a", false),
                result("b", true),
            ],
            tools: vec![],
        };
        let opts = RequestOptions {
            thinking: ThinkingLevel::Low,
            ..Default::default()
        };
        let body: Value = serde_json::from_slice(&build_body(&model, &ctx, &opts)).unwrap();
        let messages = body["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 3);
        assert_eq!(messages[1]["content"][0]["type"], "thinking");
        assert_eq!(messages[1]["content"][1]["type"], "tool_use");
        let results = messages[2]["content"].as_array().unwrap();
        assert_eq!(results.len(), 2);
        assert_eq!(results[1]["is_error"], true);
        assert_eq!(results[1]["cache_control"]["type"], "ephemeral");
        assert_eq!(body["thinking"]["budget_tokens"], 4_096);
        assert_eq!(body["max_tokens"], 4_096 + 16_384);
        assert_eq!(body["system"][0]["cache_control"]["type"], "ephemeral");
    }

    #[test]
    fn a_level_goes_out_the_way_the_model_takes_it() {
        use ThinkingLevel::*;
        let catalog = crate::Catalog::builtin().unwrap();
        let directory = crate::directory::ModelDirectory::new(None);
        let body = |provider: &str, id: &str, level: ThinkingLevel| -> Value {
            let model = directory.model(&catalog.providers[provider], id).unwrap();
            assert!(model.offers(level), "{id} {level:?}: {:?}", model.thinking);
            let opts = RequestOptions { thinking: level, ..RequestOptions::default() };
            serde_json::from_slice(&build_body(&model, &Context::default(), &opts)).unwrap()
        };
        // Effort only: a budget would be refused.
        let opus = body("anthropic", "claude-opus-5-5", Xhigh);
        assert_eq!(opus["thinking"], json!({"type": "adaptive", "display": "summarized"}));
        assert_eq!(opus["output_config"], json!({"effort": "xhigh"}));
        assert_eq!(opus["max_tokens"], 32_000);
        // Effort, and it can be turned off.
        assert_eq!(body("anthropic", "claude-sonnet-5", Off)["thinking"], json!({"type": "disabled"}));
        assert!(body("anthropic", "claude-sonnet-5", Off).get("output_config").is_none());
        assert_eq!(body("anthropic", "claude-sonnet-5", Low)["output_config"], json!({"effort": "low"}));
        // The catalog lists no switch for these, yet they turn off. Sending no effort stays within
        // Opus 5's limit: it takes `disabled` only at `high` or below, and `high` is its default.
        for id in ["claude-opus-4-7", "claude-opus-4-8", "claude-opus-5"] {
            let off = body("anthropic", id, Off);
            assert_eq!(off["thinking"], json!({"type": "disabled"}), "{id}");
            assert!(off.get("output_config").is_none(), "{id}");
            let on = body("anthropic", id, Max);
            assert_eq!(on["thinking"], json!({"type": "adaptive", "display": "summarized"}), "{id}");
            assert_eq!(on["output_config"], json!({"effort": "max"}), "{id}");
            // Correcting the row does not pin the model to the menu, as a model entry would.
            assert!(!directory.models(&catalog.providers["anthropic"]).get(id).unwrap().meta.configured, "{id}");
        }
        // These refuse `disabled` at every effort.
        for id in ["claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5", "claude-fable-5-1"] {
            assert!(!directory.model(&catalog.providers["anthropic"], id).unwrap().offers(Off), "{id}");
        }
        // Both: thinking on older models takes a budget, and the effort goes along.
        let opus_45 = body("anthropic", "claude-opus-4-5", High);
        assert_eq!(opus_45["thinking"], json!({"type": "enabled", "budget_tokens": 24_576}));
        assert_eq!(opus_45["output_config"], json!({"effort": "high"}));
        // Only a budget; no budget, no thinking.
        let haiku = body("anthropic", "claude-haiku-4-5", Low);
        assert_eq!(haiku["thinking"], json!({"type": "enabled", "budget_tokens": 4_096}));
        assert!(haiku.get("output_config").is_none());
        assert_eq!(body("anthropic", "claude-haiku-4-5", Off)["thinking"], json!({"type": "disabled"}));
        // `Default` says nothing, and the same endpoint shape serves DeepSeek's account.
        let plain = body("anthropic", "claude-opus-5-5", Default);
        assert!(plain.get("thinking").is_none() && plain.get("output_config").is_none());
        let deepseek = body("deepseek-anthropic", "deepseek-flash", High);
        assert_eq!(deepseek["output_config"], json!({"effort": "high"}));
        assert_eq!(body("deepseek-anthropic", "deepseek-flash", Off)["thinking"], json!({"type": "disabled"}));
    }

    #[test]
    fn a_required_output_limit_is_filled_in() {
        let mut model = test_model(API_ID);
        model.max_output_tokens = None;
        let body: Value = serde_json::from_slice(&build_body(&model, &Context::default(), &RequestOptions::default())).unwrap();
        assert_eq!(body["max_tokens"], REQUIRED_MAX_OUTPUT_TOKENS);
    }
}
