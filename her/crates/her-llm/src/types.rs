//! Neutral message model shared by every wire protocol.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::fmt;
use std::str::FromStr;
use std::sync::Arc;

use crate::error::LlmError;

/// Unix time in milliseconds.
pub type Timestamp = i64;

pub fn now_ms() -> Timestamp {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// A message of the conversation. The parts are shared: the session history and every
/// request built from it point at the same data, so cloning a message is cheap.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "role", rename_all = "snake_case")]
pub enum Message {
    User(Arc<UserMessage>),
    Assistant(Arc<AssistantMessage>),
    ToolResult(Arc<ToolResultMessage>),
}

impl From<UserMessage> for Message {
    fn from(message: UserMessage) -> Self {
        Message::User(Arc::new(message))
    }
}

impl From<AssistantMessage> for Message {
    fn from(message: AssistantMessage) -> Self {
        Message::Assistant(Arc::new(message))
    }
}

impl From<ToolResultMessage> for Message {
    fn from(message: ToolResultMessage) -> Self {
        Message::ToolResult(Arc::new(message))
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct UserMessage {
    pub content: Vec<UserContent>,
    #[serde(default)]
    pub ts: Timestamp,
}

impl UserMessage {
    pub fn text(text: impl Into<String>) -> Self {
        Self {
            content: vec![UserContent::Text { text: text.into() }],
            ts: now_ms(),
        }
    }

    /// Concatenated text blocks, for display.
    pub fn text_content(&self) -> String {
        join_text(&self.content)
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum UserContent {
    Text { text: String },
    /// `data` is base64 encoded.
    Image { mime_type: String, data: String },
}

pub(crate) fn join_text(content: &[UserContent]) -> String {
    let parts: Vec<&str> = content
        .iter()
        .filter_map(|c| match c {
            UserContent::Text { text } => Some(text.as_str()),
            UserContent::Image { .. } => None,
        })
        .collect();
    parts.join("\n")
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct AssistantMessage {
    pub content: Vec<AssistantContent>,
    pub provider: String,
    pub api: String,
    pub model: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub response_id: Option<String>,
    #[serde(default)]
    pub usage: Usage,
    pub stop_reason: StopReason,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    // Boxed: set on failed replies only, and the history holds every reply.
    pub error: Option<Box<LlmError>>,
    #[serde(default)]
    pub ts: Timestamp,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duration_ms: Option<u64>,
}

impl AssistantMessage {
    pub fn tool_calls(&self) -> Vec<&ToolCall> {
        self.content
            .iter()
            .filter_map(|c| match c {
                AssistantContent::ToolCall(call) => Some(call),
                _ => None,
            })
            .collect()
    }

    /// Concatenated text blocks (thinking excluded).
    pub fn text(&self) -> String {
        let parts: Vec<&str> = self
            .content
            .iter()
            .filter_map(|c| match c {
                AssistantContent::Text { text } => Some(text.as_str()),
                _ => None,
            })
            .collect();
        parts.join("")
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum AssistantContent {
    Text {
        text: String,
    },
    Thinking {
        text: String,
        /// Provider replay data (signature, encrypted reasoning or the source field name).
        /// Only valid for the model that produced it.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        signature: Option<String>,
        /// The provider withheld the reasoning; `text` is empty and the raw data is in `signature`.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        redacted: bool,
    },
    ToolCall(ToolCall),
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    /// Always a JSON object.
    #[schemars(with = "Value")]
    pub arguments: JsonText,
    /// Set when the model's arguments were not a valid JSON object; `arguments` is then `{}`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub arguments_error: Option<String>,
    /// What the provider attached to the call and wants back with it on later turns, untouched:
    /// Gemini's signature of its reasoning, for one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub provider_data: Option<Value>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ToolResultMessage {
    pub call_id: String,
    pub tool_name: String,
    /// Sent to the model.
    pub content: Vec<UserContent>,
    pub is_error: bool,
    /// For user interfaces and logs; never sent to the model.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schemars(with = "Option<Value>")]
    pub details: Option<JsonText>,
    #[serde(default)]
    pub ts: Timestamp,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duration_ms: Option<u64>,
}

impl ToolResultMessage {
    pub fn text_content(&self) -> String {
        join_text(&self.content)
    }
}

/// A JSON value kept as its compact text.
///
/// Tool call arguments and tool details stay in the history for the whole session but are
/// read once or twice. As a tree, even an object with one key takes a map node of about 640
/// bytes; as text it takes its length. The text is what serializing the value gives (compact,
/// keys in order), so it is written exactly as the value was, and reading it back gives the
/// same value.
#[derive(Clone)]
pub struct JsonText(Box<serde_json::value::RawValue>);

impl JsonText {
    pub fn from_value(value: &Value) -> Self {
        let text = serde_json::to_string(value).unwrap_or_else(|_| "null".to_string());
        Self(serde_json::value::RawValue::from_string(text).expect("serde_json writes valid JSON"))
    }

    /// `{}`.
    pub fn empty_object() -> Self {
        Self::from_value(&Value::Object(serde_json::Map::new()))
    }

    pub fn as_str(&self) -> &str {
        self.0.get()
    }

    /// The value; the text is always valid JSON.
    pub fn to_value(&self) -> Value {
        serde_json::from_str(self.0.get()).unwrap_or(Value::Null)
    }
}

impl From<Value> for JsonText {
    fn from(value: Value) -> Self {
        Self::from_value(&value)
    }
}

impl Default for JsonText {
    fn default() -> Self {
        Self::empty_object()
    }
}

impl PartialEq for JsonText {
    fn eq(&self, other: &Self) -> bool {
        self.as_str() == other.as_str()
    }
}

impl fmt::Debug for JsonText {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The compact text, as `Value` displays.
impl fmt::Display for JsonText {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl Serialize for JsonText {
    fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.0.serialize(serializer)
    }
}

/// Read as a value and written again, so that text from elsewhere (spaces, another key
/// order) ends up as the value would have.
impl<'de> Deserialize<'de> for JsonText {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        Value::deserialize(deserializer).map(|value| Self::from_value(&value))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum StopReason {
    Stop,
    Length,
    ToolUse,
    Error,
    Aborted,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "UPPERCASE")]
pub enum Currency {
    #[default]
    Usd,
    Cny,
}

impl Currency {
    pub fn symbol(self) -> &'static str {
        match self {
            Currency::Usd => "$",
            Currency::Cny => "¥",
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Cost {
    #[serde(default)]
    pub currency: Currency,
    #[serde(default)]
    pub input: f64,
    #[serde(default)]
    pub output: f64,
    #[serde(default)]
    pub cache_read: f64,
    #[serde(default)]
    pub cache_write: f64,
    #[serde(default)]
    pub total: f64,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Usage {
    /// Input tokens that did not hit the cache.
    #[serde(default)]
    pub input: u64,
    /// Output tokens, reasoning included.
    #[serde(default)]
    pub output: u64,
    #[serde(default)]
    pub cache_read: u64,
    #[serde(default)]
    pub cache_write: u64,
    /// The reasoning part of `output`, when the provider reports it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reasoning: Option<u64>,
    #[serde(default)]
    pub cost: Cost,
}

impl Usage {
    pub fn total_tokens(&self) -> u64 {
        self.input + self.output + self.cache_read + self.cache_write
    }

    /// Size of the context after this response.
    pub fn context_tokens(&self) -> u64 {
        self.total_tokens()
    }

    /// Input tokens of the request, cached or not.
    pub fn prompt_tokens(&self) -> u64 {
        self.input + self.cache_read + self.cache_write
    }

    pub fn is_empty(&self) -> bool {
        self.total_tokens() == 0
    }

    pub fn add(&mut self, other: &Usage) {
        self.input += other.input;
        self.output += other.output;
        self.cache_read += other.cache_read;
        self.cache_write += other.cache_write;
        self.reasoning = match (self.reasoning, other.reasoning) {
            (None, None) => None,
            (a, b) => Some(a.unwrap_or(0) + b.unwrap_or(0)),
        };
        self.cost.currency = other.cost.currency;
        self.cost.input += other.cost.input;
        self.cost.output += other.cost.output;
        self.cost.cache_read += other.cost.cache_read;
        self.cost.cache_write += other.cost.cache_write;
        self.cost.total += other.cost.total;
    }
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Context {
    pub system: String,
    pub messages: Vec<Message>,
    pub tools: Vec<ToolSpec>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ToolSpec {
    pub name: String,
    pub description: String,
    /// JSON Schema with `type: object`.
    pub parameters: Value,
}

/// How hard a model thinks. `Default` sends nothing and leaves it to the provider; the others
/// are the providers' own levels, and a model offers only those it supports
/// (docs/kernel/model-catalog-design.md §18).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ThinkingLevel {
    #[default]
    Default,
    Off,
    /// Reasoning switched on, for models with a switch and no levels.
    On,
    Minimal,
    Low,
    Medium,
    High,
    Xhigh,
    Max,
}

impl ThinkingLevel {
    pub const ALL: [ThinkingLevel; 9] = [
        ThinkingLevel::Default,
        ThinkingLevel::Off,
        ThinkingLevel::On,
        ThinkingLevel::Minimal,
        ThinkingLevel::Low,
        ThinkingLevel::Medium,
        ThinkingLevel::High,
        ThinkingLevel::Xhigh,
        ThinkingLevel::Max,
    ];

    /// The effort levels, weakest first.
    pub const EFFORTS: [ThinkingLevel; 6] = [
        ThinkingLevel::Minimal,
        ThinkingLevel::Low,
        ThinkingLevel::Medium,
        ThinkingLevel::High,
        ThinkingLevel::Xhigh,
        ThinkingLevel::Max,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            ThinkingLevel::Default => "default",
            ThinkingLevel::Off => "off",
            ThinkingLevel::On => "on",
            ThinkingLevel::Minimal => "minimal",
            ThinkingLevel::Low => "low",
            ThinkingLevel::Medium => "medium",
            ThinkingLevel::High => "high",
            ThinkingLevel::Xhigh => "xhigh",
            ThinkingLevel::Max => "max",
        }
    }

    pub fn is_effort(self) -> bool {
        Self::EFFORTS.contains(&self)
    }
}

impl fmt::Display for ThinkingLevel {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl FromStr for ThinkingLevel {
    type Err = String;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        ThinkingLevel::ALL
            .into_iter()
            .find(|level| level.as_str() == s)
            .ok_or_else(|| {
                format!("invalid thinking level '{s}' (expected default, off, on, minimal, low, medium, high, xhigh or max)")
            })
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolChoice {
    #[default]
    Auto,
    None,
}

#[derive(Clone, Debug, Default)]
pub struct RequestOptions {
    pub thinking: ThinkingLevel,
    /// Defaults to min(model limit, 32000).
    pub max_output_tokens: Option<u32>,
    pub temperature: Option<f32>,
    pub tool_choice: ToolChoice,
    /// Usually the session id, for providers that support a cache key.
    pub cache_key: Option<String>,
    /// Merged into the request body as is.
    pub extra_body: serde_json::Map<String, Value>,
}

#[cfg(test)]
mod json_text_tests {
    use super::*;
    use serde_json::json;

    fn call(arguments: JsonText) -> ToolCall {
        ToolCall {
            id: "c1".into(),
            name: "write".into(),
            arguments,
            arguments_error: None,
            provider_data: None,
        }
    }

    #[test]
    fn json_text_is_written_as_its_value() {
        let value = json!({"path": "a.txt", "content": "状态\n\"x\"\t", "n": [1, -2, 2.5, 1e300, null, true], "z": {}, "a": {"b": []}});
        let text = JsonText::from_value(&value);
        assert_eq!(text.as_str(), serde_json::to_string(&value).unwrap());
        assert_eq!(text.to_value(), value);
        assert_eq!(text.to_string(), value.to_string());
        // Inside a message, as JSON text and as a value, the same as the call holding a value.
        #[derive(Serialize)]
        struct Before {
            id: String,
            name: String,
            arguments: Value,
            #[serde(skip_serializing_if = "Option::is_none")]
            arguments_error: Option<String>,
        }
        let before = Before {
            id: "c1".into(),
            name: "write".into(),
            arguments: value,
            arguments_error: None,
        };
        let written = serde_json::to_string(&before).unwrap();
        assert_eq!(serde_json::to_string(&call(text.clone())).unwrap(), written);
        assert_eq!(serde_json::to_value(call(text.clone())).unwrap(), serde_json::to_value(&before).unwrap());
        let back: ToolCall = serde_json::from_str(&written).unwrap();
        assert_eq!(back, call(text));
    }

    #[test]
    fn json_text_from_elsewhere_reads_as_the_value_would() {
        // Spaces go; keys keep whatever order a value gives them (sorted, or as written when
        // serde_json preserves the order, as it does in the her binary).
        let arguments = r#"{ "z" : 1, "a" : [ 1 , 2 ] }"#;
        let read: ToolCall = serde_json::from_str(&format!(r#"{{"id":"c1","name":"write","arguments":{arguments}}}"#)).unwrap();
        let value: Value = serde_json::from_str(arguments).unwrap();
        assert_eq!(read.arguments.as_str(), value.to_string());
        assert!(!read.arguments.as_str().contains(' '));
        assert_eq!(JsonText::default().as_str(), "{}");
    }
}
