//! Events on their way to connections. Each event is encoded for the native protocol at most
//! once, by the first connection that needs it; every other connection reuses the bytes.

use bytes::Bytes;
use her_llm::AssistantContent;
use her_llm::Message;
use her_llm::UserContent;
use her_protocol::Delta;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::api::EventClass;
use std::sync::Arc;
use std::sync::OnceLock;

/// Per-session data shared by all frames of the session.
#[derive(Debug)]
pub struct SessionShared {
    pub id: Arc<str>,
    /// `{"jsonrpc":"2.0","method":"session/event","params":{"session_id":"<id>","event":`
    native_prefix: Box<[u8]>,
}

const NATIVE_SUFFIX: &[u8] = b"}}\n";

impl SessionShared {
    pub fn new(id: &str) -> Arc<Self> {
        let quoted = serde_json::to_string(id).unwrap_or_else(|_| "\"\"".to_string());
        let prefix = format!(r#"{{"jsonrpc":"2.0","method":"session/event","params":{{"session_id":{quoted},"event":"#);
        Arc::new(Self {
            id: id.into(),
            native_prefix: prefix.into_bytes().into_boxed_slice(),
        })
    }
}

pub struct EventFrame {
    pub session: Arc<SessionShared>,
    pub event: Event,
    native: OnceLock<Bytes>,
}

impl EventFrame {
    pub fn new(session: Arc<SessionShared>, event: Event) -> Self {
        Self {
            session,
            event,
            native: OnceLock::new(),
        }
    }

    /// The complete `session/event` line, encoded on first use.
    pub fn native(&self) -> &Bytes {
        self.native.get_or_init(|| {
            let mut out = Vec::with_capacity(self.session.native_prefix.len() + self.size_hint() + NATIVE_SUFFIX.len());
            out.extend_from_slice(&self.session.native_prefix);
            if serde_json::to_writer(&mut out, &self.event).is_err() {
                out.truncate(self.session.native_prefix.len());
                out.extend_from_slice(b"null");
            }
            out.extend_from_slice(NATIVE_SUFFIX);
            Bytes::from(out)
        })
    }

    pub fn is_encoded(&self) -> bool {
        self.native.get().is_some()
    }

    /// Rough encoded size, for queue accounting.
    pub fn size_hint(&self) -> usize {
        const BASE: usize = 160;
        BASE + match &self.event.msg {
            EventMsg::MessageDelta { delta, .. } => match delta {
                Delta::Text { text, .. } | Delta::Thinking { text, .. } => text.len(),
                Delta::ToolCallArgs { json, .. } => json.len(),
                Delta::ToolCallStart { name, .. } => name.len(),
            },
            EventMsg::ToolProgress { text, .. } => text.len(),
            EventMsg::MessageCompleted { message, .. } => message_size(message),
            EventMsg::ToolCompleted { result, .. } => content_size(&result.content) + 256,
            EventMsg::ToolStarted { args, .. } | EventMsg::ApprovalRequested { args, .. } => 256 + value_size(args),
            _ => 96,
        }
    }

