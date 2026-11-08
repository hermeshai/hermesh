//! Unified stream events and the accumulator that turns them into a message.

use futures::stream::BoxStream;
use serde_json::Value;
use std::time::Duration;
use std::time::Instant;

use crate::error::LlmError;
use crate::error::truncate_chars;
use crate::model::Model;
use crate::types::AssistantContent;
use crate::types::AssistantMessage;
use crate::types::StopReason;
use crate::types::Timestamp;
use crate::types::ToolCall;
use crate::types::Usage;
use crate::types::now_ms;

pub type LlmStream = BoxStream<'static, StreamEvent>;

/// Contract (every adapter follows it):
/// 1. `stream()` never returns an error; all failures end the stream with `Failed`.
/// 2. Exactly one terminal event (`Done` or `Failed`), then the stream ends.
/// 3. `index` is the position of the block in the final `content`.
/// 4. Terminal events carry the complete (or partial) message.
/// 5. Cancelling means dropping the stream; adapters never produce `Aborted`.
/// 6. Adapters do not retry; the registry does.
#[derive(Clone, Debug)]
pub enum StreamEvent {
    TextDelta { index: usize, text: String },
    ThinkingDelta { index: usize, text: String },
    ToolCallStart { index: usize, id: String, name: String },
    ToolCallArgs { index: usize, json: String },
    /// What the provider attached to a tool call and wants back (`ToolCall::provider_data`).
    ToolCallData { index: usize, data: Value },
    /// Produced by the registry's retry layer only. Deltas before it belong to the attempt that
    /// failed: drop them, the next attempt starts over.
    Retrying { attempt: u32, delay: Duration, error: LlmError },
    Done(AssistantMessage),
    Failed(AssistantMessage),
}

impl StreamEvent {
    pub fn is_content(&self) -> bool {
        matches!(
            self,
            StreamEvent::TextDelta { .. }
                | StreamEvent::ThinkingDelta { .. }
                | StreamEvent::ToolCallStart { .. }
                | StreamEvent::ToolCallArgs { .. }
                | StreamEvent::ToolCallData { .. }
        )
    }
}

#[derive(Clone, Debug)]
enum Block {
    Pending,
    Text(String),
    Thinking {
        text: String,
        signature: Option<String>,
        redacted: bool,
    },
    ToolCall {
        id: String,
        name: String,
        args: String,
        data: Option<Value>,
    },
}

/// Builds an `AssistantMessage` from stream events, indexed by block position.
pub struct MessageAccumulator {
    provider: String,
    api: String,
    model: String,
    ts: Timestamp,
    started: Instant,
    blocks: Vec<Block>,
    pub response_id: Option<String>,
    pub usage: Usage,
}

impl MessageAccumulator {
    pub fn new(provider: impl Into<String>, api: impl Into<String>, model: impl Into<String>) -> Self {
        Self {
            provider: provider.into(),
            api: api.into(),
            model: model.into(),
            ts: now_ms(),
            started: Instant::now(),
            blocks: Vec::new(),
            response_id: None,
            usage: Usage::default(),
        }
    }

    pub fn for_model(model: &Model) -> Self {
        Self::new(&model.provider, &model.api, &model.id)
    }

    fn slot(&mut self, index: usize) -> &mut Block {
        while self.blocks.len() <= index {
            self.blocks.push(Block::Pending);
        }
        &mut self.blocks[index]
    }

    pub fn apply(&mut self, event: &StreamEvent) {
        match event {
            StreamEvent::TextDelta { index, text } => {
                let slot = self.slot(*index);
                match slot {
                    Block::Text(existing) => existing.push_str(text),
                    Block::Pending => *slot = Block::Text(text.clone()),
                    _ => {}
                }
            }
            StreamEvent::ThinkingDelta { index, text } => {
                let slot = self.slot(*index);
                match slot {
                    Block::Thinking { text: existing, .. } => existing.push_str(text),
                    Block::Pending => {
                        *slot = Block::Thinking {
                            text: text.clone(),
                            signature: None,
                            redacted: false,
                        }
                    }
                    _ => {}
                }
            }
            StreamEvent::ToolCallStart { index, id, name } => {
                let slot = self.slot(*index);
                match slot {
                    Block::Pending => {
                        *slot = Block::ToolCall {
                            id: id.clone(),
                            name: name.clone(),
                            args: String::new(),
                            data: None,
                        }
                    }
                    // A later word on the same call (e.g. Responses' `output_item.done`) fills in
                    // what its opening event left blank, never overwrites it.
                    Block::ToolCall { id: i, name: n, .. } => {
                        if i.is_empty() {
                            *i = id.clone();
                        }
                        if n.is_empty() {
                            *n = name.clone();
                        }
                    }
                    _ => {}
                }
            }
            StreamEvent::ToolCallData { index, data } => {
                if let Block::ToolCall { data: slot, .. } = self.slot(*index) {
                    *slot = Some(data.clone());
                }
            }
            StreamEvent::ToolCallArgs { index, json } => {
                if let Block::ToolCall { args, .. } = self.slot(*index) {
                    args.push_str(json);
                }
            }
            StreamEvent::Retrying { .. } | StreamEvent::Done(_) | StreamEvent::Failed(_) => {}
        }
    }

