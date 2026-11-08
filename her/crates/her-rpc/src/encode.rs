//! Outgoing messages, each encoded as one line ending in `\n`.

use serde::Serialize;

use crate::message::Id;
use crate::message::RpcError;

const VERSION: &str = "2.0";

#[derive(Serialize)]
struct ResponseOut<'a, T> {
    jsonrpc: &'static str,
    id: &'a Id,
    result: &'a T,
}

#[derive(Serialize)]
struct ErrorOut<'a> {
    jsonrpc: &'static str,
    /// `null` when the request could not be read.
    id: Option<&'a Id>,
    error: &'a RpcError,
}

#[derive(Serialize)]
struct NotificationOut<'a, T> {
    jsonrpc: &'static str,
    method: &'a str,
    params: &'a T,
}

#[derive(Serialize)]
struct RequestOut<'a, T> {
    jsonrpc: &'static str,
    id: &'a Id,
    method: &'a str,
    params: &'a T,
}

fn line(value: &impl Serialize) -> Vec<u8> {
    let mut out = Vec::with_capacity(128);
    // Serializing plain data structures into a Vec cannot fail.
    if serde_json::to_writer(&mut out, value).is_err() {
        out.clear();
        out.extend_from_slice(br#"{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"unserializable message"}}"#);
    }
    out.push(b'\n');
    out
}

pub fn response<T: Serialize>(id: &Id, result: &T) -> Vec<u8> {
    line(&ResponseOut {
        jsonrpc: VERSION,
        id,
        result,
    })
}

pub fn error_response(id: Option<&Id>, error: &RpcError) -> Vec<u8> {
    line(&ErrorOut {
        jsonrpc: VERSION,
        id,
        error,
    })
}

pub fn notification<T: Serialize>(method: &str, params: &T) -> Vec<u8> {
    line(&NotificationOut {
        jsonrpc: VERSION,
        method,
        params,
    })
}

pub fn request<T: Serialize>(id: &Id, method: &str, params: &T) -> Vec<u8> {
    line(&RequestOut {
        jsonrpc: VERSION,
        id,
        method,
        params,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn encodes_single_lines() {
        assert_eq!(response(&Id::Int(1), &json!({})), b"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n");
        assert_eq!(
            error_response(None, &RpcError::new(-32700, "bad")),
            b"{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"bad\"}}\n"
        );
        let note = notification("session/event", &json!({"text": "a\nb"}));
        assert_eq!(note.iter().filter(|b| **b == b'\n').count(), 1, "embedded newlines are escaped");
        assert_eq!(
            request(&Id::Str("p1".into()), "session/request_permission", &json!(null)),
            b"{\"jsonrpc\":\"2.0\",\"id\":\"p1\",\"method\":\"session/request_permission\",\"params\":null}\n"
        );
    }
}