    /// The class a connection may filter out, if any.
    pub fn class(&self) -> Option<EventClass> {
        match &self.event.msg {
            EventMsg::MessageDelta { delta, .. } => Some(match delta {
                Delta::Text { .. } => EventClass::TextDelta,
                Delta::Thinking { .. } => EventClass::ThinkingDelta,
                Delta::ToolCallStart { .. } | Delta::ToolCallArgs { .. } => EventClass::ToolCallDelta,
            }),
            EventMsg::ToolProgress { .. } => Some(EventClass::ToolProgress),
            _ => None,
        }
    }
}

fn content_size(content: &[UserContent]) -> usize {
    content
        .iter()
        .map(|c| match c {
            UserContent::Text { text } => text.len(),
            UserContent::Image { data, .. } => data.len(),
        })
        .sum()
}

fn message_size(message: &Message) -> usize {
    match message {
        Message::User(user) => content_size(&user.content) + 64,
        Message::ToolResult(result) => content_size(&result.content) + 256,
        Message::Assistant(assistant) => {
            assistant
                .content
                .iter()
                .map(|c| match c {
                    AssistantContent::Text { text } => text.len(),
                    AssistantContent::Thinking { text, signature, .. } => {
                        text.len() + signature.as_ref().map_or(0, String::len)
                    }
                    AssistantContent::ToolCall(call) => 64 + value_size(&call.arguments.to_value()),
                })
                .sum::<usize>()
                + 256
        }
    }
}

/// Cheap upper-bound guess without serializing.
fn value_size(value: &serde_json::Value) -> usize {
    match value {
        serde_json::Value::String(s) => s.len() + 2,
        serde_json::Value::Array(items) => items.iter().map(value_size).sum::<usize>() + 2,
        serde_json::Value::Object(map) => map.iter().map(|(k, v)| k.len() + 4 + value_size(v)).sum::<usize>() + 2,
        _ => 8,
    }
}

/// Events that can be merged with an adjacent event of the same key: text, reasoning and
/// tool-argument deltas of one block, and progress output of one tool call.
#[derive(PartialEq, Eq)]
enum MergeKey<'a> {
    Text(&'a str, usize),
    Thinking(&'a str, usize),
    Args(&'a str, usize),
    Progress(&'a str),
}

fn merge_key(frame: &EventFrame) -> Option<(*const SessionShared, MergeKey<'_>)> {
    let key = match &frame.event.msg {
        EventMsg::MessageDelta { message_id, delta } => match delta {
            Delta::Text { index, .. } => MergeKey::Text(message_id, *index),
            Delta::Thinking { index, .. } => MergeKey::Thinking(message_id, *index),
            Delta::ToolCallArgs { index, .. } => MergeKey::Args(message_id, *index),
            Delta::ToolCallStart { .. } => return None,
        },
        EventMsg::ToolProgress { call_id, .. } => MergeKey::Progress(call_id),
        _ => return None,
    };
    Some((Arc::as_ptr(&frame.session), key))
}

/// Whether `next` may be appended to `frame`: same session and same text block or tool call.
pub fn mergeable(frame: &EventFrame, next: &EventFrame) -> bool {
    matches!((merge_key(frame), merge_key(next)), (Some(a), Some(b)) if a == b)
}

fn piece(frame: &EventFrame) -> &str {
    match &frame.event.msg {
        EventMsg::MessageDelta { delta, .. } => match delta {
            Delta::Text { text, .. } | Delta::Thinking { text, .. } => text,
            Delta::ToolCallArgs { json, .. } => json,
            Delta::ToolCallStart { .. } => "",
        },
        EventMsg::ToolProgress { text, .. } => text,
        _ => "",
    }
}

/// One frame for a run of mergeable frames: the texts concatenated, the last sequence number.
fn merge_run(run: &[Arc<EventFrame>]) -> Arc<EventFrame> {
    let first = &run[0];
    let last = &run[run.len() - 1];
    let mut text = String::with_capacity(run.iter().map(|f| piece(f).len()).sum());
    for frame in run {
        text.push_str(piece(frame));
    }
    build(first, text, last.event.seq, last.event.ts)
}

/// A run of deltas merged inside one connection's queue. The text grows in place, so a long
/// run costs one append per delta, not one copy.
pub struct MergedRun {
    first: Arc<EventFrame>,
    text: String,
    seq: u64,
    ts: her_llm::Timestamp,
}

impl MergedRun {
    pub fn new(first: Arc<EventFrame>, next: &EventFrame) -> Self {
        let mut text = String::with_capacity((piece(&first).len() + piece(next).len()) * 4);
        text.push_str(piece(&first));
        text.push_str(piece(next));
        Self {
            seq: next.event.seq,
            ts: next.event.ts,
            first,
            text,
        }
    }

    pub fn accepts(&self, next: &EventFrame) -> bool {
        mergeable(&self.first, next)
    }

    pub fn push(&mut self, next: &EventFrame) {
        self.text.push_str(piece(next));
        self.seq = next.event.seq;
        self.ts = next.event.ts;
    }

    /// Bytes `next` adds to a run.
    pub fn added(next: &EventFrame) -> usize {
        piece(next).len()
    }

    pub fn into_frame(self) -> Arc<EventFrame> {
        build(&self.first, self.text, self.seq, self.ts)
    }
}

/// A delta like `first`, carrying `text`.
fn build(first: &EventFrame, text: String, seq: u64, ts: her_llm::Timestamp) -> Arc<EventFrame> {
    let msg = match &first.event.msg {
        EventMsg::MessageDelta { message_id, delta } => EventMsg::MessageDelta {
            message_id: message_id.clone(),
            delta: match delta {
                Delta::Text { index, .. } => Delta::Text { index: *index, text },
                Delta::Thinking { index, .. } => Delta::Thinking { index: *index, text },
                Delta::ToolCallArgs { index, .. } => Delta::ToolCallArgs { index: *index, json: text },
                other => other.clone(),
            },
        },
        EventMsg::ToolProgress { call_id, .. } => EventMsg::ToolProgress {
            call_id: call_id.clone(),
            text,
        },
        other => other.clone(),
    };
    Arc::new(EventFrame::new(first.session.clone(), Event { seq, ts, msg }))
}

/// Merges runs of adjacent mergeable frames in place. Other frames keep their order.
pub fn coalesce(frames: Vec<Arc<EventFrame>>) -> Vec<Arc<EventFrame>> {
    if frames.len() < 2 {
        return frames;
    }
    let mut out = Vec::with_capacity(frames.len());
    let mut start = 0;
    while start < frames.len() {
        let mut end = start + 1;
        if let Some(key) = merge_key(&frames[start]) {
            while end < frames.len() && merge_key(&frames[end]).as_ref() == Some(&key) {
                end += 1;
            }
        }
        if end - start == 1 {
            out.push(frames[start].clone());
        } else {
            out.push(merge_run(&frames[start..end]));
        }
        start = end;
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn delta(session: &Arc<SessionShared>, seq: u64, message: &str, delta: Delta) -> Arc<EventFrame> {
        Arc::new(EventFrame::new(
            session.clone(),
            Event {
                seq,
                ts: seq as i64,
                msg: EventMsg::MessageDelta {
                    message_id: message.into(),
                    delta,
                },
            },
        ))
    }

    fn text(index: usize, t: &str) -> Delta {
        Delta::Text { index, text: t.into() }
    }

    #[test]
    fn native_line_wraps_the_event() {
        let session = SessionShared::new("s\"1");
        let frame = delta(&session, 3, "m1", text(0, "hi\n"));
        let line = std::str::from_utf8(frame.native()).unwrap();
        assert!(line.ends_with("}}\n"));
        assert_eq!(line.matches('\n').count(), 1);
        let value: serde_json::Value = serde_json::from_str(line).unwrap();
        assert_eq!(value["method"], "session/event");
        assert_eq!(value["params"]["session_id"], "s\"1");
        assert_eq!(value["params"]["event"]["seq"], 3);
        assert_eq!(value["params"]["event"]["delta"]["text"], "hi\n");
        // Encoded once, shared afterwards.
        assert!(frame.is_encoded());
        assert_eq!(frame.native().as_ptr(), frame.native().as_ptr());
    }

    #[test]
    fn coalescing_merges_only_adjacent_runs_of_one_block() {
        let s = SessionShared::new("s");
        let other = SessionShared::new("t");
        let frames = vec![
            delta(&s, 1, "m", text(0, "a")),
            delta(&s, 2, "m", text(0, "b")),
            delta(&s, 3, "m", Delta::Thinking { index: 1, text: "x".into() }),
            delta(&s, 4, "m", text(0, "c")),
            delta(&s, 5, "m", text(0, "d")),
            delta(&other, 6, "m", text(0, "e")),
            delta(&s, 7, "m", text(2, "f")),
        ];
        let merged = coalesce(frames);
        let summary: Vec<(u64, String)> = merged
            .iter()
            .map(|f| (f.event.seq, piece(f).to_string()))
            .collect();
        assert_eq!(
            summary,
            vec![
                (2, "ab".to_string()),
                (3, "x".to_string()),
                (5, "cd".to_string()),
                (6, "e".to_string()),
                (7, "f".to_string()),
            ]
        );
        assert!(merged.windows(2).all(|w| w[0].event.seq < w[1].event.seq));
    }
}
