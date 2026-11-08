//! Application errors shared by both protocols (codes in `her_protocol::api::error_code`).

use her_core::KernelError;
use her_core::runtime::RuntimeError;
use her_core::store::StoreError;
use her_llm::ResolveError;
use her_protocol::ErrorInfo;
use her_protocol::api::error_code;
use her_rpc::RpcError;

pub fn session_not_found(id: &str) -> RpcError {
    RpcError::with_kind(error_code::SESSION_NOT_FOUND, "session_not_found", format!("session {id} not found"))
}

pub fn bot_not_found(id: &str) -> RpcError {
    RpcError::with_kind(error_code::BOT_NOT_FOUND, "bot_not_found", format!("bot {id} not found"))
}

pub fn bot_default(message: impl Into<String>) -> RpcError {
    RpcError::with_kind(error_code::BOT_DEFAULT, "bot_default", message)
}

pub fn bot_invalid(message: impl Into<String>) -> RpcError {
    RpcError::with_kind(error_code::BOT_INVALID, "bot_invalid", message)
}

pub fn routine_not_found(id: &str) -> RpcError {
    RpcError::with_kind(error_code::ROUTINE_NOT_FOUND, "routine_not_found", format!("routine or run {id} not found"))
}

pub fn routine_busy() -> RpcError {
    RpcError::with_kind(error_code::ROUTINE_BUSY, "routine_busy", "the routine is running now")
}

pub fn routines_unavailable(why: impl Into<String>) -> RpcError {
    RpcError::with_kind(error_code::ROUTINES_UNAVAILABLE, "routines_unavailable", why)
}

pub fn not_initialized() -> RpcError {
    RpcError::with_kind(error_code::NOT_INITIALIZED, "not_initialized", "send initialize first")
}

pub fn session_busy(message: impl Into<String>) -> RpcError {
    RpcError::with_kind(error_code::SESSION_BUSY, "session_busy", message)
}

pub fn session_not_loaded(id: &str) -> RpcError {
    RpcError::with_kind(
        error_code::SESSION_NOT_LOADED,
        "session_not_loaded",
        format!("session {id} is not loaded; resume it first"),
    )
}

pub fn not_attached(id: &str) -> RpcError {
    RpcError::with_kind(
        error_code::NOT_ATTACHED,
        "not_attached",
        format!("attach to session {id} before sending operations"),
    )
}

pub fn prompt_in_progress(id: &str) -> RpcError {
    RpcError::with_kind(
        error_code::PROMPT_IN_PROGRESS,
        "prompt_in_progress",
        format!("session {id} is still working on the previous prompt"),
    )
}

pub fn approval_not_pending(call_id: &str) -> RpcError {
    RpcError::with_kind(
        error_code::APPROVAL_NOT_PENDING,
        "approval_not_pending",
        format!("no approval is pending for call {call_id}"),
    )
}

pub fn server_full(max: usize) -> RpcError {
    RpcError::with_kind(
        error_code::SERVER_FULL,
        "server_full",
        format!("the server has {max} sessions loaded and every one is in use"),
    )
}

pub fn overloaded() -> RpcError {
    RpcError::with_kind(error_code::OVERLOADED, "overloaded", "the session is busy; retry shortly")
}

pub fn unsupported_content(message: impl Into<String>) -> RpcError {
    RpcError::with_kind(error_code::UNSUPPORTED_CONTENT, "unsupported_content", message)
}

pub fn session_closed(id: &str) -> RpcError {
    RpcError::with_kind(error_code::SESSION_CLOSED, "session_closed", format!("session {id} is closed"))
}

/// A failed task, with the full error info in `data`.
pub fn task_failed(error: &ErrorInfo) -> RpcError {
    let mut data = serde_json::to_value(error).unwrap_or_default();
    if let Some(map) = data.as_object_mut() {
        map.insert("error_kind".into(), map.get("kind").cloned().unwrap_or_default());
        map.insert("kind".into(), "task_failed".into());
    }
    RpcError {
        code: error_code::TASK_FAILED,
        message: error.message.clone(),
        data: Some(data),
    }
}

pub fn from_store(err: StoreError, id: &str) -> RpcError {
    match err {
        StoreError::NotFound(_) => session_not_found(id),
        StoreError::Locked(_) => session_busy(format!("session {id} is open in another her process")),
        StoreError::Ambiguous(message) => RpcError::invalid_params(message),
        other => RpcError::internal(other.to_string()),
    }
}

/// The model named or configured cannot run yet.
pub fn no_credentials(model: &str, provider: &str) -> RpcError {
    RpcError::with_kind(
        error_code::NO_MODEL,
        "no_credentials",
        format!("{model} has no credentials yet: run `her login {provider}` in a terminal, or sign in here"),
    )
}

/// Nothing to run a session on yet. Clients send the person to set a model up.
pub fn no_model() -> RpcError {
    RpcError::with_kind(
        error_code::NO_MODEL,
        "no_model",
        "no model is set up: connect a provider (auth/set, or its key in the environment), or name a model in session/new or in the configuration",
    )
}

pub fn from_kernel(err: KernelError, id: &str) -> RpcError {
    match err {
        KernelError::NoModel => no_model(),
        KernelError::Store(err) => from_store(err, id),
        KernelError::Model(err) => match &err {
            // A provider that is gone (a removed or disconnected connection): clients say so in
            // their own words and offer another model.
            ResolveError::UnknownProvider { provider, .. } => RpcError {
                code: error_code::MODEL_UNAVAILABLE,
                data: Some(serde_json::json!({ "kind": "model_unavailable", "provider": provider })),
                message: err.to_string(),
            },
            ResolveError::BadReference(_) => RpcError::invalid_params(err),
        },
        KernelError::Other(message) => RpcError::internal(message),
    }
}

pub fn from_runtime(err: RuntimeError) -> RpcError {
    match err {
        RuntimeError::Cwd { .. } | RuntimeError::Config(_) => RpcError::invalid_params(err),
        other => RpcError::internal(other.to_string()),
    }
}
