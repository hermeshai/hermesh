//! `openai-chat`: Chat Completions and compatible endpoints (DeepSeek, Qwen, Kimi, GLM,
//! Ollama, vLLM, OpenRouter, ...).

use reqwest::header::ACCEPT;
use reqwest::header::CONTENT_TYPE;
use serde::Deserialize;
use serde::Serialize;
use serde::Serializer;
use serde::ser::SerializeMap;
use serde_json::Map;
use serde_json::Value;
use serde_json::json;
use std::borrow::Cow;
use std::collections::HashMap;
use std::fmt;

use crate::api::WireApi;
use crate::api::WireRequest;
use crate::api::http;
use crate::api::http::DisplayStr;
use crate::api::http::JsonBody;
use crate::api::http::SerializeIter;
use crate::api::http::TextLines;
use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::model::MaxTokensField;
use crate::model::Model;
use crate::model::SystemRole;
use crate::model::ThinkingControl;
use crate::model::ThinkingFormat;
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
use crate::types::Usage;
use crate::types::UserContent;

pub const API_ID: &str = "openai-chat";

const REASONING_FIELDS: [&str; 3] = ["reasoning_content", "reasoning", "reasoning_text"];

pub struct OpenAiChatApi;

impl WireApi for OpenAiChatApi {
    fn id(&self) -> &'static str {
        API_ID
    }

    fn stream(&self, req: WireRequest) -> LlmStream { stream(req, false, None) }
}
impl OpenAiChatApi {
    /// Strict terminal and JSON validation for managed services with a verified protocol.
    pub fn stream_strict(req: WireRequest) -> LlmStream { stream(req, true, None) }
    /// A managed service may normalize its own event framing without changing the default protocol.
    pub fn stream_with_events(req: WireRequest, decoder: ChatEventDecoder) -> LlmStream { stream(req, true, Some(decoder)) }
}
pub type ChatEvents = futures::stream::BoxStream<'static, Result<String, LlmError>>;
pub type ChatEventDecoder = fn(reqwest::Response) -> ChatEvents;
fn stream(req: WireRequest, strict: bool, decoder: Option<ChatEventDecoder>) -> LlmStream {
        Box::pin(async_stream::stream! {
            let model = req.model.clone();
            let mut acc = MessageAccumulator::for_model(&model);
            let overflow = model.compat.overflow_patterns();
            let body = build_body(&model, &req.context, &req.options);
            let url = format!("{}/chat/completions", model.base_url.trim_end_matches('/'));
            let mut builder = req
                .http
                .post(&url)
                .header(ACCEPT, "text/event-stream")
                .header(CONTENT_TYPE, "application/json")
                .body(body);
            if let Some(key) = &req.credentials.api_key {
                builder = builder.bearer_auth(key);
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
            use futures::StreamExt;
            let (mut custom, mut events) = if let Some(decode) = decoder {
                (Some(decode(response)), None)
            } else { (None, Some(http::sse_events(response, req.timeouts.idle))) };
            let mut state = ChatStreamState::default();
            loop {
                let next = if let Some(events) = events.as_mut() {
                    events.next().await.map(|event| event.map(|e| e.data))
                } else {
                    match tokio::time::timeout(req.timeouts.idle, custom.as_mut().expect("custom decoder").next()).await {
                        Ok(Some(event)) => event.map(Some),
                        Ok(None) => Ok(None),
                        Err(_) => Err(LlmError::new(ErrorKind::Timeout, "model stream timed out")),
                    }
                };
                let event = match next {
                    Ok(Some(event)) => event,
                    Ok(None) => break,
                    Err(error) => { yield StreamEvent::Failed(acc.fail(error)); return; }
                };
                let data = event.trim();
                if data.is_empty() {
                    continue;
                }
                if data == "[DONE]" {
                    state.done = true;
                    break;
                }
                // The typed parse borrows the text; chunks with unusual field types fall back
                // to a generic parse and are read the same way.
                let wire = serde_json::from_str::<WireChunk>(data);
                let fallback;
                let chunk = match &wire {
                    Ok(wire) => ChunkView::from_wire(wire),
                    Err(_) => {
                        fallback = match serde_json::from_str::<Value>(data) {
                            Ok(value) => value,
                            Err(err) => {
                                if strict { yield StreamEvent::Failed(acc.fail(strict_error("invalid_stream_json"))); return; }
                                tracing::debug!(%err, "skipping unparsable chunk");
                                continue;
                            }
                        };
                        ChunkView::from_value(&fallback)
                    }
                };
                if let Some(error) = chunk.error {
                    yield StreamEvent::Failed(acc.fail(LlmError::from_stream_error(error, &overflow)));
                    return;
                }
                for ev in state.on_chunk(&chunk, &mut acc) {
                    acc.apply(&ev);
                    yield ev;
                }
            }
            if strict && !state.done && state.finish_reason.is_none() {
                yield StreamEvent::Failed(acc.fail(strict_error("missing_terminal_marker")));
                return;
            }
            if let Some(field) = &state.thinking_field {
                acc.set_missing_thinking_signatures(field);
            }
            match state.finish_reason.as_deref() {
                Some("content_filter") => {
                    yield StreamEvent::Failed(acc.fail(LlmError::new(
                        ErrorKind::ContentFiltered,
                        "the response was blocked by the provider's content filter",
                    )));
                    return;
                }
                None if !state.done && !acc.has_content() => {
                    yield StreamEvent::Failed(acc.fail(LlmError::new(
                        ErrorKind::Protocol,
                        "the stream ended before any data arrived",
                    )));
                    return;
                }
                _ => {}
            }
            let stop = match state.finish_reason.as_deref() {
                Some("length") => StopReason::Length,
                Some("tool_calls") | Some("function_call") => StopReason::ToolUse,
                _ => StopReason::Stop,
            };
            let message = acc.finish(stop);
            if strict && message.tool_calls().iter().any(|call| call.id.is_empty() || call.name.is_empty() || call.arguments_error.is_some()) {
                yield StreamEvent::Failed(acc.fail(strict_error("invalid_tool_call")));
                return;
            }
            yield StreamEvent::Done(message);
        })
}

fn strict_error(code: &str) -> LlmError {
    let mut error = LlmError::new(ErrorKind::Protocol, "invalid model stream");
    error.provider_code = Some(code.into());
    error
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum BlockKind {
    Text,
    Thinking,
    Tool,
}

/// Maps chunk deltas to block indexes.
#[derive(Default)]
pub(crate) struct ChatStreamState {
    next_index: usize,
    current: Option<(BlockKind, usize)>,
    tool_blocks: HashMap<u64, usize>,
    pub(crate) thinking_field: Option<String>,
    pub(crate) finish_reason: Option<String>,
    pub(crate) done: bool,
}

impl ChatStreamState {
    fn open(&mut self, kind: BlockKind) -> usize {
        let index = self.next_index;
        self.next_index += 1;
        self.current = Some((kind, index));
        index
    }

    fn block_for(&mut self, kind: BlockKind) -> usize {
        match self.current {
            Some((k, index)) if k == kind => index,
            _ => self.open(kind),
        }
    }

    pub(crate) fn on_chunk(&mut self, chunk: &ChunkView<'_>, acc: &mut MessageAccumulator) -> Vec<StreamEvent> {
        let mut out = Vec::new();
        if acc.response_id.is_none()
            && let Some(id) = chunk.id
        {
            acc.response_id = Some(id.to_string());
        }
        if let Some(usage) = chunk.usage {
            acc.usage = parse_usage(usage);
        }
        if let Some((field, text)) = chunk.reasoning {
            if self.thinking_field.is_none() {
                self.thinking_field = Some(field.to_string());
            }
            let index = self.block_for(BlockKind::Thinking);
            out.push(StreamEvent::ThinkingDelta {
                index,
                text: text.to_string(),
            });
        }

        if let Some(text) = chunk.content.filter(|t| !t.is_empty()) {
            let index = self.block_for(BlockKind::Text);
            out.push(StreamEvent::TextDelta {
                index,
                text: text.to_string(),
            });
        }

        for (position, call) in chunk.calls.iter().enumerate() {
            let key = call.index.unwrap_or(position as u64);
            let args = call.arguments.unwrap_or("");
            let index = match self.tool_blocks.get(&key) {
                Some(index) => *index,
                None => {
                    let index = self.open(BlockKind::Tool);
                    self.tool_blocks.insert(key, index);
                    let id = call
                        .id
                        .filter(|s| !s.is_empty())
                        .map(str::to_string)
                        .unwrap_or_else(|| format!("call_{index}_{:08x}", rand::random::<u32>()));
                    let name = call.name.unwrap_or("").to_string();
                    out.push(StreamEvent::ToolCallStart { index, id, name });
                    index
                }
            };
            if !args.is_empty() {
                out.push(StreamEvent::ToolCallArgs {
                    index,
                    json: args.to_string(),
                });
            }
            if let Some(data) = call.extra {
                out.push(StreamEvent::ToolCallData { index, data: data.clone() });
            }
        }

        if let Some(reason) = chunk.finish_reason {
            self.finish_reason = Some(reason.to_string());
        }
        out
    }
}

/// A streamed chunk as the provider sends it, with only the fields the stream reads. Text
/// borrows from the line unless it has escapes.
#[derive(Deserialize)]
pub(crate) struct WireChunk<'a> {
    #[serde(borrow, default)]
    id: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    choices: Option<Vec<WireChoice<'a>>>,
    #[serde(default)]
    usage: Option<Value>,
    #[serde(default)]
    error: Option<Value>,
}

#[derive(Deserialize)]
struct WireChoice<'a> {
    #[serde(borrow, default)]
    delta: Option<WireDelta<'a>>,
    #[serde(borrow, default)]
    finish_reason: Option<Cow<'a, str>>,
}

