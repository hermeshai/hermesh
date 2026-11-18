//! Services added by their address (docs/kernel/custom-endpoint-design.md §13.2), each on its own
//! port as a real one is on its own host: which protocols it has, under which paths, which header
//! the key must come in, and the shape of its model list. The replies come from the mock's chat
//! and Messages handlers, which record the path of each request and the header its key came in.

use serde_json::Value;
use serde_json::json;

use crate::MODEL_ID;
use crate::Request;

/// The key a gateway that checks keys takes.
pub const GATEWAY_KEY: &str = "sk-gateway-test";

/// The kinds of service the end-to-end tests add by their address.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Gateway {
    /// Chat Completions only, under `/v1` (One API, Groq): Claude models go there too.
    ChatOnly,
    /// Anthropic Messages only, with Anthropic's model list and the key in `x-api-key`.
    AnthropicOnly,
    /// Azure's shape: the OpenAI routes under `/openai/v1`, the key in `api-key`.
    Azure,
    /// Chat Completions under `/v1` and Messages under `/anthropic`, both taking the key as a
    /// bearer token only (the Messages of vLLM, SGLang and Ollama Cloud).
    AnthropicPath,
    /// New API: both protocols, its list saying which ones each model takes.
    NewApi,
    /// The key before any path (DeepSeek's own API): without it every path is 401, in plain text.
    KeyFirst,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Auth {
    /// Takes a key in any header, or none.
    Any,
    Bearer,
    XApiKey,
    ApiKey,
}

struct Shape {
    /// The prefix of the OpenAI routes and the header their key comes in.
    openai: Option<(&'static str, Auth)>,
    /// The prefix the Messages route hangs off (it adds `/v1/messages`) and its key's header.
    messages: Option<(&'static str, Auth)>,
    /// The list is Anthropic's (`GET {messages prefix}/v1/models`), not OpenAI's.
    anthropic_list: bool,
    key_first: bool,
    /// The models, and for New API the protocols each takes.
    models: &'static [(&'static str, &'static [&'static str])],
}

fn shape(kind: Gateway) -> Shape {
    let plain = Shape { openai: None, messages: None, anthropic_list: false, key_first: false, models: &[] };
    match kind {
        Gateway::ChatOnly => Shape { openai: Some(("/v1", Auth::Any)), models: &[("claude-sonnet-4-5", &[]), (MODEL_ID, &[])], ..plain },
        Gateway::AnthropicOnly => Shape {
            messages: Some(("", Auth::XApiKey)),
            anthropic_list: true,
            models: &[("claude-sonnet-4-5", &[]), ("house-model-7", &[])],
            ..plain
        },
        Gateway::Azure => Shape { openai: Some(("/openai/v1", Auth::ApiKey)), models: &[(MODEL_ID, &[]), ("gpt-4o", &[])], ..plain },
        Gateway::AnthropicPath => Shape {
            openai: Some(("/v1", Auth::Bearer)),
            messages: Some(("/anthropic", Auth::Bearer)),
            models: &[("claude-sonnet-4-5", &[]), (MODEL_ID, &[])],
            ..plain
        },
        Gateway::NewApi => Shape {
            openai: Some(("/v1", Auth::Any)),
            messages: Some(("", Auth::Any)),
            models: &[("claude-sonnet-4-5", &["openai"]), ("claude-opus-4-1", &["anthropic", "openai"]), (MODEL_ID, &["openai"])],
            ..plain
        },
        Gateway::KeyFirst => Shape { openai: Some(("/v1", Auth::Bearer)), key_first: true, models: &[(MODEL_ID, &[])], ..plain },
    }
}

pub(crate) enum Routed {
    Json(u16, Value),
    Text(u16, &'static str),
    Chat,
    Messages,
}

/// How a gateway of `kind` answers `request`.
pub(crate) fn route(kind: Gateway, request: &Request) -> Routed {
    let shape = shape(kind);
    let bearer = request.authorization.as_deref().and_then(|a| a.strip_prefix("Bearer "));
    let keys = [bearer, request.x_api_key.as_deref(), request.api_key.as_deref()];
    if shape.key_first && !keys.contains(&Some(GATEWAY_KEY)) {
        return Routed::Text(401, "Authentication Fails (governor)");
    }
    let takes = |auth: Auth| match auth {
        Auth::Any => true,
        Auth::Bearer => bearer == Some(GATEWAY_KEY),
        Auth::XApiKey => request.x_api_key.as_deref() == Some(GATEWAY_KEY),
        Auth::ApiKey => request.api_key.as_deref() == Some(GATEWAY_KEY),
    };
    let path = request.path.split('?').next().unwrap_or_default();
    let method = request.method.as_str();
    if let Some((prefix, auth)) = shape.openai
        && let Some(tail) = path.strip_prefix(prefix)
    {
        let refused = || Routed::Json(401, json!({"error": {"message": "Incorrect API key provided", "type": "invalid_request_error", "code": "invalid_api_key"}}));
        match (method, tail) {
            ("GET", "/models") => return if takes(auth) { Routed::Json(200, openai_list(&shape)) } else { refused() },
            ("POST", "/chat/completions") => return if takes(auth) { Routed::Chat } else { refused() },
            _ => {}
        }
    }
    if let Some((prefix, auth)) = shape.messages
        && let Some(tail) = path.strip_prefix(prefix)
    {
        let refused = || Routed::Json(401, json!({"type": "error", "error": {"type": "authentication_error", "message": "invalid x-api-key"}}));
        match (method, tail) {
            ("POST", "/v1/messages") => return if takes(auth) { Routed::Messages } else { refused() },
            ("GET", "/v1/models") if shape.anthropic_list => return if takes(auth) { Routed::Json(200, anthropic_list(&shape)) } else { refused() },
            _ => {}
        }
    }
    Routed::Json(404, json!({"error": {"message": "not found"}}))
}

fn openai_list(shape: &Shape) -> Value {
    let data: Vec<Value> = shape
        .models
        .iter()
        .map(|(id, types)| match types {
            [] => json!({"id": id, "object": "model"}),
            types => json!({"id": id, "object": "model", "owned_by": "custom", "supported_endpoint_types": types}),
        })
        .collect();
    json!({"object": "list", "data": data})
}

fn anthropic_list(shape: &Shape) -> Value {
    let data: Vec<Value> = shape.models.iter().map(|(id, _)| json!({"id": id, "type": "model", "display_name": id})).collect();
    let (first, last) = (shape.models.first().map(|m| m.0), shape.models.last().map(|m| m.0));
    json!({"data": data, "has_more": false, "first_id": first, "last_id": last})
}
