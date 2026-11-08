//! Bounded Qoder framing compatibility, isolated from ordinary OpenAI providers.
use her_llm::{ErrorKind, LlmError, api::openai_chat::ChatEvents};
const LIMIT: usize = 1024 * 1024;
fn error(code: &str) -> LlmError {
    let mut e = LlmError::new(ErrorKind::Protocol, "Qoder 返回了无法识别的模型流");
    e.provider_code = Some(code.into());
    e
}
#[derive(Default)]
struct Frame {
    lines: Vec<Line>,
    field_prefix: String,
    bytes: usize,
    is_error: bool,
}
struct Line {
    data: String,
    is_comment: bool,
}
impl Frame {
    fn push(&mut self, line: &[u8]) -> Result<Option<String>, LlmError> {
        let line = std::str::from_utf8(line)
            .map_err(|_| error("invalid_stream_utf8"))?
            .trim_end_matches('\r');
        // Observed in Kimi/Efficient: even `data:` can be wrapped as `da\nta:`.
        // Recover only this exact field, within one event, never arbitrary prefixes.
        if !self.field_prefix.is_empty() {
            if line.is_empty() {
                return Err(error("incomplete_sse_field"));
            }
            let combined = std::mem::take(&mut self.field_prefix) + line;
            return self.push(combined.as_bytes());
        }
        if line.is_empty() {
            return self.finish();
        }
        if self.lines.is_empty() && matches!(line, "d" | "da" | "dat" | "data") {
            self.field_prefix = line.into();
            return Ok(None);
        }
        if let Some(event) = line.strip_prefix("event:") {
            self.is_error = event.trim() == "error";
            return Ok(None);
        }
        let is_comment = line.starts_with(':');
        if (is_comment && self.lines.is_empty())
            || line.starts_with("id:")
            || line.starts_with("retry:")
        {
            return Ok(None);
        }
        let data = if let Some(data) = line.strip_prefix("data:") {
            data.strip_prefix(' ').unwrap_or(data)
        } else if !self.lines.is_empty() {
            line
        } else {
            return Err(error("unexpected_sse_line"));
        };
        self.bytes += data.len();
        if self.bytes > LIMIT {
            return Err(error("sse_event_too_large"));
        }
        self.lines.push(Line {
            data: data.into(),
            is_comment,
        });
        Ok(None)
    }
    fn finish(&mut self) -> Result<Option<String>, LlmError> {
        if !self.field_prefix.is_empty() {
            return Err(error("incomplete_sse_field"));
        }
        let is_error = std::mem::take(&mut self.is_error);
        if self.lines.is_empty() {
            return if is_error {
                Err(error("empty_error_event"))
            } else {
                Ok(None)
            };
        }
        let lines = std::mem::take(&mut self.lines);
        let data: Vec<_> = lines
            .iter()
            .filter(|l| !l.is_comment)
            .map(|l| l.data.as_str())
            .collect();
        let standard = data.join("\n");
        let compact = data.concat();
        self.bytes = 0;
        if !is_error && standard.trim() == "[DONE]" {
            return Ok(Some("[DONE]".into()));
        }
        let parsed = serde_json::from_str::<serde_json::Value>(&standard)
            .map(|v| (standard, v))
            .or_else(|_| serde_json::from_str::<serde_json::Value>(&compact).map(|v| (compact, v)))
            .or_else(|err| {
                if !lines.iter().any(|l| l.is_comment) {
                    return Err(err);
                }
                // Qoder sometimes wraps JSON immediately before a colon (e.g. after
                // "model"). Try the complete, bounded event only after standard SSE
                // parsing failed. Real SSE comments continue to be ignored when valid.
                let recovered: String = lines.iter().map(|l| l.data.as_str()).collect();
                serde_json::from_str::<serde_json::Value>(&recovered).map(|v| (recovered, v))
            })
            .map_err(|_| error("invalid_stream_json"))?;
        if is_error {
            return Err(provider_error(&parsed.1));
        }
        if parsed.1.is_null() {
            return Ok(None);
        }
        if !parsed.1.is_object() {
            return Err(error("unexpected_sse_value"));
        }
        if parsed.1.get("error").is_some_and(|v| !v.is_null())
            || (parsed.1.get("code").is_some() && parsed.1.get("message").is_some())
        {
            return Err(provider_error(&parsed.1));
        }
        Ok(Some(parsed.0))
    }
}