#[derive(Deserialize)]
struct WireDelta<'a> {
    #[serde(borrow, default)]
    content: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    reasoning_content: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    reasoning: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    reasoning_text: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    tool_calls: Option<Vec<WireCall<'a>>>,
}

#[derive(Deserialize)]
struct WireCall<'a> {
    #[serde(default)]
    index: Option<u64>,
    #[serde(borrow, default)]
    id: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    function: Option<WireFunction<'a>>,
    #[serde(default)]
    extra_content: Option<Value>,
}

#[derive(Deserialize)]
struct WireFunction<'a> {
    #[serde(borrow, default)]
    name: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    arguments: Option<Cow<'a, str>>,
}

/// What the stream reads from a chunk, whichever way it was parsed.
#[derive(Default)]
pub(crate) struct ChunkView<'a> {
    id: Option<&'a str>,
    usage: Option<&'a Value>,
    error: Option<&'a Value>,
    /// The first non-empty reasoning field: its name and text.
    reasoning: Option<(&'static str, &'a str)>,
    content: Option<&'a str>,
    calls: Vec<CallView<'a>>,
    finish_reason: Option<&'a str>,
}

struct CallView<'a> {
    index: Option<u64>,
    id: Option<&'a str>,
    name: Option<&'a str>,
    arguments: Option<&'a str>,
    extra: Option<&'a Value>,
}

