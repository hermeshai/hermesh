//! HTTP and SSE helpers shared by the adapters.

use eventsource_stream::Event;
use eventsource_stream::Eventsource;
use futures::Stream;
use futures::StreamExt;
use serde::Serialize;
use serde::Serializer;
use serde_json::Map;
use serde_json::Value;
use std::cell::Cell;
use std::fmt;
use std::pin::Pin;
use std::time::Duration;

use crate::api::Timeouts;
use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::types::AssistantContent;
use crate::types::Context;
use crate::types::Message;
use crate::types::UserContent;

pub(crate) type SseStream = Pin<Box<dyn Stream<Item = Result<Event, String>> + Send>>;

/// A JSON request body written one message at a time, straight from the context: the history
/// is never copied into a JSON tree. The remaining top-level fields are small and come last.
pub(crate) struct JsonBody {
    out: Vec<u8>,
    messages: usize,
}

impl JsonBody {
    pub(crate) fn new(context: &Context) -> Self {
        // A power of two, so that the buffer freed after one request fits the next one: the
        // allocator keeps freed large blocks, and a slightly larger request each turn would
        // otherwise leave one behind every time.
        let mut out = Vec::with_capacity(estimated_len(context).next_power_of_two());
        out.extend_from_slice(br#"{"messages":["#);
        Self { out, messages: 0 }
    }

    pub(crate) fn message(&mut self, message: &(impl Serialize + ?Sized)) {
        if self.messages > 0 {
            self.out.push(b',');
        }
        self.messages += 1;
        write_json(&mut self.out, message);
    }

    pub(crate) fn finish(self, params: &Map<String, Value>) -> Vec<u8> {
        self.finish_with(params, None::<(&str, &Value)>)
    }

    /// Add a borrowed top-level value without constructing another JSON tree. Explicit
    /// params (including provider extra_body overrides) take precedence over this value.
    pub(crate) fn finish_with<T: Serialize + ?Sized>(
        mut self,
        params: &Map<String, Value>,
        extra: Option<(&str, &T)>,
    ) -> Vec<u8> {
        self.out.push(b']');
        for (key, value) in params.iter().filter(|(key, _)| key.as_str() != "messages") {
            self.field(key, value);
        }
        if let Some((key, value)) = extra
            && !params.contains_key(key)
            && key != "messages"
        {
            self.field(key, value);
        }
        self.out.push(b'}');
        self.out
    }

    fn field(&mut self, key: &str, value: &(impl Serialize + ?Sized)) {
        self.out.push(b',');
        write_json(&mut self.out, key);
        self.out.push(b':');
        write_json(&mut self.out, value);
    }
}

fn write_json(out: &mut Vec<u8>, value: &(impl Serialize + ?Sized)) {
    serde_json::to_writer(out, value).expect("JSON values serialize into a Vec");
}

/// Serializes `Display` output as a JSON string without building the string first.
pub(crate) struct DisplayStr<T>(pub T);

impl<T: fmt::Display> Serialize for DisplayStr<T> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.collect_str(&self.0)
    }
}

/// Serializes an iterator as a sequence (once).
pub(crate) struct SerializeIter<I>(Cell<Option<I>>);

impl<I> SerializeIter<I> {
    pub(crate) fn new(iter: I) -> Self {
        Self(Cell::new(Some(iter)))
    }
}

impl<I: Iterator<Item = T>, T: Serialize> Serialize for SerializeIter<I> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.collect_seq(self.0.take().into_iter().flatten())
    }
}

/// The text parts of `content`, one per line.
pub(crate) struct TextLines<'a>(pub &'a [UserContent]);

impl TextLines<'_> {
    pub(crate) fn is_empty(&self) -> bool {
        let mut texts = self.texts();
        match (texts.next(), texts.next()) {
            (None, _) => true,
            (Some(text), None) => text.is_empty(),
            _ => false,
        }
    }

    fn texts(&self) -> impl Iterator<Item = &str> {
        self.0.iter().filter_map(|c| match c {
            UserContent::Text { text } => Some(text.as_str()),
            UserContent::Image { .. } => None,
        })
    }
}

impl fmt::Display for TextLines<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for (i, text) in self.texts().enumerate() {
            if i > 0 {
                f.write_str("\n")?;
            }
            f.write_str(text)?;
        }
        Ok(())
    }
}