    /// Creates an empty thinking block (some protocols announce it before any delta).
    pub fn ensure_thinking(&mut self, index: usize) {
        let slot = self.slot(index);
        if matches!(slot, Block::Pending) {
            *slot = Block::Thinking {
                text: String::new(),
                signature: None,
                redacted: false,
            };
        }
    }

    pub fn append_thinking_signature(&mut self, index: usize, part: &str) {
        self.ensure_thinking(index);
        if let Block::Thinking { signature, .. } = self.slot(index) {
            signature.get_or_insert_with(String::new).push_str(part);
        }
    }

    /// Sets the signature of every thinking block that has none (used to record the
    /// source field name of OpenAI-compatible reasoning).
    pub fn set_missing_thinking_signatures(&mut self, value: &str) {
        for block in &mut self.blocks {
            if let Block::Thinking { signature, .. } = block
                && signature.is_none()
            {
                *signature = Some(value.to_string());
            }
        }
    }

    pub fn set_redacted_thinking(&mut self, index: usize, data: String) {
        *self.slot(index) = Block::Thinking {
            text: String::new(),
            signature: Some(data),
            redacted: true,
        };
    }

    pub fn has_content(&self) -> bool {
        self.blocks.iter().any(|b| match b {
            Block::Pending => false,
            Block::Text(t) => !t.is_empty(),
            Block::Thinking { text, redacted, .. } => !text.is_empty() || *redacted,
            Block::ToolCall { .. } => true,
        })
    }

    fn message(&self, content: Vec<AssistantContent>, stop: StopReason, error: Option<LlmError>) -> AssistantMessage {
        AssistantMessage {
            content,
            provider: self.provider.clone(),
            api: self.api.clone(),
            model: self.model.clone(),
            response_id: self.response_id.clone(),
            usage: self.usage.clone(),
            stop_reason: stop,
            error: error.map(Box::new),
            ts: self.ts,
            duration_ms: Some(self.started.elapsed().as_millis() as u64),
        }
    }

    fn text_and_thinking(&self) -> Vec<AssistantContent> {
        self.blocks
            .iter()
            .filter_map(|b| match b {
                Block::Text(t) if !t.is_empty() => Some(AssistantContent::Text { text: t.clone() }),
                Block::Thinking {
                    text,
                    signature,
                    redacted,
                } if !text.is_empty() || *redacted => Some(AssistantContent::Thinking {
                    text: text.clone(),
                    signature: signature.clone(),
                    redacted: *redacted,
                }),
                _ => None,
            })
            .collect()
    }

    /// Final message for a successful response. A `Stop` with tool calls becomes `ToolUse`
    /// (some providers report `stop` even when they called tools).
    pub fn finish(&self, stop: StopReason) -> AssistantMessage {
        let content: Vec<AssistantContent> = self
            .blocks
            .iter()
            .filter_map(|b| match b {
                Block::Pending => None,
                Block::Text(t) if t.is_empty() => None,
                Block::Text(t) => Some(AssistantContent::Text { text: t.clone() }),
                Block::Thinking {
                    text,
                    signature,
                    redacted,
                } => {
                    if text.is_empty() && !*redacted && signature.is_none() {
                        None
                    } else {
                        Some(AssistantContent::Thinking {
                            text: text.clone(),
                            signature: signature.clone(),
                            redacted: *redacted,
                        })
                    }
                }
                Block::ToolCall { id, name, args, data } => {
                    let mut call = parse_tool_call(id, name, args);
                    call.provider_data = data.clone();
                    Some(AssistantContent::ToolCall(call))
                }
            })
            .collect();
        let has_tools = content.iter().any(|c| matches!(c, AssistantContent::ToolCall(_)));
        let stop = if stop == StopReason::Stop && has_tools {
            StopReason::ToolUse
        } else {
            stop
        };
        self.message(content, stop, None)
    }