impl<'a> ChunkView<'a> {
    pub(crate) fn from_wire(wire: &'a WireChunk<'a>) -> Self {
        let mut view = ChunkView {
            id: wire.id.as_deref(),
            usage: wire.usage.as_ref().filter(|u| u.is_object()),
            error: wire.error.as_ref().filter(|e| !e.is_null()),
            ..ChunkView::default()
        };
        let Some(choice) = wire.choices.as_ref().and_then(|c| c.first()) else {
            return view;
        };
        view.finish_reason = choice.finish_reason.as_deref();
        let Some(delta) = &choice.delta else {
            return view;
        };
        let fields = [&delta.reasoning_content, &delta.reasoning, &delta.reasoning_text];
        view.reasoning = REASONING_FIELDS
            .iter()
            .zip(fields)
            .find_map(|(name, text)| text.as_deref().filter(|t| !t.is_empty()).map(|t| (*name, t)));
        view.content = delta.content.as_deref();
        view.calls = delta
            .tool_calls
            .iter()
            .flatten()
            .map(|call| CallView {
                index: call.index,
                id: call.id.as_deref(),
                name: call.function.as_ref().and_then(|f| f.name.as_deref()),
                arguments: call.function.as_ref().and_then(|f| f.arguments.as_deref()),
                extra: call.extra_content.as_ref(),
            })
            .collect();
        view
    }

    pub(crate) fn from_value(chunk: &'a Value) -> Self {
        let mut view = ChunkView {
            id: chunk.get("id").and_then(Value::as_str),
            usage: chunk.get("usage").filter(|u| u.is_object()),
            error: chunk.get("error").filter(|e| !e.is_null()),
            ..ChunkView::default()
        };
        let Some(choice) = chunk.get("choices").and_then(|c| c.get(0)) else {
            return view;
        };
        view.finish_reason = choice.get("finish_reason").and_then(Value::as_str);
        let delta = choice.get("delta").unwrap_or(&Value::Null);
        view.reasoning = REASONING_FIELDS.iter().find_map(|field| {
            delta
                .get(field)
                .and_then(Value::as_str)
                .filter(|t| !t.is_empty())
                .map(|t| (*field, t))
        });
        view.content = delta.get("content").and_then(Value::as_str);
        view.calls = delta
            .get("tool_calls")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .map(|call| {
                let function = call.get("function").unwrap_or(&Value::Null);
                CallView {
                    index: call.get("index").and_then(Value::as_u64),
                    id: call.get("id").and_then(Value::as_str),
                    name: function.get("name").and_then(Value::as_str),
                    arguments: function.get("arguments").and_then(Value::as_str),
                    extra: call.get("extra_content"),
                }
            })
            .collect();
        view
    }
}

pub(crate) fn parse_usage(usage: &Value) -> Usage {
    let get = |v: &Value, key: &str| v.get(key).and_then(Value::as_u64);
    let prompt = get(usage, "prompt_tokens").unwrap_or(0);
    let completion = get(usage, "completion_tokens").unwrap_or(0);
    let cached = usage
        .get("prompt_tokens_details")
        .and_then(|d| get(d, "cached_tokens"))
        .or_else(|| get(usage, "prompt_cache_hit_tokens"))
        .unwrap_or(0);
    let reasoning = usage
        .get("completion_tokens_details")
        .and_then(|d| get(d, "reasoning_tokens"));
    Usage {
        input: prompt.saturating_sub(cached),
        output: completion,
        cache_read: cached,
        cache_write: 0,
        reasoning,
        cost: Default::default(),
    }
}