/// Roughly the serialized size of `context`, so the body buffer seldom has to grow (each
/// growth copies the body and leaves the old buffer to the allocator).
fn estimated_len(context: &Context) -> usize {
    fn user(content: &[UserContent]) -> usize {
        content
            .iter()
            .map(|c| match c {
                UserContent::Text { text } => text.len(),
                UserContent::Image { data, .. } => data.len(),
            })
            .sum()
    }
    let messages: usize = context
        .messages
        .iter()
        .map(|m| {
            64 + match m {
                Message::User(u) => user(&u.content),
                Message::ToolResult(r) => user(&r.content),
                Message::Assistant(a) => a
                    .content
                    .iter()
                    .map(|c| match c {
                        AssistantContent::Text { text } => text.len(),
                        AssistantContent::Thinking { text, signature, .. } => {
                            text.len() + signature.as_ref().map_or(0, String::len)
                        }
                        AssistantContent::ToolCall(_) => 256,
                    })
                    .sum(),
            }
        })
        .sum();
    // Escapes (newlines, quotes) add a little; each tool definition is a few KB.
    (context.system.len() + messages) * 9 / 8 + context.tools.len() * 2048 + 1024
}

/// Sends the request; a non-2xx status becomes a classified error.
pub(crate) async fn send(
    builder: reqwest::RequestBuilder,
    timeouts: &Timeouts,
    overflow_patterns: &[String],
) -> Result<reqwest::Response, LlmError> {
    let response = match tokio::time::timeout(timeouts.first_byte, builder.send()).await {
        Err(_) => {
            return Err(LlmError::new(
                ErrorKind::Timeout,
                format!("no response from the provider within {}s", timeouts.first_byte.as_secs()),
            ));
        }
        Ok(Err(err)) => return Err(LlmError::from_reqwest(&err)),
        Ok(Ok(response)) => response,
    };
    let status = response.status();
    if status.is_success() {
        return Ok(response);
    }
    let retry_after = retry_after_ms(response.headers());
    let body = response.text().await.unwrap_or_default();
    Err(LlmError::from_http(status.as_u16(), &body, retry_after, overflow_patterns))
}

pub(crate) fn retry_after_ms(headers: &reqwest::header::HeaderMap) -> Option<u64> {
    let parse = |name: &str| {
        headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .and_then(|s| s.trim().parse::<f64>().ok())
    };
    parse("retry-after-ms")
        .map(|ms| ms as u64)
        .or_else(|| parse("retry-after").map(|secs| (secs * 1000.0) as u64))
}

pub(crate) fn sse_events(response: reqwest::Response, idle: Duration) -> SseEvents {
    SseEvents {
        stream: Box::pin(
            response
                .bytes_stream()
                .eventsource()
                .map(|item| item.map_err(|e| e.to_string())),
        ),
        idle,
        timer: None,
    }
}

/// The SSE events of a response, each bounded by the idle timeout.
///
/// One timer serves the whole stream. Arming a fresh timer for every event takes the
/// runtime's timer lock twice per event, and with many streams at once the workers spend
/// most of their time waiting on that lock. Here the timer is armed once and moved only
/// when it fires before the idle time has passed since the wait began.
pub(crate) struct SseEvents {
    stream: SseStream,
    idle: Duration,
    timer: Option<Pin<Box<tokio::time::Sleep>>>,
}

impl SseEvents {
    /// The next event. `Ok(None)` means the stream ended.
    pub(crate) async fn next(&mut self) -> Result<Option<Event>, LlmError> {
        let since = tokio::time::Instant::now();
        let idle = self.idle;
        let timer = self.timer.get_or_insert_with(|| Box::pin(tokio::time::sleep(idle)));
        let item = loop {
            tokio::select! {
                biased;
                item = self.stream.next() => break item,
                () = timer.as_mut() => {
                    let deadline = since + idle;
                    if tokio::time::Instant::now() >= deadline {
                        return Err(LlmError::new(
                            ErrorKind::Timeout,
                            format!("no data from the provider for {}s", idle.as_secs()),
                        ));
                    }
                    timer.as_mut().reset(deadline);
                }
            }
        };
        match item {
            None => Ok(None),
            Some(Err(err)) => Err(LlmError::new(ErrorKind::Network, format!("stream interrupted: {err}"))),
            Some(Ok(event)) => Ok(Some(event)),
        }
    }
}
