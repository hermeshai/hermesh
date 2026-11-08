//! The plaintext a legacy chat takes, built from the kernel's request. The turns come from the
//! kernel's own OpenAI conversion; this file only puts them in Qoder's envelope, as Qoder's
//! App sends it (its worker runtime's agent request).
//! Protocol reference: crates/her-connections/licenses/README.md.
use super::Target;
use her_llm::{
    Message, RequestOptions, ThinkingLevel, ToolChoice, WireRequest, api::openai_chat,
    model::REQUEST_MAX_OUTPUT_TOKENS,
};
use serde_json::{Map, Value, json};
use std::sync::Arc;

/// A conversation's ids, the same for every call it makes: Qoder keeps a session's prompt
/// cache and its queue place by them.
pub(crate) struct Ids {
    pub(crate) session: String,
    /// The task of the latest user turn: every call of its tool loop shares it.
    pub(crate) task: String,
}

pub(crate) fn ids(req: &WireRequest) -> Ids {
    let ctx = &req.context;
    let session = match req
        .options
        .cache_key
        .as_deref()
        .filter(|k| !k.trim().is_empty())
    {
        Some(key) => uuid_of(&["key", key]),
        None => {
            let first = ctx.messages.iter().find_map(|m| match m {
                Message::User(user) => Some(user.text_content()),
                _ => None,
            });
            uuid_of(&["chat", &ctx.system, first.as_deref().unwrap_or_default()])
        }
    };
    let (at, prompt) = last_user(req);
    let task = uuid_of(&["task", &session, &at.to_string(), &prompt]);
    Ids { session, task }
}

/// The body for one call: `request` is new on every call, the other ids are `ids`'.
pub(crate) fn build(req: &WireRequest, target: &Target, ids: &Ids, request: &str) -> Value {
    let ctx = &req.context;
    let (at, prompt) = last_user(req);
    let begin_at = ctx.messages.get(at).and_then(|m| match m {
        Message::User(user) => Some(user.ts),
        _ => None,
    });
    // Once a call of this turn was answered, the App moves its task from start to processing.
    let answered = ctx.messages[at.min(ctx.messages.len())..]
        .iter()
        .any(|m| matches!(m, Message::Assistant(_)));
    let thinking = req.model.resolve_thinking(req.options.thinking);
    let enable_thinking = match thinking {
        ThinkingLevel::Off => false,
        ThinkingLevel::Default => target.is_reasoning || !target.efforts.is_empty(),
        _ => true,
    };
    let mut parameters = Map::new();
    parameters.insert("enable_thinking".into(), json!(enable_thinking));
    parameters.insert(
        "max_tokens".into(),
        json!(
            req.options
                .max_output_tokens
                .or_else(|| req.model.default_max_output())
                .unwrap_or(REQUEST_MAX_OUTPUT_TOKENS)
        ),
    );
    if thinking.is_effort() {
        parameters.insert("reasoning_effort".into(), json!(thinking.as_str()));
    }
    if let Some(tier) = target.tier_for(u64::from(req.model.context_window)) {
        parameters.insert("context_length".into(), json!(tier));
    }
    let system = json!([{"type": "text", "text": ctx.system}]);
    let mut messages = vec![json!({"role": "system", "content": system})];
    messages.extend(turns(req));
    mark_cache(&mut messages);
    let mut body = json!({
        "request_id": request,
        "request_set_id": ids.task,
        "chat_record_id": request,
        "session_id": ids.session,
        "parameters": parameters,
        "business": {
            "product": "app",
            "version": super::super::cosy::VERSION,
            "type": "agent",
            "id": ids.task,
            "name": prompt.chars().take(10).collect::<String>(),
            "begin_at": begin_at.unwrap_or_else(her_llm::now_ms),
            "stage": if answered { "processing" } else { "start" },
        },
        "agent_id": "agent_common",
        "task_id": "common",
        "session_type": "app",
        "model_config": target.model_config(enable_thinking),
        "system": system,
        "messages": messages,
    });
    if req.options.tool_choice != ToolChoice::None && !ctx.tools.is_empty() {
        body["tools"] = ctx
            .tools
            .iter()
            .map(|t| json!({"type": "function", "function": {"name": t.name, "description": t.description, "parameters": t.parameters}}))
            .collect();
    }
    body
}

/// The index of the latest user message and its text; (messages' length, "") when none.
fn last_user(req: &WireRequest) -> (usize, String) {
    let messages = &req.context.messages;
    messages
        .iter()
        .rposition(|m| matches!(m, Message::User(_)))
        .map(|at| match &messages[at] {
            Message::User(user) => (at, user.text_content()),
            _ => unreachable!(),
        })
        .unwrap_or((messages.len(), String::new()))
}

/// The turns after the system prompt, from the kernel's OpenAI conversion: user turns as
/// blocks (also under `contents`, as the App sends them for its prompt cache), the rest as is.
fn turns(req: &WireRequest) -> Vec<Value> {
    // Only the turns are read from the conversion, so its request options do not matter;
    // thinking is replayed as the App replays it, with each earlier assistant turn.
    let mut model = (*req.model).clone();
    model.compat.replay_thinking = Some(true);
    model.compat.tool_result_images = Some(false);
    model.compat.system_role = None;
    let options = RequestOptions {
        thinking: ThinkingLevel::Default,
        ..Default::default()
    };
    let context = her_llm::Context {
        system: String::new(),
        messages: req.context.messages.clone(),
        tools: Vec::new(),
    };
    let converted: Value = serde_json::from_slice(&openai_chat::build_body(
        &Arc::new(model),
        &context,
        &options,
    ))
    .unwrap_or_default();
    let mut out = Vec::new();
    for mut message in converted["messages"]
        .as_array()
        .cloned()
        .unwrap_or_default()
    {
        match message["role"].as_str() {
            Some("system" | "developer") | None => continue,
            Some("user") => {
                let blocks = match message["content"].take() {
                    Value::String(text) => json!([{"type": "text", "text": text}]),
                    blocks => blocks,
                };
                message["content"] = blocks.clone();
                message["contents"] = blocks;
            }
            _ => {}
        }
        out.push(message);
    }
    out
}

/// Marks the last block of the last turn for the prompt cache, when that turn is the user's:
/// the App marks nothing when it is a tool's.
fn mark_cache(messages: &mut [Value]) {
    let Some(last) = messages.last_mut().filter(|m| m["role"] == "user") else {
        return;
    };
    let Some(blocks) = last["content"].as_array_mut() else {
        return;
    };
    let Some(block) = blocks
        .iter_mut()
        .rev()
        .find(|b| matches!(b["type"].as_str(), Some("text" | "image_url")))
    else {
        return;
    };
    block["cache_control"] = json!({"type": "ephemeral"});
    last["contents"] = last["content"].clone();
}

/// A UUID-shaped digest of `parts`, so that no id of the kernel's goes to Qoder as it is.
fn uuid_of(parts: &[&str]) -> String {
    let mut context = ring::digest::Context::new(&ring::digest::SHA256);
    for part in parts {
        context.update(part.as_bytes());
        context.update(&[0]);
    }
    let digest = context.finish();
    let h: String = digest.as_ref()[..16]
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    format!(
        "{}-{}-{}-{}-{}",
        &h[..8],
        &h[8..12],
        &h[12..16],
        &h[16..20],
        &h[20..32]
    )
}