/// The request body; messages are serialized one at a time (see [`JsonBody`]).
pub fn build_body(model: &Model, ctx: &Context, opts: &RequestOptions) -> Vec<u8> {
    let compat = &model.compat;
    let mut messages = JsonBody::new(ctx);
    if !ctx.system.is_empty() {
        let role = match compat.system_role.unwrap_or(SystemRole::System) {
            SystemRole::System => "system",
            SystemRole::Developer => "developer",
        };
        messages.message(&json!({ "role": role, "content": ctx.system }));
    }

    // Images from tool results go into one user message after the run of tool messages,
    // because tool messages must directly follow the assistant message that called them.
    let tool_images = compat.tool_result_images.unwrap_or(false);
    let tool_name = compat.tool_result_name.unwrap_or(false);
    let mut pending_images: Vec<&ToolResultMessage> = Vec::new();
    for message in &ctx.messages {
        if !matches!(message, Message::ToolResult(_)) && !pending_images.is_empty() {
            messages.message(&ImagesMessage(&pending_images));
            pending_images.clear();
        }
        match message {
            Message::User(user) => messages.message(&UserTurn(&user.content)),
            Message::Assistant(assistant) => messages.message(&AssistantTurn {
                message: assistant,
                reasoning: replayed_reasoning(model, assistant),
                same_model: assistant.model == model.id,
            }),
            Message::ToolResult(result) => {
                let images = result.content.iter().any(is_image);
                if images && !tool_images {
                    pending_images.push(result);
                }
                messages.message(&ToolTurn {
                    result,
                    parts: images && tool_images,
                    name: tool_name,
                });
            }
        }
    }
    if !pending_images.is_empty() {
        messages.message(&ImagesMessage(&pending_images));
    }

    let mut body = Map::new();
    body.insert("model".into(), json!(model.id));
    body.insert("stream".into(), json!(true));
    if compat.stream_usage.unwrap_or(true) {
        body.insert("stream_options".into(), json!({ "include_usage": true }));
    }
    // A limit not known is the provider's to apply: a guess here cuts replies short.
    if let Some(max_tokens) = opts.max_output_tokens.or_else(|| model.default_max_output()) {
        let field = match compat.max_tokens_field.unwrap_or(MaxTokensField::MaxCompletionTokens) {
            MaxTokensField::MaxCompletionTokens => "max_completion_tokens",
            MaxTokensField::MaxTokens => "max_tokens",
        };
        body.insert(field.into(), json!(max_tokens));
    }
    if let Some(temperature) = opts.temperature {
        body.insert("temperature".into(), json!(temperature));
    }
    if !ctx.tools.is_empty() && opts.tool_choice == ToolChoice::None {
        body.insert("tool_choice".into(), json!("none"));
    }
    if compat.prompt_cache_key.unwrap_or(false)
        && let Some(key) = &opts.cache_key
    {
        body.insert("prompt_cache_key".into(), json!(key));
    }
    apply_thinking(&mut body, model, opts.thinking);
    for (key, value) in &opts.extra_body {
        body.insert(key.clone(), value.clone());
    }
    // Serialize schemas directly from the context. Building a second Value tree copied
    // every parameter schema for every model step, multiplying burst allocations.
    let tools = SerializeIter::new(ctx.tools.iter().map(|t| ToolDefinition {
        kind: "function",
        function: FunctionDefinition {
            name: &t.name,
            description: &t.description,
            parameters: &t.parameters,
            strict: compat.strict_tools.unwrap_or(false).then_some(true),
        },
    }));
    messages.finish_with(&body, (!ctx.tools.is_empty()).then_some(("tools", &tools)))
}

#[derive(Serialize)]
struct ToolDefinition<'a> {
    #[serde(rename = "type")]
    kind: &'static str,
    function: FunctionDefinition<'a>,
}

#[derive(Serialize)]
struct FunctionDefinition<'a> {
    name: &'a str,
    description: &'a str,
    parameters: &'a Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    strict: Option<bool>,
}

/// Thinking parameters for a level the model offers (docs/kernel/model-catalog-design.md §18.3):
/// `Default` sends nothing. An effort goes out as `reasoning_effort`, whichever channel it is,
/// with the channel's switch only when the model has one; a level of a model that takes only a
/// budget goes out as that budget. `Off` and `On` are the model's `none` effort or the channel's
/// switch.
fn apply_thinking(body: &mut Map<String, Value>, model: &Model, level: ThinkingLevel) {
    let level = model.resolve_thinking(level);
    let format = model.compat.thinking_format;
    if !model.reasoning || level == ThinkingLevel::Default || format == Some(ThinkingFormat::None) {
        return;
    }
    if format == Some(ThinkingFormat::Openrouter) {
        let reasoning = match level {
            ThinkingLevel::On => json!({ "enabled": true }),
            ThinkingLevel::Off => json!({ "effort": "none" }),
            level => json!({ "effort": model.thinking_value(level) }),
        };
        body.insert("reasoning".into(), reasoning);
        return;
    }
    match level {
        ThinkingLevel::Off => match model.off_effort() {
            Some(value) => {
                body.insert("reasoning_effort".into(), json!(value));
            }
            None => apply_switch(body, format, false),
        },
        ThinkingLevel::On => apply_switch(body, format, true),
        level if format == Some(ThinkingFormat::Qwen) && model.thinking_control == ThinkingControl::Budget => {
            apply_switch(body, format, true);
            body.insert("thinking_budget".into(), json!(model.thinking_budget(level)));
        }
        level => {
            // A model that cannot be switched off may refuse being switched on.
            if model.offers(ThinkingLevel::Off) && model.off_effort().is_none() {
                apply_switch(body, format, true);
            }
            body.insert("reasoning_effort".into(), json!(model.thinking_value(level)));
        }
    }
}

/// The channel's way of turning thinking on and off.
fn apply_switch(body: &mut Map<String, Value>, format: Option<ThinkingFormat>, on: bool) {
    match format {
        Some(ThinkingFormat::Deepseek | ThinkingFormat::Zai) => {
            body.insert("thinking".into(), json!({ "type": if on { "enabled" } else { "disabled" } }));
        }
        Some(ThinkingFormat::Qwen) => {
            body.insert("enable_thinking".into(), json!(on));
        }
        Some(ThinkingFormat::Openai) if !on => {
            body.insert("reasoning_effort".into(), json!("none"));
        }
        _ => {}
    }
}

fn is_image(content: &UserContent) -> bool {
    matches!(content, UserContent::Image { .. })
}

