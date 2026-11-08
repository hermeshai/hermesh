use serde::Deserialize;
use serde::Deserializer as _;
use serde::Serialize;
use serde::de::IgnoredAny;
use serde::de::MapAccess;
use serde::de::Visitor;
use serde_json::Value;
use serde_json::value::RawValue;
use std::borrow::Cow;
use std::fmt;

/// Standard JSON-RPC error codes, plus the request-cancelled code that ACP and LSP use.
pub mod code {
    pub const PARSE_ERROR: i64 = -32700;
    pub const INVALID_REQUEST: i64 = -32600;
    pub const METHOD_NOT_FOUND: i64 = -32601;
    pub const INVALID_PARAMS: i64 = -32602;
    pub const INTERNAL_ERROR: i64 = -32603;
    pub const REQUEST_CANCELLED: i64 = -32800;
}

/// A request id: an integer or a string, echoed back unchanged.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(untagged)]
pub enum Id {
    Int(i64),
    Str(String),
}

impl fmt::Display for Id {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Id::Int(n) => write!(f, "{n}"),
            Id::Str(s) => f.write_str(s),
        }
    }
}

impl From<i64> for Id {
    fn from(n: i64) -> Self {
        Id::Int(n)
    }
}

/// The JSON-RPC error object. Application errors put a `kind` string in `data`.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, thiserror::Error)]
#[error("{message} ({code})")]
pub struct RpcError {
    pub code: i64,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl RpcError {
    pub fn new(code: i64, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            data: None,
        }
    }

    /// An error whose `data` is `{"kind": kind}`.
    pub fn with_kind(code: i64, kind: &str, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            data: Some(serde_json::json!({ "kind": kind })),
        }
    }

    pub fn parse_error(detail: impl fmt::Display) -> Self {
        Self::with_kind(code::PARSE_ERROR, "parse_error", format!("invalid JSON: {detail}"))
    }

    pub fn invalid_request(message: impl Into<String>) -> Self {
        Self::with_kind(code::INVALID_REQUEST, "invalid_request", message)
    }

    pub fn method_not_found(method: &str) -> Self {
        Self::with_kind(code::METHOD_NOT_FOUND, "method_not_found", format!("unknown method '{method}'"))
    }

    pub fn invalid_params(detail: impl fmt::Display) -> Self {
        Self::with_kind(code::INVALID_PARAMS, "invalid_params", format!("invalid params: {detail}"))
    }

    pub fn internal(message: impl Into<String>) -> Self {
        Self::with_kind(code::INTERNAL_ERROR, "internal_error", message)
    }

    pub fn cancelled() -> Self {
        Self::with_kind(code::REQUEST_CANCELLED, "request_cancelled", "the request was cancelled")
    }

    /// The `kind` in `data`, if any.
    pub fn kind(&self) -> Option<&str> {
        self.data.as_ref()?.get("kind")?.as_str()
    }
}

/// One incoming line, parsed without copying the parameters or the result.
#[derive(Debug, Deserialize)]
pub struct RawMessage<'a> {
    #[serde(default, borrow)]
    pub jsonrpc: Option<Cow<'a, str>>,
    #[serde(default)]
    pub id: Option<Id>,
    #[serde(default, borrow)]
    pub method: Option<Cow<'a, str>>,
    #[serde(default, borrow)]
    pub params: Option<&'a RawValue>,
    /// Present even when the result is `null`.
    #[serde(default, borrow, deserialize_with = "present")]
    pub result: Option<&'a RawValue>,
    #[serde(default)]
    pub error: Option<RpcError>,
}

fn present<'de, D: serde::Deserializer<'de>>(deserializer: D) -> Result<Option<&'de RawValue>, D::Error> {
    <&'de RawValue>::deserialize(deserializer).map(Some)
}

/// What a [`RawMessage`] is.
pub enum Kind<'m, 'a> {
    Request {
        id: &'m Id,
        method: &'m str,
        params: Option<&'a RawValue>,
    },
    Notification {
        method: &'m str,
        params: Option<&'a RawValue>,
    },
    Response {
        id: &'m Id,
        result: Result<&'a RawValue, &'m RpcError>,
    },
    Invalid,
}

impl<'a> RawMessage<'a> {
    pub fn parse(line: &'a [u8]) -> Result<Self, RpcError> {
        let message: RawMessage<'a> = serde_json::from_slice(line).map_err(RpcError::parse_error)?;
        Ok(message)
    }

    pub fn kind(&self) -> Kind<'_, 'a> {
        match (&self.id, self.method.as_deref()) {
            (Some(id), Some(method)) => Kind::Request {
                id,
                method,
                params: self.params,
            },
            (None, Some(method)) => Kind::Notification {
                method,
                params: self.params,
            },
            (Some(id), None) => match (&self.error, self.result) {
                (Some(error), _) => Kind::Response { id, result: Err(error) },
                (None, Some(result)) => Kind::Response { id, result: Ok(result) },
                (None, None) => Kind::Invalid,
            },
            (None, None) => Kind::Invalid,
        }
    }
}