fn provider_error(value: &serde_json::Value) -> LlmError {
    let value = value.get("error").filter(|v| !v.is_null()).unwrap_or(value);
    if value.get("type").and_then(|v| v.as_str()) == Some("intention_rejected") {
        let mut error = LlmError::new(
            ErrorKind::InvalidRequest,
            "Qoder 拒绝了此请求的任务类型，请调整任务或选择其他模型",
        );
        error.provider_code = Some("intention_rejected".into());
        return error;
    }
    // Do not stringify the envelope: it can include request IDs and private diagnostics.
    let safe = serde_json::json!({
        "code": value.get("code").or_else(|| value.get("type")),
        "message": value.get("message").and_then(|v| v.as_str()).filter(|s| !s.trim().is_empty()).unwrap_or("Qoder 模型请求失败")
    });
    let status = value
        .get("status")
        .or_else(|| value.get("statusCode"))
        .and_then(|v| v.as_u64())
        .filter(|v| (400..600).contains(v));
    let mut error = if let Some(status) = status {
        LlmError::from_http(status as u16, &safe.to_string(), None, &[])
    } else {
        LlmError::from_stream_error(&safe, &[])
    };
    if error.kind == ErrorKind::Unknown && error.provider_code.as_deref() == Some("provider_error")
    {
        error.kind = ErrorKind::Server;
    }
    error.message = crate::adapter::scrub(&error.message);
    error.provider_code = error.provider_code.map(|code| crate::adapter::scrub(&code));
    error
}
pub(super) fn decode(mut response: reqwest::Response) -> ChatEvents {
    Box::pin(async_stream::stream! {
     let mut pending=Vec::new();let mut frame=Frame::default();
     loop{
      let chunk=match response.chunk().await{Ok(Some(v))=>v,Ok(None)=>break,Err(_)=>{yield Err(error("stream_read_failed"));return;}};
      for byte in chunk {
       if byte==b'\n'{match frame.push(&pending){Ok(Some(event))=>yield Ok(event),Ok(None)=>{},Err(e)=>{yield Err(e);return;}}pending.clear();}
       else{pending.push(byte);if pending.len()+frame.bytes>LIMIT{yield Err(error("sse_event_too_large"));return;}}
      }
     }
     if !pending.is_empty(){match frame.push(&pending){Ok(Some(event))=>yield Ok(event),Ok(None)=>{},Err(e)=>{yield Err(e);return;}}}
     match frame.finish(){Ok(Some(event))=>yield Ok(event),Ok(None)=>{},Err(e)=>yield Err(e)}
    })
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn wrapped_data_field_is_recovered_only_within_its_event() {
        for split in 1..=4 {
            let mut frame = Frame::default();
            let line = br#"data: {"choices":[{"delta":{"content":"hello"}}]}"#;
            frame.push(&line[..split]).unwrap();
            frame.push(&line[split..]).unwrap();
            let value: serde_json::Value =
                serde_json::from_str(&frame.push(b"").unwrap().unwrap()).unwrap();
            assert_eq!(value["choices"][0]["delta"]["content"], "hello");
        }
        let mut frame = Frame::default();
        frame.push(b"da").unwrap();
        assert!(frame.push(b"").is_err(), "never combine different events");
        let mut frame = Frame::default();
        frame.push(b"da").unwrap();
        assert!(
            frame.finish().is_err(),
            "never accept a truncated field at EOF"
        );
        let mut frame = Frame::default();
        frame.push(b"da").unwrap();
        assert!(
            frame.push(b"ngerous: {}").is_err(),
            "only data: may be repaired"
        );
    }
    #[test]
    fn intention_rejection_has_an_actionable_non_retryable_error() {
        let mut f = Frame::default();
        f.push(b"event: error").unwrap();
        f.push(br#"data: {"error":{"code":406,"message":"","type":"intention_rejected"}}"#)
            .unwrap();
        let error = f.push(b"").unwrap_err();
        assert_eq!(error.kind, ErrorKind::InvalidRequest);
        assert_eq!(error.provider_code.as_deref(), Some("intention_rejected"));
        assert!(!error.kind.is_retryable());
        assert!(error.message.contains("任务类型"));
    }

    #[test]
    fn colon_continuation_is_recovered_without_changing_real_sse_comments() {
        let mut f = Frame::default();
        // Sanitized shape captured from Qoder's Chinese response: the wrap occurred
        // immediately after a JSON field name, so the continuation looked like a comment.
        f.push(
            "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"},\"finish_reason\"".as_bytes(),
        )
        .unwrap();
        f.push(br#":null}],"model":"auto"}"#).unwrap();
        let event: serde_json::Value =
            serde_json::from_str(&f.push(b"").unwrap().unwrap()).unwrap();
        assert_eq!(event["choices"][0]["delta"]["content"], "你好");
        assert_eq!(event["model"], "auto");

        f.push(b"data: {\"choices\":").unwrap();
        f.push(b": heartbeat").unwrap();
        f.push(b"data: []}").unwrap();
        assert_eq!(f.push(b"").unwrap().as_deref(), Some("{\"choices\":\n[]}"));

        f.push(b"data: {\"unfinished\"").unwrap();
        assert!(
            f.push(b"").is_err(),
            "cannot recover across event boundaries"
        );
    }

    #[test]
    fn error_events_and_bare_errors_are_not_terminal_marker_failures() {
        for prefix in [Some(b"event: error".as_slice()), None] {
            let mut f = Frame::default();
            if let Some(prefix) = prefix {
                f.push(prefix).unwrap();
            }
            f.push(br#"data: {"code":"provider_error","message":"All backends failed"}"#)
                .unwrap();
            let e = f.push(b"").unwrap_err();
            assert_eq!(e.kind, ErrorKind::Server);
            assert_eq!(e.provider_code.as_deref(), Some("provider_error"));
            assert_eq!(e.message, "All backends failed");
            f.push(b"data: [DONE]").unwrap();
            assert_eq!(f.push(b"").unwrap().as_deref(), Some("[DONE]"));
        }
        let mut f = Frame::default();
        f.push(br#"data: {"error":{"code":"rate_limit","message":"Too many requests"}}"#)
            .unwrap();
        assert_eq!(f.push(b"").unwrap_err().kind, ErrorKind::RateLimited);
    }

    #[test]
    fn restores_only_one_bounded_event() {
        let mut f = Frame::default();
        f.push(b"data: {\"choices\":[{\"delta\":{\"content\":\"hel")
            .unwrap();
        f.push(b"lo\"}}]}").unwrap();
        let v = f.push(b"").unwrap().unwrap();
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&v).unwrap()["choices"][0]["delta"]["content"],
            "hello"
        );
        assert!(f.push(b"data: null").unwrap().is_none());
        assert!(f.push(b"").unwrap().is_none());
    }
    #[test]
    fn malformed_event_cannot_cross_into_next() {
        let mut f = Frame::default();
        f.push(b"data: {broken").unwrap();
        assert!(f.push(b"").is_err());
    }
    #[test]
    fn caps_buffer_and_handles_unicode() {
        let mut f = Frame::default();
        f.push("data: {\"x\":\"你好\"}".as_bytes()).unwrap();
        assert!(f.push(b"").unwrap().is_some());
        assert!(f.push(&vec![b'x'; LIMIT + 1]).is_err());
    }
}