/// Reasoning to send back with an assistant message, as (field, text), for providers that
/// want it replayed. Thinking from other models was already removed by the registry.
fn replayed_reasoning<'a>(model: &Model, assistant: &'a AssistantMessage) -> Option<(&'a str, &'a str)> {
    if !model.compat.replay_thinking.unwrap_or(false) {
        return None;
    }
    assistant.content.iter().find_map(|content| match content {
        AssistantContent::Thinking {
            text,
            signature,
            redacted: false,
        } => {
            let field = signature
                .as_deref()
                .filter(|s| REASONING_FIELDS.contains(s))
                .unwrap_or("reasoning_content");
            Some((field, text.as_str()))
        }
        _ => None,
    })
}

// The messages below serialize straight from the context, without copying it.

struct UserTurn<'a>(&'a [UserContent]);

impl Serialize for UserTurn<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(2))?;
        map.serialize_entry("role", "user")?;
        map.serialize_entry("content", &UserContentBody(self.0))?;
        map.end()
    }
}

/// A string when the content is all text, otherwise a list of parts.
struct UserContentBody<'a>(&'a [UserContent]);

impl Serialize for UserContentBody<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        if self.0.iter().any(is_image) {
            serializer.collect_seq(self.0.iter().map(Part::Content))
        } else {
            serializer.collect_str(&TextLines(self.0))
        }
    }
}

enum Part<'a> {
    Content(&'a UserContent),
    /// Introduces the images of a tool result that are sent in a user message.
    ImagesOf(&'a str),
}

impl Serialize for Part<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(2))?;
        match self {
            Part::Content(UserContent::Text { text }) => {
                map.serialize_entry("type", "text")?;
                map.serialize_entry("text", text)?;
            }
            Part::Content(UserContent::Image { mime_type, data }) => {
                map.serialize_entry("type", "image_url")?;
                map.serialize_entry("image_url", &ImageUrl { mime_type, data })?;
            }
            Part::ImagesOf(call_id) => {
                map.serialize_entry("type", "text")?;
                map.serialize_entry("text", &DisplayStr(format_args!("Images returned by tool call {call_id}:")))?;
            }
        }
        map.end()
    }
}

struct ImageUrl<'a> {
    mime_type: &'a str,
    data: &'a str,
}

impl Serialize for ImageUrl<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(1))?;
        let url = format_args!("data:{};base64,{}", self.mime_type, self.data);
        map.serialize_entry("url", &DisplayStr(url))?;
        map.end()
    }
}

struct AssistantTurn<'a> {
    message: &'a AssistantMessage,
    reasoning: Option<(&'a str, &'a str)>,
    /// What the provider attached to its tool calls goes back only to the model that wrote it.
    same_model: bool,
}

impl Serialize for AssistantTurn<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let content = &self.message.content;
        let mut map = serializer.serialize_map(None)?;
        map.serialize_entry("role", "assistant")?;
        if content.iter().any(|c| matches!(c, AssistantContent::Text { text } if !text.is_empty())) {
            map.serialize_entry("content", &DisplayStr(AssistantText(content)))?;
        } else {
            map.serialize_entry("content", &())?;
        }
        if let Some((field, text)) = self.reasoning {
            map.serialize_entry(field, text)?;
        }
        if content.iter().any(|c| matches!(c, AssistantContent::ToolCall(_))) {
            let calls = content.iter().filter_map(|c| match c {
                AssistantContent::ToolCall(call) => Some(FunctionCall(call, self.same_model)),
                _ => None,
            });
            map.serialize_entry("tool_calls", &SerializeIter::new(calls))?;
        }
        map.end()
    }
}

/// The text blocks of an assistant message, concatenated.
struct AssistantText<'a>(&'a [AssistantContent]);

impl fmt::Display for AssistantText<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for content in self.0 {
            if let AssistantContent::Text { text } = content {
                f.write_str(text)?;
            }
        }
        Ok(())
    }
}

struct FunctionCall<'a>(&'a ToolCall, bool);

impl Serialize for FunctionCall<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let call = self.0;
        let mut map = serializer.serialize_map(None)?;
        map.serialize_entry("id", &call.id)?;
        map.serialize_entry("type", "function")?;
        map.serialize_entry("function", &Function(call))?;
        if let Some(data) = call.provider_data.as_ref().filter(|_| self.1) {
            map.serialize_entry("extra_content", data)?;
        }
        map.end()
    }
}

struct Function<'a>(&'a ToolCall);

impl Serialize for Function<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(2))?;
        map.serialize_entry("name", &self.0.name)?;
        // The arguments travel as a string holding JSON.
        map.serialize_entry("arguments", self.0.arguments.as_str())?;
        map.end()
    }
}

struct ToolTurn<'a> {
    result: &'a ToolResultMessage,
    /// Send the content as parts, images included.
    parts: bool,
    name: bool,
}

impl Serialize for ToolTurn<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let result = self.result;
        let mut map = serializer.serialize_map(None)?;
        map.serialize_entry("role", "tool")?;
        map.serialize_entry("tool_call_id", &result.call_id)?;
        let text = TextLines(&result.content);
        if self.parts {
            map.serialize_entry("content", &UserContentBody(&result.content))?;
        } else if text.is_empty() {
            map.serialize_entry("content", "(no output)")?;
        } else {
            map.serialize_entry("content", &DisplayStr(text))?;
        }
        if self.name {
            map.serialize_entry("name", &result.tool_name)?;
        }
        map.end()
    }
}