    /// Message for a failed response: text and thinking received so far, no tool calls.
    pub fn fail(&self, error: LlmError) -> AssistantMessage {
        self.message(self.text_and_thinking(), StopReason::Error, Some(error))
    }

    /// Message for a cancelled response: only the text received so far.
    pub fn aborted(&self) -> AssistantMessage {
        let content = self
            .blocks
            .iter()
            .filter_map(|b| match b {
                Block::Text(t) if !t.is_empty() => Some(AssistantContent::Text { text: t.clone() }),
                _ => None,
            })
            .collect();
        self.message(content, StopReason::Aborted, None)
    }
}

/// Parses accumulated tool-call arguments; invalid input is reported, not fatal.
pub fn parse_tool_call(id: &str, name: &str, raw: &str) -> ToolCall {
    let trimmed = raw.trim();
    let (arguments, arguments_error) = if trimmed.is_empty() {
        (Value::Object(Default::default()), None)
    } else {
        match serde_json::from_str::<Value>(trimmed) {
            Ok(Value::Object(map)) => (Value::Object(map), None),
            Ok(other) => (
                Value::Object(Default::default()),
                Some(format!(
                    "Tool arguments must be a JSON object, got: {}",
                    truncate_chars(&other.to_string(), 200)
                )),
            ),
            Err(err) => (
                Value::Object(Default::default()),
                Some(format!(
                    "Tool arguments are not valid JSON ({err}): {}",
                    truncate_chars(trimmed, 200)
                )),
            ),
        }
    };
    ToolCall {
        id: id.to_string(),
        name: name.to_string(),
        arguments: arguments.into(),
        arguments_error,
        provider_data: None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::ErrorKind;

    fn acc() -> MessageAccumulator {
        MessageAccumulator::new("p", "openai-chat", "m")
    }

    #[test]
    fn accumulates_blocks_by_index() {
        let mut a = acc();
        for ev in [
            StreamEvent::ThinkingDelta { index: 0, text: "think".into() },
            StreamEvent::TextDelta { index: 1, text: "Hel".into() },
            StreamEvent::TextDelta { index: 1, text: "lo".into() },
            StreamEvent::ToolCallStart { index: 2, id: "c1".into(), name: "read".into() },
            StreamEvent::ToolCallStart { index: 3, id: "c2".into(), name: "bash".into() },
            StreamEvent::ToolCallArgs { index: 2, json: "{\"path\":".into() },
            StreamEvent::ToolCallArgs { index: 3, json: "{\"command\":\"ls\"}".into() },
            StreamEvent::ToolCallArgs { index: 2, json: "\"a.txt\"}".into() },
        ] {
            a.apply(&ev);
        }
        let msg = a.finish(StopReason::Stop);
        assert_eq!(msg.stop_reason, StopReason::ToolUse);
        assert_eq!(msg.content.len(), 4);
        assert_eq!(msg.text(), "Hello");
        let calls = msg.tool_calls();
        assert_eq!(calls[0].arguments.to_value()["path"], "a.txt");
        assert_eq!(calls[1].arguments.to_value()["command"], "ls");
    }

    #[test]
    fn invalid_arguments_are_reported() {
        let call = parse_tool_call("c", "read", "{\"path\": ");
        assert!(call.arguments_error.is_some());
        assert_eq!(call.arguments.to_value(), serde_json::json!({}));
        let call = parse_tool_call("c", "read", "[1,2]");
        assert!(call.arguments_error.unwrap().contains("JSON object"));
    }

    #[test]
    fn fail_and_abort_drop_tool_calls() {
        let mut a = acc();
        a.apply(&StreamEvent::TextDelta { index: 0, text: "partial".into() });
        a.apply(&StreamEvent::ToolCallStart { index: 1, id: "c".into(), name: "bash".into() });
        let failed = a.fail(LlmError::new(ErrorKind::Network, "reset"));
        assert_eq!(failed.stop_reason, StopReason::Error);
        assert!(failed.tool_calls().is_empty());
        assert_eq!(failed.text(), "partial");
        let aborted = a.aborted();
        assert_eq!(aborted.stop_reason, StopReason::Aborted);
        assert_eq!(aborted.text(), "partial");
    }
}