/// The `id` of a request of which only the start is known, one too long to read. Found when `id`
/// and `method` come before what made it long, as they do in what JSON-RPC libraries write. Not
/// for a response: its id is the other side's, and the answer must not look like one to its own
/// request of the same number.
pub fn peek_request_id(head: &[u8]) -> Option<Id> {
    // The parser reports an error for an object left unfinished, so the answer goes out on the side.
    struct Fields<'a>(&'a mut Option<Id>);
    impl<'de> Visitor<'de> for Fields<'_> {
        type Value = ();

        fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
            f.write_str("a JSON object")
        }

        fn visit_map<A: MapAccess<'de>>(self, mut fields: A) -> Result<(), A::Error> {
            let mut id = None;
            let mut method = false;
            while let Some(key) = fields.next_key::<String>()? {
                match key.as_str() {
                    "id" => id = fields.next_value::<Option<Id>>()?,
                    other => {
                        fields.next_value::<IgnoredAny>()?;
                        method |= other == "method";
                    }
                }
                if method && id.is_some() {
                    *self.0 = id;
                    break;
                }
            }
            Ok(())
        }
    }
    let mut found = None;
    let _ = serde_json::Deserializer::from_slice(head).deserialize_map(Fields(&mut found));
    found
}

/// Deserializes request parameters; absent parameters read as `null`.
pub fn params<'a, T: Deserialize<'a>>(params: Option<&'a RawValue>) -> Result<T, RpcError> {
    let raw = params.map(RawValue::get).unwrap_or("null");
    serde_json::from_str(raw).map_err(RpcError::invalid_params)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classifies_messages() {
        let req = RawMessage::parse(br#"{"jsonrpc":"2.0","id":7,"method":"session/op","params":{"a":1}}"#).unwrap();
        match req.kind() {
            Kind::Request { id, method, params } => {
                assert_eq!(id, &Id::Int(7));
                assert_eq!(method, "session/op");
                assert_eq!(params.unwrap().get(), r#"{"a":1}"#);
            }
            _ => panic!("not a request"),
        }
        let note = RawMessage::parse(br#"{"jsonrpc":"2.0","method":"$/cancel_request","params":{"id":"x"}}"#).unwrap();
        assert!(matches!(note.kind(), Kind::Notification { method: "$/cancel_request", .. }));
        let resp = RawMessage::parse(br#"{"jsonrpc":"2.0","id":"r1","result":null}"#).unwrap();
        assert!(matches!(resp.kind(), Kind::Response { result: Ok(r), .. } if r.get() == "null"));
        let resp = RawMessage::parse(br#"{"jsonrpc":"2.0","id":"r1","result":{}}"#).unwrap();
        assert!(matches!(resp.kind(), Kind::Response { result: Ok(_), .. }));
        let err = RawMessage::parse(br#"{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"no"}}"#).unwrap();
        assert!(matches!(err.kind(), Kind::Response { result: Err(e), .. } if e.code == -32601));
        assert_eq!(RawMessage::parse(b"{nope").unwrap_err().code, code::PARSE_ERROR);
    }

    #[test]
    fn finds_the_id_of_a_request_from_its_start() {
        let int = Some(Id::Int(7));
        for head in [
            &br#"{"jsonrpc":"2.0","id":7,"method":"session/op","params":{"text":"cut he"#[..],
            br#"{"jsonrpc":"2.0","method":"session/op","id":7,"params":{"text":"cut he"#,
            br#"{"id":7,"method":"session/op","#,
            br#" { "id" : 7 , "method" : "x" }"#,
        ] {
            assert_eq!(peek_request_id(head), int, "{}", String::from_utf8_lossy(head));
        }
        assert_eq!(peek_request_id(br#"{"id":"a-b","method":"x","params":"#), Some(Id::Str("a-b".into())));
        for head in [
            // Nothing to tell: not JSON, or the id is not reached.
            &b"xxxxxxxxxxxx"[..],
            b"",
            b"[1,2,3]",
            br#"{"jsonrpc":"2.0","method":"session/op","params":{"text":"cut he"#,
            br#"{"jsonrpc":"2.0","id":7,"params":{"text":"cut he"#,
            br#"{"method":"x","id":"cut he"#,
            // A notification has no id, and a response's is not ours to answer.
            br#"{"jsonrpc":"2.0","method":"browser/cdp"}"#,
            br#"{"jsonrpc":"2.0","method":"browser/cdp","params":{"data":"cut he"#,
            br#"{"jsonrpc":"2.0","id":7,"result":{"text":"cut he"#,
            br#"{"jsonrpc":"2.0","id":7,"result":{}}"#,
            // Not an id.
            br#"{"id":null,"method":"x","params":"#,
            br#"{"id":1.5,"method":"x","params":"#,
            br#"{"id":{"a":1},"method":"x","params":"#,
        ] {
            assert_eq!(peek_request_id(head), None, "{}", String::from_utf8_lossy(head));
        }
    }

    #[test]
    fn errors_carry_their_kind() {
        let err = RpcError::method_not_found("x/y");
        assert_eq!(err.kind(), Some("method_not_found"));
        assert_eq!(err.code, code::METHOD_NOT_FOUND);
    }
}