/// The images of tool results, in one user message.
struct ImagesMessage<'a>(&'a [&'a ToolResultMessage]);

impl Serialize for ImagesMessage<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let parts = self.0.iter().flat_map(|result| {
            std::iter::once(Part::ImagesOf(&result.call_id))
                .chain(result.content.iter().filter(|c| is_image(c)).map(Part::Content))
        });
        let mut map = serializer.serialize_map(Some(2))?;
        map.serialize_entry("role", "user")?;
        map.serialize_entry("content", &SerializeIter::new(parts))?;
        map.end()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Compat;
    use crate::model::test_model;
    use crate::types::ToolCall;
    use crate::types::ToolResultMessage;
    use crate::types::ToolSpec;
    use crate::types::UserMessage;

    fn deepseek_model() -> Model {
        let mut model = test_model(API_ID);
        model.compat = Compat {
            max_tokens_field: Some(MaxTokensField::MaxTokens),
            thinking_format: Some(ThinkingFormat::Deepseek),
            ..Default::default()
        };
        // DeepSeek's own levels, as the catalog gives them.
        model.thinking = vec![ThinkingLevel::Off, ThinkingLevel::Low, ThinkingLevel::High, ThinkingLevel::Max];
        model
    }

    /// Chunks recorded from a real DeepSeek stream (two parallel tool calls with reasoning).
    const RECORDED: &[&str] = &[
        r#"{"id":"r1","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}],"usage":null}"#,
        r#"{"id":"r1","choices":[{"index":0,"delta":{"content":null,"reasoning_content":"Two calls."},"finish_reason":null}],"usage":null}"#,
        r#"{"id":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_00_a","type":"function","function":{"name":"get_time","arguments":""}}]},"finish_reason":null}],"usage":null}"#,
        r#"{"id":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\": \"Paris\"}"}}]},"finish_reason":null}],"usage":null}"#,
        r#"{"id":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_01_b","type":"function","function":{"name":"get_time","arguments":""}}]},"finish_reason":null}],"usage":null}"#,
        r#"{"id":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"city\": \"Tokyo\"}"}}]},"finish_reason":null}],"usage":null}"#,
        r#"{"id":"r1","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":318,"completion_tokens":80,"total_tokens":398,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":14},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":318}}"#,
    ];

    #[test]
    fn parses_recorded_stream() {
        let model = deepseek_model();
        // The generic parse (the fallback) reads every chunk the same way as the typed one.
        let mut generic = ChatStreamState::default();
        let mut generic_acc = MessageAccumulator::for_model(&model);
        let mut generic_events = Vec::new();
        for raw in RECORDED {
            let value: Value = serde_json::from_str(raw).unwrap();
            generic_events.extend(generic.on_chunk(&ChunkView::from_value(&value), &mut generic_acc));
        }
        let mut acc = MessageAccumulator::for_model(&model);
        let mut state = ChatStreamState::default();
        let mut events = Vec::new();
        for raw in RECORDED {
            let wire: WireChunk = serde_json::from_str(raw).unwrap();
            for ev in state.on_chunk(&ChunkView::from_wire(&wire), &mut acc) {
                acc.apply(&ev);
                events.push(ev);
            }
        }
        let differ = |a: &[StreamEvent], b: &[StreamEvent]| {
            a.len() != b.len()
                || a.iter().zip(b).any(|(x, y)| match (x, y) {
                    // Generated ids are random; compare the rest.
                    (StreamEvent::ToolCallStart { index: i, name: n, .. }, StreamEvent::ToolCallStart { index: j, name: m, .. }) => i != j || n != m,
                    _ => format!("{x:?}") != format!("{y:?}"),
                })
        };
        assert!(!differ(&events, &generic_events), "{events:?}\n{generic_events:?}");
        assert_eq!(generic.finish_reason, state.finish_reason);
        acc.set_missing_thinking_signatures(state.thinking_field.as_deref().unwrap());
        assert_eq!(state.finish_reason.as_deref(), Some("tool_calls"));
        let msg = acc.finish(StopReason::ToolUse);
        assert_eq!(msg.response_id.as_deref(), Some("r1"));
        assert_eq!(msg.usage.input, 318);
        assert_eq!(msg.usage.output, 80);
        assert_eq!(msg.usage.reasoning, Some(14));
        let calls = msg.tool_calls();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[0].id, "call_00_a");
        assert_eq!(calls[0].arguments.to_value()["city"], "Paris");
        assert_eq!(calls[1].arguments.to_value()["city"], "Tokyo");
        match &msg.content[0] {
            AssistantContent::Thinking { text, signature, .. } => {
                assert_eq!(text, "Two calls.");
                assert_eq!(signature.as_deref(), Some("reasoning_content"));
            }
            other => panic!("unexpected {other:?}"),
        }
        assert!(matches!(events[0], StreamEvent::ThinkingDelta { index: 0, .. }));
    }

    #[test]
    fn cached_tokens_fall_back_to_deepseek_field() {
        let usage = parse_usage(&json!({"prompt_tokens": 100, "completion_tokens": 5, "prompt_cache_hit_tokens": 60}));
        assert_eq!(usage.input, 40);
        assert_eq!(usage.cache_read, 60);
    }

    #[test]
    fn builds_deepseek_body() {
        let model = deepseek_model();
        let call = ToolCall {
            id: "call_1".into(),
            name: "read".into(),
            arguments: json!({"path": "a.txt"}).into(),
            arguments_error: None,
            provider_data: None,
        };
        let ctx = Context {
            system: "You are helpful.".into(),
            messages: vec![
                Message::from(UserMessage::text("read a.txt")),
                Message::from(AssistantMessage {
                    content: vec![AssistantContent::ToolCall(call)],
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
                Message::from(ToolResultMessage {
                    call_id: "call_1".into(),
                    tool_name: "read".into(),
                    content: vec![UserContent::Text { text: "hello".into() }],
                    is_error: false,
                    details: None,
                    ts: 0,
                    duration_ms: None,
                }),
            ],
            tools: vec![ToolSpec {
                name: "read".into(),
                description: "Read a file".into(),
                parameters: json!({"type": "object", "properties": {}}),
            }],
        };
        let opts = RequestOptions {
            thinking: ThinkingLevel::High,
            ..Default::default()
        };
        let body: Value = serde_json::from_slice(&build_body(&model, &ctx, &opts)).unwrap();
        assert_eq!(body["messages"][0]["role"], "system");
        assert_eq!(body["messages"][1]["content"], "read a.txt");
        assert_eq!(body["messages"][2]["content"], Value::Null);
        assert_eq!(body["messages"][2]["tool_calls"][0]["function"]["arguments"], "{\"path\":\"a.txt\"}");
        assert_eq!(body["messages"][3]["role"], "tool");
        assert_eq!(body["messages"][3]["tool_call_id"], "call_1");
        assert_eq!(body["max_tokens"], 32_000);
        assert!(body.get("max_completion_tokens").is_none());
        assert_eq!(body["thinking"]["type"], "enabled");
        assert_eq!(body["reasoning_effort"], "high");
        assert_eq!(body["stream_options"]["include_usage"], true);
        assert_eq!(body["tools"][0]["function"]["name"], "read");

        // The provider's default: nothing about thinking.
        let default: Value = serde_json::from_slice(&build_body(&model, &ctx, &RequestOptions::default())).unwrap();
        assert!(default.get("thinking").is_none() && default.get("reasoning_effort").is_none());
        let off_opts = RequestOptions { thinking: ThinkingLevel::Off, ..Default::default() };
        let off: Value = serde_json::from_slice(&build_body(&model, &ctx, &off_opts)).unwrap();
        assert_eq!(off["thinking"]["type"], "disabled");
        assert!(off.get("reasoning_effort").is_none());
    }

    #[test]
    fn a_level_goes_out_the_way_the_model_and_its_channel_take_it() {
        use ThinkingLevel::*;
        let catalog = crate::Catalog::builtin().unwrap();
        let directory = crate::directory::ModelDirectory::new(None);
        let thinking = |reference: &str, level: ThinkingLevel| -> Value {
            let (provider, id) = reference.split_once('/').unwrap();
            let model = directory.model(&catalog.providers[provider], id).unwrap_or_else(|| panic!("{reference}"));
            assert!(model.offers(level), "{reference} {level:?}: {:?}", model.thinking);
            let mut body = Map::new();
            apply_thinking(&mut body, &model, level);
            Value::Object(body)
        };
        let cases: &[(&str, ThinkingLevel, Value)] = &[
            // Named efforts, whichever channel: the switch only for a model that has one.
            ("qwen/qwen3.8-max", Xhigh, json!({"enable_thinking": true, "reasoning_effort": "xhigh"})),
            ("qwen/qwen3.8-max", Off, json!({"enable_thinking": false})),
            ("qwen/deepseek-v4.1-flash", High, json!({"enable_thinking": true, "reasoning_effort": "high"})),
            ("qwen/glm-5.3", High, json!({"reasoning_effort": "high"})),
            ("zhipu-coding-plan/glm-5.3", Max, json!({"reasoning_effort": "max"})),
            ("glm/glm-5.2", High, json!({"thinking": {"type": "enabled"}, "reasoning_effort": "high"})),
            ("glm/glm-5.2", Off, json!({"thinking": {"type": "disabled"}})),
            ("deepseek/deepseek-flash", Low, json!({"thinking": {"type": "enabled"}, "reasoning_effort": "low"})),
            ("kimi/kimi-k3", High, json!({"reasoning_effort": "high"})),
            ("kimi/kimi-k2.6", Off, json!({"thinking": {"type": "disabled"}})),
            ("kimi/kimi-k2.6", On, json!({"thinking": {"type": "enabled"}})),
            ("openai/gpt-6-sol", Off, json!({"reasoning_effort": "none"})),
            ("openai/gpt-6-sol", Xhigh, json!({"reasoning_effort": "xhigh"})),
            // A channel whose switch is not known: efforts still go out, `none` is one of them.
            ("tencent-token-plan/hy3", High, json!({"reasoning_effort": "high"})),
            ("tencent-token-plan/hy3", Off, json!({"reasoning_effort": "none"})),
            ("tencent-token-plan/kimi-k3", Max, json!({"reasoning_effort": "max"})),
            // Only a budget: the budget. The highest level is as much as the model takes.
            ("qwen/qwen3.7-max", Low, json!({"enable_thinking": true, "thinking_budget": 4_096})),
            ("qwen/qwen3.7-max", Max, json!({"enable_thinking": true, "thinking_budget": 131_072 - 4_096})),
            ("qwen/qwen3.7-max", Off, json!({"enable_thinking": false})),
            // Everything under `reasoning`.
            ("openrouter/deepseek/deepseek-v4-pro", High, json!({"reasoning": {"effort": "high"}})),
        ];
        for (reference, level, expected) in cases {
            assert_eq!(&thinking(reference, *level), expected, "{reference} {level:?}");
        }
        // Levels that do not exist for a model are not offered at all.
        let levels = |reference: &str| {
            let (provider, id) = reference.split_once('/').unwrap();
            directory.model(&catalog.providers[provider], id).unwrap().thinking.clone()
        };
        assert_eq!(levels("zhipu-coding-plan/glm-5.3"), [Low, High, Max], "it cannot be fully switched off");
        assert_eq!(levels("kimi/kimi-k3"), [Low, High, Max]);
        assert_eq!(levels("kimi/kimi-k2.7-code"), [] as [ThinkingLevel; 0], "`disabled` is an error there");
        assert_eq!(levels("tencent-token-plan/minimax-m3"), [] as [ThinkingLevel; 0], "a bare switch, not known on this channel");
    }

    #[test]
    fn an_output_limit_not_known_is_left_out() {
        let mut model = test_model(API_ID);
        model.max_output_tokens = None;
        let body = |opts: &RequestOptions| -> Value { serde_json::from_slice(&build_body(&model, &Context::default(), opts)).unwrap() };
        let none = body(&RequestOptions::default());
        assert!(none.get("max_tokens").is_none() && none.get("max_completion_tokens").is_none(), "{none}");
        let asked = body(&RequestOptions { max_output_tokens: Some(100), ..Default::default() });
        assert_eq!(asked["max_completion_tokens"], 100, "a request's own limit still applies");
    }

    #[test]
    fn borrowed_tool_schemas_preserve_nested_values_and_explicit_overrides() {
        let mut model = test_model(API_ID);
        model.compat.strict_tools = Some(true);
        let mut ctx = Context { tools: vec![ToolSpec {
            name: "quoted\"tool".into(), description: "多行\n说明".into(),
            parameters: json!({"type":"object","properties":{"x":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["x"]}),
        }], ..Default::default() };
        let mut opts = RequestOptions::default();
        let body: Value = serde_json::from_slice(&build_body(&model, &ctx, &opts)).unwrap();
        assert_eq!(body["tools"][0]["function"]["parameters"], ctx.tools[0].parameters);
        assert_eq!(body["tools"][0]["function"]["description"], ctx.tools[0].description);
        assert_eq!(body["tools"][0]["function"]["strict"], true);
        for replacement in [json!([]), Value::Null, json!([{"custom":"definition"}])] {
            opts.extra_body.insert("tools".into(), replacement.clone());
            let bytes = build_body(&model, &ctx, &opts);
            assert_eq!(String::from_utf8_lossy(&bytes).matches("\"tools\":").count(), 1);
            let body: Value = serde_json::from_slice(&bytes).unwrap();
            assert_eq!(body["tools"], replacement);
        }
        ctx.tools.clear();
        opts.extra_body.clear();
        let body: Value = serde_json::from_slice(&build_body(&model, &ctx, &opts)).unwrap();
        assert!(body.get("tools").is_none());
    }

    #[test]
    fn moves_tool_images_after_tool_messages() {
        let model = test_model(API_ID);
        let image = UserContent::Image {
            mime_type: "image/png".into(),
            data: "AAAA".into(),
        };
        let result = |id: &str, content: Vec<UserContent>| {
            Message::from(ToolResultMessage {
                call_id: id.into(),
                tool_name: "read".into(),
                content,
                is_error: false,
                details: None,
                ts: 0,
                duration_ms: None,
            })
        };
        let ctx = Context {
            system: String::new(),
            messages: vec![
                result("a", vec![UserContent::Text { text: "see image".into() }, image]),
                result("b", vec![UserContent::Text { text: "plain".into() }]),
            ],
            tools: vec![],
        };
        let body: Value = serde_json::from_slice(&build_body(&model, &ctx, &RequestOptions::default())).unwrap();
        let messages = body["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 3);
        assert_eq!(messages[0]["role"], "tool");
        assert_eq!(messages[1]["role"], "tool");
        assert_eq!(messages[2]["role"], "user");
        assert_eq!(messages[2]["content"][1]["type"], "image_url");
    }

    /// Gemini's signature comes with the call and goes back with it, to the same model only.
    #[test]
    fn what_a_provider_attaches_to_a_call_goes_back_to_that_model() {
        let data = json!({"google": {"thought_signature": "sig"}});
        let chunk = json!({"choices": [{"index": 0, "delta": {"tool_calls": [{
            "index": 0, "id": "c1", "type": "function",
            "function": {"name": "read", "arguments": "{}"},
            "extra_content": data,
        }]}}]});
        let view = ChunkView::from_value(&chunk);
        assert_eq!(view.calls[0].extra, Some(&data));
        let text = chunk.to_string();
        let typed: WireChunk = serde_json::from_str(&text).unwrap();
        assert_eq!(ChunkView::from_wire(&typed).calls[0].extra, Some(&data));

        let call = ToolCall {
            id: "c1".into(),
            name: "read".into(),
            arguments: crate::types::JsonText::default(),
            arguments_error: None,
            provider_data: Some(data.clone()),
        };
        let with = serde_json::to_value(FunctionCall(&call, true)).unwrap();
        assert_eq!(with["extra_content"], data);
        let without = serde_json::to_value(FunctionCall(&call, false)).unwrap();
        assert!(without.get("extra_content").is_none(), "{without}");
    }

}
